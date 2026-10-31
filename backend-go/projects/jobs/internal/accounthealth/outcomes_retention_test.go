package accounthealth

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// seedRetentionOutcome 插入一条最小 outcome 行（payload 置空对象）。
func seedRetentionOutcome(t *testing.T, db *sql.DB, outcomeID string, observedAt time.Time) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO juhe_jobs.account_health_outcomes
		(outcome_id, request_id, account_id, outcome, observed_at, input_version, config_revision, dispatch_revision, payload)
		VALUES ($1, $2, 'acct-retention', 'complete_success', $3, 1, 1, 1, '{}'::jsonb)`,
		outcomeID, "req-"+outcomeID, observedAt.UTC())
	if err != nil {
		t.Fatalf("seed outcome %s: %v", outcomeID, err)
	}
}

func countRetentionOutcomes(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_jobs.account_health_outcomes`).Scan(&count); err != nil {
		t.Fatalf("count outcomes: %v", err)
	}
	return count
}

func remainingRetentionIDs(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT outcome_id FROM juhe_jobs.account_health_outcomes`)
	if err != nil {
		t.Fatalf("list outcomes: %v", err)
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan outcome id: %v", err)
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list outcomes: %v", err)
	}
	return ids
}

// TestCleanupOutcomesBeforeRollingWindow 断言 W40 保留清理契约：observed_at
// 严格早于 cutoff 的行被删除（等于 cutoff 的行保留），最旧优先（limit 截断时
// 剩下的是最新行），单批受 limit 约束、分批收敛。
func TestCleanupOutcomesBeforeRollingWindow(t *testing.T) {
	db, dsn := w9hOpenKitDB(t)
	ctx := context.Background()

	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: dsn})
	if err != nil {
		t.Fatalf("open jobs store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure store schema: %v", err)
	}

	cutoff := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seedRetentionOutcome(t, db, "old-1", cutoff.Add(-48*time.Hour))
	seedRetentionOutcome(t, db, "old-2", cutoff.Add(-time.Hour))
	seedRetentionOutcome(t, db, "boundary", cutoff)
	seedRetentionOutcome(t, db, "new-1", cutoff.Add(time.Hour))

	// 批量 2：只删两条最旧（old-1、old-2），boundary（严格 < 才删）与 new-1 保留。
	deleted, err := CleanupOutcomesBefore(ctx, db, cutoff, 2)
	if err != nil {
		t.Fatalf("cleanup batch 1: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("batch 1 deleted = %d, want 2", deleted)
	}
	remaining := remainingRetentionIDs(t, db)
	if !remaining["boundary"] || !remaining["new-1"] {
		t.Fatalf("boundary/newest rows must survive strict cutoff: %v", remaining)
	}
	if remaining["old-1"] || remaining["old-2"] {
		t.Fatalf("oldest rows must be deleted first: %v", remaining)
	}

	// 第二批：剩余窗口外行为零（boundary 不满足严格 <），不再误删。
	deleted, err = CleanupOutcomesBefore(ctx, db, cutoff, 2)
	if err != nil {
		t.Fatalf("cleanup batch 2: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("batch 2 deleted = %d, want 0 (boundary is not strictly before cutoff)", deleted)
	}
	if countRetentionOutcomes(t, db) != 2 {
		t.Fatalf("unexpected row count after batch 2")
	}

	// 窗口推进后剩余行按同一契约收敛。
	deleted, err = CleanupOutcomesBefore(ctx, db, cutoff.Add(2*time.Hour), 10)
	if err != nil {
		t.Fatalf("cleanup batch 3: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("batch 3 deleted = %d, want 2", deleted)
	}
	if countRetentionOutcomes(t, db) != 0 {
		t.Fatalf("table must be empty after advancing window")
	}
}
