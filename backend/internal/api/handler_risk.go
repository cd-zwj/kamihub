package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

// ---------------------------------------------------------------- 风控规则

// listRiskRules 列出全部规则（含禁用的 —— 运营要能看到「有哪些规则可开」）
func (h *Handlers) listRiskRules(c *gin.Context) {
	var rows []models.RiskRule
	if err := h.db.WithContext(c.Request.Context()).
		Order("priority ASC, id ASC").Find(&rows).Error; err != nil {
		ServerError(c, "查询风控规则失败")
		return
	}
	OK(c, gin.H{"list": rows, "scenes": models.AllScenes})
}

type updateRiskRuleRequest struct {
	Enabled  *bool           `json:"enabled"`
	Level    *string         `json:"level"`
	Priority *int            `json:"priority"`
	Config   json.RawMessage `json:"config"`
}

// updateRiskRule 改一条规则：开关 / 级别 / 优先级 / 参数。
//
// 改完立刻 Invalidate 缓存 —— 这就是「热改不重启」的落点。
func (h *Handlers) updateRiskRule(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req updateRiskRuleRequest
	if !BindJSON(c, &req) {
		return
	}

	var rule models.RiskRule
	if err := h.db.WithContext(c.Request.Context()).First(&rule, id).Error; err != nil {
		NotFound(c, "规则不存在")
		return
	}

	updates := map[string]any{}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.Level != nil {
		switch *req.Level {
		case models.LevelPass, models.LevelReview, models.LevelReject:
			updates["level"] = *req.Level
		default:
			BadRequest(c, "level 只能是 pass / review / reject")
			return
		}
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if len(req.Config) > 0 {
		// 必须是合法 JSON 对象，否则规则评估时会静默取零值，
		// 表现成「改了阈值但行为没变」，排查起来很费劲。
		var probe map[string]any
		if err := json.Unmarshal(req.Config, &probe); err != nil {
			BadRequest(c, "config 必须是合法的 JSON 对象")
			return
		}
		updates["config"] = []byte(req.Config)
	}

	if len(updates) > 0 {
		updates["updated_at"] = time.Now()
		if err := h.db.WithContext(c.Request.Context()).Model(&models.RiskRule{}).
			Where("id = ?", id).Updates(updates).Error; err != nil {
			ServerError(c, "更新规则失败")
			return
		}
		if h.risk != nil {
			h.risk.Invalidate()
		}
	}

	// 记差量而不是只记「新值」：规则调松/调紧直接影响误拦率，
	// 事后要能看出「原来阈值是多少」。config 是 JSON 列，快照后逐字段比较。
	// 记差量而不是只记「新值」：规则调松/调紧直接影响误拦率，
	// 事后要能看出「原来阈值是多少」。config 是 JSON 列，快照后逐字段比较。
	var fresh models.RiskRule
	if err := h.db.WithContext(c.Request.Context()).First(&fresh, id).Error; err == nil {
		h.auditChange(c, "risk.rule_update", "risk_rule", rule.Code,
			service.Snapshot(&rule), service.Snapshot(&fresh))
		OK(c, fresh)
		return
	}
	h.auditChange(c, "risk.rule_update", "risk_rule", rule.Code,
		service.Snapshot(&rule), service.Snapshot(updates))
	OK(c, gin.H{"message": "已更新"})
}

// ---------------------------------------------------------------- 命中记录

// listRiskHits 命中记录列表。
//
// 支持按主体精确查（`subject=buyer:12` / `ip:1.2.3.4`）——
// 这是买家申诉时唯一能回答「你凭什么拦我」的入口。
func (h *Handlers) listRiskHits(c *gin.Context) {
	page, size := Pagination(c)
	ctx := c.Request.Context()

	q := h.db.WithContext(ctx).Model(&models.RiskHit{})
	if sub := strings.TrimSpace(c.Query("subject")); sub != "" {
		q = q.Where("subject = ?", sub)
	}
	if scene := strings.TrimSpace(c.Query("scene")); scene != "" {
		q = q.Where("scene = ?", scene)
	}
	if lv := strings.TrimSpace(c.Query("level")); lv != "" {
		q = q.Where("level = ?", lv)
	}
	// pending=1 只看未处理，用于「人工复核队列」
	if c.Query("pending") == "1" {
		q = q.Where("reviewed = ?", false)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	var rows []models.RiskHit
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).
		Find(&rows).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

type reviewRiskHitRequest struct {
	// Result allow / block
	Result string `json:"result" binding:"required"`
}

// reviewRiskHit 人工复核：标记这条命中已处理，并记下结论。
func (h *Handlers) reviewRiskHit(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req reviewRiskHitRequest
	if !BindJSON(c, &req) {
		return
	}
	if req.Result != "allow" && req.Result != "block" {
		BadRequest(c, "result 只能是 allow 或 block")
		return
	}

	me := CurrentUser(c)
	now := time.Now()
	res := h.db.WithContext(c.Request.Context()).Model(&models.RiskHit{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"reviewed":      true,
			"reviewed_by":   me.ID,
			"reviewed_at":   now,
			"review_result": req.Result,
		})
	if res.Error != nil {
		ServerError(c, "更新失败")
		return
	}
	if res.RowsAffected == 0 {
		NotFound(c, "记录不存在")
		return
	}
	h.auditLog(c, "risk.hit_review", "risk_hit", strconv.FormatUint(uint64(id), 10), gin.H{"result": req.Result})
	OK(c, gin.H{"message": "已处理"})
}

// ---------------------------------------------------------------- 黑名单

func (h *Handlers) listBlacklist(c *gin.Context) {
	page, size := Pagination(c)
	q := h.db.WithContext(c.Request.Context()).Model(&models.Blacklist{})
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		q = q.Where("value ILIKE ? OR reason ILIKE ?", "%"+kw+"%", "%"+kw+"%")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	var rows []models.Blacklist
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).
		Find(&rows).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

type createBlacklistRequest struct {
	Kind   string `json:"kind" binding:"required"`
	Value  string `json:"value" binding:"required"`
	Reason string `json:"reason"`
	// ExpiresInHours 有效小时数；0 或不传 = 永久。
	// 强烈建议填：永久黑名单积累多了会误伤（共享 IP / 运营商 NAT）。
	ExpiresInHours int `json:"expires_in_hours"`
}

func (h *Handlers) createBlacklist(c *gin.Context) {
	var req createBlacklistRequest
	if !BindJSON(c, &req) {
		return
	}
	req.Kind = strings.TrimSpace(req.Kind)
	req.Value = strings.TrimSpace(req.Value)
	if req.Value == "" {
		BadRequest(c, "黑名单值不能为空")
		return
	}
	valid := false
	for _, k := range models.AllBlacklistKinds {
		if k == req.Kind {
			valid = true
			break
		}
	}
	if !valid {
		BadRequest(c, "kind 只能是 "+strings.Join(models.AllBlacklistKinds, " / "))
		return
	}

	row := models.Blacklist{
		Kind:   req.Kind,
		Value:  req.Value,
		Reason: strings.TrimSpace(req.Reason),
	}
	if req.ExpiresInHours > 0 {
		exp := time.Now().Add(time.Duration(req.ExpiresInHours) * time.Hour)
		row.ExpiresAt = &exp
	}

	if err := h.db.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			Conflict(c, "该条目已在黑名单中")
			return
		}
		ServerError(c, "添加失败")
		return
	}
	h.auditLog(c, "risk.blacklist_add", "blacklist", row.Kind+":"+row.Value,
		gin.H{"reason": row.Reason, "expires_in_hours": req.ExpiresInHours})
	OK(c, row)
}

func (h *Handlers) deleteBlacklist(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var row models.Blacklist
	if err := h.db.WithContext(c.Request.Context()).First(&row, id).Error; err != nil {
		NotFound(c, "条目不存在")
		return
	}
	if err := h.db.WithContext(c.Request.Context()).Delete(&models.Blacklist{}, id).Error; err != nil {
		ServerError(c, "删除失败")
		return
	}
	h.auditLog(c, "risk.blacklist_delete", "blacklist", row.Kind+":"+row.Value, nil)
	OK(c, gin.H{"message": "已删除"})
}
