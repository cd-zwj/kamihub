package service

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 线上故障回归（2026-10-23）：truncateStr 原来是 s[:n] 的字节截断，
// 把一个 3 字节汉字劈成两半后，商品名变成非法 UTF-8 发给易支付，
// 上游固定回「创建订单失败,请联系客服人员」，该商品永远付不了款。
func TestTruncateStrKeepsValidUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
	}{
		// 真实踩坑的标题：51 字节，第 40 字节正好落在「，」中间
		{"listing 19 真实标题", "geminipro jio活动18个月激活链接，cdk版本", 40},
		{"全中文", "这是一个非常长的中文商品标题用来测试按字节截断会不会把汉字切坏", 20},
		{"中英混排", "Plus官方菲区半价实付非0元试用主打稳定质保30天", 40},
		{"纯 ASCII", "abcdefghijklmnopqrstuvwxyz", 10},
		{"超长 emoji", "卡密😀😀😀😀😀😀😀😀", 13},
	}

	for _, c := range cases {
		got := truncateStr(c.in, c.n)
		if !utf8.ValidString(got) {
			t.Errorf("%s: 截断后不是合法 UTF-8: %q", c.name, got)
		}
		if len(got) > c.n {
			t.Errorf("%s: 字节长度 %d 超过上限 %d", c.name, len(got), c.n)
		}
		if !strings.HasPrefix(c.in, got) {
			t.Errorf("%s: 截断结果必须是原串前缀", c.name)
		}
		// 字节上限允许往下退到字符边界，但不应退得过多（最多 3 字节）
		if want := c.n - len(got); want > 3 {
			t.Errorf("%s: 退让 %d 字节过多（结果 %q）", c.name, want, got)
		}
	}
}

// 没超长的字符串必须原样返回（不能顺手改动正常数据）。
func TestTruncateStrPassthrough(t *testing.T) {
	const s = "geminipro jio活动18个月激活链接"
	if got := truncateStr(s, 40); got != s {
		t.Errorf("未超长时不应改动，got=%q", got)
	}
	if got := truncateStr(s, 0); got != "" {
		t.Errorf("n=0 应返回空串，got=%q", got)
	}
}

// 支付商品名的真实拼法：截断后拼上「×数量」必须仍是合法 UTF-8。
// 这正是发给易支付的 name 参数。
func TestPayOrderNameIsValidUTF8(t *testing.T) {
	titles := []string{
		"geminipro jio活动18个月激活链接",      // listing 18：39 字节，不截断
		"geminipro jio活动18个月激活链接，cdk版本", // listing 19：51 字节，会截断
		"【自营】Plus官方菲区半价实付非0元试用主打稳定质保30天",
	}
	for _, title := range titles {
		name := "卡密商城 " + truncateStr(title, 40) + " ×1"
		if !utf8.ValidString(name) {
			t.Errorf("商品名不是合法 UTF-8（会被易支付拒单）: %q", name)
		}
	}
}
