package opsjobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeTestTaskRepo struct {
	mu             sync.Mutex
	maintenanceLog []ManualTestMaintenanceInput
	startResult    ManualTestMaintenanceResult
	sweepResult    ManualTestMaintenanceResult
	running        map[string]*ManualTestTaskRecord
	completed      []string
	failed         map[string]string
	canceled       []string
	messages       map[string]string
}

func newFakeTestTaskRepo() *fakeTestTaskRepo {
	return &fakeTestTaskRepo{
		running:  map[string]*ManualTestTaskRecord{},
		failed:   map[string]string{},
		messages: map[string]string{},
	}
}

func (f *fakeTestTaskRepo) Maintenance(_ context.Context, input ManualTestMaintenanceInput) (ManualTestMaintenanceResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maintenanceLog = append(f.maintenanceLog, input)
	if input.Action == "start" {
		if input.StaleRunningMS == nil {
			return ManualTestMaintenanceResult{}, errors.New("start 必须带 staleRunningMs")
		}
		return f.startResult, nil
	}
	return f.sweepResult, nil
}

func (f *fakeTestTaskRepo) MarkRunning(_ context.Context, taskID string) (*ManualTestTaskRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	task, found := f.running[taskID]
	if !found {
		return nil, nil
	}
	delete(f.running, taskID)
	return task, nil
}

func (f *fakeTestTaskRepo) Complete(_ context.Context, taskID string, _ ManualTestTaskExecutorResult, _ *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, taskID)
	return nil
}

func (f *fakeTestTaskRepo) Fail(_ context.Context, taskID, message string, _ *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed[taskID] = message
	return nil
}

func (f *fakeTestTaskRepo) Cancel(_ context.Context, taskID, message string, _ *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, taskID+":"+message)
	return nil
}

func (f *fakeTestTaskRepo) UpdateMessage(_ context.Context, taskID, message string, _ *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages[taskID] = message
	return nil
}

func testQueueConfig() ManualTestQueueConfig {
	return ManualTestQueueConfig{
		RefillMaxBatchSize:   10,
		QueuedMaxWaitMS:      600_000,
		RunningStaleMS:       300_000,
		QueuedSweepBatchSize: 50,
		SweepInterval:        50 * time.Millisecond,
		Concurrency:          2,
		NowMS:                func() int64 { return 1_000 },
	}
}

func manualTestTask(id string) *ManualTestTaskRecord {
	startedAt := "2030-01-01T00:00:00.000Z"
	return &ManualTestTaskRecord{
		ID:          id,
		AccountID:   "acc-1",
		StartedAt:   &startedAt,
		Diagnostics: "limited",
	}
}

// 正常路径：入队 → markRunning → executor → complete。
func TestManualTestQueueRunCompletesTask(t *testing.T) {
	repo := newFakeTestTaskRepo()
	repo.running["task-1"] = manualTestTask("task-1")
	var executed []string
	queue, err := NewManualTestQueue(repo, func(_ context.Context, task ManualTestTaskRecord, report ProgressReporter) (ManualTestTaskExecutorResult, error) {
		executed = append(executed, task.ID)
		report("真实请求测试中：本次诊断最长等待 30s")
		return ManualTestTaskExecutorResult{Success: true}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	queue.EnqueueLocal("task-1")
	queue.drain(context.Background())
	if len(executed) != 1 || executed[0] != "task-1" {
		t.Fatalf("执行序列 = %v", executed)
	}
	if len(repo.completed) != 1 || repo.completed[0] != "task-1" {
		t.Fatalf("completed = %v", repo.completed)
	}
	if repo.messages["task-1"] != "真实请求测试中：本次诊断最长等待 30s" {
		t.Fatalf("进度消息 = %q", repo.messages["task-1"])
	}
}

// kill-restart 硬门禁：start 维护回收中断任务并入队续跑。
func TestManualTestQueueStartResumesInterruptedTasks(t *testing.T) {
	repo := newFakeTestTaskRepo()
	// 中断时任务仍处于 DB running（陈旧），start 维护回收并返回待续跑列表。
	repo.startResult = ManualTestMaintenanceResult{TaskIDs: []string{"task-stale-1", "task-stale-2"}}
	repo.running["task-stale-1"] = manualTestTask("task-stale-1")
	repo.running["task-stale-2"] = manualTestTask("task-stale-2")
	var executed []string
	queue, err := NewManualTestQueue(repo, func(_ context.Context, task ManualTestTaskRecord, _ ProgressReporter) (ManualTestTaskExecutorResult, error) {
		executed = append(executed, task.ID)
		return ManualTestTaskExecutorResult{Success: true}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := queue.Start(context.Background())
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if len(resumed) != 2 {
		t.Fatalf("应恢复 2 个中断任务: %v", resumed)
	}
	if len(repo.maintenanceLog) == 0 || repo.maintenanceLog[0].Action != "start" {
		t.Fatalf("启动应先执行 start 维护: %+v", repo.maintenanceLog)
	}
	queue.drain(context.Background())
	if len(executed) != 2 {
		t.Fatalf("重启后应续跑全部中断任务: %v", executed)
	}
	queue.Stop(context.Background())
}

// sweep：queued 等待超限自动失败收口 + refill 补充。
func TestManualTestQueueSweepExpiresQueuedAndRefills(t *testing.T) {
	repo := newFakeTestTaskRepo()
	repo.sweepResult = ManualTestMaintenanceResult{
		TaskIDs:              []string{"task-new"},
		ExpiredQueuedTaskIDs: []string{"task-expired"},
	}
	repo.running["task-new"] = manualTestTask("task-new")
	queue, err := NewManualTestQueue(repo, func(_ context.Context, task ManualTestTaskRecord, _ ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{Success: true}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	// task-expired 已在本地 pending 等待，sweep 应移除并中止其本地执行。
	queue.EnqueueLocal("task-expired")
	taskIDs, err := queue.runMaintenance(context.Background(), "sweep")
	if err != nil {
		t.Fatal(err)
	}
	if len(taskIDs) != 1 || taskIDs[0] != "task-new" {
		t.Fatalf("refill = %v", taskIDs)
	}
	for _, input := range repo.maintenanceLog {
		if input.Action == "sweep" && input.StaleRunningMS != nil {
			t.Fatal("sweep 不得携带 staleRunningMs")
		}
	}
	// refill 语义：sweep 返回的待运行任务重新入队后执行。
	for _, taskID := range taskIDs {
		queue.EnqueueLocal(taskID)
	}
	queue.drain(context.Background())
	if len(repo.completed) != 1 {
		t.Fatalf("新任务应被执行: %v", repo.completed)
	}
}

// 执行器失败 → fail 带错误消息。
func TestManualTestQueueExecutorFailureFailsTask(t *testing.T) {
	repo := newFakeTestTaskRepo()
	repo.running["task-err"] = manualTestTask("task-err")
	queue, err := NewManualTestQueue(repo, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{}, errors.New("upstream exploded")
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	queue.EnqueueLocal("task-err")
	queue.drain(context.Background())
	if repo.failed["task-err"] != "upstream exploded" {
		t.Fatalf("fail message = %q", repo.failed["task-err"])
	}
}

// 任务已被取消（markRunning 返回缺失）→ 静默跳过。
func TestManualTestQueueMissingTaskSkipped(t *testing.T) {
	repo := newFakeTestTaskRepo()
	queue, err := NewManualTestQueue(repo, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		t.Fatal("缺失任务不应执行")
		return ManualTestTaskExecutorResult{}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	queue.EnqueueLocal("task-gone")
	queue.drain(context.Background())
}

// DispatchAccountTestTask：接受/拒绝语义与失败文案逐字节对齐 Node。
func TestDispatchAccountTestTaskSemantics(t *testing.T) {
	repo := newFakeTestTaskRepo()
	queue, err := NewManualTestQueue(repo, func(context.Context, ManualTestTaskRecord, ProgressReporter) (ManualTestTaskExecutorResult, error) {
		return ManualTestTaskExecutorResult{Success: true}, nil
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := queue.DispatchAccountTestTask(context.Background(), ""); err != nil || accepted {
		t.Fatalf("空 ID 应拒绝: accepted=%v err=%v", accepted, err)
	}
	accepted, err := queue.DispatchAccountTestTask(context.Background(), "  task-1  ")
	if err != nil || !accepted {
		t.Fatalf("trim 后应接受: %v %v", accepted, err)
	}
	// 同 key 二次入队被去重 → 拒绝路径（Node enqueue 返回 false）→ fail 收口。
	accepted, err = queue.DispatchAccountTestTask(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if accepted {
		t.Fatal("重复 key 应返回 false")
	}
	if repo.failed["task-1"] != "后台 worker 暂不可用，账号测试任务未能投递" {
		t.Fatalf("失败文案不符: %q", repo.failed["task-1"])
	}
}

// CancelLocal：pending 直接移除；无本地执行时持久化取消。
func TestManualTestQueueCancelLocal(t *testing.T) {
	repo := newFakeTestTaskRepo()
	repo.running["task-1"] = manualTestTask("task-1")
	queue, err := NewManualTestQueue(repo, func(ctx context.Context, task ManualTestTaskRecord, _ ProgressReporter) (ManualTestTaskExecutorResult, error) {
		<-ctx.Done()
		return ManualTestTaskExecutorResult{}, ctx.Err()
	}, testQueueConfig())
	if err != nil {
		t.Fatal(err)
	}
	queue.EnqueueLocal("task-1")
	done := make(chan struct{})
	go func() {
		queue.drain(context.Background())
		close(done)
	}()
	// 等 executor 启动后取消。
	time.Sleep(20 * time.Millisecond)
	queue.CancelLocal(context.Background(), "task-1", "")
	<-done
	found := false
	for _, entry := range repo.canceled {
		if entry == "task-1:已停止测试" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应持久化取消: %v", repo.canceled)
	}
}

func TestDiagnosticProgressMessages(t *testing.T) {
	cases := []struct {
		timeoutMS int64
		mode      string
		want      string
	}{
		{30_000, "", "真实请求测试中：本次诊断最长等待 30s"},
		{1_500, "", "真实请求测试中：本次诊断最长等待 2s"},
		{500, "images_json", "图像生成测试中：本次诊断最长等待 1s"},
	}
	for _, tc := range cases {
		if got := DiagnosticAttemptProgressMessage(tc.timeoutMS, tc.mode); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}

func TestNormalizeTaskID(t *testing.T) {
	if id, ok := NormalizeTaskID("  a b  "); !ok || id != "a b" {
		t.Fatalf("got %q %v", id, ok)
	}
	if _, ok := NormalizeTaskID("   "); ok {
		t.Fatal("空白应无效")
	}
}

var _ = fmt.Sprintf
