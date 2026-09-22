package pgpool

import (
	"context"
	"database/sql/driver"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
)

// rewriteDriver 装配层 PG 方言改写：把方言共享 SQL 的顺序 `?` 占位符统一改写
// 为 PostgreSQL 的 `$n`。pgx stdlib 不做 `?` → `$n` 改写，含 `?` 的 SQL 直发
// 即 42601（w20c 测试环境实测四类语句：投影列表 CTE、探针候选、circuit
// outbox、circuit incident）。在 driver 层改写使 pgpool 全部 PG 池的消费方
// 零改动获得改写，SQLite driver 不经过此路径。
//
// 幂等性：已改写为 `$n` 的 SQL 不含 `?`，现有局部改写实现（cleanuprepo 的
// DB.Bind、circuitstore 的 boundDB、accountquality 的 dollarize、
// oauthrefresh/recordmaintenance 的局部器）先于本层执行时产出不含 `?` 的
// SQL，两层互不干扰。
//
// 前置约束：本 driver 覆盖范围内的 SQL 不允许把 `?` 用作字符串字面量内容
// 或 jsonb 存在运算符（w20c 对 jobs + shared 全量扫描确认无此用法）；
// 引入此类 SQL 时必须改用参数传递或先改写为 `$n`。

type rewriteDriver struct {
	inner driver.Driver
}

func (d *rewriteDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return rewriteConn{Conn: conn}, nil
}

func (d *rewriteDriver) OpenConnector(name string) (driver.Connector, error) {
	if dc, ok := d.inner.(driver.DriverContext); ok {
		inner, err := dc.OpenConnector(name)
		if err != nil {
			return nil, err
		}
		return rewriteConnector{inner: inner}, nil
	}
	return dsnConnector{name: name, driver: d}, nil
}

type rewriteConnector struct {
	inner driver.Connector
}

func (c rewriteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return rewriteConn{Conn: conn}, nil
}

func (c rewriteConnector) Driver() driver.Driver {
	return &rewriteDriver{inner: c.inner.Driver()}
}

type dsnConnector struct {
	name   string
	driver driver.Driver
}

func (c dsnConnector) Connect(ctx context.Context) (driver.Conn, error) {
	return c.driver.Open(c.name)
}

func (c dsnConnector) Driver() driver.Driver { return c.driver }

func bindQuery(query string) string {
	return sqldialect.BindSQL(true, query)
}

// rewriteConn 透传 pgx 连接的完整能力面。database/sql 依据 conn 的静态类型
// 决定能力（Ping/BeginTx/QueryContext/…），嵌入 driver.Conn 接口只提升
// Begin/Close/Prepare，其余能力必须显式断言透传，否则降级或失效。

type rewriteConn struct {
	driver.Conn
}

func (c rewriteConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(bindQuery(query))
	if err != nil {
		return nil, err
	}
	return rewriteStmt{Stmt: stmt}, nil
}

func (c rewriteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	inner, ok := c.Conn.(driver.ConnPrepareContext)
	if !ok {
		return c.Prepare(query)
	}
	stmt, err := inner.PrepareContext(ctx, bindQuery(query))
	if err != nil {
		return nil, err
	}
	return rewriteStmt{Stmt: stmt}, nil
}

func (c rewriteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	inner, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return inner.QueryContext(ctx, bindQuery(query), args)
}

func (c rewriteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	inner, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return inner.ExecContext(ctx, bindQuery(query), args)
}

func (c rewriteConn) Ping(ctx context.Context) error {
	if inner, ok := c.Conn.(driver.Pinger); ok {
		return inner.Ping(ctx)
	}
	// 底层无 Pinger 时与 database/sql 对非 Pinger 驱动的语义一致：
	// 能取到连接即视为存活；ErrSkip 在 Ping 路径不被 database/sql 兜底。
	return nil
}

func (c rewriteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if inner, ok := c.Conn.(driver.ConnBeginTx); ok {
		return inner.BeginTx(ctx, opts)
	}
	if opts != (driver.TxOptions{}) {
		return nil, errors.New("driver 不支持非默认事务选项")
	}
	return c.Conn.Begin()
}

func (c rewriteConn) IsValid() bool {
	if inner, ok := c.Conn.(driver.Validator); ok {
		return inner.IsValid()
	}
	return true
}

func (c rewriteConn) ResetSession(ctx context.Context) error {
	if inner, ok := c.Conn.(driver.SessionResetter); ok {
		return inner.ResetSession(ctx)
	}
	return driver.ErrSkip
}

// rewriteStmt 透传语句级上下文执行能力；query 文本已在 Prepare 阶段改写。

type rewriteStmt struct {
	driver.Stmt
}

func (s rewriteStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if inner, ok := s.Stmt.(driver.StmtQueryContext); ok {
		return inner.QueryContext(ctx, args)
	}
	return nil, driver.ErrSkip
}

func (s rewriteStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if inner, ok := s.Stmt.(driver.StmtExecContext); ok {
		return inner.ExecContext(ctx, args)
	}
	return nil, driver.ErrSkip
}
