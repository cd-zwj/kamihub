// Package f16688 是 16688 开放平台的适配器实现。
//
// # 签名协议（HMAC-SHA256，2026-10 升级）
//
// 16688 已废弃旧的 MD5 签名，改为 HMAC-SHA256 并绑定请求方法与接口。
// 签名原文的组装规则：
//
//	POST\n{controller}\n{action}\n{k1=v1&k2=v2&...}
//
// 其中：
//   - 第一段固定为大写方法名（本平台全是 POST）
//   - controller / action 取自接口路径，**整段转小写**
//     例如 /openApi/stockSecret/invalidateAll → stocksecret / invalidateall
//   - 参数名按 ASCII 升序排列，仅排除 sign 自身
//   - 参数值按 canonicalValue 规则序列化
//
// 最终 sign = hex(HMAC-SHA256(secret, 原文))，小写。
//
// # 与旧 MD5 协议的行为差异（踩过坑，逐条列明）
//
//	项           旧（MD5）              新（HMAC-SHA256）
//	参与字段      跳过空值               空字符串/空数组**也参与**
//	布尔值        true→"1" false→""     true→"true" false→"false"
//	空数组        "[]" 跳过             "[]" 参与签名
//	数组顺序      保持原序              normalize 后序列化
//	前缀          无                    METHOD\ncontroller\naction\n
//	密钥          任意                  商户后台的 64 位十六进制
//
// 「空字符串也参与签名」是最容易出错的一条：旧实现会跳过空值，
// 若沿用旧逻辑，凡是带空字段的请求（例如 keywords: ""）验签必然失败。
//
// # 与 PHP / JS 参考实现的对齐
//
// 官方调试页与文档用的是 PHP 语义，Go 有两处天然差异，已在 encodeJSON 里处理：
//  1. Go 的 json.Marshal 默认转义 < > &，PHP 的 JSON_UNESCAPED_* 与
//     JS 的 JSON.stringify 都不转义 —— 必须 SetEscapeHTML(false)
//  2. Go 的 nil slice 会序列化成 null，而 JS 的空数组是 []
//     —— 见 canonicalValue 里对 typed nil 的处理
package f16688

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// SignMethod 本平台所有开放接口都走 POST
const SignMethod = "POST"

// nonceCharset 与官方调试页一致的字符集（大小写字母 + 数字）
const nonceCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// NonceLen nonce 长度。
// 官方要求 16~64 位，且 600 秒内不可重复。
// 取 32 位：空间足够大（62^32），同时留足余量。
const NonceLen = 32

// EndpointOf 从接口路径解析 controller 与 action，均转小写。
//
// 与官方调试页 endpointOf() 的语义一致：
// 先定位 openApi 段，取其后两段，整段小写。
//
//	/openApi/goods/list              → goods, list
//	/openApi/stockSecret/invalidateAll → stocksecret, invalidateall
//	/openApi/merchant/balance        → merchant, balance
func EndpointOf(path string) (controller, action string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 {
		return "", ""
	}

	// 定位 openApi 段（大小写不敏感）
	at := -1
	for i, p := range parts {
		if strings.EqualFold(p, "openApi") {
			at = i
			break
		}
	}
	rest := parts
	if at >= 0 {
		rest = parts[at+1:]
	}

	if len(rest) > 0 {
		controller = strings.ToLower(rest[0])
	}
	if len(rest) > 1 {
		action = strings.ToLower(rest[1])
	}
	return controller, action
}

// NewNonce 生成一个 32 位的随机 nonce。
//
// 用 crypto/rand 做无偏采样：直接取模会让前若干字符出现概率偏高，
// 虽然对本场景影响极小，但既然要用随机数就取干净的做法。
func NewNonce() (string, error) {
	buf := make([]byte, NonceLen)
	out := make([]byte, NonceLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 nonce 失败: %w", err)
	}
	// 拒绝采样：只接受落在最大的 n*len(charset) 范围内的字节，
	// 保证每个字符等概率。
	limit := 256 - (256 % len(nonceCharset))
	i := 0
	for {
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out[i] = nonceCharset[int(b)%len(nonceCharset)]
			i++
			if i == NonceLen {
				return string(out), nil
			}
		}
		// 极少数情况（连续拒绝）需要补一轮随机数
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成 nonce 失败: %w", err)
		}
	}
}

// BuildSignature 计算签名。
//
// 返回签名原文（便于排查对接问题）与小写十六进制签名。
func BuildSignature(method, controller, action string, params map[string]any, secret string) (plain string, sign string, err error) {
	keys := make([]string, 0, len(params))
	for k := range params {
		// 只排除 sign 自身；sign_type 在新协议里已不存在，
		// 万一被传进来也应当参与签名（与服务端行为一致）。
		if k == "sign" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys) // ASCII 升序

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, vErr := canonicalValue(params[k])
		if vErr != nil {
			return "", "", vErr
		}
		parts = append(parts, k+"="+v)
	}

	plain = strings.ToUpper(method) + "\n" + controller + "\n" + action + "\n" + strings.Join(parts, "&")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(plain))
	return plain, hex.EncodeToString(mac.Sum(nil)), nil
}

// canonicalValue 把参数值序列化成参与签名的字符串。
//
// 对齐官方 JS 实现：
//
//	function canonicalValue(value) {
//	  if (typeof value === 'boolean') return value ? 'true' : 'false';
//	  if (value && typeof value === 'object') return JSON.stringify(normalize(value));
//	  if (value === null || value === undefined) return '';
//	  return String(value);
//	}
func canonicalValue(v any) (string, error) {
	if v == nil {
		return "", nil
	}

	// typed nil（(*string)(nil)、[]string(nil)、map[string]any(nil)）对齐 JS 的 null → ''
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return "", nil
		}
	case reflect.Slice, reflect.Map:
		if rv.IsNil() {
			return "", nil
		}
	}

	// 标量
	switch t := v.(type) {
	case bool:
		if t {
			return "true", nil
		}
		return "false", nil
	case string:
		return t, nil
	case json.Number:
		return t.String(), nil
	case int:
		return strconv.Itoa(t), nil
	case int8:
		return strconv.FormatInt(int64(t), 10), nil
	case int16:
		return strconv.FormatInt(int64(t), 10), nil
	case int32:
		return strconv.FormatInt(int64(t), 10), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case uint:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint64:
		return strconv.FormatUint(t, 10), nil
	case float32:
		return formatFloat(float64(t)), nil
	case float64:
		return formatFloat(t), nil
	}

	// 复合类型：数组 / 对象
	if isComposite(v) {
		norm, err := normalize(v)
		if err != nil {
			return "", err
		}
		return encodeJSON(norm)
	}

	// 兜底：交给 fmt，虽然整型/字符串已在上面的 switch 覆盖
	return fmt.Sprintf("%v", v), nil
}

// formatFloat 对齐 JS 的 Number → String：
//
//	1      → "1"
//	1.5    → "1.5"
//	10.00  → "10"
//
// Go 的 'f',-1 与 JS 对常规业务数值的行为一致。
// 注意不要用 %v：它会输出 1.5 但也会把大数写成科学计数法。
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// normalize 递归整理复合结构：
//   - 对象：键名排序（Go 的 json.Marshal 对 map 已排序，这里显式递归保证嵌套层也对齐）
//   - 数组：保持元素顺序，递归处理每个元素
//
// 与官方 JS 的 normalize() 对齐。JS 里还有一段 isPhpList 判断，
// 用于处理 PHP 把关联数组编码成 {"0":..,"1":..} 的情况；
// Go 侧构造参数用的是原生 slice/map，不存在这种形态，故不需要。
func normalize(v any) (any, error) {
	if v == nil {
		return nil, nil
	}

	rv := reflect.ValueOf(v)

	// 先处理 typed nil
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}
		return normalize(rv.Elem().Interface())
	}

	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			nv, err := normalize(rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			out = append(out, nv)
		}
		return out, nil

	case reflect.Map:
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			nv, err := normalize(iter.Value().Interface())
			if err != nil {
				return nil, err
			}
			out[fmt.Sprintf("%v", iter.Key().Interface())] = nv
		}
		return out, nil
	}

	// 标量：保持原样，交给 json.Marshal
	return v, nil
}

// encodeJSON 等价于 JS 的 JSON.stringify。
//
// 两者都不转义 < > &、不转义 Unicode、不转义斜杠，
// 因此必须关掉 Go 默认的 HTML 转义。
func encodeJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 关键：不转义 < > &
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("序列化签名参数失败: %w", err)
	}
	// Encoder 会在末尾补换行，去掉以免影响签名
	return strings.TrimRight(buf.String(), "\n"), nil
}

func isComposite(v any) bool {
	if v == nil {
		return false
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		return true
	}
	return false
}
