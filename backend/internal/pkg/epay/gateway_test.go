package epay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 302 跳转式接入方式的测试。
//
// 这些用例是被一次真实故障逼出来的：上游 submit.php 正常返回 302（Location 就是
// 收银台地址），而当时只有「返回 HTML 表单」这一种假设的测试，于是线上必然报
// 「易支付返回 HTTP 302：Found」，在线支付整体不可用。现在把这两条路都钉住。

// 3xx + 绝对 Location：应原样当作支付地址
func TestGatewayRedirectPageLocationAbsolute(t *testing.T) {
	const cashier = "https://cashier.example.com/pay/abc123"
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Location", cashier)
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "11312", Key: "KEY", Gateway: GatewayRedirect}
	res, err := Create(context.Background(), cfg, CreateInput{
		TradeNo: "KMP1", Amount: "4.00", Name: "充值", PayType: PayTypeAlipay,
	}, false)
	if err != nil {
		t.Fatalf("302 应被当作正常返回，却报错: %v", err)
	}
	if gotPath != "/submit.php" {
		t.Errorf("应请求 /submit.php，got=%s", gotPath)
	}
	if res.PayURL != cashier {
		t.Errorf("PayURL 应为 Location，got=%s", res.PayURL)
	}
}

// 3xx + 相对 Location：必须补成绝对地址，否则浏览器会跳到我们自己域名上
func TestGatewayRedirectPageLocationRelative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/cashier/step2")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K", Gateway: GatewayRedirect}
	res, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "1.00"}, false)
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	want := srv.URL + "/cashier/step2"
	if res.PayURL != want {
		t.Errorf("相对 Location 应补成绝对地址\n got=%s\nwant=%s", res.PayURL, want)
	}
}

// 3xx 但没有 Location：必须给出带状态码的明确错误，不能是空信息
func TestGatewayRedirectPageNoLocation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K", Gateway: GatewayRedirect}
	_, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "1.00"}, false)
	if err == nil {
		t.Fatal("没有 Location 的 302 应报错")
	}
	if !strings.Contains(err.Error(), "302") {
		t.Errorf("错误信息应含状态码，got=%v", err)
	}
}

// ---- 字段转换：data 里的 payurl ----

func TestGatewayRedirectDataPayURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"code": 1, "message": "ok",
			"data": map[string]any{
				"trade_no": "UP-9",
				"payurl":   "https://cashier.example.com/x",
			},
			"redirect": "",
		})
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K", Gateway: GatewayRedirect}
	res, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "1.00"}, false)
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	if res.PayURL != "https://cashier.example.com/x" {
		t.Errorf("应从 data.payurl 转换成 PayURL，got=%s", res.PayURL)
	}
	if res.TradeNoUpstream != "UP-9" {
		t.Errorf("应从 data.trade_no 转换成 TradeNoUpstream，got=%s", res.TradeNoUpstream)
	}
}

// ---- 字段转换：顶层 redirect 字段 ----

func TestGatewayRedirectTopLevelRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"code": 1, "message": "ok", "data": map[string]any{},
			"redirect": "https://cashier.example.com/top",
		})
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K", Gateway: GatewayRedirect}
	res, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "1.00"}, false)
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	if res.PayURL != "https://cashier.example.com/top" {
		t.Errorf("应从顶层 redirect 转换成 PayURL，got=%s", res.PayURL)
	}
}

// ---- 字段转换：错误信息必须读 message（不是 msg）----

func TestGatewayRedirectErrorReadsMessageField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 这是探测真实网关拿到的原始响应形状
		w.Write([]byte(`{"code":502,"message":"必填商户ID","data":{},"redirect":""}`))
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K", Gateway: GatewayRedirect}
	_, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "1.00"}, false)
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "必填商户ID") {
		t.Errorf("错误信息应读 message 字段，got=%v", err)
	}
}

func TestGatewayRedirectErrorReadsMsgField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"msg":"签名错误","trade_no":""}`))
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K", Gateway: GatewayRedirect}
	_, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "1.00"}, false)
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "签名错误") {
		t.Errorf("错误信息应兼容 msg 字段，got=%v", err)
	}
}

// 上游连原因都没给时，错误里必须带上响应原文，避免再出现「报错但没有信息」
func TestGatewayRedirectEmptyReasonKeepsRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K", Gateway: GatewayRedirect}
	_, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "1.00"}, false)
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "原文") {
		t.Errorf("无原因时应带响应原文，got=%v", err)
	}
}

// mapi 分支：二维码字段转换
func TestGatewayRedirectAPIQrcode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mapi.php" {
			t.Errorf("路径应为 /mapi.php，got=%s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"code": 1, "message": "ok",
			"data": map[string]any{"qr_code": "https://qr.example.com/a.png", "order_no": "UP-7"},
		})
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K", Gateway: GatewayRedirect}
	res, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "1.00"}, true)
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	if res.Qrcode != "https://qr.example.com/a.png" {
		t.Errorf("应从 data.qr_code 转换成 Qrcode，got=%s", res.Qrcode)
	}
	if res.TradeNoUpstream != "UP-7" {
		t.Errorf("应从 data.order_no 转换成 TradeNoUpstream，got=%s", res.TradeNoUpstream)
	}
}

// 默认（Gateway 为空）必须仍走标准实现：返回 HTML 表单而不是报错。
// 这条保证「不改原有行为」这个前提。
func TestGatewayDefaultKeepsStandardBehaviour(t *testing.T) {
	const formHTML = `<form action="/pay" method="post"></form>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(formHTML))
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K"} // Gateway 留空
	res, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "1.00"}, false)
	if err != nil {
		t.Fatalf("标准方式下单失败: %v", err)
	}
	if res.FormHTML != formHTML {
		t.Errorf("Gateway 留空时应走标准实现并返回表单 HTML，got=%q", res.FormHTML)
	}
}

// 标准方式遇到 302 仍应报错（保持原行为，不被新接入方式带偏）
func TestGatewayDefaultStillFailsOn302(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://cashier.example.com/x")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K"}
	_, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "1.00"}, false)
	if err == nil {
		t.Fatal("标准方式下 302 仍应报错（原有行为不变）")
	}
	if !strings.Contains(err.Error(), "302") {
		t.Errorf("错误信息应含 302，got=%v", err)
	}
}
