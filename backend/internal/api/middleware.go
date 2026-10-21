package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

// 上下文键
const (
	ctxUserKey      = "current_user"
	ctxBuyerKey     = "current_buyer"
	ctxRequestIDKey = "request_id"
)

func timeNowUnix() int64 { return time.Now().Unix() }

func intQuery(c *gin.Context, key string, fallback int) int {
	raw := c.Query(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func uintParam(c *gin.Context, key string) (uint, bool) {
	raw := c.Param(key)
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		BadRequest(c, "路径参数 "+key+" 不合法")
		return 0, false
	}
	return uint(v), true
}

// RequestID 为每个请求分配追踪号，并回写到响应头。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-Id")
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(ctxRequestIDKey, id)
		c.Header("X-Request-Id", id)
		c.Next()
	}
}

// AccessLog 记录访问日志
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()

		level := slog.LevelDebug
		if c.Writer.Status() >= 500 {
			level = slog.LevelError
		} else if c.Writer.Status() >= 400 {
			level = slog.LevelWarn
		}
		slog.Log(c.Request.Context(), level, "http",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"elapsed_ms", time.Since(started).Milliseconds(),
			"ip", c.ClientIP(),
			"request_id", c.GetString(ctxRequestIDKey),
		)
	}
}

// Recovery 捕获 panic，避免单个请求打挂整个服务。
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("请求处理 panic",
					"path", c.Request.URL.Path,
					"panic", r,
					"request_id", c.GetString(ctxRequestIDKey))
				ServerError(c, "服务内部错误")
			}
		}()
		c.Next()
	}
}

// AntiRed 防红：在微信 / QQ 内置浏览器里打开时给出提示页。
//
// 为什么卡密站需要这个：这类域名的落地页很容易被微信拦截，
// 用户点进来看到的是微信的红色警告页（所以叫「防红」），
// 而不是我们自己的错误提示 —— 用户会以为站点挂了或被骗了。
// 主动识别 UA 并给一个「请用浏览器打开」的页面，转化损失最小。
//
// 对照 dujiaoka-next 的实现（DujiaoBoot 中间件里的 is_open_anti_red）：
// 它在中间件里判断 UA 含 'QQ/' 或 'MicroMessenger' 就渲染提示视图。
// 这里保留同样的判断，但做成：
//   - 只在 enabled 时生效（默认关闭：这是新增行为，不能默认改变现状）
//   - 只拦「页面跳转」类请求（Accept 含 text/html），不拦 API 的 XHR，
//     否则前端发起的所有 JSON 请求都会拿到一段 HTML，页面直接崩
func AntiRed(enabled func() bool, page string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if enabled == nil || !enabled() {
			c.Next()
			return
		}
		// 只拦浏览器导航；XHR/fetch 的 Accept 通常不含 text/html
		if !strings.Contains(c.GetHeader("Accept"), "text/html") {
			c.Next()
			return
		}
		ua := c.GetHeader("User-Agent")
		if !isWechatOrQQ(ua) {
			c.Next()
			return
		}
		// 提货页也要拦：被拦截时用户最需要的就是看到正确的提示
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Status(http.StatusOK)
		_, _ = c.Writer.WriteString(page)
		c.Abort()
	}
}

// isWechatOrQQ 判断是否在微信/QQ 内置浏览器中。
//
// 微信的 UA 一定含 MicroMessenger；手机 QQ 含 ' QQ/'（前面有空格）
// 或 'QQ/'。统一用不区分大小写的包含判断，避免漏掉各种魔改 UA。
func isWechatOrQQ(ua string) bool {
	low := strings.ToLower(ua)
	return strings.Contains(low, "micromessenger") || strings.Contains(low, "qq/")
}

// CORS 允许前端独立域名访问。生产环境建议由 Nginx 同源反代，不依赖此中间件。
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowAll := len(allowedOrigins) == 0
	set := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		set[strings.TrimSpace(o)] = true
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowAll || set[origin]) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type,Authorization,X-Request-Id")
			c.Header("Access-Control-Max-Age", "86400")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// Auth 校验 JWT 并把用户放进上下文。
//
// 只接受员工令牌（typ=staff）：买家令牌即便签名有效也会被拒。
// 这是必需的 —— buyers 与 users 的主键各自从 1 开始，
// 若不做类型区分，买家 ID=1 的令牌会被当成员工 ID=1 直接进后台。
func Auth(auth *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			Unauthorized(c, "缺少访问令牌")
			return
		}
		claims, err := auth.ParseToken(token)
		if err != nil {
			Unauthorized(c, "访问令牌无效或已过期")
			return
		}
		if !claims.IsStaffToken() {
			Unauthorized(c, "访问令牌类型不匹配")
			return
		}
		// 每次都回查用户，确保被禁用/删除的账号立即失效
		user, err := auth.GetUser(c.Request.Context(), claims.UserID)
		if err != nil {
			Unauthorized(c, "账号不可用")
			return
		}
		c.Set(ctxUserKey, user)
		c.Next()
	}
}

// BuyerAuth 校验买家 JWT 并把买家放进上下文。
//
// 与 Auth 完全对称：只接受 typ=buyer 的令牌，员工令牌无法通过。
func BuyerAuth(auth *service.BuyerAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			Unauthorized(c, "请先登录")
			return
		}
		claims, err := auth.ParseBuyerToken(token)
		if err != nil {
			Unauthorized(c, "登录状态已失效，请重新登录")
			return
		}
		buyer, err := auth.GetBuyer(c.Request.Context(), claims.BuyerID)
		if err != nil {
			Unauthorized(c, "账号不可用")
			return
		}
		c.Set(ctxBuyerKey, buyer)
		c.Next()
	}
}

// CurrentBuyer 取当前登录买家
func CurrentBuyer(c *gin.Context) *models.Buyer {
	v, ok := c.Get(ctxBuyerKey)
	if !ok {
		return nil
	}
	b, ok := v.(*models.Buyer)
	if !ok {
		return nil
	}
	return b
}

// AdminOnly 要求管理员角色
func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := CurrentUser(c)
		if user == nil || !user.IsAdmin() {
			Forbidden(c, "需要管理员权限")
			return
		}
		c.Next()
	}
}

func extractToken(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return strings.TrimSpace(c.GetHeader("X-API-Key"))
}

// CurrentUser 取当前登录用户
func CurrentUser(c *gin.Context) *models.User {
	v, ok := c.Get(ctxUserKey)
	if !ok {
		return nil
	}
	u, ok := v.(*models.User)
	if !ok {
		return nil
	}
	return u
}

// RequestIDOf 取当前请求追踪号
func RequestIDOf(c *gin.Context) string {
	return c.GetString(ctxRequestIDKey)
}
