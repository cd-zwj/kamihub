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
}

type App struct {
	Env         string // development / production
	Host        string // 监听地址，如 127.0.0.1 或 0.0.0.0
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

// Load 读取环境变量并校验。
//
// 会先尝试加载当前目录下的 .env / .env.local（本地开发用），
// 已存在的真实环境变量优先级更高，不会被文件覆盖 ——
// 这样容器里注入的环境变量永远说了算。
func Load() (*Config, error) {
	loadDotEnv()

	cfg := &Config{
		App: App{
			Env:         env("APP_ENV", "production"),
			Host:        env("APP_HOST", "127.0.0.1"),
			Port:        envInt("APP_PORT", 8080),
			BaseURL:     strings.TrimRight(env("APP_BASE_URL", "http://localhost:8080"), "/"),
			LogLevel:    env("LOG_LEVEL", "info"),
			AutoMigrate: envBool("DB_AUTO_MIGRATE", false),
			// TrustProxy 默认关闭：默认监听 127.0.0.1，前面通常没有反代，
			// 信任 X-Forwarded-For 等于允许客户端伪造来源 IP。
			// 确实放在 Nginx / 云负载均衡后面时再显式打开。
			TrustProxy:           envBool("TRUST_PROXY", false),
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
