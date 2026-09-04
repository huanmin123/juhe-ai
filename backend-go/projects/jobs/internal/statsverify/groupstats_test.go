package statsverify

import (
	"context"
	"testing"
)

// Golden derivations follow group-account-stats-cache.repository.ts:
//   - dirty consumption: '__all__' full rebuild first (cursor stored in the
//     reason prefix 'all_cursor:'), then per-group rows oldest-updated first;
//   - counting: enabled=1 and not deleted and authorized (active
//     authorization not expired at updatedAt, or account system account ==
//     group system account); active+schedulable+past-cooldown => available;
//     rate_limited counts as error AND rate_limited; concurrency_limit sums;
//   - cache rewrite: DELETE per group chunk then INSERT (empty groups keep a
//     zero row);
//   - dirty rows are deleted with an updated_at CAS.

func seedGroupFixture(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	mustExec(t, ctx, store.business, `
		INSERT INTO groups (id, system_account_id) VALUES ('g1', 'sys-a'), ('g2', 'sys-a'), ('g3', 'sys-b')
	`)
	mustExec(t, ctx, store.business, `
		INSERT INTO accounts (id, system_account_id, status, schedulable, cooldown_until, concurrency_limit, deleted_at) VALUES
		  ('a1', 'sys-a', 'active', 1, NULL, 5, NULL),
		  ('a2', 'sys-a', 'active', 1, '2026-03-01T00:00:00.000Z', 3, NULL),
		  ('a3', 'sys-a', 'disabled', 1, NULL, 2, NULL),
		  ('a4', 'sys-a', 'rate_limited', 0, NULL, 4, NULL),
		  ('a5', 'sys-b', 'active', 1, NULL, 1, NULL),
		  ('a6', 'sys-a', 'active', 1, NULL, 9, '2026-03-01T00:00:00.000Z')
	`)
	// g1: a1 (available), a2 (cooldown passed at now=03-02 => available), a3
	// (disabled), a4 (rate_limited => error+rate_limited), a6 (deleted, skip).
	mustExec(t, ctx, store.business, `
		INSERT INTO group_accounts (group_id, account_id, account_authorization_id, enabled) VALUES
		  ('g1', 'a1', NULL, 1),
		  ('g1', 'a2', NULL, 1),
		  ('g1', 'a3', NULL, 1),
		  ('g1', 'a4', NULL, 1),
		  ('g1', 'a6', NULL, 1),
		  ('g2', 'a1', NULL, 0)
	`)
}

func TestRefreshDirtyGroupAccountStatsInitialBuildAndCounting(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-02T12:00:00Z").Now()
	seedGroupFixture(t, ctx, store)

	refreshed, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	// No dirty rows and an empty cache triggers the initial_cache_build
	// branch which consumes the '__all__' row in one batch => 1.
	if refreshed != 1 {
		t.Fatalf("refreshed=%d, want 1", refreshed)
	}

	type row struct {
		total, available, active, disabled, errCount, rateLimited, concurrency int
		systemAccountID                                                        string
	}
	getRow := func(groupID string) row {
		var got row
		err := store.db.QueryRowContext(ctx, `
			SELECT total, available, active, disabled, error, rate_limited, concurrency_limit, system_account_id
			FROM group_account_stats WHERE group_id = ?
		`, groupID).Scan(&got.total, &got.available, &got.active, &got.disabled, &got.errCount, &got.rateLimited, &got.concurrency, &got.systemAccountID)
		if err != nil {
			t.Fatalf("read %s: %v", groupID, err)
		}
		return got
	}

	g1 := getRow("g1")
	// total 4 (a6 deleted), available 2 (a1 + a2 whose cooldown passed),
	// active 2, disabled 1, error 1 (rate_limited also errors), rateLimited 1,
	// concurrency 5+3+2+4=14 (a6 deleted).
	want := row{total: 4, available: 2, active: 2, disabled: 1, errCount: 1, rateLimited: 1, concurrency: 14, systemAccountID: "sys-a"}
	if g1 != want {
		t.Fatalf("g1=%+v, want %+v", g1, want)
	}

	g2 := getRow("g2")
	// Only membership is disabled (enabled=0) => zero row for an existing group.
	g2Want := row{total: 0, available: 0, active: 0, disabled: 0, errCount: 0, rateLimited: 0, concurrency: 0, systemAccountID: "sys-a"}
	if g2 != g2Want {
		t.Fatalf("g2=%+v, want %+v", g2, g2Want)
	}

	g3 := getRow("g3")
	g3Want := row{systemAccountID: "sys-b"}
	if g3 != g3Want {
		t.Fatalf("g3=%+v, want %+v (empty group keeps a zero row)", g3, g3Want)
	}

	// Dirty table drained after full rebuild.
	if got := queryInt(t, ctx, store.business, `SELECT COUNT(*) FROM group_account_stats_dirty`); got != 0 {
		t.Fatalf("dirty rows=%d, want 0", got)
	}
}

func TestRefreshDirtyGroupAccountStatsAuthorizationRules(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-02T12:00:00Z").Now()
	mustExec(t, ctx, store.business, `INSERT INTO groups (id, system_account_id) VALUES ('g', 'sys-a')`)
	mustExec(t, ctx, store.business, `INSERT INTO accounts (id, system_account_id, status, schedulable, concurrency_limit) VALUES
	  ('ax', 'sys-other', 'active', 1, 1)`)
	mustExec(t, ctx, store.business, `INSERT INTO resource_authorizations (id, status, expires_at) VALUES
	  ('ra-ok', 'active', '2026-03-05T00:00:00.000Z'),
	  ('ra-expired', 'active', '2026-03-01T00:00:00.000Z'),
	  ('ra-disabled', 'disabled', NULL)`)
	mustExec(t, ctx, store.business, `INSERT INTO group_accounts (group_id, account_id, account_authorization_id, enabled) VALUES
	  ('g', 'ax', 'ra-ok', 1)`)
	// Second account row for the expired-authorization case.
	mustExec(t, ctx, store.business, `INSERT INTO accounts (id, system_account_id, status, schedulable, concurrency_limit) VALUES
	  ('ay', 'sys-other', 'active', 1, 2)`)
	mustExec(t, ctx, store.business, `INSERT INTO group_accounts (group_id, account_id, account_authorization_id, enabled) VALUES
	  ('g', 'ay', 'ra-expired', 1)`)

	if _, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	// authorized cross-system account via active authorization counts once.
	if got := queryInt(t, ctx, store.db, `SELECT total FROM group_account_stats WHERE group_id = 'g'`); got != 1 {
		t.Fatalf("total=%d, want 1 (expired authorization excluded)", got)
	}
	if got := queryInt(t, ctx, store.db, `SELECT concurrency_limit FROM group_account_stats WHERE group_id = 'g'`); got != 1 {
		t.Fatalf("concurrency=%d, want 1", got)
	}

	// Flip the authorization to active-unexpired for the second account and
	// re-dirty; the refresh converges to the new totals.
	mustExec(t, ctx, store.business, `UPDATE resource_authorizations SET status = 'active', expires_at = '2026-03-09T00:00:00.000Z' WHERE id = 'ra-expired'`)
	mustExec(t, ctx, store.business, `INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('g', 'write', '2026-03-02T12:01:00.000Z')`)
	if _, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if got := queryInt(t, ctx, store.db, `SELECT total FROM group_account_stats WHERE group_id = 'g'`); got != 2 {
		t.Fatalf("total=%d, want 2", got)
	}
	if got := queryInt(t, ctx, store.db, `SELECT concurrency_limit FROM group_account_stats WHERE group_id = 'g'`); got != 3 {
		t.Fatalf("concurrency=%d, want 3", got)
	}
}

func TestRefreshDirtyGroupAccountStatsPerGroupBatchAndDirtyCAS(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-02T12:00:00Z").Now()
	seedGroupFixture(t, ctx, store)
	// Build the initial cache.
	if _, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err != nil {
		t.Fatalf("initial: %v", err)
	}

	// Dirty two groups with distinct updated_at ordering.
	mustExec(t, ctx, store.business, `INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES
	  ('g2', 'write', '2026-03-02T12:05:00.000Z'),
	  ('g1', 'write', '2026-03-02T12:04:00.000Z')`)
	mustExec(t, ctx, store.business, `UPDATE accounts SET status = 'disabled' WHERE id = 'a1'`)

	refreshed, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed != 2 {
		t.Fatalf("refreshed=%d, want 2", refreshed)
	}
	if got := queryInt(t, ctx, store.db, `SELECT active FROM group_account_stats WHERE group_id = 'g1'`); got != 1 {
		t.Fatalf("g1 active=%d, want 1 (a1 flipped to disabled)", got)
	}
	if got := queryInt(t, ctx, store.business, `SELECT COUNT(*) FROM group_account_stats_dirty`); got != 0 {
		t.Fatalf("dirty rows=%d, want 0", got)
	}

	// A concurrent write that re-dirties g1 with a NEWER updated_at survives
	// the CAS delete of the consumed row. The bump runs inside the same tx:
	// SQLite opens a single business connection, so an out-of-tx UPDATE would
	// wait forever for the uncommitted tx to release it.
	mustExec(t, ctx, store.business, `INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('g1', 'write', '2026-03-02T12:04:00.000Z')`)
	tx, err := store.business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	rows, err := store.loadGroupAccountStatsDirtyRows(ctx, tx, 10)
	if err != nil {
		t.Fatalf("load dirty: %v", err)
	}
	if len(rows) != 1 || rows[0].GroupID != "g1" {
		t.Fatalf("rows=%+v", rows)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE group_account_stats_dirty SET updated_at = '2026-03-02T12:06:00.000Z' WHERE group_id = 'g1'`); err != nil {
		t.Fatalf("bump dirty: %v", err)
	}
	if err := store.deleteGroupAccountStatsDirtyRows(ctx, tx, rows); err != nil {
		t.Fatalf("delete dirty: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var updated string
	if err := store.business.QueryRowContext(ctx, `SELECT updated_at FROM group_account_stats_dirty WHERE group_id = 'g1'`).Scan(&updated); err != nil {
		t.Fatalf("read dirty row: %v", err)
	}
	if updated != "2026-03-02T12:06:00.000Z" {
		t.Fatalf("CAS must keep the re-dirtied row, got %q", updated)
	}
}

func TestRefreshDirtyGroupAccountStatsAllCursorBatches(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-02T12:00:00Z").Now()
	mustExec(t, ctx, store.business, `INSERT INTO groups (id, system_account_id) VALUES ('ga', 'sys'), ('gb', 'sys'), ('gc', 'sys')`)

	// Cursor mid-stream: only groups after 'ga' are processed in this batch.
	mustExec(t, ctx, store.business, `INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('__all__', 'all_cursor:ga', '2026-03-02T12:00:00.000Z')`)
	refreshed, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Limit: 1, Now: now})
	if err != nil {
		t.Fatalf("batch1: %v", err)
	}
	if refreshed != 1 {
		t.Fatalf("batch1 refreshed=%d, want 1", refreshed)
	}
	var reason string
	if err := store.business.QueryRowContext(ctx,
		`SELECT reason FROM group_account_stats_dirty WHERE group_id = '__all__'`).Scan(&reason); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if reason != "all_cursor:gb" {
		t.Fatalf("cursor=%q, want all_cursor:gb", reason)
	}

	// Full page (< limit) finishes the rebuild and deletes the dirty row.
	// 'ga' is intentionally absent: the 'all_cursor:ga' cursor declares it
	// already processed, so only gb and gc are rebuilt.
	refreshed, err = store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Limit: 10, Now: now})
	if err != nil {
		t.Fatalf("batch2: %v", err)
	}
	if refreshed != 1 {
		t.Fatalf("batch2 refreshed=%d, want 1", refreshed)
	}
	if got := queryInt(t, ctx, store.business, `SELECT COUNT(*) FROM group_account_stats_dirty`); got != 0 {
		t.Fatalf("dirty rows=%d, want 0", got)
	}
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM group_account_stats`); got != 2 {
		t.Fatalf("cache rows=%d, want 2 (cursor resumes after ga)", got)
	}
	if got := queryInt(t, ctx, store.db, `SELECT COUNT(*) FROM group_account_stats WHERE group_id = 'ga'`); got != 0 {
		t.Fatalf("ga must not be rebuilt behind the cursor")
	}

	// An exhausted cursor (all groups processed) deletes the dirty row without
	// rewriting the cache.
	mustExec(t, ctx, store.business, `INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('__all__', 'all_cursor:gc', '2026-03-02T12:10:00.000Z')`)
	refreshed, err = store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Limit: 10, Now: now})
	if err != nil {
		t.Fatalf("batch3: %v", err)
	}
	if refreshed != 1 {
		t.Fatalf("batch3 refreshed=%d, want 1", refreshed)
	}
	if got := queryInt(t, ctx, store.business, `SELECT COUNT(*) FROM group_account_stats_dirty`); got != 0 {
		t.Fatalf("dirty rows=%d, want 0 after exhausted cursor", got)
	}
}

func TestRefreshDirtyGroupAccountStatsNoDirtyWithCacheIsNoOp(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-02T12:00:00Z").Now()
	seedGroupFixture(t, ctx, store)
	if _, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err != nil {
		t.Fatalf("initial: %v", err)
	}
	refreshed, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if refreshed != 0 {
		t.Fatalf("refreshed=%d, want 0 (clean no-op)", refreshed)
	}
}

func TestRefreshDirtyGroupAccountStatsIdempotentRepeatedRefresh(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-02T12:00:00Z").Now()
	seedGroupFixture(t, ctx, store)
	if _, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := queryInt(t, ctx, store.db, `SELECT total + available + active + disabled + error + rate_limited + concurrency_limit FROM group_account_stats WHERE group_id = 'g1'`)
	// Refresh the same group again through an explicit dirty row.
	mustExec(t, ctx, store.business, `INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('g1', 'write', '2026-03-02T12:30:00.000Z')`)
	if _, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now}); err != nil {
		t.Fatalf("second: %v", err)
	}
	second := queryInt(t, ctx, store.db, `SELECT total + available + active + disabled + error + rate_limited + concurrency_limit FROM group_account_stats WHERE group_id = 'g1'`)
	if first != second {
		t.Fatalf("repeat refresh drifted: %d -> %d (cache must be a pure projection)", first, second)
	}
}

func TestMarkGroupAccountStatsStartupDirty(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-02T12:00:00Z").Now()
	if err := store.MarkGroupAccountStatsStartupDirty(ctx, now); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if got := queryInt(t, ctx, store.business, `SELECT COUNT(*) FROM group_account_stats_dirty WHERE group_id = '__all__'`); got != 1 {
		t.Fatalf("dirty rows=%d, want 1", got)
	}
	// The initial build path consumes the startup marker.
	seedGroupFixture(t, ctx, store)
	refreshed, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
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
