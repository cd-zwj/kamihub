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
	// CardStatusHeld 预占：已被某笔「待支付」的订单占住，还没真正卖出。
	//
	// 为什么需要它：匿名购买是「先付款、后下单」，买家从点「去支付」到
	// 支付宝回调到账、自动下单取卡，中间可能隔着几分钟。原来这条链路里
	// 卡密一直是 available，别的买家（尤其秒下的登录买家）会把它买走，
	// 于是匿名买家付款成功却收到「商品暂时缺货」—— 钱退进余额，客诉不断。
	//
	// 预占把这条窗口从「几分钟」压到「几毫秒」：
	// 创建支付单时就把卡密锁成本状态，付款成功后直接转 sold，
	// 支付超时/失败则由清理任务退回 available。
	//
	// 注意：预占的卡密**不计入可售库存**（CountAvailable 只数 available），
	// 因此所有既有的「够不够卖」判断自动把预占算进去，无需各处改口径。
	CardStatusHeld = 3
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

	// HeldByRequest 预占这条件密的支付意图幂等键（status=held 时有值）。
	//
	// 用「支付意图的 request_id」而不是订单号：预占发生在**下单之前**
	// （创建支付单时），那一刻还没有订单。付款成功后按同一个键把卡密
	// 转成 sold 并绑定订单号，失败/超时则按键整批释放。
	HeldByRequest string `gorm:"size:128;index" json:"held_by_request,omitempty"`
	// HoldExpiresAt 预占失效时间。到点还没付款就自动退回可售。
	HoldExpiresAt *time.Time `gorm:"index" json:"hold_expires_at,omitempty"`

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
	case CardStatusHeld:
		return "预占中"
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
