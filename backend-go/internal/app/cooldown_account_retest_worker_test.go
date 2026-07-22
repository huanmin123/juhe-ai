package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/jobs/queue"
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

func TestCooldownAccountRetestSchedulerCarriesCursorAndSettingsAcrossPages(t *testing.T) {
	now := time.Date(2026, 7, 22, 9, 30, 0, 0, time.UTC)
	nextCursor := &port.CooldownAccountRetestCursor{CooldownUntil: now, ID: "acct_1"}
	store := &cooldownAccountRetestRuntimeStoreStub{
		pages: []port.CooldownAccountRetestPage{
			{Candidates: []port.CooldownAccountRetestCandidate{{ID: "acct_1", ConfigRevision: 3}}, NextCursor: nextCursor},
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
	if len(enqueuer.tasks) != 1 || enqueuer.tasks[0].MaxPauseMinutes != 7 || enqueuer.tasks[0].MaxRecoveryHours != 12 {
		t.Fatalf("tasks = %+v", enqueuer.tasks)
	}
	if runner.Cursor() != nil {
		t.Fatalf("cursor = %+v, want reset after final page", runner.Cursor())
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

func TestCooldownAccountRetestQueueCapacityUsesPendingAndActive(t *testing.T) {
	inspector := &cooldownAccountRetestQueueInspectorStub{info: queue.QueueInfo{Pending: 4, Active: 2, Retry: 100, Archived: 200}}
	snapshot, err := (cooldownAccountRetestQueueCapacity{inspector: inspector}).CooldownAccountRetestQueueSnapshot(t.Context())
	if err != nil {
		t.Fatalf("CooldownAccountRetestQueueSnapshot() error = %v", err)
	}
	if snapshot.PendingCount != 4 || snapshot.RunningCount != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

type cooldownAccountRetestRuntimeStoreStub struct {
	pages    []port.CooldownAccountRetestPage
	inputs   []port.CooldownAccountRetestListInput
	settings systemsettings.Snapshot
}

func (s *cooldownAccountRetestRuntimeStoreStub) Ping(context.Context) error { return nil }
func (s *cooldownAccountRetestRuntimeStoreStub) Close()                     {}
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
