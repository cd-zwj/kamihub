package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/notify"
	"kamihub/internal/service"
)

// Services worker 需要调用的业务服务。
//
// 用结构体注入而不是让 worker 反向依赖 main 的装配过程：
// worker 只知道「我需要这几个能力」，不知道它们怎么被构造出来。
type Services struct {
	Platforms *service.PlatformService
	Goods     *service.GoodsService
	Orders    *service.OrderService
	Payment   *service.PaymentService
	Reconcile *service.ReconcileService
	// Agent 销售档位重算（迭代 3）。为 nil 表示未启用阶梯专价。
	Agent *service.AgentService
	// Cards 卡密池。用来清理过期的库存预占 —— 买家点了「去支付」
	// 又关掉页面时，那几张卡会一直以 held 状态锁着，不清理就等于
	// 「池子里有货但永远卖不出去」。为 nil 时跳过这一步。
	Cards *service.CardService
}

// BuildTasks 构造全部定时任务。
func BuildTasks(db *gorm.DB, cfg *config.Config, svc Services, n *notify.Notifier) []Task {
	timeout := cfg.Worker.TaskTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	return []Task{
		{
			Name:     TaskStockSync,
			Interval: cfg.Worker.StockInterval,
			Timeout:  timeout,
			LockKey:  lockKeyStockSync,
			Run:      stockSyncTask(db, cfg, svc, n),
		}, {
			Name:     TaskBalanceSync,
			Interval: cfg.Worker.BalanceInterval,
			Timeout:  timeout,
			LockKey:  lockKeyBalanceSync,
			Run:      balanceSyncTask(db, cfg, svc, n),
		},
		{
			Name:     TaskExpirePayment,
			Interval: cfg.Worker.PaymentInterval,
			Timeout:  timeout,
			LockKey:  lockKeyExpirePayment,
			Run:      expirePaymentTask(svc, n),
		},
		{
			Name:     TaskStaleOrder,
			Interval: cfg.Worker.OrderInterval,
			Timeout:  timeout,
			LockKey:  lockKeyStaleOrder,
			Run:      staleOrderTask(db, cfg, svc, n),
		},
		{
			Name:     TaskReconcile,
			Interval: cfg.Worker.ReconcileInterval,
			Timeout:  timeout,
			LockKey:  lockKeyReconcile,
			Run:      reconcileTask(svc, n),
		},
		{
			// 档位重算：每天一次即可（档位是日更语义，实时算没有意义）。
			// 设计文档 §5.1：计算频率「每日 03:00」，这里用 24h 间隔。
			Name:     TaskTierRecalc,
			Interval: cfg.Worker.TierRecalcInterval,
			Timeout:  timeout,
			LockKey:  lockKeyTierRecalc,
			Run:      tierRecalcTask(svc, n),
		},
	}
}

// ---------------------------------------------------------------- 任务 6：档位重算

// tierRecalcTask 重算所有销售的专价档位。
//
// 幂等：结果只取决于「近 30 天 delivered 订单」这一个输入，
// 同一天跑多次结果一致。
func tierRecalcTask(svc Services, n *notify.Notifier) func(context.Context) (Result, error) {
	return func(ctx context.Context) (Result, error) {
		if svc.Agent == nil {
			return Result{Detail: "阶梯专价未装配，跳过"}, nil
		}
		started := time.Now()
		res, err := svc.Agent.RecalcTiers(ctx)
		if err != nil {
			return Result{}, err
		}
		return Result{
			Affected: int64(res.Upgraded + res.Downgraded),
			Detail: fmt.Sprintf("扫描 %d 个销售：升级 %d，降级 %d，宽限 %d，防刷拦截 %d，失败 %d（%s）",
				res.Scanned, res.Upgraded, res.Downgraded, res.InGrace, res.Held, res.Failed,
				time.Since(started).Round(time.Millisecond)),
		}, nil
	}
}

// ---------------------------------------------------------------- 任务 1：库存同步

// stockSyncTask 刷新所有启用货源的库存与成本缓存。
//
// 幂等：纯覆盖写，重复执行结果一致。
// 为什么必须做：库存是缓存值，不刷新就会超卖 ——
// 买家付了钱、上游却没货，是最伤口碑的失败。
func stockSyncTask(db *gorm.DB, cfg *config.Config, svc Services, n *notify.Notifier) func(context.Context) (Result, error) {
	return func(ctx context.Context) (Result, error) {
		var supplies []models.Supply
		if err := db.WithContext(ctx).
			Preload("Platform").
			Where("enabled = ?", true).
			Order("id ASC").
			Find(&supplies).Error; err != nil {
			return Result{}, fmt.Errorf("加载货源失败: %w", err)
		}

		var res Result
		var okCount, failCount, outOfStock int

		// 先清理过期的卡密预占：这一步必须在同步库存之前，
		// 否则池子里「看起来有货」的那几张其实还锁在别人的待支付单上。
		if svc.Cards != nil {
			if released, rerr := svc.Cards.SweepExpiredHolds(ctx); rerr != nil {
				slog.Warn("清理过期卡密预占失败", "error", rerr.Error())
			} else if released > 0 {
				slog.Info("清理过期卡密预占", "released", released)
			}
		}

		for i := range supplies {
			sup := &supplies[i]
			if sup.Platform == nil || !sup.Platform.Enabled {
				continue
			}
			// 上下文取消（任务超时/进程退出）时立刻停手，不要继续打上游
			if err := ctx.Err(); err != nil {
				return res, err
			}

			prevStock := sup.Stock
			_, err := svc.Goods.SyncSupplyStock(ctx, sup.ID)
			if err != nil {
				failCount++
				slog.Warn("货源库存同步失败",
					"supply_id", sup.ID,
					"platform", sup.Platform.Name,
					"goods_no", sup.PlatformGoodsNo,
					"error", err.Error())
				continue
			}
			okCount++
			res.Affected++

			// 读回新库存，用于「从有货变无货」的告警判断
			var fresh models.Supply
			if e := db.WithContext(ctx).Select("id", "stock").First(&fresh, sup.ID).Error; e == nil {
				if prevStock > 0 && fresh.Stock <= 0 {
					outOfStock++
					alertSupplyOutOfStock(ctx, n, sup, prevStock)
				}
			}
		}

		res.Detail = fmt.Sprintf("货源 %d 条：成功 %d / 失败 %d / 断货 %d",
			len(supplies), okCount, failCount, outOfStock)

		// 失败过多时升级为告警（单条失败只记日志，避免刷屏）
		if failCount > 0 && okCount == 0 && len(supplies) > 0 {
			n.Alert(ctx, notify.Message{
				Level:   notify.LevelError,
				Event:   notify.EventPlatformDown,
				Title:   "所有货源库存同步失败",
				Content: fmt.Sprintf("本轮 %d 条货源全部同步失败，上游可能整体不可用", failCount),
				Fields: map[string]any{
					"失败数":  failCount,
					"货源总数": len(supplies),
				},
			})
			return res, fmt.Errorf("%d 条货源全部同步失败", failCount)
		}

		return res, nil
	}
}

func alertSupplyOutOfStock(ctx context.Context, n *notify.Notifier, sup *models.Supply, prevStock int) {
	platName := ""
	if sup.Platform != nil {
		platName = sup.Platform.Name
	}
	n.Alert(ctx, notify.Message{
		Level: notify.LevelWarn,
		Event: notify.EventSupplyOutOfStock,
		Title: "货源断货",
		Content: fmt.Sprintf("%s 上的商品 %s 库存已归零",
			platName, sup.PlatformGoodsNo),
		Fields: map[string]any{
			"货源ID": sup.ID,
			"平台":   platName,
			"上游商品": sup.PlatformGoodsNo,
			"原库存":  prevStock,
		},
		Link: "/admin/goods",
	})
}

// ---------------------------------------------------------------- 任务 2：余额同步

// balanceSyncTask 刷新各上游平台余额，并在低于阈值时告警。
//
// 这是 16688 场景的命门：采购扣的是「运营钱包」，
// 余额为 0 时所有订单直接失败。没有这个任务，
// 你是「卖到没钱了才知道」。
func balanceSyncTask(db *gorm.DB, cfg *config.Config, svc Services, n *notify.Notifier) func(context.Context) (Result, error) {
	threshold := decimal.NewFromFloat(cfg.Worker.BalanceLow)

	return func(ctx context.Context) (Result, error) {
		var platforms []models.Platform
		if err := db.WithContext(ctx).
			Where("enabled = ?", true).
			Order("id ASC").
			Find(&platforms).Error; err != nil {
			return Result{}, fmt.Errorf("加载平台失败: %w", err)
		}

		var res Result
		var okCount, failCount int

		for i := range platforms {
			p := &platforms[i]
			if err := ctx.Err(); err != nil {
				return res, err
			}

			info, err := svc.Platforms.TestConnection(ctx, p.ID)
			if err != nil {
				failCount++
				slog.Warn("平台余额同步失败",
					"platform", p.Name, "error", err.Error())
				continue
			}
			okCount++
			res.Affected++

			// 低于阈值告警
			if threshold.IsPositive() && info.Balance.LessThan(threshold) {
				n.Alert(ctx, notify.Message{
					Level: notify.LevelError,
					Event: notify.EventBalanceLow,
					Title: "上游余额不足",
					Content: fmt.Sprintf("%s 可用余额 %s，低于预警线 %s，请尽快充值",
						p.Name, info.Balance.StringFixed(2), threshold.StringFixed(2)),
					Fields: map[string]any{
						"平台":   p.Name,
						"当前余额": info.Balance.StringFixed(2),
						"预警线":  threshold.StringFixed(2),
						"币种":   info.Currency,
					},
					Link: "/admin/platforms",
				})
			}
		}

		res.Detail = fmt.Sprintf("平台 %d 个：成功 %d / 失败 %d", len(platforms), okCount, failCount)

		if failCount > 0 && okCount == 0 && len(platforms) > 0 {
			n.Alert(ctx, notify.Message{
				Level:   notify.LevelError,
				Event:   notify.EventPlatformDown,
				Title:   "所有上游平台连接失败",
				Content: fmt.Sprintf("本轮 %d 个平台全部无法连接，采购链路可能已中断", failCount),
				Link:    "/admin/platforms",
			})
			return res, fmt.Errorf("%d 个平台全部连接失败", failCount)
		}

		return res, nil
	}
}

// ---------------------------------------------------------------- 任务 3：超时支付单

// expirePaymentTask 关闭超时未支付的支付单。
//
// 复用已有 PaymentService.ExpireStale —— 这段逻辑早就写好了，
// 只是一直没人调用（这也是迭代 0 实测发现的问题）。
func expirePaymentTask(svc Services, n *notify.Notifier) func(context.Context) (Result, error) {
	return func(ctx context.Context) (Result, error) {
		count, err := svc.Payment.ExpireStale(ctx)
		if err != nil {
			return Result{}, fmt.Errorf("关闭超时支付单失败: %w", err)
		}
		return Result{
			Affected: count,
			Detail:   fmt.Sprintf("关闭超时支付单 %d 笔", count),
		}, nil
	}
}

// ---------------------------------------------------------------- 任务 4：卡单补偿

// staleOrderTask 对卡在「采购中」的订单重新查单。
//
// 铁律：**只查单，绝不换源重下**。
// 上游单号已经存在说明款可能已扣，换一家买就是重复扣款。
// kamihub 原有的两阶段采购已经保证了这一点，这里只是把「人工点重新查单」
// 变成自动补偿。
func staleOrderTask(db *gorm.DB, cfg *config.Config, svc Services, n *notify.Notifier) func(context.Context) (Result, error) {
	staleMinutes := cfg.Worker.StaleMinutes
	if staleMinutes <= 0 {
		staleMinutes = 10
	}

	return func(ctx context.Context) (Result, error) {
		cutoff := time.Now().Add(-time.Duration(staleMinutes) * time.Minute)

		var orders []models.Order
		if err := db.WithContext(ctx).
			Where("status = ? AND platform_trade_no <> '' AND updated_at < ?",
				models.OrderStatusPurchasing, cutoff).
			Order("id ASC").
			Limit(100).
			Find(&orders).Error; err != nil {
			return Result{}, fmt.Errorf("加载卡单失败: %w", err)
		}

		var res Result
		var recovered, stillStale int

		for i := range orders {
			o := &orders[i]
			if err := ctx.Err(); err != nil {
				return res, err
			}

			updated, err := svc.Orders.RetryQuery(ctx, o.ID)
			if err != nil {
				slog.Warn("卡单补偿查单失败",
					"order_no", o.OrderNo, "trade_no", o.PlatformTradeNo, "error", err.Error())
				stillStale++
				continue
			}
			if updated.Status == models.OrderStatusDelivered {
				recovered++
				res.Affected++
				continue
			}
			stillStale++
		}

		res.Detail = fmt.Sprintf("卡单 %d 笔：已补发 %d / 仍待确认 %d",
			len(orders), recovered, stillStale)

		// 仍有卡单堆积时告警（每轮最多报一次，靠聚合窗口去重）
		if stillStale > 0 {
			n.Alert(ctx, notify.Message{
				Level: notify.LevelWarn,
				Event: notify.EventOrderStale,
				Title: "存在未确认发货的订单",
				Content: fmt.Sprintf("%d 笔订单超过 %d 分钟仍处于采购中，需人工关注",
					stillStale, staleMinutes),
				Fields: map[string]any{
					"卡单数":  stillStale,
					"阈值分钟": staleMinutes,
				},
				Link: "/admin/orders?status=purchasing",
			})
		}

		return res, nil
	}
}

// ---------------------------------------------------------------- 任务 5：对账

// reconcileTask 执行一次对账检测。
//
// 只写 reconcile_diffs，不改任何业务表。
func reconcileTask(svc Services, n *notify.Notifier) func(context.Context) (Result, error) {
	return func(ctx context.Context) (Result, error) {
		res, err := svc.Reconcile.Run(ctx, "")
		if err != nil {
			return Result{}, fmt.Errorf("对账失败: %w", err)
		}

		out := Result{
			Affected: res.Total,
			Detail:   fmt.Sprintf("对账 %s：发现差异 %d 条（%v）", res.BizDate, res.Total, res.ByKind),
		}

		if res.Total > 0 {
			n.Alert(ctx, notify.Message{
				Level:   notify.LevelWarn,
				Event:   notify.EventReconcileDiff,
				Title:   "对账发现差异",
				Content: fmt.Sprintf("%s 对账发现 %d 条差异，请到后台核对", res.BizDate, res.Total),
				Fields: map[string]any{
					"差异总数": res.Total,
					"分类":   fmt.Sprint(res.ByKind),
				},
				Link: "/admin/reconcile",
			})
		}

		return out, nil
	}
}
