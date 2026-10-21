package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const mergeSuperCLIFixtureDDL = `
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

// 迁移 CLI 的 SQLite 分支：dry-run 只写 manifest，apply 在单事务内把 merge
// 策略改写为绑定超级分组的 normal 策略（超级分组设计 §6）。
func TestMergeSuperGroupsMigrationCLISQLite(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+businessPath+"?mode=rwc")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(mergeSuperCLIFixtureDDL); err != nil {
		db.Close()
		t.Fatalf("create fixture: %v", err)
	}
	now := "2026-10-01T00:00:00Z"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	for _, groupID := range []string{"g1", "g2"} {
		exec(`INSERT INTO groups (id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
			VALUES (?, 'u1', ?, NULL, 1, 0, 'personal', 'standard', NULL, ?, ?)`, groupID, "组-"+groupID, now, now)
	}
	exec(`INSERT INTO route_strategies (id, system_account_id, name, mode, status, created_at, updated_at)
		VALUES ('s1', 'u1', '合并策略', 'merge', 'active', ?, ?)`, now, now)
	exec(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES ('b1', 's1', 'u1', 'g1', 1, 1, 'active', ?, ?)`, now, now)
	exec(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES ('b2', 's1', 'u1', 'g2', 2, 1, 'active', ?, ?)`, now, now)
	db.Close()

	paths := "business=" + businessPath
	manifestPath := filepath.Join(root, "manifest.json")
	argv := func(extra ...string) []string {
		args := []string{
			"--migrate-merge-super-groups", "--migration-batch-id", "batch-cli",
			"--manifest-out", manifestPath, "--driver", "sqlite", "--paths", paths,
		}
		return append(args, extra...)
	}
	if code := runMaintenance(argv("--dry-run")); code != 0 {
		t.Fatalf("dry-run exit = %d", code)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		BatchID string `json:"migration_batch_id"`
		Mapped  int    `json:"mapped_count"`
		Blocked int    `json:"blocked_count"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if manifest.Mapped != 1 || manifest.Blocked != 0 || manifest.BatchID != "batch-cli" {
		t.Fatalf("manifest = %+v", manifest)
	}

	// 正式执行缺三确认门禁必须拒绝且不写库。
	if code := runMaintenance(argv()); code != 2 {
		t.Fatalf("apply without gates exit = %d, want 2", code)
	}
	if code := runMaintenance(argv("--node-stopped", "--go-stopped", "--backup-confirmed")); code != 0 {
		t.Fatalf("apply exit = %d", code)
	}

	check, err := sql.Open("sqlite", "file:"+businessPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open result: %v", err)
	}
	defer check.Close()
	var mode, groupID string
	if err := check.QueryRow(`SELECT mode FROM route_strategies WHERE id = 's1'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "normal" {
		t.Fatalf("mode = %q, want normal", mode)
	}
	if err := check.QueryRow(`SELECT group_id FROM route_strategy_groups WHERE route_strategy_id = 's1'`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if groupID != "mgsg_s1" {
		t.Fatalf("binding group = %q", groupID)
	}
	var members int
	if err := check.QueryRow(`SELECT COUNT(*) FROM super_group_members WHERE super_group_id = 'mgsg_s1' AND enabled = 1`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 2 {
		t.Fatalf("members = %d, want 2", members)
	}
}
