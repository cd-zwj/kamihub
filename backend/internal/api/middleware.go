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
