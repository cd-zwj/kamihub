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
	// cards 自有卡密池。自建商品（stock_mode=local）不走上游，直接从这里取卡密。
	cards *CardService
}

func NewOrderService(
	db *gorm.DB,
	cfg *config.Config,
	c *cache.Client,
	platforms *PlatformService,
	settings *SettingsService,
	deliveries *DeliveryService,
	routes *routing.Registry,
	cards *CardService,
) *OrderService {
	return &OrderService{
		db: db, cfg: cfg, cache: c,
		platforms: platforms, settings: settings,
		deliveries: deliveries, routes: routes,
		cards: cards,
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

	// ---- 按供货方式分流 ----
	//
	// 分流必须发生在 buildCandidates 之前：只吃卡密池的商品本来就没有 Supply，
	// 按上游逻辑会直接报「没有可用的货源」，而这类商品压根不该有货源。
	return s.dispatch(ctx, &goods, in, quantity)
}

// dispatch 按商品的供货方式把订单交给对应的发货通道。
//
// 三条通道：
//
//	local  —— 只吃卡密池
//	hybrid —— 卡密池与上游都算货，按 LocalStockFirst 决定先后（见 createHybrid）
//	其余   —— 走上游采购（默认，历史行为）
func (s *OrderService) dispatch(
	ctx context.Context,
	goods *models.Goods,
	in CreateOrderInput,
	quantity int,
) (*models.Order, error) {
	switch {
	case goods.IsLocalStock():
		return s.createFromCardPool(ctx, goods, in, quantity)
	case goods.IsHybridStock():
		return s.createHybrid(ctx, goods, in, quantity)
	default:
		return s.createFromUpstream(ctx, goods, in, quantity)
	}
}

// stockChannel 一笔订单从哪个通道取货。
type stockChannel int

const (
	// channelPool 从自带卡密池取货
	channelPool stockChannel = iota
	// channelUpstream 向上游货源采购
	channelUpstream
)

// String 便于日志与测试断言可读
func (c stockChannel) String() string {
	if c == channelPool {
		return "pool"
	}
	return "upstream"
}

// hybridPlan 混合模式下一笔订单的取货计划。
type hybridPlan struct {
	// Primary 首选通道
	Primary stockChannel
	// FallbackToPool 首选上游失败后，是否允许退回卡密池兜底。
	// 只有「上游优先且池子够」时才为 true —— 池子不够就没有兜底能力。
	FallbackToPool bool
}

// planHybrid 根据「谁优先 + 池子够不够」算出取货计划。
//
// 抽成纯函数是为了让这条最容易搞错的判定能被穷举测试：
// 它直接决定「退货卡密到底卖不卖得出去」，判反了不会报错，只会静默地少卖。
//
//	池子优先 + 池子够  → 池子
//	池子优先 + 池子不够 → 上游（不拆单）
//	上游优先 + 池子够   → 上游，失败退回池子
//	上游优先 + 池子不够 → 上游（没有兜底）
func planHybrid(localStockFirst, poolEnough bool) hybridPlan {
	if localStockFirst {
		if poolEnough {
			return hybridPlan{Primary: channelPool}
		}
		return hybridPlan{Primary: channelUpstream}
	}
	return hybridPlan{Primary: channelUpstream, FallbackToPool: poolEnough}
}

// createHybrid 混合模式：卡密池与上游都是合法货源。
//
// 关键约束：**一笔订单只从一个通道取货，绝不拆单**。
// 拆单（池子发 2 张 + 上游发 3 张）看起来能提高成单率，但上游那半一旦失败，
// 退款金额、作废范围、卡密归属全部变得说不清 —— 收益远小于代价。
//
// 两种优先级的实际语义：
//
//	LocalStockFirst = true （默认）
//	    池子够 → 整单吃池子（清退货库存，且这部分没有上游成本）
//	    池子不够 → 整单走上游（池子里那几张这单不用，留给下一单）
//
//	LocalStockFirst = false
//	    先走上游；上游全部失败 → 池子够就整单吃池子兜底
//	    池子也不够 → 原样返回上游的失败原因
func (s *OrderService) createHybrid(
	ctx context.Context,
	goods *models.Goods,
	in CreateOrderInput,
	quantity int,
) (*models.Order, error) {
	poolAvail, err := s.cards.CountAvailable(ctx, goods.ID)
	if err != nil {
		return nil, fmt.Errorf("查询卡密池库存失败: %w", err)
	}
	plan := planHybrid(goods.LocalStockFirst, poolAvail >= int64(quantity))

	if plan.Primary == channelPool {
		slog.Info("混合模式：卡密池优先，整单走卡密池",
			"goods", goods.Code, "quantity", quantity, "pool_available", poolAvail)
		return s.createFromCardPool(ctx, goods, in, quantity)
	}

	// 以下是「首选上游」的两条分支：池子优先但池子不够，或本来就上游优先。
	if goods.LocalStockFirst {
		// 池子不够就整单走上游：不拆单，也不因为池子里有几张就先发几张。
		slog.Info("混合模式：卡密池不足，整单走上游",
			"goods", goods.Code, "quantity", quantity, "pool_available", poolAvail)
		return s.createFromUpstream(ctx, goods, in, quantity)
	}

	// 上游优先：先买上游，买不到再用池子兜底。
	order, uerr := s.createFromUpstream(ctx, goods, in, quantity)
	if uerr == nil && order != nil && order.Status == models.OrderStatusDelivered {
		return order, nil
	}
	if !plan.FallbackToPool {
		// 没有兜底能力，上游是什么结果就返回什么结果，
		// 失败原因（含「全部货源超最高供应价」）必须原样带给调用方。
		return order, uerr
	}
	if order == nil {
		// 上游在「建订单之前」就失败了（例如一个货源都没配），
		// 库里没有残留订单，可以直接新建一笔池子订单。
		slog.Warn("混合模式：上游未下单即失败，改用卡密池兜底",
			"goods", goods.Code, "pool_available", poolAvail, "error", errText(uerr))
		return s.createFromCardPool(ctx, goods, in, quantity)
	}

	// 上游已经落了一笔失败订单（同一个 request_id）。这里必须复用它：
	// 再建一笔同 request_id 的订单会撞唯一索引，反过来回放到那笔失败单。
	slog.Warn("混合模式：上游采购失败，改用卡密池兜底",
		"goods", goods.Code, "order_no", order.OrderNo,
		"pool_available", poolAvail, "error", order.ErrorMsg)
	return s.deliverFromPool(ctx, order, goods, in, quantity, true)
}

// errText 安全地把 error 转成日志文本（nil 转空串，避免打出 "<nil>"）。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// createFromUpstream 向上游货源采购并发货。
//
// 这是历史上唯一的发货通道，逻辑未做改动，只是从 Create 里抽出来，
// 好让混合模式能在它失败时接管。
func (s *OrderService) createFromUpstream(
	ctx context.Context,
	goods *models.Goods,
	in CreateOrderInput,
	quantity int,
) (*models.Order, error) {
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
		OrderNo:         generateOrderNo(),
		RequestID:       in.RequestID,
		GoodsID:         goods.ID,
		Quantity:        quantity,
		Status:          models.OrderStatusPurchasing,
		Remark:          truncateStr(in.Remark, 500),
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
		order.ExtraText = s.renderExtra(ctx, goods, order, purchased)
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

// createFromCardPool 用商品自带的卡密池本地发货。
//
// 与上游采购的区别：
//   - 没有「阶段一/阶段二」，也没有故障转移 —— 卡密就在自己库里，
//     要么有要么没有，不存在「换一家试试」
//   - 没有上游成本，成本是运营自己录的 CostPrice
//   - 卡密内容直接来自池子，使用说明来自商品的 UsageInstruction
//
// 流程刻意做成「先取卡密并提交占用，再写订单」：
// 反过来（先建订单再取卡密）会出现「订单已存在但没卡密」的中间态，
// 而取卡密的 SQL 本身是原子的，先占用最坏也只是少卖一条。
func (s *OrderService) createFromCardPool(
	ctx context.Context,
	goods *models.Goods,
	in CreateOrderInput,
	quantity int,
) (*models.Order, error) {
	// 一次可发多张：每张卡密仍逐条绑定到同一笔订单上，
	// 归属/退款/作废的追溯粒度不变，只是省掉调用方自己拆单。
	// 取卡密的过程是「取一张写一张」，中途没货就把前面已取的退回池子，
	// 绝不出现「收了三张的钱只发两张」这种半成品订单。

	order := &models.Order{
		OrderNo:         generateOrderNo(),
		RequestID:       in.RequestID,
		GoodsID:         goods.ID,
		Quantity:        quantity,
		Status:          models.OrderStatusPurchasing,
		Remark:          truncateStr(in.Remark, 500),
		CreatedByUserID: in.UserID,
	}
	if snap, err := json.Marshal(goods); err == nil {
		order.GoodsSnapshot = snap
	}

	// ---- 先落订单骨架，拿到 ID，才能让卡密绑定到它 ----
	if err := s.db.WithContext(ctx).Create(order).Error; err != nil {
		if isDuplicateKey(err) {
			// request_id 撞了：并发下另一个请求已经建好这单，回放它
			if existing, ferr := s.findByRequestID(ctx, in.RequestID); ferr == nil && existing != nil {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("保存订单失败: %w", err)
	}

	return s.deliverFromPool(ctx, order, goods, in, quantity, false)
}

// deliverFromPool 把卡密池里的卡密发到一笔**已经存在**的订单上。
//
// 抽出来是为了混合模式：上游优先时，上游已经落了一笔失败订单，
// 兜底不能再建一笔 —— 同一个 request_id 会撞唯一索引，反而回放成那笔失败单。
// 只能把卡密发到原订单上，让它从 failed 变成 delivered。
//
// reused 表示「这笔订单是复用上游失败单」：
// 复用时不覆盖 attempts，因为里面记着上游试过几次，
// 那份记录恰恰是排查「为什么这单最后吃了池子」的关键线索。
func (s *OrderService) deliverFromPool(
	ctx context.Context,
	order *models.Order,
	goods *models.Goods,
	in CreateOrderInput,
	quantity int,
	reused bool,
) (*models.Order, error) {
	now := time.Now()

	// ---- 原子取卡密（按数量逐张取） ----
	taken := make([]models.CardStock, 0, quantity)
	for i := 0; i < quantity; i++ {
		card, err := s.cards.TakeOne(ctx, goods.ID, order.ID, nil)
		if err != nil {
			// 没卡密了：这张订单标失败并说明原因，不留在 purchasing 上悬着。
			//
			// 关键：前面已经取到的卡密必须退回池子，否则它们会被永久锁在
			// 这笔失败的订单上 —— 池子里显示有货、实际发不出去。
			if len(taken) > 0 {
				if rerr := s.cards.ReleaseByOrder(ctx, order.ID); rerr != nil {
					slog.Error("取卡密不足后释放卡密失败，需人工核对",
						"order_no", order.OrderNo, "released", len(taken), "error", rerr.Error())
				}
			}
			// 文案要给运营可行动的信息：「补货」而不是把驱动错误原样抛出去。
			msg := "该商品卡密池已无库存，请先在「卡密池」补货后重新下单"
			if !errors.Is(err, ErrCardEmpty) {
				msg = "取卡密失败：" + err.Error()
			}
			if quantity > 1 {
				msg = fmt.Sprintf("该商品卡密池库存不足：需要 %d 张，只取到 %d 张，已全部退回卡密池，请补货后重新下单",
					quantity, len(taken))
				if !errors.Is(err, ErrCardEmpty) {
					msg = fmt.Sprintf("取卡密失败（已取到 %d 张，已全部退回）：%s", len(taken), err.Error())
				}
			}
			// 复用上游失败单时，池子也没兜住 —— 两条失败原因都要留下，
			// 否则运营只看到「卡密池无库存」，会以为这单根本没试过上游。
			if reused && order.ErrorMsg != "" {
				msg = fmt.Sprintf("上游采购失败（%s）；卡密池兜底也不足：%s", order.ErrorMsg, msg)
			}
			updates := map[string]any{
				"status":     models.OrderStatusFailed,
				"error_msg":  msg,
				"error_kind": string(platform.KindBusiness),
				"updated_at": time.Now(),
			}
			if uerr := s.db.WithContext(ctx).Model(&models.Order{}).Where("id = ?", order.ID).
				Updates(updates).Error; uerr != nil {
				slog.Warn("标记订单失败时出错", "order_no", order.OrderNo, "error", uerr.Error())
			}
			order.Status = models.OrderStatusFailed
			order.ErrorMsg = msg
			slog.Warn("卡密池发货失败", "order_no", order.OrderNo, "goods", goods.Code, "error", msg)
			return order, nil
		}
		taken = append(taken, *card)
	}

	// ---- 组装发货内容 ----
	cards := make([]platform.Card, 0, len(taken))
	for _, c := range taken {
		cards = append(cards, platform.Card{Code: c.Code})
	}
	raw, merr := json.Marshal(cards)
	if merr != nil {
		slog.Warn("序列化卡密失败", "order_no", order.OrderNo, "error", merr.Error())
	}

	unitPrice := goods.Price
	total := unitPrice.Mul(decimal.NewFromInt(int64(quantity)))
	// 提货页的「使用说明」用商品上那份通用说明。
	// 商品的 CostPrice 只用于内部算毛利，绝不出现在买家能看到的地方。
	updates := map[string]any{
		"status":       models.OrderStatusDelivered,
		"delivered_at": now,
		"unit_price":   unitPrice,
		"total_amount": total,
		"instruction":  goods.UsageInstruction,
		"error_msg":    "",
		"error_kind":   "",
	}
	// 复用的订单已经记着上游试过几次，别用 1 把它盖掉。
	if !reused {
		updates["attempts"] = 1
	}
	if merr == nil {
		updates["cards"] = datatypes.JSON(raw)
	}
	if err := s.db.WithContext(ctx).Model(&models.Order{}).Where("id = ?", order.ID).
		Updates(updates).Error; err != nil {
		// 订单没写成功，但卡密已经被占用 —— 全部退回去，别让卡密白白锁死。
		// 这里必须退回：卡密已售但订单没发货，对账时会对不上。
		// 一次可能取了多张，ReleaseByOrder 按 order_id 整批释放，天然覆盖。
		if rerr := s.cards.ReleaseByOrder(ctx, order.ID); rerr != nil {
			slog.Error("订单落库失败后释放卡密也失败，需人工核对",
				"order_no", order.OrderNo, "card_ids", cardIDsOf(taken), "error", rerr.Error())
		}
		return nil, fmt.Errorf("保存订单失败: %w", err)
	}

	order.Status = models.OrderStatusDelivered
	order.DeliveredAt = &now
	order.UnitPrice = unitPrice
	order.TotalAmount = total
	order.Instruction = goods.UsageInstruction
	order.ErrorMsg = ""
	order.ErrorKind = ""
	if !reused {
		order.Attempts = 1
	}
	if merr == nil {
		order.Cards = datatypes.JSON(raw)
	}

	// 附加内容模板（与上游订单用同一套渲染逻辑）
	order.ExtraText = s.renderExtra(ctx, goods, order, &platform.PurchaseResult{
		UnitPrice:   unitPrice,
		TotalAmount: total,
		Cards:       cards,
		Content:     "",
		Instruction: goods.UsageInstruction,
	})

	if errtxt := order.ExtraText; errtxt != "" {
		if uerr := s.db.WithContext(ctx).Model(&models.Order{}).Where("id = ?", order.ID).
			Update("extra_text", errtxt).Error; uerr != nil {
			slog.Warn("写入附加内容失败", "order_no", order.OrderNo, "error", uerr.Error())
		}
	}

	slog.Info("卡密池发货成功",
		"order_no", order.OrderNo, "goods", goods.Code, "cards", len(taken),
		"card_ids", cardIDsOf(taken))

	// ---- 生成提货链接 ----
	d, derr := s.deliveries.CreateForOrder(ctx, order, in.TTLHours)
	if derr != nil {
		slog.Error("生成提货链接失败", "order_no", order.OrderNo, "error", derr.Error())
		return order, nil
	}
	order.Delivery = d
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
	// 卡密池发货的订单没有上游单号，也就无所谓「重新查单」。
	// 它的失败原因是池子空了，正确动作是补货后重新下单，而不是重查。
	//
	// 注意这里判定的是「这笔订单实际从哪儿发的」，不是商品的供货方式：
	// 混合模式可能这单吃了池子、下一单吃了上游，只有真正没有上游单号的才不能查。
	if order.PlatformID == nil || order.PlatformTradeNo == "" {
		return &order, errors.New("该订单没有上游单号可查（由卡密池发货）；补货后请重新下单")
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

// cardIDsOf 把本次发出的卡密 id 拼成日志用的字符串。
// 一次可能发多张，日志里要能一眼看出到底动了池子里的哪几条。
func cardIDsOf(cards []models.CardStock) string {
	ids := make([]string, 0, len(cards))
	for _, c := range cards {
		ids = append(ids, fmt.Sprintf("%d", c.ID))
	}
	return strings.Join(ids, ",")
}

// ---- 撤销订单 ----

// 撤销时卡密的去向
const (
	// CardActionReturn 退回卡密池，可以再卖。
	CardActionReturn = "return"
	// CardActionVoid 作废，不能再卖，但保留记录便于对账。
	CardActionVoid = "void"
	// CardActionKeep 什么都不做，卡密仍标记已售。
	// 用于「只想让提货链接失效，但营收和库存都要保留原样」的场景。
	CardActionKeep = "keep"
)

// CancelOptions 撤销订单的入参
type CancelOptions struct {
	// Reason 撤销原因，会写进订单备注与审计日志
	Reason string
	// CardAction 卡密去向：return / void / keep。留空时按商品的默认配置决定
	CardAction string
	// ForceView 买家已查看过提货页时是否仍强行退回。
	//
	// 默认 false：卡密明文已经露出去了，退回池子会被卖给第二个人，
	// 造成同一张卡密发给两个买家。运营要强行退回必须显式传 true，
	// 并且这件事会写进审计日志，出问题时有据可查。
	ForceView bool
	// UserID 操作人，写审计日志用
	UserID *uint
}

// CancelResult 撤销结果，供接口回显
type CancelResult struct {
	Order      *models.Order
	CardAction string
	CardCount  int64
	ViewCount  int
	// Warn 需要提醒运营的话（例如「买家已查看过，卡密可能已暴露」）
	Warn string
}

// Cancel 撤销一笔已发货的订单。
//
// 它一次做完三件事，缺一不可：
//  1. 决定卡密去向（退回池子 / 作废 / 保持不动）
//  2. 作废提货链接 —— 否则撤销了买家还能点开看卡密
//  3. 把订单标记为 cancelled，并清掉 cards 快照
//
// 为什么要清 cards：卡密退回池子后会被别人买走，
// 订单上再留着这段明文，运营对账时会看到「已撤销的订单里还挂着卡密」，
// 很容易误判成「这张卡密还是这单的」。清掉才是诚实的。
//
// 整个方法在一个事务里。撤销是钱和货的事，不能做一半。
func (s *OrderService) Cancel(ctx context.Context, orderID uint, opts CancelOptions) (*CancelResult, error) {
	if orderID == 0 {
		return nil, errors.New("订单 ID 不能为空")
	}

	var out *CancelResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order models.Order
		if err := tx.First(&order, orderID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("订单不存在")
			}
			return err
		}
		if order.Status == models.OrderStatusCancelled {
			return errors.New("该订单已经撤销过了")
		}
		// 只有发货成功的订单才谈得上撤销（卡密确实发出去了）。
		// failed / purchasing 的单没有已售卡密，撤销没有意义，也避免误操作。
		if order.Status != models.OrderStatusDelivered {
			return fmt.Errorf("只有「已发货」的订单可以撤销，当前状态：%s", order.Status)
		}

		// 商品可能已被删除，撤销流程不能因此卡死：拿不到就按保守默认走。
		var goods models.Goods
		goodsMissing := false
		if err := tx.Select("id", "return_card_on_cancel").First(&goods, order.GoodsID).Error; err != nil {
			goodsMissing = true
		}

		// ---- 决定卡密去向 ----
		action := strings.TrimSpace(opts.CardAction)
		if action == "" {
			action = CardActionVoid // 缺省与商品默认值一致：不退回
			if !goodsMissing && goods.ReturnCardOnCancel {
				action = CardActionReturn
			}
		}
		switch action {
		case CardActionReturn, CardActionVoid, CardActionKeep:
		default:
			return fmt.Errorf("卡密去向不合法：%q（只能是 return / void / keep）", action)
		}

		// ---- 已查看保护 ----
		var d models.Delivery
		hasDelivery := false
		if err := tx.Where("order_id = ?", order.ID).First(&d).Error; err == nil {
			hasDelivery = true
		}
		warn := ""
		if hasDelivery && d.ViewCount > 0 {
			warn = fmt.Sprintf("该订单的提货链接已被查看 %d 次，卡密明文可能已经暴露给买家；退回卡密池存在重复发货风险。", d.ViewCount)
			if action == CardActionReturn && !opts.ForceView {
				return fmt.Errorf("%s 如确认可以退回，请勾选「已知悉风险，仍退回卡密池」", warn)
			}
		}

		// ---- 1) 卡密去向 ----
		var affected int64
		switch action {
		case CardActionReturn:
			if err := s.cards.ReleaseByOrder(ctx, order.ID); err != nil {
				return fmt.Errorf("退回卡密失败：%w", err)
			}
			affected = int64(len(cardsOf(order)))
		case CardActionVoid:
			reason := "撤销订单"
			if strings.TrimSpace(opts.Reason) != "" {
				reason = "撤销订单：" + strings.TrimSpace(opts.Reason)
			}
			n, err := s.cards.VoidByOrder(ctx, order.ID, reason)
			if err != nil {
				return fmt.Errorf("作废卡密失败：%w", err)
			}
			affected = n
		case CardActionKeep:
			affected = int64(len(cardsOf(order)))
		}

		// ---- 2) 作废提货链接 ----
		if hasDelivery && d.RevokedAt == nil {
			reason := strings.TrimSpace(opts.Reason)
			if reason == "" {
				reason = "订单已撤销"
			}
			if err := tx.Model(&models.Delivery{}).Where("id = ?", d.ID).
				Updates(map[string]any{
					"revoked_at":    time.Now(),
					"revoked_by":    opts.UserID,
					"revoke_reason": truncateStr("订单撤销："+reason, 255),
					"status":        "revoked",
					"updated_at":    time.Now(),
				}).Error; err != nil {
				return fmt.Errorf("作废提货链接失败：%w", err)
			}
		}

		// ---- 3) 订单落状态 ----
		note := "后台撤销订单"
		switch action {
		case CardActionReturn:
			note += "：卡密已退回卡密池"
		case CardActionVoid:
			note += "：卡密已作废（未退回）"
		case CardActionKeep:
			note += "：卡密保持已售（未退回也未作废）"
		}
		note += "、投递链接作废"
		if r := strings.TrimSpace(opts.Reason); r != "" {
			note += "；原因：" + r
		}

		updates := map[string]any{
			"status":     models.OrderStatusCancelled,
			"error_msg":  truncateStr(note, 500),
			"updated_at": time.Now(),
		}
		// 卡密已经不在这一单名下了，快照必须清掉，否则对账会误导
		if action != CardActionKeep {
			updates["cards"] = nil
		}
		if err := tx.Model(&models.Order{}).Where("id = ?", order.ID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("更新订单状态失败：%w", err)
		}

		order.Status = models.OrderStatusCancelled
		order.ErrorMsg = note
		if action != CardActionKeep {
			order.Cards = nil
		}

		out = &CancelResult{
			Order:      &order,
			CardAction: action,
			CardCount:  affected,
			ViewCount:  d.ViewCount,
			Warn:       warn,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	slog.Info("订单已撤销",
		"order_no", out.Order.OrderNo, "card_action", out.CardAction,
		"cards", out.CardCount, "views", out.ViewCount, "force_view", opts.ForceView)
	return out, nil
}

// cardsOf 从订单的 cards 快照里取出卡密列表（只用于计数，不参与业务判断）
func cardsOf(order models.Order) []platform.Card {
	if len(order.Cards) == 0 {
		return nil
	}
	var cs []platform.Card
	if err := json.Unmarshal(order.Cards, &cs); err != nil {
		return nil
	}
	return cs
}
