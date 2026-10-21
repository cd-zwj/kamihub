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

// ---------- 已信任设备（S3 配套）----------

// listMyDevices 列出当前账号已信任的浏览器。
//
// 让站长能自己核对「这些设备我都认识吗」——
// 只有能查、能删，告警才有闭环：收到提醒 → 来这看一眼 → 不认识的删掉。
func (h *Handlers) listMyDevices(c *gin.Context) {
	user := CurrentUser(c)
	if user == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.loginWatch == nil {
		ServerError(c, "登录监控服务未启用")
		return
	}
	devices, err := h.loginWatch.Devices(c.Request.Context(), user.ID, 50)
	if err != nil {
		ServerError(c, "查询设备失败")
		return
	}
	ips, err := h.loginWatch.IPHistory(c.Request.Context(), user.ID, 30)
	if err != nil {
		ServerError(c, "查询登录 IP 失败")
		return
	}
	OK(c, gin.H{
		"devices": devices,
		"ips":     ips,
	})
}

// forgetMyDevice 移除一台已信任设备。
//
// 典型用途：电脑卖掉/丢了、借别人用过、或在告警里看到不认识的设备。
// 删掉后那台浏览器下次登录会重新触发一次提醒。
func (h *Handlers) forgetMyDevice(c *gin.Context) {
	user := CurrentUser(c)
	if user == nil {
		Unauthorized(c, "请先登录")
		return
	}
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if h.loginWatch == nil {
		ServerError(c, "登录监控服务未启用")
		return
	}
	if err := h.loginWatch.ForgetDevice(c.Request.Context(), user.ID, id); err != nil {
		ServerError(c, "移除设备失败")
		return
	}
	h.auditLog(c, "auth.device_forgotten", "user", user.Username, gin.H{"device_id": id})
	OK(c, gin.H{"message": "已移除，该设备下次登录会重新提醒"})
}
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
