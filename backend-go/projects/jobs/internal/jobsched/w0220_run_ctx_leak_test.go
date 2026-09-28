package jobsched

// w0220_run_ctx_leak_test.go — BUG-0220 回归：runOnce 每轮执行不得泄漏
// goroutine 与 context 节点。
//
// 已修复的缺陷（scheduler.go）：
//
//   - contextBoundToStop 每次调用新建 1 个 ctx + 1 个监听 goroutine
//     （select stopCh / ctx.Done），其 cancel 只在 stopCh 分支执行——任务
//     正常结束不释放，每轮泄漏 1 个 goroutine，仅 Scheduler 停机时退出；
//   - runOnce 里 WithTimeout 层覆盖 cancel 引用后，WithCancel 层的 cancel
//     永不调用，该层 ctx 节点作为停机根的 child 永久滞留（Timeout>0 即
//     触发，生产全部任务如此）。
//
// 长驻 jobs 进程每轮净增 1 goroutine + 1 ctx 节点（约每天 24 万轮），
// 数周后 OOM。
//
// 验证方式：沿用 gatewaycircuit/circuitaudit_wait_leak_test.go 的基线-终值
// 对比模式（runtime.NumGoroutine），用真实时钟跑 ≥50 轮短间隔任务
//（Timeout>0 强制走 WithTimeout 覆盖 cancel 的缺陷路径，取值远大于单轮
// 时长以免触发超时记账），收尾稳定后断言净增回到基线（±2 容忍噪声）。
// ctx 节点层的释放由两层 cancel 均被调用保证，无法直接观测，以 goroutine
// 断言 + 代码路径覆盖。

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerRunOnceDoesNotLeakGoroutinePerRun(t *testing.T) {
	// 真实时钟：泄漏要由调度器真实多轮运行暴露（生产形态即 SystemClock）。
	scheduler := NewScheduler(Options{StableSeed: "leak-test", Random: func() float64 { return 0.5 }})

	const minRuns = 50
	var runs atomic.Int64
	scheduler.Schedule(Spec{
		Name:     "leak-probe",
		Interval: 10 * time.Millisecond,
		Timeout:  30 * time.Second,
		Task: func(context.Context, TaskContext) (TaskResult, error) {
			runs.Add(1)
			return TaskResult{}, nil
		},
	})

	// 基线在调度器自身 goroutine（jobLoop/stuckWatchLoop）就位后取样；
	// 首轮已跑的轮次计入基线不影响增量断言。
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baseline := stableGoroutineCount(t)

	waitFor(t, 30*time.Second, func() bool { return runs.Load() >= minRuns })
	// 等最后一轮收尾（handler 已退出、记账完成）再计数。
	waitFor(t, 5*time.Second, func() bool {
		snapshot, ok := snapshotByName(scheduler, "leak-probe")
		return ok && !snapshot.Running
	})

	// 验收断言：≥50 轮后 goroutine 净增必须回到基线（±2 容忍噪声）。
	// 缺陷形态下每轮泄漏 1 个监听 goroutine，净增接近本轮数。
	after := stableGoroutineCount(t)
	if leaked := after - baseline; leaked > 2 || leaked < -2 {
		t.Fatalf("goroutine 净增 = %d, want |Δ| <= 2（baseline=%d after=%d）：每轮执行不得泄漏 goroutine（BUG-0220）",
			leaked, baseline, after)
	}
	t.Logf("BUG-0220 回归：runs=%d goroutine baseline=%d after=%d leaked=%d",
		runs.Load(), baseline, after, after-baseline)

	// Stop 后调度器自身 goroutine 退出，计数不得高于基线（无停机残留）。
	scheduler.Stop()
	if final := stableGoroutineCount(t); final > baseline+2 {
		t.Fatalf("Stop 后 goroutine 计数 = %d 超过基线+2 = %d（BUG-0220）", final, baseline+2)
	}
}

// stableGoroutineCount 重试取样直到连续两次读数一致，避免把进行中的收尾
// 误判为泄漏。
func stableGoroutineCount(t *testing.T) int {
	t.Helper()
	previous := runtime.NumGoroutine()
	for attempt := 0; attempt < 100; attempt++ {
		time.Sleep(10 * time.Millisecond)
		current := runtime.NumGoroutine()
		if current == previous {
			return current
		}
		previous = current
	}
	return previous
}
