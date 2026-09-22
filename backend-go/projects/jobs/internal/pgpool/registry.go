package pgpool

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// Registry keeps the jobs-specific driver opener and delegates pool
// lifecycle/ref-counting to the shared platform implementation.
type Registry struct {
	initOnce sync.Once
	inner    *sqlpool.Registry
}

type Key = sqlpool.Key
type Handle = sqlpool.Handle
type PoolEvent = sqlpool.PoolEvent
type PoolSnapshot = sqlpool.PoolSnapshot

func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) Acquire(driverName, url, role string, maxOpen, maxIdle int) (*Handle, error) {
	return r.AcquireWith(func() (*sql.DB, error) { return openDriver(driverName, url) }, url, role, maxOpen, maxIdle)
}

// defaultPGXDriver 可注入点：测试用 fake driver 覆盖 OpenConnector 错误分支。
var defaultPGXDriver driver.Driver = stdlib.GetDefaultDriver()

// openDriver 打开数据库句柄：pgx 池统一套方言改写 driver（rewrite.go），
// 其余 driver（SQLite）原样打开。pgx 的 OpenConnector 是惰性包装（DSN
// 解析延迟到 Connect），与 sql.Open 惰性语义一致。
func openDriver(driverName, url string) (*sql.DB, error) {
	if driverName != "pgx" {
		return sql.Open(driverName, url)
	}
	connector, err := (&rewriteDriver{inner: defaultPGXDriver}).OpenConnector(url)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

func (r *Registry) AcquireWith(open func() (*sql.DB, error), url, role string, maxOpen, maxIdle int) (*Handle, error) {
	if r == nil {
		return nil, errors.New("postgres pool registry 未初始化")
	}
	return r.shared().Acquire(open, url, role, maxOpen, maxIdle)
}

func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	return r.shared().Close()
}

// SetObserver forwards credential-free pool lifecycle events to jobs-owned
// logging/metrics code without coupling this package to a telemetry backend.
func (r *Registry) SetObserver(observer func(PoolEvent)) {
	if r == nil {
		return
	}
	r.shared().SetObserver(observer)
}

// Stats returns a credential-free snapshot of pools currently held by jobs.
func (r *Registry) Stats() []PoolSnapshot {
	if r == nil {
		return nil
	}
	return r.shared().Stats()
}

func (r *Registry) shared() *sqlpool.Registry {
	r.initOnce.Do(func() {
		r.inner = sqlpool.NewRegistry()
	})
	return r.inner
}
