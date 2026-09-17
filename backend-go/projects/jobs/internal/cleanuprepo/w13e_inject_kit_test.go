package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"modernc.org/sqlite"
)

// w13e_inject_kit_test.go 是 w13e 波次的共享注入基建：
//   - SQLite 装饰器：在 kitFailingSQLite 语义之上补齐 Begin 失败、Commit
//     失败、RowsAffected 失败与「脚本化行集」（可注入 Scan 失败值与迭代
//     中段错误，覆盖 rows.Scan / rows.Err 错误臂）；
//   - PG 装饰器：在 kitFailingPGConn 之上补齐 Commit 失败与 RowsAffected
//     失败（覆盖 changes / tx.Commit 错误臂）。
// 断言只覆盖「该失败必须向外传播」；不改变生产语义。

// ---- SQLite 装饰器 ----

// w13eRowsScript 按子串匹配返回脚本化行集。errAfterRows >= 0 时在产出
// errAfterRows 行后令 Next 返回错误（rows.Err 臂）；values 中的字符串喂给
// 数值扫描目标时自然产生 Scan 错误。
type w13eRowsScript struct {
	match        string
	columns      []string
	values       [][]driver.Value
	errAfterRows int // -1 表示不注入迭代错误
}

type w13eScriptedRows struct {
	columns []string
	values  [][]driver.Value
	pos     int
	errAt   int // 产出第 errAt 行后返回错误；<0 表示无
}

func (r *w13eScriptedRows) Columns() []string { return r.columns }
func (r *w13eScriptedRows) Close() error      { return nil }
func (r *w13eScriptedRows) Next(dest []driver.Value) error {
	if r.errAt >= 0 && r.pos == r.errAt {
		return errors.New("w13eScriptedRows: 迭代中段注入错误")
	}
	if r.pos >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.pos])
	r.pos++
	return nil
}

type w13eSQLiteOptions struct {
	failOn             string
	failBegin          bool
	failCommit         bool
	failClose          bool
	rowsAffectedFailOn string
	rowsScripts        []w13eRowsScript
}

type w13eDecoratedSQLiteConn struct {
	raw  driver.Conn
	opts w13eSQLiteOptions
}

func (c *w13eDecoratedSQLiteConn) Prepare(query string) (driver.Stmt, error) {
	return c.raw.Prepare(query)
}
func (c *w13eDecoratedSQLiteConn) Close() error {
	if c.opts.failClose {
		return errors.New("w13eDecoratedSQLiteConn: Close 注入失败")
	}
	return c.raw.Close()
}

func (c *w13eDecoratedSQLiteConn) Begin() (driver.Tx, error) {
	if c.opts.failBegin {
		return nil, errors.New("w13eDecoratedSQLiteConn: Begin 注入失败")
	}
	tx, err := c.raw.Begin()
	if err != nil {
		return nil, err
	}
	if c.opts.failCommit {
		return w13eFailingTx{raw: tx}, nil
	}
	return tx, nil
}

func (c *w13eDecoratedSQLiteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if c.opts.failOn != "" && strings.Contains(query, c.opts.failOn) {
		return nil, fmt.Errorf("w13eDecoratedSQLiteConn: 注入失败：%s", oneLineSQL(query))
	}
	var raw driver.Stmt
	var err error
	if prep, ok := c.raw.(driver.ConnPrepareContext); ok {
		raw, err = prep.PrepareContext(ctx, query)
	} else {
		raw, err = c.raw.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &w13eDecoratedStmt{raw: raw, query: query, opts: c.opts}, nil
}

// w13eDecoratedStmt 保留查询文本，让 rowsAffected 注入能命中预编译路径。
type w13eDecoratedStmt struct {
	raw   driver.Stmt
	query string
	opts  w13eSQLiteOptions
}

func (s *w13eDecoratedStmt) Close() error  { return s.raw.Close() }
func (s *w13eDecoratedStmt) NumInput() int { return s.raw.NumInput() }
func (s *w13eDecoratedStmt) Exec(args []driver.Value) (driver.Result, error) {
	if s.opts.rowsAffectedFailOn != "" && strings.Contains(s.query, s.opts.rowsAffectedFailOn) {
		return kitFailingResult{}, nil
	}
	return s.raw.Exec(args)
}
func (s *w13eDecoratedStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.raw.Query(args)
}

func (s *w13eDecoratedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if s.opts.rowsAffectedFailOn != "" && strings.Contains(s.query, s.opts.rowsAffectedFailOn) {
		return kitFailingResult{}, nil
	}
	if exec, ok := s.raw.(driver.StmtExecContext); ok {
		return exec.ExecContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for index, arg := range args {
		values[index] = arg.Value
	}
	return s.raw.Exec(values)
}

func (s *w13eDecoratedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := s.raw.(driver.StmtQueryContext); ok {
		return queryer.QueryContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for index, arg := range args {
		values[index] = arg.Value
	}
	return s.raw.Query(values)
}

func (c *w13eDecoratedSQLiteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.opts.failOn != "" && strings.Contains(query, c.opts.failOn) {
		return nil, fmt.Errorf("w13eDecoratedSQLiteConn: 注入失败：%s", oneLineSQL(query))
	}
	if c.opts.rowsAffectedFailOn != "" && strings.Contains(query, c.opts.rowsAffectedFailOn) {
		return kitFailingResult{}, nil
	}
	if exec, ok := c.raw.(driver.ExecerContext); ok {
		return exec.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *w13eDecoratedSQLiteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.opts.failOn != "" && strings.Contains(query, c.opts.failOn) {
		return nil, fmt.Errorf("w13eDecoratedSQLiteConn: 注入失败：%s", oneLineSQL(query))
	}
	for _, script := range c.opts.rowsScripts {
		if strings.Contains(query, script.match) {
			return &w13eScriptedRows{columns: script.columns, values: script.values, errAt: script.errAfterRows}, nil
		}
	}
	if queryer, ok := c.raw.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

type w13eFailingTx struct{ raw driver.Tx }

func (t w13eFailingTx) Commit() error   { return errors.New("w13eFailingTx: Commit 注入失败") }
func (t w13eFailingTx) Rollback() error { return t.raw.Rollback() }

type w13eSQLiteConnector struct {
	dsn  string
	opts w13eSQLiteOptions
}

func (c w13eSQLiteConnector) Connect(context.Context) (driver.Conn, error) {
	raw, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &w13eDecoratedSQLiteConn{raw: raw, opts: c.opts}, nil
}

func (c w13eSQLiteConnector) Driver() driver.Driver { return &sqlite.Driver{} }

// w13eOpenDecoratedSQLite 打开带装饰器的 SQLite 句柄。
func w13eOpenDecoratedSQLite(t *testing.T, path string, opts w13eSQLiteOptions) *DB {
	t.Helper()
	db := sql.OpenDB(w13eSQLiteConnector{dsn: path, opts: opts})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return &DB{DB: db}
}

// w13eOpenDecoratedRawSQLite 与上同，但返回裸 *sql.DB（分片库等场景）。
func w13eOpenDecoratedRawSQLite(t *testing.T, path string, opts w13eSQLiteOptions) *sql.DB {
	t.Helper()
	db := sql.OpenDB(w13eSQLiteConnector{dsn: path, opts: opts})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---- PG 装饰器 ----

type w13ePGOptions struct {
	failOn             []string
	failBegin          bool
	failCommit         bool
	rowsAffectedFailOn string
}

type w13eDecoratedPGConn struct {
	*kitFailingPGConn
	opts w13ePGOptions
}

func (c *w13eDecoratedPGConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.opts.rowsAffectedFailOn != "" && strings.Contains(query, c.opts.rowsAffectedFailOn) {
		c.rec.capture(query, args)
		return kitFailingResult{}, nil
	}
	return c.kitFailingPGConn.ExecContext(ctx, query, args)
}

func (c *w13eDecoratedPGConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.opts.failBegin {
		return nil, errors.New("w13eDecoratedPGConn: BeginTx 注入失败")
	}
	c.rec.mu.Lock()
	c.rec.txDepth++
	c.rec.begins++
	c.rec.mu.Unlock()
	return w13ePGTx{rec: c.rec, failCommit: c.opts.failCommit}, nil
}

type w13ePGTx struct {
	rec        *pgRecorder
	failCommit bool
}

func (t w13ePGTx) Commit() error {
	t.rec.mu.Lock()
	defer t.rec.mu.Unlock()
	t.rec.txDepth--
	t.rec.commits++
	if t.failCommit {
		return errors.New("w13ePGTx: Commit 注入失败")
	}
	return nil
}

func (t w13ePGTx) Rollback() error {
	t.rec.mu.Lock()
	defer t.rec.mu.Unlock()
	t.rec.txDepth--
	t.rec.rollbacks++
	return nil
}

type w13ePGConnector struct {
	rec  *pgRecorder
	opts w13ePGOptions
}

func (c w13ePGConnector) Connect(context.Context) (driver.Conn, error) {
	inner := &kitFailingPGConn{recorderConn: &recorderConn{rec: c.rec}, failOn: c.opts.failOn}
	return &w13eDecoratedPGConn{kitFailingPGConn: inner, opts: c.opts}, nil
}

func (c w13ePGConnector) Driver() driver.Driver { return recorderDriver{rec: c.rec} }

// w13eOpenDecoratedPG 打开带 Commit/RowsAffected 注入的 PG 录制句柄。
func w13eOpenDecoratedPG(rec *pgRecorder, opts w13ePGOptions) *DB {
	return &DB{DB: sql.OpenDB(w13ePGConnector{rec: rec, opts: opts}), Postgres: true}
}
