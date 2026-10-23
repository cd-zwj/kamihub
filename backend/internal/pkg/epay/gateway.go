package epay

// 302 跳转式接入方式
//
// 背景：易支付没有统一规范。社区事实标准之外还有一类网关（彩豆 521cd 等）
// 在两个地方与标准实现不同：
//
//	1) submit.php 不返回「自动提交的 HTML 表单」，而是直接 302 到收银台，
//	   收银台地址放在 Location 头里；
//	2) JSON 信封的字段名不同：
//	      标准易支付  {"code":1,"msg":"ok","trade_no":"...","payurl":"...","qrcode":"..."}
//	      302 跳转式   {"code":...,"message":"...","data":{},"redirect":"..."}
//
// 处理办法是「新增一种接入方式」，而不是改动标准实现 —— 标准网关仍然走 epay.go
// 里那条已经验证过的路径，本文件只负责把新网关的字段翻译成内部统一结构。
// 管理员在后台「系统设置 → 支付 → 接入方式」里二选一。
//
// 本文件里的字段转换 redirectEnvelope.toCreateResult 是两套协议之间唯一的翻译层：
// 以后上游再加/改字段，只改这里，业务代码与前端都不用动。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// 接入方式取值
const (
	// GatewayStandard 标准易支付：submit.php 返回自动提交表单，JSON 用 msg/payurl/qrcode。
	// 配置里留空时按这个处理，保证老配置升级后行为完全不变。
	GatewayStandard = "epay"

	// GatewayRedirect 302 跳转式：submit.php 302 到收银台（地址在 Location），
	// JSON 用 message/data/redirect。
	GatewayRedirect = "epay_redirect"
)

// gatewayOrDefault 把空值归一成标准方式。
func (c Config) gatewayOrDefault() string {
	if g := strings.TrimSpace(c.Gateway); g != "" {
		return g
	}
	return GatewayStandard
}

// Create 按配置里的接入方式下单，是业务层唯一的调用入口。
//
// useAPI=true 走 mapi.php（拿二维码/跳转地址），false 走 submit.php（页面跳转）。
// 标准方式下这里只是原样转发给既有实现，不做任何包装。
func Create(ctx context.Context, cfg Config, in CreateInput, useAPI bool) (*CreateResult, error) {
	cfg.Normalize()
	if !cfg.Ready() {
		return nil, ErrNotConfigured
	}

	switch cfg.gatewayOrDefault() {
	case GatewayRedirect:
		if useAPI {
			return createRedirectAPI(ctx, cfg, in)
		}
		return createRedirectPage(ctx, cfg, in)
	default:
		if useAPI {
			return CreateAPI(ctx, cfg, in)
		}
		return CreatePagePay(ctx, cfg, in)
	}
}

// ---------------------------------------------------------------- 字段转换

// redirectEnvelope 302 跳转式网关的 JSON 信封。
//
// 不能复用 mapiResponse：字段名完全不同。真实样例（探测上游得到）：
//
//	{"code":502,"message":"必填商户ID","data":{},"redirect":""}
//	{"code":0,"msg":"签名错误","trade_no":""}
//
// 这里把见过的字段名都列上，缺哪个都不会解析失败。
type redirectEnvelope struct {
	Code     int             `json:"code"`
	Message  string          `json:"message"`
	Msg      string          `json:"msg"`      // 少数分支仍用标准字段名
	TradeNo  string          `json:"trade_no"` // 同上
	PayURL   string          `json:"payurl"`
	Qrcode   string          `json:"qrcode"`
	Redirect string          `json:"redirect"`
	Data     json.RawMessage `json:"data"`
}

// redirectData data 里可能出现的字段（各魔改版不一致，全部兼容）。
type redirectData struct {
	TradeNo  string `json:"trade_no"`
	OrderNo  string `json:"order_no"`
	PayURL   string `json:"payurl"`
	PayURL2  string `json:"pay_url"`
	URL      string `json:"url"`
	Redirect string `json:"redirect"`
	Qrcode   string `json:"qrcode"`
	QRCode2  string `json:"qr_code"`
}

// text 取可读的提示/错误信息，兼容 message 与 msg 两种字段名。
//
// 这一条是被真实故障逼出来的：网关返回 {"code":502,"message":"必填商户ID"}，
// 而按标准字段只读 msg 时拿到空串，报错文案就变成「易支付下单失败：」——
// 后面什么都没有，运营完全无法定位。
func (e redirectEnvelope) text() string {
	if s := strings.TrimSpace(e.Message); s != "" {
		return s
	}
	return strings.TrimSpace(e.Msg)
}

// toCreateResult 是本接入方式的核心：把上游五花八门的字段收敛成内部统一的
// CreateResult，业务层与前端只认这一套字段。
//
// location 传 HTTP 302 的 Location 头（没有则传空串）。取值优先级：
//
//	Location 头 > redirect 字段 > data 里的 payurl/pay_url/url > 二维码
//
// 一个可用的支付地址都没有时才算失败，并把上游原话带出来；上游连原因都没给，
// 就把响应原文截断带上 —— 绝不再出现「报错但没有任何信息」的日志。
func (e redirectEnvelope) toCreateResult(location, raw string) (*CreateResult, error) {
	var d redirectData
	if len(e.Data) > 0 {
		// data 不是对象（例如是字符串）时忽略即可，不影响其它字段
		_ = json.Unmarshal(e.Data, &d)
	}

	payURL := firstNonEmpty(location, e.Redirect, d.Redirect, d.PayURL, d.PayURL2, d.URL)
	qrcode := firstNonEmpty(e.Qrcode, d.Qrcode, d.QRCode2)
	tradeNo := firstNonEmpty(e.TradeNo, d.TradeNo, d.OrderNo)

	if payURL == "" && qrcode == "" {
		msg := e.text()
		if msg == "" {
			msg = "上游未给出原因，响应原文：" + truncate(raw, 300)
		}
		return nil, fmt.Errorf("易支付下单失败：%s", msg)
	}

	return &CreateResult{
		PayURL:          payURL,
		Qrcode:          qrcode,
		TradeNoUpstream: tradeNo,
		Raw:             raw,
	}, nil
}

// ---------------------------------------------------------------- 下单

// signedForm 组装带签名的表单参数。
//
// 签名算法与 epay.go 完全一致（同一套 Sign），这里只是抽出来给本接入方式复用；
// 标准接入方式的代码路径不做任何改动。
func signedForm(cfg Config, in CreateInput) url.Values {
	params := buildParams(cfg, in)
	params["sign"] = Sign(params, cfg.Key)
	params["sign_type"] = "MD5"

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	return form
}

// createRedirectPage 页面跳转式下单。
//
// 与标准实现的关键区别：3xx 是**正常返回**，Location 就是收银台地址。
// 标准实现把非 200 一律当失败，所以这里不能复用它。
func createRedirectPage(ctx context.Context, cfg Config, in CreateInput) (*CreateResult, error) {
	endpoint := cfg.APIURL + cfg.SubmitPath
	res, err := httpPostFormRaw(ctx, endpoint, signedForm(cfg, in), cfg.Timeout)
	if err != nil {
		return nil, err
	}

	// 本接入方式下 3xx 就是「请到这里付款」
	if loc := redirectLocation(endpoint, res); loc != "" {
		return &CreateResult{PayURL: loc, Raw: res.Body}, nil
	}

	body := strings.TrimSpace(res.Body)

	// JSON 信封：可能带回跳转地址，也可能是业务错误
	if strings.HasPrefix(body, "{") {
		var env redirectEnvelope
		if jerr := json.Unmarshal([]byte(body), &env); jerr == nil {
			return env.toCreateResult("", body)
		}
	}

	// 少数网关仍返回自动提交的 HTML 表单：与标准接入方式一致地兜住
	if res.Status == http.StatusOK && body != "" {
		if looksLikePayPage(body) {
			return &CreateResult{FormHTML: body, PayURL: endpoint, Raw: body}, nil
		}
		// 上游把失败写成了「HTTP 200 + 一行字」。真实样例：
		//
		//	创建订单失败,请联系客服人员
		//
		// 以前这里一律当成「自动提交表单」返回成功，于是前端把这一行字
		// 写进新窗口 —— 买家看到一片空白加一句话，既付不了款也拿不到任何
		// 提示，只能反复点「去支付」再放弃。必须当失败处理并把原话带出来。
		return nil, fmt.Errorf("易支付下单失败：%s", truncate(htmlToText(body), 200))
	}

	return nil, fmt.Errorf("易支付返回异常（HTTP %d）：%s", res.Status, truncate(body, 300))
}

// createRedirectAPI 二维码/API 式下单。
func createRedirectAPI(ctx context.Context, cfg Config, in CreateInput) (*CreateResult, error) {
	endpoint := cfg.APIURL + cfg.MapIPath
	res, err := httpPostFormRaw(ctx, endpoint, signedForm(cfg, in), cfg.Timeout)
	if err != nil {
		return nil, err
	}

	// 个别网关连 mapi 也走 302
	if loc := redirectLocation(endpoint, res); loc != "" {
		return &CreateResult{PayURL: loc, Raw: res.Body}, nil
	}

	body := strings.TrimSpace(res.Body)
	var env redirectEnvelope
	if jerr := json.Unmarshal([]byte(body), &env); jerr != nil {
		// 网关有时直接返回纯文本（例如 submit.php 的「签名错误」）
		if body != "" {
			return nil, fmt.Errorf("易支付下单失败：%s", truncate(body, 300))
		}
		return nil, fmt.Errorf("易支付返回为空（HTTP %d）", res.Status)
	}
	return env.toCreateResult("", body)
}

// ---------------------------------------------------------------- 辅助

// redirectLocation 从 3xx 响应里取收银台地址并补成绝对地址。
//
// 非 3xx 或没有 Location 时返回空串，由调用方继续走字段转换。
func redirectLocation(endpoint string, res *httpResponse) string {
	if res.Status < 300 || res.Status >= 400 {
		return ""
	}
	return resolveLocation(endpoint, res.Location)
}

// resolveLocation 把 Location 头补成绝对地址。
//
// 上游可能返回相对路径（例如 /pay/xxxx）。直接丢给前端会跳到我们自己的域名上，
// 所以必须按请求地址解析成绝对 URL。
func resolveLocation(endpoint, loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	u, err := url.Parse(loc)
	if err != nil {
		return loc
	}
	if u.IsAbs() {
		return loc
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return loc
	}
	return base.ResolveReference(u).String()
}

// firstNonEmpty 返回第一个非空白字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// looksLikePayPage 判断上游返回的 HTML 是不是「能把人送进收银台」的页面。
//
// 判据刻意宽松（有表单 / 有跳转脚本 / 有收银台链接任一即可），
// 因为各家魔改版的自动提交写法不一样；这里只要求「不是一段纯文字」。
func looksLikePayPage(body string) bool {
	lower := strings.ToLower(body)
	for _, marker := range []string{"<form", "location.href", "location.replace", "window.location", "/pay/"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// htmlToText 把一小段 HTML 压成一行可读文字，用于把上游错误原话写进日志/提示。
func htmlToText(body string) string {
	s := body
	if i := strings.Index(strings.ToLower(s), "<body"); i >= 0 {
		s = s[i:]
	}
	// 去标签 + 常见实体
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	out := b.String()
	out = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(out)
	out = strings.Join(strings.Fields(out), " ")
	return out
}
