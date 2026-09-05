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
	// SessionIdentity mirrors getGatewaySessionIdentity
	// (session-identity/index.ts, G14).
	SessionIdentity func(req *gatewaypreauth.GatewayRequest) SessionIdentityView

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
