package gometrics

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newSamplerTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sampler.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestSamplerEstablishesCPUBaselineBeforePersisting is migrated from the
// former jobs gometricsstore suite: the first write only establishes the CPU
// delta baseline and must not persist a partial sample.
func TestSamplerEstablishesCPUBaselineBeforePersisting(t *testing.T) {
	store := newSamplerTestStore(t)
	sampler, err := NewSampler(New("juhe-ai", "jobs"), store, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := sampler.write(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := store.QueryTrend(context.Background(), "juhe-ai", "jobs", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("baseline must not persist a partial sample: %#v", rows)
	}
	if err := sampler.write(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err = store.QueryTrend(context.Background(), "juhe-ai", "jobs", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SampleCount != 1 {
		t.Fatalf("second sample should persist: %#v", rows)
	}
}

// TestSamplerRunPersistsSamplesUntilContextCancel covers the Run cycle:
// baseline write first, ticker-driven persists after it, and a clean nil
// return on context cancellation.
func TestSamplerRunPersistsSamplesUntilContextCancel(t *testing.T) {
	store := newSamplerTestStore(t)
	sampler, err := NewSampler(New("juhe-ai", "gateway"), store, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- sampler.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	persisted := 0
	for time.Now().Before(deadline) {
		rows, queryErr := store.QueryTrend(context.Background(), "juhe-ai", "gateway", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		if queryErr != nil {
			cancel()
			t.Fatalf("query trend: %v", queryErr)
		}
		if len(rows) > 0 {
			persisted = len(rows)
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("sampler Run returned error: %v", err)
	}
	if persisted == 0 {
		t.Fatal("sampler Run never persisted a sample after the CPU baseline")
	}
}

// TestSamplerPruneAppliesConfiguredAndDefaultRetention covers the prune path:
// a positive Retention is honored as-is and a non-positive one falls back to
// the 30-day default.
func TestSamplerPruneAppliesConfiguredAndDefaultRetention(t *testing.T) {
	store := newSamplerTestStore(t)
	sampler, err := NewSampler(New("juhe-ai", "jobs"), store, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old := time.Now().UTC().Add(-45 * 24 * time.Hour).Truncate(time.Hour)
	if _, err := store.InsertSnapshot(ctx, RuntimeSnapshot{SampledAt: old, ProcessPID: 1, Service: "juhe-ai", Role: "jobs", Goroutines: 2}); err != nil {
		t.Fatal(err)
	}
	sampler.Retention = 46 * 24 * time.Hour
	if err := sampler.prune(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := store.QueryTrend(ctx, "juhe-ai", "jobs", old.Add(-time.Hour), old.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("sample inside retention must survive: %#v", rows)
	}
	sampler.Retention = 0
	if err := sampler.prune(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err = store.QueryTrend(ctx, "juhe-ai", "jobs", old.Add(-time.Hour), old.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("default retention must prune 45-day-old samples: %#v", rows)
	}
}

func TestNewSamplerRequiresCollectorAndStore(t *testing.T) {
	if _, err := NewSampler(nil, nil, time.Second); err == nil {
		t.Fatal("expected nil collector/store error")
	}
}
