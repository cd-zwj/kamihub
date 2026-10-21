package service

import "testing"

// 下单验证码策略的归一化。
//
// 这条守的是「默认值必须是安全的」：老配置里没有 order_captcha_mode 这个键，
// 解码出来是空串。如果空串被当成 off，那么升级一次就把下单口的所有防护静默关掉了
// —— 这是最难发现的一类回归（功能都正常，只是没人守门了）。
func TestNormalizeOrderCaptchaMode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", OrderCaptchaReview}, // 老配置缺键 → 默认要求（安全侧）
		{OrderCaptchaOff, OrderCaptchaOff},
		{OrderCaptchaReview, OrderCaptchaReview},
		{OrderCaptchaAlways, OrderCaptchaAlways},
		{"bogus", OrderCaptchaReview}, // 配错 → 回落到安全默认
		{"OFF", OrderCaptchaReview},   // 大小写不匹配也算配错
	}
	for _, c := range cases {
		if got := NormalizeOrderCaptchaMode(c.in); got != c.want {
			t.Errorf("NormalizeOrderCaptchaMode(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 安全开关的「反向字段」语义：零值必须是「防护开启」。
//
// 这几个方法是 settings 与 handler 之间的唯一约定。
// 一旦有人把 CaptchaOn() 写成 !CaptchaDisabled 以外的逻辑，
// 升级后旧配置（JSON 里没有这些键）就会静默关掉全站防护。
func TestSecuritySettingsDefaultsAreSafe(t *testing.T) {
	// 零值 = 老配置解码结果（没有任何新字段）
	var zero SecuritySettings

	if !zero.CaptchaOn() {
		t.Error("零值配置下验证码必须是开启的（captcha_disabled 默认 false）")
	}
	if !zero.RiskOn() {
		t.Error("零值配置下风控必须是开启的（risk_disabled 默认 false）")
	}
	if zero.AntiRedEnabled {
		t.Error("防红是新增行为，默认必须是关闭的（不改变现状）")
	}

	// 显式关闭
	off := SecuritySettings{CaptchaDisabled: true, RiskDisabled: true}
	if off.CaptchaOn() {
		t.Error("显式 captcha_disabled=true 时验证码应关闭")
	}
	if off.RiskOn() {
		t.Error("显式 risk_disabled=true 时风控应关闭")
	}
}

// 验证码 provider 未实现时必须回落到内置实现，而不是「什么都不验」。
//
// 这是安全默认：运营在设置里选了 geetest 但没接密钥时，
// 正确行为是「仍然有一道弱一点的验证码」，绝不能变成「没有验证码」。
func TestCaptchaProviderFallback(t *testing.T) {
	svc := NewCaptchaService(nil) // provider 判断不需要 Redis

	if got := svc.Provider(""); got != CaptchaProviderArithmetic {
		t.Errorf("空配置应回落到 arithmetic，实际 %q", got)
	}
	if got := svc.Provider(CaptchaProviderArithmetic); got != CaptchaProviderArithmetic {
		t.Errorf("arithmetic 应保持不变，实际 %q", got)
	}
	for _, unsupported := range []string{"geetest", "turnstile", "whatever"} {
		if got := svc.Provider(unsupported); got != CaptchaProviderArithmetic {
			t.Errorf("未实现的 provider %q 应回落到 arithmetic，实际 %q", unsupported, got)
		}
	}
}
