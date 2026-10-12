// Package api 提供 HTTP 接口层。
package api

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"

	"kamihub/internal/platform"
	"kamihub/internal/service"
)

// Envelope 统一响应结构，与前端约定一致。
type Envelope struct {
	Code  int    `json:"code"` // 1 成功 / 0 失败
	Msg   string `json:"msg"`
	Time  int64  `json:"time"`
	Data  any    `json:"data"`
	Error string `json:"error_code,omitempty"`
}

func now() int64 { return timeNowUnix() }

// OK 成功响应
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{Code: 1, Msg: "success", Time: now(), Data: data})
}

// OKPage 分页响应
//
// list 一律经 nonNilSlice 兜底：查询无结果时 GORM 会把切片留成 nil，
// 而 nil 切片会被 encoding/json 序列化成 null，前端 list.map() 直接抛错白屏。
func OKPage(c *gin.Context, list any, total int64, page, pageSize int) {
	OK(c, gin.H{
		"list":      nonNilSlice(list),
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// nonNilSlice 把 nil 切片换成同类型的空切片，保证 JSON 里永远是 []。
//
// 只处理切片：类型化的 nil 切片装进 any 后 != nil，必须用反射才能识别，
// 所以这里不能用简单的 v == nil 判断。
func nonNilSlice(v any) any {
	if v == nil {
		return []any{}
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice && rv.IsNil() {
		return reflect.MakeSlice(rv.Type(), 0, 0).Interface()
	}
	return v
}

// Fail 失败响应
func Fail(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, Envelope{Code: 0, Msg: msg, Time: now(), Data: nil, Error: code})
}

func BadRequest(c *gin.Context, msg string)   { Fail(c, http.StatusBadRequest, "BAD_REQUEST", msg) }
func Unauthorized(c *gin.Context, msg string) { Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", msg) }
func Forbidden(c *gin.Context, msg string)    { Fail(c, http.StatusForbidden, "FORBIDDEN", msg) }
func NotFound(c *gin.Context, msg string)     { Fail(c, http.StatusNotFound, "NOT_FOUND", msg) }
func Conflict(c *gin.Context, msg string)     { Fail(c, http.StatusConflict, "CONFLICT", msg) }
func ServerError(c *gin.Context, msg string) {
	Fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", msg)
}

// Abort 把业务错误映射成合适的 HTTP 状态码。
func Abort(c *gin.Context, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, service.ErrGoodsNotFound):
		NotFound(c, err.Error())
	case errors.Is(err, service.ErrInvalidCredentials):
		Unauthorized(c, err.Error())
	case errors.Is(err, service.ErrUserDisabled):
		Forbidden(c, err.Error())
	case errors.Is(err, service.ErrDeliveryNotFound):
		NotFound(c, err.Error())
	default:
		// 上游调用失败要区别于「参数写错了」：
		// 前者是 502（对方的问题，可重试），后者是 400（调用方的问题）。
		var pe *platform.Error
		if errors.As(err, &pe) {
			Fail(c, http.StatusBadGateway, "UPSTREAM_ERROR", err.Error())
			return
		}
		BadRequest(c, err.Error())
	}
}

// bodyCacheKey 是请求体原文在 gin.Context 里的键。
// 只在 BindJSON 里写入，供解析失败时打日志用。
const bodyCacheKey = "dsh.raw_body"

// BindJSON 解析并校验请求体，失败时自动写响应并返回 false。
//
// 失败时会把「原始请求体」打进日志（token/密码类字段脱敏）。
// 这条日志是被实际踩出来的：对接方（Python aiohttp / 发卡系统）拿到
// 400 之后，我们只看得到「参数不合法」这种泛化文案，
// 根本不知道对方到底发了什么——是字段类型不对、还是 JSON 被包成了字符串、
// 还是 Content-Type 不对。把原文记下来，一次就能定位，不用来回猜。
func BindJSON(c *gin.Context, target any) bool {
	// 先把 body 读出来缓存住再交给 Gin 解析。
	// 不能等解析失败后再去读：Gin 已经把 body 流读空了，那时读到的永远是空字符串，
	// 日志里就只剩一句「参数不合法」，看不到对方到底发了什么——这正是要修的问题。
	if c.Request.Body != nil {
		if raw, rerr := io.ReadAll(io.LimitReader(c.Request.Body, 64*1024)); rerr == nil {
			c.Request.Body = io.NopCloser(bytes.NewReader(raw))
			c.Set(bodyCacheKey, string(raw))
		}
	}
	if err := c.ShouldBindJSON(target); err != nil {
		raw, _ := c.Get(bodyCacheKey)
		body, _ := raw.(string)
		slog.Warn("请求体解析失败",
			"path", c.Request.URL.Path,
			"method", c.Request.Method,
			"error", err.Error(),
			"content_type", c.ContentType(),
			"content_length", c.Request.ContentLength,
			"body", redactBody(body),
			"ip", c.ClientIP())
		BadRequest(c, "请求参数不合法："+err.Error())
		return false
	}
	return true
}

// redactBody 把请求体里的敏感字段替换掉再进日志。
//
// 只做字符串级替换，不解析 JSON —— 因为走到这里的请求体很可能根本不是
// 合法 JSON（这正是我们要看它的原因），解析会失败。
func redactBody(body string) string {
	out := body
	for _, key := range []string{"Authorization", "authorization", "password", "token", "secret", "sign"} {
		// 形如 "password":"xxx" 或 "password": "xxx"
		out = redactKV(out, `"`+key+`"`)
	}
	// 请求头里的 Bearer token 不在这里，但 body 里偶尔会带
	if len(out) > 4096 {
		out = out[:4096] + "...(truncated)"
	}
	return out
}

func redactKV(s, quotedKey string) string {
	idx := 0
	for {
		i := strings.Index(s[idx:], quotedKey)
		if i < 0 {
			return s
		}
		i += idx
		j := i + len(quotedKey)
		// 跳过冒号和空白
		for j < len(s) && (s[j] == ':' || s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j >= len(s) || s[j] != '"' {
			idx = j
			continue
		}
		k := j + 1
		for k < len(s) && s[k] != '"' {
			k++
		}
		if k >= len(s) {
			return s
		}
		s = s[:j+1] + "[redacted]" + s[k:]
		idx = j + 1 + len("[redacted]")
	}
}

// Pagination 解析分页参数
func Pagination(c *gin.Context) (page, size int) {
	page = intQuery(c, "page", 1)
	size = intQuery(c, "page_size", 20)
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	return
}
