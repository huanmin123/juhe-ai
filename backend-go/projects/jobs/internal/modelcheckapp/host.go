// Package modelcheckapp assembles the Go-owned J3b control plane. It contains
// only process wiring; domain behavior remains in the modelcheck* packages.
package modelcheckapp

import (
	"context"
	"database/sql"
	"errors"
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
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("open J3b business SQLite: %w", err)
		}
		business.SetMaxOpenConns(32)
		business.SetMaxIdleConns(8)
		r, e := modelchecksource.NewSQLiteReader(business, cfg.CredentialSecret, cfg.IdentitySecret, time.Now)
		if e != nil {
			closeAll()
			return nil, e
		}
		source, sourceAny, sourceContract = r, r, r
		policy, err = modelcheckpolicy.NewSQLiteReader(business)
		if err != nil {
			closeAll()
			return nil, err
		}
	} else {
		mode = modelcheckauth.Postgres
		durable, err = modelcheckdurable.OpenPostgres(cfg.JobsPostgresURL, 1000)
		if err != nil {
			return nil, err
		}
		dataset, err = modelcheckstore.OpenPostgres(cfg.JobsPostgresURL, 1000, sqlpool.MaxIdleConns)
		if err != nil {
			closeAll()
			return nil, err
		}
		business, err = sql.Open("pgx", cfg.BusinessPostgresURL)
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("open J3b business PostgreSQL: %w", err)
		}
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
		if err != nil {
			closeAll()
			return nil, err
		}
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
	auth, err := modelcheckauth.New(business, mode, time.Now)
	if err != nil {
		closeAll()
		return nil, err
	}
	if err := auth.CheckContract(ctx); err != nil {
		closeAll()
		return nil, fmt.Errorf("verify J3b authentication contract: %w", err)
	}
	builder, err := modelcheckcommand.New(modelcheckcommand.Config{Freezer: source, PolicyLoader: policy, ProbeSetVersion: cfg.ProbeSetVersion, Deadline: cfg.Deadline, Now: time.Now})
	if err != nil {
		closeAll()
		return nil, err
	}
	targetResolver, ok := sourceAny.(modelcheckexecutor.TargetResolver)
	if !ok {
		closeAll()
		return nil, errors.New("J3b business reader does not implement target resolution")
	}
	service := &modelcheckruntime.Service{Durable: durable, Dataset: dataset, Resolver: targetResolver, Active: modelcheckactive.NewRegistry(), Now: time.Now}
	scopeReader, ok := sourceAny.(modelcheckhttp.ManagementTargetScopeReader)
	if !ok {
		closeAll()
		return nil, errors.New("J3b business reader does not implement management scope resolution")
	}
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
