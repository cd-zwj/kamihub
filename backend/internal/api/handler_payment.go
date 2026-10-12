package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

// ================================================================
// C 端在线支付（易支付）接口
// ================================================================

// shopPaymentInfo 支付可用性（公开）
//
// 前端进钱包页时调它决定是否展示「在线充值」。
// 只返回开关与限额，绝不返回商户号与密钥。
func (h *Handlers) shopPaymentInfo(c *gin.Context) {
	// 网关未注入时（例如历史版本配置）当作未开启处理，避免 panic
	if h.payment == nil {
		OK(c, service.PublicInfo{Available: false, Reason: "未启用"})
		return
	}
	OK(c, h.payment.PublicInfo(c.Request.Context()))
}

type shopCreatePaymentReq struct {
	Amount  float64 `json:"amount" binding:"required"`
	PayType string  `json:"pay_type"`
	// UseAPI true 走 mapi.php 拿二维码；false 走 submit.php 页面跳转
	UseAPI bool `json:"use_api"`
}

// shopCreatePayment 发起一笔在线充值
func (h *Handlers) shopCreatePayment(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.payment == nil {
		Forbidden(c, "在线支付未开启")
		return
	}

	var req shopCreatePaymentReq
	if !BindJSON(c, &req) {
		return
	}

	// 限流：同一买家每分钟最多发起 10 笔，防止刷单把上游打爆
	if !h.cache.Allow(c.Request.Context(),
		"shop:pay:"+itoa(int64(buyer.ID)), 10, 60*1e9) {
		Fail(c, 429, "TOO_MANY_REQUESTS", "操作过于频繁，请稍后再试")
		return
	}

	amount := decimal.NewFromFloat(req.Amount).Round(2)

	res, err := h.payment.CreatePay(c.Request.Context(), service.CreatePayInput{
		BuyerID:  buyer.ID,
		Amount:   amount,
		PayType:  strings.TrimSpace(req.PayType),
		ClientIP: c.ClientIP(),
		UseAPI:   req.UseAPI,
	})
	if err != nil {
		h.abortPayment(c, err)
		return
	}
	OK(c, res)
}

// shopPaymentStatus 查询某笔支付的状态。
//
// 买家付完钱点「我已支付」时调它，内部会主动向上游查单并补入账。
// 这样即使异步回调丢了，买家也不会钱货两空。
func (h *Handlers) shopPaymentStatus(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.payment == nil {
		Forbidden(c, "在线支付未开启")
		return
	}

	tradeNo := strings.TrimSpace(c.Param("trade_no"))
	if tradeNo == "" {
		BadRequest(c, "缺少订单号")
		return
	}

	// 先确认这笔单属于当前买家，防止拿别人的单号探测
	if _, err := h.payment.GetBuyerPayment(c.Request.Context(), buyer.ID, tradeNo); err != nil {
		NotFound(c, "支付订单不存在")
		return
	}

	po, err := h.payment.QueryAndSettle(c.Request.Context(), tradeNo)
	if err != nil {
		// 上游暂时查不到时不算致命错误：把本地状态返回给前端，
		// 让买家看到「处理中」，稍后再试
		if p, e2 := h.payment.GetBuyerPayment(c.Request.Context(), buyer.ID, tradeNo); e2 == nil {
			OK(c, gin.H{"payment": p, "warning": err.Error()})
			return
		}
		BadRequest(c, err.Error())
		return
	}
	OK(c, gin.H{"payment": po})
}

// shopListPayments 我的在线充值记录
func (h *Handlers) shopListPayments(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	// 未开启支付时返回空列表而不是报错：前端复用同一个钱包页面
	if h.payment == nil {
		OKPage(c, []models.PaymentOrder{}, 0, 1, 20)
		return
	}

	page, size := Pagination(c)
	rows, total, err := h.payment.ListBuyerPayments(c.Request.Context(), buyer.ID, page, size)
	if err != nil {
		ServerError(c, "查询支付记录失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

// shopPaymentNotify 易支付异步通知（公开，无需登录）。
//
// 这是整个支付流程里最关键的接口：
//   - 由易支付服务器直接调用，不带我们的 JWT
//   - 必须返回纯文本 "success"，上游才认为通知成功并停止重试
//   - 返回其它任何内容都会被上游持续重试
//
// 因此这里不能走统一的 JSON 信封（Envelope），必须回纯文本。
func (h *Handlers) shopPaymentNotify(c *gin.Context) {
	if h.payment == nil {
		c.String(http.StatusOK, "fail")
		return
	}

	// 读取原始请求体，便于排障（部分易支付用 form，少数用 query）
	raw, _ := io.ReadAll(io.LimitReader(c.Request.Body, 64*1024))
	c.Request.Body = io.NopCloser(strings.NewReader(string(raw)))

	params := map[string]string{}
	// GET query 参数
	for k, v := range c.Request.URL.Query() {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}
	// POST form 参数（易支付默认是 application/x-www-form-urlencoded）
	if err := c.Request.ParseForm(); err == nil {
		for k, v := range c.Request.PostForm {
			if len(v) > 0 {
				params[k] = v[0]
			}
		}
	}

	res := h.payment.HandleNotify(c.Request.Context(), params)
	if res.Handled {
		// 必须回纯文本 success
		c.String(http.StatusOK, "success")
		return
	}
	c.String(http.StatusOK, "fail")
}

// ---------------------------------------------------------------- 错误映射

func (h *Handlers) abortPayment(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrPaymentDisabled):
		Forbidden(c, "在线支付未开启")
	case errors.Is(err, service.ErrPaymentAmountBad):
		BadRequest(c, err.Error())
	case errors.Is(err, service.ErrPaymentTooMany):
		Conflict(c, err.Error())
	default:
		// 上游错误（网络不通、签名不对、商户被停）属于网关问题，
		// 用 502 与参数错误区分开，便于前端提示「请稍后重试或联系客服」
		BadRequest(c, err.Error())
	}
}

// ================================================================
// 后台支付管理接口
// ================================================================

// listPayments 后台查支付单
func (h *Handlers) listPayments(c *gin.Context) {
	if h.payment == nil {
		OKPage(c, []models.PaymentOrder{}, 0, 1, 20)
		return
	}
	page, size := Pagination(c)
	rows, total, err := h.payment.ListPaymentsForAdmin(
		c.Request.Context(), page, size, c.Query("status"), c.Query("keyword"))
	if err != nil {
		ServerError(c, "查询支付单失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

// queryPayment 后台主动查单并补入账（处理「买家说付了但没到账」）
func (h *Handlers) queryPayment(c *gin.Context) {
	if h.payment == nil {
		Forbidden(c, "在线支付未开启")
		return
	}
	tradeNo := strings.TrimSpace(c.Param("trade_no"))
	if tradeNo == "" {
		BadRequest(c, "缺少订单号")
		return
	}

	po, err := h.payment.QueryAndSettle(c.Request.Context(), tradeNo)
	if err != nil && po == nil {
		NotFound(c, err.Error())
		return
	}

	h.auditLog(c, "payment.query", "payment_order", tradeNo, gin.H{
		"status": po.Status,
	})
	OK(c, gin.H{"payment": po, "warning": errString(err)})
}

// testPayment 测试支付配置是否可用（后台点「测试连接」）。
//
// 只做参数完整性与连通性检查，不产生真实交易。
func (h *Handlers) testPayment(c *gin.Context) {
	if h.payment == nil {
		Forbidden(c, "服务未就绪")
		return
	}
	ctx := c.Request.Context()
	pay, epayCfg := h.payment.Config(ctx)

	if !pay.Enabled {
		OK(c, gin.H{"ok": false, "message": "支付开关未打开"})
		return
	}
	if !epayCfg.Ready() {
		OK(c, gin.H{"ok": false, "message": "接口地址 / 商户ID / 密钥 未填写完整"})
		return
	}

	// 用一笔不存在的订单做查询：能连上并返回结构化响应，就说明
	// 地址、商户号、密钥都是通的（订单不存在是预期结果）。
	_, err := h.payment.QueryAndSettle(ctx, "KMP-TEST-NOT-EXIST")
	if err != nil && strings.Contains(err.Error(), "不存在") {
		OK(c, gin.H{"ok": true, "message": "接口连通，商户号可识别（测试订单不存在属正常）"})
		return
	}
	if err != nil {
		OK(c, gin.H{"ok": false, "message": "连接失败：" + err.Error()})
		return
	}
	OK(c, gin.H{"ok": true, "message": "接口连通"})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
