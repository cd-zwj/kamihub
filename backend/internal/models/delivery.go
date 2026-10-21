package models

import "time"

// 提货链接状态
const (
	DeliveryStatusActive  = "active"
	DeliveryStatusRevoked = "revoked"
)

// Delivery 提货链接。token 是唯一的访问凭证，必须密码学随机、不可枚举。
type Delivery struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	OrderID uint   `gorm:"uniqueIndex;not null" json:"order_id"`
	Token   string `gorm:"size:128;uniqueIndex;not null" json:"token"`

	ExpiresAt time.Time `gorm:"not null;index" json:"expires_at"`

	// 访问统计
	FirstViewedAt *time.Time `json:"first_viewed_at"`
	LastViewedAt  *time.Time `json:"last_viewed_at"`
	ViewCount     int        `gorm:"not null;default:0" json:"view_count"`

	Status string `gorm:"size:32;not null;default:active;index" json:"status"`

	// 可选访问密码（bcrypt），为空表示不需要
	PasswordHash string `gorm:"size:255" json:"-"`

	// 可选最大访问次数，0 表示不限
	MaxViews int `gorm:"not null;default:0" json:"max_views"`

	RevokedAt   *time.Time `json:"revoked_at"`
	RevokedBy   *uint      `json:"revoked_by"`
	RevokeReason string    `gorm:"size:255" json:"revoke_reason"`

	// 生成该链接时使用的页面配置快照，保证后续改全局配置不影响历史链接
	PageSnapshot string `gorm:"type:text" json:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Delivery) TableName() string { return "deliveries" }

// IsExpired 是否已过期
func (d *Delivery) IsExpired(now time.Time) bool { return now.After(d.ExpiresAt) }

// IsUsable 是否还能正常访问
func (d *Delivery) IsUsable(now time.Time) bool {
	if d.Status != DeliveryStatusActive {
		return false
	}
	if d.IsExpired(now) {
		return false
	}
	if d.MaxViews > 0 && d.ViewCount >= d.MaxViews {
		return false
	}
	return true
}

// DeliveryView 提货页访问日志。
// 注意：绝不记录卡密内容。
type DeliveryView struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	DeliveryID uint      `gorm:"not null;index" json:"delivery_id"`
	IP         string    `gorm:"size:64" json:"ip"`
	UserAgent  string    `gorm:"size:512" json:"user_agent"`
	Referer    string    `gorm:"size:512" json:"referer"`
	CreatedAt  time.Time `json:"created_at"`
}

func (DeliveryView) TableName() string { return "delivery_views" }
