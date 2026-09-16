package j3bmodelcheck

// w12g 波次：VerifySQLiteBackfill / BackfillSQLite / RunSQLite 的高层流程分支
// 与路径隔离校验。全部使用 t.TempDir 真实 SQLite 文本/库文件。
//
// 不可达清单（w12g 登记）：
//   - backfill.go:84-109  open/query_only 错误与只读未生效分支：路径先经
//     distinctSQLitePaths stat 校验，modernc/sqlite 打开惰性，ro DSN 固定
//     携带 query_only(1)，PRAGMA 为连接级读取、不触碰文件内容
//   - backfill.go:144-146、154-185、204-210、295-301、314-336、345-364 中
//     依赖"已验证存在的真实 SQLite 表产生 Scan/Count/Commit 错误"的分支：
//     表结构与行类型受 fixture 控制，无法在真实驱动下构造失败
//   - bootstrap.go:79-93、95-117 的 PRAGMA/BeginTx/DDL/Commit 错误分支同理

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// w12gBuildBackfillTree 构造 target/dataset/stats 三个契约就绪文件：
// dataset 仅保留 Node legacy 事实表，stats 含 legacy 游标。
func w12gBuildBackfillTree(t *testing.T) (root, targetPath, datasetPath, statsPath string) {
	t.Helper()
	root = t.TempDir()
	targetPath, datasetPath, statsPath = filepath.Join(root, "target.db"), filepath.Join(root, "dataset.db"), filepath.Join(root, "stats.db")
	for _, path := range []string{targetPath, datasetPath, statsPath} {
		db := wmOpenTempJ3b(t, path)
		if path == datasetPath {
			for _, table := range []string{"model_check_input_versions", "model_check_inputs", "model_check_execution_claims", "model_check_outcomes", "model_check_scheduler_tasks"} {
				if _, err := db.Exec(`DROP TABLE ` + table); err != nil {
					t.Fatal(err)
				}
			}
			// dataset 不持有 stats 事实表，使 backfill 摘要循环必须回退 stats 读取。
			for _, table := range []string{"account_quality_health_hourly", "model_token_intercept_baseline_versions", "model_account_trust_results", "model_trust_latest_dirty_accounts", "model_trust_observation_receipts"} {
				if _, err := db.Exec(`DROP TABLE ` + table); err != nil {
					t.Fatal(err)
				}
			}
		}
		db.Close()
	}
	stats := wmOpenRW(t, statsPath)
	wmCreateStatsJobState(t, stats)
	wmInsertLegacyCursor(t, stats, "obs-1", "")
	mustCloseW12G(t, stats)
	return root, targetPath, datasetPath, statsPath
}

func mustCloseW12G(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func w12gTextFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("this is definitely not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestW12GVerifySQLiteBackfillHighLevelBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("empty paths rejected", func(t *testing.T) {
		if _, err := VerifySQLiteBackfill(ctx, "", "a", "b"); err == nil || !strings.Contains(err.Error(), "requires target, dataset and stats") {
			t.Fatalf("空路径必须拒绝: %v", err)
		}
	})

	t.Run("shared physical path fails closed", func(t *testing.T) {
		same := filepath.Join(t.TempDir(), "same.db")
		if err := os.WriteFile(same, []byte("placeholder"), 0o600); err != nil {
			t.Fatal(err)
		}
		report, err := VerifySQLiteBackfill(ctx, same, same, same)
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || report.Tables["__paths__"] != "shared physical file" {
			t.Fatalf("共享路径必须失败闭环: %+v", report)
		}
	})

	t.Run("missing file fails stat", func(t *testing.T) {
		root := t.TempDir()
		_, err := VerifySQLiteBackfill(ctx, filepath.Join(root, "t.db"), filepath.Join(root, "missing.db"), filepath.Join(root, "s.db"))
		if err == nil || !strings.Contains(err.Error(), "stat J3b SQLite path") {
			t.Fatalf("缺失文件必须报 stat 失败: %v", err)
		}
	})

	t.Run("text target schema inspection fails", func(t *testing.T) {
		root, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
		_ = root
		// 用非 SQLite 文件替换 target：sqlite_master 读取必须失败并被包装。
		text := w12gTextFile(t, "text-target.db")
		_, err := VerifySQLiteBackfill(ctx, text, datasetPath, statsPath)
		if err == nil || !strings.Contains(err.Error(), "verify J3b readback target schema") {
			t.Fatalf("文本文件 target 必须报 schema 校验失败: %v", err)
		}
		_ = targetPath
	})

	t.Run("empty target schema incomplete", func(t *testing.T) {
		_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
		empty := filepath.Join(t.TempDir(), "empty.db")
		db := wmOpenTempJ3b(t, empty)
		mustCloseW12G(t, db)
		// 重建为真正空库：删除全部契约表。
		rw := wmOpenRW(t, empty)
		rows, err := rw.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
		if err != nil {
			t.Fatal(err)
		}
		var tables []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			tables = append(tables, name)
		}
		rows.Close()
		for _, name := range tables {
			if _, err := rw.Exec(`DROP TABLE ` + name); err != nil {
				t.Fatal(err)
			}
		}
		rw.Close()
		report, err := VerifySQLiteBackfill(ctx, empty, datasetPath, statsPath)
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || report.Tables["__schema__"] != "target schema incomplete" {
			t.Fatalf("空 target 必须报告 schema 不完整: %+v", report.Tables)
		}
		_ = targetPath
	})

	t.Run("unreadable dataset fails table probe", func(t *testing.T) {
		_, _, datasetPath, statsPath := w12gBuildBackfillTree(t)
		text := w12gTextFile(t, "text-dataset.db")
		// target 需契约就绪，否则先在 schema 处返回。
		targetPath := filepath.Join(t.TempDir(), "target.db")
		target := wmOpenTempJ3b(t, targetPath)
		mustCloseW12G(t, target)
		if _, err := VerifySQLiteBackfill(ctx, targetPath, text, statsPath); err == nil {
			t.Fatal("文本文件 dataset 必须失败")
		} else if !strings.Contains(err.Error(), "file is not a database") && !strings.Contains(err.Error(), "not a database") {
			t.Fatalf("必须报出非库文件错误: %v", err)
		}
		_ = datasetPath
	})

	t.Run("mandatory absence and no-common-columns", func(t *testing.T) {
		_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
		// dataset 删除一个 mandatory 表 → "mandatory source table absent"。
		rw := wmOpenRW(t, datasetPath)
		if _, err := rw.Exec(`DROP TABLE model_check_runs`); err != nil {
			t.Fatal(err)
		}
		rw.Close()
		report, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath)
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || report.Tables["model_check_runs"] != "mandatory source table absent" {
			t.Fatalf("缺 mandatory 表必须失败闭环: %+v", report.Tables)
		}
	})

	t.Run("stats_job_state guards", func(t *testing.T) {
		_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
		rw := wmOpenRW(t, statsPath)
		if _, err := rw.Exec(`DROP TABLE stats_job_state`); err != nil {
			t.Fatal(err)
		}
		rw.Close()
		_, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath)
		if err == nil || !strings.Contains(err.Error(), "stats_job_state is missing") {
			t.Fatalf("缺失游标源表必须报错: %v", err)
		}
	})

	t.Run("trust cursor shape guard", func(t *testing.T) {
		_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
		rw := wmOpenRW(t, statsPath)
		// 加一列未映射的源列 → 形状校验失败。
		if _, err := rw.Exec(`ALTER TABLE stats_job_state ADD COLUMN w12g_extra TEXT`); err != nil {
			t.Fatal(err)
		}
		rw.Close()
		_, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath)
		if err == nil || !strings.Contains(err.Error(), "unmapped source columns") {
			t.Fatalf("未映射游标列必须拒绝: %v", err)
		}
	})

	t.Run("duplicate legacy cursor fails", func(t *testing.T) {
		_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
		rw := wmOpenRW(t, statsPath)
		if _, err := rw.Exec(`DROP TABLE stats_job_state`); err != nil {
			t.Fatal(err)
		}
		if _, err := rw.Exec(`CREATE TABLE stats_job_state (scope_type TEXT,scope_id TEXT,job_name TEXT,cursor_created_at TEXT,cursor_id TEXT,last_success_at TEXT,last_error_message TEXT,lag_seconds INTEGER,updated_at TEXT)`); err != nil {
			t.Fatal(err)
		}
		wmInsertLegacyCursor(t, rw, "obs-1", "")
		wmInsertLegacyCursor(t, rw, "obs-2", "")
		rw.Close()
		_, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath)
		if err == nil || !strings.Contains(err.Error(), "not unique") {
			t.Fatalf("重复游标必须失败: %v", err)
		}
	})
}

func TestW12GBackfillSQLiteHighLevelBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("nil and empty rejected", func(t *testing.T) {
		if _, err := BackfillSQLite(ctx, nil, "a", "b"); err == nil {
			t.Fatal("nil target 必须拒绝")
		}
		db := wmOpenTempJ3b(t, filepath.Join(t.TempDir(), "t.db"))
		defer db.Close()
		if _, err := BackfillSQLite(ctx, db, " ", "b"); err == nil {
			t.Fatal("空 dataset 路径必须拒绝")
		}
	})

	t.Run("in-memory target rejected", func(t *testing.T) {
		mem, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer mem.Close()
		_, err = BackfillSQLite(ctx, mem, "a.db", "b.db")
		if err == nil || !strings.Contains(err.Error(), "not backed by a regular file") {
			t.Fatalf("内存库必须拒绝: %v", err)
		}
	})

	t.Run("path validation failures", func(t *testing.T) {
		root := t.TempDir()
		target := wmOpenTempJ3b(t, filepath.Join(root, "target.db"))
		defer target.Close()
		if _, err := BackfillSQLite(ctx, target, filepath.Join(root, "no-dataset.db"), filepath.Join(root, "stats.db")); err == nil || !strings.Contains(err.Error(), "stat J3b SQLite source path") {
			t.Fatalf("缺失 dataset 必须在路径校验失败: %v", err)
		}
	})

	t.Run("incomplete target schema rejected", func(t *testing.T) {
		_, _, datasetPath, statsPath := w12gBuildBackfillTree(t)
		empty := filepath.Join(t.TempDir(), "empty-target.db")
		emptyDB, err := OpenSQLite(empty)
		if err != nil {
			t.Fatal(err)
		}
		// OpenSQLite 已创建空库文件；不执行 RunSQLite，target schema 即不完整。
		if _, err := emptyDB.Exec(`CREATE TABLE w12g_marker (id INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if err := emptyDB.Close(); err != nil {
			t.Fatal(err)
		}
		target := wmOpenRW(t, empty)
		defer target.Close()
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "target schema is incomplete") {
			t.Fatalf("空 target 必须拒绝回填: %v", err)
		}
	})

	t.Run("missing mandatory source rejected", func(t *testing.T) {
		_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
		rw := wmOpenRW(t, datasetPath)
		if _, err := rw.Exec(`DROP TABLE model_check_runs`); err != nil {
			t.Fatal(err)
		}
		rw.Close()
		target := wmOpenRW(t, targetPath)
		defer target.Close()
		_, err := BackfillSQLite(ctx, target, datasetPath, statsPath)
		if err == nil || !strings.Contains(err.Error(), "legacy J3b source table model_check_runs is missing") {
			t.Fatalf("缺 mandatory 源表必须拒绝: %v", err)
		}
	})

	t.Run("unmatched stats facts fall back for digest", func(t *testing.T) {
		// 正常成功路径：dataset 已删除 stats 事实表，摘要循环必须回退 stats 读取。
		_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
		target := wmOpenRW(t, targetPath)
		defer target.Close()
		report, err := BackfillSQLite(ctx, target, datasetPath, statsPath)
		if err != nil {
			t.Fatal(err)
		}
		if report.InsertedRows["account_quality_health_hourly"] != 0 || report.TargetDigest["account_quality_health_hourly"] == "" {
			t.Fatalf("stats 事实表摘要必须来自回退读取: %+v", report)
		}
		if report.InsertedRows[trustAggregationStateTable] != 1 {
			t.Fatalf("游标必须被复制: %+v", report)
		}
	})

	t.Run("text dataset fails table probe", func(t *testing.T) {
		_, targetPath, _, statsPath := w12gBuildBackfillTree(t)
		text := w12gTextFile(t, "text-dataset.db")
		target := wmOpenRW(t, targetPath)
		defer target.Close()
		_, err := BackfillSQLite(ctx, target, text, statsPath)
		if err == nil {
			t.Fatal("文本 dataset 必须失败")
		}
	})
}

func TestW12GValidateSQLiteBackfillPathsBranches(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.db")
	dataset := filepath.Join(root, "dataset.db")
	stats := filepath.Join(root, "stats.db")

	if err := ValidateSQLiteBackfillPaths("", dataset, stats); err == nil {
		t.Fatal("空路径必须拒绝")
	}
	if err := ValidateSQLiteBackfillPaths(target, filepath.Join(root, "no.db"), stats); err == nil || !strings.Contains(err.Error(), "stat J3b SQLite source path") {
		t.Fatal("缺失源必须报 stat 失败")
	}
	dir := filepath.Join(root, "as-dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSQLiteBackfillPaths(target, dir, stats); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatal("目录源必须拒绝")
	}
	// 先建源文件，target 检查（stat 顺序在源之后）才会执行。
	for _, path := range []string{dataset, stats} {
		if err := os.WriteFile(path, []byte("placeholder"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateSQLiteBackfillPaths(dir, dataset, stats); err == nil || !strings.Contains(err.Error(), "target path") {
		t.Fatal("目录 target 必须拒绝")
	}
	if err := ValidateSQLiteBackfillPaths(dataset, dataset, stats); err == nil || !strings.Contains(err.Error(), "must be distinct") {
		t.Fatal("重复路径必须拒绝")
	}
	if err := ValidateSQLiteBackfillPaths(target, dataset, stats); err != nil {
		t.Fatalf("新 target 与普通源文件必须通过: %v", err)
	}
}

func TestW12GDistinctSQLitePathsBranches(t *testing.T) {
	root := t.TempDir()
	if _, err := distinctSQLitePaths(filepath.Join(root, "no.db")); err == nil || !strings.Contains(err.Error(), "stat J3b SQLite path") {
		t.Fatal("缺失路径必须报 stat 失败")
	}
	dir := filepath.Join(root, "dir.db")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := distinctSQLitePaths(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatal("目录必须拒绝")
	}
	same := filepath.Join(root, "same.db")
	if err := os.WriteFile(same, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := distinctSQLitePaths(same, same)
	if err != nil || ok {
		t.Fatalf("同一路径必须判为共享: ok=%v err=%v", ok, err)
	}
	other := filepath.Join(root, "other.db")
	if err := os.WriteFile(other, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err = distinctSQLitePaths(same, other)
	if err != nil || !ok {
		t.Fatalf("不同文件必须判为独立: ok=%v err=%v", ok, err)
	}
}
