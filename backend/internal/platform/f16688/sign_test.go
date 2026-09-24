package f16688

import (
	"testing"
)

// 这些期望值由「官方 PHP 参考实现的等价逻辑」独立推算得到，
// 任何导致签名漂移的改动都会在这里失败。
// 签名一旦算错，所有上游调用都会返回「签名验证失败」——这是全系统最关键的不变量。
const testSecret = "s3cr3t"

func TestBuildSign_Basic(t *testing.T) {
	params := map[string]any{
		"timestamp": "1710000000",
		"goods_no":  "G10001",
		"app_id":    "test",
	}
	signString, sign, err := BuildSign(params, testSecret)
	if err != nil {
		t.Fatalf("BuildSign 出错: %v", err)
	}
	wantString := "app_id=test&goods_no=G10001&timestamp=1710000000" + testSecret
	if signString != wantString {
		t.Errorf("签名原文不对\n got: %s\nwant: %s", signString, wantString)
	}
	if want := "850aa06242421a3601681078d8fe5ce0"; sign != want {
		t.Errorf("签名不对 got=%s want=%s", sign, want)
	}
}

func TestBuildSign_ListKeepsOrder(t *testing.T) {
	params := map[string]any{
		"app_id":    "test",
		"timestamp": "1",
		"ids":       []int{3, 1, 2},
	}
	signString, sign, err := BuildSign(params, testSecret)
	if err != nil {
		t.Fatalf("BuildSign 出错: %v", err)
	}
	if want := "app_id=test&ids=[3,1,2]&timestamp=1" + testSecret; signString != want {
		t.Errorf("列表必须保持原顺序\n got: %s\nwant: %s", signString, want)
	}
	if want := "a1bb0539f55d12394688adc687b92ea2"; sign != want {
		t.Errorf("签名不对 got=%s want=%s", sign, want)
	}
}

func TestBuildSign_MapKeysSorted(t *testing.T) {
	params := map[string]any{
		"b": map[string]any{"z": 1, "a": 2},
	}
	signString, sign, err := BuildSign(params, testSecret)
	if err != nil {
		t.Fatalf("BuildSign 出错: %v", err)
	}
	if want := `b={"a":2,"z":1}` + testSecret; signString != want {
		t.Errorf("对象键必须排序\n got: %s\nwant: %s", signString, want)
	}
	if want := "d8be89d5062379757ea46004d6d6646a"; sign != want {
		t.Errorf("签名不对 got=%s want=%s", sign, want)
	}
}

func TestBuildSign_SkipsEmptyButKeepsZero(t *testing.T) {
	params := map[string]any{
		"a": "",
		"b": nil,
		"c": 0,
		"d": "x",
	}
	signString, _, err := BuildSign(params, testSecret)
	if err != nil {
		t.Fatalf("BuildSign 出错: %v", err)
	}
	// 0 必须保留 —— 这是最容易写错的地方
	if want := "c=0&d=x" + testSecret; signString != want {
		t.Errorf("空值处理不对\n got: %s\nwant: %s", signString, want)
	}
}

func TestBuildSign_ExcludesSignFields(t *testing.T) {
	withExtra := map[string]any{
		"app_id": "test", "timestamp": "1",
		"sign": "deadbeef", "sign_type": "md5",
	}
	_, signA, err := BuildSign(withExtra, testSecret)
	if err != nil {
		t.Fatalf("BuildSign 出错: %v", err)
	}
	_, signB, _ := BuildSign(map[string]any{"app_id": "test", "timestamp": "1"}, testSecret)
	if signA != signB {
		t.Errorf("sign/sign_type 不应参与签名: %s vs %s", signA, signB)
	}
	if want := "f1185401282f23427fd061b0282ae766"; signA != want {
		t.Errorf("签名不对 got=%s want=%s", signA, want)
	}
}

func TestBuildSign_SkipsEmptyContainers(t *testing.T) {
	params := map[string]any{
		"app_id": "test", "timestamp": "1",
		"ids": []int{}, "obj": map[string]any{}, "nilSlice": []string(nil),
	}
	signString, sign, err := BuildSign(params, testSecret)
	if err != nil {
		t.Fatalf("BuildSign 出错: %v", err)
	}
	if want := "app_id=test&timestamp=1" + testSecret; signString != want {
		t.Errorf("空容器必须整体跳过\n got: %s\nwant: %s", signString, want)
	}
	if want := "f1185401282f23427fd061b0282ae766"; sign != want {
		t.Errorf("签名不对 got=%s want=%s", sign, want)
	}
}

// 这是 Go 移植最容易踩的坑：json.Marshal 默认把 < > & 转义成 \u003c 等，
// 而 PHP 的 JSON_UNESCAPED_* 不转义。若忘记 SetEscapeHTML(false) 本用例会失败。
func TestBuildSign_NoHTMLEscaping(t *testing.T) {
	params := map[string]any{
		"app_id": "test", "timestamp": "1",
		"tags": []string{"<b>", "a&b"},
	}
	signString, sign, err := BuildSign(params, testSecret)
	if err != nil {
		t.Fatalf("BuildSign 出错: %v", err)
	}
	if want := `app_id=test&tags=["<b>","a&b"]&timestamp=1` + testSecret; signString != want {
		t.Errorf("< > & 不应被转义\n got: %s\nwant: %s", signString, want)
	}
	if want := "850126ab99c2cc0cade239106c15adaa"; sign != want {
		t.Errorf("签名不对 got=%s want=%s", sign, want)
	}
}

func TestBuildSign_NoUnicodeEscaping(t *testing.T) {
	params := map[string]any{
		"app_id": "test", "timestamp": "1",
		"names": []string{"卡密", "测试"},
	}
	signString, sign, err := BuildSign(params, testSecret)
	if err != nil {
		t.Fatalf("BuildSign 出错: %v", err)
	}
	if want := `app_id=test&names=["卡密","测试"]&timestamp=1` + testSecret; signString != want {
		t.Errorf("中文不应被转义\n got: %s\nwant: %s", signString, want)
	}
	if want := "b72efe818b25dfb7fd28a8dbdd249c5c"; sign != want {
		t.Errorf("签名不对 got=%s want=%s", sign, want)
	}
}

func TestBuildSign_NoSlashEscaping(t *testing.T) {
	params := map[string]any{
		"app_id": "test", "timestamp": "1",
		"urls": []string{"https://a.com/x", "http://b.cn/y"},
	}
	signString, sign, err := BuildSign(params, testSecret)
	if err != nil {
		t.Fatalf("BuildSign 出错: %v", err)
	}
	if want := `app_id=test&timestamp=1&urls=["https://a.com/x","http://b.cn/y"]` + testSecret; signString != want {
		t.Errorf("斜杠不应被转义\n got: %s\nwant: %s", signString, want)
	}
	if want := "0eddc149dc80930aeb9f880318484ac1"; sign != want {
		t.Errorf("签名不对 got=%s want=%s", sign, want)
	}
}

func TestBuildSign_BoolScalarMatchesPHP(t *testing.T) {
	params := map[string]any{
		"app_id": "test", "timestamp": "1",
		"flag": true, "off": false,
	}
	signString, sign, err := BuildSign(params, testSecret)
	if err != nil {
		t.Fatalf("BuildSign 出错: %v", err)
	}
	if want := "app_id=test&flag=1&off=&timestamp=1" + testSecret; signString != want {
		t.Errorf("布尔值语义不对\n got: %s\nwant: %s", signString, want)
	}
	if want := "acabb998fb0ce59ed3d82fc950947518"; sign != want {
		t.Errorf("签名不对 got=%s want=%s", sign, want)
	}
}

func TestURLStripsDuplicatePrefix(t *testing.T) {
	c := &Client{base: "https://www.16688.com.cn/openApi"}
	if got, want := c.url("/openApi/purchase/create"), "https://www.16688.com.cn/openApi/purchase/create"; got != want {
		t.Errorf("URL 拼接不对\n got: %s\nwant: %s", got, want)
	}
}

func TestMapStatus(t *testing.T) {
	cases := map[int]string{
		0: "unpaid", 1: "paid", 2: "closed", 3: "refunded", 4: "delivered", 5: "finished",
	}
	for in, want := range cases {
		if got := string(mapStatus(in)); got != want {
			t.Errorf("mapStatus(%d) = %s, want %s", in, got, want)
		}
	}
}
