package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/models"
	"kamihub/internal/notify"
)

// LoginWatchService 负责「新 IP 登录提醒」。
//
// 设计取舍：
//
//  1. 只盯员工账号。买家换 IP 是常态（手机切流量、通勤换基站），
//     对他们报警毫无意义且会淹没真正的异常。
//  2. 只报「第一次见到的 IP」。存历史 IP 集合而不是只比 last_login_ip，
//     否则公司和家里来回登录会天天报警。
//  3. 告警文案里带上 UA 与时间，让人能自己判断「是不是我」。
//  4. 本次登录**照常放行**。这是提醒，不是拦截 ——
//     新 IP 里绝大多数是本人换了网络，拦下来只会把站长挡在门外。
type LoginWatchService struct {
	db *gorm.DB
	// notifier 为 nil 时只落库不推送（测试或未配通知渠道）。
	notifier *notify.Notifier
	// enabled 返回是否启用告警（后台可关）。
	enabled func(ctx context.Context) bool
	now     func() time.Time
}

func NewLoginWatchService(db *gorm.DB) *LoginWatchService {
	return &LoginWatchService{db: db, now: time.Now}
}

// WithNotifier 注入通知器。
func (s *LoginWatchService) WithNotifier(n *notify.Notifier) *LoginWatchService {
	s.notifier = n
	return s
}

// WithSwitch 注入启用开关（来自后台安全设置）。
func (s *LoginWatchService) WithSwitch(fn func(ctx context.Context) bool) *LoginWatchService {
	s.enabled = fn
	return s
}

// Record 记录一次登录，并在首次见到该 IP 时发出告警。
//
// 该方法**永不返回错误打断登录**：记录失败最多让我们少一次告警，
// 而登录失败会把站长关在门外。所有内部错误只打日志。
func (s *LoginWatchService) Record(ctx context.Context, user *models.User, ip, userAgent string) {
	if s.db == nil || user == nil || user.ID == 0 {
		return
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return
	}
	userAgent = truncateRunes(strings.TrimSpace(userAgent), 255)
	now := s.now()

	var existing models.UserLoginIP
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND ip = ?", user.ID, ip).
		First(&existing).Error

	switch {
	case err == nil:
		// 见过的 IP：只更新计数与时间，不告警
		if uerr := s.db.WithContext(ctx).Model(&models.UserLoginIP{}).
			Where("id = ?", existing.ID).
			Updates(map[string]any{
				"last_seen_at": now,
				"hit_count":    gorm.Expr("hit_count + 1"),
				"user_agent":   userAgent,
			}).Error; uerr != nil {
			slog.Warn("更新登录 IP 记录失败", "user", user.Username, "ip", ip, "error", uerr.Error())
		}
		return

	case errors.Is(err, gorm.ErrRecordNotFound):
		// 第一次见到的 IP —— 落库后告警
		row := models.UserLoginIP{
			UserID:      user.ID,
			IP:          ip,
			UserAgent:   userAgent,
			FirstSeenAt: now,
			LastSeenAt:  now,
			HitCount:    1,
		}
		if cerr := s.db.WithContext(ctx).Create(&row).Error; cerr != nil {
			// 并发下唯一索引冲突属正常（同 IP 两个请求同时进来），
			// 说明别人已经插入了，不必重复告警。
			slog.Warn("写入登录 IP 记录失败", "user", user.Username, "ip", ip, "error", cerr.Error())
			return
		}
		s.alertNewIP(ctx, user, ip, userAgent, now)

	default:
		slog.Warn("查询登录 IP 记录失败", "user", user.Username, "ip", ip, "error", err.Error())
	}
}

// alertNewIP 发出「新 IP 登录」告警。
func (s *LoginWatchService) alertNewIP(
	ctx context.Context, user *models.User, ip, userAgent string, at time.Time,
) {
	if s.enabled != nil && !s.enabled(ctx) {
		return
	}
	if s.notifier == nil {
		slog.Info("检测到新 IP 登录（通知器未装配，仅记录）",
			"user", user.Username, "ip", ip)
		return
	}

	content := fmt.Sprintf("账号 %s 从新 IP %s 登录。\n\n时间：%s\nIP：%s%s\n浏览器：%s\n\n"+
		"如果是你本人（换了网络/新设备），忽略即可；\n"+
		"如果不是你，请立刻改密码，并检查后台是否被人改过东西。",
		user.Username, ip,
		at.Format("2006-01-02 15:04:05"),
		ip, ipNote(ip),
		firstNonEmpty(userAgent, "（未提供）"),
	)

	s.notifier.Alert(ctx, notify.Message{
		Level:   notify.LevelWarn,
		Event:   notify.EventLoginNewIP,
		Title:   "有新 IP 登录了后台",
		Content: content,
		Link:    "/audit-logs",
		Fields: map[string]any{
			"账号":  user.Username,
			"IP":  ip,
			"时间":  at.Format("2006-01-02 15:04:05"),
			"浏览器": userAgent,
		},
		At: at,
		// 每条新 IP 登录都是独立事实，合并后就看不出是哪个 IP 了
		NoAggregate: true,
	})
}

// Known 判断该用户是否用过这个 IP（供测试与排障使用）。
func (s *LoginWatchService) Known(ctx context.Context, userID uint, ip string) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.UserLoginIP{}).
		Where("user_id = ? AND ip = ?", userID, ip).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
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

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
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
