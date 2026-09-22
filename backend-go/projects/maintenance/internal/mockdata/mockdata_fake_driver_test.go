package mockdata

// 本文件提供 mockdata 包的错误注入 fake driver：把 openSQLiteDatabase 换成
// 打开这个驱动，就能让真实的现代 SQLite 语义下无法触发的分支（打开失败、
// PRAGMA 失败、Scan 类型错位、rows.Err、Commit/Rollback/Close 失败、
// RowsAffected 失败）各自可测。
//
// 与 internal/schema/wm_pg_fake_test.go、internal/businesshandoff/
// w12g_fake_driver_test.go 同一手法；真实 modernc/sqlite 行为仍由其余测试覆盖。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

const mockFakeDriverName = "mockdatafake"

var errMockFake = errors.New("mockdata fake driver 注入错误")

// mockFakeRows 是脚本化结果集：nextErr 覆盖 rows.Err()，类型错位的行覆盖 Scan。
type mockFakeRows struct {
	columns  []string
	rows     [][]driver.Value
	nextErr  error
	closeErr error
}

func (r *mockFakeRows) Columns() []string { return r.columns }
func (r *mockFakeRows) Close() error      { return r.closeErr }

func (r *mockFakeRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	row := r.rows[0]
	r.rows = r.rows[1:]
	copy(dest, row)
	return nil
}

// mockFakeQuery 把 SQL 子串映射到结果集；顺序敏感，先匹配者生效。
type mockFakeQuery struct {
	match string
	rows  *mockFakeRows
}

// mockFakeScript 是一次注入脚本。零值表示「什么都不失败」。
type mockFakeScript struct {
	openErr         error
	modeErr         error // PRAGMA journal_mode 失败
	execErr         error // 其它 Exec 失败
	execErrMatch    string
	queryErr        error
	queryErrMatch   string // 非空时仅影响含该子串的查询
	queries         []mockFakeQuery
	beginErr        error
	commitErr       error
	rollbackErr     error
	closeErr        error
	rowsAffectedErr bool
}

type mockFakeDriver struct{}

var mockFakeActive = &mockFakeScript{}

func init() {
	sql.Register(mockFakeDriverName, mockFakeDriver{})
}

func (mockFakeDriver) Open(string) (driver.Conn, error) {
	if mockFakeActive.openErr != nil {
		return nil, mockFakeActive.openErr
	}
	return &mockFakeConn{script: mockFakeActive}, nil
}

type mockFakeConn struct{ script *mockFakeScript }

func (c *mockFakeConn) Prepare(string) (driver.Stmt, error) { return nil, errMockFake }
func (c *mockFakeConn) Close() error                        { return c.script.closeErr }
func (c *mockFakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *mockFakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.script.beginErr != nil {
		return nil, c.script.beginErr
	}
	return &mockFakeTx{script: c.script}, nil
}

func (c *mockFakeConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *mockFakeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "PRAGMA journal_mode") && c.script.modeErr != nil {
		return nil, c.script.modeErr
	}
	if c.script.execErr != nil && (c.script.execErrMatch == "" || strings.Contains(query, c.script.execErrMatch)) {
		return nil, c.script.execErr
	}
	return mockFakeResult{script: c.script}, nil
}

func (c *mockFakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.script.queryErr != nil && (c.script.queryErrMatch == "" || strings.Contains(query, c.script.queryErrMatch)) {
		return nil, c.script.queryErr
	}
	for _, scripted := range c.script.queries {
		if strings.Contains(query, scripted.match) {
			// 每次查询返回一份独立的行游标：driver.Rows 是有状态的，
			// 共享会让第二次相同查询直接读到 io.EOF。
			clone := *scripted.rows
			clone.rows = append([][]driver.Value(nil), scripted.rows.rows...)
			return &clone, nil
		}
	}
	return nil, fmt.Errorf("mockdata fake driver: 未脚本化的查询 %q", query)
}

type mockFakeResult struct{ script *mockFakeScript }

func (r mockFakeResult) LastInsertId() (int64, error) { return 0, nil }

func (r mockFakeResult) RowsAffected() (int64, error) {
	if r.script.rowsAffectedErr {
		return 0, errMockFake
	}
	return 1, nil
}

type mockFakeTx struct{ script *mockFakeScript }

func (t *mockFakeTx) Commit() error   { return t.script.commitErr }
func (t *mockFakeTx) Rollback() error { return t.script.rollbackErr }

// installFakeDriver 把包级打开函数切到 fake driver，并在测试结束时恢复。
// 包内测试不使用 t.Parallel()，因此包级切换是安全的（与仓库既有做法一致）。
func installFakeDriver(t *testing.T, script *mockFakeScript) {
	t.Helper()
	previous := openSQLiteDatabase
	mockFakeActive = script
	openSQLiteDatabase = func(dsn string) (*sql.DB, error) { return sql.Open(mockFakeDriverName, dsn) }
	t.Cleanup(func() {
		openSQLiteDatabase = previous
		mockFakeActive = &mockFakeScript{}
	})
}

// fakeEnv 返回一个指向临时数据根的上下文，存储路径保持真实默认名（fake
// driver 不关心 DSN，但 Paths 必须已解析）。
func fakeEnv(t *testing.T) *env {
	t.Helper()
	paths, err := ResolvePaths(t.TempDir(), "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	t.Cleanup(func() { _ = e.Close() })
	return e
}
