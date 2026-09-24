// Package f16688 是 16688 开放平台的适配器实现。
//
// 签名算法严格对齐官方文档与 PHP 参考实现：
//
//	去掉 sign / sign_type 与空值 → 数组/对象按键名递归排序后 JSON 序列化
//	（空数组/空对象跳过）→ 参数名 ASCII 排序 → k1=v1&k2=v2
//	→ 末尾直接拼接 secret → md5（小写）
//
// Go 与 PHP 的两个关键差异已在 encodeJSON 里处理：
//  1. Go 的 json.Marshal 默认转义 < > &，PHP 的 JSON_UNESCAPED_* 不转义
//     —— 必须用 SetEscapeHTML(false)
//  2. Go 的 json 编码器会按键名排序 map，与 PHP 的 ksort 行为一致，无需额外处理
package f16688

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// BuildSign 计算签名。
// 返回参与 md5 的原文（便于排查）与小写签名。
func BuildSign(params map[string]any, secret string) (signString string, sign string, err error) {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" {
			continue
		}
		if isEmptyValue(v) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys) // ASCII 升序

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := params[k]

		if isComposite(v) {
			// 空数组 / 空对象整体跳过（对齐 PHP 的 '[]' / '{}' 判断）
			if isCompositeEmpty(v) {
				continue
			}
			encoded, encErr := encodeJSON(v)
			if encErr != nil {
				return "", "", encErr
			}
			if encoded == "[]" || encoded == "{}" || encoded == "null" {
				continue
			}
			parts = append(parts, k+"="+encoded)
			continue
		}

		parts = append(parts, k+"="+scalarString(v))
	}

	signString = strings.Join(parts, "&") + secret
	sum := md5.Sum([]byte(signString))
	return signString, hex.EncodeToString(sum[:]), nil
}

// encodeJSON 等价于 PHP 的 json_encode($v, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES)。
func encodeJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 关键：不转义 < > &
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("序列化签名参数失败: %w", err)
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// isEmptyValue 对齐 PHP 的 `$v === ” || $v === null`。
// 注意 0 / 0.0 / false 都不算空，必须参与签名。
func isEmptyValue(v any) bool {
	if v == nil {
		return true
	}
	switch t := v.(type) {
	case string:
		return t == ""
	case *string:
		return t == nil || *t == ""
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		return rv.IsNil()
	}
	return false
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

func isCompositeEmpty(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return rv.Len() == 0
	}
	return false
}

// scalarString 对齐 PHP 的字符串拼接语义：true → "1"，false → ""。
func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "1"
		}
		return ""
	case int:
		return strconv.Itoa(t)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint:
		return strconv.FormatUint(uint64(t), 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}
