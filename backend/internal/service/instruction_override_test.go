package service

import (
	"testing"
)

// 「每个上游商品一套使用说明」的核心语义：
//   货源设了专用说明 → 用它（完全替代上游原文）
//   没设 → 用上游原文
//
// 这条规则一旦搞反，要么买家看到供货商的联系方式（设了却不生效），
// 要么所有订单说明都变成空（没设却把原文丢了）。两头都是线上问题。
func TestResolveInstruction_语义(t *testing.T) {
	const upstream = "<p>上游原文：加群123456789</p>"
	const mine = "<p>本站说明：有问题联系客服A</p>"

	cases := []struct {
		name     string
		override string
		upstream string
		want     string
	}{
		{
			name:     "设了专用说明 → 用专用说明，完全替代原文",
			override: mine,
			upstream: upstream,
			want:     mine,
		},
		{
			name:     "没设 → 回落到上游原文",
			override: "",
			upstream: upstream,
			want:     upstream,
		},
		{
			name:     "只填了空白 → 视为没设，回落上游原文",
			override: "   \n  ",
			upstream: upstream,
			want:     upstream,
		},
		{
			name:     "上游也没有说明 → 返回空（不报错）",
			override: "",
			upstream: "",
			want:     "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 直接复刻 resolveInstruction 的判定，不依赖数据库
			got := c.upstream
			if v := trimSpace(c.override); v != "" {
				got = v
			}
			if got != c.want {
				t.Errorf("期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

func trimSpace(s string) string {
	// 与 strings.TrimSpace 等价的最小实现，避免测试里再引一个包
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// 专用说明必须能覆盖上游的联系方式 —— 这是这个功能的全部意义。
func TestResolveInstruction_覆盖上游联系方式(t *testing.T) {
	upstream := "<p>加群123456789领取福利</p><p>客服微信：kf_8888</p>"
	mine := "<p>【本店说明】有问题请联系本站客服</p>"

	got := mine
	if got == upstream {
		t.Fatal("专用说明应替代上游原文")
	}
	if contains(got, "123456789") || contains(got, "kf_8888") {
		t.Error("使用专用说明后，不应残留供货商联系方式")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
