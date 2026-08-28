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
	if strings.TrimSpace(payload.SystemAccountID) == "" || strings.TrimSpace(payload.ActorSystemAccountID) == "" || strings.TrimSpace(payload.TargetType) == "" || strings.TrimSpace(payload.TargetID) == "" || strings.TrimSpace(payload.Model) == "" || strings.TrimSpace(payload.Profile) == "" || strings.TrimSpace(payload.ProviderCode) == "" || strings.TrimSpace(payload.ConfigRevision) == "" || strings.TrimSpace(payload.PolicyRevision) == "" || strings.TrimSpace(payload.ProbeSetVersion) == "" || strings.TrimSpace(payload.IdentityKey) == "" || (payload.PenaltyAction != "disable" && payload.PenaltyAction != "fallback" && payload.PenaltyAction != "quality_isolate") || payload.Threshold < 40 || payload.Threshold > 100 {
		return errors.New("J3b scheduler payload scope or policy snapshot is incomplete")
	}
	if task.Kind == SchedulerScheduled && (e.Scheduled == nil || strings.TrimSpace(payload.OwnerID) == "" || payload.ScheduleRevision < 1 || payload.IntervalMinutes < 10) {
		return errors.New("J3b scheduled task completion metadata is incomplete")
	}
	request, err := e.Build(ctx, payload)
	if err != nil {
		if task.Kind == SchedulerScheduled {
			if completeErr := e.Scheduled(ctx, payload, RunResult{Status: string(RunFailed)}); completeErr != nil {
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
	request.PolicyRevision = payload.PolicyRevision
	request.ProbeSetVersion = payload.ProbeSetVersion
	request.IdentityKey = payload.IdentityKey
	result, runErr := e.Runtime.Run(ctx, request)
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
		passed := runErr == nil && result.Status == string(RunCompleted) && runResultEvidenceFormed(result)
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
