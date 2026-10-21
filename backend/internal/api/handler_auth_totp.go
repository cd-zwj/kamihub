package api

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

// ---------------------------------------------------------------- 验证码

// getCaptcha 下发一道验证码（公开）。
//
// 为什么要公开：注册页、查单页在未登录时就要用，不能挂在鉴权后面。
// 被刷的风险由「一次性 + 失败封禁 + 同 IP 频率规则」三层兜住。
func (h *Handlers) getCaptcha(c *gin.Context) {
	if h.captcha == nil {
		ServerError(c, "验证码服务未启用")
		return
	}
	ch, err := h.captcha.Issue(c.Request.Context())
	if err != nil {
		ServerError(c, "生成验证码失败")
		return
	}
	OK(c, ch)
}

// captchaCheck 供各业务入口复用的校验入口。
//
// 返回 false 时已经写好响应，调用方直接 return 即可。
func (h *Handlers) captchaCheck(c *gin.Context, id, answer string) bool {
	if h.captcha == nil {
		// 服务未启用时放行 —— 不能因为缺 Redis 就让注册/查单全挂
		return true
	}
	if err := h.captcha.Verify(c.Request.Context(), id, answer); err != nil {
		switch {
		case errors.Is(err, service.ErrCaptchaLocked),
			errors.Is(err, service.ErrCaptchaExpired),
			errors.Is(err, service.ErrCaptchaWrong),
			errors.Is(err, service.ErrCaptchaRequired):
			Fail(c, 400, "CAPTCHA_INVALID", err.Error())
		default:
			ServerError(c, "验证码校验失败")
		}
		return false
	}
	return true
}

// ---------------------------------------------------------------- 登录（含 2FA）

// login 账号密码登录。
//
// 三种结果：
//   - 未开 2FA：直接给 token
//   - 已开 2FA：给 need_totp + 临时票据（**不含 token**）
//   - admin 未绑定：正常给 token，但带 totp_setup_required 让前端强制引导
func (h *Handlers) login(c *gin.Context) {
	var req loginRequest
	if !BindJSON(c, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)

	// 登录爆破防护，两层，且都只统计「失败」：
	//
	//  1) IP + 用户名：挡住针对某个已知账号（例如管理员）的持续爆破
	//  2) 纯 IP：挡住靠「换用户名」绕过第一层（撞库）
	//
	// 为什么必须第二层：第一层的键里带了用户名，攻击者每次换一个用户名
	// 就能重置配额。撞库（一个密码试一批账号）在这种键下几乎等于没限制。
	//
	// 为什么只统计失败：早期版本对每次请求都计数，结果正常用户连续登录
	// 十次就会被自己的成功登录拖进限流。现在改成
	//   进门前查额度（不自增）→ 失败才记账 → 成功则清零，
	// 正常用户永远不会因为自己的正确操作被拦。
	ip := c.ClientIP()
	ctx := c.Request.Context()

	keyPerUser := "rl:login:" + ip + ":" + req.Username
	keyPerIP := "rl:login-ip:" + ip

	if !h.cache.AllowN(ctx, keyPerUser, 10) || !h.cache.AllowN(ctx, keyPerIP, 30) {
		Fail(c, 429, "RATE_LIMITED", "登录尝试过于频繁，请稍后再试")
		return
	}

	issued, user, err := h.auth.Login(ctx, req.Username, req.Password, ip)
	if err != nil {
		// 密码对但需要第二步：这不是失败，不能计入爆破配额，
		// 否则正常开 2FA 的用户多点几次就被自己的限流拦住。
		if errors.Is(err, service.ErrTOTPRequired) && user != nil {
			if h.totp == nil {
				ServerError(c, "两步验证服务未启用，请联系管理员")
				return
			}
			ticket, terr := h.totp.IssueTicket(ctx, user.ID, user.Username)
			if terr != nil {
				ServerError(c, "签发验证票据失败")
				return
			}
			h.auditLog(c, "auth.login_totp_challenge", "user", user.Username, nil)
			OK(c, gin.H{
				"need_totp": true,
				"ticket":    ticket,
				// 有效期给前端做倒计时提示
				"expires_in_seconds": int(service.TOTPTicketTTL.Seconds()),
			})
			return
		}

		// 只有失败才消耗配额
		h.cache.Fail(ctx, keyPerUser, time.Minute)
		h.cache.Fail(ctx, keyPerIP, time.Minute)
		h.auditLog(c, "auth.login_failed", "user", req.Username, gin.H{"reason": err.Error()})
		Abort(c, err)
		return
	}

	// 登录成功：清空失败计数，让正常用户立刻恢复额度
	h.cache.Reset(ctx, keyPerUser, keyPerIP)

	h.auditLog(c, "auth.login", "user", user.Username, nil)
	OK(c, h.loginOKPayload(c, issued, user))
}

// loginOKPayload 统一构造「登录成功」的响应体。
//
// 除了 token 之外还告诉前端「这个 admin 还没绑 2FA」，由前端强制跳转绑定页。
// 为什么不在后端硬拦：硬拦会让「刚初始化、还没绑手机」的站长连后台都进不去，
// 反而制造一个自锁死局。引导 + 后台可见，比一刀切更安全也更可用。
func (h *Handlers) loginOKPayload(c *gin.Context, issued *service.IssuedToken, user *models.User) gin.H {
	payload := gin.H{
		"token": issued.Token,
		"user":  user,
		// 有效期来自网页设置（回落 .env 的 JWT_TTL），这里回真实的秒数
		"expires_in_seconds": int(time.Until(issued.ExpiresAt).Seconds()),
		"expires_at":         issued.ExpiresAt,
		"totp_enabled":       user.TOTPEnabled,
		"totp_setup_required": user.IsAdmin() && !user.TOTPEnabled,
	}
	return payload
}

type totpLoginRequest struct {
	Ticket string `json:"ticket" binding:"required"`
	Code   string `json:"code" binding:"required"`
}

// loginTOTP 用临时票据 + 动态码/恢复码换取真令牌。
func (h *Handlers) loginTOTP(c *gin.Context) {
	if h.totp == nil {
		ServerError(c, "两步验证服务未启用")
		return
	}
	var req totpLoginRequest
	if !BindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	// 票据必须能被「消费」：过期、伪造、已用过的都进不来。
	user, err := h.totp.ConsumeTicket(ctx, req.Ticket)
	if err != nil {
		Fail(c, 401, "TICKET_INVALID", err.Error())
		return
	}

	// 逐步验证码的尝试也要限流，否则 6 位数字可以被在线爆破
	key := "rl:totp:" + c.ClientIP()
	if !h.cache.AllowN(ctx, key, 10) {
		Fail(c, 429, "RATE_LIMITED", "验证码尝试过于频繁，请稍后再试")
		return
	}

	usedRecovery, verr := h.totp.Verify(ctx, user.ID, req.Code)
	if verr != nil {
		h.cache.Fail(ctx, key, time.Minute)
		h.auditLog(c, "auth.totp_failed", "user", user.Username, nil)
		// 票据已被消费，这里必须让前端重新走一次密码登录
		Fail(c, 401, "TOTP_INVALID", verr.Error()+"（请重新登录）")
		return
	}
	h.cache.Reset(ctx, key)

	issued, err := h.auth.CompleteTOTPLogin(ctx, user, c.ClientIP())
	if err != nil {
		ServerError(c, "签发令牌失败")
		return
	}

	h.auditLog(c, "auth.login_totp", "user", user.Username, gin.H{"recovery_code": usedRecovery})
	payload := h.loginOKPayload(c, issued, user)
	if usedRecovery {
		payload["used_recovery_code"] = true
		payload["recovery_hint"] = "本次使用的是恢复码，该码已作废。建议尽快重新生成恢复码。"
	}
	OK(c, payload)
}

// ---------------------------------------------------------------- 2FA 管理

// totpStatus 当前账号的 2FA 状态
func (h *Handlers) totpStatus(c *gin.Context) {
	user := CurrentUser(c)
	if h.totp == nil {
		OK(c, gin.H{"enabled": false, "required": false, "recovery_left": 0})
		return
	}
	st, err := h.totp.Status(c.Request.Context(), user.ID)
	if err != nil {
		ServerError(c, "查询两步验证状态失败")
		return
	}
	OK(c, st)
}

// totpSetup 生成密钥并返回 otpauth 链接（尚未启用）
func (h *Handlers) totpSetup(c *gin.Context) {
	user := CurrentUser(c)
	if h.totp == nil {
		ServerError(c, "两步验证服务未启用")
		return
	}
	res, err := h.totp.Setup(c.Request.Context(), user.ID)
	if err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "user.totp_setup", "user", user.Username, nil)
	OK(c, res)
}

type totpConfirmRequest struct {
	Code string `json:"code" binding:"required"`
}

// totpConfirm 确认绑定，返回 10 个恢复码（只此一次）
func (h *Handlers) totpConfirm(c *gin.Context) {
	user := CurrentUser(c)
	if h.totp == nil {
		ServerError(c, "两步验证服务未启用")
		return
	}
	var req totpConfirmRequest
	if !BindJSON(c, &req) {
		return
	}
	res, err := h.totp.Confirm(c.Request.Context(), user.ID, req.Code)
	if err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "user.totp_enabled", "user", user.Username, nil)
	OK(c, res)
}

type totpDisableRequest struct {
	Password string `json:"password" binding:"required"`
}

// totpDisable 关闭 2FA（需要重新输入密码）
func (h *Handlers) totpDisable(c *gin.Context) {
	user := CurrentUser(c)
	if h.totp == nil {
		ServerError(c, "两步验证服务未启用")
		return
	}
	var req totpDisableRequest
	if !BindJSON(c, &req) {
		return
	}
	if err := h.totp.Disable(c.Request.Context(), user.ID, req.Password); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "user.totp_disabled", "user", user.Username, nil)
	OK(c, gin.H{"message": "两步验证已关闭"})
}

// totpRecoveryCodes 重新生成恢复码（需要当前动态码）
func (h *Handlers) totpRecoveryCodes(c *gin.Context) {
	user := CurrentUser(c)
	if h.totp == nil {
		ServerError(c, "两步验证服务未启用")
		return
	}
	var req totpConfirmRequest
	if !BindJSON(c, &req) {
		return
	}
	codes, err := h.totp.RegenerateRecoveryCodes(c.Request.Context(), user.ID, req.Code)
	if err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "user.totp_recovery_regenerate", "user", user.Username, nil)
	OK(c, gin.H{"recovery_codes": codes})
}

// totpReset 管理员强制重置他人的 2FA（手机丢了 + 恢复码也没了的兜底）
func (h *Handlers) totpReset(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if h.totp == nil {
		ServerError(c, "两步验证服务未启用")
		return
	}
	if err := h.totp.Reset(c.Request.Context(), id); err != nil {
		ServerError(c, "重置两步验证失败")
		return
	}
	h.auditLog(c, "user.totp_reset", "user", strconv.FormatUint(uint64(id), 10), gin.H{"by": CurrentUser(c).Username})
	OK(c, gin.H{"message": "已重置该账号的两步验证，对方下次登录需重新绑定"})
}
