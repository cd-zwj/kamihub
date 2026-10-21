package service

import (
	"context"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

// ---------------------------------------------------------------- 2FA 全流程

// 这条覆盖整个绑定 → 校验 → 恢复码链路，是本迭代最重要的用例：
//
//	1. Setup 必须返回可用密钥，且**不启用**（否则扫码失败就锁死自己）
//	2. Confirm 必须校验动态码，错误码不能启用
//	3. 启用后 Verify 必须接受正确动态码
//	4. 恢复码必须能用一次，第二次必须失败
func TestTOTPFullFlow(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Exec("TRUNCATE TABLE user_recovery_codes, users RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	// testDB 的 AutoMigrate 里没有 user_recovery_codes，这里补一下
	if err := db.AutoMigrate(&models.UserRecoveryCode{}); err != nil {
		t.Fatal(err)
	}

	cfg := testTOTPConfig()
	svc := NewTOTPService(db, cfg, nil)

	user := &models.User{
		Username: "totp-admin", PasswordHash: "x",
		Role: models.RoleAdmin, Status: models.UserStatusEnabled,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}

	// ---- 1. Setup：拿到密钥，但还不能是启用状态 ----
	setup, err := svc.Setup(ctx, user.ID)
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	if setup.Secret == "" || setup.OtpauthURL == "" {
		t.Fatalf("Setup 返回不完整: %+v", setup)
	}
	var afterSetup models.User
	if err := db.First(&afterSetup, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if afterSetup.TOTPEnabled {
		t.Fatal("Setup 之后不应直接启用 —— 否则扫码失败就把自己锁死了")
	}
	if afterSetup.TOTPSecret == "" {
		t.Fatal("Setup 应把（加密的）密钥暂存下来")
	}
	if afterSetup.TOTPSecret == setup.Secret {
		t.Error("密钥必须加密存储，不能是明文")
	}

	// ---- 2. Confirm 错误码不能启用 ----
	if _, err := svc.Confirm(ctx, user.ID, "000000"); err == nil {
		t.Error("错误动态码不应确认成功")
	}

	// ---- 3. Confirm 正确码 → 启用 + 拿恢复码 ----
	code := mustCode(t, setup.Secret)
	res, err := svc.Confirm(ctx, user.ID, code)
	if err != nil {
		t.Fatalf("Confirm 失败: %v", err)
	}
	if len(res.RecoveryCodes) != RecoveryCodeCount {
		t.Errorf("应生成 %d 个恢复码，实际 %d", RecoveryCodeCount, len(res.RecoveryCodes))
	}

	var enabled models.User
	if err := db.First(&enabled, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !enabled.TOTPEnabled || enabled.TOTPBoundAt == nil {
		t.Fatal("Confirm 成功后应标记为已启用并记录绑定时间")
	}

	// ---- 4. Verify 正确动态码 ----
	usedRecovery, err := svc.Verify(ctx, user.ID, mustCode(t, setup.Secret))
	if err != nil {
		t.Fatalf("正确动态码应通过: %v", err)
	}
	if usedRecovery {
		t.Error("用动态码登录不应报告 usedRecovery")
	}

	// ---- 5. 恢复码只能用一次 ----
	rc := res.RecoveryCodes[0]
	usedRecovery, err = svc.Verify(ctx, user.ID, rc)
	if err != nil {
		t.Fatalf("恢复码应能用: %v", err)
	}
	if !usedRecovery {
		t.Error("用恢复码登录应报告 usedRecovery=true")
	}
	if _, err := svc.Verify(ctx, user.ID, rc); err == nil {
		t.Error("同一个恢复码不应能用两次")
	}

	// 其他恢复码仍然可用（只有用过的那一个作废）
	if _, err := svc.Verify(ctx, user.ID, res.RecoveryCodes[1]); err != nil {
		t.Errorf("未被使用的恢复码应仍然有效: %v", err)
	}

	// ---- 6. 状态查询 ----
	st, err := svc.Status(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || !st.Required {
		t.Errorf("admin 应为 enabled 且 required，实际 %+v", st)
	}
	if st.RecoveryLeft != RecoveryCodeCount-2 {
		t.Errorf("已用 2 个恢复码，剩 %d，实际 %d", RecoveryCodeCount-2, st.RecoveryLeft)
	}
}

// 关闭 2FA 必须验密码 —— 否则一个被劫持的会话就能把第二因素拆掉。
func TestTOTPDisableRequiresPassword(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Exec("TRUNCATE TABLE user_recovery_codes, users RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.UserRecoveryCode{}); err != nil {
		t.Fatal(err)
	}

	hash, err := crypto.HashPassword("correct-pass")
	if err != nil {
		t.Fatal(err)
	}
	user := &models.User{
		Username: "totp-disable", PasswordHash: hash,
		Role: models.RoleAdmin, Status: models.UserStatusEnabled,
		TOTPEnabled: true, TOTPSecret: "whatever",
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewTOTPService(db, testTOTPConfig(), nil)
	if err := svc.Disable(ctx, user.ID, "wrong-pass"); err == nil {
		t.Error("密码错误不应能关闭 2FA")
	}
	if err := svc.Disable(ctx, user.ID, "correct-pass"); err != nil {
		t.Fatalf("密码正确应能关闭: %v", err)
	}
	var after models.User
	if err := db.First(&after, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.TOTPEnabled || after.TOTPSecret != "" {
		t.Error("关闭后必须同时清掉 enabled 与 secret")
	}
}

// ---------------------------------------------------------------- 风控

// 播种必须幂等：重复调用不能把运营调过的阈值覆盖回出厂值。
func TestRiskSeedIdempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Exec("TRUNCATE TABLE risk_rules, risk_hits, blacklist RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.RiskRule{}, &models.RiskHit{}, &models.Blacklist{}); err != nil {
		t.Fatal(err)
	}

	svc := NewRiskService(db, nil)
	if err := svc.SeedDefaultRules(ctx); err != nil {
		t.Fatalf("播种失败: %v", err)
	}

	var count int64
	db.Model(&models.RiskRule{}).Count(&count)
	want := int64(len(DefaultRules()))
	if count != want {
		t.Fatalf("应播种 %d 条规则，实际 %d", want, count)
	}

	// 运营把阈值改掉
	if err := db.Model(&models.RiskRule{}).
		Where("code = ?", RuleIPRegisterVelocity).
		Update("priority", 999).Error; err != nil {
		t.Fatal(err)
	}

	// 再播种一次：不能覆盖
	if err := svc.SeedDefaultRules(ctx); err != nil {
		t.Fatal(err)
	}
	var again int64
	db.Model(&models.RiskRule{}).Count(&again)
	if again != want {
		t.Errorf("重复播种不应新增规则，实际 %d 条", again)
	}
	var rule models.RiskRule
	db.Where("code = ?", RuleIPRegisterVelocity).First(&rule)
	if rule.Priority != 999 {
		t.Errorf("重复播种不应覆盖运营改过的阈值，priority 实际 %d", rule.Priority)
	}

	// 8 条规则必须都在
	for _, code := range []string{
		RuleIPRegisterVelocity, RuleIPOrderVelocity, RuleBuyerOrderVelocity,
		RuleOrderAmount, RuleDailyAmount, RuleRefundRate, RuleBlacklist, RuleSelfPurchase,
	} {
		var n int64
		db.Model(&models.RiskRule{}).Where("code = ?", code).Count(&n)
		if n != 1 {
			t.Errorf("规则 %s 应存在且唯一，实际 %d 条", code, n)
		}
	}
}

// 黑名单一击即拒，且过期后自动失效（共享 IP 场景必须能自动恢复）。
func TestRiskBlacklist(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Exec("TRUNCATE TABLE risk_rules, risk_hits, blacklist RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.RiskRule{}, &models.RiskHit{}, &models.Blacklist{}); err != nil {
		t.Fatal(err)
	}

	svc := NewRiskService(db, nil)
	if err := svc.SeedDefaultRules(ctx); err != nil {
		t.Fatal(err)
	}

	// 永久拉黑一个 IP
	if err := db.Create(&models.Blacklist{
		Kind: models.BlacklistIP, Value: "10.9.9.9", Reason: "测试",
	}).Error; err != nil {
		t.Fatal(err)
	}

	d, err := svc.Evaluate(ctx, EvalInput{Scene: models.SceneOrder, IP: "10.9.9.9", BuyerID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed() {
		t.Error("命中黑名单必须拒绝")
	}
	if d.Level != models.LevelReject {
		t.Errorf("期望 reject，实际 %s", d.Level)
	}
	if len(d.Hits) == 0 {
		t.Error("应留下命中记录")
	}

	// 未拉黑的 IP 不受影响
	d2, err := svc.Evaluate(ctx, EvalInput{Scene: models.SceneOrder, IP: "10.8.8.8", BuyerID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !d2.Allowed() {
		t.Errorf("干净 IP 应放行，实际 %s", d2.Level)
	}

	// 已过期的黑名单不应生效
	exp := time.Now().Add(-time.Hour)
	if err := db.Create(&models.Blacklist{
		Kind: models.BlacklistIP, Value: "10.7.7.7", Reason: "过期测试", ExpiresAt: &exp,
	}).Error; err != nil {
		t.Fatal(err)
	}
	d3, err := svc.Evaluate(ctx, EvalInput{Scene: models.SceneOrder, IP: "10.7.7.7", BuyerID: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !d3.Allowed() {
		t.Error("已过期的黑名单不应再生效")
	}
}

// 规则热改：改完必须立刻生效，不用重启（这是验收门里明写的一条）。
func TestRiskRuleHotReload(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Exec("TRUNCATE TABLE risk_rules, risk_hits, blacklist RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.RiskRule{}, &models.RiskHit{}, &models.Blacklist{}); err != nil {
		t.Fatal(err)
	}

	svc := NewRiskService(db, nil)
	if err := svc.SeedDefaultRules(ctx); err != nil {
		t.Fatal(err)
	}

	// 出厂配置：单笔金额规则是 review
	in := EvalInput{Scene: models.SceneOrder, IP: "10.1.1.1", BuyerID: 42, Amount: 999999}
	d, err := svc.Evaluate(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Level != models.LevelReview {
		t.Fatalf("出厂配置下超额定单应为 review，实际 %s", d.Level)
	}

	// 改级别 + 关掉，然后 Invalidate（等价于后台点保存）
	if err := db.Model(&models.RiskRule{}).Where("code = ?", RuleOrderAmount).
		Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	svc.Invalidate()

	// 换一个 buyer，避开日累计规则的干扰
	d2, err := svc.Evaluate(ctx, EvalInput{Scene: models.SceneOrder, IP: "10.2.2.2", BuyerID: 43, Amount: 999999})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range d2.Hits {
		if h.RuleCode == RuleOrderAmount {
			t.Error("规则已禁用，不应再命中")
		}
	}
}

// ---------------------------------------------------------------- 小工具

func mustCode(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, time.Now().UTC(), totp.ValidateOpts{
		Period: TOTPPeriod, Skew: TOTPSkew, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("生成动态码失败: %v", err)
	}
	return code
}
