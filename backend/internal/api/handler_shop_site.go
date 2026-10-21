package api

import (
	"github.com/gin-gonic/gin"
)

// ================================================================
// 商城公开的站点信息
//
// 买家的帮助页/页脚要展示真实的客服 QQ、交流群与工作时间。
// 这些内容存在 settings.contact 里，卖家可以在后台随时改。
//
// 为什么不复用 /api/settings：那个接口挂在 authed 组里，要员工令牌，
// 买家根本拿不到。如果前端把客服 QQ 硬编码，卖家改一次就得重新发版。
//
// 只暴露「本来就打算给买家看」的字段（提货页展示的也是这几项），
// 支付密钥、上游配置、风控阈值一律不在这里出现。
// ================================================================

// shopSiteInfo GET /api/shop/site
//
// 公开接口，无需登录：未登录的访客也要能看到客服联系方式。
func (h *Handlers) shopSiteInfo(c *gin.Context) {
	ctx := c.Request.Context()
	brand := h.settings.Brand(ctx)
	contact := h.settings.Contact(ctx)
	page := h.settings.DeliveryPage(ctx)
	security := h.settings.Security(ctx)
	payment := h.settings.Payment(ctx)

	// EnabledItems 已经过滤掉「未启用」和「值为空」的项，
	// 所以这里直接铺开，前端不用再判断一遍。
	items := make([]gin.H, 0, len(contact.Items))
	for _, it := range contact.EnabledItems() {
		items = append(items, gin.H{
			"type":  it.Type,
			"label": it.Label,
			"value": it.Value,
		})
	}

	OK(c, gin.H{
		"site_name":   brand.SiteName,
		"logo_url":    brand.LogoURL,
		"footer_note": brand.FooterNote,
		"contact": gin.H{
			"items":      items,
			"notice":     contact.Notice,
			"work_hours": contact.WorkHours,
		},
		// 提货链接的规则：帮助页必须如实告诉买家「链接多久过期」。
		// 这里给出的就是服务端创建链接时真正用的那两个值，
		// 不写死在文案里，避免以后改了设置文案就变成骗人。
		"delivery": gin.H{
			"link_ttl_hours": security.DefaultTTLHours,
			"expired_hint":   page.ExpiredHint,
		},
		// 在线支付：帮助页要写清「支付单多久会失效」。
		// 只给买家关心的两项；密钥、商户号、上游地址等一概不出现在这里。
		"payment": gin.H{
			"enabled":           payment.Enabled,
			"pay_types":         payment.PayTypes,
			"order_ttl_minutes": payment.OrderTTLMinutes,
		},
	})
}
