package pgpool

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// fake 矩阵：coreConn/fullStmt 记录收到的 query；能力开关用包装类型
// （只嵌入 driver.Conn/Stmt 接口）模拟"未实现"分支，与 rewriteConn 的
// 断言降级路径对称。

type fakeRows struct{ closed bool }

func (r *fakeRows) Columns() []string { return []string{"v"} }
func (r *fakeRows) Close() error      { r.closed = true; return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	return io.EOF
}

type fakeResult struct{ affected int64 }

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.affected, nil }

type fakeTx struct{ committed bool }

func (t *fakeTx) Commit() error   { t.committed = true; return nil }
func (t *fakeTx) Rollback() error { return nil }

type fullStmt struct {
	conn      *coreConn
	queryUsed bool
}

func (s *fullStmt) Close() error  { return nil }
func (s *fullStmt) NumInput() int { return -1 }

func (s *fullStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.queryUsed = true
	return fakeResult{affected: 1}, nil
}

func (s *fullStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.queryUsed = true
	return &fakeRows{}, nil
}

func (s *fullStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	s.queryUsed = true
	return fakeResult{affected: 2}, nil
}

func (s *fullStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	s.queryUsed = true
	return &fakeRows{}, nil
}

// stmtBasic 只实现 driver.Stmt：用于断言 StmtContext 缺失时 rewriteStmt 走 ErrSkip。
type stmtBasic struct{ inner *fullStmt }

func (s *stmtBasic) Close() error  { return s.inner.Close() }
func (s *stmtBasic) NumInput() int { return s.inner.NumInput() }
func (s *stmtBasic) Exec(args []driver.Value) (driver.Result, error) {
	return s.inner.Exec(args)
}
func (s *stmtBasic) Query(args []driver.Value) (driver.Rows, error) {
	return s.inner.Query(args)
}

type coreConn struct {
	mu           sync.Mutex
	failPrepare  bool
	prepareQuery string
	queryerQuery string
	execerQuery  string
	prepCtxQuery string
	pingCount    int
	beginCount   int
	resetCount   int
	valid        bool
	stmt         driver.Stmt
}

func (c *coreConn) Prepare(q string) (driver.Stmt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failPrepare {
		return nil, errors.New("prepare failed")
	}
	c.prepareQuery = q
	if c.stmt != nil {
		return c.stmt, nil
	}
	return &fullStmt{conn: c}, nil
}

func (c *coreConn) Close() error   { return nil }
func (c *coreConn) Begin() (driver.Tx, error) {
	c.beginCount++
	return &fakeTx{}, nil
}

// connAll 实现全部可选连接能力。
type connAll struct{ core *coreConn }

func (c *connAll) Prepare(q string) (driver.Stmt, error) { return c.core.Prepare(q) }
func (c *connAll) Close() error                          { return c.core.Close() }
func (c *connAll) Begin() (driver.Tx, error)             { return c.core.Begin() }
func (c *connAll) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	c.core.prepCtxQuery = q
	return c.core.Prepare(q)
}
func (c *connAll) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.core.queryerQuery = q
	return &fakeRows{}, nil
}
func (c *connAll) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.core.execerQuery = q
	return fakeResult{affected: 3}, nil
}
func (c *connAll) Ping(ctx context.Context) error {
	c.core.pingCount++
	return nil
}
func (c *connAll) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.core.beginCount++
	return &fakeTx{}, nil
}
func (c *connAll) IsValid() bool { return c.core.valid }
func (c *connAll) ResetSession(ctx context.Context) error {
	c.core.resetCount++
	return nil
}

// connBasic 只实现 driver.Conn：包装任意 conn 收窄方法集，模拟能力缺失。
type connBasic struct{ driver.Conn }

func wrapBasic(c driver.Conn) driver.Conn { return connBasic{Conn: c} }

type fakeDriver struct {
	mu        sync.Mutex
	conn      driver.Conn
	openErr   error
	connector driver.Connector
	lastDSN   string
}

func (d *fakeDriver) Open(name string) (driver.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastDSN = name
	if d.openErr != nil {
		return nil, d.openErr
	}
	return d.conn, nil
}

// driverCtx 版本：实现 driver.DriverContext 走 OpenConnector 路径。
type fakeDriverCtx struct{ inner *fakeDriver }

func (d *fakeDriverCtx) Open(name string) (driver.Conn, error) { return d.inner.Open(name) }
func (d *fakeDriverCtx) OpenConnector(name string) (driver.Connector, error) {
	if d.inner.openErr != nil {
		return nil, d.inner.openErr
	}
	return &fakeConnector{dsn: name, conn: d.inner.conn}, nil
}

type fakeConnector struct {
	dsn     string
	conn    driver.Conn
	connErr error
}

func (c *fakeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.connErr != nil {
		return nil, c.connErr
	}
	return c.conn, nil
}

func (c *fakeConnector) Driver() driver.Driver {
	return &fakeDriver{conn: c.conn}
}

func TestW20cBindQueryRewritesSequentialPlaceholders(t *testing.T) {
	got := bindQuery("SELECT * FROM t WHERE a = ? AND b IN (?, ?) LIMIT ?")
	want := "SELECT * FROM t WHERE a = $1 AND b IN ($2, $3) LIMIT $4"
	if got != want {
		t.Fatalf("改写结果不符：got %q want %q", got, want)
	}
	if unchanged := bindQuery("SELECT * FROM t WHERE a = $1"); unchanged != "SELECT * FROM t WHERE a = $1" {
		t.Fatalf("无 ? 的 SQL 不应变化：got %q", unchanged)
	}
}

func TestW20cRewriteDriverOpenErrorPropagates(t *testing.T) {
	wantErr := errors.New("open failed")
	ctx := context.Background()
	d := &rewriteDriver{inner: &fakeDriver{openErr: wantErr}}
	if _, err := d.Open("dsn"); !errors.Is(err, wantErr) {
		t.Fatalf("Open 错误应透传：got %v", err)
	}
	// fakeDriver 只实现 Open：OpenConnector 走 dsnConnector 惰性路径，不报错
	if _, err := d.OpenConnector("dsn"); err != nil {
		t.Fatalf("dsnConnector 路径不应在构造期报错：%v", err)
	}
	// DriverContext 实现的 OpenConnector 错误应透传
	dctx := &rewriteDriver{inner: &fakeDriverCtx{inner: &fakeDriver{openErr: wantErr}}}
	if _, err := dctx.OpenConnector("dsn"); !errors.Is(err, wantErr) {
		t.Fatalf("DriverContext OpenConnector 错误应透传：got %v", err)
	}
	_ = ctx
}

func TestW20cRewriteConnectorPaths(t *testing.T) {
	core := &coreConn{valid: true}
	full := &connAll{core: core}
	fctx := &fakeDriverCtx{inner: &fakeDriver{conn: full}}
	d := &rewriteDriver{inner: fctx}
	connector, err := d.OpenConnector("proxy-dsn")
	if err != nil {
		t.Fatalf("OpenConnector 失败：%v", err)
	}
	rc, ok := connector.(rewriteConnector)
	if !ok {
		t.Fatalf("应走 rewriteConnector 路径：got %T", connector)
	}
	_ = rc
	if _, isRew := rc.Driver().(*rewriteDriver); !isRew {
		t.Fatalf("Driver() 应回包 rewriteDriver：got %T", rc.Driver())
	}
	if _, err := (&fakeConnector{connErr: errors.New("connect failed")}).Connect(context.Background()); err == nil {
		t.Fatalf("Connect 错误应透传")
	}
	if _, err := (&rewriteConnector{inner: &fakeConnector{connErr: errors.New("boom")}}).Connect(context.Background()); err == nil {
		t.Fatalf("rewriteConnector.Connect 错误应透传")
	}
	// dsnConnector 直连路径
	dsn := &dsnConnector{name: "x", driver: &fakeDriver{conn: full}}
	if _, err := dsn.Connect(context.Background()); err != nil {
		t.Fatalf("dsnConnector.Connect 失败：%v", err)
	}
	if dsn.Driver() != dsn.driver {
		t.Fatalf("dsnConnector.Driver 应返回原 driver")
	}
	// plain driver 走 dsnConnector 路径且连接成功
	plain := &rewriteDriver{inner: &fakeDriver{conn: full}}
	plainConnector, err := plain.OpenConnector("plain-dsn")
	if err != nil {
		t.Fatalf("plain OpenConnector 失败：%v", err)
	}
	if _, isRewrite := plainConnector.(*rewriteConnector); isRewrite {
		t.Fatalf("plain driver 不应走 rewriteConnector")
	}
	conn, err := plainConnector.Connect(context.Background())
	if err != nil {
		t.Fatalf("plain Connect 失败：%v", err)
	}
	if _, ok := conn.(rewriteConn); !ok {
		t.Fatalf("Connect 产物应包 rewriteConn：got %T", conn)
	}
}

func TestW20cConnAllCapabilitiesRewriteAndForward(t *testing.T) {
	core := &coreConn{valid: true}
	full := &connAll{core: core}
	ctx := context.Background()

	rc := rewriteConn{Conn: full}

	if err := rc.Ping(ctx); err != nil || core.pingCount != 1 {
		t.Fatalf("Ping 应透传：%v count=%d", err, core.pingCount)
	}
	rows, err := rc.QueryContext(ctx, "SELECT ? , ?", []driver.NamedValue{
		{Ordinal: 1, Value: "a"}, {Ordinal: 2, Value: "b"},
	})
	if err != nil {
		t.Fatalf("QueryContext 失败：%v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("rows Close 失败：%v", err)
	}
	if core.queryerQuery != "SELECT $1 , $2" {
		t.Fatalf("QueryContext 未改写：%q", core.queryerQuery)
	}
	if _, err := rc.ExecContext(ctx, "UPDATE t SET a = ? WHERE id = ?", nil); err != nil {
		t.Fatalf("ExecContext 失败：%v", err)
	}
	if core.execerQuery != "UPDATE t SET a = $1 WHERE id = $2" {
		t.Fatalf("ExecContext 未改写：%q", core.execerQuery)
	}
	stmt, err := rc.PrepareContext(ctx, "INSERT INTO t VALUES (?)")
	if err != nil {
		t.Fatalf("PrepareContext 失败：%v", err)
	}
	if core.prepCtxQuery != "INSERT INTO t VALUES ($1)" {
		t.Fatalf("PrepareContext 未改写：%q", core.prepCtxQuery)
	}
	// stmt 层透传 StmtQueryContext/StmtExecContext
	rs, err := stmt.(driver.StmtQueryContext).QueryContext(ctx, nil)
	if err != nil {
		t.Fatalf("stmt QueryContext 失败：%v", err)
	}
	if err := rs.Close(); err != nil {
		t.Fatalf("rows Close 失败：%v", err)
	}
	if _, err := stmt.(driver.StmtExecContext).ExecContext(ctx, nil); err != nil {
		t.Fatalf("stmt ExecContext 失败：%v", err)
	}
	if !rc.IsValid() {
		t.Fatalf("IsValid 应透传 true")
	}
	if err := rc.ResetSession(ctx); err != nil || core.resetCount != 1 {
		t.Fatalf("ResetSession 应透传：%v count=%d", err, core.resetCount)
	}
	tx, err := rc.BeginTx(ctx, driver.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx 失败：%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit 失败：%v", err)
	}
	if core.beginCount != 1 {
		t.Fatalf("BeginTx 应透传：beginCount=%d", core.beginCount)
	}
	// Prepare（非 ctx）路径
	s2, err := rc.Prepare("SELECT ?")
	if err != nil {
		t.Fatalf("Prepare 失败：%v", err)
	}
	if core.prepareQuery != "SELECT $1" {
		t.Fatalf("Prepare 未改写：%q", core.prepareQuery)
	}
	if _, err := s2.Query([]driver.Value{"x"}); err != nil {
		t.Fatalf("stmt Query 失败：%v", err)
	}
	if _, err := s2.Exec([]driver.Value{"x"}); err != nil {
		t.Fatalf("stmt Exec 失败：%v", err)
	}
	if s2.(interface{ NumInput() int }).NumInput() != -1 {
		t.Fatalf("NumInput 应透传 -1")
	}
}

func TestW20cConnBasicFallbacks(t *testing.T) {
	core := &coreConn{}
	basic := wrapBasic(&connAll{core: core})
	ctx := context.Background()
	rc := rewriteConn{Conn: basic}

	if _, err := rc.QueryContext(ctx, "SELECT ?", nil); !errors.Is(err, driver.ErrSkip) {
		t.Fatalf("无 QueryerContext 应返回 ErrSkip：got %v", err)
	}
	if _, err := rc.ExecContext(ctx, "UPDATE ?", nil); !errors.Is(err, driver.ErrSkip) {
		t.Fatalf("无 ExecerContext 应返回 ErrSkip：got %v", err)
	}
	if err := rc.Ping(ctx); err != nil {
		t.Fatalf("无 Pinger 应回落成功语义：got %v", err)
	}
	if rc.IsValid() != true {
		t.Fatalf("无 Validator 应默认 true")
	}
	if err := rc.ResetSession(ctx); !errors.Is(err, driver.ErrSkip) {
		t.Fatalf("无 SessionResetter 应返回 ErrSkip：got %v", err)
	}
	if _, err := rc.BeginTx(ctx, driver.TxOptions{ReadOnly: true}); err == nil {
		t.Fatalf("无 ConnBeginTx 且非默认选项应报错")
	}
	// 降级 Prepare 路径（database/sql 收到 ErrSkip 后走 Prepare）
	if _, err := rc.PrepareContext(ctx, "SELECT ?"); err != nil {
		t.Fatalf("降级 PrepareContext 失败：%v", err)
	}
	if core.prepareQuery != "SELECT $1" {
		t.Fatalf("降级 Prepare 未改写：%q", core.prepareQuery)
	}
	// database/sql 端到端：ErrSkip → Prepare 路径仍改写
	db := sql.OpenDB(&dsnConnector{name: "", driver: &rewriteDriver{inner: &fakeDriver{conn: basic}}})
	defer db.Close()
	if _, err := db.ExecContext(ctx, "DELETE FROM t WHERE a = ? AND b = ?", "x", "y"); err != nil {
		t.Fatalf("端到端 Exec 失败：%v", err)
	}
	if core.prepareQuery != "DELETE FROM t WHERE a = $1 AND b = $2" {
		t.Fatalf("端到端未改写：%q", core.prepareQuery)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("端到端 Ping 应成功：%v", err)
	}
}

func TestW20cStmtBasicFallbacks(t *testing.T) {
	core := &coreConn{}
	full := &fullStmt{conn: core}
	basic := &stmtBasic{inner: full}
	ctx := context.Background()

	rs := rewriteStmt{Stmt: basic}
	if _, err := rs.QueryContext(ctx, nil); !errors.Is(err, driver.ErrSkip) {
		t.Fatalf("无 StmtQueryContext 应返回 ErrSkip：got %v", err)
	}
	if _, err := rs.ExecContext(ctx, nil); !errors.Is(err, driver.ErrSkip) {
		t.Fatalf("无 StmtExecContext 应返回 ErrSkip：got %v", err)
	}
	if !strings.Contains(full.queryUsedString(), "") || full.queryUsed {
		t.Fatalf("ErrSkip 路径不应触达底层语句")
	}
}

func TestW20cConnPrepareErrorAndBeginFallback(t *testing.T) {
	ctx := context.Background()
	core := &coreConn{failPrepare: true}
	basic := wrapBasic(&connAll{core: core})
	rc := rewriteConn{Conn: basic}

	if _, err := rc.Prepare("SELECT ?"); err == nil {
		t.Fatalf("底层 Prepare 错误应透传")
	}
	if _, err := rc.PrepareContext(ctx, "SELECT ?"); err == nil {
		t.Fatalf("底层 PrepareContext 降级错误应透传")
	}
	// 默认选项 + 无 ConnBeginTx → 降级 Begin()
	coreOK := &coreConn{}
	rcOK := rewriteConn{Conn: wrapBasic(&connAll{core: coreOK})}
	tx, err := rcOK.BeginTx(ctx, driver.TxOptions{})
	if err != nil {
		t.Fatalf("默认选项应降级 Begin：%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback 失败：%v", err)
	}
	if coreOK.beginCount != 1 {
		t.Fatalf("降级 Begin 应触达底层：beginCount=%d", coreOK.beginCount)
	}
}

func TestW20cConnectorConnectAndInjectableDriver(t *testing.T) {
	ctx := context.Background()
	// rewriteConnector.Connect 成功路径
	core := &coreConn{valid: true}
	full := &connAll{core: core}
	d := &rewriteDriver{inner: &fakeDriverCtx{inner: &fakeDriver{conn: full}}}
	connector, err := d.OpenConnector("dsn")
	if err != nil {
		t.Fatalf("OpenConnector 失败：%v", err)
	}
	conn, err := connector.(rewriteConnector).Connect(ctx)
	if err != nil {
		t.Fatalf("rewriteConnector.Connect 失败：%v", err)
	}
	if _, ok := conn.(rewriteConn); !ok {
		t.Fatalf("Connect 产物应包 rewriteConn：got %T", conn)
	}
}

func (s *fullStmt) queryUsedString() string { return "queryUsed" }

func TestW20cOpenDriverSelectsRewriteForPgx(t *testing.T) {
	db, err := openDriver("pgx", "postgres://127.0.0.1:5432/unused")
	if err != nil {
		t.Fatalf("pgx 打开失败：%v", err)
	}
	if db == nil {
		t.Fatalf("pgx 路径应返回句柄")
	}
	db.Close()

	// 注入 OpenConnector 即报错的 fake driver，覆盖 openDriver 错误分支
	original := defaultPGXDriver
	defaultPGXDriver = &fakeDriverCtx{inner: &fakeDriver{openErr: errors.New("connector rejected")}}
	defer func() { defaultPGXDriver = original }()
	if errDB, err := openDriver("pgx", "dsn"); err == nil || errDB != nil {
		t.Fatalf("OpenConnector 错误应使 openDriver 失败：err=%v db=%v", err, errDB)
	}

	unique := "w20c-fake-driver"
	if existing := sql.Drivers(); !containsString(existing, unique) {
		sql.Register(unique, &fakeDriver{})
	}
	fakeDB, err := openDriver(unique, "")
	if err != nil {
		t.Fatalf("非 pgx driver 打开失败：%v", err)
	}
	if fakeDB == nil {
		t.Fatalf("非 pgx 路径应返回句柄")
	}
	fakeDB.Close()
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
