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
// 迁移）退役后，本文件只保留现行命令的 CLI 契约测试（go-runtime-metrics
// bootstrap 命令族已随包删除；退役监控表清退命令的存储链路由
// monitoring_retirement_test.go 覆盖，这里只钉门禁与互斥用法契约）。

func TestMaintenanceCommandRejectsDropRetiredMonitoringTablesWithoutConfirmations(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t, "-drop-retired-monitoring-tables", "-dsn", "postgres://m@db.example.invalid:5432/juhe")
	if code != 2 {
		t.Fatalf("missing confirmation exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--node-stopped --go-stopped --backup-confirmed") {
		t.Fatalf("missing confirmation stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsDropRetiredMonitoringTablesMutualExclusion(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-drop-retired-monitoring-tables", "-apply-studio-retirement",
		"-node-stopped", "-go-stopped", "-backup-confirmed")
	if code != 2 {
		t.Fatalf("mutual exclusion exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	// 互斥链以 studio 退场检查为先（main.go 既有链序），命中即拒绝。
	if !strings.Contains(stderr, "mutually exclusive with other maintenance commands") {
		t.Fatalf("mutual exclusion stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsDropRetiredMonitoringTablesBadDSN(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-drop-retired-monitoring-tables", "-dsn", "sqlite:///x.db",
		"-node-stopped", "-go-stopped", "-backup-confirmed")
	if code != 2 {
		t.Fatalf("bad dsn exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--dsn 必须是 postgres:// 或 postgresql:// URL") {
		t.Fatalf("bad dsn stderr=%q", stderr)
	}
}

func TestMaintenanceCommandRejectsDropRetiredMonitoringTablesMissingDSN(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-drop-retired-monitoring-tables",
		"-node-stopped", "-go-stopped", "-backup-confirmed")
	if code != 2 {
		t.Fatalf("missing dsn exit=%d, want exit status 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "需要 --dsn") {
		t.Fatalf("missing dsn stderr=%q", stderr)
	}
}
