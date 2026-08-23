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

func (r *Registry) shared() *sqlpool.Registry {
	r.initOnce.Do(func() {
		r.inner = sqlpool.NewRegistry()
	})
	return r.inner
}
