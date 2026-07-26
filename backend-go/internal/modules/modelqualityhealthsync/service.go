package modelqualityhealthsync

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultClaimLimit      = port.ModelQualityHealthSyncClaimDefaultLimit
	MaximumClaimLimit      = port.ModelQualityHealthSyncClaimMaximumLimit
	DefaultLeaseDuration   = 5 * time.Minute
	DefaultWorkerCount     = 4
	MaximumWorkerCount     = 16
	DefaultClaimTimeout    = 15 * time.Second
	DefaultCompleteTimeout = 15 * time.Second
	CleanupTimeout         = 5 * time.Second

	ContextRetryDelay      = time.Second
	SQLTransientRetryDelay = 30 * time.Second
	DefaultRetryDelay      = time.Minute
	NotAppliedRetryDelay   = time.Hour

	ErrorClassContext        = "context_interrupted"
	ErrorClassSQLTransient   = "sql_transient"
	ErrorClassComplete       = "complete_failed"
	ErrorClassCompletionMiss = "completion_not_applied"
)

const (
	maximumBatchErrorReasonRunes    = 512
	maximumReleaseErrorMessageRunes = 1000
)

type RunOnceInput struct {
	OwnerID         port.ModelQualityClaimOwnerID
	ClaimLimit      int
	LeaseDuration   time.Duration
	WorkerCount     int
	ClaimTimeout    time.Duration
	CompleteTimeout time.Duration
}

// RunOnceResult contains counters only. Its memory use is independent of the
// size of individual durable facts and errors.
type RunOnceResult struct {
	Claimed       int
	Quarantined   int
	Completed     int
	Stale         int
	Released      int
	ReleaseStale  int
	Failed        int
	ReleaseFailed int
}

// BatchError deliberately keeps only the first bounded reason and a count.
// A batch of one hundred failures therefore cannot retain or render one
// hundred potentially large database errors.
type BatchError struct {
	Count          int
	FirstOperation string
	FirstReason    string
}

func (e *BatchError) Error() string {
	if e == nil || e.Count == 0 {
		return ""
	}
	if e.Count == 1 {
		return fmt.Sprintf("model quality health-sync %s failed: %s", e.FirstOperation, e.FirstReason)
	}
	return fmt.Sprintf("model quality health-sync batch had %d failures; first %s failure: %s", e.Count, e.FirstOperation, e.FirstReason)
}

type Service struct {
	claimer   port.ModelQualityHealthSyncClaimer
	completer port.ModelQualityHealthSyncCompleter
	releaser  port.ModelQualityHealthSyncReleaser
	now       func() time.Time
}

func NewService(
	claimer port.ModelQualityHealthSyncClaimer,
	completer port.ModelQualityHealthSyncCompleter,
	releaser port.ModelQualityHealthSyncReleaser,
) (*Service, error) {
	if isNilDependency(claimer) {
		return nil, fmt.Errorf("model quality health-sync claimer is required")
	}
	if isNilDependency(completer) {
		return nil, fmt.Errorf("model quality health-sync completer is required")
	}
	if isNilDependency(releaser) {
		return nil, fmt.Errorf("model quality health-sync releaser is required")
	}
	return &Service{claimer: claimer, completer: completer, releaser: releaser, now: time.Now}, nil
}

func isNilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func (s *Service) RunOnce(ctx context.Context, input RunOnceInput) (RunOnceResult, error) {
	if ctx == nil {
		return RunOnceResult{}, fmt.Errorf("model quality health-sync context is required")
	}
	if err := normalizeRunOnceInput(&input); err != nil {
		return RunOnceResult{}, err
	}

	claimCtx, cancelClaim := context.WithTimeout(ctx, input.ClaimTimeout)
	batch, claimErr := s.claimer.ClaimFailedModelQualityHealthSyncs(claimCtx, port.ModelQualityHealthSyncClaimInput{
		OwnerID:       input.OwnerID,
		LeaseDuration: input.LeaseDuration,
		Limit:         input.ClaimLimit,
	})
	cancelClaim()
	if claimErr != nil {
		errs := newBatchErrorBuilder()
		errs.add(0, "claim", claimErr)
		return RunOnceResult{}, errs.err()
	}

	result := RunOnceResult{Claimed: len(batch.Claims), Quarantined: batch.Quarantined}
	if len(batch.Claims) == 0 {
		return result, nil
	}

	workerCount := min(input.WorkerCount, len(batch.Claims))
	jobs := make(chan claimJob, len(batch.Claims))
	outcomes := make([]claimOutcome, len(batch.Claims))
	errs := newBatchErrorBuilder()
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for job := range jobs {
				outcome, completeErr, releaseErr := s.processClaim(ctx, job.claim, input.CompleteTimeout)
				outcomes[job.index] = outcome
				errs.add(job.index*2, "complete", completeErr)
				errs.add(job.index*2+1, "release", releaseErr)
			}
		}()
	}
	for index, claim := range batch.Claims {
		jobs <- claimJob{index: index, claim: claim}
	}
	close(jobs)
	workers.Wait()

	for _, outcome := range outcomes {
		result.Completed += outcome.completed
		result.Stale += outcome.stale
		result.Released += outcome.released
		result.ReleaseStale += outcome.releaseStale
		result.Failed += outcome.failed
		result.ReleaseFailed += outcome.releaseFailed
	}
	return result, errs.err()
}

func ValidateRunOnceInput(input RunOnceInput) error {
	return normalizeRunOnceInput(&input)
}

func normalizeRunOnceInput(input *RunOnceInput) error {
	if input.ClaimLimit == 0 {
		input.ClaimLimit = DefaultClaimLimit
	}
	if input.LeaseDuration == 0 {
		input.LeaseDuration = DefaultLeaseDuration
	}
	if input.WorkerCount == 0 {
		input.WorkerCount = DefaultWorkerCount
	}
	if input.ClaimTimeout == 0 {
		input.ClaimTimeout = DefaultClaimTimeout
	}
	if input.CompleteTimeout == 0 {
		input.CompleteTimeout = DefaultCompleteTimeout
	}

	owner := string(input.OwnerID)
	if owner == "" || len(owner) > 128 || strings.TrimSpace(owner) != owner || !utf8.ValidString(owner) {
		return fmt.Errorf("model quality health-sync owner id is invalid")
	}
	for _, value := range owner {
		if unicode.IsControl(value) {
			return fmt.Errorf("model quality health-sync owner id is invalid")
		}
	}
	if input.ClaimLimit < 1 || input.ClaimLimit > MaximumClaimLimit {
		return fmt.Errorf("model quality health-sync claim limit is invalid")
	}
	if input.LeaseDuration < port.ModelQualityClaimMinimumLease ||
		input.LeaseDuration > port.ModelQualityClaimMaximumLease ||
		input.LeaseDuration%time.Millisecond != 0 {
		return fmt.Errorf("model quality health-sync lease duration is invalid")
	}
	if input.WorkerCount < 1 || input.WorkerCount > MaximumWorkerCount {
		return fmt.Errorf("model quality health-sync worker count is invalid")
	}
	if !validOperationTimeout(input.ClaimTimeout) {
		return fmt.Errorf("model quality health-sync claim timeout is invalid")
	}
	if !validOperationTimeout(input.CompleteTimeout) || input.CompleteTimeout >= input.LeaseDuration {
		return fmt.Errorf("model quality health-sync complete timeout is invalid")
	}
	// A lease is acquired for the complete batch. Reserve one cleanup window
	// per worker batch so the final claims cannot be guaranteed stale before
	// their first completion attempt. Callers using the maximum claim limit
	// must opt into a larger fixed pool or a shorter completion timeout.
	workerBatches := (input.ClaimLimit + input.WorkerCount - 1) / input.WorkerCount
	perBatchBudget := input.CompleteTimeout + CleanupTimeout
	if workerBatches > 0 && perBatchBudget > 0 && time.Duration(workerBatches) >= input.LeaseDuration/perBatchBudget {
		return fmt.Errorf("model quality health-sync batch cannot complete within its lease")
	}
	return nil
}

func validOperationTimeout(value time.Duration) bool {
	return value >= time.Millisecond && value <= port.ModelQualityClaimMaximumLease && value%time.Millisecond == 0
}

type claimOutcome struct {
	completed     int
	stale         int
	released      int
	releaseStale  int
	failed        int
	releaseFailed int
}

type claimJob struct {
	index int
	claim port.ModelQualityHealthSyncClaim
}

func (s *Service) processClaim(
	ctx context.Context,
	claim port.ModelQualityHealthSyncClaim,
	timeout time.Duration,
) (claimOutcome, error, error) {
	completeCtx, cancelComplete := context.WithTimeout(ctx, timeout)
	completion, completeErr := safeComplete(s.completer, completeCtx, port.ModelQualityHealthSyncCompleteInput{
		Claim:       claim,
		CompletedAt: s.now().UTC(),
	})
	cancelComplete()
	if completeErr == nil {
		if completion.Applied {
			return claimOutcome{completed: 1}, nil, nil
		}
		outcome := claimOutcome{stale: 1}
		released, releaseErr := s.releaseClaim(ctx, claim, NotAppliedRetryDelay, ErrorClassCompletionMiss, "completion was not applied")
		if releaseErr != nil {
			outcome.releaseFailed = 1
			return outcome, nil, releaseErr
		}
		if released {
			outcome.released = 1
		} else {
			outcome.releaseStale = 1
		}
		return outcome, nil, nil
	}

	outcome := claimOutcome{failed: 1}
	errorClass, retryDelay := classifyCompleteError(completeErr)
	released, releaseErr := s.releaseClaim(ctx, claim, retryDelay, errorClass, safeErrorText(completeErr))
	if releaseErr != nil {
		outcome.releaseFailed = 1
		return outcome, completeErr, releaseErr
	}
	if released {
		outcome.released = 1
	} else {
		outcome.releaseStale = 1
	}
	return outcome, completeErr, nil
}

func (s *Service) releaseClaim(
	ctx context.Context,
	claim port.ModelQualityHealthSyncClaim,
	retryDelay time.Duration,
	errorClass string,
	errorMessage string,
) (bool, error) {
	// Release is a bounded compensating action and must remain possible after
	// the parent context is cancelled. WithoutCancel retains request values for
	// observability while the independent timeout prevents background leakage.
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), CleanupTimeout)
	defer cancelCleanup()
	return safeRelease(s.releaser, cleanupCtx, port.ModelQualityHealthSyncReleaseInput{
		RunID:        claim.RunID,
		Lease:        claim.Lease,
		RetryDelay:   retryDelay,
		ErrorClass:   errorClass,
		ErrorMessage: sanitizeReleaseErrorMessage(errorMessage),
	})
}

func safeComplete(
	completer port.ModelQualityHealthSyncCompleter,
	ctx context.Context,
	input port.ModelQualityHealthSyncCompleteInput,
) (result port.ModelQualityHealthSyncCompleteResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = port.ModelQualityHealthSyncCompleteResult{}
			err = fmt.Errorf("complete model quality health-sync panicked: %s", recoveredText(recovered))
		}
	}()
	return completer.CompleteModelQualityHealthSync(ctx, input)
}

func safeRelease(
	releaser port.ModelQualityHealthSyncReleaser,
	ctx context.Context,
	input port.ModelQualityHealthSyncReleaseInput,
) (released bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			released = false
			err = fmt.Errorf("release model quality health-sync panicked: %s", recoveredText(recovered))
		}
	}()
	return releaser.ReleaseModelQualityHealthSync(ctx, input)
}

type sqlStateError interface {
	SQLState() string
}

func classifyCompleteError(err error) (class string, retryDelay time.Duration) {
	class, retryDelay = ErrorClassComplete, DefaultRetryDelay
	defer func() {
		if recover() != nil {
			class, retryDelay = ErrorClassComplete, DefaultRetryDelay
		}
	}()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrorClassContext, ContextRetryDelay
	}
	var stateErr sqlStateError
	if errors.As(err, &stateErr) {
		state := stateErr.SQLState()
		if len(state) >= 2 {
			switch state[:2] {
			case "08", "40", "53", "57":
				return ErrorClassSQLTransient, SQLTransientRetryDelay
			}
		}
	}
	return class, retryDelay
}

type batchErrorBuilder struct {
	mu             sync.Mutex
	count          int
	firstSequence  int
	firstOperation string
	firstReason    string
}

func newBatchErrorBuilder() *batchErrorBuilder {
	return &batchErrorBuilder{firstSequence: int(^uint(0) >> 1)}
}

func (b *batchErrorBuilder) add(sequence int, operation string, err error) {
	if err == nil {
		return
	}
	reason := boundedReason(safeErrorText(err))
	b.mu.Lock()
	defer b.mu.Unlock()
	b.count++
	if sequence >= b.firstSequence {
		return
	}
	b.firstSequence = sequence
	b.firstOperation = operation
	b.firstReason = reason
}

func (b *batchErrorBuilder) err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.count == 0 {
		return nil
	}
	return &BatchError{Count: b.count, FirstOperation: b.firstOperation, FirstReason: b.firstReason}
}

func boundedReason(value string) string {
	if value == "" {
		return "unknown error"
	}
	runeCount := 0
	for index := range value {
		if runeCount == maximumBatchErrorReasonRunes {
			// Clone detaches the retained reason from a potentially very large
			// backing string owned by the adapter error.
			return strings.Clone(value[:index])
		}
		runeCount++
	}
	return strings.Clone(value)
}

func sanitizeReleaseErrorMessage(value string) string {
	if value == "" {
		return "unknown error"
	}
	if utf8.ValidString(value) && strings.IndexByte(value, 0) < 0 {
		runeCount := 0
		for index := range value {
			if runeCount == maximumReleaseErrorMessageRunes {
				return strings.Clone(value[:index])
			}
			runeCount++
		}
		return strings.Clone(value)
	}

	var sanitized strings.Builder
	sanitized.Grow(min(len(value), maximumReleaseErrorMessageRunes))
	runeCount := 0
	for _, valueRune := range value {
		if valueRune == 0 {
			continue
		}
		if runeCount == maximumReleaseErrorMessageRunes {
			break
		}
		sanitized.WriteRune(valueRune)
		runeCount++
	}
	if sanitized.Len() == 0 {
		return "unknown error"
	}
	return sanitized.String()
}

func safeErrorText(err error) (message string) {
	if err == nil {
		return "unknown error"
	}
	message = "error message unavailable"
	defer func() {
		if recover() != nil {
			message = "error message unavailable"
		}
	}()
	if value := err.Error(); value != "" {
		message = value
	}
	return message
}

func recoveredText(value any) string {
	switch recovered := value.(type) {
	case string:
		return boundedReason(recovered)
	case error:
		return boundedReason(safeErrorText(recovered))
	default:
		return "panic value unavailable"
	}
}
