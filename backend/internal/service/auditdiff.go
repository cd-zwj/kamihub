package service

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// 审计日志的「改前 → 改后」支持（对照 `04-安全风控与通知.md` S4）。
//
// 要解决的问题：以前的审计只记 action + target，`detail` 大多传 nil，
// 所以事后只能看到「有人改了设置」，**改了哪个值、从什么改成什么一个字都没有**。
// 单人也一样会遇到：想不起来上周把哪个数改错了。
//
// 两个必须守住的底线：
//
//  1. **敏感字段只记「改过」，不记值。**
//     平台凭证、支付商户密钥、密码哈希、TOTP 密钥都不能进审计日志 ——
//     否则审计日志本身变成一个明文密钥库，比不记更糟。
//  2. **只记真正变化的字段。**
//     前端每次提交都会带上整个 JSON（包括没动的字段），
//     全量记录会把「改了一个数字」淹没在几十个未变字段里，反而看不见重点。

// FieldChange 一次改动中的单个字段。
type FieldChange struct {
	Field  string `json:"field"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
	// Masked 敏感字段：只留字段名，不留值。
	Masked bool `json:"masked,omitempty"`
}

// auditSkipFields 完全不参与差异比较的字段。
//
// id / 时间戳每写一次都会变，记下来只会制造噪音；
// password_hash、totp_secret 属于「连变化都不该留痕」的类别
// （知道它变了没有，本身就不该被记录）。
var auditSkipFields = map[string]bool{
	"id":            true,
	"created_at":    true,
	"updated_at":    true,
	"password_hash": true,
	"totp_secret":   true,
	"device_hash":   true,
}

// auditSensitiveFields 命中即脱敏（精确名，小写比较）。
//
// 同时收录无下划线的写法（apikey / secretkey）：
// 字段名拼写风格不统一时，靠后缀规则是兜不住的。
var auditSensitiveFields = map[string]bool{
	"key":            true, // 易支付商户密钥的字段名就叫 key
	"secret":         true,
	"pkey":           true,
	"password":       true,
	"token":          true,
	"credentials":    true,
	"api_key":        true,
	"apikey":         true,
	"api_secret":     true,
	"app_secret":     true,
	"secretkey":      true,
	"merchant_key":   true,
	"webhook_secret": true,
	// app_id / pid 是商户号，不算密钥：留着才能核对「改的是哪个商户」
}

// auditSensitiveSuffixes 字段名以此结尾即脱敏。
//
// 用后缀规则是因为新增字段时容易只改一半：
// 以后加个 `secondary_key`、`notify_token` 也不会漏掉。
//
// 这里刻意取「宁多遮不漏」：多遮一个非敏感字段，最多少一条追查线索；
// 漏掉一个密钥，等于把密钥写进了一个所有后台账号都能看的地方。
var auditSensitiveSuffixes = []string{"_key", "_secret", "_password", "_token", "_apikey", "_pwd"}

// IsSensitiveField 判断该字段是否属于「不能记值」的敏感字段。
func IsSensitiveField(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	if v, ok := auditSensitiveFields[lower]; ok {
		return v
	}
	for _, suffix := range auditSensitiveSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// Snapshot 把结构体或 JSON 转成 map，便于逐字段比较。
//
// 走一遍 json round-trip 而不是反射读字段：
// 这样 `omitempty`、自定义 Marshal、decimal 之类都按「前端看到的形状」比较，
// 避免出现「后端认为变了、前端认为没变」的错觉。
func Snapshot(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if raw, ok := v.(json.RawMessage); ok {
		return SnapshotJSON(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return SnapshotJSON(b)
}

// SnapshotJSON 把一段 JSON 对象转成 map。非对象（数组/标量）返回 nil。
func SnapshotJSON(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// DiffFields 比较前后快照，返回真正变化的字段（按字段名排序）。
//
// before / after 任一为 nil 时按空对象处理，因此「从无到有」「从有到无」都能覆盖。
func DiffFields(before, after map[string]any) []FieldChange {
	if before == nil {
		before = map[string]any{}
	}
	if after == nil {
		after = map[string]any{}
	}

	keys := make(map[string]struct{}, len(before)+len(after))
	for k := range before {
		keys[k] = struct{}{}
	}
	for k := range after {
		keys[k] = struct{}{}
	}

	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)

	out := make([]FieldChange, 0, len(names))
	for _, name := range names {
		if auditSkipFields[strings.ToLower(name)] {
			continue
		}
		bv, bHas := before[name]
		av, aHas := after[name]
		// 「不存在」和「存在但为 null」视为等价：
		// 前端删掉一个可选项时通常是传 null，不应该记成两次变化。
		if !bHas {
			bv = nil
		}
		if !aHas {
			av = nil
		}
		if equalJSONValue(bv, av) {
			continue
		}

		if IsSensitiveField(name) {
			// 敏感字段只留字段名：让人知道「它被改过」，但不留任何值
			out = append(out, FieldChange{Field: name, Masked: true})
			continue
		}
		out = append(out, FieldChange{Field: name, Before: bv, After: av})
	}
	return out
}

// equalJSONValue 按 JSON 语义比较两个值。
//
// 必须特殊处理数字：json.Unmarshal 到 any 后所有数字都是 float64，
// 而业务代码里的 before 可能来自结构体（int / string 形式的 decimal）。
// 直接把 12 (float64) 和 12 (int) 用 == 比较会判为「变了」，
// 于是每次保存都会刷出一条假的「改了 jwt_ttl_hours」。
func equalJSONValue(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	af, aIsNum := toFloat(a)
	bf, bIsNum := toFloat(b)
	if aIsNum && bIsNum {
		return af == bf
	}
	if aIsNum != bIsNum {
		// 一边是数字一边是字符串（如 "12" vs 12）：按 JSON 语义算不同
		return false
	}
	return reflect.DeepEqual(a, b)
}

// toFloat 尝试把值转成 float64。
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}
