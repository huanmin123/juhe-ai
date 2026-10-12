// scheduling_preferences 启动只读校验（调度偏好独立实体 P1）gateway 侧测试：
// SQLite 方言走三失败臂 + 成功臂矩阵（表形状 / api_keys 绑定列 / 内置行三值），
// 错误信息必须携带 maintenance 运维动作；并用 maintenance bootstrap ensure+seed
// 产出锁同形（防两侧 DDL / 种子漂移）。PostgreSQL 方言复用 w1g2 dev env 覆盖
// 子库（缺失或不可达时跳过），在独立 scratch schema 上做同矩阵验证。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedprefdomain"
	_ "modernc.org/sqlite"
)

// schedulingPreferenceCorrectTableDDL 是与 maintenance DDL 同构的正确表声明。
const schedulingPreferenceCorrectTableDDL = `CREATE TABLE scheduling_preferences (
	id TEXT PRIMARY KEY,
	system_account_id TEXT,
	name TEXT NOT NULL,
	chat_mode TEXT NOT NULL CHECK (chat_mode IN ('cost_first', 'speed_first')),
	chat_speed_first_config_json TEXT,
	built_in INTEGER NOT NULL DEFAULT 0 CHECK (built_in IN (0, 1)),
	enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`

// schedulingPreferenceCorrectAPIKeysDDL 是含绑定列的最小 api_keys 同构声明。
const schedulingPreferenceCorrectAPIKeysDDL = `CREATE TABLE api_keys (
	id TEXT PRIMARY KEY,
	scheduling_preference_id TEXT REFERENCES scheduling_preferences(id)
)`

// schedulingPreferenceGlobalSettingsDDL 是迁移记录臂的最小 global_settings
// 同构声明（权威 DDL 在 maintenance schema，键值对形状与之同构）。
const schedulingPreferenceGlobalSettingsDDL = `CREATE TABLE global_settings (
	key TEXT PRIMARY KEY,
	value_json TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`

// seedSchedulingPreferenceMigrationRecord 在已打开的业务库写入/清除 P2 迁移
// 记录（recordJSON 为空串时清除）：记录 JSON 经共享包 Record 序列化，不在测
// 试侧手写记录字面量。
func seedSchedulingPreferenceMigrationRecord(t *testing.T, db *sql.DB, recordJSON string) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM global_settings WHERE key = ?`, schedprefdomain.MigrationSettingKey); err != nil {
		t.Fatalf("clear migration record: %v", err)
	}
	if recordJSON == "" {
		return
	}
	if _, err := db.Exec(`INSERT INTO global_settings (key, value_json, updated_at) VALUES (?, ?, '2026-10-12T08:00:00Z')`,
		schedprefdomain.MigrationSettingKey, recordJSON); err != nil {
		t.Fatalf("seed migration record: %v", err)
	}
}

// schedulingPreferenceCompletedRecordJSON 序列化一条合规完成记录。
func schedulingPreferenceCompletedRecordJSON(t *testing.T) string {
	t.Helper()
	payload, err := json.Marshal(schedprefdomain.Record{
		Stage:       schedprefdomain.StageCompleted,
		RuleVersion: schedprefdomain.RuleVersion,
		CompletedAt: "2026-10-12T08:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	return string(payload)
}

// seedSchedulingPreferenceBuiltinRow 按契约权威值插入内置行。
func seedSchedulingPreferenceBuiltinRow(t *testing.T, db *sql.DB, builtIn, enabled int, chatMode string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO scheduling_preferences (id, system_account_id, name, chat_mode, built_in, enabled, created_at, updated_at)
		VALUES (?, NULL, ?, ?, ?, ?, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`,
		schedulingPreferenceBuiltinID, "对话·成本优先", chatMode, builtIn, enabled); err != nil {
		t.Fatalf("seed builtin row: %v", err)
	}
}

func openSchedulingPreferenceFixture(t *testing.T, schedulingDDL, apiKeysDDL string, seedBuiltin bool) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:schpref-check-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{schedulingDDL, apiKeysDDL} {
		if ddl == "" {
			continue
		}
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("fixture DDL: %v: %v", ddl, err)
		}
	}
	if seedBuiltin {
		seedSchedulingPreferenceBuiltinRow(t, db, 1, 1, "cost_first")
	}
	return db
}

func TestSchedulingPreferenceSchemaSQLitePasses(t *testing.T) {
	db := openSchedulingPreferenceFixture(t, schedulingPreferenceCorrectTableDDL, schedulingPreferenceCorrectAPIKeysDDL, true)
	// 第四臂（P2 迁移完成记录）：global_settings + completed 记录。
	if _, err := db.Exec(schedulingPreferenceGlobalSettingsDDL); err != nil {
		t.Fatalf("fixture global_settings ddl: %v", err)
	}
	seedSchedulingPreferenceMigrationRecord(t, db, schedulingPreferenceCompletedRecordJSON(t))
	if err := checkSchedulingPreferenceSchema(context.Background(), db, false); err != nil {
		t.Fatalf("标准形状必须通过: %v", err)
	}
}

// TestSchedulingPreferenceMigrationRecordGateArms 覆盖 P2 迁移完成记录臂的
// 失败矩阵（记录缺失 / running 未完成 / 记录损坏）与放行臂：前三臂形状合规，
// 仅记录状态不同。错误必须携带 --migrate-scheduling-preferences 运维动作。
func TestSchedulingPreferenceMigrationRecordGateArms(t *testing.T) {
	scenarios := []struct {
		name    string
		record  string
		wantErr string
	}{
		{"record-pass", schedulingPreferenceCompletedRecordJSON(t), ""},
		{"record-missing", "", "不存在"},
		{"record-running", `{"stage":"running","ruleVersion":1,"completedAt":"2026-10-12T08:00:00Z","counts":{}}`, "running"},
		{"record-corrupt", `{"stage":`, "不是合法 JSON"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			db := openSchedulingPreferenceFixture(t, schedulingPreferenceCorrectTableDDL, schedulingPreferenceCorrectAPIKeysDDL, true)
			if _, err := db.Exec(schedulingPreferenceGlobalSettingsDDL); err != nil {
				t.Fatalf("fixture global_settings ddl: %v", err)
			}
			seedSchedulingPreferenceMigrationRecord(t, db, scenario.record)
			err := checkSchedulingPreferenceSchema(context.Background(), db, false)
			if scenario.wantErr == "" {
				if err != nil {
					t.Fatalf("completed 记录必须放行: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("记录状态 %q 必须 fail-fast", scenario.name)
			}
			if !strings.Contains(err.Error(), scenario.wantErr) || !strings.Contains(err.Error(), "--migrate-scheduling-preferences") {
				t.Fatalf("错误应包含 %q 与迁移命令指引，got: %v", scenario.wantErr, err)
			}
		})
	}
}

// TestSchedulingPreferenceSchemaSQLiteMatchesMaintenanceBootstrap 锁定启动校验
// 与 maintenance bootstrap ensure+seed 产出一致：SQLite preflight 同路径产出的
// 业务库必须通过三臂校验（fresh 库经 bootstrap 种子自动满足，无需额外步骤）。
func TestSchedulingPreferenceSchemaSQLiteMatchesMaintenanceBootstrap(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "business.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("bootstrap ensure: %v", err)
	}
	if _, err := bootstrap.SeedSQLiteBusiness(context.Background(), db, bootstrap.SeedOptions{
		Now:    func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) },
		Secret: "juhe-ai-seed-test-secret",
	}); err != nil {
		t.Fatalf("bootstrap seed: %v", err)
	}
	if err := checkSchedulingPreferenceSchema(context.Background(), db, false); err != nil {
		t.Fatalf("bootstrap ensure+seed 产出的业务库必须通过启动校验: %v", err)
	}
}

func TestSchedulingPreferenceSchemaSQLiteFailureArms(t *testing.T) {
	const builtinID = schedulingPreferenceBuiltinID
	scenarios := []struct {
		name          string
		schedulingDDL string
		apiKeysDDL    string
		seedBuiltin   bool
		corrupt       string
		wantErr       string
	}{
		{"missing-table", "", schedulingPreferenceCorrectAPIKeysDDL, false, "", "表缺失"},
		{"missing-column", strings.Replace(schedulingPreferenceCorrectTableDDL, "enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),\n", "", 1), schedulingPreferenceCorrectAPIKeysDDL, false, "", "缺列 enabled"},
		{"missing-check", `CREATE TABLE scheduling_preferences (
	id TEXT PRIMARY KEY,
	system_account_id TEXT,
	name TEXT NOT NULL,
	chat_mode TEXT NOT NULL,
	chat_speed_first_config_json TEXT,
	built_in INTEGER NOT NULL DEFAULT 0,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`, schedulingPreferenceCorrectAPIKeysDDL, true, "", "CHECK"},
		{"missing-apikey-column", schedulingPreferenceCorrectTableDDL, `CREATE TABLE api_keys (id TEXT PRIMARY KEY)`, true, "", "绑定列缺失"},
		{"builtin-row-missing", schedulingPreferenceCorrectTableDDL, schedulingPreferenceCorrectAPIKeysDDL, false, "", "内置调度偏好行"},
		{"builtin-built-in-zero", schedulingPreferenceCorrectTableDDL, schedulingPreferenceCorrectAPIKeysDDL, true,
			`UPDATE scheduling_preferences SET built_in = 0 WHERE id = '` + builtinID + `'`, "built_in=0"},
		{"builtin-enabled-zero", schedulingPreferenceCorrectTableDDL, schedulingPreferenceCorrectAPIKeysDDL, true,
			`UPDATE scheduling_preferences SET enabled = 0 WHERE id = '` + builtinID + `'`, "enabled=0"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			db := openSchedulingPreferenceFixture(t, scenario.schedulingDDL, scenario.apiKeysDDL, scenario.seedBuiltin)
			if scenario.corrupt != "" {
				if _, err := db.Exec(scenario.corrupt); err != nil {
					t.Fatalf("corrupt builtin row: %v", err)
				}
			}
			err := checkSchedulingPreferenceSchema(context.Background(), db, false)
			if err == nil {
				t.Fatalf("三臂任一不符必须 fail-fast")
			}
			if !strings.Contains(err.Error(), scenario.wantErr) || !strings.Contains(err.Error(), "--ensure-schema") {
				t.Fatalf("错误应包含 %q 与运维动作指引，got: %v", scenario.wantErr, err)
			}
		})
	}
}

// TestSchedulingPreferenceSchemaPostgresArms 在 dev PG 覆盖子库上以独立
// scratch schema 验证 PG 方言三臂判定（含整数 CHECK 的 = ANY(ARRAY[...])
// 归一化形态）；dev env 缺失或 PG 不可达时跳过，不触碰 juhe_business 数据。
func TestSchedulingPreferenceSchemaPostgresArms(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实 PG")
	}
	env := w1pgEnvFile(t)
	host := env["DEV_POSTGRES_HOST"]
	directPort := env["DEV_POSTGRES_DIRECT_PORT"]
	adminUser := env["DEV_POSTGRES_ADMIN_USERNAME"]
	adminPass := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	appUser := env["DEV_POSTGRES_APP_USERNAME"]
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if host == "" || directPort == "" || adminUser == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键（跳过 PG 形状校验测试）")
	}
	adminDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", adminUser, adminPass, host, directPort)
	adminDB, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("打开 dev PG 管理口失败: %v", err)
	}
	defer adminDB.Close()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := adminDB.PingContext(pingCtx); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}

	database := "juhe_ai_sub2api_dev_schprefcheck"
	dropDatabase := func() {
		admin, err := sql.Open("pgx", adminDSN)
		if err != nil {
			t.Logf("清理一次性子库打开管理连接失败: %v", err)
			return
		}
		defer admin.Close()
		if _, err := admin.Exec(fmt.Sprintf(
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, database)); err != nil {
			t.Logf("终止残留会话失败: %v", err)
		}
		if _, err := admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, database)); err != nil {
			t.Logf("清理一次性子库失败: %v", err)
		}
	}
	dropDatabase()
	owner := appUser
	if owner == "" {
		owner = adminUser
	}
	if _, err := adminDB.Exec(fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, database, owner)); err != nil {
		t.Fatalf("创建一次性子库失败: %v", err)
	}
	t.Cleanup(dropDatabase)

	sep := strings.LastIndex(appURL, "/")
	db, err := sql.Open("pgx", appURL[:sep+1]+database)
	if err != nil {
		t.Fatalf("打开一次性子库失败: %v", err)
	}
	defer db.Close()

	const correctTable = `CREATE TABLE %s.scheduling_preferences (
	id text PRIMARY KEY,
	system_account_id text,
	name text NOT NULL,
	chat_mode text NOT NULL CHECK (chat_mode IN ('cost_first', 'speed_first')),
	chat_speed_first_config_json text,
	built_in integer NOT NULL DEFAULT 0 CHECK (built_in IN (0, 1)),
	enabled integer NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
	created_at text NOT NULL,
	updated_at text NOT NULL
)`
	const correctAPIKeys = `CREATE TABLE %s.api_keys (id text PRIMARY KEY, scheduling_preference_id text REFERENCES %s.scheduling_preferences(id))`
	// apiKeysNoReferenceDDL 是 missing-table 臂专用变体：保留绑定列但不含
	// REFERENCES，避免外键引用未建的 scheduling_preferences 使夹具本身 42P01
	// 失败（该臂必须由守卫因「表缺失」失败，而非夹具建表失败）。占位符个数
	// 不一（1 或 2 个 %s），统一下方以 ReplaceAll 注入 schema。
	const apiKeysNoReferenceDDL = `CREATE TABLE %s.api_keys (id text PRIMARY KEY, scheduling_preference_id text)`
	const builtinInsert = `INSERT INTO %s.scheduling_preferences (id, system_account_id, name, chat_mode, built_in, enabled, created_at, updated_at)
		VALUES ('schpref-builtin-chat-cost-first', NULL, '对话·成本优先', 'cost_first', 1, 1, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`

	scenarios := []struct {
		name        string
		tableDDL    string
		apiKeysDDL  string
		seedBuiltin bool
		seedRecord  bool
		wantErr     string
	}{
		{"pass", correctTable, correctAPIKeys, true, true, ""},
		{"missing-table", "", apiKeysNoReferenceDDL, false, false, "表缺失"},
		{"missing-apikey-column", correctTable, `CREATE TABLE %s.api_keys (id text PRIMARY KEY)`, true, true, "绑定列缺失"},
		{"missing-check", `CREATE TABLE %s.scheduling_preferences (
	id text PRIMARY KEY,
	system_account_id text,
	name text NOT NULL,
	chat_mode text NOT NULL,
	chat_speed_first_config_json text,
	built_in integer NOT NULL DEFAULT 0,
	enabled integer NOT NULL DEFAULT 1,
	created_at text NOT NULL,
	updated_at text NOT NULL
)`, correctAPIKeys, true, true, "CHECK"},
		{"builtin-row-missing", correctTable, correctAPIKeys, false, true, "内置调度偏好行"},
		{"migration-record-missing", correctTable, correctAPIKeys, true, false, "不存在"},
	}
	schema := "schpref_probe"
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema)); err != nil {
				t.Fatalf("重置 scratch schema: %v", err)
			}
			if _, err := db.Exec(fmt.Sprintf(`CREATE SCHEMA %s`, schema)); err != nil {
				t.Fatalf("建 scratch schema: %v", err)
			}
			if scenario.tableDDL != "" {
				if _, err := db.Exec(fmt.Sprintf(scenario.tableDDL, schema)); err != nil {
					t.Fatalf("建 scratch scheduling_preferences: %v", err)
				}
			}
			// 各臂 apiKeysDDL 占位符个数不同（1 或 2 个 %s），按出现次数注入
			// schema，避免 Sprintf 定参个数与占位符不齐产出 %!(EXTRA ...)。
			if _, err := db.Exec(strings.ReplaceAll(scenario.apiKeysDDL, "%s", schema)); err != nil {
				t.Fatalf("建 scratch api_keys: %v", err)
			}
			if scenario.seedBuiltin {
				if _, err := db.Exec(fmt.Sprintf(builtinInsert, schema)); err != nil {
					t.Fatalf("seed scratch builtin row: %v", err)
				}
			}
			// 第四臂（P2 迁移完成记录）按业务 schema 限定读取
			// juhe_business.global_settings：一次性子库内按需建 schema 与表，
			// 再按场景写/清记录（不触碰宿主环境的 juhe_business 数据；本子库
			// 为测试自建，DROP DATABASE 时一并清理）。
			if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
				t.Fatalf("建 juhe_business schema: %v", err)
			}
			if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS juhe_business.global_settings (
	key text PRIMARY KEY,
	value_json text NOT NULL,
	updated_at text NOT NULL
)`); err != nil {
				t.Fatalf("建 juhe_business.global_settings: %v", err)
			}
			if _, err := db.Exec(`DELETE FROM juhe_business.global_settings WHERE key = $1`, schedprefdomain.MigrationSettingKey); err != nil {
				t.Fatalf("清迁移记录: %v", err)
			}
			if scenario.seedRecord {
				if _, err := db.Exec(`INSERT INTO juhe_business.global_settings (key, value_json, updated_at) VALUES ($1, $2, '2026-10-12T08:00:00Z')`,
					schedprefdomain.MigrationSettingKey, schedulingPreferenceCompletedRecordJSON(t)); err != nil {
					t.Fatalf("seed 迁移记录: %v", err)
				}
			}
			err := checkSchedulingPreferenceSchemaPostgres(context.Background(), db, schema)
			if scenario.wantErr == "" {
				if err != nil {
					t.Fatalf("标准形状必须通过: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("形状不符必须 fail-fast")
			}
			if !strings.Contains(err.Error(), scenario.wantErr) {
				t.Fatalf("错误应包含 %q，got: %v", scenario.wantErr, err)
			}
		})
	}
}
