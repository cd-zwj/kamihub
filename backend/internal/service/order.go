package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"kamihub/internal/cache"
	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
	"kamihub/internal/pkg/tmpl"
	"kamihub/internal/platform"
	"kamihub/internal/routing"
)

// CreateOrderInput 下单入参
type CreateOrderInput struct {
	RequestID string
	GoodsCode string
	Quantity  int
	Remark    string
	UserID    *uint
	TTLHours  int // 0 表示用全局默认
}

// OrderService 是采购编排的核心。
//
// 一次下单分两个阶段，这个划分非常关键：
//
//	阶段一「选货源 + 下单」—— 可以故障转移：这个货源余额不足/库存不够，换下一个
//	阶段二「等待发货」      —— 绝不可故障转移：上游单已经建了、钱已经扣了，
//	                          再换平台就是重复扣款
type OrderService struct {
	db         *gorm.DB
	cfg        *config.Config
	cache      *cache.Client
	platforms  *PlatformService
	settings   *SettingsService
	deliveries *DeliveryService
	routes     *routing.Registry
}

func NewOrderService(
	db *gorm.DB,
	cfg *config.Config,
	c *cache.Client,
	platforms *PlatformService,
	settings *SettingsService,
	deliveries *DeliveryService,
	routes *routing.Registry,
) *OrderService {
	return &OrderService{
		db: db, cfg: cfg, cache: c,
		platforms: platforms, settings: settings,
		deliveries: deliveries, routes: routes,
	}
}

// ErrGoodsNotFound 商品不存在或已下架
var ErrGoodsNotFound = errors.New("商品不存在或已下架")

// Create 执行一次采购。
func (s *OrderService) Create(ctx context.Context, in CreateOrderInput) (*models.Order, error) {
	in.RequestID = strings.TrimSpace(in.RequestID)
	in.GoodsCode = strings.TrimSpace(in.GoodsCode)
	if in.RequestID == "" {
		return nil, errors.New("request_id 不能为空（用于幂等，防止重复下单）")
	}
	if in.GoodsCode == "" {
		return nil, errors.New("goods_code 不能为空")
	}
	if len(in.RequestID) > 128 {
		return nil, errors.New("request_id 过长")
	}

	// ---- 幂等：命中直接返回，绝不下第二单 ----
	if existing, err := s.findByRequestID(ctx, in.RequestID); err == nil && existing != nil {
		slog.Info("命中幂等记录，直接回放", "request_id", in.RequestID, "order_no", existing.OrderNo)
		return existing, nil
	}

	// ---- 加载商品 ----
	var goods models.Goods
	if err := s.db.WithContext(ctx).Where("code = ?", in.GoodsCode).First(&goods).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGoodsNotFound
		}
		return nil, err
	}
	if goods.Status != models.GoodsStatusOn {
		return nil, ErrGoodsNotFound
	}

	quantity := in.Quantity
	if quantity <= 0 {
		quantity = 1
	}
	if goods.MaxQuantity > 0 && quantity > goods.MaxQuantity {
		return nil, fmt.Errorf("数量超过该商品上限：最大 %d", goods.MaxQuantity)
	}

	// ---- 同商品串行，避免并发把同一个货源抢空 ----
	release, ok := s.cache.Lock(ctx, fmt.Sprintf("lock:goods:%d", goods.ID), 3*time.Minute)
	if !ok {
		return nil, errors.New("该商品正在采购中，请稍后重试")
	}
	defer release()

	// 拿到锁后再查一次幂等，覆盖并发重复提交
	if existing, err := s.findByRequestID(ctx, in.RequestID); err == nil && existing != nil {
		return existing, nil
	}

	// ---- 构建候选货源 ----
	candidates, err := s.buildCandidates(ctx, goods.ID)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("该商品没有可用的货源，请先在后台配置并启用货源")
	}

	strategy := s.routes.Get(goods.RoutingStrategy)
	ranked := strategy.Rank(routing.FilterQuantity(candidates, quantity))

	order := &models.Order{
		OrderNo:     generateOrderNo(),
		RequestID:   in.RequestID,
		GoodsID:     goods.ID,
		Quantity:    quantity,
		Status:      models.OrderStatusPurchasing,
		Remark:      truncateStr(in.Remark, 500),
		CreatedByUserID: in.UserID,
	}
	if snap, err := json.Marshal(goods); err == nil {
		order.GoodsSnapshot = snap
	}

	slog.Info("开始采购",
		"order_no", order.OrderNo, "goods", goods.Code,
		"quantity", quantity, "strategy", strategy.Name(), "candidates", len(ranked))

	maxAttempts := goods.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if !goods.FailoverEnabled {
		maxAttempts = 1
	}

	// ================= 阶段一：选货源并下单（可故障转移）=================
	var (
		purchased   *platform.PurchaseResult
		winner      *routing.Candidate
		winnerPlat  *models.Platform
		lastErr     error
		attemptLogs []models.OrderAttempt
		// priceSkipped 统计因超过最高供应价而被跳过的货源数量。
		// 用于在全部失败时给出「不是没货，是都太贵」这种可行动的结论。
		priceSkipped int
	)

	for i, cand := range ranked {
		if i >= maxAttempts {
			break
		}
		started := time.Now()

		client, plat, cerr := s.platforms.ClientByID(cand.PlatformID)
		if cerr != nil {
			lastErr = cerr
			attemptLogs = append(attemptLogs, s.attemptRecord(order, cand, false, platform.KindFatal, cerr.Error(), started))
			slog.Warn("构造平台客户端失败，跳过", "platform", cand.PlatformName, "error", cerr.Error())
			continue
		}

		// 平台级限流
		if !s.cache.Allow(ctx, fmt.Sprintf("rl:platform:%d", cand.PlatformID), plat.RateLimitPerMin, time.Minute) {
			lastErr = platform.NewTransient("平台调用频率超限", "RATE_LIMITED", 429, nil)
			attemptLogs = append(attemptLogs, s.attemptRecord(order, cand, false, platform.KindTransient, lastErr.Error(), started))
			slog.Warn("平台限流，尝试下一个货源", "platform", cand.PlatformName)
			continue
		}

		res, perr := s.attemptPurchase(ctx, client, cand, quantity, in.Remark, goods.MaxSupplyPrice)
		if perr != nil {
			lastErr = perr
			kind := platform.KindOf(perr)
			attemptLogs = append(attemptLogs, s.attemptRecord(order, cand, false, kind, perr.Error(), started))
			if platform.ReasonOf(perr) == "SUPPLY_PRICE_EXCEEDED" {
				priceSkipped++
				slog.Info("货源报价超过最高供应价，跳过并尝试下一个",
					"order_no", order.OrderNo, "platform", cand.PlatformName,
					"max_supply_price", goods.MaxSupplyPrice.StringFixed(2))
			} else {
				slog.Warn("货源采购失败",
					"order_no", order.OrderNo, "platform", cand.PlatformName,
					"kind", kind, "error", perr.Error())
			}

			if !platform.Failoverable(perr) {
				slog.Warn("错误不可故障转移，停止尝试", "kind", kind)
				break
			}
			continue
		}

		// 下单成功 —— 从这里开始绝不能再换平台
		purchased = res
		winner = &cand
		winnerPlat = plat
		attemptLogs = append(attemptLogs, s.attemptRecord(order, cand, true, "", "", started))
		slog.Info("采购下单成功",
			"order_no", order.OrderNo, "platform", plat.Name, "trade_no", res.TradeNo, "status", res.Status)
		break
	}

	// ================= 阶段二：等待发货（不可故障转移）=================
	if purchased != nil {
		if purchased.Status == platform.StatusPaid {
			final, werr := s.waitForDelivery(ctx, winnerPlat, *winner, purchased.TradeNo)
			if werr != nil {
				// 上游单已建立、款已扣，只是还没确认发货。
				// 标记为采购中并保留单号，交给「重新查单」补偿，绝不换平台重买。
				slog.Warn("已下单但未确认发货，保留单号等待人工/自动补偿",
					"order_no", order.OrderNo, "trade_no", purchased.TradeNo, "error", werr.Error())
				order.Status = models.OrderStatusPurchasing
				order.ErrorMsg = werr.Error()
				order.ErrorKind = string(platform.KindTransient)
			} else {
				purchased = final
			}
		}
	}

	// ---- 落库 ----
	now := time.Now()
	if purchased != nil {
		order.SupplyID = &winner.SupplyID
		order.PlatformID = &winner.PlatformID
		order.PlatformTradeNo = purchased.TradeNo
		order.UnitPrice = purchased.UnitPrice
		order.TotalAmount = purchased.TotalAmount
		order.Content = purchased.Content
		// 使用说明按「本次实际成交的那条货源」决定：
		// 该货源设了专用说明就用它，没设才用上游原文。
		// 同一个商品挂多个货源时，这里天然做到「哪家供货用哪家的说明」。
		order.Instruction = resolveInstruction(ctx, s.db, winner.SupplyID, purchased.Instruction)
		order.ManualInstruction = purchased.ManualInstruction
		if len(purchased.Cards) > 0 {
			if raw, err := json.Marshal(purchased.Cards); err == nil {
				order.Cards = raw
			}
		}
		if purchased.DeliveredAt != nil {
			order.DeliveredAt = purchased.DeliveredAt
		} else {
			order.DeliveredAt = &now
		}
		if purchased.Status.IsSuccess() {
			order.Status = models.OrderStatusDelivered
			order.DeliveredAt = &now
		} else {
			order.Status = models.OrderStatusPurchasing
		}
	} else {
		order.Status = models.OrderStatusFailed
		if lastErr != nil {
			order.ErrorMsg = lastErr.Error()
			order.ErrorKind = string(platform.KindOf(lastErr))
		} else {
			order.ErrorMsg = "所有货源均不可用"
		}
		// 如果尝试过的货源全部（至少一个）是因为超价被跳过，把结论说清楚，
		// 否则运营看到一长串报价错误，仍不知道「到底是有货没货」。
		if priceSkipped > 0 && purchased == nil {
			order.ErrorMsg = fmt.Sprintf(
				"全部 %d 个货源的报价都超过最高供应价 %s，已拒绝下单（最后一条：%s）",
				priceSkipped, goods.MaxSupplyPrice.StringFixed(2), order.ErrorMsg)
			order.ErrorKind = "SUPPLY_PRICE_EXCEEDED"
		}
	}

	// 生成附加内容与提货链接（仅在发货成功时）
	if order.Status == models.OrderStatusDelivered {
		order.ExtraText = s.renderExtra(ctx, &goods, order, purchased)
	}

	if err := s.db.WithContext(ctx).Create(order).Error; err != nil {
		// request_id 唯一索引冲突：说明并发下已被另一个请求创建，回放那一条
		if isDuplicateKey(err) {
			if existing, ferr := s.findByRequestID(ctx, in.RequestID); ferr == nil && existing != nil {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("保存订单失败: %w", err)
	}

	order.Attempts = len(attemptLogs)
	if err := s.db.WithContext(ctx).Model(order).Update("attempts", len(attemptLogs)).Error; err != nil {
		slog.Warn("更新订单尝试次数失败", "order_no", order.OrderNo, "error", err.Error())
	}
	for i := range attemptLogs {
		attemptLogs[i].OrderID = order.ID
	}
	if len(attemptLogs) > 0 {
		if err := s.db.WithContext(ctx).Create(&attemptLogs).Error; err != nil {
			slog.Warn("写入订单尝试记录失败", "order_no", order.OrderNo, "error", err.Error())
		}
	}

	if order.Status != models.OrderStatusDelivered {
		slog.Error("采购最终失败", "order_no", order.OrderNo, "error", order.ErrorMsg)
		return order, nil
	}

	// ---- 生成提货链接 ----
	d, err := s.deliveries.CreateForOrder(ctx, order, in.TTLHours)
	if err != nil {
		slog.Error("生成提货链接失败", "order_no", order.OrderNo, "error", err.Error())
		return order, nil
	}
	order.Delivery = d

	slog.Info("采购完成",
		"order_no", order.OrderNo,
		"platform", winnerPlat.Name,
		"trade_no", order.PlatformTradeNo,
		"cards", len(purchased.CardCodes()),
		"delivery_url", s.deliveries.URL(d.Token))

	return order, nil
}

// resolveInstruction 决定这笔订单最终写入的「使用说明」。
//
// 规则：
//   - 该货源配置了专用说明（instruction_override 非空）→ 用它，完全替代上游原文
//   - 没配置 → 用上游原文
//
// 为什么要在这里固化而不是渲染时再查：订单发货后货源可能被改甚至被删，
// 渲染时再查会让「已经发出去的订单」内容跟着变。下单那一刻定下来才可追溯。
func resolveInstruction(ctx context.Context, db *gorm.DB, supplyID uint, upstream string) string {
	if supplyID == 0 {
		return upstream
	}
	var sup models.Supply
	if err := db.WithContext(ctx).Select("instruction_override").First(&sup, supplyID).Error; err != nil {
		// 查不到就回落到上游原文，不能让一条说明把整单搞失败
		slog.Warn("读取货源专用使用说明失败，回落到上游原文",
			"supply_id", supplyID, "error", err.Error())
		return upstream
	}
	if v := strings.TrimSpace(sup.InstructionOverride); v != "" {
		return v
	}
	return upstream
}

// attemptPurchase 阶段一的一次尝试：询价 → 校验 → 下单。
// 只做到「下单成功」为止，不等待发货。
//
// maxSupplyPrice 是商品级的最高供应价（每笔应付总额上限，含手续费）。
// 为 0 表示不限制。询价超过上限时返回 Business 错误，让上层跳过这个货源、
// 继续尝试下一个 —— 这正是「限价」与「故障转移」能配合的地方。
func (s *OrderService) attemptPurchase(
	ctx context.Context,
	client platform.Client,
	cand routing.Candidate,
	quantity int,
	remark string,
	maxSupplyPrice decimal.Decimal,
) (*platform.PurchaseResult, error) {
	quote, err := client.Quote(ctx, platform.QuoteRequest{
		GoodsNo:  cand.PlatformGoodsNo,
		Quantity: quantity,
	})
	if err != nil {
		return nil, err
	}

	// ---- 最高供应价校验 ----
	// 用询价回来的「应付总额」比较，而不是缓存里的成本价：
	// 缓存价可能过期，且不含手续费，拿它判亏不亏会失真。
	if maxSupplyPrice.IsPositive() && quote.TotalAmount.GreaterThan(maxSupplyPrice) {
		return nil, platform.NewBusiness(
			fmt.Sprintf("货源 %s 报价 %s 超过最高供应价 %s（单价 %s × %d + 手续费 %s）",
				cand.PlatformName,
				quote.TotalAmount.StringFixed(2), maxSupplyPrice.StringFixed(2),
				quote.UnitPrice.StringFixed(2), quantity, quote.Fee.StringFixed(2)),
			"SUPPLY_PRICE_EXCEEDED")
	}

	if !quote.BalanceEnough {
		return nil, platform.NewBusiness(
			fmt.Sprintf("平台余额不足：需要 %s，可用 %s", quote.TotalAmount.StringFixed(2), quote.AvailableBalance.StringFixed(2)),
			"BALANCE_NOT_ENOUGH")
	}
	if quote.AvailableQuantity >= 0 && quote.AvailableQuantity < quantity {
		return nil, platform.NewBusiness(
			fmt.Sprintf("货源库存不足：需要 %d，可售 %d", quantity, quote.AvailableQuantity),
			"STOCK_NOT_ENOUGH")
	}
	if quote.LimitQuantity > 0 && quantity < quote.LimitQuantity {
		return nil, platform.NewBusiness(
			fmt.Sprintf("低于该货源起购数量：起购 %d", quote.LimitQuantity),
			"BELOW_LIMIT_QUANTITY")
	}

	// 每日消费上限（拿真实金额判断，而不是估算）
	if err := s.checkDailyLimit(ctx, quote.TotalAmount); err != nil {
		return nil, err
	}

	res, err := client.Purchase(ctx, platform.PurchaseRequest{
		GoodsNo:   cand.PlatformGoodsNo,
		Quantity:  quantity,
		Remark:    remark,
		RequestID: "",
	})
	if err != nil {
		return nil, err
	}

	if res.Status.IsTerminalFailure() {
		return nil, platform.NewBusiness(
			fmt.Sprintf("上游订单状态异常：%s", res.Status), string(res.Status))
	}
	return res, nil
}

// waitForDelivery 阶段二：轮询上游直到发货。
func (s *OrderService) waitForDelivery(
	ctx context.Context,
	plat *models.Platform,
	cand routing.Candidate,
	tradeNo string,
) (*platform.PurchaseResult, error) {
	client, _, err := s.platforms.ClientByID(plat.ID)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(s.cfg.Upstream.PollTimeout)
	interval := s.cfg.Upstream.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		res, qerr := client.QueryOrder(ctx, tradeNo)
		if qerr != nil {
			slog.Debug("查单失败，继续等待", "trade_no", tradeNo, "error", qerr.Error())
			continue
		}
		if res.Status.IsSuccess() {
			return res, nil
		}
		if res.Status.IsTerminalFailure() {
			return nil, platform.NewBusiness(
				fmt.Sprintf("上游订单状态异常：%s", res.Status), string(res.Status))
		}
	}
	return nil, fmt.Errorf("等待发货超时（%s）", s.cfg.Upstream.PollTimeout)
}

// buildCandidates 从启用的货源与平台构建候选列表。
func (s *OrderService) buildCandidates(ctx context.Context, goodsID uint) ([]routing.Candidate, error) {
	var supplies []models.Supply
	if err := s.db.WithContext(ctx).
		Preload("Platform").
		Where("goods_id = ? AND enabled = ?", goodsID, true).
		Find(&supplies).Error; err != nil {
		return nil, err
	}

	out := make([]routing.Candidate, 0, len(supplies))
	for _, sup := range supplies {
		if sup.Platform == nil || !sup.Platform.Enabled {
			continue
		}
		// 适配器必须存在，否则配了也没法调
		if _, ok := s.platforms.Registry().Get(sup.Platform.Code); !ok {
			slog.Warn("货源的平台适配器未注册，已跳过",
				"supply_id", sup.ID, "platform_code", sup.Platform.Code)
			continue
		}
		out = append(out, routing.Candidate{
			SupplyID:        sup.ID,
			PlatformID:      sup.PlatformID,
			PlatformCode:    sup.Platform.Code,
			PlatformName:    sup.Platform.Name,
			PlatformGoodsNo: sup.PlatformGoodsNo,
			CostPrice:       sup.CostPrice,
			Stock:           sup.Stock,
			Priority:        sup.Priority,
			Weight:          sup.Weight,
		})
	}
	return out, nil
}

// checkDailyLimit 每日消费上限。取当日已成功下单的金额合计。
func (s *OrderService) checkDailyLimit(ctx context.Context, amount decimal.Decimal) error {
	security := s.settings.Security(ctx)
	if security.MaxDailyAmount <= 0 {
		return nil
	}
	limit := decimal.NewFromFloat(security.MaxDailyAmount)

	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	var used decimal.Decimal
	err := s.db.WithContext(ctx).Model(&models.Order{}).
		Where("created_at >= ? AND status IN ?", dayStart,
			[]string{models.OrderStatusDelivered, models.OrderStatusPurchasing}).
		Select("COALESCE(SUM(total_amount), 0)").Scan(&used).Error
	if err != nil {
		slog.Warn("统计当日消费失败，跳过限额检查", "error", err.Error())
		return nil
	}

	if used.Add(amount).GreaterThan(limit) {
		return platform.NewBusiness(
			fmt.Sprintf("超过每日消费上限：今日已用 %s，本单 %s，上限 %s",
				used.StringFixed(2), amount.StringFixed(2), limit.StringFixed(2)),
			"DAILY_LIMIT_EXCEEDED")
	}
	return nil
}

// renderExtra 渲染提货页附加内容。商品级模板优先于全局模板。
func (s *OrderService) renderExtra(ctx context.Context, goods *models.Goods, order *models.Order, res *platform.PurchaseResult) string {
	page := s.settings.DeliveryPage(ctx)
	template := strings.TrimSpace(goods.ExtraTemplate)
	if template == "" {
		template = page.ExtraTemplate
	}
	if template == "" {
		return ""
	}

	codes := []string{}
	if res != nil {
		codes = res.CardCodes()
	}
	deliverTime := ""
	if order.DeliveredAt != nil {
		deliverTime = order.DeliveredAt.Format("2006-01-02 15:04:05")
	}

	vars := map[string]string{
		"order_no":     order.OrderNo,
		"trade_no":     order.PlatformTradeNo,
		"goods_code":   goods.Code,
		"goods_name":   goods.Name,
		"quantity":     fmt.Sprintf("%d", order.Quantity),
		"unit_price":   order.UnitPrice.StringFixed(2),
		"total_amount": order.TotalAmount.StringFixed(2),
		"cards":        strings.Join(codes, "\n"),
		"cards_inline": strings.Join(codes, ","),
		"card_count":   fmt.Sprintf("%d", len(codes)),
		"content":      order.Content,
		"instruction":  order.Instruction,
		"deliver_time": deliverTime,
		"now":          time.Now().Format("2006-01-02 15:04:05"),
		"date":         time.Now().Format("2006-01-02"),
	}
	return tmpl.Render(template, vars)
}

func (s *OrderService) attemptRecord(
	order *models.Order,
	cand routing.Candidate,
	success bool,
	kind platform.FailKind,
	errMsg string,
	started time.Time,
) models.OrderAttempt {
	return models.OrderAttempt{
		OrderID:      order.ID,
		SupplyID:     cand.SupplyID,
		PlatformID:   cand.PlatformID,
		PlatformName: cand.PlatformName,
		Success:      success,
		FailKind:     string(kind),
		ErrorMsg:     truncateStr(errMsg, 2000),
		ElapsedMS:    int(time.Since(started).Milliseconds()),
	}
}

func (s *OrderService) findByRequestID(ctx context.Context, requestID string) (*models.Order, error) {
	var order models.Order
	err := s.db.WithContext(ctx).
		Preload("Goods").
		Preload("Platform").
		Preload("Delivery").
		Where("request_id = ?", requestID).First(&order).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &order, nil
}

// GetByOrderNo 按订单号取订单
func (s *OrderService) GetByOrderNo(ctx context.Context, orderNo string) (*models.Order, error) {
	var order models.Order
	err := s.db.WithContext(ctx).
		Preload("Goods").Preload("Platform").Preload("Delivery").
		Where("order_no = ?", orderNo).First(&order).Error
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// RetryQuery 对「已下单但未确认发货」的订单重新查单，成功则补发提货链接。
func (s *OrderService) RetryQuery(ctx context.Context, orderID uint) (*models.Order, error) {
	var order models.Order
	if err := s.db.WithContext(ctx).Preload("Goods").First(&order, orderID).Error; err != nil {
		return nil, err
	}
	if order.PlatformID == nil || order.PlatformTradeNo == "" {
		return nil, errors.New("该订单没有上游单号，无法查单")
	}
	if order.Status == models.OrderStatusDelivered {
		return &order, nil
	}

	client, _, err := s.platforms.ClientByID(*order.PlatformID)
	if err != nil {
		return nil, err
	}
	res, err := client.QueryOrder(ctx, order.PlatformTradeNo)
	if err != nil {
		return nil, err
	}

	if !res.Status.IsSuccess() {
		return &order, fmt.Errorf("上游订单仍未发货，当前状态：%s", res.Status)
	}

	now := time.Now()
	updates := map[string]any{
		"status":       models.OrderStatusDelivered,
		"delivered_at": now,
		"content":      res.Content,
		"instruction":  res.Instruction,
		"error_msg":    "",
		"error_kind":   "",
	}
	if len(res.Cards) > 0 {
		if raw, mErr := json.Marshal(res.Cards); mErr == nil {
			// 必须转成 datatypes.JSON，否则 GORM 写不进 JSONB 列
			updates["cards"] = datatypes.JSON(raw)
		}
	}
	if err := s.db.WithContext(ctx).Model(&models.Order{}).Where("id = ?", order.ID).Updates(updates).Error; err != nil {
		return nil, err
	}

	order.Status = models.OrderStatusDelivered
	order.DeliveredAt = &now
	order.Content = res.Content
	order.Instruction = res.Instruction

	// 还没有提货链接就补一个
	var d models.Delivery
	if err := s.db.WithContext(ctx).Where("order_id = ?", order.ID).First(&d).Error; err != nil {
		newD, cErr := s.deliveries.CreateForOrder(ctx, &order, 0)
		if cErr != nil {
			return &order, cErr
		}
		order.Delivery = newD
	} else {
		order.Delivery = &d
	}
	return &order, nil
}

// generateOrderNo 生成订单号：KM + 日期 + 随机后缀
func generateOrderNo() string {
	suffix, err := crypto.RandomToken(6)
	if err != nil {
		suffix = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	suffix = strings.ToUpper(strings.NewReplacer("-", "", "_", "").Replace(suffix))
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return fmt.Sprintf("KM%s%s", time.Now().Format("20060102150405"), suffix)
}

func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicated key")
}
