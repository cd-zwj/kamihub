// Command server 是 kamihub 的进程入口。
//
// 启动顺序：加载配置 → 初始化日志 → 连数据库/Redis → 迁移 → 初始化数据
//
//	→ 注册平台适配器 → 组装服务 → 挂载路由 → 监听
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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

	// ---------- 上传目录 ----------
	// 提前创建并**验证可写**。
	//
	// 只 MkdirAll 是不够的：目录可能已存在但属于别的用户
	// （挂载命名卷时很常见 —— Docker 会以 root 新建挂载点，
	// 而进程以非 root 的 app 用户运行）。
	// 那种情况下要等运营真正上传图片才会报错，排查成本高；
	// 这里在启动阶段就写一个探针文件，问题当场暴露。
	if err := ensureWritableDir(cfg.Upload.Dir); err != nil {
		return fmt.Errorf("上传目录不可写 %s: %w", cfg.Upload.Dir, err)
	}
	slog.Info("上传目录就绪", "dir", cfg.Upload.Dir, "max_mb", cfg.Upload.MaxSizeMB)

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
	// 卡密池：自建商品（不带上游货源）靠它本地发货
	cards := service.NewCardService(db)
	goods := service.NewGoodsService(db, platforms, cards, cfg.Upstream.VerifySupply)
	deliveries := service.NewDeliveryService(db, cfg, settings)
	orders := service.NewOrderService(db, cfg, c, platforms, settings, deliveries, routes, cards)
	auth := service.NewAuthService(db, cfg, settings)
	audit := service.NewAuditService(db)

	// ---------- C 端购买平台 ----------
	// 复用思路：采购与发卡继续走 orders（生产验证过的链路），
	// C 端只新增「买家身份 / 上架定价 / 钱包」三块，不改动原有采购逻辑。
	buyerAuth := service.NewBuyerAuthService(db, cfg)
	listings := service.NewListingService(db, cards)
	wallet := service.NewWalletService(db)
	shopOrders := service.NewShopOrderService(db, listings, wallet, orders)
	// 在线支付（易支付）：默认关闭，需管理员在后台「系统设置 → 在线支付」里
	// 填好商户参数并打开开关后才会对买家可见。
	//
	// 传入 shopOrders 是为了支持「直付购卡」：买家不必先充值，
	// 直接支付宝付款，到账后自动扣余额下单发货（失败则钱留在余额）。
	// 依赖方向 shopOrders → wallet/orders，payment → shopOrders，不成环。
	payment := service.NewPaymentService(db, cfg, settings, wallet, shopOrders)

	// 开放接口凭证：买家在用户页面自助签发 app_id + secret，
	// 用 HMAC-SHA256 签名调用 /api/open/*（下游 / 代理模式）。
	apiKeys := service.NewBuyerApiKeyService(db, cfg)

	// 买家工单（售后咨询）：提单时自动带入订单上下文，运营打开就能判断问题
	tickets := service.NewTicketService(db)

	// 匿名购买：不注册账号也能买，用「邮箱 + 6 位安全密码」下单与查单。
	// 匿名买家仍然落在 buyers 表（is_guest=true），因此完全复用
	// 现有支付与自动发货链路，支付核心零改动。
	guests := service.NewGuestService(db)

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
		Cards:      cards,
		Orders:     orders,
		Deliveries: deliveries,
		Settings:   settings,
		Audit:      audit,
		Routes:     routes,
		Renderer:   renderer,

		BuyerAuth:  buyerAuth,
		Listings:   listings,
		Wallet:     wallet,
		ShopOrders: shopOrders,
		Payment:    payment,
		ApiKeys:    apiKeys,
		Tickets:    tickets,
		Guests:     guests,
	})

	// ---------- HTTP ----------
	engine := gin.New()
	if cfg.App.TrustProxy {
		// 请求链路：外层 Nginx(宿主) -> web 容器内的 Nginx -> 本进程。
		// 两跳都是容器私网地址，真实访客 IP 只在 X-Forwarded-For 的最左端。
		// 注意：gin 默认只信任回环地址，且传 nil 会彻底关闭代理解析，
		// 那样 c.ClientIP() 只能取到上一跳的容器 IP（172.22.0.x），
		// 提货页访问日志、登录限流、审计日志都会记错人。
		if err := engine.SetTrustedProxies([]string{
			"127.0.0.1/32",
			"::1/128",
			// Docker 默认 bridge / compose 自定义网络的私网段
			"10.0.0.0/8",
			"172.16.0.0/12",
			"192.168.0.0/16",
		}); err != nil {
			return fmt.Errorf("设置可信代理失败: %w", err)
		}
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

	// ---------- 支付单超时巡检 ----------
	//
	// 过期未支付的单子必须被标记掉，否则会永久占用
	// max_pending_orders 的额度，买家撞上限后再也发不起新支付。
	//
	// 注意：janitor 用独立的 stop channel，不复用 quit。
	// 若共用 quit，两个 goroutine 抢读同一个 channel，
	// 信号只会被其中一个收到（Go 的 channel 是单播），
	// 另一个就永远等不到退出。这里显式 fan-out。
	janitorStop := make(chan struct{})
	janitorDone := make(chan struct{})
	go func() {
		defer close(janitorDone)
		startPaymentExpiryJanitor(payment, janitorStop)
	}()

	<-quit

	slog.Info("收到退出信号，正在优雅关闭")
	close(janitorStop) // 通知巡检停止
	<-janitorDone      // 等它真的退出，避免日志写到一半

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("关闭 HTTP 服务超时", "error", err.Error())
	}
	slog.Info("已退出")
	return nil
}

// ensureWritableDir 创建目录并实际写一个探针文件，确认进程有写权限。
//
// 为什么不能只 MkdirAll：目录可能已存在但没有写权限
// （挂载命名卷时 Docker 会以 root 新建挂载点，而进程是非 root 用户）。
// MkdirAll 对已存在的目录直接返回成功，于是问题被推迟到
// 运营上传图片时才暴露 —— 那时排查起来麻烦得多。
func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString("ok"); err != nil {
		f.Close()
		_ = os.Remove(probe)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(probe)
		return err
	}
	return os.Remove(probe)
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

// ---------------------------------------------------------------- 支付单超时巡检

// paymentExpiryInterval 巡检间隔。
//
// 5 分钟足够：支付单有效期是 30 分钟，即使晚关几轮，
// 也不会让买家的额度长期被占。间隔太短纯属空跑。
const paymentExpiryInterval = 5 * time.Minute

// startPaymentExpiryJanitor 周期性把超时未支付的支付单标记为 expired。
//
// 为什么需要它：Create() 用「进行中支付单数量」做防刷上限，
// 而计数只看 status=pending。没有巡检的话，买家点了几次支付却没付，
// 这些单会永远停在 pending，把额度占满后买家再也发不起新支付。
//
// 并发安全：ExpireStale 是一条带条件的 UPDATE，天然幂等，
// 多实例或多轮重入都不会重复处理。
//
// 退出：监听 stop，收到后立即返回。
func startPaymentExpiryJanitor(
	payment *service.PaymentService,
	stop <-chan struct{},
) {
	ticker := time.NewTicker(paymentExpiryInterval)
	defer ticker.Stop()

	// 启动先立刻跑一次：进程重启前可能已经积压了一批死单
	sweep := func() {
		// 单次巡检给 30 秒，数据库卡住时不要拖住下一个周期
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		n, err := payment.ExpireStale(ctx)
		if err != nil {
			// 失败只记日志：下一轮会重试，绝不能因为一次失败就退出
			slog.Warn("支付单超时巡检失败", "error", err.Error())
			return
		}
		if n > 0 {
			slog.Info("已关闭超时未支付的支付单", "count", n)
		}
	}

	sweep()
	for {
		select {
		case <-stop:
			slog.Info("支付单巡检已停止")
			return
		case <-ticker.C:
			sweep()
		}
	}
}
