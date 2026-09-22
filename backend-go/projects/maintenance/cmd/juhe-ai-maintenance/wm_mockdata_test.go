package main

// --mockdata / --verify-mockdata-coverage 的出口分支与互斥矩阵。与
// wm_main_exit_branches_test.go 同一手法：进程内直调 runMaintenance（测试单进程
// 纪律），stdout/stderr 通过替换 os.Stdout/os.Stderr 捕获。
//
// 全部数据都写在 t.TempDir() 里：造数命令的失败模式之一就是写错数据根，测试
// 绝不能碰 .local/dev/data。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/mockdata"
)

// wmBootstrapMockdataStores 在给定数据根下把每个固定存储与每个 codex 分片建成
// 「有 schema 且有一行数据」的状态，让覆盖校验能走到 Ready。
func wmBootstrapMockdataStores(t *testing.T, paths mockdata.Paths) {
	t.Helper()
	targets := []string{
		paths.Business, paths.Chat, paths.Dataset, paths.UsageCatalog, paths.Stats,
		paths.RuntimeLog, paths.TableMonitor, paths.AuditLog, paths.OperationLog,
		paths.ModelCheck, paths.TaskRuns, paths.AccountHealth,
	}
	for index := 0; index < paths.CodexContextShardCount; index++ {
		targets = append(targets, filepath.Join(paths.CodexContextShardRoot, fmt.Sprintf("state-%03d.sqlite3", index)))
	}
	for _, target := range targets {
		db, err := bootstrap.OpenSQLiteFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE wm_probe (id TEXT PRIMARY KEY)`); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO wm_probe (id) VALUES ('wm')`); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWMMockdataExitBranches(t *testing.T) {
	root := t.TempDir()
	// 数据根指向普通文件：ResolvePaths 不查文件系统，失败发生在写摘要阶段。
	fileRoot := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := mockdata.ResolvePaths(root, "", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	wmBootstrapMockdataStores(t, paths)

	branches := []struct {
		name     string
		args     []string
		env      map[string]string
		wantCode int
		wantErr  string
	}{
		{name: "mutex with version", args: []string{"-mockdata", "-version"}, wantCode: 2, wantErr: "mockdata flags are mutually exclusive with other maintenance commands"},
		// 互斥文案取决于分支顺序：ensure-schema 分支在 mockdata 分支之前，
		// 因此这条组合报的是存储引导分支的文案（两者都是 exit 2 的互斥错误）。
		{name: "mutex with storage bootstrap", args: []string{"-mockdata", "-ensure-schema"}, wantCode: 2, wantErr: "storage bootstrap flags are mutually exclusive"},
		{name: "mutex with manifest check", args: []string{"-verify-mockdata-coverage", "-verify-business-owner-manifest"}, wantCode: 2, wantErr: "mockdata flags are mutually exclusive"},
		{name: "run and verify are mutually exclusive", args: []string{"-mockdata", "-verify-mockdata-coverage"}, wantCode: 2, wantErr: "mockdata seeding and mockdata coverage verification flags are mutually exclusive"},
		{name: "postgres driver rejected", args: []string{"-mockdata", "-driver", "postgres", "-mockdata-data-dir", root}, wantCode: 2, wantErr: "只支持 SQLite"},
		{name: "dsn rejected", args: []string{"-mockdata", "-dsn", "postgres://u@127.0.0.1:5432/db", "-mockdata-data-dir", root}, wantCode: 2, wantErr: "不接受 --dsn"},
		{name: "sqlite driver accepted", args: []string{"-mockdata", "-driver", "SQLite", "-mockdata-data-dir", root}, wantCode: 0},
		{name: "missing data dir", args: []string{"-mockdata"}, env: map[string]string{"JUHE_AI_DATA_DIR": ""}, wantCode: 2, wantErr: "需要显式数据根目录"},
		{name: "days out of range", args: []string{"-mockdata", "-mockdata-days", "0", "-mockdata-data-dir", root}, wantCode: 2, wantErr: "--mockdata-days 必须在 1 到 90 之间"},
		{name: "days too large", args: []string{"-mockdata", "-mockdata-days", "91", "-mockdata-data-dir", root}, wantCode: 2, wantErr: "--mockdata-days 必须在 1 到 90 之间"},
		{name: "daily requests out of range", args: []string{"-mockdata", "-mockdata-daily-requests", "501", "-mockdata-data-dir", root}, wantCode: 2, wantErr: "--mockdata-daily-requests 必须在 1 到 500 之间"},
		{name: "runtime failure", args: []string{"-mockdata", "-mockdata-data-dir", fileRoot}, wantCode: 1, wantErr: "mockdata 造数失败"},
		{name: "coverage not ready", args: []string{"-verify-mockdata-coverage", "-mockdata-data-dir", filepath.Join(root, "nowhere")}, wantCode: 3},
		{name: "coverage ready", args: []string{"-verify-mockdata-coverage", "-mockdata-data-dir", root}, wantCode: 0},
		{name: "coverage rejects postgres driver", args: []string{"-verify-mockdata-coverage", "-driver", "postgres", "-mockdata-data-dir", root}, wantCode: 2, wantErr: "只支持 SQLite"},
		{name: "coverage missing data dir", args: []string{"-verify-mockdata-coverage"}, env: map[string]string{"JUHE_AI_DATA_DIR": ""}, wantCode: 2, wantErr: "需要显式数据根目录"},
	}
	for _, branch := range branches {
		branch := branch
		t.Run(branch.name, func(t *testing.T) {
			for key, value := range branch.env {
				t.Setenv(key, value)
			}
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

func TestWMMockdataReportAndCoverageOutput(t *testing.T) {
	root := t.TempDir()
	paths, err := mockdata.ResolvePaths(root, "", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	wmBootstrapMockdataStores(t, paths)

	// --mockdata：stdout 只有一条 Report JSON，摘要落在数据根下。
	code, stdout, stderr := wmRunMaintenanceCapture(t, "-mockdata", "-mockdata-data-dir", root, "-mockdata-days", "5", "-mockdata-daily-requests", "7")
	if code != 0 {
		t.Fatalf("exit=%d\nstderr=%s", code, stderr)
	}
	var report mockdata.Report
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &report); err != nil {
		t.Fatalf("stdout is not a single Report JSON: %v\n%s", err, stdout)
	}
	if report.Driver != "sqlite" || report.Days != 5 || report.DailyRequests != 7 {
		t.Fatalf("report = %+v", report)
	}
	if report.SummaryPath != filepath.Join(root, mockdata.SummaryFilename) {
		t.Fatalf("summary path = %q", report.SummaryPath)
	}
	if len(report.Domains) != 5 {
		t.Fatalf("domains = %+v", report.Domains)
	}
	// 进度日志只写 stderr，stdout 保持单条 JSON。
	if !strings.Contains(stderr, "level=INFO") && strings.TrimSpace(stderr) != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}

	// --verify-mockdata-coverage：Ready 时 exit 0，输出 CoverageReport。
	code, stdout, stderr = wmRunMaintenanceCapture(t, "-verify-mockdata-coverage", "-mockdata-data-dir", root)
	if code != 0 {
		t.Fatalf("exit=%d\nstderr=%s\nstdout=%s", code, stderr, stdout)
	}
	var coverage mockdata.CoverageReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &coverage); err != nil {
		t.Fatalf("stdout is not a CoverageReport JSON: %v\n%s", err, stdout)
	}
	if !coverage.Ready {
		t.Fatalf("coverage = %+v", coverage)
	}
	if len(coverage.NotCovered) == 0 {
		t.Fatal("domain assertions must be listed as not covered while the domains are placeholders")
	}
}

// TestWMMockdataCoverageRuntimeFailure 覆盖「覆盖校验本身运行失败」：业务库文件
// 不是 SQLite 格式时打开失败，必须 exit 1 而不是被当成「表为空」。
func TestWMMockdataCoverageRuntimeFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "business.sqlite3"), []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := wmRunMaintenanceCapture(t, "-verify-mockdata-coverage", "-mockdata-data-dir", root)
	if code != 1 {
		t.Fatalf("exit=%d want 1\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "mockdata 覆盖校验失败") {
		t.Fatalf("stderr = %s", stderr)
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

// TestWMMockdataEncodeFailures 覆盖两条命令的「报告无法输出」分支。
func TestWMMockdataEncodeFailures(t *testing.T) {
	root := t.TempDir()
	paths, err := mockdata.ResolvePaths(root, "", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}

	code := 0
	wmWithBrokenStdout(t, func() {
		code = runMockdataCommand(mockdataCommandFlags{
			DataDir:       root,
			Days:          mockdata.DefaultDays,
			DailyRequests: mockdata.DefaultDailyRequests,
		})
	})
	if code != 1 {
		t.Fatalf("run encode failure exit=%d, want 1", code)
	}

	wmBootstrapMockdataStores(t, paths)
	wmWithBrokenStdout(t, func() {
		// 两个范围参数在覆盖模式下同样被校验（CLI 总是带上 flag 默认值）。
		code = runMockdataCommand(mockdataCommandFlags{
			VerifyCoverage: true,
			DataDir:        root,
			Days:           mockdata.DefaultDays,
			DailyRequests:  mockdata.DefaultDailyRequests,
		})
	})
	if code != 1 {
		t.Fatalf("coverage encode failure exit=%d, want 1", code)
	}
}

// TestWMMockdataUsesDataDirEnv 验证 JUHE_AI_DATA_DIR 作为数据根的显式回退。
func TestWMMockdataUsesDataDirEnv(t *testing.T) {
	root := t.TempDir()
	t.Setenv("JUHE_AI_DATA_DIR", root)
	code, stdout, stderr := wmRunMaintenanceCapture(t, "-mockdata")
	if code != 0 {
		t.Fatalf("exit=%d\nstderr=%s", code, stderr)
	}
	var report mockdata.Report
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &report); err != nil {
		t.Fatalf("stdout is not a Report JSON: %v\n%s", err, stdout)
	}
	if report.DataDir != root {
		t.Fatalf("dataDir = %q, want %q", report.DataDir, root)
	}
	if _, err := os.Stat(filepath.Join(root, mockdata.SummaryFilename)); err != nil {
		t.Fatalf("summary not written into JUHE_AI_DATA_DIR: %v", err)
	}
}
