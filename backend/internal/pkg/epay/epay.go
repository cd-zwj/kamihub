// Package epay 实现彩虹易支付的对接。
//
// 易支付没有单一官方规范，但社区形成了事实标准：
//
//	页面跳转支付  POST {APIURL}/submit.php      → 返回一段自动提交的 HTML 表单
//	API 支付      POST {APIURL}/mapi.php        → 返回 JSON（含二维码/跳转地址）
//	订单查询      GET  {APIURL}/api.php?act=order&pid=&key=&out_trade_no=
//
// 签名算法（这是对接最容易出错的地方，务必按此实现）：
//
//  1. 把除 sign、sign_type 之外的所有非空参数，按「参数名 ASCII 升序」排序；
//  2. 拼成 a=1&b=2&c=3 的形式（**不参与 URL 编码**）；
//  3. 末尾直接拼接商户密钥 KEY（没有 & 分隔符）；
//  4. 对整个字符串做 MD5，取 32 位小写。
//
// 异步通知的验签同理，只是参数来自回调请求。
//
// 这里刻意不引入第三方 HTTP 框架：标准库足够，少一个依赖少一份风险。
package epay

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 支付方式
const (
	PayTypeAlipay = "alipay"
	PayTypeWxpay  = "wxpay"
	PayTypeQQPay  = "qqpay"
)

// 上游返回的错误
var (
	ErrNotConfigured = errors.New("支付未配置")
	ErrSignInvalid   = errors.New("签名校验失败")
)

// Config 一次调用所需的配置快照。
//
// 做成值类型而不是直接读设置服务，是为了让调用方在同一个请求内
// 使用一致的配置（避免请求中途管理员改了配置导致前后不一致）。
type Config struct {
	APIURL     string
	PID        string
	Key        string
	SubmitPath string
	MapIPath   string
	QueryPath  string
	Timeout    time.Duration

	// Gateway 接入方式，取值见 gateway.go 的 GatewayStandard / GatewayRedirect。
	// 留空按标准方式处理 —— 因此老配置升级后行为完全不变。
	Gateway string
}

// Normalize 补齐默认路径与超时。
func (c *Config) Normalize() {
	if c.SubmitPath == "" {
		c.SubmitPath = "/submit.php"
	}
	if c.MapIPath == "" {
		c.MapIPath = "/mapi.php"
	}
	if c.QueryPath == "" {
		c.QueryPath = "/api.php"
	}
	if c.Timeout <= 0 {
		c.Timeout = 15 * time.Second
	}
	c.APIURL = strings.TrimRight(strings.TrimSpace(c.APIURL), "/")
}

// Ready 配置是否完整。
func (c Config) Ready() bool {
	return c.APIURL != "" && c.PID != "" && c.Key != ""
}

// ---------------------------------------------------------------- 签名

// Sign 计算易支付签名。
//
// params 中的空值会被忽略；sign / sign_type 不参与计算。
func Sign(params map[string]string, key string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" {
			continue
		}
		if strings.TrimSpace(v) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k])
	}
	// 密钥直接拼在末尾，没有分隔符 —— 这是易支付协议的关键细节
	b.WriteString(key)

	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// VerifySign 校验签名，使用常量时间比较避免时序侧信道。
func VerifySign(params map[string]string, key string) bool {
	got := params["sign"]
	if got == "" {
		return false
	}
	want := Sign(params, key)
	return hmacEqual(strings.ToLower(got), strings.ToLower(want))
}

// hmacEqual 定长比较。
func hmacEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// ---------------------------------------------------------------- 下单

// CreateInput 发起一笔支付所需的参数
type CreateInput struct {
	TradeNo   string // 商户订单号
	Amount    string // 金额，两位小数字符串，如 "10.00"
	Name      string // 商品名称
	PayType   string // alipay / wxpay / qqpay
	NotifyURL string
	ReturnURL string
	ClientIP  string
	Sitename  string
}

// CreateResult 上游返回
type CreateResult struct {
	// PayURL 页面跳转支付的地址（把参数拼好后 POST 过去）
	PayURL string
	// FormHTML 上游直接返回的自动提交表单（submit.php 的返回）
	FormHTML string
	// Qrcode 二维码内容（mapi 返回）
	Qrcode string
	// TradeNoUpstream 上游交易号
	TradeNoUpstream string
	// Raw 原始响应，排障用
	Raw string
}

// CreatePagePay 页面跳转支付。
//
// 返回的是一段 HTML，前端把它写进新窗口或 iframe 即可，
// 浏览器会自动提交表单跳到支付页。
func CreatePagePay(ctx context.Context, cfg Config, in CreateInput) (*CreateResult, error) {
	cfg.Normalize()
	if !cfg.Ready() {
		return nil, ErrNotConfigured
	}

	params := buildParams(cfg, in)
	params["sign"] = Sign(params, cfg.Key)
	params["sign_type"] = "MD5"

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	endpoint := cfg.APIURL + cfg.SubmitPath
	body, err := httpPostForm(ctx, endpoint, form, cfg.Timeout)
	if err != nil {
		return nil, err
	}

	// submit.php 正常返回一段自动提交的 HTML 表单。
	// 若返回的是 JSON，说明对端把 submit 与 mapi 合并了，这里兼容处理。
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") {
		var jr mapiResponse
		if err := json.Unmarshal([]byte(trimmed), &jr); err == nil {
			if jr.Code != 1 {
				return nil, fmt.Errorf("易支付下单失败：%s", jr.Msg)
			}
			return &CreateResult{
				PayURL:          jr.PayURL,
				Qrcode:          jr.Qrcode,
				TradeNoUpstream: jr.TradeNo,
				Raw:             trimmed,
			}, nil
		}
	}

	if trimmed == "" {
		return nil, errors.New("易支付返回为空")
	}
	return &CreateResult{
		FormHTML: trimmed,
		PayURL:   endpoint,
		Raw:      trimmed,
	}, nil
}

// mapiResponse mapi.php 的返回结构
type mapiResponse struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	TradeNo string `json:"trade_no"`
	PayURL  string `json:"payurl"`
	Qrcode  string `json:"qrcode"`
	// URLScheme 部分版本用它返回唤起 App 的地址
	URLScheme string `json:"urlscheme"`
}

// CreateAPI 支付（返回二维码或跳转链接，不需要跳转到易支付页面）。
func CreateAPI(ctx context.Context, cfg Config, in CreateInput) (*CreateResult, error) {
	cfg.Normalize()
	if !cfg.Ready() {
		return nil, ErrNotConfigured
	}

	params := buildParams(cfg, in)
	params["sign"] = Sign(params, cfg.Key)
	params["sign_type"] = "MD5"

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	endpoint := cfg.APIURL + cfg.MapIPath
	body, err := httpPostForm(ctx, endpoint, form, cfg.Timeout)
	if err != nil {
		return nil, err
	}

	var jr mapiResponse
	if err := json.Unmarshal([]byte(body), &jr); err != nil {
		return nil, fmt.Errorf("易支付返回无法解析：%w（原文：%s）", err, truncate(body, 300))
	}
	if jr.Code != 1 {
		return nil, fmt.Errorf("易支付下单失败：%s", jr.Msg)
	}

	out := &CreateResult{
		PayURL:          jr.PayURL,
		Qrcode:          jr.Qrcode,
		TradeNoUpstream: jr.TradeNo,
		Raw:             body,
	}
	if out.PayURL == "" {
		out.PayURL = jr.URLScheme
	}
	return out, nil
}

// buildParams 组装公共参数（不含 sign）。
func buildParams(cfg Config, in CreateInput) map[string]string {
	p := map[string]string{
		"pid":          cfg.PID,
		"type":         in.PayType,
		"out_trade_no": in.TradeNo,
		"notify_url":   in.NotifyURL,
		"return_url":   in.ReturnURL,
		"name":         in.Name,
		"money":        in.Amount,
	}
	if in.Sitename != "" {
		p["sitename"] = in.Sitename
	}
	if in.ClientIP != "" {
		p["clientip"] = in.ClientIP
	}
	return p
}

// ---------------------------------------------------------------- 查单

// QueryResult 订单查询结果
type QueryResult struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	TradeNo string `json:"trade_no"`
	// Status 1 = 已支付
	Status int    `json:"status"`
	Money  string `json:"money"`
	Name   string `json:"name"`
}

// Query 主动查单。
//
// 用途：异步通知可能因为网络抖动丢失，导致买家付了钱但没到账。
// 前台的「查询支付结果」按钮和对账任务都靠它兜底。
func Query(ctx context.Context, cfg Config, tradeNo string) (*QueryResult, error) {
	cfg.Normalize()
	if !cfg.Ready() {
		return nil, ErrNotConfigured
	}

	q := url.Values{}
	q.Set("act", "order")
	q.Set("pid", cfg.PID)
	q.Set("key", cfg.Key)
	q.Set("out_trade_no", tradeNo)

	endpoint := cfg.APIURL + cfg.QueryPath + "?" + q.Encode()
	body, err := httpGet(ctx, endpoint, cfg.Timeout)
	if err != nil {
		return nil, err
	}

	var res QueryResult
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		return nil, fmt.Errorf("查单返回无法解析：%w（原文：%s）", err, truncate(body, 300))
	}
	return &res, nil
}

// ---------------------------------------------------------------- HTTP

var httpClient = &http.Client{
	Timeout: 20 * time.Second,
	// 不跟随重定向：submit.php 的回调地址由我们自己控制
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// httpResponse 一次 HTTP 调用的原始结果。
//
// 单独把状态码与 Location 暴露出来，是因为「302 跳转式」网关（见 gateway.go）
// 用 3xx 的 Location 表达收银台地址 —— 那是正常响应，不是错误。
// 标准接入方式仍然只看 Body，由下面的 httpPostForm 包装。
type httpResponse struct {
	Status   int
	Body     string
	Location string
}

// httpPostFormRaw 发一次表单 POST，把状态码 / 响应体 / Location 原样返回。
//
// 只有传输层失败（连不上、读不出）才返回 error；HTTP 状态码一律交给调用方判断，
// 因为「非 200 即失败」只对标准接入方式成立。
func httpPostFormRaw(ctx context.Context, endpoint string, form url.Values, timeout time.Duration) (*httpResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// 部分易支付站点会做 UA 过滤，带上一个正常的标识
	req.Header.Set("User-Agent", "kamihub/1.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求易支付失败：%w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, fmt.Errorf("读取易支付响应失败：%w", err)
	}

	out := &httpResponse{
		Status:   resp.StatusCode,
		Body:     string(body),
		Location: resp.Header.Get("Location"),
	}
	slog.Debug("易支付响应", "endpoint", endpoint, "status", out.Status, "len", len(out.Body), "location", out.Location)
	return out, nil
}

// httpPostForm 标准接入方式用的薄包装：只接受 200，其余按失败处理。
//
// 行为与拆分前逐字一致（包括错误文案），拆分只是为了给 302 跳转式复用传输层。
func httpPostForm(ctx context.Context, endpoint string, form url.Values, timeout time.Duration) (string, error) {
	res, err := httpPostFormRaw(ctx, endpoint, form, timeout)
	if err != nil {
		return "", err
	}
	if res.Status != http.StatusOK {
		return "", fmt.Errorf("易支付返回 HTTP %d：%s", res.Status, truncate(res.Body, 200))
	}
	return res.Body, nil
}

func httpGet(ctx context.Context, endpoint string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "kamihub/1.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求易支付失败：%w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("易支付返回 HTTP %d", resp.StatusCode)
	}
	return string(body), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------- 辅助

// FormatAmount 把金额格式化成易支付要求的形式：
// 两位小数的纯数字字符串，不能有千分位，不能科学计数。
func FormatAmount(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// ParseAmount 解析回调里的金额字符串。
func ParseAmount(s string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("金额格式不合法：%s", s)
	}
	return f, nil
}
