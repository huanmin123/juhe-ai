package statsverify

import (
	"context"
	"testing"
	"time"
)

// Golden derivations follow client-ip-usage-range-windows.repository.ts:
// windows are [today,today], [today-6,today], [today-30,today]; a refresh
// rewrites each window from client_ip_stats_daily with the positive-metric
// HAVING clause; ready flags live in stats_job_state
// (scope_type='client_ip_range_window', scope_id='start:end').

func TestClientIPRangeWindowsForTimezone(t *testing.T) {
	windows := ClientIPRangeWindowsForTimezone(time.UTC, fixedUTC(t, "2026-03-15T12:00:00Z").Now())
	if len(windows) != 3 {
		t.Fatalf("windows=%d, want 3", len(windows))
	}
	want := [][2]string{{"2026-03-15", "2026-03-15"}, {"2026-03-09", "2026-03-15"}, {"2026-02-13", "2026-03-15"}}
	for index, expect := range want {
		if windows[index].StartDate != expect[0] || windows[index].EndDate != expect[1] {
			t.Fatalf("window[%d]=%+v, want %v", index, windows[index], expect)
		}
	}
	// The 7-day start only collapses into the 31-day start when the fixed
	// window shrinks below 7 days; with 31 days the three keys stay distinct,
	// matching Node (which also emits three windows here).
	short := ClientIPRangeWindowsForTimezone(time.UTC, fixedUTC(t, "2026-02-13T00:00:00Z").Now())
	if len(short) != 3 || short[0].StartDate != "2026-02-13" || short[1].StartDate != "2026-02-07" || short[2].StartDate != "2026-01-14" {
		t.Fatalf("short horizon windows=%+v", short)
	}
}

func seedDailyForWindows(t *testing.T, ctx context.Context, store *Store, ipHash, statDate string, requests, inputTokens int, lastUsedAt string) {
	t.Helper()
	mustExec(t, ctx, store.db, `
		INSERT INTO client_ip_stats_daily (
		  ip_hash, stat_date, request_count, success_count, error_count,
		  input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
		  cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens,
		  input_image_tokens, output_image_tokens, total_cost_usd,
		  duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
		  last_used_at, last_error_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		ipHash, statDate, requests, requests, 0,
		inputTokens, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0,
		0, 0, 0, 0, 0,
		lastUsedAt, nil, "2026-03-01T00:00:00.000Z")
}

func TestRefreshClientIPUsageRangeWindowsDirtyClaimRefreshesAndMarksReady(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-15T12:00:00Z")
	hash := NormalizeClientIPForStats("1.2.3.4").IPHash

	seedDailyForWindows(t, ctx, store, hash, "2026-03-14", 3, 30, "2026-03-14T10:00:00.000Z")
	mustExec(t, ctx, store.db,
		`INSERT INTO client_ip_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at) VALUES (?, 2, ?, ?)`,
		hash, "2026-03-14T10:00:00.000Z", "2026-03-14T10:00:00.000Z")
	mustExec(t, ctx, store.db,
		`INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at) VALUES (?, 5, ?, ?)`,
		hash, "2026-03-14T10:00:00.000Z", "2026-03-14T10:00:00.000Z")

	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: clock.Now()}); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// CAS deleted both dirty rows (generation matched).
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM client_ip_range_window_dirty_ips`); got != 0 {
		t.Fatalf("dirty rows remain: %d", got)
	}
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM client_ip_account_range_window_dirty_ips`); got != 0 {
		t.Fatalf("account dirty rows remain: %d", got)
	}

	// All windows rebuilt for the dirty identity with summed metrics. The
	// seeded daily row sits on 2026-03-14, so the today window (03-15..03-15)
	// legitimately stays empty after the HAVING clause.
	for _, window := range [][2]string{{"2026-03-09", "2026-03-15"}, {"2026-02-13", "2026-03-15"}} {
		if got := queryInt(t, ctx, store.db,
			`SELECT request_count FROM client_ip_usage_range_windows WHERE ip_hash = ? AND start_date = ? AND end_date = ?`,
			hash, window[0], window[1]); got != 3 {
			t.Fatalf("window %v request_count=%d, want 3", window, got)
		}
		if got := queryInt(t, ctx, store.db,
			`SELECT active_days FROM client_ip_usage_range_windows WHERE ip_hash = ? AND start_date = ? AND end_date = ?`,
			hash, window[0], window[1]); got != 1 {
			t.Fatalf("window %v active_days=%d, want 1", window, got)
		}
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT COUNT(*) FROM client_ip_usage_range_windows WHERE start_date = '2026-03-15'`); got != 0 {
		t.Fatalf("today window must be empty (no rows on 03-15), got %d", got)
	}

	// No pending dirty hashes remain: windows are ready.
	readyCount := queryInt(t, ctx, store.db,
		`SELECT COUNT(*) FROM stats_job_state WHERE scope_type = ? AND last_success_at IS NOT NULL`,
		clientIpRangeWindowScopeType)
	if readyCount != 3 {
		t.Fatalf("ready windows=%d, want 3", readyCount)
	}
}

func TestRefreshClientIPUsageRangeWindowsZeroMetricRowsFilteredByHaving(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	// A registry entry whose daily rows are all zero must not appear in any
	// window (HAVING positive metric clause).
	hash := NormalizeClientIPForStats("9.9.9.9").IPHash
	seedDailyForWindows(t, ctx, store, hash, "2026-03-14", 0, 0, "")
	mustExec(t, ctx, store.db,
		`INSERT INTO client_ip_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at) VALUES (?, 1, ?, ?)`,
		hash, "2026-03-14T10:00:00.000Z", "2026-03-14T10:00:00.000Z")
	mustExec(t, ctx, store.db,
		`INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at) VALUES (?, 1, ?, ?)`,
		hash, "2026-03-14T10:00:00.000Z", "2026-03-14T10:00:00.000Z")

	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: fixedUTC(t, "2026-03-15T12:00:00Z").Now()}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM client_ip_usage_range_windows`); got != 0 {
		t.Fatalf("zero-metric identity windows=%d, want 0", got)
	}
}

func TestRefreshClientIPUsageRangeWindowsGenerationCASKeepsConcurrentDirty(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	hash := NormalizeClientIPForStats("1.2.3.4").IPHash
	seedDailyForWindows(t, ctx, store, hash, "2026-03-14", 1, 10, "2026-03-14T10:00:00.000Z")
	mustExec(t, ctx, store.db,
		`INSERT INTO client_ip_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at) VALUES (?, 3, ?, ?)`,
		hash, "2026-03-14T10:00:00.000Z", "2026-03-14T10:00:00.000Z")
	mustExec(t, ctx, store.db,
		`INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at) VALUES (?, 1, ?, ?)`,
		hash, "2026-03-14T10:00:00.000Z", "2026-03-14T10:00:00.000Z")

	// Claim observes generation 3/1; a concurrent write bumps them before the
	// CAS delete inside the same cycle.
	claimTx, err := store.beginWriteTx(ctx)
	if err != nil {
		t.Fatalf("claim tx: %v", err)
	}
	claim, err := store.takeClientIPRangeWindowDirty(ctx, claimTx, false, 100)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := claimTx.Commit(); err != nil {
		t.Fatalf("claim commit: %v", err)
	}
	if len(claim.ipHashes) != 1 || claim.clientIPRows[0].generation != 3 || claim.accountRows[0].generation != 1 {
		t.Fatalf("claim=%+v", claim)
	}
	mustExec(t, ctx, store.db,
		`UPDATE client_ip_range_window_dirty_ips SET generation = 4 WHERE ip_hash = ?`, hash)

	tx, err := store.beginWriteTx(ctx)
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	defer tx.Rollback()
	if err := store.clearClientIPRangeWindowDirty(ctx, tx, claim); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// The bumped client-ip dirty row survives (CAS), the account row is gone.
	if got := queryInt(t, ctx, store.db, `SELECT generation FROM client_ip_range_window_dirty_ips WHERE ip_hash = ?`, hash); got != 4 {
		t.Fatalf("generation=%d, want 4 (CAS kept the bumped row)", got)
	}
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM client_ip_account_range_window_dirty_ips`); got != 0 {
		t.Fatalf("account row should be cleared")
	}
}

func TestRefreshClientIPUsageRangeWindowsNoDirtyRebuildsStaleOnly(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-15T12:00:00Z")
	hash := NormalizeClientIPForStats("1.2.3.4").IPHash
	seedDailyForWindows(t, ctx, store, hash, "2026-03-14", 2, 20, "2026-03-14T10:00:00.000Z")

	// Nothing dirty, nothing stale: refresh is a no-op.
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: clock.Now()}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM client_ip_usage_range_windows`); got != 0 {
		t.Fatalf("clean no-op must not rebuild windows, got %d", got)
	}

	// Simulate the stale marking the aggregation writer performs
	// (markCurrentClientIpUsageRangeWindowsStale): one stale window state.
	mustExec(t, ctx, store.db, `
		INSERT INTO stats_job_state (scope_type, scope_id, job_name, last_success_at, updated_at)
		VALUES (?, ?, ?, NULL, ?)
	`, clientIpRangeWindowScopeType, clientIpRangeWindowScopeID("2026-03-15", "2026-03-15"), clientIpRangeWindowJobName, "2026-03-15T11:00:00.000Z")

	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: clock.Now()}); err != nil {
		t.Fatalf("stale refresh: %v", err)
	}
	// All windows rebuilt because one was stale; the today window stays empty
	// (daily rows only exist on 03-14), so two window rows appear.
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM client_ip_usage_range_windows`); got != 2 {
		t.Fatalf("stale rebuild windows=%d, want 2", got)
	}
	if got := queryInt(t, ctx, store.db,
		`SELECT COUNT(*) FROM stats_job_state WHERE scope_type = ? AND last_success_at IS NOT NULL`, clientIpRangeWindowScopeType); got != 3 {
		t.Fatalf("ready windows=%d, want 3", got)
	}
}

func TestRefreshClientIPUsageRangeWindowsFullIgnoresDirtyLimit(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-15T12:00:00Z")
	hashA := NormalizeClientIPForStats("1.2.3.4").IPHash
	hashB := NormalizeClientIPForStats("192.168.1.1").IPHash
	seedDailyForWindows(t, ctx, store, hashA, "2026-03-14", 1, 1, "2026-03-14T10:00:00.000Z")
	seedDailyForWindows(t, ctx, store, hashB, "2026-03-14", 2, 2, "2026-03-14T10:00:00.000Z")
	for _, hash := range []string{hashA, hashB} {
		mustExec(t, ctx, store.db,
			`INSERT INTO client_ip_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at) VALUES (?, 1, ?, ?)`,
			hash, "2026-03-14T10:00:00.000Z", "2026-03-14T10:00:00.000Z")
		mustExec(t, ctx, store.db,
			`INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at) VALUES (?, 1, ?, ?)`,
			hash, "2026-03-14T10:00:00.000Z", "2026-03-14T10:00:00.000Z")
	}
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Full: true, DirtyLimit: 1, Now: clock.Now()}); err != nil {
		t.Fatalf("full refresh: %v", err)
	}
	for _, hash := range []string{hashA, hashB} {
		if got := queryInt(t, ctx, store.db,
			`SELECT COUNT(*) FROM client_ip_usage_range_windows WHERE ip_hash = ?`, hash); got != 2 {
			t.Fatalf("hash %s windows=%d, want 2 (today window empty)", hash, got)
		}
	}
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM client_ip_range_window_dirty_ips`); got != 0 {
		t.Fatalf("full refresh must clear all dirty rows, got %d", got)
	}
}

// TestRefreshClientIPUsageRangeWindowsConcurrent drives two concurrent
// refresh cycles under -race; the Store write mutex serializes them and the
// second cycle finds no work.
func TestRefreshClientIPUsageRangeWindowsConcurrent(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	clock := fixedUTC(t, "2026-03-15T12:00:00Z")
	hash := NormalizeClientIPForStats("1.2.3.4").IPHash
	seedDailyForWindows(t, ctx, store, hash, "2026-03-14", 1, 1, "2026-03-14T10:00:00.000Z")
	mustExec(t, ctx, store.db,
		`INSERT INTO client_ip_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at) VALUES (?, 1, ?, ?)`,
		hash, "2026-03-14T10:00:00.000Z", "2026-03-14T10:00:00.000Z")
	mustExec(t, ctx, store.db,
		`INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at) VALUES (?, 1, ?, ?)`,
		hash, "2026-03-14T10:00:00.000Z", "2026-03-14T10:00:00.000Z")
	done := make(chan error, 2)
	for worker := 0; worker < 2; worker++ {
		go func() {
			done <- store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: clock.Now()})
		}()
	}
	for worker := 0; worker < 2; worker++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent refresh: %v", err)
		}
	}
}
