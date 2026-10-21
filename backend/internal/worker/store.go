package worker

import (
	"time"

	"kamihub/internal/models"
)

// taskRunRow 构造一条任务运行记录。
//
// 单独抽出来是为了让 worker.go 不直接依赖 models 的字段布局，
// 也便于测试。
func taskRunRow(task string, started, finished time.Time, ok bool, res Result, errMsg string, durationMS int64) models.WorkerTaskRun {
	f := finished
	return models.WorkerTaskRun{
		Task:       task,
		StartedAt:  started,
		FinishedAt: &f,
		OK:         ok,
		Affected:   res.Affected,
		ErrorMsg:   errMsg,
		Detail:     res.Detail,
		DurationMS: durationMS,
	}
}

// 任务名常量。后台与前端共用这套标识。
const (
	TaskStockSync      = "stock_sync"
	TaskBalanceSync    = "balance_sync"
	TaskExpirePayment  = "expire_payment"
	TaskStaleOrder     = "stale_order"
	TaskReconcile      = "reconcile"
)

// 各任务的 advisory lock key。
// 取固定大整数，避免与业务无关的锁冲突。
const (
	lockKeyStockSync     int64 = 910001
	lockKeyBalanceSync   int64 = 910002
	lockKeyExpirePayment int64 = 910003
	lockKeyStaleOrder    int64 = 910004
	lockKeyReconcile     int64 = 910005
)
