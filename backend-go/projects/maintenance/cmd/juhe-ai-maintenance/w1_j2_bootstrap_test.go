package main

// w1 J2 account-balance 外部预置命令（--apply-account-balance-postgres）的
// 进程内覆盖，风格对齐 w14k_runner_results_test.go：用法门（env 缺失、URL
// 形状）、与其它 maintenance 命令的互斥链（runMaintenance 直调）、outcome
// 出口映射与不可达主机的运行时失败。除不可达负臂外不连接真实 PG。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// w1J2UnreachableURL 形状合法（有主机/库/角色）但主机不可达的维护 DSN。
const w1J2UnreachableURL = "postgres://w1j2@127.0.0.1:1/w1j2-none"

func TestW1J2BootstrapUsageGates(t *testing.T) {
	t.Run("env missing", func(t *testing.T) {
		t.Setenv(accountBalanceBootstrapEnv, "")
		if got := accountBalanceBootstrapResult(); got != 2 {
			t.Fatalf("空 env 必须返回 2: %d", got)
		}
	})
	t.Run("url shape", func(t *testing.T) {
		t.Setenv(accountBalanceBootstrapEnv, "postgres://@/")
		if got := accountBalanceBootstrapResult(); got != 2 {
			t.Fatalf("缺主机 URL 必须返回 2: %d", got)
		}
		t.Setenv(accountBalanceBootstrapEnv, "sqlite://w1j2/db")
		if got := accountBalanceBootstrapResult(); got != 2 {
			t.Fatalf("非 postgres scheme 必须返回 2: %d", got)
		}
	})
}

// TestW1J2BootstrapMutualExclusion 逐链验证新 flag 与每个既有命令分支的
// 互斥：任一组合都必须在上游互斥链处 fail-closed 返回 2，而不是进入任一
// 命令主体。
func TestW1J2BootstrapMutualExclusion(t *testing.T) {
	// env 先置为形状合法但主机不可达的 URL（:1 保证连接拒绝，见
	// w1J2UnreachableURL）：既避免缺 env 的 2 掩盖互斥断言，也保证末尾
	// "单独使用"负臂不会在存在本机 PG 的环境里真实执行 DDL。
	t.Setenv(accountBalanceBootstrapEnv, w1J2UnreachableURL)
	for _, argv := range [][]string{
		{"--apply-account-balance-postgres", "--migrate-chat-account-only-binding"},
		{"--apply-account-balance-postgres", "--postgres-schema-snapshot"},
		{"--apply-account-balance-postgres", "--export-business-dataset"},
		{"--apply-account-balance-postgres", "--import-business-dataset"},
		{"--apply-account-balance-postgres", "--ensure-schema"},
		{"--apply-account-balance-postgres", "--seed"},
		{"--apply-account-balance-postgres", "--check-go-runtime-metrics"},
		{"--apply-account-balance-postgres", "--apply-go-runtime-metrics"},
		{"--apply-account-balance-postgres", "--mockdata"},
		{"--apply-account-balance-postgres", "--verify-mockdata-coverage"},
		{"--apply-account-balance-postgres", "--apply-j3a-proxy-latency-postgres"},
		{"--apply-account-balance-postgres", "--apply-j3b-model-check-postgres"},
		{"--apply-account-balance-postgres", "--apply-j3b-model-check-sqlite"},
	} {
		if got := runMaintenance(argv); got != 2 {
			t.Fatalf("%v 必须因互斥返回 2: %d", argv, got)
		}
	}
	// --version/--check-boundary 是信息性分支且先于 J3/J2 分发区，基线语义与
	// j3a/j3b 相同：组合时返回 0（版本/边界文本），不视为互斥违例。
	if got := runMaintenance([]string{"--apply-account-balance-postgres", "--version"}); got != 0 {
		t.Fatalf("--version 组合必须沿用基线返回 0: %d", got)
	}
	if got := runMaintenance([]string{"--apply-account-balance-postgres", "--check-boundary"}); got != 0 {
		t.Fatalf("--check-boundary 组合必须沿用基线返回 0: %d", got)
	}
	// 单独使用时必须进入命令主体：本负臂主机不可达，运行时失败返回 1。
	if got := runMaintenance([]string{"--apply-account-balance-postgres"}); got != 1 {
		t.Fatalf("单独使用且主机不可达必须返回 1: %d", got)
	}
}

// TestW1J2BootstrapUnreachable 直调结果函数覆盖不可达主机的运行时失败臂，
// 与 TestW14KRunnerRuntimeFailures 同口径。
func TestW1J2BootstrapUnreachable(t *testing.T) {
	t.Setenv(accountBalanceBootstrapEnv, w1J2UnreachableURL)
	if got := accountBalanceBootstrapResult(); got != 1 {
		t.Fatalf("不可达主机必须返回 1: %d", got)
	}
}

// TestW1J2BootstrapOutcomeExitCode 直调 report 层映射函数，覆盖 err /
// encode / 未就绪 / 成功四类出口（不含 DB 依赖）。
func TestW1J2BootstrapOutcomeExitCode(t *testing.T) {
	if got := accountBalanceBootstrapOutcomeExitCode(accountbalance.BootstrapReport{}, context.DeadlineExceeded); got != 1 {
		t.Fatalf("err 必须返回 1: %d", got)
	}
	wm14kWithClosedStdout(t, func() {
		if got := accountBalanceBootstrapOutcomeExitCode(accountbalance.BootstrapReport{}, nil); got != 1 {
			t.Fatalf("encode 失败必须返回 1: %d", got)
		}
	})
	if got := accountBalanceBootstrapOutcomeExitCode(accountbalance.BootstrapReport{MissingTables: []string{"account_balance_outcomes"}}, nil); got != 3 {
		t.Fatalf("未就绪必须返回 3: %d", got)
	}
	if got := accountBalanceBootstrapOutcomeExitCode(accountbalance.BootstrapReport{SchemaVerified: true, Tables: []string{"account_balance_owner_leases", "account_balance_account_leases", "account_balance_snapshots", "account_balance_outcomes"}, StatementsApplied: 6}, nil); got != 0 {
		t.Fatalf("就绪必须返回 0: %d", got)
	}
}
