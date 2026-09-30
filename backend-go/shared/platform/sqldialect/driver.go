package sqldialect

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
)

// Driver 是装配层 PG 方言改写 driver：把方言共享 SQL 的顺序 `?` 占位符统一
// 改写为 PostgreSQL 的 `$n`。这是消灭 BUG-0177/BUG-0219/BUG-0235 占位符
// 家族的结构性防御：方言共享 SQL 裸 `?` 直发 pgx 即 42601 语法错误——pgx
// stdlib 不做 `?` → `$n` 改写（w20c 测试环境实测四类语句：投影列表 CTE、
// 探针候选、circuit outbox、circuit incident）。在 driver 层兜底改写，使任
// 何未来遗漏 bind() 的执行点在 pgx 下也能正确执行，不再以方言语法错误暴露；
// SQLite driver 不经过此路径。
//
// 2026-09-30（清理批次 C5）：自 gateway/jobs pgpool 的双副本 rewrite.go
// 逐字节等价收敛而来；accountbalance/gometrics 的裸开 pgx 路径同批接入。
//
// 幂等双保险与共存关系：各 chain 文件的手动 bind() 保留不动——bind() 先把
// SQL 改写为 `$n`，到达本层的 SQL 不含 `?`，驱动层无可改写、原样透传；遗漏
// bind() 时由本层兜底改写。两层互不干扰，输出等价。已改写为 `$n` 的 SQL 不
// 含 `?`，各局部改写实现（cleanuprepo 的 DB.Bind、circuitstore 的 boundDB、
// accountquality 的 dollarize、oauthrefresh/recordmaintenance 的局部器）先于
// 本层执行时两层同样互不干扰。
//
// 前置约束：本 driver 覆盖范围内的 SQL 不允许把 `?` 用作字符串字面量内容
// 或 jsonb 存在运算符（gateway internal 与 jobs + shared 全量扫描确认无此用
// 法）；引入此类 SQL 时必须改用参数传递或先改写为 `$n`。
type Driver struct {
	inner driver.Driver
}

// WrapDriver 把 inner 包一层 `?`→`$n` 方言改写。不向 database/sql 注册新驱动
// 名，避免与 pgx 原生名冲突；调用方直接持有 inner（如 pgx stdlib 默认 driver
// 实例）并就地包装。
func WrapDriver(inner driver.Driver) *Driver {
	return &Driver{inner: inner}
}

// OpenDB 打开经改写包装的句柄：等价
// sql.OpenDB(WrapDriver(inner).OpenConnector(dsn))，OpenConnector 错误上抛。
// 对惰性 driver（如 pgx stdlib：OpenConnector 是惰性包装，DSN 解析延迟到
// Connect）与 sql.Open(inner 注册名, dsn) 语义一致。
func OpenDB(inner driver.Driver, dsn string) (*sql.DB, error) {
	connector, err := WrapDriver(inner).OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

func (d *Driver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return rewriteConn{Conn: conn}, nil
}

func (d *Driver) OpenConnector(name string) (driver.Connector, error) {
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
	return &Driver{inner: c.inner.Driver()}
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
	return BindSQL(true, query)
}

// rewriteConn 透传底层连接的完整能力面。database/sql 依据 conn 的静态类型
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

func (c rewriteConn) CheckNamedValue(nv *driver.NamedValue) error {
	if inner, ok := c.Conn.(driver.NamedValueChecker); ok {
		return inner.CheckNamedValue(nv)
	}
	// 与 database/sql 对无 NamedValueChecker 驱动的默认行为一致：
	// ErrSkip 触发 DefaultParameterConverter 回落。
	return driver.ErrSkip
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

func (s rewriteStmt) CheckNamedValue(nv *driver.NamedValue) error {
	if inner, ok := s.Stmt.(driver.NamedValueChecker); ok {
		return inner.CheckNamedValue(nv)
	}
	return driver.ErrSkip
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
