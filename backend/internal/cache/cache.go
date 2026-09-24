// Package cache 封装 Redis 用途：分布式锁、限流、幂等快路径。
//
// 重要设计取舍：Redis 在这里只做「优化」，不做「正确性保证」。
// 幂等的最终保证是数据库上 request_id 的唯一索引；锁和限流只是减少无谓的上游调用。
// 因此 Redis 不可用时系统会降级（记警告继续跑），而不是整体不可用。
package cache

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"kamihub/internal/config"
)

type Client struct {
	rdb *redis.Client
}

// New 建立 Redis 连接。连接失败不阻塞启动，返回的客户端会以降级模式工作。
func New(cfg *config.Config) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.Redis.Addr,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.DB,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     20,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Warn("Redis 连接失败，锁与限流将降级（幂等仍由数据库唯一索引保证）", "error", err.Error())
	} else {
		slog.Info("Redis 连接正常", "addr", cfg.Redis.Addr)
	}
	return &Client{rdb: rdb}
}

func (c *Client) Raw() *redis.Client { return c.rdb }

func (c *Client) Close() error { return c.rdb.Close() }

// Ping 健康检查
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// releaseScript 只有持有者才能释放锁，避免误删他人的锁
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
`)

// Lock 获取一把分布式锁。
// 返回的 release 函数可安全重复调用；ok=false 表示锁已被别人持有。
// Redis 异常时返回 ok=true 与空 release（降级放行，正确性由数据库兜底）。
func (c *Client) Lock(ctx context.Context, key string, ttl time.Duration) (release func(), ok bool) {
	token := uuid.NewString()

	acquired, err := c.rdb.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		slog.Warn("获取分布式锁失败，降级放行", "key", key, "error", err.Error())
		return func() {}, true
	}
	if !acquired {
		return func() {}, false
	}

	return func() {
		// 用独立的短超时上下文，避免调用方 ctx 已取消导致锁释放不掉
		relCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := releaseScript.Run(relCtx, c.rdb, []string{key}, token).Err(); err != nil {
			slog.Warn("释放分布式锁失败（将等待 TTL 自动过期）", "key", key, "error", err.Error())
		}
	}, true
}

// allowScript 固定窗口计数限流
var allowScript = redis.NewScript(`
local current = redis.call("INCR", KEYS[1])
if current == 1 then
    redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
if current > tonumber(ARGV[2]) then
    return 0
end
return 1
`)

// Allow 固定窗口限流。limit<=0 表示不限流。
// Redis 异常时放行（降级），避免上游限流故障拖垮整个服务。
func (c *Client) Allow(ctx context.Context, key string, limit int, window time.Duration) bool {
	if limit <= 0 {
		return true
	}
	res, err := allowScript.Run(ctx, c.rdb, []string{key}, window.Milliseconds(), limit).Int()
	if err != nil {
		slog.Warn("限流检查失败，降级放行", "key", key, "error", err.Error())
		return true
	}
	return res == 1
}

// SetNX 简单占位，用于幂等快路径
func (c *Client) SetNX(ctx context.Context, key string, value string, ttl time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, key, value, ttl).Result()
}

func (c *Client) Del(ctx context.Context, keys ...string) {
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		slog.Warn("删除缓存键失败", "keys", keys, "error", err.Error())
	}
}

// Incr 计数，用于统计
func (c *Client) Incr(ctx context.Context, key string, ttl time.Duration) {
	pipe := c.rdb.Pipeline()
	pipe.Incr(ctx, key)
	if ttl > 0 {
		pipe.Expire(ctx, key, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Debug("计数失败", "key", key, "error", err.Error())
	}
}
