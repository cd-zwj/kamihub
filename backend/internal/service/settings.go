package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"kamihub/internal/models"
	"kamihub/internal/pkg/textfilter"
)

// ContactItem 一条联系方式
type ContactItem struct {
	Type    string `json:"type"` // wechat / qq / email / telegram / other
	Label   string `json:"label"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

// ContactSettings 卖家联系方式（提货页展示）
type ContactSettings struct {
	Items     []ContactItem `json:"items"`
	WorkHours string        `json:"work_hours"`
	Notice    string        `json:"notice"`
}

// EnabledItems 只返回启用的联系方式
func (c ContactSettings) EnabledItems() []ContactItem {
	out := make([]ContactItem, 0, len(c.Items))
	for _, it := range c.Items {
		if it.Enabled && strings.TrimSpace(it.Value) != "" {
			out = append(out, it)
		}
	}
	return out
}

// BrandSettings 品牌信息
type BrandSettings struct {
	SiteName   string `json:"site_name"`
	LogoURL    string `json:"logo_url"`
	FooterNote string `json:"footer_note"`
}

// DeliveryPageSettings 提货页显示项与附加内容
type DeliveryPageSettings struct {
	ShowGoodsImage  bool   `json:"show_goods_image"`
	ShowGoodsName   bool   `json:"show_goods_name"`
	ShowOrderNo     bool   `json:"show_order_no"`
	ShowQuantity    bool   `json:"show_quantity"`
	ShowDeliverTime bool   `json:"show_deliver_time"`
	ShowInstruction bool   `json:"show_instruction"`
	ShowContact     bool   `json:"show_contact"`
	ShowNotice      bool   `json:"show_notice"`
	AllowCopy       bool   `json:"allow_copy"`
	ExtraTemplate   string `json:"extra_template"`
	ExtraPosition   string `json:"extra_position"` // after / before
	ExpiredHint     string `json:"expired_hint"`

	// InstructionRules 使用说明的过滤规则。
	//
	// 上游供货商常把自己联系方式写进商品说明，这些说明会原样展示给买家，
	// 等于把复购流量送给供货商。这里配置的规则会在渲染提货页前应用到
	// 「使用说明」上（卡密与订单存档不动）。
	InstructionRules []textfilter.Rule `json:"instruction_rules"`
}

// SecuritySettings 提货链接与风控
type SecuritySettings struct {
	DefaultTTLHours int `json:"default_ttl_hours"`
	// JWTTTLHours 登录令牌有效期（小时）。网页可改，0 表示回落到 .env 的 JWT_TTL。
	JWTTTLHours     int     `json:"jwt_ttl_hours"`
	RequirePassword bool    `json:"require_password"`
	MaxViews        int     `json:"max_views"`
	RecordViewLog   bool    `json:"record_view_log"`
	MaxDailyAmount  float64 `json:"max_daily_amount"` // 0 = 不限

	// ---------- 迭代 2：安全开关（运维保险） ----------
	//
	// 为什么这几个字段是「反向」的（xxx_disabled 而不是 xxx_enabled）：
	// settings 是一行 JSON，解码时缺失的字段等于零值 false。
	// 如果用 xxx_enabled，那么「升级到新版本、旧配置里没有这个键」就会被解成
	// 「功能关闭」—— 一次部署静默关掉全站防护，这是最危险的一类回归。
	// 反向命名后，零值 = 保持原行为（防护开着），要关必须显式操作。
	//
	// 对照项目 dujiaoka-next 的做法：它的每个安全手段都有 is_open_xxx 开关
	// （is_open_img_code / is_open_geetest / is_open_search_pwd），
	// 好处是被误伤时能一键关掉，而不用改代码发版。这里保留这个「可关」能力。

	// CaptchaDisabled 关闭注册/登录验证码。默认 false = 开启。
	CaptchaDisabled bool `json:"captcha_disabled"`
	// RiskDisabled 关闭风控评估。默认 false = 开启。
	// 关掉后所有规则不再评估（仍会写日志），用于规则配错导致误拦时应急。
	RiskDisabled bool `json:"risk_disabled"`
	// AntiRedEnabled 启用「防红」：微信/QQ 内置浏览器打开时给出提示页。
	// 默认 false = 不拦截（这是新增行为，默认不改变现状）。
	AntiRedEnabled bool `json:"anti_red_enabled"`
	// OrderCaptchaMode 下单验证码策略：off / review / always。
	// 空串按 review 处理（归一化在 Security() 里做）。
	OrderCaptchaMode string `json:"order_captcha_mode"`
	// CaptchaProvider 验证码实现：空串或 arithmetic = 内置算术题。
	// 预留 geetest / turnstile 等第三方接入位，见 CaptchaService 的说明。
	CaptchaProvider string `json:"captcha_provider"`

	// ---------- 迭代 3：销售阶梯专价 ----------
	//
	// AgentDisabled 阶梯专价总开关（反向字段，与上面几个开关同一套理由）：
	// 缺配置时零值 false = 保持开启，不会因为一次升级把全站价格体系静默关掉。
	// 关掉后：不再重算档位，且 BuyerQuote 对所有人按零售价。
	AgentDisabled bool `json:"agent_disabled"`
	// AgentSelfApply 是否允许买家自助提交销售申请。
	// V1 建议 false：早期只放信任的人（设计 §11 第 6 条）。
	AgentSelfApply bool `json:"agent_self_apply"`

	// ---------- 迭代 5：S2 / S3 ----------
	//
	// ConfirmDisabled 关闭「敏感操作二次确认」。默认 false = 要求确认。
	//
	// 需确认的动作：新建/修改上游平台、人工调整买家余额、查看卡密明文。
	// 这几个都不可逆，且后台登录态是长期 JWT —— 手机被人拿到就够了。
	ConfirmDisabled bool `json:"confirm_disabled"`
	// LoginAlertDisabled 关闭「新 IP 登录提醒」。默认 false = 提醒。
	// 关掉后仍会记录 IP（表 user_login_ips），只是不再推告警。
	LoginAlertDisabled bool `json:"login_alert_disabled"`
}

// ConfirmOn 是否需要敏感操作二次确认
func (s SecuritySettings) ConfirmOn() bool { return !s.ConfirmDisabled }

// LoginAlertOn 是否推送新 IP 登录提醒
func (s SecuritySettings) LoginAlertOn() bool { return !s.LoginAlertDisabled }

// AgentEnabled 阶梯专价是否启用
func (s SecuritySettings) AgentEnabled() bool { return !s.AgentDisabled }

// 下单验证码策略
const (
	OrderCaptchaOff    = "off"
	OrderCaptchaReview = "review" // 仅风控判为 review 时要求（默认）
	OrderCaptchaAlways = "always"
)

// NormalizeOrderCaptchaMode 把策略归一到合法值。
//
// 空串（老配置没有这个键）按 review 处理，而不是 off ——
// 默认应当是「有防护」，需要放宽时由运营显式选 off。
func NormalizeOrderCaptchaMode(mode string) string {
	switch mode {
	case OrderCaptchaOff, OrderCaptchaReview, OrderCaptchaAlways:
		return mode
	default:
		return OrderCaptchaReview
	}
}

// CaptchaOn 是否启用验证码（注册/登录）
func (s SecuritySettings) CaptchaOn() bool { return !s.CaptchaDisabled }

// RiskOn 是否启用风控评估
func (s SecuritySettings) RiskOn() bool { return !s.RiskDisabled }

// JWT TTL 允许的小时范围。
// 上限一年：再长对「无状态、无法单独吊销」的 JWT 来说风险过大。
const (
	MinJWTTTLHours = 1
	MaxJWTTTLHours = 24 * 365
)

// DefaultJWTTTLHours 网页未设置时的兜底值（与 .env 的 JWT_TTL 默认 12h 一致）
const DefaultJWTTTLHours = 12

// NormalizeJWTTTLHours 把令牌有效期小时数归正到允许区间。
//
// 老数据里没有 jwt_ttl_hours 字段时解出来是 0，直接用会让签发的令牌立刻过期
// （登录接口返回 200，但后续请求全部 401），所以必须补默认值。
func NormalizeJWTTTLHours(h int) int {
	if h < MinJWTTTLHours {
		return DefaultJWTTTLHours
	}
	if h > MaxJWTTTLHours {
		return MaxJWTTTLHours
	}
	return h
}

// PaymentSettings 第三方支付（易支付）配置。
//
// 关于「可开关」：Enabled 是总开关，藏在后台「系统设置 → 支付」里。
// 关闭时：
//   - C 端前台隐藏充值入口（/api/shop/payment/info 返回 available=false）
//   - 所有支付相关写接口直接拒绝，不会产生任何外部请求
//   - 已有的充值码兑换不受影响，买家仍可用充值码充值
//
// 这样做的价值：支付通道出问题（上游跑路、被风控、对账不平）时，
// 运营可以在网页上立刻关掉，不用改代码也不用重启服务。
type PaymentSettings struct {
	// Enabled 总开关
	Enabled bool `json:"enabled"`

	// ---- 易支付商户参数 ----
	// APIURL 接口地址，例如 https://pay.example.com
	// 末尾不要带斜杠，代码里会自动拼接路径。
	APIURL string `json:"api_url"`
	// PID 商户ID
	PID string `json:"pid"`
	// Key 商户密钥，用于签名与验签。这是敏感字段，回显时打码。
	Key string `json:"key"`

	// ---- 接口路径（做成可配置，兼容魔改版）----
	// SubmitPath 页面跳转支付路径，默认 /submit.php
	SubmitPath string `json:"submit_path"`
	// MapIPath API 支付路径，默认 /mapi.php
	MapIPath string `json:"mapi_path"`
	// QueryPath 订单查询路径，默认 /api.php
	QueryPath string `json:"query_path"`

	// ---- 业务参数 ----
	// SiteName 显示在支付页面上的站点名
	SiteName string `json:"site_name"`
	// PayTypes 允许的支付方式，常见：alipay / wxpay / qqpay
	PayTypes []string `json:"pay_types"`
	// MinAmount 单笔最低充值金额（元）
	MinAmount float64 `json:"min_amount"`
	// MaxAmount 单笔最高充值金额（元），0 表示不限
	MaxAmount float64 `json:"max_amount"`
	// NotifyURL 异步通知地址。留空时自动用 APP_BASE_URL 拼。
	// 只有当站点部署在反代后面、且需要指定特殊域名时才需要手填。
	NotifyURL string `json:"notify_url"`
	// ReturnURL 支付完成后买家跳回的地址。留空时自动拼。
	ReturnURL string `json:"return_url"`

	// ---- 风控 ----
	// OrderTTLMinutes 支付单有效期（分钟）。超时未支付则关闭。
	OrderTTLMinutes int `json:"order_ttl_minutes"`
	// MaxPendingOrders 同一买家同时进行中的支付单上限
	MaxPendingOrders int `json:"max_pending_orders"`
}

// DefaultPaymentSettings 支付未配置时的默认值。
//
// 注意 Enabled 默认 false：接入支付属于「涉及资金」的变更，
// 必须由管理员显式开启，不能因为升级代码就自动开始收钱。
func DefaultPaymentSettings() PaymentSettings {
	return PaymentSettings{
		Enabled:          false,
		SubmitPath:       "/submit.php",
		MapIPath:         "/mapi.php",
		QueryPath:        "/api.php",
		SiteName:         "卡密商城",
		PayTypes:         []string{"alipay", "wxpay"},
		MinAmount:        1,
		MaxAmount:        0,
		OrderTTLMinutes:  30,
		MaxPendingOrders: 5,
	}
}

// Normalize 补齐默认值并做基本校验。
func (p *PaymentSettings) Normalize() {
	def := DefaultPaymentSettings()
	if strings.TrimSpace(p.SubmitPath) == "" {
		p.SubmitPath = def.SubmitPath
	}
	if strings.TrimSpace(p.MapIPath) == "" {
		p.MapIPath = def.MapIPath
	}
	if strings.TrimSpace(p.QueryPath) == "" {
		p.QueryPath = def.QueryPath
	}
	if strings.TrimSpace(p.SiteName) == "" {
		p.SiteName = def.SiteName
	}
	if len(p.PayTypes) == 0 {
		p.PayTypes = def.PayTypes
	}
	if p.MinAmount <= 0 {
		p.MinAmount = def.MinAmount
	}
	if p.OrderTTLMinutes <= 0 {
		p.OrderTTLMinutes = def.OrderTTLMinutes
	}
	if p.OrderTTLMinutes > 24*60 {
		p.OrderTTLMinutes = 24 * 60
	}
	if p.MaxPendingOrders <= 0 {
		p.MaxPendingOrders = def.MaxPendingOrders
	}
	// 路径统一保证以 / 开头，避免拼出 https://hostsubmit.php 这种坏地址
	p.SubmitPath = ensureLeadingSlash(p.SubmitPath)
	p.MapIPath = ensureLeadingSlash(p.MapIPath)
	p.QueryPath = ensureLeadingSlash(p.QueryPath)
	p.APIURL = strings.TrimRight(strings.TrimSpace(p.APIURL), "/")
	p.PID = strings.TrimSpace(p.PID)
	p.Key = strings.TrimSpace(p.Key)
}

// IsUsable 判断配置是否完整到可以发起支付。
//
// 与 Enabled 分开判断：开关打开但参数没填全时，
// 前台应当按「不可用」处理，而不是让买家点了支付再报错。
func (p PaymentSettings) IsUsable() bool {
	return p.Enabled &&
		p.APIURL != "" &&
		p.PID != "" &&
		p.Key != ""
}

func ensureLeadingSlash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if !strings.HasPrefix(s, "/") {
		return "/" + s
	}
	return s
}

// SettingsService 读写全局设置。
type SettingsService struct {
	db *gorm.DB
}

func NewSettingsService(db *gorm.DB) *SettingsService {
	return &SettingsService{db: db}
}

// GetAll 返回全部设置的原始 JSON，供前端表单回填。
func (s *SettingsService) GetAll(ctx context.Context) (map[string]json.RawMessage, error) {
	var rows []models.Setting
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(rows))
	for _, r := range rows {
		out[r.Key] = json.RawMessage(r.Value)
	}
	return out, nil
}

// Update 更新一项设置。
func (s *SettingsService) Update(ctx context.Context, key string, value json.RawMessage) error {
	if !json.Valid(value) {
		return fmt.Errorf("设置 %s 的值不是合法 JSON", key)
	}
	allowed := map[string]bool{
		models.SettingContact:      true,
		models.SettingBrand:        true,
		models.SettingDeliveryPage: true,
		models.SettingSecurity:     true,
		models.SettingPayment:      true,
	}
	if !allowed[key] {
		return fmt.Errorf("不支持的设置项: %s", key)
	}

	// 使用说明的过滤规则要在保存前校验：
	// 正则写错时如果直接存进去，用户会以为「规则已保存但不生效」，
	// 排查成本很高。这里当场拒绝，把原因说清楚。
	if key == models.SettingDeliveryPage {
		var page DeliveryPageSettings
		if err := json.Unmarshal(value, &page); err != nil {
			return fmt.Errorf("提货页设置解析失败：%w", err)
		}
		if err := textfilter.ValidateAll(page.InstructionRules); err != nil {
			return err
		}
	}

	return s.db.WithContext(ctx).Save(&models.Setting{Key: key, Value: datatypes.JSON(value)}).Error
}

// ---------------------------------------------------------------- 类型化读取

func (s *SettingsService) Contact(ctx context.Context) ContactSettings {
	var out ContactSettings
	s.decode(ctx, models.SettingContact, &out)
	return out
}

func (s *SettingsService) Brand(ctx context.Context) BrandSettings {
	out := BrandSettings{SiteName: "卡密自助发货"}
	s.decode(ctx, models.SettingBrand, &out)
	return out
}

func (s *SettingsService) DeliveryPage(ctx context.Context) DeliveryPageSettings {
	out := DeliveryPageSettings{
		ShowGoodsName:   true,
		ShowOrderNo:     true,
		ShowQuantity:    true,
		ShowDeliverTime: true,
		ShowInstruction: true,
		ShowContact:     true,
		ShowNotice:      true,
		AllowCopy:       true,
		ExtraPosition:   "after",
		ExpiredHint:     "该提货链接已失效，请凭订单号联系客服补发。",
	}
	s.decode(ctx, models.SettingDeliveryPage, &out)
	if out.ExtraPosition != "before" {
		out.ExtraPosition = "after"
	}
	return out
}

func (s *SettingsService) Security(ctx context.Context) SecuritySettings {
	out := SecuritySettings{DefaultTTLHours: 168, JWTTTLHours: DefaultJWTTTLHours, RecordViewLog: true}
	s.decode(ctx, models.SettingSecurity, &out)
	if out.DefaultTTLHours <= 0 {
		out.DefaultTTLHours = 168
	}
	if out.DefaultTTLHours > 24*365 {
		out.DefaultTTLHours = 24 * 365
	}
	// 令牌有效期同样做区间归正：老数据里没有这个字段时是 0，
	// 直接用会导致签发的令牌立刻过期，所以必须补默认值。
	out.JWTTTLHours = NormalizeJWTTTLHours(out.JWTTTLHours)
	// 下单验证码策略归一：空串（老配置）按 review
	out.OrderCaptchaMode = NormalizeOrderCaptchaMode(out.OrderCaptchaMode)
	return out
}

// Payment 读支付配置。
func (s *SettingsService) Payment(ctx context.Context) PaymentSettings {
	out := DefaultPaymentSettings()
	s.decode(ctx, models.SettingPayment, &out)
	out.Normalize()
	return out
}

// decode 读一项设置；缺失或损坏时保留调用方给的默认值，不让页面崩掉。
func (s *SettingsService) decode(ctx context.Context, key string, out any) {
	var row models.Setting
	if err := s.db.WithContext(ctx).Where("key = ?", key).First(&row).Error; err != nil {
		return
	}
	if len(row.Value) == 0 {
		return
	}
	if err := json.Unmarshal(row.Value, out); err != nil {
		// 配置损坏时用默认值兜底，避免提货页白屏
		return
	}
}
