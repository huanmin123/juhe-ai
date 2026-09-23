package mockdata

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snapshotFixtureEnv 建一个只含 business 库 accounts 样本的临时数据根：让
// accounts.status.active 断言在域接线后可被评估并通过。返回解析后的 Paths，
// 供 VerifyPathsCoverage 与快照文件复用同一数据根。
func snapshotFixtureEnv(t *testing.T) (root string, paths Paths) {
	t.Helper()
	root = t.TempDir()
	resolved, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	paths = resolved
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	defer func() { _ = e.Close() }()
	createTestTable(t, e, StoreBusiness, `CREATE TABLE accounts (
		id TEXT PRIMARY KEY,
		status TEXT NOT NULL
	)`)
	if err := e.insertMap(context.Background(), StoreBusiness, "accounts", map[string]any{"id": "a1", "status": "active"}); err != nil {
		t.Fatal(err)
	}
	return root, paths
}

// writeSnapshotFixture 在数据根写一份手写的域接线快照（与 writeSummary 落盘的
// JSON 形状一致）。
func writeSnapshotFixture(t *testing.T, paths Paths, jsonBody string) {
	t.Helper()
	if err := os.WriteFile(mockdataSummaryPath(paths), []byte(jsonBody), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunSummaryCarriesDomainWiringSnapshot 验证 Run 把域接线快照写进摘要：
// wired 与 DomainResult.Counts 非空一一对应，未接线域的 counts 是空对象。
func TestRunSummaryCarriesDomainWiringSnapshot(t *testing.T) {
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
			return DomainResult{Name: DomainBusiness, Counts: map[string]int{"accounts": 2}}, nil
		}},
		{Name: DomainStats, Run: func(context.Context, *env) (DomainResult, error) {
			return DomainResult{Name: DomainStats}, nil
		}},
	}
	defer func() { domainSeeds = original }()

	report, err := Run(context.Background(), Options{Paths: paths, Days: 1, DailyRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(report.SummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	var document summaryDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("summary is not valid JSON: %v", err)
	}
	if len(document.Domains) != 2 {
		t.Fatalf("domains snapshot = %+v", document.Domains)
	}
	if document.Domains[0].Name != DomainBusiness || !document.Domains[0].Wired || len(document.Domains[0].Counts) == 0 {
		t.Fatalf("business entry = %+v, want wired with counts", document.Domains[0])
	}
	if document.Domains[1].Name != DomainStats || document.Domains[1].Wired || len(document.Domains[1].Counts) != 0 {
		t.Fatalf("stats entry = %+v, want unwired with empty counts", document.Domains[1])
	}
}

// TestVerifyPathsCoverageEvaluatesAssertionsWithSnapshot 验证跨进程断言修复的
// 两条路径：无快照时断言维持 NotCovered（向后兼容），有快照后业务域断言真正
// 被评估（满足的通过、不满足的进 Errors），未接线域的断言仍在 NotCovered。
func TestVerifyPathsCoverageEvaluatesAssertionsWithSnapshot(t *testing.T) {
	_, paths := snapshotFixtureEnv(t)
	options := Options{Paths: paths, Days: 1, DailyRequests: 1}

	// 路径 A：数据根还没有 mockdata-summary.json（造数未跑或旧实现），现状是
	// 全部 NotCovered。
	before, err := VerifyPathsCoverage(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !containsEntry(before.NotCovered, "accounts.status.active") {
		t.Fatalf("without a snapshot the assertion must stay not-covered: %v", before.NotCovered)
	}

	// 路径 B：造数进程留下的域接线快照把 business 标成已接线，断言被真正评估。
	writeSnapshotFixture(t, paths, `{"domains":[`+
		`{"name":"`+DomainBusiness+`","wired":true,"counts":{"accounts":1}},`+
		`{"name":"`+DomainStats+`","wired":false,"counts":{}}]}`)
	after, err := VerifyPathsCoverage(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if containsEntry(after.NotCovered, "accounts.status.active") {
		t.Fatalf("a wired domain assertion must be evaluated: %v", after.NotCovered)
	}
	// accounts.status.active 有满足行：既不在 NotCovered 也不在 Errors。
	if containsEntry(after.Errors, "accounts.status.active") {
		t.Fatalf("satisfied assertion must not fail: %v", after.Errors)
	}
	// 同域未满足的断言（route_strategies.mode_distribution 缺表）必须是硬失败。
	if !containsEntry(after.Errors, "route_strategies.mode_distribution") {
		t.Fatalf("unsatisfied wired assertion must fail: %v", after.Errors)
	}
	// 未接线域（stats）的断言维持 NotCovered。
	if !containsEntry(after.NotCovered, "background_task_runs") {
		t.Fatalf("unwired domain assertion must stay not-covered: %v", after.NotCovered)
	}
}

// TestVerifyPathsCoverageRejectsCorruptSummary 验证损坏的快照不会被静默忽略：
// 那会让覆盖门槛退化成全部 NotCovered。
func TestVerifyPathsCoverageRejectsCorruptSummary(t *testing.T) {
	_, paths := snapshotFixtureEnv(t)
	writeSnapshotFixture(t, paths, `{"domains": [broken`)
	if _, err := VerifyPathsCoverage(context.Background(), Options{Paths: paths, Days: 1, DailyRequests: 1}); err == nil {
		t.Fatal("a corrupt summary snapshot must fail the coverage command")
	}
}

// TestLoadWiredDomainSnapshotSemantics 钉住加载语义：缺文件返回空、wired 与空
// 计数矛盾的条目按未接线处理（wired 的定义就是 Counts 非空）。
func TestLoadWiredDomainSnapshotSemantics(t *testing.T) {
	paths, err := ResolvePaths(filepath.Join(t.TempDir(), "nowhere"), "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	wired, err := loadWiredDomainSnapshot(paths)
	if err != nil || wired != nil {
		t.Fatalf("missing summary = %v/%v, want nil/nil", wired, err)
	}

	_, paths = snapshotFixtureEnv(t)
	writeSnapshotFixture(t, paths, `{"domains":[`+
		`{"name":"`+DomainBusiness+`","wired":true,"counts":{}},`+
		`{"name":"`+DomainUsage+`","wired":true,"counts":{"usageRecords":3}}]}`)
	wired, err = loadWiredDomainSnapshot(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(wired) != 1 || wired[0].Name != DomainUsage || wired[0].Counts["usageRecords"] != 3 {
		t.Fatalf("wired = %+v, want only the usage domain", wired)
	}
}

func containsEntry(entries []string, needle string) bool {
	for _, entry := range entries {
		if strings.Contains(entry, needle) {
			return true
		}
	}
	return false
}
