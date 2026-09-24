// Package platform 定义上游平台的统一适配抽象。
//
// 核心思想：所有平台差异（签名算法、请求格式、字段命名、状态码）
// 都被收敛在各自的 Adapter 实现里，上层业务只认这里的统一类型。
package platform

import (
	"encoding/json"
	"time"

	"github.com/shopspring/decimal"
)

// DeliveryMode 统一发货方式
type DeliveryMode int

const (
	ModeCard    DeliveryMode = 1 // 卡密
	ModeContent DeliveryMode = 2 // 内容
	ModeManual  DeliveryMode = 3 // 人工发货
)

func (m DeliveryMode) String() string {
	switch m {
	case ModeCard:
		return "卡密"
	case ModeContent:
		return "内容"
	case ModeManual:
		return "人工发货"
	default:
		return "未知"
	}
}

// OrderStatus 统一订单状态。
// 各平台自己的状态码必须在适配器里映射到这套枚举。
type OrderStatus string

const (
	StatusUnpaid    OrderStatus = "unpaid"    // 待支付
	StatusPaid      OrderStatus = "paid"      // 待发货
	StatusDelivered OrderStatus = "delivered" // 已发货
	StatusFinished  OrderStatus = "finished"  // 已完成
	StatusClosed    OrderStatus = "closed"    // 已关闭
	StatusRefunded  OrderStatus = "refunded"  // 已退款
)

// IsSuccess 是否算发货成功
func (s OrderStatus) IsSuccess() bool {
	return s == StatusDelivered || s == StatusFinished
}

// IsTerminalFailure 是否为终态失败
func (s OrderStatus) IsTerminalFailure() bool {
	return s == StatusClosed || s == StatusRefunded
}

// Card 一张卡密
type Card struct {
	Code     string `json:"code"`
	SoldTime int64  `json:"sold_time,omitempty"`
}

// Credentials 平台凭证键值对，具体需要哪些键由各适配器声明
type Credentials map[string]string

// AccountInfo 账号信息（余额等）
type AccountInfo struct {
	Balance  decimal.Decimal `json:"balance"`
	Currency string          `json:"currency"`
	Extra    map[string]any  `json:"extra,omitempty"`
}

// GoodsInfo 统一商品信息
type GoodsInfo struct {
	GoodsNo           string          `json:"goods_no"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Image             string          `json:"image"`
	DeliveryMode      DeliveryMode    `json:"delivery_mode"`
	AvailableQuantity int             `json:"available_quantity"`
	LimitQuantity     int             `json:"limit_quantity"`
	UnitPrice         decimal.Decimal `json:"unit_price"`
	SalesCount        int             `json:"sales_count"`
	Source            string          `json:"source"`
}

type ListGoodsRequest struct {
	PageNo   int
	PageSize int
	Keywords string
	Source   string
}

type ListGoodsResult struct {
	Total int         `json:"total"`
	List  []GoodsInfo `json:"list"`
}

type QuoteRequest struct {
	GoodsNo  string `json:"goods_no"`
	Quantity int    `json:"quantity"`
}

// QuoteResult 询价结果。金额字段统一用 decimal，避免浮点误差。
type QuoteResult struct {
	GoodsNo           string          `json:"goods_no"`
	Quantity          int             `json:"quantity"`
	UnitPrice         decimal.Decimal `json:"unit_price"`
	GoodsAmount       decimal.Decimal `json:"goods_amount"`
	Fee               decimal.Decimal `json:"fee"`
	TotalAmount       decimal.Decimal `json:"total_amount"`
	AvailableQuantity int             `json:"available_quantity"`
	AvailableBalance  decimal.Decimal `json:"available_balance"`
	BalanceEnough     bool            `json:"balance_enough"`
	DeliveryMode      DeliveryMode    `json:"delivery_mode"`
	LimitQuantity     int             `json:"limit_quantity"`
}

type PurchaseRequest struct {
	GoodsNo   string `json:"goods_no"`
	Quantity  int    `json:"quantity"`
	Remark    string `json:"remark,omitempty"`
	RequestID string `json:"-"` // 仅用于日志追踪，不透传给上游
}

// PurchaseResult 采购/查单结果
type PurchaseResult struct {
	TradeNo           string          `json:"trade_no"`
	Status            OrderStatus     `json:"status"`
	GoodsNo           string          `json:"goods_no"`
	GoodsName         string          `json:"goods_name"`
	DeliveryMode      DeliveryMode    `json:"delivery_mode"`
	Quantity          int             `json:"quantity"`
	UnitPrice         decimal.Decimal `json:"unit_price"`
	TotalAmount       decimal.Decimal `json:"total_amount"`
	Cards             []Card          `json:"cards"`
	Content           string          `json:"content"`
	Instruction       string          `json:"instruction"`
	ManualInstruction string          `json:"manual_instruction"`
	DeliveredAt       *time.Time      `json:"delivered_at,omitempty"`
	// Raw 保留上游原始响应，便于排查与后续扩展
	Raw json.RawMessage `json:"-"`
}

// CardCodes 提取卡密文本列表
func (r *PurchaseResult) CardCodes() []string {
	out := make([]string, 0, len(r.Cards))
	for _, c := range r.Cards {
		if c.Code != "" {
			out = append(out, c.Code)
		}
	}
	return out
}
