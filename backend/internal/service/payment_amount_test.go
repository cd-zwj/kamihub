package service

import (
	"testing"

	"github.com/shopspring/decimal"
)

// 线上实测（2026-10-23 521cd）：同一金额的未支付单会占用该金额，
// 网关把新单金额自动 +0.01（85.00 → 85.01 → 85.02 → 85.03）。
// 原来严格相等校验会把这笔正常付款拒掉，买家付了钱不到账。
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
		{"边界 +0.10", "7.00", "7.10", true},
		{"超界 +0.11", "7.00", "7.11", false},
		{"少付 0.01（必须拒绝）", "7.00", "6.99", false},
		{"少付很多", "100.00", "0.01", false},
		{"多付很多（可能是配错价，人工核对）", "7.00", "70.00", false},
		{"整数与小数混比", "11.5", "11.50", true},
	}

	for _, c := range cases {
		got := amountAcceptable(d(c.local), d(c.remote))
		if got != c.want {
			t.Errorf("%s: amountAcceptable(%s, %s)=%v want %v", c.name, c.local, c.remote, got, c.want)
		}
	}
}
