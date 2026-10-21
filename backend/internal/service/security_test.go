package service

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"kamihub/internal/cache"
	"kamihub/internal/config"
)

// ---------------------------------------------------------------- 测试夹具

// testRedis 返回测试用 Redis 客户端（独立 DB 15，避免污染开发数据）。
func testRedis(t *testing.T) *cache.Client {
	t.Helper()
	addr := os.Getenv("KAMIHUB_TEST_REDIS")
	if addr == "" {
		t.Skip("未设置 KAMIHUB_TEST_REDIS，跳过需要 Redis 的用例")
	}
	cfg := &config.Config{}
	cfg.Redis.Addr = addr
	cfg.Redis.DB = 15
	c := cache.New(cfg)
	if err := c.Raw().FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("清空测试 Redis 失败: %v", err)
	}
	return c
}

func testTOTPConfig() *config.Config {
	return &config.Config{
		JWT: config.JWT{
			Secret: "test-jwt-secret-for-totp",
			TTL:    time.Hour,
		},
		Crypto: config.Crypto{Key: "test-encryption-key-32-bytes-min"},
	}
}

// ---------------------------------------------------------------- 恢复码规范化

// 恢复码必须能容忍用户抄写时的各种分隔符与大小写 ——
// 用户是从纸上/屏幕上手打的，区分大小写和连字符只会让恢复失败。
func TestNormalizeRecoveryCode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ABCD-EFGH-IJKL", "ABCDEFGHIJKL"},
		{"abcd efgh ijkl", "ABCDEFGHIJKL"},
		{"abcd_efgh_ijkl", "ABCDEFGHIJKL"},
		{"  ABCD-EFGH-IJKL  ", "ABCDEFGHIJKL"},
		{"ABCD中文EFGH", "ABCDEFGH"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeRecoveryCode(c.in); got != c.want {
			t.Errorf("normalize(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 同一个恢复码无论怎么分隔，哈希必须一致，否则用户输入正确也验不过。
func TestRecoveryCodeHashStable(t *testing.T) {
	a := hashRecoveryCode(normalizeRecoveryCode("ABCD-EFGH-IJKL"))
	b := hashRecoveryCode(normalizeRecoveryCode("abcdefghijkl"))
	if a != b {
		t.Error("不同分隔/大小写的同一恢复码，哈希应一致")
	}
	if a == hashRecoveryCode(normalizeRecoveryCode("ABCD-EFGH-IJKM")) {
		t.Error("不同恢复码不应产生相同哈希")
	}
}

// 生成的恢复码必须是 12 位有效字符，分组格式 4-4-4
func TestFormatRecoveryCode(t *testing.T) {
	plain := []string{"ABCDEFGHIJKL", "123456789012", "abcdefghijkl"}
	for _, p := range plain {
		got := formatRecoveryCode(p)
		if len(got) != 14 { // 12 字符 + 2 个连字符
			t.Errorf("format(%q) = %q，长度 %d，期望 14", p, got, len(got))
		}
		if normalizeRecoveryCode(got) != normalizeRecoveryCode(p) {
			t.Errorf("format(%q) = %q，规范化后与原文不一致", p, got)
		}
	}
}

// ---------------------------------------------------------------- TOTP 动态码

// 生成的动态码必须能被自己验过，且错误码必须被拒。
// 这条守的是「绑定流程能不能跑通」——写错参数（位数/周期/算法）会让
// 用户手机上显示正确、服务端却始终报错。
func TestTOTPVerifyCode(t *testing.T) {
	svc := NewTOTPService(nil, testTOTPConfig(), nil)
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer: "kamihub", AccountName: "tester", Period: TOTPPeriod, SecretSize: 20,
		Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCodeCustom(key.Secret(), time.Now().UTC(), totp.ValidateOpts{
		Period: TOTPPeriod, Skew: TOTPSkew, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !svc.verifyCode(key.Secret(), code) {
		t.Error("正确的动态码应通过校验")
	}

	// 明显错误的码
	bad := "000000"
	if code == bad {
		bad = "111111"
	}
	if svc.verifyCode(key.Secret(), bad) {
		t.Error("错误的动态码不应通过校验")
	}
	// 位数不对
	if svc.verifyCode(key.Secret(), "123") {
		t.Error("位数不足的动态码不应通过校验")
	}
	if svc.verifyCode(key.Secret(), "") {
		t.Error("空动态码不应通过校验")
	}
	// 换个密钥后原码必须失效
	other, _ := totp.Generate(totp.GenerateOpts{
		Issuer: "kamihub", AccountName: "other", Period: TOTPPeriod, SecretSize: 20,
		Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if svc.verifyCode(other.Secret(), code) {
		t.Error("用别人的密钥不应验证通过")
	}
}

// ---------------------------------------------------------------- 验证码

// 验证码必须：能正确校验、且**用一次就失效**。
// 一次性是这套防护的全部意义 —— 可重放的验证码等于没有。
func TestCaptchaOneTimeUse(t *testing.T) {
	rdb := testRedis(t)
	svc := NewCaptchaService(rdb)
	ctx := context.Background()

	ch, err := svc.Issue(ctx)
	if err != nil {
		t.Fatalf("生成验证码失败: %v", err)
	}
	if ch.ID == "" || ch.Question == "" {
		t.Fatalf("验证码题目/ID 为空: %+v", ch)
	}

	// 从题目里解出答案（题目形如 "12 + 5 = ?"）
	ans := solveArithmetic(t, ch.Question)

	if err := svc.Verify(ctx, ch.ID, ans); err != nil {
		t.Fatalf("正确答案应通过: %v", err)
	}
	// 第二次必须失败：答案已被 GetDel 原子取走
	if err := svc.Verify(ctx, ch.ID, ans); err == nil {
		t.Error("同一个验证码不应能重复使用")
	}
}

// 错误答案要被拒，且要计入失败次数
func TestCaptchaWrongAnswer(t *testing.T) {
	rdb := testRedis(t)
	svc := NewCaptchaService(rdb)
	ctx := context.Background()

	ch, err := svc.Issue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Verify(ctx, ch.ID, "999999"); err == nil {
		t.Error("错误答案应被拒")
	}
	// 答错会消费掉答案吗？不会 —— GetDel 也执行了，所以答案已失效。
	// 这是刻意的：不允许「猜错再猜」，否则 3 位数字可以被爆破。
	ans := solveArithmetic(t, ch.Question)
	if err := svc.Verify(ctx, ch.ID, ans); err == nil {
		t.Error("答错一次后，答案应已作废（不允许反复猜）")
	}
}

// 连续答错够次数后，该 session 应被临时封禁
func TestCaptchaLockAfterFailures(t *testing.T) {
	rdb := testRedis(t)
	svc := NewCaptchaService(rdb)
	ctx := context.Background()

	// 用一个固定 id 直接打失败计数
	id := "locktest"
	for i := 0; i < CaptchaMaxFail; i++ {
		svc.recordFail(ctx, id)
	}
	if err := svc.checkLocked(ctx, id); err == nil {
		t.Error("连续失败达到阈值后应被封禁")
	}

	// 未达阈值的另一个 session 不受影响（封禁是按 session 的，不是全局）
	if err := svc.checkLocked(ctx, "other"); err != nil {
		t.Error("其他 session 不应被连带封禁")
	}
}

// 不存在的 id 应当报「已过期」而不是「答案错误」——
// 这两种情况给用户的提示完全不同（刷新 vs 重填）。
func TestCaptchaExpired(t *testing.T) {
	rdb := testRedis(t)
	svc := NewCaptchaService(rdb)
	ctx := context.Background()

	if err := svc.Verify(ctx, "no-such-id", "1"); err != ErrCaptchaExpired {
		t.Errorf("期望 ErrCaptchaExpired，实际 %v", err)
	}
}

func solveArithmetic(t *testing.T, question string) string {
	t.Helper()
	var a, b int
	var op string
	if _, err := fmt.Sscanf(question, "%d %s %d", &a, &op, &b); err != nil {
		t.Fatalf("无法解析验证码题目 %q: %v", question, err)
	}
	if op == "+" {
		return itoaTest(int64(a + b))
	}
	return itoaTest(int64(a - b))
}
