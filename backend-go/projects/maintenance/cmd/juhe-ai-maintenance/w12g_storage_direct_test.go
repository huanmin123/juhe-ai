package main

// w12g 波次：runStorageBootstrap 的 PostgreSQL/SQLite 失败返回码路径直调，
// 以及 main() 中安全只读检查分支的进程内直调。
// 不可达（w12g 登记，传统 go test -cover 口径）：main.go 与各 runner 的
// os.Exit 出口分支只能由子进程重执行覆盖（wm_main_exit_branches_test.go），
// 该模式的覆盖数据在 -coverprofile 口径下不计入父进程统计，但行为已由
// exit-code 断言与 GOCOVERDIR 二进制覆盖模式验证。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestW12GRunStorageBootstrapPostgresFailures(t *testing.T) {
	t.Run("postgres dsn without scheme", func(t *testing.T) {
		code := runStorageBootstrap(true, false, "postgres", "", "plain-not-a-url", "")
		if code != 2 {
			t.Fatalf("非 postgres URL 的 dsn 必须返回 2: %d", code)
		}
	})

	t.Run("postgres ensure on unreachable host", func(t *testing.T) {
		code := runStorageBootstrap(true, false, "postgres", "", "postgres://wm@127.0.0.1:1/no-such-db", "")
		if code != 1 {
			t.Fatalf("不可达主机 ensure 必须返回 1: %d", code)
		}
	})

	t.Run("postgres seed on unreachable host", func(t *testing.T) {
		code := runStorageBootstrap(false, true, "postgres", "", "postgres://wm@127.0.0.1:1/no-such-db", "")
		if code != 1 {
			t.Fatalf("不可达主机 seed 必须返回 1: %d", code)
		}
	})

	t.Run("postgres seed with malformed url", func(t *testing.T) {
		code := runStorageBootstrap(false, true, "postgres", "", "postgresql://@/", "")
		if code != 1 && code != 2 {
			t.Fatalf("畸形 URL 必须返回 1 或 2: %d", code)
		}
	})
}

func TestW12GRunStorageBootstrapSQLiteFailures(t *testing.T) {
	root := t.TempDir()

	pathsFor := func(business string) string {
		return strings.Join([]string{
			"business=" + business,
			"chat=" + filepath.Join(root, "chat.db"),
			"dataset=" + filepath.Join(root, "dataset.db"),
			"usage-catalog=" + filepath.Join(root, "usage-catalog.db"),
			"stats=" + filepath.Join(root, "stats.db"),
			"codex-context-shard-root=" + filepath.Join(root, "shards"),
		}, ",")
	}

	t.Run("ensure fails when business path is a directory", func(t *testing.T) {
		dir := filepath.Join(root, "as-dir")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		code := runStorageBootstrap(true, false, "sqlite", pathsFor(dir), "", "")
		if code != 1 {
			t.Fatalf("目录 business 路径 ensure 必须返回 1: %d", code)
		}
	})

	t.Run("seed fails when business path is a directory", func(t *testing.T) {
		dir := filepath.Join(root, "as-dir-2")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		code := runStorageBootstrap(false, true, "sqlite", pathsFor(dir), "", "")
		if code != 1 {
			t.Fatalf("目录 business 路径 seed 必须返回 1: %d", code)
		}
	})
}

func TestW12GMainDirectReadOnlyCheckBranches(t *testing.T) {
	// 仓库状态使这些只读检查在当前目录状态下是真实成功分支（进程内 return，
	// 不触发 os.Exit），可直接直调覆盖 main() 的分发分支。
	t.Run("owner manifest check", func(t *testing.T) {
		output := wmCallMainWithFreshFlags(t, []string{"-verify-business-owner-manifest"})
		if strings.Contains(output, "verification failed") {
			t.Fatalf("owner manifest 检查不应失败: %q", output)
		}
	})

	t.Run("node active path scan", func(t *testing.T) {
		output := wmCallMainWithFreshFlags(t, []string{"-scan-node-j3b-active-path"})
		if strings.Contains(output, "scan failed") {
			t.Fatalf("active path 扫描不应失败: %q", output)
		}
	})

	t.Run("j3c readonly boundary", func(t *testing.T) {
		output := wmCallMainWithFreshFlags(t, []string{"-verify-j3c-readonly-boundary"})
		if strings.Contains(output, "verification failed") {
			t.Fatalf("j3c 边界检查不应失败: %q", output)
		}
	})
}
