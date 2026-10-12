package cache

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"kamihub/internal/config"
)

// testCache 返回一个连到测试 Redis 的客户端。
//
// 限流依赖 Lua 脚本，用内存 map 模拟不出真实行为，所以需要真 Redis。
// 未配置时自动跳过，保证 `go test ./...` 在无 Redis 的环境不会整体失败。
func testCache(t *testing.T) *Client {
	t.Helper()
	addr := os.Getenv("KAMIHUB_TEST_REDIS")
	if addr == "" {
		t.Skip("未设置 KAMIHUB_TEST_REDIS，跳过限流测试")
	}
	cfg := &config.Config{}
	cfg.Redis.Addr = addr
	cfg.Redis.DB = 15 // 用独立的库，避免污染开发数据
	c := New(cfg)

	if err := c.Raw().FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("清空测试 Redis 失败: %v", err)
	}
	return c
}

// AllowN 只读检查，不应该改变计数。
func TestAllowNDoesNotIncrement(t *testing.T) {
	c := testCache(t)
	ctx := context.Background()
	key := "test:peek"

	for i := 0; i < 5; i++ {
		if !c.AllowN(ctx, key, 3) {
			t.Fatalf("第 %d 次检查不该被拒（计数未变）", i+1)
		}
	}
	// 5 次只读检查后仍应放行，因为没有任何一次记账
	if !c.AllowN(ctx, key, 3) {
		t.Error("AllowN 不应自增计数")
	}
}

// Fail 达到上限后 AllowN 应拒绝。
func TestFailThenAllowNRejects(t *testing.T) {
	c := testCache(t)
	ctx := context.Background()
	key := "test:fail"

	limit := 3
	for i := 0; i < limit; i++ {
		if !c.AllowN(ctx, key, limit) {
			t.Fatalf("第 %d 次不该被拒", i+1)
		}
		c.Fail(ctx, key, time.Minute)
	}
	if c.AllowN(ctx, key, limit) {
		t.Error("达到上限后应拒绝")
	}
}

// Reset 应清空计数，让正常用户立刻恢复额度。
func TestResetClearsCounter(t *testing.T) {
	c := testCache(t)
	ctx := context.Background()
	key := "test:reset"

	limit := 2
	for i := 0; i < limit; i++ {
		c.Fail(ctx, key, time.Minute)
	}
	if c.AllowN(ctx, key, limit) {
		t.Fatal("前置条件：应已达上限")
	}

	c.Reset(ctx, key)

	if !c.AllowN(ctx, key, limit) {
		t.Error("Reset 后应恢复额度")
	}
}

// Fail 只在首次记账时设置过期，窗口从第一次失败开始算。
func TestFailSetsExpiryOnlyOnce(t *testing.T) {
	c := testCache(t)
	ctx := context.Background()
	key := "test:expiry"

	c.Fail(ctx, key, 2*time.Second)
	ttl1, err := c.Raw().TTL(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl1 <= 0 {
		t.Fatalf("首次 Fail 应设置过期时间，got=%v", ttl1)
	}

	time.Sleep(300 * time.Millisecond)
	c.Fail(ctx, key, 2*time.Second)
	ttl2, err := c.Raw().TTL(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl2 > ttl1 {
		t.Errorf("后续 Fail 不应延长窗口: ttl1=%v ttl2=%v", ttl1, ttl2)
	}
}

// 窗口过后应自动恢复，这是限流能自愈的关键。
func TestWindowExpires(t *testing.T) {
	c := testCache(t)
	ctx := context.Background()
	key := "test:window"

	limit := 1
	c.Fail(ctx, key, 1*time.Second)
	if c.AllowN(ctx, key, limit) {
		t.Fatal("前置条件：应已达上限")
	}

	time.Sleep(1200 * time.Millisecond)

	if !c.AllowN(ctx, key, limit) {
		t.Error("窗口过期后应恢复放行")
	}
}

// Redis 不可达时应降级放行，而不是把整个登录功能打死。
func TestAllowNDegradesOpenOnRedisError(t *testing.T) {
	cfg := &config.Config{}
	cfg.Redis.Addr = "127.0.0.1:1" // 指向一个不存在的 Redis
	c := New(cfg)
	defer func() { _ = c.Close() }()

	if !c.AllowN(context.Background(), "test:down", 1) {
		t.Error("Redis 故障时应降级放行，避免登录功能整体不可用")
	}
}

// 保证 redis 包被引用（Raw 返回类型里出现）
var _ = redis.Nil
