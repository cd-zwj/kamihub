package service

import (
	"testing"

	"kamihub/internal/pkg/crypto"
)

// ---------------------------------------------------------------- 凭据格式

func TestNormalizeGuestEmail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Foo@Example.COM ", "foo@example.com"},
		{"a@b.com", "a@b.com"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeGuestEmail(c.in); got != c.want {
			t.Errorf("NormalizeGuestEmail(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateGuestCredential(t *testing.T) {
	ok := []struct{ email, pin string }{
		{"a@b.com", "123456"},
		{" Foo@Bar.com ", "000000"},
		{"x.y+z@sub.example.cn", "999999"},
	}
	for _, c := range ok {
		email, err := ValidateGuestCredential(c.email, c.pin)
		if err != nil {
			t.Errorf("应通过: email=%q pin=%q err=%v", c.email, c.pin, err)
			continue
		}
		if email != NormalizeGuestEmail(c.email) {
			t.Errorf("邮箱未归一化: got %q", email)
		}
	}

	badEmail := []string{"", "abc", "a@", "@b.com", "a b@c.com", "no-at-sign.com"}
	for _, e := range badEmail {
		if _, err := ValidateGuestCredential(e, "123456"); err == nil {
			t.Errorf("邮箱 %q 应被拒绝", e)
		}
	}

	badPin := []string{"", "12345", "1234567", "abcdef", "12 456", "12345a", "１２３４５６"}
	for _, p := range badPin {
		if _, err := ValidateGuestCredential("a@b.com", p); err == nil {
			t.Errorf("密码 %q 应被拒绝（必须是 6 位纯数字）", p)
		}
	}
}

// 6 位密码只允许 ASCII 数字：全角数字、阿拉伯数字等都不能放过去
func TestGuestPinPattern(t *testing.T) {
	if !GuestPinPattern.MatchString("123456") {
		t.Fatal("正常 6 位数字应匹配")
	}
	for _, s := range []string{"１２３４５６", "١٢٣٤٥٦", "12345", "1234567", ""} {
		if GuestPinPattern.MatchString(s) {
			t.Errorf("%q 不应匹配 6 位数字", s)
		}
	}
}

// ---------------------------------------------------------------- 占位密码

// 匿名买家的占位密码必须无法通过任何 bcrypt 校验，
// 否则匿名账号就能被登录接口登录进去。
func TestGuestPasswordPlaceholderNotVerifiable(t *testing.T) {
	if guestPasswordPlaceholder == "" {
		t.Fatal("占位密码不应为空")
	}
	for _, p := range []string{"", "123456", guestPasswordPlaceholder} {
		if crypto.VerifyPassword(guestPasswordPlaceholder, p) {
			t.Errorf("占位密码竟然通过了校验: %q", p)
		}
	}
}

// 用户名应带上 guest_ 前缀，便于运营在后台一眼区分匿名买家
func TestGuestUsernamePrefix(t *testing.T) {
	for i := 0; i < 20; i++ {
		u := guestUsername()
		if len(u) < 8 || u[:6] != "guest_" {
			t.Fatalf("用户名格式不对: %q", u)
		}
	}
}
