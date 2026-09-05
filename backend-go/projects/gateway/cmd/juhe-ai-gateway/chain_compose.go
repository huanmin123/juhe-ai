package main

// G20 phase-2 chain assembly: builds every authored adapter, fail-fasts with
// the named missing entry when a required collaborator is absent (no port is
// ever silently nil), and exposes the /v1 handler plus the chat executor.
//
// The deep runtime collaborators (client-ip circuits / policy, user request
// limits, models rate limit) are injected through chainRuntimeDeps and are
// required: the preflight dereferences them on every request, so a missing
// service fails startup with the named port. The dispatch-side ordering /
// suppression / lock collaborators degrade to the explicit disabled
// implementations in chain_ports.go (logged once on use), mirroring the Node
// behaviour when the corresponding runtime feature is absent.

import (
	"fmt"
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// gatewaybodyLogger adapts the chain slog logger onto the gatewaybody.Logger
// (Debug/Info/Warn/Error msg,fields lines).
type gatewaybodyLogger struct{ inner *slog.Logger }

func (l gatewaybodyLogger) Debug(msg string, fields map[string]any) {
	l.inner.Debug(msg, fieldsArgs(fields)...)
}
func (l gatewaybodyLogger) Info(msg string, fields map[string]any) {
	l.inner.Info(msg, fieldsArgs(fields)...)
}
func (l gatewaybodyLogger) Warn(msg string, fields map[string]any) {
	l.inner.Warn(msg, fieldsArgs(fields)...)
}
func (l gatewaybodyLogger) Error(msg string, fields map[string]any) {
	l.inner.Error(msg, fieldsArgs(fields)...)
}

// chainRuntimeDeps carries the concrete runtime services the assembly
// consumes.
type chainRuntimeDeps struct {
	Cache  *gatewayruntimecache.Service
	Clock  gatewaypreauth.Clock
	Logger *slog.Logger
	// AuditLogEnabled mirrors readAuditLogSettings().enabled.
	AuditLogEnabled func() bool
	// AuditInputURL is the F3 loopback audit input server base
	// (http://127.0.0.1:<port>); finalized captures POST to
	// /__aiinternal__/v1/audit-captures.
	AuditInputURL string
	// SpoolDirectory enables the durable usage-record spool.
	SpoolDirectory string

	// G13 runtime services (required: preflight hot path).
	Circuits        gatewaypreauth.PreAuthCircuits
	IPPolicy        gatewaypreauth.ClientIPPolicy
	UserLimits      gatewaypreauth.UserRequestLimits
	ModelsRateLimit gatewaypreauth.AuthenticatedModelsRateLimit

	// G05 preauth quota + client-ip collaborator services (required: the
	// preflight dereferences the quota ports on every authenticated request).
	APIKeyQuota   *gatewayquota.APIKeyQuotaService
	AuthzQuota    *gatewayquota.AuthorizationQuotaService
	InflightQuota *gatewayquota.InflightQuotaService
	Avoidance     gatewaypreauth.ClientIPAccountAvoidanceFactory
	Affinity      *gatewaygemini.InteractionAffinity

	// Dispatch collaborator services (optional: the assembly degrades the
	// avoidance / concurrency trackers to process-local implementations).
	AvoidanceTracker   *gatewayclientip.Avoidance
	ConcurrencyTracker *gatewayclientip.MemoryAccountConcurrency

	// G14/G18 session + codex collaborators (optional; the adapters degrade
	// to the header-only identities Node serves when the resolver is absent).
	Identity    *sessionIdentityServices
	CodexBridge gatewaypreauth.CodexBridgePreflight
	Recoverable gatewaypreauth.RecoverableWait

	// Hybrid routing collaborators (optional; nil keeps the hybrid resolver
	// in the skip state Node produces for non-hybrid keys).
	HybridScoringCache  hybridSharedJSONCache
	HybridRuntimeState  hybridRuntimeStateStore
	HybridAuxiliary     hybridAuxiliaryDispatcher
	HybridUsageRecorder hybridUsageRecorder
	RouteDiagnostics    hybridRouteDiagnostics

	// Suppression / degradation / locks (optional; disabled implementations
	// below keep the attempt loop defined).
	Suppression  gatewaydispatch.SuppressionPort
	Degradation  gatewaydispatch.DegradationPort
	AccountLocks gatewaydispatch.AccountLocks
}

// sessionIdentityServices bundles the G14 services with their secret.
type sessionIdentityServices struct {
	Identity *gatewaysession.IdentityService
	Affinity *gatewaysession.AffinityService
	Secret   string
}

// composeGatewayChain assembles the /v1 chain. It fails fast naming every
// missing required entry instead of serving through nil ports.
func composeGatewayChain(deps chainRuntimeDeps) (*gatewayChain, func(), error) {
	if deps.Cache == nil {
		return nil, nil, fmt.Errorf("网关链缺少 gatewayruntimecache.Service（G10 runtime cache）")
	}
	clock := deps.Clock
	if clock == nil {
		clock = gatewaypreauth.SystemClock{}
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	var missing []string
	if deps.Circuits == nil {
		missing = append(missing, "gatewaypreauth.PreAuthCircuits（gatewayclientip.ErrorCircuit）")
	}
	if deps.IPPolicy == nil {
		missing = append(missing, "gatewaypreauth.ClientIPPolicy（gatewayclientip.PolicyCache）")
	}
	if deps.UserLimits == nil {
		missing = append(missing, "gatewaypreauth.UserRequestLimits（gatewayproxyhealth.UserRequestLimitsService）")
	}
	if deps.ModelsRateLimit == nil {
		missing = append(missing, "gatewaypreauth.AuthenticatedModelsRateLimit（gatewayproxyhealth.AuthenticatedModelsRateLimitService）")
	}
	if deps.APIKeyQuota == nil {
		missing = append(missing, "gatewaypreauth.APIKeyQuota（gatewayquota.APIKeyQuotaService）")
	}
	if deps.AuthzQuota == nil {
		missing = append(missing, "gatewaypreauth.AuthorizationQuota（gatewayquota.AuthorizationQuotaService）")
	}
	if deps.InflightQuota == nil {
		missing = append(missing, "gatewaypreauth.InflightQuota（gatewayquota.InflightQuotaService）")
	}
	if deps.Avoidance == nil {
		missing = append(missing, "gatewaypreauth.ClientIPAccountAvoidanceFactory（gatewayclientip.Avoidance）")
	}
	if deps.Affinity == nil {
		missing = append(missing, "gatewaypreauth.Affinity（gatewaygemini.InteractionAffinity）")
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("AI 网关链装配条件不足，拒绝启动，缺失项：%s", joinChinese(missing))
	}

	// ---- usage service + persistence bridge (adapter 5) ----
	spool := newUsageSpool(deps.SpoolDirectory, clock, logger)
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 4096}, spool)
	dispatch := gatewayusage.NewFinalizationDispatch(recorder, spoolOverflow{spool: spool}, 0, 0)
	dispatch.OverflowEnabled = spool != nil
	usageService := gatewayusage.NewService(dispatch, gatewayusage.ServiceConfig{SyncPricingAllowed: true}).
		WithClock(clock).
		WithLogger(slogLogger{inner: logger})

	// ---- response sink (G16) ----
	sink := gatewayresponse.NewSink(gatewayresponse.SinkDeps{
		UsageRecords:  usageDispatchAdapter{service: usageService, recorder: recorder},
		UsageDispatch: usageDispatchAdapter{service: usageService, recorder: recorder},
		Logger:        gatewayResponseLogger{inner: slog.Default()},
		NowMs:         func() int64 { return clock.Now().UnixMilli() },
	})

	// ---- observability ----
	observability := newSlogObservability(logger, clock)

	// ---- body pipeline (request/body-middleware.ts) ----
	// TextRawBodyLimitMegabytes stays unconfigured: the settings-driven
	// override rides on the G05 runtime snapshot the preflight reads; the
	// capture-time provider lands with that slice (default 16 MiB holds).
	bodyPipeline := gatewaybody.NewMiddleware(gatewaybody.Config{
		Logger: gatewaybodyLogger{inner: logger},
	})

	// ---- route resolver (adapter 1) ----
	normalRoute := gatewayrouting.NewNormalModelRouteService(
		chainRoutingCache{cache: deps.Cache},
		chainCapabilityFilter{},
	)
	routeResolver := &chainRouteResolver{cache: deps.Cache, normal: normalRoute}
	if deps.HybridAuxiliary != nil || deps.HybridScoringCache != nil || deps.HybridRuntimeState != nil {
		hybridAffinity := gatewayhybrid.NewAffinityService(hybridClockOf(clock), hybridSessionIdentityPort{}, hybridRuntimeStateOf(deps.HybridRuntimeState))
		hybridScoring := gatewayhybrid.NewScoringService(hybridClockOf(clock), hybridAuxiliaryOf(deps.HybridAuxiliary), hybridUsageRecorderOf(deps.HybridUsageRecorder), hybridSharedCacheOf(deps.HybridScoringCache), nil)
		routeResolver.scoring = hybridScoring
		routeResolver.hybrid = gatewayhybrid.NewRouteService(hybridAffinity, hybridTargetGroups{cache: deps.Cache}, hybridSessionIdentityPort{}, hybridDiagnosticsOf(deps.RouteDiagnostics))
	}

	// ---- dispatch engine + provider driver (adapter 2) ----
	engine := gatewaydispatch.NewEngine(newChainProviderDriver(), &chainFailureDispatcher{usage: usageService})
	engine.Clock = clock
	engine.Affinity = newLocalSessionAffinity()
	engine.Latency = &degradedLatency{}
	engine.ProxyHealth = &degradedProxyHealth{}
	engine.HotQuality = &degradedHotQuality{}
	engine.ClientSourceAvoidance = &degradedClientSourceAvoidance{}
	engine.ClientIPAvoidance = newChainClientIPAvoidance(deps.AvoidanceTracker)
	engine.Quota = newChainDispatchQuota(deps.AuthzQuota)
	if deps.ConcurrencyTracker == nil {
		deps.ConcurrencyTracker = gatewayclientip.NewMemoryAccountConcurrency(nil)
	}
	engine.Concurrency = newChainConcurrencyStore(deps.ConcurrencyTracker)
	engine.Cache = newChainRuntimeCachePort(deps.Cache)
	engine.Usage = usageAttemptRecorderAdapter{service: usageService}
	engine.Suppression = deps.Suppression
	if engine.Suppression == nil {
		engine.Suppression = &disabledSuppression{}
	}
	engine.Degradation = deps.Degradation
	if engine.Degradation == nil {
		engine.Degradation = &disabledDegradation{}
	}
	engine.Locks = deps.AccountLocks
	if engine.Locks == nil {
		engine.Locks = &disabledAccountLocks{}
	}
	pipeline := engine.CandidatePipelineOf()

	// ---- image permission preflight (adapter 4) ----
	imagePreflight := &chainImagePreflight{}

	// ---- pre-auth service ----
	preauthService, err := gatewaypreauth.New(gatewaypreauth.Service{
		RuntimeCache:       deps.Cache,
		Observability:      observability,
		Clock:              clock,
		Circuits:           deps.Circuits,
		IPPolicy:           deps.IPPolicy,
		UserLimits:         deps.UserLimits,
		ModelsRateLimit:    deps.ModelsRateLimit,
		APIKeyQuota:        deps.APIKeyQuota,
		AuthorizationQuota: deps.AuthzQuota,
		InflightQuota:      deps.InflightQuota,
		Affinity:           deps.Affinity,
		AccountAvoidance:   deps.Avoidance,
		APIKeyValidator:    &chainAPIKeyValidator{cache: deps.Cache},
		RouteResolver:      routeResolver,
		Candidates:         pipeline,
		Images:             imagePreflight,
		Responses:          sink,
		ClientStrategy:     clientStrategyAdapter{deps: &gatewaycodex.ClientStrategyDeps{CompactionExpected: gatewaycodex.CodexCompactionExpectedForRequest}},
		SessionIdentity:    sessionIdentityAdapter{services: deps.Identity},
		SessionAffinity:    sessionAffinityAdapter{services: deps.Identity},
		Codex:              chainCodexBridgePreflight(deps.CodexBridge),
		Recoverable:        deps.Recoverable,
		AuditSettings:      auditSettingsAdapter{enabled: deps.AuditLogEnabled},
		AuditDispatch:      auditDispatchAdapter{target: deps.AuditInputURL, logger: logger},
	})
	if err != nil {
		recorder.Close()
		return nil, nil, err
	}
	imagePreflight.preauth = preauthService

	chain := &gatewayChain{
		preauth:            preauthService,
		engine:             engine,
		observability:      observability,
		clock:              clock,
		bodyPipeline:       bodyPipeline,
		finalizationUsage:  recorder,
		auditSettings:      auditSettingsSourceAdapter{enabled: deps.AuditLogEnabled},
		auditDispatcher:    auditUsageDispatcher{target: deps.AuditInputURL, logger: logger},
		usageModelResolver: usageModelResolverAdapter{},
	}

	shutdown := func() {
		recorder.Close()
		if spool != nil {
			spool.StopReplay()
		}
	}
	return chain, shutdown, nil
}

func joinChinese(values []string) string {
	out := ""
	for index, value := range values {
		if index > 0 {
			out += "；"
		}
		out += value
	}
	return out
}

func newUsageSpool(directory string, clock gatewaypreauth.Clock, logger *slog.Logger) *gatewayusage.UsageRecordSpool {
	if directory == "" {
		return nil
	}
	// Enabled: the chain process is the usage-record producer after the flip;
	// the file spool is its durable compensation sink regardless of the
	// runtime mode (the Node standalone path enqueues into the jobs-module
	// usagewriter, which this process cannot import — see chain_usage.go).
	// Capacity defaults mirror runtimeConfig.usageSpool (JUHE_AI_USAGE_SPOOL_*).
	return gatewayusage.NewUsageRecordSpool(gatewayusage.SpoolConfig{
		Directory:        directory,
		InstanceID:       "gateway-chain",
		MaxItems:         250_000,
		MaxBytes:         4_096 * 1024 * 1024,
		ReplayBatchSize:  500,
		ReplayIntervalMs: 1_000,
		Enabled:          true,
	}, clock, slogLogger{inner: logger})
}

// spoolOverflow implements gatewayusage.DispatchOverflowSpool.
type spoolOverflow struct {
	spool *gatewayusage.UsageRecordSpool
}

func (o spoolOverflow) PersistOverflow(ctx gatewayusage.Ctx, input gatewayusage.UsageRecordInput) error {
	if o.spool == nil {
		return nil
	}
	return o.spool.Persist(ctx, input)
}

func hybridClockOf(clock gatewaypreauth.Clock) gatewayhybrid.Clock { return clock.Now }

func hybridSharedCacheOf(cache hybridSharedJSONCache) gatewayhybrid.SharedJSONCache {
	if cache == nil {
		return nil
	}
	return cache
}

func hybridRuntimeStateOf(state hybridRuntimeStateStore) gatewayhybrid.RuntimeStateStore {
	if state == nil {
		return nil
	}
	return state
}

func hybridAuxiliaryOf(dispatcher hybridAuxiliaryDispatcher) gatewayhybrid.AuxiliaryDispatcher {
	if dispatcher == nil {
		return nil
	}
	return dispatcher
}

func hybridUsageRecorderOf(recorder hybridUsageRecorder) gatewayhybrid.UsageRecorder {
	if recorder == nil {
		return nil
	}
	return recorder
}

func hybridDiagnosticsOf(publisher hybridRouteDiagnostics) gatewayhybrid.RouteDiagnosticsPublisher {
	if publisher == nil {
		return nil
	}
	return publisher
}
