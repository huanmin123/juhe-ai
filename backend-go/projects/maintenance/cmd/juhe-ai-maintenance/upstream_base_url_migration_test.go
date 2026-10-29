package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 上游 Base URL 版本根迁移命令的 CLI 分支测试（进程内直调 runMaintenance，
// 输出捕获复用 wmRunMaintenanceCapture；测试单进程纪律，不孵化子进程）。
// 涉及真实库的行为（扫描/分类/写入）由 internal/upstreambaseurlmigration 的
// 纯函数测试覆盖；本文件只钉住用法门禁与互斥矩阵。

func TestUpstreamBaseURLMigrationFormalExecutionRequiresGates(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-upstream-base-url-version-root",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
		"-manifest-out", filepath.Join(t.TempDir(), "manifest.json"),
	)
	if code != 2 {
		t.Fatalf("缺停写/备份门禁应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "--node-stopped --go-stopped --backup-confirmed") {
		t.Fatalf("stderr 应提示三确认门禁: %q", stderr)
	}
}

func TestUpstreamBaseURLMigrationDryRunRequiresManifestOut(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-upstream-base-url-version-root", "-dry-run",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
	)
	if code != 2 {
		t.Fatalf("缺 --manifest-out 应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "--manifest-out") {
		t.Fatalf("stderr 应提示 --manifest-out 必填: %q", stderr)
	}
}

func TestUpstreamBaseURLMigrationRequiresDSN(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-upstream-base-url-version-root", "-dry-run",
		"-manifest-out", filepath.Join(t.TempDir(), "manifest.json"),
	)
	if code != 2 {
		t.Fatalf("缺 --dsn 应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "--dsn") {
		t.Fatalf("stderr 应提示 --dsn 必填: %q", stderr)
	}
}

func TestUpstreamBaseURLMigrationMutuallyExclusive(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-upstream-base-url-version-root", "-dry-run",
		"-manifest-out", filepath.Join(t.TempDir(), "manifest.json"),
		"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
		"-ensure-schema",
	)
	if code != 2 {
		t.Fatalf("与其他命令组合应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("stderr 应提示互斥: %q", stderr)
	}
}

func TestUpstreamBaseURLMigrationAndVerifyMutuallyExclusive(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-upstream-base-url-version-root",
		"-verify-upstream-base-url-version-root", "-upstream-base-url-check", "preflight",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
	)
	if code != 2 {
		t.Fatalf("migrate 与 verify 组合应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("stderr 应提示互斥: %q", stderr)
	}
}

func TestUpstreamBaseURLVerifyRequiresCheck(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-verify-upstream-base-url-version-root",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
	)
	if code != 2 {
		t.Fatalf("缺 --upstream-base-url-check 应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "--upstream-base-url-check=preflight|postflight") {
		t.Fatalf("stderr 应提示 check 参数取值: %q", stderr)
	}
}
