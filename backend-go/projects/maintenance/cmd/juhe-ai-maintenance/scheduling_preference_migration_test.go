// 调度偏好迁移命令 CLI 门禁与端到端测试（进程内直调 runMaintenance，测试进
// 程纪律：main 是唯一 os.Exit 站点）：
//   - 用法门禁：migrate 缺三确认任一 / verify 缺 check / verify 非法 check /
//     migrate+verify 互斥 / 与其他命令互斥 / driver 缺失，全部退出码 2；
//   - SQLite 端到端：preflight（退出码 0，坏数据也只提示）→ migrate（三确认
//     门禁通过）→ postflight 放行 → 重复 migrate 只报告完成 → 失败清单路径
//     退出码 3 且不写完成记录。

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedprefdomain"
	_ "modernc.org/sqlite"
)

func schedulingPreferencePaths(t *testing.T) string {
	t.Helper()
	return "business=" + filepath.Join(t.TempDir(), "business.sqlite3")
}

// seedSchedulingPreferenceMigrationFixture 建库并造最小迁移数据集：一条
// speed_first 策略 + 一条 NULL 普通 Key + 内置行。
func seedSchedulingPreferenceMigrationFixture(t *testing.T, pathsFlag string) {
	t.Helper()
	db, err := bootstrap.OpenSQLiteFile(strings.TrimPrefix(pathsFlag, "business="))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	defer db.Close()
	if _, err := schema.EnsureSQLiteBusiness(context.Background(), db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatalf("toggle foreign_keys: %v", err)
	}
	defer func() { _, _ = db.Exec("PRAGMA foreign_keys=ON") }()
	statements := []string{
		`INSERT INTO scheduling_preferences
			(id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, enabled, created_at, updated_at)
			VALUES ('schpref-builtin-chat-cost-first', NULL, '对话·成本优先', 'cost_first', NULL, 1, 1, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`,
		`INSERT INTO route_strategies
			(id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
			VALUES ('st-cli-speed', 'sys-cli', '策略CLI', NULL, 'normal', 'active', 0,
			 '{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000}}', '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')`,
		`INSERT INTO api_keys
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

// seedSchedulingPreferenceBadConfig 在既有夹具库上把策略配置改为坏 JSON。
func seedSchedulingPreferenceBadConfig(t *testing.T, pathsFlag string) {
	t.Helper()
	db, err := bootstrap.OpenSQLiteFile(strings.TrimPrefix(pathsFlag, "business="))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE route_strategies SET config_json = '{"normalRoutingConfig":' WHERE id = 'st-cli-speed'`); err != nil {
		t.Fatalf("corrupt config: %v", err)
	}
}

func schedulingPreferenceMigrationArgv(paths string, gates ...string) []string {
	argv := []string{"--migrate-scheduling-preferences", "--driver", "sqlite", "--paths", paths}
	return append(argv, gates...)
}

func TestSchedulingPreferencesUsageGates(t *testing.T) {
	paths := schedulingPreferencePaths(t)
	cases := []struct {
		name string
		argv []string
	}{
		{"migrate 缺 node-stopped", schedulingPreferenceMigrationArgv(paths, "--go-stopped", "--backup-confirmed")},
		{"migrate 缺 go-stopped", schedulingPreferenceMigrationArgv(paths, "--node-stopped", "--backup-confirmed")},
		{"migrate 缺 backup-confirmed", schedulingPreferenceMigrationArgv(paths, "--node-stopped", "--go-stopped")},
		{"migrate 全缺", schedulingPreferenceMigrationArgv(paths)},
		{"migrate/verify 互斥", []string{"--migrate-scheduling-preferences", "--verify-scheduling-preferences"}},
		{"verify 缺 check", []string{"--verify-scheduling-preferences", "--driver", "sqlite", "--paths", paths}},
		{"verify 非法 check", []string{"--verify-scheduling-preferences", "--scheduling-preferences-check=midflight", "--driver", "sqlite", "--paths", paths}},
		{"与其他命令互斥", schedulingPreferenceMigrationArgv(paths, "--node-stopped", "--go-stopped", "--backup-confirmed", "--ensure-schema")},
		{"driver 缺失", []string{"--migrate-scheduling-preferences", "--node-stopped", "--go-stopped", "--backup-confirmed"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if code := runMaintenance(testCase.argv); code != 2 {
				t.Fatalf("runMaintenance(%v) = %d, want 2", testCase.argv, code)
			}
		})
	}
}

func TestSchedulingPreferencesEndToEndSQLite(t *testing.T) {
	paths := schedulingPreferencePaths(t)
	seedSchedulingPreferenceMigrationFixture(t, paths)
	gates := []string{"--node-stopped", "--go-stopped", "--backup-confirmed"}

	// 预检：只读报告，退出码 0。
	if code := runMaintenance([]string{"--verify-scheduling-preferences", "--scheduling-preferences-check=preflight", "--driver", "sqlite", "--paths", paths}); code != 0 {
		t.Fatalf("preflight = %d, want 0", code)
	}
	// 未迁移前 postflight 必须不通过（退出码 3）。
	if code := runMaintenance([]string{"--verify-scheduling-preferences", "--scheduling-preferences-check=postflight", "--driver", "sqlite", "--paths", paths}); code != 3 {
		t.Fatalf("迁移前 postflight = %d, want 3", code)
	}
	// 执行迁移。
	if code := runMaintenance(schedulingPreferenceMigrationArgv(paths, gates...)); code != 0 {
		t.Fatalf("migrate = %d, want 0", code)
	}
	// postflight 放行。
	if code := runMaintenance([]string{"--verify-scheduling-preferences", "--scheduling-preferences-check=postflight", "--driver", "sqlite", "--paths", paths}); code != 0 {
		t.Fatalf("迁移后 postflight = %d, want 0", code)
	}
	// 重复执行只报告完成（仍退出码 0）。
	if code := runMaintenance(schedulingPreferenceMigrationArgv(paths, gates...)); code != 0 {
		t.Fatalf("重复 migrate = %d, want 0", code)
	}
	// 完成记录落库且 completed。
	db, err := bootstrap.OpenSQLiteFile(strings.TrimPrefix(paths, "business="))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var valueJSON string
	if err := db.QueryRow(`SELECT value_json FROM global_settings WHERE key = ?`, schedprefdomain.MigrationSettingKey).Scan(&valueJSON); err != nil {
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
	paths := schedulingPreferencePaths(t)
	seedSchedulingPreferenceMigrationFixture(t, paths)
	seedSchedulingPreferenceBadConfig(t, paths)

	snapshotBefore := "blocked"
	if code := runMaintenance(schedulingPreferenceMigrationArgv(paths, "--node-stopped", "--go-stopped", "--backup-confirmed")); code != 3 {
		t.Fatalf("坏数据 migrate = %d, want 3", code)
	}
	// 完成记录未写入；preflight 报告坏配置但退出码 0。
	db, err := bootstrap.OpenSQLiteFile(strings.TrimPrefix(paths, "business="))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM global_settings WHERE key = ?`, schedprefdomain.MigrationSettingKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("失败路径不得写完成记录，settings rows = %d", count)
	}
	if code := runMaintenance([]string{"--verify-scheduling-preferences", "--scheduling-preferences-check=preflight", "--driver", "sqlite", "--paths", paths}); code != 0 {
		t.Fatalf("坏数据 preflight = %d, want 0", code)
	}
	if snapshotBefore != "blocked" {
		t.Fatalf("unreachable")
	}
}
