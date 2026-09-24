package models

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
)

// Platform 一个上游供货平台实例。
// Code 指向适配器实现（如 f16688），同一个适配器可以配多个实例（多账号）。
type Platform struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Code string `gorm:"size:64;not null;index" json:"code"`
	Name string `gorm:"size:128;not null" json:"name"`
	// Enabled 关闭后路由引擎不会再选它
	Enabled bool `gorm:"not null;default:true" json:"enabled"`
	// BaseURL 网关地址，留空则用适配器默认值
	BaseURL string `gorm:"size:255" json:"base_url"`
	// Credentials 适配器凭证的 AES-256-GCM 密文，绝不返回给前端
	Credentials string `gorm:"type:text" json:"-"`
	// Extra 适配器特有配置（如某些平台的额外参数）
	Extra datatypes.JSON `gorm:"type:jsonb" json:"extra"`

	TimeoutMS       int    `gorm:"not null;default:15000" json:"timeout_ms"`
	MaxRetries      int    `gorm:"not null;default:2" json:"max_retries"`
	RateLimitPerMin int    `gorm:"not null;default:120" json:"rate_limit_per_min"`
	SortOrder       int    `gorm:"not null;default:0" json:"sort_order"`
	Remark          string `gorm:"size:255" json:"remark"`

	// 连通性自检结果
	LastCheckedAt *time.Time `json:"last_checked_at"`
	LastCheckOK   *bool      `json:"last_check_ok"`
	LastCheckMsg  string     `gorm:"size:512" json:"last_check_msg"`

	// 余额缓存（运营钱包）
	Balance         decimal.Decimal `gorm:"type:numeric(14,2);default:0" json:"balance"`
	BalanceSyncedAt *time.Time      `json:"balance_synced_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Platform) TableName() string { return "platforms" }

// CredentialField 描述适配器需要哪些凭证字段，供前端动态渲染表单。
type CredentialField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Placeholder string `json:"placeholder"`
	Help        string `json:"help"`
}
