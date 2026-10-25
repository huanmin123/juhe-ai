// 调度偏好迁移命令 CLI 门禁与端到端测试（进程内直调 runMaintenance，测试进
// 程纪律：main 是唯一 os.Exit 站点）：
//   - 用法门禁：migrate 缺三确认任一 / verify 缺 check / verify 非法 check /
//     migrate+verify 互斥 / 与其他命令互斥 / dsn 缺失，全部退出码 2；
//   - 真实 PG 端到端（pgtestkit 隔离库）：preflight（退出码 0，坏数据也只提
//     示）→ migrate（三确认门禁通过）→ postflight 放行 → 重复 migrate 只报告
//     完成 → 失败清单路径退出码 3 且不写完成记录。
// SQLite 形态随 SQLite 存储形态退役删除（PLAN-20261024T051925945Z B4）。

package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedprefdomain"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// seedSchedulingPreferenceMigrationFixture 造最小迁移数据集：一条 speed_first
// 策略 + 一条 NULL 普通 Key + 内置行。
func seedSchedulingPreferenceMigrationFixture(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		`INSERT INTO "juhe_business"."system_accounts"
			(id, username, display_name, description, role, status, password_hash, created_at, updated_at)
			VALUES ('sys-cli', 'sys-cli', 'sys-cli', NULL, 'user', 'active', 'hash', '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`,
		`INSERT INTO "juhe_business"."scheduling_preferences"
			(id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
			VALUES ('schpref-builtin-chat-cost-first', NULL, '默认调度偏好', 'cost_first', NULL, 1, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`,
		`INSERT INTO "juhe_business"."route_strategies"
			(id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
			VALUES ('st-cli-speed', 'sys-cli', '策略CLI', NULL, 'normal', 'active', 0,
			 '{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000}}', '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`,
		`INSERT INTO "juhe_business"."api_keys"
			(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
			 key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
			VALUES ('k-cli', 'sys-cli', 'st-cli-speed', 'k-cli', NULL, 'hash-cli', 'sk-prefix', 'suf', 'enc',
			 'active', 0, 'general', NULL, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed fixture: %v: %v", statement, err)
		}
	}
}

// seedSchedulingPreferenceBadConfig 把策略配置改为坏 JSON。
func seedSchedulingPreferenceBadConfig(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE "juhe_business"."route_strategies" SET config_json = '{"normalRoutingConfig":' WHERE id = 'st-cli-speed'`); err != nil {
		t.Fatalf("corrupt config: %v", err)
	}
}

func schedulingPreferenceMigrationArgv(dsn string, gates ...string) []string {
	argv := []string{"--migrate-scheduling-preferences", "--dsn", dsn}
	return append(argv, gates...)
}

func TestSchedulingPreferencesUsageGates(t *testing.T) {
	dsn := "postgres://juhe-ai-test.invalid/testdb"
	cases := []struct {
		name string
		argv []string
	}{
		{"migrate 缺 node-stopped", schedulingPreferenceMigrationArgv(dsn, "--go-stopped", "--backup-confirmed")},
		{"migrate 缺 go-stopped", schedulingPreferenceMigrationArgv(dsn, "--node-stopped", "--backup-confirmed")},
		{"migrate 缺 backup-confirmed", schedulingPreferenceMigrationArgv(dsn, "--node-stopped", "--go-stopped")},
		{"migrate 全缺", schedulingPreferenceMigrationArgv(dsn)},
		{"migrate 缺 dsn", []string{"--migrate-scheduling-preferences", "--node-stopped", "--go-stopped", "--backup-confirmed"}},
		{"migrate/verify 互斥", []string{"--migrate-scheduling-preferences", "--verify-scheduling-preferences"}},
		{"verify 缺 check", []string{"--verify-scheduling-preferences", "--dsn", dsn}},
		{"verify 非法 check", []string{"--verify-scheduling-preferences", "--scheduling-preferences-check=midflight", "--dsn", dsn}},
		{"与其他命令互斥", schedulingPreferenceMigrationArgv(dsn, "--node-stopped", "--go-stopped", "--backup-confirmed", "--ensure-schema")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if code := runMaintenance(testCase.argv); code != 2 {
				t.Fatalf("runMaintenance(%v) = %d, want 2", testCase.argv, code)
			}
		})
	}
}

func TestSchedulingPreferencesEndToEndPostgres(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := schema.EnsurePostgres(ctx, db)
		return err
	}).DSN
	seedSchedulingPreferenceMigrationFixture(t, dsn)
	gates := []string{"--node-stopped", "--go-stopped", "--backup-confirmed"}

	// 预检：只读报告，退出码 0。
	if code := runMaintenance([]string{"--verify-scheduling-preferences", "--scheduling-preferences-check=preflight", "--dsn", dsn}); code != 0 {
		t.Fatalf("preflight = %d, want 0", code)
	}
	// 未迁移前 postflight 必须不通过（退出码 3）。
	if code := runMaintenance([]string{"--verify-scheduling-preferences", "--scheduling-preferences-check=postflight", "--dsn", dsn}); code != 3 {
		t.Fatalf("迁移前 postflight = %d, want 3", code)
	}
	// 执行迁移。
	if code := runMaintenance(schedulingPreferenceMigrationArgv(dsn, gates...)); code != 0 {
		t.Fatalf("migrate = %d, want 0", code)
	}
	// postflight 放行。
	if code := runMaintenance([]string{"--verify-scheduling-preferences", "--scheduling-preferences-check=postflight", "--dsn", dsn}); code != 0 {
		t.Fatalf("迁移后 postflight = %d, want 0", code)
	}
	// 重复执行只报告完成（仍退出码 0）。
	if code := runMaintenance(schedulingPreferenceMigrationArgv(dsn, gates...)); code != 0 {
		t.Fatalf("重复 migrate = %d, want 0", code)
	}
	// 完成记录落库且 completed。
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var valueJSON string
	if err := db.QueryRow(`SELECT value_json FROM "juhe_business"."global_settings" WHERE key = $1`, schedprefdomain.MigrationSettingKey).Scan(&valueJSON); err != nil {
		t.Fatalf("read migration record: %v", err)
	}
	record, err := schedprefdomain.ParseRecord(valueJSON)
	if err != nil {
		t.Fatalf("parse migration record: %v", err)
	}
	if record.Stage != schedprefdomain.StageCompleted {
		t.Fatalf("record stage = %q, want completed", record.Stage)
	}
}

func TestSchedulingPreferencesFailureExitsThree(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := schema.EnsurePostgres(ctx, db)
		return err
	}).DSN
	seedSchedulingPreferenceMigrationFixture(t, dsn)
	seedSchedulingPreferenceBadConfig(t, dsn)

	if code := runMaintenance(schedulingPreferenceMigrationArgv(dsn, "--node-stopped", "--go-stopped", "--backup-confirmed")); code != 3 {
		t.Fatalf("坏数据 migrate = %d, want 3", code)
	}
	// 完成记录未写入；preflight 报告坏配置但退出码 0。
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM "juhe_business"."global_settings" WHERE key = $1`, schedprefdomain.MigrationSettingKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("失败路径不得写完成记录，settings rows = %d", count)
	}
	if code := runMaintenance([]string{"--verify-scheduling-preferences", "--scheduling-preferences-check=preflight", "--dsn", dsn}); code != 0 {
		t.Fatalf("坏数据 preflight = %d, want 0", code)
	}
}
