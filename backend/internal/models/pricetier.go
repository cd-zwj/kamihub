package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// 销售资格状态
const (
	// AgentStatusNone 未开通（普通买家）
	AgentStatusNone = 0
	// AgentStatusActive 正常销售
	AgentStatusActive = 1
	// AgentStatusFrozen 已冻结（违规或人工暂停）
	AgentStatusFrozen = 2
)

// 档位编码。retail 是默认档，任何人都至少是 retail。
const (
	TierRetail  = "retail"
	TierBronze  = "bronze"
	TierSilver  = "silver"
	TierGold    = "gold"
	TierDiamond = "diamond"
)

// PriceTier 档位定义。
//
// 为什么按「折扣率」而不是固定价（设计文档 D2）：
// 不同商品的成本和零售价都不一样，固定价只对一个 SKU 成立；
// 折扣率是相对的，配合保底公式能自适应所有商品。
type PriceTier struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Code string `gorm:"size:32;uniqueIndex;not null" json:"code"`
	Name string `gorm:"size:64;not null" json:"name"`

	// ---- 达成条件（满足任一即可）----
	// MinMonthlyOrders 近 30 天成功订单数
	MinMonthlyOrders int `gorm:"not null;default:0" json:"min_monthly_orders"`
	// MinMonthlyAmount 近 30 天采购额
	MinMonthlyAmount decimal.Decimal `gorm:"type:numeric(14,2);not null;default:0" json:"min_monthly_amount"`

	// ---- 专价（相对零售价的折扣）----
	// DiscountRate 0.9833 表示 98.33 折。retail 固定为 1。
	DiscountRate decimal.Decimal `gorm:"type:numeric(6,4);not null;default:1" json:"discount_rate"`

	// ---- 保底与约束 ----
	// MinMarginRate 该档位要求的最低净利率。
	//
	// 注意：实际生效值 = max(本字段, MinNetMarginRate 硬编码 2%)。
	// 硬编码下限是设计文档 D7 的决定 —— 允许运营把净利率配到 0，
	// 等于允许一次配置失误把全站卖成亏本，这个风险不值得用「灵活性」去换。
	MinMarginRate decimal.Decimal `gorm:"type:numeric(6,4);not null;default:0.02" json:"min_margin_rate"`
	// MinRetailPrice 最低零售价（MAP）。仅用于后台提示，技术上无法强制代理对外卖多少。
	MinRetailPrice decimal.Decimal `gorm:"type:numeric(12,2);not null;default:0" json:"min_retail_price"`

	SortOrder int    `gorm:"not null;default:0" json:"sort_order"`
	Enabled   bool   `gorm:"not null;default:true" json:"enabled"`
	Remark    string `gorm:"size:255" json:"remark"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (PriceTier) TableName() string { return "price_tiers" }

// IsRetail 是否默认零售档（不打折）
func (t *PriceTier) IsRetail() bool { return t.Code == TierRetail }

// ListingTierPrice 商品级专价覆盖。
//
// 用途：清库存的特价品、或某个商品的成本结构特殊，需要单独定档位价。
// 没有覆盖时按「listing.sale_price × tier.discount_rate」算。
type ListingTierPrice struct {
	ID        uint            `gorm:"primaryKey" json:"id"`
	ListingID uint            `gorm:"not null;index;uniqueIndex:idx_ltp_uniq" json:"listing_id"`
	TierCode  string          `gorm:"size:32;not null;uniqueIndex:idx_ltp_uniq" json:"tier_code"`
	Price     decimal.Decimal `gorm:"type:numeric(12,2);not null" json:"price"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func (ListingTierPrice) TableName() string { return "listing_tier_prices" }

// 默认档位出厂配置（对应设计文档 §3.1，成本 110 / 零售 120 / 费率 0.25%）。
//
// 阈值和折扣率都可后台改；这里只是首次部署的起点。
func DefaultPriceTiers() []PriceTier {
	return []PriceTier{
		{
			Code: TierRetail, Name: "零售", SortOrder: 10, Enabled: true,
			MinMonthlyOrders: 0, MinMonthlyAmount: decimal.Zero,
			DiscountRate: decimal.NewFromInt(1),
			Remark:       "默认档位，任何人都至少是这一档",
		},
		{
			Code: TierBronze, Name: "铜牌", SortOrder: 20, Enabled: true,
			MinMonthlyOrders: 50, MinMonthlyAmount: decimal.NewFromInt(6000),
			DiscountRate: mustDec("0.9833"),
			Remark:       "近 30 天 ≥50 单 或 ≥¥6000",
		},
		{
			Code: TierSilver, Name: "银牌", SortOrder: 30, Enabled: true,
			MinMonthlyOrders: 200, MinMonthlyAmount: decimal.NewFromInt(24000),
			DiscountRate: mustDec("0.9667"),
			Remark:       "近 30 天 ≥200 单 或 ≥¥24000",
		},
		{
			Code: TierGold, Name: "金牌", SortOrder: 40, Enabled: true,
			MinMonthlyOrders: 500, MinMonthlyAmount: decimal.NewFromInt(60000),
			DiscountRate: mustDec("0.9500"),
			Remark:       "近 30 天 ≥500 单 或 ≥¥60000",
		},
		{
			Code: TierDiamond, Name: "钻石", SortOrder: 50, Enabled: true,
			MinMonthlyOrders: 1000, MinMonthlyAmount: decimal.NewFromInt(120000),
			DiscountRate: mustDec("0.9417"),
			Remark:       "近 30 天 ≥1000 单 或 ≥¥120000。注意余量很小，成本稍高的商品会自动回落到金牌",
		},
	}
}

// mustDec 解析十进制字符串，失败直接 panic —— 只用于代码里的常量配置。
func mustDec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic("非法的 decimal 常量: " + s)
	}
	return d
}
