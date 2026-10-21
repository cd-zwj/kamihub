package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/cache"
	"kamihub/internal/models"
	"kamihub/internal/notify"
)

// 规则编码。和 models.RiskRule.Code 一一对应，也是 risk_hits 里的 rule_code。
const (
	RuleIPRegisterVelocity = "ip_register_velocity" // R1 同 IP 注册频率
	RuleIPOrderVelocity    = "ip_order_velocity"    // R2 同 IP 下单频率
	RuleBuyerOrderVelocity = "buyer_order_velocity" // R3 同买家下单频率
	RuleOrderAmount        = "order_amount_anomaly" // R4 单笔金额异常
	RuleDailyAmount        = "daily_amount_anomaly" // R5 日累计金额异常
	RuleRefundRate         = "refund_rate_anomaly"  // R6 退款率异常
	RuleBlacklist          = "blacklist_match"      // R7 黑名单
	RuleSelfPurchase       = "self_purchase"        // R8 自购/互买（等分销上线后才有数据）
)

// 建议动作
const (
	ActionLog          = "log"
	ActionCaptcha      = "captcha"
	ActionManualReview = "manual_review"
	ActionReject       = "reject"
)

// EvalInput 一次风控评估的现场。
type EvalInput struct {
	Scene   string
	IP      string
	BuyerID uint
	Email   string
	// Amount 本笔金额（下单场景）
	Amount float64
	// OrderNo 本笔订单号，仅用于写命中记录的 subject
	OrderNo string
}

// Decision 评估结论。
type Decision struct {
	// Level 汇总后的最高级别：pass / review / reject
	Level string
	// Action 建议动作
	Action string
	// Hits 命中的规则（已落库或待落库）
	Hits []models.RiskHit
}

// Allowed 是否放行
func (d Decision) Allowed() bool { return d.Level != models.LevelReject }

// Subject 本次评估的主体标识，写 risk_hits 用
func (in EvalInput) Subject() string {
	if in.BuyerID > 0 {
		return fmt.Sprintf("buyer:%d", in.BuyerID)
	}
	if in.IP != "" {
		return "ip:" + in.IP
	}
	return "unknown"
}

// RiskService 风控规则引擎。
//
// 设计取舍：**不做 DSL**。kamihub 的量级用「规则表 + 顺序评估」足够，
// 上 Drools 那类引擎只会让「改一条阈值」变成一次发版 + 一次回归。
// 这里 Config 是纯参数，判断逻辑固定在各规则的 Go 实现里 ——
// 能热改的正是最需要热改的东西（阈值、开关、级别）。
type RiskService struct {
	db    *gorm.DB
	cache *cache.Client
	// notifier 风控命中告警。为 nil 时只记录不告警（不影响拦截逻辑）。
	// 用 setter 注入而不是构造参数：风控的核心职责是「判不判」，
	// 告警是附加能力，缺了它风控照样能工作。
	notifier *notify.Notifier

	mu sync.RWMutex
	// rules 按场景缓存的启用规则，避免每次下单都查一遍表
	rules map[string][]models.RiskRule
	// loadedAt 缓存时间。即使没收到失效通知，10 分钟后也会自然刷新，
	// 保证多进程部署（api + worker）之间最终一致。
	loadedAt time.Time

	// avgAmount 客单价基线缓存（R4 用）
	avgAmount   float64
	avgCachedAt time.Time
}

// riskCacheTTL 规则缓存的自然过期时间
const riskCacheTTL = 10 * time.Second

func NewRiskService(db *gorm.DB, c *cache.Client) *RiskService {
	return &RiskService{db: db, cache: c}
}

// WithNotifier 注入告警渠道（迭代 2 遗留项：命中接通知）。
//
// 只对 reject 级命中告警，且靠 notify 的事件聚合窗口去重 ——
// 风控命中在高峰期可能每分钟几十条，不聚合会直接把 Telegram 刷爆，
// 然后运维把告警关掉，等于白做。review 级连告警都不发：
// 「有人被标记复核」不值得打扰，但「有人被真的拦下来」必须知道 ——
// 那可能是黑产在试，也可能是我们自己规则配错在拦正常客户。
func (s *RiskService) WithNotifier(n *notify.Notifier) *RiskService {
	s.notifier = n
	return s
}

// Invalidate 规则被改动后清缓存，让下一次评估立刻用新配置（热改不重启）。
func (s *RiskService) Invalidate() {
	s.mu.Lock()
	s.rules = nil
	s.loadedAt = time.Time{}
	s.mu.Unlock()
}

// rulesFor 取某场景的启用规则（按 priority 升序）。
//
// 每个场景的规则 = 「本场景专属规则」+「通配规则（scene=all）」再按 priority 排序。
// 通配规则是为黑名单准备的：被拉黑的 IP/邮箱不该只在某一种操作里被拦，
// 否则换个入口就绕过去了。
func (s *RiskService) rulesFor(ctx context.Context, scene string) ([]models.RiskRule, error) {
	s.mu.RLock()
	if s.rules != nil && time.Since(s.loadedAt) < riskCacheTTL {
		sceneRules, hasScene := s.rules[scene]
		allRules, hasAll := s.rules[models.SceneAll]
		if hasScene || hasAll {
			out := mergeSceneRules(append([]models.RiskRule(nil), sceneRules...),
				append([]models.RiskRule(nil), allRules...))
			s.mu.RUnlock()
			return out, nil
		}
	}
	s.mu.RUnlock()

	var rows []models.RiskRule
	if err := s.db.WithContext(ctx).
		Where("enabled = ?", true).
		Order("priority ASC, id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	grouped := map[string][]models.RiskRule{}
	for _, r := range rows {
		grouped[r.Scene] = append(grouped[r.Scene], r)
	}

	s.mu.Lock()
	s.rules = grouped
	s.loadedAt = time.Now()
	s.mu.Unlock()

	return mergeSceneRules(grouped[scene], grouped[models.SceneAll]), nil
}

// mergeSceneRules 把场景规则与通配规则合并，保持 priority 升序。
//
// 为什么排序而不是简单 append：黑名单这类通配规则应当排在前面被评估。
// 拼到末尾会让它在别的规则已经加过计数之后才跑，
// 排查「命中记录的先后顺序为什么怪怪的」时会很困惑。
func mergeSceneRules(sceneRules, allRules []models.RiskRule) []models.RiskRule {
	if len(allRules) == 0 {
		return sceneRules
	}
	out := make([]models.RiskRule, 0, len(sceneRules)+len(allRules))
	out = append(out, sceneRules...)
	out = append(out, allRules...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Evaluate 按场景评估，返回汇总结论，并把命中写入 risk_hits。
//
// 「无论结果都记」是刻意的：risk_hits 是复盘和申诉的唯一依据。
// 只记拒绝的话，运营永远回答不了「这个买家到底被哪些规则擦边过」。
func (s *RiskService) Evaluate(ctx context.Context, in EvalInput) (Decision, error) {
	d := Decision{Level: models.LevelPass, Action: ActionLog}

	rules, err := s.rulesFor(ctx, in.Scene)
	if err != nil {
		// 规则读不出来时放行（降级开），并留痕。
		// 风控挂掉不该让全站下不了单 —— 这是可用性优先的取舍，写进报告。
		slog.Error("读取风控规则失败，本次放行", "scene", in.Scene, "error", err.Error())
		return d, nil
	}

	subject := in.Subject()
	for _, r := range rules {
		hit, detail := s.matches(ctx, r, in)
		if !hit {
			continue
		}
		rec := models.RiskHit{
			RuleCode: r.Code,
			Scene:    in.Scene,
			Subject:  subject,
			Level:    r.Level,
			Action:   actionOf(r.Level),
			Detail:   detail,
		}
		d.Hits = append(d.Hits, rec)

		if models.LevelRank(r.Level) > models.LevelRank(d.Level) {
			d.Level = r.Level
			d.Action = actionOf(r.Level)
		}
		slog.Warn("风控命中", "rule", r.Code, "scene", in.Scene, "subject", subject, "level", r.Level)
	}

	if len(d.Hits) > 0 {
		if err := s.db.WithContext(ctx).Create(&d.Hits).Error; err != nil {
			// 命中记录写不进去不能阻断业务：这个表是复盘用的，不是拦截依据
			slog.Error("写入风控命中记录失败", "error", err.Error())
		}
	}

	// 只有 reject 才告警（review 只落库）。
	// 告警本身走 notify 的事件聚合窗口，所以同一规则 5 分钟内只推一次。
	if d.Level == models.LevelReject {
		s.alertRejected(ctx, in, d)
	}

	return d, nil
}

// alertRejected 把一次 reject 命中推给运维。
//
// 为什么值得告警：风控「默默拦了一堆人」是最危险的状态 ——
// 要么是黑产在试（该知道），要么是我们规则配错在拦正常客户（更该知道）。
// 两种都必须有人看见。
func (s *RiskService) alertRejected(ctx context.Context, in EvalInput, d Decision) {
	if s.notifier == nil {
		return
	}
	codes := make([]string, 0, len(d.Hits))
	for _, h := range d.Hits {
		if h.Level == models.LevelReject {
			codes = append(codes, h.RuleCode)
		}
	}
	if len(codes) == 0 {
		codes = append(codes, "unknown")
	}

	fields := map[string]any{
		"scene":   in.Scene,
		"subject": in.Subject(),
		"ip":      in.IP,
		"rules":   strings.Join(codes, ","),
	}
	if in.Amount > 0 {
		fields["amount"] = round2(in.Amount)
	}
	if in.BuyerID > 0 {
		fields["buyer_id"] = in.BuyerID
	}

	s.notifier.Alert(ctx, notify.Message{
		Level: notify.LevelWarn,
		Event: notify.EventRiskRejected,
		Title: fmt.Sprintf("风控拦截了一次%s", sceneText(in.Scene)),
		Content: fmt.Sprintf("主体 %s（IP %s）触发规则 %s",
			in.Subject(), in.IP, strings.Join(codes, "、")),
		// 跳转到「风险命中」页，方便立刻复核
		Link:   "/admin/security",
		Fields: fields,
	})
}

// sceneText 场景的中文名，用于告警文案
func sceneText(scene string) string {
	switch scene {
	case models.SceneRegister:
		return "注册"
	case models.SceneLogin:
		return "登录"
	case models.SceneOrder:
		return "下单"
	case models.SceneWithdraw:
		return "提现"
	case models.SceneCommission:
		return "分销佣金"
	default:
		return scene
	}
}

// actionOf 把判定级别映射成建议动作
func actionOf(level string) string {
	switch level {
	case models.LevelReject:
		return ActionReject
	case models.LevelReview:
		return ActionManualReview
	default:
		return ActionLog
	}
}

// ruleConfig 规则参数的宽松容器。字段缺失即为零值，各规则自己给默认值 ——
// 这样运营只填想改的那个键就行，不用把整份参数抄一遍。
type ruleConfig struct {
	// 频率类
	WindowMinutes int `json:"window_minutes"`
	Max           int `json:"max"`
	// 金额类
	MaxAmount float64 `json:"max_amount"`
	Multiple  float64 `json:"multiple"`
	// 退款率类
	Ratio    float64 `json:"ratio"`
	MinCount int     `json:"min_count"`
	// 黑名单
	Kinds []string `json:"kinds"`
}

func parseConfig(raw []byte) ruleConfig {
	var c ruleConfig
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &c)
	}
	return c
}

// matches 判断单条规则是否命中，同时返回现场快照（写进 detail 便于复盘）。
func (s *RiskService) matches(ctx context.Context, r models.RiskRule, in EvalInput) (bool, []byte) {
	cfg := parseConfig(r.Config)

	switch r.Code {
	case RuleIPRegisterVelocity:
		window := minutesOr(cfg.WindowMinutes, 60)
		max := intOr(cfg.Max, 3)
		n := s.bump(ctx, "risk:register:ip:"+in.IP, window)
		return s.velocityHit(n, max, in.IP, window)

	case RuleIPOrderVelocity:
		window := minutesOr(cfg.WindowMinutes, 10)
		max := intOr(cfg.Max, 10)
		n := s.bump(ctx, "risk:order:ip:"+in.IP, window)
		return s.velocityHit(n, max, in.IP, window)

	case RuleBuyerOrderVelocity:
		window := minutesOr(cfg.WindowMinutes, 60)
		max := intOr(cfg.Max, 20)
		key := fmt.Sprintf("risk:order:buyer:%d", in.BuyerID)
		n := s.bump(ctx, key, window)
		return s.velocityHit(n, max, in.Subject(), window)

	case RuleOrderAmount:
		baseline := s.averageOrderAmount(ctx)
		multiple := cfg.Multiple
		if multiple <= 0 {
			multiple = 5
		}
		limit := baseline * multiple
		// 没有基线数据（新站）时退回绝对阈值，避免「乘 0 永远不命中」
		if limit <= 0 {
			limit = cfg.MaxAmount
		}
		if limit <= 0 || in.Amount <= 0 {
			return false, nil
		}
		if in.Amount > limit {
			return true, riskJSON(map[string]any{
				"amount": in.Amount, "limit": round2(limit),
				"baseline": round2(baseline), "multiple": multiple,
			})
		}
		return false, nil

	case RuleDailyAmount:
		max := cfg.MaxAmount
		if max <= 0 {
			max = 2000
		}
		sum := s.todaySpent(ctx, in.BuyerID)
		if sum+in.Amount > max {
			return true, riskJSON(map[string]any{
				"today_before": round2(sum), "amount": in.Amount, "max": max,
			})
		}
		return false, nil

	case RuleRefundRate:
		ratio := cfg.Ratio
		if ratio <= 0 {
			ratio = 0.3
		}
		minCount := intOr(cfg.MinCount, 3)
		total, refunded := s.refundStats(ctx, in.BuyerID)
		if total < int64(minCount) {
			return false, nil
		}
		rate := float64(refunded) / float64(total)
		if rate > ratio {
			return true, riskJSON(map[string]any{
				"orders": total, "refunded": refunded,
				"rate": round2(rate * 100), "limit_percent": round2(ratio * 100),
			})
		}
		return false, nil

	case RuleBlacklist:
		hitKind, hitValue := s.blacklistHit(ctx, in, cfg.Kinds)
		if hitKind == "" {
			return false, nil
		}
		return true, riskJSON(map[string]any{"kind": hitKind, "value": hitValue})

	case RuleSelfPurchase:
		// 分销尚未上线，没有「上级/下级」关系可判。
		// 保留规则位（后台可看到它存在但暂不生效），等 03 的阶梯专价落地后接上。
		return false, nil
	}

	return false, nil
}

// velocityHit 频率类规则的统一判定 + 快照
func (s *RiskService) velocityHit(n int64, max int, subject string, windowMin int) (bool, []byte) {
	if n <= int64(max) {
		return false, nil
	}
	return true, riskJSON(map[string]any{
		"window_minutes": windowMin, "max": max, "actual": n, "subject": subject,
	})
}

// bump 在窗口内自增计数并返回当前值。
//
// Redis 不可用时返回 0（即不命中）—— 频率类规则依赖计数器，
// 拿不到计数就当作「没有异常」，不让风控把业务卡死。
func (s *RiskService) bump(ctx context.Context, key string, windowMin int) int64 {
	if s.cache == nil {
		return 0
	}
	window := time.Duration(windowMin) * time.Minute
	n, err := s.cache.Raw().Incr(ctx, key).Result()
	if err != nil {
		slog.Warn("风控计数失败，按未命中处理", "key", key, "error", err.Error())
		return 0
	}
	if n == 1 {
		s.cache.Raw().Expire(ctx, key, window)
	}
	return n
}

// averageOrderAmount 近 30 天平均客单价，缓存 10 分钟避免每单都扫库。
func (s *RiskService) averageOrderAmount(ctx context.Context) float64 {
	s.mu.RLock()
	if s.avgCachedAt.After(time.Now().Add(-10*time.Minute)) && s.avgAmount > 0 {
		v := s.avgAmount
		s.mu.RUnlock()
		return v
	}
	s.mu.RUnlock()

	var avg *float64
	if err := s.db.WithContext(ctx).Model(&models.Order{}).
		Where("paid_amount IS NOT NULL AND paid_amount > 0").
		Where("created_at > ?", time.Now().AddDate(0, 0, -30)).
		Select("AVG(paid_amount)").Scan(&avg).Error; err != nil {
		return 0
	}
	if avg == nil {
		return 0
	}
	s.mu.Lock()
	s.avgAmount = *avg
	s.avgCachedAt = time.Now()
	s.mu.Unlock()
	return *avg
}

// todaySpent 买家今日已消费金额（含冻结中的）
func (s *RiskService) todaySpent(ctx context.Context, buyerID uint) float64 {
	if buyerID == 0 {
		return 0
	}
	start := time.Now().Truncate(24 * time.Hour)
	var sum *float64
	if err := s.db.WithContext(ctx).Model(&models.Order{}).
		Where("buyer_id = ? AND created_at >= ?", buyerID, start).
		Where("status <> ?", models.OrderStatusFailed).
		Select("SUM(paid_amount)").Scan(&sum).Error; err != nil {
		return 0
	}
	if sum == nil {
		return 0
	}
	return *sum
}

// refundStats 近 30 天订单数与退款数
func (s *RiskService) refundStats(ctx context.Context, buyerID uint) (total, refunded int64) {
	if buyerID == 0 {
		return 0, 0
	}
	since := time.Now().AddDate(0, 0, -30)
	_ = s.db.WithContext(ctx).Model(&models.Order{}).
		Where("buyer_id = ? AND created_at > ?", buyerID, since).
		Count(&total).Error
	_ = s.db.WithContext(ctx).Model(&models.Order{}).
		Where("buyer_id = ? AND created_at > ? AND status = ?", buyerID, since, models.OrderStatusRefunded).
		Count(&refunded).Error
	return total, refunded
}

// blacklistHit 黑名单匹配。命中即拒绝。
//
// 只比对本场景相关的 kind，避免「把一个 IP 拉黑导致同名邮箱也被拒」这种误伤。
func (s *RiskService) blacklistHit(ctx context.Context, in EvalInput, kinds []string) (kind, value string) {
	if len(kinds) == 0 {
		kinds = []string{models.BlacklistIP, models.BlacklistBuyer, models.BlacklistEmail}
	}

	candidates := map[string]string{}
	for _, k := range kinds {
		switch k {
		case models.BlacklistIP:
			if in.IP != "" {
				candidates[k] = in.IP
			}
		case models.BlacklistBuyer:
			if in.BuyerID > 0 {
				candidates[k] = fmt.Sprintf("%d", in.BuyerID)
			}
		case models.BlacklistEmail:
			if in.Email != "" {
				candidates[k] = in.Email
			}
		}
	}
	if len(candidates) == 0 {
		return "", ""
	}

	now := time.Now()
	for k, v := range candidates {
		var row models.Blacklist
		err := s.db.WithContext(ctx).
			Where("kind = ? AND value = ?", k, v).
			Where("expires_at IS NULL OR expires_at > ?", now).
			First(&row).Error
		if err == nil {
			return k, v
		}
	}
	return "", ""
}

func minutesOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func intOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

func riskJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// DefaultRules 8 条基础规则的出厂配置。
//
// 阈值取自 `04-安全风控与通知.md` §2.3 的建议初值。
// 用 OnConflict DoNothing 播种：已经存在的规则**不会被覆盖**，
// 因为运营可能已经把阈值调成适合自己业务的数字。
func DefaultRules() []models.RiskRule {
	return []models.RiskRule{
		{
			Code: RuleIPRegisterVelocity, Name: "同 IP 注册频率", Scene: models.SceneRegister,
			Level: models.LevelReview, Priority: 10, Enabled: true,
			Config:      riskJSON(ruleConfig{WindowMinutes: 60, Max: 3}),
			Description: "1 小时内同一 IP 注册超过 3 个账号即标记人工复核（共享 IP 场景建议放宽或关闭）",
		},
		{
			Code: RuleIPOrderVelocity, Name: "同 IP 下单频率", Scene: models.SceneOrder,
			Level: models.LevelReview, Priority: 20, Enabled: true,
			Config:      riskJSON(ruleConfig{WindowMinutes: 10, Max: 10}),
			Description: "10 分钟内同一 IP 下单超过 10 笔即标记复核",
		},
		{
			Code: RuleBuyerOrderVelocity, Name: "同买家下单频率", Scene: models.SceneOrder,
			Level: models.LevelReview, Priority: 30, Enabled: true,
			Config:      riskJSON(ruleConfig{WindowMinutes: 60, Max: 20}),
			Description: "1 小时内同一买家下单超过 20 笔即标记复核",
		},
		{
			Code: RuleOrderAmount, Name: "单笔金额异常", Scene: models.SceneOrder,
			Level: models.LevelReview, Priority: 40, Enabled: true,
			Config:      riskJSON(ruleConfig{Multiple: 5, MaxAmount: 2000}),
			Description: "单笔金额超过近 30 天客单价 5 倍即标记复核；无历史数据时用 max_amount 兜底",
		},
		{
			Code: RuleDailyAmount, Name: "日累计金额异常", Scene: models.SceneOrder,
			Level: models.LevelReview, Priority: 50, Enabled: true,
			Config:      riskJSON(ruleConfig{MaxAmount: 2000}),
			Description: "单个买家当日累计消费超过 2000 元即标记复核",
		},
		{
			Code: RuleRefundRate, Name: "退款率异常", Scene: models.SceneOrder,
			Level: models.LevelReject, Priority: 60, Enabled: true,
			Config:      riskJSON(ruleConfig{Ratio: 0.3, MinCount: 3}),
			Description: "近 30 天退款率超过 30% 且订单数 ≥ 3 即拒绝下单（默认关闭前请先观察一段时间）",
		},
		{
			Code: RuleBlacklist, Name: "黑名单匹配", Scene: models.SceneAll,
			Level: models.LevelReject, Priority: 1, Enabled: true,
			Config:      riskJSON(ruleConfig{Kinds: []string{models.BlacklistIP, models.BlacklistBuyer, models.BlacklistEmail}}),
			Description: "命中黑名单（IP / 买家 / 邮箱）直接拒绝。场景=全部，优先级最高，先于其他规则评估",
		},
		{
			Code: RuleSelfPurchase, Name: "分销自购/互买", Scene: models.SceneCommission,
			Level: models.LevelReject, Priority: 70,
			// 分销未上线，先关闭：开着只会白占一次评估
			Enabled:     false,
			Config:      riskJSON(ruleConfig{}),
			Description: "分销上线后启用：上级与下级同一支付账号/设备/收货信息即判自购刷佣",
		},
	}
}

// SeedDefaultRules 播种出厂规则。幂等，可重复调用。
//
// 只「补缺失」和「修正结构性字段（scene）」，绝不覆盖运营调过的
// enabled / level / priority / config —— 那些是业务决策，不是代码常量。
func (s *RiskService) SeedDefaultRules(ctx context.Context) error {
	for _, r := range DefaultRules() {
		row := r
		var existing models.RiskRule
		err := s.db.WithContext(ctx).Where("code = ?", row.Code).First(&existing).Error
		if err == nil {
			// 已存在：只修正 scene（结构性、和代码强绑定），其余一律不动。
			// 典型场景：黑名单规则的 scene 从 order 升级成 all，
			// 老库里那条必须跟着改，否则注册场景永远评估不到黑名单。
			if existing.Scene != row.Scene {
				if uerr := s.db.WithContext(ctx).Model(&models.RiskRule{}).
					Where("id = ?", existing.ID).
					Updates(map[string]any{"scene": row.Scene, "updated_at": time.Now()}).Error; uerr != nil {
					return uerr
				}
				slog.Info("修正风控规则场景", "code", row.Code,
					"from", existing.Scene, "to", row.Scene)
			}
			// 出厂即关闭的规则（例如分销未上线时的自购规则）：
			// 老库若被旧版「bool+default:true」的坑顶成启用，这里纠正一次。
			// 只对「代码里明确声明关闭」的规则生效，不会碰运营自己关掉的规则。
			if !row.Enabled && existing.Enabled {
				if uerr := s.db.WithContext(ctx).Model(&models.RiskRule{}).
					Where("id = ?", existing.ID).
					Updates(map[string]any{"enabled": false, "updated_at": time.Now()}).Error; uerr != nil {
					return uerr
				}
				slog.Warn("修正风控规则启用状态（出厂应为关闭）", "code", row.Code)
			}
			continue
		}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return err
		}
	}
	s.Invalidate()
	return nil
}
