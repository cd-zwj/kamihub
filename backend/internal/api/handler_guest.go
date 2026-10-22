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
	// 这个阈值必须能装下「付款后轮询」：前端每 3~5 秒查一次支付状态，
	// 一笔订单最多会打上百次。原来定成 60 次/10 分钟，一个人连买两单、
	// 或同一条宽带/公司 NAT 后坐两个人，就会把出口 IP 打满。
	guestIPLimit  = 600
	guestIPWindow = 10 * time.Minute

	// guestFailThreshold 单邮箱连续失败 N 次后锁定多久。
	// 这是真正防「6 位数字爆破」的那一道：只有**凭据错误**才计数，
	// 正常下单、正常查单、付款后的轮询都不会消耗它。
	guestFailThreshold = 5
	guestFailLock      = 30 * time.Minute

	// guestFailIPThreshold 单 IP 连续失败上限，防止「换邮箱喷洒」绕过邮箱级锁定
	guestFailIPThreshold = 30
)

// guestGate 做匿名接口的限流与爆破防护。
//
// 历史教训（2026-10-22 线上事故）：
// 老实现里有一道「单邮箱 8 次 / 30 分钟」的固定窗口限流，而且**每个请求都计数**。
// 但匿名购买的付款后轮询本身就是每 3 秒一次 —— 买家付完款 24 秒后额度就打满，
// 之后连「查支付状态」和「订单查询」都被一起拒 30 分钟，
// 表现为「付了钱却跳不到卡密页」「订单查询提示过于频繁」。
//
// 现在的口径：**频率限制只管 IP（宽松），爆破限制只管失败次数（严格）**。
// 成功请求不再消耗任何「次数」额度，所以正常买家永远撞不到限流。
func (h *Handlers) guestGate(c *gin.Context, email string) bool {
	ctx := c.Request.Context()
	ip := c.ClientIP()
	email = service.NormalizeGuestEmail(email)

	if h.cache == nil {
		return true
	}

	// 1) 单 IP 频率：只为挡住「把接口打爆」，不是给正常买家设的门槛
	if !h.cache.Allow(ctx, "guest:ip:"+ip, guestIPLimit, guestIPWindow) {
		Fail(c, 429, "TOO_MANY_REQUESTS", "操作过于频繁，请稍后再试")
		return false
	}

	// 2) 单邮箱连续失败锁定：密码连错 5 次锁 30 分钟
	if email != "" && !h.cache.AllowN(ctx, "guest:fail:"+email, guestFailThreshold) {
		Fail(c, 429, "TOO_MANY_REQUESTS", "安全密码错误次数过多，请 30 分钟后再试")
		return false
	}

	// 3) 单 IP 连续失败锁定：防止换着邮箱撞 6 位密码
	if !h.cache.AllowN(ctx, "guest:failip:"+ip, guestFailIPThreshold) {
		Fail(c, 429, "TOO_MANY_REQUESTS", "操作过于频繁，请稍后再试")
		return false
	}

	return true
}

// guestRecordFail 记一次凭据失败（用于锁定）。
func (h *Handlers) guestRecordFail(c *gin.Context, email string) {
	if h.cache == nil {
		return
	}
	ctx := c.Request.Context()
	// 失败才计数：这是「爆破防护」而不是「频率限制」
	h.cache.Fail(ctx, "guest:failip:"+c.ClientIP(), guestFailLock)
	email = service.NormalizeGuestEmail(email)
	if email == "" {
		return
	}
	h.cache.Fail(ctx, "guest:fail:"+email, guestFailLock)
}

// guestResetFail 凭据成功后清空失败计数。
func (h *Handlers) guestResetFail(c *gin.Context, email string) {
	if h.cache == nil {
		return
	}
	ctx := c.Request.Context()
	h.cache.Reset(ctx, "guest:fail:"+service.NormalizeGuestEmail(email))
	h.cache.Reset(ctx, "guest:failip:"+c.ClientIP())
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
