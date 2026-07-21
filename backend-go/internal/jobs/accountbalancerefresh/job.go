package accountbalancerefresh

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultWindowSize       = 24
	DefaultWorkerLimit      = 12
	DefaultRunBudget        = 45 * time.Second
	DefaultCandidateTimeout = 20 * time.Second
	MaxWindowSize           = 256
)

type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomePartial Outcome = "partial"
)

type Summary struct {
	Outcome               Outcome
	SelectedCount         int
	ProcessedCount        int
	DeferredCount         int
	CandidateFailureCount int
	Duration              time.Duration
}

type RuntimeAvailabilityFilter interface {
	Filter(ctx context.Context, candidates []port.AccountBalanceRefreshCandidate) (filtered []port.AccountBalanceRefreshCandidate, available bool, err error)
}

type Dependencies struct {
	Store            port.AccountBalanceRefreshJobReader
	RuntimeFilter    RuntimeAvailabilityFilter
	RefreshCandidate func(context.Context, port.AccountBalanceRefreshCandidate) error
	WindowSize       int
	WorkerLimit      int
	RunBudget        time.Duration
	CandidateTimeout time.Duration
	Now              func() time.Time
}

type candidateTimeoutError struct {
	accountID string
}

func (e candidateTimeoutError) Error() string {
	return fmt.Sprintf("account balance refresh candidate %q timed out", e.accountID)
}

func (candidateTimeoutError) Unwrap() error {
	return context.DeadlineExceeded
}

func Run(ctx context.Context, dependencies Dependencies) (Summary, error) {
	startedAt := now(dependencies.Now)
	if dependencies.Store == nil {
		return Summary{}, fmt.Errorf("account balance refresh store is required")
	}
	windowSize := boundedPositiveInt(dependencies.WindowSize, DefaultWindowSize, MaxWindowSize)
	recovery, err := dependencies.Store.ListAccountBalanceRefreshRecoveryCandidates(ctx, windowSize)
	if err != nil {
		return Summary{}, fmt.Errorf("list account balance refresh recovery candidates: %w", err)
	}
	if len(recovery) > windowSize {
		recovery = recovery[:windowSize]
	}
	dueLimit := windowSize - len(recovery)
	due := []port.AccountBalanceRefreshCandidate(nil)
	if dueLimit > 0 {
		due, err = dependencies.Store.ListAccountBalanceRefreshDueCandidates(ctx, startedAt, dueLimit)
		if err != nil {
			return Summary{}, fmt.Errorf("list account balance refresh due candidates: %w", err)
		}
		if len(due) > dueLimit {
			due = due[:dueLimit]
		}
	}
	selected := append(append(make([]port.AccountBalanceRefreshCandidate, 0, len(recovery)+len(due)), recovery...), due...)
	candidates, runtimeDeferred, err := filterRuntimeCandidates(ctx, dependencies.RuntimeFilter, selected)
	if err != nil {
		return Summary{}, fmt.Errorf("filter account balance refresh runtime availability: %w", err)
	}
	if len(candidates) > 0 && dependencies.RefreshCandidate == nil {
		return Summary{}, fmt.Errorf("account balance refresh candidate runner is required")
	}

	runBudget := boundedPositiveDuration(dependencies.RunBudget, DefaultRunBudget)
	candidateTimeout := boundedPositiveDuration(dependencies.CandidateTimeout, DefaultCandidateTimeout)
	runCtx, cancel := context.WithTimeout(ctx, runBudget)
	defer cancel()

	workerLimit := boundedPositiveInt(dependencies.WorkerLimit, DefaultWorkerLimit, windowSize)
	workerCount := min(workerLimit, len(candidates))
	var cursor atomic.Int64
	var processed atomic.Int64
	var failed atomic.Int64
	var taskFailed atomic.Bool
	var taskFailure error
	var taskFailureOnce sync.Once
	done := make(chan struct{}, workerCount)
	for range workerCount {
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				if runCtx.Err() != nil || taskFailed.Load() {
					return
				}
				index := int(cursor.Add(1) - 1)
				if index >= len(candidates) {
					return
				}
				candidateCtx, candidateCancel := context.WithTimeout(runCtx, candidateTimeout)
				err := runCandidate(candidateCtx, dependencies.RefreshCandidate, candidates[index])
				candidateCancel()
				if err != nil {
					var timeoutErr candidateTimeoutError
					if errors.As(err, &timeoutErr) {
						failed.Add(1)
						continue
					}
					taskFailureOnce.Do(func() {
						taskFailure = fmt.Errorf("refresh account balance candidate %q: %w", candidates[index].ID, err)
						taskFailed.Store(true)
						cancel()
					})
					return
				}
				processed.Add(1)
			}
		}()
	}
	for range workerCount {
		<-done
	}
	if taskFailure != nil {
		return Summary{}, taskFailure
	}
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	claimed := min(int(cursor.Load()), len(candidates))
	failureCount := int(failed.Load())
	result := Summary{
		Outcome:               OutcomeSuccess,
		SelectedCount:         len(selected),
		ProcessedCount:        int(processed.Load()),
		DeferredCount:         runtimeDeferred + len(candidates) - claimed,
		CandidateFailureCount: failureCount,
		Duration:              max(0, now(dependencies.Now).Sub(startedAt)),
	}
	if failureCount > 0 {
		result.Outcome = OutcomePartial
	}
	return result, nil
}

func filterRuntimeCandidates(ctx context.Context, filter RuntimeAvailabilityFilter, selected []port.AccountBalanceRefreshCandidate) ([]port.AccountBalanceRefreshCandidate, int, error) {
	if filter == nil || len(selected) == 0 {
		return selected, 0, nil
	}
	filtered, available, err := filter.Filter(ctx, selected)
	if err != nil {
		return nil, 0, err
	}
	if !available {
		return selected, 0, nil
	}
	keep := make(map[string]struct{}, len(filtered))
	for _, candidate := range filtered {
		keep[candidate.ID] = struct{}{}
	}
	output := make([]port.AccountBalanceRefreshCandidate, 0, len(filtered))
	for _, candidate := range selected {
		if _, ok := keep[candidate.ID]; ok {
			output = append(output, candidate)
		}
	}
	return output, len(selected) - len(output), nil
}

func runCandidate(ctx context.Context, refresh func(context.Context, port.AccountBalanceRefreshCandidate) error, candidate port.AccountBalanceRefreshCandidate) (err error) {
	result := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				result <- fmt.Errorf("account balance refresh candidate panic: %v", recovered)
			}
		}()
		result <- refresh(ctx, candidate)
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return candidateTimeoutError{accountID: candidate.ID}
		}
		return ctx.Err()
	}
}

func boundedPositiveInt(value int, fallback int, maximum int) int {
	if value <= 0 {
		value = fallback
	}
	return min(value, maximum)
}

func boundedPositiveDuration(value time.Duration, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func now(clock func() time.Time) time.Time {
	if clock == nil {
		return time.Now().UTC()
	}
	return clock().UTC()
}
