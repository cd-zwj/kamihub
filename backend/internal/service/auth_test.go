package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"kamihub/internal/config"
	"kamihub/internal/models"
)

func testAuth(cfgTTL time.Duration) *AuthService {
	// settings 传 nil：模拟「网页还没配过」，用于验证回落到 .env
	cfg := &config.Config{JWT: config.JWT{Secret: strings.Repeat("k", 64), TTL: cfgTTL}}
	return NewAuthService(nil, cfg, nil)
}

func sampleUser() *models.User {
	return &models.User{ID: 7, Username: "faka-partner", Role: models.RoleOperator, Status: 1}
}

// 管理员在网页上指定小时数时，签发的令牌必须真的按这个时长过期。
func TestIssueToken_自定义小时数(t *testing.T) {
	svc := testAuth(12 * time.Hour)

	issued, err := svc.IssueToken(context.Background(), sampleUser(), 720*time.Hour)
	if err != nil {
		t.Fatalf("签发失败：%v", err)
	}
	if issued.TTLHours != 720 {
		t.Errorf("TTLHours 应为 720，实际 %d", issued.TTLHours)
	}

	wantExpiry := time.Now().Add(720 * time.Hour)
	if diff := issued.ExpiresAt.Sub(wantExpiry); diff > time.Second || diff < -time.Second {
		t.Errorf("到期时间偏差过大：got %v want %v", issued.ExpiresAt, wantExpiry)
	}

	// 令牌本身要能被自己解析出来，且携带正确的用户与角色
	claims, err := svc.ParseToken(issued.Token)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if claims.UserID != 7 {
		t.Errorf("uid 应为 7，实际 %d", claims.UserID)
	}
	if claims.Role != models.RoleOperator {
		t.Errorf("role 应为 operator，实际 %s", claims.Role)
	}
	if got := int(time.Until(claims.ExpiresAt.Time).Hours()); got != 719 && got != 720 {
		t.Errorf("令牌内的 exp 应为 ~720 小时后，实际 %d 小时", got)
	}
}

// 不传小时数时，回落到当前默认有效期（网页设置 → .env）。
func TestIssueToken_零值回落到默认(t *testing.T) {
	svc := testAuth(3 * time.Hour) // settings 为 nil，应回落到 .env 的 3h

	issued, err := svc.IssueToken(context.Background(), sampleUser(), 0)
	if err != nil {
		t.Fatalf("签发失败：%v", err)
	}
	if issued.TTLHours != 3 {
		t.Errorf("应回落到 .env 的 3 小时，实际 %d", issued.TTLHours)
	}
}

// 不同时长签出的令牌必须互不相同（防止 TTL 参数被忽略）。
func TestIssueToken_不同时长不同到期(t *testing.T) {
	svc := testAuth(time.Hour)

	short, err := svc.IssueToken(context.Background(), sampleUser(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	long, err := svc.IssueToken(context.Background(), sampleUser(), 168*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !long.ExpiresAt.After(short.ExpiresAt.Add(160 * time.Hour)) {
		t.Errorf("168h 令牌的到期时间应显著晚于 1h 令牌：short=%v long=%v",
			short.ExpiresAt, long.ExpiresAt)
	}
}

func TestNormalizeJWTTTLHours(t *testing.T) {
	cases := []struct {
		in, want int
		why      string
	}{
		{0, DefaultJWTTTLHours, "老数据缺字段解出 0，必须补默认值（否则令牌立刻过期）"},
		{-5, DefaultJWTTTLHours, "负数同样补默认值"},
		{1, 1, "下界允许"},
		{12, 12, "常规值原样保留"},
		{720, 720, "30 天原样保留"},
		{MaxJWTTTLHours, MaxJWTTTLHours, "上界允许"},
		{MaxJWTTTLHours + 1, MaxJWTTTLHours, "超上限截断到一年"},
		{999999, MaxJWTTTLHours, "超大值截断"},
	}
	for _, c := range cases {
		if got := NormalizeJWTTTLHours(c.in); got != c.want {
			t.Errorf("NormalizeJWTTTLHours(%d) = %d, want %d（%s）", c.in, got, c.want, c.why)
		}
	}
}
