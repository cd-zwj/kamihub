package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kamihub/internal/cache"
	"kamihub/internal/config"
	"kamihub/internal/notify"
	"kamihub/internal/render"
	"kamihub/internal/routing"
	"kamihub/internal/service"
	"kamihub/internal/worker"
)

// antiRedPage 防红提示页。
//
// 刻意做成极简、无外链、不依赖 JS：这个页面出现时，
// 用户的浏览器正处在「被平台判定为风险环境」的状态，
// 任何外部资源都可能加载失败或再次触发拦截。
// 只做一件事：告诉用户「复制链接到浏览器打开」。
const antiRedPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>请用浏览器打开</title>
<style>
  body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
       background:#f5f7fa;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif;color:#1f2937}
  .box{max-width:420px;padding:32px 24px;background:#fff;border-radius:12px;box-shadow:0 2px 12px rgba(0,0,0,.06);text-align:center}
  h1{font-size:19px;margin:0 0 12px}
  p{font-size:14px;line-height:1.7;color:#4b5563;margin:0 0 10px}
  .tip{font-size:13px;color:#6b7280;margin-top:16px}
</style></head>
<body><div class="box">
  <h1>请用手机浏览器打开</h1>
  <p>当前页面在微信 / QQ 内置浏览器中无法正常访问。</p>
  <p>请点击右上角「···」，选择<b>「在浏览器打开」</b>。</p>
  <p class="tip">如需帮助，请联系站点客服。</p>
</div></body></html>`

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

	// ---------- 迭代 1：运维地基 ----------
	reconcile *service.ReconcileService
	notifier  *notify.Notifier
	worker    *worker.Runner

	// ---------- 迭代 2：安全加固 ----------
	totp    *service.TOTPService
	captcha *service.CaptchaService
	risk    *service.RiskService

	// ---------- 迭代 3：销售阶梯专价 ----------
	pricing *service.PricingService
	agent   *service.AgentService

	// ---------- 迭代 5：敏感操作二次确认 ----------
	confirm *service.ConfirmService
	// ---------- 迭代 5：新 IP 登录提醒 ----------
	loginWatch *service.LoginWatchService
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

	// ---------- 迭代 1：运维地基 ----------
	Reconcile    *service.ReconcileService
	Notify       *notify.Notifier
	WorkerRunner *worker.Runner

	// ---------- 迭代 2：安全加固 ----------
	TOTP    *service.TOTPService
	Captcha *service.CaptchaService
	Risk    *service.RiskService

	// ---------- 迭代 3：销售阶梯专价 ----------
	Pricing *service.PricingService
	Agent   *service.AgentService

	// ---------- 迭代 5：敏感操作二次确认 ----------
	Confirm *service.ConfirmService
	// ---------- 迭代 5：新 IP 登录提醒 ----------
	LoginWatch *service.LoginWatchService
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

		reconcile: d.Reconcile, notifier: d.Notify, worker: d.WorkerRunner,

		totp: d.TOTP, captcha: d.Captcha, risk: d.Risk,

		pricing: d.Pricing, agent: d.Agent,

		confirm: d.Confirm,
		loginWatch: d.LoginWatch,
	}
}

// Register 挂载全部路由。
func (h *Handlers) Register(r *gin.Engine) {
	// 防红：微信/QQ 内置浏览器打开页面时给提示页而不是被平台拦截。
	// 开关取自设置（默认关闭），每次请求现读 —— 改了立刻生效，不用重启。
	r.Use(AntiRed(func() bool {
		return h.settings.Security(context.Background()).AntiRedEnabled
	}, antiRedPage))

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
	// 二次验证第二步：用临时票据 + 动态码/恢复码换真令牌。
	// 必须公开 —— 此时调用方只有一枚 totp 票据，还没有任何可用权限。
	api.POST("/auth/totp", h.loginTOTP)
	// 验证码下发（公开）：注册页、查单页在未登录时就要用
	api.GET("/captcha", h.getCaptcha)

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

	// 验证码：挂 /api/shop/captcha 这一份，是因为 C 端 axios 实例的 baseURL
	// 就是 /api/shop，而前端写的是 http.get('/captcha')。
	//
	// 之前只挂了 /api/captcha，前端实际请求 /api/shop/captcha 拿到 404；
	// 前端对取题失败又是「静默忽略」（catch 里不提示），于是表现为
	// 「注册页点『获取验证码』没反应」＋「注册永远提示请先获取验证码」。
	// 两个路径同时保留：老页面与 scripts/ 里的验证脚本各用各的，都不受影响。
	shop.GET("/captcha", h.getCaptcha)

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
	// 档位说明表是公开的：它是「拉人做销售」的展示物料，不该要求登录才能看
	shop.GET("/agent/tiers", h.shopAgentTiers)

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

		// ---------- 迭代 3：销售阶梯专价 ----------
		// 普通买家也能调 /agent/me（返回 agent_status=0），
		// 前端用同一个接口渲染「申请入口 / 销售中心」两种状态。
		shopAuthed.GET("/agent/me", h.shopAgentMe)
		shopAuthed.POST("/agent/apply", h.shopAgentApply)
		shopAuthed.GET("/agent/price-list", h.shopAgentPriceList)
		shopAuthed.GET("/agent/stats", h.shopAgentStats)

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

		// ---------- 迭代 2：两步验证（自己管自己）----------
		// 都在 authed 而不是 admin：operator 也应该被允许自己绑 2FA
		authed.GET("/auth/totp/status", h.totpStatus)
		authed.POST("/auth/totp/setup", h.totpSetup)
		authed.POST("/auth/totp/confirm", h.totpConfirm)
		authed.POST("/auth/totp/disable", h.totpDisable)
		authed.POST("/auth/totp/recovery-codes", h.totpRecoveryCodes)

		// ---------- 迭代 5：敏感操作二次确认 ----------
		// 在 authed 而不是 admin：operator 也要能确认自己的操作。
		authed.GET("/auth/confirm/status", h.confirmStatus)
		authed.POST("/auth/confirm", h.confirmSensitive)
		// 已信任设备：收到「新设备登录」提醒后，来这里核对并删掉不认识的
		authed.GET("/auth/devices", h.listMyDevices)
		authed.DELETE("/auth/devices/:id", h.forgetMyDevice)

		authed.GET("/dashboard/stats", h.dashboardStats)
		// 迭代 4：图表看板数据（一次返回 6 组，避免首屏 6 次请求闪烁）
		authed.GET("/dashboard/charts", h.dashboardCharts)

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

		// ---------- 迭代 1：运维地基 ----------
		// 定时任务：查看执行记录 + 手动触发（排障用）
		admin.GET("/workers/tasks", h.listWorkerTasks)
		admin.GET("/workers/runs", h.listWorkerRuns)
		admin.POST("/workers/:task/run", h.runWorkerTask)

		// 对账：差异查看、标记已处理、手动触发
		admin.GET("/reconcile/diffs", h.listReconcileDiffs)
		admin.POST("/reconcile/diffs/:id/resolve", h.resolveReconcileDiff)
		admin.POST("/reconcile/run", h.runReconcile)

		// 运维告警 / 站内信
		admin.GET("/notifications", h.listNotifications)
		admin.POST("/notifications/:id/read", h.readNotification)
		admin.POST("/notifications/read-all", h.readAllNotifications)

		// 通知渠道状态与测试
		admin.GET("/notify/status", h.notifyStatus)
		admin.POST("/notify/test", h.testNotify)

		// ---------- 迭代 2：安全加固 ----------
		// 强制重置他人的 2FA（手机丢了 + 恢复码也没了的兜底手段）
		admin.POST("/users/:id/totp/reset", h.totpReset)

		// 风控规则：可热改（改完立刻生效，不用重启）
		admin.GET("/risk/rules", h.listRiskRules)
		admin.PUT("/risk/rules/:id", h.updateRiskRule)

		// 风控命中记录：人工复核队列 + 按主体追溯
		admin.GET("/risk/hits", h.listRiskHits)
		admin.POST("/risk/hits/:id/review", h.reviewRiskHit)

		// 黑名单
		admin.GET("/blacklist", h.listBlacklist)
		admin.POST("/blacklist", h.createBlacklist)
		admin.DELETE("/blacklist/:id", h.deleteBlacklist)

		// ---------- 迭代 3：销售阶梯专价 ----------
		// 申请审核
		admin.GET("/agent/applications", h.adminListAgentApplications)
		admin.POST("/agent/applications/:id/review", h.adminReviewAgent)
		// 销售管理
		admin.GET("/agent/buyers", h.adminListAgents)
		admin.PUT("/agent/buyers/:id", h.adminUpdateAgent)
		// 档位配置
		admin.GET("/agent/tiers", h.adminListTiers)
		admin.PUT("/agent/tiers/:id", h.adminUpdateTier)
		admin.POST("/agent/recalc", h.adminRecalcTiers)
		// 销售看板
		admin.GET("/agent/stats", h.adminAgentStats)
		// 商品级专价覆盖
		admin.GET("/agent/tier-prices", h.adminListTierPrices)
		admin.POST("/agent/tier-prices", h.adminUpsertTierPrice)
		admin.DELETE("/agent/tier-prices/:id", h.adminDeleteTierPrice)
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

// auditChange 记录「改前 → 改后」。
//
// 与 auditLog 的区别：这里传的是前后两份快照，由 service 层算出真正变化的字段，
// 并自动脱敏敏感字段。凡是「修改某项配置/数据」的动作都应该用这个，
// 否则事后只看到「有人改了设置」，看不出改成了什么。
func (h *Handlers) auditChange(
	c *gin.Context, action, targetType, targetID string,
	before, after map[string]any,
) {
	user := CurrentUser(c)
	var uid *uint
	username := ""
	if user != nil {
		uid = &user.ID
		username = user.Username
	}
	h.audit.RecordChange(c.Request.Context(), uid, username,
		action, targetType, targetID, c.ClientIP(), before, after)
}

var _ = slog.Default
