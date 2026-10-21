package models

import "time"

// UserLoginIP 记录某个员工账号用过的登录 IP。
//
// 为什么需要一张表而不是只靠 users.last_login_ip：
// 只存「上一次」的 IP 时，一个在公司和家里来回登录的人会**每次都**被判为
// 「新 IP」，告警立刻变成噪音，然后运维关掉告警 —— 等于没有。
// 存住全部历史 IP，才能做到「只有真的第一次见到的 IP 才提醒」。
//
// 为什么不用 Redis 存集合：Redis 重启或淘汰后历史全部丢失，
// 会把所有 IP 重新当成「新 IP」再报一遍。
type UserLoginIP struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"not null;index:idx_login_ip_user_ip,unique" json:"user_id"`
	// IP 登录来源地址。内网地址也会记录，但告警文案会注明。
	IP string `gorm:"size:64;not null;index:idx_login_ip_user_ip,unique" json:"ip"`
	// UserAgent 最近一次的浏览器指纹，仅用于告警时给人看，不参与「新设备」判定。
	//
	// 不做 UA 判定是刻意的：浏览器升级、字体变化都会改 UA，
	// 按 UA 判定新设备会产生大量误报，反而淹没真正的异常。
	UserAgent string `gorm:"size:255" json:"user_agent"`
	// FirstSeenAt 该 IP 首次出现的时间 —— 告警就发生在这一刻。
	FirstSeenAt time.Time `gorm:"not null" json:"first_seen_at"`
	// LastSeenAt 最近一次登录时间
	LastSeenAt time.Time `gorm:"not null;index" json:"last_seen_at"`
	// HitCount 累计从该 IP 登录的次数
	HitCount int64 `gorm:"not null;default:1" json:"hit_count"`
}

// TableName 固定表名，避免 GORM 复数化规则变化导致迁移意外。
func (UserLoginIP) TableName() string { return "user_login_ips" }
