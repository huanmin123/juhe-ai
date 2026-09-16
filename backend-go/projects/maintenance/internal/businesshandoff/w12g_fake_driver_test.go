package businesshandoff

// w12g 波次：错误注入 fake driver，直接驱动 schema.go 各 Impl 辅助查询，
// 覆盖真实 modernc/sqlite 语义下无法触发的 Scan / rows.Err / rows.Close
// 错误分支。行数据按每个 PRAGMA 的真实列数构造，类型错位即可让 Scan 失败。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

var errW12GFakeDriver = errors.New("w12g fake driver 注入错误")

type w12gFakeRows struct {
	columns  []string
	rows     [][]driver.Value
	pos      int
	nextErr  error // 行耗尽后的迭代错误（喂给 rows.Err()）
	closeErr error // Close 返回的错误
}

func (r *w12gFakeRows) Columns() []string { return r.columns }
func (r *w12gFakeRows) Close() error      { return r.closeErr }
func (r *w12gFakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

type w12gFakeQuery struct {
	rows *w12gFakeRows
	err  error
}

type w12gFakeConn struct {
	queries []w12gFakeQuery
}

func (c *w12gFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errW12GFakeDriver
}
func (c *w12gFakeConn) Close() error              { return nil }
func (c *w12gFakeConn) Begin() (driver.Tx, error) { return nil, errW12GFakeDriver }

func (c *w12gFakeConn) CheckNamedValue(value *driver.NamedValue) error { return nil }

func (c *w12gFakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if len(c.queries) == 0 {
		return nil, errW12GFakeDriver
	}
	next := c.queries[0]
	c.queries = c.queries[1:]
	if next.err != nil {
		return nil, next.err
	}
	return next.rows, nil
}

type w12gFakeConnector struct{ conn *w12gFakeConn }

func (c w12gFakeConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c w12gFakeConnector) Driver() driver.Driver                        { return w12gFakeDriver{} }

type w12gFakeDriver struct{}

func (w12gFakeDriver) Open(string) (driver.Conn, error) { return nil, errW12GFakeDriver }

// w12gFakeDB 用按序查询响应构造一个 *sql.DB。
func w12gFakeDB(t *testing.T, queries ...w12gFakeQuery) *sql.DB {
	t.Helper()
	return sql.OpenDB(w12gFakeConnector{conn: &w12gFakeConn{queries: queries}})
}

// w12gRowsOf 构造单行/迭代错误/关闭错误的 rows 快捷方式。
func w12gRowsOf(columns []string, row []driver.Value) *w12gFakeRows {
	return &w12gFakeRows{columns: columns, rows: [][]driver.Value{row}}
}

func TestW12GSQLiteImplScanErrorBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("columns scan error", func(t *testing.T) {
		// table_info: (cid,name,type,notNull,dflt_value,pk)
		db := w12gFakeDB(t, w12gFakeQuery{rows: w12gRowsOf([]string{"c0", "c1", "c2", "c3", "c4", "c5"},
			[]driver.Value{true, "id", "TEXT", 0, nil, 0})})
		defer db.Close()
		if _, err := sqliteColumnsImpl(ctx, db, "t"); err == nil {
			t.Fatal("cid 位置 bool 必须报 Scan 错误")
		}
	})

	t.Run("primary key scan and iterate errors", func(t *testing.T) {
		dbScan := w12gFakeDB(t, w12gFakeQuery{rows: w12gRowsOf([]string{"c0", "c1", "c2", "c3", "c4", "c5"},
			[]driver.Value{true, "id", "TEXT", 0, nil, 1})})
		defer dbScan.Close()
		if _, err := sqlitePrimaryKeyImpl(ctx, dbScan, "t"); err == nil {
			t.Fatal("主键 Scan 错误必须上抛")
		}

		dbIter := w12gFakeDB(t, w12gFakeQuery{rows: &w12gFakeRows{
			columns: []string{"c0", "c1", "c2", "c3", "c4", "c5"},
			rows:    [][]driver.Value{{0, "id", "TEXT", 0, nil, 0}},
			nextErr: errW12GFakeDriver,
		}})
		defer dbIter.Close()
		if _, err := sqlitePrimaryKeyImpl(ctx, dbIter, "t"); err == nil {
			t.Fatal("主键迭代错误必须上抛")
		}
	})

	t.Run("unique constraint scan iterate and close errors", func(t *testing.T) {
		// index_list: (seq,name,unique,origin,partial)
		row := []driver.Value{0, "idx_t", 1, "c", 0}
		dbScan := w12gFakeDB(t, w12gFakeQuery{rows: w12gRowsOf([]string{"c0", "c1", "c2", "c3", "c4"},
			[]driver.Value{true, "idx_t", 1, "c", 0})})
		defer dbScan.Close()
		if _, err := sqliteHasUniqueConstraintImpl(ctx, dbScan, "t", []string{"id"}); err == nil {
			t.Fatal("unique 约束 Scan 错误必须上抛")
		}

		dbIter := w12gFakeDB(t, w12gFakeQuery{rows: &w12gFakeRows{
			columns: []string{"c0", "c1", "c2", "c3", "c4"},
			rows:    [][]driver.Value{row},
			nextErr: errW12GFakeDriver,
		}})
		defer dbIter.Close()
		if _, err := sqliteHasUniqueConstraintImpl(ctx, dbIter, "t", []string{"id"}); err == nil {
			t.Fatal("unique 约束迭代错误必须上抛")
		}

		dbClose := w12gFakeDB(t, w12gFakeQuery{rows: &w12gFakeRows{
			columns: []string{"c0", "c1", "c2", "c3", "c4"},
			rows:    [][]driver.Value{row}, closeErr: errW12GFakeDriver,
		}})
		defer dbClose.Close()
		if _, err := sqliteHasUniqueConstraintImpl(ctx, dbClose, "t", []string{"id"}); err == nil {
			t.Fatal("index_list Close 错误必须上抛")
		}
	})

	t.Run("index columns scan and iterate errors", func(t *testing.T) {
		// index_info: (seqno,cid,name)
		dbScan := w12gFakeDB(t, w12gFakeQuery{rows: w12gRowsOf([]string{"c0", "c1", "c2"},
			[]driver.Value{true, 0, "id"})})
		defer dbScan.Close()
		if _, err := sqliteIndexColumnsImpl(ctx, dbScan, "idx"); err == nil {
			t.Fatal("index_info Scan 错误必须上抛")
		}

		dbIter := w12gFakeDB(t, w12gFakeQuery{rows: &w12gFakeRows{
			columns: []string{"c0", "c1", "c2"},
			rows:    [][]driver.Value{{0, 0, "id"}},
			nextErr: errW12GFakeDriver,
		}})
		defer dbIter.Close()
		if _, err := sqliteIndexColumnsImpl(ctx, dbIter, "idx"); err == nil {
			t.Fatal("index_info 迭代错误必须上抛")
		}
	})

	t.Run("foreign keys scan and iterate errors", func(t *testing.T) {
		// foreign_key_list: (id,seq,table,from,to,on_update,on_delete,match)
		base := []driver.Value{0, 0, "accounts", "account_id", "id", "NO ACTION", "CASCADE", "NONE"}
		dbScan := w12gFakeDB(t, w12gFakeQuery{rows: w12gRowsOf([]string{"c0", "c1", "c2", "c3", "c4", "c5", "c6", "c7"},
			[]driver.Value{true, 0, "accounts", "account_id", "id", "NO ACTION", "CASCADE", "NONE"})})
		defer dbScan.Close()
		if _, err := sqliteForeignKeysImpl(ctx, dbScan, "t"); err == nil {
			t.Fatal("外键 Scan 错误必须上抛")
		}

		dbIter := w12gFakeDB(t, w12gFakeQuery{rows: &w12gFakeRows{
			columns: []string{"c0", "c1", "c2", "c3", "c4", "c5", "c6", "c7"},
			rows:    [][]driver.Value{base},
			nextErr: errW12GFakeDriver,
		}})
		defer dbIter.Close()
		if _, err := sqliteForeignKeysImpl(ctx, dbIter, "t"); err == nil {
			t.Fatal("外键迭代错误必须上抛")
		}
	})
}

func TestW12GSQLiteIndexMatchesImplErrorBranches(t *testing.T) {
	ctx := context.Background()
	required := contracts.SQLiteIndexDefinition{Name: "idx_w12g", Columns: []string{"id"}, Unique: true}
	listColumns := []string{"c0", "c1", "c2", "c3", "c4"}
	infoColumns := []string{"c0", "c1", "c2"}

	t.Run("index list scan error", func(t *testing.T) {
		db := w12gFakeDB(t, w12gFakeQuery{rows: w12gRowsOf(listColumns, []driver.Value{true, "idx_w12g", 1, "c", 0})})
		defer db.Close()
		if _, _, err := sqliteIndexMatchesImpl(ctx, db, "t", required); err == nil {
			t.Fatal("index_list Scan 错误必须上抛")
		}
	})

	t.Run("index list iterate error", func(t *testing.T) {
		db := w12gFakeDB(t, w12gFakeQuery{rows: &w12gFakeRows{
			columns: listColumns,
			rows:    [][]driver.Value{{0, "idx_w12g", 1, "c", 0}},
			nextErr: errW12GFakeDriver,
		}})
		defer db.Close()
		if _, _, err := sqliteIndexMatchesImpl(ctx, db, "t", required); err == nil {
			t.Fatal("index_list 迭代错误必须上抛")
		}
	})

	t.Run("index list close error", func(t *testing.T) {
		db := w12gFakeDB(t, w12gFakeQuery{rows: &w12gFakeRows{
			columns: listColumns, rows: [][]driver.Value{{0, "idx_w12g", 1, "c", 0}}, closeErr: errW12GFakeDriver,
		}})
		defer db.Close()
		if _, _, err := sqliteIndexMatchesImpl(ctx, db, "t", required); err == nil {
			t.Fatal("index_list Close 错误必须上抛")
		}
	})

	t.Run("index info query error", func(t *testing.T) {
		db := w12gFakeDB(t,
			w12gFakeQuery{rows: w12gRowsOf(listColumns, []driver.Value{0, "idx_w12g", 1, "c", 0})},
			w12gFakeQuery{err: errW12GFakeDriver},
		)
		defer db.Close()
		if _, _, err := sqliteIndexMatchesImpl(ctx, db, "t", required); err == nil {
			t.Fatal("index_info 查询错误必须上抛")
		}
	})

	t.Run("index info scan error", func(t *testing.T) {
		db := w12gFakeDB(t,
			w12gFakeQuery{rows: w12gRowsOf(listColumns, []driver.Value{0, "idx_w12g", 1, "c", 0})},
			w12gFakeQuery{rows: w12gRowsOf(infoColumns, []driver.Value{true, 0, "id"})},
		)
		defer db.Close()
		if _, _, err := sqliteIndexMatchesImpl(ctx, db, "t", required); err == nil {
			t.Fatal("index_info Scan 错误必须上抛")
		}
	})

	t.Run("index info iterate error", func(t *testing.T) {
		db := w12gFakeDB(t,
			w12gFakeQuery{rows: w12gRowsOf(listColumns, []driver.Value{0, "idx_w12g", 1, "c", 0})},
			w12gFakeQuery{rows: &w12gFakeRows{
				columns: infoColumns, rows: [][]driver.Value{{0, 0, "id"}}, nextErr: errW12GFakeDriver,
			}},
		)
		defer db.Close()
		if _, _, err := sqliteIndexMatchesImpl(ctx, db, "t", required); err == nil {
			t.Fatal("index_info 迭代错误必须上抛")
		}
	})

	t.Run("index info close error", func(t *testing.T) {
		db := w12gFakeDB(t,
			w12gFakeQuery{rows: w12gRowsOf(listColumns, []driver.Value{0, "idx_w12g", 1, "c", 0})},
			w12gFakeQuery{rows: &w12gFakeRows{
				columns: infoColumns, rows: [][]driver.Value{{0, 0, "id"}}, closeErr: errW12GFakeDriver,
			}},
		)
		defer db.Close()
		if _, _, err := sqliteIndexMatchesImpl(ctx, db, "t", required); err == nil {
			t.Fatal("index_info Close 错误必须上抛")
		}
	})

	t.Run("sqlite master definition read error", func(t *testing.T) {
		db := w12gFakeDB(t,
			w12gFakeQuery{rows: w12gRowsOf(listColumns, []driver.Value{0, "idx_w12g", 1, "c", 0})},
			w12gFakeQuery{rows: w12gRowsOf(infoColumns, []driver.Value{0, 0, "id"})},
			w12gFakeQuery{err: errW12GFakeDriver},
		)
		defer db.Close()
		_, detail, err := sqliteIndexMatchesImpl(ctx, db, "t", required)
		if err == nil || detail != "read sqlite_master definition failed" {
			t.Fatalf("sqlite_master 读取错误必须上抛: err=%v detail=%q", err, detail)
		}
	})
}
