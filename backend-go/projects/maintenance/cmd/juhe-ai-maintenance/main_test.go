package main

import (
	"strings"
	"testing"
)

// 本文件原以 go build + 运行编译产物的方式验证 CLI 契约；按“测试单进程纪律”
// （docs/develop/后端测试分层规则.md “测试进程纪律（硬性）”章节）改写为进程内
// 直调 runMaintenance（wmRunMaintenanceCapture 捕获退出码与 stdout/stderr），
// 退出码与输出断言语义不变。“编译产物 + 真实启动”的验证走 acceptance/ 冒烟
// 体系，不进 go test。Node→Go 迁移期命令族（owner/capability/route manifest、
// J3a/J3b backfill/readback、cutover/inventory evidence、hybrid_smart
// 迁移）退役后，本文件只保留现行命令的 CLI 契约测试（J3a/J3b bootstrap
// apply 命令为现行生产初始化契约，由 w14k/wm 系列文件覆盖）。

func TestGoRuntimeMetricsApplyPreflightRequiresURLAndAllConfirmations(t *testing.T) {
	validURL := "postgres://metrics@db.example.invalid:5432/juhe"
	tests := []struct {
		name                                   string
		url                                    string
		nodeStopped, goStopped, backupVerified bool
		want                                   int
	}{
		{name: "missing url", url: "", nodeStopped: true, goStopped: true, backupVerified: true, want: 2},
		{name: "node running", url: validURL, nodeStopped: false, goStopped: true, backupVerified: true, want: 2},
		{name: "go running", url: validURL, nodeStopped: true, goStopped: false, backupVerified: true, want: 2},
		{name: "backup unverified", url: validURL, nodeStopped: true, goStopped: true, backupVerified: false, want: 2},
		{name: "all confirmed", url: validURL, nodeStopped: true, goStopped: true, backupVerified: true, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := goRuntimeMetricsApplyPreflightExitCode(test.url, test.nodeStopped, test.goStopped, test.backupVerified); got != test.want {
				t.Fatalf("preflight exit code=%d, want %d", got, test.want)
			}
		})
	}
}

func TestMaintenanceCommandRejectsGoRuntimeMetricsCheckWithoutURL(t *testing.T) {
	t.Setenv("JUHE_AI_MAINTENANCE_GO_RUNTIME_METRICS_POSTGRES_URL", "")
	code, _, stderr := wmRunMaintenanceCapture(t, "-check-go-runtime-metrics")
	if code != 2 {
		t.Fatalf("missing Go runtime metrics URL exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "requires --go-runtime-metrics-postgres-url") {
		t.Fatalf("missing Go runtime metrics URL stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsGoRuntimeMetricsApplyWithoutConfirmations(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t, "-apply-go-runtime-metrics", "-go-runtime-metrics-postgres-url", "postgres://metrics@db.example.invalid:5432/juhe")
	if code != 2 {
		t.Fatalf("missing confirmation exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--node-stopped --go-stopped --backup-confirmed") {
		t.Fatalf("missing confirmation stderr=%q", stderr)
	}
}
