package modelcheckowner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

// HostDependencies are the explicit in-process ports required to expose J3b.
// No dependency may be an HTTP/IPC/queue adapter; source and authentication
// are owned by the Gateway process and are supplied by its management layer.
type HostDependencies struct {
	Resolve           Resolver
	ResolveComparison Resolver
	Tokenizer         modelcheckprobe.Tokenizer
	ModelLimits       modelcheckprobe.ModelLimitSnapshot
	AccountOptions    AccountOptions
	Authorize         Authorize
	Build             BuildRequest
	Scheduler         SchedulerSource
	Executor          SchedulerExecutor
	Enforcement       EnforcementApplier
	Quality           QualityManagement
	ExecutorFactory   func(*Runtime, *QualityProjector) SchedulerExecutor
	SchedulerFactory  func(*Store, *Runtime, *QualityProjector) (SchedulerSource, SchedulerExecutor)
}

type Host struct {
	Store     *Store
	Runtime   *Runtime
	Projector *QualityProjector
	Handler   http.Handler
	Scheduler *Scheduler
	ready     atomic.Bool
}

func OpenHost(ctx context.Context, cfg Config, deps HostDependencies) (*Host, error) {
	if !cfg.Enabled {
		return nil, errors.New("J3b Gateway owner config is disabled")
	}
	if deps.Resolve == nil || deps.Authorize == nil || deps.Build == nil || deps.Enforcement == nil || deps.Quality == nil {
		return nil, errors.New("J3b Gateway owner dependencies are incomplete")
	}
	// Full-profile probes must have explicit tokenizer and model-limit
	// snapshots. Allowing a nil dependency would silently mark those probe
	// families as skipped and could make an incomplete observation look like a
	// runnable owner. Keep the host fail-closed until Gateway wires real,
	// versioned sources.
	if deps.Tokenizer == nil || strings.TrimSpace(deps.Tokenizer.Version()) == "" {
		return nil, errors.New("J3b Gateway tokenizer snapshot is not configured")
	}
	if deps.ModelLimits == nil || strings.TrimSpace(deps.ModelLimits.Version()) == "" {
		return nil, errors.New("J3b Gateway model-limit snapshot is not configured")
	}
	store, err := OpenStore(cfg)
	if err != nil {
		return nil, fmt.Errorf("open J3b Gateway store: %w", err)
	}
	closeOnError := func(e error) (*Host, error) {
		_ = store.Close()
		return nil, e
	}
	if err := store.CheckSchema(ctx); err != nil {
		return closeOnError(fmt.Errorf("verify J3b Gateway schema: %w", err))
	}
	projector := &QualityProjector{Store: store, Enforcement: deps.Enforcement}
	runtime := &Runtime{Store: store, Resolve: deps.Resolve, ResolveComparison: deps.ResolveComparison, Tokenizer: deps.Tokenizer, ModelLimits: deps.ModelLimits, Projector: projector, OwnerID: cfg.InstanceID}
	handler := &HTTPHandler{Service: runtime, Quality: deps.Quality, AccountOptions: deps.AccountOptions, Active: modelcheckactive.NewRegistry(), Authorize: deps.Authorize, Build: deps.Build}
	// HTTP and scheduler share the same Runtime/Store but never call across
	// processes. A Gateway owner is not ready until all durable scheduler
	// dependencies are present; serving only the HTTP half would create a
	// partial owner and leave scheduled/recovery work without an owner.
	schedulerSource := deps.Scheduler
	executor := deps.Executor
	if deps.SchedulerFactory != nil {
		schedulerSource, executor = deps.SchedulerFactory(store, runtime, projector)
	}
	if schedulerSource == nil || (executor == nil && deps.ExecutorFactory == nil) {
		return closeOnError(errors.New("J3b scheduler dependencies are incomplete"))
	}
	if executor == nil {
		executor = deps.ExecutorFactory(runtime, projector)
		if executor == nil {
			return closeOnError(errors.New("J3b scheduler executor factory returned nil"))
		}
	}
	scheduler := &Scheduler{Source: schedulerSource, Executor: executor}
	host := &Host{Store: store, Runtime: runtime, Projector: projector, Handler: handler, Scheduler: scheduler}
	host.ready.Store(true)
	return host, nil
}

func (h *Host) Ready() bool { return h != nil && h.ready.Load() }

// Run is the Gateway lifecycle entry point for the J3b owner. The host is
// deliberately not considered runnable until a durable scheduler and its
// executor have both been supplied; a partially assembled owner must fail
// closed instead of serving only HTTP routes.
func (h *Host) Run(ctx context.Context) error {
	if h == nil {
		return errors.New("J3b Gateway host is nil")
	}
	if !h.Ready() {
		return errors.New("J3b Gateway host is not ready")
	}
	if h.Scheduler == nil {
		return errors.New("J3b Gateway scheduler is not configured")
	}
	return h.Scheduler.Run(ctx)
}

// Component adapts the host to the Gateway's common supervisor. The
// supervisor owns retry and shutdown boundaries; Host owns its store and
// scheduler resources.
func (h *Host) Component() supervisor.Component {
	return supervisor.Component{
		Name: "J3b model-check owner",
		Run:  h.Run,
		Close: func() error {
			if h == nil {
				return nil
			}
			return h.Close()
		},
	}
}

// Mount registers the complete J3b management surface under one explicit
// prefix. The prefix is stripped before dispatch so the handler keeps the
// stable Node-compatible paths (/run, /runs, ...). Mount refuses an
// unready host to prevent a route from being advertised before owner gates
// have passed.
func (h *Host) Mount(mux *http.ServeMux, prefix string) error {
	if h == nil || !h.Ready() || h.Handler == nil {
		return errors.New("J3b Gateway host is not ready")
	}
	if mux == nil {
		return errors.New("J3b Gateway route mux is nil")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix[0] != '/' || !strings.HasSuffix(prefix, "/") {
		return errors.New("J3b Gateway route prefix must be an absolute path ending with slash")
	}
	mux.Handle(prefix, http.StripPrefix(strings.TrimSuffix(prefix, "/"), h.Handler))
	return nil
}

func (h *Host) Close() error {
	if h == nil || h.Store == nil {
		return nil
	}
	h.ready.Store(false)
	return h.Store.Close()
}
