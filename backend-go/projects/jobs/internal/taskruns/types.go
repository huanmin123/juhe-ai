package taskruns

import (
	"fmt"
	"strings"
	"time"
)

// TaskRunStatus 与 Node BackgroundTaskRunStatus 一致。
type TaskRunStatus string

const (
	StatusQueued    TaskRunStatus = "queued"
	StatusRunning   TaskRunStatus = "running"
	StatusCompleted TaskRunStatus = "completed"
	StatusFailed    TaskRunStatus = "failed"
	StatusSkipped   TaskRunStatus = "skipped"
)

// TemporaryMaintenanceWorkerRole 是 background-task-run-reconcile 的对账对象：
// 只有该 worker_role 的 queued/running 陈旧行会被收口为 failed。
const TemporaryMaintenanceWorkerRole = "temporary-maintenance-worker"

// TaskRun 是 background_task_runs 行的只读视图（等价 BackgroundTaskRunSummary）。
type TaskRun struct {
	RunID        string
	JobName      string
	JobType      string
	WorkerRole   string
	Status       TaskRunStatus
	LeaseKey     string
	OwnerID      string
	Params       map[string]any
	Result       map[string]any
	ErrorMessage string
	SubmittedAt  time.Time
	StartedAt    *time.Time
	HeartbeatAt  *time.Time
	FinishedAt   *time.Time
	DurationMs   *int64
	ExitCode     *int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TaskRunCreateInput 等价 Node BackgroundTaskRunCreateInput。
type TaskRunCreateInput struct {
	JobName     string
	JobType     string
	WorkerRole  string
	LeaseKey    string
	Params      map[string]any
	SubmittedAt *time.Time
}

// TaskRunStartInput 等价 Node BackgroundTaskRunStartInput。
type TaskRunStartInput struct {
	RunID      string
	OwnerID    string
	LeaseUntil time.Time
	Now        *time.Time
}

// TaskRunFinishInput 等价 Node BackgroundTaskRunFinishInput。
type TaskRunFinishInput struct {
	RunID        string
	Status       TaskRunStatus // completed | failed | skipped
	Result       map[string]any
	ErrorMessage string
	ExitCode     *int64
	FinishedAt   *time.Time
}

// TaskRunReconcileInput 等价 Node BackgroundTaskRunReconcileInput。
type TaskRunReconcileInput struct {
	QueuedBefore           time.Time
	RunningHeartbeatBefore time.Time
	Now                    *time.Time
	Limit                  int
}

// TaskRunReconcileResult 等价 Node BackgroundTaskRunReconcileResult。
type TaskRunReconcileResult struct {
	FailedQueuedCount        int64
	FailedRunningCount       int64
	DeletedExpiredLeaseCount int64
}

// LeaseIdentity 是 background_job_leases 的带 fencing token 租约身份
// （等价 Node ScheduledJobLeaseIdentity）。
type LeaseIdentity struct {
	LeaseKey     string
	OwnerID      string
	FencingToken int64
	LeaseUntil   time.Time
}

// LeaseFence 是写路径做 fence 校验所需的最小身份（等价 ScheduledJobLeaseFence）。
type LeaseFence struct {
	LeaseKey     string
	OwnerID      string
	FencingToken int64
}

func (f LeaseFence) Fence() LeaseFence { return f }

// Identity 返回完整租约身份的 fence 视图。
func (l LeaseIdentity) Fence() LeaseFence {
	return LeaseFence{LeaseKey: l.LeaseKey, OwnerID: l.OwnerID, FencingToken: l.FencingToken}
}

// AcquireFailReason 与 Node ScheduledJobLeaseAcquireResult 的失败原因一致。
type AcquireFailReason string

const (
	AcquireAdvisoryBusy AcquireFailReason = "advisory_busy"
	AcquireLeaseHeld    AcquireFailReason = "lease_held"
)

// AcquireResult 表示一次租约获取尝试。
type AcquireResult struct {
	Acquired bool
	Reason   AcquireFailReason // 未获取时为 advisory_busy 或 lease_held
	LeaseKey string
	Lease    *LeaseIdentity
}

// ScheduledLeaseAcquireInput 等价 Node ScheduledJobLeaseAcquireInput。
type ScheduledLeaseAcquireInput struct {
	JobName  string
	ShardKey string
	LeaseKey string // 为空时由 ScheduledLeaseKey(jobName, shardKey) 推导
	OwnerID  string
	RunID    string
	TTL      time.Duration
}

// ErrLeaseLost 等价 Node ScheduledJobLeaseLostError。
type ErrLeaseLost struct {
	Lease LeaseFence
}

func (e *ErrLeaseLost) Error() string {
	return "后台任务租约已失效：" + e.Lease.LeaseKey
}

// Lease TTL 边界与 Node minimum/maximumScheduledJobLeaseTtlMs 一致。
const (
	MinimumScheduledJobLeaseTTL = 100 * time.Millisecond
	MaximumScheduledJobLeaseTTL = 24 * time.Hour
)

// Reconcile 行数上限与 Node normalizeReconcileLimit 一致。
const (
	ReconcileDefaultLimit = 500
	ReconcileMaxLimit     = 1000
)

// TemporaryTaskLeaseKey 与 Node backgroundTaskLeaseKey 一致。
func TemporaryTaskLeaseKey(runID string) string {
	return TemporaryMaintenanceWorkerRole + ":" + runID
}

// ScheduledLeaseKey 与 Node scheduledJobLeaseKey 的键形一致
// （`scheduled:<jobName>:<shardKey>`）；入参校验由 TryAcquireScheduledLease
// 在推导前完成。
func ScheduledLeaseKey(jobName, shardKey string) string {
	return "scheduled:" + jobName + ":" + shardKey
}

// NormalizeReconcileLimit 与 Node normalizeReconcileLimit 一致：
// 非法（<=0 或超出上限）时回落 500，合法时截断到 [1,1000]。
func NormalizeReconcileLimit(limit int) int {
	if limit <= 0 {
		return ReconcileDefaultLimit
	}
	if limit > ReconcileMaxLimit {
		return ReconcileMaxLimit
	}
	return limit
}

// NormalizeStatus 与 Node normalizeStatus 一致：未知状态一律视为 failed。
func NormalizeStatus(value string) TaskRunStatus {
	switch TaskRunStatus(value) {
	case StatusQueued, StatusRunning, StatusCompleted, StatusFailed, StatusSkipped:
		return TaskRunStatus(value)
	default:
		return StatusFailed
	}
}

func requiredText(value, fieldName string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%s 不能为空", fieldName)
	}
	if len(trimmed) > 512 {
		return "", fmt.Errorf("%s 长度不能超过 512", fieldName)
	}
	return trimmed, nil
}

// NormalizeLeaseTTL 与 Node normalizedScheduledJobLeaseTtlMs 一致。
func NormalizeLeaseTTL(ttl time.Duration) (time.Duration, error) {
	if ttl < MinimumScheduledJobLeaseTTL || ttl > MaximumScheduledJobLeaseTTL {
		return 0, fmt.Errorf("ttlMs 必须介于 %d 和 %d 之间", MinimumScheduledJobLeaseTTL.Milliseconds(), MaximumScheduledJobLeaseTTL.Milliseconds())
	}
	return ttl, nil
}
