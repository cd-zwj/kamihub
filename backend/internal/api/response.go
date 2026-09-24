// Package api 提供 HTTP 接口层。
package api

import (
	"errors"
	"net/http"
	"reflect"

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

// BindJSON 解析并校验请求体，失败时自动写响应并返回 false。
func BindJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		BadRequest(c, "请求参数不合法："+err.Error())
		return false
	}
	return true
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
