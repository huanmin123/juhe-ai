package bootstrap

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// 本文件覆盖受控导出面 bootstrap 的六库 ensure+seed 与 PostgreSQL 包装。
// SQLite 走 t.TempDir 真实文件；PostgreSQL 用录制驱动（全部执行成功、查询
// 返回空行）验证 EnsurePostgres/SeedPostgres 的语句计数与错误传播。

func TestWMEnsureSQLiteSchemaAllKinds(t *testing.T) {
	ctx := context.Background()
	kinds := []struct {
		kind SQLiteSchemaKind
		name string
	}{
		{SQLiteSchemaBusiness, "business"},
		{SQLiteSchemaStats, "stats"},
		{SQLiteSchemaChat, "chat"},
		{SQLiteSchemaCodexContext, "codex-context"},
		{SQLiteSchemaDataset, "dataset"},
		{SQLiteSchemaUsageCatalog, "usage-catalog"},
	}
	for _, item := range kinds {
		t.Run(item.name, func(t *testing.T) {
			db, err := OpenSQLiteFile(filepath.Join(t.TempDir(), item.name+".sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			counts, err := EnsureSQLiteSchema(ctx, item.kind, db)
			if err != nil {
				t.Fatalf("ensure %s: %v", item.name, err)
			}
			if counts.Tables <= 0 {
				t.Fatalf("每个 schema 至少应建一张表: %+v", counts)
			}
			// 幂等：重复执行不再新增对象。
			repeat, err := EnsureSQLiteSchema(ctx, item.kind, db)
			if err != nil || repeat != counts {
				t.Fatalf("重复 ensure 必须幂等: %+v err=%v", repeat, err)
			}
		})
	}
	t.Run("unknown kind rejected", func(t *testing.T) {
		db, err := OpenSQLiteFile(filepath.Join(t.TempDir(), "unknown.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := EnsureSQLiteSchema(ctx, SQLiteSchemaKind(99), db); err == nil || !strings.Contains(err.Error(), "unknown sqlite schema kind") {
			t.Fatalf("未知 kind 必须拒绝: %v", err)
		}
	})
}

func TestWMEnsureAllSQLiteCreatesSixSchemas(t *testing.T) {
	db, err := OpenSQLiteFile(filepath.Join(t.TempDir(), "all.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	counts, err := EnsureAllSQLite(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureAllSQLite: %v", err)
	}
	for index, count := range counts {
		if count.Tables <= 0 {
			t.Fatalf("第 %d 个 schema 必须至少建一张表: %+v", index, count)
		}
	}
	if len(counts) != 6 {
		t.Fatalf("必须恰好报告六个 schema: %d", len(counts))
	}
}

func TestWMSeedSQLiteBusinessMirrorsSchemaSeed(t *testing.T) {
	db, err := OpenSQLiteFile(filepath.Join(t.TempDir(), "seed.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := EnsureSQLiteSchema(context.Background(), SQLiteSchemaBusiness, db); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	result, err := SeedSQLiteBusiness(context.Background(), db, SeedOptions{Now: func() time.Time { return clock }, Secret: "wm-secret"})
	if err != nil {
		t.Fatalf("SeedSQLiteBusiness: %v", err)
	}
	if result.StatementCount <= 0 || result.ModelCatalogRows <= 0 {
		t.Fatalf("seed 结果必须为正: %+v", result)
	}
}

func TestWMOpenSQLiteFileCreatesParentDirectories(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "deep", "nested", "dir", "business.sqlite3")
	db, err := OpenSQLiteFile(nested)
	if err != nil {
		t.Fatalf("OpenSQLiteFile 必须递归创建父目录: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE wm_probe (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("打开的句柄必须可写: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// ---- PostgreSQL 录制驱动 ----

type wmPGRecorder struct {
	mu    sync.Mutex
	execs []string
	fail  bool
}

func (r *wmPGRecorder) record(query string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("wm bootstrap fake: 注入执行失败")
	}
	r.execs = append(r.execs, query)
	return nil
}

type wmPGConnector struct{ rec *wmPGRecorder }

func (c wmPGConnector) Connect(context.Context) (driver.Conn, error) {
	return &wmPGConn{rec: c.rec}, nil
}

func (c wmPGConnector) Driver() driver.Driver { return wmPGFakeDriver{} }

type wmPGFakeDriver struct{}

func (wmPGFakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("wm bootstrap fake: 请使用 sql.OpenDB")
}

type wmPGConn struct{ rec *wmPGRecorder }

func (c *wmPGConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("wm bootstrap fake: Prepare 不应被调用")
}
func (c *wmPGConn) Close() error   { return nil }
func (c *wmPGConn) Begin() (driver.Tx, error) {
	return nil, errors.New("wm bootstrap fake: Begin 不应被调用")
}

func (c *wmPGConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, bool, int64, float64, []byte, string, []string:
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	default:
		return fmt.Errorf("wm bootstrap fake: 不支持的参数类型 %T", value.Value)
	}
}

func (c *wmPGConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.rec.record(query); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (c *wmPGConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.rec.record(query); err != nil {
		return nil, err
	}
	// 零行结果：QueryRow().Scan() 语义等价 sql.ErrNoRows。
	return &wmPGRows{columns: []string{"c0"}}, nil
}

type wmPGRows struct {
	columns []string
	pos     int
}

func (r *wmPGRows) Columns() []string { return r.columns }
func (r *wmPGRows) Close() error      { return nil }
func (r *wmPGRows) Next(dest []driver.Value) error {
	if r.pos >= 0 {
		return io.EOF
	}
	r.pos++
	return nil
}

func openWMBootstrapPG(rec *wmPGRecorder) *sql.DB {
	return sql.OpenDB(wmPGConnector{rec: rec})
}

func TestWMEnsurePostgresWrapperMirrorsSchemaCounts(t *testing.T) {
	rec := &wmPGRecorder{}
	db := openWMBootstrapPG(rec)
	defer db.Close()
	count, err := EnsurePostgres(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsurePostgres: %v", err)
	}
	if count <= 0 {
		t.Fatalf("语句计数必须为正: %d", count)
	}
	if len(rec.execs) != count+6 {
		t.Fatalf("执行数应等于语句数 + 6 个 CREATE SCHEMA: %d vs %d", len(rec.execs), count)
	}
	rec.fail = true
	if _, err := EnsurePostgres(context.Background(), db); err == nil {
		t.Fatal("执行失败必须上抛")
	}
}

func TestWMSeedPostgresWrapperMirrorsSchemaSeed(t *testing.T) {
	rec := &wmPGRecorder{}
	db := openWMBootstrapPG(rec)
	defer db.Close()
	result, err := SeedPostgres(context.Background(), db, SeedOptions{
		Now:    func() time.Time { return time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC) },
		Secret: "wm-secret",
	})
	if err != nil {
		t.Fatalf("SeedPostgres: %v", err)
	}
	if result.StatementCount <= 0 {
		t.Fatalf("语句计数必须为正: %d", result.StatementCount)
	}
}
