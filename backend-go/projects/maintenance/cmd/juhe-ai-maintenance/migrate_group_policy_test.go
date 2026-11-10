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

// migrate-group-policy CLI 夹具：迁移面三表（列形状与生产业务库同构的测试
// 子集）。
const groupPolicyCLIFixtureDDL = `
CREATE SCHEMA juhe_business;
SET search_path = juhe_business;
CREATE TABLE groups (
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
  enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0,
  group_type TEXT NOT NULL DEFAULT 'personal', group_kind TEXT NOT NULL DEFAULT 'standard',
  scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE group_authorization_settings (
  authorization_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1, group_type TEXT NOT NULL DEFAULT 'personal',
  scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE super_group_members (
  id TEXT PRIMARY KEY, super_group_id TEXT NOT NULL, member_group_id TEXT NOT NULL,
  priority INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (super_group_id, member_group_id), UNIQUE (super_group_id, priority));
`

// TestGroupPolicyGlobalMaxFromEnv 钉住 upgrade 步骤的 globalMax 来源：执行时
// env 优先（契约 §5.2），缺失回落默认，非法值回落默认并提示（不静默吞配置错
// 误）。t.Setenv 自动还原。
func TestGroupPolicyGlobalMaxFromEnv(t *testing.T) {
	if got := groupPolicyGlobalMaxFromEnv(func(string, ...any) {}); got != 5000 {
		t.Fatalf("未设置 env 时 globalMax = %d, want 5000", got)
	}
	t.Setenv("JUHE_AI_CONCURRENCY_GLOBAL_MAX", "8000")
	if got := groupPolicyGlobalMaxFromEnv(func(string, ...any) {}); got != 8000 {
		t.Fatalf("env=8000 时 globalMax = %d, want 8000", got)
	}
	t.Setenv("JUHE_AI_CONCURRENCY_GLOBAL_MAX", "not-a-number")
	warned := false
	got := groupPolicyGlobalMaxFromEnv(func(format string, _ ...any) {
		warned = true
	})
	if got != 5000 || !warned {
		t.Fatalf("env 非法时 globalMax = %d warned=%t, want 回落 5000 且 WARN", got, warned)
	}
	t.Setenv("JUHE_AI_CONCURRENCY_GLOBAL_MAX", "0")
	if got := groupPolicyGlobalMaxFromEnv(func(string, ...any) {}); got != 5000 {
		t.Fatalf("env=0 时 globalMax = %d, want 回落 5000", got)
	}
	t.Setenv("JUHE_AI_CONCURRENCY_GLOBAL_MAX", "50001")
	if got := groupPolicyGlobalMaxFromEnv(func(string, ...any) {}); got != 5000 {
		t.Fatalf("env=50001 时 globalMax = %d, want 回落 5000", got)
	}
}

func TestMigrateGroupPolicyUsageGates(t *testing.T) {
	argv := func(extra ...string) []string {
		args := []string{"migrate-group-policy", "--dsn", "postgres://u:p@127.0.0.1:5432/db"}
		return append(args, extra...)
	}
	cases := []struct {
		name string
		argv []string
	}{
		{name: "缺dsn", argv: []string{"migrate-group-policy", "--dry-run", "--manifest-out", "x.json"}},
		{name: "dry-run缺manifest", argv: argv("--dry-run")},
		{name: "apply缺停写备份确认", argv: argv("--node-stopped", "--go-stopped")},
		{name: "check非法值", argv: argv("--check", "midflight")},
		{name: "global-max非法值", argv: argv("--global-max", "0", "--check", "postflight")},
		{name: "global-max超上限", argv: argv("--global-max", "50001", "--check", "postflight")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code := runMaintenance(tc.argv); code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
		})
	}
}

func TestMigrateGroupPolicyCLIPostgres(t *testing.T) {
	tdb := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(groupPolicyCLIFixtureDDL)
		return err
	})
	dsn := tdb.DSN
	db := tdb.DB
	now := "2026-10-01T00:00:00Z"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	const legacyFull = `{"mode":"balanced_fast","defaultSoftConcurrency":5000,"fastFirstEnabled":true,
		"fallbackOnQueueEnabled":true,"breakAffinityOnSoftLimit":true,"breakAffinityOnQueueWaitMs":0,
		"slowRequestThresholdMs":30000,"firstOutputSlowThresholdMs":15000,"recentTimeoutWindowSeconds":120,
		"recentTimeoutPenaltyThreshold":2,"maxQueueWaitMs":60000,"maxQueueSize":5000,"perApiKeyQueueLimit":5000,
		"clientIpConcurrencyLimit":0,"clientIpConcurrencyOverflowMode":"reject","imageLaneMaxConcurrency":0}`
	exec(`INSERT INTO juhe_business.groups (id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
		VALUES ('g1', 'u1', '高并发一', NULL, 1, 0, 'high_concurrency', 'standard', $1, $2, $2)`, legacyFull, now)
	exec(`INSERT INTO juhe_business.groups (id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
		VALUES ('g2', 'u1', '普通一', NULL, 1, 0, 'personal', 'standard', NULL, $1, $1)`, now)
	exec(`INSERT INTO juhe_business.group_authorization_settings (authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json, created_at, updated_at)
		VALUES ('a1', 'u2', 'g1', 1, 'high_concurrency', '{"defaultSoftConcurrency":5000,"clientIpConcurrencyLimit":2,"maxQueueWaitMs":60000}', $1, $1)`, now)

	snapshot := func(t *testing.T) (string, int) {
		t.Helper()
		var raw string
		if err := db.QueryRow(`SELECT group_type || '|' || COALESCE(scheduling_policy_json,'') FROM juhe_business.groups WHERE id = 'g1'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM juhe_business.group_authorization_settings`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return raw, count
	}

	// dry-run：写 manifest、库零写入。
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if code := runMaintenance([]string{"migrate-group-policy", "--dsn", dsn, "--dry-run", "--manifest-out", manifestPath}); code != 0 {
		t.Fatalf("dry-run exit = %d", code)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		GlobalMax  int    `json:"globalMax"`
		Mode       string `json:"mode"`
		Mapped     int    `json:"mappedCount"`
		Blocked    int    `json:"blockedCount"`
		HighGroups int    `json:"highConcurrencyGroups"`
		HighAuth   int    `json:"highConcurrencyAuthorizationSettings"`
		Applied    int    `json:"appliedCount"`
		Database   string `json:"database"`
		Rollback   string `json:"rollbackNote"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if manifest.Mode != "plan" || manifest.Mapped != 2 || manifest.Blocked != 0 || manifest.Applied != 0 ||
		manifest.HighGroups != 1 || manifest.HighAuth != 1 || manifest.GlobalMax != 5000 || manifest.Rollback == "" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if snapshotState, _ := snapshot(t); snapshotState != "high_concurrency|"+legacyFull {
		t.Fatal("dry-run 后 g1 必须保持原状（库零写入）")
	}

	// preflight：只读报告，退出码 0。
	if code := runMaintenance([]string{"migrate-group-policy", "--dsn", dsn, "--check", "preflight"}); code != 0 {
		t.Fatalf("preflight exit = %d", code)
	}

	// 正式执行缺门禁必须拒绝且不写库。
	if code := runMaintenance([]string{"migrate-group-policy", "--dsn", dsn}); code != 2 {
		t.Fatalf("apply without gates exit = %d, want 2", code)
	}
	if code := runMaintenance([]string{"migrate-group-policy", "--dsn", dsn, "--global-max", "5000",
		"--node-stopped", "--go-stopped", "--backup-confirmed"}); code != 0 {
		t.Fatalf("apply exit = %d", code)
	}
	groupType, policy := "unknown", "unknown"
	if err := db.QueryRow(`SELECT group_type, COALESCE(scheduling_policy_json,'NULL') FROM juhe_business.groups WHERE id = 'g1'`).
		Scan(&groupType, &policy); err != nil {
		t.Fatal(err)
	}
	if groupType != "personal" || policy != "NULL" {
		t.Fatalf("g1 = (%q, %q), want (personal, NULL)", groupType, policy)
	}
	var authType string
	var authPolicy sql.NullString
	if err := db.QueryRow(`SELECT group_type, scheduling_policy_json FROM juhe_business.group_authorization_settings WHERE authorization_id = 'a1'`).
		Scan(&authType, &authPolicy); err != nil {
		t.Fatal(err)
	}
	if authType != "personal" || authPolicy.Valid {
		t.Fatalf("a1 = (%q, %v), want (personal, NULL)", authType, authPolicy)
	}

	// postflight：完成态核验通过。
	if code := runMaintenance([]string{"migrate-group-policy", "--dsn", dsn, "--check", "postflight"}); code != 0 {
		t.Fatalf("postflight exit = %d", code)
	}
	// 幂等重跑：0 写入仍退出 0。
	if code := runMaintenance([]string{"migrate-group-policy", "--dsn", dsn,
		"--node-stopped", "--go-stopped", "--backup-confirmed"}); code != 0 {
		t.Fatalf("idempotent rerun exit = %d", code)
	}

	// needs_review 行阻断 apply（退出码 3，零写入）。
	exec(`INSERT INTO juhe_business.groups (id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
		VALUES ('g_bad', 'u1', '畸形', NULL, 1, 0, 'personal', 'standard', '{"customLegacyKey":1}', $1, $1)`, now)
	if code := runMaintenance([]string{"migrate-group-policy", "--dsn", dsn,
		"--node-stopped", "--go-stopped", "--backup-confirmed"}); code != 3 {
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
	if code := runMaintenance([]string{"migrate-group-policy", "--dsn", dsn, "--check", "postflight"}); code != 3 {
		t.Fatalf("postflight with invalid row exit = %d, want 3", code)
	}
}
