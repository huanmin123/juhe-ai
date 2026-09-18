package runtimelog

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// w14i_direct_arms_test.go 用直接调用与取消 ctx 覆盖防御性错误臂：
//   - EnsureSchema 的 schema 执行错误臂（query_only 连接）；
//   - ensureSQLiteFenceTokenColumn / checkSQLiteColumns / insertSQLiteRecords
//     的取消 ctx 错误臂；
//   - decrementSQLiteFacets 空行集与取消 ctx 错误臂；
//   - decrementPostgresFacets 空行集错误臂（pgFake）。

func TestW14iEnsureSchemaFailsOnGarbageFile(t *testing.T) {
	// 非 SQLite 文件上的 CREATE TABLE 必须使 EnsureSchema fail-closed。
	path := filepath.Join(t.TempDir(), "w14i-garbage-rl.sqlite3")
	if err := os.WriteFile(path, []byte("w14i this is not a sqlite database at all...."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &sqliteStore{db: db}
	if err := EnsureSchema(context.Background(), store); err == nil {
		t.Fatalf("非 SQLite 文件上的 schema 初始化应失败")
	}
}

func TestW14iCanceledContextFailsSQLitePragmas(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ensureSQLiteFenceTokenColumn(ctx, store.db); err == nil {
		t.Fatalf("取消 ctx 应使 fence 字段检查失败")
	}
	if err := checkSQLiteColumns(ctx, store.db, "runtime_logs"); err == nil {
		t.Fatalf("取消 ctx 应使列检查失败")
	}
}

func TestW14iInsertSQLiteRecordsFailsOnCanceledContext(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	records := []Record{{ID: "w14i-canceled-rec", Time: "2026-08-01T00:00:00.000Z", CreatedAt: "2026-08-01T00:00:00.000Z", RawJSON: "{}"}}
	if _, err := insertSQLiteRecords(ctx, tx, records); err == nil {
		t.Fatalf("取消 ctx 应使记录写入失败")
	}
}

func TestW14iDecrementSQLiteFacetsEmptyRowsAndCanceledCtx(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// 空行集直接短路。
	if err := decrementSQLiteFacets(context.Background(), tx, nil, "2026-08-02T00:00:00.000Z"); err != nil {
		t.Fatalf("空行集不应报错: %v", err)
	}
	// 取消 ctx 后第一条查询即失败。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows := []facetRow{{Time: "2026-08-01T00:00:00.000Z", Level: "info"}}
	if err := decrementSQLiteFacets(ctx, tx, rows, "2026-08-02T00:00:00.000Z"); err == nil {
		t.Fatalf("取消 ctx 应使 facet 扣减失败")
	}
}

func TestW14iDecrementPostgresFacetsEmptyRows(t *testing.T) {
	server := newPGFakeServer(t)
	w14iRegisterHappyCleanup(t, server)
	store := openFakePostgresStore(t, server)
	tx, err := beginPostgresTx(context.Background(), store.pool)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackPostgresTx(tx)
	if err := decrementPostgresFacets(context.Background(), tx, nil, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("空行集不应报错: %v", err)
	}
}

func TestW14iDecrementSQLiteFacetsFailsOnDroppedRuntimeLogs(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE runtime_logs`); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rows := []facetRow{{Time: "2026-08-01T00:00:00.000Z", Level: "info"}}
	if err := decrementSQLiteFacets(context.Background(), tx, rows, "2026-08-02T00:00:00.000Z"); err == nil {
		t.Fatalf("runtime_logs 缺失应使 facet 扣减失败")
	}
}
