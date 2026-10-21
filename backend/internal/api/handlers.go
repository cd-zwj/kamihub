package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kamihub/internal/cache"
	"kamihub/internal/config"
	"kamihub/internal/render"
	"kamihub/internal/routing"
	"kamihub/internal/service"
)

// Handlers 汇总所有依赖。
type Handlers struct {
	cfg       *config.Config
	db        *gorm.DB
	cache     *cache.Client
	auth      *service.AuthService
	platforms *service.PlatformService
	goods     *service.GoodsService
	// cards 自有卡密池（自建商品发货用）
	cards      *service.CardService
	orders     *service.OrderService
	deliveries *service.DeliveryService
	settings   *service.SettingsService
	audit      *service.AuditService
	routes     *routing.Registry
	renderer   *render.Renderer

	// ---------- C 端购买平台 ----------
	buyerAuth  *service.BuyerAuthService
	listings   *service.ListingService
	wallet     *service.WalletService
	shopOrders *service.ShopOrderService
	payment    *service.PaymentService
	// apiKeys 买家自助签发的开放接口凭证（下游 / 代理）
	apiKeys *service.BuyerApiKeyService
	// tickets 买家工单（售后咨询）
	tickets *service.TicketService
	// guests 匿名购买（邮箱 + 6 位安全密码）
	guests *service.GuestService
}

// Deps 构造依赖
type Deps struct {
	Config     *config.Config
	DB         *gorm.DB
	Cache      *cache.Client
	Auth       *service.AuthService
	Platforms  *service.PlatformService
	Goods      *service.GoodsService
	Cards      *service.CardService
	Orders     *service.OrderService
	Deliveries *service.DeliveryService
	Settings   *service.SettingsService
	Audit      *service.AuditService
	Routes     *routing.Registry
	Renderer   *render.Renderer

	// ---------- C 端购买平台 ----------
	BuyerAuth  *service.BuyerAuthService
	Listings   *service.ListingService
	Wallet     *service.WalletService
	ShopOrders *service.ShopOrderService
	Payment    *service.PaymentService
	// ApiKeys 买家自助签发的开放接口凭证（下游 / 代理）
	ApiKeys *service.BuyerApiKeyService
	// Tickets 买家工单
	Tickets *service.TicketService
	// Guests 匿名购买服务
	Guests *service.GuestService
}

func NewHandlers(d Deps) *Handlers {
	return &Handlers{
		cfg: d.Config, db: d.DB, cache: d.Cache,
		auth: d.Auth, platforms: d.Platforms, goods: d.Goods, cards: d.Cards,
		orders: d.Orders, deliveries: d.Deliveries, settings: d.Settings,
		audit: d.Audit, routes: d.Routes, renderer: d.Renderer,

		buyerAuth: d.BuyerAuth, listings: d.Listings,
		wallet: d.Wallet, shopOrders: d.ShopOrders,
		payment: d.Payment,
		apiKeys: d.ApiKeys,
		tickets: d.Tickets,
		guests:  d.Guests,
	}
}

// Register 挂载全部路由。
func (h *Handlers) Register(r *gin.Engine) {
	r.Use(RequestID(), AccessLog(), Recovery(), CORS(nil))

	// ---------- 健康检查 ----------
	r.GET("/health", h.health)

	// ---------- 提货页（公开，token 即凭证） ----------
	r.GET("/d/:token", h.deliveryPage)
	r.POST("/d/:token", h.deliveryUnlock)

	// ---------- 上传的图片（公开读） ----------
	// 商品封面必须能被买家看到，所以读是公开的；
	// 写（上传）在 admin 组里，见下方。
	// gin 的 Static 内部用 http.Dir，自带路径穿越防护。
	r.Static(h.cfg.Upload.URLPrefix, h.cfg.Upload.Dir)

	r.GET("/robots.txt", func(c *gin.Context) {
		// 提货页绝不能被收录
		c.String(http.StatusOK, "User-agent: *\nDisallow: /d/\n")
	})

	api := r.Group("/api")

	// ---------- 公开接口 ----------
	api.POST("/auth/login", h.login)

	// ================================================================
	// C 端购买平台（/api/shop）
	//
	// 独立前缀 + 独立中间件。买家令牌只在这里被接受，
	// 下面 /api 的员工接口一律拒绝买家令牌，反之亦然。
	// ================================================================
	shop := api.Group("/shop")

	// 公开：商品浏览与买家注册登录
	shop.GET("/listings", h.shopListListings)
	shop.GET("/listings/:id", h.shopGetListing)

	// ---------- 匿名购买 / 匿名查单（公开，凭邮箱 + 6 位安全密码）----------
	//
	// 不注册账号也能买：下单时用邮箱 + 自设的 6 位安全密码，
	// 之后凭同样的「邮箱 + 密码」查自己的订单与提货链接。
	//
	// 全部用 POST + JSON body：6 位密码绝不能進 URL，
	// 否则会同时泄露到 nginx 访问日志、浏览器历史和 Referer 里。
	shop.POST("/guest/purchase", h.guestPurchase)
	shop.POST("/guest/payment/status", h.guestPaymentStatus)
	shop.POST("/guest/orders", h.guestListOrders)
	shop.POST("/guest/order", h.guestGetOrder)

	// 实时报价：前端改数量时调用，返回折后单价/合计/命中档位
	shop.GET("/listings/:id/quote", h.shopQuoteListing)
	shop.POST("/auth/register", h.shopRegister)
	shop.POST("/auth/login", h.shopLogin)

	// 站点信息（品牌 + 客服联系方式）：帮助页与页脚要用，
	// 未登录也要能看，所以放在公开组里。
	shop.GET("/site", h.shopSiteInfo)

	// 在线支付（易支付）
	//
	// notify 必须放在公开路由：它是易支付服务器直接回调的地址，
	// 不带也不可能有我们的 JWT。安全性由「MD5 验签 + 金额核对 + 幂等」
	// 三重保障，而不是靠鉴权中间件。
	shop.GET("/payment/info", h.shopPaymentInfo)
	shop.POST("/payment/notify", h.shopPaymentNotify)
	// 部分易支付站点会用 GET 回调，两种方法都要接
	shop.GET("/payment/notify", h.shopPaymentNotify)

	// 需买家登录
	shopAuthed := shop.Group("")
	shopAuthed.Use(BuyerAuth(h.buyerAuth))
	{
		shopAuthed.GET("/auth/me", h.shopMe)
		shopAuthed.POST("/auth/logout", h.shopLogout)
		shopAuthed.POST("/auth/password", h.shopChangePassword)

		shopAuthed.GET("/wallet", h.shopWallet)
		shopAuthed.GET("/wallet/transactions", h.shopWalletTransactions)
		shopAuthed.POST("/wallet/recharge", h.shopRecharge)

		// 在线支付
		shopAuthed.POST("/payment/create", h.shopCreatePayment)
		shopAuthed.GET("/payment/status/:trade_no", h.shopPaymentStatus)
		shopAuthed.GET("/payments", h.shopListPayments)

		shopAuthed.POST("/orders", h.shopPurchase)
		shopAuthed.GET("/orders", h.shopListOrders)
		shopAuthed.GET("/orders/:id", h.shopGetOrder)

		// 下游 / 代理：买家自助管理开放接口凭证
		shopAuthed.GET("/api-keys", h.shopListApiKeys)
		shopAuthed.POST("/api-keys", h.shopCreateApiKey)
		shopAuthed.POST("/api-keys/:id/reveal", h.shopRevealApiKey)
		shopAuthed.POST("/api-keys/:id/reset", h.shopResetApiKey)
		shopAuthed.POST("/api-keys/:id/status", h.shopSetApiKeyStatus)
		shopAuthed.POST("/api-keys/:id/rename", h.shopRenameApiKey)
		shopAuthed.DELETE("/api-keys/:id", h.shopDeleteApiKey)

		// ---------- 工单（售后咨询）----------
		// 必须登录才能提单：既能自动关联身份防刷，
		// 也能在提单时自动带入订单上下文，运营不用来回问订单号。
		shopAuthed.POST("/tickets/upload", h.shopTicketUploadImage)
		shopAuthed.POST("/tickets", h.shopCreateTicket)
		shopAuthed.GET("/tickets", h.shopListTickets)
		shopAuthed.GET("/tickets/unread", h.shopTicketUnread)
		shopAuthed.GET("/tickets/:id", h.shopGetTicket)
		shopAuthed.POST("/tickets/:id/reply", h.shopReplyTicket)
	}

	// ================================================================
	// 开放接口（下游 / 代理用签名调用）
	//
	// 与 /api/shop 完全独立的一套鉴权：这里用 app_id + secret 的
	// HMAC-SHA256 签名，而不是买家登录令牌。
	//
	// 为什么不复用 Bearer 令牌：签名方案里 secret 不随请求发送，
	// 抓包也拿不到可复用的凭证；再加上时间窗与 nonce 去重，
	// 旧请求被重放会被直接拒掉。这是给「系统对系统」用的形态。
	// ================================================================
	open := api.Group("/open")
	open.Use(h.openAPIAuth())
	{
		open.POST("/goods", h.openListGoods)
		open.POST("/purchase", h.openPurchase)
		open.POST("/order", h.openGetOrder)
		open.POST("/balance", h.openBalance)
	}

	// ---------- 需登录 ----------
	authed := api.Group("")
	authed.Use(Auth(h.auth))
	{
		authed.GET("/auth/me", h.me)
		authed.POST("/auth/logout", h.logout)
		authed.POST("/auth/password", h.changePassword)
		authed.GET("/dashboard/stats", h.dashboardStats)

		authed.GET("/strategies", h.listStrategies)
		authed.GET("/adapters", h.listAdapters)

		authed.GET("/platforms", h.listPlatforms)
		authed.GET("/platforms/:id", h.getPlatform)
		authed.GET("/platforms/:id/goods", h.listPlatformGoods)

		authed.GET("/goods", h.listGoods)
		authed.GET("/goods/:id", h.getGoods)
		authed.GET("/goods/:id/cards", h.listCards)
		authed.GET("/goods/:id/card-stats", h.cardStats)

		authed.GET("/orders", h.listOrders)
		authed.GET("/orders/:id", h.getOrder)
		authed.GET("/orders/:id/attempts", h.listOrderAttempts)
		authed.POST("/orders/:id/retry", h.retryOrder)
		authed.POST("/orders/purchase", h.createOrder)

		authed.GET("/deliveries", h.listDeliveries)
		authed.GET("/deliveries/:id", h.getDelivery)
		authed.POST("/deliveries/:id/revoke", h.revokeDelivery)
		authed.POST("/deliveries/:id/extend", h.extendDelivery)
		authed.POST("/deliveries/:id/password", h.setDeliveryPassword)
		authed.GET("/deliveries/:id/views", h.listDeliveryViews)

		authed.GET("/settings", h.getSettings)

		authed.GET("/call-logs", h.listCallLogs)
		authed.GET("/audit-logs", h.listAuditLogs)

		// ---------- 买家工单（售后）----------
		authed.GET("/tickets", h.adminListTickets)
		authed.GET("/tickets/unread", h.adminTicketUnread)
		authed.GET("/tickets/:id", h.adminGetTicket)
		authed.POST("/tickets/:id/reply", h.adminReplyTicket)
		authed.POST("/tickets/:id/status", h.adminSetTicketStatus)
	}

	// ---------- 仅管理员 ----------
	admin := api.Group("")
	admin.Use(Auth(h.auth), AdminOnly())
	{
		admin.POST("/platforms", h.createPlatform)
		admin.PUT("/platforms/:id", h.updatePlatform)
		admin.DELETE("/platforms/:id", h.deletePlatform)
		admin.POST("/platforms/:id/test", h.testPlatform)

		// 撤销订单会改动卡密库存与营收统计，所以归管理员，与卡密池写接口同级。
		admin.POST("/orders/:id/cancel", h.cancelOrder)

		admin.POST("/goods", h.createGoods)
		admin.PUT("/goods/:id", h.updateGoods)
		admin.DELETE("/goods/:id", h.deleteGoods)
		admin.POST("/goods/:id/supplies", h.createSupply)
		admin.PUT("/supplies/:id", h.updateSupply)
		admin.DELETE("/supplies/:id", h.deleteSupply)
		admin.POST("/supplies/:id/sync-stock", h.syncSupplyStock)
		admin.POST("/supplies/:id/fetch-instruction", h.fetchSupplyInstruction)

		// ---------- 自有卡密池（自建商品）----------
		// 读接口挂 authed（操作员要能查库存），写接口挂 admin（卡密是钱）
		admin.POST("/goods/:id/cards", h.importCards)
		admin.POST("/cards/:id/void", h.voidCard)
		admin.POST("/cards/:id/restore", h.restoreCard)
		admin.DELETE("/cards/:id", h.deleteCard)
		admin.POST("/goods/:id/cards/delete-batch", h.deleteCardBatch)

		admin.PUT("/settings/:key", h.updateSetting)

		admin.GET("/users", h.listUsers)
		admin.POST("/users", h.createUser)
		admin.PUT("/users/:id", h.updateUser)
		admin.DELETE("/users/:id", h.deleteUser)
		admin.POST("/users/:id/reset-password", h.resetPassword)

		// 令牌签发：只有管理员能给别人签长期令牌
		admin.GET("/auth/token-policy", h.getTokenPolicy)
		admin.POST("/auth/tokens", h.issueToken)

		// ---------- C 端运营管理 ----------
		// 买家管理
		admin.GET("/buyers", h.listBuyers)
		admin.GET("/buyers/:id", h.getBuyer)
		admin.PUT("/buyers/:id", h.updateBuyer)
		admin.POST("/buyers/:id/balance", h.adjustBuyerBalance)
		admin.GET("/buyers/:id/transactions", h.listBuyerTransactions)

		// C 端上架管理（定价与上架控制）
		admin.GET("/listings", h.listListings)
		admin.GET("/listings/:id", h.getListing)
		admin.POST("/listings", h.createListing)
		admin.PUT("/listings/:id", h.updateListing)
		admin.DELETE("/listings/:id", h.deleteListing)
		// 阶梯折扣：整体替换语义（后台表格编辑完一次性保存）
		admin.GET("/listings/:id/tiers", h.adminGetListingTiers)
		admin.PUT("/listings/:id/tiers", h.adminSetListingTiers)

		// 充值码
		admin.GET("/recharge-codes", h.listRechargeCodes)
		admin.POST("/recharge-codes", h.generateRechargeCodes)
		admin.POST("/recharge-codes/:id/void", h.voidRechargeCode)

		// C 端订单与运营概览
		admin.GET("/shop-orders", h.listShopOrders)
		admin.GET("/shop-stats", h.shopDashboardStats)

		// 在线支付管理
		// 支付配置存在 settings 表的 payment 键里，走已有的 /settings 接口读写；
		// 这里只加支付单的查询、补单与连通性测试。
		admin.GET("/payments", h.listPayments)
		admin.POST("/payments/:trade_no/query", h.queryPayment)
		admin.POST("/payment/test", h.testPayment)

		// 图片上传（商品封面等）
		admin.POST("/upload/image", h.uploadImage)
	}

	r.NoRoute(func(c *gin.Context) {
		NotFound(c, "接口不存在")
	})
}

func (h *Handlers) health(c *gin.Context) {
	dbOK := true
	if sqlDB, err := h.db.DB(); err != nil || sqlDB.Ping() != nil {
		dbOK = false
	}
	redisOK := h.cache.Ping(c.Request.Context()) == nil

	status := http.StatusOK
	if !dbOK {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, Envelope{
		Code: 1, Msg: "ok", Time: now(),
		Data: gin.H{
			"database": dbOK,
			"redis":    redisOK,
			"env":      h.cfg.App.Env,
		},
	})
}

// audit 便捷写审计日志
func (h *Handlers) auditLog(c *gin.Context, action, targetType, targetID string, detail any) {
	user := CurrentUser(c)
	var uid *uint
	username := ""
	if user != nil {
		uid = &user.ID
		username = user.Username
	}
	h.audit.Record(c.Request.Context(), uid, username, action, targetType, targetID, detail, c.ClientIP())
}

var _ = slog.Default
