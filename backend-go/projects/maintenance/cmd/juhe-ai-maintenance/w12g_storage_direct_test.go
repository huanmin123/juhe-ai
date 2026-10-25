package main

// w12g 波次：runStorageBootstrap 的 PostgreSQL 失败返回码路径直调。
// SQLite 臂随 SQLite 存储形态退役删除（PLAN-20261024T051925945Z B4）。
// 不可达（w12g 登记，传统 go test -cover 口径）：runMaintenance 的非零出口
// 分支行为已由 wm_main_exit_branches_test.go 以 runMaintenance 进程内直调
// 断言返回码覆盖（测试单进程纪律，禁止 exec 子进程）；-coverprofile 语句
// 计数口径对该形态的统计限制见 w14k_runner_results_test.go 头注释。

import "testing"

func TestW12GRunStorageBootstrapPostgresFailures(t *testing.T) {
	t.Run("postgres dsn without scheme", func(t *testing.T) {
		code := runStorageBootstrap(true, false, "plain-not-a-url", "")
		if code != 2 {
			t.Fatalf("非 postgres URL 的 dsn 必须返回 2: %d", code)
		}
	})

	t.Run("postgres ensure on unreachable host", func(t *testing.T) {
		code := runStorageBootstrap(true, false, "postgres://wm@127.0.0.1:1/no-such-db", "")
		if code != 1 {
			t.Fatalf("不可达主机 ensure 必须返回 1: %d", code)
		}
	})

	t.Run("postgres seed on unreachable host", func(t *testing.T) {
		code := runStorageBootstrap(false, true, "postgres://wm@127.0.0.1:1/no-such-db", "")
		if code != 1 {
			t.Fatalf("不可达主机 seed 必须返回 1: %d", code)
		}
	})

	t.Run("postgres seed with malformed url", func(t *testing.T) {
		code := runStorageBootstrap(false, true, "postgresql://@/", "")
		if code != 1 && code != 2 {
			t.Fatalf("畸形 URL 必须返回 1 或 2: %d", code)
		}
	})
}
