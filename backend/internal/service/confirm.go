package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/cache"
	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

// 敏感操作二次确认（对照 `04-安全风控与通知.md` S2）。
//
// 要解决的问题：后台登录态是长期有效的 JWT（默认 12 小时）。
// 手机被人拿到、或浏览器没关，对方就能改上游凭证、给买家调余额、
// 把整池卡密明文导出 —— 这些动作都不可逆。
//
// 设计取舍：
//
//  1. **用票据而不是每个请求都带密码。** 若每次翻页看卡密都要重新输密码，
//     运维会把密码写在便签上，等于没做。改成「确认一次，10 分钟内有效」。
//  2. **票据绑 user_id。** 换账号不能用别人的票据。
//  3. **票据存 Redis 且短命。** 进程重启即失效，宁可多问一次。
//  4. **失败计数限流。** 否则确认接口变成一个在线校验密码的预言机 ——
//     攻击者在已登录状态下可以无限次猜密码。
//  5. **可整体关闭**（`confirm_disabled`）—— 反向字段，零值即「要求确认」，
//     不会因为一次升级把防护静默关掉。
const (
	// ConfirmTicketTTL 票据有效期。10 分钟是「够改几处配置」与
	// 「离开工位后被别人利用」之间的折中。
	ConfirmTicketTTL = 10 * time.Minute
	// ConfirmMaxFail 同一用户连续确认失败次数上限（窗口内）
	ConfirmMaxFail = 5
	// confirmFailTTL 失败计数窗口
	confirmFailTTL = 10 * time.Minute
)

// ErrConfirmRequired 缺少或无效的二次确认票据。
var ErrConfirmRequired = errors.New("该操作需要再次输入密码确认")

// ErrConfirmPasswordWrong 确认密码错误。
var ErrConfirmPasswordWrong = errors.New("密码错误")

// ErrConfirmLocked 失败次数过多。
var ErrConfirmLocked = errors.New("确认密码错误次数过多，请稍后再试")

// ConfirmService 负责敏感操作的二次确认。
type ConfirmService struct {
	db    *gorm.DB
	cache *cache.Client
	// enabled 返回是否需要确认（后台可关）。nil 视为需要。
	enabled func(ctx context.Context) bool
	now     func() time.Time
}

func NewConfirmService(db *gorm.DB, c *cache.Client) *ConfirmService {
	return &ConfirmService{db: db, cache: c, now: time.Now}
}

// WithSwitch 注入开关。返回 false 时所有确认直接放行。
func (s *ConfirmService) WithSwitch(fn func(ctx context.Context) bool) *ConfirmService {
	s.enabled = fn
	return s
}

// Required 当前是否要求二次确认。
func (s *ConfirmService) Required(ctx context.Context) bool {
	if s.enabled == nil {
		return true
	}
	return s.enabled(ctx)
}

// Issue 校验密码并签发一张票据。
func (s *ConfirmService) Issue(ctx context.Context, userID uint, password string) (string, time.Time, error) {
	if s.cache == nil {
		return "", time.Time{}, errors.New("确认服务未启用（缺少 Redis）")
	}
	if !s.Required(ctx) {
		// 关闭时也给一张票据，让前端流程保持一致，不必特判
		return "disabled", s.now().Add(ConfirmTicketTTL), nil
	}

	failKey := confirmFailKey(userID)
	// AllowN 是只读检查：返回 false 表示计数已达上限
	if !s.cache.AllowN(ctx, failKey, ConfirmMaxFail) {
		return "", time.Time{}, ErrConfirmLocked
	}

	var user models.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return "", time.Time{}, err
	}
	if !crypto.VerifyPassword(user.PasswordHash, password) {
		// 只统计失败；成功时清零，正常用户不会被自己之前的输错拖进锁定
		s.cache.Fail(ctx, failKey, confirmFailTTL)
		return "", time.Time{}, ErrConfirmPasswordWrong
	}

	s.cache.Reset(ctx, failKey)

	ticket, err := crypto.RandomToken(24)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := s.now().Add(ConfirmTicketTTL)
	if err := s.cache.Set(ctx, confirmKey(userID, ticket), "1", ConfirmTicketTTL); err != nil {
		return "", time.Time{}, err
	}
	return ticket, expires, nil
}

// Check 校验票据是否有效（不消费，10 分钟内可重复使用）。
func (s *ConfirmService) Check(ctx context.Context, userID uint, ticket string) bool {
	if !s.Required(ctx) {
		return true
	}
	if s.cache == nil || ticket == "" || ticket == "disabled" {
		return false
	}
	v, err := s.cache.Get(ctx, confirmKey(userID, ticket))
	if err != nil {
		// Redis 出问题时按「未确认」处理：宁可多问一次密码，
		// 也不能因为缓存挂了就放行敏感操作。
		return false
	}
	return v != ""
}

// Revoke 主动作废某张票据（如退出登录时）。
func (s *ConfirmService) Revoke(ctx context.Context, userID uint, ticket string) {
	if s.cache == nil || ticket == "" {
		return
	}
	s.cache.Del(ctx, confirmKey(userID, ticket))
}

// ---------- Redis key ----------

func confirmKey(userID uint, ticket string) string {
	return "auth:confirm:" + strconv.FormatUint(uint64(userID), 10) + ":" + ticket
}

func confirmFailKey(userID uint) string {
	return "auth:confirm-fail:" + strconv.FormatUint(uint64(userID), 10)
}
