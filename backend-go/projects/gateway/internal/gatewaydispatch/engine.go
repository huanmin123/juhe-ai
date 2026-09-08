package gatewaydispatch

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// Engine is the dispatch engine assembly: every collaborator port the Node
// implementation imported directly arrives here for G20 wiring. Nil ports
// degrade exactly like the Node guards (feature disabled).

// EngineConfig carries the runtime-config numbers the engine reads (Node
// config/runtime.ts gateway.* values).
type EngineConfig struct {
	// AccountConcurrencyRetryBudgetMs mirrors
	// gateway.accountConcurrencyRetryBudgetMs.
	AccountConcurrencyRetryBudgetMs int64
	// AccountConcurrencyRetryInitialDelayMs mirrors the initial delay.
	AccountConcurrencyRetryInitialDelayMs int64
	// AccountConcurrencyRetryMaxDelayMs mirrors the max delay.
	AccountConcurrencyRetryMaxDelayMs int64
	// AccountApiKeyRequestAttemptSafetyLimit mirrors
	// gateway.accountApiKeyRequestAttemptSafetyLimit.
	AccountApiKeyRequestAttemptSafetyLimit int
	// AccountCircuitConfirmationFailuresRequired mirrors the optional
	// runtime override (nil = settings value).
	AccountCircuitConfirmationFailuresRequired *int64
	// KeyModelForegroundQueueWaitMs / PollMs mirror the local dispatch
	// constants keyModelForegroundQueueWaitMs=1_200 /
	// keyModelForegroundQueuePollMs=25
	// (gateway/dispatch/upstream-dispatch.ts:305-306), not config/runtime.ts.
	KeyModelForegroundQueueWaitMs int64
	KeyModelForegroundQueuePollMs int64
	// Secret 是 JUHE_AI_SECRET（Node runtimeConfig.secret）：账户 API Key
	// 指纹 HMAC-SHA256(secret, key) 的密钥（B-1，BUG-0174）。组合根接线前为
	// 空串；Node createHmac 对空 key 正常计算，空值不产生空串快捷路径，但
	// 组合根必须与水合层（chainAccountsSelector.secret）注入同一 secret，
	// dispatch 产出的 SelectedAPIKeyFingerprint 才能与
	// account_api_key_runtime_states 及探活池命中。
	Secret string
}

// DefaultEngineConfig mirrors the Node runtime defaults: the retry budget,
// initial delay and max delay align with gateway.accountConcurrencyRetry*
// (config/runtime.ts:766-768). The attempt safety limit mirrors
// gateway.accountApiKeyRequestAttemptSafetyLimit (runtime.ts:769), whose Node
// default is globalConcurrencyMax (JUHE_AI_CONCURRENCY_GLOBAL_MAX, default
// 5_000, range 1..50_000, runtime.ts:410); this constant takes that default
// and the assembly root must override it with the env-configured globalMax at
// wiring time (this package ships no assembly, so there is no runtime impact
// today). KeyModelForegroundQueueWaitMs/PollMs mirror the upstream-dispatch
// local constants (upstream-dispatch.ts:305-306), not config/runtime.ts.
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		AccountConcurrencyRetryBudgetMs:        1_200,
		AccountConcurrencyRetryInitialDelayMs:  120,
		AccountConcurrencyRetryMaxDelayMs:      480,
		AccountApiKeyRequestAttemptSafetyLimit: 5_000,
		KeyModelForegroundQueueWaitMs:          1_200,
		KeyModelForegroundQueuePollMs:          25,
	}
}

// Engine holds the dispatch collaborators. The zero value is not usable;
// construct with NewEngine.
type Engine struct {
	Config EngineConfig
	Clock  gatewaypreauth.Clock

	// ProviderDriver mirrors providers/drivers/registry.ts (required).
	Driver ProviderDriver
	// FailureDispatcher mirrors response/failure-dispatch.ts (required for
	// the attempt engine; the candidate pipeline does not need it).
	FailureDispatcher FailureDispatcher
	// Usage mirrors usage/records.ts recordFailedUpstreamAttempt (G17).
	Usage UsageAttemptRecorder
	// AttemptAuditSinkFactory provides the attempt-level audit sink when the
	// request capture does not implement it (G17).
	AttemptAuditSinkFactory func() AttemptAuditSink

	Suppression           SuppressionPort
	Degradation           DegradationPort
	Latency               LatencyDegradationPort
	ProxyHealth           ProxyHealthPort
	ClientIPAvoidance     ClientIPAvoidancePort
	ClientSourceAvoidance ClientSourceAvoidancePort
	HotQuality            HotQualityPort
	Affinity              SessionAffinityPort
	HighConcurrencyQueue  HighConcurrencyWaiter
	ClientIPConcurrency   ClientIPConcurrencyAcquirer
	Quota                 AuthorizationQuotaChecker
	Concurrency           AccountConcurrencyStore
	Cache                 RuntimeCachePort
	Locks                 AccountLocks
	RecoverableWait       RecoverableSuppressionWaiter
	KeyModel              KeyModelAdmission
	KeyModelStore         gatewayaccounteffects.KeyModelRuntimeStore
	Circuits              *gatewaycircuit.CircuitService
	APIKeyEffects         APIKeyEffectsPort
	AccountState          AccountStateMutations
	CodexBridge           CodexBridgePort
	// KeyRotation 轮转计数器端口（Node Redis 账户 Key 轮换计数器，
	// account-api-key-rotation.ts:269-299；B-3，BUG-0174）。nil 时回落包级
	// 进程内计数器 defaultAPIKeyRotationCounter；组合根下一波接 Redis 实现。
	KeyRotation APIKeyRotationCounter
	// SessionIdentity mirrors getGatewaySessionIdentity
	// (session-identity/index.ts, G14).
	SessionIdentity func(req *gatewaypreauth.GatewayRequest) SessionIdentityView

	// HotQualityAttemptFactory 挂接 G12 热质量 attempt 记账（D-137，
	// BUG-0175：attempt 记录 / first-byte / 终态结算）。nil 保持引擎的中性
	// no-op 生命周期（runtime 缺席语义）。
	HotQualityAttemptFactory HotQualityAttemptLifecycleFactory

	// Transport carries the shared upstreamhttp collaborators.
	Transport TransportDeps
}

// NewEngine wires the engine with defaults.
func NewEngine(driver ProviderDriver, failureDispatcher FailureDispatcher) *Engine {
	return &Engine{
		Config:            DefaultEngineConfig(),
		Clock:             gatewaypreauth.SystemClock{},
		Driver:            driver,
		FailureDispatcher: failureDispatcher,
	}
}

// CandidatePipelineOf returns the pipeline facade for this engine.
func (e *Engine) CandidatePipelineOf() *CandidatePipeline { return NewCandidatePipeline(e) }

// keyRotationOf returns the injected rotation counter; the package-level
// in-process counter is the fallback while the composition root has not
// wired a Redis implementation.
func (e *Engine) keyRotationOf() APIKeyRotationCounter {
	if e.KeyRotation != nil {
		return e.KeyRotation
	}
	return defaultAPIKeyRotationCounter
}

// auditCaptureOf adapts the frozen G05 capture context into the dispatch
// capture: the frozen context wins when it implements the attempt-level
// surface, otherwise the engine factory provides the sink.
func (e *Engine) auditCaptureOf(context gatewaypreauth.AuditCaptureContext) AuditCapture {
	if sink, ok := context.(AttemptAuditSink); ok && context != nil {
		return AuditCapture{Context: context, Sink: sink}
	}
	capture := AuditCapture{Context: context}
	if e.AttemptAuditSinkFactory != nil {
		capture.Sink = e.AttemptAuditSinkFactory()
	}
	return capture
}
