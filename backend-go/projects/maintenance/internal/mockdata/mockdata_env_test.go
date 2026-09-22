package mockdata

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// testEnv 构建一个指向临时数据根的存储上下文；调用方负责 Close（t.Cleanup
// 已登记）。
func testEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: DefaultDays, DailyRequests: DefaultDailyRequests}, nil)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// createTestTable 在建好文件的存储里建一张最小表。
func createTestTable(t *testing.T, e *env, storeName, ddl string) {
	t.Helper()
	if _, err := e.exec(context.Background(), storeName, ddl); err != nil {
		t.Fatalf("create table in %s: %v", storeName, err)
	}
}

func TestEnvOpenIsLazyCachingAndCreating(t *testing.T) {
	e := testEnv(t)

	// 打开前文件不存在：openExisting 返回 nil 而不是创建。
	db, err := e.openExisting(StoreBusiness)
	if err != nil {
		t.Fatal(err)
	}
	if db != nil {
		t.Fatal("openExisting on a missing store should return nil")
	}
	if _, statErr := os.Stat(e.byName[StoreBusiness].Path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("openExisting created the file: %v", statErr)
	}

	opened, err := e.open(StoreBusiness)
	if err != nil {
		t.Fatal(err)
	}
	if opened == nil {
		t.Fatal("open returned nil handle")
	}
	// 目录与文件都被创建（open 负责 mkdir + create）。
	if _, statErr := os.Stat(e.byName[StoreBusiness].Path); statErr != nil {
		t.Fatalf("open did not create the file: %v", statErr)
	}
	again, err := e.open(StoreBusiness)
	if err != nil {
		t.Fatal(err)
	}
	if again != opened {
		t.Fatal("open should reuse the cached handle")
	}
	if cached, err := e.openExisting(StoreBusiness); err != nil || cached != opened {
		t.Fatalf("openExisting after open = %v/%v", cached, err)
	}
	if _, err := e.open("not-a-store"); err == nil {
		t.Fatal("unknown store should fail")
	}
}

func TestEnvTableHelpersAndInsert(t *testing.T) {
	e := testEnv(t)
	ctx := context.Background()
	createTestTable(t, e, StoreChat, `CREATE TABLE notes (
		id TEXT PRIMARY KEY,
		body TEXT NOT NULL,
		flag INTEGER NOT NULL DEFAULT 0
	)`)
	createTestTable(t, e, StoreChat, `CREATE TABLE other (id TEXT PRIMARY KEY)`)

	exists, err := e.existsTable(ctx, StoreChat, "notes")
	if err != nil || !exists {
		t.Fatalf("existsTable(notes) = %v/%v", exists, err)
	}
	if exists, err := e.existsTable(ctx, StoreChat, "ghost"); err != nil || exists {
		t.Fatalf("existsTable(ghost) = %v/%v", exists, err)
	}
	if exists, err := e.existsTable(ctx, StoreBusiness, "notes"); err != nil || exists {
		t.Fatalf("existsTable on a missing store = %v/%v", exists, err)
	}

	names, err := e.tableNames(ctx, StoreChat)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "notes" || names[1] != "other" {
		t.Fatalf("tableNames = %v", names)
	}
	if names, err := e.tableNames(ctx, StoreBusiness); err != nil || names != nil {
		t.Fatalf("tableNames on a missing store = %v/%v", names, err)
	}

	columns, err := e.tableColumns(ctx, StoreChat, "notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 3 {
		t.Fatalf("columns = %+v", columns)
	}
	// SQLite 的 TEXT PRIMARY KEY 不隐含 NOT NULL（只有 INTEGER PRIMARY KEY 的
	// rowid 别名才有隐式约束），这里按真实语义断言，autofill 的主键取值分支
	// 依赖的正是 PrimaryKey 而不是 NotNull。
	if columns[0].Name != "id" || columns[0].PrimaryKey != 1 || columns[0].Type != "TEXT" {
		t.Fatalf("id column = %+v", columns[0])
	}
	if columns[1].Name != "body" || !columns[1].NotNull || columns[1].PrimaryKey != 0 {
		t.Fatalf("body column = %+v", columns[1])
	}
	if columns[2].DefaultValue != "0" {
		t.Fatalf("flag default = %#v", columns[2].DefaultValue)
	}
	if columns, err := e.tableColumns(ctx, StoreBusiness, "notes"); err != nil || columns != nil {
		t.Fatalf("tableColumns on a missing store = %v/%v", columns, err)
	}
	// PRAGMA table_info 对不存在的表返回空结果集而不是错误（SQLite 语义），
	// 因此缺失表走「零列」分支。
	if columns, err := e.tableColumns(ctx, StoreChat, "ghost"); err != nil || len(columns) != 0 {
		t.Fatalf("tableColumns(ghost) = %v/%v", columns, err)
	}

	// insertMap 列序排序后拼 SQL，且拒绝空列集。
	if err := e.insertMap(ctx, StoreChat, "notes", map[string]any{"id": "n-missing-body"}); err == nil {
		t.Fatal("insertMap without the NOT NULL body column should fail")
	}
	if err := e.insertMap(ctx, StoreChat, "notes", map[string]any{}); err == nil {
		t.Fatal("insertMap with no columns should fail")
	}
	if err := e.insertMap(ctx, StoreChat, "notes", map[string]any{"id": "n1", "body": "hello", "flag": 1}); err != nil {
		t.Fatal(err)
	}
	count, err := e.queryCount(ctx, StoreChat, "notes")
	if err != nil || count != 1 {
		t.Fatalf("queryCount = %d/%v", count, err)
	}
	if _, err := e.queryCount(ctx, StoreChat, "ghost"); err == nil {
		t.Fatal("queryCount on a missing table should fail")
	}
	if _, err := e.exec(ctx, StoreChat, `INSERT INTO notes (id, body) VALUES (?, ?)`, "n2", "second"); err != nil {
		t.Fatal(err)
	}
	if count, err = e.queryCount(ctx, StoreChat, "notes"); err != nil || count != 2 {
		t.Fatalf("queryCount = %d/%v", count, err)
	}
	if _, err := e.exec(ctx, "not-a-store", "SELECT 1"); err == nil {
		t.Fatal("exec on an unknown store should fail")
	}
}

func TestEnvTxCommitAndRollback(t *testing.T) {
	e := testEnv(t)
	ctx := context.Background()
	createTestTable(t, e, StoreStats, `CREATE TABLE samples (id TEXT PRIMARY KEY, amount INTEGER NOT NULL)`)

	if err := e.tx(ctx, StoreStats, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO samples (id, amount) VALUES ('a', 1)`); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := e.queryCount(ctx, StoreStats, "samples"); err != nil || count != 1 {
		t.Fatalf("after commit count = %d/%v", count, err)
	}

	sentinel := errors.New("domain failure")
	err := e.tx(ctx, StoreStats, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO samples (id, amount) VALUES ('b', 2)`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("tx error = %v, want the sentinel", err)
	}
	if count, err := e.queryCount(ctx, StoreStats, "samples"); err != nil || count != 1 {
		t.Fatalf("after rollback count = %d/%v", count, err)
	}
	if err := e.tx(ctx, "not-a-store", func(*sql.Tx) error { return nil }); err == nil {
		t.Fatal("tx on an unknown store should fail")
	}
	// fn 返回错误且事务已被外部结束：回滚失败不能再掩盖原始错误。
	if err := e.tx(ctx, StoreStats, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO samples (id, amount) VALUES ('c', 3)`); err != nil {
			return err
		}
		if err := tx.Rollback(); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("tx double rollback error = %v", err)
	}
	// 语句本身失败（表不存在）也要走回滚路径。
	if err := e.tx(ctx, StoreStats, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO ghost (id) VALUES ('x')`)
		return err
	}); err == nil {
		t.Fatal("tx with a failing statement should return the error")
	}
}

func TestEnvCloseClosesHandles(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	if _, err := e.open(StoreBusiness); err != nil {
		t.Fatal(err)
	}
	db := e.opened[StoreBusiness]
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if len(e.opened) != 0 {
		t.Fatal("Close should drop the handle table")
	}
	if err := db.Ping(); err == nil {
		t.Fatal("the underlying handle should be closed")
	}
	// 幂等：再次 Close 无错误。
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEnvUsageShardDiscovery(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	shardDir := filepath.Join(paths.UsageShardRoot, "20260101")
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"s00.sqlite3", "s01.sqlite3", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(shardDir, name), []byte{}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 非目录条目与无扩展名的文件都不算分片。
	if err := os.WriteFile(filepath.Join(paths.UsageShardRoot, "stray.sqlite3"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	stores := paths.usageShardStores()
	if len(stores) != 2 {
		t.Fatalf("usage shards = %v, want 2", stores)
	}
	if stores[0].Name != StoreUsageShardPrefix+"[20260101/s00]" {
		t.Fatalf("shard name = %q", stores[0].Name)
	}
	if stores[0].Domain != DomainUsage {
		t.Fatalf("shard domain = %q", stores[0].Domain)
	}
	// 根目录不存在时安全返回空。
	missing, err := ResolvePaths(filepath.Join(root, "nowhere"), "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := missing.usageShardStores(); got != nil {
		t.Fatalf("missing shard root should yield nothing, got %v", got)
	}
}

func TestEnvDomainResultBookkeeping(t *testing.T) {
	e := testEnv(t)
	if e.domainWired(DomainBusiness) {
		t.Fatal("no domain should be wired before a run")
	}
	e.recordDomainResult(DomainResult{Name: DomainBusiness})
	if e.domainWired(DomainBusiness) {
		t.Fatal("an empty result must not count as wired")
	}
	e.recordDomainResult(DomainResult{Name: DomainBusiness, Counts: map[string]int{"users": 3}})
	if !e.domainWired(DomainBusiness) {
		t.Fatal("a non-empty result must count as wired")
	}
	results := e.domainResults()
	if len(results) != 1 || results[0].Name != DomainBusiness {
		t.Fatalf("domainResults = %+v", results)
	}
	// 未登记的域不出现在结果里（报告只列实际跑过的域）。
	if len(e.domainResults()) != 1 {
		t.Fatalf("domainResults = %+v", e.domainResults())
	}
}
