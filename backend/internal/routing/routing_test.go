package routing

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

func sample() []Candidate {
	return []Candidate{
		{SupplyID: 1, PlatformID: 10, PlatformName: "A", CostPrice: dec("9.50"), Stock: 100, Priority: 2, Weight: 10},
		{SupplyID: 2, PlatformID: 20, PlatformName: "B", CostPrice: dec("9.20"), Stock: 5, Priority: 1, Weight: 80},
		{SupplyID: 3, PlatformID: 30, PlatformName: "C", CostPrice: dec("9.80"), Stock: 500, Priority: 3, Weight: 10},
	}
}

func ids(cs []Candidate) []uint {
	out := make([]uint, len(cs))
	for i, c := range cs {
		out[i] = c.SupplyID
	}
	return out
}

func equal(a, b []uint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPriorityStrategy(t *testing.T) {
	got := ids(PriorityStrategy{}.Rank(sample()))
	want := []uint{2, 1, 3} // priority 1,2,3
	if !equal(got, want) {
		t.Errorf("优先级排序不对 got=%v want=%v", got, want)
	}
}

func TestCheapestStrategy(t *testing.T) {
	got := ids(CheapestStrategy{}.Rank(sample()))
	want := []uint{2, 1, 3} // 9.20, 9.50, 9.80
	if !equal(got, want) {
		t.Errorf("最低价排序不对 got=%v want=%v", got, want)
	}
}

func TestStockStrategy(t *testing.T) {
	got := ids(StockStrategy{}.Rank(sample()))
	want := []uint{3, 1, 2} // 500, 100, 5
	if !equal(got, want) {
		t.Errorf("库存排序不对 got=%v want=%v", got, want)
	}
}

func TestRankDoesNotMutateInput(t *testing.T) {
	in := sample()
	before := ids(in)
	_ = PriorityStrategy{}.Rank(in)
	if !equal(ids(in), before) {
		t.Error("Rank 不应修改入参切片")
	}
}

func TestPriorityTieBreakByCost(t *testing.T) {
	in := []Candidate{
		{SupplyID: 1, CostPrice: dec("10.00"), Priority: 1},
		{SupplyID: 2, CostPrice: dec("8.00"), Priority: 1},
	}
	got := ids(PriorityStrategy{}.Rank(in))
	if !equal(got, []uint{2, 1}) {
		t.Errorf("同优先级应按成本升序 got=%v", got)
	}
}

func TestWeightedReturnsAllCandidates(t *testing.T) {
	seed := int64(42)
	s := NewWeightedStrategy(&seed)
	in := sample()
	got := s.Rank(in)
	if len(got) != len(in) {
		t.Fatalf("加权策略不应丢失候选 got=%d want=%d", len(got), len(in))
	}
	seen := map[uint]bool{}
	for _, c := range got {
		if seen[c.SupplyID] {
			t.Fatalf("候选重复: %d", c.SupplyID)
		}
		seen[c.SupplyID] = true
	}
}

// 权重 80 的候选应显著多于权重 10 的候选排在第一
func TestWeightedRespectsWeight(t *testing.T) {
	seed := int64(7)
	s := NewWeightedStrategy(&seed)
	firstCount := map[uint]int{}
	const rounds = 4000
	for i := 0; i < rounds; i++ {
		got := s.Rank(sample())
		firstCount[got[0].SupplyID]++
	}
	// 权重 80 的 SupplyID=2 应占明显多数
	if firstCount[2] < rounds/2 {
		t.Errorf("权重最高的候选未被优先选中: %v", firstCount)
	}
	if firstCount[1]+firstCount[3] == 0 {
		t.Errorf("低权重候选完全没被选中，加权逻辑可能退化为固定排序: %v", firstCount)
	}
}

func TestWeightedSingleCandidate(t *testing.T) {
	s := NewWeightedStrategy(nil)
	in := []Candidate{{SupplyID: 9}}
	got := s.Rank(in)
	if len(got) != 1 || got[0].SupplyID != 9 {
		t.Errorf("单候选应原样返回, got=%v", got)
	}
}

func TestRegistryFallsBackToPriority(t *testing.T) {
	r := NewRegistry()
	if got := r.Get("priority").Name(); got != "priority" {
		t.Errorf("应取到 priority, got=%s", got)
	}
	if got := r.Get("不存在的策略").Name(); got != "priority" {
		t.Errorf("未知策略应回落到 priority, got=%s", got)
	}
}

func TestRegistryMetasCoversAllStrategies(t *testing.T) {
	r := NewRegistry()
	metas := r.Metas()
	if len(metas) != 4 {
		t.Fatalf("应有 4 个策略, got=%d", len(metas))
	}
	names := map[string]bool{}
	for _, m := range metas {
		names[m.Name] = true
		if m.Description == "" {
			t.Errorf("策略 %s 缺少说明", m.Name)
		}
	}
	for _, want := range []string{"priority", "cheapest", "stock", "weighted"} {
		if !names[want] {
			t.Errorf("缺少策略 %s", want)
		}
	}
}

func TestFilterQuantity(t *testing.T) {
	in := []Candidate{
		{SupplyID: 1, Stock: 1},
		{SupplyID: 2, Stock: 100},
		{SupplyID: 3, Stock: 0}, // 缓存未知，不应剔除
	}
	got := ids(FilterQuantity(in, 5))
	want := []uint{2, 3}
	if !equal(got, want) {
		t.Errorf("库存过滤不对 got=%v want=%v", got, want)
	}
}

func TestFilterQuantityFallsBackWhenAllFiltered(t *testing.T) {
	in := []Candidate{{SupplyID: 1, Stock: 1}, {SupplyID: 2, Stock: 2}}
	got := FilterQuantity(in, 99)
	if len(got) != 2 {
		t.Errorf("全被过滤时应退回全集, got=%v", got)
	}
}
