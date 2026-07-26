package modelqualityhealthsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/store/port"
)

func TestRunOnceUsesDefaultsAndFixedWorkerPool(t *testing.T) {
	claims := makeClaims(12)
	var active atomic.Int32
	var maximum atomic.Int32
	store := &healthSyncStoreStub{claims: claims}
	store.complete = func(context.Context, port.ModelQualityHealthSyncCompleteInput) (port.ModelQualityHealthSyncCompleteResult, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		return port.ModelQualityHealthSyncCompleteResult{Applied: true}, nil
	}
	service, err := NewService(store, store, store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	result, runErr := service.RunOnce(context.Background(), RunOnceInput{OwnerID: "worker-1"})
	if runErr != nil {
		t.Fatalf("RunOnce() error = %v", runErr)
	}
	if result.Claimed != len(claims) || result.Completed != len(claims) || result.Failed != 0 {
		t.Fatalf("RunOnce() result = %+v", result)
	}
	if maximum.Load() > DefaultWorkerCount || maximum.Load() < 2 {
		t.Fatalf("maximum concurrent completions = %d, want 2..%d", maximum.Load(), DefaultWorkerCount)
	}
	if store.claimInput.Limit != DefaultClaimLimit || store.claimInput.LeaseDuration != DefaultLeaseDuration || store.claimInput.OwnerID != "worker-1" {
		t.Fatalf("claim input = %+v", store.claimInput)
	}
}

func TestRunOnceCancellationStillAttemptsEveryCompleteAndCleanupRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	claims := makeClaims(7)
	store := &healthSyncStoreStub{claims: claims}
	store.afterClaim = cancel
	store.complete = func(callCtx context.Context, _ port.ModelQualityHealthSyncCompleteInput) (port.ModelQualityHealthSyncCompleteResult, error) {
		if !errors.Is(callCtx.Err(), context.Canceled) {
			return port.ModelQualityHealthSyncCompleteResult{}, fmt.Errorf("complete context was not cancelled")
		}
		return port.ModelQualityHealthSyncCompleteResult{}, callCtx.Err()
	}
	store.release = func(callCtx context.Context, input port.ModelQualityHealthSyncReleaseInput) (bool, error) {
		if callCtx.Err() != nil {
			return false, fmt.Errorf("cleanup context inherited cancellation: %w", callCtx.Err())
		}
		if input.RetryDelay != ContextRetryDelay || input.ErrorClass != ErrorClassContext {
			return false, fmt.Errorf("release classification = %q/%s", input.ErrorClass, input.RetryDelay)
		}
		return true, nil
	}
	service, _ := NewService(store, store, store)

	result, runErr := service.RunOnce(ctx, RunOnceInput{OwnerID: "worker-1", WorkerCount: 3})
	if runErr == nil {
		t.Fatal("RunOnce() error = nil")
	}
	if store.completeCalls.Load() != int32(len(claims)) || store.releaseCalls.Load() != int32(len(claims)) {
		t.Fatalf("complete calls=%d release calls=%d, want %d each", store.completeCalls.Load(), store.releaseCalls.Load(), len(claims))
	}
	if result.Failed != len(claims) || result.Released != len(claims) || result.ReleaseFailed != 0 {
		t.Fatalf("RunOnce() result = %+v", result)
	}
	wantByRunID := make(map[string]port.ModelQualityHealthSyncLease, len(claims))
	for _, claim := range claims {
		wantByRunID[claim.RunID] = claim.Lease
	}
	for index, release := range store.releaseInputs() {
		wantLease, ok := wantByRunID[release.RunID]
		if !ok || release.Lease != wantLease {
			t.Fatalf("release[%d] fence = %+v, want lease=%+v found=%t", index, release, wantLease, ok)
		}
	}
}

func TestRunOnceClassifiesSQLStateAndRecordsReleaseCASStale(t *testing.T) {
	claim := makeClaims(1)[0]
	store := &healthSyncStoreStub{claims: []port.ModelQualityHealthSyncClaim{claim}}
	store.complete = func(context.Context, port.ModelQualityHealthSyncCompleteInput) (port.ModelQualityHealthSyncCompleteResult, error) {
		return port.ModelQualityHealthSyncCompleteResult{}, fmt.Errorf("wrapped: %w", sqlStateStub{state: "40001"})
	}
	store.release = func(_ context.Context, input port.ModelQualityHealthSyncReleaseInput) (bool, error) {
		if input.RetryDelay != SQLTransientRetryDelay || input.ErrorClass != ErrorClassSQLTransient {
			t.Fatalf("release classification = %q/%s", input.ErrorClass, input.RetryDelay)
		}
		if input.RunID != claim.RunID || input.Lease != claim.Lease {
			t.Fatalf("release fence = %+v", input)
		}
		return false, nil
	}
	service, _ := NewService(store, store, store)

	result, runErr := service.RunOnce(context.Background(), RunOnceInput{OwnerID: "worker-1"})
	if runErr == nil || result.Failed != 1 || result.ReleaseStale != 1 || result.Released != 0 {
		t.Fatalf("RunOnce() result=%+v error=%v", result, runErr)
	}
	var batchErr *BatchError
	if !errors.As(runErr, &batchErr) || batchErr.Count != 1 || batchErr.FirstOperation != "complete" {
		t.Fatalf("RunOnce() error = %#v", runErr)
	}
}

func TestRunOnceTreatsCompleteCASFalseAsStaleAndAttemptsBackoffRelease(t *testing.T) {
	store := &healthSyncStoreStub{claims: makeClaims(1)}
	store.complete = func(context.Context, port.ModelQualityHealthSyncCompleteInput) (port.ModelQualityHealthSyncCompleteResult, error) {
		return port.ModelQualityHealthSyncCompleteResult{Applied: false}, nil
	}
	store.release = func(_ context.Context, input port.ModelQualityHealthSyncReleaseInput) (bool, error) {
		if input.ErrorClass != ErrorClassCompletionMiss || input.RetryDelay != NotAppliedRetryDelay || input.ErrorMessage != "completion was not applied" {
			t.Fatalf("release input = %+v", input)
		}
		return false, nil
	}
	service, _ := NewService(store, store, store)

	result, runErr := service.RunOnce(context.Background(), RunOnceInput{OwnerID: "worker-1"})
	if runErr != nil || result.Stale != 1 || result.Completed != 0 || result.ReleaseStale != 1 || store.releaseCalls.Load() != 1 {
		t.Fatalf("RunOnce() result=%+v error=%v release calls=%d", result, runErr, store.releaseCalls.Load())
	}
}

func TestRunOnceSanitizesReleaseMessageAtUnicodeBoundary(t *testing.T) {
	store := &healthSyncStoreStub{claims: makeClaims(1)}
	unsafeReason := "prefix\x00" + strings.Repeat("界", maximumReleaseErrorMessageRunes+50)
	store.complete = func(context.Context, port.ModelQualityHealthSyncCompleteInput) (port.ModelQualityHealthSyncCompleteResult, error) {
		return port.ModelQualityHealthSyncCompleteResult{}, errors.New(unsafeReason)
	}
	store.release = func(_ context.Context, input port.ModelQualityHealthSyncReleaseInput) (bool, error) {
		if strings.ContainsRune(input.ErrorMessage, 0) || !utf8.ValidString(input.ErrorMessage) {
			t.Fatalf("release message is unsafe: %q", input.ErrorMessage)
		}
		if utf8.RuneCountInString(input.ErrorMessage) != maximumReleaseErrorMessageRunes || len(input.ErrorMessage) >= 64<<10 {
			t.Fatalf("release message runes=%d bytes=%d", utf8.RuneCountInString(input.ErrorMessage), len(input.ErrorMessage))
		}
		return true, nil
	}
	service, _ := NewService(store, store, store)

	result, runErr := service.RunOnce(context.Background(), RunOnceInput{OwnerID: "worker-1"})
	if runErr == nil || result.Failed != 1 || result.Released != 1 || result.ReleaseFailed != 0 {
		t.Fatalf("RunOnce() result=%+v error=%v", result, runErr)
	}
}

func TestRunOnceBoundsBatchErrorInsteadOfJoiningEveryCause(t *testing.T) {
	store := &healthSyncStoreStub{claims: makeClaims(MaximumClaimLimit)}
	largeReason := strings.Repeat("x", 10000)
	store.complete = func(context.Context, port.ModelQualityHealthSyncCompleteInput) (port.ModelQualityHealthSyncCompleteResult, error) {
		return port.ModelQualityHealthSyncCompleteResult{}, errors.New(largeReason)
	}
	store.release = func(context.Context, port.ModelQualityHealthSyncReleaseInput) (bool, error) {
		return true, nil
	}
	service, _ := NewService(store, store, store)

	result, runErr := service.RunOnce(context.Background(), RunOnceInput{OwnerID: "worker-1", ClaimLimit: MaximumClaimLimit, WorkerCount: MaximumWorkerCount})
	var batchErr *BatchError
	if !errors.As(runErr, &batchErr) {
		t.Fatalf("RunOnce() error = %T %v", runErr, runErr)
	}
	if batchErr.Count != MaximumClaimLimit || len([]rune(batchErr.FirstReason)) != maximumBatchErrorReasonRunes {
		t.Fatalf("batch error = %+v", batchErr)
	}
	if len(runErr.Error()) > 1024 || result.Failed != MaximumClaimLimit || result.Released != MaximumClaimLimit {
		t.Fatalf("result=%+v rendered error bytes=%d", result, len(runErr.Error()))
	}
}

func TestValidateRunOnceInputRejectsUnsafeBounds(t *testing.T) {
	valid := RunOnceInput{OwnerID: "worker-1"}
	if err := ValidateRunOnceInput(valid); err != nil {
		t.Fatalf("ValidateRunOnceInput(valid) error = %v", err)
	}
	invalid := []RunOnceInput{
		{},
		{OwnerID: " worker-1"},
		{OwnerID: "worker-1", ClaimLimit: MaximumClaimLimit + 1},
		{OwnerID: "worker-1", LeaseDuration: time.Minute - time.Millisecond},
		{OwnerID: "worker-1", WorkerCount: MaximumWorkerCount + 1},
		{OwnerID: "worker-1", ClaimTimeout: time.Microsecond},
		{OwnerID: "worker-1", CompleteTimeout: DefaultLeaseDuration},
		{OwnerID: "worker-1", ClaimLimit: MaximumClaimLimit},
	}
	for _, input := range invalid {
		if err := ValidateRunOnceInput(input); err == nil {
			t.Fatalf("ValidateRunOnceInput(%+v) error = nil", input)
		}
	}
}

func TestValidateRunOnceInputLeaseBudgetIncludesClaimWavesAndSafetyMargin(t *testing.T) {
	exact := RunOnceInput{
		OwnerID:         "worker-1",
		ClaimLimit:      1,
		LeaseDuration:   time.Minute,
		WorkerCount:     1,
		ClaimTimeout:    27 * time.Second,
		CompleteTimeout: 27 * time.Second,
	}
	if err := ValidateRunOnceInput(exact); err != nil {
		t.Fatalf("ValidateRunOnceInput(exact budget) error = %v", err)
	}
	claimOver := exact
	claimOver.ClaimTimeout += time.Millisecond
	if err := ValidateRunOnceInput(claimOver); err == nil {
		t.Fatal("ValidateRunOnceInput(claim over budget) error = nil")
	}
	marginOver := exact
	marginOver.CompleteTimeout += time.Millisecond
	if err := ValidateRunOnceInput(marginOver); err == nil {
		t.Fatal("ValidateRunOnceInput(safety margin over budget) error = nil")
	}
	twoWaves := exact
	twoWaves.ClaimLimit = 2
	twoWaves.ClaimTimeout = time.Second
	twoWaves.CompleteTimeout = 24 * time.Second
	if err := ValidateRunOnceInput(twoWaves); err != nil {
		t.Fatalf("ValidateRunOnceInput(two exact waves) error = %v", err)
	}
	twoWaves.ClaimLimit = 3
	if err := ValidateRunOnceInput(twoWaves); err == nil {
		t.Fatal("ValidateRunOnceInput(three waves over budget) error = nil")
	}
}

func TestNewServiceRejectsTypedNilDependencies(t *testing.T) {
	var typedNil *healthSyncStoreStub
	if _, err := NewService(typedNil, &healthSyncStoreStub{}, &healthSyncStoreStub{}); err == nil {
		t.Fatal("NewService(typed nil claimer) error = nil")
	}
	if _, err := NewService(&healthSyncStoreStub{}, typedNil, &healthSyncStoreStub{}); err == nil {
		t.Fatal("NewService(typed nil completer) error = nil")
	}
	if _, err := NewService(&healthSyncStoreStub{}, &healthSyncStoreStub{}, typedNil); err == nil {
		t.Fatal("NewService(typed nil releaser) error = nil")
	}
}

func makeClaims(count int) []port.ModelQualityHealthSyncClaim {
	claims := make([]port.ModelQualityHealthSyncClaim, count)
	for index := range claims {
		runID := fmt.Sprintf("run-%03d", index)
		claims[index] = port.ModelQualityHealthSyncClaim{
			RunID:   runID,
			Failure: port.ModelQualityHealthFailureInput{RunID: runID},
			Lease: port.ModelQualityHealthSyncLease{
				OwnerID:    "worker-1",
				ClaimToken: port.ModelQualityHealthSyncClaimToken(fmt.Sprintf("token-%03d", index)),
				Epoch:      uint64(index + 1),
				Until:      time.Date(2026, 7, 26, 12, 10, 0, 0, time.UTC),
			},
		}
	}
	return claims
}

type healthSyncStoreStub struct {
	claims     []port.ModelQualityHealthSyncClaim
	claimErr   error
	afterClaim func()
	complete   func(context.Context, port.ModelQualityHealthSyncCompleteInput) (port.ModelQualityHealthSyncCompleteResult, error)
	release    func(context.Context, port.ModelQualityHealthSyncReleaseInput) (bool, error)

	claimInput    port.ModelQualityHealthSyncClaimInput
	completeCalls atomic.Int32
	releaseCalls  atomic.Int32
	releaseMu     sync.Mutex
	releases      []port.ModelQualityHealthSyncReleaseInput
}

func (s *healthSyncStoreStub) ClaimFailedModelQualityHealthSyncs(_ context.Context, input port.ModelQualityHealthSyncClaimInput) (port.ModelQualityHealthSyncClaimBatch, error) {
	s.claimInput = input
	if s.afterClaim != nil {
		s.afterClaim()
	}
	return port.ModelQualityHealthSyncClaimBatch{Claims: append([]port.ModelQualityHealthSyncClaim(nil), s.claims...)}, s.claimErr
}

func (s *healthSyncStoreStub) CompleteModelQualityHealthSync(ctx context.Context, input port.ModelQualityHealthSyncCompleteInput) (port.ModelQualityHealthSyncCompleteResult, error) {
	s.completeCalls.Add(1)
	if s.complete != nil {
		return s.complete(ctx, input)
	}
	return port.ModelQualityHealthSyncCompleteResult{Applied: true}, nil
}

func (s *healthSyncStoreStub) ReleaseModelQualityHealthSync(ctx context.Context, input port.ModelQualityHealthSyncReleaseInput) (bool, error) {
	s.releaseCalls.Add(1)
	s.releaseMu.Lock()
	s.releases = append(s.releases, input)
	s.releaseMu.Unlock()
	if s.release != nil {
		return s.release(ctx, input)
	}
	return true, nil
}

func (s *healthSyncStoreStub) releaseInputs() []port.ModelQualityHealthSyncReleaseInput {
	s.releaseMu.Lock()
	defer s.releaseMu.Unlock()
	return append([]port.ModelQualityHealthSyncReleaseInput(nil), s.releases...)
}

type sqlStateStub struct {
	state string
}

func (e sqlStateStub) Error() string    { return "sql state " + e.state }
func (e sqlStateStub) SQLState() string { return e.state }
