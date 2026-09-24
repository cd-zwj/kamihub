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
	cfg        *config.Config
	db         *gorm.DB
	cache      *cache.Client
	auth       *service.AuthService
	platforms  *service.PlatformService
	goods      *service.GoodsService
	orders     *service.OrderService
	deliveries *service.DeliveryService
	settings   *service.SettingsService
	audit      *service.AuditService
	routes     *routing.Registry
	renderer   *render.Renderer
}

// Deps 构造依赖
type Deps struct {
	Config     *config.Config
	DB         *gorm.DB
	Cache      *cache.Client
	Auth       *service.AuthService
	Platforms  *service.PlatformService
	Goods      *service.GoodsService
	Orders     *service.OrderService
	Deliveries *service.DeliveryService
	Settings   *service.SettingsService
	Audit      *service.AuditService
	Routes     *routing.Registry
	Renderer   *render.Renderer
}

func NewHandlers(d Deps) *Handlers {
	return &Handlers{
		cfg: d.Config, db: d.DB, cache: d.Cache,
		auth: d.Auth, platforms: d.Platforms, goods: d.Goods,
		orders: d.Orders, deliveries: d.Deliveries, settings: d.Settings,
		audit: d.Audit, routes: d.Routes, renderer: d.Renderer,
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
	r.GET("/robots.txt", func(c *gin.Context) {
		// 提货页绝不能被收录
		c.String(http.StatusOK, "User-agent: *\nDisallow: /d/\n")
	})

	api := r.Group("/api")

	// ---------- 公开接口 ----------
	api.POST("/auth/login", h.login)

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
	}

	// ---------- 仅管理员 ----------
	admin := api.Group("")
	admin.Use(Auth(h.auth), AdminOnly())
	{
		admin.POST("/platforms", h.createPlatform)
		admin.PUT("/platforms/:id", h.updatePlatform)
		admin.DELETE("/platforms/:id", h.deletePlatform)
		admin.POST("/platforms/:id/test", h.testPlatform)

		admin.POST("/goods", h.createGoods)
		admin.PUT("/goods/:id", h.updateGoods)
		admin.DELETE("/goods/:id", h.deleteGoods)
		admin.POST("/goods/:id/supplies", h.createSupply)
		admin.PUT("/supplies/:id", h.updateSupply)
		admin.DELETE("/supplies/:id", h.deleteSupply)
		admin.POST("/supplies/:id/sync-stock", h.syncSupplyStock)
		admin.POST("/supplies/:id/fetch-instruction", h.fetchSupplyInstruction)

		admin.PUT("/settings/:key", h.updateSetting)

		admin.GET("/users", h.listUsers)
		admin.POST("/users", h.createUser)
		admin.PUT("/users/:id", h.updateUser)
		admin.DELETE("/users/:id", h.deleteUser)
		admin.POST("/users/:id/reset-password", h.resetPassword)

		// 令牌签发：只有管理员能给别人签长期令牌
		admin.GET("/auth/token-policy", h.getTokenPolicy)
		admin.POST("/auth/tokens", h.issueToken)
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
