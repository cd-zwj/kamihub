package models

import "time"

// 工单状态
const (
	// TicketStatusPending 买家已提交，等运营处理
	TicketStatusPending = "pending"
	// TicketStatusProcessing 运营已接手
	TicketStatusProcessing = "processing"
	// TicketStatusResolved 已解决
	TicketStatusResolved = "resolved"
	// TicketStatusClosed 已关闭（买家或运营主动关闭）
	TicketStatusClosed = "closed"
)

// 工单分类。分类决定优先级与运营的处理路径。
const (
	// TicketCategoryUndelivered 付了钱没收到货/卡密
	TicketCategoryUndelivered = "undelivered"
	// TicketCategoryInvalidCard 卡密无效、激活失败
	TicketCategoryInvalidCard = "invalid_card"
	// TicketCategoryLinkExpired 提货链接失效
	TicketCategoryLinkExpired = "link_expired"
	// TicketCategoryRefund 退款申诉
	TicketCategoryRefund = "refund"
	// TicketCategoryOther 其它
	TicketCategoryOther = "other"
)

// 消息发送方
const (
	TicketSenderBuyer  = "buyer"
	TicketSenderAdmin  = "admin"
	TicketSenderSystem = "system"
)

// Ticket 工单。
//
// 设计要点：工单的价值不在「聊天」，而在**自动携带上下文**。
// 买家来 QQ 问「我付了钱没收到货」，运营得先问订单号、再去后台查，
// 来回好几轮。这里在提交时就把订单快照写进 ContextSnapshot，
// 运营打开工单一眼就能判断是发货问题、卡密问题还是链接问题。
type Ticket struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	TicketNo string `gorm:"size:32;uniqueIndex;not null" json:"ticket_no"`

	BuyerID uint `gorm:"not null;index" json:"buyer_id"`

	// OrderID 关联订单，可为空（纯咨询类工单不挂订单）
	OrderID *uint `gorm:"index" json:"order_id,omitempty"`

	Category string `gorm:"size:32;not null;index" json:"category"`
	// Priority 1=低 2=中 3=高，按分类自动定级，运营也可手改
	Priority int    `gorm:"not null;default:2;index" json:"priority"`
	Subject  string `gorm:"size:200;not null" json:"subject"`

	Status string `gorm:"size:32;not null;default:pending;index" json:"status"`

	// ContextSnapshot 提交时的订单上下文快照（JSON）。
	// 存快照而不是每次实时查，是为了让「运营看到的」与「买家提交时的」一致，
	// 也避免订单后续状态变化导致理解偏差。
	ContextSnapshot string `gorm:"type:text" json:"context_snapshot,omitempty"`

	// 未读计数，两边各一个，省掉独立的已读表
	BuyerUnread int `gorm:"not null;default:0" json:"buyer_unread"`
	AdminUnread int `gorm:"not null;default:0" json:"admin_unread"`

	LastReplyAt *time.Time `json:"last_reply_at"`
	// LastReplyBy 最后一条消息是谁发的，列表里用来区分「等谁回」
	LastReplyBy string `gorm:"size:16" json:"last_reply_by"`

	// 处理人（运营）
	AdminID    *uint      `json:"admin_id,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TicketMessage 工单里的一条消息。
type TicketMessage struct {
	ID       uint `gorm:"primaryKey" json:"id"`
	TicketID uint `gorm:"not null;index" json:"ticket_id"`

	// SenderType buyer / admin / system
	SenderType string `gorm:"size:16;not null" json:"sender_type"`
	// SenderID 对应 buyers.id 或 users.id，system 消息为 NULL
	SenderID *uint `json:"sender_id,omitempty"`
	// SenderName 冗余存名字，运营账号被删也能看出当时是谁回的
	SenderName string `gorm:"size:64" json:"sender_name"`

	Content string `gorm:"type:text;not null" json:"content"`
	// Images 图片 URL 数组（JSON），复用现有上传能力
	Images string `gorm:"type:text" json:"images,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

func (Ticket) TableName() string        { return "tickets" }
func (TicketMessage) TableName() string { return "ticket_messages" }
