package main

// w14k 波次：main.go 各 runner 抽出的 result 函数的进程内覆盖。runner 原主体
// 曾以 os.Exit 终止；os.Exit 包装已全部删除（os.Exit→return 收敛，出口契约由
// runMaintenance 统一承载，测试单进程纪律禁止 exec 子进程重执行），本文件直调
// 等价的 *Result / *OutcomeExitCode 函数，覆盖 usage 预检、Open 拒绝、运行时
// 失败、encode 失败、未就绪门与成功路径的全部语句。Node→Go 迁移期命令族
// （J3a/J3b backfill/readback、cutover/inventory evidence、manifest/
// handoff/j3c/active-path 检查、hybrid_smart 迁移）退役后，本文件只保留现行
// runner 的覆盖（含 J3a/J3b bootstrap apply 命令的用法门）。
//
// 结构性限制登记（迁移期 runner 删除后同步收敛）：
//  1. main() 的非零 os.Exit 行（main.go main() 内唯一残留）：仅当二进制以非
//     零码退出时执行；出口行为已由 wm_main_exit_branches_test.go 进程内直调
//     runMaintenance 断言返回码覆盖，传统 -coverprofile 口径对该行无计数。
//  2. database/sql 懒连接使 sql.Open 的错误分支不可达（openSnapshotDB /
//     openPostgresBootstrap）：pgx 驱动把 DSN 解析推迟到首个语句，Open 仅在
//     未知驱动时失败，属 API 契约守卫，按 w12g sql.Open 懒注册同口径保留不删。
//  3. runStorageBootstrap 的 postgres ensure/seed 成功后 report 赋值行（需真实
//     可写 PG，共享库禁止 schema 变更）、ensureSQLiteStorage 闭包的 apply err
//     包装（需“可打开但写入失败”的 SQLite 文件，Windows 只读属性仍导致打开
//     失败）。
// 另登记上游缺陷（不属本改动范围）：internal/schemasnapshot CollectSnapshot
// 的 $1::text[] 查询调用 collectRows 时缺 schemaNames 实参，真实 PG 上
// --postgres-schema-snapshot 必失败；详见 w14k_pg_gate_test.go 注释。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/goruntimemetrics"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3aproxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
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
	// J3a/J3b 一次性 bootstrap apply 命令（现行生产初始化契约）的用法门：
	// 空 env、缺主机 URL 与 apply 无确认都必须 fail-closed 返回 2。
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
}

func TestW14KRunnerRuntimeFailures(t *testing.T) {
	t.Run("go runtime metrics unreachable", func(t *testing.T) {
		if got := goRuntimeMetricsBootstrapResult(false, wm14kUnreachableURL, false, false, false); got != 1 {
			t.Fatalf("不可达主机必须返回 1: %d", got)
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
}

func TestW14KRunnerRemainingBranches(t *testing.T) {
	t.Run("parse paths tolerates empty entries", func(t *testing.T) {
		// 尾随/连续逗号产生空条目，必须被跳过而不是报“key=value 形式”错；
		// 其余必填 key 齐全时解析成功。
		paths, err := parseSQLiteStoragePaths("business=a,,chat=b,dataset=c,usage-catalog=d,stats=e,codex-context-shard-root=f,")
		if err != nil {
			t.Fatalf("空条目必须被跳过: %v", err)
		}
		if paths.Business != "a" || paths.Chat != "b" {
			t.Fatalf("空条目两侧的键值必须正常解析: %+v", paths)
		}
	})
}
