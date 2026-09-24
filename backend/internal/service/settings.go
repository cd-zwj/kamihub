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
}

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
