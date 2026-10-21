package epay

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 签名是易支付对接里最容易出错的地方（排序、空值、空格、大小写），
// 这里把关键规则都固定成测试。

// 基本签名：参数按 ASCII 升序拼接 + 直接追加密钥 + MD5 小写
func TestSignBasic(t *testing.T) {
	params := map[string]string{
		"pid":          "1000",
		"type":         "alipay",
		"out_trade_no": "T123",
		"money":        "10.00",
	}
	got := Sign(params, "SECRETKEY")

	// 手工按规则算一遍作为对照
	keys := []string{"money", "out_trade_no", "pid", "type"}
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k + "=" + params[k])
	}
	b.WriteString("SECRETKEY")
	want := md5Hex(b.String())

	if got != want {
		t.Errorf("签名不符:\n got=%s\nwant=%s", got, want)
	}
	if len(got) != 32 {
		t.Errorf("MD5 应为 32 位，got=%d", len(got))
	}
	if got != strings.ToLower(got) {
		t.Error("签名应是小写")
	}
}

// sign 与 sign_type 不参与签名计算
func TestSignExcludesSignFields(t *testing.T) {
	base := map[string]string{"pid": "1", "money": "5.00"}
	withSign := map[string]string{
		"pid": "1", "money": "5.00",
		"sign": "whatever", "sign_type": "MD5",
	}
	if Sign(base, "K") != Sign(withSign, "K") {
		t.Error("sign/sign_type 不应参与签名")
	}
}

// 空值参数被忽略 —— 这是易支付的常见坑：
// 把空参数也算进去会导致验签失败。
func TestSignIgnoresEmptyValues(t *testing.T) {
	a := map[string]string{"pid": "1", "money": "5.00"}
	b := map[string]string{"pid": "1", "money": "5.00", "return_url": "", "param": "   "}
	if Sign(a, "K") != Sign(b, "K") {
		t.Error("空值/空白参数应被忽略")
	}
}

// 参数顺序不影响结果（内部排序）
func TestSignOrderIndependent(t *testing.T) {
	a := map[string]string{"z": "1", "a": "2", "m": "3"}
	b := map[string]string{"m": "3", "a": "2", "z": "1"}
	if Sign(a, "K") != Sign(b, "K") {
		t.Error("参数传入顺序不应影响签名")
	}
}

// 密钥不同 → 签名不同
func TestSignKeyMatters(t *testing.T) {
	p := map[string]string{"pid": "1"}
	if Sign(p, "KEY1") == Sign(p, "KEY2") {
		t.Error("不同密钥应产生不同签名")
	}
}

// 金额字符串必须原样参与签名，不能做数值归一化。
// 「10」和「10.00」是两个不同的签名，这正是很多对接失败的原因。
func TestSignAmountIsLiteral(t *testing.T) {
	a := map[string]string{"money": "10"}
	b := map[string]string{"money": "10.00"}
	if Sign(a, "K") == Sign(b, "K") {
		t.Error("金额字符串应原样参与签名，不做数值归一化")
	}
}

// ---------------------------------------------------------------- 验签

func TestVerifySign(t *testing.T) {
	params := map[string]string{
		"pid": "1000", "trade_no": "UP1", "out_trade_no": "T1",
		"money": "10.00", "trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = Sign(params, "KEY")
	params["sign_type"] = "MD5"

	if !VerifySign(params, "KEY") {
		t.Error("正确签名应通过验签")
	}
	if VerifySign(params, "WRONGKEY") {
		t.Error("错误密钥不应通过验签")
	}

	// 篡改金额必须被检出
	tampered := map[string]string{}
	for k, v := range params {
		tampered[k] = v
	}
	tampered["money"] = "100.00"
	if VerifySign(tampered, "KEY") {
		t.Error("金额被篡改后应验签失败")
	}
}

func TestVerifySignMissing(t *testing.T) {
	params := map[string]string{"pid": "1000", "money": "1.00"}
	if VerifySign(params, "KEY") {
		t.Error("缺少 sign 应验签失败")
	}
}

// 大小写不敏感（部分易支付返回大写 MD5）
func TestVerifySignCaseInsensitive(t *testing.T) {
	params := map[string]string{"pid": "1000"}
	params["sign"] = strings.ToUpper(Sign(params, "KEY"))
	if !VerifySign(params, "KEY") {
		t.Error("大写签名也应通过验签")
	}
}

// ---------------------------------------------------------------- 下单（用 mock 服务器）

func TestCreatePagePayReturnsForm(t *testing.T) {
	const formHTML = `<form action="https://pay.example.com/pay" method="post"><input name="a" value="1"></form>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/submit.php" {
			t.Errorf("路径应为 /submit.php，got=%s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		// 服务端应能收到签名
		if r.PostForm.Get("sign") == "" {
			t.Error("请求里应带 sign")
		}
		if r.PostForm.Get("pid") != "1000" {
			t.Errorf("pid 不对: %s", r.PostForm.Get("pid"))
		}
		if r.PostForm.Get("money") != "10.00" {
			t.Errorf("money 格式应为 10.00, got=%s", r.PostForm.Get("money"))
		}
		w.Write([]byte(formHTML))
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1000", Key: "KEY"}
	res, err := CreatePagePay(context.Background(), cfg, CreateInput{
		TradeNo: "T1", Amount: "10.00", Name: "充值", PayType: PayTypeAlipay,
		NotifyURL: "https://mysite.com/notify",
	})
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	if res.FormHTML != formHTML {
		t.Errorf("应原样返回表单 HTML")
	}
}

func TestCreateAPIParsesJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mapi.php" {
			t.Errorf("路径应为 /mapi.php，got=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"code": 1, "msg": "ok",
			"trade_no": "UP123",
			"qrcode":   "https://qr.example.com/x.png",
		})
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1000", Key: "KEY"}
	res, err := CreateAPI(context.Background(), cfg, CreateInput{
		TradeNo: "T1", Amount: "5.00", Name: "充值", PayType: PayTypeWxpay,
	})
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	if res.TradeNoUpstream != "UP123" {
		t.Errorf("交易号不对: %s", res.TradeNoUpstream)
	}
	if res.Qrcode == "" {
		t.Error("应解析出二维码")
	}
}

// 上游返回 code != 1 时应报错并带上 msg
func TestCreateAPIUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "商户不存在"})
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1000", Key: "KEY"}
	_, err := CreateAPI(context.Background(), cfg, CreateInput{TradeNo: "T1", Amount: "5.00"})
	if err == nil {
		t.Fatal("上游报错时应返回 error")
	}
	if !strings.Contains(err.Error(), "商户不存在") {
		t.Errorf("错误信息应带上游 msg，got=%v", err)
	}
}

// 未配置时直接拒绝，不发请求
func TestCreateRejectsUnconfigured(t *testing.T) {
	_, err := CreatePagePay(context.Background(), Config{}, CreateInput{TradeNo: "T1"})
	if err != ErrNotConfigured {
		t.Errorf("未配置应返回 ErrNotConfigured, got=%v", err)
	}
}

// ---------------------------------------------------------------- 查单

func TestQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php" {
			t.Errorf("路径应为 /api.php, got=%s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("act") != "order" {
			t.Errorf("act 应为 order, got=%s", q.Get("act"))
		}
		if q.Get("out_trade_no") != "T1" {
			t.Errorf("out_trade_no 不对: %s", q.Get("out_trade_no"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"code": 1, "msg": "ok", "trade_no": "UP1",
			"status": 1, "money": "10.00",
		})
	}))
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, PID: "1000", Key: "KEY"}
	res, err := Query(context.Background(), cfg, "T1")
	if err != nil {
		t.Fatalf("查单失败: %v", err)
	}
	if res.Status != 1 {
		t.Errorf("status 应为 1（已支付），got=%d", res.Status)
	}
}

// ---------------------------------------------------------------- 辅助函数

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{10, "10.00"},
		{10.5, "10.50"},
		{10.567, "10.57"},
		{0.1, "0.10"},
		{1000, "1000.00"},
	}
	for _, c := range cases {
		if got := FormatAmount(c.in); got != c.want {
			t.Errorf("FormatAmount(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestParseAmount(t *testing.T) {
	if v, err := ParseAmount("10.00"); err != nil || v != 10 {
		t.Errorf("解析 10.00 失败: %v %v", v, err)
	}
	if _, err := ParseAmount("abc"); err == nil {
		t.Error("非法金额应报错")
	}
}

// 路径归一化：末尾斜杠 + 缺前导斜杠都应被处理
func TestConfigNormalize(t *testing.T) {
	c := Config{APIURL: "https://pay.example.com/", SubmitPath: "submit.php"}
	c.Normalize()
	if c.APIURL != "https://pay.example.com" {
		t.Errorf("APIURL 末尾斜杠应被去掉，got=%s", c.APIURL)
	}
	if c.MapIPath != "/mapi.php" {
		t.Errorf("未指定的路径应补默认值，got=%s", c.MapIPath)
	}
}

// md5Hex 测试内的小工具
func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
