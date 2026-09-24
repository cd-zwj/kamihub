package f16688

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"kamihub/internal/models"
	"kamihub/internal/platform"
)

const (
	Code            = "f16688"
	DefaultBaseURL  = "https://www.16688.com.cn/openApi"
	credAppID       = "app_id"
	credAppSecret   = "app_secret"
)

// Adapter 16688 适配器
type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Code() string        { return Code }
func (a *Adapter) Name() string        { return "16688 开放平台" }
func (a *Adapter) DefaultBaseURL() string { return DefaultBaseURL }

func (a *Adapter) Description() string {
	return "数字卡密自动交易平台。采购接口仅支持「已代理的货源商品」（source=supply），自营商品无法通过采购接口下单。"
}

func (a *Adapter) CredentialFields() []models.CredentialField {
	return []models.CredentialField{
		{
			Key:         credAppID,
			Label:       "App ID",
			Required:    true,
			Placeholder: "商户后台 → 其他渠道 → API → 对接配置",
		},
		{
			Key:         credAppSecret,
			Label:       "App Secret",
			Required:    true,
			Secret:      true,
			Placeholder: "与 App ID 同页",
		},
	}
}

func (a *Adapter) Build(cfg platform.ClientConfig) (platform.Client, error) {
	appID := strings.TrimSpace(cfg.Credentials[credAppID])
	secret := strings.TrimSpace(cfg.Credentials[credAppSecret])
	if appID == "" || secret == "" {
		return nil, errors.New("16688 适配器需要 app_id 与 app_secret")
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}

	return &Client{
		appID:  appID,
		secret: secret,
		base:   base,
		doer:   platform.NewHTTPDoer(cfg.TimeoutMS, cfg.MaxRetries, cfg.OnCall),
	}, nil
}

// Client 绑定到某个 16688 商户账号的客户端
type Client struct {
	appID  string
	secret string
	base   string
	doer   *platform.HTTPDoer
}

// url 拼接真实地址。
// 网关 https://.../openApi 已自带前缀，而文档里的 path 又写成 /openApi/xxx，
// 因此要去掉 path 上的前缀，避免出现 /openApi/openApi/...
func (c *Client) url(path string) string {
	suffix := strings.TrimPrefix(path, "/openApi")
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	return c.base + suffix
}

// envelope 统一响应信封
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Time int64           `json:"time"`
	Data json.RawMessage `json:"data"`
}

// call 执行一次带签名的调用。
func (c *Client) call(ctx context.Context, path, action string, biz map[string]any, out any) error {
	payload := map[string]any{
		"app_id":    c.appID,
		"timestamp": strconv.FormatInt(time.Now().Unix(), 10),
	}
	for k, v := range biz {
		// 业务参数不得覆盖公共参数
		switch k {
		case "app_id", "timestamp", "sign", "sign_type":
			continue
		}
		payload[k] = v
	}

	_, sign, err := BuildSign(payload, c.secret)
	if err != nil {
		return platform.NewFatal("签名计算失败", err.Error())
	}
	payload["sign"] = sign

	var env envelope
	if _, err := c.doer.PostJSON(ctx, c.url(path), payload, nil, action, &env); err != nil {
		return err
	}

	if env.Code != 1 {
		return classify(env.Msg)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return platform.NewTransient("上游 data 字段解析失败", truncate(string(env.Data), 200), 200, err)
		}
	}
	return nil
}

// classify 把 16688 的文本错误归类到统一的 FailKind。
// 分类直接决定路由引擎会不会换下一个货源，所以要保守：拿不准就按业务错误处理（可换），
// 但凭证/签名类问题必须判为 fatal，否则会把所有货源都无谓地试一遍。
func classify(msg string) error {
	m := strings.TrimSpace(msg)

	fatalKeywords := []string{"签名", "应用不存在", "已禁用", "无权", "未认证", "参数", "非法", "格式"}
	for _, kw := range fatalKeywords {
		if strings.Contains(m, kw) {
			return platform.NewFatal(m, m)
		}
	}

	transientKeywords := []string{"过期", "超时", "繁忙", "频繁", "系统", "稍后"}
	for _, kw := range transientKeywords {
		if strings.Contains(m, kw) {
			return platform.NewTransient(m, m, 200, nil)
		}
	}

	// 余额不足、库存不足、售罄、下架等 —— 换一个货源有意义
	return platform.NewBusiness(m, m)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------- 接口实现

type apiBalance struct {
	PlatformMoney decimal.Decimal `json:"platform_money"`
	DepositMoney  decimal.Decimal `json:"deposit_money"`
	OperateMoney  decimal.Decimal `json:"operate_money"`
}

func (c *Client) Ping(ctx context.Context) (*platform.AccountInfo, error) {
	var data apiBalance
	if err := c.call(ctx, "/openApi/merchant/balance", "merchant.balance", nil, &data); err != nil {
		return nil, err
	}
	return &platform.AccountInfo{
		Balance:  data.OperateMoney,
		Currency: "CNY",
		Extra: map[string]any{
			"platform_money": data.PlatformMoney,
			"deposit_money":  data.DepositMoney,
			"operate_money":  data.OperateMoney,
			"note":           "货源采购扣运营钱包",
		},
	}, nil
}

type apiGoods struct {
	GoodsNo           string          `json:"goods_no"`
	Source            string          `json:"source"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Image             string          `json:"image"`
	DeliveryMethod    int             `json:"delivery_method"`
	AvailableQuantity int             `json:"available_quantity"`
	LimitQuantity     int             `json:"limit_quantity"`
	UnitPrice         decimal.Decimal `json:"unit_price"`
	SalesCount        int             `json:"sales_count"`
}

func (g apiGoods) toInfo() platform.GoodsInfo {
	return platform.GoodsInfo{
		GoodsNo:           g.GoodsNo,
		Name:              g.Name,
		Description:       g.Description,
		Image:             g.Image,
		DeliveryMode:      platform.DeliveryMode(g.DeliveryMethod),
		AvailableQuantity: g.AvailableQuantity,
		LimitQuantity:     g.LimitQuantity,
		UnitPrice:         g.UnitPrice,
		SalesCount:        g.SalesCount,
		Source:            g.Source,
	}
}

func (c *Client) ListGoods(ctx context.Context, req platform.ListGoodsRequest) (*platform.ListGoodsResult, error) {
	biz := map[string]any{
		"page_no":   req.PageNo,
		"page_size": req.PageSize,
		"keywords":  req.Keywords,
		"source":    req.Source,
	}
	var data struct {
		Total int        `json:"total"`
		List  []apiGoods `json:"list"`
	}
	if err := c.call(ctx, "/openApi/goods/list", "goods.list", biz, &data); err != nil {
		return nil, err
	}
	out := &platform.ListGoodsResult{Total: data.Total, List: make([]platform.GoodsInfo, 0, len(data.List))}
	for _, g := range data.List {
		out.List = append(out.List, g.toInfo())
	}
	return out, nil
}

func (c *Client) GetGoods(ctx context.Context, goodsNo string) (*platform.GoodsInfo, error) {
	var data apiGoods
	if err := c.call(ctx, "/openApi/goods/detail", "goods.detail", map[string]any{"goods_no": goodsNo}, &data); err != nil {
		return nil, err
	}
	info := data.toInfo()
	return &info, nil
}

type apiQuote struct {
	GoodsNo           string          `json:"goods_no"`
	DeliveryMethod    int             `json:"delivery_method"`
	LimitQuantity     int             `json:"limit_quantity"`
	Quantity          int             `json:"quantity"`
	UnitPrice         decimal.Decimal `json:"unit_price"`
	GoodsTotalAmount  decimal.Decimal `json:"goods_total_amount"`
	Rate              decimal.Decimal `json:"rate"`
	Fee               decimal.Decimal `json:"fee"`
	FeePayer          int             `json:"fee_payer"`
	TotalAmount       decimal.Decimal `json:"total_amount"`
	AvailableQuantity int             `json:"available_quantity"`
	AvailableMoney    decimal.Decimal `json:"available_money"`
	BalanceEnough     bool            `json:"balance_enough"`
}

func (c *Client) Quote(ctx context.Context, req platform.QuoteRequest) (*platform.QuoteResult, error) {
	var data apiQuote
	biz := map[string]any{"goods_no": req.GoodsNo, "quantity": req.Quantity}
	if err := c.call(ctx, "/openApi/purchase/quotePrice", "purchase.quote", biz, &data); err != nil {
		return nil, err
	}
	return &platform.QuoteResult{
		GoodsNo:           data.GoodsNo,
		Quantity:          data.Quantity,
		UnitPrice:         data.UnitPrice,
		GoodsAmount:       data.GoodsTotalAmount,
		Fee:               data.Fee,
		TotalAmount:       data.TotalAmount,
		AvailableQuantity: data.AvailableQuantity,
		AvailableBalance:  data.AvailableMoney,
		BalanceEnough:     data.BalanceEnough,
		DeliveryMode:      platform.DeliveryMode(data.DeliveryMethod),
		LimitQuantity:     data.LimitQuantity,
	}, nil
}

type apiCard struct {
	Code     string `json:"code"`
	SoldTime int64  `json:"sold_time"`
}

type apiOrder struct {
	TradeNo           string          `json:"trade_no"`
	GoodsNo           string          `json:"goods_no"`
	GoodsName         string          `json:"goods_name"`
	DeliveryMethod    int             `json:"delivery_method"`
	Quantity          int             `json:"quantity"`
	UnitPrice         decimal.Decimal `json:"unit_price"`
	TotalAmount       decimal.Decimal `json:"total_amount"`
	Status            int             `json:"status"`
	CreateTime        int64           `json:"create_time"`
	DeliverTime       int64           `json:"deliver_time"`
	Cards             []apiCard       `json:"cards"`
	Content           string          `json:"content"`
	Instruction       string          `json:"instruction"`
	ManualInstruction string          `json:"manual_instruction"`
	ComplaintNo       string          `json:"complaint_no"`
	ComplaintStatus   int             `json:"complaint_status"`
}

// mapStatus 把 16688 的状态码映射到统一枚举。
// 0待支付 / 1待发货 / 2已关闭 / 3已退款 / 4已发货 / 5已完成
func mapStatus(s int) platform.OrderStatus {
	switch s {
	case 0:
		return platform.StatusUnpaid
	case 1:
		return platform.StatusPaid
	case 2:
		return platform.StatusClosed
	case 3:
		return platform.StatusRefunded
	case 4:
		return platform.StatusDelivered
	case 5:
		return platform.StatusFinished
	default:
		return platform.StatusPaid
	}
}

func (o apiOrder) toResult() *platform.PurchaseResult {
	cards := make([]platform.Card, 0, len(o.Cards))
	for _, c := range o.Cards {
		cards = append(cards, platform.Card{Code: c.Code, SoldTime: c.SoldTime})
	}
	res := &platform.PurchaseResult{
		TradeNo:           o.TradeNo,
		Status:            mapStatus(o.Status),
		GoodsNo:           o.GoodsNo,
		GoodsName:         o.GoodsName,
		DeliveryMode:      platform.DeliveryMode(o.DeliveryMethod),
		Quantity:          o.Quantity,
		UnitPrice:         o.UnitPrice,
		TotalAmount:       o.TotalAmount,
		Cards:             cards,
		Content:           o.Content,
		Instruction:       o.Instruction,
		ManualInstruction: o.ManualInstruction,
	}
	if o.DeliverTime > 0 {
		t := time.Unix(o.DeliverTime, 0)
		res.DeliveredAt = &t
	}
	if raw, err := json.Marshal(o); err == nil {
		res.Raw = raw
	}
	return res
}

func (c *Client) Purchase(ctx context.Context, req platform.PurchaseRequest) (*platform.PurchaseResult, error) {
	biz := map[string]any{
		"goods_no": req.GoodsNo,
		"quantity": req.Quantity,
	}
	if strings.TrimSpace(req.Remark) != "" {
		biz["remark"] = req.Remark
	}
	var data apiOrder
	if err := c.call(ctx, "/openApi/purchase/create", "purchase.create", biz, &data); err != nil {
		return nil, err
	}
	return data.toResult(), nil
}

func (c *Client) QueryOrder(ctx context.Context, tradeNo string) (*platform.PurchaseResult, error) {
	var data apiOrder
	if err := c.call(ctx, "/openApi/purchase/query", "purchase.query", map[string]any{"trade_no": tradeNo}, &data); err != nil {
		return nil, err
	}
	return data.toResult(), nil
}

// 编译期确保接口实现完整
var (
	_ platform.Client  = (*Client)(nil)
	_ platform.Factory = (*Adapter)(nil)
)
