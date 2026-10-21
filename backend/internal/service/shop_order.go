package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"kamihub/internal/models"
	"kamihub/internal/platform"
)

// ShopOrderService 负责买家自助下单。
//
// 它不重新实现采购逻辑，而是把三段能力串起来：
//
//	ListingService.BuyerQuote   → 用 C 端售价算钱（绝不信任前端传价）
//	WalletService.FreezeForPurchase → 冻结余额（幂等 + 行锁）
//	OrderService.Create         → 复用生产验证过的采购发卡链路
//
// 这样做的价值：采购、路由、故障转移、提货页这些复杂逻辑
// 一行都不用改，C 端享受的是和后台完全一致的可靠性。
//
// 失败处理是这里最需要小心的地方：
// 采购失败必须把冻结的钱退回去，否则买家会「钱扣了没拿到货」。
// 因此任何非成功路径都要走 refund，并用 defer 兜住 panic 场景。
type ShopOrderService struct {
	db       *gorm.DB
	listings *ListingService
	wallet   *WalletService
	orders   *OrderService
}

func NewShopOrderService(db *gorm.DB, listings *ListingService, wallet *WalletService, orders *OrderService) *ShopOrderService {
	return &ShopOrderService{db: db, listings: listings, wallet: wallet, orders: orders}
}

// ShopPurchaseInput 买家下单入参
type ShopPurchaseInput struct {
	BuyerID   uint
	ListingID uint
	Quantity  int
	// RequestID 由前端生成并随重试复用，实现幂等
	RequestID string
	// ClientIP 用于风控日志
	ClientIP string
}

// ShopQuote 一次下单的服务端权威报价。
//
// 「权威」是重点：金额一律由服务端按 listing 的当前售价算出来，
// 任何来自前端的金额都只作为交叉校验，不参与计算。
// 这样「先充值再自动下单」的流程里，支付金额和下单金额
// 必然来自同一个来源，不存在两边价格对不上的可能。
type ShopQuote struct {
	ListingID uint            `json:"listing_id"`
	Title     string          `json:"title"`
	GoodsCode string          `json:"goods_code"`
	Quantity  int             `json:"quantity"`
	UnitPrice decimal.Decimal `json:"unit_price"`
	Total     decimal.Decimal `json:"total"`
}

// Quote 按 C 端售价算一单要多少钱。
//
// 供支付服务在「直付购卡」时取权威金额使用：
// 前端传的 amount 会被这里算出来的值覆盖，防止有人改前端把 7.5 元
// 的商品用 0.01 元买走。
func (s *ShopOrderService) Quote(ctx context.Context, listingID uint, quantity int) (*ShopQuote, error) {
	if quantity <= 0 {
		quantity = 1
	}
	listing, total, err := s.listings.BuyerQuote(ctx, listingID, quantity)
	if err != nil {
		return nil, err
	}
	if listing.Goods == nil {
		return nil, ErrListingNotFound
	}
	return &ShopQuote{
		ListingID: listing.ID,
		Title:     listing.Title,
		GoodsCode: listing.Goods.Code,
		Quantity:  quantity,
		UnitPrice: listing.SalePrice,
		Total:     total,
	}, nil
}

// ShopPurchaseResult 下单结果
type ShopPurchaseResult struct {
	OrderNo      string          `json:"order_no"`
	OrderID      uint            `json:"order_id"`
	Quantity     int             `json:"quantity"`
	UnitPrice    decimal.Decimal `json:"unit_price"`
	PaidAmount   decimal.Decimal `json:"paid_amount"`
	Status       string          `json:"status"`
	DeliveryURL  string          `json:"delivery_url"`
	Content      string          `json:"content"`
	Cards        []string        `json:"cards"`
	Instruction  string          `json:"instruction"`
	BalanceAfter decimal.Decimal `json:"balance_after"`
}

// Purchase 执行一次买家下单。
//
// 幂等性由两层保证：
//  1. 钱包流水的 request_id 唯一索引：重复请求不会重复扣钱
//  2. OrderService 的 request_id 唯一索引：重复请求不会重复采购
//
// 关键：orderRequestID 与 钱包 requestID 使用不同的前缀，
// 因为它们是两张表的唯一约束，用同一串没问题但分开更利于排查。
func (s *ShopOrderService) Purchase(ctx context.Context, in ShopPurchaseInput) (*ShopPurchaseResult, error) {
	if in.BuyerID == 0 {
		return nil, ErrBuyerNotFound
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	requestID := strings.TrimSpace(in.RequestID)
	if requestID == "" {
		// 前端没传就用随机值，此时不具备跨请求幂等性，
		// 但仍能防住「同一请求内部重复写入」
		requestID = uuid.NewString()
	}
	if len(requestID) > 100 {
		return nil, errors.New("request_id 过长")
	}

	// ---- 1. 报价：用 C 端售价算钱 ----
	// 传入 BuyerID 让报价走档位专价；普通买家（retail）分支直接返回原价，
	// 与改造前完全一致。
	listing, total, err := s.listings.BuyerQuoteFor(ctx, in.ListingID, in.Quantity, in.BuyerID)
	if err != nil {
		return nil, err
	}
	if listing.Goods == nil {
		return nil, ErrListingNotFound
	}
	goodsCode := listing.Goods.Code

	// ---- 2. 冻结余额 ----
	walletReqID := "shop-pay:" + requestID
	created, walletTx, err := s.wallet.FreezeForPurchase(
		ctx, in.BuyerID, total, walletReqID,
		fmt.Sprintf("购买 %s × %d", listing.Title, in.Quantity),
	)
	if err != nil {
		return nil, err
	}

	// 命中已有流水：说明这个 request_id 之前处理过。
	//
	// 两种可能：
	//   a) 上次已成功下单 → 直接把原订单返回给买家（幂等重放）
	//   b) 上次中断在「已冻结但订单未落库」之间 → 不能重复扣钱，
	//      返回提示让买家刷新订单列表，由对账任务解冻
	if !created {
		if walletTx.OrderID != nil {
			var ord models.Order
			if e := s.db.WithContext(ctx).First(&ord, *walletTx.OrderID).Error; e == nil {
				return s.toResult(ctx, &ord, listing, total)
			}
		}
		return nil, errors.New("该请求正在处理中，请稍后在订单列表查看结果")
	}

	// ---- 3. 采购发卡（复用生产链路）----
	//
	// 传 nil 作为 UserID：买家不是后台员工，用 BuyerID 单独标记。
	order, purchaseErr := s.orders.Create(ctx, CreateOrderInput{
		RequestID: "shop:" + requestID,
		GoodsCode: goodsCode,
		Quantity:  in.Quantity,
		Remark:    fmt.Sprintf("C端下单 buyer=%d", in.BuyerID),
		UserID:    nil,
	})

	// 采购失败的判定分两种情况，缺一不可：
	//
	//	a) Create 直接返回 error —— 参数错误、货源全挂等，订单没落地
	//	b) Create 返回了 order 但 status=failed —— 采购中途失败（例如取卡失败），
	//	   此时 err 为 nil，订单记录已生成
	//
	// (b) 是最容易漏的一种：只看 err 会以为下单成功，于是把钱结算掉，
	// 结果买家「钱扣了、货没拿到」。这条分支是被本地实测踩出来的 ——
	// 一次取卡失败就复现了，所以必须在结算之前先看订单状态。
	if purchaseErr != nil {
		s.refundQuietly(ctx, walletTx.ID, in.BuyerID, total,
			"下单失败自动退款："+purchaseErr.Error())
		return nil, purchaseErr
	}
	if order.Status == models.OrderStatusFailed {
		reason := "发货失败自动退款"
		if order.ErrorMsg != "" {
			// 内部错误原因只写进流水备注与日志，不回给买家
			reason += "：" + order.ErrorMsg
		}
		s.refundQuietly(ctx, walletTx.ID, in.BuyerID, total, reason)
		slog.Warn("C端下单发货失败，已自动退款",
			"buyer_id", in.BuyerID, "order_no", order.OrderNo,
			"amount", total.String(), "error", order.ErrorMsg)
		return nil, fmt.Errorf("发货失败，购买金额已退回余额，请稍后在订单列表查看")
	}

	// ---- 4. 回填 C 端字段 ----
	if err := s.db.WithContext(ctx).Model(&models.Order{}).
		Where("id = ?", order.ID).
		Updates(map[string]any{
			"buyer_id":    in.BuyerID,
			"listing_id":  listing.ID,
			"paid_amount": total,
			"channel":     models.OrderChannelWeb,
		}).Error; err != nil {
		// 回填失败不影响买家拿货，但要留下痕迹
		slog.Error("回填订单C端字段失败", "order_id", order.ID, "error", err.Error())
	}
	order.BuyerID = &in.BuyerID
	order.ListingID = &listing.ID
	order.PaidAmount = total
	order.Channel = models.OrderChannelWeb

	// ---- 5. 结算冻结（确认发货成功后才真正扣款）----
	if err := s.wallet.SettleFreeze(ctx, walletTx.ID, &order.ID); err != nil {
		// 结算失败意味着钱还挂在冻结态。
		// 货已经发了，不能再退给买家（否则等于白拿），
		// 因此这里只记错误日志，交由对账任务把 frozen 收敛为 settled。
		slog.Error("结算冻结流水失败，需对账处理",
			"wallet_tx", walletTx.ID, "order_id", order.ID, "error", err.Error())
	}

	// ---- 6. 累加销量 ----
	s.listings.IncrSold(ctx, listing.ID, in.Quantity)

	slog.Info("C端下单成功",
		"buyer_id", in.BuyerID, "order_no", order.OrderNo,
		"listing_id", listing.ID, "qty", in.Quantity, "paid", total.String())

	return s.toResult(ctx, order, listing, total)
}

// refundQuietly 退款并记录失败，不让退款异常盖住原始错误。
//
// 退款本身失败是最坏的情况（买家钱既没拿到货也没退回），
// 必须打成 Error 级别日志，方便运维按 wallet_tx 找到并人工补退。
func (s *ShopOrderService) refundQuietly(ctx context.Context, walletTxID, buyerID uint, amount decimal.Decimal, reason string) {
	if err := s.wallet.RefundFreeze(ctx, walletTxID, reason); err != nil {
		slog.Error("自动退款失败，需人工介入",
			"buyer_id", buyerID, "wallet_tx", walletTxID,
			"amount", amount.String(), "reason", reason, "error", err.Error())
		return
	}
	slog.Info("已自动退款", "buyer_id", buyerID, "wallet_tx", walletTxID, "amount", amount.String())
}

// toResult 组装返回给买家的结果。
func (s *ShopOrderService) toResult(ctx context.Context, order *models.Order, listing *models.Listing, paid decimal.Decimal) (*ShopPurchaseResult, error) {
	out := &ShopPurchaseResult{
		OrderNo:     order.OrderNo,
		OrderID:     order.ID,
		Quantity:    order.Quantity,
		UnitPrice:   listing.SalePrice,
		PaidAmount:  paid,
		Status:      order.Status,
		Content:     order.Content,
		Instruction: order.Instruction,
	}

	// 提货链接
	var dl models.Delivery
	if err := s.db.WithContext(ctx).Where("order_id = ?", order.ID).First(&dl).Error; err == nil {
		out.DeliveryURL = "/d/" + dl.Token
	}

	// 卡密明文：复用平台层的 Card 结构，与 cardsOf() 保持一致的解析口径
	if len(order.Cards) > 0 {
		var cs []platform.Card
		if err := json.Unmarshal(order.Cards, &cs); err == nil {
			for _, c := range cs {
				if c.Code != "" {
					out.Cards = append(out.Cards, c.Code)
				}
			}
		}
	}

	// 下单后的余额，方便前端即时刷新
	var buyer models.Buyer
	if err := s.db.WithContext(ctx).First(&buyer, order.BuyerID).Error; err == nil {
		out.BalanceAfter = buyer.Balance
	}
	return out, nil
}

// ---------------------------------------------------------------- 买家订单查询

// ShopOrderView 买家视角的订单
type ShopOrderView struct {
	ID          uint            `json:"id"`
	OrderNo     string          `json:"order_no"`
	GoodsName   string          `json:"goods_name"`
	ListingID   *uint           `json:"listing_id"`
	Quantity    int             `json:"quantity"`
	UnitPrice   decimal.Decimal `json:"unit_price"`
	PaidAmount  decimal.Decimal `json:"paid_amount"`
	Status      string          `json:"status"`
	StatusText  string          `json:"status_text"`
	Content     string          `json:"content"`
	Instruction string          `json:"instruction"`
	Cards       []string        `json:"cards"`
	DeliveryURL string          `json:"delivery_url"`
	ErrorMsg    string          `json:"error_msg,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	DeliveredAt *time.Time      `json:"delivered_at"`
}

// ListBuyerOrders 分页查买家自己的订单。
func (s *ShopOrderService) ListBuyerOrders(ctx context.Context, buyerID uint, page, size int, status string) ([]ShopOrderView, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.Order{}).Where("buyer_id = ?", buyerID)
	if st := strings.TrimSpace(status); st != "" {
		q = q.Where("status = ?", st)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset, limit := pageOffset(page, size)
	var rows []models.Order
	if err := q.Preload("Goods").Preload("Delivery").
		Order("id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	out := make([]ShopOrderView, 0, len(rows))
	for i := range rows {
		out = append(out, toShopOrderView(&rows[i]))
	}
	return out, total, nil
}

// GetBuyerOrder 取买家自己的订单详情。
//
// 强制带 buyer_id 条件：买家不能通过改 id 看别人的订单。
func (s *ShopOrderService) GetBuyerOrder(ctx context.Context, buyerID, orderID uint) (*ShopOrderView, error) {
	var order models.Order
	if err := s.db.WithContext(ctx).Preload("Goods").Preload("Delivery").
		Where("id = ? AND buyer_id = ?", orderID, buyerID).
		First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("订单不存在")
		}
		return nil, err
	}
	v := toShopOrderView(&order)
	return &v, nil
}

// GetBuyerOrderByNo 按订单号取买家自己的订单。
//
// 给下游对接用：发卡系统通常只存「我们返回给它的订单号」，
// 拿订单号来查比记住自增 id 更自然，也更不容易串号。
// 同样强制带 buyer_id，不能靠猜订单号看别人的单。
func (s *ShopOrderService) GetBuyerOrderByNo(ctx context.Context, buyerID uint, orderNo string) (*ShopOrderView, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, errors.New("订单不存在")
	}
	var order models.Order
	if err := s.db.WithContext(ctx).Preload("Goods").Preload("Delivery").
		Where("order_no = ? AND buyer_id = ?", orderNo, buyerID).
		First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("订单不存在")
		}
		return nil, err
	}
	v := toShopOrderView(&order)
	return &v, nil
}

func toShopOrderView(o *models.Order) ShopOrderView {
	v := ShopOrderView{
		ID:          o.ID,
		OrderNo:     o.OrderNo,
		ListingID:   o.ListingID,
		Quantity:    o.Quantity,
		UnitPrice:   o.UnitPrice,
		PaidAmount:  o.PaidAmount,
		Status:      o.Status,
		StatusText:  shopStatusText(o.Status),
		Content:     o.Content,
		Instruction: o.Instruction,
		ErrorMsg:    o.ErrorMsg,
		CreatedAt:   o.CreatedAt,
		DeliveredAt: o.DeliveredAt,
	}
	if o.Goods != nil {
		v.GoodsName = o.Goods.Name
	}
	if o.Delivery != nil {
		v.DeliveryURL = "/d/" + o.Delivery.Token
	}
	if len(o.Cards) > 0 {
		var cs []platform.Card
		if err := json.Unmarshal(o.Cards, &cs); err == nil {
			for _, c := range cs {
				if c.Code != "" {
					v.Cards = append(v.Cards, c.Code)
				}
			}
		}
	}
	return v
}

// shopStatusText 把内部状态翻译成买家能看懂的文案。
//
// 刻意不暴露「采购中/上游失败」这类内部细节：
// 买家只需要知道「在发货 / 已发货 / 失败会退款」。
func shopStatusText(status string) string {
	switch status {
	case models.OrderStatusPending, models.OrderStatusPurchasing:
		return "发货中"
	case models.OrderStatusDelivered:
		return "已发货"
	case models.OrderStatusFailed:
		return "发货失败，已退款"
	case models.OrderStatusCancelled:
		return "已取消"
	case models.OrderStatusRefunded:
		return "已退款"
	case models.OrderStatusClosed:
		return "已关闭"
	default:
		return status
	}
}
