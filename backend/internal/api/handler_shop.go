package api

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

// ================================================================
// 买家侧（C 端）HTTP 接口
//
// 全部挂在 /api/shop 下，用独立的 BuyerAuth 中间件鉴权。
// 与后台接口在中间件层就隔离，买家令牌无法访问 /api/* 下的管理接口。
// ================================================================

// ---------------------------------------------------------------- 鉴权

type shopRegisterReq struct {
	Username string `json:"username" binding:"required"`
	Email    string `json:"email"`
	Password string `json:"password" binding:"required"`
}

// shopRegister 买家注册
func (h *Handlers) shopRegister(c *gin.Context) {
	var req shopRegisterReq
	if !BindJSON(c, &req) {
		return
	}
	buyer, err := h.buyerAuth.Register(c.Request.Context(), service.BuyerRegisterInput{
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
		IP:       c.ClientIP(),
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrBuyerExists), errors.Is(err, service.ErrBuyerEmailExists):
			Conflict(c, err.Error())
		default:
			BadRequest(c, err.Error())
		}
		return
	}

	// 注册成功后直接签发令牌，免去再登录一次
	issued, err := h.buyerAuth.IssueBuyerToken(buyer)
	if err != nil {
		ServerError(c, "签发登录凭证失败")
		return
	}
	OK(c, gin.H{
		"token":      issued.Token,
		"expires_at": issued.ExpiresAt,
		"buyer":      buyer,
	})
}

type shopLoginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// shopLogin 买家登录
//
// 带简单的失败限流：同一 IP + 用户名 15 分钟内最多 10 次失败，
// 避免被拿来撞库。
func (h *Handlers) shopLogin(c *gin.Context) {
	var req shopLoginReq
	if !BindJSON(c, &req) {
		return
	}

	// 与后台登录同构的两层限流，且只统计失败（见 handler_auth.go 的说明）：
	//  1) IP + 用户名：挡住针对某个买家的持续爆破
	//  2) 纯 IP：挡住靠换用户名绕过的撞库
	// 买家侧阈值比后台宽松，因为共享出口 IP（公司/学校/网吧）在 C 端更常见。
	ip := c.ClientIP()
	username := strings.ToLower(strings.TrimSpace(req.Username))
	ctx := c.Request.Context()

	limKey := "shop:login:" + ip + ":" + username
	ipKey := "shop:login-ip:" + ip

	if !h.cache.AllowN(ctx, limKey, 10) || !h.cache.AllowN(ctx, ipKey, 60) {
		Fail(c, 429, "TOO_MANY_REQUESTS", "登录尝试过于频繁，请 15 分钟后再试")
		return
	}

	issued, buyer, err := h.buyerAuth.BuyerLogin(ctx, req.Username, req.Password, ip)
	if err != nil {
		// 只有失败才消耗配额
		h.cache.Fail(ctx, limKey, 15*time.Minute)
		h.cache.Fail(ctx, ipKey, 15*time.Minute)
		switch {
		case errors.Is(err, service.ErrBuyerInvalidCredentials):
			Unauthorized(c, "用户名或密码错误")
		case errors.Is(err, service.ErrBuyerDisabled):
			Forbidden(c, "账号已被禁用，请联系客服")
		default:
			ServerError(c, "登录失败")
		}
		return
	}

	// 登录成功：清空失败计数
	h.cache.Reset(ctx, limKey, ipKey)
	OK(c, gin.H{
		"token":      issued.Token,
		"expires_at": issued.ExpiresAt,
		"buyer":      buyer,
	})
}

// shopMe 当前买家信息
func (h *Handlers) shopMe(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	OK(c, buyer)
}

type shopChangePasswordReq struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// shopChangePassword 买家改密码
func (h *Handlers) shopChangePassword(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	var req shopChangePasswordReq
	if !BindJSON(c, &req) {
		return
	}
	if err := h.buyerAuth.ChangeBuyerPassword(c.Request.Context(), buyer.ID, req.OldPassword, req.NewPassword); err != nil {
		BadRequest(c, err.Error())
		return
	}
	OK(c, gin.H{"message": "密码已修改"})
}

// shopLogout 买家登出
//
// JWT 无状态，服务端没有会话可清，这里只作为前端清理本地令牌的语义接口。
func (h *Handlers) shopLogout(c *gin.Context) {
	OK(c, gin.H{"message": "已登出"})
}

// ---------------------------------------------------------------- 商品

// shopListListings 商品列表（公开）
func (h *Handlers) shopListListings(c *gin.Context) {
	page, size := Pagination(c)
	list, total, err := h.listings.ListForShop(c.Request.Context(), service.ShopListingFilter{
		Keyword:  c.Query("keyword"),
		Category: c.Query("category"),
		Page:     page,
		Size:     size,
	})
	if err != nil {
		ServerError(c, "查询商品失败")
		return
	}
	OKPage(c, list, total, page, size)
}

// shopGetListing 商品详情（公开）
func (h *Handlers) shopGetListing(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	item, err := h.listings.GetForShop(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrListingNotFound) {
			NotFound(c, err.Error())
			return
		}
		ServerError(c, "查询商品失败")
		return
	}
	OK(c, item)
}

// ---------------------------------------------------------------- 钱包

// shopWallet 余额概览
func (h *Handlers) shopWallet(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	sum, err := h.wallet.Summary(c.Request.Context(), buyer.ID)
	if err != nil {
		ServerError(c, "查询余额失败")
		return
	}
	OK(c, sum)
}

// shopWalletTransactions 余额流水
func (h *Handlers) shopWalletTransactions(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	page, size := Pagination(c)
	rows, total, err := h.wallet.ListTransactions(c.Request.Context(), buyer.ID, page, size, c.Query("type"))
	if err != nil {
		ServerError(c, "查询流水失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

type shopRechargeReq struct {
	Code string `json:"code" binding:"required"`
}

// shopRecharge 充值码充值
func (h *Handlers) shopRecharge(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	var req shopRechargeReq
	if !BindJSON(c, &req) {
		return
	}

	// 限流：防止暴力枚举充值码
	if !h.cache.Allow(c.Request.Context(), "shop:recharge:"+c.ClientIP(), 20, 10*60*1e9) {
		Fail(c, 429, "TOO_MANY_REQUESTS", "操作过于频繁，请稍后再试")
		return
	}

	res, err := h.wallet.RechargeByCode(c.Request.Context(), buyer.ID, req.Code)
	if err != nil {
		if errors.Is(err, service.ErrRechargeCodeBad) {
			BadRequest(c, err.Error())
			return
		}
		Abort(c, err)
		return
	}
	OK(c, res)
}

// ---------------------------------------------------------------- 下单

type shopPurchaseReq struct {
	ListingID uint   `json:"listing_id" binding:"required"`
	Quantity  int    `json:"quantity"`
	RequestID string `json:"request_id"`
}

// shopPurchase 买家下单
func (h *Handlers) shopPurchase(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	var req shopPurchaseReq
	if !BindJSON(c, &req) {
		return
	}
	if req.Quantity <= 0 {
		req.Quantity = 1
	}
	// 单次限购上限，避免一次性下超大单把库存和余额打穿
	if req.Quantity > 1000 {
		BadRequest(c, "单次购买数量不能超过 1000")
		return
	}

	// 下单限流：同一买家每分钟最多 30 单
	if !h.cache.Allow(c.Request.Context(),
		"shop:order:"+itoa(int64(buyer.ID)), 30, 60*1e9) {
		Fail(c, 429, "TOO_MANY_REQUESTS", "下单过于频繁，请稍后再试")
		return
	}

	res, err := h.shopOrders.Purchase(c.Request.Context(), service.ShopPurchaseInput{
		BuyerID:   buyer.ID,
		ListingID: req.ListingID,
		Quantity:  req.Quantity,
		RequestID: req.RequestID,
		ClientIP:  c.ClientIP(),
	})
	if err != nil {
		h.abortShopPurchase(c, err)
		return
	}
	OK(c, res)
}

// abortShopPurchase 把下单错误映射成买家看得懂的状态码与文案。
func (h *Handlers) abortShopPurchase(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInsufficientFunds):
		// 402 Payment Required：语义上最贴切，前端可据此引导去充值
		Fail(c, 402, "INSUFFICIENT_FUNDS", "余额不足，请先充值")
	case errors.Is(err, service.ErrListingNotFound):
		NotFound(c, err.Error())
	case errors.Is(err, service.ErrListingNoStock):
		Conflict(c, err.Error())
	case errors.Is(err, service.ErrListingQtyInvalid):
		BadRequest(c, err.Error())
	case errors.Is(err, service.ErrGoodsNotFound):
		NotFound(c, "商品已下架")
	case errors.Is(err, service.ErrBuyerNotFound):
		Unauthorized(c, "账号不可用")
	default:
		BadRequest(c, err.Error())
	}
}

// ---------------------------------------------------------------- 订单

// shopListOrders 我的订单
func (h *Handlers) shopListOrders(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	page, size := Pagination(c)
	rows, total, err := h.shopOrders.ListBuyerOrders(c.Request.Context(), buyer.ID, page, size, c.Query("status"))
	if err != nil {
		ServerError(c, "查询订单失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

// shopGetOrder 订单详情
func (h *Handlers) shopGetOrder(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	view, err := h.shopOrders.GetBuyerOrder(c.Request.Context(), buyer.ID, id)
	if err != nil {
		NotFound(c, err.Error())
		return
	}
	OK(c, view)
}

// ---------------------------------------------------------------- 辅助

// 确保 models 被引用（Buyer 类型在签名中出现）
var _ = models.Buyer{}
