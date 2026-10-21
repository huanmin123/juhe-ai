package mergesupermigration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const fixtureDDL = `
CREATE TABLE groups (
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
  enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0,
  group_type TEXT NOT NULL DEFAULT 'personal', group_kind TEXT NOT NULL DEFAULT 'standard',
  scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE route_strategies (
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
  mode TEXT NOT NULL DEFAULT 'normal', status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0,
  config_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE route_strategy_groups (
  id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 1, weight INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE super_group_members (
  id TEXT PRIMARY KEY, super_group_id TEXT NOT NULL, member_group_id TEXT NOT NULL,
  priority INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (super_group_id, member_group_id), UNIQUE (super_group_id, priority));
`

func openFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "merge-migration.sqlite3")+"?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("打开 fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(fixtureDDL); err != nil {
		t.Fatalf("建 fixture: %v", err)
	}
	return db
}

func exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture 写入失败 %s: %v", query, err)
	}
}

func seedFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	now := "2026-10-01T00:00:00Z"
	for _, group := range []struct{ id, owner, name string }{
		{"g1", "u1", "组一"}, {"g2", "u1", "组二"}, {"g3", "u1", "组三"}, {"g4", "u2", "他人组"},
	} {
		exec(t, db, `INSERT INTO groups (id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
			VALUES (?, ?, ?, NULL, 1, 0, 'personal', 'standard', NULL, ?, ?)`, group.id, group.owner, group.name, now, now)
	}
	exec(t, db, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, created_at, updated_at) VALUES ('s1', 'u1', '合并一', 'merge', 'active', ?, ?)`, now, now)
	exec(t, db, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, created_at, updated_at) VALUES ('s2', 'u1', '合并二', 'merge', 'active', ?, ?)`, now, now)
	exec(t, db, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, created_at, updated_at) VALUES ('s3', 'u1', '合并三', 'merge', 'active', ?, ?)`, now, now)
	for _, binding := range []struct{ id, strategy, group string; priority int }{
		{"b1", "s1", "g1", 1}, {"b2", "s1", "g2", 2},
		{"b3", "s2", "g3", 1},
		{"b4", "s3", "g1", 1}, {"b5", "s3", "g4", 2},
	} {
		exec(t, db, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
			VALUES (?, ?, 'u1', ?, ?, 1, 'active', ?, ?)`, binding.id, binding.strategy, binding.group, binding.priority, now, now)
	}
}

func TestPlanAndApply(t *testing.T) {
	db := openFixture(t)
	seedFixture(t, db)
	options := Options{Dialect: DialectSQLite, BatchID: "batch-1"}

	plan, err := BuildPlan(context.Background(), db, options)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Mapped != 1 || plan.Blocked != 2 {
		t.Fatalf("plan counts = %d/%d, want 1/2: %+v", plan.Mapped, plan.Blocked, plan)
	}
	for _, strategy := range plan.Strategies {
		switch strategy.StrategyID {
		case "s1":
			if strategy.Disposition != DispositionMapped || strategy.SuperGroupID != "mgsg_s1" || len(strategy.Members) != 2 {
				t.Fatalf("s1 plan = %+v", strategy)
			}
			if strategy.Members[0].Priority != 0 || strategy.Members[1].Priority != 1 || !strategy.Members[0].Enabled {
				t.Fatalf("s1 members = %+v", strategy.Members)
			}
		case "s2":
			if strategy.BlockedReason != BlockedEnabledMembersLessThan2 {
				t.Fatalf("s2 blocked = %q", strategy.BlockedReason)
			}
		case "s3":
			if strategy.BlockedReason != BlockedMemberOwnerMismatch {
				t.Fatalf("s3 blocked = %q", strategy.BlockedReason)
			}
		}
	}

	applied, err := Apply(context.Background(), db, options)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied.Mapped != 1 {
		t.Fatalf("applied mapped = %d", applied.Mapped)
	}
	var mode, groupKind string
	var enabled int
	if err := db.QueryRow(`SELECT mode FROM route_strategies WHERE id = 's1'`).Scan(&mode); err != nil || mode != "normal" {
		t.Fatalf("s1 mode = %q/%v", mode, err)
	}
	if err := db.QueryRow(`SELECT group_kind, enabled FROM groups WHERE id = 'mgsg_s1'`).Scan(&groupKind, &enabled); err != nil || groupKind != "super" || enabled != 0 {
		t.Fatalf("generated group = %q/%d/%v", groupKind, enabled, err)
	}
	var memberCount, bindingCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM super_group_members WHERE super_group_id = 'mgsg_s1'`).Scan(&memberCount); err != nil || memberCount != 2 {
		t.Fatalf("member rows = %d/%v", memberCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_strategy_groups WHERE route_strategy_id = 's1'`).Scan(&bindingCount); err != nil || bindingCount != 1 {
		t.Fatalf("binding rows = %d/%v", bindingCount, err)
	}
	var boundGroup string
	if err := db.QueryRow(`SELECT group_id FROM route_strategy_groups WHERE route_strategy_id = 's1'`).Scan(&boundGroup); err != nil || boundGroup != "mgsg_s1" {
		t.Fatalf("bound group = %q/%v", boundGroup, err)
	}

	// 幂等：已无 mapped 候选；未解决的两条 blocked merge 行仍保留待人工处理。
	again, err := BuildPlan(context.Background(), db, options)
	if err != nil || again.Mapped != 0 || again.Blocked != 2 {
		t.Fatalf("rerun plan = %+v/%v", again, err)
	}
}

func TestApplyIsAtomicOnBlockedStrategyConflict(t *testing.T) {
	db := openFixture(t)
	seedFixture(t, db)
	// 预置同名分组使 s1 在事务内判定 blocked；s2/s3 本就 blocked，全部不写库。
	exec(t, db, `INSERT INTO groups (id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
		VALUES ('pre', 'u1', 'migration-super-group:s1', NULL, 1, 0, 'personal', 'standard', NULL, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`)
	applied, err := Apply(context.Background(), db, Options{Dialect: DialectSQLite, BatchID: "batch-1"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied.Mapped != 0 || applied.Blocked != 3 {
		t.Fatalf("applied = %d/%d, want 0/3", applied.Mapped, applied.Blocked)
	}
	var mode string
	if err := db.QueryRow(`SELECT mode FROM route_strategies WHERE id = 's1'`).Scan(&mode); err != nil || mode != "merge" {
		t.Fatalf("blocked strategy must stay merge: %q/%v", mode, err)
	}
}
