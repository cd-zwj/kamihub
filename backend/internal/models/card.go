package models

import (
	"strings"
	"time"
)

// 卡密池条目的状态
const (
	// CardStatusAvailable 可售：等待被某笔订单取用
	CardStatusAvailable = 0
	// CardStatusSold 已售：已被某笔订单锁定并发出
	CardStatusSold = 1
	// CardStatusVoid 已作废：运营手工剔除（发错了、上游退款等）
	CardStatusVoid = 2
)

// CardStock 自建商品的一条卡密。
//
// 背景：kamihub 原本只有「向上游采购」这一种供货方式 —— 商品必须挂货源
// （Supply），下单时去上游平台买。但有一类商品是运营自己手里就有的：
// 卡密是自己囤的、使用说明是通用的，根本不需要上游平台。
// 这种商品挂不了货源，也就永远下不了单。
//
// 所以有了这张表：把卡密当成「库存」存起来，下单时原子取一条，
// 直接本地发货、生成提货链接，全程不碰上游。
//
// 并发安全靠数据库而不是内存锁：
//
//	UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1)
//
// SKIP LOCKED 保证两个并发订单不会拿到同一条卡密。
// 这一点很重要 —— 卡密重复发出是不可逆的事故，不像库存数字算错还能改。
type CardStock struct {
	ID      uint `gorm:"primaryKey" json:"id"`
	GoodsID uint `gorm:"not null;index" json:"goods_id"`

	// Code 卡密内容。唯一索引防止运营重复粘贴同一条卡密 ——
	// 同一个卡密卖两次，买家会直接来投诉。
	Code string `gorm:"size:4096;not null;uniqueIndex" json:"code"`

	Status int `gorm:"not null;default:0;index" json:"status"`

	// 售出后关联的订单，便于「这条卡密发给谁了」的追溯
	OrderID *uint      `gorm:"index" json:"order_id"`
	SoldAt  *time.Time `json:"sold_at"`
	// VoidReason 作废原因（仅 State=Void 时有意义）
	VoidReason string `gorm:"size:255" json:"void_reason"`

	// 这批卡密的进货成本价（单条）。用于统计毛利。
	//
	// 为什么不放在 Goods.CostPrice 上：运营不同时间补货的成本可能不同，
	// 一条一条记才准；商品上那个是「当前成本」，用做定价参考。
	CostPrice float64 `gorm:"type:numeric(12,2);default:0" json:"cost_price"`

	// BatchNo 批次号，方便运营回滚「刚导入的那一批」
	BatchNo string `gorm:"size:64;index" json:"batch_no"`

	// Remark 运营自己的备注（不进提货页）
	Remark string `gorm:"size:255" json:"remark"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Goods *Goods `gorm:"foreignKey:GoodsID" json:"goods,omitempty"`
}

func (CardStock) TableName() string { return "card_stocks" }

// StatusText 供前端显示
func (c *CardStock) StatusText() string {
	switch c.Status {
	case CardStatusAvailable:
		return "可售"
	case CardStatusSold:
		return "已售"
	case CardStatusVoid:
		return "作废"
	default:
		return "未知"
	}
}

// MaskCode 打码显示。列表接口默认不回传卡密明文 ——
// 后台账号可能是操作员，卡密明文只应在「商品详情」里按需查看。
func MaskCode(code string) string {
	s := strings.TrimSpace(code)
	if s == "" {
		return ""
	}
	r := []rune(s)
	n := len(r)
	if n <= 6 {
		return strings.Repeat("*", n)
	}
	// 保留头 3 尾 3，中间打码；太短的就不留头尾了
	if n <= 10 {
		return string(r[:2]) + strings.Repeat("*", n-4) + string(r[n-2:])
	}
	return string(r[:3]) + strings.Repeat("*", n-6) + string(r[n-3:])
}
