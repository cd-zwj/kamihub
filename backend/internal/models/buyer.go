package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// ---------------------------------------------------------------- 买家账号

// 买家状态
const (
	BuyerStatusDisabled = 0
	BuyerStatusEnabled  = 1
)

// Buyer 面向 C 端的买家账号。
//
// 与后台员工表 users 完全隔离：两张表、两套 JWT 类型、两套中间件。
// 之所以不复用 users 表加 role 区分，是因为员工的任何一条越权路径
// 都可能直接触达平台凭证与卡密池，物理隔离比角色判断更可靠。
type Buyer struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	Username     string `gorm:"size:64;uniqueIndex;not null" json:"username"`
	Email        string `gorm:"size:128;index" json:"email"`
	PasswordHash string `gorm:"size:255;not null" json:"-"`

	DisplayName string `gorm:"size:64" json:"display_name"`
	Status      int    `gorm:"not null;default:1;index" json:"status"`

	// Balance 余额缓存。权威值以 wallet_transactions 累计为准，
	// 这里冗余一份用于快速展示与下单前校验，任何写入都必须同事务更新。
	Balance decimal.Decimal `gorm:"type:numeric(14,2);not null;default:0" json:"balance"`

	Remark      string     `gorm:"size:255" json:"remark"`
	RegisterIP  string     `gorm:"size:64" json:"register_ip"`
	LastLoginAt *time.Time `json:"last_login_at"`
	LastLoginIP string     `gorm:"size:64" json:"last_login_ip"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Buyer) TableName() string { return "buyers" }

func (b *Buyer) IsEnabled() bool { return b.Status == BuyerStatusEnabled }

// ---------------------------------------------------------------- C 端商品

// Listing 是商品在 C 端的「上架条目」。
//
// 刻意与 Goods 解耦而不是直接给 goods 加字段，原因有两个：
//  1. 售价必须与内部成本/报价分离。生产上存在 price=0 的上游代发商品，
//     若 C 端直接读 goods.price 就会出现 0 元购。
//  2. 同一个 goods 可能需要多种包装（不同套餐/不同文案），一对一不够用。
//
// 因此 SalePrice 是 C 端唯一的定价来源，且必须 > 0 才允许上架。
type Listing struct {
	ID      uint `gorm:"primaryKey" json:"id"`
	GoodsID uint `gorm:"not null;index" json:"goods_id"`

	// C 端展示字段，允许与 goods 的名称/描述不同
	Title       string `gorm:"size:255;not null" json:"title"`
	Subtitle    string `gorm:"size:255" json:"subtitle"`
	Description string `gorm:"type:text" json:"description"`
	CoverImage  string `gorm:"size:512" json:"cover_image"`
	Category    string `gorm:"size:64;index" json:"category"`

	// SalePrice C 端售价。上架校验要求 > 0。
	SalePrice decimal.Decimal `gorm:"type:numeric(12,2);not null;default:0" json:"sale_price"`
	// OriginPrice 划线价，仅用于展示，不参与计算
	OriginPrice decimal.Decimal `gorm:"type:numeric(12,2);not null;default:0" json:"origin_price"`

	// 限购，0 表示不限制
	MinQty int `gorm:"not null;default:1" json:"min_qty"`
	MaxQty int `gorm:"not null;default:0" json:"max_qty"`

	SortOrder int  `gorm:"not null;default:0;index" json:"sort_order"`
	IsListed  bool `gorm:"not null;default:false;index" json:"is_listed"`

	// 已售数量，用于热度排序展示
	SoldCount int64 `gorm:"not null;default:0" json:"sold_count"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Goods *Goods `gorm:"foreignKey:GoodsID" json:"goods,omitempty"`
}

func (Listing) TableName() string { return "listings" }

// ---------------------------------------------------------------- 钱包

// 流水类型
const (
	WalletTypeRecharge    = "recharge"     // 充值卡密充值
	WalletTypePurchase    = "purchase"     // 下单扣款
	WalletTypeRefund      = "refund"       // 下单失败退款
	WalletTypeAdminAdjust = "admin_adjust" // 管理员人工调账
)

// 流水状态
const (
	// WalletStatusSettled 已结算，金额已计入余额
	WalletStatusSettled = "settled"
	// WalletStatusFrozen 已冻结：下单扣款已占用，等采购结果。
	// 超时未决的 frozen 流水由对账任务解冻退款，避免买家余额被永久占用。
	WalletStatusFrozen = "frozen"
	// WalletStatusRefunded 已退回
	WalletStatusRefunded = "refunded"
	// WalletStatusPending 待支付（预留给后续第三方支付接入）
	WalletStatusPending = "pending"
)

// WalletTransaction 余额流水，是余额的唯一真相来源。
//
// RequestID 上有唯一索引，这是防重复扣款的闸门：
// 买家重复提交同一请求只会产生一条流水。
type WalletTransaction struct {
	ID      uint `gorm:"primaryKey" json:"id"`
	BuyerID uint `gorm:"not null;index" json:"buyer_id"`

	Type string `gorm:"size:32;not null;index" json:"type"`
	// Amount 正数入账、负数出账
	Amount decimal.Decimal `gorm:"type:numeric(14,2);not null" json:"amount"`
	// BalanceAfter 记账后余额，便于对账与追溯
	BalanceAfter decimal.Decimal `gorm:"type:numeric(14,2);not null;default:0" json:"balance_after"`

	Status string `gorm:"size:32;not null;default:settled;index" json:"status"`

	// RequestID 幂等键（唯一）
	RequestID string `gorm:"size:128;uniqueIndex;not null" json:"request_id"`
	OrderID   *uint  `gorm:"index" json:"order_id"`

	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (WalletTransaction) TableName() string { return "wallet_transactions" }

// ---------------------------------------------------------------- 充值卡密

// 充值卡密状态
const (
	RechargeCodeUnused = 0
	RechargeCodeUsed   = 1
	RechargeCodeVoid   = 2
)

// RechargeCode 用于余额充值的卡密。
//
// 这是「余额制先行、后续接支付」方案里第一期的充值入口：
// 买家从客服购买充值码，在站点兑换成余额，再用余额下单。
// 后续接入第三方支付时，只需新增支付回调写入 recharge 流水，
// 本表与兑换逻辑不用改。
type RechargeCode struct {
	ID     uint            `gorm:"primaryKey" json:"id"`
	Code   string          `gorm:"size:64;uniqueIndex;not null" json:"code"`
	Amount decimal.Decimal `gorm:"type:numeric(12,2);not null;default:0" json:"amount"`

	Status    int        `gorm:"not null;default:0;index" json:"status"`
	UsedBy    *uint      `gorm:"index" json:"used_by"`
	UsedAt    *time.Time `json:"used_at"`
	BatchNo   string     `gorm:"size:64;index" json:"batch_no"`
	Remark    string     `gorm:"size:255" json:"remark"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (RechargeCode) TableName() string { return "recharge_codes" }

// ---------------------------------------------------------------- 支付单

// 支付单状态
const (
	// PaymentStatusPending 已创建，等待买家付款
	PaymentStatusPending = "pending"
	// PaymentStatusPaid 上游已确认收款，余额已入账
	PaymentStatusPaid = "paid"
	// PaymentStatusExpired 超时未支付
	PaymentStatusExpired = "expired"
	// PaymentStatusFailed 上游明确返回失败
	PaymentStatusFailed = "failed"
	// PaymentStatusClosed 管理员或系统主动关闭
	PaymentStatusClosed = "closed"
)

// PaymentOrder 一笔第三方支付（易支付）订单。
//
// 为什么需要独立一张表，而不是直接给 wallet_transactions 加字段：
//
//  1. 支付单有自己的生命周期（创建 → 待付款 → 已付款/超时/关闭），
//     而钱包流水是「已经发生的账」，两者语义不同。
//  2. 支付网关回调可能重复、可能乱序、也可能丢失，需要按支付单维度
//     做幂等与对账；wallet_transactions 只记录最终的入账结果。
//  3. 交易号、支付方式、回调原文这些排障信息属于支付单，不该污染流水表。
//
// TradeNo 上有唯一索引：这是防「同一笔支付重复入账」的第一道闸。
type PaymentOrder struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// TradeNo 本站生成的商户订单号，传给易支付的 out_trade_no
	TradeNo string `gorm:"size:64;uniqueIndex;not null" json:"trade_no"`

	BuyerID uint            `gorm:"not null;index" json:"buyer_id"`
	Amount  decimal.Decimal `gorm:"type:numeric(12,2);not null;default:0" json:"amount"`

	// PayType 支付方式：alipay / wxpay / qqpay
	PayType string `gorm:"size:32;not null;default:alipay" json:"pay_type"`

	Status string `gorm:"size:32;not null;default:pending;index" json:"status"`

	// ---- 上游返回的信息 ----
	// TradeNoUpstream 易支付返回的交易号（trade_no），用于和上游对账
	TradeNoUpstream string `gorm:"size:128;index" json:"trade_no_upstream"`
	// PayURL 支付跳转地址
	PayURL string `gorm:"size:1024" json:"pay_url"`
	// Qrcode 二维码内容（mapi 接口可能直接返回二维码链接）
	Qrcode string `gorm:"size:1024" json:"qrcode"`

	// ---- 回调与入账 ----
	// WalletTxID 入账后关联的钱包流水，为空表示尚未入账
	WalletTxID *uint `gorm:"index" json:"wallet_tx_id"`
	// NotifyCount 收到回调的次数，用于排查重复通知
	NotifyCount int `gorm:"not null;default:0" json:"notify_count"`
	// LastNotifyAt 最近一次回调时间
	LastNotifyAt *time.Time `json:"last_notify_at"`
	// RawNotify 最近一次回调的原始参数（保留排障证据）
	RawNotify string `gorm:"type:text" json:"raw_notify"`

	Remark    string     `gorm:"size:255" json:"remark"`
	ExpiresAt time.Time  `gorm:"not null;index" json:"expires_at"`
	PaidAt    *time.Time `json:"paid_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Buyer *Buyer `gorm:"foreignKey:BuyerID" json:"buyer,omitempty"`
}

func (PaymentOrder) TableName() string { return "payment_orders" }

// IsExpired 是否已过支付有效期
func (p *PaymentOrder) IsExpired(now time.Time) bool {
	return p.Status == PaymentStatusPending && now.After(p.ExpiresAt)
}

// ---------------------------------------------------------------- 常量导出

// AllBuyerOrderChannels 下单渠道，供后台筛选
const (
	OrderChannelAdmin = "admin" // 后台代客下单
	OrderChannelWeb   = "web"   // 买家自助下单
)

// 钱包流水类型补充：第三方支付充值
//
// 与 WalletTypeRecharge（充值码）分开记，是因为两者的对账口径完全不同：
// 充值码是站内发放的凭证，支付是真实资金流入，财务要能区分。
const (
	WalletTypeOnlinePay = "online_pay" // 在线支付充值
)
