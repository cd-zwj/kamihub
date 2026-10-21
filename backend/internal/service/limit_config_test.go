package service

import "testing"

// ---------------------------------------------------------------- 生效单次上限

func TestEffectiveMaxQty(t *testing.T) {
	cases := []struct {
		name       string
		listingMax int
		goodsMax   int
		want       int
	}{
		{"两边都不限", 0, 0, 0},
		{"listing 不限、goods 限 1（本次线上故障场景）", 0, 1, 1},
		{"listing 不限、goods 限 10", 0, 10, 10},
		{"listing 限 5、goods 不限", 5, 0, 5},
		{"两边都限，取较小者(listing)", 5, 10, 5},
		{"两边都限，取较小者(goods)", 10, 5, 5},
		{"两边相同", 3, 3, 3},
		{"负值按不限处理", -1, 0, 0},
		{"负值 listing + goods 限 2", -1, 2, 2},
	}
	for _, c := range cases {
		if got := effectiveMaxQty(c.listingMax, c.goodsMax); got != c.want {
			t.Errorf("%s: effectiveMaxQty(%d, %d) = %d, want %d",
				c.name, c.listingMax, c.goodsMax, got, c.want)
		}
	}
}

// ---------------------------------------------------------------- 失败文案

func TestAutoOrderFailHint(t *testing.T) {
	const fallback = autoOrderFailedMessage

	cases := []struct {
		name string
		raw  string
		want string // 空串表示只断言「不是 fallback 且包含关键词」
	}{
		{"空错误 → 兜底", "", fallback},
		{"无法识别的错误 → 兜底", "some internal db error", fallback},
	}
	for _, c := range cases {
		if got := autoOrderFailHint(c.raw); got != c.want {
			t.Errorf("%s: autoOrderFailHint(%q) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}

	// 限购：必须带出具体件数，且不能让买家以为钱丢了
	got := autoOrderFailHint("数量超过该商品上限：最大 1")
	if !containsAll(got, "最多购买 1 件", "退回余额") {
		t.Errorf("限购文案不合格: %q", got)
	}
	if got == fallback {
		t.Errorf("限购场景不应回落到兜底文案")
	}

	// 只有「最多购买 N 件」这种其它写法也要能解析出件数
	got = autoOrderFailHint("最多购买 3 件")
	if !containsAll(got, "最多购买 3 件") {
		t.Errorf("最多购买文案不合格: %q", got)
	}

	// 售罄 / 缺货
	got = autoOrderFailHint("商品已售罄 (上游: 商品已售罄)")
	if !containsAll(got, "缺货", "退回余额") {
		t.Errorf("售罄文案不合格: %q", got)
	}

	// 下架
	got = autoOrderFailHint("货源商品不存在或已下架 (上游: 货源商品不存在或已下架)")
	if !containsAll(got, "下架", "退回余额") {
		t.Errorf("下架文案不合格: %q", got)
	}

	// 上游余额不足
	got = autoOrderFailHint("平台余额不足：需要 3.08，可用 0.61 (上游: BALANCE_NOT_ENOUGH)")
	if !containsAll(got, "余额不足") {
		t.Errorf("余额不足文案不合格: %q", got)
	}

	// 所有非兜底文案都必须带上「钱回余额」，否则买家会以为钱丢了
	for _, raw := range []string{
		"数量超过该商品上限：最大 1",
		"最少购买 2 件",
		"商品已售罄 (上游: 商品已售罄)",
		"该商品卡密池已无库存，请先在「卡密池」补货后重新下单",
		"货源商品不存在或已下架 (上游: 货源商品不存在或已下架)",
	} {
		hint := autoOrderFailHint(raw)
		if hint == fallback {
			continue
		}
		if !containsAll(hint, "余额") {
			t.Errorf("文案缺少余额安抚: raw=%q hint=%q", raw, hint)
		}
	}
}

func TestFirstIntIn(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"数量超过该商品上限：最大 1", 1},
		{"最多购买 12 件", 12},
		{"没有任何数字", 0},
		{"末尾 42", 42},
		{"abc7def88", 7},
	}
	for _, c := range cases {
		if got := firstIntIn(c.in); got != c.want {
			t.Errorf("firstIntIn(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
