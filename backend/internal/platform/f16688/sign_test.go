package f16688

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// 新协议是 HMAC-SHA256 且绑定方法/接口，旧的 MD5 用例全部失效，这里按官方算法重写。
//
// 参考实现（官方调试页 JS）：
//
//	function canonical(params, method, controller, action) {
//	  const keys = Object.keys(params).filter((k) => k !== 'sign').sort();
//	  const parts = keys.map((k) => k + '=' + canonicalValue(params[k]));
//	  return method + '\n' + controller + '\n' + action + '\n' + parts.join('&');
//	}
//
// 签名一旦算错，所有上游调用都会返回「签名验证失败」——
// 这是全系统最关键的不变量，所以每条规则都固定成用例。
const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// expectSign 用独立实现算一遍，作为对照
func expectSign(t *testing.T, method, controller, action string, params map[string]any) string {
	t.Helper()
	plain, _, err := BuildSignature(method, controller, action, params, testSecret)
	if err != nil {
		t.Fatalf("BuildSignature 出错: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(plain))
	return hex.EncodeToString(mac.Sum(nil))
}

// ---------------------------------------------------------------- 原文结构

func TestSignStringStructure(t *testing.T) {
	plain, _, err := BuildSignature("POST", "goods", "list",
		map[string]any{"app_id": "test", "timestamp": "1710000000", "nonce": "abc123"}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\ngoods\nlist\napp_id=test&nonce=abc123&timestamp=1710000000"
	if plain != want {
		t.Errorf("签名原文不符:\n got=%q\nwant=%q", plain, want)
	}
}

func TestSignMethodUppercased(t *testing.T) {
	lower, _, _ := BuildSignature("post", "goods", "list", map[string]any{"a": "1"}, testSecret)
	upper, _, _ := BuildSignature("POST", "goods", "list", map[string]any{"a": "1"}, testSecret)
	if lower != upper {
		t.Error("方法名应统一转大写后再参与签名")
	}
}

// 签名绑定接口：换 action 或换拼写（大小写）都应得到不同签名
func TestSignBindsEndpoint(t *testing.T) {
	a, _, _ := BuildSignature("POST", "goods", "list", map[string]any{"a": "1"}, testSecret)
	b, _, _ := BuildSignature("POST", "goods", "detail", map[string]any{"a": "1"}, testSecret)
	if a == b {
		t.Error("不同 action 必须产生不同签名")
	}

	c, _, _ := BuildSignature("POST", "stocksecret", "invalidateall", map[string]any{"a": "1"}, testSecret)
	d, _, _ := BuildSignature("POST", "stockSecret", "invalidateAll", map[string]any{"a": "1"}, testSecret)
	if c == d {
		t.Error("controller/action 大小写不同应产生不同签名（调用方必须先转小写）")
	}
}

// ---------------------------------------------------------------- EndpointOf

func TestEndpointOf(t *testing.T) {
	cases := []struct {
		path       string
		controller string
		action     string
	}{
		{"/openApi/goods/list", "goods", "list"},
		{"/openApi/goods/detail", "goods", "detail"},
		{"/openApi/merchant/balance", "merchant", "balance"},
		{"/openApi/purchase/quotePrice", "purchase", "quoteprice"},
		{"/openApi/stockSecret/invalidateAll", "stocksecret", "invalidateall"},
		{"/openApi/order/info", "order", "info"},
		{"/openApi/channelBuyerBlack/addFromOrder", "channelbuyerblack", "addfromorder"},
		{"/openApi/upload/image", "upload", "image"},
		{"/goods/list", "goods", "list"},         // 无 openApi 段
		{"/OPENAPI/goods/list", "goods", "list"}, // 前缀大小写不敏感
	}
	for _, c := range cases {
		gc, ga := EndpointOf(c.path)
		if gc != c.controller || ga != c.action {
			t.Errorf("EndpointOf(%q) = (%q,%q), want (%q,%q)", c.path, gc, ga, c.controller, c.action)
		}
	}
}

// ---------------------------------------------------------------- 空值（与旧协议的关键差异）

// 新协议：空字符串也参与签名。旧 MD5 实现会跳过空值——
// 沿用旧逻辑会让所有带空字段的请求验签失败。
func TestSignIncludesEmptyString(t *testing.T) {
	plain, _, err := BuildSignature("POST", "goods", "list",
		map[string]any{"keywords": "", "page_no": "1"}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\ngoods\nlist\nkeywords=&page_no=1"
	if plain != want {
		t.Errorf("空字符串应序列化为 k=:\n got=%q\nwant=%q", plain, want)
	}

	without, _, _ := BuildSignature("POST", "goods", "list", map[string]any{"page_no": "1"}, testSecret)
	if plain == without {
		t.Error("带空字段与不带该字段必须产生不同签名")
	}
}

func TestSignIncludesEmptyArray(t *testing.T) {
	plain, _, err := BuildSignature("POST", "upload", "image",
		map[string]any{"images": []string{}}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\nupload\nimage\nimages=[]"
	if plain != want {
		t.Errorf("空数组应序列化为 []:\n got=%q\nwant=%q", plain, want)
	}

	noField, _, _ := BuildSignature("POST", "upload", "image", map[string]any{}, testSecret)
	if plain == noField {
		t.Error("空数组字段与缺失字段应产生不同签名")
	}
}

// nil 对齐 JS 的 null → 空串
func TestSignNilValue(t *testing.T) {
	plain, _, err := BuildSignature("POST", "goods", "list",
		map[string]any{"keywords": nil, "page_no": "1"}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(plain, "keywords=&page_no=1") {
		t.Errorf("nil 应序列化为空串，got=%q", plain)
	}
}

// typed nil 不能序列化成 null 字面量
func TestSignTypedNil(t *testing.T) {
	var s *string
	plain, _, err := BuildSignature("POST", "goods", "list",
		map[string]any{"keywords": s}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(plain, "keywords=") {
		t.Errorf("typed nil 应序列化为空串，got=%q", plain)
	}

	var sl []string
	plain2, _, _ := BuildSignature("POST", "goods", "list",
		map[string]any{"ids": sl}, testSecret)
	if !strings.HasSuffix(plain2, "ids=") {
		t.Errorf("nil 切片应序列化为空串，got=%q", plain2)
	}
}

// ---------------------------------------------------------------- 布尔（与旧协议的关键差异）

// 新协议：true → "true"，false → "false"（旧协议是 "1" / ""）
func TestSignBooleanMatchesJS(t *testing.T) {
	plain, _, err := BuildSignature("POST", "goods", "list",
		map[string]any{"on": true, "off": false}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\ngoods\nlist\noff=false&on=true"
	if plain != want {
		t.Errorf("布尔值应序列化为 true/false:\n got=%q\nwant=%q", plain, want)
	}
}

// ---------------------------------------------------------------- 数值

func TestSignNumbersNoDecimalPoint(t *testing.T) {
	plain, _, err := BuildSignature("POST", "purchase", "quotePrice",
		map[string]any{"quantity": 1}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(plain, "quantity=1") {
		t.Errorf("整数应输出 1 而非 1.0，got=%q", plain)
	}

	plain2, _, _ := BuildSignature("POST", "goods", "create",
		map[string]any{"price": 10.5}, testSecret)
	if !strings.HasSuffix(plain2, "price=10.5") {
		t.Errorf("小数应输出 10.5，got=%q", plain2)
	}

	plain3, _, _ := BuildSignature("POST", "goods", "create",
		map[string]any{"price": float64(10)}, testSecret)
	if !strings.HasSuffix(plain3, "price=10") {
		t.Errorf("10.0 应输出 10，got=%q", plain3)
	}
}

// ---------------------------------------------------------------- 数组与对象

// 数组保持元素顺序（不能排序）
func TestSignArrayKeepsOrder(t *testing.T) {
	plain, _, err := BuildSignature("POST", "stocksecret", "invalidate",
		map[string]any{"ids": []int{3, 1, 2}}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\nstocksecret\ninvalidate\nids=[3,1,2]"
	if plain != want {
		t.Errorf("数组应保持原序:\n got=%q\nwant=%q", plain, want)
	}

	b, _, _ := BuildSignature("POST", "stocksecret", "invalidate",
		map[string]any{"ids": []int{1, 2, 3}}, testSecret)
	if plain == b {
		t.Error("数组元素顺序不同应产生不同签名（不能排序）")
	}
}

func TestSignNestedObjectKeysSorted(t *testing.T) {
	plain, _, err := BuildSignature("POST", "goods", "create",
		map[string]any{"meta": map[string]any{"z": 1, "a": 2}}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\ngoods\ncreate\nmeta={\"a\":2,\"z\":1}"
	if plain != want {
		t.Errorf("嵌套对象键应排序:\n got=%q\nwant=%q", plain, want)
	}
}

// ---------------------------------------------------------------- 编码（Go 与 JS/PHP 的差异）

func TestSignNoHTMLEscaping(t *testing.T) {
	plain, _, err := BuildSignature("POST", "goods", "create",
		map[string]any{"tags": []string{"<b>", "a&b"}}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\ngoods\ncreate\ntags=[\"<b>\",\"a&b\"]"
	if plain != want {
		t.Errorf("不应转义 < > &:\n got=%q\nwant=%q", plain, want)
	}
}

func TestSignNoUnicodeEscaping(t *testing.T) {
	plain, _, err := BuildSignature("POST", "goods", "create",
		map[string]any{"names": []string{"卡密", "测试"}}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\ngoods\ncreate\nnames=[\"卡密\",\"测试\"]"
	if plain != want {
		t.Errorf("不应转义 Unicode:\n got=%q\nwant=%q", plain, want)
	}
}

func TestSignNoSlashEscaping(t *testing.T) {
	plain, _, err := BuildSignature("POST", "goods", "create",
		map[string]any{"urls": []string{"https://a.com/x", "http://b.cn/y"}}, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\ngoods\ncreate\nurls=[\"https://a.com/x\",\"http://b.cn/y\"]"
	if plain != want {
		t.Errorf("不应转义斜杠:\n got=%q\nwant=%q", plain, want)
	}
}

// ---------------------------------------------------------------- 排除 sign

func TestSignExcludesSign(t *testing.T) {
	a, sa, _ := BuildSignature("POST", "goods", "list",
		map[string]any{"app_id": "test", "sign": "deadbeef"}, testSecret)
	_, sb, _ := BuildSignature("POST", "goods", "list",
		map[string]any{"app_id": "test"}, testSecret)
	if sa != sb {
		t.Error("sign 字段不应参与签名计算")
	}
	if strings.Contains(a, "sign=") {
		t.Errorf("原文里不应出现 sign=，got=%q", a)
	}
}

// ---------------------------------------------------------------- nonce

func TestNewNonce(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		n, err := NewNonce()
		if err != nil {
			t.Fatal(err)
		}
		// 官方要求 16~64 位
		if len(n) < 16 || len(n) > 64 {
			t.Fatalf("nonce 长度必须在 16~64 之间，got=%d (%q)", len(n), n)
		}
		for _, c := range n {
			if !strings.ContainsRune(nonceCharset, c) {
				t.Fatalf("nonce 含非法字符 %q", c)
			}
		}
		if seen[n] {
			t.Fatalf("nonce 重复: %s（600 秒内不可重复）", n)
		}
		seen[n] = true
	}
}

// nonce 必须参与签名，否则服务端的防重放形同虚设
func TestSignIncludesNonce(t *testing.T) {
	a, _, _ := BuildSignature("POST", "goods", "list",
		map[string]any{"nonce": "AAAAAAAAAAAAAAAA"}, testSecret)
	b, _, _ := BuildSignature("POST", "goods", "list",
		map[string]any{"nonce": "BBBBBBBBBBBBBBBB"}, testSecret)
	if a == b {
		t.Error("不同 nonce 必须产生不同签名")
	}
}

// ---------------------------------------------------------------- 完整用例

func TestSignRealisticRequest(t *testing.T) {
	params := map[string]any{
		"app_id":    "test_app",
		"timestamp": "1710000000",
		"nonce":     "AbCdEfGhIjKlMnOpQrStUvWxYz012345",
		"page_no":   1,
		"page_size": 20,
		"keywords":  "",
		"source":    "supply",
	}
	plain, sign, err := BuildSignature("POST", "goods", "list", params, testSecret)
	if err != nil {
		t.Fatal(err)
	}

	wantPlain := "POST\ngoods\nlist\n" +
		"app_id=test_app&keywords=&nonce=AbCdEfGhIjKlMnOpQrStUvWxYz012345" +
		"&page_no=1&page_size=20&source=supply&timestamp=1710000000"
	if plain != wantPlain {
		t.Errorf("原文不符:\n got=%q\nwant=%q", plain, wantPlain)
	}

	if sign != expectSign(t, "POST", "goods", "list", params) {
		t.Error("签名与独立计算的结果不一致")
	}
	if len(sign) != 64 {
		t.Errorf("HMAC-SHA256 十六进制应为 64 位，got=%d", len(sign))
	}
	if sign != strings.ToLower(sign) {
		t.Error("签名应为小写十六进制")
	}
}

func TestSignSecretMatters(t *testing.T) {
	p := map[string]any{"app_id": "test"}
	_, a, _ := BuildSignature("POST", "goods", "list", p, testSecret)
	_, b, _ := BuildSignature("POST", "goods", "list", p, "another-secret")
	if a == b {
		t.Error("不同密钥应产生不同签名")
	}
}
