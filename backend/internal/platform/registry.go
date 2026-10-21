package platform

import (
	"fmt"
	"sort"
	"sync"
)

// Registry 适配器注册表。所有平台适配器在这里登记，业务层按 code 取用。
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register 注册一个适配器。code 重复会 panic —— 这属于编码错误，应在启动时暴露。
func (r *Registry) Register(f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	code := f.Code()
	if code == "" {
		panic("适配器 code 不能为空")
	}
	if _, dup := r.factories[code]; dup {
		panic(fmt.Sprintf("适配器 code 重复注册: %s", code))
	}
	r.factories[code] = f
}

// Get 按 code 取适配器
func (r *Registry) Get(code string) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[code]
	return f, ok
}

// MustGet 取不到直接返回错误（避免调用方到处判空）
func (r *Registry) MustGet(code string) (Factory, error) {
	f, ok := r.Get(code)
	if !ok {
		return nil, fmt.Errorf("未找到平台适配器: %s", code)
	}
	return f, nil
}

// Codes 返回全部已注册的适配器 code
func (r *Registry) Codes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.factories))
	for code := range r.factories {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// Metas 返回全部适配器元信息，供前端展示
func (r *Registry) Metas() []FactoryMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]FactoryMeta, 0, len(r.factories))
	for _, f := range r.factories {
		out = append(out, FactoryMeta{
			Code:             f.Code(),
			Name:             f.Name(),
			Description:      f.Description(),
			DefaultBaseURL:   f.DefaultBaseURL(),
			CredentialFields: f.CredentialFields(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}
