package modelcheckowner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ScheduledPayload is the immutable, credential-free scheduler input. The
// source that creates a task must persist all scope and policy references;
// the executor never reloads a mutable global policy by itself.
type ScheduledPayload struct {
	SystemAccountID         string `json:"systemAccountId"`
	ActorSystemAccountID    string `json:"actorSystemAccountId"`
	TargetType              string `json:"targetType"`
	TargetID                string `json:"targetId"`
	Model                   string `json:"model"`
	Profile                 string `json:"profile"`
	ProviderCode            string `json:"providerCode"`
	Threshold               int    `json:"threshold"`
	PenaltyAction           string `json:"penaltyAction"`
	ConfigRevision          string `json:"configRevision"`
	DispatchRevision        int64  `json:"dispatchRevision"`
	SourceConfigRevision    string `json:"sourceConfigRevision,omitempty"`
	SourceDispatchRevision  int64  `json:"sourceDispatchRevision,omitempty"`
	PolicyRevision          string `json:"policyRevision"`
	ProbeSetVersion         string `json:"probeSetVersion"`
	IdentityKey             string `json:"identityKey"`
	ScheduleID              string `json:"scheduleId"`
	OwnerID                 string `json:"ownerId,omitempty"`
	ScheduleRevision        int    `json:"scheduleRevision,omitempty"`
	IntervalMinutes         int    `json:"intervalMinutes,omitempty"`
	EnforcementID           string `json:"enforcementId,omitempty"`
	Generation              int    `json:"generation,omitempty"`
	RecoveryIntervalMinutes int    `json:"recoveryIntervalMinutes,omitempty"`
	// CustomQuestionIds 在 claim 时从 schedule（或恢复来源的 schedule/policy）
	// 行冻结进 payload，运行中题目被删/被驳回只影响后续运行；本份 payload
	// 的解析结果不受影响（不可变契约）。空 = 不执行题库家族。
	CustomQuestionIds []string `json:"customQuestionIds,omitempty"`
}

// SchedulerRunBuilder resolves a durable scheduled payload to a complete
// in-process Runtime request. It owns source/config/credential resolution and
// must fail closed when a referenced revision is unavailable.
type SchedulerRunBuilder func(context.Context, ScheduledPayload) (RunRequest, error)
type ScheduledCompletion func(context.Context, ScheduledPayload, RunResult) error

// RecoveryCompletion is the Business-owner generation/CAS boundary for a
// quality-isolated account. It is intentionally separate from Runtime.Run:
// a recovery probe is only complete after this callback clears or reschedules
// the matching enforcement lease.
type RecoveryCompletion func(context.Context, RecoveryPayload, bool) error

// FailedRunRecorder persists a queryable terminal failed run when a leased
// build fails before any probe executes (D6). *Store satisfies it with
// CreateFailedRun.
type FailedRunRecorder interface {
	CreateFailedRun(context.Context, RunRecord, string, string, time.Time) error
}

// buildFailureRunRecord freezes the payload's scope into a failed-run row.
// triggerKind keeps the leased kind (scheduled / quality_recovery) so run
// history stays attributable to the owning scheduler family.
func buildFailureRunRecord(runID string, kind SchedulerKind, payload ScheduledPayload, cause error, now time.Time) RunRecord {
	summary := map[string]any{"targetType": payload.TargetType, "targetId": payload.TargetID, "model": payload.Model, "profile": payload.Profile, "configRevision": payload.ConfigRevision, "sourceConfigRevision": payload.SourceConfigRevision, "policyRevision": payload.PolicyRevision, "probeSetVersion": payload.ProbeSetVersion}
	if payload.ScheduleID != "" {
		summary["scheduleId"] = payload.ScheduleID
	}
	policy := map[string]any{"revision": payload.PolicyRevision, "threshold": payload.Threshold, "action": payload.PenaltyAction, "recoveryIntervalMinutes": payload.RecoveryIntervalMinutes}
	request, _ := json.Marshal(summary)
	policySnapshot, _ := json.Marshal(policy)
	accountID := ""
	if payload.TargetType == "account" {
		accountID = payload.TargetID
	}
	return RunRecord{ID: runID, SystemAccountID: payload.SystemAccountID, ActorSystemAccountID: payload.ActorSystemAccountID, ProviderCode: payload.ProviderCode, TargetType: payload.TargetType, TargetID: payload.TargetID, AccountID: accountID, Model: payload.Model, Profile: payload.Profile, TriggerKind: string(kind), ScheduleID: payload.ScheduleID, ProbeSetVersion: payload.ProbeSetVersion, RequestSummary: request, PolicySnapshot: policySnapshot, StartedAt: now}
}

type RecoveryPayload struct {
	OwnerID, AccountID, EnforcementID, RunID string
	Generation, PolicyRevision               int
	RecoveryIntervalMinutes                  int
	CompletedAt                              time.Time
}

// SchedulerRunExecutor executes scheduled and recovery tasks in the Gateway
// process. It deliberately does not implement health_sync_retry, which has a
// separate formed/trusted retry executor.
type SchedulerRunner interface {
	Run(context.Context, RunRequest) (RunResult, error)
}

type SchedulerRunExecutor struct {
	Runtime   SchedulerRunner
	Build     SchedulerRunBuilder
	Recovery  RecoveryCompletion
	Scheduled ScheduledCompletion
	// FailedRuns optionally records a terminal failed run when Build fails
	// before any probe executes, so CompleteScheduled can point last_run_id at
	// a queryable detail (D6). nil keeps the legacy behavior (no run row).
	FailedRuns FailedRunRecorder
	// RunBudget bounds one leased run's probe execution. The Business
	// scheduler claims schedule/recovery rows with a finite lease
	// (BusinessSchedulerSource.EffectiveLease, default six minutes) and never
	// renews it during execution; without a budget a slow full-profile run
	// could outlive the lease, letting another owner re-claim the same
	// schedule while the first run is still probing and permanently rejecting
	// its completion (stale lease). When positive, Runtime.Run executes under
	// this deadline and a timeout ends through the same durable failure path
	// as any probe error, so the completion write lands inside the remaining
	// lease window. Zero disables the budget: manual HTTP runs run without a
	// Business lease and executors assembled without one must not inherit a
	// deadline they cannot justify.
	RunBudget time.Duration
}

// BusinessLeaseExecutionMargin is the safety margin kept between a leased
// run's execution budget and its Business lease expiry. The margin covers the
// durable terminal writes (CompleteScheduled / recovery CAS) that must land
// while lease_until is still in the future.
const BusinessLeaseExecutionMargin = 30 * time.Second

// ScheduleRunBudget derives the bounded execution budget for one leased
// schedule/recovery run from the Business lease. A run must always end inside
// its lease window: exceeding the lease lets another owner re-claim the same
// schedule/recovery row and makes the first run's terminal write permanently
// stale. A lease that does not exceed the completion margin keeps half of
// itself as the budget so small leases remain usable in tests. Zero or
// negative leases produce a zero budget (disabled), matching the field
// contract above.
func ScheduleRunBudget(lease time.Duration) time.Duration {
	if lease <= 0 {
		return 0
	}
	if lease <= BusinessLeaseExecutionMargin {
		return lease / 2
	}
	return lease - BusinessLeaseExecutionMargin
}

func (e *SchedulerRunExecutor) Execute(ctx context.Context, task ScheduleTask) error {
	if e == nil || e.Runtime == nil || e.Build == nil {
		return errors.New("J3b scheduler run executor is not initialized")
	}
	if task.Kind != SchedulerScheduled && task.Kind != SchedulerQualityRecovery {
		return fmt.Errorf("J3b scheduler run executor received %s", task.Kind)
	}
	if task.Kind == SchedulerQualityRecovery && e.Recovery == nil {
		return errors.New("J3b quality recovery completion owner is not configured")
	}
	var payload ScheduledPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode J3b scheduler payload: %w", err)
	}
	if strings.TrimSpace(payload.SystemAccountID) == "" || strings.TrimSpace(payload.ActorSystemAccountID) == "" || strings.TrimSpace(payload.TargetType) == "" || strings.TrimSpace(payload.TargetID) == "" || strings.TrimSpace(payload.Model) == "" || strings.TrimSpace(payload.Profile) == "" || strings.TrimSpace(payload.ProviderCode) == "" || strings.TrimSpace(payload.ConfigRevision) == "" || payload.DispatchRevision < 1 || strings.TrimSpace(payload.SourceConfigRevision) == "" || payload.SourceDispatchRevision < 1 || strings.TrimSpace(payload.PolicyRevision) == "" || strings.TrimSpace(payload.ProbeSetVersion) == "" || strings.TrimSpace(payload.IdentityKey) == "" || (payload.PenaltyAction != "disable" && payload.PenaltyAction != "fallback" && payload.PenaltyAction != "quality_isolate") || payload.Threshold < 40 || payload.Threshold > 100 {
		return errors.New("J3b scheduler payload scope or policy snapshot is incomplete")
	}
	if task.Kind == SchedulerScheduled && (e.Scheduled == nil || strings.TrimSpace(payload.OwnerID) == "" || payload.ScheduleRevision < 1 || payload.IntervalMinutes < 1) {
		return errors.New("J3b scheduled task completion metadata is incomplete")
	}
	request, err := e.Build(ctx, payload)
	if err != nil {
		// D6：Build 失败先落一条可查的 failed run 记录，再走计划/恢复回写，
		// 让 last_run_id 指向失败详情而不是 NULL。恢复类保持既有语义：
		// 记录 run 后返回错误，恢复租约自然到期后重试。
		failedRunID := ""
		if e.FailedRuns != nil {
			failureAt := time.Now().UTC()
			record := buildFailureRunRecord(newID("run"), task.Kind, payload, err, failureAt)
			if createErr := e.FailedRuns.CreateFailedRun(ctx, record, "model_check_build_failed", err.Error(), failureAt); createErr != nil {
				return errors.Join(fmt.Errorf("build J3b scheduled request: %w", err), fmt.Errorf("record J3b build-failure run: %w", createErr))
			}
			failedRunID = record.ID
		}
		if task.Kind == SchedulerScheduled {
			completion := RunResult{Status: string(RunFailed)}
			if failedRunID != "" {
				completion.RunID = failedRunID
			}
			if completeErr := e.Scheduled(ctx, payload, completion); completeErr != nil {
				return errors.Join(fmt.Errorf("build J3b scheduled request: %w", err), fmt.Errorf("complete J3b scheduled task: %w", completeErr))
			}
		}
		return fmt.Errorf("build J3b scheduled request: %w", err)
	}
	request.TriggerKind = string(task.Kind)
	request.ScheduleID = payload.ScheduleID
	request.SystemAccountID = payload.SystemAccountID
	request.ActorSystemAccountID = payload.ActorSystemAccountID
	request.TargetType = payload.TargetType
	request.TargetID = payload.TargetID
	request.Model = payload.Model
	request.Profile = payload.Profile
	request.ProviderCode = payload.ProviderCode
	request.Threshold = payload.Threshold
	request.PenaltyAction = payload.PenaltyAction
	request.RecoveryIntervalMinutes = payload.RecoveryIntervalMinutes
	request.ConfigRevision = payload.ConfigRevision
	request.DispatchRevision = payload.DispatchRevision
	request.SourceConfigRevision = payload.SourceConfigRevision
	request.SourceDispatchRevision = payload.SourceDispatchRevision
	request.PolicyRevision = payload.PolicyRevision
	request.ProbeSetVersion = payload.ProbeSetVersion
	request.IdentityKey = payload.IdentityKey
	// 题库配置随 payload 冻结传递（scheduled 与 quality_recovery 同源同语义）。
	request.CustomQuestionIds = payload.CustomQuestionIds
	// The budget deliberately wraps only the probe run. The terminal CAS
	// writes below must keep the scheduler's undeadlined context: an expired
	// budget context would otherwise prevent the very completion/failure
	// fence that records the timeout inside the remaining lease window.
	runCtx := ctx
	if e.RunBudget > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, e.RunBudget)
		defer cancel()
	}
	result, runErr := e.Runtime.Run(runCtx, request)
	if task.Kind == SchedulerScheduled {
		completion := result
		if runErr != nil {
			completion.Status = string(RunFailed)
		} else if completion.Status == "" {
			completion.Status = string(RunFailed)
		}
		if completeErr := e.Scheduled(ctx, payload, completion); completeErr != nil {
			return fmt.Errorf("complete J3b scheduled task: %w", completeErr)
		}
		// The durable schedule completion records failed probe results and
		// advances the next due time. Do not stop the owner cycle merely because
		// an upstream probe failed; only the completion write is fatal.
		return nil
	}
	if task.Kind == SchedulerQualityRecovery {
		var generation, policyRevision, interval int
		if _, err := fmt.Sscanf(payload.PolicyRevision, "%d", &policyRevision); err != nil || policyRevision < 0 {
			return errors.New("J3b quality recovery policy revision is invalid")
		}
		// Recovery metadata is carried in the scheduler payload under the
		// existing immutable fields; generation/owner are required extensions.
		if strings.TrimSpace(payload.OwnerID) == "" || strings.TrimSpace(payload.EnforcementID) == "" || payload.Generation < 1 || payload.RecoveryIntervalMinutes < 10 {
			return errors.New("J3b quality recovery lease metadata is incomplete")
		}
		generation, interval = payload.Generation, payload.RecoveryIntervalMinutes
		// A successful HTTP/probe execution is not sufficient to clear a
		// quality-isolated account. Recovery must observe the same durable
		// evidence/trust gates used by health projection; missing metadata is
		// fail-closed so an older/partial runtime cannot accidentally recover.
		passed := runErr == nil && result.Status == string(RunCompleted) && runResultRecoveryEligible(result, payload.Threshold, payload.Profile)
		return e.Recovery(ctx, RecoveryPayload{OwnerID: payload.OwnerID, AccountID: payload.TargetID, EnforcementID: payload.EnforcementID, RunID: result.RunID, Generation: generation, PolicyRevision: policyRevision, RecoveryIntervalMinutes: interval, CompletedAt: time.Now().UTC()}, passed)
	}
	if runErr != nil {
		return runErr
	}
	return nil
}

func runResultEvidenceFormed(result RunResult) bool {
	data, ok := result.Data.(map[string]any)
	if !ok {
		return false
	}
	evidence, evidenceOK := data["evidenceFormed"].(bool)
	trust, trustOK := data["trustFormed"].(bool)
	return evidenceOK && trustOK && evidence && trust
}

// runResultRecoveryEligible mirrors the Node recovery boundary: only a
// completed result whose score meets the frozen threshold and whose level is
// not unavailable may clear quality isolation. A successful transport alone
// must never release an enforcement lease. quick 检测证据族少于 full，与发布
// 口径对齐：不要求 formed/trusted，但 suspicious 与 hardFailure 同样不可
// 放行；full/空 profile 维持 formed+trusted 的 6 族通用证据门槛。
func runResultRecoveryEligible(result RunResult, threshold int, profile string) bool {
	if threshold < 40 || threshold > 100 {
		return false
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		return false
	}
	score, scoreOK := data["score"].(int)
	level, levelOK := data["level"].(string)
	if !scoreOK || !levelOK {
		return false
	}
	hardFailure, _ := data["hardFailure"].(bool)
	if profile != "quick" && !runResultEvidenceFormed(result) {
		return false
	}
	if score < threshold || level == "unavailable" || hardFailure {
		return false
	}
	return profile != "quick" || level != "suspicious"
}

var _ SchedulerExecutor = (*SchedulerRunExecutor)(nil)

// SchedulerExecutorMux keeps the three scheduler kinds in one owner without
// allowing one kind to impersonate another kind's payload contract.
type SchedulerExecutorMux struct {
	Runs   *SchedulerRunExecutor
	Health *HealthSyncRetryExecutor
}

func (m *SchedulerExecutorMux) Execute(ctx context.Context, task ScheduleTask) error {
	if m == nil {
		return errors.New("J3b scheduler executor mux is not initialized")
	}
	switch task.Kind {
	case SchedulerScheduled, SchedulerQualityRecovery:
		if m.Runs == nil {
			return errors.New("J3b scheduled executor is not initialized")
		}
		return m.Runs.Execute(ctx, task)
	case SchedulerHealthRetry:
		if m.Health == nil {
			return errors.New("J3b health retry executor is not initialized")
		}
		return m.Health.Execute(ctx, task)
	default:
		return fmt.Errorf("unsupported J3b scheduler kind %q", task.Kind)
	}
}

var _ SchedulerExecutor = (*SchedulerExecutorMux)(nil)
