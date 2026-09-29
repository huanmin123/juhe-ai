package main

// w12g 波次：runStorageBootstrap 的 PostgreSQL/SQLite 失败返回码路径直调。
// 迁移期只读检查分支（owner manifest/active path/j3c 边界）已随命令族退役删除。
// 不可达（w12g 登记，传统 go test -cover 口径）：runMaintenance 的非零出口
// 分支行为已由 wm_main_exit_branches_test.go 以 runMaintenance 进程内直调
// 断言返回码覆盖（测试单进程纪律，禁止 exec 子进程）；-coverprofile 语句
// 计数口径对该形态的统计限制见 w14k_runner_results_test.go 头注释。

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
