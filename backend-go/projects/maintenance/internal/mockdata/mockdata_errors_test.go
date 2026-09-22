package mockdata

// 本文件用 fake driver 驱动真实 SQLite 无法复现的错误分支。每个用例只注入
// 一种错误，断言错误被包装后向上冒泡，而不是被静默吞掉。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errRollbackCause = errors.New("域逻辑失败")

// touchStore 让存储文件在磁盘上存在（openExisting 的前置条件）。
func touchStore(t *testing.T, e *env, name string) {
	t.Helper()
	item, err := e.store(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(item.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(item.Path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEnvOpenErrorBranches(t *testing.T) {
	// 落库目录被普通文件占住：MkdirAll 失败。
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	paths.Business = filepath.Join(blocker, "business.sqlite3")
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	defer func() { _ = e.Close() }()
	if _, err := e.open(StoreBusiness); err == nil || !strings.Contains(err.Error(), "创建 business 目录") {
		t.Fatalf("mkdir failure = %v", err)
	}
	if _, err := e.exec(context.Background(), StoreBusiness, "SELECT 1"); err == nil {
		t.Fatal("exec must propagate the mkdir failure")
	}
	if err := e.tx(context.Background(), StoreBusiness, func(*sql.Tx) error { return nil }); err == nil {
		t.Fatal("tx must propagate the mkdir failure")
	}

	// driver 层打开失败。
	installFakeDriver(t, &mockFakeScript{openErr: errMockFake})
	fake := fakeEnv(t)
	if _, err := fake.open(StoreBusiness); !errors.Is(err, errMockFake) {
		t.Fatalf("open failure = %v", err)
	}

	// PRAGMA journal_mode 失败：句柄必须被关闭且不缓存。
	installFakeDriver(t, &mockFakeScript{modeErr: errMockFake})
	fake = fakeEnv(t)
	if _, err := fake.open(StoreBusiness); !errors.Is(err, errMockFake) {
		t.Fatalf("pragma failure = %v", err)
	}
	if _, err := fake.open(StoreBusiness); !errors.Is(err, errMockFake) {
		t.Fatalf("pragma failure must repeat: %v", err)
	}

	// 文件存在但打开失败：openExisting 走 open 并冒泡。
	installFakeDriver(t, &mockFakeScript{openErr: errMockFake})
	fake = fakeEnv(t)
	touchStore(t, fake, StoreBusiness)
	if _, err := fake.openExisting(StoreBusiness); !errors.Is(err, errMockFake) {
		t.Fatalf("openExisting open failure = %v", err)
	}
}

func TestEnvOpenExistingStatError(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	defer func() { _ = e.Close() }()
	// NUL 字节让 os.Stat 返回 EINVAL（而不是 ErrNotExist）：这条分支不能退化成
	// 「文件不存在」的静默跳过。
	e.byName[StoreBusiness] = store{Name: StoreBusiness, Path: root + string(rune(0)) + "bad.sqlite3"}
	if _, err := e.openExisting(StoreBusiness); err == nil || !strings.Contains(err.Error(), "检查 business") {
		t.Fatalf("stat failure = %v", err)
	}
}

func TestEnvCloseErrorBranches(t *testing.T) {
	installFakeDriver(t, &mockFakeScript{closeErr: errMockFake})
	e := fakeEnv(t)
	if _, err := e.open(StoreChat); err != nil {
		t.Fatal(err)
	}
	if _, err := e.open(StoreStats); err != nil {
		t.Fatal(err)
	}
	err := e.Close()
	if err == nil || !strings.Contains(err.Error(), "关闭 mockdata 存储失败") {
		t.Fatalf("close failure = %v", err)
	}
	if !strings.Contains(err.Error(), "chat:") || !strings.Contains(err.Error(), "stats:") {
		t.Fatalf("close failure must list both stores: %v", err)
	}
	// 句柄表已清空，重复 Close 不再报错。
	if err := e.Close(); err != nil {
		t.Fatalf("second close = %v", err)
	}
}

func TestEnvStatementErrorBranches(t *testing.T) {
	ctx := context.Background()

	installFakeDriver(t, &mockFakeScript{execErr: errMockFake})
	e := fakeEnv(t)
	if _, err := e.exec(ctx, StoreChat, "INSERT INTO notes (id) VALUES (?)", "x"); !errors.Is(err, errMockFake) {
		t.Fatalf("exec failure = %v", err)
	}
	if err := e.insertMap(ctx, StoreChat, "notes", map[string]any{"id": "x"}); !errors.Is(err, errMockFake) {
		t.Fatalf("insertMap failure = %v", err)
	}
	if _, err := e.queryCount(ctx, StoreChat, "notes"); err == nil {
		t.Fatal("queryCount must propagate statement failures")
	}

	installFakeDriver(t, &mockFakeScript{})
	e = fakeEnv(t)
	if _, err := e.queryCount(ctx, "not-a-store", "notes"); err == nil {
		t.Fatal("queryCount on an unknown store must fail")
	}
	if _, err := e.tableColumns(ctx, "not-a-store", "notes"); err == nil {
		t.Fatal("tableColumns on an unknown store must fail")
	}
	if _, err := e.tableNames(ctx, "not-a-store"); err == nil {
		t.Fatal("tableNames on an unknown store must fail")
	}
	if _, err := e.existsTable(ctx, "not-a-store", "notes"); err == nil {
		t.Fatal("existsTable on an unknown store must fail")
	}
}

func TestEnvRowIterationErrorBranches(t *testing.T) {
	ctx := context.Background()
	tableColumnsColumns := []string{"cid", "name", "type", "notnull", "dflt_value", "pk"}
	tableNameColumns := []string{"name"}

	// fake driver 不落盘，所以先手工创建存储文件，否则 openExisting 会按
	// 「存储不存在」返回 nil（这是另一条分支，单独覆盖）。
	// Scan 类型错位（第一列是 int 位置却给字符串）。
	installFakeDriver(t, &mockFakeScript{queries: []mockFakeQuery{{
		match: "table_info",
		rows:  &mockFakeRows{columns: tableColumnsColumns, rows: [][]driver.Value{{"not-an-int", "id", "TEXT", 0, nil, 0}}},
	}}})
	e := fakeEnv(t)
	touchStore(t, e, StoreChat)
	if _, err := e.tableColumns(ctx, StoreChat, "notes"); err == nil {
		t.Fatal("a type-mismatched table_info row must fail the scan")
	}

	// NULL 落进 string 目标会失败（int64/bool/[]byte 都能被 convertAssign 转成
	// 字符串，实测如此），因此用 nil 触发 Scan 失败。
	installFakeDriver(t, &mockFakeScript{queries: []mockFakeQuery{{
		match: "name NOT LIKE",
		rows:  &mockFakeRows{columns: tableNameColumns, rows: [][]driver.Value{{nil}}},
	}}})
	e = fakeEnv(t)
	touchStore(t, e, StoreChat)
	if _, err := e.tableNames(ctx, StoreChat); err == nil {
		t.Fatal("a type-mismatched table name row must fail the scan")
	}

	// rows.Err：迭代到最后一行之后报错。
	installFakeDriver(t, &mockFakeScript{queries: []mockFakeQuery{{
		match: "table_info",
		rows:  &mockFakeRows{columns: tableColumnsColumns, nextErr: errMockFake},
	}}})
	e = fakeEnv(t)
	touchStore(t, e, StoreChat)
	if _, err := e.tableColumns(ctx, StoreChat, "notes"); !errors.Is(err, errMockFake) {
		t.Fatalf("table_info iteration failure = %v", err)
	}

	installFakeDriver(t, &mockFakeScript{queries: []mockFakeQuery{{
		match: "name NOT LIKE",
		rows:  &mockFakeRows{columns: tableNameColumns, nextErr: errMockFake},
	}}})
	e = fakeEnv(t)
	touchStore(t, e, StoreChat)
	if _, err := e.tableNames(ctx, StoreChat); !errors.Is(err, errMockFake) {
		t.Fatalf("table list iteration failure = %v", err)
	}

	// 查询阶段直接失败。
	installFakeDriver(t, &mockFakeScript{queryErr: errMockFake})
	e = fakeEnv(t)
	touchStore(t, e, StoreChat)
	if _, err := e.tableColumns(ctx, StoreChat, "notes"); !errors.Is(err, errMockFake) {
		t.Fatalf("query failure = %v", err)
	}
	if _, err := e.tableNames(ctx, StoreChat); !errors.Is(err, errMockFake) {
		t.Fatalf("table list query failure = %v", err)
	}
	if _, err := e.existsTable(ctx, StoreChat, "notes"); !errors.Is(err, errMockFake) {
		t.Fatalf("exists query failure = %v", err)
	}
	// 存储文件缺失的分支：openExisting 返回 nil，三个读取助手都返回零值。
	e = fakeEnv(t)
	if columns, err := e.tableColumns(ctx, StoreChat, "notes"); err != nil || columns != nil {
		t.Fatalf("missing store tableColumns = %v/%v", columns, err)
	}
	if names, err := e.tableNames(ctx, StoreChat); err != nil || names != nil {
		t.Fatalf("missing store tableNames = %v/%v", names, err)
	}
	if exists, err := e.existsTable(ctx, StoreChat, "notes"); err != nil || exists {
		t.Fatalf("missing store existsTable = %v/%v", exists, err)
	}
	if exists, err := e.storeExists(e.byName[StoreChat]); err != nil || exists {
		t.Fatalf("storeExists on a missing store = %v/%v", exists, err)
	}
}

func TestEnvTxErrorBranches(t *testing.T) {
	ctx := context.Background()

	installFakeDriver(t, &mockFakeScript{beginErr: errMockFake})
	e := fakeEnv(t)
	if err := e.tx(ctx, StoreChat, func(*sql.Tx) error { return nil }); err == nil {
		t.Fatal("begin failure must fail")
	}

	installFakeDriver(t, &mockFakeScript{commitErr: errMockFake})
	e = fakeEnv(t)
	if err := e.tx(ctx, StoreChat, func(*sql.Tx) error { return nil }); !errors.Is(err, errMockFake) {
		t.Fatalf("commit failure = %v", err)
	}

	installFakeDriver(t, &mockFakeScript{rollbackErr: errMockFake})
	e = fakeEnv(t)
	err := e.tx(ctx, StoreChat, func(*sql.Tx) error { return errRollbackCause })
	if !errors.Is(err, errRollbackCause) || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("rollback failure = %v", err)
	}
}

func TestDeleteIfTableExistsRowsAffectedError(t *testing.T) {
	installFakeDriver(t, &mockFakeScript{
		rowsAffectedErr: true,
		queries: []mockFakeQuery{{
			match: "sqlite_master",
			rows:  &mockFakeRows{columns: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(1)}}},
		}},
	})
	e := fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	if _, err := deleteIfTableExists(context.Background(), e, e.byName[StoreBusiness], "t", "DELETE FROM t", nil); !errors.Is(err, errMockFake) {
		t.Fatalf("RowsAffected failure = %v", err)
	}
}

func TestCleanupAndCoveragePropagateDriverErrors(t *testing.T) {
	ctx := context.Background()
	// 语句失败：清理的删除语句与自动补全的插入都会冒泡到调用方。
	installFakeDriver(t, &mockFakeScript{
		execErr: errMockFake,
		queries: []mockFakeQuery{{
			match: "sqlite_master WHERE type='table' AND name = ?",
			rows:  &mockFakeRows{columns: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(1)}}},
		}},
	})
	e := fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	if _, _, err := cleanupAll(ctx, e); !errors.Is(err, errMockFake) {
		t.Fatalf("cleanupAll failure = %v", err)
	}

	// 表清单可读、计数失败：VerifyCoverage 与 sweep 的第二级错误。
	installFakeDriver(t, &mockFakeScript{
		queries: []mockFakeQuery{
			{match: "name NOT LIKE", rows: &mockFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"t"}}}},
			{match: "table_info", rows: &mockFakeRows{columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk"}, nextErr: errMockFake}},
			{match: "sqlite_master WHERE type='table' AND name = ?", rows: &mockFakeRows{columns: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(1)}}}},
		},
	})
	e = fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	if _, err := sweepCleanupMarkers(ctx, e); !errors.Is(err, errMockFake) {
		t.Fatalf("sweepCleanupMarkers failure = %v", err)
	}
	if _, err := VerifyCoverage(ctx, e); err == nil {
		t.Fatal("VerifyCoverage must propagate per-table failures")
	}
}

func TestAutofillAndRunPropagateDriverErrors(t *testing.T) {
	ctx := context.Background()

	// 表清单查询失败：autofillTables 必须整体失败（不是逐表降级）。
	installFakeDriver(t, &mockFakeScript{queryErr: errMockFake, queryErrMatch: "name NOT LIKE"})
	e := fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	if _, _, err := autofillTables(ctx, e); !errors.Is(err, errMockFake) {
		t.Fatalf("autofillTables enumeration failure = %v", err)
	}

	// Run 的清理阶段失败。
	installFakeDriver(t, &mockFakeScript{queryErr: errMockFake})
	e = fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	paths := e.options.Paths
	if _, err := Run(ctx, Options{Paths: paths, Days: 1, DailyRequests: 1}); err == nil {
		t.Fatal("Run must fail when cleanup fails")
	}

	// Run 的自动补全阶段失败：清理阶段只能查 sqlite_master 存在性（脚本化成功），
	// 表清单查询失败。
	installFakeDriver(t, &mockFakeScript{
		queryErr:      errMockFake,
		queryErrMatch: "name NOT LIKE",
		queries: []mockFakeQuery{{
			match: "sqlite_master WHERE type='table' AND name = ?",
			rows:  &mockFakeRows{columns: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(0)}}},
		}},
	})
	e = fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	if _, err := Run(ctx, Options{Paths: e.options.Paths, Days: 1, DailyRequests: 1}); err == nil {
		t.Fatal("Run must fail when autofill fails")
	}
}

func TestRunFailsWhenSummaryCannotBeWritten(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	// 数据根是普通文件：摘要目录创建失败，Run 在写摘要阶段返回错误。
	fileRoot := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := paths
	broken.DataDir = fileRoot
	if _, err := Run(context.Background(), Options{Paths: broken, Days: 1, DailyRequests: 1}); err == nil {
		t.Fatal("Run must fail when the summary cannot be written")
	}
	// 数据根存在但摘要路径被目录占住：WriteFile 失败。
	occupied := paths
	occupied.DataDir = filepath.Join(t.TempDir(), "occupied")
	if err := os.MkdirAll(filepath.Join(occupied.DataDir, SummaryFilename), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{Paths: occupied, Days: 1, DailyRequests: 1}); err == nil {
		t.Fatal("Run must fail when the summary file path is occupied")
	}
}

func TestRunCloseFailureIsReportedAsWarning(t *testing.T) {
	// 清理阶段必须真的打开一个句柄（业务库文件存在），否则 Close 无事可做。
	installFakeDriver(t, &mockFakeScript{
		closeErr: errMockFake,
		queries: []mockFakeQuery{
			{match: "sqlite_master WHERE type='table' AND name = ?", rows: &mockFakeRows{columns: []string{"COUNT(*)"}, rows: [][]driver.Value{{int64(0)}}}},
			{match: "name NOT LIKE", rows: &mockFakeRows{columns: []string{"name"}}},
		},
	})
	e := fakeEnv(t)
	touchStore(t, e, StoreBusiness)
	report, err := Run(context.Background(), Options{Paths: e.options.Paths, Days: 1, DailyRequests: 1})
	if err != nil {
		t.Fatalf("a close failure must not fail the run: %v", err)
	}
	if len(report.Warnings) == 0 || !strings.Contains(report.Warnings[0], "关闭 mockdata 存储失败") {
		t.Fatalf("warnings = %v", report.Warnings)
	}
}

func TestOpenSQLiteDatabaseSeamIsRestored(t *testing.T) {
	// 未安装 fake driver 时默认打开函数必须仍是真实 SQLite：防止上一用例的注入
	// 泄漏到后续用例。
	e := testEnv(t)
	createTestTable(t, e, StoreChat, `CREATE TABLE real_driver_check (id TEXT PRIMARY KEY)`)
	if err := e.insertMap(context.Background(), StoreChat, "real_driver_check", map[string]any{"id": "x"}); err != nil {
		t.Fatal(err)
	}
	if count, err := e.queryCount(context.Background(), StoreChat, "real_driver_check"); err != nil || count != 1 {
		t.Fatalf("real driver round trip = %d/%v", count, err)
	}
}
