package models

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
)

// 订单状态（本系统内部状态，与上游状态解耦）
const (
	OrderStatusPending    = "pending"    // 已受理，尚未开始采购
	OrderStatusPurchasing = "purchasing" // 采购中
	OrderStatusDelivered  = "delivered"  // 采购成功、已发货
	OrderStatusFailed     = "failed"     // 全部货源都失败
	OrderStatusClosed     = "closed"     // 人工关闭
	OrderStatusCancelled  = "cancelled"  // 已撤销（卡密可能已退回卡密池）
	OrderStatusRefunded   = "refunded"   // 已退款
)

// AllOrderStatuses 供前端筛选使用
var AllOrderStatuses = []string{
	OrderStatusPending, OrderStatusPurchasing, OrderStatusDelivered,
	OrderStatusFailed, OrderStatusClosed, OrderStatusCancelled, OrderStatusRefunded,
}

// 失败类型，决定是否可以换货源重试
const (
	FailKindTransient = "transient" // 网络/5xx/限流：可换平台
	FailKindBusiness  = "business"  // 余额不足/库存不足：可换平台
	FailKindFatal     = "fatal"     // 参数/签名错误：换平台也没用
)

// Order 一次采购请求。
// RequestID 上有唯一索引，这是防重复扣款的第一道闸。
type Order struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	OrderNo   string `gorm:"size:64;uniqueIndex;not null" json:"order_no"`
	RequestID string `gorm:"size:128;uniqueIndex;not null" json:"request_id"`

	GoodsID uint `gorm:"not null;index" json:"goods_id"`
	// GoodsSnapshot 下单时的商品快照，避免商品改名后历史订单失真
	GoodsSnapshot datatypes.JSON `gorm:"type:jsonb" json:"goods_snapshot"`

	// 最终成交的货源与平台
	SupplyID   *uint `gorm:"index" json:"supply_id"`
	PlatformID *uint `gorm:"index" json:"platform_id"`

	// 上游采购单号
	PlatformTradeNo string `gorm:"size:128;index" json:"platform_trade_no"`

	Quantity    int             `gorm:"not null;default:1" json:"quantity"`
	UnitPrice   decimal.Decimal `gorm:"type:numeric(12,2);default:0" json:"unit_price"`
	TotalAmount decimal.Decimal `gorm:"type:numeric(12,2);default:0" json:"total_amount"`

	Status string `gorm:"size:32;not null;index" json:"status"`

	// 上游返回的发货内容
	Cards             datatypes.JSON `gorm:"type:jsonb" json:"cards"`
	Content           string         `gorm:"type:text" json:"content"`
	Instruction       string         `gorm:"type:text" json:"instruction"`
	ManualInstruction string         `gorm:"type:text" json:"manual_instruction"`

	// 附加内容（渲染后的最终文本）
	ExtraText string `gorm:"type:text" json:"extra_text"`

	// 失败信息
	ErrorMsg  string `gorm:"type:text" json:"error_msg"`
	ErrorKind string `gorm:"size:32" json:"error_kind"`

	Attempts int `gorm:"not null;default:0" json:"attempts"`

	// 调用方备注
	Remark string `gorm:"size:500" json:"remark"`

	CreatedByUserID *uint `json:"created_by_user_id"`

	// ---------- C 端下单（买家自助购买）----------
	//
	// 以下字段全部可空或带默认值，保证 AutoMigrate 不改动历史订单：
	// 老订单 BuyerID 为 NULL，语义就是「后台代客下单」。
	//
	// BuyerID 指向 buyers 表，与 CreatedByUserID（指向 users 表）是两套体系，
	// 不要混用：代客下单时两者可能同时有值。
	BuyerID *uint `gorm:"index" json:"buyer_id"`
	// ListingID 下单时使用的 C 端上架条目，用于回溯当时的价格与文案
	ListingID *uint `gorm:"index" json:"listing_id"`
	// PaidAmount 买家实付金额（余额扣款额），与 TotalAmount（成本口径）分开记
	PaidAmount decimal.Decimal `gorm:"type:numeric(12,2);not null;default:0" json:"paid_amount"`
	// Channel 下单渠道：admin=后台代客，web=买家自助
	Channel string `gorm:"size:16;not null;default:admin;index" json:"channel"`

	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeliveredAt *time.Time `json:"delivered_at"`

	Goods    *Goods    `gorm:"foreignKey:GoodsID" json:"goods,omitempty"`
	Platform *Platform `gorm:"foreignKey:PlatformID" json:"platform,omitempty"`
	Delivery *Delivery `gorm:"foreignKey:OrderID" json:"delivery,omitempty"`
	Buyer    *Buyer    `gorm:"foreignKey:BuyerID" json:"buyer,omitempty"`
}

func (Order) TableName() string { return "orders" }

// OrderAttempt 记录每一次货源尝试，用于排查故障转移过程。
type OrderAttempt struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	OrderID      uint      `gorm:"not null;index" json:"order_id"`
	SupplyID     uint      `gorm:"not null" json:"supply_id"`
	PlatformID   uint      `gorm:"not null" json:"platform_id"`
	PlatformName string    `gorm:"size:128" json:"platform_name"`
	Strategy     string    `gorm:"size:32" json:"strategy"`
	Success      bool      `gorm:"not null" json:"success"`
	FailKind     string    `gorm:"size:32" json:"fail_kind"`
	ErrorMsg     string    `gorm:"type:text" json:"error_msg"`
	ElapsedMS    int       `json:"elapsed_ms"`
	CreatedAt    time.Time `json:"created_at"`
}

func (OrderAttempt) TableName() string { return "order_attempts" }
