package pgpool

import (
	"context"
	"database/sql"
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

func openPGX(url string) (*sql.DB, error) {
	if os.Getenv("JUHE_AI_DEBUG_SQL") != "1" {
		return sql.Open("pgx", url)
	}
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.Tracer = sqlDebugTracer{}
	return stdlib.OpenDB(*cfg), nil
}

// Registry keeps the gateway-specific pgx opener and delegates pool
// lifecycle/ref-counting to the shared platform implementation.
type Registry struct {
	initOnce sync.Once
	inner    *sqlpool.Registry
}

type Key = sqlpool.Key
type Handle = sqlpool.Handle

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

func (r *Registry) shared() *sqlpool.Registry {
	r.initOnce.Do(func() {
		r.inner = sqlpool.NewRegistry()
	})
	return r.inner
}
