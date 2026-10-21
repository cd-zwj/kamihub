package api

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

func nowTime() time.Time { return time.Now() }

func (h *Handlers) getSettings(c *gin.Context) {
	all, err := h.settings.GetAll(c.Request.Context())
	if err != nil {
		ServerError(c, "读取设置失败")
		return
	}
	OK(c, gin.H{
		"settings": all,
		// 附带解析后的类型化视图，前端表单可直接用
		"contact":       h.settings.Contact(c.Request.Context()),
		"brand":         h.settings.Brand(c.Request.Context()),
		"delivery_page": h.settings.DeliveryPage(c.Request.Context()),
		"security":      h.settings.Security(c.Request.Context()),
	})
}

func (h *Handlers) updateSetting(c *gin.Context) {
	key := c.Param("key")
	var raw json.RawMessage
	if err := c.ShouldBindJSON(&raw); err != nil {
		BadRequest(c, "设置内容不是合法 JSON")
		return
	}

	// 改之前先取出旧值 —— 差量审计的全部意义就在这个 before 上。
	var before map[string]any
	if all, err := h.settings.GetAll(c.Request.Context()); err == nil {
		if old, ok := all[key]; ok {
			before = service.SnapshotJSON(old)
		}
	}

	if err := h.settings.Update(c.Request.Context(), key, raw); err != nil {
		Abort(c, err)
		return
	}
	// 记录「改了哪些字段、从什么改成什么」。
	// 像 security / payment 这两类设置改错会直接漏钱，
	// 只记「有人改了设置」等于没记。
	h.auditChange(c, "settings.update", "setting", key, before, service.SnapshotJSON(raw))
	OK(c, gin.H{"message": "已保存"})
}

// dashboardStats 概览数据
func (h *Handlers) dashboardStats(c *gin.Context) {
	ctx := c.Request.Context()
	db := h.db.WithContext(ctx)

	todayStart := time.Now().Truncate(0)
	now := time.Now()
	todayStart = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	type statusCount struct {
		Status string `json:"status"`
		Count  int64  `json:"count"`
	}
	var byStatus []statusCount
	db.Model(&models.Order{}).Select("status, COUNT(*) as count").Group("status").Scan(&byStatus)

	statusMap := map[string]int64{}
	var totalOrders int64
	for _, s := range byStatus {
		statusMap[s.Status] = s.Count
		totalOrders += s.Count
	}

	var todayOrders, todayDelivered int64
	db.Model(&models.Order{}).Where("created_at >= ?", todayStart).Count(&todayOrders)
	db.Model(&models.Order{}).Where("created_at >= ? AND status = ?", todayStart, models.OrderStatusDelivered).Count(&todayDelivered)

	var todayAmount float64
	db.Model(&models.Order{}).
		Where("created_at >= ? AND status IN ?", todayStart,
			[]string{models.OrderStatusDelivered, models.OrderStatusPurchasing}).
		Select("COALESCE(SUM(total_amount), 0)").Scan(&todayAmount)

	var platformTotal, platformEnabled, platformError int64
	db.Model(&models.Platform{}).Count(&platformTotal)
	db.Model(&models.Platform{}).Where("enabled = ?", true).Count(&platformEnabled)
	db.Model(&models.Platform{}).Where("last_check_ok = ?", false).Count(&platformError)

	var goodsTotal, goodsOn int64
	db.Model(&models.Goods{}).Count(&goodsTotal)
	db.Model(&models.Goods{}).Where("status = ?", models.GoodsStatusOn).Count(&goodsOn)

	var deliveryActive, deliveryExpired int64
	db.Model(&models.Delivery{}).Where("status = ? AND expires_at >= ?", models.DeliveryStatusActive, now).Count(&deliveryActive)
	db.Model(&models.Delivery{}).Where("expires_at < ?", now).Count(&deliveryExpired)

	// 近 7 天趋势
	type trendRow struct {
		Day       string `json:"day"`
		Total     int64  `json:"total"`
		Delivered int64  `json:"delivered"`
	}
	// 必须初始化为空切片而不是 nil：
	// Go 的 nil 切片会被 encoding/json 序列化成 null，
	// 前端拿到 null 后调 .length 会直接抛 TypeError 并整页白屏。
	trend := make([]trendRow, 0, 7)
	db.Raw(`
		SELECT to_char(created_at, 'YYYY-MM-DD') AS day,
		       COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE status = 'delivered') AS delivered
		FROM orders
		WHERE created_at >= ?
		GROUP BY day ORDER BY day ASC
	`, now.AddDate(0, 0, -6)).Scan(&trend)

	// 各平台订单分布
	type platformRow struct {
		PlatformID uint   `json:"platform_id"`
		Name       string `json:"name"`
		Total      int64  `json:"total"`
		Delivered  int64  `json:"delivered"`
	}
	platformStats := make([]platformRow, 0)
	db.Raw(`
		SELECT p.id AS platform_id, p.name AS name,
		       COUNT(o.id) AS total,
		       COUNT(o.id) FILTER (WHERE o.status = 'delivered') AS delivered
		FROM platforms p
		LEFT JOIN orders o ON o.platform_id = p.id
		GROUP BY p.id, p.name
		ORDER BY total DESC
	`).Scan(&platformStats)

	security := h.settings.Security(ctx)
	usedToday := todayAmount
	var remaining float64
	if security.MaxDailyAmount > 0 {
		remaining = security.MaxDailyAmount - usedToday
		if remaining < 0 {
			remaining = 0
		}
	}

	OK(c, gin.H{
		"orders": gin.H{
			"total":           totalOrders,
			"today":           todayOrders,
			"today_delivered": todayDelivered,
			"by_status":       statusMap,
		},
		"amount": gin.H{
			"today":            usedToday,
			"daily_limit":      security.MaxDailyAmount,
			"daily_remaining":  remaining,
		},
		"platforms": gin.H{
			"total":   platformTotal,
			"enabled": platformEnabled,
			"error":   platformError,
		},
		"goods": gin.H{
			"total": goodsTotal,
			"on":    goodsOn,
		},
		"deliveries": gin.H{
			"active":  deliveryActive,
			"expired": deliveryExpired,
		},
		"trend":          trend,
		"platform_stats": platformStats,
	})
}

func (h *Handlers) listCallLogs(c *gin.Context) {
	page, size := Pagination(c)
	platformID := uint(intQuery(c, "platform_id", 0))
	onlyFailed := c.Query("only_failed") == "true"

	list, total, err := h.audit.ListCallLogs(c.Request.Context(), page, size, platformID, onlyFailed)
	if err != nil {
		ServerError(c, "查询失败")
		return
	}
	OKPage(c, list, total, page, size)
}

func (h *Handlers) listAuditLogs(c *gin.Context) {
	page, size := Pagination(c)
	list, total, err := h.audit.List(c.Request.Context(), page, size, c.Query("action"))
	if err != nil {
		ServerError(c, "查询失败")
		return
	}
	OKPage(c, list, total, page, size)
}
