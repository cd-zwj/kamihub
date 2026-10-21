package api

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/shopspring/decimal"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

// ================================================================
// 迭代 3：销售阶梯专价
//
// 两组接口：
//   /api/shop/agent/*  —— 买家侧（需 BuyerAuth）
//   /api/admin/agent/* —— 后台侧（需 AdminOnly）
//
// 一个刻意的设计：**普通买家调用 /agent/me 不会报错**，
// 而是返回 agent_status=0 + retail 档位。
// 让前端可以用同一个接口渲染「申请入口 / 销售中心」两种状态，
// 不用先探测身份再决定调哪个接口。
// ================================================================

// ---------------------------------------------------------------- 买家侧

// shopAgentMe 我的档位与销量概览
func (h *Handlers) shopAgentMe(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.agent == nil {
		OK(c, gin.H{"agent_status": models.AgentStatusNone, "enabled": false})
		return
	}
	ov, err := h.agent.Overview(c.Request.Context(), buyer.ID)
	if err != nil {
		ServerError(c, "查询销售信息失败")
		return
	}
	sec := h.settings.Security(c.Request.Context())
	OK(c, gin.H{
		"agent_status":   ov.AgentStatus,
		"enabled":        ov.Enabled,
		"self_apply":     sec.AgentSelfApply,
		"current_tier":   ov.CurrentTier,
		"next_tier":      ov.NextTier,
		"stats_30d":      ov.Stats30d,
		"to_next_orders": ov.ToNextOrders,
		"to_next_amount": ov.ToNextAmount,
		"grace_until":    ov.GraceUntil,
		"remark":         ov.Remark,
	})
}

// shopAgentTiers 全部档位说明（公开，用于激励展示）
func (h *Handlers) shopAgentTiers(c *gin.Context) {
	if h.pricing == nil {
		OK(c, gin.H{"list": []any{}, "enabled": false})
		return
	}
	tiers, err := h.pricing.Tiers(c.Request.Context())
	if err != nil {
		ServerError(c, "查询档位失败")
		return
	}
	sec := h.settings.Security(c.Request.Context())
	OK(c, gin.H{
		"list":     tiers,
		"enabled":  sec.AgentEnabled(),
		"fee_rate": h.pricing.FeeRate().String(),
	})
}

type agentApplyReq struct {
	Reason string `json:"reason"`
}

// shopAgentApply 申请成为销售。
//
// 受 agent_self_apply 开关控制：V1 建议关闭，只由后台人工开通
// （设计 §11 第 6 条：早期只放信任的人）。
func (h *Handlers) shopAgentApply(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.agent == nil {
		ServerError(c, "销售功能未启用")
		return
	}
	sec := h.settings.Security(c.Request.Context())
	if !sec.AgentSelfApply {
		Fail(c, 403, "AGENT_APPLY_CLOSED",
			"暂未开放自助申请，请联系客服开通销售资格")
		return
	}
	var req agentApplyReq
	if !BindJSON(c, &req) {
		return
	}
	if err := h.agent.Apply(c.Request.Context(), service.AgentApplyInput{
		BuyerID: buyer.ID,
		Reason:  strings.TrimSpace(req.Reason),
	}); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "agent.apply", "buyer", buyer.Username, gin.H{"reason": req.Reason})
	OK(c, gin.H{"message": "申请已提交，等待审核"})
}

// shopAgentPriceList 我的专价目表
func (h *Handlers) shopAgentPriceList(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.agent == nil {
		OK(c, gin.H{"list": []any{}})
		return
	}
	rows, err := h.agent.PriceList(c.Request.Context(), buyer.ID)
	if err != nil {
		ServerError(c, "查询专价目表失败")
		return
	}
	OK(c, gin.H{"list": rows})
}

// shopAgentStats 我的销量统计（近 30 天 + 最近 14 天日曲线）
func (h *Handlers) shopAgentStats(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.agent == nil {
		ServerError(c, "销售功能未启用")
		return
	}
	ctx := c.Request.Context()
	st, err := h.agent.Stats30d(ctx, buyer.ID)
	if err != nil {
		ServerError(c, "查询销量失败")
		return
	}

	// 最近 14 天日曲线。为什么只取 14 天：再长运营看不动，
	// 而且前端图表本来就要滚动。
	type dayPoint struct {
		Day    string `json:"day"`
		Orders int    `json:"orders"`
		Amount string `json:"amount"`
	}
	since := time.Now().AddDate(0, 0, -14)
	var raw []struct {
		Day    string
		Orders int
		Amount string
	}
	_ = h.db.WithContext(ctx).Model(&models.Order{}).
		Where("buyer_id = ? AND status = ? AND created_at >= ?",
			buyer.ID, models.OrderStatusDelivered, since).
		Select("to_char(created_at,'YYYY-MM-DD') AS day, COUNT(*) AS orders, COALESCE(SUM(paid_amount),0)::text AS amount").
		Group("day").Order("day ASC").Scan(&raw).Error

	trend := make([]dayPoint, 0, len(raw))
	for _, r := range raw {
		trend = append(trend, dayPoint{Day: r.Day, Orders: r.Orders, Amount: r.Amount})
	}

	OK(c, gin.H{"stats_30d": st, "trend": trend})
}

// ---------------------------------------------------------------- 后台：申请与销售管理

// adminListAgentApplications 销售申请列表
//
// 「申请」的判定：agent_status=0 但 agent_remark 以 [申请] 开头。
// 为什么不单独建申请表：申请只是一个待办状态，为它建表意味着
// 还要处理「申请后又撤回」「重复申请」等状态机，收益不抵成本。
func (h *Handlers) adminListAgentApplications(c *gin.Context) {
	page, size := Pagination(c)
	q := h.db.WithContext(c.Request.Context()).Model(&models.Buyer{}).
		Where("agent_status = ? AND agent_remark LIKE ?", models.AgentStatusNone, "[申请]%")

	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	var rows []models.Buyer
	if err := q.Order("updated_at DESC").Offset((page - 1) * size).Limit(size).
		Find(&rows).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

type reviewAgentReq struct {
	Approve  bool   `json:"approve"`
	Remark   string `json:"remark"`
	TierCode string `json:"tier_code"`
}

// adminReviewAgent 审核销售申请
func (h *Handlers) adminReviewAgent(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if h.agent == nil {
		ServerError(c, "销售功能未启用")
		return
	}
	var req reviewAgentReq
	if !BindJSON(c, &req) {
		return
	}
	if err := h.agent.Review(c.Request.Context(), id, service.ReviewInput{
		Approve:  req.Approve,
		Remark:   strings.TrimSpace(req.Remark),
		TierCode: strings.TrimSpace(req.TierCode),
	}); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "agent.review", "buyer", itoa(int64(id)),
		gin.H{"approve": req.Approve, "tier": req.TierCode})
	OK(c, gin.H{"message": "已处理"})
}

// adminListAgents 销售列表（含档位与 30 天销量）
func (h *Handlers) adminListAgents(c *gin.Context) {
	page, size := Pagination(c)
	ctx := c.Request.Context()

	q := h.db.WithContext(ctx).Model(&models.Buyer{}).
		Where("agent_status <> ?", models.AgentStatusNone)
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("username ILIKE ? OR email ILIKE ?", like, like)
	}
	if tier := strings.TrimSpace(c.Query("tier")); tier != "" {
		q = q.Where("price_tier = ?", tier)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	var buyers []models.Buyer
	if err := q.Order("agent_bound_at DESC NULLS LAST, id DESC").
		Offset((page - 1) * size).Limit(size).Find(&buyers).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}

	// 逐条补 30 天销量。代理数量在几十个量级，这里不做批量优化 ——
	// 真到 500 个以上再考虑建 tier_sales_stats 汇总表（设计 §4.4）。
	type row struct {
		models.Buyer
		Orders30d int    `json:"orders_30d"`
		Amount30d string `json:"amount_30d"`
	}
	out := make([]row, 0, len(buyers))
	for i := range buyers {
		r := row{Buyer: buyers[i]}
		if h.agent != nil {
			if st, err := h.agent.Stats30d(ctx, buyers[i].ID); err == nil {
				r.Orders30d = st.Orders
				r.Amount30d = st.Amount.StringFixed(2)
			}
		}
		out = append(out, r)
	}
	OKPage(c, out, total, page, size)
}

type updateAgentReq struct {
	// TierCode 手动改档位（传了就改）
	TierCode *string `json:"tier_code"`
	// AgentStatus 改销售状态：0/1/2
	AgentStatus *int   `json:"agent_status"`
	Remark      string `json:"remark"`
}

// adminUpdateAgent 手动改档位 / 冻结 / 备注
func (h *Handlers) adminUpdateAgent(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if h.agent == nil {
		ServerError(c, "销售功能未启用")
		return
	}
	var req updateAgentReq
	if !BindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	if req.AgentStatus != nil {
		if err := h.agent.SetAgentStatus(ctx, id, *req.AgentStatus, strings.TrimSpace(req.Remark)); err != nil {
			Abort(c, err)
			return
		}
	}
	if req.TierCode != nil && strings.TrimSpace(*req.TierCode) != "" {
		if err := h.agent.SetTier(ctx, id, strings.TrimSpace(*req.TierCode)); err != nil {
			Abort(c, err)
			return
		}
	}
	if req.AgentStatus == nil && req.TierCode == nil && strings.TrimSpace(req.Remark) != "" {
		if err := h.db.WithContext(ctx).Model(&models.Buyer{}).Where("id = ?", id).
			Update("agent_remark", req.Remark).Error; err != nil {
			ServerError(c, "更新失败")
			return
		}
	}

	h.auditLog(c, "agent.update", "buyer", itoa(int64(id)), gin.H{
		"tier": req.TierCode, "status": req.AgentStatus, "remark": req.Remark,
	})
	OK(c, gin.H{"message": "已更新"})
}

// ---------------------------------------------------------------- 后台：档位配置

func (h *Handlers) adminListTiers(c *gin.Context) {
	if h.pricing == nil {
		OK(c, gin.H{"list": []any{}})
		return
	}
	rows, err := h.pricing.ListTiers(c.Request.Context())
	if err != nil {
		ServerError(c, "查询档位失败")
		return
	}
	OK(c, gin.H{"list": rows, "fee_rate": h.pricing.FeeRate().String()})
}

// adminUpdateTier 改档位配置（改完立刻生效）
func (h *Handlers) adminUpdateTier(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if h.pricing == nil {
		ServerError(c, "定价服务未启用")
		return
	}
	var in service.UpdateTierInput
	if !BindJSON(c, &in) {
		return
	}
	tier, err := h.pricing.UpdateTier(c.Request.Context(), id, in)
	if err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "agent.tier_update", "price_tier", tier.Code, gin.H{
		"discount_rate": tier.DiscountRate.String(),
		"enabled":       tier.Enabled,
	})
	OK(c, tier)
}

// adminRecalcTiers 手动触发档位重算
func (h *Handlers) adminRecalcTiers(c *gin.Context) {
	if h.agent == nil {
		ServerError(c, "销售功能未启用")
		return
	}
	res, err := h.agent.RecalcTiers(c.Request.Context())
	if err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "agent.recalc", "price_tier", "", gin.H{
		"scanned": res.Scanned, "upgraded": res.Upgraded, "downgraded": res.Downgraded,
	})
	OK(c, res)
}

// adminAgentStats 销售看板
func (h *Handlers) adminAgentStats(c *gin.Context) {
	ctx := c.Request.Context()

	// 档位分布
	type tierCount struct {
		PriceTier string `json:"price_tier"`
		N         int64  `json:"n"`
	}
	var dist []tierCount
	_ = h.db.WithContext(ctx).Model(&models.Buyer{}).
		Where("agent_status = ?", models.AgentStatusActive).
		Select("price_tier, COUNT(*) AS n").Group("price_tier").Scan(&dist).Error

	// 销售贡献的 GMV（近 30 天 delivered，且买家是销售）
	type agg struct {
		Orders int64
		Amount string
	}
	var a agg
	_ = h.db.WithContext(ctx).Model(&models.Order{}).
		Where("status = ? AND created_at >= ? AND buyer_id IN (?)",
			models.OrderStatusDelivered, time.Now().AddDate(0, 0, -30),
			h.db.Model(&models.Buyer{}).Select("id").Where("agent_status = ?", models.AgentStatusActive),
		).
		Select("COUNT(*) AS orders, COALESCE(SUM(paid_amount),0)::text AS amount").
		Scan(&a).Error

	var agentTotal, frozen int64
	h.db.WithContext(ctx).Model(&models.Buyer{}).Where("agent_status = ?", models.AgentStatusActive).Count(&agentTotal)
	h.db.WithContext(ctx).Model(&models.Buyer{}).Where("agent_status = ?", models.AgentStatusFrozen).Count(&frozen)

	OK(c, gin.H{
		"agents":     agentTotal,
		"frozen":     frozen,
		"tier_dist":  dist,
		"gmv_30d":    a.Amount,
		"orders_30d": a.Orders,
		"enabled":    h.settings.Security(ctx).AgentEnabled(),
		"self_apply": h.settings.Security(ctx).AgentSelfApply,
	})
}

// ---------------------------------------------------------------- 后台：商品级专价覆盖

func (h *Handlers) adminListTierPrices(c *gin.Context) {
	listingID := c.Query("listing_id")
	q := h.db.WithContext(c.Request.Context()).Model(&models.ListingTierPrice{})
	if listingID != "" {
		q = q.Where("listing_id = ?", listingID)
	}
	var rows []models.ListingTierPrice
	if err := q.Order("listing_id ASC, tier_code ASC").Limit(500).Find(&rows).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	OK(c, gin.H{"list": rows})
}

type upsertTierPriceReq struct {
	ListingID uint    `json:"listing_id" binding:"required"`
	TierCode  string  `json:"tier_code" binding:"required"`
	Price     float64 `json:"price"`
}

// adminUpsertTierPrice 设置商品级专价覆盖（按 listing+tier 幂等 upsert）
func (h *Handlers) adminUpsertTierPrice(c *gin.Context) {
	var req upsertTierPriceReq
	if !BindJSON(c, &req) {
		return
	}
	if req.Price <= 0 {
		BadRequest(c, "专价必须大于 0")
		return
	}
	price := decimal.NewFromFloat(req.Price).Round(2)

	var row models.ListingTierPrice
	err := h.db.WithContext(c.Request.Context()).
		Where("listing_id = ? AND tier_code = ?", req.ListingID, req.TierCode).
		First(&row).Error
	if err == nil {
		if err := h.db.Model(&row).Update("price", price).Error; err != nil {
			ServerError(c, "更新失败")
			return
		}
	} else {
		row = models.ListingTierPrice{
			ListingID: req.ListingID, TierCode: req.TierCode, Price: price,
		}
		if err := h.db.Create(&row).Error; err != nil {
			ServerError(c, "保存失败")
			return
		}
	}
	h.auditLog(c, "agent.tier_price_set", "listing", itoa(int64(req.ListingID)),
		gin.H{"tier": req.TierCode, "price": req.Price})
	OK(c, row)
}

func (h *Handlers) adminDeleteTierPrice(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if err := h.db.WithContext(c.Request.Context()).
		Delete(&models.ListingTierPrice{}, id).Error; err != nil {
		ServerError(c, "删除失败")
		return
	}
	h.auditLog(c, "agent.tier_price_delete", "listing_tier_price", itoa(int64(id)), nil)
	OK(c, gin.H{"message": "已删除"})
}

var _ = errors.New
