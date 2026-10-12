package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
	"kamihub/internal/service"
)

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

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

	keyPerUser := fmt.Sprintf("rl:login:%s:%s", ip, req.Username)
	keyPerIP := fmt.Sprintf("rl:login-ip:%s", ip)

	if !h.cache.AllowN(ctx, keyPerUser, 10) || !h.cache.AllowN(ctx, keyPerIP, 30) {
		Fail(c, 429, "RATE_LIMITED", "登录尝试过于频繁，请稍后再试")
		return
	}

	issued, user, err := h.auth.Login(ctx, req.Username, req.Password, ip)
	if err != nil {
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
	OK(c, gin.H{
		"token": issued.Token,
		"user":  user,
		// 有效期来自网页设置（回落 .env 的 JWT_TTL），这里回真实的秒数
		"expires_in_seconds": int(time.Until(issued.ExpiresAt).Seconds()),
		"expires_at":         issued.ExpiresAt,
	})
}

func (h *Handlers) me(c *gin.Context) {
	OK(c, CurrentUser(c))
}

func (h *Handlers) logout(c *gin.Context) {
	// JWT 无状态，注销由前端清除令牌完成；这里仅记录审计
	h.auditLog(c, "auth.logout", "user", "", nil)
	OK(c, gin.H{"message": "已退出登录"})
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

func (h *Handlers) changePassword(c *gin.Context) {
	var req changePasswordRequest
	if !BindJSON(c, &req) {
		return
	}
	user := CurrentUser(c)
	if err := h.auth.ChangePassword(c.Request.Context(), user.ID, req.OldPassword, req.NewPassword); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "user.change_password", "user", user.Username, nil)
	OK(c, gin.H{"message": "密码已更新"})
}

// ---------------------------------------------------------------- 令牌签发

type issueTokenRequest struct {
	// UserID 令牌代表的账号。不传则签给当前登录的管理员自己。
	UserID uint `json:"user_id"`
	// TTLHours 有效期（小时）。不传则用网页设置的默认值。
	TTLHours int `json:"ttl_hours"`
	// Remark 用途备注，仅写进审计日志，便于事后追溯是谁为谁签发的。
	Remark string `json:"remark"`
}

// issueToken 管理员为指定账号签发一个长期 JWT，供服务端对接使用。
//
// 注意：JWT 是无状态的，签发后**无法单独吊销**。
// 泄露时的处置方式是禁用对应账号（Auth 中间件每次请求都回查用户状态）。
func (h *Handlers) issueToken(c *gin.Context) {
	var req issueTokenRequest
	if !BindJSON(c, &req) {
		return
	}

	me := CurrentUser(c)
	targetID := req.UserID
	if targetID == 0 {
		targetID = me.ID
	}

	// 允许签发给自己。TTLHours 为 0（或不传）表示用网页设置的默认有效期；
	// 其他值必须在允许区间内 —— 负数也走这个分支被拒，避免被静默当成默认值。
	var ttl time.Duration
	if req.TTLHours != 0 {
		if req.TTLHours < service.MinJWTTTLHours || req.TTLHours > service.MaxJWTTTLHours {
			BadRequest(c, fmt.Sprintf("有效期需在 %d ~ %d 小时之间（不传表示用默认值）",
				service.MinJWTTTLHours, service.MaxJWTTTLHours))
			return
		}
		ttl = time.Duration(req.TTLHours) * time.Hour
	}

	var target models.User
	if err := h.db.First(&target, targetID).Error; err != nil {
		NotFound(c, "账号不存在")
		return
	}
	if !target.IsEnabled() {
		BadRequest(c, "该账号已被禁用，无法签发令牌")
		return
	}

	issued, err := h.auth.IssueToken(c.Request.Context(), &target, ttl)
	if err != nil {
		ServerError(c, "签发令牌失败")
		return
	}

	// 审计里只记元信息，绝不落令牌明文
	h.auditLog(c, "auth.token_issue", "user", target.Username, gin.H{
		"ttl_hours":  issued.TTLHours,
		"expires_at": issued.ExpiresAt.Format(time.RFC3339),
		"issued_by":  me.Username,
		"remark":     strings.TrimSpace(req.Remark),
	})

	OK(c, gin.H{
		"token":      issued.Token,
		"expires_at": issued.ExpiresAt,
		"ttl_hours":  issued.TTLHours,
		"user":       target,
	})
}

// getTokenPolicy 返回当前生效的令牌有效期策略，供前端页面回填与校验。
func (h *Handlers) getTokenPolicy(c *gin.Context) {
	sec := h.settings.Security(c.Request.Context())
	OK(c, gin.H{
		"default_ttl_hours": sec.JWTTTLHours,
		"min_ttl_hours":     service.MinJWTTTLHours,
		"max_ttl_hours":     service.MaxJWTTTLHours,
		// 令牌是按用户名还是按 id 吊销，写清楚方便运维照做
		"revoke_hint": "JWT 无状态、无法单独吊销。泄露时请在「用户」页禁用对应账号，令牌立即失效。",
	})
}

// ---------------------------------------------------------------- 用户管理

func (h *Handlers) listUsers(c *gin.Context) {
	page, size := Pagination(c)
	q := h.db.Model(&models.User{})
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		q = q.Where("username ILIKE ? OR display_name ILIKE ?", "%"+kw+"%", "%"+kw+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	var users []models.User
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&users).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	OKPage(c, users, total, page, size)
}

type createUserRequest struct {
	Username    string `json:"username" binding:"required"`
	Password    string `json:"password" binding:"required"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Remark      string `json:"remark"`
}

func (h *Handlers) createUser(c *gin.Context) {
	var req createUserRequest
	if !BindJSON(c, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		BadRequest(c, "用户名不能为空")
		return
	}
	role := req.Role
	if role != models.RoleAdmin && role != models.RoleOperator {
		role = models.RoleOperator
	}
	hash, err := crypto.HashPassword(req.Password)
	if err != nil {
		Abort(c, err)
		return
	}

	user := models.User{
		Username:     req.Username,
		PasswordHash: hash,
		DisplayName:  req.DisplayName,
		Role:         role,
		Status:       models.UserStatusEnabled,
		Remark:       req.Remark,
	}
	if err := h.db.Create(&user).Error; err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			Conflict(c, "用户名已存在")
			return
		}
		ServerError(c, "创建用户失败")
		return
	}
	h.auditLog(c, "user.create", "user", req.Username, gin.H{"role": role})
	OK(c, user)
}

type updateUserRequest struct {
	DisplayName *string `json:"display_name"`
	Role        *string `json:"role"`
	Status      *int    `json:"status"`
	Remark      *string `json:"remark"`
}

func (h *Handlers) updateUser(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req updateUserRequest
	if !BindJSON(c, &req) {
		return
	}

	var user models.User
	if err := h.db.First(&user, id).Error; err != nil {
		NotFound(c, "用户不存在")
		return
	}

	updates := map[string]any{}
	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
	}
	if req.Remark != nil {
		updates["remark"] = *req.Remark
	}
	if req.Role != nil && (*req.Role == models.RoleAdmin || *req.Role == models.RoleOperator) {
		// 不允许把自己降级，避免把自己锁在门外
		if user.ID == CurrentUser(c).ID && *req.Role != models.RoleAdmin {
			BadRequest(c, "不能修改自己的角色")
			return
		}
		updates["role"] = *req.Role
	}
	if req.Status != nil {
		if user.ID == CurrentUser(c).ID && *req.Status != models.UserStatusEnabled {
			BadRequest(c, "不能禁用自己")
			return
		}
		updates["status"] = *req.Status
	}

	if len(updates) > 0 {
		if err := h.db.Model(&models.User{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			ServerError(c, "更新失败")
			return
		}
	}
	h.auditLog(c, "user.update", "user", user.Username, updates)

	if err := h.db.First(&user, id).Error; err != nil {
		ServerError(c, "读取失败")
		return
	}
	OK(c, user)
}

func (h *Handlers) deleteUser(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if id == CurrentUser(c).ID {
		BadRequest(c, "不能删除自己")
		return
	}

	// 至少保留一个管理员
	var user models.User
	if err := h.db.First(&user, id).Error; err != nil {
		NotFound(c, "用户不存在")
		return
	}
	if user.IsAdmin() {
		var adminCount int64
		h.db.Model(&models.User{}).Where("role = ?", models.RoleAdmin).Count(&adminCount)
		if adminCount <= 1 {
			BadRequest(c, "系统至少需要保留一个管理员")
			return
		}
	}

	if err := h.db.Delete(&models.User{}, id).Error; err != nil {
		ServerError(c, "删除失败")
		return
	}
	h.auditLog(c, "user.delete", "user", user.Username, nil)
	OK(c, gin.H{"message": "已删除"})
}

type resetPasswordRequest struct {
	NewPassword string `json:"new_password" binding:"required"`
}

func (h *Handlers) resetPassword(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req resetPasswordRequest
	if !BindJSON(c, &req) {
		return
	}
	if err := h.auth.ResetPassword(c.Request.Context(), id, req.NewPassword); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "user.reset_password", "user", fmt.Sprintf("%d", id), nil)
	OK(c, gin.H{"message": "密码已重置"})
}

var _ = service.ErrUserDisabled
