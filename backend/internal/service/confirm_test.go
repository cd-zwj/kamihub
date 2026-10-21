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

	for i := 0; i < ConfirmMaxFail-1; i++ {
		if _, _, err := svc.Issue(ctx, user.ID, pwBad); err == nil {
			t.Fatal("错误密码不应成功")
		}
	}
	if _, _, err := svc.Issue(ctx, user.ID, pwOK); err != nil {
		t.Fatalf("未达上限时正确密码应当成功: %v", err)
	}
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

// ---------------------------------------------------------------- S3 新设备提醒

// 核心场景（这条用例就是被真实使用场景推翻过一次的设计的回归保护）：
// 同一个浏览器、IP 每次都不一样，只能报一次 —— 不能因为换 IP 就天天提醒。
func TestLoginWatchSameDeviceChangingIPAlertsOnce(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	truncateLoginWatch(t, db)

	user := mkConfirmUser(t, db, "watch-device", pwOK)
	svc := NewLoginWatchService(db).
		WithNotifier(notify.New(db, notify.DefaultConfig())).
		WithMode(func(context.Context) string { return LoginAlertDevice })

	// 第一次登录：没有 Cookie → 新设备，报警一次，并下发设备标识
	first := svc.Record(ctx, user, "198.51.100.1", "Mozilla/5.0 Chrome/120 Windows", "")
	if !first.NewDevice || first.DeviceID == "" {
		t.Fatalf("首次登录应判为新设备并下发标识: %+v", first)
	}
	if n := countNotifications(t, db, notify.EventLoginNewDevice); n != 1 {
		t.Fatalf("首次新设备应推 1 条告警，实际 %d", n)
	}

	// 之后带着同一个 Cookie，从完全不同的 IP（模拟梯子/代理换出口）登录：
	// 必须静默，且不重复下发 Cookie
	for _, ip := range []string{"203.0.113.7", "192.0.2.44", "203.0.113.99"} {
		res := svc.Record(ctx, user, ip, "Mozilla/5.0 Chrome/120 Windows", first.DeviceID)
		if res.NewDevice {
			t.Fatalf("同一设备换 IP(%s) 不应判为新设备", ip)
		}
		if res.NewIP != true {
			t.Fatalf("IP %s 应被记为第一次见到", ip)
		}
		if res.DeviceID != "" {
			t.Fatalf("已知设备不应重复下发 Cookie，IP=%s", ip)
		}
	}
	if n := countNotifications(t, db, notify.EventLoginNewDevice); n != 1 {
		t.Fatalf("换 IP 不应重复告警，实际 %d 条", n)
	}

	// IP 历史仍要记下来，供事后追查
	ips, err := svc.IPHistory(ctx, user.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 4 {
		t.Fatalf("应记录 4 个 IP，实际 %d", len(ips))
	}
}

// 换了浏览器（没有 Cookie）就必须报 —— 这正是「攻击者拿密码在本机登录」的形状。
func TestLoginWatchNewBrowserAlerts(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	truncateLoginWatch(t, db)

	user := mkConfirmUser(t, db, "watch-browser", pwOK)
	svc := NewLoginWatchService(db).
		WithNotifier(notify.New(db, notify.DefaultConfig())).
		WithMode(func(context.Context) string { return LoginAlertDevice })

	first := svc.Record(ctx, user, "198.51.100.1", "Mozilla/5.0 Chrome/120 Windows", "")
	if n := countNotifications(t, db, notify.EventLoginNewDevice); n != 1 {
		t.Fatalf("第一台设备应告警，实际 %d", n)
	}

	// 同一 IP，但换了个浏览器（无 Cookie）
	second := svc.Record(ctx, user, "198.51.100.1", "Mozilla/5.0 Firefox/121 Linux", "")
	if !second.NewDevice {
		t.Fatal("新浏览器应判为新设备")
	}
	if second.DeviceID == first.DeviceID {
		t.Fatal("新设备应拿到新的设备标识")
	}
	if n := countNotifications(t, db, notify.EventLoginNewDevice); n != 2 {
		t.Fatalf("新浏览器应再报一条，实际 %d", n)
	}

	// 设备列表应能看到两台
	devices, err := svc.Devices(ctx, user.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("应记录 2 台设备，实际 %d", len(devices))
	}
}

// 伪造 / 别的账号的 Cookie 不能冒充已信任设备。
func TestLoginWatchUnknownCookieTreatedAsNewDevice(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	truncateLoginWatch(t, db)

	user := mkConfirmUser(t, db, "watch-forge", pwOK)
	other := mkConfirmUser(t, db, "watch-forge-other", pwOK)
	svc := NewLoginWatchService(db).
		WithNotifier(notify.New(db, notify.DefaultConfig())).
		WithMode(func(context.Context) string { return LoginAlertDevice })

	// 别人的设备标识
	othersDevice := svc.Record(ctx, other, "198.51.100.9", "Mozilla/5.0 Chrome/120 Windows", "").DeviceID
	res := svc.Record(ctx, user, "198.51.100.9", "Mozilla/5.0 Chrome/120 Windows", othersDevice)
	if !res.NewDevice {
		t.Fatal("别的账号的设备标识不能被当成本人的已信任设备")
	}

	// 完全伪造的值
	res2 := svc.Record(ctx, user, "198.51.100.9", "Mozilla/5.0 Chrome/120 Windows", "totally-made-up")
	if !res2.NewDevice {
		t.Fatal("伪造设备标识应判为新设备")
	}
}

// 按 IP 模式：适合固定出口 IP 的场景。
func TestLoginWatchIPMode(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	truncateLoginWatch(t, db)

	user := mkConfirmUser(t, db, "watch-ipmode", pwOK)
	svc := NewLoginWatchService(db).
		WithNotifier(notify.New(db, notify.DefaultConfig())).
		WithMode(func(context.Context) string { return LoginAlertIP })

	// IP 模式不按设备告警：同一个 Cookie 换 IP 也不该报（本模式下不报设备）
	svc.Record(ctx, user, "198.51.100.1", "UA", "")

	// 但设备表仍会记录（便于切回 device 模式时已有基线）
	if d, _ := svc.Devices(ctx, user.ID, 10); len(d) != 1 {
		t.Fatalf("IP 模式也应记录设备，实际 %d", len(d))
	}
	if n := countNotifications(t, db, notify.EventLoginNewDevice); n != 0 {
		t.Fatalf("IP 模式不应报设备告警，实际 %d", n)
	}
}

// 关闭模式：只记录，不推送、也不下发 Cookie。
func TestLoginWatchOffModeOnlyRecords(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	truncateLoginWatch(t, db)

	user := mkConfirmUser(t, db, "watch-off", pwOK)
	svc := NewLoginWatchService(db).
		WithNotifier(notify.New(db, notify.DefaultConfig())).
		WithMode(func(context.Context) string { return LoginAlertOff })

	res := svc.Record(ctx, user, "198.51.100.1", "UA", "")
	if res.DeviceID != "" {
		t.Fatal("关闭模式不应下发设备 Cookie")
	}
	if n := countNotifications(t, db, notify.EventLoginNewDevice); n != 0 {
		t.Fatalf("关闭模式不应推送，实际 %d", n)
	}
	// IP 仍要记录
	if ips, _ := svc.IPHistory(ctx, user.ID, 10); len(ips) != 1 {
		t.Fatalf("关闭模式仍应记录 IP，实际 %d", len(ips))
	}
}

// 移除设备后，该浏览器下次登录应重新触发提醒。
func TestLoginWatchForgetDevice(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	truncateLoginWatch(t, db)

	user := mkConfirmUser(t, db, "watch-forget", pwOK)
	svc := NewLoginWatchService(db).
		WithNotifier(notify.New(db, notify.DefaultConfig())).
		WithMode(func(context.Context) string { return LoginAlertDevice })

	first := svc.Record(ctx, user, "198.51.100.1", "UA", "")
	devices, _ := svc.Devices(ctx, user.ID, 10)
	if len(devices) != 1 {
		t.Fatalf("应有 1 台设备，实际 %d", len(devices))
	}

	if err := svc.ForgetDevice(ctx, user.ID, devices[0].ID); err != nil {
		t.Fatal(err)
	}
	// 删掉后用旧 Cookie 登录：库里查不到 → 当作新设备
	res := svc.Record(ctx, user, "198.51.100.1", "UA", first.DeviceID)
	if !res.NewDevice {
		t.Fatal("移除后该设备应被判为新设备")
	}
}

// 空 IP 与空 UA 不能把逻辑搞崩。
func TestLoginWatchHandlesEmptyFields(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	truncateLoginWatch(t, db)

	user := mkConfirmUser(t, db, "watch-empty", pwOK)
	svc := NewLoginWatchService(db).
		WithNotifier(notify.New(db, notify.DefaultConfig())).
		WithMode(func(context.Context) string { return LoginAlertDevice })

	res := svc.Record(ctx, user, "   ", "", "")
	if !res.NewDevice || res.DeviceID == "" {
		t.Fatalf("空 IP/UA 仍应判为新设备: %+v", res)
	}
	if n := countNotifications(t, db, notify.EventLoginNewDevice); n != 1 {
		t.Fatalf("应推 1 条告警，实际 %d", n)
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
	for _, tbl := range []string{"user_devices", "user_login_ips", "notifications", "users"} {
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
