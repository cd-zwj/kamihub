// Package config 负责从环境变量加载并校验全部配置。
// 任何缺失或非法的配置都会让进程直接退出（fail fast），避免带病上线。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	App      App
	Database Database
	Redis    Redis
	JWT      JWT
	Crypto   Crypto
	Delivery Delivery
	Upstream Upstream
	Upload   Upload
	Worker   Worker
	Notify   Notify
}

// 运行模式
const (
	// ModeAll HTTP + 定时任务（本地开发、小规模单机）
	ModeAll = "all"
	// ModeAPI 只起 HTTP
	ModeAPI = "api"
	// ModeWorker 只跑定时任务
	ModeWorker = "worker"
)

// Worker 后台定时任务配置。
//
// 拆出独立进程的价值：定时任务卡住或崩了不影响买家下单，反之亦然；
// 也能单独重启、单独扩容。
type Worker struct {
	// Enabled 关掉后 worker 进程只做空转（便于维护窗口）
	Enabled bool
	// 各任务执行间隔
	StockInterval     time.Duration
	BalanceInterval   time.Duration
	PaymentInterval   time.Duration
	OrderInterval     time.Duration
	ReconcileInterval time.Duration
	// TaskTimeout 单次执行超时
	TaskTimeout time.Duration
	// BalanceLow 平台余额预警阈值（元）。低于该值触发告警。
	BalanceLow float64
	// StaleMinutes 订单卡在 purchasing 超过该分钟数即视为卡单
	StaleMinutes int
}

// Notify 通知渠道配置。
//
// 渠道参数走环境变量而不是 settings 表：告警通道属于「部署配置」，
// 应该在部署时确定，而不是让运营在后台误改导致告警静默。
type Notify struct {
	// Enabled 总开关
	Enabled bool
	// Telegram
	TelegramBotToken string
	TelegramChatID   string
	// Webhook
	WebhookURL    string
	WebhookSecret string
	// WebhookFormat raw / feishu / dingtalk / wecom
	WebhookFormat string
	// AggregateWindow 同类事件聚合窗口
	AggregateWindow time.Duration
	// QuietStart / QuietEnd 静默期（小时，左闭右开）
	QuietStart int
	QuietEnd   int
}

// Upload 商品图片上传。
//
// 图片存本地磁盘而不是对象存储：商品封面总量很小（几十张），
// 走本地目录最简单，也不引入外部依赖与额外费用。
// 目录通过 docker volume 持久化，容器重建不会丢。
type Upload struct {
	// Dir 保存目录。相对路径按进程工作目录解析。
	Dir string
	// MaxSizeMB 单张图片大小上限
	MaxSizeMB int
	// URLPrefix 对外访问前缀，与静态路由保持一致
	URLPrefix string
}

type App struct {
	Env         string // development / production
	// Mode 运行模式：all / api / worker
	Mode        string
	Port        int
	BaseURL     string // 对外访问地址，用于拼提货链接，如 https://api.example.com
	LogLevel    string
	AutoMigrate bool
	TrustProxy  bool
	// 首次启动时创建的默认管理员
	DefaultAdminUsername string
	DefaultAdminPassword string
}

type Database struct {
	Host            string
	Port            int
	User            string
	Password        string
	Name            string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// DSN 返回 Postgres 连接串
func (d Database) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=Asia/Shanghai",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

type Redis struct {
	Addr     string
	Password string
	DB       int
}

type JWT struct {
	Secret string
	TTL    time.Duration
}

type Crypto struct {
	// 用于加密平台凭证（AES-256-GCM）。缺失时启动会直接报错。
	Key string
}

type Delivery struct {
	// 提货链接默认有效期
	DefaultTTL time.Duration
	// token 随机字节数（base64url 后长度约为 4/3 倍）
	TokenBytes int
}

type Upstream struct {
	// 采购单「待发货」时的轮询等待
	PollTimeout  time.Duration
	PollInterval time.Duration
	// VerifySupply 创建/更新货源时，是否向上游校验商品是否存在，
	// 并用询价结果预填库存与成本缓存。
	//
	// 开启后配错上游商品编号会在「配置时」就报错，而不是等买家下单才失败。
	// 代价是每次建/改货源多 1~2 次上游调用；若某平台接口不稳定可关掉，
	// 也可以在单次请求里传 skip_verify=true 临时跳过。
	VerifySupply bool
}

func (c *Config) IsProduction() bool { return c.App.Env == "production" }

// ValidateMode 校验运行模式。
//
// 单独暴露是因为命令行 -mode 可以覆盖 APP_MODE，
// 覆盖后必须重新校验一次，否则会带着非法值跑起来。
func (c *Config) ValidateMode() error {
	switch c.App.Mode {
	case ModeAll, ModeAPI, ModeWorker:
		return nil
	default:
		return fmt.Errorf("APP_MODE 非法：%q（可选 all / api / worker）", c.App.Mode)
	}
}

// RunsAPI 该模式是否需要起 HTTP 服务
func (c *Config) RunsAPI() bool {
	return c.App.Mode == ModeAll || c.App.Mode == ModeAPI
}

// RunsWorker 该模式是否需要跑定时任务
func (c *Config) RunsWorker() bool {
	return c.App.Mode == ModeAll || c.App.Mode == ModeWorker
}

// Load 读取环境变量并校验。
//
// 会先尝试加载当前目录下的 .env / .env.local（本地开发用），
// 已存在的真实环境变量优先级更高，不会被文件覆盖 ——
// 这样容器里注入的环境变量永远说了算。
func Load() (*Config, error) {
	loadDotEnv()

	cfg := &Config{
		App: App{
			Env:                  env("APP_ENV", "production"),
			Mode:                 env("APP_MODE", ModeAll),
			Port:                 envInt("APP_PORT", 8080),
			BaseURL:              strings.TrimRight(env("APP_BASE_URL", "http://localhost:8080"), "/"),
			LogLevel:             env("LOG_LEVEL", "info"),
			AutoMigrate:          envBool("DB_AUTO_MIGRATE", true),
			TrustProxy:           envBool("TRUST_PROXY", true),
			DefaultAdminUsername: env("DEFAULT_ADMIN_USERNAME", "admin"),
			DefaultAdminPassword: env("DEFAULT_ADMIN_PASSWORD", ""),
		},
		Database: Database{
			Host:            env("DB_HOST", "127.0.0.1"),
			Port:            envInt("DB_PORT", 5432),
			User:            env("DB_USER", "kamihub"),
			Password:        env("DB_PASSWORD", ""),
			Name:            env("DB_NAME", "kamihub"),
			SSLMode:         env("DB_SSLMODE", "disable"),
			MaxOpenConns:    envInt("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    envInt("DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: envDuration("DB_CONN_MAX_LIFETIME", time.Hour),
		},
		Redis: Redis{
			Addr:     env("REDIS_ADDR", "127.0.0.1:6379"),
			Password: env("REDIS_PASSWORD", ""),
			DB:       envInt("REDIS_DB", 0),
		},
		JWT: JWT{
			Secret: env("JWT_SECRET", ""),
			TTL:    envDuration("JWT_TTL", 12*time.Hour),
		},
		Crypto: Crypto{
			Key: env("ENCRYPTION_KEY", ""),
		},
		Delivery: Delivery{
			DefaultTTL: envDuration("DELIVERY_DEFAULT_TTL", 7*24*time.Hour),
			TokenBytes: envInt("DELIVERY_TOKEN_BYTES", 32),
		},
		Upstream: Upstream{
			PollTimeout:  envDuration("UPSTREAM_POLL_TIMEOUT", 30*time.Second),
			PollInterval: envDuration("UPSTREAM_POLL_INTERVAL", 2*time.Second),
			VerifySupply: envBool("UPSTREAM_VERIFY_SUPPLY", true),
		},
		Upload: Upload{
			Dir:       env("UPLOAD_DIR", "./data/uploads"),
			MaxSizeMB: envInt("UPLOAD_MAX_MB", 5),
			URLPrefix: "/uploads",
		},
		Worker: Worker{
			Enabled:           envBool("WORKER_ENABLED", true),
			StockInterval:     envDuration("WORKER_STOCK_INTERVAL", 2*time.Minute),
			BalanceInterval:   envDuration("WORKER_BALANCE_INTERVAL", 5*time.Minute),
			PaymentInterval:   envDuration("WORKER_PAYMENT_INTERVAL", 1*time.Minute),
			OrderInterval:     envDuration("WORKER_ORDER_INTERVAL", 2*time.Minute),
			ReconcileInterval: envDuration("WORKER_RECONCILE_INTERVAL", time.Hour),
			TaskTimeout:       envDuration("WORKER_TASK_TIMEOUT", 5*time.Minute),
			BalanceLow:        envFloat("WORKER_BALANCE_LOW", 100),
			StaleMinutes:      envInt("WORKER_STALE_MINUTES", 10),
		},
		Notify: Notify{
			Enabled:          envBool("NOTIFY_ENABLED", true),
			TelegramBotToken: env("NOTIFY_TELEGRAM_BOT_TOKEN", ""),
			TelegramChatID:   env("NOTIFY_TELEGRAM_CHAT_ID", ""),
			WebhookURL:       env("NOTIFY_WEBHOOK_URL", ""),
			WebhookSecret:    env("NOTIFY_WEBHOOK_SECRET", ""),
			WebhookFormat:    env("NOTIFY_WEBHOOK_FORMAT", "raw"),
			AggregateWindow:  envDuration("NOTIFY_AGGREGATE_WINDOW", 5*time.Minute),
			QuietStart:       envInt("NOTIFY_QUIET_START", 2),
			QuietEnd:         envInt("NOTIFY_QUIET_END", 7),
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var problems []string

	if c.JWT.Secret == "" {
		problems = append(problems, "JWT_SECRET 不能为空（生成方式：openssl rand -hex 32）")
	} else if len(c.JWT.Secret) < 32 {
		problems = append(problems, fmt.Sprintf("JWT_SECRET 至少 32 位，当前 %d 位", len(c.JWT.Secret)))
	}

	if c.Crypto.Key == "" {
		problems = append(problems, "ENCRYPTION_KEY 不能为空（用于加密平台凭证，生成方式：openssl rand -hex 32）")
	} else if len(c.Crypto.Key) < 32 {
		problems = append(problems, fmt.Sprintf("ENCRYPTION_KEY 至少 32 位，当前 %d 位", len(c.Crypto.Key)))
	}

	if c.Database.Password == "" {
		problems = append(problems, "DB_PASSWORD 不能为空")
	}

	if c.App.Port < 1 || c.App.Port > 65535 {
		problems = append(problems, fmt.Sprintf("APP_PORT 非法：%d", c.App.Port))
	}

	if c.Delivery.TokenBytes < 16 {
		problems = append(problems, "DELIVERY_TOKEN_BYTES 至少 16，否则 token 可被暴力枚举")
	}

	if c.Delivery.DefaultTTL <= 0 {
		problems = append(problems, "DELIVERY_DEFAULT_TTL 必须大于 0")
	}

	if c.Upload.MaxSizeMB < 1 || c.Upload.MaxSizeMB > 50 {
		problems = append(problems, fmt.Sprintf("UPLOAD_MAX_MB 需在 1~50 之间，当前 %d", c.Upload.MaxSizeMB))
	}

	switch c.App.Mode {
	case ModeAll, ModeAPI, ModeWorker:
		// ok
	default:
		problems = append(problems, fmt.Sprintf("APP_MODE 非法：%q（可选 all / api / worker）", c.App.Mode))
	}

	if c.Worker.StockInterval <= 0 {
		problems = append(problems, "WORKER_STOCK_INTERVAL 必须大于 0")
	}
	if c.Worker.TaskTimeout <= 0 {
		problems = append(problems, "WORKER_TASK_TIMEOUT 必须大于 0")
	}
	if c.Worker.StaleMinutes <= 0 {
		problems = append(problems, "WORKER_STALE_MINUTES 必须大于 0")
	}
	if c.Notify.QuietStart < 0 || c.Notify.QuietStart > 23 || c.Notify.QuietEnd < 0 || c.Notify.QuietEnd > 23 {
		problems = append(problems, "NOTIFY_QUIET_START / NOTIFY_QUIET_END 需在 0~23 之间")
	}

	if len(problems) > 0 {
		return fmt.Errorf("配置校验失败：\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// GenerateSecret 生成一个随机 hex 串，供运维生成密钥用。
func GenerateSecret(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ---------------------------------------------------------------- env helpers

// loadDotEnv 加载 .env 文件。文件不存在时静默跳过（生产环境靠真实环境变量）。
// godotenv.Load 不会覆盖已存在的环境变量，因此容器注入的配置始终优先。
func loadDotEnv() {
	for _, name := range []string{".env", ".env.local"} {
		if _, err := os.Stat(name); err != nil {
			continue
		}
		if err := godotenv.Load(name); err != nil {
			fmt.Fprintf(os.Stderr, "警告：解析 %s 失败：%v\n", name, err)
		}
	}
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func envInt(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

func envFloat(key string, fallback float64) float64 {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return fallback
	}
	return v
}
