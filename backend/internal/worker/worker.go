// Package worker 提供定时任务框架与全部后台任务。
//
// 设计要点（为什么不用 asynq 之类的队列）：
//
//	当前量级是「单实例 + 每天几千次定时拉取」，用 time.Ticker 足够，
//	零新依赖、零运维成本、调试直观。
//	代价是要求所有任务必须写成幂等的 —— 这样将来换 asynq 只是换调度器，
//	任务函数一行不用改。
//
// 三条硬约束：
//
//	1. 幂等：任何任务重复执行都不能产生副作用叠加。
//	2. 可观测：每次执行都写 worker_task_runs，后台能看到跑没跑、成没成。
//	3. 隔离：单任务 panic 不能拖死整个 worker；单任务卡住不能阻塞其他任务。
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/notify"
)

// Result 一次任务执行的结果。
type Result struct {
	// Affected 本次处理的记录数，用于判断「任务有没有真干活」
	Affected int64
	// Detail 可读的补充说明，写进 worker_task_runs.detail
	Detail string
}

// Task 一个定时任务。
type Task struct {
	// Name 任务标识，写入 worker_task_runs.task
	Name string
	// Interval 执行间隔
	Interval time.Duration
	// Timeout 单次执行超时。超时后 context 取消，任务应尽快返回。
	Timeout time.Duration
	// LockKey PG advisory lock 的键。0 表示不加锁。
	// 多实例部署时靠它保证同一时刻只有一个实例在跑同一个任务。
	LockKey int64
	// Run 任务主体。必须幂等。
	Run func(ctx context.Context) (Result, error)
}

// Deps worker 运行所需的依赖。
type Deps struct {
	DB     *gorm.DB
	Notify *notify.Notifier
	// Tasks 由调用方（main）构造后注入，避免 worker 包反向依赖 service 包。
	Tasks []Task
}

// Runner 任务调度器。
type Runner struct {
	db    *gorm.DB
	tasks []Task

	// running 记录正在执行的任务名，防止同一任务上一轮未结束时重入
	running sync.Map

	stopOnce sync.Once
}

// New 构造调度器。
func New(deps Deps) *Runner {
	return &Runner{db: deps.DB, tasks: deps.Tasks}
}

// Start 启动所有任务，阻塞直到 ctx 取消。
//
// 每个任务独立 goroutine：某个任务卡住不会影响其他任务。
func (r *Runner) Start(ctx context.Context) error {
	if len(r.tasks) == 0 {
		slog.Warn("worker 未配置任何任务，将立即退出")
		return nil
	}

	slog.Info("worker 启动", "tasks", len(r.tasks))
	for _, t := range r.tasks {
		slog.Info("任务已注册",
			"task", t.Name,
			"interval", t.Interval.String(),
			"timeout", t.Timeout.String())
	}

	var wg sync.WaitGroup
	for i := range r.tasks {
		t := r.tasks[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.loop(ctx, t)
		}()
	}

	<-ctx.Done()
	slog.Info("worker 收到退出信号，等待任务结束")
	wg.Wait()
	slog.Info("worker 已退出")
	return nil
}

// loop 单任务的主循环。
func (r *Runner) loop(ctx context.Context, t Task) {
	interval := t.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 启动后先等一个完整间隔再执行，避免和进程启动时的初始化抢资源
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOne(ctx, t)
		}
	}
}

// RunNow 立即执行一次指定任务（供后台手动触发 / 测试用）。
func (r *Runner) RunNow(ctx context.Context, name string) (Result, error) {
	for _, t := range r.tasks {
		if t.Name == name {
			return r.execute(ctx, t)
		}
	}
	return Result{}, fmt.Errorf("未找到任务: %s", name)
}

// TaskNames 返回全部任务名，供后台展示。
func (r *Runner) TaskNames() []string {
	out := make([]string, 0, len(r.tasks))
	for _, t := range r.tasks {
		out = append(out, t.Name)
	}
	return out
}

// runOne 带重入保护的执行。
func (r *Runner) runOne(ctx context.Context, t Task) {
	if _, loaded := r.running.LoadOrStore(t.Name, struct{}{}); loaded {
		slog.Warn("上一轮任务尚未结束，跳过本轮", "task", t.Name)
		return
	}
	defer r.running.Delete(t.Name)

	_, _ = r.execute(ctx, t)
}

// execute 执行一次任务：加锁 → 超时 → recover → 记录。
func (r *Runner) execute(ctx context.Context, t Task) (res Result, err error) {
	started := time.Now()

	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("任务 panic: %v", rec)
			slog.Error("任务 panic",
				"task", t.Name,
				"panic", fmt.Sprint(rec),
				"stack", string(debug.Stack()))
		}
		r.record(ctx, t, started, res, err)
	}()

	runCtx := ctx
	if t.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, t.Timeout)
		defer cancel()
	}

	if t.LockKey != 0 {
		res, err = r.withLock(runCtx, t)
		return res, err
	}
	res, err = t.Run(runCtx)
	return res, err
}

// errLockBusy 表示另一个实例正在跑同一个任务。
var errLockBusy = errors.New("任务已被其他实例持有")

// withLock 用 PG advisory lock 保证同一时刻只有一个实例执行该任务。
//
// 必须用 db.Connection 把连接钉住：advisory lock 是会话级的，
// 如果加锁和解锁走到连接池里两个不同连接，锁会泄漏。
func (r *Runner) withLock(ctx context.Context, t Task) (Result, error) {
	var res Result
	err := r.db.WithContext(ctx).Connection(func(tx *gorm.DB) error {
		var acquired bool
		if err := tx.Raw("SELECT pg_try_advisory_lock(?)", t.LockKey).Scan(&acquired).Error; err != nil {
			return fmt.Errorf("获取任务锁失败: %w", err)
		}
		if !acquired {
			return errLockBusy
		}
		defer func() {
			if uerr := tx.Exec("SELECT pg_advisory_unlock(?)", t.LockKey).Error; uerr != nil {
				slog.Warn("释放任务锁失败", "task", t.Name, "error", uerr.Error())
			}
		}()
		var runErr error
		res, runErr = t.Run(ctx)
		return runErr
	})
	if errors.Is(err, errLockBusy) {
		slog.Debug("任务被其他实例持有，跳过", "task", t.Name)
		return Result{}, nil
	}
	return res, err
}

// record 写执行记录。
func (r *Runner) record(ctx context.Context, t Task, started time.Time, res Result, err error) {
	finished := time.Now()
	duration := finished.Sub(started)

	ok := err == nil
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}

	if ok {
		slog.Info("任务完成",
			"task", t.Name,
			"affected", res.Affected,
			"duration_ms", duration.Milliseconds())
	} else {
		slog.Warn("任务失败",
			"task", t.Name,
			"error", errMsg,
			"duration_ms", duration.Milliseconds())
	}

	// 写记录本身失败不能影响任务结果，也不能 panic
	if r.db == nil {
		return
	}
	// 用独立的 context：即使任务超时导致 ctx 已取消，也要把记录写进去
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	row := taskRunRow(t.Name, started, finished, ok, res, errMsg, duration.Milliseconds())
	if werr := r.db.WithContext(writeCtx).Create(&row).Error; werr != nil {
		slog.Error("写入任务运行记录失败", "task", t.Name, "error", werr.Error())
	}
}
