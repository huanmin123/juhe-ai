package mockdata

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOptionsValidate(t *testing.T) {
	paths, err := ResolvePaths(t.TempDir(), "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		options Options
		want    string
	}{
		{name: "days zero", options: Options{Paths: paths, Days: 0, DailyRequests: 1}, want: "days 必须在 1 到 90"},
		{name: "days too large", options: Options{Paths: paths, Days: 91, DailyRequests: 1}, want: "days 必须在 1 到 90"},
		{name: "daily requests zero", options: Options{Paths: paths, Days: 1, DailyRequests: 0}, want: "dailyRequests 必须在 1 到 500"},
		{name: "daily requests too large", options: Options{Paths: paths, Days: 1, DailyRequests: 501}, want: "dailyRequests 必须在 1 到 500"},
		{name: "missing data dir", options: Options{Days: 1, DailyRequests: 1}, want: "需要已解析的存储路径"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.options.validate()
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want substring %q", err, testCase.want)
			}
		})
	}
	if err := (Options{Paths: paths, Days: 1, DailyRequests: 1}).validate(); err != nil {
		t.Fatalf("lower bounds must be accepted: %v", err)
	}
	if err := (Options{Paths: paths, Days: MaxDays, DailyRequests: MaxDailyRequests}).validate(); err != nil {
		t.Fatalf("upper bounds must be accepted: %v", err)
	}
}

func TestOptionsClock(t *testing.T) {
	pinned := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := (Options{Now: pinned}).clock(); !got.Equal(pinned) {
		t.Fatalf("clock = %v, want the pinned instant", got)
	}
	if got := (Options{}).clock(); got.IsZero() {
		t.Fatal("an unpinned clock must fall back to the wall clock")
	}
}

func TestRunOnEmptyDataRootWritesReportAndSummary(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	pinned := autofillTestNow
	report, err := Run(context.Background(), Options{Paths: paths, Days: 7, DailyRequests: 5, Now: pinned})
	if err != nil {
		t.Fatalf("run on an empty data root must succeed: %v", err)
	}
	if report.Driver != "sqlite" || report.DataDir != root {
		t.Fatalf("report head = %+v", report)
	}
	if report.Days != 7 || report.DailyRequests != 5 {
		t.Fatalf("report options = %d/%d", report.Days, report.DailyRequests)
	}
	if report.StartedAt != pinned.UTC().Format(time.RFC3339) {
		t.Fatalf("startedAt = %q", report.StartedAt)
	}
	if report.FinishedAt == "" {
		t.Fatal("finishedAt must be filled")
	}
	if report.Cleanup == nil || report.Counts == nil || report.Autofill == nil {
		t.Fatalf("report maps must be non-nil: %+v", report)
	}
	if report.Domains == nil {
		t.Fatal("domains must be an empty slice, not nil")
	}
	// 首次运行：所有清理目标都因存储/表不存在被跳过。
	if len(report.CleanupSkipped) == 0 {
		t.Fatal("the first run must record skipped cleanup targets")
	}
	if report.SummaryPath != filepath.Join(root, SummaryFilename) {
		t.Fatalf("summary path = %q", report.SummaryPath)
	}
	raw, err := os.ReadFile(report.SummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	var document summaryDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("summary is not valid JSON: %v", err)
	}
	if document.MockUserPassword != MockUserPassword {
		t.Fatalf("mockUserPassword = %q", document.MockUserPassword)
	}
	if document.Options.Days != 7 || document.Options.DailyRequests != 5 {
		t.Fatalf("summary options = %+v", document.Options)
	}
	if document.MockUsers == nil || document.APIKeys == nil || document.Counts == nil {
		t.Fatalf("summary collections must be non-nil: %+v", document)
	}
	// 域占位实现：五个域都出现在报告里且计数为空。
	if len(report.Domains) != len(domainSeeds) {
		t.Fatalf("domains = %+v", report.Domains)
	}
	for _, result := range report.Domains {
		if len(result.Counts) != 0 {
			t.Fatalf("domain %s should be unwired: %v", result.Name, result.Counts)
		}
	}
}

func TestRunRejectsInvalidOptions(t *testing.T) {
	if _, err := Run(context.Background(), Options{Days: 1, DailyRequests: 1}); err == nil {
		t.Fatal("missing paths must fail")
	}
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{Paths: paths, Days: 200, DailyRequests: 1}); err == nil {
		t.Fatal("out-of-range days must fail")
	}
	if _, err := RunWithLogger(context.Background(), Options{Paths: paths, Days: 200, DailyRequests: 1}, nil); err == nil {
		t.Fatal("RunWithLogger must validate too")
	}
}

// TestRunCleansAndReinserts 验证「重复执行」语义：第二轮先删掉第一轮写的
// mock 行，再重建。
func TestRunCleansAndReinserts(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	// 先手工放一批带清理标识的旧数据与一条真实数据。
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT NOT NULL)`)
	if err := e.insertMap(context.Background(), StoreBusiness, "groups", map[string]any{"id": CleanupIDPrefix + "old", "name": CleanupNamePrefix + "旧分组"}); err != nil {
		t.Fatal(err)
	}
	if err := e.insertMap(context.Background(), StoreBusiness, "groups", map[string]any{"id": "real", "name": "真实分组"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := Run(context.Background(), Options{Paths: paths, Days: 3, DailyRequests: 2})
	if err != nil {
		t.Fatal(err)
	}
	if report.Cleanup[StoreBusiness+".groups"] != 1 {
		t.Fatalf("cleanup = %v, want one deleted group", report.Cleanup)
	}
	// groups 在自动补全跳过清单里（业务域表），因此只剩真实行。
	after := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	defer func() { _ = after.Close() }()
	count, err := after.queryCount(context.Background(), StoreBusiness, "groups")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("groups rows = %d, want the single real row", count)
	}
}

// failingDomainEnv 用注入的域失败验证 Run 的失败路径。Run 的域表是包级变量，
// 这里用一个临时替换并立刻恢复的方式（测试串行执行，无并发写者）。
func TestRunReportsDomainFailure(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	original := domainSeeds
	domainSeeds = []struct {
		Name string
		Run  func(context.Context, *env) (DomainResult, error)
	}{
		{Name: DomainBusiness, Run: func(context.Context, *env) (DomainResult, error) {
			return DomainResult{}, errors.New("boom")
		}},
	}
	defer func() { domainSeeds = original }()

	report, err := Run(context.Background(), Options{Paths: paths, Days: 1, DailyRequests: 1})
	if err == nil {
		t.Fatal("a failing domain must fail the run")
	}
	if !strings.Contains(err.Error(), "mockdata 域 business 失败") {
		t.Fatalf("error = %v", err)
	}
	// 失败报告仍是结构完整的：清理计数与时间字段都在。
	if report.Days != 1 || report.FinishedAt == "" || report.DurationMs < 0 {
		t.Fatalf("failure report = %+v", report)
	}
	if report.Cleanup == nil || report.Counts == nil || report.Autofill == nil {
		t.Fatalf("failure report maps = %+v", report)
	}
}

// TestRunFillsDomainResultName 验证域返回空 Name 时按注册名补齐。
func TestRunFillsDomainResultName(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	original := domainSeeds
	domainSeeds = []struct {
		Name string
		Run  func(context.Context, *env) (DomainResult, error)
	}{
		{Name: DomainUsage, Run: func(context.Context, *env) (DomainResult, error) {
			return DomainResult{Counts: map[string]int{"usageRecords": 4}}, nil
		}},
	}
	defer func() { domainSeeds = original }()

	report, err := Run(context.Background(), Options{Paths: paths, Days: 1, DailyRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Domains) != 1 || report.Domains[0].Name != DomainUsage {
		t.Fatalf("domains = %+v", report.Domains)
	}
	if report.Counts["usageRecords"] != 4 {
		t.Fatalf("merged counts = %v", report.Counts)
	}
}

func TestRunSummaryRegistersUsersAndKeys(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	original := domainSeeds
	domainSeeds = []struct {
		Name string
		Run  func(context.Context, *env) (DomainResult, error)
	}{
		{Name: DomainBusiness, Run: func(_ context.Context, e *env) (DomainResult, error) {
			e.setOwner(mockOwner{ID: CleanupIDPrefix + "admin", Username: "admin", DisplayName: CleanupNamePrefix + "管理员用户"})
			e.addMockUser(mockUser{Name: "manager", ID: CleanupIDPrefix + "manager", Username: CleanupIDPrefix + "manager", Role: "manager", Status: "active", Password: MockUserPassword})
			e.addAPIKey(mockAPIKey{Name: "admin_main", ID: CleanupIDPrefix + "key_2", Key: "sk-" + CleanupIDPrefix + "local", Status: "active"})
			e.addAPIKey(mockAPIKey{Name: "admin_alt", ID: CleanupIDPrefix + "key_1", Key: "sk-" + CleanupIDPrefix + "alt", Status: "active"})
			return DomainResult{Name: DomainBusiness, Counts: map[string]int{"users": 1}}, nil
		}},
	}
	defer func() { domainSeeds = original }()

	report, err := Run(context.Background(), Options{Paths: paths, Days: 2, DailyRequests: 3})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(report.SummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	var document summaryDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if document.Owner.Username != "admin" {
		t.Fatalf("owner = %+v", document.Owner)
	}
	if len(document.MockUsers) != 1 || document.MockUsers[0].Password != MockUserPassword {
		t.Fatalf("mock users = %+v", document.MockUsers)
	}
	if len(document.APIKeys) != 2 {
		t.Fatalf("api keys = %+v", document.APIKeys)
	}
	// 摘要里的 Key 按 name 稳定排序，便于逐字节比对。
	if document.APIKeys[0].Name != "admin_alt" {
		t.Fatalf("api keys not sorted: %+v", document.APIKeys)
	}
	if document.Counts["users"] != 1 {
		t.Fatalf("counts = %v", document.Counts)
	}
}

func TestMergedDomainCountsAndSortedKeys(t *testing.T) {
	merged := mergedDomainCounts([]DomainResult{
		{Name: "a", Counts: map[string]int{"usageRecords": 2, "users": 1}},
		{Name: "b", Counts: map[string]int{"usageRecords": 3}},
	})
	if merged["usageRecords"] != 5 || merged["users"] != 1 {
		t.Fatalf("merged = %v", merged)
	}
	if len(mergedDomainCounts(nil)) != 0 {
		t.Fatal("merging nothing must yield an empty map")
	}
	keys := sortedKeys(map[string]int{"b": 1, "a": 2})
	if len(keys) != 2 || keys[0] != "a" {
		t.Fatalf("sortedKeys = %v", keys)
	}
}

func TestRunWithLoggerEmitsProgress(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	var sink strings.Builder
	logger := slog.New(slog.NewTextHandler(&sink, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if _, err := RunWithLogger(context.Background(), Options{Paths: paths, Days: 1, DailyRequests: 1}, logger); err != nil {
		t.Fatal(err)
	}
	// 日志器被接受并且不改变结果（骨架阶段没有进度日志，这里只钉住接口可用）。
	_ = sink.String()
}
