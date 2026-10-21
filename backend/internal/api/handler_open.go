package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"kamihub/internal/service"
)

// ================================================================
// 开放接口（下游签名调用）
//
// 挂载在 /api/open 下，用 openAPIAuth（HMAC 签名）鉴权。
// 与 /api/shop（买家登录令牌）是两套独立入口，互不影响。
//
// 这里刻意不直接返回内部错误原文：下游拿到的应该是
// 「能指导他怎么改」的信息，而不是我们的表名和堆栈。
// ================================================================

// ---------------------------------------------------------------- 商品列表

// openListGoods 查询可调货的商品。
//
// 下游需要一个「先看看有什么、什么价」的接口，
// 否则每次下单都要试错，体验和上游压力都很差。
func (h *Handlers) openListGoods(c *gin.Context) {
	keyword := openString(c, "keyword")
	page := openInt(c, "page")
	size := openInt(c, "page_size")
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 50
	}
	if size > 200 {
		size = 200
	}

	list, total, err := h.listings.ListForShop(c.Request.Context(), service.ShopListingFilter{
		Keyword: keyword,
		Page:    page,
		Size:    size,
	})
	if err != nil {
		ServerError(c, "查询商品失败")
		return
	}

	// 只暴露下游下单真正需要的字段：商品 id（下单时用这个）、
	// 标题、售价、库存、起购量。上游成本、货源信息一概不出去。
	type openGoods struct {
		ListingID uint   `json:"listing_id"`
		Title     string `json:"title"`
		SalePrice string `json:"sale_price"`
		Stock     int64  `json:"stock"`
		MinQty    int    `json:"min_qty"`
		MaxQty    int    `json:"max_qty"`
		SoldCount int64  `json:"sold_count"`
	}
	out := make([]openGoods, 0, len(list))
	for i := range list {
		it := list[i]
		out = append(out, openGoods{
			ListingID: it.ID,
			Title:     it.Title,
			SalePrice: it.SalePrice.String(),
			Stock:     it.Stock,
			MinQty:    it.MinQty,
			MaxQty:    it.MaxQty,
			SoldCount: it.SoldCount,
		})
	}
	OK(c, gin.H{"list": out, "total": total, "page": page, "page_size": size})
}

// ---------------------------------------------------------------- 下单（调货）

// openPurchase 下游下单。
//
// 语义与买家在页面上点「立即购买」完全一致：
// 扣余额 → 走同一套采购/发卡/生成提货链接链路 → 同步返回结果。
// 复用 shopOrders.Purchase 而不是另写一套，是为了让下游拿到的
// 可靠性和站内下单完全一样（路由、故障转移、自动退款都在里面）。
func (h *Handlers) openPurchase(c *gin.Context) {
	buyer := openBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先完成签名鉴权")
		return
	}

	listingID := openUint(c, "listing_id")
	if listingID == 0 {
		BadRequest(c, "缺少 listing_id（先用 /api/open/goods 查询可调货商品）")
		return
	}
	quantity := openInt(c, "quantity")
	if quantity <= 0 {
		quantity = 1
	}
	if quantity > 1000 {
		BadRequest(c, "单次购买数量不能超过 1000")
		return
	}
	requestID := openString(c, "request_id")
	if requestID == "" {
		// 没有幂等键就失去重试安全性，直接拒绝比默默接受更负责
		BadRequest(c, "缺少 request_id（幂等键，重试请复用同一个值）")
		return
	}
	if len(requestID) > 100 {
		BadRequest(c, "request_id 过长")
		return
	}

	res, err := h.shopOrders.Purchase(c.Request.Context(), service.ShopPurchaseInput{
		BuyerID:   buyer.ID,
		ListingID: listingID,
		Quantity:  quantity,
		RequestID: "open:" + requestID,
		ClientIP:  c.ClientIP(),
	})
	if err != nil {
		h.abortOpenPurchase(c, err)
		return
	}

	OK(c, gin.H{
		"order_no":     res.OrderNo,
		"order_id":     res.OrderID,
		"quantity":     res.Quantity,
		"unit_price":   res.UnitPrice.String(),
		"paid_amount":  res.PaidAmount.String(),
		"status":       res.Status,
		"delivery_url": h.absoluteDeliveryURL(res.DeliveryURL),
		"content":      res.Content,
		"cards":        res.Cards,
		"instruction":  res.Instruction,
		"balance_after": res.BalanceAfter.String(),
	})
}

// abortOpenPurchase 把下单错误映射成下游看得懂的状态码。
//
// 与站内下单的映射保持一致，但文案更偏向「接口调用方」：
// 例如余额不足会明确告诉下游该去充值，而不是让他猜。
func (h *Handlers) abortOpenPurchase(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInsufficientFunds):
		Fail(c, 402, "INSUFFICIENT_FUNDS", "账户余额不足，请先充值后再调用")
	case errors.Is(err, service.ErrListingNotFound):
		NotFound(c, "商品不存在或已下架，请用 /api/open/goods 重新获取")
	case errors.Is(err, service.ErrListingNoStock):
		Conflict(c, "该商品库存不足")
	case errors.Is(err, service.ErrListingQtyInvalid):
		BadRequest(c, err.Error())
	case errors.Is(err, service.ErrGoodsNotFound):
		NotFound(c, "商品已下架")
	case errors.Is(err, service.ErrBuyerNotFound):
		Unauthorized(c, "账号不可用")
	default:
		BadRequest(c, err.Error())
	}
}

// ---------------------------------------------------------------- 查询订单

// openGetOrder 按订单号或订单 id 查询订单（强制限定为当前密钥所属买家）。
func (h *Handlers) openGetOrder(c *gin.Context) {
	buyer := openBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先完成签名鉴权")
		return
	}

	orderNo := openString(c, "order_no")
	orderID := openUint(c, "order_id")
	if orderNo == "" && orderID == 0 {
		BadRequest(c, "请提供 order_no 或 order_id")
		return
	}

	var view *service.ShopOrderView
	var err error
	if orderID > 0 {
		view, err = h.shopOrders.GetBuyerOrder(c.Request.Context(), buyer.ID, orderID)
	} else {
		view, err = h.shopOrders.GetBuyerOrderByNo(c.Request.Context(), buyer.ID, orderNo)
	}
	if err != nil {
		NotFound(c, err.Error())
		return
	}

	OK(c, gin.H{
		"order_no":     view.OrderNo,
		"order_id":     view.ID,
		"quantity":     view.Quantity,
		"unit_price":   view.UnitPrice.String(),
		"paid_amount":  view.PaidAmount.String(),
		"status":       view.Status,
		"status_text":  view.StatusText,
		"delivery_url": h.absoluteDeliveryURL(view.DeliveryURL),
		"content":      view.Content,
		"cards":        view.Cards,
		"instruction":  view.Instruction,
		"created_at":   view.CreatedAt,
		"delivered_at": view.DeliveredAt,
	})
}

// ---------------------------------------------------------------- 账户余额

// openBalance 查询账户余额（下游的「额度」）。
func (h *Handlers) openBalance(c *gin.Context) {
	buyer := openBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先完成签名鉴权")
		return
	}
	sum, err := h.wallet.Summary(c.Request.Context(), buyer.ID)
	if err != nil {
		ServerError(c, "查询余额失败")
		return
	}
	OK(c, gin.H{
		"balance":         sum.Balance.String(),
		"frozen":          sum.Frozen.String(),
		"total_recharged": sum.TotalRecharged.String(),
		"total_spent":     sum.TotalSpent.String(),
	})
}

// absoluteDeliveryURL 把站内的相对提货链接补成完整地址。
//
// 下游拿到链接是要直接转发给最终买家的，相对路径对他们没用。
func (h *Handlers) absoluteDeliveryURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	base := strings.TrimRight(h.cfg.App.BaseURL, "/")
	if base == "" {
		return u
	}
	return base + "/" + strings.TrimLeft(u, "/")
}
