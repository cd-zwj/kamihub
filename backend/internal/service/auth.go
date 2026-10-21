package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

var (
	ErrInvalidCredentials = errors.New("用户名或密码错误")
	ErrUserDisabled       = errors.New("账号已被禁用")
)

// Claims JWT 载荷（员工）
type Claims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// Type 令牌类型，固定为 TokenTypeStaff。
	//
	// 为兼容 C 端上线前签发的历史令牌，空值一律按 staff 处理
	// （见 IsStaffToken）。新签发的令牌一定会带上该字段。
	Type string `json:"typ,omitempty"`
	jwt.RegisteredClaims
}

// IsStaffToken 判断该令牌是否属于员工体系。
//
// 空 typ 视为员工，是为了让 C 端上线前签发的旧令牌继续有效；
// 买家令牌的 typ 恒为 buyer，因此永远无法通过这里。
func (c *Claims) IsStaffToken() bool {
	return c.Type == "" || c.Type == TokenTypeStaff
}

// AuthService 负责登录、令牌签发与用户管理。
type AuthService struct {
	db       *gorm.DB
	cfg      *config.Config
	settings *SettingsService
}

func NewAuthService(db *gorm.DB, cfg *config.Config, settings *SettingsService) *AuthService {
	return &AuthService{db: db, cfg: cfg, settings: settings}
}

// IssuedToken 一次 JWT 签发的结果。
type IssuedToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	TTLHours  int       `json:"ttl_hours"`
}

// DefaultTTL 当前生效的登录令牌有效期。
//
// 优先用网页上配的 jwt_ttl_hours，没配时回落到 .env 的 JWT_TTL。
// 这样运维不用改环境变量、重建容器就能调整有效期。
func (s *AuthService) DefaultTTL(ctx context.Context) time.Duration {
	if s.settings != nil {
		if h := s.settings.Security(ctx).JWTTTLHours; h >= MinJWTTTLHours {
			return time.Duration(h) * time.Hour
		}
	}
	return s.cfg.JWT.TTL
}

// Login 校验账号密码并签发 JWT。
//
// 账号开了两步验证时，这里**不签发令牌**，而是返回 ErrTOTPRequired 和已校验的用户：
// 调用方必须再走一次 TOTP 校验（换成临时票据 → 验码 → 才签发）。
// 这样即使密码泄露，攻击者也拿不到任何可用权限。
func (s *AuthService) Login(ctx context.Context, username, password, ip string) (*IssuedToken, *models.User, error) {
	var user models.User
	if err := s.db.WithContext(ctx).Where("username = ?", username).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 不区分「用户不存在」和「密码错误」，避免账号枚举
			return nil, nil, ErrInvalidCredentials
		}
		return nil, nil, err
	}

	if !crypto.VerifyPassword(user.PasswordHash, password) {
		return nil, nil, ErrInvalidCredentials
	}
	if !user.IsEnabled() {
		return nil, nil, ErrUserDisabled
	}

	// 密码正确但还需要第二步：立刻返回，绝不签发令牌。
	// 此时也不更新 last_login_at —— 那次登录还没真正完成；
	// 否则「只过了密码」的尝试会被记成一次成功登录，审计就失真了。
	if user.TOTPEnabled {
		return nil, &user, ErrTOTPRequired
	}

	issued, err := s.IssueToken(ctx, &user, 0)
	if err != nil {
		return nil, nil, err
	}

	s.touchLogin(ctx, &user, ip)
	return issued, &user, nil
}

// CompleteTOTPLogin 在二次验证通过后收尾：签发令牌并记录登录。
//
// 与 Login 分开是为了让「密码通过」和「2FA 通过」各有一处落点，
// 谁先谁后一目了然，不会出现「某条分支忘了检查 2FA」。
func (s *AuthService) CompleteTOTPLogin(ctx context.Context, user *models.User, ip string) (*IssuedToken, error) {
	issued, err := s.IssueToken(ctx, user, 0)
	if err != nil {
		return nil, err
	}
	s.touchLogin(ctx, user, ip)
	return issued, nil
}

// touchLogin 记录最后登录时间与 IP。失败不影响登录本身。
func (s *AuthService) touchLogin(ctx context.Context, user *models.User, ip string) {
	now := time.Now()
	if err := s.db.WithContext(ctx).Model(user).Updates(map[string]any{
		"last_login_at": now,
		"last_login_ip": ip,
	}).Error; err != nil {
		// 登录统计失败不该影响登录本身
		return
	}
	user.LastLoginAt = &now
	user.LastLoginIP = ip
}

// IssueToken 签发 JWT。
//
// ttl <= 0 时使用 DefaultTTL（网页设置 → .env 兜底）；
// 传入正数则按该时长签发，供管理员在网页上为对接方生成长期令牌。
func (s *AuthService) IssueToken(ctx context.Context, user *models.User, ttl time.Duration) (*IssuedToken, error) {
	if ttl <= 0 {
		ttl = s.DefaultTTL(ctx)
	}
	now := time.Now()
	expiresAt := now.Add(ttl)

	claims := Claims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		Type:     TokenTypeStaff,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			Issuer:    "kamihub",
			Subject:   fmt.Sprintf("%d", user.ID),
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

// ParseToken 解析并校验 JWT
func (s *AuthService) ParseToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("不支持的签名算法: %v", t.Header["alg"])
		}
		return []byte(s.cfg.JWT.Secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("令牌无效")
	}
	return claims, nil
}

// GetUser 按 ID 取用户，并确认账号仍然可用。
func (s *AuthService) GetUser(ctx context.Context, id uint) (*models.User, error) {
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, id).Error; err != nil {
		return nil, err
	}
	if !user.IsEnabled() {
		return nil, ErrUserDisabled
	}
	return &user, nil
}

// ChangePassword 修改自己的密码
func (s *AuthService) ChangePassword(ctx context.Context, userID uint, oldPassword, newPassword string) error {
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return err
	}
	if !crypto.VerifyPassword(user.PasswordHash, oldPassword) {
		return errors.New("原密码不正确")
	}
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&user).Update("password_hash", hash).Error
}

// ResetPassword 管理员重置他人密码
func (s *AuthService) ResetPassword(ctx context.Context, userID uint, newPassword string) error {
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", userID).Update("password_hash", hash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("用户不存在")
	}
	return nil
}
