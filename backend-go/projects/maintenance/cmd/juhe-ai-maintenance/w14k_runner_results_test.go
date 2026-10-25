package main

// w14k 波次：main.go 各 runner 抽出的 result 函数的进程内覆盖。runner 原主体
// 曾以 os.Exit 终止；os.Exit 包装已全部删除（os.Exit→return 收敛，出口契约由
// runMaintenance 统一承载，测试单进程纪律禁止 exec 子进程重执行），本文件直调
// 等价的 *Result / *OutcomeExitCode 函数，覆盖 usage 预检、Open 拒绝等分支。
// J3b SQLite bootstrap 臂与 parseSQLiteStoragePaths 随 SQLite 存储形态退役删除
// （PLAN-20261024T051925945Z B4）。
//
// 结构性限制登记（迁移期 runner 删除后同步收敛）：
//  1. main() 的非零 os.Exit 行（main.go main() 内唯一残留）：仅当二进制以非
//     零码退出时执行；出口行为已由 wm_main_exit_branches_test.go 进程内直调
//     runMaintenance 断言返回码覆盖，传统 -coverprofile 口径对该行无计数。
//  2. database/sql 懒连接使 sql.Open 的错误分支不可达（openSnapshotDB /
//     openPostgresBootstrap）：pgx 驱动把 DSN 解析推迟到首个语句，Open 仅在
//     未知驱动时失败，属 API 契约守卫，按 w12g sql.Open 懒注册同口径保留不删。
// 另登记上游缺陷（不属本改动范围）：internal/schemasnapshot CollectSnapshot
// 的 $1::text[] 查询调用 collectRows 时缺 schemaNames 实参，真实 PG 上
// --postgres-schema-snapshot 必失败；详见 w14k_pg_gate_test.go 注释。

import (
	"os"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3aproxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
)

// wm14kWithClosedStdout 用已关闭的管道替换 os.Stdout，使 json 编码写入必然
// 失败，从而覆盖各 report 层的 encode 错误分支（w1/w14k 存储链路测试共用）。
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

func TestW14KRunnerUsageGates(t *testing.T) {
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
}
