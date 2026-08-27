package pgpool

import (
	"database/sql"
	"errors"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
	_ "github.com/jackc/pgx/v5/stdlib"
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

func (r *Registry) Acquire(driver, url, role string, maxOpen, maxIdle int) (*Handle, error) {
	return r.AcquireWith(func() (*sql.DB, error) { return sql.Open(driver, url) }, url, role, maxOpen, maxIdle)
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
