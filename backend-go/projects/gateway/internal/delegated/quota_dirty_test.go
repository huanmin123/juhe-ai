package delegated

// W2-A 待办2 回归：delegated api-key 状态补丁重建配额小时窗绑定 → 脏范围打脏
// （request-quota-hourly-windows.repository.ts:79-106/:384-405，PG-only，
// 提交后尾随效果，失败不回滚主写）。PG 分支用 SQLite ATTACH
// juhe_business/juhe_stats 承载（SetMaxOpenConns(1)）。

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const delegatedQuotaLimitsJSON = `{"hourly":{"enabled":true,"hours":8}}`

func newDelegatedQuotaDirtyFixture(t *testing.T, pg bool) (*Deps, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "delegated-quota-dirty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	statements := []string{}
	if pg {
		statements = append(statements,
			`ATTACH ':memory:' AS juhe_business`,
			`ATTACH ':memory:' AS juhe_stats`)
	}
	statements = append(statements,
		`CREATE TABLE `+quotaDirtyQualified(pg, "juhe_business", "request_quota_hourly_window_scope_bindings")+` (
			system_account_id TEXT NOT NULL,
			scope_type TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			source_type TEXT NOT NULL,
			source_id TEXT NOT NULL,
			window_hours INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id)
		)`,
		`CREATE TABLE `+quotaDirtyQualified(pg, "juhe_stats", "usage_quota_hourly_window_dirty_scopes")+` (
			system_account_id TEXT NOT NULL,
			scope_type TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			generation INTEGER NOT NULL,
			first_dirty_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id)
		)`)
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture exec %q: %v", statement, err)
		}
	}
	return &Deps{DB: db, PGDialect: pg, Now: func() time.Time { return time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC) }}, db
}

func quotaDirtyQualified(pg bool, schema, name string) string {
	if pg {
		return schema + "." + name
	}
	return name
}

func delegatedDirtyGeneration(t *testing.T, deps *Deps, ownerID, keyID string) int {
	t.Helper()
	var generation int
	if err := deps.DB.QueryRow(`SELECT generation FROM `+deps.statsTable("usage_quota_hourly_window_dirty_scopes")+`
		WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?`, ownerID, keyID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	return generation
}

// TestDelegatedStatusPatchBindingRebuildMarksDirtyScope mirrors :89-105:
// status change rebuilds the binding → dirty scope row appears; a re-mark
// bumps generation.
func TestDelegatedStatusPatchBindingRebuildMarksDirtyScope(t *testing.T) {
	deps, db := newDelegatedQuotaDirtyFixture(t, true)
	ctx := context.Background()
	current := &apiKeyMutationRow{
		ID:              "key-1",
		SystemAccountID: "owner-1",
		QuotaLimitsJSON: sql.NullString{String: delegatedQuotaLimitsJSON, Valid: true},
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	upserted, err := deps.syncApiKeyQuotaScopeBinding(ctx, tx, current, true, "2026-09-08T08:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	if !upserted {
		t.Fatal("status change must rebuild the binding row")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	deps.markQuotaHourlyWindowDirtyScopeAfterCommit(ctx, current.SystemAccountID, current.ID, "2026-09-08T08:00:00.000Z")
	if generation := delegatedDirtyGeneration(t, deps, "owner-1", "key-1"); generation != 1 {
		t.Fatalf("dirty generation = %d, want 1（绑定重建→脏行出现）", generation)
	}

	// 再启用一次：重新打脏 generation 自增。
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	upserted, err = deps.syncApiKeyQuotaScopeBinding(ctx, tx, current, true, "2026-09-08T08:01:00.000Z")
	if err != nil || !upserted {
		t.Fatalf("second rebuild: upserted=%v err=%v", upserted, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	deps.markQuotaHourlyWindowDirtyScopeAfterCommit(ctx, current.SystemAccountID, current.ID, "2026-09-08T08:01:00.000Z")
	if generation := delegatedDirtyGeneration(t, deps, "owner-1", "key-1"); generation != 2 {
		t.Fatalf("dirty generation after re-mark = %d, want 2", generation)
	}
}

// TestDelegatedDeactivateSkipsDirtyMark: 停用分支按归档 :94-105 早退，
// 绑定删除且 upserted=false（调用方门控后不会打脏）。
func TestDelegatedDeactivateSkipsDirtyMark(t *testing.T) {
	deps, db := newDelegatedQuotaDirtyFixture(t, true)
	ctx := context.Background()
	current := &apiKeyMutationRow{
		ID:              "key-1",
		SystemAccountID: "owner-1",
		QuotaLimitsJSON: sql.NullString{String: delegatedQuotaLimitsJSON, Valid: true},
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	upserted, err := deps.syncApiKeyQuotaScopeBinding(ctx, tx, current, false, "t")
	if err != nil {
		t.Fatal(err)
	}
	if upserted {
		t.Fatal("deactivation must not report a binding upsert")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var bindings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + deps.table("request_quota_hourly_window_scope_bindings")).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 {
		t.Fatalf("deactivation must clear bindings, got %d", bindings)
	}
}

// TestDelegatedDirtyMarkFailureWarnsNotFails: the trailing mark fails (stats
// table dropped) without panicking or propagating — 主写已提交契约。
func TestDelegatedDirtyMarkFailureWarnsNotFails(t *testing.T) {
	deps, db := newDelegatedQuotaDirtyFixture(t, true)
	if _, err := db.Exec(`DROP TABLE juhe_stats.usage_quota_hourly_window_dirty_scopes`); err != nil {
		t.Fatal(err)
	}
	deps.markQuotaHourlyWindowDirtyScopeAfterCommit(context.Background(), "owner-1", "key-1", "t")
	err := deps.markQuotaHourlyWindowDirtyScope(context.Background(), db, "owner-1", "key-1", "t")
	if err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("direct mark against the dropped table must surface the error, got %v", err)
	}
}

// TestDelegatedSQLiteModeSkipsDirtyMark: SQLite 分支不打脏（全量重建调度无
// 脏范围消费者）。
func TestDelegatedSQLiteModeSkipsDirtyMark(t *testing.T) {
	deps, _ := newDelegatedQuotaDirtyFixture(t, false)
	// SQLite 模式 AfterCommit 直接早退，脏表保持空。
	deps.markQuotaHourlyWindowDirtyScopeAfterCommit(context.Background(), "owner-1", "key-1", "t")
	var dirty int
	if err := deps.DB.QueryRow(`SELECT COUNT(*) FROM usage_quota_hourly_window_dirty_scopes`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Fatalf("sqlite mode must not write dirty scopes, got %d", dirty)
	}
}
