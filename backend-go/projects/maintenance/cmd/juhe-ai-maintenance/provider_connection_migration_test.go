package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/providerconnectionmigration"
)

// 供应商与上游协议面迁移命令的 CLI 分支测试（进程内直调 runMaintenance，输出
// 捕获复用 wmRunMaintenanceCapture；测试单进程纪律，不孵化子进程）。

func TestProviderConnectionMigrationFormalExecutionNotDelivered(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-provider-connection-model",
		"-driver", "postgres",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/legacy",
	)
	if code != 2 {
		t.Fatalf("非 dry-run 调用应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "正式执行尚未交付：历史 usage/audit 列改造完成后提供") {
		t.Fatalf("stderr 应包含切片边界错误原文: %q", stderr)
	}
}

func TestProviderConnectionMigrationDryRunRequiresManifestOut(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-provider-connection-model", "-dry-run",
		"-driver", "postgres",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/legacy",
	)
	if code != 2 {
		t.Fatalf("缺 --manifest-out 应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "--manifest-out") {
		t.Fatalf("stderr 应提示 --manifest-out 必填: %q", stderr)
	}
}

func TestProviderConnectionMigrationDryRunSQLiteRejectedThisSlice(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-provider-connection-model", "-dry-run",
		"-manifest-out", filepath.Join(t.TempDir(), "manifest.json"),
		"-driver", "sqlite",
	)
	if code != 2 {
		t.Fatalf("sqlite 形态在本切片应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "SQLite 形态后补") {
		t.Fatalf("stderr 应说明切片只支持 postgres: %q", stderr)
	}
}

func TestProviderConnectionMigrationMutuallyExclusive(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-provider-connection-model", "-dry-run",
		"-manifest-out", filepath.Join(t.TempDir(), "manifest.json"),
		"-driver", "postgres",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/legacy",
		"-ensure-schema",
	)
	if code != 2 {
		t.Fatalf("与其他命令组合应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("stderr 应提示互斥: %q", stderr)
	}
}

func TestProviderConnectionMigrationManifestSHA256Contract(t *testing.T) {
	// --manifest-sha256 的复核口径（正式执行切片消费）：manifest 原始字节 SHA-256
	// 的 64 位小写十六进制。
	digest := providerconnectionmigration.ManifestSHA256([]byte("bytes"))
	if len(digest) != 64 || strings.ToLower(digest) != digest {
		t.Fatalf("manifest sha256 口径不符: %q", digest)
	}
}
