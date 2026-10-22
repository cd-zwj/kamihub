package api

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"

	"kamihub/internal/cache"
	"kamihub/internal/config"
)

// testCache 连一个真实 Redis（限流依赖 Lua 脚本，假实现测不出行为）。
// 未配置 KAMIHUB_TEST_REDIS 时自动跳过，保证无 Redis 环境 go test ./... 不失败。
func testCache(t *testing.T) *cache.Client {
	t.Helper()
	addr := os.Getenv("KAMIHUB_TEST_REDIS")
	if addr == "" {
		t.Skip("未设置 KAMIHUB_TEST_REDIS，跳过匿名限流测试")
	}
	cfg := &config.Config{}
	cfg.Redis.Addr = addr
	cfg.Redis.DB = 15 // 独立库，避免污染开发数据
	c := cache.New(cfg)
	if err := c.Raw().FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("清空测试 Redis 失败: %v", err)
	}
	return c
}

// newGuestCtx 造一个带客户端 IP 的 gin 上下文。
func newGuestCtx() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("POST", "/api/shop/guest/payment/status", nil)
	req.RemoteAddr = "203.0.113.9:12345"
	c.Request = req
	return c, w
}

// TestGuestGateAllowsPaymentPolling 是 2026-10-22 线上事故的回归测试。
//
// 事故：匿名接口有一道「单邮箱 8 次 / 30 分钟」的固定窗口限流，且**每个请求都计数**。
// 而付款后前端每 3 秒轮询一次支付状态 —— 买家付完款 24 秒就打满额度，
// 之后「查支付状态」和「订单查询」双双被拒 30 分钟，表现为
// 「付了钱跳不到卡密页」「订单查询提示过于频繁」。
//
// 现在频率限制只挂在 IP 上（宽松），凭据正确时不该消耗任何次数额度。
func TestGuestGateAllowsPaymentPolling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := testCache(t)
	h := &Handlers{cache: c}

	const email = "polling-buyer@example.com"
	// 一笔订单最多轮询上百次，这里取 200 次，远大于旧实现的 8 次
	for i := 0; i < 200; i++ {
		ctx, w := newGuestCtx()
		if !h.guestGate(ctx, email) {
			t.Fatalf("第 %d 次轮询被限流（status=%d body=%s），正常买家不该被自己的轮询挡住",
				i+1, w.Code, w.Body.String())
		}
	}
}

// TestGuestGateLocksAfterWrongPin 确认防爆破还在：
// 凭据连续错 5 次后，同一邮箱必须被锁住。
func TestGuestGateLocksAfterWrongPin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := testCache(t)
	h := &Handlers{cache: c}

	const email = "victim@example.com"
	for i := 0; i < guestFailThreshold; i++ {
		ctx, _ := newGuestCtx()
		h.guestRecordFail(ctx, email)
	}

	ctx, w := newGuestCtx()
	if h.guestGate(ctx, email) {
		t.Fatalf("连续失败 %d 次后仍然放行，爆破防护失效", guestFailThreshold)
	}
	if w.Code != 429 {
		t.Fatalf("锁定时应返回 429，实际 %d", w.Code)
	}
}

// TestGuestResetFailUnlocks 成功一次就清空失败计数，别让手滑的用户被自己锁住。
func TestGuestResetFailUnlocks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := testCache(t)
	h := &Handlers{cache: c}

	const email = "clumsy@example.com"
	for i := 0; i < guestFailThreshold; i++ {
		ctx, _ := newGuestCtx()
		h.guestRecordFail(ctx, email)
	}
	ctx, _ := newGuestCtx()
	h.guestResetFail(ctx, email)

	ctx2, w := newGuestCtx()
	if !h.guestGate(ctx2, email) {
		t.Fatalf("凭据成功后应立刻恢复额度（status=%d body=%s）", w.Code, w.Body.String())
	}
}
