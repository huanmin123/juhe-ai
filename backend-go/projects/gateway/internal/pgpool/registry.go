package pgpool

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
	stdlib "github.com/jackc/pgx/v5/stdlib"
)

// defaultPGXDriver 可注入点：测试用 fake driver 覆盖 OpenConnector 错误分支
// 与改写接线。直接持有 pgx stdlib 的默认 driver 实例（与 database/sql 注册
// 名 "pgx" 指向同一实例），不向 database/sql 注册新驱动名，避免与 pgx 原生
// 名冲突。
var defaultPGXDriver driver.Driver = stdlib.GetDefaultDriver()

// openPGX 打开 gateway 的 PG 池句柄：统一套方言改写 driver
// （shared/platform/sqldialect，清理批次 C5 收敛）。与 sql.Open("pgx", url)
// 惰性语义一致（pgx 的 OpenConnector 是惰性包装，DSN 解析延迟到 Connect）。
// 原 JUHE_AI_DEBUG_SQL 临时调试臂已随 BUG-0219 诊断完成删除（清理批次 C6）。
func openPGX(url string) (*sql.DB, error) {
	return sqldialect.OpenDB(defaultPGXDriver, url)
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
