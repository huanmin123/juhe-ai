package j3bmodelcheck

// w12g 波次：错误注入 fake driver，按查询顺序回放响应/错误/畸形行，直接
// 驱动 backfill.go 与 bootstrap.go 的 helper，覆盖真实 modernc/sqlite 与
// wmPGCatalog 无法构造的 Scan / rows.Err / rows.Close / Query 错误分支。
// 该 fake 是测试专用驱动，不参与生产路径。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

var errW12GDriver = errors.New("w12g fake driver 注入错误")

type w12gFakeRows struct {
	columns  []string
	rows     [][]driver.Value
	pos      int
	nextErr  error
	closeErr error
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
	match string
	rows  *w12gFakeRows
	err   error
}

type w12gFakeConn struct {
	queries     []w12gFakeQuery
	defaultRows *w12gFakeRows // 无匹配脚本时的默认响应；nil 则报注入错误
	execErr     error
	prepareErr  bool
	beginErr    bool
	pragmaErr   bool
	commitErr   error
}

var w12gDebugPop bool

func (c *w12gFakeConn) pop(query string) (w12gFakeQuery, error) {
	for index, item := range c.queries {
		if item.match != "" && !strings.Contains(query, item.match) {
			if w12gDebugPop {
				println("w12g pop skip:", item.match, "<-", query[:min(len(query), 90)])
			}
			continue
		}
		if w12gDebugPop {
			println("w12g pop hit:", item.match, "<-", query[:min(len(query), 90)])
		}
		c.queries = append(c.queries[:index], c.queries[index+1:]...)
		return item, nil
	}
	if c.defaultRows != nil {
		if w12gDebugPop {
			println("w12g pop default:", query[:min(len(query), 60)])
		}
		return w12gFakeQuery{rows: c.defaultRows}, nil
	}
	if w12gDebugPop {
		println("w12g pop EXHAUSTED:", query[:min(len(query), 60)])
	}
	return w12gFakeQuery{}, errW12GDriver
}

func (c *w12gFakeConn) Prepare(query string) (driver.Stmt, error) {
	if c.prepareErr {
		return nil, errW12GDriver
	}
	return &w12gFakeStmt{conn: c, query: query}, nil
}

type w12gFakeStmt struct {
	conn  *w12gFakeConn
	query string
}

func (s *w12gFakeStmt) Close() error  { return nil }
func (s *w12gFakeStmt) NumInput() int { return -1 }

func (s *w12gFakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	if s.conn.execErr != nil {
		return nil, s.conn.execErr
	}
	return driver.RowsAffected(1), nil
}

func (s *w12gFakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.conn.QueryContext(context.Background(), s.query, namedW12G(args))
}

func namedW12G(args []driver.Value) []driver.NamedValue {
	named := make([]driver.NamedValue, 0, len(args))
	for index, value := range args {
		named = append(named, driver.NamedValue{Ordinal: index + 1, Value: value})
	}
	return named
}
func (c *w12gFakeConn) Close() error              { return nil }
func (c *w12gFakeConn) Begin() (driver.Tx, error) { return &w12gFakeTx{conn: c}, nil }

func (c *w12gFakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.beginErr {
		return nil, errW12GDriver
	}
	return &w12gFakeTx{conn: c, commitErr: c.commitErr}, nil
}

func (c *w12gFakeConn) CheckNamedValue(value *driver.NamedValue) error { return nil }

func (c *w12gFakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	item, err := c.pop(query)
	if err != nil {
		return nil, err
	}
	if item.err != nil {
		return nil, item.err
	}
	return item.rows, nil
}

func (c *w12gFakeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(strings.TrimSpace(query), "PRAGMA") {
		if c.pragmaErr {
			return nil, errW12GDriver
		}
		return driver.RowsAffected(0), nil
	}
	if c.execErr != nil {
		return nil, c.execErr
	}
	return driver.RowsAffected(0), nil
}

type w12gFakeTx struct {
	conn      *w12gFakeConn
	commitErr error
}

func (t *w12gFakeTx) Commit() error   { return t.commitErr }
func (t *w12gFakeTx) Rollback() error { return nil }

type w12gFakeConnector struct{ conn *w12gFakeConn }

func (c w12gFakeConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c w12gFakeConnector) Driver() driver.Driver                        { return w12gFakeDriver2{} }

type w12gFakeDriver2 struct{}

func (w12gFakeDriver2) Open(string) (driver.Conn, error) { return nil, errW12GDriver }

// w12gFakeDB 构造按子串匹配查询脚本的 *sql.DB。
func w12gFakeDB(t *testing.T, conn *w12gFakeConn) *sql.DB {
	if t != nil {
		t.Helper()
	}
	db := sql.OpenDB(w12gFakeConnector{conn: conn})
	if t != nil {
		t.Cleanup(func() { _ = db.Close() })
	}
	return db
}

func w12gColumnsRow(name string, pk int) []driver.Value {
	return []driver.Value{int64(0), name, "TEXT", int64(0), nil, int64(pk)}
}

// w12gTableInfo 构造 PRAGMA table_info 形状的 rows。
func w12gTableInfo(columns []string, pks []string) *w12gFakeRows {
	pkSet := map[string]bool{}
	for _, name := range pks {
		pkSet[name] = true
	}
	rows := make([][]driver.Value, 0, len(columns))
	for _, name := range columns {
		pk := 0
		if pkSet[name] {
			pk = 1
		}
		rows = append(rows, w12gColumnsRow(name, pk))
	}
	return &w12gFakeRows{columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk"}, rows: rows}
}

func w12gRowsBadTableInfo() *w12gFakeRows {
	return &w12gFakeRows{columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk"},
		rows: [][]driver.Value{{"not-an-int", "id", "TEXT", int64(0), nil, int64(0)}}}
}

func w12gRowsIterErrTableInfo() *w12gFakeRows {
	return &w12gFakeRows{columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk"},
		rows: [][]driver.Value{w12gColumnsRow("id", 0)}, nextErr: errW12GDriver}
}
