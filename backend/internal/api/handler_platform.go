package api

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

// listAdapters 返回所有已注册的平台适配器，前端据此渲染「新增平台」表单。
func (h *Handlers) listAdapters(c *gin.Context) {
	OK(c, gin.H{"list": h.platforms.Registry().Metas()})
}

// listStrategies 返回可用的路由策略
func (h *Handlers) listStrategies(c *gin.Context) {
	OK(c, gin.H{"list": h.routes.Metas()})
}

func (h *Handlers) listPlatforms(c *gin.Context) {
	page, size := Pagination(c)

	q := h.db.Model(&models.Platform{})
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name ILIKE ? OR code ILIKE ?", like, like)
	}
	if enabled := c.Query("enabled"); enabled != "" {
		q = q.Where("enabled = ?", enabled == "true" || enabled == "1")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}

	var platforms []models.Platform
	if err := q.Order("sort_order ASC, id DESC").Offset((page - 1) * size).Limit(size).
		Find(&platforms).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}

	// 转成脱敏视图：绝不返回凭证内容
	out := make([]any, 0, len(platforms))
	for i := range platforms {
		out = append(out, h.platforms.ToMasked(&platforms[i]))
	}
	OKPage(c, out, total, page, size)
}

func (h *Handlers) getPlatform(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var p models.Platform
	if err := h.db.First(&p, id).Error; err != nil {
		NotFound(c, "平台不存在")
		return
	}
	OK(c, h.platforms.ToMasked(&p))
}

// platformCreateRequest 新建平台。Code 是适配器标识，必填且不可改。
type platformCreateRequest struct {
	Code            string            `json:"code" binding:"required"`
	Name            string            `json:"name" binding:"required"`
	Enabled         *bool             `json:"enabled"`
	BaseURL         string            `json:"base_url"`
	Credentials     map[string]string `json:"credentials"`
	Extra           json.RawMessage   `json:"extra"`
	TimeoutMS       *int              `json:"timeout_ms"`
	MaxRetries      *int              `json:"max_retries"`
	RateLimitPerMin *int              `json:"rate_limit_per_min"`
	SortOrder       *int              `json:"sort_order"`
	Remark          string            `json:"remark"`
}

// platformUpdateRequest 更新平台。
// 刻意不含 Code：适配器标识一旦创建就不能改，改了会让已配置的货源指向不存在的适配器。
// 也刻意不加 binding:"required"：更新是部分更新，调用方可能只想改其中一两项。
type platformUpdateRequest struct {
	Name            *string           `json:"name"`
	Enabled         *bool             `json:"enabled"`
	BaseURL         *string           `json:"base_url"`
	Credentials     map[string]string `json:"credentials"`
	Extra           json.RawMessage   `json:"extra"`
	TimeoutMS       *int              `json:"timeout_ms"`
	MaxRetries      *int              `json:"max_retries"`
	RateLimitPerMin *int              `json:"rate_limit_per_min"`
	SortOrder       *int              `json:"sort_order"`
	Remark          *string           `json:"remark"`
}

func (h *Handlers) createPlatform(c *gin.Context) {
	// 建平台等于写入上游凭证（app_secret 等），属敏感操作
	if !h.requireConfirm(c) {
		return
	}
	var req platformCreateRequest
	if !BindJSON(c, &req) {
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	req.Name = strings.TrimSpace(req.Name)

	if _, err := h.platforms.Registry().MustGet(req.Code); err != nil {
		BadRequest(c, "不支持的平台类型："+req.Code)
		return
	}
	if req.Name == "" {
		BadRequest(c, "平台名称不能为空")
		return
	}
	if len(req.Credentials) == 0 {
		BadRequest(c, "请填写平台凭证")
		return
	}

	encrypted, err := h.platforms.EncryptCredentials(req.Credentials)
	if err != nil {
		ServerError(c, "凭证加密失败")
		return
	}

	p := models.Platform{
		Code:            req.Code,
		Name:            req.Name,
		Enabled:         true,
		BaseURL:         strings.TrimSpace(req.BaseURL),
		Credentials:     encrypted,
		Extra:           datatypes.JSON(req.Extra),
		TimeoutMS:       15000,
		MaxRetries:      2,
		RateLimitPerMin: 120,
		Remark:          req.Remark,
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if req.TimeoutMS != nil && *req.TimeoutMS > 0 {
		p.TimeoutMS = *req.TimeoutMS
	}
	if req.MaxRetries != nil && *req.MaxRetries >= 0 {
		p.MaxRetries = *req.MaxRetries
	}
	if req.RateLimitPerMin != nil && *req.RateLimitPerMin > 0 {
		p.RateLimitPerMin = *req.RateLimitPerMin
	}
	if req.SortOrder != nil {
		p.SortOrder = *req.SortOrder
	}

	if err := h.db.Create(&p).Error; err != nil {
		ServerError(c, "创建平台失败")
		return
	}
	h.auditLog(c, "platform.create", "platform", req.Name, gin.H{"code": req.Code})
	OK(c, h.platforms.ToMasked(&p))
}

func (h *Handlers) updatePlatform(c *gin.Context) {
	// 改平台可能改掉上游凭证，属敏感操作
	if !h.requireConfirm(c) {
		return
	}
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req platformUpdateRequest
	if !BindJSON(c, &req) {
		return
	}

	var p models.Platform
	if err := h.db.First(&p, id).Error; err != nil {
		NotFound(c, "平台不存在")
		return
	}

	// 部分更新：只有显式传了的字段才会被写入，没传的保持原值。
	updates := map[string]any{}
	if req.Name != nil {
		if name := strings.TrimSpace(*req.Name); name != "" {
			updates["name"] = name
		}
	}
	// Code 不允许改：改了会导致已配置的货源指向不存在的适配器
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.BaseURL != nil {
		updates["base_url"] = strings.TrimSpace(*req.BaseURL)
	}
	if req.Remark != nil {
		updates["remark"] = *req.Remark
	}
	if req.Extra != nil {
		// 必须显式转成 datatypes.JSON：GORM 的 Updates 传 json.RawMessage
		// 会写不进 JSONB 列（静默丢弃），导致扩展配置更新不生效。
		updates["extra"] = datatypes.JSON(req.Extra)
	}
	if req.TimeoutMS != nil && *req.TimeoutMS > 0 {
		updates["timeout_ms"] = *req.TimeoutMS
	}
	if req.MaxRetries != nil && *req.MaxRetries >= 0 {
		updates["max_retries"] = *req.MaxRetries
	}
	if req.RateLimitPerMin != nil && *req.RateLimitPerMin > 0 {
		updates["rate_limit_per_min"] = *req.RateLimitPerMin
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}

	// 凭证：传了才更新，留空表示保持原值（前端不回显密文）
	if len(req.Credentials) > 0 {
		hasValue := false
		for _, v := range req.Credentials {
			if strings.TrimSpace(v) != "" {
				hasValue = true
				break
			}
		}
		if hasValue {
			encrypted, err := h.platforms.EncryptCredentials(req.Credentials)
			if err != nil {
				ServerError(c, "凭证加密失败")
				return
			}
			updates["credentials"] = encrypted
		}
	}

	// 改之前留一份快照。Platform.Credentials 是 json:"-"，快照天然拿不到密文（好），
	// 所以额外用一个布尔标记表达「凭证被换过」这件事实。
	before := service.Snapshot(&p)
	if before == nil {
		before = map[string]any{}
	}
	before["credentials_changed"] = false

	if err := h.db.Model(&models.Platform{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		ServerError(c, "更新失败")
		return
	}

	after := map[string]any{}
	credentialsChanged := false
	if _, ok := updates["credentials"]; ok {
		credentialsChanged = true
	}
	if err := h.db.First(&p, id).Error; err != nil {
		ServerError(c, "读取失败")
		return
	}
	after = service.Snapshot(&p)
	if after == nil {
		after = map[string]any{}
	}
	after["credentials_changed"] = credentialsChanged

	h.auditChange(c, "platform.update", "platform", p.Name, before, after)
	OK(c, h.platforms.ToMasked(&p))
}

func (h *Handlers) deletePlatform(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}

	var p models.Platform
	if err := h.db.First(&p, id).Error; err != nil {
		NotFound(c, "平台不存在")
		return
	}

	// 有货源引用时不允许删除，避免商品货源悬空
	var supplyCount int64
	h.db.Model(&models.Supply{}).Where("platform_id = ?", id).Count(&supplyCount)
	if supplyCount > 0 {
		Conflict(c, "该平台已被 "+itoa(supplyCount)+" 条货源引用，请先删除这些货源")
		return
	}
	var orderCount int64
	h.db.Model(&models.Order{}).Where("platform_id = ?", id).Count(&orderCount)
	if orderCount > 0 {
		Conflict(c, "该平台已有 "+itoa(orderCount)+" 条订单记录，建议改为禁用")
		return
	}

	if err := h.db.Delete(&models.Platform{}, id).Error; err != nil {
		ServerError(c, "删除失败")
		return
	}
	h.auditLog(c, "platform.delete", "platform", p.Name, nil)
	OK(c, gin.H{"message": "已删除"})
}

func (h *Handlers) testPlatform(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	info, err := h.platforms.TestConnection(c.Request.Context(), id)
	if err != nil {
		h.auditLog(c, "platform.test_failed", "platform", itoa(int64(id)), gin.H{"error": err.Error()})
		Fail(c, 502, "PLATFORM_TEST_FAILED", "连接失败："+err.Error())
		return
	}
	h.auditLog(c, "platform.test", "platform", itoa(int64(id)), nil)
	OK(c, info)
}

// listPlatformGoods 拉取某平台的上游商品，供绑定货源时选品。
func (h *Handlers) listPlatformGoods(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	page, size := Pagination(c)
	res, err := h.goods.ListPlatformGoods(c.Request.Context(), id, c.Query("keyword"), page, size)
	if err != nil {
		Fail(c, 502, "UPSTREAM_ERROR", "拉取上游商品失败："+err.Error())
		return
	}
	OKPage(c, res.List, int64(res.Total), page, size)
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
