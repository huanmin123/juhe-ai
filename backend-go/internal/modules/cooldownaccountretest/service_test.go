package cooldownaccountretest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type fakeStore struct {
	page  port.CooldownAccountRetestPage
	found port.CooldownAccountRetestCandidate
	ok    bool
	input port.CooldownAccountRetestListInput
	lists int
	finds int
}

func (f *fakeStore) ListDueCooldownAccountRetests(_ context.Context, input port.CooldownAccountRetestListInput) (port.CooldownAccountRetestPage, error) {
	f.lists++
	f.input = input
	return f.page, nil
}
func (f *fakeStore) FindDueCooldownAccountRetest(_ context.Context, _ string, _ time.Time) (port.CooldownAccountRetestCandidate, bool, error) {
	f.finds++
	return f.found, f.ok, nil
}

func validTaskAndCandidate() (port.CooldownAccountRetestTask, port.CooldownAccountRetestCandidate) {
	started := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	sourceRevision := 11
	task := port.CooldownAccountRetestTask{
		AccountID: "a", ConfigRevision: 2, DispatchRevision: 3,
		ObservationStartedAt: &started, Generation: "generation-1", SourceConfigRevision: &sourceRevision,
	}
	candidate := port.CooldownAccountRetestCandidate{
		ID: task.AccountID, ConfigRevision: task.ConfigRevision, DispatchRevision: task.DispatchRevision,
		ObservationStartedAt: task.ObservationStartedAt, Generation: task.Generation,
		SourceConfigRevision: task.SourceConfigRevision,
	}
	return task, candidate
}

func validCandidate(accountID string) port.CooldownAccountRetestCandidate {
	_, candidate := validTaskAndCandidate()
	candidate.ID = accountID
	return candidate
}

type fakeEnqueuer struct {
	mu                       sync.Mutex
	active, maxActive, calls int
	accountIDs               []string
	err                      error
}

type fakeQuotaChecker struct {
	eligible map[string]bool
	err      error
}

func (f fakeQuotaChecker) EligibleByAccountID(_ context.Context, candidates []port.CooldownAccountRetestCandidate, _ time.Time) (map[string]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.eligible != nil {
		return f.eligible, nil
	}
	eligible := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		eligible[candidate.ID] = true
	}
	return eligible, nil
}

func (f *fakeEnqueuer) EnqueueCooldownAccountRetest(ctx context.Context, task port.CooldownAccountRetestTask) (bool, error) {
	f.mu.Lock()
	f.calls++
	f.accountIDs = append(f.accountIDs, task.AccountID)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	select {
	case <-time.After(5 * time.Millisecond):
	case <-ctx.Done():
		return false, ctx.Err()
	}
	f.mu.Lock()
	f.active--
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return false, err
	}
	return task.AccountID != "duplicate", nil
}

func TestSchedulerUsesBoundedPageAndEnqueueWorkers(t *testing.T) {
	store := &fakeStore{page: port.CooldownAccountRetestPage{Candidates: []port.CooldownAccountRetestCandidate{validCandidate("a"), validCandidate("duplicate"), validCandidate("c")}}}
	enqueuer := &fakeEnqueuer{}
	result, _, err := (Scheduler{Store: store, Enqueuer: enqueuer, Quota: fakeQuotaChecker{}, BatchSize: 1000, EnqueueWorkers: 2}).RunPage(context.Background(), nil, time.Now())
	if err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if store.input.Limit != port.CooldownAccountRetestMaxPageSize {
		t.Fatalf("limit = %d", store.input.Limit)
	}
	if result.EnqueuedCount != 2 || result.DuplicateCount != 1 {
		t.Fatalf("result = %+v", result)
	}
	if enqueuer.maxActive > 2 {
		t.Fatalf("max active = %d", enqueuer.maxActive)
	}
}

type fakeQueueCapacity struct {
	snapshot QueueSnapshot
	err      error
}

func (f fakeQueueCapacity) CooldownAccountRetestQueueSnapshot(context.Context) (QueueSnapshot, error) {
	return f.snapshot, f.err
}

func TestSchedulerLimitsPageToAvailableQueueSlots(t *testing.T) {
	store := &fakeStore{page: port.CooldownAccountRetestPage{Candidates: []port.CooldownAccountRetestCandidate{validCandidate("a")}}}
	result, _, err := (Scheduler{
		Store: store, Enqueuer: &fakeEnqueuer{}, Quota: fakeQuotaChecker{}, BatchSize: 5,
		Capacity: fakeQueueCapacity{snapshot: QueueSnapshot{PendingCount: 2, RunningCount: 2}},
	}).RunPage(context.Background(), nil, time.Now())
	if err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if store.input.Limit != port.CooldownAccountRetestMaxPageSize || result.AvailableSlots != 1 {
		t.Fatalf("limit=%d result=%+v, want one available slot", store.input.Limit, result)
	}
}

func TestSchedulerCountsRetryTasksAsOccupiedQueueSlots(t *testing.T) {
	store := &fakeStore{}
	result, next, err := (Scheduler{
		Store: store, Enqueuer: &fakeEnqueuer{}, Quota: fakeQuotaChecker{}, BatchSize: 3,
		Capacity: fakeQueueCapacity{snapshot: QueueSnapshot{RetryCount: 3}},
	}).RunPage(context.Background(), nil, time.Now())
	if err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if store.lists != 0 || next != nil || result.AvailableSlots != 0 {
		t.Fatalf("lists=%d next=%+v result=%+v", store.lists, next, result)
	}
}

func TestSchedulerKeepsCursorWhenQueueIsFull(t *testing.T) {
	cursor := &port.CooldownAccountRetestCursor{ID: "cursor-account"}
	store := &fakeStore{}
	result, next, err := (Scheduler{
		Store: store, Enqueuer: &fakeEnqueuer{}, Quota: fakeQuotaChecker{}, BatchSize: 3,
		Capacity: fakeQueueCapacity{snapshot: QueueSnapshot{PendingCount: 2, RunningCount: 1}},
	}).RunPage(context.Background(), cursor, time.Now())
	if err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if store.lists != 0 || next != cursor || result.AvailableSlots != 0 {
		t.Fatalf("lists=%d next=%+v result=%+v", store.lists, next, result)
	}
}

func TestSchedulerRejectsCandidateMissingFence(t *testing.T) {
	store := &fakeStore{page: port.CooldownAccountRetestPage{Candidates: []port.CooldownAccountRetestCandidate{{ID: "a", ConfigRevision: 1}}}}
	enqueuer := &fakeEnqueuer{}
	result, _, err := (Scheduler{Store: store, Enqueuer: enqueuer, Quota: fakeQuotaChecker{}}).RunPage(context.Background(), nil, time.Now())
	if err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if result.InvalidCandidateCount != 1 || result.EnqueuedCount != 0 || enqueuer.calls != 0 {
		t.Fatalf("result=%+v enqueue calls=%d", result, enqueuer.calls)
	}
}

func TestSchedulerUsesECMAScriptGenerationWhitespaceContract(t *testing.T) {
	bomOnly := validCandidate("bom-only")
	bomOnly.Generation = "\ufeff"
	nbspOnly := validCandidate("nbsp-only")
	nbspOnly.Generation = "\u00a0"
	nonCanonical := validCandidate("non-canonical")
	nonCanonical.Generation = "\ufeffgeneration-1\u00a0"
	nel := validCandidate("nel")
	nel.Generation = "\u0085generation-1\u0085"
	store := &fakeStore{page: port.CooldownAccountRetestPage{Candidates: []port.CooldownAccountRetestCandidate{bomOnly, nbspOnly, nonCanonical, nel}}}
	enqueuer := &fakeEnqueuer{}
	result, _, err := (Scheduler{Store: store, Enqueuer: enqueuer, Quota: fakeQuotaChecker{}}).RunPage(context.Background(), nil, time.Now())
	if err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if result.InvalidCandidateCount != 3 || result.EnqueuedCount != 1 || enqueuer.calls != 1 {
		t.Fatalf("result=%+v enqueue calls=%d", result, enqueuer.calls)
	}
}

func TestSchedulerRejectsQuotaIneligibleCandidatesBeforeEnqueue(t *testing.T) {
	store := &fakeStore{page: port.CooldownAccountRetestPage{Candidates: []port.CooldownAccountRetestCandidate{
		validCandidate("eligible"), validCandidate("quota-exhausted"),
	}}}
	enqueuer := &fakeEnqueuer{}
	result, _, err := (Scheduler{
		Store: store, Enqueuer: enqueuer,
		Quota: fakeQuotaChecker{eligible: map[string]bool{"eligible": true}},
	}).RunPage(context.Background(), nil, time.Now())
	if err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if result.QuotaRejectedCount != 1 || result.EnqueuedCount != 1 || enqueuer.calls != 1 {
		t.Fatalf("result=%+v enqueue calls=%d", result, enqueuer.calls)
	}
}

func TestSchedulerOverScansQuotaRejectedCandidates(t *testing.T) {
	first := validCandidate("quota-exhausted")
	first.CooldownUntil = time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	first.Priority = 1
	first.CreatedAt = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	second := validCandidate("eligible")
	second.CooldownUntil = first.CooldownUntil.Add(time.Minute)
	second.Priority = 2
	second.CreatedAt = first.CreatedAt.Add(time.Minute)
	third := validCandidate("later-eligible")
	third.CooldownUntil = second.CooldownUntil.Add(time.Minute)
	third.Priority = 3
	third.CreatedAt = second.CreatedAt.Add(time.Minute)
	store := &fakeStore{page: port.CooldownAccountRetestPage{Candidates: []port.CooldownAccountRetestCandidate{first, second, third}}}
	enqueuer := &fakeEnqueuer{}
	result, next, err := (Scheduler{
		Store: store, Enqueuer: enqueuer, Quota: fakeQuotaChecker{eligible: map[string]bool{"eligible": true}},
		BatchSize: 1, Capacity: fakeQueueCapacity{snapshot: QueueSnapshot{}},
	}).RunPage(context.Background(), nil, time.Now())
	if err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if store.input.Limit != port.CooldownAccountRetestMaxPageSize || result.AvailableSlots != 1 || result.QuotaRejectedCount != 1 || result.EnqueuedCount != 1 {
		t.Fatalf("input=%+v result=%+v", store.input, result)
	}
	if len(enqueuer.accountIDs) != 1 || enqueuer.accountIDs[0] != "eligible" {
		t.Fatalf("enqueued account IDs = %v", enqueuer.accountIDs)
	}
	if next == nil || next.ID != second.ID || !next.CooldownUntil.Equal(second.CooldownUntil) || next.Priority != second.Priority || !next.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("next cursor = %+v, want cursor after selected candidate %+v", next, second)
	}
}

func TestSchedulerKeepsOriginalCursorWhenEnqueueFails(t *testing.T) {
	cursor := &port.CooldownAccountRetestCursor{ID: "original-cursor"}
	store := &fakeStore{page: port.CooldownAccountRetestPage{Candidates: []port.CooldownAccountRetestCandidate{validCandidate("eligible")}}}
	result, next, err := (Scheduler{
		Store: store, Enqueuer: &fakeEnqueuer{err: errors.New("queue unavailable")}, Quota: fakeQuotaChecker{},
	}).RunPage(context.Background(), cursor, time.Now())
	if err == nil || !strings.Contains(err.Error(), "enqueue cooldown account retest") {
		t.Fatalf("RunPage() error = %v", err)
	}
	if next != cursor || result.EnqueuedCount != 0 {
		t.Fatalf("next=%+v result=%+v, want original cursor and no enqueue", next, result)
	}
}

func TestSchedulerFailsClosedWhenQuotaCheckFails(t *testing.T) {
	store := &fakeStore{page: port.CooldownAccountRetestPage{Candidates: []port.CooldownAccountRetestCandidate{validCandidate("a")}}}
	enqueuer := &fakeEnqueuer{}
	_, _, err := (Scheduler{
		Store: store, Enqueuer: enqueuer, Quota: fakeQuotaChecker{err: errors.New("quota unavailable")},
	}).RunPage(context.Background(), nil, time.Now())
	if err == nil || !strings.Contains(err.Error(), "quota eligibility") || enqueuer.calls != 0 {
		t.Fatalf("error=%v enqueue calls=%d", err, enqueuer.calls)
	}
}

func TestSchedulerRequiresQuotaChecker(t *testing.T) {
	_, _, err := (Scheduler{Store: &fakeStore{}, Enqueuer: &fakeEnqueuer{}}).RunPage(context.Background(), nil, time.Now())
	if err == nil || !strings.Contains(err.Error(), "quota checker") {
		t.Fatalf("RunPage() error = %v", err)
	}
}

type fakeProbe struct {
	calls         int
	waitForCancel bool
	result        port.CooldownAccountRetestProbeResult
	err           error
}

func (f *fakeProbe) Probe(ctx context.Context, _ port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error) {
	f.calls++
	if f.waitForCancel {
		<-ctx.Done()
		return port.CooldownAccountRetestProbeResult{}, ctx.Err()
	}
	if f.err != nil {
		return port.CooldownAccountRetestProbeResult{}, f.err
	}
	if f.result.Outcome != "" {
		return f.result, nil
	}
	return port.CooldownAccountRetestProbeResult{Outcome: "complete_success"}, nil
}

type fakeOutcomes struct {
	success, deferred, failed int
	deferDelay                time.Duration
	failureResult             port.CooldownAccountRetestProbeResult
	deferContextErr           error
	deferErr                  error
}

type blockingOutcomes struct {
	started chan struct{}
}

func (o blockingOutcomes) RecordCooldownAccountRetestSuccess(ctx context.Context, _ port.CooldownAccountRetestTask) error {
	close(o.started)
	<-ctx.Done()
	return ctx.Err()
}

func (blockingOutcomes) DeferCooldownAccountRetest(context.Context, port.CooldownAccountRetestTask, time.Duration) error {
	return nil
}

func (blockingOutcomes) RecordCooldownAccountRetestFailure(context.Context, port.CooldownAccountRetestTask, port.CooldownAccountRetestProbeResult) error {
	return nil
}

func (f *fakeOutcomes) RecordCooldownAccountRetestSuccess(context.Context, port.CooldownAccountRetestTask) error {
	f.success++
	return nil
}
func (f *fakeOutcomes) DeferCooldownAccountRetest(ctx context.Context, _ port.CooldownAccountRetestTask, delay time.Duration) error {
	f.deferred++
	f.deferDelay = delay
	f.deferContextErr = ctx.Err()
	return f.deferErr
}
func (f *fakeOutcomes) RecordCooldownAccountRetestFailure(_ context.Context, _ port.CooldownAccountRetestTask, result port.CooldownAccountRetestProbeResult) error {
	f.failed++
	f.failureResult = result
	return nil
}

func TestProcessorDiscardsEachStaleFence(t *testing.T) {
	task, current := validTaskAndCandidate()
	otherObservation := task.ObservationStartedAt.Add(time.Second)
	otherSourceRevision := *task.SourceConfigRevision + 1
	tests := map[string]port.CooldownAccountRetestCandidate{
		"config revision":   func() port.CooldownAccountRetestCandidate { v := current; v.ConfigRevision++; return v }(),
		"dispatch revision": func() port.CooldownAccountRetestCandidate { v := current; v.DispatchRevision++; return v }(),
		"observation": func() port.CooldownAccountRetestCandidate {
			v := current
			v.ObservationStartedAt = &otherObservation
			return v
		}(),
		"generation": func() port.CooldownAccountRetestCandidate { v := current; v.Generation = "generation-2"; return v }(),
		"source config revision": func() port.CooldownAccountRetestCandidate {
			v := current
			v.SourceConfigRevision = &otherSourceRevision
			return v
		}(),
		"source removed": func() port.CooldownAccountRetestCandidate { v := current; v.SourceConfigRevision = nil; return v }(),
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			probe := &fakeProbe{}
			outcomes := &fakeOutcomes{}
			err := (Processor{Store: &fakeStore{ok: true, found: candidate}, Outcomes: outcomes, Probe: probe, Quota: fakeQuotaChecker{}}).RunTask(context.Background(), task)
			if err != nil {
				t.Fatalf("RunTask() error = %v", err)
			}
			if probe.calls != 0 || outcomes.success != 0 || outcomes.deferred != 0 || outcomes.failed != 0 {
				t.Fatalf("stale task executed: probe=%d outcomes=%+v", probe.calls, outcomes)
			}
		})
	}
}

func TestProcessorDiscardsInvalidTaskBeforeStoreLookup(t *testing.T) {
	validTask, validCandidate := validTaskAndCandidate()
	zeroTime := time.Time{}
	zeroSourceRevision := 0
	tests := map[string]port.CooldownAccountRetestTask{
		"empty account":            func() port.CooldownAccountRetestTask { v := validTask; v.AccountID = " "; return v }(),
		"zero config revision":     func() port.CooldownAccountRetestTask { v := validTask; v.ConfigRevision = 0; return v }(),
		"zero dispatch revision":   func() port.CooldownAccountRetestTask { v := validTask; v.DispatchRevision = 0; return v }(),
		"missing observation":      func() port.CooldownAccountRetestTask { v := validTask; v.ObservationStartedAt = nil; return v }(),
		"zero observation":         func() port.CooldownAccountRetestTask { v := validTask; v.ObservationStartedAt = &zeroTime; return v }(),
		"BOM generation":           func() port.CooldownAccountRetestTask { v := validTask; v.Generation = "\ufeff"; return v }(),
		"NBSP generation":          func() port.CooldownAccountRetestTask { v := validTask; v.Generation = "\u00a0"; return v }(),
		"non-canonical generation": func() port.CooldownAccountRetestTask { v := validTask; v.Generation = " generation-1"; return v }(),
		"zero source revision": func() port.CooldownAccountRetestTask {
			v := validTask
			v.SourceConfigRevision = &zeroSourceRevision
			return v
		}(),
	}
	for name, task := range tests {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{ok: true, found: validCandidate}
			probe := &fakeProbe{}
			outcomes := &fakeOutcomes{}
			if err := (Processor{Store: store, Outcomes: outcomes, Probe: probe, Quota: fakeQuotaChecker{}}).RunTask(context.Background(), task); err != nil {
				t.Fatalf("RunTask() error = %v", err)
			}
			if store.finds != 0 || probe.calls != 0 || outcomes.success != 0 || outcomes.deferred != 0 || outcomes.failed != 0 {
				t.Fatalf("invalid task escaped fence: finds=%d probe=%d outcomes=%+v", store.finds, probe.calls, outcomes)
			}
		})
	}
}

func TestProcessorDiscardsInvalidOrMismatchedCandidate(t *testing.T) {
	task, validCandidate := validTaskAndCandidate()
	zeroSourceRevision := 0
	tests := map[string]port.CooldownAccountRetestCandidate{
		"mismatched account":     func() port.CooldownAccountRetestCandidate { v := validCandidate; v.ID = "other"; return v }(),
		"zero config revision":   func() port.CooldownAccountRetestCandidate { v := validCandidate; v.ConfigRevision = 0; return v }(),
		"zero dispatch revision": func() port.CooldownAccountRetestCandidate { v := validCandidate; v.DispatchRevision = 0; return v }(),
		"missing observation": func() port.CooldownAccountRetestCandidate {
			v := validCandidate
			v.ObservationStartedAt = nil
			return v
		}(),
		"BOM generation":           func() port.CooldownAccountRetestCandidate { v := validCandidate; v.Generation = "\ufeff"; return v }(),
		"NBSP generation":          func() port.CooldownAccountRetestCandidate { v := validCandidate; v.Generation = "\u00a0"; return v }(),
		"non-canonical generation": func() port.CooldownAccountRetestCandidate { v := validCandidate; v.Generation += "\u00a0"; return v }(),
		"zero source revision": func() port.CooldownAccountRetestCandidate {
			v := validCandidate
			v.SourceConfigRevision = &zeroSourceRevision
			return v
		}(),
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			probe := &fakeProbe{}
			outcomes := &fakeOutcomes{}
			if err := (Processor{Store: &fakeStore{ok: true, found: candidate}, Outcomes: outcomes, Probe: probe, Quota: fakeQuotaChecker{}}).RunTask(context.Background(), task); err != nil {
				t.Fatalf("RunTask() error = %v", err)
			}
			if probe.calls != 0 || outcomes.success != 0 || outcomes.deferred != 0 || outcomes.failed != 0 {
				t.Fatalf("invalid candidate escaped fence: probe=%d outcomes=%+v", probe.calls, outcomes)
			}
		})
	}
}

func TestProcessorRechecksQuotaBeforeProbe(t *testing.T) {
	task, candidate := validTaskAndCandidate()
	probe := &fakeProbe{}
	outcomes := &fakeOutcomes{}
	err := (Processor{
		Store: &fakeStore{ok: true, found: candidate}, Outcomes: outcomes, Probe: probe,
		Quota: fakeQuotaChecker{eligible: map[string]bool{candidate.ID: false}},
	}).RunTask(context.Background(), task)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if probe.calls != 0 || outcomes.success != 0 || outcomes.deferred != 0 || outcomes.failed != 0 {
		t.Fatalf("quota-ineligible task executed: probe=%d outcomes=%+v", probe.calls, outcomes)
	}
}

func TestProcessorFailsClosedWhenQuotaRecheckFails(t *testing.T) {
	task, candidate := validTaskAndCandidate()
	probe := &fakeProbe{}
	err := (Processor{
		Store: &fakeStore{ok: true, found: candidate}, Outcomes: &fakeOutcomes{}, Probe: probe,
		Quota: fakeQuotaChecker{err: errors.New("quota unavailable")},
	}).RunTask(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), "recheck") || probe.calls != 0 {
		t.Fatalf("RunTask() error=%v probe calls=%d", err, probe.calls)
	}
}

func TestProcessorDefersAfterProbeTimeoutWithFreshOutcomeContext(t *testing.T) {
	task, candidate := validTaskAndCandidate()
	store := &fakeStore{ok: true, found: candidate}
	probe := &fakeProbe{waitForCancel: true}
	outcomes := &fakeOutcomes{}
	err := (Processor{Store: store, Outcomes: outcomes, Probe: probe, Quota: fakeQuotaChecker{}, TaskTimeout: 10 * time.Millisecond}).RunTask(context.Background(), task)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.deferred != 1 || outcomes.deferContextErr != nil {
		t.Fatalf("outcomes = %+v, want deferred with live context", outcomes)
	}
}

func TestProcessorDefersLateSuccessReturnedAfterTaskDeadline(t *testing.T) {
	task, candidate := validTaskAndCandidate()
	outcomes := &fakeOutcomes{}
	err := (Processor{
		Store: &fakeStore{ok: true, found: candidate}, Outcomes: outcomes,
		Probe: lateSuccessProbe{}, Quota: fakeQuotaChecker{}, TaskTimeout: 10 * time.Millisecond,
	}).RunTask(context.Background(), task)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.deferred != 1 || outcomes.success != 0 || outcomes.failed != 0 {
		t.Fatalf("late success wrote attributable outcome: %+v", outcomes)
	}
}

func TestProcessorDoesNotDeferWhenWorkerShutdownCancelsProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	task, candidate := validTaskAndCandidate()
	store := &fakeStore{ok: true, found: candidate}
	outcomes := &fakeOutcomes{}
	probe := cancelingProbe{cancel: cancel}
	err := (Processor{Store: store, Outcomes: outcomes, Probe: probe, Quota: fakeQuotaChecker{}}).RunTask(
		ctx,
		task,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunTask() error = %v, want context cancellation", err)
	}
	if outcomes.deferred != 0 || outcomes.success != 0 || outcomes.failed != 0 {
		t.Fatalf("shutdown cancellation wrote outcome: %+v", outcomes)
	}
}

func TestProcessorCancelsOutcomeWriteWhenWorkerShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	task, candidate := validTaskAndCandidate()
	store := &fakeStore{ok: true, found: candidate}
	outcomes := blockingOutcomes{started: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- (Processor{Store: store, Outcomes: outcomes, Probe: &fakeProbe{}, Quota: fakeQuotaChecker{}, OutcomeTimeout: time.Second}).RunTask(
			ctx,
			task,
		)
	}()
	<-outcomes.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunTask() error = %v, want context cancellation", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("outcome write outlived worker cancellation")
	}
}

type cancelingProbe struct {
	cancel context.CancelFunc
}

type lateSuccessProbe struct{}

func (lateSuccessProbe) Probe(ctx context.Context, _ port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error) {
	<-ctx.Done()
	return port.CooldownAccountRetestProbeResult{Outcome: "complete_success"}, nil
}

func (p cancelingProbe) Probe(ctx context.Context, _ port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error) {
	p.cancel()
	<-ctx.Done()
	return port.CooldownAccountRetestProbeResult{}, ctx.Err()
}

func TestProcessorDefersTransientProbeErrorWithoutQueueRetry(t *testing.T) {
	probeErr := errors.New("upstream transport unavailable")
	task, candidate := validTaskAndCandidate()
	store := &fakeStore{ok: true, found: candidate}
	outcomes := &fakeOutcomes{}
	err := (Processor{Store: store, Outcomes: outcomes, Probe: &fakeProbe{err: probeErr}, Quota: fakeQuotaChecker{}}).RunTask(context.Background(), task)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.deferred != 1 || outcomes.failed != 0 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}

func TestProcessorReturnsRetryableErrorWhenDeferredOutcomeCannotPersist(t *testing.T) {
	persistErr := errors.New("database unavailable")
	task, candidate := validTaskAndCandidate()
	store := &fakeStore{ok: true, found: candidate}
	outcomes := &fakeOutcomes{deferErr: persistErr}
	err := (Processor{Store: store, Outcomes: outcomes, Probe: &fakeProbe{err: errors.New("upstream timeout")}, Quota: fakeQuotaChecker{}}).RunTask(context.Background(), task)
	if !errors.Is(err, persistErr) {
		t.Fatalf("error = %v, want persistence error", err)
	}
	if outcomes.deferred != 1 {
		t.Fatalf("deferred = %d, want 1", outcomes.deferred)
	}
}

func TestProcessorRecordsSuccess(t *testing.T) {
	task, candidate := validTaskAndCandidate()
	store := &fakeStore{ok: true, found: candidate}
	outcomes := &fakeOutcomes{}
	if err := (Processor{Store: store, Outcomes: outcomes, Probe: &fakeProbe{}, Quota: fakeQuotaChecker{}}).RunTask(context.Background(), task); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.success != 1 {
		t.Fatalf("success calls = %d", outcomes.success)
	}
}

func TestProcessorDiscardsStaleObservationGeneration(t *testing.T) {
	task, candidate := validTaskAndCandidate()
	queued := *task.ObservationStartedAt
	current := queued.Add(time.Second)
	candidate.ObservationStartedAt = &current
	store := &fakeStore{ok: true, found: candidate}
	probe := &fakeProbe{}
	err := (Processor{Store: store, Outcomes: &fakeOutcomes{}, Probe: probe, Quota: fakeQuotaChecker{}}).RunTask(
		context.Background(),
		task,
	)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if probe.calls != 0 {
		t.Fatal("probe called for stale observation generation")
	}
}

func TestProcessorDefersUnattributableProbeFailureWithStableBackoff(t *testing.T) {
	task, candidate := validTaskAndCandidate()
	now := *task.ObservationStartedAt
	store := &fakeStore{ok: true, found: candidate}
	outcomes := &fakeOutcomes{}
	probe := &fakeProbe{result: port.CooldownAccountRetestProbeResult{Outcome: "probe_task_failure"}}
	if err := (Processor{Store: store, Outcomes: outcomes, Probe: probe, Quota: fakeQuotaChecker{}, Now: func() time.Time { return now }}).RunTask(context.Background(), task); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.deferred != 1 || outcomes.deferDelay != neutralDeferDelay(task, now) || outcomes.failed != 0 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}

func TestProcessorDefersFramingCompleteNeutral(t *testing.T) {
	task, candidate := validTaskAndCandidate()
	now := task.ObservationStartedAt.Add(2 * time.Minute)
	outcomes := &fakeOutcomes{}
	probe := &fakeProbe{result: port.CooldownAccountRetestProbeResult{Outcome: "framing_complete_neutral", StatusCode: 401}}
	if err := (Processor{
		Store: &fakeStore{ok: true, found: candidate}, Outcomes: outcomes, Probe: probe, Quota: fakeQuotaChecker{},
		Now: func() time.Time { return now },
	}).RunTask(context.Background(), task); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.deferred != 1 || outcomes.failed != 0 || outcomes.success != 0 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}

func TestNeutralDeferDelayIsDeterministicAndBounded(t *testing.T) {
	task, _ := validTaskAndCandidate()
	wantSeconds := map[time.Duration]int{
		0: 36, 29 * time.Second: 36, 30 * time.Second: 51,
		90 * time.Second: 122, time.Hour: 720, 30 * 24 * time.Hour: 720,
	}
	for elapsed, expectedSeconds := range wantSeconds {
		now := task.ObservationStartedAt.Add(elapsed)
		first := neutralDeferDelay(task, now)
		second := neutralDeferDelay(task, now)
		if first != second {
			t.Fatalf("elapsed=%s delay changed from %s to %s", elapsed, first, second)
		}
		if first < 3*time.Second || first > 15*time.Minute {
			t.Fatalf("elapsed=%s delay=%s outside [3s, 15m]", elapsed, first)
		}
		if first != time.Duration(expectedSeconds)*time.Second {
			t.Fatalf("elapsed=%s delay=%s, want Node parity %ds", elapsed, first, expectedSeconds)
		}
	}
	unicodeTask := task
	unicodeTask.AccountID = "\u8d26\u53f7-A"
	unicodeTask.Generation = "\u4ee3\u6b21-\U0001F600"
	if got := neutralDeferDelay(unicodeTask, unicodeTask.ObservationStartedAt.Add(90*time.Second)); got != 128*time.Second {
		t.Fatalf("UTF-16 stable hash delay = %s, want Node parity 128s", got)
	}
}

func TestProcessorRecordsAttributableUpstreamFailure(t *testing.T) {
	task, candidate := validTaskAndCandidate()
	store := &fakeStore{ok: true, found: candidate}
	outcomes := &fakeOutcomes{}
	want := port.CooldownAccountRetestProbeResult{Outcome: "upstream_failure", StatusCode: 429, ErrorCode: "rate_limit"}
	probe := &fakeProbe{result: want}
	if err := (Processor{Store: store, Outcomes: outcomes, Probe: probe, Quota: fakeQuotaChecker{}}).RunTask(context.Background(), task); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if outcomes.failed != 1 || outcomes.deferred != 0 || outcomes.failureResult != want {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}
