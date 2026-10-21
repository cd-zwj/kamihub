package api

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"kamihub/internal/service"
)

// ---------------------------------------------------------------- 阶梯折扣

// adminGetListingTiers 查看某商品的阶梯折扣配置。
func (h *Handlers) adminGetListingTiers(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	tiers, err := h.listings.ListTiers(c.Request.Context(), id)
	if err != nil {
		ServerError(c, "读取阶梯折扣失败")
		return
	}
	OK(c, gin.H{"tiers": tiers})
}

// adminSetListingTiers 整体替换某商品的阶梯折扣配置。
//
// 入参 {"tiers":[{"min_qty":10,"discount":90,"label":""}]}
// discount=90 表示 9 折（即打 90%）。
func (h *Handlers) adminSetListingTiers(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req struct {
		Tiers []service.TierInput `json:"tiers"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "请求格式不正确")
		return
	}

	if err := h.listings.ReplaceTiers(c.Request.Context(), id, req.Tiers); err != nil {
		if errors.Is(err, service.ErrListingNotFound) {
			NotFound(c, "商品不存在")
			return
		}
		// 校验类错误直接回给运营，方便在表单里就地修正
		BadRequest(c, err.Error())
		return
	}

	h.auditLog(c, "listing.set_tiers", "listing", itoa(int64(id)), gin.H{
		"count": len(req.Tiers),
	})
	OK(c, gin.H{"ok": true})
}

// shopQuoteListing 买家端实时报价。
//
// 前端改数量时调它，用来显示「合计」「已省 X 元」
// 以及「再买 N 件可享 M 折」的引导。
// 金额口径与下单、支付完全一致（都走 BuyerQuote）。
func (h *Handlers) shopQuoteListing(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	qty := 1
	if v := strings.TrimSpace(c.Query("quantity")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			BadRequest(c, "购买数量不合法")
			return
		}
		qty = n
	}

	quote, err := h.shopOrders.Quote(c.Request.Context(), id, qty)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrListingNotFound):
			NotFound(c, "商品不存在或已下架")
		case errors.Is(err, service.ErrListingQtyInvalid),
			errors.Is(err, service.ErrListingNoStock):
			BadRequest(c, err.Error())
		default:
			ServerError(c, "计算价格失败")
		}
		return
	}

	// 一并返回档位信息，前端不用再单独请求一次商品详情
	tiers, _ := h.listings.ListTiers(c.Request.Context(), id)
	out := gin.H{
		"listing_id": quote.ListingID,
		"quantity":   quote.Quantity,
		"unit_price": quote.UnitPrice,
		"total":      quote.Total,
	}
	if len(tiers) > 0 {
		// 注意：quote.UnitPrice 已经是【命中档位后的折后单价】，
		// 不能再拿它乘折扣（会变成折上折）。这里取商品原价来算各档展示单价。
		base := quote.UnitPrice
		if p, perr := h.listings.BaseSalePrice(c.Request.Context(), id); perr == nil {
			base = p
		}
		list := make([]gin.H, 0, len(tiers))
		for _, t := range tiers {
			if !t.Enabled {
				continue
			}
			list = append(list, gin.H{
				"min_qty":    t.MinQty,
				"discount":   t.Discount,
				"unit_price": t.UnitPriceFor(base),
			})
		}
		if len(list) > 0 {
			out["tiers"] = list
		}
	}
	OK(c, out)
}

