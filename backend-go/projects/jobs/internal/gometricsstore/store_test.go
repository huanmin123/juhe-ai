package gometricsstore_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	_ "modernc.org/sqlite"
)

func TestSQLiteStoreSchemaInsertIdempotencyAndTrend(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "gometrics.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := gometrics.NewStore(db, gometrics.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 2, 3, 15, 0, 0, time.UTC)
	sample := gometrics.RuntimeSnapshot{SampledAt: when, ProcessPID: 7, Service: "jobs", Role: "active", Goroutines: 2, HeapAllocBytes: 100, HeapLiveBytes: 90, HeapObjects: 10, Threads: 3}
	inserted, err := store.InsertSnapshot(ctx, sample)
	if err != nil || !inserted {
		t.Fatalf("first insert inserted=%v err=%v", inserted, err)
	}
	inserted, err = store.InsertSnapshot(ctx, sample)
	if err != nil || inserted {
		t.Fatalf("duplicate insert inserted=%v err=%v", inserted, err)
	}
	second := sample
	second.SampledAt = when.Add(15 * time.Minute)
	second.Goroutines = 6
	second.HeapAllocBytes = 300
	if inserted, err = store.Record(ctx, second); err != nil || !inserted {
		t.Fatalf("second insert inserted=%v err=%v", inserted, err)
	}
	trend, err := store.QueryTrend(ctx, "jobs", "active", when.Add(-time.Hour), when.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 1 || trend[0].SampleCount != 2 || trend[0].GoroutinesAvg != 4 || trend[0].GoroutinesMax != 6 {
		t.Fatalf("unexpected hourly trend: %+v", trend)
	}
	daily, err := store.QueryTrendWindows(ctx, "jobs", "active", when.Truncate(24*time.Hour), when.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 1 || daily[0].SampleCount != 2 {
		t.Fatalf("unexpected daily trend: %+v", daily)
	}
}

func TestSQLiteStoreAggregatesOptionalAndSchedulerMetrics(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "optional.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := gometrics.NewStore(db, gometrics.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cpu1, cpu2 := 10.0, 30.0
	rss1, rss2 := uint64(1000), uint64(2000)
	fd1, fd2 := uint64(3), uint64(5)
	when := time.Date(2026, 9, 2, 4, 0, 0, 0, time.UTC)
	for _, sample := range []gometrics.RuntimeSnapshot{
		{SampledAt: when, ProcessPID: 1, Service: "jobs", Role: "active", Goroutines: 2, GoroutinesRunnable: 4, GoroutinesWaiting: 1, GOMAXPROCS: 8, Threads: 3, UptimeSeconds: 1},
		{SampledAt: when.Add(time.Minute), ProcessPID: 1, Service: "jobs", Role: "active", Goroutines: 6, GoroutinesRunnable: 8, GoroutinesWaiting: 3, GOMAXPROCS: 8, Threads: 5, CPUPercent: &cpu2, RSSBytes: &rss2, FDCount: &fd2, UptimeSeconds: 2},
		{SampledAt: when.Add(2 * time.Minute), ProcessPID: 1, Service: "jobs", Role: "active", Goroutines: 10, GoroutinesRunnable: 6, GoroutinesWaiting: 5, GOMAXPROCS: 8, Threads: 7, CPUPercent: &cpu1, RSSBytes: &rss1, FDCount: &fd1, UptimeSeconds: 3},
	} {
		if inserted, err := store.InsertSnapshot(ctx, sample); err != nil || !inserted {
			t.Fatalf("inserted=%v err=%v", inserted, err)
		}
	}
	trend, err := store.QueryTrend(ctx, "jobs", "active", when.Add(-time.Hour), when.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 1 {
		t.Fatalf("unexpected trend: %#v", trend)
	}
	got := trend[0]
	if got.GoroutinesRunnableAvg != 6 || got.GoroutinesRunnableMax != 8 || got.GoroutinesWaitingAvg != 3 || got.GOMAXPROCSAvg != 8 || got.UptimeSecondsAvg != 2 || got.UptimeSecondsMax != 3 {
		t.Fatalf("scheduler/runtime aggregate mismatch: %#v", got)
	}
	if got.CPUPercentAvg == nil || *got.CPUPercentAvg != 20 || got.CPUPercentMax == nil || *got.CPUPercentMax != 30 || got.RSSBytesAvg == nil || *got.RSSBytesAvg != 1500 || got.FDCountAvg == nil || *got.FDCountAvg != 4 {
		t.Fatalf("optional aggregate mismatch: %#v", got)
	}
}

func TestInsertRequiresExplicitSchema(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "missing-schema.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := gometrics.NewStore(db, gometrics.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.InsertSnapshot(context.Background(), gometrics.RuntimeSnapshot{SampledAt: time.Now().UTC(), ProcessPID: 1, Service: "jobs", Role: "active"})
	if err == nil {
		t.Fatal("expected missing schema error")
	}
}

func TestCheckSchemaRejectsMalformedSQLitePrimaryKey(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "malformed-schema.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := gometrics.NewStore(db, gometrics.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE go_runtime_metrics_samples`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE go_runtime_metrics_samples (
service TEXT NOT NULL,
role TEXT NOT NULL,
runtime_kind TEXT NOT NULL,
process_pid INTEGER NOT NULL,
sampled_at TIMESTAMP NOT NULL,
goroutines INTEGER NOT NULL,
goroutines_runnable INTEGER NOT NULL,
goroutines_waiting INTEGER NOT NULL,
threads INTEGER NOT NULL,
gomaxprocs INTEGER NOT NULL,
heap_alloc_bytes INTEGER NOT NULL,
heap_live_bytes INTEGER NOT NULL,
heap_objects INTEGER NOT NULL,
cpu_percent REAL,
rss_bytes INTEGER,
fd_count INTEGER,
uptime_seconds REAL NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "primary key mismatch") {
		t.Fatalf("malformed primary key must fail closed, err=%v", err)
	}
}

func TestPruneBeforeRemovesExpiredSamplesAndWindows(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "prune.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := gometrics.NewStore(db, gometrics.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 7, 1, 3, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 9, 2, 3, 0, 0, 0, time.UTC)
	for _, sample := range []gometrics.RuntimeSnapshot{
		{SampledAt: old, ProcessPID: 1, Service: "jobs", Role: "active"},
		{SampledAt: newer, ProcessPID: 1, Service: "jobs", Role: "active"},
	} {
		if _, err := store.InsertSnapshot(ctx, sample); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PruneBefore(ctx, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	trend, err := store.QueryTrend(ctx, "jobs", "active", old.Add(-time.Hour), newer.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 1 || !trend[0].WindowStart.Equal(newer.Truncate(time.Hour)) {
		t.Fatalf("unexpected retained trend: %+v", trend)
	}
}

func TestTrendRangeIsBounded(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "range.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := gometrics.NewStore(db, gometrics.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(91 * 24 * time.Hour)
	if _, err := store.QueryTrend(context.Background(), "jobs", "active", from, to); !errors.Is(err, gometrics.ErrTrendRangeTooLarge) {
		t.Fatalf("expected bounded trend error, got %v", err)
	}
}
