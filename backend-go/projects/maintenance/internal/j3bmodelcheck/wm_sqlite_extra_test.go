package j3bmodelcheck

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件覆盖 J3b SQLite 链路中既有测试未触达的 evidence/digest helper 与
// 路径隔离错误分支，全部基于 t.TempDir 真实 SQLite 文件，不引入新依赖。

// wmAppliedJ3bFile 创建一个已应用契约 schema 的独立 J3b SQLite 文件并返回路径。
func wmAppliedJ3bFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := RunSQLite(context.Background(), db, true); err != nil {
		t.Fatalf("应用 J3b SQLite 契约 schema: %v", err)
	}
	return path
}

func wmOpenRW(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// wmOpenTempJ3b 打开（并注册关闭）一个已应用契约 schema 的临时 J3b 连接。
func wmOpenTempJ3b(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := RunSQLite(context.Background(), db, true); err != nil {
		t.Fatalf("应用 J3b SQLite 契约 schema: %v", err)
	}
	return db
}

// wmCreateStatsJobState 在指定库上创建 legacy Node 游标表。
func wmCreateStatsJobState(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE stats_job_state (scope_type TEXT NOT NULL,scope_id TEXT NOT NULL DEFAULT '',job_name TEXT NOT NULL,cursor_created_at TEXT,cursor_id TEXT,last_success_at TEXT,last_error_message TEXT,lag_seconds INTEGER,updated_at TEXT NOT NULL,PRIMARY KEY(scope_type,scope_id,job_name))`); err != nil {
		t.Fatal(err)
	}
}

// wmInsertLegacyCursor 写入一行标准 trust 聚合游标。
func wmInsertLegacyCursor(t *testing.T, db *sql.DB, cursorID, lastError string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO stats_job_state(scope_type,scope_id,job_name,cursor_created_at,cursor_id,last_success_at,last_error_message,lag_seconds,updated_at) VALUES ('global','','model-trust-observation-aggregation','2026-08-27T10:00:00Z',?,'2026-08-27T10:01:00Z',?,3,'2026-08-27T10:01:00Z')`, cursorID, lastError); err != nil {
		t.Fatal(err)
	}
}

func TestWMSQLiteEvidenceHelpersOnRealFile(t *testing.T) {
	ctx := context.Background()
	path := wmAppliedJ3bFile(t, "wm-evidence.db")
	db := wmOpenRW(t, path)
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at) VALUES ('run-1','sys','actor','openai','account','acct','acct','gpt-5.6','quick','manual','completed','success',90,100,'ok','{}','{}','{}','{}','probe-v1','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	count, digest, err := sqliteTableEvidence(ctx, db, "model_check_runs")
	if err != nil {
		t.Fatalf("sqliteTableEvidence: %v", err)
	}
	if count != 1 || digest == "" {
		t.Fatalf("evidence 必须同时返回行数与 digest: %d %q", count, digest)
	}
	fullDigest, err := sqliteTableDigest(ctx, db, "model_check_runs")
	if err != nil {
		t.Fatalf("sqliteTableDigest: %v", err)
	}
	if fullDigest != digest {
		t.Fatalf("同库同表的全列 digest 应与 evidence digest 一致")
	}
	if _, _, err := sqliteTableEvidence(ctx, db, "missing_table"); err == nil {
		t.Fatal("缺失表的 evidence 必须报错")
	}
	if _, err := sqliteTableDigest(ctx, db, "missing_table"); err == nil {
		t.Fatal("缺失表的 digest 必须报错")
	}

	// source 具备同名列时，against-source 证据应取共同投影且 digest 稳定。
	source, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "wm-source.db")+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY, score INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO model_check_runs(id,score) VALUES ('run-1',90)`); err != nil {
		t.Fatal(err)
	}
	commonCount, commonDigest, err := sqliteTableEvidenceAgainstSource(ctx, db, source, "model_check_runs")
	if err != nil {
		t.Fatalf("sqliteTableEvidenceAgainstSource: %v", err)
	}
	if commonCount != 1 || commonDigest == "" {
		t.Fatalf("共同投影 evidence 异常: %d %q", commonCount, commonDigest)
	}
	// 主键被排除在投影外时必须拒绝（digest 无排序稳定性）。
	if _, err := sqliteTableDigestColumns(ctx, db, "model_check_runs", []string{"score"}); err == nil || !strings.Contains(err.Error(), "not in digest projection") {
		t.Fatalf("投影缺少主键必须报错: %v", err)
	}
	if _, err := sqliteTableDigestColumns(ctx, db, "missing_table", []string{"id"}); err == nil || !strings.Contains(err.Error(), "has no primary key") {
		t.Fatalf("缺失表必须报 no primary key: %v", err)
	}
}

func TestWMSQLiteDatabasePathRejectsNonFileHandle(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := sqliteDatabasePath(context.Background(), db); err == nil || !strings.Contains(err.Error(), "not backed by a regular file") {
		t.Fatalf("内存库必须被拒绝: %v", err)
	}
}

func TestWMValidateSQLiteBackfillPathsRejectsBadInputs(t *testing.T) {
	root := t.TempDir()
	dataset := wmAppliedJ3bFile(t, "wm-paths-dataset.db")
	stats := wmAppliedJ3bFile(t, "wm-paths-stats.db")
	tests := []struct {
		name     string
		target   string
		dataset  string
		stats    string
		fragment string
	}{
		{name: "empty target", target: " ", dataset: dataset, stats: stats, fragment: "requires target"},
		{name: "missing dataset", target: filepath.Join(root, "t.db"), dataset: filepath.Join(root, "nope.db"), stats: stats, fragment: "stat J3b SQLite source path"},
		{name: "dataset is directory", target: filepath.Join(root, "t.db"), dataset: root, stats: stats, fragment: "not a regular file"},
		{name: "stats is directory", target: filepath.Join(root, "t.db"), dataset: dataset, stats: root, fragment: "not a regular file"},
		{name: "duplicate source paths", target: filepath.Join(root, "t.db"), dataset: dataset, stats: dataset, fragment: "must be distinct"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateSQLiteBackfillPaths(test.target, test.dataset, test.stats)
			if err == nil || !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("期望错误包含 %q，实际: %v", test.fragment, err)
			}
		})
	}
}

func TestWMVerifySQLiteBackfillFailsClosedOnBadPaths(t *testing.T) {
	ctx := context.Background()
	if _, err := VerifySQLiteBackfill(ctx, " ", "a", "b"); err == nil || !strings.Contains(err.Error(), "requires target") {
		t.Fatalf("空路径必须拒绝: %v", err)
	}
	dataset := wmAppliedJ3bFile(t, "wm-verify-dataset.db")
	if _, err := VerifySQLiteBackfill(ctx, filepath.Join(t.TempDir(), "missing.db"), filepath.Join(t.TempDir(), "missing2.db"), dataset); err == nil {
		t.Fatal("缺失文件必须返回 stat 错误")
	}
}

func TestWMSQLiteTrustAggregationStateGuards(t *testing.T) {
	ctx := context.Background()

	t.Run("duplicate legacy cursor fails closed", func(t *testing.T) {
		root := t.TempDir()
		target := wmOpenTempJ3b(t, filepath.Join(root, "target.db"))
		dataset := wmOpenTempJ3b(t, filepath.Join(root, "dataset.db"))
		dataset.Close()
		stats := wmOpenTempJ3b(t, filepath.Join(root, "stats.db"))
		// legacy 游标表的历史形态可能没有 (scope_type,scope_id,job_name) 主键，
		// 此时两条同 scope 游标会同时存在，复制必须失败闭环。
		if _, err := stats.Exec(`CREATE TABLE stats_job_state (scope_type TEXT NOT NULL,scope_id TEXT NOT NULL DEFAULT '',job_name TEXT NOT NULL,cursor_created_at TEXT,cursor_id TEXT,last_success_at TEXT,last_error_message TEXT,lag_seconds INTEGER,updated_at TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		wmInsertLegacyCursor(t, stats, "obs-1", "")
		wmInsertLegacyCursor(t, stats, "obs-2", "")
		statsPath := filepath.Join(root, "stats.db")
		datasetPath := filepath.Join(root, "dataset.db")
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "not unique") {
			t.Fatalf("重复游标必须失败: %v", err)
		}
	})

	t.Run("conflicting target cursor fails closed", func(t *testing.T) {
		root := t.TempDir()
		target := wmOpenTempJ3b(t, filepath.Join(root, "target.db"))
		dataset := wmOpenTempJ3b(t, filepath.Join(root, "dataset.db"))
		dataset.Close()
		if _, err := target.Exec(`INSERT INTO model_trust_aggregation_state(scope_key,cursor_created_at,cursor_id,last_success_at,last_error_message,lag_seconds,updated_at) VALUES ('model-trust-observation-aggregation','2026-08-27T10:00:00Z','different','2026-08-27T10:01:00Z',NULL,3,'2026-08-27T10:01:00Z')`); err != nil {
			t.Fatal(err)
		}
		stats := wmOpenTempJ3b(t, filepath.Join(root, "stats.db"))
		wmCreateStatsJobState(t, stats)
		wmInsertLegacyCursor(t, stats, "obs-1", "")
		if _, err := BackfillSQLite(ctx, target, filepath.Join(root, "dataset.db"), filepath.Join(root, "stats.db")); err == nil || !strings.Contains(err.Error(), "conflict in model_trust_aggregation_state") {
			t.Fatalf("目标游标冲突必须失败: %v", err)
		}
	})

	t.Run("malformed source shape fails closed", func(t *testing.T) {
		root := t.TempDir()
		target := wmOpenTempJ3b(t, filepath.Join(root, "target.db"))
		dataset := wmOpenTempJ3b(t, filepath.Join(root, "dataset.db"))
		dataset.Close()
		stats := wmOpenTempJ3b(t, filepath.Join(root, "stats.db"))
		// 缺少 lag_seconds 列：形状校验必须先于复制拒绝。
		if _, err := stats.Exec(`CREATE TABLE stats_job_state (scope_type TEXT NOT NULL,scope_id TEXT NOT NULL DEFAULT '',job_name TEXT NOT NULL,cursor_created_at TEXT,cursor_id TEXT,last_success_at TEXT,last_error_message TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(scope_type,scope_id,job_name))`); err != nil {
			t.Fatal(err)
		}
		if _, err := BackfillSQLite(ctx, target, filepath.Join(root, "dataset.db"), filepath.Join(root, "stats.db")); err == nil || !strings.Contains(err.Error(), "missing source columns") {
			t.Fatalf("游标源表缺列必须拒绝: %v", err)
		}
	})

	t.Run("cursor drift surfaces in readback", func(t *testing.T) {
		root := t.TempDir()
		stats := wmOpenTempJ3b(t, filepath.Join(root, "stats.db"))
		dataset := wmOpenTempJ3b(t, filepath.Join(root, "dataset.db"))
		dataset.Close()
		wmCreateStatsJobState(t, stats)
		wmInsertLegacyCursor(t, stats, "obs-1", "")
		stats.Close()
		target, err := OpenSQLite(filepath.Join(root, "target.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := RunSQLite(ctx, target, true); err != nil {
			target.Close()
			t.Fatal(err)
		}
		if _, err := BackfillSQLite(ctx, target, filepath.Join(root, "dataset.db"), filepath.Join(root, "stats.db")); err != nil {
			target.Close()
			t.Fatal(err)
		}
		target.Close()
		report, err := VerifySQLiteBackfill(ctx, filepath.Join(root, "target.db"), filepath.Join(root, "dataset.db"), filepath.Join(root, "stats.db"))
		if err != nil {
			t.Fatal(err)
		}
		if !report.Ready || report.Tables[trustAggregationStateTable] != "match" {
			t.Fatalf("一致游标应 match: %+v", report)
		}
		// 篡改源游标后 readback 必须报告 drift。
		rw := wmOpenRW(t, filepath.Join(root, "stats.db"))
		if _, err := rw.Exec(`UPDATE stats_job_state SET cursor_id='obs-9'`); err != nil {
			t.Fatal(err)
		}
		rw.Close()
		report, err = VerifySQLiteBackfill(ctx, filepath.Join(root, "target.db"), filepath.Join(root, "dataset.db"), filepath.Join(root, "stats.db"))
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || report.Tables[trustAggregationStateTable] != "drift" {
			t.Fatalf("游标漂移必须报告 drift: %+v", report)
		}
	})
}

func TestWMBackfillSQLiteRejectsNilAndEmptyInputs(t *testing.T) {
	ctx := context.Background()
	if _, err := BackfillSQLite(ctx, nil, "a", "b"); err == nil {
		t.Fatal("nil target 必须被拒绝")
	}
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "wm-nil.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := BackfillSQLite(ctx, db, " ", "b"); err == nil {
		t.Fatal("空 dataset 路径必须被拒绝")
	}
}

func TestWMVerifyQueryOnlyOnWritableConnection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wm-rw.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	readOnly, err := verifyQueryOnly(ctx, db)
	if err != nil {
		t.Fatalf("verifyQueryOnly: %v", err)
	}
	if readOnly {
		t.Fatal("rw 连接的 query_only 必须为 0")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件应已创建: %v", err)
	}
}
