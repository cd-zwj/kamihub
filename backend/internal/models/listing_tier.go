package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// ListingTier 商品阶梯折扣。
//
// 运营配「买满 N 件打 M 折」，命中后该单所有商品按折后单价结算。
//
// 为什么用折扣百分比而不是直接存单价：
//   - 运营脑子里想的就是「9 折」「8.5 折」，直接填百分比不用心算
//   - 商品原价调整后，阶梯价自动跟着变，不用逐档改
//   - 只在结算时算一次，避免维护两套价格
type ListingTier struct {
	ID        uint `gorm:"primaryKey" json:"id"`
	ListingID uint `gorm:"not null;index:idx_tier_listing_qty,priority:1" json:"listing_id"`

	// MinQty 满几件起生效（含）。同一商品多档按 MinQty 降序匹配。
	MinQty int `gorm:"not null;index:idx_tier_listing_qty,priority:2" json:"min_qty"`

	// Discount 折扣：85 表示 8.5 折（即打 85%）。
	// 用整数存，避免浮点误差；范围 1~99。
	Discount int `gorm:"not null" json:"discount"`

	// Label 展示文案，例如「买 10 件 9 折」。留空时前端自动拼。
	Label string `gorm:"size:64" json:"label"`

	// Enabled 单档开关，方便临时停用而不用删掉
	Enabled bool `gorm:"not null;default:true" json:"enabled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (ListingTier) TableName() string { return "listing_tiers" }

// UnitPriceFor 按原价与本档折扣算出折后单价。
//
// 四舍五入到分。极端情况下（原价极低 + 大折扣）可能算出 0，
// 这里兜底保留最低 0.01，避免出现 0 元单。
func (t *ListingTier) UnitPriceFor(base decimal.Decimal) decimal.Decimal {
	if t == nil || t.Discount <= 0 || t.Discount >= 100 {
		return base
	}
	p := base.Mul(decimal.NewFromInt(int64(t.Discount))).
		Div(decimal.NewFromInt(100)).
		Round(2)
	if !p.IsPositive() {
		return decimal.NewFromFloat(0.01)
	}
	return p
}

