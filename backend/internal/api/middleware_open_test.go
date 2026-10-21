package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// 签名算法是开放接口的信任根：一旦它和文档里写的不一致，
// 所有下游都会验签失败，而且报错只有一句「签名校验失败」，
// 排查成本极高。所以这里把每个规则都钉死成测试。
//
// 这些用例同时也是「文档示例」的回归测试：文档里的签名原文
// 就是从这些断言里抄出来的。

func TestOpenSignPlain_Basic(t *testing.T) {
	params := map[string]any{
		"app_id":    "kmh_abc",
		"timestamp": "1710000000",
		"nonce":     "a1b2c3d4e5f67890",
		"sign":      "should_be_ignored",
	}
	got, err := OpenSignPlain("POST", "/api/open/balance", params)
	if err != nil {
		t.Fatalf("构造签名原文失败: %v", err)
	}
	want := "POST\n/api/open/balance\napp_id=kmh_abc&nonce=a1b2c3d4e5f67890&timestamp=1710000000"
	if got != want {
		t.Fatalf("签名原文不一致\n got: %q\nwant: %q", got, want)
	}
}

// sign 必须被排除，且大小写不敏感的方法名统一转大写
func TestOpenSignPlain_MethodUpperAndSignExcluded(t *testing.T) {
	got, err := OpenSignPlain("post", "/api/open/goods", map[string]any{
		"b":    "2",
		"a":    "1",
		"sign": "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\n/api/open/goods\na=1&b=2"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// 数组/对象要递归按键名排序后 JSON 序列化；标量原样
func TestOpenSignPlain_NestedSortAndJSON(t *testing.T) {
	got, err := OpenSignPlain("POST", "/p", map[string]any{
		"a": []any{1, "x"},
		"b": map[string]any{"y": 2, "x": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `POST
/p
a=[1,"x"]&b={"x":1,"y":2}`
	if got != want {
		t.Fatalf("嵌套结构规范化不一致\n got: %q\nwant: %q", got, want)
	}
}

// 中文与斜杠不能被转义（关闭 HTML 转义），否则下游按常规 JSON 序列化算出来就对不上
func TestOpenSignPlain_NoHTMLEscape(t *testing.T) {
	got, err := OpenSignPlain("POST", "/p", map[string]any{
		"title": "中文/测试",
		"nested": map[string]any{
			"k": "a/b&c<d>",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "title=中文/测试") {
		t.Fatalf("中文/斜杠被转义了: %q", got)
	}
	if !strings.Contains(got, `nested={"k":"a/b&c<d>"}`) {
		t.Fatalf("嵌套值里的 & < > 被转义了: %q", got)
	}
}

// 布尔写 true/false；空字符串与 null 都参与且为空值
func TestOpenSignPlain_BoolAndEmpty(t *testing.T) {
	got, err := OpenSignPlain("POST", "/p", map[string]any{
		"t":   true,
		"f":   false,
		"e":   "",
		"nil": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\n/p\ne=&f=false&nil=&t=true"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// 数字必须保留客户端原始写法：1.50 不能变成 1.5，否则两边签名不一致
func TestOpenSignPlain_NumberPreserved(t *testing.T) {
	raw := []byte(`{"amount":1.50,"qty":2,"big":12345678901234567890}`)
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var params map[string]any
	if err := dec.Decode(&params); err != nil {
		t.Fatal(err)
	}
	got, err := OpenSignPlain("POST", "/p", params)
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\n/p\namount=1.50&big=12345678901234567890&qty=2"
	if got != want {
		t.Fatalf("数字精度/写法没保留\n got: %q\nwant: %q", got, want)
	}
}

// 签名必须是 64 位小写十六进制，且任何一个参数变化都会导致签名变化
func TestOpenSign_FormatAndSensitivity(t *testing.T) {
	plain := "POST\n/api/open/balance\napp_id=a&nonce=n&timestamp=1"
	sig := OpenSign(plain, "secret123")
	if len(sig) != 64 {
		t.Fatalf("签名长度应为 64，实际 %d", len(sig))
	}
	if strings.ToLower(sig) != sig {
		t.Fatalf("签名应为小写十六进制: %s", sig)
	}
	if !isHex(sig) {
		t.Fatalf("签名不是十六进制: %s", sig)
	}

	// 同样的原文与密钥必须稳定
	if OpenSign(plain, "secret123") != sig {
		t.Fatal("同样输入得到不同签名")
	}
	// 换密钥必须不同
	if OpenSign(plain, "secret124") == sig {
		t.Fatal("换密钥后签名未变化")
	}
	// 改一个参数必须不同
	plain2 := "POST\n/api/open/balance\napp_id=a&nonce=n&timestamp=2"
	if OpenSign(plain2, "secret123") == sig {
		t.Fatal("参数变化后签名未变化")
	}
	// 换路径必须不同（路径参与签名，防止一个签名被拿去调另一个接口）
	plain3 := "POST\n/api/open/purchase\napp_id=a&nonce=n&timestamp=1"
	if OpenSign(plain3, "secret123") == sig {
		t.Fatal("路径变化后签名未变化")
	}
}

// 签名原文里的值顺序必须与参数名 ASCII 升序一致：
// 大写字母排在小写字母之前，数字排在最前
func TestOpenSignPlain_ASCIIOrder(t *testing.T) {
	got, err := OpenSignPlain("POST", "/p", map[string]any{
		"b": "1",
		"B": "2",
		"1": "3",
		"a": "4",
		"Z": "5",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "POST\n/p\n1=3&B=2&Z=5&a=4&b=1"
	if got != want {
		t.Fatalf("ASCII 排序不对\n got: %q\nwant: %q", got, want)
	}
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
