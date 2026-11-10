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
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycallertier"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayobs"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/realtimetoken"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/securitypolicies"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
	"github.com/redis/go-redis/v9"
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
	// AuditLogSuccessSampleRate / AuditLogSuccessHotRetentionHours mirror
	// readAuditLogSettings() 的 env-merged 值（auditlog.LoadConfig 的
	// JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE / SUCCESS_HOT_RETENTION_HOURS，
	// 默认 0.1 / 1）。E2E-FINDING #10：此前组合根只传递 Enabled 位，成功
	// 采样率恒 0，成功信封 attempts 永远被写侧清空。
	AuditLogSuccessSampleRate        float64
	AuditLogSuccessHotRetentionHours int
	// AuditUsageDispatch carries the same producer behind the usage-face
	// audit port (gatewayusage.AuditDispatcher). Nil degrades identically.
	AuditUsageDispatch gatewayusage.AuditDispatcher
	// UsageDeliverer 是用量记录投递端口：生产为网关直投 PG（共享
	// usagewriter，2026-11-09 T188）；测试可注入文件 spool 或捕获替身。
	// nil 时 recorder 按丢弃计数与采样告警处理（不再回落生产文件 spool）。
	UsageDeliverer usageRecordDeliverer
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

	// M2 媒体任务面运行时输入（媒体设计 §8.2）：MediaJobsDB 是业务库句柄
	//（media_jobs 仓储 + 账户亲和水合读面，juhe_business schema 限定）；
	// MediaJobsSecret 是账户凭据解密密钥（生产与 newChainAccountsSelector
	// 的 cfg.Secret 同源）。DB 为 nil 仅组合测试——任务面端点显式 503 降级
	//（不静默吞请求）。
	MediaJobsDB     *sql.DB
	MediaJobsSecret string

	// RealtimeTokens 是 M5b realtime ephemeral token 签发/校验面（Realtime
	// 设计 §4；runtimeStateDriver!=='redis' 时 nil——client_secrets 端点显式
	// 503 降级，同 MediaJobsDB nil 的降级先例）。
	RealtimeTokens *realtimetoken.Service
	// RealtimeConnections 是每 API Key WS 并发连接计数（生产 Redis 全局，
	// 组合测试 nil 时退回进程内）。
	RealtimeConnections *chainRealtimeConnectionLimiter

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
	ConcurrencyTracker accountConcurrencyTracker

	// G14/G18 session + codex collaborators (optional; the adapters degrade
	// to the header-only identities Node serves when the resolver is absent).
	Identity *sessionIdentityServices
	// GroupAffinity 是组级会话绑定服务（策略路由分组会话亲和设计 §3）：独立
	// 数据面，消费方为 preauth 键派生/黏住端口、引擎最终成功写回端口与响应层
	// 三层删除。nil = 未装配（组合测试），组黏性整体不生效（零改动路径）。
	GroupAffinity *gatewaysession.GroupAffinityService
	// CodexBridge 组合注入点已随 2026-11-07 跨协议 Responses→Chat 桥退役删除；
	// preauth 的 Codex 端口在生产装配为原生 compaction contract 单点
	//（codexCompactionExpectedPort，chain_ports.go），不再有 bridge 服务注入。
	Recoverable gatewaypreauth.RecoverableWait
	// DispatchRecoverableWait 是 G11 等待引擎的 dispatch 侧句柄（nil 仅组合
	// 测试——engine.RecoverableWait 保持缺席语义，抑制耗尽路径快速退出）。
	DispatchRecoverableWait *gatewaycircuit.PreAuthRecoverableWait

	// Suppression / degradation / locks (optional; disabled implementations
	// below keep the attempt loop defined). AccountLocks 生产装配为
	// chain_account_locks.go 的 SQL 运行面（BUG-0174 B-2）；nil 仅组合测试
	//（生产恒装配），落到 disabledAccountLocks 的「视为未锁」降级。
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

	// UpstreamRetryBackoffDelaysMs 是 dispatch 尝试循环退避上限的 R4 测试
	// 注入点（gatewaydispatch.EngineConfig.UpstreamRetryBackoffDelaysMs）。
	// nil 保持生产硬编码上限（1000/500/3000ms，逐字节一致）。
	UpstreamRetryBackoffDelaysMs []int64

	// KeyRotation 是账户 API Key 轮转计数器（gatewaydispatch.Engine.
	// KeyRotation，B-3 BUG-0174）：生产装配为
	// chain_apikey_rotation_redis.go 的 Redis 计数器（StateClient 同源）；
	// nil 时引擎回落包级进程内计数器。
	KeyRotation gatewaydispatch.APIKeyRotationCounter

	// StateClient 是该链的 runtime-state Redis 客户端（与 turn-retry /
	// KeyRotation 同源判据，runtimeStateDriver==='redis' 时非 nil）。调用方
	// 分级影子观测据此选驱动（chain_compose 装配段）：有则 Redis 跨进程，
	// 无则进程内 memory（部署形态选择，设计 §6.1；非运行时回退）。
	StateClient redis.UniversalClient
	// CallerTierNamespace 是调用方分级域的带 tag namespace。
	CallerTierNamespace string
	// SecurityPolicyProtection 是 API Key 安全策略防护次数的 Redis 增量计数
	// 面（写后缓存设计 §4.3.1；组合根与 securitypolicies 管理面构造同一实
	// 例，刷写任务共用同一客户端与 namespace）。nil（StateClient 缺席 / 组合
	// 测试）时终态 INCRBY 整体无操作。
	SecurityPolicyProtection *securitypolicies.ProtectionCountStore

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

	// anthropic（Claude OAuth）unified rate limit 响应头派发（AI账户Grok用量
	// 快照设计 §8.2；optional：nil 派发器在 gatewaycodex 资格门后静默跳过）。
	// 与 codex 同一通道适配器（compose_codex_usage_headers.go 的
	// record_maintenance_jobs 快照行，kind anthropic_claude）。
	AnthropicUsageHeadersDispatcher gatewaycodex.AnthropicUsageHeadersDispatcher

	// OAuthTokenRefresh 是 OAuth 401 自动刷新协调器（T65-①，
	// chain_oauth_refresh.go；optional：nil 时派发器保持现状 SkipAccount，自动
	// 刷新整体缺席——仅组合测试）。生产装配在 compose.go：oauthStore +
	// chainServices.Cache 同源注入。
	OAuthTokenRefresh *chainOAuthRefreshCoordinator

	// 失败派发链装配（chain_request_failure_health.go / chain_turn_probe_store.go /
	// chain_turn_retry_redis.go）：健康检查派发的进程内 outbox writer（常驻，
	// 无 HTTP 目标）+ Redis 驱动的 turn-retry 状态存储（nil → memory 驱动，
	// Node runtimeStateDriver !== 'redis' 分叉）。
	HealthProbeOutbox   *chainProbeRequestOutboxWriter
	TurnRetryStateStore gatewaycodex.TurnRetryStateStore

	// KeyModelHealthDispatch 是 key-model attempt 的健康检查派发端口
	//（gatewayaccounteffects.GatewayKeyModelAttempt.SetDispatcher；optional：
	// nil 保持 dispatcher 缺席）。生产装配为 compose.go 的
	// chainKeyModelHealthDispatcher（accounts runtime-reset bridge 的
	// DispatchAccountHealthCheck，落 account_health_probe_request_outbox）。
	KeyModelHealthDispatch gatewayaccounteffects.AccountHealthCheckDispatcher

	// ---- W2-C production wiring (BUG-0175)；nil 仅保留给显式关闭开关 /
	// 组合测试，链条回落 chain_ports.go 的 disabled*/degraded* 直通。 ----
	// AccountCircuits 是 D-131 账户电路服务（SUSPECT/confirmation/恢复）。
	AccountCircuits *gatewaycircuit.CircuitService
	// ClientIPSlots 是 D-109 普通分组的 client-IP 并发槽。
	ClientIPSlots gatewaydispatch.ClientIPConcurrencyAcquirer
	// KeyModelStore 是 D-133 的 key-model 前台准入状态存储（driver 选择器）。
	KeyModelStore gatewayaccounteffects.KeyModelRuntimeStore
	// ProxyHealth 是 D-136 的上游桶健康端口。
	ProxyHealth gatewaydispatch.ProxyHealthPort
	// ProxyPoolTargets 是池绑定账户的请求时健康门/租约修复端口（代理池设计
	// §10.1）。nil 仅在组合测试/未装配时保持既有直接代理语义。
	ProxyPoolTargets gatewaydispatch.AccountProxyTargetResolver
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
	// ContentFingerprintLineage 是内容指纹会话身份的谱系判别器（内容指纹会话
	// 身份设计 §4.3），门控确认档 ConversationKey 的产出；nil = 未装配（组合
	// 测试），确认档不产出，审计临时档不受影响。
	ContentFingerprintLineage gatewayContentFingerprintLineage
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
		missing = append(missing, "gatewaydispatch.InflightQuota（gatewayquota.InflightQuotaService）")
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
	// 用量记录直投：生产由组合根注入 PG deliverer；测试注入捕获替身。
	// nil 时 recorder 逐条计数并采样告警，不静默吞掉投递面缺失。
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 4096, Logger: logger}, deps.UsageDeliverer)
	// D-147（BUG-0175）：收尾队列容量/并发改由组合根 env 旋钮传入——
	// JUHE_AI_GATEWAY_USAGE_FINALIZATION_MAX_ITEMS 与
	// JUHE_AI_CONCURRENCY_GLOBAL_MAX（Node runtimeConfig.gateway.
	// usageFinalizationMaxItems / concurrency.globalMax）；此前硬传 (0,0)
	// 使两个 env 对队列失效，只能吃到包级默认。
	// 2026-11-09 T188/T189：文件 spool 与溢出补偿臂已删除；usage 属运营
	// 统计，收尾队列满时直接丢弃并计数（不反压请求），recorder 缓冲满后同样
	// 按丢弃计数与采样告警暴露。
	dispatch := gatewayusage.NewFinalizationDispatch(recorder, deps.UsageFinalizationMaxItems, deps.ConcurrencyGlobalMax)
	dispatch.EnableDropOnFull()
	dispatch.WithLogger(slogLogger{inner: logger})
	// chainSyncPricingAllowed 是同步定价目录 gate 的组合根字面量（D6，存储
	// 形态收口：原按 cacheDriver 判断的写法本就以常量表达，redis 下现行为
	// true 逐字节保持；启用同步定价属独立行为裁决，不在收口战役内）：usage
	// Service 的 ServiceConfig.SyncPricingAllowed 与完成尝试记录
	//（chainFinalizationUsage.syncPricingAllowed）同源共用，不另设配置项。
	const chainSyncPricingAllowed = true
	// 同步定价目录（chain_pricing.go）单实例共享：usageService 与完成尝试
	// 记录（gatewayChain.finalizationPricing）走同一 catalog 实例。
	chainPricingCatalog := newChainUsagePricingCatalog(deps.Cache)
	usageService := gatewayusage.NewService(dispatch, gatewayusage.ServiceConfig{SyncPricingAllowed: chainSyncPricingAllowed}).
		WithClock(clock).
		WithLogger(slogLogger{inner: logger}).
		// Synchronous catalog pricing (chain_pricing.go): the cacheDriver!=='redis'
		// gate is chainSyncPricingAllowed above; the adapter resolves
		// the catalog row through the same runtime cache and bills through the
		// shared internal/pricing engine (Node model-catalog.service.ts).
		WithPricingCatalog(chainPricingCatalog).
		// D-190（BUG-0175）：上游失败 prometheus 指标族生产装配——
		// recordGatewayUpstreamFailureMetric 从此有进程内注册表可写。
		WithMetrics(gatewayusage.HTTPMetrics{}).
		// 审计缺口收口（chain_usage_wiring.go）：失败记录 providerCode 兜底
		// （Node defaultGatewayUsageProviderCode = GPT_VENDOR_CODE）、语义
		// 字段解析（usageSemanticForProfile 驱动回退）与协议错误 payload
		// 解析（按账户协议档案分派响应驱动）。WithModelResolver（2026-09-20
		// 收口：UsageModelAccount 已携带 ModelMappings，真实解析组件
		// gatewayopenai.ResolveAccountModelMapping 经 usageModelResolverAdapter
		// 接线，映射后上游模型名进记账）接线；WithAccountAPIKeySuccess 已随
		// 死代码清理删除（原因见 chain_usage_wiring.go 文件头）。
		WithDefaultProviderCode(chainUsageDefaultProviderCode{}).
		WithUsageSemantics(chainUsageSemanticResolver{}).
		WithProtocolErrorParser(newChainUsageProtocolErrorParser()).
		WithModelResolver(usageModelResolverAdapter{})

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
	// The models fast-path loads the account union through the runtime cache
	// (chain_gatewaykeymodels.go：union 成员 ∪ 目录字典，/v1/models 账户并集
	// 设计 §6.1/§6.4)；an unwired ModelCatalog port fails the load with an
	// explicit error instead of rendering an empty list.
	sink := gatewayresponse.NewSink(gatewayresponse.SinkDeps{
		UsageRecords:  &usageDispatchAdapter{service: usageService, recorder: recorder},
		UsageDispatch: &usageDispatchAdapter{service: usageService, recorder: recorder},
		ModelCatalog:  chainGatewayKeyModelCatalog{cache: deps.Cache},
		// HTTP 完成观测：sink 失败 usage 的 CompletedAtMs 按请求上下文里的
		// subject 解析（chain_http_completion.go），缺失回退 nowMs 兜底。
		HTTPCompletion: chainHTTPCompletionObserver{},
		Logger:         gatewayResponseLogger{inner: slog.Default()},
		NowMs:          func() int64 { return clock.Now().UnixMilli() },
	})

	// ---- observability ----
	observability := newSlogObservability(logger, clock)

	// ---- caller tier shadow observation（调用方分级调度设计 §8 M1）----
	// 驱动选择跟随该链 state Redis 客户端可用性（newCallerTierStore）。
	// 指标回调接 gatewaycallertier 进程注册表（/__aisys__/metrics 暴露）；
	// 构造失败 fail-fast（对齐 recorder 的装配惯例）。影子运行，
	// 零资源效果，不新增任何配置项。
	callerTier, err := gatewaycallertier.NewRecorder(newCallerTierStore(deps.StateClient, deps.CallerTierNamespace), gatewaycallertier.PrometheusMetrics(), nil)
	if err != nil {
		recorder.Close()
		return nil, nil, fmt.Errorf("compose caller tier shadow recorder: %w", err)
	}

	// ---- body pipeline (request/body-middleware.ts) ----
	// D-119（BUG-0175）：拒绝记录 Recorder 落 usage failure（Node
	// recordGatewayBodyRejection）；TextRawBodyLimitMegabytes 读运行时快照
	// （req.gatewayRuntime.settings.gatewayTextRawBodyLimitMegabytes），读取
	// 失败回落 16 MiB 默认。
	rejectionRecorder := &chainBodyRejectionRecorder{
		usage: usageService,
	}
	// One shared bounded JSON parser for the whole chain: the body pipeline's
	// materialization jobs and the dispatch driver's once-per-request body
	// parse share the same worker pool.
	chainBodyParser := gatewaybody.NewJSONParser(gatewaybody.JSONParserOptions{Logger: gatewaybodyLogger{inner: logger}})
	bodyPipeline := gatewaybody.NewMiddleware(gatewaybody.Config{
		Logger:                    gatewaybodyLogger{inner: logger},
		Parser:                    chainBodyParser,
		Recorder:                  rejectionRecorder,
		TextRawBodyLimitMegabytes: chainTextRawBodyLimitOf(deps.Cache),
		// 调用方分级影子观测（调用方分级调度设计 §3）：解析器拒绝面只在
		// 「责任方=调用方的 400 无效请求体」时透传 invalid_json 信号；身份
		// 经 D-119 挂进请求上下文的 runtime 快照解析（body 管线运行于认证
		// 之后，认证失败的请求到不了这里）。
		CallerTierSignals: callerTier,
		GatewayAPIKeyIDOf: chainGatewayAPIKeyIDOf,
	})

	// ---- route resolver (adapter 1) ----
	normalRoute := gatewayrouting.NewNormalModelRouteService(
		chainRoutingCache{cache: deps.Cache},
		chainCapabilityFilter{},
	)
	// 超级分组解析期逐片段配额批查与 dispatch 窗口门共用同一 G07 服务
	// （chainDispatchQuota → gatewaydispatch.AuthorizationQuotaChecker）。
	routeResolver := &chainRouteResolver{
		cache:                deps.Cache,
		normal:               normalRoute,
		quota:                newChainDispatchQuota(deps.AuthzQuota),
		globalConcurrencyMax: deps.ConcurrencyGlobalMax,
	}

	// ---- dispatch engine + provider driver (adapter 2) ----
	// The failure dispatcher shares the engine's session-affinity port: the
	// Node dispatcher forgets the account's session affinity on its failure
	// branches (failure-dispatch.ts:208/346/570).
	sessionAffinity := newLocalSessionAffinity()
	// 会话亲和读写面合流：Redis cacheDriver 下 dispatch 侧三个消费面
	//（engine.Affinity / chainFailureDispatcher.affinity /
	// chainResponseAccountEffects.affinity）与 preauth（sessionAffinityAdapter）
	// 共用同一 G14 AffinityService——绑定读写落 Redis，多实例/重启下两侧
	// 同一数据面（此前 dispatch 全走进程内存，读写割裂）。memory
	// cacheDriver（含组合测试 deps.Identity == nil）保持
	// localSessionAffinity，「网关链端口显式降级」warn 只在该真实降级路径
	// 触发（localSessionAffinity.ClaimAsync 的 once warn）。
	var dispatchSessionAffinity gatewaydispatch.SessionAffinityPort = sessionAffinity
	if redisBacked := newChainSessionAffinityDispatchPort(deps.Identity); redisBacked != nil {
		dispatchSessionAffinity = redisBacked
	}
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
	// failureDispatcher 与 gatewayChain.streamFailureAvoidance（BUG-0298 流式
	// 换号的来源级避让入口）同一实例——协作方（strategy/turnRetry/probe/affinity）
	// 同源，避免第二份装配漂移。
	// L2 请求负面缓存运行态（请求级提前耗尽与负面缓存设计 §5/§7）：进程内有
	// 界 TTL map，与失败派发器同生命周期（单机单实例）。
	requestNegativeCache := newChainRequestNegativeCache(func() int64 { return clock.Now().UnixMilli() })
	failureDispatcher := &chainFailureDispatcher{
		usage:                 usageService,
		affinity:              dispatchSessionAffinity,
		clientStrategy:        codexClientStrategy,
		turnRetry:             chainTurnRetry,
		avoidanceProbe:        chainTurnAvoidanceProbe,
		healthDispatch:        chainHealthDispatch,
		policy:                deps.AccountErrorPolicy,
		effects:               deps.AccountErrorPolicyEffects,
		apiKeyObservation:     deps.AccountAPIKeyObservation,
		codexUsageHeaders:     deps.CodexUsageHeadersDispatcher,
		anthropicUsageHeaders: deps.AnthropicUsageHeadersDispatcher,
		negativeCache:         requestNegativeCache,
		oauthRefresh:          deps.OAuthTokenRefresh,
	}
	engine := gatewaydispatch.NewEngine(newChainProviderDriverWithCache(deps.Cache, chainBodyParser), failureDispatcher)
	// B-1（BUG-0174）波1遗留接线：dispatch 的 Key 指纹密钥与水合层同源
	//（chain_runtime.go newChainAccountsSelectorWithStats 的 cfg.Secret）。
	engine.Config.Secret = deps.EngineSecret
	// R4：上游尝试循环退避上限注入（capacity queued / capacity plain /
	// recoverable wait 三语义位）；nil 保持生产硬编码，组合测试注入短退避。
	engine.Config.UpstreamRetryBackoffDelaysMs = deps.UpstreamRetryBackoffDelaysMs
	// W2：engine.go DefaultEngineConfig 注释契约——attempt safety limit 的
	// Node 默认即 globalConcurrencyMax（runtime.ts:769），组合根必须用 env
	// 配置的 JUHE_AI_CONCURRENCY_GLOBAL_MAX 覆盖编译期 5000 默认。deps 值与
	// 用量收尾队列/全局并发槽同源（runtimeConfig.ConcurrencyGlobalMax，env
	// 未设置时已回落 5000）；异常零值构造保持 engine 默认不变。
	if deps.ConcurrencyGlobalMax > 0 {
		engine.Config.AccountApiKeyRequestAttemptSafetyLimit = deps.ConcurrencyGlobalMax
	}
	// B-4（BUG-0175）接线：跨协议桥响应面（Node transformUpstreamResponse
	// driver 链）——桥响应转换挂在 attempt 尾部，非桥请求保持直通。
	engine.ResponseTransformer = newChainBridgeResponseTransformer()
	// P2 接线：上游响应模型观察挂在 attempt 内、桥转换之前的原始上游流上
	//（Node upstream-attempts.ts:180-210 response =
	// observeUpstreamResponseModelResponse(response, observation) 先于
	// transformGatewayUpstreamResponseForAccount），协议按上游账户解析——桥
	// 路径归因的是上游原生模型（modelVersion / 上游 model），不是转换后的
	// 客户端形态；观察值经 UpstreamDispatchResult.UpstreamResponseModelSlot
	// 带回 chain 响应面发布。
	engine.ObserveUpstreamResponseModel = newChainUpstreamResponseModelObserver()
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
	// M2 媒体任务面运行时（媒体设计 §7/§8）：与派发引擎同源传输面（URL 安全
	// 策略 / 全局并发槽 / keep-alive 池共用同一实例）；DB 缺席（组合测试）
	// 保持 nil——任务面端点显式 503 降级。
	var mediaJobs *mediaJobsRuntime
	if deps.MediaJobsDB != nil {
		mediaJobs = &mediaJobsRuntime{
			repo:      gatewaymedia.NewMediaJobsRepo(deps.MediaJobsDB, clock.Now),
			db:        deps.MediaJobsDB,
			secret:    deps.MediaJobsSecret,
			transport: engine.Transport,
		}
	}
	// B-3（BUG-0174）波1遗留接线：Redis 轮转计数器（nil 保持进程内回退）。
	engine.KeyRotation = deps.KeyRotation
	engine.Clock = clock
	// 会话亲和读写面合流：Redis cacheDriver 下与 preauth 共用 AffinityService
	//（见上方 dispatchSessionAffinity 装配注释）；本地降级保持
	// localSessionAffinity。
	engine.Affinity = dispatchSessionAffinity
	// 组级会话黏性写回端口（策略路由分组会话亲和设计 §5）：最终协议成功把
	// 最终成功派发账户所属分组写回组绑定（成功者赢 CAS ≤2 轮）。nil = 未装配
	//（组合测试），写回句柄为 nil，链面结算自然跳过。
	engine.GroupAffinity = deps.GroupAffinity
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
	// 池绑定请求时健康门/租约修复（代理池设计 §10.1）：nil 保持直接代理
	// 既有语义；生产组合根传入 writer-backed 解析器。
	engine.ProxyTargets = deps.ProxyPoolTargets
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
	// BypassKeyModelAdmission 语义禁用准入）。KeyModelHealthDispatch 随
	// 准入端口挂到每个 admitted attempt（SetDispatcher，attempt 为
	// per-request 对象）。
	if deps.KeyModelStore != nil {
		engine.KeyModel = chainKeyModelAdmission{healthDispatch: deps.KeyModelHealthDispatch}
		engine.KeyModelStore = deps.KeyModelStore
	}
	// D-109（BUG-0175）接线：普通分组的 client-IP 并发槽（nil 时
	// preparation 的槽获取段不会运行——组合根缺槽即 panic 风险，故
	// chainRuntimeServices 保证非 nil）。
	if deps.ClientIPSlots != nil {
		engine.ClientIPConcurrency = deps.ClientIPSlots
	}
	// D-134（BUG-0175）接线：半开租约释放 → 恢复等待者唤醒。
	if deps.WakeRecoverableWaiter != nil {
		gatewaydispatch.SetRecoverableUnavailableRuntimeWaiterNotifier(deps.WakeRecoverableWaiter)
	}
	// dispatch 引擎 RecoverableWait 接线（G11 等待引擎，与 preauth
	// Recoverable 共用实例）：全部候选被本地抑制时等待恢复而非快速耗尽。
	// nil 仅组合测试——引擎保持缺席语义（抑制耗尽直接退出）。
	if deps.DispatchRecoverableWait != nil {
		engine.RecoverableWait = &chainDispatchRecoverableWait{wait: deps.DispatchRecoverableWait}
	}
	// W1b 接线：候选准备完成 → gateway_dispatch_decision 结构化日志
	//（进程级观察槽，观察回调 panic-safe；引擎侧默认 no-op，本组合根必装）。
	gatewaydispatch.SetDispatchDecisionObserver(newChainDispatchDecisionObserver(logger))
	if chainTurnRetry != nil {
		engine.ClientSourceAvoidance = &chainClientSourceAvoidance{turnRetry: chainTurnRetry}
	} else {
		engine.ClientSourceAvoidance = &degradedClientSourceAvoidance{}
	}
	engine.ClientIPAvoidance = newChainClientIPAvoidance(deps.AvoidanceTracker)
	engine.Quota = newChainDispatchQuota(deps.AuthzQuota)
	// T93-1（C3-H1）+ T93-4（C3-H8）：请求级在途额度预占（API Key + 授权/
	// 团队 scope）在选定账户、拿到 vendor 与上游映射模型之后执行；nil = 未
	// 装配时只保留 preflight 的已结算额度拒绝。
	engine.InflightQuota = newChainRequestInflightQuota(deps.InflightQuota, deps.APIKeyQuota, deps.AuthzQuota)
	if deps.ConcurrencyTracker == nil {
		// nil 兜底仅供组合测试：生产组合根必须传 chainServices.
		// ConcurrencyTracker（engine 并发槽与容量忙判定共用同一事实源）。
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
		GroupAffinity:      deps.GroupAffinity,
		// 调用方分级影子观测信号口（调用方分级调度设计 §3/§5.4）：preflight
		// 在本地模型过滤失败按调用方责任渲染终态时透传 model_filter_failed。
		CallerTier:  callerTier,
		Codex:       codexCompactionExpectedPort{},
		Recoverable: deps.Recoverable,
	})
	if err != nil {
		recorder.Close()
		return nil, nil, err
	}
	imagePreflight.preauth = preauthService

	// 多副本 realtime 每 API Key 连接计数：生产由 chain runtime 注入 Redis
	// 实现；组合测试未注入时保持进程内，避免测试依赖外部 Redis。
	realtimeConnections := deps.RealtimeConnections
	if realtimeConnections == nil {
		realtimeConnections = &chainRealtimeConnectionLimiter{}
	}

	chain := &gatewayChain{
		preauth:       preauthService,
		engine:        engine,
		observability: observability,
		clock:         clock,
		bodyPipeline:  bodyPipeline,
		// 调用方分级影子观测记录器（调用方分级调度设计 §8 M1）：分母在本链
		// 认证成功点计入，信号经 preauth CallerTier 口与 bodyPipeline 适配
		// 透传；影子运行零资源效果。
		callerTier:        callerTier,
		finalizationUsage: recorder,
		// 完成尝试记录的同步定价面：catalog 与上方 usageService 共享同一实例，
		// gate 与 ServiceConfig.SyncPricingAllowed 同源（chainSyncPricingAllowed）。
		finalizationPricing:            chainPricingCatalog,
		finalizationSyncPricingAllowed: chainSyncPricingAllowed,
		auditSettings: auditSettingsSourceAdapter{
			enabled:                  deps.AuditLogEnabled,
			successSampleRate:        deps.AuditLogSuccessSampleRate,
			successHotRetentionHours: deps.AuditLogSuccessHotRetentionHours,
		},
		auditDispatcher:    deps.AuditUsageDispatch,
		usageModelResolver: usageModelResolverAdapter{},
		// anthropic unified rate limit 头成功面派发（AI账户Grok用量快照设计
		// §8.2；失败面对称口在 chainFailureDispatcher.anthropicUsageHeaders）。
		anthropicUsageHeaders: deps.AnthropicUsageHeadersDispatcher,
		// BUG-0298：流式换号的来源级避让入口，与 engine 失败派发器同一实例
		//（见上方 failureDispatcher 注释）；nil = 未装配，换号仍执行、仅不记录。
		streamFailureAvoidance: failureDispatcher,
		// M2 媒体任务面（媒体设计 §4.2/§7/§8）。
		mediaJobs: mediaJobs,
		// M5b realtime ephemeral token 面（Realtime 设计 §2/§4）。
		realtimeTokens: deps.RealtimeTokens,
		// M5b2 realtime WS 桥接面（Realtime 设计 §3）：运行时缓存句柄 +
		// 每 API Key 并发连接计数器（生产 Redis 全局计数）。
		cache:               deps.Cache,
		realtimeConnections: realtimeConnections,
		// 防护次数 Redis 计数面（安全策略防护次数计数 V1）：终态消费点
		// fire-and-forget INCRBY；nil（组合测试 / StateClient 缺席）无操作。
		securityPolicyProtection: deps.SecurityPolicyProtection,
	}
	// W4-B（BUG-0175）D-132 接线：响应层账户副作用面（配置策略避让 +
	// 上游桶避让写侧）。nil 服务（组合测试）保持 nil——finalization 对 nil
	// AccountEffects 已有守卫分支。
	if deps.ConfiguredPolicyAvoidance != nil && deps.ProxyHealthService != nil && deps.Cache != nil {
		chain.responseAccountEffects = &chainResponseAccountEffects{
			avoidance:     deps.ConfiguredPolicyAvoidance,
			proxyHealth:   deps.ProxyHealthService,
			cache:         deps.Cache,
			affinity:      dispatchSessionAffinity,
			groupAffinity: deps.GroupAffinity,
		}
	}

	shutdown := func() {
		recorder.Close()
		// caller tier 影子事件在 Redis 形态下由后台 worker 消费；停机时尽力
		// 排空（有界，不阻塞进程退出）。
		callerTierCtx, callerTierCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = callerTier.Close(callerTierCtx)
		callerTierCancel()
		// 请求失败探针的 outbox INSERT 由单 worker 异步执行；停机时尽力排空。
		probeDispatchCtx, probeDispatchCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = chainHealthDispatch.Close(probeDispatchCtx)
		probeDispatchCancel()
		// D-192/D-146：链条停机时释放上游 keep-alive 传输的空闲连接。
		upstreamClientPool.CloseIdleConnections()
		// 停掉共享 JSON 解析池的常驻 worker，防止多次组合（测试全量/链重建）泄漏 goroutine。
		chainBodyParser.Stop()
	}
	return chain, shutdown, nil
}

// newChainUpstreamResponseModelObserver 把 gatewayobs 的上游响应模型观察装配
// 成 dispatch 引擎钩子：观察器在 fetch 之后、桥转换之前挂到原始上游流
// （Node upstream-attempts.ts:180-210），协议按上游账户解析，观察器不改写
// 字节，干净 EOF / 提前关闭时把观察到的模型交给 publish（即本尝试的
// UpstreamResponseModelSlot.Set）。
func newChainUpstreamResponseModelObserver() func(*gatewaydispatch.GatewayUpstreamResponse, gatewaydispatch.UpstreamResponseModelObservationInfo, func(string)) {
	return func(response *gatewaydispatch.GatewayUpstreamResponse, info gatewaydispatch.UpstreamResponseModelObservationInfo, publish func(model string)) {
		if response == nil || response.Body == nil || publish == nil {
			return
		}
		observation := gatewayobs.CreateUpstreamResponseModelObservation(gatewayobs.UpstreamResponseModelObserverOptions{
			Protocol: gatewayobs.UpstreamResponseModelProtocolForRequest(gatewayobs.UpstreamResponseModelRequestInfo{
				Headers:      info.Headers,
				UpstreamURL:  info.UpstreamURL,
				ProviderCode: info.ProviderCode,
				ProtocolCode: info.ProtocolCode,
			}),
			SSE: info.SSE,
		})
		response.Body = gatewayobs.ObserveUpstreamResponseModelBodyPublishing(response.Body, observation, publish)
	}
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

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// client model catalog dictionary (models fast-path; /v1/models 账户并集设计
// §4.9——目录降级为元数据字典。成员资格装载见 chain_gatewaykeymodels.go 的
// chainGatewayKeyModelCatalog；此处只保留字典构建所需的条目投影与 scope 秩。
// 旧的成员过滤职责（selectClientCatalogItems 及可见性/价格/运营排序 helper）
// 已随并集契约退役：catalog_visible、可见价格、音视频/realtime 模式过滤不再
// 影响 /v1/models 列出什么，只保留 active 行内 personal > global > built_in
// 的每模型最优行选取。)
// ---------------------------------------------------------------------------

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
		InputModalities:               item.InputModalities,
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
// worker / 429 speed-first 背压）落成 usage failure。请求体拒绝属于网关拦截
// 面（零上游尝试），2026-10-16 收窄后不再落审计；usage failure 仅在 runtime
// 快照已解析出 API Key 时写（Node !apiKey early return）。记录失败只告警，
// 不改变原始拒绝响应。
type chainBodyRejectionRecorder struct {
	usage *gatewayusage.Service
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
	message := input.ErrorMessage
	if message == "" {
		message = input.ResponsePayload.Error.Message
	}
	if runtime := chainGatewayRuntimeOf(req); runtime != nil && runtime.APIKey != nil {
		r.recordUsageFailure(requestCtx, req, traceID, message, input, runtime)
	}
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
				Method: strings.ToUpper(req.Method),
				Path:   req.URL.Path,
				// originalUrl 经凭据 query 掩码（Gemini `?key=` 等）：
				// RecordGatewayFailure 的 usageRecordSnapshot 直通透传，不经过
				// BuildUsageRequestSnapshot 的统一脱敏，必须在组合根先脱敏。
				OriginalURL: gatewayusage.SanitizeURLForLog(req.URL.RequestURI()),
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

// chainGatewayAPIKeyIDOf 从请求上下文解析 preauth 已认证的 API Key ID
// （D-119 runtime 快照同一身份通道，供 gatewaybody 信号透传面消费）；
// 未解析/类型不符返回空串，调用方按「身份不可用」跳过影子信号。
func chainGatewayAPIKeyIDOf(r *http.Request) string {
	if runtime := chainGatewayRuntimeOf(r); runtime != nil && runtime.APIKey != nil {
		return runtime.APIKey.ID
	}
	return ""
}

// newCallerTierStore 按该链 state Redis 客户端可用性选调用方分级的驱动
// （调用方分级调度设计 §6.1/§6.3）：与 turn-retry / KeyRotation 同源判据
// （compose.go 传 chainServices.StateClient，runtimeStateDriver==='redis'
// 时非 nil）。redis 驱动落 juhe-ai:caller-tiers:<apiKeyID>（滑动 TTL 48h，
// 重启/丢失回 T2 保守默认）；standalone/memory 驱动落进程内 store。双形态
// 都是启动时选定的部署形态，非运行时回退。
func newCallerTierStore(stateClient redis.UniversalClient, namespace string) gatewaycallertier.Store {
	if stateClient != nil {
		return gatewaycallertier.NewRedisStore(stateClient, rediscfg.NamespacedKey("juhe-ai:caller-tiers:", namespace), time.Now)
	}
	return gatewaycallertier.NewMemoryStore(time.Now)
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
