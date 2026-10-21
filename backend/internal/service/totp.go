package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"gorm.io/gorm"

	"kamihub/internal/cache"
	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

// TOTP 相关错误
var (
	// ErrTOTPRequired 密码正确但还需要第二步验证。
	// 由 Login 返回，调用方据此回一个临时票据而不是真令牌。
	ErrTOTPRequired = errors.New("该账号已开启两步验证，请输入动态验证码")

	ErrTOTPNotBound   = errors.New("尚未绑定两步验证")
	ErrTOTPAlreadyOn  = errors.New("两步验证已开启，如需重置请先关闭")
	ErrTOTPCodeWrong  = errors.New("动态验证码不正确")
	ErrRecoveryWrong  = errors.New("恢复码不正确或已被使用")
	ErrTOTPTicketBad  = errors.New("验证票据无效或已过期，请重新登录")
)

// RecoveryCodeCount 一次生成多少个恢复码
const (
	RecoveryCodeCount = 10
	// TOTPPeriod / TOTPDigits 与主流验证器（Google Authenticator 等）保持一致
	TOTPPeriod = 30
	TOTPDigits = 6
	// TOTPSkew 容忍前后各 1 个时间窗（±30s），吸收手机与服务器的时钟漂移。
	// 取 1 而不是 2：窗口越宽暴力破解空间越大，1 个已经足够覆盖常见漂移。
	TOTPSkew = 1
)

// TOTPService 负责两步验证的绑定、校验与恢复码。
type TOTPService struct {
	db    *gorm.DB
	cfg   *config.Config
	cache *cache.Client
}

func NewTOTPService(db *gorm.DB, cfg *config.Config, c *cache.Client) *TOTPService {
	return &TOTPService{db: db, cfg: cfg, cache: c}
}

// TOTPStatus 供前端决定 UI：未绑定、已绑定、恢复码剩几个
type TOTPStatus struct {
	Enabled bool `json:"enabled"`
	// BoundAt 绑定时间（未绑定为 null）
	BoundAt *time.Time `json:"bound_at"`
	// RecoveryLeft 剩余可用的恢复码数量 —— 少于 3 个就该提醒重新生成
	RecoveryLeft int64 `json:"recovery_left"`
	// Required 该账号是否被强制要求绑定（admin 角色）
	Required bool `json:"required"`
}

// Status 查询某账号的 2FA 状态。
func (s *TOTPService) Status(ctx context.Context, userID uint) (*TOTPStatus, error) {
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return nil, err
	}
	var left int64
	if err := s.db.WithContext(ctx).Model(&models.UserRecoveryCode{}).
		Where("user_id = ? AND used_at IS NULL", userID).Count(&left).Error; err != nil {
		return nil, err
	}
	return &TOTPStatus{
		Enabled:      user.TOTPEnabled,
		BoundAt:      user.TOTPBoundAt,
		RecoveryLeft: left,
		// 只有 admin 强制。operator 可选，避免把日常发货的人也锁住。
		Required: user.IsAdmin(),
	}, nil
}

// SetupResult 绑定第一步的结果：把 Secret 和二维码链接交给前端。
type SetupResult struct {
	Secret string `json:"secret"`
	// OtpauthURL 前端可以据此渲染二维码，也可以手动输入 Secret
	OtpauthURL string `json:"otpauth_url"`
	Issuer     string `json:"issuer"`
	Account    string `json:"account"`
}

// Setup 生成一个新密钥并暂存（**不启用**）。
//
// 为什么不直接启用：密钥还没经过「用户输入正确验证码」这一步确认，
// 直接启用会导致「手机扫码失败但账号已被 2FA 锁住」的死局。
// 必须走 Confirm 二次确认。
func (s *TOTPService) Setup(ctx context.Context, userID uint) (*SetupResult, error) {
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return nil, err
	}
	if user.TOTPEnabled {
		return nil, ErrTOTPAlreadyOn
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "kamihub",
		AccountName: user.Username,
		Period:      TOTPPeriod,
		SecretSize:  20, // 160 bit，RFC 4226 推荐
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1, // 兼容性最好，验证器普遍只支持 SHA1
	})
	if err != nil {
		return nil, fmt.Errorf("生成 TOTP 密钥失败: %w", err)
	}

	enc, err := crypto.Encrypt(key.Secret(), s.cfg.Crypto.Key)
	if err != nil {
		return nil, fmt.Errorf("加密 TOTP 密钥失败: %w", err)
	}

	// 只写 secret，不动 totp_enabled —— 等 Confirm 成功才启用
	if err := s.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", userID).
		Update("totp_secret", enc).Error; err != nil {
		return nil, err
	}

	return &SetupResult{
		Secret:     key.Secret(),
		OtpauthURL: key.URL(),
		Issuer:     "kamihub",
		Account:    user.Username,
	}, nil
}

// ConfirmResult 绑定成功的结果。RecoveryCodes 只在这里出现一次。
type ConfirmResult struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// Confirm 用一次正确验证码确认绑定，并生成恢复码。
func (s *TOTPService) Confirm(ctx context.Context, userID uint, code string) (*ConfirmResult, error) {
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return nil, err
	}
	if user.TOTPEnabled {
		return nil, ErrTOTPAlreadyOn
	}
	if strings.TrimSpace(user.TOTPSecret) == "" {
		// 没走过 Setup。不能在这里偷偷生成密钥 ——
		// 用户手机上还没有那个密钥，验证码必然不对。
		return nil, errors.New("请先获取密钥并添加到验证器，再输入验证码确认")
	}

	secret, err := crypto.Decrypt(user.TOTPSecret, s.cfg.Crypto.Key)
	if err != nil {
		return nil, fmt.Errorf("解密 TOTP 密钥失败（ENCRYPTION_KEY 可能已变更）: %w", err)
	}
	if !s.verifyCode(secret, code) {
		return nil, ErrTOTPCodeWrong
	}

	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", userID).
		Updates(map[string]any{
			"totp_enabled":  true,
			"totp_bound_at": now,
		}).Error; err != nil {
		return nil, err
	}

	codes, err := s.regenerateRecoveryCodes(ctx, userID)
	if err != nil {
		// 恢复码生成失败不该让绑定失败：2FA 已经生效，
		// 用户可以再点一次「重新生成恢复码」。
		slog.Error("生成恢复码失败，2FA 已启用", "user_id", userID, "error", err.Error())
		return &ConfirmResult{RecoveryCodes: nil}, nil
	}
	return &ConfirmResult{RecoveryCodes: codes}, nil
}

// Disable 关闭 2FA。要求重新输入密码 —— 光有已登录会话不够，
// 否则一个被劫持的会话就能把第二因素拆掉。
func (s *TOTPService) Disable(ctx context.Context, userID uint, password string) error {
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return err
	}
	if !crypto.VerifyPassword(user.PasswordHash, password) {
		return ErrInvalidCredentials
	}
	if err := s.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", userID).
		Updates(map[string]any{
			"totp_enabled":  false,
			"totp_secret":   "",
			"totp_bound_at": nil,
		}).Error; err != nil {
		return err
	}
	// 恢复码一并作废，避免关闭后又被旧码「复活」
	return s.db.WithContext(ctx).Where("user_id = ?", userID).
		Delete(&models.UserRecoveryCode{}).Error
}

// Reset 管理员强制重置他人 2FA（手机丢失且恢复码也没了的兜底手段）。
func (s *TOTPService) Reset(ctx context.Context, userID uint) error {
	if err := s.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", userID).
		Updates(map[string]any{
			"totp_enabled":  false,
			"totp_secret":   "",
			"totp_bound_at": nil,
		}).Error; err != nil {
		return err
	}
	return s.db.WithContext(ctx).Where("user_id = ?", userID).
		Delete(&models.UserRecoveryCode{}).Error
}

// Verify 校验一次二次验证凭据，支持动态码或恢复码。
//
// 返回 usedRecovery=true 表示这次用的是恢复码（调用方应提示用户「该码已作废」）。
func (s *TOTPService) Verify(ctx context.Context, userID uint, code string) (usedRecovery bool, err error) {
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return false, err
	}
	if !user.TOTPEnabled {
		return false, ErrTOTPNotBound
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return false, ErrTOTPCodeWrong
	}

	// 先试动态码（99% 的情况），再试恢复码。
	// 顺序不能反：恢复码是 10 个一组的 16 位串，先试它要多查一次库。
	secret, derr := crypto.Decrypt(user.TOTPSecret, s.cfg.Crypto.Key)
	if derr == nil && s.verifyCode(secret, code) {
		return false, nil
	}

	if ok := s.consumeRecoveryCode(ctx, userID, code); ok {
		return true, nil
	}
	return false, ErrTOTPCodeWrong
}

// verifyCode 校验 6 位动态码
func (s *TOTPService) verifyCode(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != TOTPDigits {
		return false
	}
	ok, err := totp.ValidateCustom(code, secret, time.Now().UTC(), totp.ValidateOpts{
		Period:    TOTPPeriod,
		Skew:      TOTPSkew,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		return false
	}
	return ok
}

// consumeRecoveryCode 尝试用掉一个恢复码（一次性）。
//
// 用「先查后更新的条件更新」保证并发下同一个码不会被用两次：
// UPDATE ... WHERE used_at IS NULL 的 RowsAffected 就是仲裁结果。
func (s *TOTPService) consumeRecoveryCode(ctx context.Context, userID uint, code string) bool {
	normalized := normalizeRecoveryCode(code)
	if normalized == "" {
		return false
	}
	hash := hashRecoveryCode(normalized)

	var rc models.UserRecoveryCode
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND code_hash = ? AND used_at IS NULL", userID, hash).
		First(&rc).Error; err != nil {
		return false
	}

	now := time.Now()
	res := s.db.WithContext(ctx).Model(&models.UserRecoveryCode{}).
		Where("id = ? AND used_at IS NULL", rc.ID).
		Update("used_at", now)
	if res.Error != nil || res.RowsAffected == 0 {
		return false
	}
	slog.Info("使用恢复码登录", "user_id", userID, "code_id", rc.ID)
	return true
}

// regenerateRecoveryCodes 重新生成一组恢复码（旧的作废）。
func (s *TOTPService) regenerateRecoveryCodes(ctx context.Context, userID uint) ([]string, error) {
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).
		Delete(&models.UserRecoveryCode{}).Error; err != nil {
		return nil, err
	}

	plain := make([]string, 0, RecoveryCodeCount)
	rows := make([]models.UserRecoveryCode, 0, RecoveryCodeCount)
	for i := 0; i < RecoveryCodeCount; i++ {
		// 12 位随机（base32 风格字符），约 60 bit 熵。
		// 分组显示只为好抄，入库前统一去掉分隔符。
		raw, err := crypto.RandomToken(9) // 9 字节 → 12 个 base64 字符
		if err != nil {
			return nil, err
		}
		code := formatRecoveryCode(raw)
		plain = append(plain, code)
		rows = append(rows, models.UserRecoveryCode{
			UserID:   userID,
			CodeHash: hashRecoveryCode(normalizeRecoveryCode(code)),
		})
	}
	if err := s.db.WithContext(ctx).Create(&rows).Error; err != nil {
		return nil, err
	}
	return plain, nil
}

// RegenerateRecoveryCodes 已绑定的用户主动重新生成恢复码。
//
// 必须校验是一次合法登录（有会话即可），且要求输入当前动态码 ——
// 否则一个被劫持的会话能悄悄把受害者手里的恢复码全部作废。
func (s *TOTPService) RegenerateRecoveryCodes(ctx context.Context, userID uint, code string) ([]string, error) {
	if _, err := s.Verify(ctx, userID, code); err != nil {
		return nil, err
	}
	return s.regenerateRecoveryCodes(ctx, userID)
}

// ---------------------------------------------------------------- 临时票据

// TOTPTicketTTL 二次验证临时票据的有效期。
//
// 5 分钟：足够用户掏出手机看验证码，又短到「被截获也基本没用」。
const TOTPTicketTTL = 5 * time.Minute

// ticketNonceKey 票据一次性消费的 Redis 键前缀。
//
// 光有 5 分钟过期不够 —— 同一张票据在 5 分钟内可以被重放。
// 用 SetNX 记录「这张票据已经用掉了」，才能真正做到一次性。
const ticketNonceKey = "totp:ticket:"

// hashRecoveryCode 恢复码哈希。去掉分隔符、统一大写后再哈希，
// 这样用户输入 "abcd-efgh" 和 "ABCDEFGH" 都能命中。
func hashRecoveryCode(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// normalizeRecoveryCode 去掉所有分隔符并转大写
func normalizeRecoveryCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(code)) {
		if r == '-' || r == ' ' || r == '_' {
			continue
		}
		// 只保留字母数字，避免把用户误输入的中文/符号算进去
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// formatRecoveryCode 把随机串切成 4-4-4 便于抄写
func formatRecoveryCode(raw string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.ToUpper(raw) {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			if n == 4 || n == 8 {
				b.WriteRune('-')
			}
			b.WriteRune(r)
			n++
			if n == 12 {
				break
			}
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- 临时票据（第二步）

// IssueTicket 签发一枚「已过密码、待过 2FA」的临时票据。
//
// 为什么必须是独立的一次性票据，而不是「带标记的登录态」：
// 如果前端能在未过 2FA 时就拿到一个可调用后台接口的令牌，
// 那 2FA 只是一层装饰 —— 攻击者拿到密码后直接跳过第二步。
//
// 因此：
//   - typ=totp，Auth 中间件的 IsStaffToken() 会拒绝它（只认 ""/staff）
//   - 绑一个随机 nonce 写进 Redis，消费即删除，物理上只能用一次
func (s *TOTPService) IssueTicket(ctx context.Context, userID uint, username string) (string, error) {
	nonce, err := crypto.RandomToken(16)
	if err != nil {
		return "", err
	}

	now := time.Now()
	claims := Claims{
		UserID:   userID,
		Username: username,
		Type:     TokenTypeTOTP,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        nonce,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TOTPTicketTTL)),
			Issuer:    "kamihub",
			Subject:   fmt.Sprintf("totp:%d", userID),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).
		SignedString([]byte(s.cfg.JWT.Secret))
	if err != nil {
		return "", err
	}

	// 记录 nonce 的「未被使用」状态。Redis 不可用时直接失败 ——
	// 这里不能降级放行，否则一次性保证就没了。
	if s.cache != nil {
		ok, err := s.cache.SetNX(ctx, ticketNonceKey+nonce, "1", TOTPTicketTTL)
		if err != nil || !ok {
			return "", errors.New("签发验证票据失败，请稍后重试")
		}
	}
	return signed, nil
}

// ConsumeTicket 校验并**销毁**一枚临时票据，返回它代表的用户。
func (s *TOTPService) ConsumeTicket(ctx context.Context, ticket string) (*models.User, error) {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" {
		return nil, ErrTOTPTicketBad
	}

	parsed, err := jwt.ParseWithClaims(ticket, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("不支持的签名算法: %v", t.Header["alg"])
		}
		return []byte(s.cfg.JWT.Secret), nil
	})
	if err != nil {
		return nil, ErrTOTPTicketBad
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid || claims.Type != TokenTypeTOTP {
		return nil, ErrTOTPTicketBad
	}

	// 原子地「取走」nonce：删成功才说明这张票还没被用过。
	if s.cache != nil {
		n, err := s.cache.Raw().Del(ctx, ticketNonceKey+claims.ID).Result()
		if err != nil || n == 0 {
			// n==0：已消费或已过期。两种情况都当无效处理。
			return nil, ErrTOTPTicketBad
		}
	}

	var user models.User
	if err := s.db.WithContext(ctx).First(&user, claims.UserID).Error; err != nil {
		return nil, ErrTOTPTicketBad
	}
	if !user.IsEnabled() {
		return nil, ErrUserDisabled
	}
	return &user, nil
}

var _ = subtle.ConstantTimeCompare // 保留：恢复码比较如需恒时可改用
