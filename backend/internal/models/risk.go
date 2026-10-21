package models

import (
	"time"

	"gorm.io/datatypes"
)

// 风控场景。规则只在对应场景下被评估，避免「注册的规则误伤下单」。
const (
	SceneRegister = "register"
	SceneLogin    = "login"
	SceneOrder    = "order"
	SceneWithdraw = "withdraw"
	SceneCommission = "commission"

	// SceneAll 通配场景：该规则在**任何**场景下都会评估。
	//
	// 黑名单必须用它（设计文档里 R7 的「场景」就是「全部」）：
	// 被拉黑的 IP 不该只在某一种操作里被拦，否则换个入口就绕过去了。
	SceneAll = "all"
)

// AllScenes 供前端下拉使用（不含通配值 —— 那是规则内部用的，不该让运营手选）
var AllScenes = []string{SceneRegister, SceneLogin, SceneOrder, SceneWithdraw, SceneCommission}

// 风控判定级别。数值越大越严格，汇总时取最大值。
const (
	LevelPass   = "pass"
	LevelReview = "review"
	LevelReject = "reject"
)

// LevelRank 把级别映射成可比较的序号，便于「取最严格的一条」。
//
// 未知级别按 pass 处理：宁可放过也不因为一条配置写错的规则把全站拦住。
func LevelRank(level string) int {
	switch level {
	case LevelReject:
		return 2
	case LevelReview:
		return 1
	default:
		return 0
	}
}

// RiskRule 风控规则。
//
// 刻意不做成 DSL：kamihub 的量级用「规则表 + 顺序评估」足够，
// 上 Drools 那类引擎只会让「改一条阈值」变成一次发版。
// 这里的 Config 是纯参数，判断逻辑写在各规则的 Go 实现里。
type RiskRule struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Code string `gorm:"size:64;not null;uniqueIndex" json:"code"`
	Name string `gorm:"size:128;not null" json:"name"`
	// Scene 该规则在哪个场景生效（register/login/order/...）
	Scene string `gorm:"size:32;not null;index" json:"scene"`
	// Level 命中后的判定：pass/review/reject
	Level string `gorm:"size:16;not null;default:review" json:"level"`
	// Priority 越小越先评估
	Priority int `gorm:"not null;default:100" json:"priority"`
	// Enabled 是否启用。
	//
	// DDL 默认值是 false（零值），而不是 true —— 又是 GORM 那个坑：
	// 「bool + default:true」会把显式写入的 false 当成「没填」，
	// 用数据库默认值把它顶成 true。出厂就关闭的规则（如分销自购）
	// 会因此被静默打开。让 DDL 默认值等于零值，两条路径才都对。
	// 应用层「默认启用」由 DefaultRules 逐条显式写 Enabled: true。
	Enabled bool `gorm:"not null;default:false" json:"enabled"`
	// Config 规则参数，例如 {"window_minutes":60,"max":3}
	Config datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"config"`
	// Description 给人看的说明，直接展示在后台，免得运营去翻代码
	Description string    `gorm:"size:512" json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (RiskRule) TableName() string { return "risk_rules" }

// RiskHit 一次规则命中记录。
//
// 无论最终放行还是拒绝都要记：这个表是复盘和申诉的唯一依据。
// 「为什么这个买家被拦了」必须能查出来，否则运营只能猜。
type RiskHit struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	RuleCode string `gorm:"size:64;not null;index" json:"rule_code"`
	Scene    string `gorm:"size:32;not null" json:"scene"`
	// Subject 主体标识：buyer:<id> / ip:<addr> / order:<no>
	Subject string `gorm:"size:128;not null;index" json:"subject"`
	Level   string `gorm:"size:16;not null" json:"level"`
	// Action 建议动作：log/captcha/manual_review/reject
	Action string `gorm:"size:32;not null" json:"action"`
	// Detail 命中时的现场快照（阈值、实际值），用于解释「为什么命中」
	Detail datatypes.JSON `gorm:"type:jsonb" json:"detail"`
	// 人工处理
	Reviewed     bool       `gorm:"not null;default:false;index" json:"reviewed"`
	ReviewedBy   *uint      `json:"reviewed_by"`
	ReviewedAt   *time.Time `json:"reviewed_at"`
	ReviewResult string     `gorm:"size:32" json:"review_result"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (RiskHit) TableName() string { return "risk_hits" }

// 黑名单类型
const (
	BlacklistIP     = "ip"
	BlacklistBuyer  = "buyer"
	BlacklistPhone  = "phone"
	BlacklistEmail  = "email"
	BlacklistBank   = "bank_account"
)

// AllBlacklistKinds 供前端下拉使用
var AllBlacklistKinds = []string{BlacklistIP, BlacklistBuyer, BlacklistPhone, BlacklistEmail, BlacklistBank}

// Blacklist 黑名单条目。
//
// ExpiresAt 必须支持：永久黑名单积累多了会误伤（共享 IP、运营商 NAT 场景），
// 到期自动失效比人工清理可靠。
type Blacklist struct {
	ID    uint   `gorm:"primaryKey" json:"id"`
	Kind  string `gorm:"size:16;not null;uniqueIndex:idx_blacklist_kind_value" json:"kind"`
	Value string `gorm:"size:255;not null;uniqueIndex:idx_blacklist_kind_value" json:"value"`
	// Reason 为什么拉黑，必填 —— 半年后没人记得当时的判断
	Reason    string     `gorm:"size:255" json:"reason"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
}

func (Blacklist) TableName() string { return "blacklist" }

// IsExpired 是否已过期（NULL 表示永久）
func (b *Blacklist) IsExpired(now time.Time) bool {
	return b.ExpiresAt != nil && b.ExpiresAt.Before(now)
}
