package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"kamihub/internal/models"
)

// 销售与档位相关错误
var (
	ErrAgentAlreadyActive = errors.New("该账号已经是销售")
	ErrAgentNotActive     = errors.New("该账号还不是销售")
	ErrAgentFrozen        = errors.New("该账号的销售资格已被冻结")
	ErrTierNotFound       = errors.New("档位不存在")
)

// 档位重算参数
const (
	// TierStatWindowDays 销量统计窗口（设计 D3：近 30 天滚动，不是永久累计）
	TierStatWindowDays = 30
	// TierGraceDays 降级宽限期（设计 D5）
	TierGraceDays = 7
	// RefundRateThreshold 退款率超过这个比例时不予升级（设计 R3）
	RefundRateThreshold = 0.30
	// RefundRateMinOrders 退款率判定的最少订单数
	RefundRateMinOrders = 3
	// BurstRatio 单日订单占窗口总量超过这个比例视为异常暴量（设计 R5）
	BurstRatio = 0.50
	// BurstMinOrders 暴量判定的最少订单数（单量太少时比例没有意义）
	BurstMinOrders = 20
)

// AgentService 销售资格、档位统计与重算。
//
// 为什么把「统计」和「重算」放在同一个服务：
// 它们共用同一套口径（哪些订单算数、窗口多长、怎么排除退款），
// 拆成两处必然会出现「后台看到的销量」和「实际用来算档位的销量」不一致。
type AgentService struct {
	db      *gorm.DB
	pricing *PricingService
	// enabled 总开关（settings 的 agent.enabled）。关闭时：
	//   - 不再重算档位
	//   - BuyerQuote 不做任何档位折扣（所有人按零售价）
	enabled func(ctx context.Context) bool
}

func NewAgentService(db *gorm.DB, pricing *PricingService) *AgentService {
	return &AgentService{db: db, pricing: pricing}
}

// WithSwitch 装配总开关（出问题一键关闭，不用回滚代码）
func (s *AgentService) WithSwitch(fn func(ctx context.Context) bool) *AgentService {
	s.enabled = fn
	return s
}

// Enabled 当前是否启用阶梯专价
func (s *AgentService) Enabled(ctx context.Context) bool {
	if s.enabled == nil {
		return true
	}
	return s.enabled(ctx)
}

// ---------------------------------------------------------------- 统计口径

// TierStats 某个买家在统计窗口内的销量。
type TierStats struct {
	Orders int             `json:"orders"`
	Amount decimal.Decimal `json:"amount"`
	// Refunded 窗口内退款/失败单数（用于退款率闸门）
	Refunded int `json:"refunded"`
	// RefundRate 退款率（0-1）
	RefundRate float64 `json:"refund_rate"`
	// MaxDayOrders 单日最高订单数（用于暴量检测）
	MaxDayOrders int `json:"max_day_orders"`
	// Suspicious 是否命中防刷闸门（命中则不升级）
	Suspicious bool   `json:"suspicious"`
	Reason     string `json:"reason"`
}

// Stats30d 统计某买家近 30 天的有效销量。
//
// 口径（设计 D4）：
//   - 只算 delivered —— 退款/失败/取消都不算，这是最直接的防刷
//   - 金额用 paid_amount（实际支付额），不是订单原价
//
// 关于「排除自购」：设计文档 R1 写的是「自购订单不计入统计」。
// 但那是**返佣模式**的规则（防止上级买自己的货套佣金）。
// 本项目是批发模式 —— 销售本来就是「从我这拿货再卖出去」，
// 他自己的下单就是业务本身；如果排除自购，所有人销量都是 0，整个档位系统失效。
// 所以这里**不排除自购**，防刷改由下面两道闸承担：
//   - R3 退款率过高不升级
//   - R5 单日暴量不升级
func (s *AgentService) Stats30d(ctx context.Context, buyerID uint) (TierStats, error) {
	since := time.Now().AddDate(0, 0, -TierStatWindowDays)
	var st TierStats

	type aggRow struct {
		Orders int
		Amount decimal.Decimal
	}
	var agg aggRow
	if err := s.db.WithContext(ctx).Model(&models.Order{}).
		Where("buyer_id = ? AND status = ? AND created_at >= ?",
			buyerID, models.OrderStatusDelivered, since).
		Select("COUNT(*) AS orders, COALESCE(SUM(paid_amount),0) AS amount").
		Scan(&agg).Error; err != nil {
		return st, err
	}
	st.Orders = agg.Orders
	st.Amount = agg.Amount

	// 窗口内所有订单数（用于算退款率的分母）
	var allCount int64
	if err := s.db.WithContext(ctx).Model(&models.Order{}).
		Where("buyer_id = ? AND created_at >= ?", buyerID, since).
		Count(&allCount).Error; err != nil {
		return st, err
	}
	if allCount > 0 {
		st.Refunded = int(allCount) - st.Orders
		st.RefundRate = float64(st.Refunded) / float64(allCount)
	}

	// 单日最高订单数（只看 delivered，与统计口径一致）
	type dayRow struct {
		Day string
		N   int
	}
	var dayRows []dayRow
	if err := s.db.WithContext(ctx).Model(&models.Order{}).
		Where("buyer_id = ? AND status = ? AND created_at >= ?",
			buyerID, models.OrderStatusDelivered, since).
		Select("to_char(created_at, 'YYYY-MM-DD') AS day, COUNT(*) AS n").
		Group("day").Scan(&dayRows).Error; err != nil {
		return st, err
	}
	for _, r := range dayRows {
		if r.N > st.MaxDayOrders {
			st.MaxDayOrders = r.N
		}
	}

	// ---- 防刷闸门 ----
	if st.Orders >= RefundRateMinOrders && st.RefundRate > RefundRateThreshold {
		st.Suspicious = true
		st.Reason = fmt.Sprintf("退款率 %.0f%% 超过 %.0f%% 阈值，本次不予升级",
			st.RefundRate*100, RefundRateThreshold*100)
	} else if st.Orders >= BurstMinOrders && float64(st.MaxDayOrders) > float64(st.Orders)*BurstRatio {
		st.Suspicious = true
		st.Reason = fmt.Sprintf("单日最高 %d 单占窗口总量 %.0f%%，疑似一次性冲量，本次不予升级",
			st.MaxDayOrders, float64(st.MaxDayOrders)/float64(st.Orders)*100)
	}

	return st, nil
}

// bestTier 按统计结果匹配应得的最高档位。
//
// 判定条件：订单数 **或** 采购额，满足任一即可（设计 §5.1）。
// 双口径更公平：卖便宜货的靠单量、卖贵货的靠金额，都能上去。
func bestTier(tiers []models.PriceTier, st TierStats) *models.PriceTier {
	var best *models.PriceTier
	for i := range tiers {
		t := &tiers[i]
		if !t.Enabled {
			continue
		}
		hit := false
		if t.MinMonthlyOrders > 0 && st.Orders >= t.MinMonthlyOrders {
			hit = true
		}
		if t.MinMonthlyAmount.IsPositive() && st.Amount.GreaterThanOrEqual(t.MinMonthlyAmount) {
			hit = true
		}
		if !hit {
			continue
		}
		// 折扣越低（让价越多）的档位越"高"
		if best == nil || t.DiscountRate.LessThan(best.DiscountRate) {
			best = t
		}
	}
	if best == nil {
		for i := range tiers {
			if tiers[i].IsRetail() {
				return &tiers[i]
			}
		}
	}
	return best
}

// ---------------------------------------------------------------- 档位重算

// RecalcResult 一次重算的统计
type RecalcResult struct {
	Scanned    int `json:"scanned"`
	Upgraded   int `json:"upgraded"`
	Downgraded int `json:"downgraded"`
	InGrace    int `json:"in_grace"`
	Held       int `json:"held"` // 因防刷闸门不予升级
	Failed     int `json:"failed"`
}

// RecalcTiers 重算所有正常销售的档位（每日跑一次）。
//
// 升降级规则（设计 §5.2）：
//
//	达到更高档 → 立即升级
//	跌破当前档 → 进入 7 天宽限；宽限期内保留原档；期满仍未达标才降级
//
// 为什么要宽限：销售某月冲量达标，下月月初回落，立刻降级会让他「不敢休息」。
// 7 天的让价总额有限，体验收益远大于成本。
func (s *AgentService) RecalcTiers(ctx context.Context) (RecalcResult, error) {
	var res RecalcResult
	if !s.Enabled(ctx) {
		slog.Info("阶梯专价总开关已关闭，跳过档位重算")
		return res, nil
	}

	tiers, err := s.pricing.Tiers(ctx)
	if err != nil {
		return res, err
	}
	if len(tiers) == 0 {
		return res, errors.New("档位表为空，无法重算（请检查 price_tiers 配置）")
	}

	var agents []models.Buyer
	if err := s.db.WithContext(ctx).
		Where("agent_status = ?", models.AgentStatusActive).
		Find(&agents).Error; err != nil {
		return res, err
	}
	res.Scanned = len(agents)

	now := time.Now()
	for i := range agents {
		b := &agents[i]
		st, serr := s.Stats30d(ctx, b.ID)
		if serr != nil {
			slog.Error("统计销量失败", "buyer_id", b.ID, "error", serr.Error())
			res.Failed++
			continue
		}
		target := bestTier(tiers, st)
		if target == nil {
			continue
		}
		current := b.PriceTier
		if current == "" {
			current = models.TierRetail
		}

		updates := map[string]any{
			"tier_calc_at": now,
		}

		switch {
		case target.Code == current:
			// 持平：清掉宽限标记（如果之前进过宽限又恢复了）
			if b.TierGraceUntil != nil {
				updates["tier_grace_until"] = nil
			}

		case s.tierRank(tiers, target.Code) > s.tierRank(tiers, current):
			// 升级：立即生效
			if st.Suspicious {
				// 命中防刷闸门 → 暂不升级，等下一次
				res.Held++
				slog.Warn("命中防刷闸门，暂不升级", "buyer_id", b.ID, "reason", st.Reason)
				continue
			}
			updates["price_tier"] = target.Code
			updates["tier_grace_until"] = nil
			res.Upgraded++
			slog.Info("销售档位升级", "buyer_id", b.ID, "from", current, "to", target.Code,
				"orders", st.Orders, "amount", st.Amount.StringFixed(2))

		default:
			// 降级：先看是否在宽限期
			if b.TierGraceUntil != nil && now.Before(*b.TierGraceUntil) {
				res.InGrace++
				continue // 宽限期内保留原档
			}
			if b.TierGraceUntil == nil {
				// 第一次跌破：开启宽限期，本次不降级
				grace := now.AddDate(0, 0, TierGraceDays)
				updates["tier_grace_until"] = grace
				res.InGrace++
				slog.Info("销售档位跌破，进入宽限期", "buyer_id", b.ID,
					"current", current, "target", target.Code, "grace_until", grace.Format(time.RFC3339))
				break
			}
			updates["price_tier"] = target.Code
			updates["tier_grace_until"] = nil
			res.Downgraded++
			slog.Info("销售档位降级", "buyer_id", b.ID, "from", current, "to", target.Code)
		}

		if err := s.db.WithContext(ctx).Model(&models.Buyer{}).
			Where("id = ?", b.ID).Updates(updates).Error; err != nil {
			slog.Error("更新档位失败", "buyer_id", b.ID, "error", err.Error())
			res.Failed++
		}
	}
	return res, nil
}

// tierRank 档位高低序号（越大越高级）。
//
// 档位越高 → 折扣率越低（让价越多）。所以序号 = 10000 − 折扣率×10000：
//
//	retail  1.0000 → 0      （最低）
//	bronze  0.9833 → 167
//	silver  0.9667 → 333
//	gold    0.9500 → 500
//	diamond 0.9417 → 583    （最高）
//
// 直接用折扣率当序号是错的：那样 retail 的 10000 最大，
// 「升级」判断会全部变成「降级」。这个方向错误不会报错，
// 只会让所有人永远升不上去，极难发现（本用例就是被它绊住的）。
func (s *AgentService) tierRank(tiers []models.PriceTier, code string) int {
	for i := range tiers {
		if tiers[i].Code == code {
			return 10000 - int(tiers[i].DiscountRate.Mul(decimal.NewFromInt(10000)).InexactFloat64())
		}
	}
	return 0 // 未知档位按最低处理
}

// ---------------------------------------------------------------- 销售资格

// AgentApplyInput 销售申请
type AgentApplyInput struct {
	BuyerID uint   `json:"buyer_id"`
	Reason  string `json:"reason"`
}

// Apply 买家申请成为销售。
//
// 申请只是把 agent_remark 写下来并留待审核；**不会**自动开通。
// 早期只放信任的人（设计 D8）。
func (s *AgentService) Apply(ctx context.Context, in AgentApplyInput) error {
	var b models.Buyer
	if err := s.db.WithContext(ctx).First(&b, in.BuyerID).Error; err != nil {
		return err
	}
	if b.AgentStatus == models.AgentStatusActive {
		return ErrAgentAlreadyActive
	}
	if b.AgentStatus == models.AgentStatusFrozen {
		return ErrAgentFrozen
	}
	return s.db.WithContext(ctx).Model(&models.Buyer{}).
		Where("id = ?", in.BuyerID).
		Updates(map[string]any{
			"agent_remark": truncateStr("[申请] "+in.Reason, 250),
			"updated_at":   time.Now(),
		}).Error
}

// ReviewInput 后台审核
type ReviewInput struct {
	Approve bool   `json:"approve"`
	Remark  string `json:"remark"`
	// TierCode 通过时可直接指定初始档位（默认 retail）
	TierCode string `json:"tier_code"`
}

// Review 审核销售申请。
func (s *AgentService) Review(ctx context.Context, buyerID uint, in ReviewInput) error {
	var b models.Buyer
	if err := s.db.WithContext(ctx).First(&b, buyerID).Error; err != nil {
		return err
	}

	now := time.Now()
	if !in.Approve {
		return s.db.WithContext(ctx).Model(&models.Buyer{}).
			Where("id = ?", buyerID).
			Updates(map[string]any{
				"agent_status": models.AgentStatusNone,
				"agent_remark": truncateStr("[拒绝] "+in.Remark, 250),
				"updated_at":   now,
			}).Error
	}

	tier := in.TierCode
	if tier == "" {
		tier = models.TierRetail
	}
	// 校验档位存在，避免写入一个不存在的档位导致算价时找不到
	tiers, err := s.pricing.Tiers(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, t := range tiers {
		if t.Code == tier {
			found = true
			break
		}
	}
	if !found {
		return ErrTierNotFound
	}

	return s.db.WithContext(ctx).Model(&models.Buyer{}).
		Where("id = ?", buyerID).
		Updates(map[string]any{
			"agent_status":   models.AgentStatusActive,
			"agent_bound_at": now,
			"agent_remark":   truncateStr(in.Remark, 250),
			"price_tier":     tier,
			"tier_calc_at":   now,
			"updated_at":     now,
		}).Error
}

// SetTier 后台手动改档位（立即生效，设计验收项）
func (s *AgentService) SetTier(ctx context.Context, buyerID uint, tierCode string) error {
	tiers, err := s.pricing.Tiers(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, t := range tiers {
		if t.Code == tierCode {
			found = true
			break
		}
	}
	if !found {
		return ErrTierNotFound
	}
	res := s.db.WithContext(ctx).Model(&models.Buyer{}).
		Where("id = ?", buyerID).
		Updates(map[string]any{
			"price_tier":       tierCode,
			"tier_calc_at":     time.Now(),
			"tier_grace_until": nil,
			"updated_at":       time.Now(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("买家不存在")
	}
	return nil
}

// SetAgentStatus 后台冻结 / 解冻销售资格
func (s *AgentService) SetAgentStatus(ctx context.Context, buyerID uint, status int, remark string) error {
	if status != models.AgentStatusNone &&
		status != models.AgentStatusActive &&
		status != models.AgentStatusFrozen {
		return errors.New("非法的销售状态")
	}
	updates := map[string]any{
		"agent_status": status,
		"updated_at":   time.Now(),
	}
	if remark != "" {
		updates["agent_remark"] = truncateStr(remark, 250)
	}
	// 冻结时立刻降回零售价，避免「已冻结但还在享受专价」
	if status == models.AgentStatusFrozen {
		updates["price_tier"] = models.TierRetail
	}
	return s.db.WithContext(ctx).Model(&models.Buyer{}).
		Where("id = ?", buyerID).Updates(updates).Error
}

// ---------------------------------------------------------------- 销售侧视图

// AgentOverview 销售中心页的数据（GET /api/shop/agent/me）
type AgentOverview struct {
	AgentStatus int               `json:"agent_status"`
	Enabled     bool              `json:"enabled"`
	CurrentTier *models.PriceTier `json:"current_tier"`
	NextTier    *models.PriceTier `json:"next_tier"`
	Stats30d    TierStats         `json:"stats_30d"`
	// ToNext 距离下一档还差多少（「还差 X 单」是转化率最高的 UI 元素）
	ToNextOrders int             `json:"to_next_orders"`
	ToNextAmount decimal.Decimal `json:"to_next_amount"`
	GraceUntil   *time.Time      `json:"grace_until"`
	// FellBackHint 当前档位有商品撑不住（后台提示用）
	Remark string `json:"remark"`
}

// Overview 取某买家的销售概览。
func (s *AgentService) Overview(ctx context.Context, buyerID uint) (*AgentOverview, error) {
	var b models.Buyer
	if err := s.db.WithContext(ctx).First(&b, buyerID).Error; err != nil {
		return nil, err
	}
	tiers, err := s.pricing.Tiers(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.Stats30d(ctx, buyerID)
	if err != nil {
		return nil, err
	}

	out := &AgentOverview{
		AgentStatus: b.AgentStatus,
		Enabled:     s.Enabled(ctx),
		Stats30d:    st,
		GraceUntil:  b.TierGraceUntil,
		Remark:      b.AgentRemark,
	}

	cur := b.PriceTier
	if cur == "" {
		cur = models.TierRetail
	}
	for i := range tiers {
		if tiers[i].Code == cur {
			t := tiers[i]
			out.CurrentTier = &t
			break
		}
	}

	// 下一档 = 折扣率比当前更低的档位里，折扣最接近当前的那个
	if out.CurrentTier != nil {
		for i := range tiers {
			t := &tiers[i]
			if !t.DiscountRate.LessThan(out.CurrentTier.DiscountRate) {
				continue
			}
			if out.NextTier == nil || t.DiscountRate.GreaterThan(out.NextTier.DiscountRate) {
				out.NextTier = t
			}
		}
	}
	if out.NextTier != nil {
		if out.NextTier.MinMonthlyOrders > st.Orders {
			out.ToNextOrders = out.NextTier.MinMonthlyOrders - st.Orders
		}
		if out.NextTier.MinMonthlyAmount.GreaterThan(st.Amount) {
			out.ToNextAmount = out.NextTier.MinMonthlyAmount.Sub(st.Amount)
		}
	}
	return out, nil
}

// PriceListRow 专价目表的一行
type PriceListRow struct {
	ListingID   uint            `json:"listing_id"`
	Title       string          `json:"title"`
	Category    string          `json:"category"`
	RetailPrice decimal.Decimal `json:"retail_price"`
	TierPrice   decimal.Decimal `json:"tier_price"`
	// Profit 销售卖零售价时能赚的差价（零售价 − 专价）
	Profit decimal.Decimal `json:"profit"`
	// FellBack 该商品撑不住当前档位，已回落
	FellBack bool   `json:"fell_back"`
	TierCode string `json:"tier_code"`
}

// PriceList 生成某买家的专价目表（GET /api/shop/agent/price-list）
func (s *AgentService) PriceList(ctx context.Context, buyerID uint) ([]PriceListRow, error) {
	var b models.Buyer
	if err := s.db.WithContext(ctx).First(&b, buyerID).Error; err != nil {
		return nil, err
	}
	tier := b.PriceTier
	if tier == "" {
		tier = models.TierRetail
	}

	var listings []models.Listing
	if err := s.db.WithContext(ctx).Preload("Goods").
		Where("is_listed = ? AND sale_price > 0", true).
		Order("sort_order DESC, id DESC").Find(&listings).Error; err != nil {
		return nil, err
	}

	out := make([]PriceListRow, 0, len(listings))
	for i := range listings {
		l := &listings[i]
		row := PriceListRow{
			ListingID:   l.ID,
			Title:       l.Title,
			Category:    l.Category,
			RetailPrice: l.SalePrice,
			TierPrice:   l.SalePrice,
			TierCode:    models.TierRetail,
		}
		if s.Enabled(ctx) {
			q, err := s.pricing.Quote(ctx, l, tier)
			if err != nil {
				slog.Error("专价目表算价失败", "listing_id", l.ID, "error", err.Error())
			} else {
				row.TierPrice = q.UnitPrice
				row.TierCode = q.TierCode
				row.FellBack = q.FellBack
			}
		}
		row.Profit = row.RetailPrice.Sub(row.TierPrice)
		out = append(out, row)
	}
	return out, nil
}
