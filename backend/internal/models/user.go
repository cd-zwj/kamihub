// Package models 定义全部持久化实体。
package models

import "time"

// 用户角色
const (
	RoleAdmin    = "admin"    // 管理员：可管理用户、平台、系统设置
	RoleOperator = "operator" // 操作员：只能看订单、发货、提货链接
)

// 用户状态
const (
	UserStatusDisabled = 0
	UserStatusEnabled  = 1
)

// User 后台账号。单租户多操作员模型，所有用户共享同一套平台与商品数据。
type User struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Username     string     `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string     `gorm:"size:255;not null" json:"-"`
	DisplayName  string     `gorm:"size:64" json:"display_name"`
	Role         string     `gorm:"size:32;not null;default:operator" json:"role"`
	Status       int        `gorm:"not null;default:1" json:"status"`
	Remark       string     `gorm:"size:255" json:"remark"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	LastLoginIP  string     `gorm:"size:64" json:"last_login_ip"`

	// ---------- TOTP 两步验证 ----------

	// TOTPSecret 加密后的 TOTP 密钥（AES-256-GCM）。
	//
	// 为什么要加密：这个密钥等价于「第二因素的全部秘密」。
	// 库被拖走但密钥没泄露 → 攻击者仍然算不出验证码；
	// 明文存库则等于 2FA 完全失效。
	//
	// json:"-" 保证它永远不会出现在任何 API 响应里。
	TOTPSecret string `gorm:"size:255" json:"-"`
	// TOTPEnabled 是否已完成绑定（只有确认过一次正确验证码才置 true）
	TOTPEnabled bool `gorm:"not null;default:false" json:"totp_enabled"`
	// TOTPBoundAt 绑定完成时间
	TOTPBoundAt *time.Time `json:"totp_bound_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (User) TableName() string { return "users" }

func (u *User) IsAdmin() bool   { return u.Role == RoleAdmin }
func (u *User) IsEnabled() bool { return u.Status == UserStatusEnabled }
