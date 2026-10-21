package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
		// 这三个字段用 flexString / flexInt 而不是原生类型。
		//
		// 原因是被实际对接踩出来的：发卡系统 / 中间层拼 JSON 时，
		// request_id 常常是裸数字（{"request_id": 12345}），quantity 有时是
		// 字符串 "1"、有时是 ""。原生 string / int 遇到这些一律
		// "cannot unmarshal ... into Go struct field"，而调用方拿到的
		// 只有一句「请求参数不合法」，根本对不上是自己哪个字段写错了。
		// 兼容掉这些常见形态，比强迫每个对接方都改代码更划算。
		RequestID string    `json:"request_id" binding:"required"`
		GoodsCode string    `json:"goods_code" binding:"required"`
		Quantity  flexInt   `json:"quantity"`
		Remark    string    `json:"remark"`
		TTLHours  flexInt   `json:"ttl_hours"`
		// ResponseMode 控制「成功响应」的形态，专为对接方设计：
		//
		//   json   -> 仍是 JSON，但把提货链接抬到 code/msg/data 一层
		//             （data.link），对接方用 JSON 路径就能取值，不必下钻
		//   url    -> 只返回提货链接（纯文本），给「整段取走」的发卡系统
		//   cards  -> 只返回卡密（纯文本，多张时每行一个）
		//   text   -> 与 url 同义；link 同理
		//   full   -> 完整 JSON（默认，保持向后兼容）
		//
		// 之所以要新增 json：第三方发卡平台（如 EZAPI）多数只能配
		// 「JSON 路径」而不能整段取走响应体。它们拿 url 模式的 text/plain
		// 一行文本去解 JSON 会直接失败，表现出来就是「下单成功但没返回东西」。
		// 失败时一律仍返回标准的 JSON 错误信封 + 对应 HTTP 状态码，
		// 对接方按 HTTP 状态码判断成败即可。
		ResponseMode string `json:"response_mode"`
	}
	if !BindJSON(c, &req) {
		return
	}

	user := CurrentUser(c)
	order, err := h.orders.Create(c.Request.Context(), service.CreateOrderInput{
		RequestID: strings.TrimSpace(req.RequestID),
		GoodsCode: req.GoodsCode,
		Quantity:  int(req.Quantity),
		Remark:    req.Remark,
		TTLHours:  int(req.TTLHours),
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

	switch strings.ToLower(strings.TrimSpace(req.ResponseMode)) {
	case "url", "link", "text":
		// 纯文本模式：直接把提货链接作为响应体返回。
		// 适合能把响应体整段取走的发卡系统 —— 拿到什么就发给买家什么。
		//
		// 订单没发货成功时这里绝不能回 200 + 空字符串：第三方发卡平台拿到
		// 一个空的 200，只会把它当成「卡密是空白的」发给买家，客服那边
		// 收到的投诉是「下单成功但没返回东西」，而真实原因是订单失败了。
		// 失败就按失败返回：错误信封 + 对应 HTTP 状态码，让上游把这一单挂起。
		if order.Status != models.OrderStatusDelivered {
			Abort(c, failedOrderError(order))
			return
		}
		c.String(http.StatusOK, deliveryURL)
		return
	case "cards", "card", "cardno":
		// 纯文本模式：只返回卡密，多张时每行一个。
		// 同上：订单失败时不能回空 200。
		if order.Status != models.OrderStatusDelivered {
			Abort(c, failedOrderError(order))
			return
		}
		c.String(http.StatusOK, strings.Join(cards, "\n"))
		return
	case "json":
		// 仍然是 JSON，但把调用方真正要的东西抬到顶层：
		//   link      提货链接（等价于 full 的 data.delivery_url）
		//   cards     卡密明文数组
		//   order_no  订单号，便于对账
		// 对接方只要配一个 JSON 路径（例如 data.link）就能取值，
		// 不必再下钻 data.order.xxx。
		//
		// 同时保留 code/msg/time 三个公共字段，与其它接口的错误信封同构，
		// 对接方用同一套判断逻辑就能区分成败。
		c.JSON(http.StatusOK, gin.H{
			"code": 1,
			"msg":  "success",
			"time": time.Now().Unix(),
			"data": gin.H{
				"link":           deliveryURL,
				"cards":          cards,
				"order_no":       order.OrderNo,
				"delivery_token": deliveryToken,
				"quantity":       order.Quantity,
			},
		})
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

// failedOrderError 把「订单落库了但没发货成功」翻译成一个 API 错误。
//
// 采购接口对失败订单的处理是「返回订单对象 + HTTP 200」，这在 full 模式下
// 没问题（调用方能读 status 字段），但对 url / cards 这种纯文本模式是致命的：
// 响应体只能是链接或卡密，订单失败时它就是个空字符串，调用方无从分辨。
// 所以这些模式下一律转成错误信封。
func failedOrderError(order *models.Order) error {
	msg := strings.TrimSpace(order.ErrorMsg)
	if msg == "" {
		msg = "订单未发货成功"
	}
	return fmt.Errorf("订单 %s 发货失败：%s", order.OrderNo, msg)
}

// flexInt 是一个「宽容的整数」：JSON 里写 1、"1"、"" 、null 都能收。
//
// 为什么不用原生 int：发卡系统模板替换出来的值经常是字符串，
// 空模板变量则变成 ""。原生 int 遇到这两种直接解析失败，
// 调用方只看到「请求参数不合法」，排查成本极高。
// "" 和 null 一律当成「没传」，由后续逻辑取默认值。
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	// 去掉可能存在的引号
	s = strings.Trim(s, `"`)
	if s == "" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		// 兼容 "1.0" 这种浮点写法
		fl, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return fmt.Errorf("数量必须是整数，收到的是 %q", s)
		}
		n = int(fl)
	}
	*f = flexInt(n)
	return nil
}

// cancelOrder 撤销一笔已发货的订单：决定卡密去向 + 作废提货链接 + 订单转 cancelled。
//
// 权限：管理员。撤销涉及卡密库存与营收，操作员不应有。
func (h *Handlers) cancelOrder(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Reason     string `json:"reason"`
		CardAction string `json:"card_action"` // return / void / keep，留空用商品默认
		ForceView  bool   `json:"force_view"`  // 已查看过时是否仍强行退回
	}
	// body 可以整个为空（只撤销、用全部默认值），所以这里不用 BindJSON 强校验
	if c.Request.ContentLength > 0 {
		_ = c.ShouldBindJSON(&req)
	}

	user := CurrentUser(c)
	res, err := h.orders.Cancel(c.Request.Context(), id, service.CancelOptions{
		Reason:     req.Reason,
		CardAction: req.CardAction,
		ForceView:  req.ForceView,
		UserID:     &user.ID,
	})
	if err != nil {
		Abort(c, err)
		return
	}

	h.auditLog(c, "order.cancel", "order", res.Order.OrderNo, gin.H{
		"card_action": res.CardAction,
		"card_count":  res.CardCount,
		"view_count":  res.ViewCount,
		"force_view":  req.ForceView,
		"reason":      req.Reason,
	})

	OK(c, gin.H{
		"order":       res.Order,
		"card_action": res.CardAction,
		"card_count":  res.CardCount,
		"view_count":  res.ViewCount,
		"warn":        res.Warn,
	})
}
