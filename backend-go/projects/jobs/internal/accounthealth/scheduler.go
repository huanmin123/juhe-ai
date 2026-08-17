package accounthealth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Runner is the only J1 scheduler.  Its inputs are immutable signed files and
// its durable state lives in the jobs-owned store; it has no Node/Gateway/IPC
// or Redis dependency.
type Runner struct {
	cfg               Config
	store             *Store
	logger            *slog.Logger
	directInputReader directInputLoader

	mu     sync.RWMutex
	status RunnerStatus
}

// directInputLoader permits the scheduler to load immutable, currently eligible
// inputs from the independently configured business read model.  It deliberately
// has no Node/Gateway client surface: signed request files only carry a trigger
// and fences; the effective probe input is read directly from PostgreSQL.
type directInputLoader interface {
	LoadDue(ctx context.Context, limit int) ([]Input, error)
	LoadAccount(ctx context.Context, accountID string) ([]Input, error)
}

type RunnerStatus struct {
	OwnerHeld   bool
	LastScanAt  time.Time
	LastSuccess time.Time
	LastError   string
	Inputs      int
	Executed    int
}

const maxScheduleDuration = 365 * 24 * time.Hour
const maxScheduleMilliseconds = int64(maxScheduleDuration / time.Millisecond)

func NewRunner(cfg Config, store *Store, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{cfg: cfg, store: store, logger: logger}
}

func NewRunnerWithDirectInputReader(cfg Config, store *Store, logger *slog.Logger, reader *PostgresDirectInputReader) *Runner {
	runner := NewRunner(cfg, store, logger)
	runner.directInputReader = reader
	return runner
}

func (r *Runner) Ready() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status.OwnerHeld && !r.status.LastSuccess.IsZero() && r.status.LastError == ""
}

func (r *Runner) Status() RunnerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.store == nil {
		return errors.New("account-health runner 未初始化")
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lease, acquired, err := r.store.AcquireOwnerLease(ctx, r.cfg.InstanceID, r.cfg.OwnerLease)
		if err != nil {
			r.setError(err)
			if err := waitContext(ctx, r.cfg.ScanInterval); err != nil {
				return err
			}
			continue
		}
		if !acquired {
			if err := waitContext(ctx, minDuration(r.cfg.ScanInterval, r.cfg.OwnerLease/3)); err != nil {
				return err
			}
			continue
		}
		if err := r.runOwned(ctx, lease); err != nil && !errors.Is(err, context.Canceled) {
			r.setError(err)
			r.logger.Warn("account-health owner lease released", "error", err)
		}
	}
}

func (r *Runner) runOwned(parent context.Context, lease OwnerLease) error {
	r.setOwnerHeld(true)
	defer func() {
		r.setOwnerHeld(false)
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		if err := r.store.ReleaseOwnerLease(releaseCtx, lease); err != nil {
			r.logger.Warn("release account-health owner lease", "error", err)
		}
	}()
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	renewEvery := maxDuration(3*time.Second, r.cfg.OwnerLease/3)
	renewTicker := time.NewTicker(renewEvery)
	scanTicker := time.NewTicker(r.cfg.ScanInterval)
	defer renewTicker.Stop()
	defer scanTicker.Stop()
	if err := r.runCycle(ctx, lease); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-renewTicker.C:
			renewed, err := r.store.RenewOwnerLease(ctx, lease, r.cfg.OwnerLease)
			if err != nil {
				return fmt.Errorf("续约 account-health owner lease: %w", err)
			}
			if !renewed {
				return ErrOwnerLeaseLost
			}
		case <-scanTicker.C:
			if err := r.runCycle(ctx, lease); err != nil {
				if errors.Is(err, ErrOwnerLeaseLost) || ctx.Err() != nil {
					return err
				}
				r.setError(err)
				r.logger.Error("account-health scan failed; owner stays alive for the next scan", "error", err)
			}
		}
	}
}

func (r *Runner) runCycle(ctx context.Context, lease OwnerLease) error {
	now := r.cfg.Now().UTC()
	r.setScan(now)
	var err error
	var inputs []Input
	if r.directInputReader != nil {
		inputs, err = r.directInputReader.LoadDue(ctx, r.cfg.DirectInputLimit)
		if err != nil {
			return err
		}
	} else {
		inputs, err = LoadSignedInputFiles(r.cfg.InputDirectory, r.cfg.InputKeys)
		if err != nil {
			return err
		}
	}
	requests, err := LoadSignedProbeRequests(r.cfg.InputDirectory, r.cfg.InputKeys)
	if err != nil {
		return err
	}
	// A PostgreSQL direct-input read freezes each candidate's issued_at after
	// this cycle starts. Refresh the due-time fence after every input/request
	// read so a newly read active candidate with zero jitter is eligible in
	// this cycle rather than perpetually appearing a few microseconds early.
	// This does not change the durable input fence or broaden any candidate.
	now = r.cfg.Now().UTC()
	r.setScan(now)
	inputsByAccount := make(map[string]Input, len(inputs))
	for _, input := range inputs {
		inputsByAccount[input.AccountID] = input
	}
	for _, request := range requests {
		if _, found := inputsByAccount[request.AccountID]; !found && r.directInputReader != nil {
			explicitInputs, loadErr := r.directInputReader.LoadAccount(ctx, request.AccountID)
			if loadErr != nil {
				return loadErr
			}
			if len(explicitInputs) == 1 {
				inputsByAccount[request.AccountID] = explicitInputs[0]
			}
		}
		if err := r.runExplicitRequest(ctx, lease, inputsByAccount[request.AccountID], request, now); err != nil {
			return err
		}
		if err := r.removeConsumedRequest(ctx, request); err != nil {
			return err
		}
	}
	sort.Slice(inputs, func(left, right int) bool { return inputs[left].AccountID < inputs[right].AccountID })
	jobs := make(chan Input)
	var workers sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	var executed atomic.Int64
	for range r.cfg.MaxConcurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for input := range jobs {
				if err := r.runInput(ctx, lease, input, now); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					continue
				}
				// This is a scan-attempt metric; durable outcome count remains in
				// the store and is never inferred from this in-memory value.
				executed.Add(1)
			}
		}()
	}
	for _, input := range inputs {
		select {
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return context.Cause(ctx)
		case jobs <- input:
		}
	}
	close(jobs)
	workers.Wait()
	errMu.Lock()
	err = firstErr
	errMu.Unlock()
	if err != nil {
		return err
	}
	r.setSuccess(len(inputs), int(executed.Load()))
	return nil
}

func (r *Runner) removeConsumedRequest(ctx context.Context, request ProbeRequest) error {
	if strings.TrimSpace(request.sourcePath) == "" {
		return nil
	}
	completed, err := r.store.HasRequest(ctx, request.RequestID)
	if err != nil {
		return err
	}
	if !completed {
		return nil
	}
	if err := os.Remove(request.sourcePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理已消费 account-health request %q 失败: %w", filepath.Base(request.sourcePath), err)
	}
	return nil
}

func (r *Runner) runExplicitRequest(ctx context.Context, lease OwnerLease, input Input, request ProbeRequest, now time.Time) error {
	already, err := r.store.HasRequest(ctx, request.RequestID)
	if err != nil || already {
		return err
	}
	if input.AccountID == "" || input.InputVersion != request.InputVersion || input.ConfigRevision != request.ConfigRevision || input.DispatchRevision != request.DispatchRevision || !inputEligible(input) {
		return r.persistExplicitTerminal(ctx, lease, request, OutcomeStale, now, "input_stale", "request 对应的 input 已失效")
	}
	if requestDeadlineExpired(request, now) {
		return r.persistExplicitTerminal(ctx, lease, request, OutcomeTaskFailed, now, "request_deadline_elapsed", "探活请求已过期")
	}
	if err := validateScheduledInput(input, now); err != nil {
		return r.persistExplicitTerminal(ctx, lease, request, OutcomeTaskFailed, now, "input_invalid", err.Error())
	}
	probeCtx, cancel := context.WithDeadline(ctx, request.Deadline)
	outcome, err := ExecuteInputProbe(probeCtx, r.store, lease, input, request, ProbeOptions{Secret: r.cfg.CredentialSecret, Timeout: r.cfg.ProbeTimeout, MaxResponseBytes: r.cfg.MaxResponseBytes, Now: r.cfg.Now})
	cancel()
	if err != nil {
		return err
	}
	prior, found, err := r.store.LoadCurrentState(ctx, input.AccountID)
	if err != nil {
		return err
	}
	if request.MutateAccount || (request.SourceFence != nil && outcome.Outcome == OutcomeUpstreamFailed) {
		applyOutcomeDecision(&outcome, input, prior, found, "health")
	} else {
		preserveStateForSourceOnlyOutcome(&outcome, input, prior, found)
	}
	_, err = r.store.AppendOutcome(ctx, lease, outcome)
	return err
}

func (r *Runner) persistExplicitTerminal(ctx context.Context, lease OwnerLease, request ProbeRequest, kind string, observed time.Time, code, message string) error {
	outcome := Outcome{OutcomeID: newOutcomeID(), RequestID: request.RequestID, AccountID: request.AccountID, Outcome: kind, ObservedAt: observed, InputVersion: request.InputVersion, ConfigRevision: request.ConfigRevision, DispatchRevision: request.DispatchRevision, ErrorCode: code, ErrorMessage: message, SourceFence: request.SourceFence}
	_, err := r.store.AppendOutcome(ctx, lease, outcome)
	return err
}

func preserveStateForSourceOnlyOutcome(outcome *Outcome, input Input, prior CurrentState, found bool) {
	if found && prior.InputVersion == input.InputVersion && prior.ConfigRevision == input.ConfigRevision && prior.DispatchRevision == input.DispatchRevision {
		outcome.NextDueAt = prior.NextDueAt
		outcome.FailureCount = prior.FailureCount
		outcome.FailureStartedAt = prior.FailureStartedAt
		outcome.AccountStatus = prior.AccountStatus
		outcome.CooldownFence = prior.CooldownFence
		return
	}
	outcome.AccountStatus = input.Eligibility.AccountStatus
}

func (r *Runner) runInput(ctx context.Context, lease OwnerLease, input Input, now time.Time) error {
	// A signed revoke/disable snapshot deliberately carries no credential or
	// protocol data. It immediately suppresses older durable state and makes no
	// upstream call; treating it as malformed would create noisy retries.
	if !inputEligible(input) {
		return nil
	}
	if err := validateScheduledInput(input, now); err != nil {
		return r.persistTaskFailure(ctx, lease, input, now, "input_invalid", err.Error())
	}
	state, found, err := r.store.LoadCurrentState(ctx, input.AccountID)
	if err != nil {
		return err
	}
	kind, due, ok := nextDue(input, state, found, now)
	if !ok || due.After(now) {
		return nil
	}
	request := ProbeRequest{
		RequestID:        scheduledRequestID(input, kind, due),
		AccountID:        input.AccountID,
		Reason:           kind,
		InputVersion:     input.InputVersion,
		ConfigRevision:   input.ConfigRevision,
		DispatchRevision: input.DispatchRevision,
		Deadline:         now.Add(r.cfg.ProbeTimeout),
	}
	already, err := r.store.HasRequest(ctx, request.RequestID)
	if err != nil {
		return err
	}
	if already {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, r.cfg.ProbeTimeout)
	outcome, err := ExecuteInputProbe(probeCtx, r.store, lease, input, request, ProbeOptions{
		Secret:           r.cfg.CredentialSecret,
		Timeout:          r.cfg.ProbeTimeout,
		MaxResponseBytes: r.cfg.MaxResponseBytes,
		Now:              r.cfg.Now,
	})
	cancel()
	if err != nil {
		return err
	}
	applyOutcomeDecision(&outcome, input, state, found, kind)
	_, err = r.store.AppendOutcome(ctx, lease, outcome)
	return err
}

func (r *Runner) persistTaskFailure(ctx context.Context, lease OwnerLease, input Input, observed time.Time, code, message string) error {
	requestID := scheduledRequestID(input, "invalid_input", observed)
	already, err := r.store.HasRequest(ctx, requestID)
	if err != nil || already {
		return err
	}
	outcome := Outcome{
		OutcomeID:        newOutcomeID(),
		RequestID:        requestID,
		AccountID:        input.AccountID,
		Outcome:          OutcomeTaskFailed,
		ObservedAt:       observed,
		InputVersion:     input.InputVersion,
		ConfigRevision:   input.ConfigRevision,
		DispatchRevision: input.DispatchRevision,
		ErrorCode:        code,
		ErrorMessage:     message,
		NextDueAt:        ptrTime(observed.Add(5 * time.Minute)),
		AccountStatus:    input.Eligibility.AccountStatus,
	}
	_, err = r.store.AppendOutcome(ctx, lease, outcome)
	return err
}

func nextDue(input Input, state CurrentState, found bool, now time.Time) (kind string, due time.Time, ok bool) {
	if !found || state.InputVersion != input.InputVersion || state.ConfigRevision != input.ConfigRevision || state.DispatchRevision != input.DispatchRevision {
		if input.Eligibility.AccountStatus == "temporary_unavailable" || input.Eligibility.AccountStatus == "rate_limited" {
			if !validCooldownFence(input.Cooldown, input) || input.Eligibility.CooldownUntil == nil {
				return "", time.Time{}, false
			}
			return "cooldown_retest", *input.Eligibility.CooldownUntil, true
		}
		if input.Eligibility.AccountStatus == "pending_test" {
			return "health", input.IssuedAt, true
		}
		return "health", input.IssuedAt.Add(stableJitter(input.AccountID, input.Schedule.HealthJitterMS)), true
	}
	if state.NextDueAt == nil {
		return "health", now, true
	}
	if state.AccountStatus == "temporary_unavailable" || state.AccountStatus == "rate_limited" {
		if !validCooldownFence(state.CooldownFence, input) {
			return "", time.Time{}, false
		}
		return "cooldown_retest", *state.NextDueAt, true
	}
	if state.AccountStatus == "error" {
		return "", time.Time{}, false
	}
	return "health", *state.NextDueAt, true
}

func applyOutcomeDecision(outcome *Outcome, input Input, prior CurrentState, priorFound bool, kind string) {
	observed := outcome.ObservedAt.UTC()
	priorStatus := input.Eligibility.AccountStatus
	if priorFound && prior.InputVersion == input.InputVersion && prior.ConfigRevision == input.ConfigRevision && prior.DispatchRevision == input.DispatchRevision && prior.AccountStatus != "" {
		priorStatus = prior.AccountStatus
	}
	if kind == "cooldown_retest" {
		applyCooldownDecision(outcome, input, prior, priorFound, priorStatus, observed)
		return
	}
	applyHealthDecision(outcome, input, prior, priorStatus, observed)
}

func applyHealthDecision(outcome *Outcome, input Input, prior CurrentState, priorStatus string, observed time.Time) {
	interval := durationMS(input.Schedule.HealthIntervalMS, time.Hour)
	retry := durationMS(input.Schedule.FailureRetryMS, 5*time.Minute)
	switch outcome.Outcome {
	case OutcomeSuccess:
		next := observed.Add(interval + stableJitter(input.AccountID, input.Schedule.HealthJitterMS))
		outcome.NextDueAt = &next
		outcome.FailureCount = 0
		outcome.AccountStatus = "active"
		transition := "health_success"
		if priorStatus == "pending_test" {
			transition = "activation_success"
		}
		outcome.Projection = &Projection{TargetAccountID: input.AccountID, TransitionKind: transition, InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: priorStatus, Values: map[string]any{"last_health_check_at": observed.Format(time.RFC3339Nano), "last_health_success_at": observed.Format(time.RFC3339Nano), "last_health_check_status_code": outcome.StatusCode}}
	case OutcomeNeutral, OutcomeUpstreamFailed:
		failures := prior.FailureCount + 1
		outcome.FailureCount = failures
		started := prior.FailureStartedAt
		if started == nil {
			started = &observed
		}
		outcome.FailureStartedAt = started
		if priorStatus == "pending_test" && observed.Sub(*started) >= 24*time.Hour {
			outcome.AccountStatus = "error"
			outcome.Projection = healthProjection(input, "activation_error", priorStatus, nil, observed, outcome, failures)
			return
		}
		if priorStatus == "active" {
			outcome.AccountStatus = "temporary_unavailable"
			generation := newOutcomeID()
			next := observed.Add(durationMS(input.Schedule.CooldownFailureBackoffMS, retry))
			outcome.NextDueAt = &next
			outcome.Projection = healthProjection(input, "temporary_unavailable", priorStatus, nil, observed, outcome, failures)
			outcome.CooldownFence = &CooldownFence{ObservationStartedAt: observed, Generation: generation, SourceConfigRevision: input.Eligibility.SourceConfigRevision}
			outcome.Projection.CooldownFence = outcome.CooldownFence
			return
		}
		next := observed.Add(retry)
		outcome.NextDueAt = &next
		outcome.AccountStatus = priorStatus
		outcome.Projection = healthProjection(input, "health_failure", priorStatus, nil, observed, outcome, failures)
	default:
		next := observed.Add(retry)
		outcome.NextDueAt = &next
		outcome.FailureCount = prior.FailureCount
		outcome.FailureStartedAt = prior.FailureStartedAt
		outcome.AccountStatus = priorStatus
	}
}

func applyCooldownDecision(outcome *Outcome, input Input, prior CurrentState, priorFound bool, expectedStatus string, observed time.Time) {
	fence := input.Cooldown
	if priorFound && prior.InputVersion == input.InputVersion && prior.ConfigRevision == input.ConfigRevision && prior.DispatchRevision == input.DispatchRevision {
		fence = prior.CooldownFence
	}
	if !validCooldownFence(fence, input) {
		outcome.Outcome = OutcomeTaskFailed
		outcome.ErrorCode = "cooldown_fence_invalid"
		outcome.ErrorMessage = "冷却复测缺少或不匹配五元 fence"
		outcome.AccountStatus = input.Eligibility.AccountStatus
		return
	}
	base := durationMS(input.Schedule.CooldownNeutralBaseMS, 30*time.Second)
	maxDelay := durationMS(input.Schedule.CooldownNeutralMaxMS, 15*time.Minute)
	backoff := durationMS(input.Schedule.CooldownFailureBackoffMS, 5*time.Minute)
	switch outcome.Outcome {
	case OutcomeSuccess:
		next := observed.Add(durationMS(input.Schedule.HealthIntervalMS, time.Hour) + stableJitter(input.AccountID, input.Schedule.HealthJitterMS))
		outcome.NextDueAt = &next
		outcome.FailureCount = 0
		outcome.AccountStatus = "active"
		outcome.Projection = &Projection{TargetAccountID: input.AccountID, TransitionKind: "cooldown_success", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: expectedStatus, ExpectedCooldownFence: fence, Values: map[string]any{"last_health_check_at": observed.Format(time.RFC3339Nano), "last_health_success_at": observed.Format(time.RFC3339Nano), "last_health_check_status_code": outcome.StatusCode}}
	case OutcomeNeutral, OutcomeTaskFailed:
		next := observed.Add(stableCooldownDefer(input.AccountID, prior.FailureCount, base, maxDelay))
		outcome.NextDueAt = &next
		outcome.FailureCount = prior.FailureCount
		outcome.FailureStartedAt = prior.FailureStartedAt
		outcome.AccountStatus = "temporary_unavailable"
		outcome.CooldownFence = fence
		outcome.Projection = &Projection{TargetAccountID: input.AccountID, TransitionKind: "cooldown_defer", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: expectedStatus, ExpectedCooldownFence: fence, CooldownFence: fence}
	case OutcomeUpstreamFailed:
		failures := prior.FailureCount + 1
		next := observed.Add(backoff)
		outcome.NextDueAt = &next
		outcome.FailureCount = failures
		outcome.AccountStatus = "temporary_unavailable"
		outcome.CooldownFence = fence
		outcome.Projection = &Projection{TargetAccountID: input.AccountID, TransitionKind: "cooldown_failure", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: expectedStatus, ExpectedCooldownFence: fence, CooldownFence: fence}
	default:
		outcome.AccountStatus = prior.AccountStatus
	}
}

func healthProjection(input Input, transition, expectedStatus string, expectedFence *CooldownFence, observed time.Time, outcome *Outcome, failures int) *Projection {
	return &Projection{TargetAccountID: input.AccountID, TransitionKind: transition, InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: expectedStatus, ExpectedCooldownFence: expectedFence, Values: map[string]any{"last_health_check_at": observed.Format(time.RFC3339Nano), "last_health_check_status_code": outcome.StatusCode, "last_health_check_error_code": outcome.ErrorCode, "last_health_check_error_message": outcome.ErrorMessage, "health_check_failure_count": failures}}
}

func validateScheduledInput(input Input, now time.Time) error {
	if strings.TrimSpace(input.AccountID) == "" || input.InputVersion < 1 || input.ConfigRevision < 1 || input.DispatchRevision < 1 {
		return errors.New("input version 或账户 fence 无效")
	}
	if input.IssuedAt.IsZero() || input.ExpiresAt.IsZero() || !input.ExpiresAt.After(now) {
		return errors.New("input 已过期或缺少时间 fence")
	}
	if input.Schedule.HealthIntervalMS < 60_000 || input.Schedule.HealthIntervalMS > maxScheduleMilliseconds || input.Schedule.HealthJitterMS < 0 || input.Schedule.HealthJitterMS > maxScheduleMilliseconds || input.Schedule.HealthJitterMS > input.Schedule.HealthIntervalMS || input.Schedule.FailureThreshold < 1 || input.Schedule.FailureRetryMS < 3_000 || input.Schedule.FailureRetryMS > maxScheduleMilliseconds || input.Schedule.CooldownNeutralBaseMS < 0 || input.Schedule.CooldownNeutralBaseMS > maxScheduleMilliseconds || input.Schedule.CooldownNeutralMaxMS < 0 || input.Schedule.CooldownNeutralMaxMS > maxScheduleMilliseconds || input.Schedule.CooldownFailureBackoffMS < 0 || input.Schedule.CooldownFailureBackoffMS > maxScheduleMilliseconds {
		return errors.New("input schedule 无效")
	}
	if !input.Eligibility.BoundGroup || !input.Eligibility.AuthorizationEligible {
		return errors.New("input eligibility 缺少绑定或授权证据")
	}
	if input.Eligibility.AccountStatus != "active" && input.Eligibility.AccountStatus != "pending_test" && input.Eligibility.AccountStatus != "temporary_unavailable" && input.Eligibility.AccountStatus != "rate_limited" {
		return errors.New("input account status 不可调度")
	}
	if input.Eligibility.AccountStatus == "temporary_unavailable" || input.Eligibility.AccountStatus == "rate_limited" {
		if input.Eligibility.CooldownUntil == nil || input.Eligibility.CooldownUntil.IsZero() {
			return errors.New("cooldown input 缺少 cooldown_until")
		}
		if !validCooldownFence(input.Cooldown, input) {
			return errors.New("cooldown input 缺少或不匹配五元 fence")
		}
	}
	return nil
}

func validCooldownFence(fence *CooldownFence, input Input) bool {
	if fence == nil || fence.ObservationStartedAt.IsZero() || strings.TrimSpace(fence.Generation) == "" {
		return false
	}
	if input.Eligibility.SourceConfigRevision == nil {
		return fence.SourceConfigRevision == nil
	}
	return fence.SourceConfigRevision != nil && *fence.SourceConfigRevision == *input.Eligibility.SourceConfigRevision
}

func inputEligible(input Input) bool {
	// pending_test is the activation probe state and is intentionally allowed
	// to carry schedulable=false until the first successful probe activates it.
	return (input.Eligibility.Schedulable || input.Eligibility.AccountStatus == "pending_test") &&
		input.Eligibility.BoundGroup &&
		input.Eligibility.AuthorizationEligible
}

func scheduledRequestID(input Input, kind string, due time.Time) string {
	value := sha256.Sum256([]byte(strings.Join([]string{input.AccountID, fmt.Sprintf("%d", input.InputVersion), fmt.Sprintf("%d", input.ConfigRevision), fmt.Sprintf("%d", input.DispatchRevision), kind, due.UTC().Format(time.RFC3339Nano)}, "\n")))
	return "account-health-" + hex.EncodeToString(value[:])
}

func stableJitter(accountID string, maximumMS int64) time.Duration {
	if maximumMS <= 0 {
		return 0
	}
	if maximumMS > maxScheduleMilliseconds {
		maximumMS = maxScheduleMilliseconds
	}
	value := sha256.Sum256([]byte(accountID))
	n := uint64(value[0])<<56 | uint64(value[1])<<48 | uint64(value[2])<<40 | uint64(value[3])<<32 | uint64(value[4])<<24 | uint64(value[5])<<16 | uint64(value[6])<<8 | uint64(value[7])
	return time.Duration(n%(uint64(maximumMS)+1)) * time.Millisecond
}

func stableCooldownDefer(accountID string, failures int, base, maximum time.Duration) time.Duration {
	if failures < 0 {
		failures = 0
	}
	if base < 0 {
		base = 0
	}
	if base > maxScheduleDuration {
		base = maxScheduleDuration
	}
	if maximum < 0 {
		maximum = 0
	}
	if maximum > maxScheduleDuration {
		maximum = maxScheduleDuration
	}
	delay := base
	for step := 0; step < failures && delay < maximum; step++ {
		if delay > maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	if delay > maximum {
		delay = maximum
	}
	return delay + stableJitter(accountID+fmt.Sprintf(":%d", failures), int64(delay/time.Millisecond)/5) - delay/10
}

func durationMS(value int64, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	if value > maxScheduleMilliseconds {
		return maxScheduleDuration
	}
	return time.Duration(value) * time.Millisecond
}

func ptrTime(value time.Time) *time.Time { return &value }

func (r *Runner) setOwnerHeld(value bool) {
	r.mu.Lock()
	r.status.OwnerHeld = value
	r.mu.Unlock()
}

func (r *Runner) setScan(value time.Time) {
	r.mu.Lock()
	r.status.LastScanAt = value
	r.mu.Unlock()
}

func (r *Runner) setSuccess(inputs, executed int) {
	r.mu.Lock()
	r.status.LastSuccess = r.cfg.Now().UTC()
	r.status.LastError = ""
	r.status.Inputs = inputs
	r.status.Executed += executed
	r.mu.Unlock()
}

func (r *Runner) setError(err error) {
	r.mu.Lock()
	r.status.LastError = err.Error()
	r.mu.Unlock()
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}
