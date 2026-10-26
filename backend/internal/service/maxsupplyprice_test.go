package service

import (
	"testing"

	"github.com/shopspring/decimal"

	"kamihub/internal/platform"
)

// 最高供应价的判定逻辑：报价总额 > 上限 才算超限。
//
// 这里用与 OrderService.attemptPurchase 完全相同的比较表达式，
// 保证「边界值」的行为被固定下来 —— 限价功能一旦算错，
// 要么放行亏损单（真花钱），要么把正常单全拦死（业务停摆），两头都很贵。
func exceeded(total, limit string) bool {
	t := decimal.RequireFromString(total)
	l := decimal.RequireFromString(limit)
	return l.IsPositive() && t.GreaterThan(l)
}

func TestMaxSupplyPrice_边界(t *testing.T) {
	cases := []struct {
		name  string
		total string
		limit string
		want  bool
	}{
		{"报价低于上限 → 放行", "4.37", "5.00", false},
		{"报价等于上限 → 放行（用满额度不算超）", "5.00", "5.00", false},
		{"报价高于上限 → 拦截", "5.01", "5.00", true},
		{"上限为 0 → 不限制", "9999.00", "0", false},
		{"上限为负数 → 不限制（等价于未设置）", "9999.00", "-1", false},
		{"小额差异也要拦住（0.01 元）", "4.3800", "4.3700", true},
		{"多位小数：4.3699 未超 4.37", "4.3699", "4.3700", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exceeded(c.total, c.limit); got != c.want {
				t.Errorf("报价 %s / 上限 %s：期望 %v，实际 %v", c.total, c.limit, c.want, got)
			}
		})
	}
}

// 超价必须归类为 Business（可换货源），否则「跳过该货源试下一个」就不成立。
func TestSupplyPriceExceeded_可故障转移(t *testing.T) {
	err := platform.NewBusiness("报价超过最高供应价", "SUPPLY_PRICE_EXCEEDED")

	if !platform.Failoverable(err) {
		t.Fatal("超价错误必须可故障转移，否则不会去试下一个货源")
	}
	if got := platform.KindOf(err); got != platform.KindBusiness {
		t.Errorf("错误类别应为 business，实际 %s", got)
	}
	if got := platform.ReasonOf(err); got != "SUPPLY_PRICE_EXCEEDED" {
		t.Errorf("原因码应为 SUPPLY_PRICE_EXCEEDED，实际 %s", got)
	}
}

// 余额不足等其他 Business 错误不应被误认成超价。
func TestReasonOf_区分不同原因(t *testing.T) {
	other := platform.NewBusiness("余额不足", "BALANCE_NOT_ENOUGH")
	if platform.ReasonOf(other) == "SUPPLY_PRICE_EXCEEDED" {
		t.Error("余额不足不应被当成超价")
	}
	if platform.ReasonOf(nil) != "" {
		t.Error("nil 错误应返回空原因")
	}
}
