package main

// --mockdata / --verify-mockdata-coverage 的出口矩阵与真实运行（PG 五域造数）：
//   - 用法错误（互斥、缺 --dsn、参数越界）exit 2；
//   - 真实造数（pgtestkit 独立库）exit 0，stdout 一条 Report JSON，摘要按
//     --mockdata-summary-file 显式写出；
//   - 覆盖校验：五域接线 + autofill 后 fresh 库 Ready（exit 0）；清空一张
//     非白名单业务表后 Ready=false → exit 3 且 empty 列表命中。
//
// 与 wm_main_exit_branches_test.go 同一手法：进程内直调 runMaintenance（测试
// 单进程纪律），stdout/stderr 通过替换 os.Stdout/os.Stderr 捕获。

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/mockdata"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
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
		{name: "missing dsn coverage", args: []string{"-verify-mockdata-coverage"}, wantCode: 2, wantErr: "需要 --dsn"},
		{name: "days out of range", args: []string{"-mockdata", "-mockdata-days", "0", "-dsn", "postgres://u@127.0.0.1:5432/db"}, wantCode: 2, wantErr: "--mockdata-days 必须在 1 到 90 之间"},
		{name: "days too large", args: []string{"-mockdata", "-mockdata-days", "91", "-dsn", "postgres://u@127.0.0.1:5432/db"}, wantCode: 2, wantErr: "--mockdata-days 必须在 1 到 90 之间"},
		{name: "daily requests out of range", args: []string{"-mockdata", "-mockdata-daily-requests", "501", "-dsn", "postgres://u@127.0.0.1:5432/db"}, wantCode: 2, wantErr: "--mockdata-daily-requests 必须在 1 到 500 之间"},
		{name: "boundary values accepted at usage layer", args: []string{"-verify-mockdata-coverage", "-dsn", "postgres://u@127.0.0.1:1/db", "-mockdata-days", "1", "-mockdata-daily-requests", "1"}, wantCode: 1, wantErr: "mockdata 覆盖校验失败"},
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

// TestWMMockdataRunEndToEnd 是 CLI 全链路验收：pgtestkit 独立库上 exit 0、
// stdout 一条 Report JSON、摘要文件按显式路径写出。
func TestWMMockdataRunEndToEnd(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t).DSN
	summaryPath := filepath.Join(t.TempDir(), "mockdata-summary.json")
	code, stdout, stderr := wmRunMaintenanceCapture(t, "-mockdata", "-dsn", dsn,
		"-mockdata-days", "5", "-mockdata-daily-requests", "7",
		"-mockdata-summary-file", summaryPath)
	if code != 0 {
		t.Fatalf("exit=%d want 0\nstderr=%s", code, stderr)
	}
	var report mockdata.Report
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &report); err != nil {
		t.Fatalf("stdout is not a single Report JSON: %v\n%s", err, stdout)
	}
	if report.Driver != "postgres" || report.Days != 5 || report.DailyRequests != 7 {
		t.Fatalf("report = %+v", report)
	}
	if report.SummaryPath != summaryPath {
		t.Fatalf("SummaryPath = %q", report.SummaryPath)
	}
	wired := false
	for _, domain := range report.Domains {
		if domain.Name == mockdata.DomainBusiness && len(domain.Counts) > 0 {
			wired = true
		}
	}
	if !wired {
		t.Fatalf("business 域未产出: %+v", report.Domains)
	}
	if _, err := os.Stat(summaryPath); err != nil {
		t.Fatalf("摘要文件未写出: %v", err)
	}
}

// TestWMMockdataCoverageReadyAndExitThree 验证覆盖命令两态：五域接线 +
// autofill 后 fresh 库 Ready（exit 0）；清空一张非白名单业务表后
// Ready=false → exit 3 且 empty 列表命中。
func TestWMMockdataCoverageReadyAndExitThree(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t).DSN
	summaryPath := filepath.Join(t.TempDir(), "mockdata-summary.json")
	if code, _, stderr := wmRunMaintenanceCapture(t, "-mockdata", "-dsn", dsn, "-mockdata-summary-file", summaryPath); code != 0 {
		t.Fatalf("造数 exit=%d want 0\n%s", code, stderr)
	}
	code, stdout, stderr := wmRunMaintenanceCapture(t, "-verify-mockdata-coverage", "-dsn", dsn, "-mockdata-summary-file", summaryPath)
	if code != 0 {
		t.Fatalf("五域 + autofill 后覆盖应 Ready（exit 0），got exit=%d\nstderr=%s", code, stderr)
	}
	var coverage mockdata.CoverageReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &coverage); err != nil {
		t.Fatalf("stdout is not a single CoverageReport JSON: %v\n%s", err, stdout)
	}
	if !coverage.Ready {
		t.Fatalf("五域 + autofill 后覆盖应 Ready: %+v", coverage)
	}
	if len(coverage.OwnerStoresPending) == 0 {
		t.Fatalf("OwnerStoresPending 应列出缺失属主: %+v", coverage)
	}
	// 清空一张非白名单业务表：Empty 命中 → exit 3。
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM juhe_business.announcements`); err != nil {
		t.Fatalf("clear announcements: %v", err)
	}
	code, stdout, stderr = wmRunMaintenanceCapture(t, "-verify-mockdata-coverage", "-dsn", dsn, "-mockdata-summary-file", summaryPath)
	if code != 3 {
		t.Fatalf("清空业务表后 exit=%d want 3\nstderr=%s", code, stderr)
	}
	coverage = mockdata.CoverageReport{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &coverage); err != nil {
		t.Fatalf("stdout is not a single CoverageReport JSON: %v\n%s", err, stdout)
	}
	if coverage.Ready {
		t.Fatalf("空表存在时覆盖不应 Ready: %+v", coverage)
	}
	found := false
	for _, item := range coverage.Empty {
		if strings.Contains(item, "juhe_business.announcements") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Empty 应命中 juhe_business.announcements: %v", coverage.Empty)
	}
}

// TestWMMockdataCoverageRuntimeFailureExitOne 验证校验运行失败（不可达 DSN）
// 时 exit 1，与用法错误（exit 2）、未 Ready（exit 3）区分。
func TestWMMockdataCoverageRuntimeFailureExitOne(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t, "-verify-mockdata-coverage", "-dsn", "postgres://juhe_ai_no_such_role@127.0.0.1:1/no_such_db")
	if code != 1 {
		t.Fatalf("exit=%d want 1\nstderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "mockdata 覆盖校验失败") {
		t.Fatalf("stderr 缺运行失败文案: %s", stderr)
	}
}

// TestWMMockdataSummaryFileOptionalWithoutFallback 验证摘要路径无隐式回退：
// 不传 --mockdata-summary-file 时运行成功且 Report.SummaryPath 为空。
func TestWMMockdataSummaryFileOptionalWithoutFallback(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t).DSN
	code, stdout, stderr := wmRunMaintenanceCapture(t, "-mockdata", "-dsn", dsn, "-mockdata-days", "1", "-mockdata-daily-requests", "1")
	if code != 0 {
		t.Fatalf("exit=%d want 0\nstderr=%s", code, stderr)
	}
	var report mockdata.Report
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &report); err != nil {
		t.Fatalf("stdout is not a single Report JSON: %v\n%s", err, stdout)
	}
	if report.SummaryPath != "" {
		t.Fatalf("未传摘要路径时 SummaryPath 应为空: %q", report.SummaryPath)
	}
	// 覆盖校验不传摘要路径（无隐式回退）：表已由 autofill 全量门填满，Ready
	// 仍为 true（exit 0），但域断言因无接线快照全部进 NotCovered——证明覆盖
	// 命令没有偷偷读取上次运行的摘要。
	code, stdout, _ = wmRunMaintenanceCapture(t, "-verify-mockdata-coverage", "-dsn", dsn)
	if code != 0 {
		t.Fatalf("无摘要覆盖 exit=%d want 0（Ready 判定不依赖摘要）", code)
	}
	var coverage mockdata.CoverageReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &coverage); err != nil {
		t.Fatalf("stdout is not a single CoverageReport JSON: %v", err)
	}
	notCovered := false
	for _, item := range coverage.NotCovered {
		if strings.Contains(item, "未接线") {
			notCovered = true
			break
		}
	}
	if !notCovered {
		t.Fatalf("无摘要时域断言应全部 NotCovered: %v", coverage.NotCovered)
	}
}
