// Package logger 提供结构化 JSON 日志，敏感字段自动打码。
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// 这些 key 的值一律打码，避免 secret / 卡密 落到日志里
var sensitiveKeys = map[string]struct{}{
	"secret":        {},
	"app_secret":    {},
	"appsecret":     {},
	"sign":          {},
	"password":      {},
	"password_hash": {},
	"token":         {},
	"jwt":           {},
	"authorization": {},
	"credentials":   {},
	"encryption_key": {},
	"cards":         {},
	"card_codes":    {},
	"content":       {},
	"delivery_text": {},
}

// Setup 初始化全局 logger。
func Setup(level string, production bool) {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level:       lv,
		ReplaceAttr: redactAttr,
	}

	var handler slog.Handler
	if production {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}

// redactAttr 在输出前把敏感字段替换成 [redacted]
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if _, hit := sensitiveKeys[strings.ToLower(a.Key)]; hit {
		return slog.String(a.Key, "[redacted]")
	}
	return a
}

// Mask 只保留首尾若干位，用于日志里核对而不泄露全文。
func Mask(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= 8 {
		return string(r[:1]) + "***"
	}
	return string(r[:4]) + "***" + string(r[len(r)-4:])
}
