package gatewaypreauth

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Port interfaces toward the gateway slices owned by later work packages.
// The orchestration (preflight.go / preauth.go) calls exactly these seams in
// the Node order; production wiring lands with each slice. Ports exist only
// where no Go package carries the contract yet:
//
//   - runtime guards (G13): circuits, client-ip policy, user request limits,
//     authenticated models rate limit;
//   - session identity + affinity (G14);
//   - dispatch candidate pipeline (G15);
//   - fixed responses + failure response sink (G16);
//   - audit capture dispatch (G17);
//   - client strategy + codex bridge (G18);
//   - image permission preflight + recoverable wait (request/runtime helpers
//     not yet owned by a Go package).

// PreAuthCircuits mirrors the consumed surface of
// runtime/client-ip-error-circuit.service.ts (G13).
type PreAuthCircuits interface {
	InspectPreAuthCircuit(ctx context.Context, input PreAuthCircuitInput) (CircuitDecision, error)
	RecordPreAuthFailure(ctx context.Context, input PreAuthFailureInput) (CircuitDecision, error)
	InspectClientIPErrorCircuit(ctx context.Context, input ClientIPErrorCircuitInput) (CircuitDecision, error)
	RecordClientIPErrorCircuitSuccess(ctx context.Context, input ClientIPErrorCircuitInput) error
	RecordClientIPErrorCircuitSample(ctx context.Context, input ClientIPErrorCircuitSampleInput) (CircuitDecision, error)
}

// ClientIPPolicy mirrors the consumed surface of
// runtime/client-ip-policy-cache.service.ts (G13).
type ClientIPPolicy interface {
	// InspectClientIPPolicy mirrors inspectClientIpPolicy.
	InspectClientIPPolicy(ctx context.Context, clientIP string, cacheOnly bool) (ClientIPPolicyDecision, error)
	// RecordClientIPPolicyHitAsync mirrors recordClientIpPolicyHitAsync:
	// fire-and-forget; the implementation owns error handling.
	RecordClientIPPolicyHit(policy BlacklistPolicy)
}

// UserRequestLimits mirrors the consumed surface of
// runtime/user-request-limit-counter.ts + user-request-limit-coordinator.ts
// (G13).
type UserRequestLimits interface {
	// Consume mirrors userRequestLimitCounter.consume.
	Consume(input UserRequestLimitConsumeInput) UserRequestLimitDecision
	// StartCoordinator mirrors startUserRequestLimitCoordinator().
	StartCoordinator()
}

// UserRequestLimitConsumeInput mirrors UserRequestLimitConsumeInput; settings
// come from the resolved runtime settings, overrides from the API key row.
type UserRequestLimitConsumeInput struct {
	SystemAccountID string
	Settings        gatewayruntimecache.GatewaySettings
	Overrides       *gatewayruntimecache.UserRequestLimits
	NowMs           *int64
}

// AuthenticatedModelsRateLimit mirrors
// runtime/authenticated-models-rate-limit.service.ts (G13).
type AuthenticatedModelsRateLimit interface {
	Consume(ctx context.Context, input AuthenticatedModelsRateLimitInput) (AuthenticatedModelsRateLimitDecision, error)
}

// AuthenticatedModelsRateLimitInput mirrors consumeAuthenticatedModelsRateLimit.
type AuthenticatedModelsRateLimitInput struct {
	APIKeyID string
	ClientIP string
}

// ClientStrategyContext is the frozen subset of
// OpenAIGatewayClientStrategyContext the preflight consumes; the full
// context stays with client-profiles (G18) via Opaque.
type ClientStrategyContext struct {
	ClientProfile              string
	DownstreamProtocol         string
	RequestClientCompatibility string
	// ClientSource carries the explicit client source (header-derived); nil
	// when absent.
	ClientSource *ClientSource
	// Opaque carries the full G18 context for downstream slices.
	Opaque any
}

// ClientSource mirrors the clientSource subset: the header-derived session
// identity, when present.
type ClientSource struct {
	SessionIdentity *SessionIdentity
}

// SessionIdentity mirrors GatewaySessionIdentity.
type SessionIdentity struct {
	SessionID       string
	ConversationKey string
}

// ClientStrategy mirrors the consumed surface of
// client-profiles/strategy.ts (G18).
type ClientStrategy interface {
	// ResolveOpenAIGatewayClientStrategy mirrors resolveOpenAIGatewayClientStrategy.
	Resolve(req *GatewayRequest, input ClientStrategyInput) ClientStrategyContext
	// AuditMetadata mirrors openAIGatewayClientStrategyAuditMetadata.
	AuditMetadata(strategy ClientStrategyContext) map[string]any
}

// ClientStrategyInput mirrors the resolution input.
type ClientStrategyInput struct {
	SystemAccountID string
	APIKeyID        string
	GroupID         string
	Endpoint        string
	ProviderCode    string
	ClientIP        string
}

// SessionIdentityResolver mirrors session-identity/index.ts (G14).
type SessionIdentityResolver interface {
	ResolveGatewaySessionIdentity(req *GatewayRequest, input SessionIdentityInput) SessionIdentity
}

// SessionIdentityInput mirrors the resolution input.
type SessionIdentityInput struct {
	ClientProfile   string
	SystemAccountID string
	APIKeyID        string
}

// SessionAffinityScope mirrors the session affinity scope.
type SessionAffinityScope struct {
	SystemAccountID           string
	APIKeyID                  string
	GroupID                   string
	RouteStrategyID           string
	ProviderProtocolProfileID string
}

// SessionAffinity mirrors runtime/session-affinity.service.ts (G14).
type SessionAffinity interface {
	// ResolveKeyFromClientSource mirrors
	// resolveOpenAIGatewaySessionAffinityKeyFromClientSource.
	ResolveKeyFromClientSource(clientSource *ClientSource, scope SessionAffinityScope) (string, bool)
	// ResolveKey mirrors resolveOpenAIGatewaySessionAffinityKey.
	ResolveKey(identity SessionIdentity, scope SessionAffinityScope) (string, bool)
}

// CodexBridgePreflight mirrors the consumed codex-responses preflight surface
// (G18): chat-bridge-state + compact-preflight + compaction contract.
type CodexBridgePreflight interface {
	// CompactionExpectedForRequest mirrors codexCompactionExpectedForRequest.
	CompactionExpectedForRequest(req *GatewayRequest) bool
	// ApplyContextStatePreflight mirrors applyCodexResponsesContextStatePreflight;
	// completed=true means the request finished inside the preflight.
	ApplyContextStatePreflight(ctx context.Context, input CodexContextStateInput) (completed bool, err error)
	// ApplyChatBridgeCompactPreflight mirrors
	// applyCodexResponsesChatBridgeCompactPreflight.
	ApplyChatBridgeCompactPreflight(ctx context.Context, input CodexCompactPreflightInput) (CodexCompactPreflightResult, error)
}

// CodexContextStateInput mirrors the context state preflight input.
type CodexContextStateInput struct {
	Req             *GatewayRequest
	Res             GatewayResponseWriter
	AuditCapture    AuditCaptureContext
	UsageContext    GatewayFailureUsageContext
	StartedAt       int64
	SystemAccountID string
	APIKeyID        string
	GroupID         string
	GroupAccess     gatewayruntimecache.GroupUsageAccessMetadata
	Signal          context.Context
}

// CodexCompactPreflightInput mirrors the compact preflight input.
type CodexCompactPreflightInput struct {
	Req                        *GatewayRequest
	Res                        GatewayResponseWriter
	AuditCapture               AuditCaptureContext
	UsageContext               GatewayFailureUsageContext
	StartedAt                  int64
	SystemAccountID            string
	APIKeyID                   string
	GroupID                    string
	GroupAccess                gatewayruntimecache.GroupUsageAccessMetadata
	RequestClientCompatibility string
	DispatchAccounts           []gatewayruntimecache.OpenAIAccountSecret
	ActiveGatewaySettings      gatewayruntimecache.GatewaySettings
	ClientIPAccountAvoidance   ClientIPAccountAvoidanceTracker
	ModelPriority              *gatewayrouting.GatewayAccountModelPriority
	RequestLane                string
	GroupSchedulingPolicy      *map[string]any
	RequestCoordination        CodexRequestCoordination
	OnDispatchedAccount        func(account gatewayruntimecache.OpenAIAccountSecret)
	Signal                     context.Context
}

// CodexRequestCoordination mirrors requestCoordination.
type CodexRequestCoordination struct {
	Scope                    string
	TimeoutPolicy            string
	ServerRetryBudget        *ServerRetryBudget
	GatewayRequestWallBudget *gatewayrouting.GatewayRequestWallBudget
	RouteCoordinationBudget  *gatewayrouting.RouteCoordinationBudget
	RequestAttemptTracker    *gatewayrouting.GatewayRequestAttemptTracker
}

// CodexCompactPreflightResult mirrors the compact preflight outcome.
type CodexCompactPreflightResult struct {
	// Completed mirrors outcome === 'completed'.
	Completed bool
	// Accounts mirrors the post-preflight dispatch accounts.
	Accounts []gatewayruntimecache.OpenAIAccountSecret
}

// ClientIPAccountAvoidanceTracker is the opaque handle created per request
// (runtime/client-ip-account-avoidance.service.ts, G13). The orchestration
// only forwards it.
type ClientIPAccountAvoidanceTracker interface{}

// AccountCandidate is the candidate account carrier of the preflight
// pipeline. Node uses OpenAIAccountSecret directly; the alias keeps the
// dispatch port signatures readable.
type AccountCandidate = gatewayruntimecache.OpenAIAccountSecret

// CandidateFilterInput mirrors filterOpenAIGatewayRequestCandidateAccounts's
// input; callbacks mirror the optional loaders the Node caller supplies.
type CandidateFilterInput struct {
	Req                  *GatewayRequest
	Res                  GatewayResponseWriter
	AuditCapture         AuditCaptureContext
	UsageContext         GatewayFailureUsageContext
	StartedAt            int64
	RawCandidates        []AccountCandidate
	ClientStrategy       ClientStrategyContext
	SystemAccountID      string
	APIKeyID             string
	GroupID              string
	ClientIP             string
	Endpoint             string
	BypassModelFilter    bool
	RequestModelOverride string
	// LoadModelAwareCandidateAccounts mirrors loadModelAwareCandidateAccounts.
	LoadModelAwareCandidateAccounts func(model, sourceEndpointFamily string) ([]AccountCandidate, error)
	// RecoverUnavailableCandidateAccounts mirrors
	// recoverUnavailableCandidateAccounts.
	RecoverUnavailableCandidateAccounts func() ([]AccountCandidate, error)
	// RouteCoordinator mirrors the routeCoordinator owner.
	RouteCoordinator gatewayrouting.GatewayRouteCoordinatorOwner
}

// CandidateFilterResult mirrors the filter outcome union.
type CandidateFilterResult struct {
	// Outcome is 'accounts' | 'fallback' | 'completed'.
	Outcome string
	// accounts variant
	Accounts      []AccountCandidate
	ModelPriority *gatewayrouting.GatewayAccountModelPriority
	// fallback variant
	Reason string
}

// Candidate pipeline outcomes.
const (
	CandidateOutcomeAccounts  = "accounts"
	CandidateOutcomeFallback  = "fallback"
	CandidateOutcomeCompleted = "completed"
)

// CandidatePipeline mirrors dispatch/candidate-filter.ts +
// dispatch/preparation.ts + dispatch/api-key-group-fallback-candidate.ts
// (G15).
type CandidatePipeline interface {
	// FilterCandidates mirrors filterOpenAIGatewayRequestCandidateAccounts.
	FilterCandidates(ctx context.Context, input CandidateFilterInput) (CandidateFilterResult, error)
	// PrepareDispatchAccounts mirrors prepareOpenAIGatewayDispatchAccounts.
	PrepareDispatchAccounts(ctx context.Context, input DispatchPreparationInput) (DispatchPreparationResult, error)
	// ResolveNextGroupFallbackCandidate mirrors
	// resolveNextApiKeyGroupFallbackCandidate; found=false mirrors undefined.
	ResolveNextGroupFallbackCandidate(ctx context.Context, input GroupFallbackCandidateInput) (GroupFallbackCandidate, bool, error)
}

// DispatchPreparationInput mirrors the preparation input.
type DispatchPreparationInput struct {
	Req                             *GatewayRequest
	Res                             GatewayResponseWriter
	AuditCapture                    AuditCaptureContext
	UsageContext                    GatewayFailureUsageContext
	StartedAt                       int64
	CandidateAccounts               []AccountCandidate
	ModelPriority                   *gatewayrouting.GatewayAccountModelPriority
	SessionAffinityKey              string
	GroupAccess                     gatewayruntimecache.GroupUsageAccessMetadata
	SystemAccountID                 string
	APIKeyID                        string
	GroupID                         string
	RouteStrategyID                 string
	NormalRouteSpeedFirstConfig     *NormalRouteSpeedFirstRuntimeConfig
	ClientIP                        string
	ClientStrategy                  ClientStrategyContext
	RequestLane                     string
	ServerRetryBudget               *ServerRetryBudget
	RouteCoordinationBudget         *gatewayrouting.RouteCoordinationBudget
	GatewayRequestWallBudget        *gatewayrouting.GatewayRequestWallBudget
	Signal                          context.Context
	IgnoreAccountRuntimeSuppression bool
	RouteCoordinator                gatewayrouting.GatewayRouteCoordinatorOwner
}

// DispatchPreparationResult mirrors DispatchPreparationResult.
type DispatchPreparationResult struct {
	// Outcome is 'accounts' | 'fallback' | 'completed'.
	Outcome string
	// accounts variant
	Accounts                                 []AccountCandidate
	HotQualityExplorationReservation         *HotQualityExplorationReservation
	SettleHotQualityExplorationAfterDispatch func(outcome string) error
	ReleaseClientIPConcurrency               func()
	NormalRouteLatencyDegradationApplied     bool
	CodexTurnAccountAvoidanceApplied         bool
	CodexTurnAvoidedAccountIDs               []string
	PrecheckHalfOpenEligible                 bool
	// fallback variant
	Reason string
}

// HotQualityExplorationReservation is the opaque reservation handle (G13).
type HotQualityExplorationReservation struct {
	AccountRuntimeKey string
}

// GroupFallbackCandidateInput mirrors ApiKeyGroupFallbackCandidateInput.
type GroupFallbackCandidateInput struct {
	Req                        *GatewayRequest
	Reason                     string
	APIKeyRecord               *gatewayruntimecache.GatewayAPIKeyRow
	SystemAccountID            string
	GroupID                    string
	RequestLane                string
	RequestClientCompatibility string
	// ExcludedAccountIDs mirrors excludedAccountIds: the request-level
	// exhausted account set the dispatch loop hands to switchToFallbackGroup
	// (routes.ts:568/625). Every candidate group window drops these accounts
	// before the capability/model/quota gates
	// (api-key-group-fallback-candidate.ts:79-84); nil keeps every account.
	// The preflight-time requestFallback stays nil — Node passes no
	// excludedAccountIds there (preflight.ts:1078), the set only exists on
	// the dispatch loop.
	ExcludedAccountIDs         map[string]struct{}
	RoutePlanSnapshot          gatewayrouting.RoutePlanSnapshot[string]
}

// GroupFallbackCandidate mirrors ApiKeyGroupFallbackCandidate.
type GroupFallbackCandidate struct {
	GroupID                    string
	Accounts                   []AccountCandidate
	ResponseInspectionPolicies []gatewayruntimecache.ResponseInspectionPolicySummary
	RoutePlanSnapshot          *gatewayrouting.RoutePlanSnapshot[string]
}

// ImagePermissionPreflight mirrors request/image-permission-preflight.ts.
type ImagePermissionPreflight interface {
	Apply(ctx context.Context, input ImagePermissionPreflightInput) (ImagePermissionPreflightResult, error)
}

// ImagePermissionPreflightInput mirrors the Node input.
type ImagePermissionPreflightInput struct {
	Req                              *GatewayRequest
	Res                              GatewayResponseWriter
	AuditCapture                     AuditCaptureContext
	UsageContext                     GatewayFailureUsageContext
	StartedAt                        int64
	APIKeyRecord                     *gatewayruntimecache.GatewayAPIKeyRow
	RequestLane                      string
	SystemAccountID                  string
	APIKeyID                         string
	GroupID                          string
	ClientIP                         string
	Endpoint                         string
	GatewayTextRawBodyLimitMegabytes *int64
	DeferForcedImageGenerationTool   bool
	Signal                           context.Context
}

// ImagePermissionPreflightResult mirrors the outcome union.
type ImagePermissionPreflightResult struct {
	// Completed mirrors outcome === 'completed'; otherwise continue.
	Completed   bool
	RequestLane string
}

// FailureResponseInput mirrors sendGatewayFailureResponse's input.
type FailureResponseInput struct {
	Req                          *GatewayRequest
	Res                          GatewayResponseWriter
	AuditCapture                 AuditCaptureContext
	UsageContext                 GatewayFailureUsageContext
	StartedAt                    int64
	StatusCode                   int
	ResponsePayload              GatewayErrorPayload
	Audit                        FailureAudit
	RecordUsage                  *bool
	UsageErrorMessage            string
	FailureAttribution           string
	FailureScope                 string
	PreserveUpstreamErrorMessage bool
}

// FailureAudit mirrors the audit bag of sendGatewayFailureResponse.
type FailureAudit struct {
	Outcome      string
	ErrorPhase   string
	ErrorCode    string
	ErrorMessage string
}

// ResponseSink mirrors the response/failure-response.ts +
// response/fixed-responses.ts functions the preflight calls (G16). The JSON
// error senders live in this package (responses.go); everything that records
// usage / audit or renders model lists goes through this sink.
type ResponseSink interface {
	// SendGatewayFailureResponse mirrors sendGatewayFailureResponse.
	SendGatewayFailureResponse(input FailureResponseInput)
	// FinalizeGatewayAuthFailureAudit mirrors finalizeGatewayAuthFailureAudit.
	FinalizeGatewayAuthFailureAudit(req *GatewayRequest, res GatewayResponseWriter, auditCapture AuditCaptureContext)
	// SendAuthenticatedModelsGatewayResponse mirrors
	// sendAuthenticatedModelsGatewayResponse.
	SendAuthenticatedModelsGatewayResponse(input ModelsResponseInput)
	// SendOpenAIModelsGatewayResponse mirrors sendOpenAIModelsGatewayResponse.
	SendOpenAIModelsGatewayResponse(input ModelsResponseInput)
	// SendAnthropicModelsGatewayResponse mirrors sendAnthropicModelsGatewayResponse.
	SendAnthropicModelsGatewayResponse(input ModelsResponseInput)
	// SendGeminiModelsGatewayResponse mirrors sendGeminiModelsGatewayResponse.
	SendGeminiModelsGatewayResponse(input ModelsResponseInput)
}

// ModelsResponseInput mirrors the models response input.
type ModelsResponseInput struct {
	Req           *GatewayRequest
	Res           GatewayResponseWriter
	AuditCapture  AuditCaptureContext
	UsageContext  GatewayFailureUsageContext
	ProviderCodes []string
	// Protocol mirrors modelsResponseKind for the authenticated sender;
	// empty means the openai sender variant applies.
	Protocol  string
	StartedAt int64
}

// RecoverableWait mirrors runtime/recoverable-unavailable-wait.ts (G11).
type RecoverableWait interface {
	// WaitForRecoverableUnavailableState mirrors waitForRecoverableUnavailableState;
	// the state payload stays opaque to the orchestration.
	WaitForRecoverableUnavailableState(ctx context.Context, input RecoverableWaitInput) error
}

// RecoverableWaitInput mirrors the wait input; Refresh reads through the
// caller-supplied loaders.
type RecoverableWaitInput struct {
	ScopeKey                 string
	Reason                   string
	Refresh                  func(ctx context.Context) error
	IsReady                  func(ctx context.Context) bool
	NextRetryAfterMs         func(ctx context.Context) (int64, bool)
	AuditCapture             AuditCaptureContext
	MaxWaitMs                int64
	RequestStartedAtMs       int64
	DeadlineAtMs             int64
	RouteCoordinationBudget  *gatewayrouting.RouteCoordinationBudget
	GatewayRequestWallBudget *gatewayrouting.GatewayRequestWallBudget
	Signal                   context.Context
}

// AuditSettings mirrors readAuditLogSettings().enabled.
type AuditSettings interface {
	AuditLogEnabled() bool
}

// AuditDispatcher mirrors dispatchAuditLogToGo (audit-logs go input service).
type AuditDispatcher interface {
	Dispatch(input DispatchedAuditLogInput)
}

// DispatchedAuditLogInput mirrors the AuditLogInput fields the dropped
// capture fills.
type DispatchedAuditLogInput struct {
	ID              string
	LifecycleStatus string
	TraceID         string
	TrafficSource   string
	AuditOutcome    string
	Success         bool
	Method          string
	Path            string
	QueryString     string
	ClientIP        string
	UserAgent       string
	FinalStatusCode int
	ErrorPhase      string
	ErrorCode       string
	ErrorMessage    string
	SampleBucket    int
	SampleReason    string
	CaptureStatus   string
	StartedAt       string
	EndedAt         string
}

// NormalRouteSpeedFirstRuntimeConfig mirrors NormalRouteSpeedFirstRuntimeConfig.
type NormalRouteSpeedFirstRuntimeConfig struct {
	SchedulingPreference string
	FirstByteDeadlineMs  *int64
	// Raw mirrors the stored speedFirstConfig object (opaque to G05; typed
	// decoding belongs to the latency-degradation slice).
	Raw map[string]any
}

// NormalRouteFirstByteRuntimeConfig mirrors NormalRouteFirstByteRuntimeConfig.
type NormalRouteFirstByteRuntimeConfig struct {
	SchedulingPreference string
	FirstByteDeadlineMs  *int64
}
