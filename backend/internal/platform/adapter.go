package platform

import (
	"context"
	"log/slog"

	"kamihub/internal/models"
)

// ClientConfig 构造一个平台客户端所需的全部信息。
type ClientConfig struct {
	PlatformID   uint
	PlatformName string
	BaseURL      string
	Credentials  Credentials
	Extra        map[string]any
	TimeoutMS    int
	MaxRetries   int
	Logger       *slog.Logger
	// OnCall 每次调用上游后回调，用于写调用日志
	OnCall func(CallRecord)
}

// CallRecord 一次上游调用的记录（已脱敏）。
type CallRecord struct {
	Action       string
	URL          string
	HTTPStatus   int
	ElapsedMS    int
	Success      bool
	ErrorMsg     string
	RequestBody  string
	ResponseBody string
}

// Client 绑定到某个平台实例的操作接口。
// 上层业务（路由引擎、订单服务）只依赖这个接口。
type Client interface {
	// Ping 校验凭证并返回账号信息（余额）
	Ping(ctx context.Context) (*AccountInfo, error)
	// ListGoods 拉取上游商品列表，用于货源选品
	ListGoods(ctx context.Context, req ListGoodsRequest) (*ListGoodsResult, error)
	// GetGoods 查询单个商品详情
	GetGoods(ctx context.Context, goodsNo string) (*GoodsInfo, error)
	// Quote 询价：拿到真实单价、手续费与余额是否充足
	Quote(ctx context.Context, req QuoteRequest) (*QuoteResult, error)
	// Purchase 下单采购
	Purchase(ctx context.Context, req PurchaseRequest) (*PurchaseResult, error)
	// QueryOrder 按上游单号查单
	QueryOrder(ctx context.Context, tradeNo string) (*PurchaseResult, error)
}

// Factory 描述一个平台适配器，并负责构造客户端。
// 新增一个平台 = 新增一个 Factory 实现 + 在 registry 里注册，不动上层代码。
type Factory interface {
	// Code 适配器唯一标识，如 "f16688"
	Code() string
	// Name 展示名
	Name() string
	// Description 说明
	Description() string
	// DefaultBaseURL 默认网关地址
	DefaultBaseURL() string
	// CredentialFields 声明需要哪些凭证字段，前端据此动态渲染表单
	CredentialFields() []models.CredentialField
	// Build 构造绑定到具体平台实例的客户端
	Build(cfg ClientConfig) (Client, error)
}

// FactoryMeta 供前端展示的适配器元信息
type FactoryMeta struct {
	Code             string                   `json:"code"`
	Name             string                   `json:"name"`
	Description      string                   `json:"description"`
	DefaultBaseURL   string                   `json:"default_base_url"`
	CredentialFields []models.CredentialField `json:"credential_fields"`
}
