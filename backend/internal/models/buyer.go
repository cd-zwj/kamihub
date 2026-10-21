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

	// IsGuest 匿名买家（不注册账号，用「邮箱 + 6 位安全密码」下单/查单）。
	//
	// 为什么要复用 buyers 表而不是新建表：orders.buyer_id 和
	// payment_orders.buyer_id 都有外键指向 buyers(id)，支付/发货/退款
	// 整条链路都以 buyer 为载体。匿名买家当成一种「没有登录凭证的账号」
	// 复用同一张表，就能原样跑通支付与自动发货，不必改支付核心。
	IsGuest bool `gorm:"not null;default:false;index" json:"is_guest"`
	// GuestPinHash 匿名买家的 6 位安全密码（bcrypt）。非匿名买家为空。
	GuestPinHash string `gorm:"size:255;not null;default:''" json:"-"`

	// Balance 余额缓存。权威值以 wallet_transactions 累计为准，
	// 这里冗余一份用于快速展示与下单前校验，任何写入都必须同事务更新。
	Balance decimal.Decimal `gorm:"type:numeric(14,2);not null;default:0" json:"balance"`

	Remark      string     `gorm:"size:255" json:"remark"`
	RegisterIP  string     `gorm:"size:64" json:"register_ip"`
	LastLoginAt *time.Time `json:"last_login_at"`
	LastLoginIP string     `gorm:"size:64" json:"last_login_ip"`

	// ---------- 迭代 3：销售阶梯专价 ----------

	// PriceTier 当前专价档位（缓存值，由每日重算任务维护）。
	//
	// 为什么不实时算：档位依赖「近 30 天销量」聚合，每下一单都算一次
	// 会把下单链路拖慢；而且档位本来就是日更语义，实时算没有意义。
	// 默认 retail —— 任何没被设为销售的人都是零售价，这与改造前完全一致。
	PriceTier string `gorm:"size:32;not null;default:retail;index" json:"price_tier"`
	// TierCalcAt 档位最近计算时间
	TierCalcAt *time.Time `json:"tier_calc_at"`
	// TierGraceUntil 降级宽限截止时间。
	// 跌破条件后保留原档位到此刻（设计 D5：7 天宽限，避免销售「不敢休息」）。
	TierGraceUntil *time.Time `json:"tier_grace_until"`

	// AgentStatus 销售资格：0 未开通 / 1 正常 / 2 冻结
	AgentStatus int `gorm:"not null;default:0;index" json:"agent_status"`
	// AgentBoundAt 销售资格开通时间
	AgentBoundAt *time.Time `json:"agent_bound_at"`
	// AgentRemark 销售备注（申请理由、违规记录等）
	AgentRemark string `gorm:"size:255" json:"agent_remark"`

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

// 支付单用途。区分「给钱包充值」和「直接买某个商品」。
//
// 为什么要显式区分而不是根据 listing_id 是否为空推断：
//
//  1. 付款成功后的动作完全不同 —— 充值只入余额就结束，
//     购卡还要接着自动下单发货，失败时的兜底文案也不一样。
//  2. 历史数据（这张表已有的单子）必须继续按充值处理，
//     因此新增列的默认值就是 recharge，老数据不需要回填。
//  3. 排障时一眼能看出这笔钱当初是干什么用的。
const (
	// PaymentPurposeRecharge 钱包充值（默认，历史行为不变）
	PaymentPurposeRecharge = "recharge"
	// PaymentPurposeOrder 直接购买某商品：付款成功后自动下单发货
	PaymentPurposeOrder = "order"
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

	// ---- 直付购卡（Purpose = order 时才有值）----
	//
	// 这一组列是纯新增，历史支付单全部是空值/默认值，
	// 原有的「充值」链路完全不受影响（AutoMigrate 只加列，不动数据）。
	//
	// Purpose 支付用途：recharge=钱包充值（默认）/ order=直接购买某商品
	Purpose string `gorm:"size:32;not null;default:recharge;index" json:"purpose"`
	// ListingID 购买的商品 ID（Purpose=order 时有值）
	ListingID *uint `gorm:"index" json:"listing_id,omitempty"`
	// Quantity 购买数量（Purpose=order 时有值）
	Quantity int `gorm:"not null;default:0" json:"quantity,omitempty"`
	// PurchaseRequestID 自动下单用的幂等键，前端随请求生成。
	// 同一个键重复下单不会重复扣款/重复采购，这是自动发货能安全重试的基础。
	PurchaseRequestID string `gorm:"size:128;index" json:"purchase_request_id,omitempty"`
	// OrderID 自动下单成功后关联的订单。非空即代表这笔支付已经发过货，
	// 也是「付款成功但发货没跑完」时补跑幂等判断的依据。
	OrderID *uint `gorm:"index" json:"order_id,omitempty"`
	// AutoOrderError 自动下单失败的原因。买家看到的是通用文案，
	// 这里存详细信息（上游报错等）供运营排障。
	AutoOrderError string `gorm:"size:512" json:"auto_order_error,omitempty"`
	// AutoOrderAt 最近一次尝试自动下单的时间
	AutoOrderAt *time.Time `json:"auto_order_at,omitempty"`

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

// ---------------------------------------------------------------- 下游 API 密钥

// 买家 API 密钥状态
const (
	// ApiKeyStatusEnabled 可用
	ApiKeyStatusEnabled = 1
	// ApiKeyStatusDisabled 已停用。保留记录而不是删除，
	// 便于排查「这个下游以前调过什么」以及密钥轮换历史。
	ApiKeyStatusDisabled = 0
)

// BuyerApiKey 买家自助签发的开放接口凭证（下游 / 代理用）。
//
// 为什么是 app_id + secret + HMAC 签名，而不是直接发一个 Bearer token：
//
//  1. **secret 只在调用方本地参与签名计算，永远不随请求发送。**
//     Bearer token 一旦被抓包（网关日志、代理、中间层）就等于凭证泄漏；
//     签名方案里即使抓到完整请求，也拿不到能再次签名的密钥。
//  2. 每次请求带 timestamp + nonce，服务端校验时间窗并做去重，
//     旧请求被原样重放会被直接拒掉。
//  3. 一个买家可以签多个密钥（按下游 / 按系统区分），
//     某一个泄漏时只停用那一个，不影响其它对接方。
//
// SecretEnc 存的是 AES-256-GCM 密文（crypto.Encrypt，密钥来自 ENCRYPTION_KEY），
// 刻意不做哈希：买家需要能随时回看自己的 secret（与主流开放平台一致），
// 一旦哈希就再也拿不回来了。明文只在创建 / 重置 / 主动查看时解密返回。
type BuyerApiKey struct {
	ID      uint `gorm:"primaryKey" json:"id"`
	BuyerID uint `gorm:"not null;index" json:"buyer_id"`

	// Name 备注名，便于买家区分「这个是给哪个下游 / 系统用的」
	Name string `gorm:"size:64" json:"name"`

	// AppID 公开的接入标识，会出现在每个请求里
	AppID string `gorm:"size:64;uniqueIndex;not null" json:"app_id"`
	// SecretEnc 加密后的签名密钥。json:"-" 保证它永远不会被顺手序列化出去
	SecretEnc string `gorm:"size:512;not null" json:"-"`

	Status int `gorm:"not null;default:1;index" json:"status"`

	// ---- 调用统计：买家可据此自查，运营也能看出密钥是否在被使用 ----
	CallCount  int64      `gorm:"not null;default:0" json:"call_count"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `gorm:"size:64" json:"last_used_ip"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (BuyerApiKey) TableName() string { return "buyer_api_keys" }

// IsEnabled 密钥是否可用
func (k *BuyerApiKey) IsEnabled() bool { return k.Status == ApiKeyStatusEnabled }
