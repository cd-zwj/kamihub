package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/pkg/textfilter"
	"kamihub/internal/render"
	"kamihub/internal/service"
)

// filterInstruction 按规则净化「使用说明」再展示给买家。
//
// 规则为空时原样返回，保证没配置过的站点行为完全不变。
func filterInstruction(text string, rules []textfilter.Rule) string {
	if text == "" || len(rules) == 0 {
		return text
	}
	return textfilter.Apply(text, rules)
}

// deliveryPage 公开提货页。token 本身就是访问凭证，不需要登录。
//
// 安全要点：
//   - 全程 noindex / no-store，禁止搜索引擎收录与中间层缓存
//   - 卡密只在服务端渲染进 HTML，不经过任何前端 API
//   - 失效后仍展示联系方式，避免买家以为被骗
func (h *Handlers) deliveryPage(c *gin.Context) {
	token := strings.TrimSpace(c.Param("token"))
	if token == "" || len(token) > 128 {
		h.renderDeliveryError(c, "链接无效", "该提货链接格式不正确，请确认链接是否完整。")
		return
	}

	d, order, err := h.deliveries.FindByToken(c.Request.Context(), token)
	if err != nil {
		if errors.Is(err, service.ErrDeliveryNotFound) {
			h.renderDeliveryError(c, "链接无效", "未找到对应的提货记录，请确认链接是否正确。")
			return
		}
		h.renderDeliveryError(c, "系统异常", "读取提货信息失败，请稍后重试或联系卖家。")
		return
	}

	ctx := c.Request.Context()

	// 需要密码且尚未解锁
	if d.PasswordHash != "" && !h.isUnlocked(c, token) {
		h.renderPage(c, render.PageData{
			SiteName:   h.settings.Brand(ctx).SiteName,
			LogoURL:    h.settings.Brand(ctx).LogoURL,
			NeedUnlock: true,
			OrderNo:    order.OrderNo,
			ShowOrderNo: true,
		})
		return
	}

	// 链接不可用
	if err := h.deliveries.CheckUsable(d); err != nil {
		h.renderExpired(c, d, order, err)
		return
	}

	h.deliveries.RecordView(ctx, d, c.ClientIP(), c.Request.UserAgent(), c.Request.Referer())
	h.renderDeliveryOK(c, d, order)
}

// deliveryUnlock 处理提货页密码提交。
func (h *Handlers) deliveryUnlock(c *gin.Context) {
	token := strings.TrimSpace(c.Param("token"))
	if token == "" {
		h.renderDeliveryError(c, "链接无效", "该提货链接格式不正确。")
		return
	}

	d, order, err := h.deliveries.FindByToken(c.Request.Context(), token)
	if err != nil {
		h.renderDeliveryError(c, "链接无效", "未找到对应的提货记录。")
		return
	}

	password := strings.TrimSpace(c.PostForm("password"))
	if !h.deliveries.VerifyPassword(d, password) {
		brand := h.settings.Brand(c.Request.Context())
		h.renderPage(c, render.PageData{
			SiteName:    brand.SiteName,
			LogoURL:     brand.LogoURL,
			NeedUnlock:  true,
			UnlockError: "密码不正确，请重新输入",
			OrderNo:     order.OrderNo,
			ShowOrderNo: true,
		})
		return
	}

	// 解锁成功：种一个 HMAC 签名 cookie，避免把密码反复带在 URL 上
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     h.unlockCookieName(token),
		Value:    h.unlockSignature(token),
		Path:     "/d/" + token,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.IsProduction(),
		MaxAge:   3600,
	})
	c.Redirect(http.StatusSeeOther, "/d/"+token)
}

// ---------------------------------------------------------------- 渲染

func (h *Handlers) renderDeliveryOK(c *gin.Context, d *models.Delivery, order *models.Order) {
	ctx := c.Request.Context()
	brand := h.settings.Brand(ctx)
	contact := h.settings.Contact(ctx)
	page := h.settings.DeliveryPage(ctx)

	data := render.PageData{
		SiteName:   brand.SiteName,
		LogoURL:    brand.LogoURL,
		FooterNote: brand.FooterNote,

		Usable: true,

		ShowGoodsImage:  page.ShowGoodsImage,
		ShowGoodsName:   page.ShowGoodsName,
		ShowOrderNo:     page.ShowOrderNo,
		ShowQuantity:    page.ShowQuantity,
		ShowDeliverTime: page.ShowDeliverTime,
		ShowInstruction: page.ShowInstruction,
		ShowContact:     page.ShowContact,
		ShowNotice:      page.ShowNotice,
		AllowCopy:       page.AllowCopy,

		OrderNo: order.OrderNo,
		TradeNo: order.PlatformTradeNo,
		Quantity: order.Quantity,

		Cards:     cardList(*order),
		Content:   order.Content,
		// 使用说明会原样展示给买家，上游供货商往往在里面写了自己的联系方式。
		// 这里在「渲染时」过滤，不改数据库里的 order.Instruction ——
		// 存档保留原文便于追溯对账，买家看到的是净化后的版本。
		// 卡密（Cards）绝不参与过滤：那是买家真正要买的东西。
		Instruction: filterInstruction(order.Instruction, page.InstructionRules),

		Extra:         order.ExtraText,
		ExtraPosition: page.ExtraPosition,
		ExtraFirst:    page.ExtraPosition == "before",
		ExtraLast:     page.ExtraPosition != "before",

		WorkHours: contact.WorkHours,
		Notice:    contact.Notice,
	}

	// 商品信息：优先用关联商品，商品被删则回落到下单快照
	if order.Goods != nil {
		data.GoodsName = order.Goods.Name
		data.GoodsImage = order.Goods.Image
	} else if len(order.GoodsSnapshot) > 0 {
		var snap models.Goods
		if err := json.Unmarshal(order.GoodsSnapshot, &snap); err == nil {
			data.GoodsName = snap.Name
			data.GoodsImage = snap.Image
		}
	}

	if order.DeliveredAt != nil {
		data.DeliverTime = order.DeliveredAt.Format("2006-01-02 15:04")
	} else {
		data.DeliverTime = order.CreatedAt.Format("2006-01-02 15:04")
	}

	for _, it := range contact.EnabledItems() {
		data.Contacts = append(data.Contacts, render.ContactItem{
			Type:  it.Type,
			Label: it.Label,
			Value: it.Value,
		})
	}

	h.renderPage(c, data)
}

func (h *Handlers) renderExpired(c *gin.Context, d *models.Delivery, order *models.Order, reason error) {
	ctx := c.Request.Context()
	brand := h.settings.Brand(ctx)
	contact := h.settings.Contact(ctx)
	page := h.settings.DeliveryPage(ctx)

	hint := page.ExpiredHint
	switch {
	case errors.Is(reason, service.ErrDeliveryRevoked):
		hint = "该提货链接已被卖家作废。"
	case errors.Is(reason, service.ErrDeliveryLimit):
		hint = "该提货链接的访问次数已达上限。"
	case errors.Is(reason, service.ErrDeliveryExpired):
		if hint == "" {
			hint = "该提货链接已过期。"
		}
	}
	if hint == "" {
		hint = "该提货链接已失效，请凭订单号联系客服补发。"
	}

	data := render.PageData{
		SiteName:    brand.SiteName,
		LogoURL:     brand.LogoURL,
		FooterNote:  brand.FooterNote,
		Usable:      false,
		ErrorText:   hint,
		OrderNo:     order.OrderNo,
		ShowOrderNo: true,
		ShowContact: page.ShowContact,
		WorkHours:   contact.WorkHours,
	}
	for _, it := range contact.EnabledItems() {
		data.Contacts = append(data.Contacts, render.ContactItem{Type: it.Type, Label: it.Label, Value: it.Value})
	}
	h.renderPage(c, data)
}

func (h *Handlers) renderDeliveryError(c *gin.Context, title, message string) {
	ctx := c.Request.Context()
	brand := h.settings.Brand(ctx)
	contact := h.settings.Contact(ctx)

	data := render.PageData{
		SiteName:    brand.SiteName,
		LogoURL:     brand.LogoURL,
		FooterNote:  brand.FooterNote,
		Usable:      false,
		ErrorText:   message,
		ShowContact: true,
		WorkHours:   contact.WorkHours,
	}
	for _, it := range contact.EnabledItems() {
		data.Contacts = append(data.Contacts, render.ContactItem{Type: it.Type, Label: it.Label, Value: it.Value})
	}
	_ = title
	h.renderPage(c, data)
}

func (h *Handlers) renderPage(c *gin.Context, data render.PageData) {
	// 提货页禁止被收录、禁止缓存
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("X-Robots-Tag", "noindex, nofollow, noarchive, nosnippet")
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private")
	c.Header("Pragma", "no-cache")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	// 不允许被 iframe 嵌套，防止钓鱼站套壳
	c.Header("X-Frame-Options", "DENY")
	c.Header("Content-Security-Policy",
		"default-src 'none'; img-src https: data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; form-action 'self'")

	c.Status(http.StatusOK)
	if err := h.renderer.Render(c.Writer, data); err != nil {
		// 渲染失败时给出最简兜底，绝不让买家看到空白页
		_, _ = c.Writer.WriteString("<!DOCTYPE html><html><body style=\"font-family:sans-serif;padding:24px\">页面渲染失败，请凭订单号联系卖家。</body></html>")
	}
}

// ---------------------------------------------------------------- 解锁 cookie

func (h *Handlers) unlockCookieName(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "kmh_u_" + hex.EncodeToString(sum[:8])
}

func (h *Handlers) unlockSignature(token string) string {
	mac := hmac.New(sha256.New, []byte(h.cfg.JWT.Secret))
	mac.Write([]byte("delivery-unlock:" + token))
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *Handlers) isUnlocked(c *gin.Context, token string) bool {
	value, err := c.Cookie(h.unlockCookieName(token))
	if err != nil || value == "" {
		return false
	}
	return hmac.Equal([]byte(value), []byte(h.unlockSignature(token)))
}

var _ = time.Now
