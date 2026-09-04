package statsverify

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"
)

// steppingClock advances by a fixed step on every Sleep, letting tests drive
// the 5s aggregation time budget without real waiting.
type steppingClock struct {
	FixedClock
	Step time.Duration
}

func (c *steppingClock) Sleep(d time.Duration) {
	c.Current = c.Current.Add(c.Step)
}

func TestRunClientIPStatsAggregationBatchLoopStopsOnShortBatch(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-10T12:00:00Z")
	for index := 0; index < 3; index++ {
		insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
			ID: string(rune('a' + index)), SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
			Success: 1, CreatedAt: "2026-03-10T10:0" + string(rune('0'+index)) + ":00.000Z",
		})
	}
	// Node caps: batchSize <= 1000, maxBatches <= 10 regardless of settings.
	result, err := store.RunClientIPStatsAggregation(ctx, RunClientIPStatsAggregationOptions{
		Clock:                            clock,
		StatsAggregationBatchSize:        10000, // setting above the cap
		StatsAggregationMaxBatchesPerRun: 100,   // setting above the cap
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// batchSize caps to 1000 so the 3 rows finish in one short batch.
	if result.Batches != 1 || result.Processed != 3 {
		t.Fatalf("result=%+v, want one short batch of 3", result)
	}
}

func TestRunClientIPStatsAggregationTimeBudgetMirrorsNodeLoop(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	base := fixedUTC(t, "2026-03-10T12:00:00Z").Now()
	clock := &steppingClock{FixedClock: FixedClock{Current: base}, Step: 6 * time.Second}
	for index := 0; index < 4; index++ {
		insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
			ID: string(rune('a' + index)), SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
			Success: 1, CreatedAt: "2026-03-10T10:0" + string(rune('0'+index)) + ":00.000Z",
		})
	}
	// Mirror aggregateClientIpStats: batchSize=1, maxBatches=10.
	result, err := store.RunClientIPStatsAggregation(ctx, RunClientIPStatsAggregationOptions{
		Clock:                            clock,
		StatsAggregationBatchSize:        1,
		StatsAggregationMaxBatchesPerRun: 10,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// batch1 full -> sleep(+6s); batch2 full -> elapsed 6s >= 5s stops.
	if result.Batches != 2 || result.Processed != 2 {
		t.Fatalf("result=%+v, want 2 batches / 2 processed before the time budget", result)
	}
	// Remaining rows are picked up by the next scheduled run.
	rest, err := store.RunClientIPStatsAggregation(ctx, RunClientIPStatsAggregationOptions{
		Clock:                            clock,
		StatsAggregationBatchSize:        1,
		StatsAggregationMaxBatchesPerRun: 10,
	})
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if rest.Processed != 2 {
		t.Fatalf("rest processed=%d, want 2", rest.Processed)
	}
}

func TestRunClientIPStatsAggregationEmptyInputRunsWindowRefresh(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-10T12:00:00Z")
	result, err := store.RunClientIPStatsAggregation(ctx, RunClientIPStatsAggregationOptions{Clock: clock})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Processed != 0 || result.Batches != 1 {
		t.Fatalf("result=%+v, want a single empty batch", result)
	}
}

func TestRunUsageStatsConsistencyCheckWarnsOnDrift(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-10T12:00:00Z").Now()
	seedConsistencyDaily(t, ctx, store, "sys", "system_account", "sys", "2026-03-08", 5, 1)
	seedConsistencyHourly(t, ctx, store, "sys", "system_account", "sys", "2026-03-08T00", 3, 1)

	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	issues, err := store.RunUsageStatsConsistencyCheck(ctx, now, logger)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("expected drift issues")
	}
	if !bytes.Contains(buf.Bytes(), []byte("usage_stats_consistency_mismatch")) {
		t.Fatalf("warn log missing: %q", buf.String())
	}

	// Consistent buckets stay silent.
	buf.Reset()
	consistent := openTestStore(t, "UTC")
	seedConsistencyDaily(t, ctx, consistent, "sys", "system_account", "sys", "2026-03-08", 5, 1)
	seedConsistencyHourly(t, ctx, consistent, "sys", "system_account", "sys", "2026-03-08T00", 5, 1)
	issues, err = consistent.RunUsageStatsConsistencyCheck(ctx, now, logger)
	if err != nil {
		t.Fatalf("check2: %v", err)
	}
	if len(issues) != 0 || buf.Len() != 0 {
		t.Fatalf("consistent buckets must not warn: issues=%v log=%q", issues, buf.String())
	}
}

func TestRunGroupAccountStatsRefreshEndToEnd(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-02T12:00:00Z").Now()
	seedGroupFixture(t, ctx, store)
	if err := store.MarkGroupAccountStatsStartupDirty(ctx, now); err != nil {
		t.Fatalf("startup dirty: %v", err)
	}
	refreshed, err := store.RunGroupAccountStatsRefresh(ctx, now)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed != 1 {
		t.Fatalf("refreshed=%d, want 1", refreshed)
	}
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM group_account_stats`); got != 3 {
		t.Fatalf("cache rows=%d, want 3", got)
	}
}

func TestLoadUsageStatsTimezoneCacheAndValidation(t *testing.T) {
	store := openTestStore(t, "Asia/Shanghai")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-10T12:00:00Z")

	got, err := store.LoadUsageStatsTimezone(ctx, clock.Now())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != "Asia/Shanghai" {
		t.Fatalf("timezone=%q", got)
	}

	// Mutating the setting inside the 60s TTL must not change the cached value.
	mustExec(t, ctx, store.business, `UPDATE system_settings SET value_json = '"UTC"' WHERE key = 'usageStatsTimezone'`)
	cached, err := store.LoadUsageStatsTimezone(ctx, clock.Now().Add(30*time.Second))
	if err != nil {
		t.Fatalf("cached load: %v", err)
	}
	if cached != "Asia/Shanghai" {
		t.Fatalf("cached timezone=%q, want Asia/Shanghai within TTL", cached)
	}

	// After the TTL the fresh value is read.
	afterTTL, err := store.LoadUsageStatsTimezone(ctx, clock.Now().Add(61*time.Second))
	if err != nil {
		t.Fatalf("post-TTL load: %v", err)
	}
	if afterTTL != "UTC" {
		t.Fatalf("post-TTL timezone=%q, want UTC", afterTTL)
	}

	// An unknown IANA name mirrors normalizeUsageStatsTimezone's failure.
	mustExec(t, ctx, store.business, `UPDATE system_settings SET value_json = '"Mars/Olympus"' WHERE key = 'usageStatsTimezone'`)
	if _, err := store.LoadUsageStatsTimezone(ctx, clock.Now().Add(200*time.Second)); err == nil {
		t.Fatal("unknown timezone must fail")
	}

	// A missing setting mirrors the Node error text.
	mustExec(t, ctx, store.business, `DELETE FROM system_settings WHERE key = 'usageStatsTimezone'`)
	if _, err := store.LoadUsageStatsTimezone(ctx, clock.Now().Add(300*time.Second)); err == nil {
		t.Fatal("missing timezone setting must fail")
	}
}

func TestBoundPositiveInt(t *testing.T) {
	cases := []struct {
		in, min, max, want int
	}{
		{in: 5, min: 1, max: 10, want: 5},
		{in: 0, min: 1, max: 10, want: 1},
		{in: -3, min: 1, max: 10, want: 1},
		{in: 99, min: 1, max: 10, want: 10},
	}
	for _, tc := range cases {
		if got := boundPositiveInt(tc.in, tc.min, tc.max); got != tc.want {
			t.Fatalf("boundPositiveInt(%d,%d,%d)=%d, want %d", tc.in, tc.min, tc.max, got, tc.want)
		}
	}
}
