package platform

import (
	"errors"
	"fmt"
)

// FailKind 决定一次失败是否值得换一个货源重试。
type FailKind string

const (
	// KindTransient 网络抖动、超时、5xx、限流 —— 换平台重试有意义
	KindTransient FailKind = "transient"
	// KindBusiness 余额不足、库存不足、商品下架 —— 换平台重试有意义
	KindBusiness FailKind = "business"
	// KindFatal 参数错误、签名错误、凭证失效 —— 换平台也救不了
	KindFatal FailKind = "fatal"
)

// Error 适配器统一错误。
// 每个适配器都必须把上游五花八门的错误归类到 FailKind 上，
// 路由引擎据此决定「继续尝试下一个货源」还是「直接失败」。
type Error struct {
	Kind         FailKind
	PlatformCode string // 上游原始错误码/消息
	Message      string
	HTTPStatus   int
	Cause        error
}

func (e *Error) Error() string {
	if e.PlatformCode != "" {
		return fmt.Sprintf("%s (上游: %s)", e.Message, e.PlatformCode)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

// NewTransient 构造可重试错误
func NewTransient(message, platformCode string, httpStatus int, cause error) *Error {
	return &Error{Kind: KindTransient, Message: message, PlatformCode: platformCode, HTTPStatus: httpStatus, Cause: cause}
}

// NewBusiness 构造业务类错误（可换货源）
func NewBusiness(message, platformCode string) *Error {
	return &Error{Kind: KindBusiness, Message: message, PlatformCode: platformCode}
}

// NewFatal 构造致命错误（不该换货源）
func NewFatal(message, platformCode string) *Error {
	return &Error{Kind: KindFatal, Message: message, PlatformCode: platformCode}
}

// Failoverable 判断错误是否值得换货源重试。
// 非本包错误一律视为不可换（保守策略，避免掩盖真正的 bug）。
func Failoverable(err error) bool {
	if err == nil {
		return false
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Kind == KindTransient || pe.Kind == KindBusiness
	}
	return false
}

// KindOf 提取错误类别，非本包错误返回 fatal。
func KindOf(err error) FailKind {
	if err == nil {
		return ""
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Kind
	}
	return KindFatal
}

// ReasonOf 提取上游原始错误码（PlatformCode）。
// 用于上层按具体原因做差异化处理，例如 SUPPLY_PRICE_EXCEEDED 要单独统计，
// 而不是混在普通「采购失败」里 —— 那会让运营分不清「没货」和「嫌贵」。
func ReasonOf(err error) string {
	if err == nil {
		return ""
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.PlatformCode
	}
	return ""
}
