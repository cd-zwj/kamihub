package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"kamihub/internal/service"
)

// 敏感操作二次确认（`04` 文档 S2）。
//
// 前端流程：
//   1. 正常调用敏感接口
//   2. 若返回 428 + code=CONFIRM_REQUIRED → 弹密码框
//   3. POST /api/auth/confirm {password} → 拿到 ticket（10 分钟有效）
//   4. 带上 X-Confirm-Ticket 重发原请求
//
// 为什么用请求头而不是请求体：
// 查看卡密明文的接口是 GET（?reveal=1），没有请求体；
// 用请求头能让 GET / POST / PUT 走同一套逻辑，前端只需一个拦截器。

// confirmHeader 携带二次确认票据的请求头。
const confirmHeader = "X-Confirm-Ticket"

// confirmRequest 二次确认请求体。
type confirmRequest struct {
	Password string `json:"password"`
}

// confirmSensitive 校验密码并签发一张二次确认票据。
func (h *Handlers) confirmSensitive(c *gin.Context) {
	user := CurrentUser(c)
	if user == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.confirm == nil {
		ServerError(c, "二次确认服务未启用")
		return
	}

	var req confirmRequest
	if !BindJSON(c, &req) {
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		Fail(c, 400, "INVALID_ARGUMENT", "请输入密码")
		return
	}

	ticket, expires, err := h.confirm.Issue(c.Request.Context(), user.ID, req.Password)
	switch {
	case err == nil:
	case errors.Is(err, service.ErrConfirmPasswordWrong):
		h.auditLog(c, "auth.confirm_failed", "user", user.Username, nil)
		Fail(c, 400, "CONFIRM_PASSWORD_WRONG", "密码错误")
		return
	case errors.Is(err, service.ErrConfirmLocked):
		h.auditLog(c, "auth.confirm_locked", "user", user.Username, nil)
		Fail(c, 429, "CONFIRM_LOCKED", "密码错误次数过多，请稍后再试")
		return
	default:
		ServerError(c, "确认失败，请稍后重试")
		return
	}

	OK(c, gin.H{
		"ticket":             ticket,
		"expires_in_seconds": int(service.ConfirmTicketTTL.Seconds()),
		"expires_at":         expires,
	})
}

// confirmStatus 告诉前端当前是否需要二次确认（决定是否显示提示）。
func (h *Handlers) confirmStatus(c *gin.Context) {
	if h.confirm == nil {
		// 服务没装配时宁可报错，也不能返回 "required:false" 假装一切正常 ——
		// 那会让前端静默地不弹框，敏感操作等于失去保护。
		ServerError(c, "二次确认服务未启用")
		return
	}
	OK(c, gin.H{
		"required":    h.confirm.Required(c.Request.Context()),
		"ttl_seconds": int(service.ConfirmTicketTTL.Seconds()),
		"max_fail":    service.ConfirmMaxFail,
	})
}

// requireConfirm 校验请求头里的二次确认票据。
//
// 返回 false 时已经写好了响应，调用方直接 return 即可。
// 用 428 Precondition Required 而不是 403：
// 前端拦截器据此弹密码框，而不是当成「没权限」提示。
func (h *Handlers) requireConfirm(c *gin.Context) bool {
	// 服务缺失时**失败关闭**（deny），而不是放行。
	// 放行会让「忘了装配依赖」变成一个没人发现的静默安全漏洞。
	if h.confirm == nil {
		ServerError(c, "二次确认服务未启用")
		return false
	}
	if !h.confirm.Required(c.Request.Context()) {
		return true
	}
	user := CurrentUser(c)
	if user == nil {
		Unauthorized(c, "请先登录")
		return false
	}
	if h.confirm.Check(c.Request.Context(), user.ID, c.GetHeader(confirmHeader)) {
		return true
	}

	Fail(c, 428, "CONFIRM_REQUIRED", "该操作需要再次输入密码确认")
	return false
}
