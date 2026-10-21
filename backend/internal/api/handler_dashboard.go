package api

import (
	"time"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
)

// dashboardCharts 图表看板数据（迭代 4 · U3）。
//
// 为什么合成一个接口而不是 6 个：
// 看板是「一眼看全」的场景，6 个请求会让首屏出现 6 次闪烁和 6 次 loading；
// 合成一个只查一次、渲染一次。代价是接口稍大，但这个页面的数据量本来就小。
//
// 所有数据都来自真实表，没有任何 mock。空数据返回空数组（不是 null），
// 前端直接判 length 显示 Empty。
func (h *Handlers) dashboardCharts(c *gin.Context) {
	ctx := c.Request.Context()
	db := h.db.WithContext(ctx)

	// 时间窗：默认 14 天（看板一屏能放下），最多 90 天
	days := intQuery(c, "days", 14)
	if days <= 0 {
		days = 14
	}
	if days > 90 {
		days = 90
	}
	since := time.Now().AddDate(0, 0, -(days - 1))
	since = time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, since.Location())

	// ---------------- C1 每日订单量 / 销售额 / 毛利 ----------------
	//
	// 口径说明（重要）：
	//   - gmv   = Σ paid_amount（买家实付），只对 C 端订单有意义
	//   - cost  = Σ total_amount（采购成本口径，模型注释已明确）
	//   - profit = gmv − cost，只统计「两个数都有值」的 C 端订单
	// 后台手工单（channel=admin）没有买家实付，强行算毛利会得出负数，
	// 所以它们只计入订单量，不计入 gmv/profit。
	type dailyRow struct {
		Day    string  `json:"day"`
		Orders int64   `json:"orders"`
		GMV    float64 `json:"gmv"`
		Cost   float64 `json:"cost"`
		Profit float64 `json:"profit"`
	}
	var daily []dailyRow
	_ = db.Model(&models.Order{}).
		Where("created_at >= ?", since).
		Select(`to_char(created_at,'MM-DD') AS day,
		        COUNT(*) AS orders,
		        COALESCE(SUM(CASE WHEN channel <> 'admin' THEN paid_amount ELSE 0 END),0) AS gmv,
		        COALESCE(SUM(CASE WHEN channel <> 'admin' AND status = ? THEN total_amount ELSE 0 END),0) AS cost,
		        COALESCE(SUM(CASE WHEN channel <> 'admin' AND status = ? THEN paid_amount - total_amount ELSE 0 END),0) AS profit`,
			models.OrderStatusDelivered, models.OrderStatusDelivered).
		Group("day").Order("day ASC").Scan(&daily).Error
	if daily == nil {
		daily = []dailyRow{}
	}

	// ---------------- C2 订单状态分布 ----------------
	type statusRow struct {
		Status string `json:"status"`
		Count  int64  `json:"count"`
	}
	var statusDist []statusRow
	_ = db.Model(&models.Order{}).
		Select("status, COUNT(*) AS count").Group("status").Scan(&statusDist).Error
	if statusDist == nil {
		statusDist = []statusRow{}
	}

	// ---------------- C3 货源成本对比 ----------------
	//
	// 回答「哪家最便宜、该切哪家」。只列启用中的货源，按成本升序，
	// 取前 20 条 —— 再多条形图就看不清了。
	type supplyCostRow struct {
		SupplyID  uint    `json:"supply_id"`
		Platform  string  `json:"platform"`
		GoodsName string  `json:"goods_name"`
		GoodsNo   string  `json:"goods_no"`
		CostPrice float64 `json:"cost_price"`
		Stock     int     `json:"stock"`
	}
	var supplyCosts []supplyCostRow
	_ = db.Table("supplies AS s").
		Select(`s.id AS supply_id, COALESCE(p.name,'') AS platform,
		        COALESCE(g.name,'') AS goods_name, s.platform_goods_no AS goods_no,
		        s.cost_price, s.stock`).
		Joins("LEFT JOIN platforms p ON p.id = s.platform_id").
		Joins("LEFT JOIN goods g ON g.id = s.goods_id").
		Where("s.enabled = ? AND s.cost_price > 0", true).
		Order("s.cost_price ASC").Limit(20).Scan(&supplyCosts).Error
	if supplyCosts == nil {
		supplyCosts = []supplyCostRow{}
	}

	// ---------------- C4 各上游成功率 ----------------
	//
	// 回答「哪个上游在恶化」。成功率 = delivered / 总尝试订单数。
	// 用订单表按 platform_id 聚合（比 OrderAttempt 更贴近「最终成单」口径）。
	type platformRateRow struct {
		PlatformID  uint    `json:"platform_id"`
		Name        string  `json:"name"`
		Total       int64   `json:"total"`
		Delivered   int64   `json:"delivered"`
		Failed      int64   `json:"failed"`
		SuccessRate float64 `json:"success_rate"`
	}
	var platformRates []platformRateRow
	_ = db.Table("orders AS o").
		Select(`o.platform_id,
		        COALESCE(p.name,'未知') AS name,
		        COUNT(*) AS total,
		        SUM(CASE WHEN o.status = ? THEN 1 ELSE 0 END) AS delivered,
		        SUM(CASE WHEN o.status = ? THEN 1 ELSE 0 END) AS failed`,
			models.OrderStatusDelivered, models.OrderStatusFailed).
		Joins("LEFT JOIN platforms p ON p.id = o.platform_id").
		Where("o.platform_id IS NOT NULL AND o.created_at >= ?", since).
		Group("o.platform_id, p.name").Order("total DESC").Scan(&platformRates).Error
	for i := range platformRates {
		if platformRates[i].Total > 0 {
			platformRates[i].SuccessRate = float64(platformRates[i].Delivered) / float64(platformRates[i].Total)
		}
	}
	if platformRates == nil {
		platformRates = []platformRateRow{}
	}

	// ---------------- C5 每日消耗 vs 余额 ----------------
	//
	// 这是整页最值钱的图：把「余额告警」变成「看得见的趋势」，
	// 让你提前几天知道要充值，而不是等卖不出去。
	//
	// 消耗 = 每日上游采购支出（total_amount，含手续费）
	type spendRow struct {
		Day   string  `json:"day"`
		Spend float64 `json:"spend"`
	}
	var spendTrend []spendRow
	_ = db.Model(&models.Order{}).
		Where("created_at >= ? AND status = ?", since, models.OrderStatusDelivered).
		Select("to_char(created_at,'MM-DD') AS day, COALESCE(SUM(total_amount),0) AS spend").
		Group("day").Order("day ASC").Scan(&spendTrend).Error
	if spendTrend == nil {
		spendTrend = []spendRow{}
	}

	// 当前余额与日均消耗 → 预计可用天数
	type balanceRow struct {
		PlatformID uint    `json:"platform_id"`
		Name       string  `json:"name"`
		Balance    float64 `json:"balance"`
		Enabled    bool    `json:"enabled"`
	}
	var balances []balanceRow
	_ = db.Model(&models.Platform{}).
		Select("id AS platform_id, name, balance, enabled").
		Order("balance ASC").Scan(&balances).Error
	if balances == nil {
		balances = []balanceRow{}
	}

	var totalSpend float64
	for _, s := range spendTrend {
		totalSpend += s.Spend
	}
	avgDailySpend := 0.0
	if len(spendTrend) > 0 {
		avgDailySpend = totalSpend / float64(len(spendTrend))
	}
	var totalBalance float64
	for _, b := range balances {
		if b.Enabled {
			totalBalance += b.Balance
		}
	}
	daysLeft := -1.0 // -1 = 无法估算（没有消耗数据）
	if avgDailySpend > 0 {
		daysLeft = totalBalance / avgDailySpend
	}

	// ---------------- C6 商品销量 TOP 10 ----------------
	type topGoodsRow struct {
		GoodsID uint    `json:"goods_id"`
		Name    string  `json:"name"`
		Orders  int64   `json:"orders"`
		Amount  float64 `json:"amount"`
	}
	var topGoods []topGoodsRow
	_ = db.Table("orders AS o").
		Select(`o.goods_id, COALESCE(g.name, o.goods_id::text) AS name,
		        COUNT(*) AS orders,
		        COALESCE(SUM(o.total_amount),0) AS amount`).
		Joins("LEFT JOIN goods g ON g.id = o.goods_id").
		Where("o.created_at >= ? AND o.status = ?", since, models.OrderStatusDelivered).
		Group("o.goods_id, g.name").Order("orders DESC").Limit(10).Scan(&topGoods).Error
	if topGoods == nil {
		topGoods = []topGoodsRow{}
	}

	OK(c, gin.H{
		"days":            days,
		"daily":           daily,
		"status_dist":     statusDist,
		"supply_costs":    supplyCosts,
		"platform_rates":  platformRates,
		"spend_trend":     spendTrend,
		"balances":        balances,
		"avg_daily_spend": avgDailySpend,
		"total_balance":   totalBalance,
		"days_left":       daysLeft,
		"top_goods":       topGoods,
	})
}
