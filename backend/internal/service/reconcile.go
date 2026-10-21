package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"kamihub/internal/models"
	"kamihub/internal/platform"
)

// ReconcileService 对账。
//
// 第一版原则：**只检测、只记录、不自动改库**。
//
// 为什么不自动修：
//   - 订单状态不一致可以以上游为准自动修，但钱包差异不行 —— 钱的事必须人工确认。
//   - 先跑两周，确认没有误报，再逐项开启自动修复。
//
// 覆盖四类差异（见 models.ReconcileDiff 的 kind 常量）。
type ReconcileService struct {
	db        *gorm.DB
	platforms *PlatformService
	cfg       ReconcileConfig
}

// ReconcileConfig 对账参数
type ReconcileConfig struct {
	// FrozenStaleMinutes 钱包冻结超过该分钟数仍未结算/退款，视为异常
	FrozenStaleMinutes int
	// LookbackHours 上游订单回溯窗口（16688 无增量接口，只能按时间窗全量拉）
	LookbackHours int
	// MaxOrdersPerPlatform 单个平台每次对账最多拉多少单，防止上游分页拉爆
	MaxOrdersPerPlatform int
}

// DefaultReconcileConfig 推荐默认值
func DefaultReconcileConfig() ReconcileConfig {
	return ReconcileConfig{
		FrozenStaleMinutes:   30,
		LookbackHours:        72,
		MaxOrdersPerPlatform: 500,
	}
}

// NewReconcileService 构造对账服务
func NewReconcileService(db *gorm.DB, platforms *PlatformService, cfg ReconcileConfig) *ReconcileService {
	if cfg.FrozenStaleMinutes <= 0 {
		cfg.FrozenStaleMinutes = 30
	}
	if cfg.LookbackHours <= 0 {
		cfg.LookbackHours = 72
	}
	if cfg.MaxOrdersPerPlatform <= 0 {
		cfg.MaxOrdersPerPlatform = 500
	}
	return &ReconcileService{db: db, platforms: platforms, cfg: cfg}
}

// ReconcileResult 一次对账的结果
type ReconcileResult struct {
	BizDate    string
	Total      int64
	ByKind     map[string]int64
	DurationMS int64
}

// Run 执行一次对账（检测版）。
//
// bizDate 传空表示「今天」。所有差异按 bizDate 归档，同一天重复跑
// 靠唯一索引 (kind, order_no, biz_date) 去重，因此**天然幂等**。
func (s *ReconcileService) Run(ctx context.Context, bizDate string) (*ReconcileResult, error) {
	started := time.Now()
	if bizDate == "" {
		bizDate = time.Now().Format("2006-01-02")
	}

	res := &ReconcileResult{BizDate: bizDate, ByKind: map[string]int64{}}

	// 1. 冻结超时
	if n, err := s.checkFrozenStale(ctx, bizDate); err != nil {
		slog.Warn("对账：检查冻结超时失败", "error", err.Error())
	} else {
		res.ByKind[models.ReconcileWalletFrozenStale] = n
		res.Total += n
	}

	// 2. 支付已成功但未入账
	if n, err := s.checkPaymentNotSettled(ctx, bizDate); err != nil {
		slog.Warn("对账：检查支付入账失败", "error", err.Error())
	} else {
		res.ByKind[models.ReconcilePaymentNotSettled] = n
		res.Total += n
	}

	// 3. 上游订单状态比对（需要上游可达，失败不阻塞其他项）
	if n, err := s.checkUpstreamOrders(ctx, bizDate); err != nil {
		slog.Warn("对账：上游订单比对失败", "error", err.Error())
	} else {
		res.ByKind[models.ReconcileOrderStatusMismatch] = n
		res.Total += n
	}

	res.DurationMS = time.Since(started).Milliseconds()
	slog.Info("对账完成",
		"biz_date", bizDate,
		"total", res.Total,
		"by_kind", fmt.Sprint(res.ByKind),
		"duration_ms", res.DurationMS)
	return res, nil
}

// checkFrozenStale 找出冻结超时未决的钱包流水。
func (s *ReconcileService) checkFrozenStale(ctx context.Context, bizDate string) (int64, error) {
	cutoff := time.Now().Add(-time.Duration(s.cfg.FrozenStaleMinutes) * time.Minute)

	var rows []models.WalletTransaction
	if err := s.db.WithContext(ctx).
		Where("status = ? AND created_at < ?", models.WalletStatusFrozen, cutoff).
		Limit(200).
		Find(&rows).Error; err != nil {
		return 0, err
	}

	var created int64
	for i := range rows {
		tx := &rows[i]
		detail, _ := json.Marshal(map[string]any{
			"wallet_tx_id": tx.ID,
			"buyer_id":     tx.BuyerID,
			"amount":       tx.Amount.String(),
			"request_id":   tx.RequestID,
			"created_at":   tx.CreatedAt.Format(time.RFC3339),
			"age_minutes":  int(time.Since(tx.CreatedAt).Minutes()),
		})
		ok, err := s.insertDiff(ctx, models.ReconcileDiff{
			BizDate: bizDate,
			Kind:    models.ReconcileWalletFrozenStale,
			Amount:  tx.Amount.Abs(),
			Detail:  detail,
		})
		if err != nil {
			return created, err
		}
		if ok {
			created++
		}
	}
	return created, nil
}

// checkPaymentNotSettled 找出支付单已 paid 但没有关联入账流水的记录。
func (s *ReconcileService) checkPaymentNotSettled(ctx context.Context, bizDate string) (int64, error) {
	var rows []models.PaymentOrder
	if err := s.db.WithContext(ctx).
		Where("status = ? AND wallet_tx_id IS NULL", models.PaymentStatusPaid).
		Limit(200).
		Find(&rows).Error; err != nil {
		return 0, err
	}

	var created int64
	for i := range rows {
		po := &rows[i]
		detail, _ := json.Marshal(map[string]any{
			"trade_no":  po.TradeNo,
			"buyer_id":  po.BuyerID,
			"amount":    po.Amount.String(),
			"pay_type":  po.PayType,
			"paid_at":   formatTimePtr(po.PaidAt),
			"notify_n":  po.NotifyCount,
		})
		ok, err := s.insertDiff(ctx, models.ReconcileDiff{
			BizDate: bizDate,
			Kind:    models.ReconcilePaymentNotSettled,
			TradeNo: po.TradeNo,
			Amount:  po.Amount,
			Detail:  detail,
		})
		if err != nil {
			return created, err
		}
		if ok {
			created++
		}
	}
	return created, nil
}

// checkUpstreamOrders 拉上游订单与本地比对。
//
// 16688 没有增量接口，只能按时间窗全量拉。为避免拉爆上游，
// 每个平台有 MaxOrdersPerPlatform 上限，且失败不影响其他平台。
func (s *ReconcileService) checkUpstreamOrders(ctx context.Context, bizDate string) (int64, error) {
	var platforms []models.Platform
	if err := s.db.WithContext(ctx).
		Where("enabled = ?", true).
		Find(&platforms).Error; err != nil {
		return 0, err
	}

	since := time.Now().Add(-time.Duration(s.cfg.LookbackHours) * time.Hour)

	// 本地窗口内的订单，按上游单号索引
	var localOrders []models.Order
	if err := s.db.WithContext(ctx).
		Where("created_at >= ? AND platform_trade_no <> ''", since).
		Find(&localOrders).Error; err != nil {
		return 0, err
	}
	localByTrade := make(map[string]*models.Order, len(localOrders))
	for i := range localOrders {
		localByTrade[localOrders[i].PlatformTradeNo] = &localOrders[i]
	}

	var created int64
	for i := range platforms {
		p := &platforms[i]
		n, err := s.reconcilePlatform(ctx, p, bizDate, localByTrade)
		if err != nil {
			slog.Warn("对账：平台比对失败",
				"platform", p.Name, "error", err.Error())
			continue
		}
		created += n
	}
	return created, nil
}

// upstreamOrderLister 可选接口：支持列订单的适配器可参与订单级对账。
//
// 不是所有平台都能列单（16688 的 purchase/list 可以，但别的适配器未必），
// 所以用类型断言探测，而不是硬塞进 platform.Client 接口 —— 那会让
// 所有适配器都被迫实现一个可能用不上的方法。
type upstreamOrderLister interface {
	ListOrders(ctx context.Context, since time.Time, limit int) ([]platform.PurchaseResult, error)
}

func (s *ReconcileService) reconcilePlatform(
	ctx context.Context,
	p *models.Platform,
	bizDate string,
	localByTrade map[string]*models.Order,
) (int64, error) {
	client, _, err := s.platforms.ClientByID(p.ID)
	if err != nil {
		return 0, err
	}
	lister, ok := client.(upstreamOrderLister)
	if !ok {
		// 适配器不支持列单，跳过订单级对账（不算失败）
		slog.Debug("对账：适配器不支持列单，跳过订单级比对",
			"platform", p.Name, "code", p.Code)
		return 0, nil
	}

	since := time.Now().Add(-time.Duration(s.cfg.LookbackHours) * time.Hour)
	remote, err := lister.ListOrders(ctx, since, s.cfg.MaxOrdersPerPlatform)
	if err != nil {
		return 0, err
	}

	var created int64
	for i := range remote {
		r := &remote[i]
		if r.TradeNo == "" {
			continue
		}
		local, exists := localByTrade[r.TradeNo]

		// 上游有、本地无
		if !exists {
			detail, _ := json.Marshal(map[string]any{
				"platform": p.Name,
				"trade_no": r.TradeNo,
				"goods_no": r.GoodsNo,
				"status":   string(r.Status),
				"amount":   r.TotalAmount.String(),
			})
			ok, ierr := s.insertDiff(ctx, models.ReconcileDiff{
				BizDate:      bizDate,
				Kind:         models.ReconcileOrderMissingLocal,
				TradeNo:      r.TradeNo,
				PlatformID:   &p.ID,
				RemoteStatus: string(r.Status),
				Amount:       r.TotalAmount,
				Detail:       detail,
			})
			if ierr != nil {
				return created, ierr
			}
			if ok {
				created++
			}
			continue
		}

		// 两边状态不一致
		localStatus := mapLocalToUpstreamStatus(local.Status)
		if localStatus != string(r.Status) {
			detail, _ := json.Marshal(map[string]any{
				"platform":     p.Name,
				"trade_no":     r.TradeNo,
				"order_no":     local.OrderNo,
				"local_status": local.Status,
				"remote":       string(r.Status),
			})
			ok, ierr := s.insertDiff(ctx, models.ReconcileDiff{
				BizDate:      bizDate,
				Kind:         models.ReconcileOrderStatusMismatch,
				OrderNo:      local.OrderNo,
				TradeNo:      r.TradeNo,
				PlatformID:   &p.ID,
				LocalStatus:  local.Status,
				RemoteStatus: string(r.Status),
				Amount:       r.TotalAmount,
				Detail:       detail,
			})
			if ierr != nil {
				return created, ierr
			}
			if ok {
				created++
			}
		}
	}
	return created, nil
}

// insertDiff 插入一条差异，返回是否真的新增（已存在则返回 false）。
//
// 幂等实现：先按 (kind, order_no/trade_no, biz_date) 查重。
// 不用 ON CONFLICT 是因为 order_no 可能为空，唯一索引带 NULL 时行为不直观。
func (s *ReconcileService) insertDiff(ctx context.Context, d models.ReconcileDiff) (bool, error) {
	q := s.db.WithContext(ctx).Model(&models.ReconcileDiff{}).
		Where("kind = ? AND biz_date = ?", d.Kind, d.BizDate)
	if d.OrderNo != "" {
		q = q.Where("order_no = ?", d.OrderNo)
	} else if d.TradeNo != "" {
		q = q.Where("trade_no = ?", d.TradeNo)
	} else {
		// 没有业务键（如钱包冻结）时用 detail 里的主键兜底
		q = q.Where("detail->>'wallet_tx_id' = ?", extractWalletTxID(d.Detail))
	}

	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	d.CreatedAt = time.Now()
	if err := s.db.WithContext(ctx).Create(&d).Error; err != nil {
		return false, err
	}
	return true, nil
}

// extractWalletTxID 从 detail 里取 wallet_tx_id 字符串，用于查重。
func extractWalletTxID(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if v, ok := m["wallet_tx_id"]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// mapLocalToUpstreamStatus 把本地订单状态映射成平台层状态字符串，用于比对。
//
// 注意这只是「大致等价」，不追求完全一致：
// 本地有 pending/purchasing 两个中间态，而上游只有 paid。
func mapLocalToUpstreamStatus(local string) string {
	switch local {
	case models.OrderStatusPending, models.OrderStatusPurchasing:
		return string(platform.StatusPaid)
	case models.OrderStatusDelivered:
		return string(platform.StatusDelivered)
	case models.OrderStatusRefunded:
		return string(platform.StatusRefunded)
	case models.OrderStatusClosed, models.OrderStatusCancelled:
		return string(platform.StatusClosed)
	default:
		return local
	}
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// ---------------------------------------------------------------- 查询

// ListDiffs 分页查差异
func (s *ReconcileService) ListDiffs(ctx context.Context, page, size int, resolved *bool, kind string) ([]models.ReconcileDiff, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.ReconcileDiff{})
	if resolved != nil {
		q = q.Where("resolved = ?", *resolved)
	}
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset, limit := pageOffset(page, size)
	var rows []models.ReconcileDiff
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ResolveDiff 标记差异已处理（人工确认后调用）
func (s *ReconcileService) ResolveDiff(ctx context.Context, id uint, userID uint, remark string) error {
	now := time.Now()
	updates := map[string]any{
		"resolved":    true,
		"resolved_by": userID,
		"resolved_at": &now,
	}
	if remark != "" {
		updates["remark"] = truncateStr(remark, 255)
	}
	return s.db.WithContext(ctx).Model(&models.ReconcileDiff{}).
		Where("id = ?", id).Updates(updates).Error
}

// SumUnresolved 未处理差异汇总，供告警使用
func (s *ReconcileService) SumUnresolved(ctx context.Context) (int64, decimal.Decimal, error) {
	type row struct {
		Cnt  int64
		Amt  decimal.Decimal
	}
	var r row
	err := s.db.WithContext(ctx).Model(&models.ReconcileDiff{}).
		Where("resolved = ?", false).
		Select("COUNT(*) AS cnt, COALESCE(SUM(amount),0) AS amt").
		Scan(&r).Error
	return r.Cnt, r.Amt, err
}
