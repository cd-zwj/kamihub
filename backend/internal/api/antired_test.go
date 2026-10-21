package api

import "testing"

// 防红的 UA 判断。
//
// 漏判会导致「在微信里打开看到平台拦截页」，误判会把正常浏览器用户挡在门外，
// 两个方向都是线上问题，所以把真实 UA 片段固定下来。
func TestIsWechatOrQQ(t *testing.T) {
	cases := []struct {
		name string
		ua   string
		want bool
	}{
		{
			name: "微信 iOS",
			ua:   "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 MicroMessenger/8.0.44(0x18002c2b) NetType/WIFI Language/zh_CN",
			want: true,
		},
		{
			name: "微信安卓",
			ua:   "Mozilla/5.0 (Linux; Android 13; PGT-AN10 Build/HONORPGT-AN10; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/116.0.0.0 Mobile Safari/537.36 MMWEBID/1234 MicroMessenger/8.0.42.2460(0x28002A35) WeChat/arm64",
			want: true,
		},
		{
			name: "手机 QQ",
			ua:   "Mozilla/5.0 (Linux; Android 13) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/116.0.0.0 Mobile Safari/537.36 V1_AND_SQ_8.9.63_4470_YYB_D QQ/8.9.63.11390 NetType/WIFI",
			want: true,
		},
		{
			name: "普通 Chrome（不能被误判）",
			ua:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			want: false,
		},
		{
			name: "Safari iOS",
			ua:   "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			want: false,
		},
		{
			name: "空 UA",
			ua:   "",
			want: false,
		},
		{
			name: "大小写混写也要认出来",
			ua:   "Mozilla/5.0 micromessenger/8.0",
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isWechatOrQQ(c.ua); got != c.want {
				t.Errorf("isWechatOrQQ(%q) = %v，期望 %v", c.ua, got, c.want)
			}
		})
	}
}
