package accounttest

// w12h 补充 arms：队列主循环 Run/refill、sweep 循环（含注入 Random 的抖动
// 分支与维护错误收口）、构造校验错误、并发上限、running 任务清理、取消
// 分支与空错误消息兜底。

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// w12hQueueHooks 在 fake 仓储上叠加可控的错误注入。
type w12hQueueHooks struct {
	*fakeTestTaskRepo
	maintenanceErr error
	markRunningErr error
}

func (h *w12hQueueHooks) Maintenance(ctx context.Context, input ManualTestMaintenanceInput) (ManualTestMaintenanceResult, error) {
	if h.maintenanceErr != nil {
		return ManualTestMaintenanceResult{}, h.maintenanceErr
	}
	return h.fakeTestTaskRepo.Maintenance(ctx, input)
}

func (h *w12hQueueHooks) MarkRunning(ctx context.Context, taskID string) (*ManualTestTaskRecord, error) {
	if h.markRunningErr != nil {
		return nil, h.markRunningErr
	}
	return h.fakeTestTaskRepo.MarkRunning(ctx, taskID)
}

func TestW12HNewManualTestQueueValidation(t *testing.T) {
	repo := newFakeTestTaskRepo()
	executor := func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{}, nil
	}
	if _, err := NewManualTestQueue(nil, executor, testQueueConfig()); err == nil || !strings.Contains(err.Error(), "依赖未初始化") {
		t.Fatalf("缺 repo 必须报错: %v", err)
	}
	if _, err := NewManualTestQueue(repo, nil, testQueueConfig()); err == nil || !strings.Contains(err.Error(), "依赖未初始化") {
		t.Fatalf("缺 executor 必须报错: %v", err)
	}
	config := testQueueConfig()
	config.NowMS = nil
	if _, err := NewManualTestQueue(repo, executor, config); err == nil || !strings.Contains(err.Error(), "NowMS") {
		t.Fatalf("缺时钟必须报错: %v", err)
	}
	config = testQueueConfig()
	config.RefillMaxBatchSize = 0
	if _, err := NewManualTestQueue(repo, executor, config); err == nil || !strings.Contains(err.Error(), "正整数") {
		t.Fatalf("批处理必须为正: %v", err)
	}
	config = testQueueConfig()
	config.Concurrency = 0
	if _, err := NewManualTestQueue(repo, executor, config); err == nil || !strings.Contains(err.Error(), "正整数") {
		t.Fatalf("并发必须为正: %v", err)
	}
	config = testQueueConfig()
	config.QueuedMaxWaitMS = 0
	if _, err := NewManualTestQueue(repo, executor, config); err == nil || !strings.Contains(err.Error(), "超时配置") {
		t.Fatalf("超时必须为正: %v", err)
	}
	config = testQueueConfig()
	config.RunningStaleMS = 0
	if _, err := NewManualTestQueue(repo, executor, config); err == nil || !strings.Contains(err.Error(), "超时配置") {
		t.Fatalf("stale 超时必须为正: %v", err)
	}
	// SweepInterval 未配置时默认 2s。
	config = testQueueConfig()
	config.SweepInterval = 0
	queue, err := NewManualTestQueue(repo, executor, config)
	if err != nil {
		t.Fatal(err)
	}
	if queue.cfg.SweepInterval != 2*time.Second {
		t.Fatalf("默认 sweep 间隔 = %s", queue.cfg.SweepInterval)
	}
}

func TestW12HStartMaintenanceErrorSurfaces(t *testing.T) {
	hooks := &w12hQueueHooks{fakeTestTaskRepo: newFakeTestTaskRepo(), maintenanceErr: errors.New("w12h maintenance failure")}
	queue, err := NewManualTestQueue(hooks, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "w12h maintenance failure") {
		t.Fatalf("启动维护失败必须上抛: %v", err)
	}
	stopCtx, stopCancel := context.WithCancel(context.Background())
	stopCancel()
	queue.Stop(stopCtx)
}

func TestW12HStopIsIdempotent(t *testing.T) {
	queue, err := NewManualTestQueue(newFakeTestTaskRepo(), func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	// 未启动 sweep 循环时 sweepDone 不会关闭，Stop 必须携带可取消 ctx。
	stopCtx, stopCancel := context.WithCancel(context.Background())
	stopCancel()
	queue.Stop(stopCtx)
	queue.Stop(stopCtx)
	if !queue.Stopped() {
		t.Fatal("停止后应报告 stopped")
	}
	// 停止后 drain 直接返回。
	queue.drain(context.Background())
}

func TestW12HQueueRunLoopDrainsRefillsAndStops(t *testing.T) {
	repo := newFakeTestTaskRepo()
	repo.running["w12h-run-1"] = manualTestTask("w12h-run-1")
	// Run 每轮 drain 后执行 sweep 维护；首轮 sweep 补充一个新任务。
	repo.sweepResult = ManualTestMaintenanceResult{TaskIDs: []string{"w12h-run-2"}}
	repo.running["w12h-run-2"] = manualTestTask("w12h-run-2")

	var mu sync.Mutex
	executed := []string{}
	blockUntilExecuted := make(chan struct{}, 1)
	queue, err := NewManualTestQueue(repo, func(_ context.Context, task ManualTestTaskRecord, _ ProgressReporter) (ManualTestTaskExecutorResult, error) {
		mu.Lock()
		executed = append(executed, task.ID)
		count := len(executed)
		mu.Unlock()
		if count == 2 {
			select {
			case blockUntilExecuted <- struct{}{}:
			default:
			}
		}
		return ManualTestTaskExecutorResult{Success: true}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Start 启动 sweep 循环（Stop 依赖 sweepDone 关闭），再跑主循环。
	if _, err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	queue.EnqueueLocal("w12h-run-1")
	runErr := make(chan error, 1)
	go func() { runErr <- queue.Run(ctx) }()

	select {
	case <-blockUntilExecuted:
	case <-time.After(2 * time.Second):
		t.Fatalf("队列未执行两个任务: %v", executed)
	}
	mu.Lock()
	ran := append([]string(nil), executed...)
	repo.mu.Lock()
	repo.sweepResult = ManualTestMaintenanceResult{}
	repo.mu.Unlock()
	mu.Unlock()
	if len(ran) != 2 {
		t.Fatalf("应执行初始+refill 任务: %v", ran)
	}

	// Stop 后 Run 返回 nil（stopped 收口），sweep 循环退出。
	queue.Stop(context.Background())
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Stop 收口必须返回 nil: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在 Stop 后退出")
	}
}

func TestW12HQueueRunReturnsCtxError(t *testing.T) {
	hooks := &w12hQueueHooks{fakeTestTaskRepo: newFakeTestTaskRepo(), maintenanceErr: errors.New("w12h sweep failure")}
	queue, err := NewManualTestQueue(hooks, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- queue.Run(ctx) }()
	cancel()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后 Run 必须返回 ctx.Err(): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未响应取消")
	}
}

func TestW12HSweepLoopTicksWithInjectedRandom(t *testing.T) {
	repo := newFakeTestTaskRepo()
	ticks := make(chan ManualTestMaintenanceInput, 4)
	queue, err := NewManualTestQueue(repo, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{}, nil
	}, ManualTestQueueConfig{
		RefillMaxBatchSize: 4, QueuedMaxWaitMS: 60_000, RunningStaleMS: 60_000,
		QueuedSweepBatchSize: 4, SweepInterval: 20 * time.Millisecond, Concurrency: 1,
		NowMS: func() int64 { return 1_000 },
		Random: func(intervalMS int64) int64 {
			return 1
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Start 启动 sweep 循环；注入 Random 走确定性抖动分支。
	if _, err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 记录 sweep 维护是否发生（循环 tick 后 runMaintenance("sweep")）。
	go func() {
		for range time.Tick(5 * time.Millisecond) {
			repo.mu.Lock()
			count := len(repo.maintenanceLog)
			repo.mu.Unlock()
			if count > 1 {
				select {
				case ticks <- ManualTestMaintenanceInput{}:
				default:
				}
				return
			}
		}
	}()
	select {
	case <-ticks:
	case <-time.After(2 * time.Second):
		t.Fatal("sweep 循环未按注入间隔触发")
	}
	queue.Stop(context.Background())
}

func TestW12HRunMaintenanceCancelsExpiredRunningTask(t *testing.T) {
	repo := newFakeTestTaskRepo()
	repo.sweepResult = ManualTestMaintenanceResult{ExpiredQueuedTaskIDs: []string{"w12h-running"}}
	queue, err := NewManualTestQueue(repo, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	canceled := false
	queue.mu.Lock()
	queue.running["w12h-running"] = func() { canceled = true }
	queue.mu.Unlock()
	if _, err := queue.runMaintenance(context.Background(), "sweep"); err != nil {
		t.Fatal(err)
	}
	if !canceled {
		t.Fatal("expired 且在跑的任务必须被本地中止")
	}

	// running 任务的重复入队被拒绝（幂等成功）。
	if queue.EnqueueLocal("w12h-running") {
		t.Fatal("running 任务的 EnqueueLocal 必须返回 false")
	}
	// 空白 ID 的 CancelLocal 直接返回，不触发持久化。
	queue.CancelLocal(context.Background(), "   ", "")
	if len(repo.canceled) != 0 {
		t.Fatalf("空白 ID 不得触发取消写入: %v", repo.canceled)
	}
	// 非本地任务的取消走持久化，空消息使用默认文案。
	queue.CancelLocal(context.Background(), "w12h-remote", "")
	found := false
	for _, entry := range repo.canceled {
		if entry == "w12h-remote:已停止测试" {
			found = true
		}
	}
	if !found {
		t.Fatalf("远程任务必须持久化默认取消文案: %v", repo.canceled)
	}
}

func TestW12HDrainHonorsConcurrencyCapAndParentCancel(t *testing.T) {
	repo := newFakeTestTaskRepo()
	repo.running["w12h-cap-1"] = manualTestTask("w12h-cap-1")
	repo.running["w12h-cap-2"] = manualTestTask("w12h-cap-2")
	release := make(chan struct{})
	started := make(chan string, 2)
	config := testQueueConfig()
	config.Concurrency = 1
	queue, err := NewManualTestQueue(repo, func(ctx context.Context, task ManualTestTaskRecord, _ ProgressReporter) (ManualTestTaskExecutorResult, error) {
		started <- task.ID
		<-release
		return ManualTestTaskExecutorResult{Success: true}, nil
	}, config)
	if err != nil {
		t.Fatal(err)
	}
	queue.EnqueueLocal("w12h-cap-1")
	queue.EnqueueLocal("w12h-cap-2")
	drained := make(chan struct{})
	go func() { queue.drain(context.Background()); close(drained) }()
	first := <-started
	if first != "w12h-cap-1" && first != "w12h-cap-2" {
		t.Fatalf("意外任务: %s", first)
	}
	// 并发上限 1：等 drain 进入「1 个在跑 + 1 个 pending」状态（只有触发
	// 上限回填分支后才会出现），再放行执行器。
	deadline := time.After(2 * time.Second)
	for {
		queue.mu.Lock()
		pending, running := len(queue.pending), len(queue.running)
		queue.mu.Unlock()
		if pending == 1 && running == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("未观察到并发上限回填状态: pending=%d running=%d", pending, running)
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(release)
	<-drained

	// 父 ctx 在执行后被取消时 drain 立即收口。
	repo.running["w12h-cap-3"] = manualTestTask("w12h-cap-3")
	ctx, cancel := context.WithCancel(context.Background())
	queue, err = NewManualTestQueue(repo, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		cancel()
		return ManualTestTaskExecutorResult{Success: true}, nil
	}, config)
	if err != nil {
		t.Fatal(err)
	}
	queue.EnqueueLocal("w12h-cap-3")
	queue.drain(ctx)
}

func TestW12HExecuteErrorArms(t *testing.T) {
	// MarkRunning 失败 → 静默返回。
	hooks := &w12hQueueHooks{fakeTestTaskRepo: newFakeTestTaskRepo(), markRunningErr: errors.New("w12h mark failure")}
	queue, err := NewManualTestQueue(hooks, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	queue.execute(context.Background(), "w12h-mark-err")

	// 执行器错误消息为空 → 使用默认文案。
	repo := newFakeTestTaskRepo()
	repo.running["w12h-empty-err"] = manualTestTask("w12h-empty-err")
	queue, err = NewManualTestQueue(repo, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{}, errors.New("")
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	queue.EnqueueLocal("w12h-empty-err")
	queue.drain(context.Background())
	if repo.failed["w12h-empty-err"] != "账号测试任务执行失败" {
		t.Fatalf("空错误消息兜底 = %q", repo.failed["w12h-empty-err"])
	}

	// 执行器返回 Canceled → 持久化取消。
	repo2 := newFakeTestTaskRepo()
	repo2.running["w12h-canceled"] = manualTestTask("w12h-canceled")
	queue2, err := NewManualTestQueue(repo2, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{Canceled: true}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	queue2.EnqueueLocal("w12h-canceled")
	queue2.drain(context.Background())
	found := false
	for _, entry := range repo2.canceled {
		if strings.HasPrefix(entry, "w12h-canceled:") {
			found = true
		}
	}
	if found != true {
		t.Fatalf("Canceled 结果必须持久化取消: %v", repo2.canceled)
	}
}

func TestW12HJitterArms(t *testing.T) {
	// NaN/Inf 随机样本必须收敛为 0 再参与偏移（2s 间隔 → 窗口 1000ms）。
	if got := PassiveScheduleOffsetMS(2_000, func() float64 { return math.NaN() }); got != -1_000 {
		t.Fatalf("NaN 随机源偏移 = %d, want -1000", got)
	}
	if got := PassiveScheduleOffsetMS(2_000, func() float64 { return math.Inf(1) }); got != -1_000 {
		t.Fatalf("Inf 随机源偏移 = %d, want -1000", got)
	}
	// 确定性 0 样本下 1ms 间隔的延迟被钳制为严格正 1ms（max64 左支 1>0）。
	if got := PassiveScheduleDelayMS(1, nil); got != 1 {
		t.Fatalf("PassiveScheduleDelayMS(1) = %d, want 1", got)
	}
	if got := PassiveScheduleDelayMS(2, nil); got != 1 {
		t.Fatalf("PassiveScheduleDelayMS(2) = %d, want 1", got)
	}
}

// w12hSweepFailRepo 让第 N 次 sweep 维护失败，驱动 sweep 循环的错误收口。
type w12hSweepFailRepo struct {
	*fakeTestTaskRepo
	failAfter int32
	calls     int32
}

func (s *w12hSweepFailRepo) Maintenance(ctx context.Context, input ManualTestMaintenanceInput) (ManualTestMaintenanceResult, error) {
	if input.Action == "sweep" {
		if atomic.AddInt32(&s.calls, 1) > s.failAfter {
			return ManualTestMaintenanceResult{}, errors.New("w12h sweep failure")
		}
	}
	return s.fakeTestTaskRepo.Maintenance(ctx, input)
}

func TestW12HSweepLoopSurvivesMaintenanceError(t *testing.T) {
	repo := &w12hSweepFailRepo{fakeTestTaskRepo: newFakeTestTaskRepo(), failAfter: 1}
	queue, err := NewManualTestQueue(repo, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{}, nil
	}, ManualTestQueueConfig{
		RefillMaxBatchSize: 4, QueuedMaxWaitMS: 60_000, RunningStaleMS: 60_000,
		QueuedSweepBatchSize: 4, SweepInterval: 20 * time.Millisecond, Concurrency: 1,
		NowMS: func() int64 { return 1_000 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 等到失败的 sweep 维护发生后停止，循环必须存活。
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&repo.calls) < 3 {
		select {
		case <-deadline:
			t.Fatalf("sweep 维护未按预期执行 3 次: %d", atomic.LoadInt32(&repo.calls))
		case <-time.After(5 * time.Millisecond):
		}
	}
	queue.Stop(context.Background())
}
