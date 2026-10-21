// Package mock 是一个「假上游」适配器，用于本地联调与自动化测试。
//
// 它不访问任何外部网络，行为完全由平台配置里的 extra 字段控制：
//
//	{
//	  "stock": 100,               // 可售库存
//	  "balance": 1000,            // 账户余额
//	  "unit_price": 9.5,          // 采购单价
//	  "fail_quote": "",           // 询价时模拟失败: transient | business | fatal
//	  "fail_purchase": "",        // 下单时模拟失败: transient | business | fatal
//	  "delivery_status": "delivered", // delivered 立即发货 / paid 先返回待发货
//	  "poll_success_after": 2,    // delivery_status=paid 时，第几次查单才返回已发货
//	  "delay_ms": 0,              // 模拟网络延迟
//	  "prefix": "MOCK"            // 卡密前缀
//	}
//
// 有了它，验证「多货源路由 + 故障转移 + 提货页」全链路不需要任何真实凭证。
package mock

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"kamihub/internal/models"
	"kamihub/internal/platform"
)

const (
	Code           = "mock"
	DefaultBaseURL = "http://mock.local"
)

// Adapter 模拟平台适配器
type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Code() string           { return Code }
func (a *Adapter) Name() string           { return "模拟平台（联调用）" }
func (a *Adapter) DefaultBaseURL() string { return DefaultBaseURL }

func (a *Adapter) Description() string {
	return "不访问外网的假上游，用于验证多货源路由、故障转移与提货页。行为由「扩展配置」里的 JSON 控制。"
}

func (a *Adapter) CredentialFields() []models.CredentialField {
	return []models.CredentialField{
		{
			Key:         "api_key",
			Label:       "模拟 API Key",
			Required:    false,
			Placeholder: "随便填，例如 test-key",
			Help:        "模拟平台不校验凭证，留空也能用",
		},
	}
}

func (a *Adapter) Build(cfg platform.ClientConfig) (platform.Client, error) {
	o := options{
		Stock:            100,
		Balance:          decimal.NewFromInt(1000),
		UnitPrice:        decimal.RequireFromString("9.50"),
		DeliveryStatus:   "delivered",
		PollSuccessAfter: 1,
		Prefix:           "MOCK",
	}
	o.apply(cfg.Extra)

	return &Client{
		name:   cfg.PlatformName,
		opts:   o,
		onCall: cfg.OnCall,
		store:  storeFor(cfg.PlatformID),
	}, nil
}

type options struct {
	Stock            int
	Balance          decimal.Decimal
	UnitPrice        decimal.Decimal
	FailQuote        string
	FailPurchase     string
	DeliveryStatus   string
	PollSuccessAfter int
	DelayMS          int
	Prefix           string
}

func (o *options) apply(extra map[string]any) {
	if extra == nil {
		return
	}
	if v, ok := toInt(extra["stock"]); ok {
		o.Stock = v
	}
	if v, ok := toDecimal(extra["balance"]); ok {
		o.Balance = v
	}
	if v, ok := toDecimal(extra["unit_price"]); ok {
		o.UnitPrice = v
	}
	if v, ok := extra["fail_quote"].(string); ok {
		o.FailQuote = v
	}
	if v, ok := extra["fail_purchase"].(string); ok {
		o.FailPurchase = v
	}
	if v, ok := extra["delivery_status"].(string); ok {
		o.DeliveryStatus = v
	}
	if v, ok := toInt(extra["poll_success_after"]); ok {
		o.PollSuccessAfter = v
	}
	if v, ok := toInt(extra["delay_ms"]); ok {
		o.DelayMS = v
	}
	if v, ok := extra["prefix"].(string); ok && v != "" {
		o.Prefix = v
	}
}

// Client 模拟客户端
//
// 订单状态存在**包级 store**里（按平台 ID 隔离），而不是实例字段。
//
// 为什么必须这样：PlatformService.ClientByID 每次调用都会 Build 一个
// 全新的 Client 实例。若订单存在实例上，「下单」和之后的「查单 / 重查补偿」
// 会落在两个不同实例上，查单必然报「订单不存在」。
// 真实上游是服务端有状态，mock 必须模拟这一点，否则卡单补偿、
// 对账这类跨请求链路根本没法在本地验证。
type Client struct {
	name   string
	opts   options
	onCall func(platform.CallRecord)
	store  *orderStore
}

// orderStore 某个平台实例的订单存储（跨 Client 实例共享）
type orderStore struct {
	mu     sync.Mutex
	orders map[string]*platform.PurchaseResult
	hits   map[string]int
}

var (
	storeMu sync.Mutex
	stores  = map[uint]*orderStore{}
)

// storeFor 取（或创建）某平台的订单存储。
func storeFor(platformID uint) *orderStore {
	storeMu.Lock()
	defer storeMu.Unlock()
	s, ok := stores[platformID]
	if !ok {
		s = &orderStore{
			orders: map[string]*platform.PurchaseResult{},
			hits:   map[string]int{},
		}
		stores[platformID] = s
	}
	return s
}

// ResetStore 清空某平台的模拟订单。仅测试用。
func ResetStore(platformID uint) {
	storeMu.Lock()
	delete(stores, platformID)
	storeMu.Unlock()
}

func (c *Client) emit(action string, ok bool, errMsg string) {
	if c.onCall == nil {
		return
	}
	c.onCall(platform.CallRecord{
		Action:     action,
		URL:        "mock://" + c.name + "/" + action,
		HTTPStatus: 200,
		Success:    ok,
		ErrorMsg:   errMsg,
	})
}

func (c *Client) delay() {
	if c.opts.DelayMS > 0 {
		time.Sleep(time.Duration(c.opts.DelayMS) * time.Millisecond)
	}
}

// fail 把配置里的失败模式转成统一的分类错误
func fail(mode, action string) error {
	switch mode {
	case "transient":
		return platform.NewTransient(fmt.Sprintf("[模拟] %s 网络抖动", action), "MOCK_TRANSIENT", 503, errors.New("mock transient"))
	case "business":
		return platform.NewBusiness(fmt.Sprintf("[模拟] %s 余额不足", action), "MOCK_BUSINESS")
	case "fatal":
		return platform.NewFatal(fmt.Sprintf("[模拟] %s 凭证无效", action), "MOCK_FATAL")
	default:
		return nil
	}
}

func (c *Client) Ping(ctx context.Context) (*platform.AccountInfo, error) {
	c.delay()
	c.emit("merchant.balance", true, "")
	return &platform.AccountInfo{
		Balance:  c.opts.Balance,
		Currency: "CNY",
		Extra:    map[string]any{"mock": true, "platform": c.name},
	}, nil
}

func (c *Client) ListGoods(ctx context.Context, req platform.ListGoodsRequest) (*platform.ListGoodsResult, error) {
	c.delay()
	c.emit("goods.list", true, "")
	list := []platform.GoodsInfo{}
	for i := 1; i <= 5; i++ {
		no := fmt.Sprintf("%s-G%d", strings.ToUpper(c.opts.Prefix), i)
		if req.Keywords != "" && !strings.Contains(strings.ToUpper(no), strings.ToUpper(req.Keywords)) {
			continue
		}
		list = append(list, platform.GoodsInfo{
			GoodsNo:           no,
			Name:              fmt.Sprintf("[模拟] 卡密商品 %d 号", i),
			Description:       "由模拟平台生成的测试商品",
			DeliveryMode:      platform.ModeCard,
			AvailableQuantity: c.opts.Stock,
			LimitQuantity:     1,
			UnitPrice:         c.opts.UnitPrice,
			SalesCount:        i * 10,
			Source:            "supply",
		})
	}
	return &platform.ListGoodsResult{Total: len(list), List: list}, nil
}

func (c *Client) GetGoods(ctx context.Context, goodsNo string) (*platform.GoodsInfo, error) {
	c.delay()
	c.emit("goods.detail", true, "")
	return &platform.GoodsInfo{
		GoodsNo:           goodsNo,
		Name:              "[模拟] " + goodsNo,
		DeliveryMode:      platform.ModeCard,
		AvailableQuantity: c.opts.Stock,
		LimitQuantity:     1,
		UnitPrice:         c.opts.UnitPrice,
		Source:            "supply",
	}, nil
}

func (c *Client) Quote(ctx context.Context, req platform.QuoteRequest) (*platform.QuoteResult, error) {
	c.delay()
	if err := fail(c.opts.FailQuote, "询价"); err != nil {
		c.emit("purchase.quote", false, err.Error())
		return nil, err
	}
	c.emit("purchase.quote", true, "")

	total := c.opts.UnitPrice.Mul(decimal.NewFromInt(int64(req.Quantity)))
	fee := total.Mul(decimal.RequireFromString("0.006")).Round(2)

	return &platform.QuoteResult{
		GoodsNo:           req.GoodsNo,
		Quantity:          req.Quantity,
		UnitPrice:         c.opts.UnitPrice,
		GoodsAmount:       total,
		Fee:               fee,
		TotalAmount:       total.Add(fee),
		AvailableQuantity: c.opts.Stock,
		AvailableBalance:  c.opts.Balance,
		BalanceEnough:     c.opts.Balance.GreaterThanOrEqual(total.Add(fee)),
		DeliveryMode:      platform.ModeCard,
		LimitQuantity:     1,
	}, nil
}

func (c *Client) Purchase(ctx context.Context, req platform.PurchaseRequest) (*platform.PurchaseResult, error) {
	c.delay()
	if err := fail(c.opts.FailPurchase, "下单"); err != nil {
		c.emit("purchase.create", false, err.Error())
		return nil, err
	}

	now := time.Now()
	tradeNo := fmt.Sprintf("MOCK%s%04d", now.Format("150405"), now.Nanosecond()%10000)

	cards := make([]platform.Card, 0, req.Quantity)
	for i := 0; i < req.Quantity; i++ {
		cards = append(cards, platform.Card{
			Code:     fmt.Sprintf("%s-%s-%03d", c.opts.Prefix, now.Format("20060102"), i+1),
			SoldTime: now.Unix(),
		})
	}

	status := platform.StatusDelivered
	if c.opts.DeliveryStatus == "paid" {
		status = platform.StatusPaid
	}

	res := &platform.PurchaseResult{
		TradeNo:      tradeNo,
		Status:       status,
		GoodsNo:      req.GoodsNo,
		GoodsName:    "[模拟] " + req.GoodsNo,
		DeliveryMode: platform.ModeCard,
		Quantity:     req.Quantity,
		UnitPrice:    c.opts.UnitPrice,
		TotalAmount:  c.opts.UnitPrice.Mul(decimal.NewFromInt(int64(req.Quantity))),
		Cards:        cards,
		Instruction:  "模拟平台生成的卡密，仅用于联调，不具备任何真实价值。",
	}
	if status == platform.StatusDelivered {
		res.DeliveredAt = &now
	}

	c.store.mu.Lock()
	c.store.orders[tradeNo] = res
	c.store.hits[tradeNo] = 0
	c.store.mu.Unlock()

	c.emit("purchase.create", true, "")
	return res, nil
}

func (c *Client) QueryOrder(ctx context.Context, tradeNo string) (*platform.PurchaseResult, error) {
	c.delay()
	c.store.mu.Lock()
	res, ok := c.store.orders[tradeNo]
	if !ok {
		c.store.mu.Unlock()
		c.emit("purchase.query", false, "订单不存在")
		return nil, platform.NewFatal("模拟平台未找到该订单", "MOCK_NOT_FOUND")
	}
	c.store.hits[tradeNo]++
	hits := c.store.hits[tradeNo]
	opts := c.opts
	copied := *res
	c.store.mu.Unlock()

	c.emit("purchase.query", true, "")

	// 配置为「先待发货」时，需要查够 poll_success_after 次才返回已发货
	if opts.DeliveryStatus == "paid" {
		if hits >= opts.PollSuccessAfter {
			now := time.Now()
			copied.Status = platform.StatusDelivered
			copied.DeliveredAt = &now
		} else {
			copied.Status = platform.StatusPaid
			copied.Cards = nil
		}
	}
	return &copied, nil
}

// ---------------------------------------------------------------- 工具

func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func toDecimal(v any) (decimal.Decimal, bool) {
	switch t := v.(type) {
	case float64:
		return decimal.NewFromFloat(t), true
	case int:
		return decimal.NewFromInt(int64(t)), true
	case int64:
		return decimal.NewFromInt(t), true
	case string:
		d, err := decimal.NewFromString(strings.TrimSpace(t))
		if err != nil {
			return decimal.Zero, false
		}
		return d, true
	default:
		return decimal.Zero, false
	}
}

var (
	_ platform.Client  = (*Client)(nil)
	_ platform.Factory = (*Adapter)(nil)
)
