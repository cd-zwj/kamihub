package models

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
)

// 商品状态
const (
	GoodsStatusOff = 0
	GoodsStatusOn  = 1
)

// 发货方式（与上游语义对齐）
const (
	DeliveryModeCard    = 1 // 卡密
	DeliveryModeContent = 2 // 内容
	DeliveryModeManual  = 3 // 人工发货
)

// 路由策略标识
const (
	StrategyPriority = "priority" // 优先级 + 故障转移
	StrategyCheapest = "cheapest" // 最低成本优先
	StrategyStock    = "stock"    // 库存最充足优先
	StrategyWeighted = "weighted" // 加权轮询
)

// 供货方式：商品从哪里拿货
const (
	// StockModeUpstream 向上游平台采购（挂 Supply 货源）
	StockModeUpstream = 1
	// StockModeLocal 用商品自带的卡密池本地发货（不依赖任何上游平台）
	StockModeLocal = 2
	// StockModeHybrid 卡密池 + 上游双通道。
	//
	// 为什么需要它：上游代发模式下，买家退款/撤单退回池子的卡密，
	// 因为商品被钉死在「只走上游」而永远卖不出去，只能烂在池子里。
	// 混合模式让同一笔订单既可能吃池子、也可能吃上游，
	// 池子里的退货库存因此重新变成可售库存。
	StockModeHybrid = 3
)

// 混合模式的取货顺序
const (
	// HybridPoolFirst 池子优先：池子够就整单吃池子，不够整单走上游。
	// 目的是尽快清掉退货/自采库存（这部分没有上游成本，毛利更高）。
	HybridPoolFirst = true
	// HybridUpstreamFirst 上游优先：先向上游买，上游全挂了才用池子兜底。
	// 目的是保住「上游有货就发上游」的既有习惯，池子只当保险。
	HybridUpstreamFirst = false
)

// AllStrategies 供前端下拉使用
var AllStrategies = []string{StrategyPriority, StrategyCheapest, StrategyStock, StrategyWeighted}

// Goods 你自己的统一商品。一个商品可以挂多个货源（supplies）。
type Goods struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Code        string `gorm:"size:64;uniqueIndex;not null" json:"code"`
	Name        string `gorm:"size:255;not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	Image       string `gorm:"size:512" json:"image"`
	Category    string `gorm:"size:64" json:"category"`
	Status      int    `gorm:"not null;default:1" json:"status"`

	DeliveryMode int             `gorm:"not null;default:1" json:"delivery_mode"`
	Price        decimal.Decimal `gorm:"type:numeric(12,2);default:0" json:"price"`

	// 路由配置
	RoutingStrategy string `gorm:"size:32;not null;default:priority" json:"routing_strategy"`
	FailoverEnabled bool   `gorm:"not null;default:true" json:"failover_enabled"`
	MaxAttempts     int    `gorm:"not null;default:3" json:"max_attempts"`

	// 单次采购数量上限。0 表示不限购。
	//
	// 默认值必须是 0：以前默认 1，新建商品时若没注意这个字段，
	// 商品就会静默变成「限购 1 件」——买家买 2 件会被本地校验拦下，
	// 而商城前端当时只按 listing.max_qty 展示，看不出任何限制，
	// 结果是买家看到「发货失败、钱退回余额」，运营查上游却明明有货。
	MaxQuantity int `gorm:"not null;default:0" json:"max_quantity"`

	// StockMode 供货方式，决定下单时去哪儿找货：
	//
	//	StockModeUpstream(1) —— 向上游平台的货源（Supply）采购，默认，历史行为
	//	StockModeLocal(2)    —— 用商品自带的卡密池（card_stocks）本地发货
	//	StockModeHybrid(3)   —— 卡密池与上游都算货，按 LocalStockFirst 决定先后
	//
	// 为什么需要它：有些商品运营自己手里就有卡密，不需要任何上游平台。
	// 这类商品没有 Supply，按老逻辑永远「没有可用的货源」而下不了单。
	StockMode int `gorm:"not null;default:1" json:"stock_mode"`

	// LocalStockFirst 仅在 StockModeHybrid 下有意义：卡密池是否优先于上游。
	//
	//	true （应用默认）—— 池子够就整单吃池子，不够整单走上游
	//	false            —— 先走上游，上游全失败才用池子兜底
	//
	// 默认 true 的理由：池子里的货是已经付过钱的沉没成本，
	// 而且是退货/自采进来的，不清掉就一直占着资金。
	//
	// 注意 DDL 默认值是 false 而不是 true，这是刻意的：
	// GORM 对「bool + default:true」字段，会把显式传入的 false 当成「没填」，
	// 于是插入时用数据库默认值把它顶成 true —— 结果就是运营选了「上游优先」
	// 却永远得到「卡密池优先」，而且不报错。把 DDL 默认值设成零值 false，
	// 让「省略」和「false」落到同一个结果上，这条路才走得通。
	// 应用层的默认 true 由 GoodsService.Create 显式赋值。
	LocalStockFirst bool `gorm:"not null;default:false" json:"local_stock_first"`

	// CostPrice 自建商品的单条成本价。仅用于算毛利与给定价做参考，
	// 不参与下单判定（上游商品的成本红线是 MaxSupplyPrice）。
	CostPrice decimal.Decimal `gorm:"type:numeric(12,2);default:0" json:"cost_price"`

	// UsageInstruction 自建商品的通用「使用说明」。
	//
	// 「卡密 + 使用方式」是一套的，所以放在商品上：同一个商品的所有卡密
	// 共用同一份说明。下单时它会被固化进订单（order.instruction），
	// 之后再改商品说明不会影响已经发出去的订单。
	UsageInstruction string `gorm:"type:text" json:"usage_instruction"`

	// MaxSupplyPrice 最高供应价（每笔采购应付总额上限，含上游手续费）。
	//
	// 用途：给商品设一条成本红线，保证不亏。采购时用询价返回的
	// TotalAmount（数量 × 单价 + 手续费）与此值比较，超过就跳过该货源、
	// 继续尝试下一个；全部货源都超限则整单失败并说明原因。
	//
	// 0 表示不限制（保持与历史行为一致）。
	MaxSupplyPrice decimal.Decimal `gorm:"type:numeric(12,2);default:0" json:"max_supply_price"`

	// ReturnCardOnCancel 撤销订单时，卡密是否默认退回卡密池。
	//
	// 默认 false（不退回），这是刻意选的保守值：
	// 卡密一旦暴露给买家看过，退回池子就会被二次卖给别人，
	// 造成「两个人拿到同一张卡密」的重复发货。
	// 所以默认按「作废处理」，要退回必须由运营显式打开。
	//
	// 注意这只是「默认值」：真正撤销订单时，弹窗里还能逐单覆盖。
	// 放在商品上是因为退回策略通常跟商品品类相关，不是每单都要重选。
	ReturnCardOnCancel bool `gorm:"not null;default:false" json:"return_card_on_cancel"`

	// 提货页覆盖配置（为空则用全局设置）
	PageOverride datatypes.JSON `gorm:"type:jsonb" json:"page_override"`

	// 附加内容模板（为空则用全局设置）
	ExtraTemplate string `gorm:"type:text" json:"extra_template"`

	Supplies []Supply `gorm:"foreignKey:GoodsID" json:"supplies,omitempty"`

	// CardTotal / CardAvailable 卡密池的统计值，不是数据库列。
	//
	// 标 `gorm:"-"` 是必须的：不标的话 GORM 会试图给它建列，
	// 而这两个值每次都要现算（卡密池可能上万条，存冗余计数必然会不一致）。
	// 列表和详情接口在返回前用 CardService 填上。
	CardTotal     int64 `gorm:"-" json:"card_total"`
	CardAvailable int64 `gorm:"-" json:"card_available"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Goods) TableName() string { return "goods" }

// IsLocalStock 该商品是否「只走自带卡密池」发货。
//
// 注意：混合模式也会吃卡密池，但它同时能走上游，
// 所以判断「能不能用卡密池」要用 UsesCardPool，不能用这个。
func (g *Goods) IsLocalStock() bool { return g.StockMode == StockModeLocal }

// IsHybridStock 该商品是否是「卡密池 + 上游」混合模式
func (g *Goods) IsHybridStock() bool { return g.StockMode == StockModeHybrid }

// UsesCardPool 该商品的货是否可能来自自带卡密池（自有 / 混合都算）
func (g *Goods) UsesCardPool() bool {
	return g.StockMode == StockModeLocal || g.StockMode == StockModeHybrid
}

// UsesUpstream 该商品的货是否可能来自上游货源（上游 / 混合都算）
func (g *Goods) UsesUpstream() bool {
	return g.StockMode == StockModeUpstream || g.StockMode == StockModeHybrid
}

// Supply 一条货源：某个商品在某个平台上的对应商品。
// 一个商品下可以有多条 Supply，路由引擎据此做选择与故障转移。
//
// (goods_id, platform_id, platform_goods_no) 上有复合唯一索引：
// 同一个商品不能重复挂同一个平台上的同一个上游商品，
// 否则路由引擎会把它们当成两个候选，白白消耗 max_attempts 的故障转移次数。
// CreateSupply 里的 isDuplicateKey 分支依赖这个索引，缺了它那段错误处理就是死代码。
type Supply struct {
	ID         uint `gorm:"primaryKey" json:"id"`
	GoodsID    uint `gorm:"not null;index;uniqueIndex:idx_supplies_identity" json:"goods_id"`
	PlatformID uint `gorm:"not null;index;uniqueIndex:idx_supplies_identity" json:"platform_id"`

	// PlatformGoodsNo 上游平台的商品编号
	PlatformGoodsNo string `gorm:"size:128;not null;uniqueIndex:idx_supplies_identity" json:"platform_goods_no"`
	// 采购成本价（缓存，实际以询价为准）
	CostPrice decimal.Decimal `gorm:"type:numeric(12,2);default:0" json:"cost_price"`

	Priority int  `gorm:"not null;default:100" json:"priority"`
	Weight   int  `gorm:"not null;default:100" json:"weight"`
	Enabled  bool `gorm:"not null;default:true" json:"enabled"`

	// 库存缓存
	Stock         int        `gorm:"not null;default:0" json:"stock"`
	StockSyncedAt *time.Time `json:"stock_synced_at"`

	// 可采购性自检结果（与 Platform 的自检字段同构）。
	//
	// 为什么需要它：商品在平台上「存在」不等于「现在能买」。
	// 起购量限制（例如起购 100 件）、已下架、余额不足，都会让询价失败，
	// 但 GetGoods 依然成功。若只把 stock 留 0，运营会误以为「只是没库存」，
	// 实际是根本买不了。所以把失败原因持久化，让后台能直接显示。
	LastCheckedAt *time.Time `json:"last_checked_at"`
	LastCheckOK   *bool      `json:"last_check_ok"`
	LastCheckMsg  string     `gorm:"size:512" json:"last_check_msg"`

	// 该货源的提货页联系方式覆盖
	ContactOverride datatypes.JSON `gorm:"type:jsonb" json:"contact_override"`

	// UpstreamInstruction 上一次从上游抓回来的「使用说明」原文。
	//
	// 为什么要存：上游供货商经常在说明里塞自己的 QQ/微信，而说明又必须展示给买家。
	// 运营需要先看到原文，才知道要改成什么。抓一次存下来，后台可直接展示。
	UpstreamInstruction string `gorm:"type:text" json:"upstream_instruction"`
	// UpstreamFetchedAt 上次抓取时间，供后台判断「这条说明是不是陈旧了」
	UpstreamFetchedAt *time.Time `json:"upstream_fetched_at"`

	// InstructionOverride 本货源专用的「使用说明」覆盖内容（富文本）。
	//
	// 语义：
	//   - 非空 → 买家看到的完全是这一段，替代上游原文
	//   - 空   → 回落到上游原文（再经全局过滤规则处理）
	//
	// 这是「每个上游商品各自一套说明」的落点：同一个商品挂多个货源时，
	// 每条货源可以有各自不同的说明，因为它们来自不同的供货商。
	InstructionOverride string `gorm:"type:text" json:"instruction_override"`

	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Platform *Platform `gorm:"foreignKey:PlatformID" json:"platform,omitempty"`
}

func (Supply) TableName() string { return "supplies" }
