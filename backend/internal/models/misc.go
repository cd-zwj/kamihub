package models

import (
	"time"

	"gorm.io/datatypes"
)

// Setting 全局设置，键值对 + JSON 值。
// 约定的 key：
//
//	contact        联系方式（微信/QQ/邮箱/TG/工作时间）
//	brand          品牌（站点名/Logo/页脚）
//	delivery_page  提货页显示项与附加内容模板
//	security       提货链接默认有效期等
//	payment        第三方支付（易支付）配置与开关
const (
	SettingContact      = "contact"
	SettingBrand        = "brand"
	SettingDeliveryPage = "delivery_page"
	SettingSecurity     = "security"
	SettingPayment      = "payment"
)

type Setting struct {
	Key       string         `gorm:"primaryKey;size:64" json:"key"`
	Value     datatypes.JSON `gorm:"type:jsonb" json:"value"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func (Setting) TableName() string { return "settings" }

// AuditLog 操作审计。凡是改配置、改用户、作废链接这类动作都要留痕。
type AuditLog struct {
	ID         uint           `gorm:"primaryKey" json:"id"`
	UserID     *uint          `gorm:"index" json:"user_id"`
	Username   string         `gorm:"size:64" json:"username"`
	Action     string         `gorm:"size:64;not null;index" json:"action"`
	TargetType string         `gorm:"size:64" json:"target_type"`
	TargetID   string         `gorm:"size:64" json:"target_id"`
	Detail     datatypes.JSON `gorm:"type:jsonb" json:"detail"`
	IP         string         `gorm:"size:64" json:"ip"`
	CreatedAt  time.Time      `json:"created_at"`
}

func (AuditLog) TableName() string { return "audit_logs" }

// PlatformCallLog 上游调用日志。请求/响应中的敏感字段已脱敏。
type PlatformCallLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	PlatformID   uint      `gorm:"not null;index" json:"platform_id"`
	OrderID      *uint     `gorm:"index" json:"order_id"`
	Action       string    `gorm:"size:64;index" json:"action"`
	URL          string    `gorm:"size:512" json:"url"`
	HTTPStatus   int       `json:"http_status"`
	ElapsedMS    int       `json:"elapsed_ms"`
	Success      bool      `json:"success"`
	ErrorMsg     string    `gorm:"type:text" json:"error_msg"`
	RequestBody  string    `gorm:"type:text" json:"request_body"`
	ResponseBody string    `gorm:"type:text" json:"response_body"`
	CreatedAt    time.Time `json:"created_at"`
}

func (PlatformCallLog) TableName() string { return "platform_call_logs" }
