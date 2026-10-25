package main

// --mockdata / --verify-mockdata-coverage 的出口分支与互斥矩阵（D7 PG 骨架：
// 命令在 PG 造数重写完成前对一切有效参数返回 exit 2 与固定 pending 错误；
// sqlite 数据根旗标族随 sqlite 实现删除）。与 wm_main_exit_branches_test.go
// 同一手法：进程内直调 runMaintenance（测试单进程纪律），stdout/stderr 通过
// 替换 os.Stdout/os.Stderr 捕获。

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/mockdata"
)

func TestWMMockdataExitBranches(t *testing.T) {
	branches := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{name: "mutex with version", args: []string{"-mockdata", "-version"}, wantCode: 2, wantErr: "mockdata flags are mutually exclusive with other maintenance commands"},
		// 互斥文案取决于分支顺序：ensure-schema 分支在 mockdata 分支之前，
		// 因此这条组合报的是存储引导分支的文案（两者都是 exit 2 的互斥错误）。
		{name: "mutex with storage bootstrap", args: []string{"-mockdata", "-ensure-schema"}, wantCode: 2, wantErr: "storage bootstrap flags are mutually exclusive"},
		{name: "mutex with boundary check", args: []string{"-verify-mockdata-coverage", "-check-boundary"}, wantCode: 2, wantErr: "mockdata flags are mutually exclusive"},
		{name: "run and verify are mutually exclusive", args: []string{"-mockdata", "-verify-mockdata-coverage"}, wantCode: 2, wantErr: "mockdata seeding and mockdata coverage verification flags are mutually exclusive"},
		{name: "missing dsn", args: []string{"-mockdata"}, wantCode: 2, wantErr: "需要 --dsn"},
		{name: "days out of range", args: []string{"-mockdata", "-mockdata-days", "0", "-dsn", "postgres://u@127.0.0.1:5432/db"}, wantCode: 2, wantErr: "--mockdata-days 必须在 1 到 90 之间"},
		{name: "days too large", args: []string{"-mockdata", "-mockdata-days", "91", "-dsn", "postgres://u@127.0.0.1:5432/db"}, wantCode: 2, wantErr: "--mockdata-days 必须在 1 到 90 之间"},
		{name: "daily requests out of range", args: []string{"-mockdata", "-mockdata-daily-requests", "501", "-dsn", "postgres://u@127.0.0.1:5432/db"}, wantCode: 2, wantErr: "--mockdata-daily-requests 必须在 1 到 500 之间"},
		{name: "PG seed pending", args: []string{"-mockdata", "-dsn", "postgres://u@127.0.0.1:5432/db"}, wantCode: 2, wantErr: "PG 造数尚未交付"},
		{name: "coverage PG seed pending", args: []string{"-verify-mockdata-coverage", "-dsn", "postgres://u@127.0.0.1:5432/db"}, wantCode: 2, wantErr: "PG 造数尚未交付"},
	}
	for _, branch := range branches {
		branch := branch
		t.Run(branch.name, func(t *testing.T) {
			code, _, stderr := wmRunMaintenanceCapture(t, branch.args...)
			if code != branch.wantCode {
				t.Fatalf("exit=%d want %d\n%s", code, branch.wantCode, stderr)
			}
			if branch.wantErr != "" && !strings.Contains(stderr, branch.wantErr) {
				t.Fatalf("输出缺少 %q:\n%s", branch.wantErr, stderr)
			}
		})
	}
}

// TestWMMockdataPendingReportKeepsParameterEcho 校验 pending 路径仍输出参数
// 回显骨架报告（driver=postgres + DSN + 参数），便于调用方排查误投递。
func TestWMMockdataPendingReportKeepsParameterEcho(t *testing.T) {
	code, stdout, stderr := wmRunMaintenanceCapture(t, "-mockdata", "-dsn", "postgres://u@127.0.0.1:5432/db", "-mockdata-days", "5", "-mockdata-daily-requests", "7")
	if code != 2 {
		t.Fatalf("exit=%d want 2\nstderr=%s", code, stderr)
	}
	var report mockdata.Report
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &report); err != nil {
		t.Fatalf("stdout is not a single Report JSON: %v\n%s", err, stdout)
	}
	if report.Driver != "postgres" || report.Days != 5 || report.DailyRequests != 7 {
		t.Fatalf("report = %+v", report)
	}
}

// wmWithBrokenStdout 把 os.Stdout 换成只读句柄：JSON Encoder 写失败。真实进程里
// 对应「stdout 被重定向到不可写目标 / 管道提前关闭」。
func wmWithBrokenStdout(t *testing.T, fn func()) {
	t.Helper()
	readOnly, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = readOnly
	defer func() {
		os.Stdout = saved
		_ = readOnly.Close()
	}()
	if _, err := readOnly.Write([]byte("probe")); err == nil {
		t.Skip("只读句柄仍可写入，无法构造输出失败")
	}
	fn()
}

// TestWMMockdataEncodeFailures 覆盖「参数回显报告无法输出」分支。
func TestWMMockdataEncodeFailures(t *testing.T) {
	code := 0
	wmWithBrokenStdout(t, func() {
		code = runMockdataCommand(mockdataCommandFlags{
			DSN:           "postgres://u@127.0.0.1:5432/db",
			Days:          mockdata.DefaultDays,
			DailyRequests: mockdata.DefaultDailyRequests,
		})
	})
	// pending 错误先于 encode 发生（exit 2），输出失败分支只在 pending 解除后
	// 可达——此处钉住当前契约：不 panic、不 os.Exit。
	if code != 2 {
		t.Fatalf("pending encode path exit=%d, want 2", code)
	}
}
