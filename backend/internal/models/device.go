package models

import "time"

// UserDevice 记录某个员工账号用过的浏览器设备。
//
// 为什么按「设备」而不是「IP」判定 —— 这是被真实场景推翻过一次的设计：
// 站长需要用代理/梯子访问后台，出口 IP 每次都不一样。
// 若按 IP 判定新设备，他**每次登录都会收到一条告警**，
// 几天后就会把提醒关掉，功能等于没做。
//
// 真正稳定的信号是浏览器本身（同一个浏览器带着同一个长期 Cookie）。
// 而「没有 Cookie」恰好精确覆盖了我们真正关心的两种情况：
//   - 攻击者拿到密码，在他自己的电脑上登录
//   - 有人在别的设备上用了你的登录态
//
// DeviceHash 存的是 Cookie 值的 SHA-256，**不存原值**：
// 这样即使数据库泄露，也无法拿库里的数据去伪造一台「已信任设备」。
type UserDevice struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"not null;index:idx_device_user_hash,unique" json:"user_id"`
	// DeviceHash 设备标识的 SHA-256（hex）。Cookie 原值只存在于浏览器里。
	DeviceHash string `gorm:"size:64;not null;index:idx_device_user_hash,unique" json:"-"`
	// Label 展示用的简短描述，由 UA 推导，如「Chrome · Windows」
	Label string `gorm:"size:64" json:"label"`
	// UserAgent 首次见到该设备时的完整 UA，排障时看
	UserAgent string `gorm:"size:255" json:"user_agent"`
	// FirstIP / LastIP 该设备用过的首末 IP。
	// 用梯子访问时这两个值会一直变，属正常现象，不参与判定。
	FirstIP string `gorm:"size:64" json:"first_ip"`
	LastIP  string `gorm:"size:64" json:"last_ip"`
	// FirstSeenAt 该设备首次登录时间 —— 告警就发生在这一刻
	FirstSeenAt time.Time `gorm:"not null" json:"first_seen_at"`
	// LastSeenAt 最近一次登录时间
	LastSeenAt time.Time `gorm:"not null;index" json:"last_seen_at"`
	// HitCount 该设备累计登录次数
	HitCount int64 `gorm:"not null;default:1" json:"hit_count"`
}

// TableName 固定表名，避免 GORM 复数化规则变化导致迁移意外。
func (UserDevice) TableName() string { return "user_devices" }
