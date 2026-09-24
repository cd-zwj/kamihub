package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

func (h *Handlers) listOrders(c *gin.Context) {
	page, size := Pagination(c)

	q := h.db.Model(&models.Order{})

	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("status = ?", status)
	}
	if goodsID := c.Query("goods_id"); goodsID != "" {
		q = q.Where("goods_id = ?", goodsID)
	}
	if platformID := c.Query("platform_id"); platformID != "" {
		q = q.Where("platform_id = ?", platformID)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("order_no ILIKE ? OR request_id ILIKE ? OR platform_trade_no ILIKE ?", like, like, like)
	}
	if start := c.Query("start_time"); start != "" {
		q = q.Where("created_at >= to_timestamp(?)", start)
	}
	if end := c.Query("end_time"); end != "" {
		q = q.Where("created_at <= to_timestamp(?)", end)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}

	var orders []models.Order
	if err := q.Preload("Goods").Preload("Platform").Preload("Delivery").
		Order("id DESC").Offset((page - 1) * size).Limit(size).
		Find(&orders).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}

	// 列表里不回传卡密明文，避免列表接口成为卡密泄露面。
	//
	// 注意：models.Order 内嵌进来后，它的 Cards 字段（json:"cards"）会被一起序列化，
	// 光靠"不回传"这个注释是拦不住的，必须在序列化前显式清空。
	// 卡密明文只允许出现在订单详情接口（getOrder）里。
	type orderBrief struct {
		models.Order
		CardCount int `json:"card_count"`
	}
	out := make([]orderBrief, 0, len(orders))
	for _, o := range orders {
		brief := o
		brief.Cards = nil
		out = append(out, orderBrief{Order: brief, CardCount: countCards(o)})
	}
	OKPage(c, out, total, page, size)
}

func (h *Handlers) getOrder(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var order models.Order
	if err := h.db.Preload("Goods").Preload("Platform").Preload("Delivery").
		First(&order, id).Error; err != nil {
		NotFound(c, "订单不存在")
		return
	}

	deliveryURL := ""
	if order.Delivery != nil {
		deliveryURL = h.deliveries.URL(order.Delivery.Token)
	}
	OK(c, gin.H{
		"order":        order,
		"cards":        cardList(order),
		"delivery_url": deliveryURL,
	})
}

func (h *Handlers) listOrderAttempts(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	// 无尝试记录时保持空切片，避免序列化成 null 让前端 .length 抛错
	attempts := make([]models.OrderAttempt, 0)
	if err := h.db.Where("order_id = ?", id).Order("id ASC").Find(&attempts).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	OK(c, gin.H{"list": attempts})
}

// createOrder 通过 API 触发一次采购。
// 这是「用 API 方式把购买的内容发出去」的入口：调用方拿到订单详情与提货链接。
func (h *Handlers) createOrder(c *gin.Context) {
	var req struct {
		RequestID string `json:"request_id" binding:"required"`
		GoodsCode string `json:"goods_code" binding:"required"`
		Quantity  int    `json:"quantity"`
		Remark    string `json:"remark"`
		TTLHours  int    `json:"ttl_hours"`
		// ResponseMode 控制「成功响应」的形态，专为对接方设计：
		// 有些发卡系统只能把响应体整段取走，无法按 JSON 路径提取字段，
		// 拿到完整 JSON 就没法直接给买家。这些系统可以把本字段设成
		//   url    -> 只返回提货链接（纯文本）
		//   cards  -> 只返回卡密（纯文本，多张时每行一个）
		//   full   -> 完整 JSON（默认，保持向后兼容）
		// 失败时一律仍返回标准的 JSON 错误信封 + 对应 HTTP 状态码，
		// 对接方按 HTTP 状态码判断成败即可。
		ResponseMode string `json:"response_mode"`
	}
	if !BindJSON(c, &req) {
		return
	}

	user := CurrentUser(c)
	order, err := h.orders.Create(c.Request.Context(), service.CreateOrderInput{
		RequestID: req.RequestID,
		GoodsCode: req.GoodsCode,
		Quantity:  req.Quantity,
		Remark:    req.Remark,
		TTLHours:  req.TTLHours,
		UserID:    &user.ID,
	})
	if err != nil {
		Abort(c, err)
		return
	}

	deliveryURL := ""
	deliveryToken := ""
	if order.Delivery != nil {
		deliveryURL = h.deliveries.URL(order.Delivery.Token)
		deliveryToken = order.Delivery.Token
	}
	cards := cardList(*order)

	h.auditLog(c, "order.create", "order", order.OrderNo, gin.H{
		"status": order.Status, "goods": req.GoodsCode, "quantity": order.Quantity,
	})

	// 纯文本模式：直接把对接方要的那一个值作为响应体返回
	switch strings.ToLower(strings.TrimSpace(req.ResponseMode)) {
	case "url", "link", "text":
		c.String(http.StatusOK, deliveryURL)
		return
	case "cards", "card", "cardno":
		c.String(http.StatusOK, strings.Join(cards, "\n"))
		return
	}

	OK(c, gin.H{
		"order":          order,
		"cards":          cards,
		"delivery_url":   deliveryURL,
		"delivery_token": deliveryToken,
	})
}

func (h *Handlers) retryOrder(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	order, err := h.orders.RetryQuery(c.Request.Context(), id)
	if err != nil {
		Abort(c, err)
		return
	}
	deliveryURL := ""
	if order.Delivery != nil {
		deliveryURL = h.deliveries.URL(order.Delivery.Token)
	}
	h.auditLog(c, "order.retry", "order", order.OrderNo, nil)
	OK(c, gin.H{"order": order, "delivery_url": deliveryURL, "cards": cardList(*order)})
}

// countCards 统计订单卡密张数（不返回明文）
func countCards(o models.Order) int {
	if len(o.Cards) == 0 {
		return 0
	}
	var cards []struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(o.Cards, &cards); err != nil {
		return 0
	}
	n := 0
	for _, c := range cards {
		if c.Code != "" {
			n++
		}
	}
	return n
}

// cardList 提取卡密明文（仅详情接口使用）
func cardList(o models.Order) []string {
	out := []string{}
	if len(o.Cards) == 0 {
		return out
	}
	var cards []struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(o.Cards, &cards); err != nil {
		return out
	}
	for _, c := range cards {
		if c.Code != "" {
			out = append(out, c.Code)
		}
	}
	return out
}
