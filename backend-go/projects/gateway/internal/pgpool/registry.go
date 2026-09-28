package pgpool

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
	pgx "github.com/jackc/pgx/v5"
	stdlib "github.com/jackc/pgx/v5/stdlib"
)

// sqlDebugTracer 是临时诊断工具(JUHE_AI_DEBUG_SQL=1 启用):打印每条失败
// SQL 的完整语句与参数,用于定位 PG 方言回归;诊断完成后移除。
type sqlDebugTracer struct{}

type sqlDebugKey struct{}

func (sqlDebugTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, sqlDebugKey{}, data)
}

func (sqlDebugTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil {
		return
	}
	if start, ok := ctx.Value(sqlDebugKey{}).(pgx.TraceQueryStartData); ok {
		slog.Error("SQL_DEBUG 查询失败", "sql", start.SQL, "args", fmt.Sprint(start.Args), "err", data.Err.Error())
	}
}

// defaultPGXDriver 可注入点：测试用 fake driver 覆盖 OpenConnector 错误分支
// 与改写接线（rewrite.go）。直接持有 pgx stdlib 的默认 driver 实例并就地包
// rewriteDriver，不向 database/sql 注册新驱动名，避免与 pgx 原生名冲突。
var defaultPGXDriver driver.Driver = stdlib.GetDefaultDriver()

// openPGX 打开 gateway 的 PG 池句柄：两条臂都统一套方言改写 driver
// （rewrite.go）。默认臂与 sql.Open("pgx", url) 惰性语义一致（pgx 的
// OpenConnector 是惰性包装，DSN 解析延迟到 Connect）；JUHE_AI_DEBUG_SQL=1
// 调试臂解析 ConnConfig 挂 tracer 后经 stdlib.GetConnector 同样包一层
// rewriteConnector，tracer 行为不变。
func openPGX(url string) (*sql.DB, error) {
	if os.Getenv("JUHE_AI_DEBUG_SQL") != "1" {
		connector, err := (&rewriteDriver{inner: defaultPGXDriver}).OpenConnector(url)
		if err != nil {
			return nil, err
		}
		return sql.OpenDB(connector), nil
	}
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.Tracer = sqlDebugTracer{}
	return sql.OpenDB(rewriteConnector{inner: stdlib.GetConnector(*cfg)}), nil
}

// Registry keeps the gateway-specific pgx opener and delegates pool
// lifecycle/ref-counting to the shared platform implementation.
type Registry struct {
	initOnce sync.Once
	inner    *sqlpool.Registry
}

type Key = sqlpool.Key
type Handle = sqlpool.Handle
type PoolEvent = sqlpool.PoolEvent

func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) Acquire(url, role string, maxOpen, maxIdle int) (*Handle, error) {
	return r.AcquireWith(func() (*sql.DB, error) { return openPGX(url) }, url, role, maxOpen, maxIdle)
}

func (r *Registry) AcquireWith(open func() (*sql.DB, error), url, role string, maxOpen, maxIdle int) (*Handle, error) {
	if r == nil {
		return nil, errors.New("gateway postgres pool registry 未初始化")
	}
	return r.shared().Acquire(open, url, role, maxOpen, maxIdle)
}

func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	return r.shared().Close()
}

// SetObserver forwards credential-free pool lifecycle events to gateway-owned
// logging/metrics code without coupling this package to a telemetry backend
// (mirror of the jobs-side registry). The shared contract already excludes the
// URL, so a DSN password can never reach the observer.
func (r *Registry) SetObserver(observer func(PoolEvent)) {
	if r == nil {
		return
	}
	r.shared().SetObserver(observer)
}

func (r *Registry) shared() *sqlpool.Registry {
	r.initOnce.Do(func() {
		r.inner = sqlpool.NewRegistry()
	})
	return r.inner
}
