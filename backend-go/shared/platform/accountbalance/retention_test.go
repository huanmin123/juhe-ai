package accountbalance

// retention_test.go 覆盖 outcomes 表保留清理契约（参照 accounthealth
// outcomes_retention_test.go 同型）：
//   - PG 臂走 pgtestkit 每测试独立库（JUHE_AI_TEST_EXTERNAL=1 授权门，未授权
//     时 skip）：prepare 只补建 juhe_jobs schema 命名空间，四表由生产
//     OpenStore + EnsureSchema 按生产 DDL 自建，测试零 DDL 复刻（与
//     accountbalance_pgkit_test.go 同约定）。
//   - 守卫臂（nil db / limit<=0）在触达任何连接前返回，不依赖真库，恒执行。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// openKitRetentionDB 打开 pgtestkit 隔离 PG 测试库（prepare 补建 juhe_jobs
// schema 命名空间），经生产 OpenStore(StorePostgres) 装配并 EnsureSchema 按
// 生产 DDL 建四表；返回裸 *sql.DB 供 CleanupOutcomesBefore 直连臂使用，测试
// 结束先关 store、后删库（Cleanup LIFO）。
func openKitRetentionDB(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_jobs`)
		return err
	})
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: testDB.DSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("accountbalance PG EnsureSchema 失败: %v", err)
	}
	return testDB.DB
}

// seedRetentionOutcome 插入一条最小余额 outcome 行（committed 走默认 FALSE，
// payload 置空对象）。
func seedRetentionOutcome(t *testing.T, db *sql.DB, outcomeID string, observedAt time.Time) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO juhe_jobs.account_balance_outcomes
		(outcome_id, request_id, account_id, input_version, config_revision, trigger, observed_at, payload)
		VALUES ($1, $2, 'acct-retention', 1, 1, $3, $4, '{}'::jsonb)`,
		outcomeID, "req-"+outcomeID, string(TriggerPeriodic), observedAt.UTC())
	if err != nil {
		t.Fatalf("seed outcome %s: %v", outcomeID, err)
	}
}

func countRetentionOutcomes(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_jobs.account_balance_outcomes`).Scan(&count); err != nil {
		t.Fatalf("count outcomes: %v", err)
	}
	return count
}

func remainingRetentionIDs(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT outcome_id FROM juhe_jobs.account_balance_outcomes`)
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

// TestCleanupOutcomesBeforeGuards 断言参数守卫：nil db 与非正 limit 必须报
// 错且错误发生在触达任何连接之前（故不依赖 PG 授权门）。
func TestCleanupOutcomesBeforeGuards(t *testing.T) {
	if _, err := CleanupOutcomesBefore(context.Background(), nil, time.Now(), 1); err == nil {
		t.Fatal("nil db 必须报错")
	}
	// 零值 *sql.DB 非 nil：limit 守卫先于任何连接访问返回。
	db := &sql.DB{}
	if _, err := CleanupOutcomesBefore(context.Background(), db, time.Now(), 0); err == nil {
		t.Fatal("limit=0 必须报错")
	}
	if _, err := CleanupOutcomesBefore(context.Background(), db, time.Now(), -1); err == nil {
		t.Fatal("limit=-1 必须报错")
	}
}

// TestCleanupOutcomesBeforeRollingWindow 断言保留清理契约：observed_at 严格
// 早于 cutoff 的行被删除（等于 cutoff 的行保留），最旧优先（limit 截断时删
// 掉的是最旧行、保留较新行），单批受 limit 约束、分批收敛。
func TestCleanupOutcomesBeforeRollingWindow(t *testing.T) {
	db := openKitRetentionDB(t)
	ctx := context.Background()

	cutoff := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seedRetentionOutcome(t, db, "old-a", cutoff.Add(-48*time.Hour))
	seedRetentionOutcome(t, db, "old-b", cutoff.Add(-24*time.Hour))
	seedRetentionOutcome(t, db, "old-c", cutoff.Add(-time.Hour))
	seedRetentionOutcome(t, db, "boundary", cutoff)
	seedRetentionOutcome(t, db, "new-1", cutoff.Add(time.Hour))

	// 批量 2：只删两条最旧（old-a、old-b），old-c（虽在窗口外但被 limit 截断）
	// 与 boundary（严格 < 才删）、new-1 保留。
	deleted, err := CleanupOutcomesBefore(ctx, db, cutoff, 2)
	if err != nil {
		t.Fatalf("cleanup batch 1: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("batch 1 deleted = %d, want 2", deleted)
	}
	remaining := remainingRetentionIDs(t, db)
	if !remaining["old-c"] || !remaining["boundary"] || !remaining["new-1"] {
		t.Fatalf("limit-truncated/boundary/newest rows must survive batch 1: %v", remaining)
	}
	if remaining["old-a"] || remaining["old-b"] {
		t.Fatalf("oldest rows must be deleted first: %v", remaining)
	}

	// 第二批：删掉剩余窗口外最旧的 old-c；boundary 不满足严格 <，即使 limit
	// 未用满也不得误删。
	deleted, err = CleanupOutcomesBefore(ctx, db, cutoff, 2)
	if err != nil {
		t.Fatalf("cleanup batch 2: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("batch 2 deleted = %d, want 1 (boundary is not strictly before cutoff)", deleted)
	}
	remaining = remainingRetentionIDs(t, db)
	if !remaining["boundary"] || !remaining["new-1"] {
		t.Fatalf("boundary/newest rows must survive strict cutoff: %v", remaining)
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

// TestCleanupOutcomesBeforeEmptyTable 断言空表清理返回 0 行且不报错。
func TestCleanupOutcomesBeforeEmptyTable(t *testing.T) {
	db := openKitRetentionDB(t)
	deleted, err := CleanupOutcomesBefore(context.Background(), db, time.Now().UTC(), 100)
	if err != nil {
		t.Fatalf("cleanup empty table: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("empty table deleted = %d, want 0", deleted)
	}
	if countRetentionOutcomes(t, db) != 0 {
		t.Fatalf("empty table must stay empty")
	}
}
