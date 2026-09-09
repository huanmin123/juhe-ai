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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
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
	// AuditDispatch carries the in-process F3 audit producer adapter
	// (去跨进程战役第四刀: the loopback audit input URL/POST is gone; the
	// producer persists finalized dropped captures directly). Nil adapters
	// keep the retired empty-target degrade branch (silent drop).
	AuditDispatch gatewaypreauth.AuditDispatcher
	// AuditUsageDispatch carries the same producer behind the usage-face
	// audit port (gatewayusage.AuditDispatcher). Nil degrades identically.
	AuditUsageDispatch gatewayusage.AuditDispatcher
	// SpoolDirectory enables the durable usage-record spool.
	SpoolDirectory string
	// Usage spool capacity knobs（D-209，Node runtimeConfig.usageSpool：
	// JUHE_AI_USAGE_SPOOL_MAX_ITEMS / MAX_MB / REPLAY_BATCH_SIZE /
	// REPLAY_INTERVAL_MS）。<=0 时回落 Node 同值默认。
	UsageSpoolMaxItems         int
	UsageSpoolMaxBytes         int
	UsageSpoolReplayBatchSize  int
	UsageSpoolReplayIntervalMs int
	// UsageFinalizationMaxItems 是用量收尾队列容量（D-147，Node
	// runtimeConfig.gateway.usageFinalizationMaxItems，
	// JUHE_AI_GATEWAY_USAGE_FINALIZATION_MAX_ITEMS 默认 2048）。<=0 回落
	// 同值默认。
	UsageFinalizationMaxItems int
	// ConcurrencyGlobalMax mirrors Node runtimeConfig.concurrency.globalMax
	//（JUHE_AI_CONCURRENCY_GLOBAL_MAX 默认 5000）：用量收尾队列并发上限与
	// dispatch 全局并发槽容量同源。
	ConcurrencyGlobalMax int
	// UpstreamURLSecurity 是请求期上游 URL 安全配置（D-192/D-146，Node
	// runtimeConfig.upstreamUrlSecurity）。
	UpstreamURLSecurity UpstreamURLSecurityConfig
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
	// below keep the attempt loop defined). AccountLocks 生产装配为
	// chain_account_locks.go 的 SQL 运行面（BUG-0174 B-2）；nil 仅保留给
	// 显式关闭开关（compose.go JUHE_AI_ACCOUNT_LOCKS_DISABLED），落到
	// disabledAccountLocks 的「视为未锁」降级。
	Suppression  gatewaydispatch.SuppressionPort
	Degradation  gatewaydispatch.DegradationPort
	AccountLocks gatewaydispatch.AccountLocks

	// EngineSecret 是注入 dispatch 引擎的 JUHE_AI_SECRET
	//（gatewaydispatch.EngineConfig.Secret，B-1 BUG-0174）：必须与水合层
	// chainAccountsSelector.secret（chain_runtime.go
	// newChainAccountsSelectorWithStats 的 cfg.Secret）同源，dispatch 产出的
	// SelectedAPIKeyFingerprint 指纹才能与 account_api_key_runtime_states 及
	// 探活池命中。
	EngineSecret string

	// KeyRotation 是账户 API Key 轮转计数器（gatewaydispatch.Engine.
	// KeyRotation，B-3 BUG-0174）：生产装配为
	// chain_apikey_rotation_redis.go 的 Redis 计数器（StateClient 同源）；
	// nil 时引擎回落包级进程内计数器。
	KeyRotation gatewaydispatch.APIKeyRotationCounter

	// 显式账户错误策略（chain_error_policy*.go）：决策服务 + 状态写侧窄口
	// （optional；nil 时派发器保留决策事实，状态变更加显式降级日志）。生产
	// 装配在 compose.go 的链条运行服务段（newChainErrorPolicyEffectsBridge）。
	AccountErrorPolicy        *chainErrorPolicyService
	AccountErrorPolicyEffects chainAccountErrorPolicyEffects

	// 失败观察代际捕获（failure-dispatch.ts:421-434
	// captureGatewayAccountApiKeyFailureObservation；optional：nil 时挂起
	// Key 失败不带代际）。生产装配为 chainRuntimeServices.AccountAPIKeyGuard。
	AccountAPIKeyObservation chainAPIKeyObservationPort

	// codex 用量响应头失败面派发（failure-dispatch.ts:340-344；optional：
	// gateway→jobs record-maintenance 快照通道已接——compose.go 经
	// newCodexUsageHeadersChannelDispatcher 把快照 job 落 record_maintenance_jobs
	// v2 快照行；gatewaycodex 对 nil 派发器仍静默跳过，仅测试/降级装配传 nil）。
	CodexUsageHeadersDispatcher gatewaycodex.CodexUsageHeadersDispatcher

	// 失败派发链装配（chain_request_failure_health.go / chain_turn_probe_store.go /
	// chain_turn_retry_redis.go）：健康检查派发的进程内 outbox writer（常驻，
	// 无 HTTP 目标）+ Redis 驱动的 turn-retry 状态存储（nil → memory 驱动，
	// Node runtimeStateDriver !== 'redis' 分叉）。
	HealthProbeOutbox   *chainProbeRequestOutboxWriter
	TurnRetryStateStore gatewaycodex.TurnRetryStateStore

	// ---- W2-C production wiring (BUG-0175)；nil 仅保留给显式关闭开关 /
	// 组合测试，链条回落 chain_ports.go 的 disabled*/degraded* 直通。 ----
	// AccountCircuits 是 D-131 账户电路服务（SUSPECT/confirmation/恢复）。
	AccountCircuits *gatewaycircuit.CircuitService
	// ClientIPSlots 是 D-109 high_concurrency 分组的 client-IP 并发槽。
	ClientIPSlots gatewaydispatch.ClientIPConcurrencyAcquirer
	// KeyModelStore 是 D-133 的 key-model 前台准入状态存储（driver 选择器）。
	KeyModelStore gatewayaccounteffects.KeyModelRuntimeStore
	// ProxyHealth 是 D-136 的上游桶健康端口。
	ProxyHealth gatewaydispatch.ProxyHealthPort
	// HotQuality 是 D-137 的热质量排序端口。
	HotQuality gatewaydispatch.HotQualityPort
	// HotQualityFactory 是 D-137 的 attempt 记账生命周期工厂。
	HotQualityFactory gatewaydispatch.HotQualityAttemptLifecycleFactory
	// WakeRecoverableWaiter 绑定半开租约释放 → 恢复等待者唤醒（D-134）。
	WakeRecoverableWaiter func(runtimeKey string)

	// ---- W4-B production wiring (BUG-0175: D-111/D-114/D-132)。nil 仅出现
	// 在组合测试，链条回落既有的显式降级实现。 ----
	// AccountAPIKeyGuard 是 D-111 瞬态加载的守卫（chainRuntimeCachePort）。
	AccountAPIKeyGuard *gatewayaccounteffects.AccountAPIKeyFailureGuard
	// APIKeyEffects 是 D-111 的账户 API Key 效果链 dispatch 端口。
	APIKeyEffects gatewaydispatch.APIKeyEffectsPort
	// ConfiguredPolicyAvoidance 是 D-132 的配置策略避让服务（写侧 + 候选
	// 过滤装饰器 + 响应层副作用）。
	ConfiguredPolicyAvoidance *gatewayaccounteffects.ConfiguredPolicyAvoidanceService
	// ProxyHealthService 是 D-132 响应层 avoid_upstream_bucket_ttl 的桶避让
	// 写侧（具体服务，端口之上的旁路写面）。
	ProxyHealthService *gatewayproxyhealth.ProxyHealthService
	// LatencyService 是 D-114 的普通路由速度优先时延降级服务。
	LatencyService *gatewayproxyhealth.LatencyDegradationService
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
	spool := newUsageSpool(deps.SpoolDirectory, clock, logger, usageSpoolCapacity{
		MaxItems:         deps.UsageSpoolMaxItems,
		MaxBytes:         deps.UsageSpoolMaxBytes,
		ReplayBatchSize:  deps.UsageSpoolReplayBatchSize,
		ReplayIntervalMs: deps.UsageSpoolReplayIntervalMs,
	})
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 4096, Logger: logger}, spool)
	// D-147（BUG-0175）：收尾队列容量/并发改由组合根 env 旋钮传入——
	// JUHE_AI_GATEWAY_USAGE_FINALIZATION_MAX_ITEMS 与
	// JUHE_AI_CONCURRENCY_GLOBAL_MAX（Node runtimeConfig.gateway.
	// usageFinalizationMaxItems / concurrency.globalMax）；此前硬传 (0,0)
	// 使两个 env 对队列失效，只能吃到包级默认。
	dispatch := gatewayusage.NewFinalizationDispatch(recorder, spoolOverflow{spool: spool}, deps.UsageFinalizationMaxItems, deps.ConcurrencyGlobalMax)
	dispatch.OverflowEnabled = spool != nil
	usageService := gatewayusage.NewService(dispatch, gatewayusage.ServiceConfig{SyncPricingAllowed: true}).
		WithClock(clock).
		WithLogger(slogLogger{inner: logger}).
		// Synchronous catalog pricing (chain_pricing.go): the cacheDriver!=='redis'
		// gate is ServiceConfig.SyncPricingAllowed above; the adapter resolves
		// the catalog row through the same runtime cache and bills through the
		// shared internal/pricing engine (Node model-catalog.service.ts).
		WithPricingCatalog(newChainUsagePricingCatalog(deps.Cache)).
		// D-190（BUG-0175）：上游失败 prometheus 指标族生产装配——
		// recordGatewayUpstreamFailureMetric 从此有进程内注册表可写。
		WithMetrics(gatewayusage.HTTPMetrics{})

	// D-190 / D-191（BUG-0175）：kernel 边界的 HTTP 指标钩子与请求生命周期
	// 事件汇。事件字段经 slog JSON handler 落成顶层键，运行日志检索
	// （logreads runtime_grep）按 event/traceId 解析的契约由此恢复。
	kernel.SetHTTPMetricHooks(&kernel.HTTPMetricHooks{
		Start: func(path string, method string, startedAtMs int64) any {
			return gatewayusage.StartHTTPMetricRequest(path, method, startedAtMs)
		},
		Finish: func(request any, statusCode *int, outcome string, finishedAtMs int64, failureScope string) {
			handle, _ := request.(*gatewayusage.HTTPMetricRequest)
			gatewayusage.FinishHTTPMetricRequest(handle, statusCode, outcome, finishedAtMs, failureScope)
		},
	})
	kernel.SetRequestEventSink(kernel.NewSlogRequestEventSink(logger))

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
	// D-119（BUG-0175）：拒绝记录 Recorder 与文本 lane 的 settings 覆盖装配。
	// Recorder 落 dropped audit + usage failure（Node recordGatewayBodyRejection，
	// 413/503/429 拒绝面此前不写任何审计/用量）；TextRawBodyLimitMegabytes 读
	// 运行时快照（req.gatewayRuntime.settings.gatewayTextRawBodyLimitMegabytes），
	// 读取失败回落 16 MiB 默认。
	rejectionRecorder := &chainBodyRejectionRecorder{
		audit:        deps.AuditDispatch,
		auditEnabled: deps.AuditLogEnabled,
		usage:        usageService,
		clock:        clock,
	}
	bodyPipeline := gatewaybody.NewMiddleware(gatewaybody.Config{
		Logger:                    gatewaybodyLogger{inner: logger},
		Recorder:                  rejectionRecorder,
		TextRawBodyLimitMegabytes: chainTextRawBodyLimitOf(deps.Cache),
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
	// 派发 + turn-availability-probe 激活探活）：派发经进程内 DB outbox
	// （account_health_probe_request_outbox，chain_request_failure_health.go）
	// 常驻写入，没有跨进程 HTTP 可达性门；outbox writer 未装配（或 deadline
	// env 非法）时派发按 input_unavailable 拒绝（Node input_unavailable 分叉）。
	chainHealthDispatch := newChainRequestFailureHealthDispatcher(deps.HealthProbeOutbox)
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
	// D-151（BUG-0175）接线：把 runtime cache 的 provider model catalog 适配进
	// gpt 请求覆盖能力解析（Nil cache 保持能力解析为空，覆盖保持惰性）。
	engine := gatewaydispatch.NewEngine(newChainProviderDriverWithCache(deps.Cache), &chainFailureDispatcher{
		usage:             usageService,
		affinity:          sessionAffinity,
		clientStrategy:    codexClientStrategy,
		turnRetry:         chainTurnRetry,
		avoidanceProbe:    chainTurnAvoidanceProbe,
		healthDispatch:    chainHealthDispatch,
		policy:            deps.AccountErrorPolicy,
		effects:           deps.AccountErrorPolicyEffects,
		apiKeyObservation: deps.AccountAPIKeyObservation,
		codexUsageHeaders: deps.CodexUsageHeadersDispatcher,
	})
	// B-1（BUG-0174）波1遗留接线：dispatch 的 Key 指纹密钥与水合层同源
	//（chain_runtime.go newChainAccountsSelectorWithStats 的 cfg.Secret）。
	engine.Config.Secret = deps.EngineSecret
	// B-4（BUG-0175）接线：跨协议桥响应面（Node transformUpstreamResponse
	// driver 链）——桥响应转换挂在 attempt 尾部，非桥请求保持直通。
	engine.ResponseTransformer = newChainBridgeResponseTransformer()
	// D-192/D-146（BUG-0175）接线：请求期上游 URL 安全（DNS resolve-all +
	// 钉扎）与全局并发槽。此前 engine.Transport 保持零值——Governor=Nop、
	// URLPolicy=Passthrough，UnsafeResolvedUpstreamURLError 有消费端无
	// producer。容量取 JUHE_AI_CONCURRENCY_GLOBAL_MAX（Node
	// concurrency.globalMax）；客户端池按代理维度复用 keep-alive 传输。
	upstreamURLPolicy := gatewaydispatch.NewResolvedUpstreamURLPolicy(deps.UpstreamURLSecurity)
	upstreamClientPool := sharedupstreamhttp.NewClientPool()
	engine.Transport = gatewaydispatch.TransportDeps{
		Governor:   gatewaydispatch.NewBoundedConcurrencyGovernor(deps.ConcurrencyGlobalMax),
		URLPolicy:  upstreamURLPolicy,
		ClientPool: upstreamClientPool,
		DialGuard:  upstreamURLPolicy.Guard(),
	}
	// 辅助派发器与主尝试链共用同一 TransportDeps（零值 deps 仅保留给组合
	// 测试——newChainHybridAuxiliaryDispatcher 不经由此接线时）。
	wireChainHybridAuxiliaryTransport(deps.HybridAuxiliary, engine.Transport)
	// B-3（BUG-0174）波1遗留接线：Redis 轮转计数器（nil 保持进程内回退）。
	engine.KeyRotation = deps.KeyRotation
	engine.Clock = clock
	engine.Affinity = sessionAffinity
	// W4-B（BUG-0175）D-114 接线：普通路由速度优先时延降级排序端口
	//（nil 保持 degradedLatency 显式降级——组合测试专用）。
	if deps.LatencyService != nil {
		engine.Latency = chainLatencyDegradationPort{service: deps.LatencyService}
	} else {
		engine.Latency = &degradedLatency{}
	}
	// D-136（BUG-0175）接线：上游桶健康排序 + 失败记录（nil 保持显式降级）。
	if deps.ProxyHealth != nil {
		engine.ProxyHealth = deps.ProxyHealth
	} else {
		engine.ProxyHealth = &degradedProxyHealth{}
	}
	// D-137（BUG-0175）接线：热质量排序 + attempt 记账生命周期（nil 保持
	// 显式降级 / 中性 no-op 生命周期）。
	if deps.HotQuality != nil {
		engine.HotQuality = deps.HotQuality
	} else {
		engine.HotQuality = &degradedHotQuality{}
	}
	engine.HotQualityAttemptFactory = deps.HotQualityFactory
	// D-131（BUG-0175）接线：账户电路生产链（SUSPECT/confirmation/父升级/
	// 恢复；nil 时引擎保持缺席语义——Node runtime 缺席分叉）。
	engine.Circuits = deps.AccountCircuits
	// D-133（BUG-0175）接线：key-model 前台准入 + 状态存储（nil 时按
	// BypassKeyModelAdmission 语义禁用准入）。
	if deps.KeyModelStore != nil {
		engine.KeyModel = chainKeyModelAdmission{}
		engine.KeyModelStore = deps.KeyModelStore
	}
	// D-109（BUG-0175）接线：high_concurrency 分组的 client-IP 并发槽
	//（nil 时 preparation 的槽获取段不会运行——组合根缺槽即panic 风险，
	// 故 chainRuntimeServices 保证非 nil）。
	if deps.ClientIPSlots != nil {
		engine.ClientIPConcurrency = deps.ClientIPSlots
	}
	// D-134（BUG-0175）接线：半开租约释放 → 恢复等待者唤醒。
	if deps.WakeRecoverableWaiter != nil {
		gatewaydispatch.SetRecoverableUnavailableRuntimeWaiterNotifier(deps.WakeRecoverableWaiter)
	}
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
	// W4-B（BUG-0175）D-111 接线：瞬态加载守卫 + API Key 效果链端口
	//（nil 守卫保持空集降级；nil 端口保持 confirmed-rotation 静默跳过——
	// 仅组合测试）。
	if deps.AccountAPIKeyGuard != nil {
		engine.Cache.(*chainRuntimeCachePort).guard = deps.AccountAPIKeyGuard
	}
	if deps.APIKeyEffects != nil {
		engine.APIKeyEffects = deps.APIKeyEffects
	}
	engine.Usage = usageAttemptRecorderAdapter{service: usageService}
	engine.Suppression = deps.Suppression
	if engine.Suppression == nil {
		engine.Suppression = &disabledSuppression{}
	}
	// W4-B（BUG-0175）D-132 接线：候选过滤装饰器——配置策略避让先于本地
	// 屏蔽过滤（Node filterGatewayAccountRuntimeSuppressions 第一步）。
	if deps.ConfiguredPolicyAvoidance != nil {
		engine.Suppression = &chainConfiguredPolicyAvoidanceSuppression{
			inner:     engine.Suppression,
			avoidance: deps.ConfiguredPolicyAvoidance,
		}
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
		AuditDispatch:      deps.AuditDispatch,
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
			// D-119（BUG-0175）：429 背压拒绝面写 dropped audit + usage
			// failure（Node recordGatewayBodyRejection）；此前 nil 保持静默。
			Recorder: rejectionRecorder,
		},
		finalizationUsage:  recorder,
		auditSettings:      auditSettingsSourceAdapter{enabled: deps.AuditLogEnabled},
		auditDispatcher:    deps.AuditUsageDispatch,
		usageModelResolver: usageModelResolverAdapter{},
	}
	// W4-B（BUG-0175）D-132 接线：响应层账户副作用面（配置策略避让 +
	// 上游桶避让写侧）。nil 服务（组合测试）保持 nil——finalization 对 nil
	// AccountEffects 已有守卫分支。
	if deps.ConfiguredPolicyAvoidance != nil && deps.ProxyHealthService != nil && deps.Cache != nil {
		chain.responseAccountEffects = &chainResponseAccountEffects{
			avoidance:   deps.ConfiguredPolicyAvoidance,
			proxyHealth: deps.ProxyHealthService,
			cache:       deps.Cache,
			affinity:    sessionAffinity,
		}
	}

	shutdown := func() {
		recorder.Close()
		if spool != nil {
			spool.StopReplay()
		}
		// D-192/D-146：链条停机时释放上游 keep-alive 传输的空闲连接。
		upstreamClientPool.CloseIdleConnections()
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

// usageSpoolCapacity 汇集 Node runtimeConfig.usageSpool 的四个容量旋钮
// （D-209）；<=0 的字段回落 Node 同值默认。
type usageSpoolCapacity struct {
	MaxItems         int
	MaxBytes         int
	ReplayBatchSize  int
	ReplayIntervalMs int
}

func newUsageSpool(directory string, clock gatewaypreauth.Clock, logger *slog.Logger, capacity usageSpoolCapacity) *gatewayusage.UsageRecordSpool {
	if directory == "" {
		return nil
	}
	// Enabled: the chain process is the usage-record producer after the flip;
	// the file spool is its durable compensation sink regardless of the
	// runtime mode (the Node standalone path enqueues into the jobs-module
	// usagewriter, which this process cannot import — see chain_usage.go).
	// Capacity defaults mirror runtimeConfig.usageSpool (JUHE_AI_USAGE_SPOOL_*).
	if capacity.MaxItems <= 0 {
		capacity.MaxItems = 250_000
	}
	if capacity.MaxBytes <= 0 {
		capacity.MaxBytes = 4_096 * 1024 * 1024
	}
	if capacity.ReplayBatchSize <= 0 {
		capacity.ReplayBatchSize = 500
	}
	if capacity.ReplayIntervalMs <= 0 {
		capacity.ReplayIntervalMs = 1_000
	}
	return gatewayusage.NewUsageRecordSpool(gatewayusage.SpoolConfig{
		Directory:        directory,
		InstanceID:       "gateway-chain",
		MaxItems:         capacity.MaxItems,
		MaxBytes:         capacity.MaxBytes,
		ReplayBatchSize:  capacity.ReplayBatchSize,
		ReplayIntervalMs: capacity.ReplayIntervalMs,
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
	// transport 是与主尝试链同源的 TransportDeps（D-192/D-146）：并发槽 +
	// URL 安全策略 + 钉扎拨号对辅助派发同样生效。
	transport gatewaydispatch.TransportDeps
}

func newChainHybridAuxiliaryDispatcher(cache *gatewayruntimecache.Service) *chainHybridAuxiliaryDispatcher {
	return &chainHybridAuxiliaryDispatcher{
		cache:  cache,
		driver: newChainProviderDriver(),
	}
}

// wireChainHybridAuxiliaryTransport 把链条引擎的 TransportDeps 注回辅助派发
// 器（D-192/D-146：组合根在 engine.Transport 装配完成后调用；非具体类型或
// nil 引擎保持零值 deps 的测试语义）。
func wireChainHybridAuxiliaryTransport(dispatcher hybridAuxiliaryDispatcher, transport gatewaydispatch.TransportDeps) {
	if concrete, ok := dispatcher.(*chainHybridAuxiliaryDispatcher); ok && concrete != nil {
		concrete.transport = transport
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
		// D-192/D-146：辅助派发与主尝试链共用同一 TransportDeps（并发槽 +
		// URL 安全策略 + 钉扎拨号），此前零值 deps 完全绕过这两层。
		response, requestErr := gatewaydispatch.RequestUpstream(timeoutCtx, urls[0], gatewaydispatch.UpstreamRequestOptions{
			Method:    http.MethodPost,
			Header:    parts.Headers,
			Body:      body,
			ProxyURL:  deref(account.ProxyURL),
			TimeoutMs: &timeoutMs,
			Signal:    timeoutCtx,
		}, d.transport)
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

// ---------------------------------------------------------------------------
// body rejection recording（D-119；Node recordGatewayBodyRejection，
// request/body-middleware.ts:639-711）
// ---------------------------------------------------------------------------

// chainBodyRejectionRecorder 把 body 拒绝面（413 尺寸 / 503 in-flight 与
// worker / 429 speed-first 背压）落成 dropped audit + usage failure。审计按
// 审计设置门控；usage failure 仅在 runtime 快照已解析出 API Key 时写（Node
// !apiKey early return）。记录失败只告警，不改变原始拒绝响应。
type chainBodyRejectionRecorder struct {
	audit        gatewaypreauth.AuditDispatcher
	auditEnabled func() bool
	usage        *gatewayusage.Service
	clock        gatewaypreauth.Clock
}

var _ gatewaybody.RejectionRecorder = (*chainBodyRejectionRecorder)(nil)

func (r *chainBodyRejectionRecorder) RecordGatewayBodyRejection(req *http.Request, _ *gatewaybody.Request, input gatewaybody.RejectionInput) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Warn("网关请求体拒绝记录写入失败，已保留原始拒绝响应",
				"event", "gateway_body_rejection_record_failed",
				"reason", input.Reason,
				"statusCode", input.StatusCode)
		}
	}()
	requestCtx := kernel.Context(req)
	traceID := requestCtx.TraceID
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = "UNKNOWN"
	}
	originalURL := req.URL.RequestURI()
	path, queryString, _ := strings.Cut(originalURL, "?")
	if path == "" {
		path = req.URL.Path
	}
	message := input.ErrorMessage
	if message == "" {
		message = input.ResponsePayload.Error.Message
	}
	// Node: limitBytes + limitScope 存在时审计消息追加尺寸标注。
	auditErrorMessage := message
	if input.LimitBytes > 0 && input.LimitScope != "" {
		auditErrorMessage = fmt.Sprintf("%s（rawBodyBytes=%d, limitBytes=%d, limitScope=%s）",
			message, input.RawBodyBytes, input.LimitBytes, input.LimitScope)
	}
	r.dispatchDroppedAudit(req, requestCtx, traceID, method, path, queryString, auditErrorMessage, input)
	if runtime := chainGatewayRuntimeOf(req); runtime != nil && runtime.APIKey != nil {
		r.recordUsageFailure(requestCtx, req, traceID, message, input, runtime)
	}
}

// dispatchDroppedAudit 对齐 dispatchDroppedAuditCapture：审计设置门控 +
// finalized dropped envelope（reason 'gateway_body_rejected'）。
func (r *chainBodyRejectionRecorder) dispatchDroppedAudit(req *http.Request, requestCtx *kernel.RequestContext, traceID string, method string, path string, queryString string, auditErrorMessage string, input gatewaybody.RejectionInput) {
	if r.audit == nil || r.auditEnabled == nil || !r.auditEnabled() {
		return
	}
	timestamp := r.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	r.audit.Dispatch(gatewaypreauth.DispatchedAuditLogInput{
		ID:              chainNewAuditID(r.clock),
		LifecycleStatus: "finalized",
		TraceID:         traceID,
		TrafficSource:   gatewayTrafficSource,
		AuditOutcome:    gatewaypreauth.AuditOutcomeGatewayFailed,
		Success:         false,
		Method:          method,
		Path:            path,
		QueryString:     queryString,
		ClientIP:        requestCtx.ClientIP,
		UserAgent:       req.Header.Get("User-Agent"),
		FinalStatusCode: input.StatusCode,
		ErrorPhase:      "gateway",
		ErrorCode:       input.ErrorCode,
		ErrorMessage:    auditErrorMessage,
		SampleBucket:    0,
		SampleReason:    "gateway_body_rejected",
		CaptureStatus:   "complete",
		StartedAt:       timestamp,
		EndedAt:         timestamp,
	})
}

// recordUsageFailure 对齐 recordGatewayFailure 的 body 拒绝分支：身份取自
// runtime 快照（apiKey + groupAccess 元数据），请求快照按 bodyOmission 形态
// 记录（正文不落库）。
func (r *chainBodyRejectionRecorder) recordUsageFailure(requestCtx *kernel.RequestContext, req *http.Request, traceID string, message string, input gatewaybody.RejectionInput, runtime *gatewayruntimecache.GatewayRuntime) {
	if r.usage == nil {
		return
	}
	apiKey := runtime.APIKey
	var groupFields gatewaypreauth.GroupUsageMetadataFields
	if runtime.GroupAccess != nil {
		groupFields = gatewaypreauth.GroupUsageMetadata(*runtime.GroupAccess)
	}
	endpoint := strings.ToUpper(req.Method) + " " + pathWithoutQueryOf(req)
	failureContext := gatewayusage.GatewayFailureUsageContext{
		GatewayUsageContext: gatewayusage.GatewayUsageContext{
			TraceID:         traceID,
			TrafficSource:   gatewayusage.OpenAIGatewayTrafficSource(gatewayTrafficSource),
			ClientIP:        requestCtx.ClientIP,
			SystemAccountID: apiKey.SystemAccountID,
			APIKeyID:        apiKey.ID,
			GroupID:         apiKey.SelectedGroupID,
			Endpoint:        endpoint,
			RequestSnapshot: gatewayusage.UsageRequestSnapshot{
				Method:      strings.ToUpper(req.Method),
				Path:        req.URL.Path,
				OriginalURL: req.URL.RequestURI(),
				ClientIP:    requestCtx.ClientIP,
				TraceID:     traceID,
				Headers:     map[string]any{},
				BodyOmission: map[string]any{
					"omitted":      true,
					"reason":       input.Reason,
					"message":      message,
					"rawBodyBytes": input.RawBodyBytes,
					"statusCode":   input.StatusCode,
				},
			},
		},
		ProviderCode:              groupFields.ProviderCode,
		GroupOwnerSystemAccountID: groupFields.GroupOwnerSystemAccountID,
		GroupAccessType:           groupFields.GroupAccessType,
	}
	payload := map[string]any{
		"error": map[string]any{
			"message": input.ResponsePayload.Error.Message,
			"type":    input.ResponsePayload.Error.Type,
		},
	}
	startedAtMs := requestCtx.StartedAt.UnixMilli()
	_ = r.usage.RecordGatewayFailure(context.Background(), failureContext, gatewayusage.RecordGatewayFailureInput{
		StatusCode:         input.StatusCode,
		StartedAtMs:        startedAtMs,
		ResponsePayload:    payload,
		ErrorMessage:       message,
		ErrorCode:          input.ErrorCode,
		FailureAttribution: gatewayusage.UsageFailureAttribution(input.FailureAttribution),
	})
}

func pathWithoutQueryOf(req *http.Request) string {
	path := req.URL.Path
	if path == "" {
		path = strings.SplitN(req.URL.RequestURI(), "?", 2)[0]
	}
	return path
}

// chainGatewayRuntimeKey 携带 preauth 解析的 runtime 快照：body 拒绝记录在
// body 阶段读取身份（Node req.gatewayRuntime 同源）；gatewaybody 无法引用
// gatewaypreauth 类型，故以 context 值传递。
type chainGatewayRuntimeKey struct{}

func chainGatewayRuntimeOf(r *http.Request) *gatewayruntimecache.GatewayRuntime {
	if r == nil {
		return nil
	}
	runtime, _ := r.Context().Value(chainGatewayRuntimeKey{}).(*gatewayruntimecache.GatewayRuntime)
	return runtime
}

// chainTextRawBodyLimitOf 提供捕获期文本 lane 上限 provider：读运行时设置
// 快照（readCachedGatewaySettings）；快照缺失或未配置时保持 unconfigured
// （gatewaybody 回落 16 MiB 默认）。
func chainTextRawBodyLimitOf(cache *gatewayruntimecache.Service) gatewaybody.TextRawBodyLimitProvider {
	return func() (int, bool) {
		if cache == nil {
			return 0, false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		settings, err := cache.ReadCachedGatewaySettings(ctx)
		if err != nil || settings.GatewayTextRawBodyLimitMegabytes <= 0 {
			return 0, false
		}
		return int(settings.GatewayTextRawBodyLimitMegabytes), true
	}
}

// chainNewAuditID mirrors `audit_${Date.now()}_${randomUUID()}`（gateway
// preauth 的审计 ID 形状，供组合根的 dropped audit 面复用）。
func chainNewAuditID(clock gatewaypreauth.Clock) string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("audit_%d", clock.Now().UnixMilli())
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst, buf[:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], buf[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], buf[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], buf[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:], buf[10:])
	return fmt.Sprintf("audit_%d_%s", clock.Now().UnixMilli(), string(dst))
}
