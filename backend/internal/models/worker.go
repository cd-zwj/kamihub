package models

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
)

// ---------------------------------------------------------------- 定时任务运行记录

// WorkerTaskRun 记录一次定时任务的执行结果。
//
// 为什么需要落库：定时任务是「无人看着的后台进程」，
// 一旦它静默失败（连不上上游、panic、被锁挡住），
// 运营完全无感知，直到买家投诉才发现库存是陈旧的。
// 有了这张表，后台可以直接看到「哪个任务最后一次跑在什么时候、成没成功」。
type WorkerTaskRun struct {
	ID    uint   `gorm:"primaryKey" json:"id"`
	Task  string `gorm:"size:64;not null;index:idx_wtr_task_started,priority:1" json:"task"`

	StartedAt  time.Time  `gorm:"not null;index:idx_wtr_task_started,priority:2" json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`

	OK         bool   `gorm:"not null;default:false" json:"ok"`
	Affected   int64  `gorm:"not null;default:0" json:"affected"`
	ErrorMsg   string `gorm:"type:text" json:"error_msg"`
	Detail     string `gorm:"type:text" json:"detail"`
	DurationMS int64  `gorm:"not null;default:0" json:"duration_ms"`
}

func (WorkerTaskRun) TableName() string { return "worker_task_runs" }

// ---------------------------------------------------------------- 对账差异

// 对账差异类型
const (
	// ReconcileOrderMissingLocal 上游有单、本地无：通常是下单响应超时或写库失败
	ReconcileOrderMissingLocal = "order_missing_local"
	// ReconcileOrderStatusMismatch 两边状态不一致：轮询漏了，以上游为准
	ReconcileOrderStatusMismatch = "order_status_mismatch"
	// ReconcileWalletFrozenStale 冻结超过阈值仍未结算/退款：采购中断
	ReconcileWalletFrozenStale = "wallet_frozen_stale"
	// ReconcilePaymentNotSettled 支付已成功但无对应入账流水：回调丢失
	ReconcilePaymentNotSettled = "payment_not_settled"
)

// ReconcileDiff 一条对账差异。
//
// 第一版只「检测并记录」，不自动改库：
// 改钱的事必须人工确认，自动修复留到跑稳两周后再开。
type ReconcileDiff struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	BizDate string `gorm:"size:10;not null;index:idx_rd_biz_date" json:"biz_date"`
	Kind    string `gorm:"size:32;not null;index:idx_rd_kind" json:"kind"`

	OrderNo      string          `gorm:"size:64;index" json:"order_no"`
	PlatformID   *uint           `gorm:"index" json:"platform_id"`
	TradeNo      string          `gorm:"size:128" json:"trade_no"`
	LocalStatus  string          `gorm:"size:32" json:"local_status"`
	RemoteStatus string          `gorm:"size:32" json:"remote_status"`
	Amount       decimal.Decimal `gorm:"type:numeric(12,2);not null;default:0" json:"amount"`
	Detail       datatypes.JSON  `gorm:"type:jsonb" json:"detail"`

	Resolved   bool       `gorm:"not null;default:false;index:idx_rd_open,priority:1" json:"resolved"`
	ResolvedBy *uint      `json:"resolved_by"`
	ResolvedAt *time.Time `json:"resolved_at"`
	Remark     string     `gorm:"size:255" json:"remark"`

	CreatedAt time.Time `gorm:"not null;index:idx_rd_open,priority:2" json:"created_at"`
}

func (ReconcileDiff) TableName() string { return "reconcile_diffs" }

// ---------------------------------------------------------------- 通知

// Notification 一条通知。
//
// BuyerID 为空 = 内部运维告警（给站长看）；
// 非空 = 发给某个买家的站内信。
// Channels 记录各渠道投递结果，便于排查「为什么没收到」。
type Notification struct {
	ID      uint  `gorm:"primaryKey" json:"id"`
	BuyerID *uint `gorm:"index:idx_notif_buyer,priority:1" json:"buyer_id"`

	Level   string `gorm:"size:16;not null;default:info" json:"level"`
	Event   string `gorm:"size:64;not null;index:idx_notif_event,priority:1" json:"event"`
	Title   string `gorm:"size:255;not null" json:"title"`
	Content string `gorm:"type:text" json:"content"`
	Link    string `gorm:"size:512" json:"link"`

	Read   bool       `gorm:"not null;default:false;index:idx_notif_buyer,priority:2" json:"read"`
	ReadAt *time.Time `json:"read_at"`

	Channels datatypes.JSON `gorm:"type:jsonb" json:"channels"`

	CreatedAt time.Time `gorm:"not null;index:idx_notif_event,priority:2" json:"created_at"`
}

func (Notification) TableName() string { return "notifications" }
