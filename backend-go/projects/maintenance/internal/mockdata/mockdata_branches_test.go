package mockdata

// 本文件补齐剩余分支：nil getenv、摘要的空集合兜底、自动补全的可空带默认值列
// 与空赋值集、CHECK 约束解析的重复列、通用清理的删除计数、覆盖校验的
// VerifyPathsCoverage 成功路径与断言查询错误路径。
//
// 覆盖率（go test -cover，2026-09-22 骨架落地实测）为 98.8%，剩余 8 条语句按
// 「设计上不可达 / 需要权限或竞态」登记如下，未用测试硬凑：
//   - autofill.go:54（e.open 失败）：表清单来自同一句柄，句柄能打开时这里必然
//     命中缓存，除非并发关闭，属竞态。
//   - autofill.go:181（sqlite_master.sql 为 NULL）：PRAGMA/类型过滤只列普通表，
//     普通表的建表文本不会为 NULL。
//   - autofill.go:186（正则分组不足）：checkInPattern 固定三个捕获组，长度检查
//     是防御性写法。
//   - env.go:74（存储名重复）：allStores() 的名字由固定表 + 分片序号构造，天然
//     唯一，保留是为了让新增存储时不会悄悄覆盖同名项。
//   - env.go:108（分片桶目录不可读）：需要文件系统权限变化，测试无法稳定构造。
//   - mockdata.go:193（自动补全阶段失败）：Run 的清理阶段用同一批存储与同一套
//     枚举语句，注入的失败总在清理阶段先触发；该分支由 autofillTables 单测
//     （TestAutofillPropagatesCheckQueryError 等）在函数层覆盖。
//   - summary.go:148（MarshalIndent 失败）：摘要只含字符串、整数与 map，无不可
//     序列化字段。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolvePathsAcceptsNilGetenv(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Business != filepath.Join(root, "business.sqlite3") {
		t.Fatalf("business = %q", paths.Business)
	}
	if paths.CodexContextShardCount != defaultShardCount {
		t.Fatalf("shard count = %d", paths.CodexContextShardCount)
	}
}

func TestWriteSummaryFillsEmptyCollections(t *testing.T) {
	e := testEnv(t)
	path, err := writeSummary(e, Report{Counts: nil})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(e.options.Paths.DataDir, SummaryFilename) {
		t.Fatalf("summary path = %q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document summaryDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	// 空集合必须序列化成 [] 与 {}，而不是 null：页面对工具读取摘要时
	// null 与空集合的处理不一致。
	if document.MockUsers == nil || document.APIKeys == nil || document.Counts == nil {
		t.Fatalf("summary must normalise nil collections: %+v", document)
	}
	if !strings.Contains(string(raw), "\"mockUsers\": []") {
		t.Fatalf("mockUsers should be an empty array:\n%s", raw)
	}
}

func TestFinishReportNormalisesNilMaps(t *testing.T) {
	options := Options{Days: 1, DailyRequests: 1, Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	report := finishReport(Report{}, options, time.Now())
	if report.Cleanup == nil || report.Counts == nil || report.Autofill == nil {
		t.Fatalf("maps = %+v", report)
	}
	if report.Domains == nil {
		t.Fatal("domains must be normalised to an empty slice")
	}
	if report.FinishedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("finishedAt = %q", report.FinishedAt)
	}
}

func TestAutofillSkipsNullableColumnsWithDefaults(t *testing.T) {
	e := testEnv(t)
	// note 可空且有默认值 → 必须留给数据库默认值；只有 NOT NULL 的 label 参与赋值。
	createTestTable(t, e, StoreBusiness, `CREATE TABLE defaults_mix (
		id TEXT PRIMARY KEY,
		label TEXT NOT NULL,
		note TEXT DEFAULT 'db-default',
		amount INTEGER DEFAULT 7
	)`)
	if _, skipped, err := autofillTables(context.Background(), e); err != nil {
		t.Fatal(err)
	} else if skipped[StoreBusiness+".defaults_mix"] != "" {
		t.Fatalf("defaults_mix should not be skipped: %v", skipped[StoreBusiness+".defaults_mix"])
	}
	var note string
	var amount int
	if err := e.opened[StoreBusiness].QueryRow(`SELECT note, amount FROM defaults_mix LIMIT 1`).Scan(&note, &amount); err != nil {
		t.Fatal(err)
	}
	if note != "db-default" || amount != 7 {
		t.Fatalf("nullable defaults were overwritten: note=%q amount=%d", note, amount)
	}
}

func TestAutofillRecordsTableWithoutInsertableColumns(t *testing.T) {
	e := testEnv(t)
	// 只有 INTEGER PRIMARY KEY（rowid 别名）：没有可插入的必填列，必须记原因跳过。
	createTestTable(t, e, StoreBusiness, `CREATE TABLE only_rowid (seq INTEGER PRIMARY KEY)`)
	inserted, skipped, err := autofillTables(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inserted[StoreBusiness+".only_rowid"]; ok {
		t.Fatalf("only_rowid must not be filled: %v", inserted)
	}
	if !strings.Contains(skipped[StoreBusiness+".only_rowid"], "没有必填列") {
		t.Fatalf("only_rowid skip reason = %q", skipped[StoreBusiness+".only_rowid"])
	}
}

func TestQueryCheckInValuesKeepsFirstConstraintPerColumn(t *testing.T) {
	e := testEnv(t)
	// 同一列写两条 CHECK IN：解析必须保留第一条，避免后面的约束覆盖前面。
	createTestTable(t, e, StoreBusiness, `CREATE TABLE double_check (
		id TEXT PRIMARY KEY,
		status TEXT NOT NULL CHECK (status IN ('first', 'second')) CHECK (status IN ('other'))
	)`)
	checks, err := queryCheckInValues(context.Background(), e.opened[StoreBusiness], "double_check")
	if err != nil {
		t.Fatal(err)
	}
	if len(checks["status"]) != 2 || checks["status"][0] != "first" {
		t.Fatalf("status checks = %v", checks["status"])
	}
}

func TestCleanupAllRecordsSweepDeletions(t *testing.T) {
	e := testEnv(t)
	ctx := context.Background()
	// 这张表没有显式清理规则，只有通用扫描能命中；同时混入真实行。
	createTestTable(t, e, StoreBusiness, `CREATE TABLE account_name_search_documents (
		id TEXT PRIMARY KEY,
		document TEXT
	)`)
	for _, id := range []string{CleanupIDPrefix + "doc_a", CleanupIDPrefix + "doc_b"} {
		if err := e.insertMap(ctx, StoreBusiness, "account_name_search_documents", map[string]any{"id": id, "document": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.insertMap(ctx, StoreBusiness, "account_name_search_documents", map[string]any{"id": "real-doc", "document": "y"}); err != nil {
		t.Fatal(err)
	}
	deleted, _, err := cleanupAll(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if deleted[StoreBusiness+".account_name_search_documents"] != 2 {
		t.Fatalf("sweep deletions not recorded: %v", deleted)
	}
	if count, err := e.queryCount(ctx, StoreBusiness, "account_name_search_documents"); err != nil || count != 1 {
		t.Fatalf("rows left = %d/%v", count, err)
	}
}

func TestCleanupSweepPropagatesStatError(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	defer func() { _ = e.Close() }()
	e.byName[StoreBusiness] = store{Name: StoreBusiness, Path: root + string(rune(0)) + "bad.sqlite3"}
	if _, err := sweepCleanupMarkers(context.Background(), e); err == nil {
		t.Fatal("a stat failure must abort the sweep instead of being skipped")
	}
	if _, _, err := cleanupAll(context.Background(), e); err == nil {
		t.Fatal("cleanupAll must propagate the sweep failure")
	}
}

func TestVerifyPathsCoverageSuccessPath(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	// 空的临时数据根：所有存储缺失 → 报告未就绪，但调用本身成功（不返回错误），
	// 且句柄在返回前关闭。
	report, err := VerifyPathsCoverage(context.Background(), Options{Paths: paths, Days: 1, DailyRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatal("an empty data root cannot be ready")
	}
	if len(report.Errors) == 0 {
		t.Fatal("missing stores must be reported as errors")
	}
}

func TestVerifyCoveragePropagatesStoreAndTableErrors(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	defer func() { _ = e.Close() }()
	// 存储路径带 NUL：storeExists 的 os.Stat 报错，必须冒泡而不是当成「不存在」。
	e.byName[StoreBusiness] = store{Name: StoreBusiness, Path: root + string(rune(0)) + "bad.sqlite3"}
	if _, err := VerifyCoverage(ctx, e); err == nil {
		t.Fatal("a stat failure must fail the coverage run")
	}

	// 表清单失败（查询注入）。
	installFakeDriver(t, &mockFakeScript{queryErr: errMockFake, queryErrMatch: "name NOT LIKE"})
	e = fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	if _, err := VerifyCoverage(ctx, e); err == nil {
		t.Fatal("a table-list failure must fail the coverage run")
	}
}

// TestAssertionCountsMissingDataAsZero 走真实 SQLite：目标存储/表缺失时断言按
// 0 行计（域已接线但数据还没写，就是要失败）。
func TestAssertionCountsMissingDataAsZero(t *testing.T) {
	ctx := context.Background()
	plain := testEnv(t)
	plain.recordDomainResult(DomainResult{Name: DomainStats, Counts: map[string]int{"runs": 1}})
	_, failures := evaluateAssertions(ctx, plain)
	if !strings.Contains(strings.Join(failures, "\n"), "background_task_runs: 命中 0 行") {
		t.Fatalf("failures = %v", failures)
	}
	// 存储存在但目标表不存在：existsTable 分支同样按 0 行处理。
	createTestTable(t, plain, StoreStats, `CREATE TABLE unrelated (id TEXT PRIMARY KEY)`)
	if _, failures = evaluateAssertions(ctx, plain); !strings.Contains(strings.Join(failures, "\n"), "background_task_runs: 命中 0 行") {
		t.Fatalf("failures = %v", failures)
	}
}

// TestAssertionQueryErrorPaths 走 fake driver：查询失败必须是「断言失败」而不是
// 静默通过。
func TestAssertionQueryErrorPaths(t *testing.T) {
	ctx := context.Background()
	installFakeDriver(t, &mockFakeScript{queryErr: errMockFake})
	e := fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	e.recordDomainResult(DomainResult{Name: DomainBusiness, Counts: map[string]int{"accounts": 1}})
	notCovered, failures := evaluateAssertions(ctx, e)
	if len(notCovered) == 0 {
		t.Fatal("unwired domains must still be listed")
	}
	if len(failures) == 0 {
		t.Fatal("wired domain assertions must fail loudly when the query fails")
	}
	if !strings.Contains(strings.Join(failures, "\n"), "accounts.status.active") {
		t.Fatalf("failures = %v", failures)
	}

	// 表存在但计数查询失败：断言按失败记录（不是静默通过）。
	// 这里不能注入 exec 失败：打开句柄时的 PRAGMA 会先失败，覆盖不到计数分支。
	installFakeDriver(t, &mockFakeScript{
		queries: []mockFakeQuery{
			{match: "sqlite_master WHERE type='table' AND name = ?", rows: &mockFakeRows{columns: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(1)}}}},
		},
	})
	broken := fakeEnv(t)
	touchStore(t, broken, StoreStats)
	broken.recordDomainResult(DomainResult{Name: DomainStats, Counts: map[string]int{"runs": 1}})
	if _, failures = evaluateAssertions(ctx, broken); len(failures) == 0 {
		t.Fatal("a failing count query must surface as an assertion failure")
	} else if !strings.Contains(strings.Join(failures, "\n"), "background_task_runs") {
		t.Fatalf("failures = %v", failures)
	}
}

// TestOpenDatabaseFailureSurfaces 直接替换打开函数：database/sql 的驱动级打开
// 失败会推迟到首次使用，只有这一层替换才能覆盖「打开句柄失败」的分支。
func TestOpenDatabaseFailureSurfaces(t *testing.T) {
	previous := openSQLiteDatabase
	openSQLiteDatabase = func(string) (*sql.DB, error) { return nil, errMockFake }
	t.Cleanup(func() { openSQLiteDatabase = previous })

	e := fakeEnv(t)
	if _, err := e.open(StoreBusiness); !errors.Is(err, errMockFake) {
		t.Fatalf("open failure = %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("nothing was opened, so Close must succeed: %v", err)
	}
}

// TestVerifyPathsCoverageCloseFailure 覆盖「校验完成后关闭句柄失败」：句柄已经
// 用真实 SQLite 打开，关闭失败由 driver 注入。
func TestVerifyPathsCoverageCloseFailure(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	// fake driver 不落盘，因此业务库文件要先真实存在，让校验真的打开一个句柄。
	if err := os.WriteFile(paths.Business, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	installFakeDriver(t, &mockFakeScript{
		closeErr: errMockFake,
		queries: []mockFakeQuery{
			{match: "name NOT LIKE", rows: &mockFakeRows{columns: []string{"name"}}},
		},
	})
	if _, err := VerifyPathsCoverage(context.Background(), Options{Paths: paths, Days: 1, DailyRequests: 1}); !errors.Is(err, errMockFake) {
		t.Fatalf("close failure must surface: %v", err)
	}
}

// TestAutofillPropagatesCheckQueryError 覆盖「CHECK 约束解析查询失败」分支：
// 单表降级并记录原因，不影响其它表。
func TestAutofillPropagatesCheckQueryError(t *testing.T) {
	installFakeDriver(t, &mockFakeScript{
		queryErr:      errMockFake,
		queryErrMatch: "SELECT sql FROM sqlite_master",
		queries: []mockFakeQuery{
			{match: "name NOT LIKE", rows: &mockFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"t"}}}},
			{match: "table_info", rows: &mockFakeRows{columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk"}, rows: [][]driver.Value{{int64(0), "id", "TEXT", 1, nil, int64(1)}}}},
		},
	})
	e := fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	inserted, skipped, err := autofillTables(context.Background(), e)
	if err != nil {
		t.Fatalf("per-table failures must degrade, not abort: %v", err)
	}
	if _, ok := inserted[StoreBusiness+".t"]; ok {
		t.Fatalf("the table cannot be filled without its CHECK constraints: %v", inserted)
	}
	if !strings.Contains(skipped[StoreBusiness+".t"], "mockdata fake driver") {
		t.Fatalf("skip reason = %q", skipped[StoreBusiness+".t"])
	}
}

// TestSweepEnumerationAndColumnErrors 覆盖通用清理的表清单/列信息失败分支。
func TestSweepEnumerationAndColumnErrors(t *testing.T) {
	ctx := context.Background()

	installFakeDriver(t, &mockFakeScript{
		queryErr:      errMockFake,
		queryErrMatch: "name NOT LIKE",
		queries: []mockFakeQuery{{
			match: "sqlite_master WHERE type='table' AND name = ?",
			rows:  &mockFakeRows{columns: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(0)}}},
		}},
	})
	e := fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	if _, err := sweepCleanupMarkers(ctx, e); !errors.Is(err, errMockFake) {
		t.Fatalf("table list failure = %v", err)
	}
	// 显式清理规则先跑（存在性查询脚本化为「表不存在」），随后通用扫描在表清单
	// 查询上失败并冒泡。
	if _, _, err := cleanupAll(ctx, e); !errors.Is(err, errMockFake) {
		t.Fatalf("cleanupAll table list failure = %v", err)
	}

	installFakeDriver(t, &mockFakeScript{
		queryErr:      errMockFake,
		queryErrMatch: "table_info",
		queries:       []mockFakeQuery{{match: "name NOT LIKE", rows: &mockFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"t"}}}}},
	})
	e = fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	if _, err := sweepCleanupMarkers(ctx, e); !errors.Is(err, errMockFake) {
		t.Fatalf("column info failure = %v", err)
	}

	// 删除语句失败与 RowsAffected 失败：只注入 DELETE 或结果行数，避免影响
	// 打开句柄时的 PRAGMA。两种注入必须互斥，否则 DELETE 会先失败而走不到
	// RowsAffected。
	sweepScript := func(rowsAffectedErr bool) *mockFakeScript {
		script := &mockFakeScript{
			queries: []mockFakeQuery{
				{match: "name NOT LIKE", rows: &mockFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"t"}}}},
				{match: "table_info", rows: &mockFakeRows{columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk"}, rows: [][]driver.Value{{int64(0), "id", "TEXT", 1, nil, int64(1)}}}},
			},
		}
		if rowsAffectedErr {
			script.rowsAffectedErr = true
		} else {
			script.execErr = errMockFake
			script.execErrMatch = "DELETE FROM"
		}
		return script
	}
	installFakeDriver(t, sweepScript(false))
	e = fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	if _, err := sweepCleanupMarkers(ctx, e); !errors.Is(err, errMockFake) {
		t.Fatalf("delete failure = %v", err)
	}
	installFakeDriver(t, sweepScript(true))
	e = fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	if _, err := sweepCleanupMarkers(ctx, e); !errors.Is(err, errMockFake) {
		t.Fatalf("RowsAffected failure = %v", err)
	}
}

// TestAssertionStoreOpenFailure 覆盖断言目标存储打开失败（统计句柄不受影响，
// 失败必须记成断言失败而不是静默 0）。
func TestAssertionStoreOpenFailure(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	defer func() { _ = e.Close() }()
	e.byName[StoreStats] = store{Name: StoreStats, Path: root + string(rune(0)) + "bad.sqlite3"}
	e.recordDomainResult(DomainResult{Name: DomainStats, Counts: map[string]int{"runs": 1}})
	_, failures := evaluateAssertions(context.Background(), e)
	if !strings.Contains(strings.Join(failures, "\n"), "background_task_runs") {
		t.Fatalf("failures = %v", failures)
	}
}

// TestAutofillColumnErrorsCoverZeroColumnAndQueryFailure 覆盖自动补全的
// 「列信息查询失败」与「表零列」两个分支。
func TestAutofillColumnErrorsCoverZeroColumnAndQueryFailure(t *testing.T) {
	ctx := context.Background()

	// 列信息查询失败。
	installFakeDriver(t, &mockFakeScript{
		queryErr:      errMockFake,
		queryErrMatch: "table_info",
		queries:       []mockFakeQuery{{match: "name NOT LIKE", rows: &mockFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"t"}}}}},
	})
	e := fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	if _, skipped, err := autofillTables(ctx, e); err != nil {
		t.Fatalf("per-table failure must degrade: %v", err)
	} else if !strings.Contains(skipped[StoreBusiness+".t"], "mockdata fake driver") {
		t.Fatalf("skip reason = %q", skipped[StoreBusiness+".t"])
	}

	// 表在 sqlite_master 里但 PRAGMA 返回零列（防御分支）。
	installFakeDriver(t, &mockFakeScript{queries: []mockFakeQuery{
		{match: "name NOT LIKE", rows: &mockFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"t"}}}},
		{match: "table_info", rows: &mockFakeRows{columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk"}}},
	}})
	e = fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	_, skipped, err := autofillTables(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(skipped[StoreBusiness+".t"], "没有可读列信息") {
		t.Fatalf("skip reason = %q", skipped[StoreBusiness+".t"])
	}
}
