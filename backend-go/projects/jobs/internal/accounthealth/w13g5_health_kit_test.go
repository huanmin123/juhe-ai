package accounthealth

// w13g5_health_kit_test.go 提供可回放的失败注入 SQLite 驱动（包装
// modernc.org/sqlite）与注入版 store 构造，覆盖存储层深层 err 传播臂。
// 注入策略对齐 statsagg/circuitstore 的 w13g5 kit；错误消息固定为
// "w13g5 注入失败"，不携带连接串或敏感数据。
//
// 注意：sql.Tx 在 Commit 失败后标记 done，驱动级事务仅随连接释放；
// 涉及 Commit 失败的阶段使用独立注入库。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"modernc.org/sqlite"
)

const w13g5HealthFailDriverName = "w13g5-health-fail-sqlite"

var (
	w13g5HealthFailDriverRegister sync.Once
	w13g5HealthFailDriversMu      sync.Mutex
	w13g5HealthFailDrivers        = map[string]*w13g5HealthSpec{}
)

// w13g5HealthSpec 描述一次注入：match 命中查询子串；once 只注入一次。
type w13g5HealthSpec struct {
	mu    sync.Mutex
	match string
	once  bool
	fired bool
}

func (spec *w13g5HealthSpec) arm(match string) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.fired = match, false, false
}

func (spec *w13g5HealthSpec) armOnce(match string) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.fired = match, true, false
}

func (spec *w13g5HealthSpec) disarm() {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match = ""
}

func (spec *w13g5HealthSpec) hit(key string) bool {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	if spec.match == "" || !strings.Contains(key, spec.match) {
		return false
	}
	if spec.once && spec.fired {
		return false
	}
	spec.fired = true
	return true
}

func w13g5HealthInjectedErr() error { return errors.New("w13g5 注入失败") }

type w13g5HealthFailDriver struct{ inner driver.Driver }

func (d *w13g5HealthFailDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	w13g5HealthFailDriversMu.Lock()
	spec := w13g5HealthFailDrivers[w13g5HealthFailDriverName]
	w13g5HealthFailDriversMu.Unlock()
	return &w13g5HealthFailConn{inner: conn, spec: spec}, nil
}

type w13g5HealthFailConn struct {
	inner driver.Conn
	spec  *w13g5HealthSpec
}

func (c *w13g5HealthFailConn) Prepare(query string) (driver.Stmt, error) {
	if c.spec.hit(query) {
		return nil, w13g5HealthInjectedErr()
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &w13g5HealthFailStmt{inner: stmt}, nil
}

func (c *w13g5HealthFailConn) Close() error { return c.inner.Close() }

func (c *w13g5HealthFailConn) Begin() (driver.Tx, error) {
	if c.spec.hit("w13g5-BEGIN") {
		return nil, w13g5HealthInjectedErr()
	}
	tx, err := c.inner.Begin()
	if err != nil {
		return nil, err
	}
	return &w13g5HealthFailTx{inner: tx, spec: c.spec}, nil
}

func (c *w13g5HealthFailConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.spec.hit(query) {
		return nil, w13g5HealthInjectedErr()
	}
	if execer, ok := c.inner.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.Exec(w13g5HealthDriverValues(args))
}

func (c *w13g5HealthFailConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.spec.hit(query) {
		return nil, w13g5HealthInjectedErr()
	}
	if queryer, ok := c.inner.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.Query(w13g5HealthDriverValues(args))
}

type w13g5HealthFailTx struct {
	inner driver.Tx
	spec  *w13g5HealthSpec
}

func (tx *w13g5HealthFailTx) Commit() error {
	if tx.spec.hit("w13g5-COMMIT") {
		return w13g5HealthInjectedErr()
	}
	return tx.inner.Commit()
}

func (tx *w13g5HealthFailTx) Rollback() error {
	if tx.spec.hit("w13g5-ROLLBACK") {
		return w13g5HealthInjectedErr()
	}
	return tx.inner.Rollback()
}

type w13g5HealthFailStmt struct{ inner driver.Stmt }

func (s *w13g5HealthFailStmt) Close() error { return s.inner.Close() }

func (s *w13g5HealthFailStmt) NumInput() int { return -1 }

func (s *w13g5HealthFailStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.inner.Exec(args)
}

func (s *w13g5HealthFailStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.inner.Query(args)
}

func w13g5HealthDriverValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}

// w13g5HealthInjectStore 打开绑定注入 spec 的 store（复用生产 OpenStore
// 语义：单连接、WAL pragma），由调用方负责 EnsureSchema。
func w13g5HealthInjectStore(t *testing.T) (*Store, *w13g5HealthSpec) {
	t.Helper()
	w13g5HealthFailDriverRegister.Do(func() {
		sql.Register(w13g5HealthFailDriverName, &w13g5HealthFailDriver{inner: &sqlite.Driver{}})
	})
	spec := &w13g5HealthSpec{}
	w13g5HealthFailDriversMu.Lock()
	w13g5HealthFailDrivers[w13g5HealthFailDriverName] = spec
	w13g5HealthFailDriversMu.Unlock()
	dsn, err := sqliteDSN(filepath.Join(t.TempDir(), "w13g5-health.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(w13g5HealthFailDriverName, dsn)
	if err != nil {
		t.Fatalf("打开注入 store 失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store := newStore(db, StoreSQLite, nil)
	t.Cleanup(func() { _ = store.Close() })
	return store, spec
}
