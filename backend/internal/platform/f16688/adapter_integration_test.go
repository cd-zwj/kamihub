package f16688

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kamihub/internal/platform"
)

// 适配器集成测试。
//
// 前面 crosscheck_test.go 验证的是「签名函数」与官方一致；
// 这里验证的是「适配器实际发出的 HTTP 报文」是否自洽：
// 模拟上游把收到的 body 重新算一遍签名，和报文里的 sign 比对。
//
// 这能抓住「签名算对了、但发出去的字段不对」这类问题 ——
// 例如漏发 nonce、公共参数被覆盖、路径拼错、Content-Type 不对。
// 单纯测签名函数是发现不了这些的。

const integSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// mockUpstream 启动一个按官方协议校验签名的模拟上游。
// 返回接收到的原始报文，供用例断言。
func mockUpstream(t *testing.T, secret string) (*httptest.Server, *map[string]any, *string) {
	t.Helper()
	received := map[string]any{}
	path := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", 500)
			return
		}
		if err := json.Unmarshal(body, &received); err != nil {
			writeEnv(w, 0, "报文不是合法 JSON")
			return
		}

		// 用收到的报文重算签名（模拟服务端行为）
		sig, _ := received["sign"].(string)
		params := map[string]any{}
		for k, v := range received {
			params[k] = v
		}
		delete(params, "sign")

		controller, action := EndpointOf(r.URL.Path)
		_, want, err := BuildSignature(SignMethod, controller, action, params, secret)
		if err != nil {
			writeEnv(w, 0, "服务端算签名失败")
			return
		}
		if sig != want {
			writeEnv(w, 0, "签名验证失败")
			return
		}

		writeEnvData(w, map[string]any{
			"total": 1,
			"list":  []any{},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &received, &path
}

func writeEnv(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg})
}

func writeEnvData(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"code": 1, "msg": "ok", "data": data})
}

func newTestClient(base, secret string) *Client {
	// 生产环境的 base_url 形如 https://www.16688.com.cn/openApi，
	// Client.url() 会在它后面拼接「去掉 /openApi 前缀后的 path」。
	// 测试里必须保持一致，否则测不出真实的路径拼接行为。
	return &Client{
		appID:  "test_app_0001",
		secret: secret,
		base:   strings.TrimRight(base, "/") + "/openApi",
		doer:   platform.NewHTTPDoer(5000, 0, nil),
	}
}

// 核心用例：适配器发出的报文能通过服务端按官方算法的验签
func TestAdapterSignatureAcceptedByUpstream(t *testing.T) {
	srv, _, path := mockUpstream(t, integSecret)
	c := newTestClient(srv.URL, integSecret)

	res, err := c.ListGoods(context.Background(), platform.ListGoodsRequest{
		PageNo:   1,
		PageSize: 20,
	})
	if err != nil {
		t.Fatalf("调用失败（说明发出的报文验签没过）: %v", err)
	}
	if res.Total != 1 {
		t.Errorf("应解析出 total=1，got=%d", res.Total)
	}

	// 路径必须拼对（网关自带 /openApi 前缀，path 上不能重复）
	if *path != "/openApi/goods/list" {
		t.Errorf("请求路径不对: got=%s want=/openApi/goods/list", *path)
	}
}

// 报文必须带齐公共参数
func TestAdapterSendsRequiredCommonParams(t *testing.T) {
	srv, received, _ := mockUpstream(t, integSecret)
	c := newTestClient(srv.URL, integSecret)

	if _, err := c.ListGoods(context.Background(), platform.ListGoodsRequest{PageNo: 1, PageSize: 20}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}

	// app_id
	if v, _ := (*received)["app_id"].(string); v != "test_app_0001" {
		t.Errorf("app_id 不对: %v", (*received)["app_id"])
	}

	// timestamp：秒级时间戳，应是纯数字字符串
	ts, _ := (*received)["timestamp"].(string)
	if ts == "" {
		t.Error("缺少 timestamp")
	}
	for _, c := range ts {
		if c < '0' || c > '9' {
			t.Errorf("timestamp 应为纯数字字符串，got=%q", ts)
			break
		}
	}

	// nonce：官方要求 16~64 位
	nonce, _ := (*received)["nonce"].(string)
	if len(nonce) < 16 || len(nonce) > 64 {
		t.Errorf("nonce 长度必须在 16~64 之间，got=%d (%q)", len(nonce), nonce)
	}

	// sign：64 位小写十六进制
	sign, _ := (*received)["sign"].(string)
	if len(sign) != 64 {
		t.Errorf("sign 应为 64 位十六进制，got=%d", len(sign))
	}
	if sign != strings.ToLower(sign) {
		t.Error("sign 应为小写")
	}
}

// 每次调用的 nonce 必须不同（600 秒内不可重复，否则服务端会拒）
func TestAdapterGeneratesFreshNoncePerCall(t *testing.T) {
	seen := map[string]bool{}

	var currentNonce string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		currentNonce, _ = m["nonce"].(string)
		writeEnvData(w, map[string]any{"total": 0, "list": []any{}})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, integSecret)
	for i := 0; i < 20; i++ {
		if _, err := c.ListGoods(context.Background(), platform.ListGoodsRequest{PageNo: 1, PageSize: 1}); err != nil {
			t.Fatalf("第 %d 次调用失败: %v", i+1, err)
		}
		if seen[currentNonce] {
			t.Fatalf("nonce 重复: %s（同一秒内多次调用必须换 nonce）", currentNonce)
		}
		seen[currentNonce] = true
	}
}

// 业务参数不得覆盖公共参数（否则可以伪造 app_id / nonce 绕过校验）
func TestAdapterBizCannotOverrideCommonParams(t *testing.T) {
	srv, received, _ := mockUpstream(t, integSecret)
	c := newTestClient(srv.URL, integSecret)

	// 恶意业务参数试图覆盖 app_id / nonce / timestamp
	biz := map[string]any{
		"page_no":   1,
		"page_size": 20,
		"app_id":    "attacker",
		"nonce":     "HACKEDNONCEHACKED",
		"timestamp": "1",
		"sign":      "forged",
	}
	if err := c.call(context.Background(), "/openApi/goods/list", biz, &struct{}{}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}

	if v, _ := (*received)["app_id"].(string); v == "attacker" {
		t.Error("业务参数覆盖了 app_id —— 存在伪造风险")
	}
	if v, _ := (*received)["nonce"].(string); v == "HACKEDNONCEHACKED" {
		t.Error("业务参数覆盖了 nonce —— 防重放会失效")
	}
	if v, _ := (*received)["timestamp"].(string); v == "1" {
		t.Error("业务参数覆盖了 timestamp")
	}
	if v, _ := (*received)["sign"].(string); v == "forged" {
		t.Error("业务参数覆盖了 sign")
	}
}

// 上游返回签名错误时，错误应能正确冒泡（并归类为可换货源的业务错误）
func TestAdapterSurfacesSignatureError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnv(w, 0, "签名验证失败")
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, integSecret)
	_, err := c.ListGoods(context.Background(), platform.ListGoodsRequest{PageNo: 1, PageSize: 1})
	if err == nil {
		t.Fatal("上游报签名失败时应返回 error")
	}
	if !strings.Contains(err.Error(), "签名") {
		t.Errorf("错误信息应包含签名相关提示，got=%v", err)
	}
}

// 空字符串参数也要发出去（官方要求它们参与签名）
func TestAdapterSendsEmptyStringParams(t *testing.T) {
	srv, received, _ := mockUpstream(t, integSecret)
	c := newTestClient(srv.URL, integSecret)

	// keywords / source 传空串
	if _, err := c.ListGoods(context.Background(), platform.ListGoodsRequest{PageNo: 1, PageSize: 20}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}

	if _, ok := (*received)["keywords"]; !ok {
		t.Error("空字符串参数 keywords 必须出现在报文里（要参与签名）")
	}
	if _, ok := (*received)["source"]; !ok {
		t.Error("空字符串参数 source 必须出现在报文里（要参与签名）")
	}
}
