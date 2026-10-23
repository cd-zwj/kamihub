package epay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 线上故障回归（2026-10-23）：上游把失败写成了 HTTP 200 + 一行纯文字。
//
// 当时 createRedirectPage 把所有「200 且非空」的响应体都当成自动提交表单，
// 于是「创建订单失败,请联系客服人员」被当作支付页返回给前端，
// 买家新窗口里只有一行字，既付不了款也没有任何提示。
func TestGatewayRedirectPlainTextErrorIsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("创建订单失败,请联系客服人员"))
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K", Gateway: GatewayRedirect}
	res, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "11.50"}, false)
	if err == nil {
		t.Fatalf("纯文字错误页必须当失败，却返回成功: %+v", res)
	}
	if !strings.Contains(err.Error(), "创建订单失败") {
		t.Errorf("错误信息应带出上游原话，got=%v", err)
	}
}

// 真正的自动提交表单仍要正常返回（不能把正常路径一起判死）。
func TestGatewayRedirectHTMLFormStillOK(t *testing.T) {
	form := `<html><body><form id="f" action="https://cashier.example.com/pay/x" method="post">` +
		`<input type="hidden" name="a" value="1"></form><script>document.getElementById('f').submit()</script></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(form))
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1", Key: "K", Gateway: GatewayRedirect}
	res, err := Create(context.Background(), cfg, CreateInput{TradeNo: "T", Amount: "11.50"}, false)
	if err != nil {
		t.Fatalf("自动提交表单应视为成功，却报错: %v", err)
	}
	if !strings.Contains(res.FormHTML, "<form") {
		t.Errorf("FormHTML 应原样带回表单，got=%q", res.FormHTML)
	}
}

// looksLikePayPage / htmlToText 的基本行为
func TestLooksLikePayPageAndHTMLToText(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`<form action="/pay/1"></form>`, true},
		{`<script>window.location.href='/pay/2'</script>`, true},
		{`<a href="https://x/pay/3">去支付</a>`, true},
		{`创建订单失败,请联系客服人员`, false},
		{`签名错误`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := looksLikePayPage(c.body); got != c.want {
			t.Errorf("looksLikePayPage(%q)=%v want %v", c.body, got, c.want)
		}
	}
	if got := htmlToText("<html><body><p>创建订单失败，<b>请联系客服</b></p></body></html>"); got != "创建订单失败，请联系客服" {
		t.Errorf("htmlToText 结果不符: %q", got)
	}
}
