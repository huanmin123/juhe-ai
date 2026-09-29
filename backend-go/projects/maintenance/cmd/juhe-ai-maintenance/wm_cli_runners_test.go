package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
)

// 本文件在同包内直调 CLI 运行层：main() 的 flag/版本分支、runStorageBootstrap
// 的 SQLite/Postgres 驱动矩阵、只读检查 runner 的成功路径与退出码 helper。
// 全部经 *Result 函数（返回退出码）进程内直调并断言返回码；os.Exit 包装已删除，
// 失败/未就绪出口由 wm_main_exit_branches_test.go 经 runMaintenance 直调覆盖
// （测试单进程纪律：一次 go test 只允许一个测试进程，禁止 exec 子进程）。
// 依赖真实 PostgreSQL 的 runner（PG bootstrap、schema snapshot 顶层入口）仍不
// 在直调范围内；Node→Go 迁移期 runner（manifest/handoff/J3a/J3b backfill/
// readback/cutover）已随命令族退役删除，J3a/J3b bootstrap apply 命令为现行
// 生产初始化契约，其 runner 覆盖保留/恢复。

// wmCaptureStdout 捕获 fn 期间写入 os.Stdout 的内容。
func wmCaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original }()
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- string(data)
	}()
	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	return <-done
}

// wmCaptureRunnerOutput 捕获 runner（*Result 函数）的 stdout 并断言返回码为
// 0；返回码检查放在捕获结束之后，避免 t.Fatalf 在捕获闭包内中断清理。
func wmCaptureRunnerOutput(t *testing.T, name string, fn func() int) string {
	t.Helper()
	var code int
	output := wmCaptureStdout(t, func() { code = fn() })
	if code != 0 {
		t.Fatalf("%s exit=%d, want 0", name, code)
	}
	return output
}

// wmCallRunMaintenance 进程内直调 runMaintenance（main 的同参异体：main 仅多
// 一层非零 os.Exit），断言出口 0 并返回 stdout。FlagSet 由 runMaintenance 每次
// 新建（ContinueOnError），无需替换 flag.CommandLine 或 os.Args。
func wmCallRunMaintenance(t *testing.T, args []string) string {
	t.Helper()
	return wmCaptureRunnerOutput(t, "runMaintenance", func() int {
		return runMaintenance(args)
	})
}

func TestWMMainVersionAndBoundaryBranches(t *testing.T) {
	t.Run("version", func(t *testing.T) {
		output := wmCallRunMaintenance(t, []string{"-version"})
		if !strings.Contains(output, "juhe-ai-maintenance project=") || !strings.Contains(output, "contract=") {
			t.Fatalf("--version 输出必须包含项目与契约版本: %q", output)
		}
	})
	t.Run("check boundary", func(t *testing.T) {
		output := wmCallRunMaintenance(t, []string{"-check-boundary"})
		if !strings.Contains(output, "boundary=ready") {
			t.Fatalf("--check-boundary 输出异常: %q", output)
		}
	})
}

func TestWMRunStorageBootstrapDriverMatrix(t *testing.T) {
	root := t.TempDir()
	paths := "business=" + filepath.Join(root, "business.sqlite3") +
		",chat=" + filepath.Join(root, "chat.sqlite3") +
		",dataset=" + filepath.Join(root, "dataset.sqlite3") +
		",usage-catalog=" + filepath.Join(root, "usage.sqlite3") +
		",stats=" + filepath.Join(root, "stats.sqlite3") +
		",codex-context-shard-root=" + filepath.Join(root, "shards")

	t.Run("full sqlite ensure and seed", func(t *testing.T) {
		output := wmCaptureStdout(t, func() {
			if code := runStorageBootstrap(true, true, "sqlite", paths+",codex-context-shard-count=2", "", ""); code != 0 {
				t.Fatalf("runStorageBootstrap exit=%d", code)
			}
		})
		var report storageBootstrapReport
		if err := json.Unmarshal([]byte(output), &report); err != nil {
			t.Fatalf("报告必须可解码: %v\n%s", err, output)
		}
		if !report.EnsureRan || !report.SeedRan || report.Driver != "sqlite" {
			t.Fatalf("报告驱动矩阵错误: %+v", report)
		}
		// ensureSQLiteStorage 的键：business、stats、chat、2 个 codex shard、
		// dataset、usage-catalog。
		if len(report.SQLite.Ensure) != 7 || report.SQLite.Seed == nil || report.SQLite.Seed.ModelCatalogRows <= 0 {
			t.Fatalf("六库 ensure + business seed 语义缺失: %+v", report.SQLite)
		}
		// 每个库都真实落盘。
		for _, name := range []string{"business.sqlite3", "chat.sqlite3", "dataset.sqlite3", "usage.sqlite3", "stats.sqlite3", "shards/state-000.sqlite3", "shards/state-001.sqlite3"} {
			if _, err := os.Stat(filepath.Join(root, name)); err != nil {
				t.Fatalf("库文件 %s 应存在: %v", name, err)
			}
		}
	})

	usageExitCode := func(name string, ensure, seed bool, driverName, pathsArg, dsn string, want int) {
		t.Run(name, func(t *testing.T) {
			if code := runStorageBootstrap(ensure, seed, driverName, pathsArg, dsn, ""); code != want {
				t.Fatalf("exit=%d want %d", code, want)
			}
		})
	}
	usageExitCode("invalid driver", true, false, "mysql", "", "", 2)
	usageExitCode("sqlite with dsn", true, false, "sqlite", "business=x", "postgres://u@h/db", 2)
	usageExitCode("postgres with paths", true, false, "postgres", "business=x", "", 2)
	usageExitCode("bad paths entry", true, false, "sqlite", "business", "", 2)
	usageExitCode("missing required key", true, false, "sqlite", "business=x", "", 2)
	usageExitCode("unknown paths key", true, false, "sqlite", "business=x,unknown=y", "", 2)
	usageExitCode("bad shard count", true, false, "sqlite", "business=x,codex-context-shard-count=0", "", 2)
	usageExitCode("postgres without dsn", true, false, "postgres", "", "  ", 2)
	usageExitCode("postgres dsn wrong scheme", true, false, "postgres", "", "sqlite:///x.db", 2)
	usageExitCode("seed without ensure", false, true, "postgres", "", "", 2)

	t.Run("codex shard count bounds", func(t *testing.T) {
		for _, raw := range []string{"codex-context-shard-count=0", "codex-context-shard-count=257", "codex-context-shard-count=x"} {
			if _, err := parseSQLiteStoragePaths(paths + "," + raw); err == nil {
				t.Fatalf("shard 计数 %q 必须被拒绝", raw)
			}
		}
	})

	t.Run("sqlite ensure failure returns 1", func(t *testing.T) {
		// business 指向一个目录：路径解析通过但 ensure 打不开文件。
		brokenPaths := "business=" + root +
			",chat=" + filepath.Join(root, "chat.sqlite3") +
			",dataset=" + filepath.Join(root, "dataset.sqlite3") +
			",usage-catalog=" + filepath.Join(root, "usage.sqlite3") +
			",stats=" + filepath.Join(root, "stats.sqlite3") +
			",codex-context-shard-root=" + filepath.Join(root, "shards2")
		if code := runStorageBootstrap(true, false, "sqlite", brokenPaths, "", ""); code != 1 {
			t.Fatalf("ensure 失败必须返回 1: %d", code)
		}
	})

	t.Run("postgres unreachable dsn returns 1", func(t *testing.T) {
		// sql.Open 是懒连接：前缀校验通过后 DSN 故障在首个语句（ensure）时暴露，
		// 按契约返回运行时失败 1 而非用法错误 2。
		if code := runStorageBootstrap(true, false, "postgres", "", "postgres://wm@127.0.0.1:1/wm_none", ""); code != 1 {
			t.Fatalf("连接失败必须返回 1: %d", code)
		}
	})
}

func TestWMSeedSecretValueResolvesFlagThenEnv(t *testing.T) {
	t.Setenv("JUHE_AI_SECRET", "")
	if got := seedSecretValue(" flag "); got != "flag" {
		t.Fatalf("flag 值必须去空白并优先: %q", got)
	}
	if got := seedSecretValue(""); got != "" {
		t.Fatalf("无 flag 无 env 必须为空（Node dev 默认在 schema 内部选择）: %q", got)
	}
	t.Setenv("JUHE_AI_SECRET", " from-env ")
	if got := seedSecretValue(""); got != "from-env" {
		t.Fatalf("env 必须去空白生效: %q", got)
	}
}

// TestWMRunJ3bSQLiteBootstrapRunner 覆盖 --apply-j3b-model-check-sqlite 的
// runner 成功路径：check 模式在已就绪 schema 上直接返回 0；apply 模式经完整
// 确认后在专属路径创建文件并输出报告（对照基线语义恢复）。
func TestWMRunJ3bSQLiteBootstrapRunner(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	checkPath := filepath.Join(root, "check.db")
	applyPath := filepath.Join(root, "apply.db")

	// check 模式在 schema 已就绪的文件上应直接返回（不触发 exit 3）。
	db, err := j3bmodelcheck.OpenSQLite(checkPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j3bmodelcheck.RunSQLite(ctx, db, true); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Run("check mode returns on ready schema", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, checkPath)
		wmCaptureRunnerOutput(t, "j3bModelCheckSQLiteBootstrapResult", func() int {
			return j3bModelCheckSQLiteBootstrapResult(false, false, false, false)
		})
	})
	t.Run("apply mode requires confirmations handled upstream", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, applyPath)
		wmCaptureRunnerOutput(t, "j3bModelCheckSQLiteBootstrapResult", func() int {
			return j3bModelCheckSQLiteBootstrapResult(true, true, true, true)
		})
		if _, err := os.Stat(applyPath); err != nil {
			t.Fatalf("apply 应创建专属文件: %v", err)
		}
	})
}
