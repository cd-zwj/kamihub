package api

import (
	"strings"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
)

func (h *Handlers) listDeliveries(c *gin.Context) {
	page, size := Pagination(c)

	q := h.db.Model(&models.Delivery{})

	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("status = ?", status)
	}
	if v := c.Query("expired"); v == "true" {
		q = q.Where("expires_at < NOW()")
	} else if v == "false" {
		q = q.Where("expires_at >= NOW()")
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		// 支持按订单号反查
		q = q.Where("order_id IN (SELECT id FROM orders WHERE order_no ILIKE ?)", "%"+kw+"%")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}

	var rows []models.Delivery
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}

	// 组装订单信息，前端列表要显示「哪个订单的链接」
	type deliveryView struct {
		models.Delivery
		URL       string `json:"url"`
		Expired   bool   `json:"expired"`
		OrderNo   string `json:"order_no"`
		GoodsName string `json:"goods_name"`
	}
	out := make([]deliveryView, 0, len(rows))
	orderIDs := make([]uint, 0, len(rows))
	for _, r := range rows {
		orderIDs = append(orderIDs, r.OrderID)
	}
	orderMap := map[uint]models.Order{}
	if len(orderIDs) > 0 {
		var orders []models.Order
		h.db.Preload("Goods").Where("id IN ?", orderIDs).Find(&orders)
		for _, o := range orders {
			orderMap[o.ID] = o
		}
	}
	for _, r := range rows {
		v := deliveryView{
			Delivery: r,
			URL:      h.deliveries.URL(r.Token),
			Expired:  r.IsExpired(nowTime()),
		}
		if o, ok := orderMap[r.OrderID]; ok {
			v.OrderNo = o.OrderNo
			if o.Goods != nil {
				v.GoodsName = o.Goods.Name
			}
		}
		out = append(out, v)
	}
	OKPage(c, out, total, page, size)
}

func (h *Handlers) getDelivery(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var d models.Delivery
	if err := h.db.First(&d, id).Error; err != nil {
		NotFound(c, "提货链接不存在")
		return
	}
	var order models.Order
	h.db.Preload("Goods").Preload("Platform").First(&order, d.OrderID)

	OK(c, gin.H{
		"delivery":     d,
		"url":          h.deliveries.URL(d.Token),
		"expired":      d.IsExpired(nowTime()),
		"order":        order,
		"cards":        cardList(order),
		"has_password": d.PasswordHash != "",
	})
}

func (h *Handlers) revokeDelivery(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req)

	user := CurrentUser(c)
	if err := h.deliveries.Revoke(c.Request.Context(), id, &user.ID, req.Reason); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "delivery.revoke", "delivery", itoa(int64(id)), gin.H{"reason": req.Reason})
	OK(c, gin.H{"message": "已作废"})
}

func (h *Handlers) extendDelivery(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Hours int `json:"hours" binding:"required"`
	}
	if !BindJSON(c, &req) {
		return
	}
	d, err := h.deliveries.Extend(c.Request.Context(), id, req.Hours)
	if err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "delivery.extend", "delivery", itoa(int64(id)), gin.H{"hours": req.Hours})
	OK(c, gin.H{"delivery": d, "url": h.deliveries.URL(d.Token)})
}

func (h *Handlers) setDeliveryPassword(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	_ = c.ShouldBindJSON(&req)

	if err := h.deliveries.SetPassword(c.Request.Context(), id, strings.TrimSpace(req.Password)); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "delivery.set_password", "delivery", itoa(int64(id)), nil)
	OK(c, gin.H{"message": "已更新访问密码"})
}

func (h *Handlers) listDeliveryViews(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	page, size := Pagination(c)

	var total int64
	h.db.Model(&models.DeliveryView{}).Where("delivery_id = ?", id).Count(&total)

	var views []models.DeliveryView
	if err := h.db.Where("delivery_id = ?", id).
		Order("id DESC").Offset((page - 1) * size).Limit(size).
		Find(&views).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	OKPage(c, views, total, page, size)
}
