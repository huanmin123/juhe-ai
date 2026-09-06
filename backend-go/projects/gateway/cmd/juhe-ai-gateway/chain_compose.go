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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
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
	// QueueDefaults carries the concurrency.globalMax derived DEFAULT
	// high-concurrency scheduling bounds (Node runtimeConfig.concurrency.
	// globalMax, default 5000) for the speed-first body admission gate.
	QueueDefaults gatewayclientip.HighConcurrencyPolicyDefaults

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

	// 显式账户错误策略（chain_error_policy*.go）：决策服务 + 状态写侧窄口
	// （optional；nil 时派发器保留决策事实，状态变更加显式降级日志）。生产
	// 装配在 compose.go 的链条运行服务段（newChainErrorPolicyEffectsBridge）。
	AccountErrorPolicy        *chainErrorPolicyService
	AccountErrorPolicyEffects chainAccountErrorPolicyEffects

	// 失败派发链装配（chain_request_failure_health.go / chain_turn_probe_store.go /
	// chain_turn_retry_redis.go）：jobs internal-api loopback 目标 + Redis 驱动的
	// turn-retry 状态存储（nil → memory 驱动，Node runtimeStateDriver !== 'redis'
	// 分叉）。
	JobsInternalURL     string
	TurnRetryStateStore gatewaycodex.TurnRetryStateStore
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
		WithLogger(slogLogger{inner: logger}).
		// Synchronous catalog pricing (chain_pricing.go): the cacheDriver!=='redis'
		// gate is ServiceConfig.SyncPricingAllowed above; the adapter resolves
		// the catalog row through the same runtime cache and bills through the
		// shared internal/pricing engine (Node model-catalog.service.ts).
		WithPricingCatalog(newChainUsagePricingCatalog(deps.Cache))

	// ---- response sink (G16) ----
	// The models fast-path reads the client model catalog through the same
	// runtime cache the preflight uses (Node listClientModelCatalogAsync);
	// an unwired ModelCatalog port renders /v1/models as an empty list.
	sink := gatewayresponse.NewSink(gatewayresponse.SinkDeps{
		UsageRecords:  usageDispatchAdapter{service: usageService, recorder: recorder},
		UsageDispatch: usageDispatchAdapter{service: usageService, recorder: recorder},
		ModelCatalog:  chainClientModelCatalog{cache: deps.Cache},
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
	// The failure dispatcher shares the engine's session-affinity port: the
	// Node dispatcher forgets the account's session affinity on its failure
	// branches (failure-dispatch.ts:208/346/570).
	sessionAffinity := newLocalSessionAffinity()
	// G18 client-source avoidance collaborators: the source-identity resolver
	// plugs into the shared client-strategy deps (preauth resolution and the
	// failure-time re-resolution use the same scope), and the turn-retry
	// service owns the avoidance state (memory driver; the Redis state-store
	// adapter is a registered residual). Without the G14 identity services
	// there is no HMAC secret, so no source scope can be derived and the
	// avoidance stays off — exactly the Node missing-source-key semantics.
	codexClientStrategy := &gatewaycodex.ClientStrategyDeps{CompactionExpected: gatewaycodex.CodexCompactionExpectedForRequest}
	// 失败派发链（failure-dispatch.ts:404/571 request-failure health-check
	// 派发 + turn-availability-probe 激活探活）：桥以 jobs internal-api
	// loopback HMAC 为目标；baseURL 或 secret 缺失时派发按 input_unavailable
	// 拒绝（Node input_unavailable 分叉），探活装配随 secret 分叉。
	var chainHealthDispatch *chainRequestFailureHealthDispatcher
	secret := ""
	if deps.Identity != nil {
		secret = deps.Identity.Secret
	}
	if strings.TrimSpace(deps.JobsInternalURL) != "" && strings.TrimSpace(secret) != "" {
		chainHealthDispatch = newChainRequestFailureHealthDispatcher(deps.JobsInternalURL, secret, nil)
	} else {
		slogOnceWarn("gateway.response.requestFailureHealthCheckDispatch", "健康检查派发桥缺少 jobs internal-api 目标或签名密钥，按 input_unavailable 拒绝")
	}
	var chainTurnRetry *gatewaycodex.TurnRetryService
	var chainTurnAvoidanceProbe *gatewaycodex.TurnAvoidanceProbeService
	if deps.Identity != nil && strings.TrimSpace(deps.Identity.Secret) != "" {
		codexClientStrategy.Source = &gatewaycodex.SourceIdentityResolver{
			Secret:  deps.Identity.Secret,
			Session: codexSourceSessionAdapter{identity: deps.Identity.Identity},
		}
		chainTurnRetry = &gatewaycodex.TurnRetryService{
			Secret: deps.Identity.Secret,
			Clock:  clock,
			Logger: slogWarnLogger{inner: logger},
			// 装配 3：Redis 驱动（runtimeStateDriver==='redis'）；nil 保持
			// memory 驱动（键空间 juhe-ai:<ns>:state:gateway-codex-turn-retry:）。
			Store: deps.TurnRetryStateStore,
		}
		// 装配 2：gatewaycircuit.ProbeCoordinator 桥接（memory probe-state
		// store，Node memory driver 语义；gatewaycircuit 的 Redis store 待其
		// 自身工作包落地后切换）。healthDispatch 为 nil 时探活派发按
		// input_unavailable 拒绝并结算 fence（turnprobe 契约）。
		chainTurnAvoidanceProbe = newChainTurnAvoidanceProbeService(chainTurnRetry, clock, chainHealthDispatch)
	}
	engine := gatewaydispatch.NewEngine(newChainProviderDriver(), &chainFailureDispatcher{
		usage:          usageService,
		affinity:       sessionAffinity,
		clientStrategy: codexClientStrategy,
		turnRetry:      chainTurnRetry,
		avoidanceProbe: chainTurnAvoidanceProbe,
		healthDispatch: chainHealthDispatch,
		policy:         deps.AccountErrorPolicy,
		effects:        deps.AccountErrorPolicyEffects,
	})
	engine.Clock = clock
	engine.Affinity = sessionAffinity
	engine.Latency = &degradedLatency{}
	engine.ProxyHealth = &degradedProxyHealth{}
	engine.HotQuality = &degradedHotQuality{}
	if chainTurnRetry != nil {
		engine.ClientSourceAvoidance = &chainClientSourceAvoidance{turnRetry: chainTurnRetry}
	} else {
		engine.ClientSourceAvoidance = &degradedClientSourceAvoidance{}
	}
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
		ClientStrategy:     clientStrategyAdapter{deps: codexClientStrategy},
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
		preauth:       preauthService,
		engine:        engine,
		observability: observability,
		clock:         clock,
		bodyPipeline:  bodyPipeline,
		speedFirstAdmission: &chainSpeedFirstBodyAdmissionGate{
			preauth:       preauthService,
			QueueDefaults: deps.QueueDefaults,
		},
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

// ---------------------------------------------------------------------------
// hybrid auxiliary dispatcher (T2 终局遗留①装配; Node
// modules/gateway/hybrid/auxiliary-dispatch.service.ts dispatchHybridAuxiliaryChatCompletion)
// ---------------------------------------------------------------------------

// chainHybridAuxiliaryDispatcher implements gatewayhybrid.AuxiliaryDispatcher
// by replaying the Node auxiliary loop over the same in-process pieces the /v1
// orchestrator uses: the routing runtime cache selects the target group and
// provides the hydrated account secrets (prepareOpenAIGatewayDispatchAccounts
// equivalent for the single-attempt auxiliary lane), the shared provider
// driver builds the upstream URL/headers/body (buildGatewayUpstream*), and the
// engine transport executes the one attempt (fetchFirstAvailableUpstream).
//
// Assembled-minimal residuals against the full Node loop (documented handover,
// each degrades to the Node failure path, never to a wrong success):
//   - audit capture / hot-quality attempt records / client-ip avoidance
//     tracker: the auxiliary call is invisible to those channels;
//   - circuit confirm/lease hooks (confirmSameAccountApiKeyFailures,
//     confirmHalfOpenSuccess) run inside Finish in Node; the Go Finish is a
//     call-once no-op because the adapter holds no circuit lease;
//   - server retry budget rides on the caller context deadline only.
type chainHybridAuxiliaryDispatcher struct {
	cache  *gatewayruntimecache.Service
	driver *chainProviderDriver
}

func newChainHybridAuxiliaryDispatcher(cache *gatewayruntimecache.Service) *chainHybridAuxiliaryDispatcher {
	return &chainHybridAuxiliaryDispatcher{
		cache:  cache,
		driver: newChainProviderDriver(),
	}
}

// auxiliaryDispatchFailure mirrors the failed arm constructor.
func auxiliaryDispatchFailure(input gatewayhybrid.AuxiliaryDispatchInput, errorCode, errorMessage string, account *gatewayhybrid.OpenAIAccountSecret, groupID string, hasGroupID bool, statusCode int, hasStatusCode bool, shouldRecordUsage bool) (gatewayhybrid.AuxiliaryDispatchSuccess, *gatewayhybrid.AuxiliaryDispatchFailure) {
	return gatewayhybrid.AuxiliaryDispatchSuccess{}, &gatewayhybrid.AuxiliaryDispatchFailure{
		ErrorCode:         errorCode,
		ErrorMessage:      errorMessage,
		Account:           account,
		GroupID:           groupID,
		HasGroupID:        hasGroupID,
		StatusCode:        statusCode,
		HasStatusCode:     hasStatusCode,
		ShouldRecordUsage: shouldRecordUsage,
	}
}

// DispatchHybridAuxiliaryChatCompletion mirrors dispatchHybridAuxiliaryChatCompletion:
// select the auxiliary target group, dispatch the synthesized body once, and
// settle through the returned Finish callback (call-once, side-effect free in
// the assembled-minimal wiring).
func (d *chainHybridAuxiliaryDispatcher) DispatchHybridAuxiliaryChatCompletion(ctx context.Context, input gatewayhybrid.AuxiliaryDispatchInput) (gatewayhybrid.AuxiliaryDispatchSuccess, *gatewayhybrid.AuxiliaryDispatchFailure) {
	if d == nil || d.cache == nil {
		return auxiliaryDispatchFailure(input, input.DispatchErrorCode, input.DispatchErrorMessage, nil, "", false, 0, false, false)
	}
	// 1. selectGatewayModelTargetGroup over the routing runtime cache.
	selection, err := (hybridTargetGroups{cache: d.cache}).SelectTargetGroup(ctx, gatewayhybrid.TargetGroupSelectorInput{
		APIKeyRecord:               input.APIKeyRecord,
		TargetModel:                input.TargetModel,
		RequestClientCompatibility: input.RequestClientCompatibility,
	})
	if err != nil {
		return auxiliaryDispatchFailure(input, input.DispatchErrorCode, input.DispatchErrorMessage, nil, "", false, 0, false, false)
	}
	if selection == nil || len(selection.Accounts) == 0 {
		return auxiliaryDispatchFailure(input, input.NoAccountErrorCode, input.NoAccountErrorMessage, nil, "", false, 0, false, false)
	}

	// 2. Hydrated candidate accounts (Node prepareOpenAIGatewayDispatchAccounts):
	// the runtime cache snapshots carry the decrypted upstream credentials.
	candidates, err := d.cache.ListCachedOpenAIAccountsForGroupAsync(ctx, selection.GroupID, input.APIKeyRecord.SystemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
		RequestedModel:          input.TargetModel,
		RequestedEndpointFamily: requestEndpointFamilyOf("/v1/chat/completions"),
	})
	if err != nil {
		return auxiliaryDispatchFailure(input, input.DispatchErrorCode, input.DispatchErrorMessage, nil, selection.GroupID, true, 0, false, false)
	}
	byID := make(map[string]gatewayruntimecache.OpenAIAccountSecret, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	// Keep the selection order (Node preparation preserves the binding order).
	ordered := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(selection.Accounts))
	for _, secret := range selection.Accounts {
		if candidate, ok := byID[secret.ID]; ok {
			ordered = append(ordered, candidate)
		}
	}
	if len(ordered) == 0 {
		return auxiliaryDispatchFailure(input, input.NoAccountErrorCode, input.NoAccountErrorMessage, nil, selection.GroupID, true, 0, false, false)
	}

	// 3. One upstream attempt over the first available account
	// (fetchFirstAvailableUpstream, single-shot; per-account compatibility
	// skipping mirrors the attempt loop's capability filter).
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(input.TimeoutMs)*time.Millisecond)
	defer cancel()
	var lastAccount *gatewayhybrid.OpenAIAccountSecret
	for _, account := range ordered {
		if account.BaseURL == "" {
			continue
		}
		lastAccount = &gatewayhybrid.OpenAIAccountSecret{ID: account.ID}
		httpReq, reqErr := http.NewRequestWithContext(timeoutCtx, http.MethodPost, "http://hybrid-auxiliary.internal/v1/chat/completions", bytes.NewReader(input.RawBody))
		if reqErr != nil {
			break
		}
		httpReq.Header.Set("Content-Type", "application/json")
		gatewayReq := gatewaypreauth.NewGatewayRequest(httpReq)
		gatewayReq.Body = &gatewaybody.Request{RawBody: input.RawBody, ContentTypeHeader: "application/json"}
		urls, urlErr := d.driver.BuildGatewayUpstreamURLsForAccount(ctx, account, gatewayReq)
		if urlErr != nil || len(urls) == 0 {
			continue
		}
		parts, partsErr := d.driver.BuildGatewayUpstreamRequestParts(ctx, gatewayReq, account, gatewaydispatch.UsageIdentity{}, input.RequestClientCompatibility)
		if partsErr != nil {
			continue
		}
		body := parts.Body
		// The synthesized body carries the scoring/quality model as the
		// target model; an account-level model mapping switches it upstream
		// exactly like the dispatch pipeline (the generic parsed-body path
		// cannot run on the synthetic request, so the mapping replays through
		// the shared openai resolver directly).
		if mapping := resolveAuxiliaryAccountModelMapping(account, input.TargetModel); mapping != nil {
			transformed, transformErr := d.driver.openai.BuildUpstreamRequest(gatewayproto.BuildUpstreamRequestInput{
				Method:              http.MethodPost,
				ClientPathAndQuery:  "/v1/chat/completions",
				Body:                body,
				Header:              parts.Headers,
				ParsedBody:          gatewayhybrid.ToNativeValue(input.Body),
				ParsedBodyAvailable: input.Body != nil,
				ModelMapping:        mapping,
			})
			if transformErr != nil {
				continue
			}
			body = transformed.Body
		}
		timeoutMs := int64(input.TimeoutMs)
		response, requestErr := gatewaydispatch.RequestUpstream(timeoutCtx, urls[0], gatewaydispatch.UpstreamRequestOptions{
			Method:    http.MethodPost,
			Header:    parts.Headers,
			Body:      body,
			ProxyURL:  deref(account.ProxyURL),
			TimeoutMs: &timeoutMs,
			Signal:    timeoutCtx,
		}, gatewaydispatch.TransportDeps{})
		if requestErr != nil {
			message := requestErr.Error()
			return auxiliaryDispatchFailure(input, input.DispatchErrorCode, firstNonEmptyString(message, input.DispatchErrorMessage), lastAccount, selection.GroupID, true, 0, false, true)
		}
		// 4. Bounded body read (readUpstreamBodyLimited) + parse + usage.
		bodyBytes, readErr := io.ReadAll(io.LimitReader(response.Body, int64(input.ResponseMaxBytes)+1))
		_ = response.Body.Close()
		if readErr != nil {
			return auxiliaryDispatchFailure(input, input.DispatchErrorCode, input.DispatchErrorMessage, lastAccount, selection.GroupID, true, response.Status(), true, true)
		}
		truncated := len(bodyBytes) > input.ResponseMaxBytes
		if truncated {
			bodyBytes = bodyBytes[:input.ResponseMaxBytes]
			return auxiliaryDispatchFailure(input, input.DispatchErrorCode, input.ResponseTooLargeMessage, lastAccount, selection.GroupID, true, response.Status(), true, true)
		}
		bodyText := string(bodyBytes)
		if !response.OK() {
			errorCode, errorMessage := gatewayhybrid.AuxiliaryUpstreamFailure(gatewayhybrid.AuxiliaryUpstreamFailureInput{
				Account:           *lastAccount,
				BodyText:          bodyText,
				ContentType:       response.ContentType(),
				StatusCode:        response.Status(),
				FallbackErrorCode: input.HTTPErrorCode,
			})
			return auxiliaryDispatchFailure(input, errorCode, errorMessage, lastAccount, selection.GroupID, true, response.Status(), true, true)
		}
		parsedResponseBody, usage := gatewayhybrid.ParseHybridAuxiliaryResponse(bodyText, response.ContentType())
		return gatewayhybrid.AuxiliaryDispatchSuccess{
			Account:               *lastAccount,
			GroupID:               selection.GroupID,
			StatusCode:            response.Status(),
			ResponseBody:          bodyBytes,
			ResponseBodyText:      bodyText,
			ResponseBodyTruncated: false,
			ParsedResponseBody:    parsedResponseBody,
			Usage:                 usage,
			Finish: func(context.Context, gatewayhybrid.AuxiliaryDispatchFinishInput) error {
				// createFinish call-once guard (the scoring service wraps it
				// in AuxiliaryFinishOnce); the audit / hot-quality /
				// circuit-lease side effects stay unported (residuals above).
				return nil
			},
		}, nil
	}
	if lastAccount != nil {
		return auxiliaryDispatchFailure(input, input.DispatchErrorCode, input.DispatchErrorMessage, lastAccount, selection.GroupID, true, 0, false, true)
	}
	return auxiliaryDispatchFailure(input, input.NoAccountErrorCode, input.NoAccountErrorMessage, nil, selection.GroupID, true, 0, false, false)
}

// resolveAuxiliaryAccountModelMapping resolves the account mapping for the
// auxiliary target model through the shared openai resolver (the same source
// of truth the provider driver uses).
func resolveAuxiliaryAccountModelMapping(account gatewayruntimecache.OpenAIAccountSecret, targetModel string) *gatewayproto.ResolvedModelMapping {
	if targetModel == "" {
		return nil
	}
	runtime := &gatewayopenai.RuntimeAccount{
		ModelMappings:             openAIModelMappingsOf(account.ModelMappings),
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
	}
	return gatewayopenai.ResolveAccountModelMapping(runtime, targetModel, gatewayopenai.FamilyChatCompletions)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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

// ---------------------------------------------------------------------------
// client model catalog (models fast-path; Node client-model-catalog.service.ts)
// ---------------------------------------------------------------------------

// chainClientModelCatalog implements gatewayresponse.ModelCatalogLoader over
// the runtime cache catalog read: listClientModelCatalogAsync +
// selectClientModelCatalog. The composition previously left the port
// unwired, rendering every /v1/models response as an empty list.
type chainClientModelCatalog struct {
	cache *gatewayruntimecache.Service
}

func (c chainClientModelCatalog) ListClientModelCatalog(systemAccountID string, providerCodes []string) []gatewayresponse.ModelCatalogEntry {
	if c.cache == nil || len(providerCodes) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	codes := sortedUniqueProviderCodes(providerCodes)
	var items []gatewayruntimecache.ProviderModelCatalogItem
	for _, code := range codes {
		catalog, err := c.cache.ListCachedProviderModelCatalogAsync(ctx, gatewayruntimecache.ModelCatalogListOptions{
			ProviderCode:    code,
			SystemAccountID: systemAccountID,
		})
		if err != nil {
			return nil
		}
		items = append(items, catalog...)
	}
	selected := selectClientCatalogItems(items)
	entries := make([]gatewayresponse.ModelCatalogEntry, 0, len(selected))
	for _, item := range selected {
		entries = append(entries, clientCatalogEntryOf(item))
	}
	return entries
}

// sortedUniqueProviderCodes mirrors resolveClientModelCatalogProviderCodes'
// normalization output for an explicit provider-code list.
func sortedUniqueProviderCodes(providerCodes []string) []string {
	seen := map[string]bool{}
	codes := []string{}
	for _, code := range providerCodes {
		normalized := chainNormalizeProviderToken(code)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		codes = append(codes, normalized)
	}
	sort.Strings(codes)
	return codes
}

// selectClientCatalogItems mirrors selectClientModelCatalog: active, visible
// and priced candidates, best-scope-first dedupe by model, client ordering.
func selectClientCatalogItems(items []gatewayruntimecache.ProviderModelCatalogItem) []gatewayruntimecache.ProviderModelCatalogItem {
	candidates := make([]gatewayruntimecache.ProviderModelCatalogItem, 0, len(items))
	for _, item := range items {
		if item.Status != "active" {
			continue
		}
		if item.Scope == "built_in" && item.CatalogVisible != nil && !*item.CatalogVisible {
			continue
		}
		if !clientCatalogHasVisiblePrice(item) {
			continue
		}
		candidates = append(candidates, item)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		scopeOrder := clientCatalogScopeRank(candidates[j]) - clientCatalogScopeRank(candidates[i])
		if scopeOrder != 0 {
			return scopeOrder < 0
		}
		return clientCatalogCompareItems(candidates[i], candidates[j])
	})
	byModel := map[string]bool{}
	selected := make([]gatewayruntimecache.ProviderModelCatalogItem, 0, len(candidates))
	for _, item := range candidates {
		model := strings.TrimSpace(item.Model)
		if model == "" || byModel[model] {
			continue
		}
		byModel[model] = true
		selected = append(selected, item)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		return clientCatalogCompareItems(selected[i], selected[j])
	})
	return selected
}

func clientCatalogScopeRank(item gatewayruntimecache.ProviderModelCatalogItem) int {
	switch item.Scope {
	case "personal":
		return 3
	case "global":
		return 2
	default:
		return 1
	}
}

func clientCatalogCompareItems(left, right gatewayruntimecache.ProviderModelCatalogItem) bool {
	if dateOrder := strings.Compare(clientCatalogReleaseDate(right), clientCatalogReleaseDate(left)); dateOrder != 0 {
		return dateOrder < 0
	}
	if providerOrder := strings.Compare(chainNormalizeProviderToken(left.ProviderCode), chainNormalizeProviderToken(right.ProviderCode)); providerOrder != 0 {
		return providerOrder < 0
	}
	return left.Model < right.Model
}

func clientCatalogReleaseDate(item gatewayruntimecache.ProviderModelCatalogItem) string {
	if item.ReleaseDate == nil {
		return ""
	}
	return strings.TrimSpace(*item.ReleaseDate)
}

func clientCatalogHasVisiblePrice(item gatewayruntimecache.ProviderModelCatalogItem) bool {
	return item.InputUsdPer1M != nil || item.OutputUsdPer1M != nil ||
		item.CachedInputUsdPer1M != nil || item.CacheWriteUsdPer1M != nil ||
		item.CacheWrite1hUsdPer1M != nil || item.CacheStorageUsdPer1MPerHour != nil ||
		item.ImageInputUsdPer1M != nil || item.ImageOutputUsdPer1M != nil ||
		item.AudioInputUsdPer1M != nil || item.AudioOutputUsdPer1M != nil ||
		item.OutputUsdPerImage != nil || len(item.ServiceTierPrices) > 0
}

func clientCatalogEntryOf(item gatewayruntimecache.ProviderModelCatalogItem) gatewayresponse.ModelCatalogEntry {
	return gatewayresponse.ModelCatalogEntry{
		Model:                         item.Model,
		Scope:                         item.Scope,
		ReleaseDate:                   nilString(item.ReleaseDate),
		CreatedAt:                     nilString(item.CreatedAt),
		CapabilityNotes:               nilString(item.CapabilityNotes),
		PricingNotes:                  nilString(item.PricingNotes),
		Notes:                         nilString(item.Notes),
		ContextWindowTokens:           nilInt(item.ContextWindowTokens),
		SupportedServiceTiers:         item.SupportedServiceTiers,
		CodexSupportedReasoningLevels: rawMessageStringList(item.CodexSupportedReasoningLevels),
		CodexDefaultReasoningLevel:    rawMessageString(item.CodexDefaultReasoningLevel),
		CodexMultiAgentVersion:        nilString(item.CodexMultiAgentVersion),
	}
}

func nilString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nilInt(value *int64) int {
	if value == nil {
		return 0
	}
	return int(*value)
}

func rawMessageStringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	return values
}

func rawMessageString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}
