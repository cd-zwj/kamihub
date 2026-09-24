// Package tmpl 提供极简的 {{变量}} 模板渲染。
package tmpl

import (
	"regexp"
	"strings"
)

var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// Render 把 {{key}} 替换为 vars 中的值。
// 未定义的占位符原样保留，方便排查拼写错误；空值渲染为空串。
func Render(text string, vars map[string]string) string {
	if text == "" {
		return ""
	}
	return placeholder.ReplaceAllStringFunc(text, func(match string) string {
		groups := placeholder.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		if v, ok := vars[groups[1]]; ok {
			return v
		}
		return match
	})
}

// Compose 把主内容与附加内容按位置拼起来，任一侧为空时不留多余分隔符。
func Compose(body, extra, position, separator string) string {
	body = strings.TrimSpace(body)
	extra = strings.TrimSpace(extra)

	switch {
	case extra == "":
		return body
	case body == "":
		return extra
	case position == "before":
		return extra + separator + body
	default:
		return body + separator + extra
	}
}
