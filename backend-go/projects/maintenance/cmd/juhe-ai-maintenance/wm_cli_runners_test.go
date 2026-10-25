package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// 本文件在同包内直调 CLI 运行层：main() 的 flag/版本分支、runStorageBootstrap
// 的 PostgreSQL 用法矩阵。全部经 *Result 函数（返回退出码）进程内直调并断言
// 返回码；os.Exit 包装已删除，失败/未就绪出口由 wm_main_exit_branches_test.go
// 经 runMaintenance 直调覆盖（测试单进程纪律：一次 go test 只允许一个测试
// 进程，禁止 exec 子进程）。SQLite 驱动矩阵与 J3b SQLite bootstrap runner 随
// SQLite 存储形态退役删除（PLAN-20261024T051925945Z B4）。

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

func TestWMRunStorageBootstrapUsageMatrix(t *testing.T) {
	usageExitCode := func(name string, ensure, seed bool, dsn string, want int) {
		t.Run(name, func(t *testing.T) {
			if code := runStorageBootstrap(ensure, seed, dsn, ""); code != want {
				t.Fatalf("exit=%d want %d", code, want)
			}
		})
	}
	usageExitCode("missing dsn", true, false, "", 2)
	usageExitCode("dsn wrong scheme", true, false, "sqlite:///x.db", 2)
	usageExitCode("seed without dsn", false, true, "", 2)

	t.Run("postgres unreachable dsn returns 1", func(t *testing.T) {
		// sql.Open 是懒连接：前缀校验通过后 DSN 故障在首个语句（ensure）时暴露，
		// 按契约返回运行时失败 1 而非用法错误 2。
		if code := runStorageBootstrap(true, false, "postgres://wm@127.0.0.1:1/wm_none", ""); code != 1 {
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
