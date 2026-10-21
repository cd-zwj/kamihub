package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

// ================================================================
// 买家自助管理开放接口凭证（/api/shop/api-keys）
//
// 这是「买家也可以是下游」的入口：买家在用户页面自己签发
// app_id + secret，不需要找运营开权限。
//
// 鉴权用买家登录令牌（BuyerAuth），与开放接口的签名鉴权是两套：
// 管理凭证要用人登录态，调用业务接口用签名，职责分开。
// ================================================================

type shopCreateApiKeyReq struct {
	Name string `json:"name"`
}

// shopListApiKeys 列出我的密钥（不回显 secret 明文）
func (h *Handlers) shopListApiKeys(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	list, err := h.apiKeys.List(c.Request.Context(), buyer.ID)
	if err != nil {
		ServerError(c, "查询密钥失败")
		return
	}
	OK(c, gin.H{"list": list, "max": service.MaxApiKeysPerBuyer})
}

// shopCreateApiKey 签发一个新密钥。
//
// 响应里带明文 secret —— 这是唯一一次「必须」返回明文的时机。
// 不过我们的 secret 是加密存储的，买家之后仍可在页面上再次查看
// （用 reveal 接口），所以就算这次没存下来也不会丢。
func (h *Handlers) shopCreateApiKey(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	var req shopCreateApiKeyReq
	if !BindJSON(c, &req) {
		return
	}

	// 限流：防止有人刷密钥把表撑爆（上限本身也有兜底）
	if !h.cache.Allow(c.Request.Context(), "shop:apikey:"+itoa(int64(buyer.ID)), 20, 10*60*1e9) {
		Fail(c, 429, "TOO_MANY_REQUESTS", "操作过于频繁，请稍后再试")
		return
	}

	key, secret, err := h.apiKeys.Create(c.Request.Context(), buyer.ID, req.Name)
	if err != nil {
		h.abortApiKey(c, err)
		return
	}
	h.auditLog(c, "buyer.apikey.create", "buyer_api_key", key.AppID, gin.H{"name": key.Name})
	OK(c, gin.H{"key": key, "secret": secret})
}

// shopRevealApiKey 查看明文 secret
func (h *Handlers) shopRevealApiKey(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	// 限流：secret 是敏感信息，避免被脚本高频拉取
	if !h.cache.Allow(c.Request.Context(), "shop:apikeyreveal:"+itoa(int64(buyer.ID)), 60, 10*60*1e9) {
		Fail(c, 429, "TOO_MANY_REQUESTS", "操作过于频繁，请稍后再试")
		return
	}
	secret, err := h.apiKeys.Reveal(c.Request.Context(), buyer.ID, id)
	if err != nil {
		h.abortApiKey(c, err)
		return
	}
	h.auditLog(c, "buyer.apikey.reveal", "buyer_api_key", itoa(int64(id)), nil)
	OK(c, gin.H{"secret": secret})
}

// shopResetApiKey 重置 secret（app_id 不变）
func (h *Handlers) shopResetApiKey(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	secret, err := h.apiKeys.Reset(c.Request.Context(), buyer.ID, id)
	if err != nil {
		h.abortApiKey(c, err)
		return
	}
	h.auditLog(c, "buyer.apikey.reset", "buyer_api_key", itoa(int64(id)), nil)
	OK(c, gin.H{"secret": secret})
}

type shopApiKeyStatusReq struct {
	// Status 1 启用 / 0 停用
	Status int `json:"status"`
}

// shopSetApiKeyStatus 启用 / 停用
func (h *Handlers) shopSetApiKeyStatus(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req shopApiKeyStatusReq
	if !BindJSON(c, &req) {
		return
	}
	if err := h.apiKeys.SetStatus(c.Request.Context(), buyer.ID, id, req.Status); err != nil {
		h.abortApiKey(c, err)
		return
	}
	h.auditLog(c, "buyer.apikey.status", "buyer_api_key", itoa(int64(id)),
		gin.H{"status": req.Status})
	OK(c, gin.H{"message": "已更新"})
}

type shopRenameApiKeyReq struct {
	Name string `json:"name"`
}

// shopRenameApiKey 改备注名
func (h *Handlers) shopRenameApiKey(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req shopRenameApiKeyReq
	if !BindJSON(c, &req) {
		return
	}
	if err := h.apiKeys.Rename(c.Request.Context(), buyer.ID, id, req.Name); err != nil {
		h.abortApiKey(c, err)
		return
	}
	OK(c, gin.H{"message": "已更新"})
}

// shopDeleteApiKey 删除
func (h *Handlers) shopDeleteApiKey(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if err := h.apiKeys.Delete(c.Request.Context(), buyer.ID, id); err != nil {
		h.abortApiKey(c, err)
		return
	}
	h.auditLog(c, "buyer.apikey.delete", "buyer_api_key", itoa(int64(id)), nil)
	OK(c, gin.H{"message": "已删除"})
}

// abortApiKey 错误映射
func (h *Handlers) abortApiKey(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrApiKeyNotFound):
		NotFound(c, err.Error())
	case errors.Is(err, service.ErrApiKeyDisabled):
		Forbidden(c, err.Error())
	case errors.Is(err, service.ErrApiKeyTooMany):
		Conflict(c, err.Error())
	case errors.Is(err, service.ErrApiKeyNameBad):
		BadRequest(c, err.Error())
	default:
		// 解密失败（ENCRYPTION_KEY 被改过）属于服务端问题，别让买家猜
		if strings.Contains(err.Error(), "解密失败") {
			ServerError(c, "密钥解密失败，请重置该密钥的 secret")
			return
		}
		BadRequest(c, err.Error())
	}
}

// 确保 models 被引用
var _ = models.BuyerApiKey{}
