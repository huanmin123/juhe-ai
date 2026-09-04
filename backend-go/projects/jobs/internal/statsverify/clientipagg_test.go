package statsverify

import (
	"context"
	"testing"
	"time"
)

// Golden derivations follow the Node chain
// aggregateClientIpStatsBatchAsync -> writeClientIpStatsAggregatesFromUsageRowsAsync:
//   - dimension key ipHash:statDate with statDate = dateKey(created_at, tz);
//   - eligible rows: created_at <= now-15s, traffic_source NOT IN
//     ('runtime_recovery_probe','cooldown_retest');
//   - daily UPSERT accumulates every metric and keeps MAX(duration_ms_max);
//   - registry keeps MIN(first_seen_at) / MAX(last_seen_at);
//   - the job-state cursor lands on the last processed (created_at, id).

func TestAggregateClientIPStatsBatchNormalizesAndAccumulates(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-10T12:00:00Z")

	// Two rows for 1.2.3.4 on the same stats day: input 10+20=30,
	// duration max(100,200)=200, one success one failure.
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "r1", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"), AccountID: strPtr("acc-1"),
		Success: 1, InputTokens: intPtr(10), OutputTokens: intPtr(1),
		DurationMs: intPtr(100), FirstTokenMs: intPtr(10),
		CacheReadCostUsd: f64Ptr(0.25), CostUsd: f64Ptr(0.5),
		CreatedAt: "2026-03-10T10:00:00.000Z",
	})
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "r2", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
		Success: 0, InputTokens: intPtr(20), OutputTokens: intPtr(2),
		DurationMs: intPtr(200), FirstTokenMs: nil,
		CostUsd:   f64Ptr(0.75),
		CreatedAt: "2026-03-10T10:30:00.000Z",
	})
	// Second identity on the same day.
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "r3", SystemAccountID: "sys", ClientIP: strPtr("192.168.1.1"),
		Success: 1, InputTokens: intPtr(5),
		CreatedAt: "2026-03-10T11:00:00.000Z",
	})

	processed, err := store.AggregateClientIPStatsBatch(ctx, 100, clock.Now())
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if processed != 3 {
		t.Fatalf("processed=%d, want 3", processed)
	}

	hashMain := NormalizeClientIPForStats("1.2.3.4").IPHash
	hashSecond := NormalizeClientIPForStats("192.168.1.1").IPHash

	if got := queryInt(t, ctx, store.db,
		`SELECT request_count FROM client_ip_stats_daily WHERE ip_hash = ? AND stat_date = '2026-03-10'`, hashMain); got != 2 {
		t.Fatalf("request_count=%d, want 2", got)
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT success_count FROM client_ip_stats_daily WHERE ip_hash = ? AND stat_date = '2026-03-10'`, hashMain); got != 1 {
		t.Fatalf("success_count=%d, want 1", got)
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT error_count FROM client_ip_stats_daily WHERE ip_hash = ? AND stat_date = '2026-03-10'`, hashMain); got != 1 {
		t.Fatalf("error_count=%d, want 1", got)
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT input_tokens FROM client_ip_stats_daily WHERE ip_hash = ? AND stat_date = '2026-03-10'`, hashMain); got != 30 {
		t.Fatalf("input_tokens=%d, want 30", got)
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT duration_ms_max FROM client_ip_stats_daily WHERE ip_hash = ? AND stat_date = '2026-03-10'`, hashMain); got != 200 {
		t.Fatalf("duration_ms_max=%d, want 200", got)
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT first_token_ms_count FROM client_ip_stats_daily WHERE ip_hash = ? AND stat_date = '2026-03-10'`, hashMain); got != 1 {
		t.Fatalf("first_token_ms_count=%d, want 1 (null first_token contributes no count)", got)
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT COUNT(*) FROM client_ip_account_stats_daily WHERE ip_hash = ? AND account_id = 'acc-1'`, hashMain); got != 1 {
		t.Fatalf("account daily rows=%d, want 1", got)
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT COUNT(*) FROM client_ip_account_stats_daily WHERE ip_hash = ?`, hashSecond); got != 0 {
		t.Fatalf("row without account must not create account stats")
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT COUNT(*) FROM client_ip_registry`); got != 2 {
		t.Fatalf("registry rows=%d, want 2", got)
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT bucket_no FROM client_ip_registry WHERE ip_hash = ?`, hashMain); got != 2884 {
		t.Fatalf("registry bucket=%d, want golden 2884", got)
	}

	// Cursor landed on the last processed row.
	state, err := store.AggregateClientIPStatsBatchState(ctx)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.CursorCreatedAt != "2026-03-10T11:00:00.000Z" || state.CursorID != "r3" {
		t.Fatalf("cursor=%+v", state)
	}
	// cursorLagSecondsFromCreatedAt(last.created_at) = now(12:00) - 11:00.
	if state.LagSeconds == nil || *state.LagSeconds != 3600 {
		t.Fatalf("lag=%v, want 3600", state.LagSeconds)
	}

	// Both identities were marked dirty for both range windows.
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM client_ip_range_window_dirty_ips`); got != 2 {
		t.Fatalf("dirty rows=%d, want 2", got)
	}
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM client_ip_account_range_window_dirty_ips`); got != 2 {
		t.Fatalf("account dirty rows=%d, want 2", got)
	}
}

func TestAggregateClientIPStatsBatchRespectsSafetyWindowAndExcludedSources(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-10T12:00:00Z")

	// Fresh rows inside the 15s safety delay stay untouched.
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "fresh", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
		Success: 1, CreatedAt: "2026-03-10T11:59:50.000Z",
	})
	// Excluded traffic sources are skipped for aggregation...
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "probe", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
		Success: 1, TrafficSource: "runtime_recovery_probe", CreatedAt: "2026-03-10T10:00:00.000Z",
	})
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "cooldown", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
		Success: 1, TrafficSource: "cooldown_retest", CreatedAt: "2026-03-10T10:30:00.000Z",
	})
	// ...but a regular row after them still aggregates and drives the cursor.
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "real", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
		Success: 1, CreatedAt: "2026-03-10T11:00:00.000Z",
	})

	processed, err := store.AggregateClientIPStatsBatch(ctx, 100, clock.Now())
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed=%d, want 1 (safety-delayed and excluded rows skipped)", processed)
	}
	hash := NormalizeClientIPForStats("1.2.3.4").IPHash
	if got := queryInt(t, ctx, store.db, `SELECT request_count FROM client_ip_stats_daily WHERE ip_hash = ?`, hash); got != 1 {
		t.Fatalf("request_count=%d, want 1", got)
	}
	state, err := store.AggregateClientIPStatsBatchState(ctx)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.CursorID != "real" {
		t.Fatalf("cursor must skip excluded rows, got %+v", state)
	}
}

func TestAggregateClientIPStatsBatchEmptyInputAdvancesIgnoredCursorAndLag(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-10T12:00:00Z")

	// Only an excluded row: processed stays 0, the cursor jumps to the
	// ignored row (latestIgnoredUsageRecordCursor) and lag reflects it.
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "probe", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
		Success: 1, TrafficSource: "runtime_recovery_probe", CreatedAt: "2026-03-10T09:00:00.000Z",
	})
	processed, err := store.AggregateClientIPStatsBatch(ctx, 100, clock.Now())
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if processed != 0 {
		t.Fatalf("processed=%d, want 0", processed)
	}
	state, err := store.AggregateClientIPStatsBatchState(ctx)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.CursorID != "probe" || state.CursorCreatedAt != "2026-03-10T09:00:00.000Z" {
		t.Fatalf("ignored cursor not advanced: %+v", state)
	}
	// Node computes lag from the newest eligible record AFTER the advanced
	// cursor (latestUsageRecordLagSeconds with the ignored cursor); with only
	// an excluded row present there is no such record, so lag is 0.
	if state.LagSeconds == nil || *state.LagSeconds != 0 {
		t.Fatalf("lag=%v, want 0", state.LagSeconds)
	}

	// A completely empty store reports lag 0 with success timestamps
	// (aggregateClientIpStatsBatch shardLocations-empty branch).
	emptyStore := openTestStore(t, "UTC")
	emptyProcessed, err := emptyStore.AggregateClientIPStatsBatch(ctx, 100, clock.Now())
	if err != nil {
		t.Fatalf("empty aggregate: %v", err)
	}
	if emptyProcessed != 0 {
		t.Fatalf("empty processed=%d", emptyProcessed)
	}
	emptyState, err := emptyStore.AggregateClientIPStatsBatchState(ctx)
	if err != nil {
		t.Fatalf("empty state: %v", err)
	}
	if emptyState.CursorID != "" {
		t.Fatalf("empty cursor=%q", emptyState.CursorID)
	}
}

func TestAggregateClientIPStatsBatchSplitsStatDatesByTimezone(t *testing.T) {
	store := openTestStore(t, "Asia/Shanghai")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-10T20:00:00Z")

	// 2026-03-10T17:00Z is 2026-03-11T01:00+08:00; 2026-03-10T15:00Z is
	// still 2026-03-10T23:00+08:00 — same UTC day, two stats days.
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "tz1", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
		Success: 1, InputTokens: intPtr(7), CreatedAt: "2026-03-10T15:00:00.000Z",
	})
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "tz2", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
		Success: 1, InputTokens: intPtr(9), CreatedAt: "2026-03-10T17:00:00.000Z",
	})
	if _, err := store.AggregateClientIPStatsBatch(ctx, 100, clock.Now()); err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	hash := NormalizeClientIPForStats("1.2.3.4").IPHash
	if got := queryInt(t, ctx, store.db,
		`SELECT input_tokens FROM client_ip_stats_daily WHERE ip_hash = ? AND stat_date = '2026-03-10'`, hash); got != 7 {
		t.Fatalf("2026-03-10 input_tokens=%d, want 7", got)
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT input_tokens FROM client_ip_stats_daily WHERE ip_hash = ? AND stat_date = '2026-03-11'`, hash); got != 9 {
		t.Fatalf("2026-03-11 input_tokens=%d, want 9", got)
	}
}

func TestAggregateClientIPStatsBatchLimitSplitsBatches(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-10T12:00:00Z")
	for index := 0; index < 3; index++ {
		insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
			ID: string(rune('a' + index)), SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
			Success: 1, InputTokens: intPtr(1), CreatedAt: "2026-03-10T10:0" + string(rune('0'+index)) + ":00.000Z",
		})
	}

	processed, err := store.AggregateClientIPStatsBatch(ctx, 2, clock.Now())
	if err != nil {
		t.Fatalf("batch1: %v", err)
	}
	if processed != 2 {
		t.Fatalf("batch1 processed=%d, want 2", processed)
	}
	processed, err = store.AggregateClientIPStatsBatch(ctx, 2, clock.Now())
	if err != nil {
		t.Fatalf("batch2: %v", err)
	}
	if processed != 1 {
		t.Fatalf("batch2 processed=%d, want 1", processed)
	}
	hash := NormalizeClientIPForStats("1.2.3.4").IPHash
	if got := queryInt(t, ctx, store.db, `SELECT request_count FROM client_ip_stats_daily WHERE ip_hash = ?`, hash); got != 3 {
		t.Fatalf("request_count=%d, want 3 after both batches", got)
	}
}

// TestAggregateClientIPStatsBatchIdempotentCursor proves the aggregation is
// not blindly replayable: the additive UPSERT plus the cursor advance means
// a second run with no new rows writes nothing and leaves totals untouched.
func TestAggregateClientIPStatsBatchIdempotentCursor(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-10T12:00:00Z")
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "r1", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
		Success: 1, InputTokens: intPtr(10), CreatedAt: "2026-03-10T10:00:00.000Z",
	})
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, clock.Now()); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Repeat with an advanced clock but no new rows.
	second := clock.Now().Add(time.Minute)
	processed, err := store.AggregateClientIPStatsBatch(ctx, 10, second)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if processed != 0 {
		t.Fatalf("second processed=%d, want 0", processed)
	}
	hash := NormalizeClientIPForStats("1.2.3.4").IPHash
	if got := queryInt(t, ctx, store.db, `SELECT request_count FROM client_ip_stats_daily WHERE ip_hash = ?`, hash); got != 1 {
		t.Fatalf("request_count=%d, want 1 (no double counting)", got)
	}

	// New rows for the same identity accumulate on top of the existing row.
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "r2", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"),
		Success: 1, InputTokens: intPtr(4), CreatedAt: "2026-03-10T11:30:00.000Z",
	})
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, second); err != nil {
		t.Fatalf("third: %v", err)
	}
	if got := queryInt(t, ctx, store.db, `SELECT input_tokens FROM client_ip_stats_daily WHERE ip_hash = ?`, hash); got != 14 {
		t.Fatalf("input_tokens=%d, want 14 (additive upsert)", got)
	}
	if got := queryInt(t, ctx, store.db, `SELECT request_count FROM client_ip_stats_daily WHERE ip_hash = ?`, hash); got != 2 {
		t.Fatalf("request_count=%d, want 2", got)
	}
}

func TestAggregateClientIPStatsBatchNormalizesInvalidIPAndNilAccount(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-10T12:00:00Z")
	// IPv6 sources are dropped from stats entirely (normalizeClientIpForStats
	// returns undefined) and cursor/registration still advance.
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "v6", SystemAccountID: "sys", ClientIP: strPtr("2001:db8::1"),
		Success: 1, CreatedAt: "2026-03-10T10:00:00.000Z",
	})
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "noip", SystemAccountID: "sys", ClientIP: nil,
		Success: 1, CreatedAt: "2026-03-10T10:05:00.000Z",
	})
	processed, err := store.AggregateClientIPStatsBatch(ctx, 10, clock.Now())
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if processed != 2 {
		t.Fatalf("processed=%d, want 2 (rows are consumed even when no identity survives)", processed)
	}
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM client_ip_stats_daily`); got != 0 {
		t.Fatalf("no identity may be registered, got %d rows", got)
	}
	state, err := store.AggregateClientIPStatsBatchState(ctx)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.CursorID != "noip" {
		t.Fatalf("cursor=%+v", state)
	}
}

// TestAggregateClientIPStatsBatchConcurrent exercises the Store write mutex
// and SQLite BEGIN IMMEDIATE serialization under -race.
func TestAggregateClientIPStatsBatchConcurrent(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-10T12:00:00Z")
	for index := 0; index < 8; index++ {
		insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
			ID: string(rune('A' + index)), SystemAccountID: "sys", ClientIP: strPtr("10.0.0.1"),
			Success: 1, CreatedAt: "2026-03-10T10:00:00.000Z",
		})
	}
	done := make(chan struct{})
	for worker := 0; worker < 4; worker++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for batch := 0; batch < 2; batch++ {
				_, _ = store.AggregateClientIPStatsBatch(ctx, 1, clock.Now())
			}
		}()
	}
	for worker := 0; worker < 4; worker++ {
		<-done
	}
	if got := queryInt(t, ctx, store.db, `SELECT request_count FROM client_ip_stats_daily`); got != 8 {
		t.Fatalf("request_count=%d, want 8 after concurrent cursor batches", got)
	}
}
