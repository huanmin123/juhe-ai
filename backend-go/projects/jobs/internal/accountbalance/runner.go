package accountbalance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/schedulejitter"
)

type RunnerConfig struct {
	Store            *Store
	OwnerID          string
	OwnerLeaseTTL    time.Duration
	AccountLeaseTTL  time.Duration
	InputTTL         time.Duration
	MaxConcurrent    int
	CredentialSecret string
	HTTPClient       HTTPDoer
	ProbeTimeout     time.Duration
	MaxResponseBytes int64
	Now              func() time.Time
}

type Runner struct {
	store            *Store
	ownerID          string
	ownerLeaseTTL    time.Duration
	accountLeaseTTL  time.Duration
	inputTTL         time.Duration
	maxConcurrent    int
	credentialSecret string
	httpClient       HTTPDoer
	probeTimeout     time.Duration
	maxResponseBytes int64
	now              func() time.Time
}

func NewRunner(config RunnerConfig) (*Runner, error) {
	if config.Store == nil || strings.TrimSpace(config.OwnerID) == "" || strings.TrimSpace(config.CredentialSecret) == "" {
		return nil, errors.New("account-balance runner 缺少 store、owner ID 或 credential secret")
	}
	if config.OwnerLeaseTTL <= 0 {
		config.OwnerLeaseTTL = 30 * time.Second
	}
	if config.AccountLeaseTTL <= 0 {
		config.AccountLeaseTTL = 30 * time.Second
	}
	if config.InputTTL <= 0 || config.InputTTL > 15*time.Minute {
		config.InputTTL = 15 * time.Minute
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 4
	}
	if config.MaxConcurrent > 256 {
		return nil, errors.New("account-balance runner 最大并发不能超过 256")
	}
	if config.ProbeTimeout <= 0 || config.ProbeTimeout > defaultBalanceTimeout {
		config.ProbeTimeout = defaultBalanceTimeout
	}
	if config.MaxResponseBytes <= 0 || config.MaxResponseBytes > defaultMaxBodyBytes {
		config.MaxResponseBytes = defaultMaxBodyBytes
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Runner{store: config.Store, ownerID: config.OwnerID, ownerLeaseTTL: config.OwnerLeaseTTL, accountLeaseTTL: config.AccountLeaseTTL, inputTTL: config.InputTTL, maxConcurrent: config.MaxConcurrent, credentialSecret: config.CredentialSecret, httpClient: config.HTTPClient, probeTimeout: config.ProbeTimeout, maxResponseBytes: config.MaxResponseBytes, now: config.Now}, nil
}

// RunPeriodic executes a bounded batch of already-frozen due candidates.
func (r *Runner) RunPeriodic(ctx context.Context, candidates []Candidate) (RunReport, error) {
	return r.runCandidates(ctx, TriggerPeriodic, candidates)
}

// RunFirstProbe executes the one-time post-activation built-in detection
// candidates and appends outcomes for the Node projector.
func (r *Runner) RunFirstProbe(ctx context.Context, candidates []Candidate) (RunReport, error) {
	return r.runCandidates(ctx, TriggerFirstProbe, candidates)
}

// RunManual executes a single saved input.  It still uses the jobs owner and
// account fences, but never increments the scheduled transient-failure count.
func (r *Runner) RunManual(ctx context.Context, input Input) (RunReport, error) {
	input.Trigger = TriggerManual
	return r.runInputs(ctx, TriggerManual, []Input{input})
}

// Run is a small common entry point for callers that dispatch by mode.
func (r *Runner) Run(ctx context.Context, trigger Trigger, candidates []Candidate) (RunReport, error) {
	switch trigger {
	case TriggerPeriodic, TriggerFirstProbe:
		return r.runCandidates(ctx, trigger, candidates)
	default:
		return RunReport{}, errors.New("account-balance Run 仅接受 periodic 或 first_probe")
	}
}

func (r *Runner) runCandidates(ctx context.Context, trigger Trigger, candidates []Candidate) (RunReport, error) {
	result := RunReport{Trigger: trigger, Seen: len(candidates), Errors: make(map[string]error)}
	now := r.now().UTC()
	inputs := make([]Input, 0, len(candidates))
	for _, candidate := range candidates {
		if err := candidateEligible(candidate, trigger); err != nil {
			result.addError(candidate.AccountID, err)
			continue
		}
		input, err := candidate.ToInput(trigger, now, r.inputTTL)
		if err != nil {
			result.addError(candidate.AccountID, err)
			continue
		}
		inputs = append(inputs, input)
	}
	result2, err := r.runInputs(ctx, trigger, inputs)
	result.Executed = result2.Executed
	result.Skipped = result2.Skipped
	for accountID, itemErr := range result2.Errors {
		result.addError(accountID, itemErr)
	}
	return result, err
}

func candidateEligible(candidate Candidate, trigger Trigger) error {
	if strings.TrimSpace(candidate.AccountID) == "" {
		return errors.New("account-balance candidate account ID 不能为空")
	}
	if candidate.Deleted || candidate.Authorized {
		return errors.New("account-balance candidate 已删除或属于授权实例")
	}
	keyCount := candidate.APIKeyCount
	if keyCount == 0 && (strings.TrimSpace(candidate.APIKey.Ciphertext) != "" || strings.TrimSpace(candidate.Credential.Ciphertext) != "") {
		keyCount = 1
	}
	if candidate.Type != "api_key" || keyCount != 1 {
		return errors.New("account-balance candidate 必须是单 API Key 物理账户")
	}
	switch trigger {
	case TriggerPeriodic:
		if !candidate.BalanceEnabled || candidate.Status != "active" || !candidate.Schedulable {
			return errors.New("account-balance 周期候选不可调度")
		}
	case TriggerFirstProbe:
		if !candidate.FirstProbe || candidate.BalanceEnabled || candidate.Status != "active" || !candidate.Schedulable {
			return errors.New("account-balance 首次探测候选不满足 active/schedulable/first-probe 条件")
		}
	case TriggerManual:
		return errors.New("account-balance manual 不接受 candidate")
	}
	return nil
}

func (r *Runner) runInputs(ctx context.Context, trigger Trigger, inputs []Input) (RunReport, error) {
	result := RunReport{Trigger: trigger, Seen: len(inputs), Errors: make(map[string]error)}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	owner, acquired, err := r.store.AcquireOwnerLease(ctx, r.ownerID, r.ownerLeaseTTL)
	if err != nil {
		return result, err
	}
	if !acquired {
		result.Skipped = len(inputs)
		return result, nil
	}
	defer func() { _ = r.store.ReleaseOwnerLease(context.Background(), owner) }()

	workers := r.maxConcurrent
	if workers > len(inputs) {
		workers = len(inputs)
	}
	if workers == 0 {
		return result, nil
	}
	jobs := make(chan Input)
	var wg sync.WaitGroup
	var executed atomic.Int64
	var skipped atomic.Int64
	var stale atomic.Int64
	var mu sync.Mutex
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for input := range jobs {
				state, itemErr := r.runInput(ctx, owner, input)
				switch state {
				case runStateExecuted:
					executed.Add(1)
				case runStateSkipped:
					skipped.Add(1)
				case runStateExecutedStale:
					stale.Add(1)
				}
				if itemErr != nil {
					mu.Lock()
					result.addError(input.AccountID, itemErr)
					mu.Unlock()
				}
			}
		}()
	}
	for _, input := range inputs {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			result.Executed = int(executed.Load())
			result.Skipped = int(skipped.Load())
			result.Stale = int(stale.Load())
			return result, ctx.Err()
		case jobs <- input:
		}
	}
	close(jobs)
	wg.Wait()
	result.Executed = int(executed.Load())
	result.Skipped = int(skipped.Load())
	result.Stale = int(stale.Load())
	return result, nil
}

type runState uint8

const (
	runStateSkipped runState = iota
	runStateExecuted
	runStateExecutedStale
)

func (r *Runner) runInput(ctx context.Context, owner OwnerLease, input Input) (runState, error) {
	if err := input.Validate(r.now().UTC()); err != nil {
		return runStateSkipped, err
	}
	if err := ctx.Err(); err != nil {
		return runStateSkipped, err
	}
	accountLease, acquired, err := r.store.AcquireAccountLease(ctx, owner, input.AccountID, r.accountLeaseTTL)
	if err != nil {
		if errors.Is(err, ErrAccountLeaseHeld) {
			return runStateSkipped, nil
		}
		return runStateSkipped, err
	}
	if !acquired {
		return runStateSkipped, nil
	}
	defer func() { _ = r.store.ReleaseAccountLease(context.Background(), owner, accountLease) }()
	query, queryErr := ExecuteBalanceQuery(ctx, input, QueryOptions{Secret: r.credentialSecret, Client: r.httpClient, Timeout: r.probeTimeout, MaxResponseBytes: r.maxResponseBytes, Now: r.now})
	if queryErr != nil {
		// Local setup/decryption errors are not upstream balance diagnostics.
		// Keep the original error visible and do not fabricate a snapshot.
		return runStateExecuted, queryErr
	}
	now := r.now().UTC()
	if !now.Before(input.ExpiresAt.UTC()) {
		return runStateExecuted, errors.New("account-balance input 在上游查询后已过期，拒绝写入")
	}
	prior, found, err := r.store.LoadSnapshot(ctx, input.AccountID)
	if err != nil {
		return runStateExecuted, err
	}
	snapshot := applyQueryResult(query, prior, found, input.Trigger, now)
	intervalMinutes := input.Config.IntervalMinutes
	if intervalMinutes == 0 {
		intervalMinutes = 5
	}
	next := now.Add(schedulejitter.Delay(time.Duration(intervalMinutes) * time.Minute))
	outcome := Outcome{OutcomeID: OutcomeIDForInput(input), RequestID: RequestIDForInput(input), AccountID: input.AccountID, InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, Trigger: input.Trigger, ObservedAt: now, Snapshot: snapshot, Adapter: query.Adapter, NextRefreshAt: &next, ErrorCode: query.ErrorCode, ErrorMessage: query.ErrorMessage}
	outcome.SystemAccountID = input.SystemAccountID
	if found {
		outcome.ExpectedSnapshotInput = prior.InputVersion
		outcome.ExpectedSnapshotConfig = prior.ConfigRevision
	}
	// expected_next_refresh_at is a Node business scheduling fence. Manual
	// refresh intentionally has no such fence, whereas periodic recovery must
	// preserve an expected null schedule in its serialized outcome.
	if input.Trigger != TriggerManual {
		outcome.ExpectedNextRefreshAt = cloneTime(input.NextRefreshAt)
		outcome.ExpectedNextRefreshSet = true
	}
	_, err = r.store.AppendOutcome(ctx, owner, accountLease, outcome)
	if errors.Is(err, ErrOutcomeStale) {
		return runStateExecutedStale, nil
	}
	if err != nil {
		return runStateExecuted, err
	}
	return runStateExecuted, nil
}

// OutcomeIDForInput returns the deterministic identity used by the runner.
// Manual callers use it to read back the exact committed outcome rather than
// returning whichever newer snapshot happens to be current.
func OutcomeIDForInput(input Input) string { return newBalanceID("outcome", inputIdentitySeed(input)) }
func RequestIDForInput(input Input) string { return newBalanceID("request", inputIdentitySeed(input)) }
func inputIdentitySeed(input Input) string {
	return fmt.Sprintf("%s\n%d\n%d\n%s\n%s", input.AccountID, input.InputVersion, input.ConfigRevision, input.Trigger, input.IssuedAt.UTC().Format(time.RFC3339Nano))
}

func applyQueryResult(query QueryResult, prior SnapshotRecord, found bool, trigger Trigger, now time.Time) Snapshot {
	if query.Snapshot.Status == StatusFresh || query.Snapshot.Status == StatusUnlimited {
		query.Snapshot.LastAttemptAt = now.Format(time.RFC3339Nano)
		query.Snapshot.LastSuccessAt = now.Format(time.RFC3339Nano)
		query.Snapshot.ConsecutiveTransientFails = 0
		query.Snapshot.LastTransientErrorMessage = ""
		query.Snapshot.LastTransientFailureAt = ""
		return query.Snapshot
	}
	if query.Snapshot.Status == StatusUnsupported && !query.Temporary {
		query.Snapshot.LastAttemptAt = now.Format(time.RFC3339Nano)
		return query.Snapshot
	}
	// Manual diagnostics are immediately visible but never participate in the
	// scheduled three-failure sequence.
	if trigger == TriggerManual {
		query.Snapshot.Status = StatusFailed
		query.Snapshot.RemainingUSD = ""
		query.Snapshot.RawRemaining = ""
		query.Snapshot.LastAttemptAt = now.Format(time.RFC3339Nano)
		return query.Snapshot
	}
	count := 1
	if found {
		count = prior.Snapshot.ConsecutiveTransientFails + 1
	}
	if count > 3 {
		count = 3
	}
	if count < 3 && found && prior.Snapshot.LastSuccessAt != "" {
		retained := prior.Snapshot
		retained.LastAttemptAt = now.Format(time.RFC3339Nano)
		retained.ConsecutiveTransientFails = count
		retained.LastTransientErrorMessage = query.ErrorMessage
		retained.LastTransientFailureAt = now.Format(time.RFC3339Nano)
		return retained
	}
	if count < 3 {
		return Snapshot{Status: StatusPending, LastAttemptAt: now.Format(time.RFC3339Nano), ConsecutiveTransientFails: count, LastTransientErrorMessage: query.ErrorMessage, LastTransientFailureAt: now.Format(time.RFC3339Nano)}
	}
	return Snapshot{Status: StatusFailed, LastAttemptAt: now.Format(time.RFC3339Nano), ConsecutiveTransientFails: count, LastTransientErrorMessage: query.ErrorMessage, LastTransientFailureAt: now.Format(time.RFC3339Nano), ErrorMessage: query.ErrorMessage}
}

func newBalanceID(prefix, seed string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s", prefix, seed)))
	return "account-balance-" + prefix + "-" + hex.EncodeToString(sum[:])
}
