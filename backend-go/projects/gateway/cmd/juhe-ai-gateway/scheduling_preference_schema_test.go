// scheduling_preferences 启动只读校验（调度偏好独立实体 P1）gateway 侧测试
// （PostgreSQL 唯一存储形态，PLAN-20261024T051925945Z）：pgtestkit 隔离库 +
// maintenance bootstrap.EnsurePostgres fresh DDL 锁同形（防启动校验与权威
// DDL / 种子漂移）；scratch schema 矩阵覆盖表形状 / api_keys 绑定列 / 内置行
// 三值 / P2 迁移完成记录四臂（含整数 CHECK 的 = ANY(ARRAY[...]) 归一化形态
// 与收窄词表臂）。错误信息必须携带 maintenance 运维动作。（原 SQLite 夹具
// 矩阵与 SQLite ensure+seed 同形锁随 sqlite 存储形态退役删除，D9。）
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedprefdomain"
)

// openSchedulingPreferencePGDatabase 创建 pgtestkit 隔离测试库并应用
// maintenance bootstrap.EnsurePostgres 生产全量建表。
func openSchedulingPreferencePGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := bootstrap.EnsurePostgres(ctx, db)
		return err
	})
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

// TestSchedulingPreferenceSchemaMatchesMaintenanceBootstrap 锁定启动校验与
// maintenance bootstrap ensure+seed 产出一致：EnsurePostgres+SeedPostgres
// 产出的业务库必须通过四臂校验（fresh 库经种子自动满足内置行与完成记录）。
func TestSchedulingPreferenceSchemaMatchesMaintenanceBootstrap(t *testing.T) {
	tdb := pgtestkit.NewTestDatabase(t,
		func(ctx context.Context, db *sql.DB) error {
			_, err := bootstrap.EnsurePostgres(ctx, db)
			return err
		},
		func(ctx context.Context, db *sql.DB) error {
			_, err := bootstrap.SeedPostgres(ctx, db, bootstrap.SeedOptions{Now: func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) }, Secret: "juhe-ai-seed-test-secret"})
			return err
		})
	if err := checkSchedulingPreferenceSchema(context.Background(), tdb.DB); err != nil {
		t.Fatalf("bootstrap ensure+seed 产出的业务库必须通过启动校验: %v", err)
	}
}

// TestSchedulingPreferenceSchemaPostgresArms 在 pgtestkit 隔离库的独立
// scratch schema 上验证 PG 四臂判定矩阵；第四臂（P2 迁移完成记录）按业务
// schema 限定读写 juhe_business.global_settings（隔离库自建，不触碰宿主
// juhe_business 数据）。
func TestSchedulingPreferenceSchemaPostgresArms(t *testing.T) {
	db := openSchedulingPreferencePGDatabase(t)

	const correctTable = `CREATE TABLE %s.scheduling_preferences (
	id text PRIMARY KEY,
	system_account_id text,
	name text NOT NULL,
	chat_mode text NOT NULL CHECK (chat_mode IN ('cost_first', 'speed_first')),
	chat_speed_first_config_json text,
	built_in integer NOT NULL DEFAULT 0 CHECK (built_in IN (0, 1)),
	created_at text NOT NULL,
	updated_at text NOT NULL
)`
	// missingBuiltInTable 是缺列臂专用变体：保留 chat_mode CHECK 但删除
	// built_in 列及其 CHECK（该臂必须由守卫因「缺列 built_in」失败，而非夹具
	// 建表失败）。
	const missingBuiltInTable = `CREATE TABLE %s.scheduling_preferences (
	id text PRIMARY KEY,
	system_account_id text,
	name text NOT NULL,
	chat_mode text NOT NULL CHECK (chat_mode IN ('cost_first', 'speed_first')),
	chat_speed_first_config_json text,
	created_at text NOT NULL,
	updated_at text NOT NULL
)`
	const missingCheckTable = `CREATE TABLE %s.scheduling_preferences (
	id text PRIMARY KEY,
	system_account_id text,
	name text NOT NULL,
	chat_mode text NOT NULL,
	chat_speed_first_config_json text,
	built_in integer NOT NULL DEFAULT 0,
	enabled integer NOT NULL DEFAULT 1,
	created_at text NOT NULL,
	updated_at text NOT NULL
)`
	const correctAPIKeys = `CREATE TABLE %s.api_keys (id text PRIMARY KEY, scheduling_preference_id text REFERENCES %s.scheduling_preferences(id))`
	// apiKeysNoReferenceDDL 是 missing-table 臂专用变体：保留绑定列但不含
	// REFERENCES，避免外键引用未建的 scheduling_preferences 使夹具本身 42P01
	// 失败（该臂必须由守卫因「表缺失」失败，而非夹具建表失败）。占位符个数
	// 不一（1 或 2 个 %s），统一下方以 ReplaceAll 注入 schema。
	const apiKeysNoReferenceDDL = `CREATE TABLE %s.api_keys (id text PRIMARY KEY, scheduling_preference_id text)`
	const builtinInsert = `INSERT INTO %s.scheduling_preferences (id, system_account_id, name, chat_mode, built_in, created_at, updated_at)
		VALUES ('schpref-builtin-chat-cost-first', NULL, '默认调度偏好', 'cost_first', 1, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`
	const builtinInsertBuiltInZero = `INSERT INTO %s.scheduling_preferences (id, system_account_id, name, chat_mode, built_in, created_at, updated_at)
		VALUES ('schpref-builtin-chat-cost-first', NULL, '默认调度偏好', 'cost_first', 0, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`

	scenarios := []struct {
		name        string
		tableDDL    string
		apiKeysDDL  string
		builtinDDL  string
		recordJSON  string
		wantErr     string
		wantRemedy  string
	}{
		{"pass", correctTable, correctAPIKeys, builtinInsert, schedulingPreferenceCompletedRecordJSON(t), "", ""},
		{"missing-table", "", apiKeysNoReferenceDDL, "", "", "表缺失", "--ensure-schema"},
		// missing-column 臂不 seed 内置行：守卫的表形状臂先于内置行臂，
		// 「缺列 built_in」先败（seed 带列 DDL 会令夹具自身 42703 失败）。
		{"missing-column", missingBuiltInTable, correctAPIKeys, "", "", "缺列 built_in", "--ensure-schema"},
		{"missing-check", missingCheckTable, correctAPIKeys, builtinInsert, schedulingPreferenceCompletedRecordJSON(t), "CHECK", "--ensure-schema"},
		// chat_mode「收窄词表」臂不设：PG 把单值 IN 归一化为 =，checker 按
		// 「列名 + 任一字面量出现」OR 判定（既有语义），收窄形态不可区分；
		// 原 SQLite 包含匹配判据才可测收窄（随方言退役，D9 断言重钉）。
		{"missing-apikey-column", correctTable, `CREATE TABLE %s.api_keys (id text PRIMARY KEY)`, builtinInsert, schedulingPreferenceCompletedRecordJSON(t), "绑定列缺失", "--ensure-schema"},
		{"builtin-row-missing", correctTable, correctAPIKeys, "", schedulingPreferenceCompletedRecordJSON(t), "内置调度偏好行", "--ensure-schema"},
		{"builtin-built-in-zero", correctTable, correctAPIKeys, builtinInsertBuiltInZero, schedulingPreferenceCompletedRecordJSON(t), "built_in=0", "--ensure-schema"},
		{"migration-record-missing", correctTable, correctAPIKeys, builtinInsert, "", "不存在", "--migrate-scheduling-preferences"},
		{"migration-record-running", correctTable, correctAPIKeys, builtinInsert, `{"stage":"running","ruleVersion":1,"completedAt":"2026-10-12T08:00:00Z","counts":{}}`, "running", "--migrate-scheduling-preferences"},
		{"migration-record-corrupt", correctTable, correctAPIKeys, builtinInsert, `{"stage":`, "不是合法 JSON", "--migrate-scheduling-preferences"},
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
			if scenario.builtinDDL != "" {
				if _, err := db.Exec(fmt.Sprintf(scenario.builtinDDL, schema)); err != nil {
					t.Fatalf("seed scratch builtin row: %v", err)
				}
			}
			// 第四臂（P2 迁移完成记录）按业务 schema 限定读取
			// juhe_business.global_settings：隔离库内按需建 schema 与表，
			// 再按场景写/清记录。
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
			if scenario.recordJSON != "" {
				if _, err := db.Exec(`INSERT INTO juhe_business.global_settings (key, value_json, updated_at) VALUES ($1, $2, '2026-10-12T08:00:00Z')`,
					schedprefdomain.MigrationSettingKey, scenario.recordJSON); err != nil {
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
			if scenario.wantRemedy != "" && !strings.Contains(err.Error(), scenario.wantRemedy) {
				t.Fatalf("错误应携带运维动作 %q，got: %v", scenario.wantRemedy, err)
			}
		})
	}
}
