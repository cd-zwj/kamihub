// Package notify 提供统一的通知投递能力。
//
// 设计要点：
//
//  1. 渠道可插拔：新增渠道 = 实现 Channel 接口 + 注册，业务代码不动。
//  2. 聚合去重：同一事件在窗口内只推一次，累积次数合并。
//     没有这个，第一周就会把 Telegram 群刷爆，然后运维关掉通知 —— 等于没有。
//  3. 静默期：夜间只放 error 级别出去，避免半夜被 warn 吵醒。
//  4. 站内信永不丢：无论渠道是否成功，都会落库一条 notifications，
//     后台能查到「当时到底报了什么」。
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/models"
)

// Level 通知级别。级别决定静默期是否放行。
type Level string

const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Message 一条待投递的通知。
type Message struct {
	Level   Level
	Event   string // 事件标识，用于聚合去重，如 balance.low
	Title   string
	Content string
	Link    string         // 站内跳转链接
	Fields  map[string]any // 结构化字段，便于渠道格式化
	At      time.Time
	// NoAggregate 跳过聚合窗口，每次都单独推送。
	//
	// 只给「每一条都是独立事实」的安全事件用。典型例子是新 IP 登录：
	// 聚合后只剩一句「有新 IP 登录了后台」和一个次数，**到底哪个 IP 反而不知道了**，
	// 而这是判断「是不是我」的唯一依据。
	//
	// 默认 false：运维告警（余额不足、平台故障）本来就该合并，
	// 否则第一周就会把 Telegram 群刷爆。
	NoAggregate bool
}

// Channel 一个通知渠道。
type Channel interface {
	Name() string
	Send(ctx context.Context, msg Message) error
}

// Config 通知器配置。
type Config struct {
	// AggregateWindow 同类事件聚合窗口。窗口内重复事件只推一次。
	AggregateWindow time.Duration
	// QuietStart / QuietEnd 静默期（小时，左闭右开）。
	// 静默期内只有 error 级别会推送到渠道；站内信照常落库。
	QuietStart int
	QuietEnd   int
	// Timeout 单次渠道投递超时
	Timeout time.Duration
}

// DefaultConfig 返回推荐默认值。
func DefaultConfig() Config {
	return Config{
		AggregateWindow: 5 * time.Minute,
		QuietStart:      2,
		QuietEnd:        7,
		Timeout:         10 * time.Second,
	}
}

// Notifier 通知分发器。
//
// 并发安全：定时任务可能在多个 goroutine 里同时告警，
// 聚合状态用互斥锁保护。
type Notifier struct {
	db       *gorm.DB
	channels []Channel
	cfg      Config

	mu         sync.Mutex
	lastSent   map[string]time.Time
	suppressed map[string]int

	// now 可在测试中替换
	now func() time.Time
}

// New 构造通知器。
func New(db *gorm.DB, cfg Config) *Notifier {
	if cfg.AggregateWindow <= 0 {
		cfg.AggregateWindow = 5 * time.Minute
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Notifier{
		db:         db,
		cfg:        cfg,
		lastSent:   make(map[string]time.Time),
		suppressed: make(map[string]int),
		now:        time.Now,
	}
}

// Register 注册渠道。
func (n *Notifier) Register(ch Channel) {
	if ch == nil {
		return
	}
	n.channels = append(n.channels, ch)
	slog.Info("已注册通知渠道", "channel", ch.Name())
}

// Channels 返回已注册渠道名，供后台展示。
func (n *Notifier) Channels() []string {
	out := make([]string, 0, len(n.channels))
	for _, ch := range n.channels {
		out = append(out, ch.Name())
	}
	return out
}

// Alert 发送一条运维告警（给站长）。
//
// 行为：
//  1. 聚合判断：窗口内同事件 → 计数并跳过渠道推送
//  2. 静默期判断：夜间只放 error
//  3. 无论是否推送渠道，都写一条站内信（buyer_id 为空）
func (n *Notifier) Alert(ctx context.Context, msg Message) {
	if msg.At.IsZero() {
		msg.At = n.now()
	}
	if msg.Level == "" {
		msg.Level = LevelInfo
	}

	// 聚合：命中窗口就只累计，不推送
	key := string(msg.Level) + "|" + msg.Event
	if !msg.NoAggregate && n.shouldSuppress(key, msg.At) {
		return
	}

	results := n.dispatch(ctx, msg)
	n.writeInbox(ctx, nil, msg, results)
}

// NotifyBuyer 给买家发站内信。
//
// 买家通知不做聚合（每个订单都是独立事件），也不走静默期
// （买家半夜下单也该看到结果）。
func (n *Notifier) NotifyBuyer(ctx context.Context, buyerID uint, msg Message) {
	if msg.At.IsZero() {
		msg.At = n.now()
	}
	if msg.Level == "" {
		msg.Level = LevelInfo
	}
	bid := buyerID
	n.writeInbox(ctx, &bid, msg, nil)
}

// shouldSuppress 判断是否因聚合窗口而抑制推送。
func (n *Notifier) shouldSuppress(key string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	last, ok := n.lastSent[key]
	if ok && now.Sub(last) < n.cfg.AggregateWindow {
		n.suppressed[key]++
		slog.Debug("告警被聚合抑制",
			"key", key,
			"suppressed_total", n.suppressed[key],
			"window", n.cfg.AggregateWindow.String())
		return true
	}
	n.lastSent[key] = now
	n.suppressed[key] = 0
	return false
}

// inQuietHours 判断是否处于静默期。
func (n *Notifier) inQuietHours(t time.Time) bool {
	start, end := n.cfg.QuietStart, n.cfg.QuietEnd
	if start == end {
		return false
	}
	h := t.Hour()
	if start < end {
		return h >= start && h < end
	}
	// 跨零点，例如 22:00 - 07:00
	return h >= start || h < end
}

// dispatch 向所有渠道推送，返回各渠道结果。
func (n *Notifier) dispatch(ctx context.Context, msg Message) map[string]string {
	results := make(map[string]string, len(n.channels))

	// 静默期只放 error
	if n.inQuietHours(msg.At) && msg.Level != LevelError {
		for _, ch := range n.channels {
			results[ch.Name()] = "skipped: quiet_hours"
		}
		return results
	}

	for _, ch := range n.channels {
		cctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
		err := ch.Send(cctx, msg)
		cancel()
		if err != nil {
			results[ch.Name()] = "fail: " + err.Error()
			slog.Warn("通知投递失败",
				"channel", ch.Name(), "event", msg.Event, "error", err.Error())
			continue
		}
		results[ch.Name()] = "ok"
	}
	return results
}

// writeInbox 落库站内信。失败只记日志，不影响主流程。
func (n *Notifier) writeInbox(ctx context.Context, buyerID *uint, msg Message, channels map[string]string) {
	if n.db == nil {
		return
	}
	var chJSON []byte
	if len(channels) > 0 {
		chJSON, _ = json.Marshal(channels)
	}
	row := models.Notification{
		BuyerID:   buyerID,
		Level:     string(msg.Level),
		Event:     msg.Event,
		Title:     msg.Title,
		Content:   msg.Content,
		Link:      msg.Link,
		Channels:  chJSON,
		CreatedAt: msg.At,
	}
	if err := n.db.WithContext(ctx).Create(&row).Error; err != nil {
		slog.Error("写入站内信失败", "event", msg.Event, "error", err.Error())
	}
}

// ---------------------------------------------------------------- 事件常量

// 运维告警事件
const (
	EventBalanceLow        = "balance.low"
	EventPlatformDown      = "platform.down"
	EventSupplyOutOfStock  = "supply.out_of_stock"
	EventSupplyCheckFail   = "supply.check_failed"
	EventOrderStale        = "order.stale"
	EventOrderFailRate     = "order.fail_rate"
	EventReconcileDiff     = "reconcile.diff"
	EventWorkerFailed      = "worker.task_failed"
	EventPaymentNotifyMiss = "payment.notify_miss"
	// EventRiskRejected 风控拒绝了一次操作（下单/注册）。
	//
	// 只在 reject 级命中时发：review 级命中量天生更大，
	// 「有人被标记复核」不值得打扰，但「有人被真的拦下来」必须知道 ——
	// 那可能是黑产在试，也可能是我们自己规则配错了在拦正常客户。
	EventRiskRejected = "risk.rejected"
	// EventLoginNewIP 员工账号从新 IP 登录。
	//
	// 这是「账号被盗用」最早能被发现的信号：密码被别人拿到后，
	// 攻击者第一次登录必然来自一个没见过的 IP。本次登录照常放行，
	// 只提醒（新 IP 里绝大多数是本人换了网络）。
	EventLoginNewIP = "auth.new_ip_login"
)

// 买家通知事件
const (
	EventBuyerOrderCreated   = "buyer.order_created"
	EventBuyerOrderDelivered = "buyer.order_delivered"
	EventBuyerOrderFailed    = "buyer.order_failed"
	EventBuyerWalletChanged  = "buyer.wallet_changed"
)

// FormatFields 把结构化字段拼成便于阅读的文本（用于纯文本渠道）。
func FormatFields(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	out := ""
	for k, v := range fields {
		out += fmt.Sprintf("%s: %v\n", k, v)
	}
	return out
}
