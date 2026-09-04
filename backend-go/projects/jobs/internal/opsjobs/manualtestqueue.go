package opsjobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 手动账号测试队列，逐语义对齐 Node
// modules/accounts/account-test-task-queue.service.ts 的 worker 侧队列语义：
//   - start 维护回收中断任务（kill-restart 恢复硬门禁）：DB 侧 stale cutoff、
//     原子 queued claim 与 started_at 围栏保证并发启动安全；
//   - sweep 定时维护：queued 等待超限自动失败收口，并补充可运行任务；
//   - 每 key 去重、并发上限、成功/耗尽后 refill。
//
// 真实上游测试执行属于 gateway 域，jobs 侧经 TaskExecutor port 注入。

// ManualTestMaintenanceInput 对齐 account_test_task_maintenance 请求参数。
type ManualTestMaintenanceInput struct {
	Action         string // start | sweep
	MaxQueuedMS    int64
	StaleRunningMS *int64 // 仅 start 传入
	SweepLimit     int
	RefillLimit    int
}

// ManualTestMaintenanceResult 对齐 DB service 返回结构。
type ManualTestMaintenanceResult struct {
	TaskIDs              []string `json:"taskIds"`
	CanceledTaskIDs      []string `json:"canceledTaskIds"`
	ExpiredQueuedTaskIDs []string `json:"expiredQueuedTaskIds"`
}

// ManualTestTaskRecord 是 mark_running 返回的任务窄投影。
type ManualTestTaskRecord struct {
	ID                           string  `json:"id"`
	AccountID                    string  `json:"account_id"`
	Message                      string  `json:"message,omitempty"`
	Model                        string  `json:"model,omitempty"`
	TestEndpointMode             string  `json:"test_endpoint_mode,omitempty"`
	Diagnostics                  string  `json:"diagnostics,omitempty"`
	RequestSystemAccountID       string  `json:"request_system_account_id,omitempty"`
	RequestRole                  string  `json:"request_role,omitempty"`
	RequestSystemAccountFilterID string  `json:"request_system_account_filter_id,omitempty"`
	StartedAt                    *string `json:"started_at,omitempty"`
	HasDraftAccount              bool    `json:"has_draft_account"`
}

// ManualTestTaskExecutorResult 是执行器的窄结果。
type ManualTestTaskExecutorResult struct {
	Success  bool
	Message  string
	Canceled bool
}

// ManualTestTaskRepo 是测试任务持久化 port。
type ManualTestTaskRepo interface {
	Maintenance(ctx context.Context, input ManualTestMaintenanceInput) (ManualTestMaintenanceResult, error)
	MarkRunning(ctx context.Context, taskID string) (*ManualTestTaskRecord, error)
	Complete(ctx context.Context, taskID string, result ManualTestTaskExecutorResult, expectedStartedAt *string) error
	Fail(ctx context.Context, taskID string, message string, expectedStartedAt *string) error
	Cancel(ctx context.Context, taskID string, message string, expectedStartedAt *string) error
	UpdateMessage(ctx context.Context, taskID string, message string, expectedStartedAt *string) error
}

// ManualTestExecutor 执行单条测试任务；任务已被取消（running 标记缺失）时
// 返回 found=false。
type ManualTestExecutor func(ctx context.Context, task ManualTestTaskRecord, report ProgressReporter) (ManualTestTaskExecutorResult, error)

// ProgressReporter 更新任务状态消息（expectedStartedAt 围栏内）。
type ProgressReporter func(message string)

// ManualTestQueueConfig 对齐 Node runtime 配置项。
type ManualTestQueueConfig struct {
	RefillMaxBatchSize   int
	QueuedMaxWaitMS      int64
	RunningStaleMS       int64
	QueuedSweepBatchSize int
	SweepInterval        time.Duration // 默认 2s + 被动抖动
	Concurrency          int
	NowMS                func() int64
	Random               func(int64) int64 // 被动抖动随机源；nil = 确定性 +1ms
}

// ManualTestQueue 是可独立运行的手动测试队列。
type ManualTestQueue struct {
	repo     ManualTestTaskRepo
	executor ManualTestExecutor
	cfg      ManualTestQueueConfig
	nowMS    func() int64

	mu        sync.Mutex
	pending   map[string]struct{}
	running   map[string]context.CancelFunc
	stopped   bool
	wake      chan struct{}
	sweepStop chan struct{}
	sweepDone chan struct{}
}

func NewManualTestQueue(repo ManualTestTaskRepo, executor ManualTestExecutor, cfg ManualTestQueueConfig) (*ManualTestQueue, error) {
	if repo == nil || executor == nil {
		return nil, errors.New("手动测试队列依赖未初始化")
	}
	if cfg.NowMS == nil {
		return nil, errors.New("手动测试队列必须注入 NowMS 时钟")
	}
	if cfg.RefillMaxBatchSize < 1 || cfg.QueuedSweepBatchSize < 1 || cfg.Concurrency < 1 {
		return nil, errors.New("手动测试队列批处理与并发配置必须是正整数")
	}
	if cfg.QueuedMaxWaitMS < 1 || cfg.RunningStaleMS < 1 {
		return nil, errors.New("手动测试队列超时配置必须是正数")
	}
	if cfg.SweepInterval == 0 {
		cfg.SweepInterval = 2 * time.Second
	}
	return &ManualTestQueue{
		repo:      repo,
		executor:  executor,
		cfg:       cfg,
		nowMS:     cfg.NowMS,
		pending:   map[string]struct{}{},
		running:   map[string]context.CancelFunc{},
		wake:      make(chan struct{}, 1),
		sweepStop: make(chan struct{}),
		sweepDone: make(chan struct{}),
	}, nil
}

// NormalizeTaskID 对齐 normalizedString：trim 后非空才有值。
func NormalizeTaskID(taskID string) (string, bool) {
	normalized := strings.TrimSpace(taskID)
	return normalized, normalized != ""
}

// Start 执行启动维护：回收中断任务并入队续跑（kill-restart 恢复路径），
// 随后启动 sweep 定时维护。返回本轮恢复的任务 ID 列表。
func (q *ManualTestQueue) Start(ctx context.Context) ([]string, error) {
	resumed, err := q.runMaintenance(ctx, "start")
	if err != nil {
		return nil, err
	}
	for _, taskID := range resumed {
		q.EnqueueLocal(taskID)
	}
	q.startSweepLoop()
	q.kick()
	return resumed, nil
}

// Stop 停止 sweep 循环并等待在跑任务收尾。
func (q *ManualTestQueue) Stop(ctx context.Context) {
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return
	}
	q.stopped = true
	close(q.sweepStop)
	q.kick()
	q.mu.Unlock()
	select {
	case <-q.sweepDone:
	case <-ctx.Done():
	}
}

func (q *ManualTestQueue) startSweepLoop() {
	go func() {
		defer close(q.sweepDone)
		for {
			delay := PassiveScheduleDelayMS(q.cfg.SweepInterval.Milliseconds(), nil)
			if q.cfg.Random != nil {
				delay = PassiveScheduleDelayMS(q.cfg.SweepInterval.Milliseconds(), nil)
				_ = delay
				delay = max64(1, q.cfg.SweepInterval.Milliseconds()+q.cfg.Random(q.cfg.SweepInterval.Milliseconds()))
			}
			timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
			select {
			case <-q.sweepStop:
				timer.Stop()
				return
			case <-timer.C:
			}
			sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 30*time.Second)
			if _, err := q.runMaintenance(sweepCtx, "sweep"); err != nil {
				cancel()
				continue
			}
			cancel()
		}
	}()
}

// EnqueueLocal 本地入队（去重）。返回是否新入队。
func (q *ManualTestQueue) EnqueueLocal(taskID string) bool {
	normalized, ok := NormalizeTaskID(taskID)
	if !ok {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return false
	}
	if _, running := q.running[normalized]; running {
		return false
	}
	if _, exists := q.pending[normalized]; exists {
		return false
	}
	q.pending[normalized] = struct{}{}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

// CancelLocal 取消本地队列中的任务：移除 pending 或中止在跑任务。
func (q *ManualTestQueue) CancelLocal(ctx context.Context, taskID string, cancelMessage string) {
	normalized, ok := NormalizeTaskID(taskID)
	if !ok {
		return
	}
	q.mu.Lock()
	delete(q.pending, normalized)
	cancelRunning, running := q.running[normalized]
	q.mu.Unlock()
	if running {
		cancelRunning()
		return
	}
	if cancelMessage == "" {
		cancelMessage = "已停止测试"
	}
	_ = q.repo.Cancel(ctx, normalized, cancelMessage, nil)
}

func (q *ManualTestQueue) kick() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Run 运行队列主循环直到 ctx 或 Stop。每次 drain 之间按 refill 补充任务。
func (q *ManualTestQueue) Run(ctx context.Context) error {
	for {
		q.drain(ctx)
		q.refill(ctx)
		q.mu.Lock()
		stopped := q.stopped
		q.mu.Unlock()
		if stopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-q.wake:
		}
	}
}

func (q *ManualTestQueue) refill(ctx context.Context) {
	taskIDs, err := q.runMaintenance(ctx, "sweep")
	if err != nil {
		return
	}
	for _, taskID := range taskIDs {
		q.EnqueueLocal(taskID)
	}
}

func (q *ManualTestQueue) runMaintenance(ctx context.Context, action string) ([]string, error) {
	input := ManualTestMaintenanceInput{
		Action:      action,
		MaxQueuedMS: q.cfg.QueuedMaxWaitMS,
		SweepLimit:  q.cfg.QueuedSweepBatchSize,
		RefillLimit: q.cfg.RefillMaxBatchSize,
	}
	if action == "start" {
		staleRunningMS := q.cfg.RunningStaleMS
		input.StaleRunningMS = &staleRunningMS
	}
	result, err := q.repo.Maintenance(ctx, input)
	if err != nil {
		return nil, err
	}
	// expiredQueuedTaskIds：queued 等待超过后台上限的自动失败收口。
	for _, taskID := range result.ExpiredQueuedTaskIDs {
		q.mu.Lock()
		delete(q.pending, taskID)
		if cancelRunning, running := q.running[taskID]; running {
			q.mu.Unlock()
			cancelRunning()
			continue
		}
		q.mu.Unlock()
	}
	return result.TaskIDs, nil
}

func (q *ManualTestQueue) drain(ctx context.Context) {
	for {
		q.mu.Lock()
		if q.stopped {
			q.mu.Unlock()
			return
		}
		taskID := ""
		for key := range q.pending {
			taskID = key
			break
		}
		if taskID == "" {
			q.mu.Unlock()
			return
		}
		delete(q.pending, taskID)
		if len(q.running) >= q.cfg.Concurrency {
			q.pending[taskID] = struct{}{}
			q.mu.Unlock()
			return
		}
		runCtx, cancel := context.WithCancel(ctx)
		q.running[taskID] = cancel
		q.mu.Unlock()

		q.execute(runCtx, taskID)

		q.mu.Lock()
		delete(q.running, taskID)
		q.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return
		}
	}
}

func (q *ManualTestQueue) execute(ctx context.Context, taskID string) {
	// 标记 running 并读取任务事实；missing 表示任务已被取消或清理。
	task, err := q.repo.MarkRunning(ctx, taskID)
	if err != nil {
		return
	}
	if task == nil {
		return
	}
	var expectedStartedAt *string
	if task.StartedAt != nil {
		value := *task.StartedAt
		expectedStartedAt = &value
	}
	reporter := func(message string) {
		_ = q.repo.UpdateMessage(context.WithoutCancel(ctx), task.ID, message, expectedStartedAt)
	}
	result, execErr := q.executor(ctx, *task, reporter)
	if execErr != nil {
		if ctx.Err() != nil || errors.Is(execErr, context.Canceled) {
			_ = q.repo.Cancel(context.WithoutCancel(ctx), task.ID, "已停止测试", expectedStartedAt)
			return
		}
		message := execErr.Error()
		if message == "" {
			message = "账号测试任务执行失败"
		}
		_ = q.repo.Fail(context.WithoutCancel(ctx), task.ID, message, expectedStartedAt)
		return
	}
	if result.Canceled {
		_ = q.repo.Cancel(context.WithoutCancel(ctx), task.ID, "已停止测试", expectedStartedAt)
		return
	}
	if result.Success {
		_ = q.repo.Complete(context.WithoutCancel(ctx), task.ID, result, expectedStartedAt)
		return
	}
	_ = q.repo.Fail(context.WithoutCancel(ctx), task.ID, result.Message, expectedStartedAt)
}

// DispatchAccountTestTask 对齐 Node internal-api dispatchAccountTestTask：
// 入队成功返回 true；队列拒绝时把任务置为失败并返回 false。
func (q *ManualTestQueue) DispatchAccountTestTask(ctx context.Context, taskID string) (bool, error) {
	normalized, ok := NormalizeTaskID(taskID)
	if !ok {
		return false, nil
	}
	accepted := q.EnqueueLocal(normalized)
	if !accepted {
		if err := q.repo.Fail(ctx, normalized, "后台 worker 暂不可用，账号测试任务未能投递", nil); err != nil {
			return false, err
		}
	}
	return accepted, nil
}

// DiagnosticAttemptProgressMessage 对齐 accountDiagnosticAttemptMessage。
func DiagnosticAttemptProgressMessage(maxTotalTimeoutMS int64, testEndpointMode string) string {
	action := "真实请求测试中"
	if testEndpointMode == "images_json" {
		action = "图像生成测试中"
	}
	return fmt.Sprintf("%s：本次诊断最长等待 %s", action, FormatDiagnosticTimeout(maxTotalTimeoutMS))
}

// FormatDiagnosticTimeout 对齐 formatDiagnosticTimeout。
func FormatDiagnosticTimeout(timeoutMS int64) string {
	return fmt.Sprintf("%ds", max64(1, (timeoutMS+999)/1000))
}
