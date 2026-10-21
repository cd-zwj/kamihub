package api

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"kamihub/internal/service"
)

// ================================================================
// 匿名购买 / 匿名查单
//
// 需求：不注册账号也能买，用「邮箱 + 6 位安全密码」下单，
// 之后凭同样的邮箱 + 密码查自己的订单。
//
// 安全设计（这里全是攻击面，必须逐条守住）：
//  1. 6 位纯数字只有 100 万种组合，密码用 bcrypt 存，绝不落明文。
//  2. 查单接口一律 POST + JSON body，密码不進 URL（否则会被 nginx
//     访问日志、浏览器历史、Referer 三处泄露）。
//  3. 三层限流：按 IP 限速、按邮箱限速、连续失败锁定。
//  4. 凭据错误统一返回「邮箱或安全密码不正确」，不区分邮箱是否存在，
//     避免被当成邮箱枚举接口。
//  5. 匿名通道只认 is_guest=true 的账号，绝不触碰正式注册账号的订单。
// ================================================================

const (
	// guestIPLimit 单 IP 的窗口与次数。
	//
	// 定得比邮箱级宽松：同一出口 IP 后面可能坐着多个买家
	// （公司网络 / 网吧 / 运营商 NAT），按 IP 卡太死会误伤正常用户。
	// 真正的防爆破交给「邮箱级限流 + 连续失败锁定」这两道。
	guestIPLimit  = 60
	guestIPWindow = 10 * time.Minute

	// guestEmailLimit 单邮箱窗口：防止盯着一个邮箱暴力猜 6 位密码
	guestEmailLimit  = 8
	guestEmailWindow = 30 * time.Minute

	// guestFailLock 连续失败 N 次后锁定多久
	guestFailThreshold = 5
	guestFailLock      = 30 * time.Minute
)

// guestGate 做匿名接口的三层限流。
//
// 返回 false 表示应当拒绝（已经写好响应）。
func (h *Handlers) guestGate(c *gin.Context, email string) bool {
	ctx := c.Request.Context()
	ip := c.ClientIP()
	email = service.NormalizeGuestEmail(email)

	if h.cache != nil {
		if !h.cache.Allow(ctx, "guest:ip:"+ip, guestIPLimit, guestIPWindow) {
			Fail(c, 429, "TOO_MANY_REQUESTS", "操作过于频繁，请稍后再试")
			return false
		}
		if email != "" && !h.cache.Allow(ctx, "guest:mail:"+email, guestEmailLimit, guestEmailWindow) {
			Fail(c, 429, "TOO_MANY_REQUESTS", "该邮箱尝试过于频繁，请 30 分钟后再试")
			return false
		}
		// 连续失败锁定：密码连错 5 次锁 30 分钟，避免被慢速爆破
		if email != "" && !h.cache.AllowN(ctx, "guest:fail:"+email, guestFailThreshold) {
			Fail(c, 429, "TOO_MANY_REQUESTS", "安全密码错误次数过多，请 30 分钟后再试")
			return false
		}
	}
	return true
}

// guestRecordFail 记一次凭据失败（用于锁定）。
func (h *Handlers) guestRecordFail(c *gin.Context, email string) {
	if h.cache == nil {
		return
	}
	email = service.NormalizeGuestEmail(email)
	if email == "" {
		return
	}
	h.cache.Fail(c.Request.Context(), "guest:fail:"+email, guestFailLock)
}

// guestResetFail 凭据成功后清空失败计数。
func (h *Handlers) guestResetFail(c *gin.Context, email string) {
	if h.cache == nil {
		return
	}
	h.cache.Reset(c.Request.Context(), "guest:fail:"+service.NormalizeGuestEmail(email))
}

// abortGuest 把匿名相关错误映射成合适的 HTTP 响应。
func (h *Handlers) abortGuest(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrGuestAuthFailed):
		Fail(c, 401, "GUEST_AUTH_FAILED", err.Error())
	case errors.Is(err, service.ErrGuestEmailBad), errors.Is(err, service.ErrGuestPinBad):
		BadRequest(c, err.Error())
	case errors.Is(err, service.ErrGuestDisabled):
		Forbidden(c, err.Error())
	default:
		Fail(c, 400, "BAD_REQUEST", err.Error())
	}
}

type guestCredential struct {
	Email string `json:"email"`
	Pin   string `json:"pin"`
}

// ---------------------------------------------------------------- 下单

type guestPurchaseReq struct {
	Email     string `json:"email"`
	Pin       string `json:"pin"`
	ListingID uint   `json:"listing_id"`
	Quantity  int    `json:"quantity"`
	PayType   string `json:"pay_type"`
	UseAPI    bool   `json:"use_api"`
	RequestID string `json:"request_id"`
}

// guestPurchase 匿名下单：邮箱+安全密码 → 在线支付 → 付完自动发货。
//
// 走的是和登录买家「直付购卡」完全相同的一条链路，只是买家载体
// 换成了匿名买家。价格、限购、库存全部由服务端权威计算。
func (h *Handlers) guestPurchase(c *gin.Context) {
	var req guestPurchaseReq
	if !BindJSON(c, &req) {
		return
	}

	email, err := service.ValidateGuestCredential(req.Email, req.Pin)
	if err != nil {
		h.abortGuest(c, err)
		return
	}
	if !h.guestGate(c, email) {
		return
	}

	if h.payment == nil {
		Forbidden(c, "在线支付未开启")
		return
	}
	if req.ListingID == 0 {
		BadRequest(c, "缺少商品信息")
		return
	}
	qty := req.Quantity
	if qty <= 0 {
		qty = 1
	}
	if qty > 1000 {
		BadRequest(c, "单次购买数量不能超过 1000")
		return
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		BadRequest(c, "缺少 request_id（幂等键，重试请复用同一个值）")
		return
	}

	buyer, err := h.guests.EnsureGuest(c.Request.Context(), email, req.Pin, c.ClientIP())
	if err != nil {
		// 只有凭据类错误才记失败，其它（如数据库抖动）不惩罚买家
		if errors.Is(err, service.ErrGuestAuthFailed) {
			h.guestRecordFail(c, email)
		}
		h.abortGuest(c, err)
		return
	}
	h.guestResetFail(c, email)

	res, err := h.payment.CreatePay(c.Request.Context(), service.CreatePayInput{
		BuyerID:  buyer.ID,
		PayType:  strings.TrimSpace(req.PayType),
		ClientIP: c.ClientIP(),
		UseAPI:   req.UseAPI,
		Intent: &service.CreatePayIntent{
			ListingID: req.ListingID,
			Quantity:  qty,
			RequestID: requestID,
		},
	})
	if err != nil {
		h.abortPayment(c, err)
		return
	}
	OK(c, gin.H{
		"pay":   res,
		"email": email,
	})
}

// ---------------------------------------------------------------- 查支付状态

type guestPaymentStatusReq struct {
	guestCredential
	TradeNo string `json:"trade_no"`
}

// guestPaymentStatus 匿名查支付/发货结果（前端付款后轮询用）。
func (h *Handlers) guestPaymentStatus(c *gin.Context) {
	var req guestPaymentStatusReq
	if !BindJSON(c, &req) {
		return
	}
	email, err := service.ValidateGuestCredential(req.Email, req.Pin)
	if err != nil {
		h.abortGuest(c, err)
		return
	}
	if !h.guestGate(c, email) {
		return
	}
	if h.payment == nil {
		Forbidden(c, "在线支付未开启")
		return
	}
	tradeNo := strings.TrimSpace(req.TradeNo)
	if tradeNo == "" {
		BadRequest(c, "缺少订单号")
		return
	}

	buyer, err := h.guests.AuthenticateGuest(c.Request.Context(), email, req.Pin)
	if err != nil {
		if errors.Is(err, service.ErrGuestAuthFailed) {
			h.guestRecordFail(c, email)
		}
		h.abortGuest(c, err)
		return
	}
	h.guestResetFail(c, email)

	// 归属校验：这笔支付单必须属于该匿名买家
	if _, err := h.payment.GetBuyerPayment(c.Request.Context(), buyer.ID, tradeNo); err != nil {
		NotFound(c, "支付订单不存在")
		return
	}

	po, err := h.payment.QueryAndSettle(c.Request.Context(), tradeNo)
	if err != nil {
		if p, e2 := h.payment.GetBuyerPayment(c.Request.Context(), buyer.ID, tradeNo); e2 == nil {
			OK(c, gin.H{
				"payment":  p,
				"warning":  err.Error(),
				"purchase": h.payment.DispatchResult(c.Request.Context(), p),
			})
			return
		}
		BadRequest(c, err.Error())
		return
	}
	OK(c, gin.H{
		"payment":  po,
		"purchase": h.payment.DispatchResult(c.Request.Context(), po),
	})
}

// ---------------------------------------------------------------- 查订单

type guestOrdersReq struct {
	guestCredential
	Status string `json:"status"`
	Page   int    `json:"page"`
	Size   int    `json:"size"`
}

// guestListOrders 匿名查订单列表：邮箱 + 6 位安全密码。
func (h *Handlers) guestListOrders(c *gin.Context) {
	var req guestOrdersReq
	if !BindJSON(c, &req) {
		return
	}
	email, err := service.ValidateGuestCredential(req.Email, req.Pin)
	if err != nil {
		h.abortGuest(c, err)
		return
	}
	if !h.guestGate(c, email) {
		return
	}

	buyer, err := h.guests.AuthenticateGuest(c.Request.Context(), email, req.Pin)
	if err != nil {
		if errors.Is(err, service.ErrGuestAuthFailed) {
			h.guestRecordFail(c, email)
		}
		h.abortGuest(c, err)
		return
	}
	h.guestResetFail(c, email)

	page, size := req.Page, req.Size
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}

	rows, total, err := h.shopOrders.ListBuyerOrders(c.Request.Context(),
		buyer.ID, page, size, strings.TrimSpace(req.Status))
	if err != nil {
		ServerError(c, "查询订单失败")
		return
	}
	OK(c, gin.H{
		"list":     rows,
		"total":    total,
		"page":     page,
		"size":     size,
		"email":    email,
		"is_guest": true,
	})
}

type guestOrderDetailReq struct {
	guestCredential
	OrderID uint `json:"order_id"`
}

// guestGetOrder 匿名查单个订单详情（含提货链接）。
func (h *Handlers) guestGetOrder(c *gin.Context) {
	var req guestOrderDetailReq
	if !BindJSON(c, &req) {
		return
	}
	email, err := service.ValidateGuestCredential(req.Email, req.Pin)
	if err != nil {
		h.abortGuest(c, err)
		return
	}
	if !h.guestGate(c, email) {
		return
	}
	if req.OrderID == 0 {
		BadRequest(c, "缺少订单 id")
		return
	}

	buyer, err := h.guests.AuthenticateGuest(c.Request.Context(), email, req.Pin)
	if err != nil {
		if errors.Is(err, service.ErrGuestAuthFailed) {
			h.guestRecordFail(c, email)
		}
		h.abortGuest(c, err)
		return
	}
	h.guestResetFail(c, email)

	view, err := h.shopOrders.GetBuyerOrder(c.Request.Context(), buyer.ID, req.OrderID)
	if err != nil {
		NotFound(c, "订单不存在")
		return
	}
	OK(c, view)
}
