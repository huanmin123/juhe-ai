package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// migrate-key-client-ip-limit CLI 夹具：迁移面五表（列形状与生产业务库同构的
// 测试子集）。
const keyClientIPLimitCLIFixtureDDL = `
CREATE SCHEMA juhe_business;
SET search_path = juhe_business;
CREATE TABLE groups (
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
  enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0,
  group_type TEXT NOT NULL DEFAULT 'personal', group_kind TEXT NOT NULL DEFAULT 'standard',
  scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE super_group_members (
  id TEXT PRIMARY KEY, super_group_id TEXT NOT NULL, member_group_id TEXT NOT NULL,
  priority INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (super_group_id, member_group_id), UNIQUE (super_group_id, priority));
CREATE TABLE route_strategies (
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, mode TEXT NOT NULL DEFAULT 'normal',
  status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, config_json TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE route_strategy_groups (
  id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 1, weight INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE api_keys (
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT NOT NULL, name TEXT NOT NULL,
  client_ip_concurrency_limit INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
`

// TestMigrateKeyClientIPLimitUsageGates 钉住参数与停写/备份门禁：apply 缺任一
// 确认必须 exit 2；dry-run/--check 不要求门禁。
func TestMigrateKeyClientIPLimitUsageGates(t *testing.T) {
	argv := func(extra ...string) []string {
		args := []string{"migrate-key-client-ip-limit", "--dsn", "postgres://u:p@127.0.0.1:5432/db"}
		return append(args, extra...)
	}
	cases := []struct {
		name string
		argv []string
	}{
		{name: "缺dsn", argv: []string{"migrate-key-client-ip-limit", "--dry-run", "--manifest-out", "x.json"}},
		{name: "dry-run缺manifest", argv: argv("--dry-run")},
		{name: "apply缺停写备份确认", argv: argv()},
		{name: "apply仅停写确认", argv: argv("--stop-write-confirmed")},
		{name: "apply仅备份确认", argv: argv("--backup-confirmed")},
		{name: "conflict-policy非法值", argv: argv("--check", "postflight", "--conflict-policy", "max")},
		{name: "check非法值", argv: argv("--check", "midflight")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code := runMaintenance(tc.argv); code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
		})
	}
}

func TestMigrateKeyClientIPLimitCLIPostgres(t *testing.T) {
	tdb := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(keyClientIPLimitCLIFixtureDDL)
		return err
	})
	dsn := tdb.DSN
	db := tdb.DB
	now := "2026-11-01T00:00:00Z"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO juhe_business.groups (id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
		VALUES ('g1', 'u1', '组一', NULL, 1, 0, 'personal', 'standard', '{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":8}', $1, $1)`, now)
	exec(`INSERT INTO juhe_business.route_strategies (id, system_account_id, name, mode, status, created_at, updated_at)
		VALUES ('s1', 'u1', '策略一', 'normal', 'active', $1, $1)`, now)
	exec(`INSERT INTO juhe_business.route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES ('b1', 's1', 'u1', 'g1', 1, 1, 'active', $1, $1)`, now)
	exec(`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, name, client_ip_concurrency_limit, created_at, updated_at)
		VALUES ('k1', 'u1', 's1', 'key1', 0, $1, $1)`, now)

	groupState := func() string {
		var raw string
		if err := db.QueryRow(`SELECT scheduling_policy_json FROM juhe_business.groups WHERE id = 'g1'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	keyState := func() int {
		var value int
		if err := db.QueryRow(`SELECT client_ip_concurrency_limit FROM juhe_business.api_keys WHERE id = 'k1'`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}

	// dry-run：写 manifest、库零写入。
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if code := runMaintenance([]string{"migrate-key-client-ip-limit", "--dsn", dsn, "--dry-run", "--manifest-out", manifestPath}); code != 0 {
		t.Fatalf("dry-run exit = %d", code)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Mode            string `json:"mode"`
		DryRun          bool   `json:"dryRun"`
		KeyColumn       bool   `json:"keyColumnPresent"`
		KeysUpdated     int    `json:"keysUpdated"`
		GroupsRewritten int    `json:"groupsRewritten"`
		Blocked         int    `json:"blockedCount"`
		Applied         int    `json:"appliedCount"`
		ConflictPolicy  string `json:"conflictPolicy"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if manifest.Mode != "plan" || !manifest.DryRun || !manifest.KeyColumn || manifest.KeysUpdated != 1 ||
		manifest.GroupsRewritten != 1 || manifest.Blocked != 0 || manifest.Applied != 0 ||
		manifest.ConflictPolicy != "block" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if groupState() != `{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":8}` || keyState() != 0 {
		t.Fatal("dry-run 后库必须保持原状")
	}

	// preflight：只读报告，退出码 0。
	if code := runMaintenance([]string{"migrate-key-client-ip-limit", "--dsn", dsn, "--check", "preflight"}); code != 0 {
		t.Fatalf("preflight exit = %d", code)
	}

	// 正式执行缺门禁必须拒绝且不写库。
	if code := runMaintenance([]string{"migrate-key-client-ip-limit", "--dsn", dsn}); code != 2 {
		t.Fatalf("apply without gates exit = %d, want 2", code)
	}
	if keyState() != 0 {
		t.Fatal("缺门禁不得写库")
	}
	if code := runMaintenance([]string{"migrate-key-client-ip-limit", "--dsn", dsn,
		"--stop-write-confirmed", "--backup-confirmed"}); code != 0 {
		t.Fatalf("apply exit = %d", code)
	}
	if keyState() != 8 {
		t.Fatalf("apply 后 Key 值 = %d, want 8", keyState())
	}
	if groupState() != `{"defaultSoftConcurrency":5}` {
		t.Fatalf("apply 后分组策略 = %s", groupState())
	}

	// postflight：完成态核验通过；幂等重跑仍退出 0。
	if code := runMaintenance([]string{"migrate-key-client-ip-limit", "--dsn", dsn, "--check", "postflight"}); code != 0 {
		t.Fatalf("postflight exit = %d", code)
	}
	if code := runMaintenance([]string{"migrate-key-client-ip-limit", "--dsn", dsn,
		"--stop-write-confirmed", "--backup-confirmed"}); code != 0 {
		t.Fatalf("idempotent rerun exit = %d", code)
	}

	// needs_review 行阻断 apply（退出码 3，零写入）。
	exec(`INSERT INTO juhe_business.groups (id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
		VALUES ('g_bad', 'u1', '畸形', NULL, 1, 0, 'personal', 'standard', '{"customLegacyKey":1}', $1, $1)`, now)
	if code := runMaintenance([]string{"migrate-key-client-ip-limit", "--dsn", dsn,
		"--stop-write-confirmed", "--backup-confirmed"}); code != 3 {
		t.Fatalf("apply with needs_review exit = %d, want 3", code)
	}
	var badPolicy string
	if err := db.QueryRow(`SELECT scheduling_policy_json FROM juhe_business.groups WHERE id = 'g_bad'`).Scan(&badPolicy); err != nil {
		t.Fatal(err)
	}
	if badPolicy != `{"customLegacyKey":1}` {
		t.Fatalf("阻断运行不得改写 needs_review 行原值: %s", badPolicy)
	}
	// postflight 在违规行存在时未就绪（退出码 3）。
	if code := runMaintenance([]string{"migrate-key-client-ip-limit", "--dsn", dsn, "--check", "postflight"}); code != 3 {
		t.Fatalf("postflight with invalid row exit = %d, want 3", code)
	}
}
