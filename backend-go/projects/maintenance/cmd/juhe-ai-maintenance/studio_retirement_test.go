// studio 退场命令 CLI 门禁与端到端测试（进程内直调 runMaintenance，测试进
// 程纪律：main 是唯一 os.Exit 站点；调度偏好迁移命令同名先例）：
//   - 用法门禁：缺三确认任一 / 全缺 / 与其他命令互斥 / driver 缺失，退出码 2；
//   - SQLite 端到端：全量 legacy 工件一次 Run 退出码 0 且 stdout 为单条
//     Report JSON（Ready=true、删除计数齐全）、库内对象消失、重复执行只报
//     告已退场（仍退出码 0）；后检残留路径退出码 3。

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/studioretirement"
	_ "modernc.org/sqlite"
)

func studioRetirementPaths(t *testing.T) string {
	t.Helper()
	return "business=" + filepath.Join(t.TempDir(), "business.sqlite3")
}

// seedStudioRetirementFixture 建库并造全量 legacy 工件：4 张真实 studio 表、
// 遗留索引、purpose='studio' 行与 studio_domains_json 列（夹具造数按
// schedprefmigration 先例关闭外键）。
func seedStudioRetirementFixture(t *testing.T, pathsFlag string) {
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
		`INSERT INTO route_strategies
			(id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
			VALUES ('st-studio', 'sys-studio', '策略', NULL, 'normal', 'active', 0, NULL, '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		`INSERT INTO api_keys
			(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
			 key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
			VALUES ('k-studio', 'sys-studio', 'st-studio', 'k-studio', NULL, 'hash-studio', 'sk-prefix', 'suf',
			 'enc', 'active', 0, 'studio', NULL, '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		`INSERT INTO api_keys
			(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
			 key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
			VALUES ('k-general', 'sys-studio', 'st-studio', 'k-general', NULL, 'hash-general', 'sk-prefix', 'suf',
			 'enc', 'active', 0, 'general', NULL, '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		`CREATE TABLE studio_assets (id TEXT PRIMARY KEY, created_at TEXT NOT NULL)`,
		`CREATE TABLE studio_tasks (id TEXT PRIMARY KEY, created_at TEXT NOT NULL)`,
		`CREATE TABLE studio_operations (id TEXT PRIMARY KEY, created_at TEXT NOT NULL)`,
		`CREATE TABLE studio_user_asset_usage (system_account_id TEXT NOT NULL, day TEXT NOT NULL, PRIMARY KEY (system_account_id, day))`,
		`CREATE UNIQUE INDEX idx_api_keys_studio_purpose_unique ON api_keys(system_account_id) WHERE purpose = 'studio'`,
		`ALTER TABLE api_keys ADD COLUMN studio_domains_json TEXT`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed fixture: %v: %v", statement, err)
		}
	}
}

func TestStudioRetirementUsageGates(t *testing.T) {
	paths := studioRetirementPaths(t)
	withGates := []string{"--apply-studio-retirement", "--driver", "sqlite", "--paths", paths}
	cases := []struct {
		name string
		argv []string
	}{
		{"缺 node-stopped", append(append([]string{}, withGates...), "--go-stopped", "--backup-confirmed")},
		{"缺 go-stopped", append(append([]string{}, withGates...), "--node-stopped", "--backup-confirmed")},
		{"缺 backup-confirmed", append(append([]string{}, withGates...), "--node-stopped", "--go-stopped")},
		{"全缺", withGates},
		{"与其他命令互斥", append(append([]string{}, withGates...), "--node-stopped", "--go-stopped", "--backup-confirmed", "--ensure-schema")},
		{"与 J3a 互斥", append(append([]string{}, withGates...), "--node-stopped", "--go-stopped", "--backup-confirmed", "--apply-j3a-proxy-latency-postgres")},
		{"与 J3b PG 互斥", append(append([]string{}, withGates...), "--node-stopped", "--go-stopped", "--backup-confirmed", "--apply-j3b-model-check-postgres")},
		{"与 J3b SQLite 互斥", append(append([]string{}, withGates...), "--node-stopped", "--go-stopped", "--backup-confirmed", "--apply-j3b-model-check-sqlite")},
		{"与 J2 互斥", append(append([]string{}, withGates...), "--node-stopped", "--go-stopped", "--backup-confirmed", "--apply-account-balance-postgres")},
		{"version 互斥", append(append([]string{}, withGates...), "--version")},
		{"check 互斥", append(append([]string{}, withGates...), "--check-boundary")},
		{"dry-run 互斥", append(append([]string{}, withGates...), "--dry-run")},
		{"driver 缺失", []string{"--apply-studio-retirement", "--node-stopped", "--go-stopped", "--backup-confirmed"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if code := runMaintenance(testCase.argv); code != 2 {
				t.Fatalf("runMaintenance(%v) = %d, want 2", testCase.argv, code)
			}
		})
	}
}

func TestStudioRetirementEndToEndSQLite(t *testing.T) {
	paths := studioRetirementPaths(t)
	seedStudioRetirementFixture(t, paths)
	gates := []string{"--node-stopped", "--go-stopped", "--backup-confirmed"}
	argv := append([]string{"--apply-studio-retirement", "--driver", "sqlite", "--paths", paths}, gates...)

	// 执行清理：退出码 0，stdout 单条 Report JSON，删除计数齐全。
	code, stdout, stderr := wmRunMaintenanceCapture(t, argv...)
	if code != 0 {
		t.Fatalf("apply = %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	var report studioretirement.Report
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &report); err != nil {
		t.Fatalf("stdout is not a single Report JSON: %v\n%s", err, stdout)
	}
	if !report.Ready() || report.AlreadyRetired {
		t.Fatalf("report = %+v, want ready and not alreadyRetired", report)
	}
	if report.StudioAPIKeyRows != 1 || report.DeletedAPIKeyRows != 1 {
		t.Fatalf("studio key rows preflight=%d deleted=%d, want 1/1", report.StudioAPIKeyRows, report.DeletedAPIKeyRows)
	}
	if len(report.DroppedTables) != 4 || !report.DroppedIndex || !report.DroppedColumn || !report.RebuiltTable {
		t.Fatalf("actions = %+v", report)
	}
	if !strings.Contains(stderr, "--backup-confirmed") {
		t.Fatalf("stderr must carry the rollback guidance:\n%s", stderr)
	}
	// 库内对象消失、general Key 保真。
	db, err := bootstrap.OpenSQLiteFile(strings.TrimPrefix(paths, "business="))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"studio_assets", "studio_tasks", "studio_operations", "studio_user_asset_usage"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("table %s still exists", table)
		}
	}
	var keyCount int
	if err := db.QueryRow(`SELECT count(*) FROM api_keys WHERE id = 'k-general'`).Scan(&keyCount); err != nil {
		t.Fatal(err)
	}
	if keyCount != 1 {
		t.Fatal("general key must survive retirement")
	}
	// 重复执行只报告已退场（仍退出码 0）。
	if code, stdout, stderr := wmRunMaintenanceCapture(t, argv...); code != 0 {
		t.Fatalf("rerun = %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	} else {
		var rerunReport studioretirement.Report
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &rerunReport); err != nil {
			t.Fatalf("rerun stdout is not a single Report JSON: %v\n%s", err, stdout)
		}
		if !rerunReport.Ready() || !rerunReport.AlreadyRetired {
			t.Fatalf("rerun report = %+v, want ready/alreadyRetired", rerunReport)
		}
	}
}
