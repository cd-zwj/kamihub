package api

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/notify"
	"kamihub/internal/worker"
)

// 本文件提供迭代 1（运维地基）的后台接口：
//   - 定时任务运行记录与手动触发
//   - 对账差异查看与标记
//   - 站内信 / 运维告警查看
//   - 通知渠道状态与测试发送
//
// 全部挂在 admin 组（AdminOnly），因为这些能力能触发上游调用、
// 能看到资金差异，不应让 operator 触碰。

// ---------------------------------------------------------------- 定时任务

// workerTaskView 任务静态信息
type workerTaskView struct {
	Name    string                `json:"name"`
	LastRun *models.WorkerTaskRun `json:"last_run"`
}

// listWorkerTasks 列出全部定时任务及最近一次执行结果。
//
// 即使当前进程是 api 模式（本进程不跑任务），也会从库里读最近记录 ——
// 生产上 worker 在另一个容器里跑，运维需要能在一个地方看到全部状态。
func (h *Handlers) listWorkerTasks(c *gin.Context) {
	names := []string{
		worker.TaskStockSync,
		worker.TaskBalanceSync,
		worker.TaskExpirePayment,
		worker.TaskStaleOrder,
		worker.TaskReconcile,
	}
	if h.worker != nil {
		names = h.worker.TaskNames()
	}

	out := make([]workerTaskView, 0, len(names))
	for _, name := range names {
		var last models.WorkerTaskRun
		_ = h.db.WithContext(c.Request.Context()).
			Where("task = ?", name).
			Order("id DESC").First(&last).Error
		v := workerTaskView{Name: name}
		if last.ID != 0 {
			v.LastRun = &last
		}
		out = append(out, v)
	}
	OK(c, gin.H{
		"list": out,
		// worker_active 表示当前进程是否能真正执行任务（api 模式下为 false）
		"worker_active": h.worker != nil,
	})
}

// listWorkerRuns 分页查任务执行记录。
func (h *Handlers) listWorkerRuns(c *gin.Context) {
	page, size := Pagination(c)
	task := c.Query("task")

	q := h.db.WithContext(c.Request.Context()).Model(&models.WorkerTaskRun{})
	if task != "" {
		q = q.Where("task = ?", task)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	var rows []models.WorkerTaskRun
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).
		Find(&rows).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

// runWorkerTask 手动触发一次任务（运维排障用）。
//
// 只在本进程持有 worker 时可用。生产 api 容器里 worker 为 nil，
// 会返回明确提示，避免运维误以为「点了没反应」。
func (h *Handlers) runWorkerTask(c *gin.Context) {
	name := c.Param("task")
	if h.worker == nil {
		BadRequest(c, "当前进程未启用定时任务（APP_MODE 不是 all/worker）")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	res, err := h.worker.RunNow(ctx, name)
	if err != nil {
		ServerError(c, "任务执行失败："+err.Error())
		return
	}
	h.auditLog(c, "worker.run", "worker_task", name, gin.H{"affected": res.Affected})
	OK(c, gin.H{"task": name, "affected": res.Affected, "detail": res.Detail})
}

// ---------------------------------------------------------------- 对账

// listReconcileDiffs 分页查对账差异。
func (h *Handlers) listReconcileDiffs(c *gin.Context) {
	page, size := Pagination(c)
	kind := c.Query("kind")

	var resolved *bool
	switch c.Query("resolved") {
	case "true", "1":
		v := true
		resolved = &v
	case "false", "0":
		v := false
		resolved = &v
	}

	rows, total, err := h.reconcile.ListDiffs(c.Request.Context(), page, size, resolved, kind)
	if err != nil {
		ServerError(c, "查询失败")
		return
	}

	// 顺带返回未处理汇总，前端可以做成顶部提示条
	unresolvedCount, unresolvedAmount, _ := h.reconcile.SumUnresolved(c.Request.Context())

	OK(c, gin.H{
		"list":  rows,
		"total": total,
		"page":  page,
		"size":  size,
		"unresolved": gin.H{
			"count":  unresolvedCount,
			"amount": unresolvedAmount.StringFixed(2),
		},
	})
}

// resolveReconcileDiff 标记一条差异已处理。
func (h *Handlers) resolveReconcileDiff(c *gin.Context) {
	id, okID := uintParam(c, "id")
	if !okID {
		return
	}
	var req struct {
		Remark string `json:"remark"`
	}
	_ = c.ShouldBindJSON(&req)

	user := CurrentUser(c)
	var uid uint
	if user != nil {
		uid = user.ID
	}
	if err := h.reconcile.ResolveDiff(c.Request.Context(), id, uid, req.Remark); err != nil {
		ServerError(c, "标记失败")
		return
	}
	h.auditLog(c, "reconcile.resolve", "reconcile_diff", strconv.FormatUint(uint64(id), 10),
		gin.H{"remark": req.Remark})
	OK(c, gin.H{"id": id})
}

// runReconcile 手动触发一次对账。
func (h *Handlers) runReconcile(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	res, err := h.reconcile.Run(ctx, "")
	if err != nil {
		ServerError(c, "对账失败："+err.Error())
		return
	}
	h.auditLog(c, "reconcile.run", "reconcile", res.BizDate,
		gin.H{"total": res.Total, "by_kind": res.ByKind})
	OK(c, gin.H{
		"biz_date":    res.BizDate,
		"total":       res.Total,
		"by_kind":     res.ByKind,
		"duration_ms": res.DurationMS,
	})
}

// ---------------------------------------------------------------- 通知

// listNotifications 分页查运维告警（buyer_id 为空的那批）。
//
// 买家站内信不在这里返回：那是买家自己的东西，
// 后台不该把全站买家的通知混进来。
func (h *Handlers) listNotifications(c *gin.Context) {
	page, size := Pagination(c)
	level := c.Query("level")
	event := c.Query("event")

	q := h.db.WithContext(c.Request.Context()).Model(&models.Notification{}).
		Where("buyer_id IS NULL")
	if level != "" {
		q = q.Where("level = ?", level)
	}
	if event != "" {
		q = q.Where("event = ?", event)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}
	var rows []models.Notification
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).
		Find(&rows).Error; err != nil {
		ServerError(c, "查询失败")
		return
	}

	var unread int64
	_ = h.db.WithContext(c.Request.Context()).Model(&models.Notification{}).
		Where("buyer_id IS NULL AND read = ?", false).Count(&unread).Error

	OK(c, gin.H{"list": rows, "total": total, "page": page, "size": size, "unread": unread})
}

// readNotification 标记单条已读。
func (h *Handlers) readNotification(c *gin.Context) {
	id, okID := uintParam(c, "id")
	if !okID {
		return
	}
	now := time.Now()
	if err := h.db.WithContext(c.Request.Context()).Model(&models.Notification{}).
		Where("id = ?", id).
		Updates(map[string]any{"read": true, "read_at": &now}).Error; err != nil {
		ServerError(c, "标记失败")
		return
	}
	OK(c, gin.H{"id": id})
}

// readAllNotifications 全部标记已读。
func (h *Handlers) readAllNotifications(c *gin.Context) {
	now := time.Now()
	if err := h.db.WithContext(c.Request.Context()).Model(&models.Notification{}).
		Where("buyer_id IS NULL AND read = ?", false).
		Updates(map[string]any{"read": true, "read_at": &now}).Error; err != nil {
		ServerError(c, "标记失败")
		return
	}
	OK(c, gin.H{"done": true})
}

// notifyStatus 通知渠道状态。
func (h *Handlers) notifyStatus(c *gin.Context) {
	channels := []string{}
	if h.notifier != nil {
		channels = h.notifier.Channels()
	}
	OK(c, gin.H{
		"enabled":  h.cfg.Notify.Enabled,
		"channels": channels,
		"quiet": gin.H{
			"start": h.cfg.Notify.QuietStart,
			"end":   h.cfg.Notify.QuietEnd,
		},
		"aggregate_window_sec": int(h.cfg.Notify.AggregateWindow.Seconds()),
	})
}

// testNotify 发送一条测试通知，验证渠道配置是否正确。
//
// 运维配好 Telegram/webhook 后第一件事就是点这个 ——
// 否则等到真出事才发现 token 填错，就晚了。
func (h *Handlers) testNotify(c *gin.Context) {
	if h.notifier == nil {
		BadRequest(c, "通知器未初始化")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	h.notifier.Alert(ctx, notify.Message{
		Level:   notify.LevelInfo,
		Event:   "notify.test",
		Title:   "kamihub 测试通知",
		Content: "如果你看到这条消息，说明通知渠道配置正确。",
		Fields:  map[string]any{"来源": "后台手动测试"},
	})
	OK(c, gin.H{"sent": true, "channels": h.notifier.Channels()})
}
