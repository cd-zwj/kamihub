package service

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"kamihub/internal/models"
	"kamihub/internal/notify"
	"kamihub/internal/pkg/crypto"
)

// 测试用密码常量（HashPassword 要求至少 6 位）
const (
	pwOK  = "correct-password"
	pwBad = "wrong-password"
)

// ---------------------------------------------------------------- S2 二次确认

// 正确密码能拿到票据，且票据绑在自己账号上。
func TestConfirmIssueAndCheck(t *testing.T) {
	db := testDB(t)
	c := testRedis(t)
	ctx := context.Background()

	user := mkConfirmUser(t, db, "confirm-ok", pwOK)
	svc := NewConfirmService(db, c)

	ticket, expires, err := svc.Issue(ctx, user.ID, pwOK)
	if err != nil {
		t.Fatalf("正确密码应能签发票据: %v", err)
	}
	if ticket == "" || expires.IsZero() {
		t.Fatal("票据与过期时间都不应为空")
	}
	if !svc.Check(ctx, user.ID, ticket) {
		t.Fatal("刚签发的票据应当有效")
	}

	// 票据绑 user_id：换个账号用同一张票必须无效
	other := mkConfirmUser(t, db, "confirm-other", "another-password")
	if svc.Check(ctx, other.ID, ticket) {
		t.Fatal("票据必须绑定用户，别的账号不能用")
	}
	if svc.Check(ctx, user.ID, "") {
		t.Fatal("空票据必须无效")
	}
	if svc.Check(ctx, user.ID, "not-a-real-ticket") {
		t.Fatal("伪造票据必须无效")
	}
}

// 密码错误不发票，且连续错 N 次后锁定 —— 否则这个接口就是一个
// 「在已登录状态下无限猜密码」的预言机。
func TestConfirmRejectsWrongPasswordAndLocks(t *testing.T) {
	db := testDB(t)
	c := testRedis(t)
	ctx := context.Background()

	user := mkConfirmUser(t, db, "confirm-bad", pwOK)
	svc := NewConfirmService(db, c)

	for i := 0; i < ConfirmMaxFail; i++ {
		if _, _, err := svc.Issue(ctx, user.ID, pwBad); !errors.Is(err, ErrConfirmPasswordWrong) {
			t.Fatalf("第 %d 次错误密码应返回 ErrConfirmPasswordWrong，实际 %v", i+1, err)
		}
	}
	// 达到上限后：即使密码正确也要先被锁住
	if _, _, err := svc.Issue(ctx, user.ID, pwOK); !errors.Is(err, ErrConfirmLocked) {
		t.Fatalf("连续失败 %d 次后应锁定，实际 %v", ConfirmMaxFail, err)
	}
}

// 成功确认后要清零失败计数，否则正常用户会被自己之前的输错拖进锁定。
func TestConfirmSuccessResetsFailCount(t *testing.T) {
	db := testDB(t)
	c := testRedis(t)
	ctx := context.Background()

	user := mkConfirmUser(t, db, "confirm-reset", pwOK)
	svc := NewConfirmService(db, c)

	// 错 4 次（还没到 5 次上限）
	for i := 0; i < ConfirmMaxFail-1; i++ {
		if _, _, err := svc.Issue(ctx, user.ID, pwBad); err == nil {
			t.Fatal("错误密码不应成功")
		}
	}
	// 第 5 次用对的
	if _, _, err := svc.Issue(ctx, user.ID, pwOK); err != nil {
		t.Fatalf("未达上限时正确密码应当成功: %v", err)
	}
	// 计数已清零：再错 4 次仍然不会被锁
	for i := 0; i < ConfirmMaxFail-1; i++ {
		if _, _, err := svc.Issue(ctx, user.ID, pwBad); err == nil {
			t.Fatal("错误密码不应成功")
		}
	}
	if _, _, err := svc.Issue(ctx, user.ID, pwOK); err != nil {
		t.Fatalf("成功确认应清零失败计数，实际 %v", err)
	}
}

// 关闭开关后所有确认都放行 —— 应急时不必改代码发版。
func TestConfirmDisabledAllowsEverything(t *testing.T) {
	db := testDB(t)
	c := testRedis(t)
	ctx := context.Background()

	user := mkConfirmUser(t, db, "confirm-off", pwOK)
	svc := NewConfirmService(db, c).WithSwitch(func(context.Context) bool { return false })

	if svc.Required(ctx) {
		t.Fatal("开关关闭时 Required 应为 false")
	}
	if !svc.Check(ctx, user.ID, "any-ticket-value") {
		t.Fatal("关闭后不带票据也应放行")
	}
}

// 作废后票据立即失效（退出登录时用）。
func TestConfirmRevoke(t *testing.T) {
	db := testDB(t)
	c := testRedis(t)
	ctx := context.Background()

	user := mkConfirmUser(t, db, "confirm-revoke", pwOK)
	svc := NewConfirmService(db, c)

	ticket, _, err := svc.Issue(ctx, user.ID, pwOK)
	if err != nil {
		t.Fatal(err)
	}
	svc.Revoke(ctx, user.ID, ticket)
	if svc.Check(ctx, user.ID, ticket) {
		t.Fatal("作废后票据必须立即失效")
	}
}

// ---------------------------------------------------------------- S3 新 IP 提醒

// 首次出现的 IP 要落库并推一条告警；同一个 IP 再来不能重复报。
func TestLoginWatchAlertsOnlyOnNewIP(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	truncateLoginWatch(t, db)

	user := mkConfirmUser(t, db, "watch-user", pwOK)
	notifier := notify.New(db, notify.DefaultConfig())
	svc := NewLoginWatchService(db).WithNotifier(notifier)

	// 第一次：新 IP
	svc.Record(ctx, user, "203.0.113.10", "Mozilla/5.0 TestBrowser")
	if n := countNotifications(t, db, notify.EventLoginNewIP); n != 1 {
		t.Fatalf("首次新 IP 应推 1 条告警，实际 %d", n)
	}
	if known, err := svc.Known(ctx, user.ID, "203.0.113.10"); err != nil || !known {
		t.Fatalf("IP 应已被记录: known=%v err=%v", known, err)
	}

	// 同一 IP 再次登录：不重复告警
	svc.Record(ctx, user, "203.0.113.10", "Mozilla/5.0 TestBrowser")
	if n := countNotifications(t, db, notify.EventLoginNewIP); n != 1 {
		t.Fatalf("同一 IP 不应重复告警，实际 %d 条", n)
	}

	// 换一个 IP：又要报 —— 这是「异地登录」的核心信号
	svc.Record(ctx, user, "198.51.100.7", "Mozilla/5.0 Other")
	if n := countNotifications(t, db, notify.EventLoginNewIP); n != 2 {
		t.Fatalf("换新 IP 应再报一条，实际 %d 条", n)
	}

	// 命中次数要累加，供排障看
	var row models.UserLoginIP
	if err := db.Where("user_id = ? AND ip = ?", user.ID, "203.0.113.10").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.HitCount != 2 {
		t.Fatalf("同一 IP 登录两次，hit_count 应为 2，实际 %d", row.HitCount)
	}
}

// 告警关闭时仍然记录 IP（只是不推送）—— 这样关了以后还能查历史。
func TestLoginWatchDisabledStillRecords(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	truncateLoginWatch(t, db)

	user := mkConfirmUser(t, db, "watch-off", pwOK)
	svc := NewLoginWatchService(db).
		WithNotifier(notify.New(db, notify.DefaultConfig())).
		WithSwitch(func(context.Context) bool { return false })

	svc.Record(ctx, user, "203.0.113.99", "Mozilla/5.0 UA")
	if n := countNotifications(t, db, notify.EventLoginNewIP); n != 0 {
		t.Fatalf("关闭告警后不应推送，实际 %d 条", n)
	}
	if known, _ := svc.Known(ctx, user.ID, "203.0.113.99"); !known {
		t.Fatal("关闭告警后仍应记录 IP")
	}
}

// 空 IP 不记录也不报警（某些代理场景下取不到）。
func TestLoginWatchIgnoresEmptyIP(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	truncateLoginWatch(t, db)

	user := mkConfirmUser(t, db, "watch-empty", pwOK)
	svc := NewLoginWatchService(db).WithNotifier(notify.New(db, notify.DefaultConfig()))

	svc.Record(ctx, user, "   ", "Mozilla/5.0 UA")
	if n := countNotifications(t, db, notify.EventLoginNewIP); n != 0 {
		t.Fatalf("空 IP 不应告警，实际 %d 条", n)
	}
}

// ---------------------------------------------------------------- 小工具

func mkConfirmUser(t *testing.T, db *gorm.DB, username, password string) *models.User {
	t.Helper()
	db.Where("username = ?", username).Delete(&models.User{})
	hash, err := crypto.HashPassword(password)
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	u := &models.User{
		Username: username, PasswordHash: hash,
		Role: models.RoleAdmin, Status: models.UserStatusEnabled,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	return u
}

func truncateLoginWatch(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, tbl := range []string{"user_login_ips", "notifications", "users"} {
		if err := db.Exec("TRUNCATE TABLE " + tbl + " RESTART IDENTITY CASCADE").Error; err != nil {
			t.Fatalf("清空 %s 失败: %v", tbl, err)
		}
	}
}

func countNotifications(t *testing.T, db *gorm.DB, event string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.Notification{}).Where("event = ?", event).Count(&n).Error; err != nil {
		t.Fatalf("统计通知失败: %v", err)
	}
	return n
}
