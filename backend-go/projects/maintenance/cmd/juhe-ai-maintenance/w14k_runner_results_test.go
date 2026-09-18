package main

// w14k 波次：main.go 各 runner 抽出的 result 函数的进程内覆盖。runner 原主体
// 以 os.Exit 终止，传统 -coverprofile 口径只能靠子进程重执行（wm_main_exit_
// branches_test.go）拿到行为断言、拿不到计数；本文件直调等价的 *Result /
// *OutcomeExitCode 函数，覆盖 usage 预检、Open 拒绝、运行时失败、encode 失败、
// 未就绪门与成功路径的全部语句。wrapper 的 os.Exit 一行保留子进程行为覆盖。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/goruntimemetrics"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3aproxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// wm14kWithClosedStdout 用已关闭的管道替换 os.Stdout，使 json 编码写入必然
// 失败，从而覆盖各 report 层的 encode 错误分支。
func wm14kWithClosedStdout(t *testing.T, fn func()) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = saved }()
	fn()
}

// wm14kUnreachableURL 形状合法（有主机/库/角色）但主机不可达的维护 DSN。
const wm14kUnreachableURL = "postgres://w14k@127.0.0.1:1/w14k-none"

func TestW14KRunnerUsageGates(t *testing.T) {
	root := t.TempDir()
	malformed := filepath.Join(root, "malformed.json")
	if err := os.WriteFile(malformed, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyFacts := filepath.Join(root, "empty-facts.json")
	if err := os.WriteFile(emptyFacts, []byte(`{"facts":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "missing.json")

	t.Run("go runtime metrics", func(t *testing.T) {
		t.Setenv(goruntimemetrics.BootstrapEnv, "")
		if got := goRuntimeMetricsBootstrapResult(false, "  ", false, false, false); got != 2 {
			t.Fatalf("空 URL 必须返回 2: %d", got)
		}
		if got := goRuntimeMetricsBootstrapResult(true, "postgres://w14k@127.0.0.1:5432/db", false, false, false); got != 2 {
			t.Fatalf("apply 无确认必须返回 2: %d", got)
		}
		// Open 的 URL 校验：缺主机必须被拒绝。
		if got := goRuntimeMetricsBootstrapResult(false, "postgres://@/", false, false, false); got != 2 {
			t.Fatalf("缺主机 URL 必须返回 2: %d", got)
		}
	})
	t.Run("j3b pg backfill", func(t *testing.T) {
		if got := j3bModelCheckPostgresBackfillResult("", 1, 1, false, false, false, ""); got != 2 {
			t.Fatalf("空 URL 必须返回 2: %d", got)
		}
		if got := j3bModelCheckPostgresBackfillResult("postgres://w14k@127.0.0.1:5432/db", 1, 1, false, false, false, ""); got != 2 {
			t.Fatalf("无确认必须返回 2: %d", got)
		}
		if got := j3bModelCheckPostgresBackfillResult("postgres://w14k@127.0.0.1:5432/db", 1, 1, true, true, true, " "); got != 2 {
			t.Fatalf("空 evidence 路径必须返回 2: %d", got)
		}
		if got := j3bModelCheckPostgresBackfillResult("postgres://w14k@127.0.0.1:5432/db", 1, 1, true, true, true, missing); got != 2 {
			t.Fatalf("缺失 evidence 文件必须返回 2: %d", got)
		}
		if got := j3bModelCheckPostgresBackfillResult("postgres://w14k@127.0.0.1:5432/db", 1, 1, true, true, true, malformed); got != 3 {
			t.Fatalf("malformed evidence 必须 fail-closed 返回 3: %d", got)
		}
	})
	t.Run("j3b pg readback", func(t *testing.T) {
		if got := j3bModelCheckPostgresReadbackResult("", 1); got != 2 {
			t.Fatalf("空 URL 必须返回 2: %d", got)
		}
		if got := j3bModelCheckPostgresReadbackResult("postgres://@/", 1); got != 2 {
			t.Fatalf("缺主机 URL 必须返回 2: %d", got)
		}
	})
	t.Run("j3b pg bootstrap", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.BootstrapEnv, "")
		if got := j3bModelCheckBootstrapResult(false); got != 2 {
			t.Fatalf("空 env 必须返回 2: %d", got)
		}
		t.Setenv(j3bmodelcheck.BootstrapEnv, "postgres://@/")
		if got := j3bModelCheckBootstrapResult(false); got != 2 {
			t.Fatalf("缺主机 URL 必须返回 2: %d", got)
		}
	})
	t.Run("j3a bootstrap", func(t *testing.T) {
		t.Setenv(j3aproxylatency.BootstrapEnv, "")
		if got := j3aProxyLatencyBootstrapResult(false); got != 2 {
			t.Fatalf("空 env 必须返回 2: %d", got)
		}
		t.Setenv(j3aproxylatency.BootstrapEnv, "postgres://@/")
		if got := j3aProxyLatencyBootstrapResult(false); got != 2 {
			t.Fatalf("缺主机 URL 必须返回 2: %d", got)
		}
	})
	t.Run("j3b sqlite bootstrap", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, "")
		if got := j3bModelCheckSQLiteBootstrapResult(false, false, false, false); got != 2 {
			t.Fatalf("空 env 必须返回 2: %d", got)
		}
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, filepath.Join(root, "apply.db"))
		if got := j3bModelCheckSQLiteBootstrapResult(true, false, false, false); got != 2 {
			t.Fatalf("apply 无确认必须返回 2: %d", got)
		}
	})
	t.Run("j3b sqlite backfill", func(t *testing.T) {
		if got := j3bModelCheckSQLiteBackfillResult(false, true, true, ""); got != 2 {
			t.Fatalf("无确认必须返回 2: %d", got)
		}
		if got := j3bModelCheckSQLiteBackfillResult(true, true, true, " "); got != 2 {
			t.Fatalf("空 evidence 必须返回 2: %d", got)
		}
		if got := j3bModelCheckSQLiteBackfillResult(true, true, true, missing); got != 2 {
			t.Fatalf("缺失 evidence 必须返回 2: %d", got)
		}
		if got := j3bModelCheckSQLiteBackfillResult(true, true, true, malformed); got != 3 {
			t.Fatalf("malformed evidence 必须 fail-closed 返回 3: %d", got)
		}
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, "")
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH", "")
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH", "")
		if got := j3bModelCheckSQLiteBackfillResult(true, true, true, emptyFacts); got != 2 {
			t.Fatalf("env 缺失必须返回 2: %d", got)
		}
		// 路径隔离：target 与 dataset 不得相同。
		same := filepath.Join(root, "same.db")
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, same)
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH", same)
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH", filepath.Join(root, "stats.db"))
		if got := j3bModelCheckSQLiteBackfillResult(true, true, true, emptyFacts); got != 2 {
			t.Fatalf("target==dataset 必须返回 2: %d", got)
		}
	})
	t.Run("j3b sqlite readback", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, "")
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH", "")
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH", "")
		if got := j3bModelCheckSQLiteReadbackResult(); got != 2 {
			t.Fatalf("env 缺失必须返回 2: %d", got)
		}
	})
	t.Run("j3b inventory", func(t *testing.T) {
		if got := j3bModelCheckInventoryResult(" "); got != 2 {
			t.Fatalf("空路径必须返回 2: %d", got)
		}
		if got := j3bModelCheckInventoryResult(missing); got != 2 {
			t.Fatalf("缺失文件必须返回 2: %d", got)
		}
		// 解码成功但清单为空：inventory 门保持关闭。
		if got := j3bModelCheckInventoryResult(emptyFacts); got != 3 {
			t.Fatalf("空清单必须返回 3: %d", got)
		}
	})
	t.Run("j3b cutover evidence", func(t *testing.T) {
		if got := j3bCutoverEvidenceCheckResult(missing); got != 2 {
			t.Fatalf("缺失文件必须返回 2: %d", got)
		}
		if got := j3bCutoverEvidenceCheckResult(malformed); got != 3 {
			t.Fatalf("malformed 必须 fail-closed 返回 3: %d", got)
		}
	})
	t.Run("business schema check", func(t *testing.T) {
		t.Setenv("JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH", "")
		if got := businessSQLiteSchemaCheckResult("  "); got != 2 {
			t.Fatalf("空 flag 与空 env 必须返回 2: %d", got)
		}
	})
	t.Run("business handoff check", func(t *testing.T) {
		t.Setenv("JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH", "")
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, "")
		if got := businessSQLiteHandoffCheckResult("", ""); got != 2 {
			t.Fatalf("空 flag 与空 env 必须返回 2: %d", got)
		}
	})
	t.Run("j3c boundary on bare root", func(t *testing.T) {
		if got := j3cReadOnlyBoundaryResult(t.TempDir()); got != 1 {
			t.Fatalf("无边界文件的目录必须返回 1: %d", got)
		}
	})
	t.Run("node active path on bare root", func(t *testing.T) {
		if got := nodeJ3bActivePathResult(t.TempDir()); got != 1 {
			t.Fatalf("无源码无归档的目录必须返回 1: %d", got)
		}
	})
	t.Run("gateway route manifest broken path", func(t *testing.T) {
		if got := gatewayRouteOwnerManifestResult(filepath.Join(root, "nope.json"), root); got != 1 {
			t.Fatalf("缺失 manifest 必须返回 1: %d", got)
		}
	})
	t.Run("capability manifest broken path", func(t *testing.T) {
		if got := businessCapabilityManifestResult(filepath.Join(root, "nope-cap.json"), filepath.Join(root, "nope-op.json")); got != 1 {
			t.Fatalf("缺失 manifest 必须返回 1: %d", got)
		}
	})
	t.Run("owner manifest broken path", func(t *testing.T) {
		if got := businessOwnerManifestResult(filepath.Join(root, "nope-owner.json"), root, root, root); got != 1 {
			t.Fatalf("缺失 manifest 必须返回 1: %d", got)
		}
	})
	t.Run("schema snapshot env gates", func(t *testing.T) {
		t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_TARGET", "")
		if got := postgresSchemaSnapshotResult(); got != 2 {
			t.Fatalf("缺失 target 必须返回 2: %d", got)
		}
		t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_TARGET", "test")
		t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL", "")
		if got := postgresSchemaSnapshotResult(); got != 2 {
			t.Fatalf("缺失 URL 必须返回 2: %d", got)
		}
		t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL", "postgres://w14k@127.0.0.1:5432/db")
		t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM", "")
		if got := postgresSchemaSnapshotResult(); got != 2 {
			t.Fatalf("缺失只读确认必须返回 2: %d", got)
		}
		t.Setenv("JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM", "READ_ONLY")
		if got := postgresSchemaSnapshotResult(); got != 1 {
			t.Fatalf("SQLite URL 必须被 openSnapshotDB 拒绝返回 1: %d", got)
		}
	})
}

func TestW14KRunnerRuntimeFailures(t *testing.T) {
	t.Run("go runtime metrics unreachable", func(t *testing.T) {
		if got := goRuntimeMetricsBootstrapResult(false, wm14kUnreachableURL, false, false, false); got != 1 {
			t.Fatalf("不可达主机必须返回 1: %d", got)
		}
	})
	t.Run("j3b pg readback unreachable", func(t *testing.T) {
		if got := j3bModelCheckPostgresReadbackResult(wm14kUnreachableURL, 1); got != 1 {
			t.Fatalf("不可达主机必须返回 1: %d", got)
		}
	})
	t.Run("j3b pg bootstrap unreachable", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.BootstrapEnv, wm14kUnreachableURL)
		if got := j3bModelCheckBootstrapResult(false); got != 1 {
			t.Fatalf("不可达主机必须返回 1: %d", got)
		}
	})
	t.Run("j3a bootstrap unreachable", func(t *testing.T) {
		t.Setenv(j3aproxylatency.BootstrapEnv, wm14kUnreachableURL)
		if got := j3aProxyLatencyBootstrapResult(false); got != 1 {
			t.Fatalf("不可达主机必须返回 1: %d", got)
		}
	})
	t.Run("j3b pg backfill unreachable with ready evidence", func(t *testing.T) {
		evidence := wmWriteCompleteCutoverEvidenceWithManifest(t)
		if got := j3bModelCheckPostgresBackfillResult(wm14kUnreachableURL, 1, 1, true, true, true, evidence); got != 1 {
			t.Fatalf("不可达主机 backfill 必须返回 1: %d", got)
		}
	})
	t.Run("j3b sqlite bootstrap on directory", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, t.TempDir())
		if got := j3bModelCheckSQLiteBootstrapResult(false, false, false, false); got != 1 {
			t.Fatalf("目录路径必须返回 1: %d", got)
		}
	})
	t.Run("j3b sqlite backfill missing dataset", func(t *testing.T) {
		root := t.TempDir()
		target, err := j3bmodelcheck.OpenSQLite(filepath.Join(root, "target.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := target.Close(); err != nil {
			t.Fatal(err)
		}
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, filepath.Join(root, "target.db"))
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH", filepath.Join(root, "missing-dataset.db"))
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH", filepath.Join(root, "missing-stats.db"))
		if got := j3bModelCheckSQLiteBackfillResult(true, true, true, wmWriteCompleteCutoverEvidenceWithManifest(t)); got != 1 {
			t.Fatalf("缺失 dataset 的 backfill 必须返回 1: %d", got)
		}
	})
	t.Run("j3b sqlite readback missing files", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, filepath.Join(root, "missing-target.db"))
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH", filepath.Join(root, "missing-dataset.db"))
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH", filepath.Join(root, "missing-stats.db"))
		if got := j3bModelCheckSQLiteReadbackResult(); got != 1 {
			t.Fatalf("缺失文件的 readback 必须返回 1: %d", got)
		}
	})
	t.Run("business schema check missing file", func(t *testing.T) {
		if got := businessSQLiteSchemaCheckResult(filepath.Join(t.TempDir(), "nope.db")); got != 1 {
			t.Fatalf("缺失文件的 schema 预检必须返回 1: %d", got)
		}
	})
}

func TestW14KRunnerNotReadyGates(t *testing.T) {
	t.Run("j3b sqlite bootstrap empty db not ready", func(t *testing.T) {
		// 0 字节文件是合法空 SQLite 库：check 模式 schema 缺失 → 未就绪门 3。
		path := filepath.Join(t.TempDir(), "empty.db")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, path)
		if got := j3bModelCheckSQLiteBootstrapResult(false, false, false, false); got != 3 {
			t.Fatalf("空库 check 必须返回 3: %d", got)
		}
	})
	t.Run("j3b sqlite readback drifted digest", func(t *testing.T) {
		_, targetPath, datasetPath, statsPath := wmBuildJ3bSQLiteCutoverSet(t)
		ds, err := j3bmodelcheck.OpenSQLite(datasetPath)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ds.ExecContext(context.Background(), `INSERT INTO model_check_items (id,run_id,item_key,item_type,status,score,max_score,evidence_summary_json,created_at,updated_at) VALUES ('w14k-drift-row','w14k-run','w14k-key','latency','ok',1,1,'{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
		if err != nil {
			ds.Close()
			t.Fatalf("插入 drift 行: %v", err)
		}
		if err := ds.Close(); err != nil {
			t.Fatal(err)
		}
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, targetPath)
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH", datasetPath)
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH", statsPath)
		if got := j3bModelCheckSQLiteReadbackResult(); got != 3 {
			t.Fatalf("digest 漂移的 readback 必须返回 3: %d", got)
		}
	})
	t.Run("business handoff missing files not ready", func(t *testing.T) {
		root := t.TempDir()
		if got := businessSQLiteHandoffCheckResult(filepath.Join(root, "missing-b.db"), filepath.Join(root, "missing-j.db")); got != 3 {
			t.Fatalf("缺失文件的 handoff 预检必须 fail-closed 返回 3: %d", got)
		}
	})
}

// TestW14KOutcomeExitCodeHelpers 直调 report 层映射函数，覆盖 err / encode /
// 未就绪 / 成功四类出口（不含 DB 依赖）。
func TestW14KOutcomeExitCodeHelpers(t *testing.T) {
	t.Run("go runtime metrics", func(t *testing.T) {
		if got := goRuntimeMetricsOutcomeExitCode(goruntimemetrics.Report{}, context.DeadlineExceeded); got != 1 {
			t.Fatalf("err 必须返回 1: %d", got)
		}
		wm14kWithClosedStdout(t, func() {
			if got := goRuntimeMetricsOutcomeExitCode(goruntimemetrics.Report{}, nil); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
		if got := goRuntimeMetricsOutcomeExitCode(goruntimemetrics.Report{MissingTables: []string{"w14k-table"}}, nil); got != 3 {
			t.Fatalf("未就绪必须返回 3: %d", got)
		}
		if got := goRuntimeMetricsOutcomeExitCode(goruntimemetrics.Report{}, nil); got != 0 {
			t.Fatalf("就绪必须返回 0: %d", got)
		}
	})
	t.Run("j3b pg bootstrap", func(t *testing.T) {
		if got := j3bBootstrapOutcomeExitCode(j3bmodelcheck.Report{}, context.DeadlineExceeded); got != 1 {
			t.Fatalf("err 必须返回 1: %d", got)
		}
		wm14kWithClosedStdout(t, func() {
			if got := j3bBootstrapOutcomeExitCode(j3bmodelcheck.Report{}, nil); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
		if got := j3bBootstrapOutcomeExitCode(j3bmodelcheck.Report{MissingSchema: true}, nil); got != 3 {
			t.Fatalf("未就绪必须返回 3: %d", got)
		}
		if got := j3bBootstrapOutcomeExitCode(j3bmodelcheck.Report{}, nil); got != 0 {
			t.Fatalf("就绪必须返回 0: %d", got)
		}
	})
	t.Run("j3a bootstrap", func(t *testing.T) {
		if got := j3aBootstrapOutcomeExitCode(j3aproxylatency.Report{}, context.DeadlineExceeded); got != 1 {
			t.Fatalf("err 必须返回 1: %d", got)
		}
		wm14kWithClosedStdout(t, func() {
			if got := j3aBootstrapOutcomeExitCode(j3aproxylatency.Report{}, nil); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
		if got := j3aBootstrapOutcomeExitCode(j3aproxylatency.Report{MissingTables: []string{"w14k"}}, nil); got != 3 {
			t.Fatalf("未就绪必须返回 3: %d", got)
		}
		if got := j3aBootstrapOutcomeExitCode(j3aproxylatency.Report{}, nil); got != 0 {
			t.Fatalf("就绪必须返回 0: %d", got)
		}
	})
	t.Run("j3b sqlite bootstrap", func(t *testing.T) {
		if got := j3bSQLiteBootstrapOutcomeExitCode(j3bmodelcheck.SQLiteReport{}, context.DeadlineExceeded); got != 1 {
			t.Fatalf("err 必须返回 1: %d", got)
		}
		wm14kWithClosedStdout(t, func() {
			if got := j3bSQLiteBootstrapOutcomeExitCode(j3bmodelcheck.SQLiteReport{}, nil); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
		if got := j3bSQLiteBootstrapOutcomeExitCode(j3bmodelcheck.SQLiteReport{MissingTables: []string{"w14k"}}, nil); got != 3 {
			t.Fatalf("未就绪必须返回 3: %d", got)
		}
		if got := j3bSQLiteBootstrapOutcomeExitCode(j3bmodelcheck.SQLiteReport{}, nil); got != 0 {
			t.Fatalf("就绪必须返回 0: %d", got)
		}
	})
	t.Run("j3b pg backfill", func(t *testing.T) {
		if got := j3bPostgresBackfillOutcomeExitCode(j3bmodelcheck.PostgresBackfillReport{}, context.DeadlineExceeded); got != 1 {
			t.Fatalf("err 必须返回 1: %d", got)
		}
		wm14kWithClosedStdout(t, func() {
			if got := j3bPostgresBackfillOutcomeExitCode(j3bmodelcheck.PostgresBackfillReport{}, nil); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
		if got := j3bPostgresBackfillOutcomeExitCode(j3bmodelcheck.PostgresBackfillReport{}, nil); got != 0 {
			t.Fatalf("成功必须返回 0: %d", got)
		}
	})
	t.Run("j3b sqlite backfill", func(t *testing.T) {
		if got := j3bSQLiteBackfillOutcomeExitCode(j3bmodelcheck.BackfillReport{}, context.DeadlineExceeded); got != 1 {
			t.Fatalf("err 必须返回 1: %d", got)
		}
		wm14kWithClosedStdout(t, func() {
			if got := j3bSQLiteBackfillOutcomeExitCode(j3bmodelcheck.BackfillReport{}, nil); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
		if got := j3bSQLiteBackfillOutcomeExitCode(j3bmodelcheck.BackfillReport{}, nil); got != 0 {
			t.Fatalf("成功必须返回 0: %d", got)
		}
	})
	t.Run("j3b pg readback", func(t *testing.T) {
		if got := j3bPostgresReadbackOutcomeExitCode(j3bmodelcheck.PostgresBackfillVerificationReport{}, context.DeadlineExceeded); got != 1 {
			t.Fatalf("err 必须返回 1: %d", got)
		}
		wm14kWithClosedStdout(t, func() {
			if got := j3bPostgresReadbackOutcomeExitCode(j3bmodelcheck.PostgresBackfillVerificationReport{Ready: true}, nil); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
		if got := j3bPostgresReadbackOutcomeExitCode(j3bmodelcheck.PostgresBackfillVerificationReport{}, nil); got != 3 {
			t.Fatalf("未就绪必须返回 3: %d", got)
		}
		if got := j3bPostgresReadbackOutcomeExitCode(j3bmodelcheck.PostgresBackfillVerificationReport{Ready: true}, nil); got != 0 {
			t.Fatalf("就绪必须返回 0: %d", got)
		}
	})
}

// TestW14KRunnerEncodeFailureOnRealReports 对依赖仓库状态/真实库文件的
// runner，用关闭的 stdout 覆盖其 encode 错误分支。
func TestW14KRunnerEncodeFailureOnRealReports(t *testing.T) {
	t.Run("inventory", func(t *testing.T) {
		path := wm14kWriteCompleteInventoryEvidence(t)
		wm14kWithClosedStdout(t, func() {
			if got := j3bModelCheckInventoryResult(path); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
	})
	t.Run("cutover evidence", func(t *testing.T) {
		path := wmWriteCompleteCutoverEvidenceWithManifest(t)
		wm14kWithClosedStdout(t, func() {
			if got := j3bCutoverEvidenceCheckResult(path); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
	})
	t.Run("business schema check", func(t *testing.T) {
		path := wm14kReadyBusinessSQLite(t)
		wm14kWithClosedStdout(t, func() {
			if got := businessSQLiteSchemaCheckResult(path); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
	})
	t.Run("business handoff check", func(t *testing.T) {
		business := wm14kReadyBusinessSQLite(t)
		j3b := wm14kReadyJ3bSQLite(t)
		wm14kWithClosedStdout(t, func() {
			if got := businessSQLiteHandoffCheckResult(business, j3b); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
	})
	t.Run("gateway route manifest", func(t *testing.T) {
		wm14kWithClosedStdout(t, func() {
			if got := gatewayRouteOwnerManifestResult(
				resolveRepoPath("docs/migration/GatewayManagementRouteOwnerManifest.json"),
				resolveRepositoryRoot()); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
	})
	t.Run("capability manifest", func(t *testing.T) {
		wm14kWithClosedStdout(t, func() {
			if got := businessCapabilityManifestResult(
				resolveRepoPath("docs/migration/GoBusinessCapabilityManifest.json"),
				resolveRepoPath("docs/migration/BusinessSQLite-owner-manifest.json")); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
	})
	t.Run("owner manifest", func(t *testing.T) {
		typesPath := resolveRepoPath(filepath.Join(archivedDBServiceSourceRoot, "db-service-types.ts"))
		accessPath := resolveRepoPath(filepath.Join(archivedDBServiceSourceRoot, "db-service-operation-access-mode.ts"))
		handlerPath := resolveRepoPath(filepath.Join(archivedDBServiceSourceRoot, "db-service-handlers.ts"))
		if _, err := os.Stat(typesPath); err != nil {
			t.Skip("migration-backup-1 墓地契约源缺席")
		}
		wm14kWithClosedStdout(t, func() {
			if got := businessOwnerManifestResult(resolveRepoPath("docs/migration/BusinessSQLite-owner-manifest.json"), typesPath, accessPath, handlerPath); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
	})
	t.Run("node active path", func(t *testing.T) {
		wm14kWithClosedStdout(t, func() {
			if got := nodeJ3bActivePathResult(resolveRepositoryRoot()); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
	})
	t.Run("j3c boundary", func(t *testing.T) {
		wm14kWithClosedStdout(t, func() {
			if got := j3cReadOnlyBoundaryResult(resolveRepositoryRoot()); got != 1 {
				t.Fatalf("encode 失败必须返回 1: %d", got)
			}
		})
	})
}

// TestW14KRepoStateRunnerResults 断言仓库状态相关 runner 的当前真实出口
// （与 wm_main_exit_branches_test.go 的子进程断言同源）。
func TestW14KRepoStateRunnerResults(t *testing.T) {
	t.Run("gateway route manifest gate closed", func(t *testing.T) {
		if got := gatewayRouteOwnerManifestResult(
			resolveRepoPath("docs/migration/GatewayManagementRouteOwnerManifest.json"),
			resolveRepositoryRoot()); got != 3 {
			t.Fatalf("当前仓库 gateway 路由清单门应保持关闭: %d", got)
		}
	})
	t.Run("capability manifest gate closed", func(t *testing.T) {
		if got := businessCapabilityManifestResult(
			resolveRepoPath("docs/migration/GoBusinessCapabilityManifest.json"),
			resolveRepoPath("docs/migration/BusinessSQLite-owner-manifest.json")); got != 3 {
			t.Fatalf("当前仓库 capability 门应保持关闭: %d", got)
		}
	})
	t.Run("node active path clean", func(t *testing.T) {
		if got := nodeJ3bActivePathResult(resolveRepositoryRoot()); got != 0 {
			t.Fatalf("归档后仓库 active-path 应为 0: %d", got)
		}
	})
	t.Run("j3c boundary clean", func(t *testing.T) {
		if got := j3cReadOnlyBoundaryResult(resolveRepositoryRoot()); got != 0 {
			t.Fatalf("当前仓库 j3c 边界应为 0: %d", got)
		}
	})
}

// wm14kWriteCompleteInventoryEvidence 生成让 inventory 门放行的完整证据。
func wm14kWriteCompleteInventoryEvidence(t *testing.T) string {
	t.Helper()
	evidence := make(map[string]j3bmodelcheck.LegacyJ3bFactEvidence, len(j3bmodelcheck.LegacyJ3bFactInventory))
	for _, item := range j3bmodelcheck.LegacyJ3bFactInventory {
		evidence[item.Name] = j3bmodelcheck.LegacyJ3bFactEvidence{
			SourceSchema: item.SourceSchema, SourceTable: item.SourceTable, Scope: item.Scope,
			Digest:                   "sha256:" + strings.Repeat("a", 64),
			BackfillReadbackVerified: item.Disposition == j3bmodelcheck.LegacyFactBackfill,
			RetentionVerified:        item.Disposition == j3bmodelcheck.LegacyFactRetain,
		}
	}
	data, err := json.Marshal(j3bmodelcheck.LegacyJ3bFactEvidenceDocument{Facts: evidence})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "inventory-evidence.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// wm14kReadyBusinessSQLite 构造满足 handoff 契约的 Business SQLite 文件。
func wm14kReadyBusinessSQLite(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := bootstrap.OpenSQLiteFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := schema.EnsureSQLiteBusiness(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return path
}

// wm14kReadyJ3bSQLite 构造契约就绪的 J3b SQLite 文件。
func wm14kReadyJ3bSQLite(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "j3b.db")
	db, err := j3bmodelcheck.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := j3bmodelcheck.RunSQLite(context.Background(), db, true); err != nil {
		t.Fatal(err)
	}
	return path
}
