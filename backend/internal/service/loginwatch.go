package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/models"
	"kamihub/internal/notify"
	"kamihub/internal/pkg/crypto"
)

// 登录提醒的三种模式。
const (
	// LoginAlertDevice 按浏览器设备判定（默认）。
	//
	// 这是唯一能同时满足两种现实的设计：
	//   - 站长用代理/梯子访问，出口 IP 每次不同 → 按 IP 判会天天报警
	//   - 攻击者拿到密码在别处登录 → 他没有你的浏览器 Cookie，一定报警
	LoginAlertDevice = "device"
	// LoginAlertIP 按 IP 判定。适合固定出口 IP 的场景（如公司专线）。
	LoginAlertIP = "ip"
	// LoginAlertOff 只记录不提醒。
	LoginAlertOff = "off"
)

// DeviceCookieName 存放设备标识的 Cookie 名。
//
// Path 限定 /api：只在接口请求时携带，不会出现在静态资源或提货页请求里，
// 缩小暴露面。HttpOnly 让页面脚本读不到，降低被 XSS 偷走的风险。
const (
	DeviceCookieName = "kamihub_device"
	// DeviceCookieTTL 设备 Cookie 有效期。180 天足够"记住这台电脑"，
	// 又不会长到一台公用电脑永久被信任。
	DeviceCookieTTL = 180 * 24 * time.Hour
)

// NormalizeLoginAlertMode 把配置归一到合法值。
//
// 空串（老配置没有这个键）按 device 处理：这是最不容易误伤的模式。
func NormalizeLoginAlertMode(mode string) string {
	switch mode {
	case LoginAlertDevice, LoginAlertIP, LoginAlertOff:
		return mode
	default:
		return LoginAlertDevice
	}
}

// LoginCheck 一次登录的判定结果。
type LoginCheck struct {
	// NewDevice 本次登录来自一台没见过的浏览器
	NewDevice bool
	// NewIP 本次登录来自一个没见过的 IP（仅供参考，不再单独告警）
	NewIP bool
	// DeviceID 需要下发给浏览器的设备标识。为空表示不需要下发
	// （已有有效 Cookie，或该账号不在监控范围内）。
	DeviceID string
}

// LoginWatchService 负责「新设备登录提醒」。
//
// 设计取舍：
//
//  1. 只盯员工账号。买家换设备是常态（手机、平板、换浏览器），
//     对他们报警毫无意义且会淹没真正的异常。
//  2. 按浏览器 Cookie 判定设备，不按 IP —— 站长用梯子时 IP 每次不同，
//     按 IP 判会把他自己淹掉（细节见 models.UserDevice 的注释）。
//  3. 同时记录 IP 历史，仅用于事后追查，不参与告警判定。
//  4. 本次登录**照常放行**。这是提醒不是拦截，
//     拦下来只会把站长挡在门外。
type LoginWatchService struct {
	db *gorm.DB
	// notifier 为 nil 时只落库不推送（测试或未配通知渠道）。
	notifier *notify.Notifier
	// enabled 返回「是否提醒」以及模式（后台可改）。
	mode func(ctx context.Context) string
	now  func() time.Time
}

func NewLoginWatchService(db *gorm.DB) *LoginWatchService {
	return &LoginWatchService{db: db, now: time.Now}
}

// WithNotifier 注入通知器。
func (s *LoginWatchService) WithNotifier(n *notify.Notifier) *LoginWatchService {
	s.notifier = n
	return s
}

// WithMode 注入提醒模式提供函数（来自后台安全设置）。
func (s *LoginWatchService) WithMode(fn func(ctx context.Context) string) *LoginWatchService {
	s.mode = fn
	return s
}

// modeOf 取当前的提醒模式，未注入时默认 device。
func (s *LoginWatchService) modeOf(ctx context.Context) string {
	if s.mode == nil {
		return LoginAlertDevice
	}
	return NormalizeLoginAlertMode(s.mode(ctx))
}

// Record 记录一次登录，并按模式决定是否告警。
//
// 该方法**永不返回错误打断登录**：记录失败最多让我们少一次告警，
// 而登录失败会把站长关在门外。所有内部错误只打日志。
//
// deviceID 是浏览器带上来的 Cookie 原值（可能为空）。
// 返回的 LoginCheck.DeviceID 非空时，调用方应把它写回 Cookie。
func (s *LoginWatchService) Record(
	ctx context.Context, user *models.User, ip, userAgent, deviceID string,
) LoginCheck {
	out := LoginCheck{}
	if s.db == nil || user == nil || user.ID == 0 {
		return out
	}
	ip = strings.TrimSpace(ip)
	userAgent = truncateRunes(strings.TrimSpace(userAgent), 255)
	now := s.now()
	mode := s.modeOf(ctx)

	// ---------- IP 历史（只记录，不告警）----------
	if ip != "" {
		newIP := s.recordIP(ctx, user, ip, userAgent, now)
		out.NewIP = newIP
	}

	// ---------- 设备判定 ----------
	// 浏览器没带 Cookie：要么第一次来，要么清了缓存，要么是别人的电脑。
	// 三种情况都该发一个新 Cookie；是否告警取决于模式。
	if deviceID == "" {
		out.NewDevice = true
		if mode == LoginAlertOff {
			return out
		}
		newID, err := crypto.RandomToken(24)
		if err != nil {
			slog.Warn("生成设备标识失败", "user", user.Username, "error", err.Error())
			return out
		}
		if err := s.saveDevice(ctx, user, newID, ip, userAgent, now); err != nil {
			slog.Warn("记录设备失败", "user", user.Username, "error", err.Error())
		}
		out.DeviceID = newID
		if mode == LoginAlertDevice {
			s.alert(ctx, user, ip, userAgent, now, newID)
		}
		return out
	}

	// 带了 Cookie：查是不是已知设备
	hash := hashDevice(deviceID)
	var dev models.UserDevice
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND device_hash = ?", user.ID, hash).
		First(&dev).Error

	switch {
	case err == nil:
		// 认识的设备：静默更新，不发 Cookie、不告警。
		// IP 会变（梯子），这是预期内的，只记下来供事后看。
		if uerr := s.db.WithContext(ctx).Model(&models.UserDevice{}).
			Where("id = ?", dev.ID).
			Updates(map[string]any{
				"last_seen_at": now,
				"last_ip":      ip,
				"hit_count":    gorm.Expr("hit_count + 1"),
			}).Error; uerr != nil {
			slog.Warn("更新设备记录失败", "user", user.Username, "error", uerr.Error())
		}
		return out

	case errors.Is(err, gorm.ErrRecordNotFound):
		// Cookie 是自己发的但库里没有：可能是换了账号、或数据库被重置。
		// 当作新设备处理，并换发一个新 Cookie。
		out.NewDevice = true
		newID, gerr := crypto.RandomToken(24)
		if gerr != nil {
			slog.Warn("生成设备标识失败", "user", user.Username, "error", gerr.Error())
			return out
		}
		if serr := s.saveDevice(ctx, user, newID, ip, userAgent, now); serr != nil {
			slog.Warn("记录设备失败", "user", user.Username, "error", serr.Error())
		}
		out.DeviceID = newID
		if mode == LoginAlertDevice {
			s.alert(ctx, user, ip, userAgent, now, newID)
		}
		return out

	default:
		slog.Warn("查询设备失败", "user", user.Username, "error", err.Error())
		return out
	}
}

// recordIP 维护 IP 历史，返回是否首次见到该 IP。
func (s *LoginWatchService) recordIP(
	ctx context.Context, user *models.User, ip, userAgent string, now time.Time,
) bool {
	var existing models.UserLoginIP
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND ip = ?", user.ID, ip).
		First(&existing).Error

	switch {
	case err == nil:
		if uerr := s.db.WithContext(ctx).Model(&models.UserLoginIP{}).
			Where("id = ?", existing.ID).
			Updates(map[string]any{
				"last_seen_at": now,
				"hit_count":    gorm.Expr("hit_count + 1"),
				"user_agent":   userAgent,
			}).Error; uerr != nil {
			slog.Warn("更新登录 IP 记录失败", "user", user.Username, "ip", ip, "error", uerr.Error())
		}
		return false

	case errors.Is(err, gorm.ErrRecordNotFound):
		row := models.UserLoginIP{
			UserID: user.ID, IP: ip, UserAgent: userAgent,
			FirstSeenAt: now, LastSeenAt: now, HitCount: 1,
		}
		if cerr := s.db.WithContext(ctx).Create(&row).Error; cerr != nil {
			// 并发下唯一索引冲突属正常（同 IP 两个请求同时进来）
			slog.Debug("写入登录 IP 记录失败", "user", user.Username, "ip", ip, "error", cerr.Error())
			return false
		}
		return true

	default:
		slog.Warn("查询登录 IP 记录失败", "user", user.Username, "ip", ip, "error", err.Error())
		return false
	}
}

// saveDevice 记录一台新设备。
func (s *LoginWatchService) saveDevice(
	ctx context.Context, user *models.User, deviceID, ip, userAgent string, now time.Time,
) error {
	row := models.UserDevice{
		UserID:      user.ID,
		DeviceHash:  hashDevice(deviceID),
		Label:       deviceLabel(userAgent),
		UserAgent:   userAgent,
		FirstIP:     ip,
		LastIP:      ip,
		FirstSeenAt: now,
		LastSeenAt:  now,
		HitCount:    1,
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

// alert 推送「新设备登录」告警。
func (s *LoginWatchService) alert(
	ctx context.Context, user *models.User, ip, userAgent string, at time.Time, deviceID string,
) {
	if s.notifier == nil {
		slog.Info("检测到新设备登录（通知器未装配，仅记录）",
			"user", user.Username, "ip", ip)
		return
	}

	device := deviceLabel(userAgent)
	content := fmt.Sprintf("账号 %s 从一台没见过的设备登录。\n\n"+
		"时间：%s\nIP：%s%s\n设备：%s\n\n"+
		"如果这是你新换的浏览器、或刚清过 Cookie，忽略即可；\n"+
		"如果不是你，说明密码可能已泄露 —— 请立刻改密码，并检查后台是否被人改过东西。\n\n"+
		"（已信任的设备不再提醒；本提醒不看 IP，所以用梯子换 IP 不会误报。）",
		user.Username,
		at.Format("2006-01-02 15:04:05"),
		ip, ipNote(ip),
		device,
	)

	s.notifier.Alert(ctx, notify.Message{
		Level:   notify.LevelWarn,
		Event:   notify.EventLoginNewDevice,
		Title:   "有新设备登录了后台",
		Content: content,
		Link:    "/audit-logs",
		Fields: map[string]any{
			"账号": user.Username,
			"时间": at.Format("2006-01-02 15:04:05"),
			"IP": ip,
			"设备": device,
		},
		At: at,
		// 每条新设备登录都是独立事实，合并后就看不出是哪台设备了
		NoAggregate: true,
	})
}

// Devices 列出某账号已信任的设备，供后台展示。
func (s *LoginWatchService) Devices(ctx context.Context, userID uint, limit int) ([]models.UserDevice, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var list []models.UserDevice
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("last_seen_at DESC").
		Limit(limit).
		Find(&list).Error
	return list, err
}

// IPHistory 列出某账号登录过的 IP，供事后追查（梯子场景下会很长）。
func (s *LoginWatchService) IPHistory(ctx context.Context, userID uint, limit int) ([]models.UserLoginIP, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var list []models.UserLoginIP
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("last_seen_at DESC").
		Limit(limit).
		Find(&list).Error
	return list, err
}

// ForgetDevice 移除一台已信任设备（设备丢失/卖掉电脑时用）。
// 下次该浏览器登录会重新触发一次提醒。
func (s *LoginWatchService) ForgetDevice(ctx context.Context, userID, deviceID uint) error {
	return s.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", deviceID, userID).
		Delete(&models.UserDevice{}).Error
}

// ---------- 小工具 ----------

// hashDevice 设备标识只存哈希。
func hashDevice(deviceID string) string {
	sum := sha256.Sum256([]byte(deviceID))
	return hex.EncodeToString(sum[:])
}

// deviceLabel 从 UA 猜一个简短描述，仅用于让人一眼认出是哪台设备。
//
// 刻意做得粗糙：UA 本来就是启发式信息，写得再复杂也不可靠，
// 真正用于判定的是 Cookie 不是 UA。
func deviceLabel(ua string) string {
	low := strings.ToLower(ua)
	browser := "未知浏览器"
	switch {
	case strings.Contains(low, "edg/"):
		browser = "Edge"
	case strings.Contains(low, "chrome/") && !strings.Contains(low, "chromium"):
		browser = "Chrome"
	case strings.Contains(low, "firefox/"):
		browser = "Firefox"
	case strings.Contains(low, "safari/") && !strings.Contains(low, "chrome"):
		browser = "Safari"
	case strings.Contains(low, "curl/"):
		browser = "curl"
	case strings.Contains(low, "node"):
		browser = "脚本"
	}

	osName := "未知系统"
	switch {
	case strings.Contains(low, "windows"):
		osName = "Windows"
	case strings.Contains(low, "iphone") || strings.Contains(low, "ipad"):
		osName = "iOS"
	case strings.Contains(low, "android"):
		osName = "Android"
	case strings.Contains(low, "mac os"):
		osName = "macOS"
	case strings.Contains(low, "linux"):
		osName = "Linux"
	}
	return browser + " · " + osName
}

// ipNote 给内网/回环地址加一句说明，避免站长看到 127.0.0.1 时困惑。
func ipNote(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	if parsed.IsLoopback() {
		return "（本机回环地址，通常是反向代理未透传真实 IP）"
	}
	if parsed.IsPrivate() {
		return "（内网地址）"
	}
	return ""
}

// truncateRunes 按字符（非字节）截断，避免把中文切成半个字。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
