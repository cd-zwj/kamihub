// Command server 是 kamihub 的进程入口。
//
// 启动顺序：加载配置 → 初始化日志 → 连数据库/Redis → 迁移 → 初始化数据
//
//	→ 注册平台适配器 → 组装服务 → 按运行模式启动 HTTP 和/或定时任务
//
// 运行模式（-mode 或 APP_MODE）：
//
//	all     HTTP + 定时任务（本地开发、小规模单机）
//	api     只起 HTTP（生产，与 worker 分离）
//	worker  只跑定时任务（生产，与 api 分离）
//
// 为什么要拆：定时任务卡住或崩了不影响买家下单，反之亦然；
// 两者也能单独重启、单独扩容。拆开后仍然是同一个镜像、同一份代码。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kamihub/internal/api"
	"kamihub/internal/cache"
	"kamihub/internal/config"
	"kamihub/internal/database"
	"kamihub/internal/logger"
	"kamihub/internal/notify"
	"kamihub/internal/platform"
	"kamihub/internal/platform/f16688"
	"kamihub/internal/platform/mock"
	"kamihub/internal/render"
	"kamihub/internal/routing"
	"kamihub/internal/service"
	"kamihub/internal/worker"
)

func main() {
	mode := flag.String("mode", "", "运行模式：all / api / worker（覆盖 APP_MODE）")
	flag.Parse()

	if err := run(*mode); err != nil {
		slog.Error("服务启动失败", "error", err.Error())
		os.Exit(1)
	}
}

// deps 组装好的依赖。抽出来是为了让「起 HTTP」和「起 worker」共用同一份装配。
type deps struct {
	cfg        *config.Config
	db         *gorm.DB
	cache      *cache.Client
	registry   *platform.Registry
	platforms  *service.PlatformService
	goods      *service.GoodsService
	cards      *service.CardService
	orders     *service.OrderService
	settings   *service.SettingsService
	audit      *service.AuditService
	deliveries *service.DeliveryService

	buyerAuth  *service.BuyerAuthService
	listings   *service.ListingService
	wallet     *service.WalletService
	shopOrders *service.ShopOrderService
	payment    *service.PaymentService
	// ---------- 生产热修功能（合并自 prod-snapshot-20261021）----------
	// apiKeys 买家自助签发的开放接口凭证（下游 / 代理签名调用）
	apiKeys *service.BuyerApiKeyService
	// tickets 买家工单（售后咨询）
	tickets *service.TicketService
	// guests 匿名购买（邮箱 + 6 位安全密码）
	guests    *service.GuestService
	reconcile *service.ReconcileService

	notifier *notify.Notifier
	renderer *render.Renderer
	routes   *routing.Registry

	// ---------- 迭代 2：安全加固 ----------
	totp    *service.TOTPService
	captcha *service.CaptchaService
	risk    *service.RiskService

	// ---------- 迭代 3：销售阶梯专价 ----------
	pricing *service.PricingService
	agent   *service.AgentService

	// ---------- 迭代 5：S2 / S3 ----------
	confirm    *service.ConfirmService
	loginWatch *service.LoginWatchService

	// workerRunner 定时任务调度器。为 nil 表示当前模式不跑任务。
	workerRunner *worker.Runner
}

func run(modeOverride string) error {
	// ---------- 配置 ----------
	cfg, err := config.Load()
	if err != nil {
		// 此时 logger 还没初始化，直接写 stderr
		return err
	}
	// 命令行 -mode 优先级最高
	if modeOverride != "" {
		cfg.App.Mode = modeOverride
	}
	if err := cfg.ValidateMode(); err != nil {
		return err
	}

	logger.Setup(cfg.App.LogLevel, cfg.IsProduction())
	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	slog.Info("启动 kamihub",
		"env", cfg.App.Env,
		"mode", cfg.App.Mode,
		"port", cfg.App.Port,
		"base_url", cfg.App.BaseURL,
		"delivery_ttl", cfg.Delivery.DefaultTTL.String(),
		"api", cfg.RunsAPI(),
		"worker", cfg.RunsWorker(),
	)

	// ---------- 装配 ----------
	d, err := bootstrap(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if d.cache != nil {
			_ = d.cache.Close()
		}
	}()

	// ---------- 按模式启动 ----------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	if cfg.RunsAPI() {
		srv, err := startHTTP(d)
		if err != nil {
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ctx.Done()
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer shutCancel()
			if err := srv.Shutdown(shutCtx); err != nil {
				slog.Warn("关闭 HTTP 服务超时", "error", err.Error())
			}
			slog.Info("HTTP 服务已关闭")
		}()
	}

	if cfg.RunsWorker() {
		if d.workerRunner == nil {
			slog.Warn("WORKER_ENABLED=false，worker 将空转不执行任何任务")
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d.workerRunner == nil {
				<-ctx.Done()
				return
			}
			if err := d.workerRunner.Start(ctx); err != nil {
				errCh <- err
			}
		}()
	}

	// ---------- 优雅退出 ----------
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.Info("收到退出信号，正在优雅关闭", "signal", sig.String())
	case err := <-errCh:
		slog.Error("后台组件异常退出", "error", err.Error())
		cancel()
		wg.Wait()
		return err
	}

	cancel()
	wg.Wait()
	slog.Info("已退出")
	return nil
}

// bootstrap 装配全部依赖。api 与 worker 共用。
func bootstrap(cfg *config.Config) (*deps, error) {
	// ---------- 上传目录（仅 api 需要）----------
	// 提前创建并**验证可写**。
	//
	// 只 MkdirAll 是不够的：目录可能已存在但属于别的用户
	// （挂载命名卷时很常见 —— Docker 会以 root 新建挂载点，
	// 而进程以非 root 的 app 用户运行）。
	// 那种情况下要等运营真正上传图片才会报错，排查成本高；
	// 这里在启动阶段就写一个探针文件，问题当场暴露。
	if cfg.RunsAPI() {
		if err := ensureWritableDir(cfg.Upload.Dir); err != nil {
			return nil, fmt.Errorf("上传目录不可写 %s: %w", cfg.Upload.Dir, err)
		}
		slog.Info("上传目录就绪", "dir", cfg.Upload.Dir, "max_mb", cfg.Upload.MaxSizeMB)
	}

	// ---------- 数据库 ----------
	db, err := database.Connect(cfg)
	if err != nil {
		return nil, err
	}
	slog.Info("数据库连接成功")

	if cfg.App.AutoMigrate {
		if err := database.Migrate(db); err != nil {
			return nil, err
		}
		slog.Info("数据库迁移完成")
	}

	seed, err := database.Seed(db, cfg)
	if err != nil {
		return nil, err
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

	// ---------- 平台适配器注册 ----------
	registry := platform.NewRegistry()
	registry.Register(f16688.New())
	registry.Register(mock.New())
	// 新增平台在这里注册即可，上层业务无需改动
	slog.Info("已注册平台适配器", "codes", registry.Codes())

	// ---------- 通知 ----------
	notifier := buildNotifier(db, cfg)

	// ---------- 加密密钥环 ----------
	// 主密钥加密所有新数据；ENCRYPTION_KEY_OLD 只用于解密历史数据。
	// 必须全进程共享同一把 keyring，否则加密/解密行为会不一致。
	keys, err := cfg.Crypto.Keyring()
	if err != nil {
		return nil, fmt.Errorf("初始化加密密钥环失败: %w", err)
	}
	slog.Info("加密密钥环就绪", "primary_key", keys.Tag(), "key_count", keys.KeyCount())
	if keys.KeyCount() > 1 {
		slog.Warn("检测到 ENCRYPTION_KEY_OLD：当前处于密钥轮换窗口，" +
			"请跑 `keytool -mode rotate` 重新加密历史数据，完成后清空该环境变量")
	}

	// ---------- 服务组装 ----------
	routes := routing.NewRegistry()
	settings := service.NewSettingsService(db)
	platforms := service.NewPlatformService(db, registry, keys)
	// 卡密池：自建商品（不带上游货源）靠它本地发货
	cards := service.NewCardService(db)
	goods := service.NewGoodsService(db, platforms, cards, cfg.Upstream.VerifySupply)
	deliveries := service.NewDeliveryService(db, cfg, settings)
	orders := service.NewOrderService(db, cfg, c, platforms, settings, deliveries, routes, cards)
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

	// ---------- 对账 ----------
	reconcileCfg := service.DefaultReconcileConfig()
	reconcile := service.NewReconcileService(db, platforms, reconcileCfg)

	// ---------- 迭代 2：安全加固 ----------
	totpSvc := service.NewTOTPService(db, cfg, c)
	captchaSvc := service.NewCaptchaService(c)
	// 注入 notifier：reject 级命中会推告警（review 只落库不打扰）。
	// 告警自身走 notify 的聚合窗口，同一规则 5 分钟内只推一次。
	riskSvc := service.NewRiskService(db, c).WithNotifier(notifier)
	// 播种出厂风控规则（幂等，绝不覆盖运营已调过的阈值）
	if err := riskSvc.SeedDefaultRules(context.Background()); err != nil {
		slog.Error("播种默认风控规则失败", "error", err.Error())
	}

	// ---------- 迭代 3：销售阶梯专价 ----------
	// 费率取自 PAY_FEE_RATE（默认 0.0025），保底公式依赖它。
	pricingSvc := service.NewPricingService(db, cfg.Pricing.FeeRate)
	if err := pricingSvc.SeedDefaultTiers(context.Background()); err != nil {
		slog.Error("播种默认档位失败", "error", err.Error())
	}
	agentSvc := service.NewAgentService(db, pricingSvc).
		WithSwitch(func(ctx context.Context) bool {
			return settings.Security(ctx).AgentEnabled()
		})
	// 把档位定价装配进 ListingService —— 不装配时所有买家按零售价，
	// 这是「阶梯专价总开关关闭」时的等价行为。
	listings.WithPricing(pricingSvc)
	// 「先占后拿」：创建支付单时预占卡密，付款成功后直接转 sold。
	// 装配在两处 —— 支付侧负责占（CreatePay）、下单侧负责拿（Purchase）。
	payment.WithCards(cards)
	shopOrders.WithCards(cards)
	// 上游库存缓存过期时，下单前强制实时询价一次：
	// 免得「上游早没货了，商城里还显示有货」，收了钱再退款。
	listings.WithStockRefresher(goods.RefreshStaleSupplyStock)
	slog.Info("阶梯专价已装配", "fee_rate", cfg.Pricing.FeeRate,
		"enabled", agentSvc.Enabled(context.Background()))

	// ---------- 迭代 5：S2 敏感操作确认 / S3 新 IP 登录提醒 ----------
	// 两个开关都取自后台安全设置，改完立刻生效，不用重启。
	confirmSvc := service.NewConfirmService(db, c).
		WithSwitch(func(ctx context.Context) bool {
			return settings.Security(ctx).ConfirmOn()
		})
	loginWatchSvc := service.NewLoginWatchService(db).
		WithNotifier(notifier).
		WithMode(func(ctx context.Context) string {
			return settings.Security(ctx).LoginAlertMode
		})

	// ---------- 提货页渲染（仅 api 需要）----------
	var renderer *render.Renderer
	if cfg.RunsAPI() {
		renderer, err = render.New()
		if err != nil {
			_ = c.Close()
			return nil, err
		}
	}

	// ---------- 定时任务（仅 worker 需要）----------
	// 在装配阶段构造好，main 只负责 Start；
	// 同时挂到 HTTP 上，后台可手动触发单次执行（运维排障用）。
	var workerRunner *worker.Runner
	if cfg.RunsWorker() && cfg.Worker.Enabled {
		tasks := worker.BuildTasks(db, cfg, worker.Services{
			Platforms: platforms,
			Goods:     goods,
			Orders:    orders,
			Payment:   payment,
			Reconcile: reconcile,
			Agent:     agentSvc,
			// 卡密池：定时清理过期预占（买家点了去支付又关页面）
			Cards: cards,
		}, notifier)
		workerRunner = worker.New(worker.Deps{DB: db, Notify: notifier, Tasks: tasks})
		slog.Info("定时任务已装配", "count", len(tasks))
	}

	return &deps{
		cfg:          cfg,
		db:           db,
		cache:        c,
		registry:     registry,
		platforms:    platforms,
		goods:        goods,
		cards:        cards,
		orders:       orders,
		settings:     settings,
		audit:        audit,
		deliveries:   deliveries,
		buyerAuth:    buyerAuth,
		listings:     listings,
		wallet:       wallet,
		shopOrders:   shopOrders,
		payment:      payment,
		apiKeys:      apiKeys,
		tickets:      tickets,
		guests:       guests,
		reconcile:    reconcile,
		notifier:     notifier,
		totp:         totpSvc,
		captcha:      captchaSvc,
		risk:         riskSvc,
		pricing:      pricingSvc,
		agent:        agentSvc,
		confirm:      confirmSvc,
		loginWatch:   loginWatchSvc,
		renderer:     renderer,
		routes:       routes,
		workerRunner: workerRunner,
	}, nil
}

// buildNotifier 按配置装配通知渠道。
func buildNotifier(db *gorm.DB, cfg *config.Config) *notify.Notifier {
	n := notify.New(db, notify.Config{
		AggregateWindow: cfg.Notify.AggregateWindow,
		QuietStart:      cfg.Notify.QuietStart,
		QuietEnd:        cfg.Notify.QuietEnd,
		Timeout:         10 * time.Second,
	})

	if !cfg.Notify.Enabled {
		slog.Warn("通知总开关已关闭（NOTIFY_ENABLED=false），告警只写站内信")
		return n
	}

	if cfg.Notify.TelegramBotToken != "" && cfg.Notify.TelegramChatID != "" {
		n.Register(&notify.TelegramChannel{
			BotToken: cfg.Notify.TelegramBotToken,
			ChatID:   cfg.Notify.TelegramChatID,
		})
	}
	if cfg.Notify.WebhookURL != "" {
		n.Register(&notify.WebhookChannel{
			URL:    cfg.Notify.WebhookURL,
			Secret: cfg.Notify.WebhookSecret,
			Format: cfg.Notify.WebhookFormat,
		})
	}

	if len(n.Channels()) == 0 {
		slog.Warn("未配置任何外部通知渠道，告警只写站内信（后台可见）")
	}
	return n
}

// startHTTP 构造并启动 HTTP 服务。
func startHTTP(d *deps) (*http.Server, error) {
	cfg := d.cfg

	handlers := api.NewHandlers(api.Deps{
		Config:     cfg,
		DB:         d.db,
		Cache:      d.cache,
		Auth:       d.auditServiceAuth(),
		Platforms:  d.platforms,
		Goods:      d.goods,
		Cards:      d.cards,
		Orders:     d.orders,
		Deliveries: d.deliveries,
		Settings:   d.settings,
		Audit:      d.audit,
		Routes:     d.routes,
		Renderer:   d.renderer,

		BuyerAuth:  d.buyerAuth,
		Listings:   d.listings,
		Wallet:     d.wallet,
		ShopOrders: d.shopOrders,
		Payment:    d.payment,
		ApiKeys:    d.apiKeys,
		Tickets:    d.tickets,
		Guests:     d.guests,

		Reconcile:    d.reconcile,
		Notify:       d.notifier,
		WorkerRunner: d.workerRunner,

		TOTP:    d.totp,
		Captcha: d.captcha,
		Risk:    d.risk,

		Pricing: d.pricing,
		Agent:   d.agent,

		Confirm:    d.confirm,
		LoginWatch: d.loginWatch,
	})

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
			return nil, fmt.Errorf("设置可信代理失败: %w", err)
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

	return srv, nil
}

// auditServiceAuth 兼容 api.Deps 里的 Auth 字段。
func (d *deps) auditServiceAuth() *service.AuthService {
	return service.NewAuthService(d.db, d.cfg, d.settings)
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
