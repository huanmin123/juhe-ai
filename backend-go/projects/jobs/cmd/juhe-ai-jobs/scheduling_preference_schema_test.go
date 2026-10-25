// scheduling_preferences 启动只读校验（调度偏好独立实体 P1）jobs 侧测试：
// PostgreSQL 方言复用 w12c 覆盖库连接串（不可用时跳过），在独立 scratch
// schema 上验证真实 PG 判定（含整数 CHECK 的 = ANY(ARRAY[...]) 归一化形态），
// 不触碰 juhe_business 数据。原 SQLite 方言三臂矩阵与 AtStartup 包装臂已随
// sqlite 存储形态退役删除（生产校验恒走 PG）。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/schedprefdomain"
	_ "github.com/jackc/pgx/v5/stdlib"
)

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

// TestSchedulingPreferenceSchemaPostgresArms 在 w12c 覆盖库上以独立 scratch
// schema 验证 PG 方言三臂判定；覆盖库不可用时跳过。
func TestSchedulingPreferenceSchemaPostgresArms(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实 PG")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w12c: 覆盖库连接串不可用")
	}
	db, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Skipf("打开覆盖库失败（跳过）: %v", err)
	}
	defer db.Close()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skipf("覆盖库不可达（跳过）: %v", err)
	}

	schema := "w12c_schpref_probe"
	// juhe_business 归属探测：覆盖库的 juhe_business schema 可能承载其他域
	// （J3a 等）的夹具数据——不存在则由本测试创建（清理时 DROP）；已存在则
	// 复用（清理时只删本测试写入的迁移记录行，不动他域数据）。
	var juheBusinessExists bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = 'juhe_business')`).Scan(&juheBusinessExists); err != nil {
		t.Fatalf("探测 juhe_business schema 失败: %v", err)
	}
	if !juheBusinessExists {
		if _, err := db.Exec(`CREATE SCHEMA juhe_business`); err != nil {
			t.Fatalf("建 juhe_business schema: %v", err)
		}
	}
	// defer db.Close() 先于 t.Cleanup 执行，清理必须走测试体内的显式 DROP。
	defer func() {
		if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema)); err != nil {
			t.Errorf("清理 scratch schema 失败: %v", err)
		}
		if !juheBusinessExists {
			if _, err := db.Exec(`DROP SCHEMA IF EXISTS juhe_business CASCADE`); err != nil {
				t.Errorf("清理 juhe_business scratch schema 失败: %v", err)
			}
		} else if _, err := db.Exec(`DELETE FROM juhe_business.global_settings WHERE key = $1`, schedprefdomain.MigrationSettingKey); err != nil {
			t.Errorf("清理迁移记录失败: %v", err)
		}
	}()

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
	const correctAPIKeys = `CREATE TABLE %s.api_keys (id text PRIMARY KEY, scheduling_preference_id text REFERENCES %s.scheduling_preferences(id))`
	// apiKeysNoReferenceDDL 是 missing-table 臂专用变体：保留绑定列但不含
	// REFERENCES，避免外键引用未建的 scheduling_preferences 使夹具本身 42P01
	// 失败（该臂必须由守卫因「表缺失」失败，而非夹具建表失败）。占位符个数
	// 不一（1 或 2 个 %s），统一下方以 ReplaceAll 注入 schema。
	const apiKeysNoReferenceDDL = `CREATE TABLE %s.api_keys (id text PRIMARY KEY, scheduling_preference_id text)`
	const builtinInsert = `INSERT INTO %s.scheduling_preferences (id, system_account_id, name, chat_mode, built_in, created_at, updated_at)
		VALUES ('schpref-builtin-chat-cost-first', NULL, '默认调度偏好', 'cost_first', 1, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`

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
		{"builtin-row-missing", correctTable, correctAPIKeys, false, true, "内置调度偏好行"},
		{"migration-record-missing", correctTable, correctAPIKeys, true, false, "不存在"},
	}
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
			// juhe_business.global_settings：schema 已按上方归属探测建好
			// （自建或复用预存），此处按需补表并按场景写/清记录；预存表形状
			// 由权威 DDL 保证，不复建不覆盖他域数据。
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
