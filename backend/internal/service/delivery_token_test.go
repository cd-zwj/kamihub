package service

import "testing"

// ---------------------------------------------------------------- 归一化

func TestNormalizeDeliveryToken(t *testing.T) {
	const tok = "055Ec6Kb9pN0UkArrsrnIYYEx2IqKj9k-NFDeyAzS_o"

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"干净链接", tok, tok},
		{"两侧空白", "  " + tok + "\n", tok},
		{"后面粘中文说明", tok + "售后时间为发货后一小时", tok},
		{"后面粘中文+换行（本 bug 现场）", tok + "售后时间%0A", tok},
		{"后面粘换行和中文", tok + "\n打开链接后，卡密如果是cdk模式", tok},
		{"真实世界脏 URL", tok + "售后时间为发货后一小时，没有质保\n打开链接后，卡密如果是cdk模式可以保留较长时间", tok},
		{"抄漏最后一位（长度不足，原样返回）", tok[:42], tok[:42]},
		{"开头就有非法字符（原样返回）", "!!" + tok[2:], "!!" + tok[2:]},
		{"空串", "", ""},
	}
	for _, c := range cases {
		if got := NormalizeDeliveryToken(c.in); got != c.want {
			t.Errorf("%s: NormalizeDeliveryToken(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------- 编辑距离

func TestEditDistanceAtMost(t *testing.T) {
	const a = "055Ec6Kb9pN0UkArrsrnIYYEx2IqKj9k-NFDeyAzS_o"

	cases := []struct {
		name string
		b    string
		max  int
		want bool
	}{
		{"完全相同", a, 2, true},
		{"I 抄成 l（本 bug 现场）", "055Ec6Kb9pN0UkArrsrnlYYEx2IqKj9k-NFDeyAzS_o", 2, true},
		{"6 抄成 b、0 抄成 O", "fK9-NAY2qHbOGg3pFUOHoDAOtxr_bXKW3wK5NB7m5V0", 2, false}, // 与 a 差太远
		{"少最后一位", a[:42], 2, true},
		{"少最后两位", a[:41], 2, true},
		{"少最后三位", a[:40], 2, false},
		{"中间少一位", a[:20] + a[21:], 2, true},
		{"两个字符都不对", a[:10] + "XX" + a[12:], 2, true},
		{"三个字符都不对", a[:10] + "XXX" + a[13:], 2, false},
	}
	for _, c := range cases {
		if got := editDistanceAtMost(a, c.b, c.max); got != c.want {
			t.Errorf("%s: editDistanceAtMost(%q, %q, %d) = %v, want %v", c.name, a, c.b, c.max, got, c.want)
		}
	}
}

// 形近字符两组真实样本互比：应当是「距离 2 命中」
func TestEditDistanceAtMost_Homoglyph(t *testing.T) {
	real := "fK9-NAY2qH60Gg3pFUOHoDAOtxr_bXKW3wK5NB7m5V0"
	got := "fK9-NAY2qHbOGg3pFUOHoDAOtxr_bXKW3wK5NB7m5V0" // 6→b, 0→O，两处
	if !editDistanceAtMost(real, got, 2) {
		t.Fatalf("形近字符样本应判定为距离 2 之内")
	}
	if editDistanceAtMost(real, got, 1) {
		t.Fatalf("该样本是两处差异，不应在距离 1 之内")
	}
}

// 安全边界：完全不相关的 43 位串不能命中
func TestEditDistanceAtMost_Unrelated(t *testing.T) {
	a := "055Ec6Kb9pN0UkArrsrnIYYEx2IqKj9k-NFDeyAzS_o"
	b := "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
	if editDistanceAtMost(a, b, 2) {
		t.Fatalf("无关 token 不应在距离 2 之内")
	}
}
