package service

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

// 密钥轮换对 TOTP 的影响是整条链路里最危险的地方：
// 用户手机上已经装好了那个密钥，服务端一旦解不开，
// 该账号就永远登不进去（只能靠一次性恢复码或人工重置）。
//
// 本测试模拟一次真实轮换：
//  1. 用密钥 A 绑定 TOTP
//  2. 主密钥换成 B，A 降级为 ENCRYPTION_KEY_OLD
//  3. B 配置下必须仍能验证通过
//  4. 重新加密后，即使彻底清掉 A，也必须仍然可用
func TestTOTPSurvivesKeyRotation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ensureRecoveryTable(t, db)

	const keyA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-stage1"
	const keyB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-stage2"

	user := mkRotateUser(t, db, "rotate-totp")

	// ---- 阶段 1：用密钥 A 绑定 ----
	cfgA := testTOTPConfig()
	cfgA.Crypto = config.Crypto{Key: keyA}
	svcA := NewTOTPService(db, cfgA, nil)

	setup, err := svcA.Setup(ctx, user.ID)
	if err != nil {
		t.Fatalf("阶段1 Setup 失败: %v", err)
	}
	if _, err := svcA.Confirm(ctx, user.ID, mustCode(t, setup.Secret)); err != nil {
		t.Fatalf("阶段1 Confirm 失败: %v", err)
	}

	var before models.User
	if err := db.First(&before, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if before.TOTPSecret == "" || before.TOTPSecret == setup.Secret {
		t.Fatal("totp_secret 应已加密存储")
	}

	// ---- 阶段 2：主密钥换成 B，A 只用于解密 ----
	cfgB := testTOTPConfig()
	cfgB.Crypto = config.Crypto{Key: keyB, OldKeys: []string{keyA}}
	svcB := NewTOTPService(db, cfgB, nil)

	if _, err := svcB.Verify(ctx, user.ID, mustCode(t, setup.Secret)); err != nil {
		t.Fatalf("轮换窗口内验证失败（旧密钥解不开）: %v", err)
	}

	// ---- 阶段 3：模拟 keytool rotate 重新加密 ----
	if err := reencryptTOTPWithKeyring(db, cfgB, user.ID); err != nil {
		t.Fatalf("重新加密失败: %v", err)
	}

	var after models.User
	if err := db.First(&after, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.TOTPSecret == before.TOTPSecret {
		t.Fatal("重新加密后密文应当改变（GCM 带随机 nonce，必然不同）")
	}

	// ---- 阶段 4：彻底清掉旧密钥，仍然可用 ----
	cfgC := testTOTPConfig()
	cfgC.Crypto = config.Crypto{Key: keyB}
	svcC := NewTOTPService(db, cfgC, nil)

	if _, err := svcC.Verify(ctx, user.ID, mustCode(t, setup.Secret)); err != nil {
		t.Fatalf("清掉旧密钥后验证失败: %v", err)
	}
}

// 换密钥却不配 ENCRYPTION_KEY_OLD 时，验证必须明确失败。
// 这条断言同时证明了「密钥确实参与了加密」—— 否则整个 S6 都是假命题。
func TestTOTPFailsWithoutOldKey(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ensureRecoveryTable(t, db)

	user := mkRotateUser(t, db, "rotate-totp-nokey")

	cfgA := testTOTPConfig()
	cfgA.Crypto = config.Crypto{Key: "cccccccccccccccccccccccccccccccc-stage1"}
	svcA := NewTOTPService(db, cfgA, nil)

	setup, err := svcA.Setup(ctx, user.ID)
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	if _, err := svcA.Confirm(ctx, user.ID, mustCode(t, setup.Secret)); err != nil {
		t.Fatalf("Confirm 失败: %v", err)
	}

	// 换成完全不相干的密钥，且不配置旧密钥
	cfgWrong := testTOTPConfig()
	cfgWrong.Crypto = config.Crypto{Key: "dddddddddddddddddddddddddddddddd-unrelated"}
	svcWrong := NewTOTPService(db, cfgWrong, nil)

	if _, err := svcWrong.Verify(ctx, user.ID, mustCode(t, setup.Secret)); err == nil {
		t.Fatal("换了密钥又没配旧密钥时验证必须失败")
	}
}

// 平台凭证同理：旧密钥加密的凭证在轮换窗口内必须仍可解出。
func TestPlatformCredentialsSurviveKeyRotation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	const keyA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-cred1"
	const keyB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-cred2"

	ringA := crypto.MustNewKeyring(keyA)
	svcA := NewPlatformService(db, nil, ringA)

	enc, err := svcA.EncryptCredentials(map[string]string{
		"api_key": "secret-value", "api_secret": "s3cr3t",
	})
	if err != nil {
		t.Fatalf("加密凭证失败: %v", err)
	}

	// 轮换窗口：B 为主，A 只解密
	ringB := crypto.MustNewKeyring(keyB, keyA)
	svcB := NewPlatformService(db, nil, ringB)

	got, err := svcB.DecryptCredentials(enc)
	if err != nil {
		t.Fatalf("轮换窗口内凭证应可解: %v", err)
	}
	if got["api_key"] != "secret-value" || got["api_secret"] != "s3cr3t" {
		t.Fatalf("凭证内容不一致: %+v", got)
	}

	// 用 B 重新加密后，彻底丢掉 A 也应可用
	enc2, err := svcB.EncryptCredentials(got)
	if err != nil {
		t.Fatal(err)
	}
	if enc2 == enc {
		t.Fatal("重新加密后密文应当改变")
	}
	ringC := crypto.MustNewKeyring(keyB)
	svcC := NewPlatformService(db, nil, ringC)
	got2, err := svcC.DecryptCredentials(enc2)
	if err != nil {
		t.Fatalf("清掉旧密钥后凭证应可解: %v", err)
	}
	if got2["api_key"] != "secret-value" {
		t.Fatalf("凭证内容不一致: %+v", got2)
	}

	_ = ctx
}

// ---------- 测试小工具 ----------

// ensureRecoveryTable testDB 的 AutoMigrate 里没有 user_recovery_codes，补上。
func ensureRecoveryTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&models.UserRecoveryCode{}); err != nil {
		t.Fatalf("建 user_recovery_codes 失败: %v", err)
	}
}

// mkRotateUser 创建一个启用状态的普通用户。
func mkRotateUser(t *testing.T, db *gorm.DB, username string) *models.User {
	t.Helper()
	db.Where("username = ?", username).Delete(&models.User{})
	u := &models.User{
		Username: username, PasswordHash: "x",
		Role: models.RoleAdmin, Status: models.UserStatusEnabled,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	return u
}

// reencryptTOTPWithKeyring 复现 keytool rotate 对 users.totp_secret 的处理：
// 用 keyring（主密钥 + 历史密钥）解出明文，再用主密钥重新加密写回。
func reencryptTOTPWithKeyring(db *gorm.DB, cfg *config.Config, userID uint) error {
	ring, err := cfg.Crypto.Keyring()
	if err != nil {
		return err
	}
	var user models.User
	if err := db.First(&user, userID).Error; err != nil {
		return err
	}
	plain, err := ring.Decrypt(user.TOTPSecret)
	if err != nil {
		return err
	}
	enc, err := ring.Encrypt(plain)
	if err != nil {
		return err
	}
	return db.Model(&models.User{}).Where("id = ?", userID).
		Update("totp_secret", enc).Error
}

var _ = time.Now
