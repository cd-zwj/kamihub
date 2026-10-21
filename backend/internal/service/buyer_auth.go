package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

// 买家侧错误
var (
	ErrBuyerInvalidCredentials = errors.New("用户名或密码错误")
	ErrBuyerDisabled           = errors.New("账号已被禁用")
	ErrBuyerExists             = errors.New("该用户名已被注册")
	ErrBuyerEmailExists        = errors.New("该邮箱已被注册")
)

// BuyerClaims 买家 JWT 载荷。
//
// 与员工 Claims 的关键区别是 Type 字段：
// 两套中间件各自校验 typ，买家令牌无法通过员工中间件，
// 反之亦然。这是防止「买家 ID 命中员工 ID」越权的根本手段 ——
// 因为两张表的主键各自从 1 开始，仅靠 ID 无法区分身份体系。
type BuyerClaims struct {
	BuyerID  uint   `json:"uid"`
	Username string `json:"username"`
	// Type 固定为 TokenTypeBuyer
	Type string `json:"typ"`
	jwt.RegisteredClaims
}

// 令牌类型
const (
	TokenTypeStaff = "staff"
	TokenTypeBuyer = "buyer"
	// TokenTypeTOTP 二次验证临时票据（不是登录令牌）。
	//
	// IsStaffToken() 只认 ""/staff，因此带着它的请求过不了 Auth 中间件 ——
	// 这样「密码对、TOTP 没给」就绝不可能拿到可用权限。
	TokenTypeTOTP = "totp"
)

// BuyerAuthService 负责买家注册、登录与令牌签发。
type BuyerAuthService struct {
	db  *gorm.DB
	cfg *config.Config
}

func NewBuyerAuthService(db *gorm.DB, cfg *config.Config) *BuyerAuthService {
	return &BuyerAuthService{db: db, cfg: cfg}
}

// ---------------------------------------------------------------- 注册

// BuyerRegisterInput 注册入参
type BuyerRegisterInput struct {
	Username string
	Email    string
	Password string
	IP       string
}

// Register 创建买家账号。
func (s *BuyerAuthService) Register(ctx context.Context, in BuyerRegisterInput) (*models.Buyer, error) {
	username := strings.TrimSpace(in.Username)
	email := strings.TrimSpace(strings.ToLower(in.Email))

	if err := validateBuyerUsername(username); err != nil {
		return nil, err
	}
	if err := validateBuyerPassword(in.Password); err != nil {
		return nil, err
	}
	if email != "" && !strings.Contains(email, "@") {
		return nil, errors.New("邮箱格式不正确")
	}

	// 先查重给出友好提示；并发下仍靠唯一索引兜底
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.Buyer{}).
		Where("username = ?", username).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, ErrBuyerExists
	}
	if email != "" {
		if err := s.db.WithContext(ctx).Model(&models.Buyer{}).
			Where("email = ?", email).Count(&count).Error; err != nil {
			return nil, err
		}
		if count > 0 {
			return nil, ErrBuyerEmailExists
		}
	}

	hash, err := crypto.HashPassword(in.Password)
	if err != nil {
		return nil, fmt.Errorf("密码不合法: %w", err)
	}

	buyer := models.Buyer{
		Username:     username,
		Email:        email,
		PasswordHash: hash,
		DisplayName:  username,
		Status:       models.BuyerStatusEnabled,
		RegisterIP:   in.IP,
	}
	if err := s.db.WithContext(ctx).Create(&buyer).Error; err != nil {
		if isDuplicateKey(err) {
			return nil, ErrBuyerExists
		}
		return nil, err
	}
	return &buyer, nil
}

// ---------------------------------------------------------------- 登录

// BuyerLogin 校验买家账号密码并签发令牌。
func (s *BuyerAuthService) BuyerLogin(ctx context.Context, username, password, ip string) (*IssuedToken, *models.Buyer, error) {
	var buyer models.Buyer
	if err := s.db.WithContext(ctx).Where("username = ?", strings.TrimSpace(username)).First(&buyer).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 不区分「不存在」与「密码错」，避免账号枚举
			return nil, nil, ErrBuyerInvalidCredentials
		}
		return nil, nil, err
	}
	if !crypto.VerifyPassword(buyer.PasswordHash, password) {
		return nil, nil, ErrBuyerInvalidCredentials
	}
	if !buyer.IsEnabled() {
		return nil, nil, ErrBuyerDisabled
	}

	issued, err := s.IssueBuyerToken(&buyer)
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	// 登录统计失败不影响登录本身
	_ = s.db.WithContext(ctx).Model(&buyer).Updates(map[string]any{
		"last_login_at": now,
		"last_login_ip": ip,
	}).Error
	buyer.LastLoginAt = &now
	buyer.LastLoginIP = ip

	return issued, &buyer, nil
}

// IssueBuyerToken 签发买家 JWT，typ 固定为 buyer。
func (s *BuyerAuthService) IssueBuyerToken(buyer *models.Buyer) (*IssuedToken, error) {
	ttl := s.cfg.JWT.TTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	now := time.Now()
	expiresAt := now.Add(ttl)

	claims := BuyerClaims{
		BuyerID:  buyer.ID,
		Username: buyer.Username,
		Type:     TokenTypeBuyer,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			Issuer:    "kamihub-shop",
			Subject:   fmt.Sprintf("%d", buyer.ID),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.JWT.Secret))
	if err != nil {
		return nil, err
	}
	return &IssuedToken{
		Token:     signed,
		ExpiresAt: expiresAt,
		TTLHours:  int(ttl.Hours()),
	}, nil
}

// ParseBuyerToken 解析并校验买家令牌。
//
// 显式拒绝 typ != buyer 的令牌：员工令牌即便签名有效也不能当买家令牌用。
func (s *BuyerAuthService) ParseBuyerToken(tokenString string) (*BuyerClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &BuyerClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("不支持的签名算法: %v", t.Header["alg"])
		}
		return []byte(s.cfg.JWT.Secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*BuyerClaims)
	if !ok || !token.Valid {
		return nil, errors.New("令牌无效")
	}
	if claims.Type != TokenTypeBuyer {
		return nil, errors.New("令牌类型不匹配")
	}
	return claims, nil
}

// GetBuyer 按 ID 取买家并确认可用。
func (s *BuyerAuthService) GetBuyer(ctx context.Context, id uint) (*models.Buyer, error) {
	var buyer models.Buyer
	if err := s.db.WithContext(ctx).First(&buyer, id).Error; err != nil {
		return nil, err
	}
	if !buyer.IsEnabled() {
		return nil, ErrBuyerDisabled
	}
	return &buyer, nil
}

// ChangeBuyerPassword 买家修改自己的密码
func (s *BuyerAuthService) ChangeBuyerPassword(ctx context.Context, buyerID uint, oldPassword, newPassword string) error {
	var buyer models.Buyer
	if err := s.db.WithContext(ctx).First(&buyer, buyerID).Error; err != nil {
		return err
	}
	if !crypto.VerifyPassword(buyer.PasswordHash, oldPassword) {
		return errors.New("原密码不正确")
	}
	if err := validateBuyerPassword(newPassword); err != nil {
		return err
	}
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&buyer).Update("password_hash", hash).Error
}

// ResetBuyerPassword 重置买家密码（找回密码 / 管理员重置）
func (s *BuyerAuthService) ResetBuyerPassword(ctx context.Context, buyerID uint, newPassword string) error {
	if err := validateBuyerPassword(newPassword); err != nil {
		return err
	}
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Model(&models.Buyer{}).
		Where("id = ?", buyerID).Update("password_hash", hash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("买家不存在")
	}
	return nil
}

// ---------------------------------------------------------------- 校验

func validateBuyerUsername(name string) error {
	n := len([]rune(name))
	if n < 3 || n > 32 {
		return errors.New("用户名长度需在 3~32 个字符之间")
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
		if !ok {
			return errors.New("用户名只能包含字母、数字、下划线、中划线和点")
		}
	}
	return nil
}

func validateBuyerPassword(pwd string) error {
	if len(pwd) < 8 {
		return errors.New("密码至少 8 位")
	}
	if len(pwd) > 72 {
		// bcrypt 只取前 72 字节，超长会被静默截断，这里直接拒绝
		return errors.New("密码过长（最多 72 位）")
	}
	var hasLetter, hasDigit bool
	for _, r := range pwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errors.New("密码需同时包含字母和数字")
	}
	return nil
}
