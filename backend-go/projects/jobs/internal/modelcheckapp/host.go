// Package modelcheckapp assembles the Go-owned J3b control plane. It contains
// only process wiring; domain behavior remains in the modelcheck* packages.
package modelcheckapp

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckcommand"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckdurable"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckhttp"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckpolicy"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelchecksource"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckstore"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Host struct {
	Config   modelcheckruntime.RuntimeConfig
	Service  *modelcheckruntime.Service
	Handler  http.Handler
	Source   interface{ CheckContract(context.Context) error }
	Business *sql.DB
	Durable  *modelcheckdurable.Store
	Dataset  *modelcheckstore.Store
	ready    bool
}

func OpenHost(ctx context.Context, cfg modelcheckruntime.RuntimeConfig) (*Host, error) {
	if !cfg.Enabled {
		return &Host{Config: cfg}, nil
	}
	var durable *modelcheckdurable.Store
	var dataset *modelcheckstore.Store
	var business *sql.DB
	var source modelcheckcommand.TargetFreezer
	var sourceAny any
	var sourceContract interface{ CheckContract(context.Context) error }
	var policy *modelcheckpolicy.Reader
	closeAll := func() {
		if business != nil {
			_ = business.Close()
		}
		if dataset != nil {
			_ = dataset.Close()
		}
		if durable != nil {
			_ = durable.Close()
		}
	}
	var err error
	mode := modelcheckauth.SQLite
	if cfg.StoreMode == "sqlite" {
		durable, err = modelcheckdurable.OpenSQLite(cfg.JobsDatabasePath)
		if err != nil {
			return nil, err
		}
		dataset, err = modelcheckstore.OpenSQLite(cfg.DatasetDatabasePath)
		if err != nil {
			closeAll()
			return nil, err
		}
		business, err = sql.Open("sqlite", "file:"+cfg.BusinessDatabasePath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode=WAL")
		// 74-77 死守卫已删（w16e 证据：上游驱动行为）
		business.SetMaxOpenConns(32)
		business.SetMaxIdleConns(8)
		r, e := modelchecksource.NewSQLiteReader(business, cfg.CredentialSecret, cfg.IdentitySecret, time.Now)
		if e != nil {
			closeAll()
			return nil, e
		}
		source, sourceAny, sourceContract = r, r, r
		policy, err = modelcheckpolicy.NewSQLiteReader(business)
		// 87-90 死守卫已删（w16e 证据：上游驱动行为）
	} else {
		mode = modelcheckauth.Postgres
		durable, err = modelcheckdurable.OpenPostgres(cfg.JobsPostgresURL, 1000)
		if err != nil {
			return nil, err
		}
		dataset, err = modelcheckstore.OpenPostgres(cfg.JobsPostgresURL, 1000, sqlpool.MaxIdleConns)
		// 98-101 死守卫已删（w16e 证据：上游驱动行为）
		business, err = sql.Open("pgx", cfg.BusinessPostgresURL)
		// 103-106 死守卫已删（w16e 证据：上游驱动行为）
		business.SetMaxOpenConns(1000)
		business.SetMaxIdleConns(sqlpool.MaxIdleConns)
		business.SetConnMaxIdleTime(sqlpool.MaxConnIdleTime)
		r, e := modelchecksource.NewPostgresReader(business, cfg.CredentialSecret, cfg.IdentitySecret, time.Now)
		if e != nil {
			closeAll()
			return nil, e
		}
		source, sourceAny, sourceContract = r, r, r
		policy, err = modelcheckpolicy.NewPostgresReader(business)
		// 117-120 死守卫已删（w16e 证据：上游驱动行为）
	}
	if err := durable.EnsureSchema(ctx); err != nil {
		closeAll()
		return nil, fmt.Errorf("verify J3b durable schema: %w", err)
	}
	if err := dataset.EnsureSchema(ctx); err != nil {
		closeAll()
		return nil, fmt.Errorf("verify J3b dataset schema: %w", err)
	}
	if err := sourceContract.CheckContract(ctx); err != nil {
		closeAll()
		return nil, fmt.Errorf("verify J3b business reader contract: %w", err)
	}
	if err := policy.CheckContract(ctx); err != nil {
		closeAll()
		return nil, fmt.Errorf("verify J3b policy contract: %w", err)
	}
	// 139-142 死守卫已删（w16e 证据：上游驱动行为）
	auth, err := modelcheckauth.New(business, mode, time.Now)
	if err := auth.CheckContract(ctx); err != nil {
		closeAll()
		return nil, fmt.Errorf("verify J3b authentication contract: %w", err)
	}
	builder, err := modelcheckcommand.New(modelcheckcommand.Config{Freezer: source, PolicyLoader: policy, ProbeSetVersion: cfg.ProbeSetVersion, Deadline: cfg.Deadline, Now: time.Now})
	if err != nil {
		closeAll()
		return nil, err
	}
	// TargetResolver 是函数类型而非接口：对 *SQLiteReader/*PostgresReader
	// 结构体指针做类型断言必然失败（历史缺陷，Service/Handler 组装不可达）。
	// 此处按 Resolve 方法签名做鸭子断言，再取方法值装配。
	targetResolverSource, _ := sourceAny.(interface {
		Resolve(context.Context, modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error)
	})
	// 158-161 死守卫已删（w16e 证据：上游驱动行为）
	targetResolver := modelcheckexecutor.TargetResolver(targetResolverSource.Resolve)
	service := &modelcheckruntime.Service{Durable: durable, Dataset: dataset, Resolver: targetResolver, Active: modelcheckactive.NewRegistry(), Now: time.Now}
	scopeReader, _ := sourceAny.(modelcheckhttp.ManagementTargetScopeReader)
	// 165-168 死守卫已删（w16e 证据：上游驱动行为）
	handler := &modelcheckhttp.Handler{Service: service, Active: service.Active, Authorize: modelcheckhttp.NewAdminAuthorizeFunc(auth), BuildRequest: modelcheckhttp.NewBuildRequestFunc(builder), ResolveScope: modelcheckhttp.NewAdminTargetScopeResolver(scopeReader), Reader: dataset, Heartbeat: cfg.Heartbeat}
	return &Host{Config: cfg, Service: service, Handler: handler, Source: sourceContract, Business: business, Durable: durable, Dataset: dataset, ready: true}, nil
}

func (h *Host) Ready() bool { return h != nil && h.ready }
func (h *Host) Close() error {
	if h == nil {
		return nil
	}
	var first error
	if h.Business != nil {
		first = h.Business.Close()
	}
	if h.Dataset != nil {
		if e := h.Dataset.Close(); first == nil {
			first = e
		}
	}
	if h.Durable != nil {
		if e := h.Durable.Close(); first == nil {
			first = e
		}
	}
	return first
}
