package systemmetricsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestServiceSnapshotIsGoNativeAndBoundsCollectionCardinality(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 22, 9, 30, 0, 0, time.UTC)
	service := NewService(Dependencies{
		Runtime: runtimeSourceStub{snapshot: RuntimeSnapshot{
			ProcessRole: RuntimeRoleServer,
			SampledAt:   now,
			GoVersion:   "go1.26",
			Goroutines:  12,
		}},
		PostgreSQL: postgresSourceStub{snapshot: PostgreSQLPoolSnapshot{Max: 20, Total: 5, Acquired: 2, Idle: 3}},
		Redis: redisSourceStub{snapshots: []RedisPoolSnapshot{
			{Role: RedisRoleCache, Total: 3},
			{Role: RedisRoleState, Total: 4},
			{Role: RedisRoleQueue, Total: 5},
		}},
		Workers: workerSourceStub{snapshots: []WorkerSnapshot{
			{Role: RuntimeRoleServer, Ready: true},
			{Role: RuntimeRoleIngestWorker, Ready: true},
			{Role: RuntimeRoleStatsWorker, Ready: true},
		}},
		Queues: queueSourceStub{snapshots: []QueueSnapshot{
			{Name: "ingest", Pending: 1},
			{Name: "stats", Active: 1},
			{Name: "ops", Retry: 1},
		}},
	}, Options{
		Now: nowFunc(now),
		Limits: Limits{
			RedisPools: 2,
			Workers:    2,
			Queues:     2,
		},
	})

	snapshot := service.Snapshot(context.Background())
	if snapshot.RuntimeKind != RuntimeKindGo {
		t.Fatalf("runtime kind = %q, want %q", snapshot.RuntimeKind, RuntimeKindGo)
	}
	if snapshot.SampledAt != now {
		t.Fatalf("sampled at = %s, want %s", snapshot.SampledAt, now)
	}
	if !snapshot.Runtime.Available || snapshot.Runtime.Value.ProcessRole != RuntimeRoleServer {
		t.Fatalf("runtime section = %#v", snapshot.Runtime)
	}
	if !snapshot.PostgreSQL.Available || snapshot.PostgreSQL.Value.Acquired != 2 {
		t.Fatalf("postgres section = %#v", snapshot.PostgreSQL)
	}
	assertBoundedSection(t, "redis", snapshot.Redis, 2)
	assertBoundedSection(t, "workers", snapshot.Workers, 2)
	assertBoundedSection(t, "queues", snapshot.Queues, 2)

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	for _, forbidden := range []string{"eventLoop", "event_loop", "db-service"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Go runtime contract contains Node-only field %q: %s", forbidden, encoded)
		}
	}
}

func TestServiceSnapshotKeepsDependencyFailuresLocalAndRedacted(t *testing.T) {
	t.Parallel()

	service := NewService(Dependencies{
		Runtime:    runtimeSourceStub{snapshot: RuntimeSnapshot{ProcessRole: RuntimeRoleOpsWorker}},
		Workers:    workerSourceStub{err: errors.New("worker token=secret")},
		Queues:     queueSourceStub{err: errors.New("redis://user:password@example")},
		PostgreSQL: postgresSourceStub{snapshot: PostgreSQLPoolSnapshot{Max: 10}},
	}, Options{})

	snapshot := service.Snapshot(context.Background())
	if snapshot.Workers.Available || snapshot.Workers.UnavailableReason != UnavailableSnapshotFailed {
		t.Fatalf("workers section = %#v", snapshot.Workers)
	}
	if snapshot.Queues.Available || snapshot.Queues.UnavailableReason != UnavailableSnapshotFailed {
		t.Fatalf("queues section = %#v", snapshot.Queues)
	}
	if !snapshot.Runtime.Available || !snapshot.PostgreSQL.Available {
		t.Fatalf("unrelated sections should remain available: %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	for _, secret := range []string{"secret", "password", "redis://"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("snapshot leaked provider error %q: %s", secret, encoded)
		}
	}
}

func TestServiceSnapshotMarksMissingSourcesUnavailable(t *testing.T) {
	t.Parallel()

	snapshot := NewService(Dependencies{}, Options{}).Snapshot(context.Background())
	if snapshot.Runtime.Available || snapshot.Runtime.UnavailableReason != UnavailableNotConfigured {
		t.Fatalf("runtime section = %#v", snapshot.Runtime)
	}
	if snapshot.PostgreSQL.Available || snapshot.PostgreSQL.UnavailableReason != UnavailableNotConfigured {
		t.Fatalf("postgres section = %#v", snapshot.PostgreSQL)
	}
	if snapshot.Redis.Available || snapshot.Redis.UnavailableReason != UnavailableNotConfigured {
		t.Fatalf("redis section = %#v", snapshot.Redis)
	}
	if snapshot.Workers.Available || snapshot.Workers.UnavailableReason != UnavailableNotConfigured {
		t.Fatalf("workers section = %#v", snapshot.Workers)
	}
	if snapshot.Queues.Available || snapshot.Queues.UnavailableReason != UnavailableNotConfigured {
		t.Fatalf("queues section = %#v", snapshot.Queues)
	}
}

func TestStandardRuntimeSourceReportsNativeGoRuntime(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	startedAt := now.Add(-3 * time.Minute)
	source := NewStandardRuntimeSource(RuntimeRoleStatsWorker, startedAt, nowFunc(now))

	snapshot := source.SnapshotRuntime()
	if snapshot.ProcessRole != RuntimeRoleStatsWorker {
		t.Fatalf("process role = %q", snapshot.ProcessRole)
	}
	if snapshot.SampledAt != now || snapshot.UptimeSeconds != 180 {
		t.Fatalf("sample time/uptime = %s/%d", snapshot.SampledAt, snapshot.UptimeSeconds)
	}
	if snapshot.ProcessPID <= 0 || snapshot.GoVersion == "" || snapshot.GoMaxProcs <= 0 {
		t.Fatalf("process identity incomplete: %#v", snapshot)
	}
	if snapshot.Goroutines <= 0 {
		t.Fatalf("goroutines = %d, want positive", snapshot.Goroutines)
	}
	if snapshot.HeapAllocBytes == 0 || snapshot.HeapObjects == 0 {
		t.Fatalf("heap metrics incomplete: %#v", snapshot)
	}
}

func TestLimitsAreAlwaysClampedToHardMaximums(t *testing.T) {
	t.Parallel()

	service := NewService(Dependencies{
		Redis:   redisSourceStub{snapshots: make([]RedisPoolSnapshot, HardMaxRedisPools+10)},
		Workers: workerSourceStub{snapshots: make([]WorkerSnapshot, HardMaxWorkers+10)},
		Queues:  queueSourceStub{snapshots: make([]QueueSnapshot, HardMaxQueues+10)},
	}, Options{Limits: Limits{
		RedisPools: HardMaxRedisPools + 100,
		Workers:    HardMaxWorkers + 100,
		Queues:     HardMaxQueues + 100,
	}})

	snapshot := service.Snapshot(context.Background())
	assertBoundedSection(t, "redis", snapshot.Redis, HardMaxRedisPools)
	assertBoundedSection(t, "workers", snapshot.Workers, HardMaxWorkers)
	assertBoundedSection(t, "queues", snapshot.Queues, HardMaxQueues)
}

func assertBoundedSection[T any](t *testing.T, name string, section ListSection[T], want int) {
	t.Helper()
	if !section.Available || !section.Truncated || len(section.Items) != want {
		t.Fatalf("%s section = %#v, want available/truncated with %d items", name, section, want)
	}
}

func nowFunc(now time.Time) func() time.Time {
	return func() time.Time { return now }
}

type runtimeSourceStub struct{ snapshot RuntimeSnapshot }

func (s runtimeSourceStub) SnapshotRuntime() RuntimeSnapshot { return s.snapshot }

type postgresSourceStub struct{ snapshot PostgreSQLPoolSnapshot }

func (s postgresSourceStub) SnapshotPostgreSQLPool() PostgreSQLPoolSnapshot { return s.snapshot }

type redisSourceStub struct{ snapshots []RedisPoolSnapshot }

func (s redisSourceStub) SnapshotRedisPools() []RedisPoolSnapshot { return s.snapshots }

type workerSourceStub struct {
	snapshots []WorkerSnapshot
	err       error
}

func (s workerSourceStub) SnapshotWorkers(context.Context, int) ([]WorkerSnapshot, error) {
	return s.snapshots, s.err
}

type queueSourceStub struct {
	snapshots []QueueSnapshot
	err       error
}

func (s queueSourceStub) SnapshotQueues(context.Context, int) ([]QueueSnapshot, error) {
	return s.snapshots, s.err
}
