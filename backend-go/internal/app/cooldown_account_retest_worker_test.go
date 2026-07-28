package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	job "juhe-ai/backend-go/internal/jobs/cooldownaccountretest"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/jobs/worker"
	module "juhe-ai/backend-go/internal/modules/cooldownaccountretest"
	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/systemsettings"
)

func TestRunCooldownAccountRetestWorkerDisabledFailsBeforeRuntimeDependencies(t *testing.T) {
	deps := cooldownAccountRetestWorkerDependencies{
		openStore: func(context.Context, string) (cooldownAccountRetestRuntimeStore, error) {
			t.Fatal("disabled worker opened PostgreSQL")
			return nil, nil
		},
	}
	err := runCooldownAccountRetestWorker(t.Context(), config.Config{}, nil, CooldownAccountRetestWorkerOptions{}, deps)
	if err == nil || !strings.Contains(err.Error(), "默认关闭") {
		t.Fatalf("runCooldownAccountRetestWorker() error = %v", err)
	}
}

func TestRunCooldownAccountRetestWorkerRequiresOwnerLock(t *testing.T) {
	cfg := config.Config{CooldownAccountRetestWorkerEnabled: true}
	err := runCooldownAccountRetestWorker(t.Context(), cfg, nil, CooldownAccountRetestWorkerOptions{}, cooldownAccountRetestWorkerDependencies{})
	if err == nil || !strings.Contains(err.Error(), "owner lock") {
		t.Fatalf("runCooldownAccountRetestWorker() error = %v", err)
	}
}

func TestRunCooldownAccountRetestWorkerMissingProbeFailsBeforeRuntimeDependencies(t *testing.T) {
	cfg := config.Config{
		CooldownAccountRetestWorkerEnabled: true,
		OwnerLockEnabled:                   true,
		OwnerLockRole:                      "worker",
	}
	deps := cooldownAccountRetestWorkerDependencies{
		openStore: func(context.Context, string) (cooldownAccountRetestRuntimeStore, error) {
			t.Fatal("missing probe opened PostgreSQL")
			return nil, nil
		},
	}
	err := runCooldownAccountRetestWorker(t.Context(), cfg, nil, CooldownAccountRetestWorkerOptions{}, deps)
	if !errors.Is(err, module.ErrProbeNotConfigured) {
		t.Fatalf("runCooldownAccountRetestWorker() error = %v, want missing probe", err)
	}
}

func TestRunCooldownAccountRetestWorkerDefaultsOutcomesToRuntimeStore(t *testing.T) {
	runtimeStore := &cooldownAccountRetestOutcomeRuntimeStoreStub{
		cooldownAccountRetestRuntimeStoreStub: &cooldownAccountRetestRuntimeStoreStub{},
	}
	consumerErr := errors.New("consumer stopped")
	var captured port.CooldownAccountRetestOutcomeStore
	err := runCooldownAccountRetestWorker(
		t.Context(),
		cooldownAccountRetestWorkerTestConfig(),
		nil,
		CooldownAccountRetestWorkerOptions{Probe: cooldownAccountRetestProbeStub{}},
		cooldownAccountRetestWorkerTestDependencies(runtimeStore, func(_ context.Context, opts worker.CooldownAccountRetestConsumerOptions) error {
			captured = opts.Processor.Outcomes
			return consumerErr
		}),
	)
	if !errors.Is(err, consumerErr) {
		t.Fatalf("runCooldownAccountRetestWorker() error = %v, want consumer error", err)
	}
	if captured != runtimeStore {
		t.Fatalf("processor outcomes = %T %p, want runtime store %p", captured, captured, runtimeStore)
	}
}

func TestRunCooldownAccountRetestWorkerExplicitOutcomesOverrideRuntimeStore(t *testing.T) {
	runtimeStore := &cooldownAccountRetestOutcomeRuntimeStoreStub{
		cooldownAccountRetestRuntimeStoreStub: &cooldownAccountRetestRuntimeStoreStub{},
	}
	override := &cooldownAccountRetestOutcomesStub{}
	consumerErr := errors.New("consumer stopped")
	var captured port.CooldownAccountRetestOutcomeStore
	err := runCooldownAccountRetestWorker(
		t.Context(),
		cooldownAccountRetestWorkerTestConfig(),
		nil,
		CooldownAccountRetestWorkerOptions{Probe: cooldownAccountRetestProbeStub{}, Outcomes: override},
		cooldownAccountRetestWorkerTestDependencies(runtimeStore, func(_ context.Context, opts worker.CooldownAccountRetestConsumerOptions) error {
			captured = opts.Processor.Outcomes
			return consumerErr
		}),
	)
	if !errors.Is(err, consumerErr) {
		t.Fatalf("runCooldownAccountRetestWorker() error = %v, want consumer error", err)
	}
	if captured != override {
		t.Fatalf("processor outcomes = %T %p, want explicit override %p", captured, captured, override)
	}
}

func TestRunCooldownAccountRetestWorkerRequiresOutcomeCapableRuntimeStoreWithoutOverride(t *testing.T) {
	runtimeStore := &cooldownAccountRetestRuntimeStoreStub{}
	err := runCooldownAccountRetestWorker(
		t.Context(),
		cooldownAccountRetestWorkerTestConfig(),
		nil,
		CooldownAccountRetestWorkerOptions{Probe: cooldownAccountRetestProbeStub{}},
		cooldownAccountRetestWorkerDependencies{
			openStore: func(context.Context, string) (cooldownAccountRetestRuntimeStore, error) {
				return runtimeStore, nil
			},
			newQueueClient: func(queue.RedisOptions) cooldownAccountRetestQueueClient {
				t.Fatal("outcome capability rejection created queue client")
				return nil
			},
			newQueueInspector: func(queue.RedisOptions) cooldownAccountRetestQueueInspector {
				t.Fatal("outcome capability rejection created queue inspector")
				return nil
			},
			runConsumer: func(context.Context, worker.CooldownAccountRetestConsumerOptions) error {
				t.Fatal("outcome capability rejection ran consumer")
				return nil
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "outcome store is not configured") {
		t.Fatalf("runCooldownAccountRetestWorker() error = %v", err)
	}
	if !runtimeStore.closed.Load() {
		t.Fatal("runtime store was not closed after outcome capability rejection")
	}
}

func TestCooldownAccountRetestTaskBudgetReservesOutcomeAndShutdownMargin(t *testing.T) {
	wantMinimum := module.DefaultTaskTimeout + module.DefaultOutcomeTimeout + 5*time.Second
	if job.DefaultTaskTimeout < wantMinimum {
		t.Fatalf("Asynq task timeout = %s, want at least %s", job.DefaultTaskTimeout, wantMinimum)
	}
}

func TestCooldownAccountRetestSchedulerCarriesCursorAndSettingsAcrossPages(t *testing.T) {
	now := time.Date(2026, 7, 22, 9, 30, 0, 0, time.UTC)
	nextCursor := &port.CooldownAccountRetestCursor{CooldownUntil: now, ID: "acct_1"}
	store := &cooldownAccountRetestRuntimeStoreStub{
		pages: []port.CooldownAccountRetestPage{
			{Candidates: []port.CooldownAccountRetestCandidate{cooldownAccountRetestCandidate("acct_1", now, 3)}, NextCursor: nextCursor},
			{},
		},
	}
	enqueuer := &cooldownAccountRetestEnqueuerStub{}
	runner := newCooldownAccountRetestSchedulerRunner(
		store,
		cooldownAccountRetestSettingsReaderStub{settings: CooldownAccountRetestScheduleSettings{
			Interval: 3 * time.Second, BatchSize: 4, MaxPauseMinutes: 7, MaxRecoveryHours: 12,
		}},
		enqueuer,
		cooldownAccountRetestCapacityStub{},
	)
	if _, err := runner.RunPage(t.Context(), now); err != nil {
		t.Fatalf("first RunPage() error = %v", err)
	}
	if _, err := runner.RunPage(t.Context(), now.Add(3*time.Second)); err != nil {
		t.Fatalf("second RunPage() error = %v", err)
	}
	if len(store.inputs) != 2 || store.inputs[1].Cursor == nil || store.inputs[1].Cursor.ID != "acct_1" {
		t.Fatalf("inputs = %+v", store.inputs)
	}
	if len(enqueuer.tasks) != 1 || enqueuer.tasks[0].MaxPauseMinutes != 7 || enqueuer.tasks[0].MaxRecoveryHours != 12 ||
		enqueuer.tasks[0].DispatchRevision != 1 || enqueuer.tasks[0].ObservationStartedAt == nil || enqueuer.tasks[0].Generation != "generation-1" {
		t.Fatalf("tasks = %+v", enqueuer.tasks)
	}
	if runner.Cursor() != nil {
		t.Fatalf("cursor = %+v, want reset after final page", runner.Cursor())
	}
}

func TestCooldownAccountRetestSchedulerBoundsRedisEnqueueConcurrency(t *testing.T) {
	candidates := make([]port.CooldownAccountRetestCandidate, 20)
	for i := range candidates {
		candidates[i] = cooldownAccountRetestCandidate("acct_"+strconv.Itoa(i), time.Now(), 1)
	}
	store := &cooldownAccountRetestRuntimeStoreStub{pages: []port.CooldownAccountRetestPage{{Candidates: candidates}}}
	enqueuer := &cooldownAccountRetestConcurrencyEnqueuerStub{}
	runner := newCooldownAccountRetestSchedulerRunner(
		store,
		cooldownAccountRetestSettingsReaderStub{settings: CooldownAccountRetestScheduleSettings{
			Interval: time.Second, BatchSize: len(candidates), MaxPauseMinutes: 7, MaxRecoveryHours: 12,
		}},
		enqueuer,
		cooldownAccountRetestCapacityStub{},
	)
	if _, err := runner.RunPage(t.Context(), time.Now()); err != nil {
		t.Fatalf("RunPage() error = %v", err)
	}
	if maxActive := enqueuer.maxActive.Load(); maxActive > int32(module.DefaultEnqueueWorkers) {
		t.Fatalf("concurrent enqueue calls = %d, want at most %d", maxActive, module.DefaultEnqueueWorkers)
	}
}

func cooldownAccountRetestCandidate(accountID string, started time.Time, configRevision int) port.CooldownAccountRetestCandidate {
	return port.CooldownAccountRetestCandidate{
		ID: accountID, ConfigRevision: configRevision, DispatchRevision: 1,
		ObservationStartedAt: &started, Generation: "generation-1",
	}
}

func TestCooldownAccountRetestRuntimeWaitsForConsumerShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stopped := make(chan struct{})
	err := runCooldownAccountRetestRuntime(ctx, nil, time.Hour, nil, func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		close(stopped)
		return nil
	})
	if err != nil {
		t.Fatalf("runCooldownAccountRetestRuntime() error = %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("runtime returned before consumer shutdown completed")
	}
}

func TestPostgresCooldownAccountRetestSettingsReaderMapsRequiredValues(t *testing.T) {
	store := &cooldownAccountRetestRuntimeStoreStub{settings: cooldownAccountRetestSettingsSnapshot(t, map[string]string{
		"cooldownAccountRetestIntervalSeconds": "9",
		"cooldownAccountRetestBatchSize":       "6",
		"defaultTemporaryUnschedulableMinutes": "8",
		"cooldownAccountRetestMaxBackoffHours": "15",
	})}
	settings, err := (postgresCooldownAccountRetestSettingsReader{store: store}).CooldownAccountRetestScheduleSettings(t.Context())
	if err != nil {
		t.Fatalf("CooldownAccountRetestScheduleSettings() error = %v", err)
	}
	if settings.Interval != 9*time.Second || settings.BatchSize != 6 || settings.MaxPauseMinutes != 8 || settings.MaxRecoveryHours != 15 {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestCooldownAccountRetestQueueCapacityUsesPendingActiveAndRetry(t *testing.T) {
	inspector := &cooldownAccountRetestQueueInspectorStub{info: queue.QueueInfo{Pending: 4, Active: 2, Retry: 100, Archived: 200}}
	snapshot, err := (cooldownAccountRetestQueueCapacity{inspector: inspector}).CooldownAccountRetestQueueSnapshot(t.Context())
	if err != nil {
		t.Fatalf("CooldownAccountRetestQueueSnapshot() error = %v", err)
	}
	if snapshot.PendingCount != 4 || snapshot.RunningCount != 2 || snapshot.RetryCount != 100 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

type cooldownAccountRetestRuntimeStoreStub struct {
	pages    []port.CooldownAccountRetestPage
	inputs   []port.CooldownAccountRetestListInput
	settings systemsettings.Snapshot
	closed   atomic.Bool
}

func (s *cooldownAccountRetestRuntimeStoreStub) Ping(context.Context) error { return nil }
func (s *cooldownAccountRetestRuntimeStoreStub) Close()                     { s.closed.Store(true) }
func (s *cooldownAccountRetestRuntimeStoreStub) ManagementSystemSettings(context.Context) (systemsettings.Snapshot, error) {
	return s.settings, nil
}

func (s *cooldownAccountRetestRuntimeStoreStub) ListDueCooldownAccountRetests(_ context.Context, input port.CooldownAccountRetestListInput) (port.CooldownAccountRetestPage, error) {
	s.inputs = append(s.inputs, input)
	page := s.pages[0]
	s.pages = s.pages[1:]
	return page, nil
}

func (s *cooldownAccountRetestRuntimeStoreStub) FindDueCooldownAccountRetest(context.Context, string, time.Time) (port.CooldownAccountRetestCandidate, bool, error) {
	return port.CooldownAccountRetestCandidate{}, false, nil
}

func (s *cooldownAccountRetestRuntimeStoreStub) LoadCooldownAccountRetestQuotaSubjects(_ context.Context, accountIDs []string, _ time.Time) ([]port.CooldownAccountRetestQuotaSubject, error) {
	subjects := make([]port.CooldownAccountRetestQuotaSubject, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		subjects = append(subjects, port.CooldownAccountRetestQuotaSubject{
			AccountID: accountID, AccessType: port.CooldownAccountRetestQuotaAccessOwner, AuthorizationValid: true,
		})
	}
	return subjects, nil
}

func (s *cooldownAccountRetestRuntimeStoreStub) LoadGatewayQuotaSnapshotCosts(_ context.Context, inputs []port.GatewayQuotaCostLookupInput) (map[string]port.GatewayQuotaCosts, error) {
	costs := make(map[string]port.GatewayQuotaCosts, len(inputs))
	for _, input := range inputs {
		costs[input.Key] = port.GatewayQuotaCosts{}
	}
	return costs, nil
}

func (s *cooldownAccountRetestRuntimeStoreStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	return "UTC", true, nil
}

type cooldownAccountRetestOutcomeRuntimeStoreStub struct {
	*cooldownAccountRetestRuntimeStoreStub
}

func (*cooldownAccountRetestOutcomeRuntimeStoreStub) RecordCooldownAccountRetestSuccess(context.Context, port.CooldownAccountRetestTask) error {
	return nil
}

func (*cooldownAccountRetestOutcomeRuntimeStoreStub) DeferCooldownAccountRetest(context.Context, port.CooldownAccountRetestTask, time.Duration) error {
	return nil
}

func (*cooldownAccountRetestOutcomeRuntimeStoreStub) RecordCooldownAccountRetestFailure(context.Context, port.CooldownAccountRetestTask, port.CooldownAccountRetestProbeResult) error {
	return nil
}

type cooldownAccountRetestOutcomesStub struct{}

func (*cooldownAccountRetestOutcomesStub) RecordCooldownAccountRetestSuccess(context.Context, port.CooldownAccountRetestTask) error {
	return nil
}

func (*cooldownAccountRetestOutcomesStub) DeferCooldownAccountRetest(context.Context, port.CooldownAccountRetestTask, time.Duration) error {
	return nil
}

func (*cooldownAccountRetestOutcomesStub) RecordCooldownAccountRetestFailure(context.Context, port.CooldownAccountRetestTask, port.CooldownAccountRetestProbeResult) error {
	return nil
}

type cooldownAccountRetestProbeStub struct{}

func (cooldownAccountRetestProbeStub) Probe(context.Context, port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error) {
	return port.CooldownAccountRetestProbeResult{}, nil
}

type cooldownAccountRetestQueueClientStub struct{}

func (*cooldownAccountRetestQueueClientStub) Ping() error  { return nil }
func (*cooldownAccountRetestQueueClientStub) Close() error { return nil }
func (*cooldownAccountRetestQueueClientStub) Enqueue(context.Context, string, []byte, queue.EnqueueOptions) (queue.TaskInfo, error) {
	return queue.TaskInfo{}, nil
}

func cooldownAccountRetestWorkerTestConfig() config.Config {
	return config.Config{
		CooldownAccountRetestWorkerEnabled: true,
		OwnerLockEnabled:                   true,
		OwnerLockRole:                      "worker",
		PostgresURL:                        "postgres://test.invalid/juhe_ai",
		RedisQueueURL:                      "redis://test.invalid:6379/0",
	}
}

func cooldownAccountRetestWorkerTestDependencies(
	store cooldownAccountRetestRuntimeStore,
	runConsumer func(context.Context, worker.CooldownAccountRetestConsumerOptions) error,
) cooldownAccountRetestWorkerDependencies {
	return cooldownAccountRetestWorkerDependencies{
		openStore: func(context.Context, string) (cooldownAccountRetestRuntimeStore, error) {
			return store, nil
		},
		newQueueClient: func(queue.RedisOptions) cooldownAccountRetestQueueClient {
			return &cooldownAccountRetestQueueClientStub{}
		},
		newQueueInspector: func(queue.RedisOptions) cooldownAccountRetestQueueInspector {
			return &cooldownAccountRetestQueueInspectorStub{}
		},
		runConsumer: runConsumer,
	}
}

type cooldownAccountRetestSettingsReaderStub struct {
	settings CooldownAccountRetestScheduleSettings
}

func (s cooldownAccountRetestSettingsReaderStub) CooldownAccountRetestScheduleSettings(context.Context) (CooldownAccountRetestScheduleSettings, error) {
	return s.settings, nil
}

type cooldownAccountRetestEnqueuerStub struct {
	tasks []port.CooldownAccountRetestTask
}

func (s *cooldownAccountRetestEnqueuerStub) EnqueueCooldownAccountRetest(_ context.Context, task port.CooldownAccountRetestTask) (bool, error) {
	s.tasks = append(s.tasks, task)
	return true, nil
}

type cooldownAccountRetestConcurrencyEnqueuerStub struct {
	active    atomic.Int32
	maxActive atomic.Int32
}

func (s *cooldownAccountRetestConcurrencyEnqueuerStub) EnqueueCooldownAccountRetest(context.Context, port.CooldownAccountRetestTask) (bool, error) {
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		observed := s.maxActive.Load()
		if observed >= active || s.maxActive.CompareAndSwap(observed, active) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	return true, nil
}

type cooldownAccountRetestCapacityStub struct{}

func (cooldownAccountRetestCapacityStub) CooldownAccountRetestQueueSnapshot(context.Context) (module.QueueSnapshot, error) {
	return module.QueueSnapshot{}, nil
}

type cooldownAccountRetestQueueInspectorStub struct {
	info queue.QueueInfo
	err  error
}

func (s *cooldownAccountRetestQueueInspectorStub) QueueInfo(string) (queue.QueueInfo, error) {
	return s.info, s.err
}

func (*cooldownAccountRetestQueueInspectorStub) Close() error { return nil }

func cooldownAccountRetestSettingsSnapshot(t *testing.T, overrides map[string]string) systemsettings.Snapshot {
	t.Helper()
	values := make(map[string]json.RawMessage, len(systemsettings.Definitions()))
	for _, definition := range systemsettings.Definitions() {
		switch definition.Kind {
		case systemsettings.ValueKindInteger:
			values[definition.Key] = json.RawMessage(strconv.Itoa(definition.Minimum))
		case systemsettings.ValueKindDecimal:
			values[definition.Key] = json.RawMessage(strconv.FormatFloat(definition.DecimalMinimum, 'f', -1, 64))
		case systemsettings.ValueKindTimezone:
			values[definition.Key] = json.RawMessage(`"UTC"`)
		}
	}
	for key, value := range overrides {
		values[key] = json.RawMessage(value)
	}
	snapshot, err := systemsettings.NewSnapshot(values)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	return snapshot
}
