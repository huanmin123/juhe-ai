package apikeys

// W2-A 待办1 回归：api_key 配额小时窗绑定重建 → 脏范围打脏
// （request-quota-hourly-windows.repository.ts:79-106/:384-405）。
//
// PG 分支用 SQLite ATTACH juhe_business/juhe_stats 承载（SetMaxOpenConns(1)
// 保证 ATTACH 对所有查询可见；表名限定与占位符改写走生产同款 s.table/
// s.statsTable/s.bind）。SQLite 分支断言不打脏（归档同步语义：全量重建调度
// 无脏范围消费者）。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const quotaDirtyLimitsJSON = `{"hourly":{"enabled":true,"hours":6,"limit":12.5}}`

// newQuotaDirtyPGFixture opens one SQLite handle with the business and stats
// schemas attached (production PG table qualification) and pre-creates the two
// tables the quota binding sync and dirty mark touch.
func newQuotaDirtyPGFixture(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "quota-dirty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`ATTACH ':memory:' AS juhe_business`,
		`ATTACH ':memory:' AS juhe_stats`,
		`CREATE TABLE juhe_business.request_quota_hourly_window_scope_bindings (
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
		`CREATE TABLE juhe_stats.usage_quota_hourly_window_dirty_scopes (
			system_account_id TEXT NOT NULL,
			scope_type TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			generation INTEGER NOT NULL,
			first_dirty_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id)
		)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture exec %q: %v", statement, err)
		}
	}
	return &Store{db: db, pg: true, secret: "quota-dirty-secret", now: func() time.Time { return time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC) }}
}

func quotaDirtyRowCount(t *testing.T, store *Store) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM juhe_stats.usage_quota_hourly_window_dirty_scopes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func quotaDirtyGeneration(t *testing.T, store *Store, ownerID, keyID string) int {
	t.Helper()
	var generation int
	if err := store.db.QueryRow(`SELECT generation FROM juhe_stats.usage_quota_hourly_window_dirty_scopes
		WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?`, ownerID, keyID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	return generation
}

func quotaBindingWindowHours(t *testing.T, store *Store, keyID string) int {
	t.Helper()
	var hours int
	if err := store.db.QueryRow(`SELECT window_hours FROM juhe_business.request_quota_hourly_window_scope_bindings
		WHERE source_type = 'api_key' AND source_id = ?`, keyID).Scan(&hours); err != nil {
		t.Fatal(err)
	}
	return hours
}

// TestQuotaHourlyWindowBindingRebuildMarksDirtyScope mirrors the archived
// :89-105 sequence: limits change → binding upsert → dirty scope row appears;
// a second rebuild bumps generation instead of inserting a second row.
func TestQuotaHourlyWindowBindingRebuildMarksDirtyScope(t *testing.T) {
	store := newQuotaDirtyPGFixture(t)
	ctx := context.Background()

	upserted, err := store.syncQuotaHourlyWindowBinding(ctx, store.db, "key-1", "owner-1",
		sql.NullString{String: quotaDirtyLimitsJSON, Valid: true}, true, "2026-09-06T08:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	if !upserted {
		t.Fatal("limits change must rebuild the binding row")
	}
	if hours := quotaBindingWindowHours(t, store, "key-1"); hours != 6 {
		t.Fatalf("binding window_hours = %d, want 6", hours)
	}
	store.markQuotaHourlyWindowDirtyScopeAfterCommit(ctx, "owner-1", "key-1", "2026-09-06T08:00:00.000Z")
	if count := quotaDirtyRowCount(t, store); count != 1 {
		t.Fatalf("dirty scope rows = %d, want 1（limits 变更→脏行出现）", count)
	}
	if generation := quotaDirtyGeneration(t, store, "owner-1", "key-1"); generation != 1 {
		t.Fatalf("dirty generation = %d, want 1", generation)
	}

	// 二次重建：generation 自增（消费侧按 (scope, generation) 认领）。
	upserted, err = store.syncQuotaHourlyWindowBinding(ctx, store.db, "key-1", "owner-1",
		sql.NullString{String: `{"hourly":{"enabled":true,"hours":12,"limit":20}}`, Valid: true}, true, "2026-09-06T08:01:00.000Z")
	if err != nil || !upserted {
		t.Fatalf("second rebuild: upserted=%v err=%v", upserted, err)
	}
	store.markQuotaHourlyWindowDirtyScopeAfterCommit(ctx, "owner-1", "key-1", "2026-09-06T08:01:00.000Z")
	if count := quotaDirtyRowCount(t, store); count != 1 {
		t.Fatalf("dirty scope rows after re-mark = %d, want 1", count)
	}
	if generation := quotaDirtyGeneration(t, store, "owner-1", "key-1"); generation != 2 {
		t.Fatalf("dirty generation after re-mark = %d, want 2", generation)
	}
}

// TestQuotaHourlyWindowDeactivateSkipsDirtyMark mirrors the archived :94-95
// early return: deactivation deletes the binding and never marks dirty.
func TestQuotaHourlyWindowDeactivateSkipsDirtyMark(t *testing.T) {
	store := newQuotaDirtyPGFixture(t)
	ctx := context.Background()
	if _, err := store.db.Exec(`INSERT INTO juhe_business.request_quota_hourly_window_scope_bindings
		(system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at)
		VALUES ('owner-1', 'api_key', 'key-1', 'api_key', 'key-1', 6, 't', 't')`); err != nil {
		t.Fatal(err)
	}

	upserted, err := store.syncQuotaHourlyWindowBinding(ctx, store.db, "key-1", "owner-1", sql.NullString{}, false, "t")
	if err != nil {
		t.Fatal(err)
	}
	if upserted {
		t.Fatal("deactivation must not report a binding upsert")
	}
	// 生产调用面按 upserted 门控（归档 :94-95 早退）：停用分支不会走到打脏。
	if upserted {
		store.markQuotaHourlyWindowDirtyScopeAfterCommit(ctx, "owner-1", "key-1", "t")
	}
	var bindings int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM juhe_business.request_quota_hourly_window_scope_bindings`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 || quotaDirtyRowCount(t, store) != 0 {
		t.Fatalf("deactivation must clear the binding and skip the dirty mark: bindings=%d dirty=%d", bindings, quotaDirtyRowCount(t, store))
	}
}

// TestQuotaHourlyWindowDirtyMarkFailureNeverPanics: the trailing mark fails
// (stats table dropped) after the committed mutation without failing it.
func TestQuotaHourlyWindowDirtyMarkFailureNeverPanics(t *testing.T) {
	store := newQuotaDirtyPGFixture(t)
	if _, err := store.db.Exec(`DROP TABLE juhe_stats.usage_quota_hourly_window_dirty_scopes`); err != nil {
		t.Fatal(err)
	}
	// 主写已提交的等价场景：AfterCommit 内部吞错并告警，不向调用方传播。
	store.markQuotaHourlyWindowDirtyScopeAfterCommit(context.Background(), "owner-1", "key-1", "t")
	if err := store.markQuotaHourlyWindowDirtyScope(context.Background(), store.db, "owner-1", "key-1", "t"); err == nil {
		t.Fatal("direct mark against the dropped table must surface the error")
	}
}

// TestQuotaHourlyWindowSQLiteModeSkipsDirtyMark: the SQLite arm keeps the
// archived sync semantics (binding rebuilt, stats dirty table untouched).
func TestQuotaHourlyWindowSQLiteModeSkipsDirtyMark(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "quota-dirty-sqlite.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE request_quota_hourly_window_scope_bindings (
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
		`CREATE TABLE usage_quota_hourly_window_dirty_scopes (
			system_account_id TEXT NOT NULL,
			scope_type TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			generation INTEGER NOT NULL,
			first_dirty_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id)
		)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("sqlite fixture exec: %v", err)
		}
	}
	store := &Store{db: db, pg: false, secret: "quota-dirty-secret", now: func() time.Time { return time.Now() }}
	ctx := context.Background()
	upserted, err := store.syncQuotaHourlyWindowBinding(ctx, db, "key-1", "owner-1",
		sql.NullString{String: quotaDirtyLimitsJSON, Valid: true}, true, "t")
	if err != nil || !upserted {
		t.Fatalf("sqlite binding rebuild: upserted=%v err=%v", upserted, err)
	}
	// SQLite 模式 AfterCommit 直接早退（无 juhe_stats 消费面）。
	store.markQuotaHourlyWindowDirtyScopeAfterCommit(ctx, "owner-1", "key-1", "t")
	var dirty int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_quota_hourly_window_dirty_scopes`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 {
		t.Fatalf("sqlite mode must not write dirty scopes, got %d", dirty)
	}
}
