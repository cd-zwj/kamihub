package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunnerRejectsUnknownTask 未注册的任务名要明确报错，不能静默成功。
func TestRunnerRejectsUnknownTask(t *testing.T) {
	r := New(Deps{Tasks: []Task{{
		Name:     "known",
		Interval: time.Hour,
		Run:      func(ctx context.Context) (Result, error) { return Result{}, nil },
	}}})
	if _, err := r.RunNow(context.Background(), "nope"); err == nil {
		t.Fatal("未知任务应返回错误")
	}
}

// TestRunNowReturnsResult RunNow 要能拿到任务的返回值。
func TestRunNowReturnsResult(t *testing.T) {
	r := New(Deps{Tasks: []Task{{
		Name:     "counter",
		Interval: time.Hour,
		Run: func(ctx context.Context) (Result, error) {
			return Result{Affected: 7, Detail: "ok"}, nil
		},
	}}})
	res, err := r.RunNow(context.Background(), "counter")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.Affected != 7 {
		t.Errorf("Affected 期望 7，实际 %d", res.Affected)
	}
}

// TestPanicIsRecovered 任务 panic 不能拖死进程，要转成错误。
func TestPanicIsRecovered(t *testing.T) {
	r := New(Deps{Tasks: []Task{{
		Name:     "boom",
		Interval: time.Hour,
		Run: func(ctx context.Context) (Result, error) {
			panic("测试 panic")
		},
	}}})
	_, err := r.RunNow(context.Background(), "boom")
	if err == nil {
		t.Fatal("panic 应被 recover 成错误")
	}
	if !containsStr(err.Error(), "panic") {
		t.Errorf("错误信息应包含 panic：%v", err)
	}
}

// TestTimeoutCancelsContext 超时后 ctx 应被取消，任务能感知到。
func TestTimeoutCancelsContext(t *testing.T) {
	var sawCancel atomic.Bool
	r := New(Deps{Tasks: []Task{{
		Name:     "slow",
		Interval: time.Hour,
		Timeout:  50 * time.Millisecond,
		Run: func(ctx context.Context) (Result, error) {
			select {
			case <-ctx.Done():
				sawCancel.Store(true)
				return Result{}, ctx.Err()
			case <-time.After(2 * time.Second):
				return Result{}, nil
			}
		},
	}}})

	start := time.Now()
	_, err := r.RunNow(context.Background(), "slow")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("超时应返回错误")
	}
	if !sawCancel.Load() {
		t.Error("任务应感知到 ctx 被取消")
	}
	if elapsed > time.Second {
		t.Errorf("超时未生效，耗时 %v", elapsed)
	}
}

// TestReentrancyGuard 上一轮未结束时不应重入同一任务。
func TestReentrancyGuard(t *testing.T) {
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32

	task := Task{
		Name:     "reenter",
		Interval: 10 * time.Millisecond,
		Run: func(ctx context.Context) (Result, error) {
			cur := concurrent.Add(1)
			for {
				prev := maxConcurrent.Load()
				if cur <= prev || maxConcurrent.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(60 * time.Millisecond)
			concurrent.Add(-1)
			return Result{}, nil
		},
	}
	r := New(Deps{Tasks: []Task{task}})

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	// 直接驱动 loop，模拟 ticker 连续触发
	go r.loop(ctx, task)
	<-ctx.Done()
	time.Sleep(100 * time.Millisecond) // 等最后一批跑完

	if got := maxConcurrent.Load(); got > 1 {
		t.Fatalf("同一任务不应并发执行，最大并发 %d", got)
	}
}

// TestTaskNames 返回全部任务名。
func TestTaskNames(t *testing.T) {
	r := New(Deps{Tasks: []Task{
		{Name: "a", Run: func(context.Context) (Result, error) { return Result{}, nil }},
		{Name: "b", Run: func(context.Context) (Result, error) { return Result{}, nil }},
	}})
	names := r.TaskNames()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("任务名列表错误: %v", names)
	}
}

// TestErrorPropagates 任务返回的错误要原样传出。
func TestErrorPropagates(t *testing.T) {
	sentinel := errors.New("上游不可用")
	r := New(Deps{Tasks: []Task{{
		Name: "fail",
		Run:  func(context.Context) (Result, error) { return Result{}, sentinel },
	}}})
	_, err := r.RunNow(context.Background(), "fail")
	if !errors.Is(err, sentinel) {
		t.Fatalf("应返回原始错误，实际 %v", err)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
