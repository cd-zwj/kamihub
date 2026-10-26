package service

import (
	"testing"

	"github.com/shopspring/decimal"
)

// 金额判定的唯一规则：少付拒绝，多付放行。
//
// 历史：
//   - 最早是「严格相等」，网关给同金额订单自动 +0.01，正常付款被拒（2026-10-23）。
//   - 改成「多付 ≤0.10 放行」，结果线上又出现 10.00 → 10.10 之外的抬价，
//     以及「报价按零售价、扣款按档位专价」造成的 10 vs 9 差价，
//     回调直接被判死、上游无限重试。
//
// 现在多付一律放行，并按实付金额入账（差额留在买家余额），只拒少付。
func TestAmountAcceptable(t *testing.T) {
	d := func(s string) decimal.Decimal { return decimal.RequireFromString(s) }

	cases := []struct {
		name   string
		local  string
		remote string
		want   bool
	}{
		{"完全一致", "85.00", "85.00", true},
		{"网关 +0.01（真实场景）", "85.00", "85.01", true},
		{"网关 +0.02", "11.50", "11.52", true},
		{"网关 +0.03", "85.00", "85.03", true},
		{"网关累加 +0.10（线上出现过）", "10.00", "10.10", true},
		{"网关继续累加 +0.11", "7.00", "7.11", true},
		{"页面显示 10、实际扣 9、网关再抬到 10.1", "9.00", "10.10", true},
		{"多付很多也放行（钱照入账，差额留余额）", "7.00", "70.00", true},
		{"少付 0.01（必须拒绝）", "7.00", "6.99", false},
		{"少付很多", "100.00", "0.01", false},
		{"整数与小数混比", "11.5", "11.50", true},
		{"零金额上游", "7.00", "0.00", false},
	}

	for _, c := range cases {
		got := amountAcceptable(d(c.local), d(c.remote))
		if got != c.want {
			t.Errorf("%s: amountAcceptable(%s, %s)=%v want %v", c.name, c.local, c.remote, got, c.want)
		}
	}
}
