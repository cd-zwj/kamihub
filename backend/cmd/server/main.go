// Command server 是 kamihub 的进程入口。
//
// 启动顺序：加载配置 → 初始化日志 → 连数据库/Redis → 迁移 → 初始化数据
//          → 注册平台适配器 → 组装服务 → 挂载路由 → 监听
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"kamihub/internal/api"
	"kamihub/internal/cache"
	"kamihub/internal/config"
	"kamihub/internal/database"
	"kamihub/internal/logger"
	"kamihub/internal/platform"
	"kamihub/internal/platform/f16688"
	"kamihub/internal/platform/mock"
	"kamihub/internal/render"
	"kamihub/internal/routing"
	"kamihub/internal/service"
)

func main() {
	if err := run(); err != nil {
		slog.Error("服务启动失败", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	// ---------- 配置 ----------
	cfg, err := config.Load()
	if err != nil {
		// 此时 logger 还没初始化，直接写 stderr
		return err
	}
	logger.Setup(cfg.App.LogLevel, cfg.IsProduction())
	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	slog.Info("启动 kamihub",
		"env", cfg.App.Env,
		"port", cfg.App.Port,
		"base_url", cfg.App.BaseURL,
		"delivery_ttl", cfg.Delivery.DefaultTTL.String(),
	)

	// ---------- 数据库 ----------
	db, err := database.Connect(cfg)
	if err != nil {
		return err
	}
	slog.Info("数据库连接成功")

	if cfg.App.AutoMigrate {
		if err := database.Migrate(db); err != nil {
			return err
		}
		slog.Info("数据库迁移完成")
	}

	seed, err := database.Seed(db, cfg)
	if err != nil {
		return err
	}
	if seed.AdminCreated {
		if seed.AdminPassword != "" {
			// 自动生成的初始密码只在这里输出一次
			slog.Warn("已创建默认管理员，请立即登录并修改密码",
				"username", seed.AdminUsername,
				"password", seed.AdminPassword)
		} else {
			slog.Info("已创建默认管理员", "username", seed.AdminUsername)
		}
	}

	// ---------- Redis ----------
	c := cache.New(cfg)
	defer func() { _ = c.Close() }()

	// ---------- 平台适配器注册 ----------
	registry := platform.NewRegistry()
	registry.Register(f16688.New())
	registry.Register(mock.New())
	// 新增平台在这里注册即可，上层业务无需改动
	slog.Info("已注册平台适配器", "codes", registry.Codes())

	// ---------- 服务组装 ----------
	routes := routing.NewRegistry()
	settings := service.NewSettingsService(db)
	platforms := service.NewPlatformService(db, registry, cfg.Crypto.Key)
	goods := service.NewGoodsService(db, platforms, cfg.Upstream.VerifySupply)
	deliveries := service.NewDeliveryService(db, cfg, settings)
	orders := service.NewOrderService(db, cfg, c, platforms, settings, deliveries, routes)
	auth := service.NewAuthService(db, cfg, settings)
	audit := service.NewAuditService(db)

	renderer, err := render.New()
	if err != nil {
		return err
	}

	handlers := api.NewHandlers(api.Deps{
		Config:     cfg,
		DB:         db,
		Cache:      c,
		Auth:       auth,
		Platforms:  platforms,
		Goods:      goods,
		Orders:     orders,
		Deliveries: deliveries,
		Settings:   settings,
		Audit:      audit,
		Routes:     routes,
		Renderer:   renderer,
	})

	// ---------- HTTP ----------
	engine := gin.New()
	if cfg.App.TrustProxy {
		_ = engine.SetTrustedProxies(nil)
	}
	handlers.Register(engine)

	srv := &http.Server{
		Addr:              listenAddr(cfg),
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second, // 采购含轮询等待，要给够
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		slog.Info("HTTP 服务已就绪", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP 服务异常退出", "error", err.Error())
			os.Exit(1)
		}
	}()

	// ---------- 优雅退出 ----------
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("收到退出信号，正在优雅关闭")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("关闭 HTTP 服务超时", "error", err.Error())
	}
	slog.Info("已退出")
	return nil
}

func listenAddr(cfg *config.Config) string {
	host := os.Getenv("APP_HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	return host + ":" + itoa(cfg.App.Port)
}

func itoa(v int) string {
	if v == 0 {
		return "8080"
	}
	digits := []byte{}
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
