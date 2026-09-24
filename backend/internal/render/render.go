// Package render 负责提货页的服务端渲染。
//
// 为什么不用前端 SPA 渲染提货页：
//   - 受众是终端买家，多在微信/QQ 内置浏览器打开，要秒开、不能白屏
//   - 需要独立于管理后台的视觉风格（可信、正规、营销感）
//   - 卡密不该经由前端 API 暴露，服务端直接渲染更安全
//
// 模板与静态样式全部内嵌，部署时不需要额外的静态资源服务。
package render

import (
	"embed"
	"html/template"
	"io"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

// ContactItem 提货页展示的一条联系方式
type ContactItem struct {
	Type  string
	Label string
	Value string
}

// PageData 提货页渲染数据
type PageData struct {
	// 品牌
	SiteName   string
	LogoURL    string
	FooterNote string

	// 状态
	Usable      bool   // 链接是否可用
	NeedUnlock  bool   // 需要输入访问密码
	ErrorText   string // 不可用时的提示
	UnlockError string // 密码错误提示

	// 商品信息
	ShowGoodsImage  bool
	ShowGoodsName   bool
	ShowOrderNo     bool
	ShowQuantity    bool
	ShowDeliverTime bool
	GoodsName       string
	GoodsImage      string
	OrderNo         string
	TradeNo         string
	Quantity        int
	DeliverTime     string

	// 发货内容
	Cards           []string
	Content         string
	Instruction     string
	ShowInstruction bool
	AllowCopy       bool

	// 附加内容
	Extra         string
	ExtraPosition string
	ExtraFirst    bool
	ExtraLast     bool

	// 联系方式
	ShowContact bool
	Contacts    []ContactItem
	WorkHours   string
	ShowNotice  bool
	Notice      string
}

// Renderer 提货页渲染器
type Renderer struct {
	tmpl *template.Template
}

// New 加载内嵌模板
func New() (*Renderer, error) {
	t, err := template.New("delivery.html").Funcs(template.FuncMap{
		"contactIcon": contactIcon,
		"nl2br": func(s string) template.HTML {
			return template.HTML(strings.ReplaceAll(template.HTMLEscapeString(s), "\n", "<br>"))
		},
		// richhtml 用于上游返回的富文本（使用说明、发货内容）。
		// 模板里必须显式写 {{ richhtml .Xxx }} 才会走净化，
		// 写成 {{ .Xxx }} 会退回自动转义（安全但会显示标签源码）。
		// 净化逻辑见 sanitize.go。
		"richhtml": SanitizeHTML,
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Renderer{tmpl: t}, nil
}

// Render 输出提货页 HTML
func (r *Renderer) Render(w io.Writer, data PageData) error {
	return r.tmpl.ExecuteTemplate(w, "delivery.html", data)
}

// contactIcon 返回联系方式对应的 emoji 图标，避免引入图标字体。
func contactIcon(t string) string {
	switch strings.ToLower(t) {
	case "wechat", "weixin":
		return "💬"
	case "qq":
		return "🐧"
	case "email", "mail":
		return "✉️"
	case "telegram", "tg":
		return "📨"
	case "phone", "tel":
		return "📞"
	default:
		return "🔗"
	}
}
