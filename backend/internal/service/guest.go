package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

// 匿名购买相关错误。
//
// 注意：对外一律用 ErrGuestAuthFailed，不区分「邮箱不存在」和「密码错误」，
// 否则等于给攻击者一个邮箱枚举接口。
var (
	ErrGuestAuthFailed = errors.New("邮箱或安全密码不正确")
	ErrGuestEmailBad   = errors.New("请填写正确的邮箱地址")
	ErrGuestPinBad     = errors.New("安全密码必须是 6 位数字")
	ErrGuestDisabled   = errors.New("该账号已被禁用")
)

// GuestPinPattern 安全密码格式：6 位纯数字。
var GuestPinPattern = regexp.MustCompile(`^\d{6}$`)

// GuestService 匿名购买 / 匿名查单。
//
// 设计说明：
//   - 匿名买家仍然落在 buyers 表（IsGuest=true），因为 orders 和
//     payment_orders 的 buyer_id 都有外键指向 buyers(id)，支付、发货、
//     退款整条链路都以 buyer 为载体。把它当成「没有登录凭证的账号」
//     就能完全复用现有支付与发货逻辑，不必改支付核心。
//   - 匿名买家没有可用密码（PasswordHash 置为不可用于登录的占位值），
//     登录接口无法用它们登录；唯一凭证是「邮箱 + 6 位安全密码」。
type GuestService struct {
	db *gorm.DB
}

func NewGuestService(db *gorm.DB) *GuestService {
	return &GuestService{db: db}
}

// NormalizeGuestEmail 统一邮箱大小写与空白。
func NormalizeGuestEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// ValidateGuestCredential 校验邮箱与 6 位密码的格式。
func ValidateGuestCredential(email, pin string) (string, error) {
	e := NormalizeGuestEmail(email)
	if e == "" || len(e) > 128 {
		return "", ErrGuestEmailBad
	}
	if _, err := mail.ParseAddress(e); err != nil {
		return "", ErrGuestEmailBad
	}
	if !GuestPinPattern.MatchString(strings.TrimSpace(pin)) {
		return "", ErrGuestPinBad
	}
	return e, nil
}

// guestPasswordPlaceholder 匿名买家的 PasswordHash 占位值。
//
// 必须是「不可能匹配任何 bcrypt 校验」的字符串：正常密码哈希都以
// $2a$/$2b$ 开头，这里放 ! 开头的串，crypto.VerifyPassword 必然返回 false，
// 因此匿名账号永远无法通过登录接口登录。
const guestPasswordPlaceholder = "!guest-no-login!"

// EnsureGuest 找到或创建匿名买家，并校验安全密码。
//
// 语义：
//   - 该邮箱下已有匿名买家 → 必须密码正确，否则返回 ErrGuestAuthFailed
//   - 该邮箱下没有匿名买家、但属于正式注册账号 → 返回 ErrGuestAuthFailed
//     （不允许匿名通道接管已有账号的订单）
//   - 都没有 → 创建匿名买家（首次下单即注册，密码用本次传入的 pin）
//
// 关键：查询必须先按「匿名买家」这个口径过滤，再做「是否正式账号」的判断。
//
// 原来是一条 Where("email = ?").Order("id ASC").First()，只看最早的那行：
// buyers.email 上没有唯一索引，同一个邮箱可以同时存在「正式账号（早）」
// 和「匿名买家（晚）」。于是先注册过、后来改用匿名通道下单的老客户，
// 每一次都会被判成 ErrGuestAuthFailed —— 表现为「明明是第一次匿名买，
// 却说过邮箱或安全密码不正确」，而且换成正确密码也永远进不去。
func (s *GuestService) EnsureGuest(ctx context.Context, email, pin, ip string) (*models.Buyer, error) {
	e := NormalizeGuestEmail(email)
	pin = strings.TrimSpace(pin)

	var b models.Buyer
	err := s.db.WithContext(ctx).
		Where("email = ? AND is_guest = ?", e, true).
		Order("id ASC").First(&b).Error
	switch {
	case err == nil:
		if !b.IsEnabled() {
			return nil, ErrGuestDisabled
		}
		if !crypto.VerifyPassword(b.GuestPinHash, pin) {
			return nil, ErrGuestAuthFailed
		}
		return &b, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}

	// 没有匿名买家：若这个邮箱属于正式注册账号，仍然拒绝匿名接管
	var formal int64
	if err := s.db.WithContext(ctx).Model(&models.Buyer{}).
		Where("email = ? AND is_guest = ?", e, false).
		Count(&formal).Error; err != nil {
		return nil, err
	}
	if formal > 0 {
		// 对外仍是统一的凭据错误，不暴露「这个邮箱注册过」
		return nil, ErrGuestAuthFailed
	}

	// 首次出现：创建匿名买家
	hash, herr := crypto.HashPassword(pin)
	if herr != nil {
		return nil, fmt.Errorf("安全密码处理失败: %w", herr)
	}
	guest := &models.Buyer{
		Username:     guestUsername(),
		Email:        e,
		PasswordHash: guestPasswordPlaceholder,
		Status:       models.BuyerStatusEnabled,
		IsGuest:      true,
		GuestPinHash: hash,
		RegisterIP:   truncateStr(ip, 64),
	}
	if cerr := s.db.WithContext(ctx).Create(guest).Error; cerr != nil {
		// 并发下可能被另一个请求抢先创建：回读并按密码校验
		var again models.Buyer
		if e2 := s.db.WithContext(ctx).Where("email = ? AND is_guest = ?", e, true).
			Order("id ASC").First(&again).Error; e2 == nil {
			if crypto.VerifyPassword(again.GuestPinHash, pin) {
				return &again, nil
			}
			return nil, ErrGuestAuthFailed
		}
		return nil, cerr
	}
	return guest, nil
}

// AuthenticateGuest 只校验凭据，不创建新账号（查询订单用）。
func (s *GuestService) AuthenticateGuest(ctx context.Context, email, pin string) (*models.Buyer, error) {
	e := NormalizeGuestEmail(email)
	pin = strings.TrimSpace(pin)

	var b models.Buyer
	err := s.db.WithContext(ctx).Where("email = ? AND is_guest = ?", e, true).
		Order("id ASC").First(&b).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 故意不暴露「邮箱没注册过」，统一成凭据错误
			return nil, ErrGuestAuthFailed
		}
		return nil, err
	}
	if !b.IsEnabled() {
		return nil, ErrGuestDisabled
	}
	if !crypto.VerifyPassword(b.GuestPinHash, pin) {
		return nil, ErrGuestAuthFailed
	}
	return &b, nil
}

// guestUsername 生成匿名买家用户名。
//
// 只是给 buyers.username 唯一索引一个值，不会用于登录
// （匿名买家的 PasswordHash 是不可校验的占位值）。
func guestUsername() string {
	tok, err := crypto.RandomToken(12)
	if err != nil {
		return fmt.Sprintf("guest_%d", time.Now().UnixNano())
	}
	return "guest_" + strings.ToLower(tok)
}
