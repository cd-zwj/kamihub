package models

import "time"

// UserRecoveryCode TOTP 恢复码。
//
// 为什么需要：管理员手机丢了 / 换了 / 时间错乱，TOTP 就再也算不出正确验证码。
// 没有恢复码的后果是「永久锁在后台外面」，只能改库。
//
// 存储约定：
//   - 只存哈希，绝不存明文（明文只在那一次响应里给出，之后无法再取）
//   - 用过的码标记 used_at，不删除 —— 保留痕迹用于事后审计「这个码什么时候被用了」
type UserRecoveryCode struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"not null;index" json:"user_id"`
	// CodeHash SHA-256(恢复码明文)。恢复码本身是高熵随机串，
	// 不存在字典攻击面，因此不需要 bcrypt 那种慢哈希。
	CodeHash  string     `gorm:"size:64;not null" json:"-"`
	UsedAt    *time.Time `json:"used_at"`
	CreatedAt time.Time  `json:"created_at"`
}

func (UserRecoveryCode) TableName() string { return "user_recovery_codes" }

// IsUsed 该恢复码是否已被使用（一次性）
func (r *UserRecoveryCode) IsUsed() bool { return r.UsedAt != nil }
