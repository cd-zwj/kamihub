// Package routing 实现多货源选择与排序。
//
// 设计要点：策略只负责「给候选货源排序」，不负责「尝试采购」。
// 上层拿到排好序的列表后按顺序尝试，失败就换下一个 ——
// 这样「优先级/最低价/库存/加权」四种策略天然都带故障转移能力，
// 不需要在每个策略里各写一遍重试逻辑。
package routing

import (
	"math"
	"math/rand"
	"sort"
	"sync"

	"github.com/shopspring/decimal"
)

// Candidate 一个候选货源
type Candidate struct {
	SupplyID        uint
	PlatformID      uint
	PlatformCode    string
	PlatformName    string
	PlatformGoodsNo string
	CostPrice       decimal.Decimal
	Stock           int
	Priority        int
	Weight          int
}

// Strategy 排序策略
type Strategy interface {
	// Name 策略标识，与 models.Strategy* 对应
	Name() string
	// Description 供前端展示的说明
	Description() string
	// Rank 返回排好序的候选（第一个最优先）
	Rank(candidates []Candidate) []Candidate
}

// Meta 供前端展示的策略元信息
type Meta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ---------------------------------------------------------------- 优先级

// PriorityStrategy 按人工设定的优先级排序，最符合「主货源 + 备用货源」的直觉。
type PriorityStrategy struct{}

func (PriorityStrategy) Name() string { return "priority" }
func (PriorityStrategy) Description() string {
	return "按优先级排序，主货源失败自动切备用。适合明确指定主次货源的场景。"
}

func (PriorityStrategy) Rank(candidates []Candidate) []Candidate {
	return rankStable(candidates, func(a, b Candidate) bool {
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if !a.CostPrice.Equal(b.CostPrice) {
			return a.CostPrice.LessThan(b.CostPrice)
		}
		return a.SupplyID < b.SupplyID
	})
}

// ---------------------------------------------------------------- 最低价

// CheapestStrategy 采购价最低的优先，直接提升毛利。
type CheapestStrategy struct{}

func (CheapestStrategy) Name() string { return "cheapest" }
func (CheapestStrategy) Description() string {
	return "采购成本最低的优先。适合对毛利敏感、且各货源质量相当的场景。"
}

func (CheapestStrategy) Rank(candidates []Candidate) []Candidate {
	return rankStable(candidates, func(a, b Candidate) bool {
		if !a.CostPrice.Equal(b.CostPrice) {
			return a.CostPrice.LessThan(b.CostPrice)
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.SupplyID < b.SupplyID
	})
}

// ---------------------------------------------------------------- 库存

// StockStrategy 库存最充足的优先，降低缺货风险。
type StockStrategy struct{}

func (StockStrategy) Name() string { return "stock" }
func (StockStrategy) Description() string {
	return "可售库存最多的优先。适合大促或高并发场景，减少缺货失败。"
}

func (StockStrategy) Rank(candidates []Candidate) []Candidate {
	return rankStable(candidates, func(a, b Candidate) bool {
		if a.Stock != b.Stock {
			return a.Stock > b.Stock
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.SupplyID < b.SupplyID
	})
}

// ---------------------------------------------------------------- 加权

// WeightedStrategy 加权随机。
// 采用「指数键」法：为每个候选生成 key = -ln(U)/weight，按 key 升序排列。
// 这样 weight 越大越靠前，同时保持随机性，能把流量按权重分散到多个平台。
type WeightedStrategy struct {
	mu   sync.Mutex
	rand *rand.Rand
}

// NewWeightedStrategy 构造加权策略。seed 为 nil 时使用随机种子。
func NewWeightedStrategy(seed *int64) *WeightedStrategy {
	var src rand.Source
	if seed != nil {
		src = rand.NewSource(*seed)
	} else {
		src = rand.NewSource(rand.Int63())
	}
	return &WeightedStrategy{rand: rand.New(src)}
}

func (s *WeightedStrategy) Name() string { return "weighted" }
func (s *WeightedStrategy) Description() string {
	return "按权重随机分配流量，避免单一平台被限流。适合多个等价货源的场景。"
}

func (s *WeightedStrategy) Rank(candidates []Candidate) []Candidate {
	if len(candidates) <= 1 {
		return append([]Candidate(nil), candidates...)
	}

	type keyed struct {
		c   Candidate
		key float64
	}
	s.mu.Lock()
	items := make([]keyed, 0, len(candidates))
	for _, c := range candidates {
		w := float64(c.Weight)
		if w <= 0 {
			w = 1 // 权重非法时按 1 处理，保证仍会被选中
		}
		u := s.rand.Float64()
		if u <= 0 {
			u = math.SmallestNonzeroFloat64
		}
		items = append(items, keyed{c: c, key: -math.Log(u) / w})
	}
	s.mu.Unlock()

	sort.SliceStable(items, func(i, j int) bool { return items[i].key < items[j].key })

	out := make([]Candidate, len(items))
	for i, it := range items {
		out[i] = it.c
	}
	return out
}

// ---------------------------------------------------------------- 注册表

type Registry struct {
	strategies map[string]Strategy
	order      []string
}

// NewRegistry 注册全部内置策略
func NewRegistry() *Registry {
	r := &Registry{strategies: make(map[string]Strategy)}
	r.Register(PriorityStrategy{})
	r.Register(CheapestStrategy{})
	r.Register(StockStrategy{})
	r.Register(NewWeightedStrategy(nil))
	return r
}

func (r *Registry) Register(s Strategy) {
	name := s.Name()
	if _, dup := r.strategies[name]; !dup {
		r.order = append(r.order, name)
	}
	r.strategies[name] = s
}

// Get 取策略，找不到时回落到优先级策略（保证永远有可用策略）。
func (r *Registry) Get(name string) Strategy {
	if s, ok := r.strategies[name]; ok {
		return s
	}
	return PriorityStrategy{}
}

// Metas 供前端下拉
func (r *Registry) Metas() []Meta {
	out := make([]Meta, 0, len(r.order))
	for _, name := range r.order {
		s := r.strategies[name]
		out = append(out, Meta{Name: s.Name(), Description: s.Description()})
	}
	return out
}

// ---------------------------------------------------------------- 辅助

// rankStable 按 less 排序，返回新切片（不改动入参）。
func rankStable(candidates []Candidate, less func(a, b Candidate) bool) []Candidate {
	out := append([]Candidate(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// FilterQuantity 过滤掉起购数量/库存不满足本次采购量的候选。
// 注意：库存是缓存值，可能不准；因此库存为 0 的候选不直接剔除，
// 而是由上层在询价阶段用真实库存再判断一次。
func FilterQuantity(candidates []Candidate, quantity int) []Candidate {
	out := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Stock > 0 && c.Stock < quantity {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return candidates // 全被过滤时退回全集，交给询价兜底
	}
	return out
}
