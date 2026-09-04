package gatewaydispatch

import (
	"context"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Collaborator ports of the dispatch engine. Each port mirrors the consumed
// surface of the corresponding Node service; the doc comment names the
// source file. Concrete implementations live with their owning slices and
// are wired in G20; tests supply fakes.

// AccountCandidate mirrors the dispatch account carrier (Node
// OpenAIAccountSecret directly).
type AccountCandidate = gatewayruntimecache.OpenAIAccountSecret

// ModelPriority mirrors GatewayAccountModelPriority with rank lookup.
type ModelPriority = gatewayrouting.GatewayAccountModelPriority

// ---------------------------------------------------------------------------
// Provider drivers (providers/drivers/registry.ts)
// ---------------------------------------------------------------------------

// PreparedRequestParts mirrors PreparedUpstreamRequestParts.
type PreparedRequestParts struct {
	Headers                 http.Header
	Body                    []byte
	EffectiveServiceTier    string
	EffectiveReasoningEffort string
}

// ProviderDriver mirrors the consumed registry surface:
// prepareGatewayUpstreamAccount / buildGatewayUpstreamUrlsForAccount /
// buildGatewayUpstreamRequestParts / accountSupportsGatewayRequest /
// gatewayRequestCapabilityMismatchReason /
// transformGatewayUpstreamResponseForAccount.
type ProviderDriver interface {	// PrepareGatewayUpstreamAccount mirrors prepareGatewayUpstreamAccount.
	PrepareGatewayUpstreamAccount(ctx context.Context, account AccountCandidate) (AccountCandidate, error)
	// BuildGatewayUpstreamURLsForAccount mirrors buildGatewayUpstreamUrlsForAccount.
	BuildGatewayUpstreamURLsForAccount(ctx context.Context, account AccountCandidate, req *gatewaypreauth.GatewayRequest) ([]string, error)
	// BuildGatewayUpstreamRequestParts mirrors buildGatewayUpstreamRequestParts.
	BuildGatewayUpstreamRequestParts(ctx context.Context, req *gatewaypreauth.GatewayRequest, account AccountCandidate, identity UsageIdentity, requestClientCompatibility string) (PreparedRequestParts, error)
	// AccountSupportsGatewayRequest mirrors accountSupportsGatewayRequest.
	AccountSupportsGatewayRequest(req *gatewaypreauth.GatewayRequest, account AccountCandidate, requestClientCompatibility string) bool
	// GatewayRequestCapabilityMismatchReason mirrors
	// gatewayRequestCapabilityMismatchReason.
	GatewayRequestCapabilityMismatchReason(req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate) string
}

// UsageIdentity mirrors the identity triple passed to the driver.
type UsageIdentity struct {
	SystemAccountID string
	APIKeyID        string
	GroupID         string
}

// ---------------------------------------------------------------------------
// Usage records (usage/records.ts, G17)
// ---------------------------------------------------------------------------

// FailedAttemptRecord mirrors recordFailedUpstreamAttempt's input.
type FailedAttemptRecord struct {
	UpstreamURL              string
	StartedAt                int64
	StatusCode               int
	HasStatusCode            bool
	BodyText                 string
	ErrorMessage             string
	FailureAttribution       string
	InterpretUpstreamSemantics *bool
}

// UsageAttemptRecorder mirrors the consumed recordFailedUpstreamAttempt
// surface (usage/records.ts, G17).
type UsageAttemptRecorder interface {
	RecordFailedUpstreamAttempt(ctx context.Context, req *gatewaypreauth.GatewayRequest, usageContext gatewaypreauth.GatewayFailureUsageContext, account AccountCandidate, record FailedAttemptRecord) error
}

// AttemptAuditSink mirrors the attempt-level audit surface
// (audit/capture.service.ts startAttempt / completeAttempt /
// recordFailedDispatchAttempt, G17). Nil sinks are no-ops.
type AttemptAuditSink interface {
	StartAttempt(input StartAttemptInput) string
	CompleteAttempt(attemptID string, input CompleteAttemptInput)
	RecordFailedDispatchAttempt(input FailedDispatchAttemptInput)
}

// AuditCapture bundles the frozen G05 capture context with the dispatch
// attempt-level surface. Methods delegate to the frozen context first; when
// the frozen context itself implements AttemptAuditSink the sink override is
// unnecessary.
type AuditCapture struct {
	Context gatewaypreauth.AuditCaptureContext
	Sink    AttemptAuditSink
}

// Nil reports whether no capture is wired.
func (c AuditCapture) Nil() bool { return c.Context == nil && c.Sink == nil }

// BindContext mirrors bindContext.
func (c AuditCapture) BindContext(context gatewaypreauth.AuditGatewayContext) {
	if c.Context != nil {
		c.Context.BindContext(context)
	}
}

// AddGatewayMetadata mirrors addGatewayMetadata.
func (c AuditCapture) AddGatewayMetadata(label string, metadata map[string]any) {
	if c.Context != nil {
		c.Context.AddGatewayMetadata(label, metadata)
	}
}

// Finalize mirrors finalize.
func (c AuditCapture) Finalize(input gatewaypreauth.AuditFinalizeInput) {
	if c.Context != nil {
		c.Context.Finalize(input)
	}
}

// StartAttempt mirrors startAttempt.
func (c AuditCapture) StartAttempt(input StartAttemptInput) string {
	if sink, ok := c.Context.(AttemptAuditSink); ok && sink != nil {
		return sink.StartAttempt(input)
	}
	if c.Sink != nil {
		return c.Sink.StartAttempt(input)
	}
	return ""
}

// CompleteAttempt mirrors completeAttempt.
func (c AuditCapture) CompleteAttempt(attemptID string, input CompleteAttemptInput) {
	if sink, ok := c.Context.(AttemptAuditSink); ok && sink != nil {
		sink.CompleteAttempt(attemptID, input)
		return
	}
	if c.Sink != nil {
		c.Sink.CompleteAttempt(attemptID, input)
	}
}

// RecordFailedDispatchAttempt mirrors recordFailedDispatchAttempt.
func (c AuditCapture) RecordFailedDispatchAttempt(input FailedDispatchAttemptInput) {
	if sink, ok := c.Context.(AttemptAuditSink); ok && sink != nil {
		sink.RecordFailedDispatchAttempt(input)
		return
	}
	if c.Sink != nil {
		c.Sink.RecordFailedDispatchAttempt(input)
	}
}

// StartAttemptInput mirrors startAttempt's input.
type StartAttemptInput struct {
	Account                  AccountCandidate
	AttemptIndex             int
	UpstreamURL              string
	Method                   string
	Headers                  map[string]string
	Body                     []byte
	RequestForModelAccounting *gatewaypreauth.GatewayRequest
}

// CompleteAttemptInput mirrors completeAttempt's input.
type CompleteAttemptInput struct {
	Success      bool
	ErrorPhase   string
	ErrorCode    string
	ErrorMessage string
}

// FailedDispatchAttemptInput mirrors recordFailedDispatchAttempt's input.
type FailedDispatchAttemptInput struct {
	Account                  AccountCandidate
	AttemptIndex             int
	UpstreamURL              string
	Method                   string
	StartedAtMs              int64
	ErrorPhase               string
	ErrorCode                string
	ErrorMessage             string
	RequestForModelAccounting *gatewaypreauth.GatewayRequest
}

// ---------------------------------------------------------------------------
// Failure dispatch (response/failure-dispatch.ts, G16)
// ---------------------------------------------------------------------------

// FailureDispatcher mirrors the consumed failure-dispatch surface.
type FailureDispatcher interface {
	// HandleFailedUpstreamResponse mirrors handleFailedUpstreamResponse.
	HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error)
	// HandleUpstreamRequestError mirrors handleUpstreamRequestError.
	HandleUpstreamRequestError(ctx context.Context, input UpstreamRequestErrorInput) (UpstreamRequestErrorResult, error)
	// IsOpaqueUpstreamFailoverAllowed mirrors isOpaqueUpstreamFailoverAllowed.
	IsOpaqueUpstreamFailoverAllowed(req *gatewaypreauth.GatewayRequest) bool
}

// FailedUpstreamResponseInput mirrors the handleFailedUpstreamResponse input.
type FailedUpstreamResponseInput struct {
	Req                        *gatewaypreauth.GatewayRequest
	RequestLane                string
	UsageContext               gatewaypreauth.GatewayFailureUsageContext
	AuditCapture               AuditCapture
	AuditAttemptID             string
	Account                    AccountCandidate
	UpstreamURL                string
	Response                   *GatewayUpstreamResponse
	RequestBody                []byte
	Settings                   gatewayruntimecache.GatewaySettings
	AttemptStartedAt           int64
	AttemptIndex               int
	AuditAttemptIndex          int
	SessionAffinityKey         string
	LastAttempt                *UpstreamAttempt
	RequestClientCompatibility string
	ClientIPAccountAvoidance   gatewaypreauth.ClientIPAccountAvoidanceTracker
	AccountStateMutationEnabled bool
	AutomaticAccountStateMutationEnabled bool
	DeferAutomaticSameAccountKeyRotation bool
}

// Failed response action union mirrors the Node action union.
const (
	FailedResponseActionReturnResponse                = "return_response"
	FailedResponseActionSkipAccount                   = "skip_account"
	FailedResponseActionRetryWithCompatibilityRecovery = "retry_with_compatibility_recovery"
)

// FailureKind values mirror failureKind.
const (
	FailureKindExplicitPolicy = "explicit_policy"
)

// PendingAccountApiKeyFailure mirrors PendingAccountApiKeyFailure.
type PendingAccountApiKeyFailure struct {
	Account          AccountCandidate
	Status           string // 'temporary_unavailable' | ...
	StatusCode       int
	ErrorCode        string
	ErrorMessage     string
	MutationContext  map[string]any
	ObservationEpoch string
	CooldownUntil    *string
}

// FailedUpstreamResponseResult mirrors the result union.
type FailedUpstreamResponseResult struct {
	// Action is 'return_response' | 'skip_account' |
	// 'retry_with_compatibility_recovery'.
	Action        string
	Response      *GatewayUpstreamResponse
	Recovery      CompatibilityRecovery
	LastAttempt   *UpstreamAttempt
	FailureKind   string
	TryNextApiKeyForRequest bool
	KeyScopedFailure         bool
	PendingApiKeyFailure     *PendingAccountApiKeyFailure
}

// CompatibilityRecovery mirrors recovery.
type CompatibilityRecovery struct {
	Body            []byte
	SemanticRetryID string
}

// UpstreamRequestErrorInput mirrors the handleUpstreamRequestError input.
type UpstreamRequestErrorInput struct {
	Req                         *gatewaypreauth.GatewayRequest
	UsageContext                gatewaypreauth.GatewayFailureUsageContext
	AuditCapture                AuditCapture
	AuditAttemptID              string
	Account                     AccountCandidate
	UpstreamURL                 string
	AttemptStartedAt            int64
	AttemptIndex                int
	AuditAttemptIndex           int
	Settings                    gatewayruntimecache.GatewaySettings
	SessionAffinityKey          string
	LastAttempt                 *UpstreamAttempt
	FailedProxyDispatchKeys     map[string]string
	Error                       error
	ClientIPAccountAvoidance    gatewaypreauth.ClientIPAccountAvoidanceTracker
	AccountStateMutationEnabled bool
}

// UpstreamRequestErrorResult mirrors the result union.
type UpstreamRequestErrorResult struct {
	Action           string // 'skip_account' | ''
	LastAttempt      *UpstreamAttempt
	KeyScopedFailure bool
}

// ---------------------------------------------------------------------------
// Runtime suppression + degradation (runtime/account-side-effects.service.ts,
// runtime/local-suppression-preflight.ts, G11/G13)
// ---------------------------------------------------------------------------

// HalfOpenLease mirrors GatewayAccountHalfOpenLease.
type HalfOpenLease interface {
	Generation() *int64
	RuntimeKey() string
	Release() (bool, error)
	CompleteSuccess() (bool, error)
}

// SuppressionFilterResult mirrors LocalAccountSuppressionFilterResult.
type SuppressionFilterResult struct {
	Accounts                              []AccountCandidate
	SuppressedCount                       int
	AllSuppressed                         bool
	SuppressedAccountIDs                  []string
	NextRetryAfterMs                      *int64
	PrecheckSuppressedAccountIDs          []string
	PrecheckSuppressedRuntimeScopes       []PrecheckSuppressedRuntimeScope
	AcquiredHalfOpenLeases                []HalfOpenLease
	ConfiguredPolicySuppressedAccountIDs  []string
}

// PrecheckSuppressedRuntimeScope mirrors the scope entries.
type PrecheckSuppressedRuntimeScope struct {
	RuntimeKey string
	Generation int64
}

// SuppressionFilterOptions mirrors the per-call options.
type SuppressionFilterOptions struct {
	AcquireHalfOpenLease         bool
	AcquirePrecheckHalfOpenLease bool
	PrecheckHalfOpenGroupKey     string
}

// SuppressionPort mirrors filterGatewayAccountRuntimeSuppressionsAsync +
// resolveLocalSuppressionFilter.
type SuppressionPort interface {
	// FilterAsync mirrors filterGatewayAccountRuntimeSuppressionsAsync.
	FilterAsync(ctx context.Context, accounts []AccountCandidate, options SuppressionFilterOptions) (SuppressionFilterResult, error)
	// ResolveLocalSuppressionFilter mirrors resolveLocalSuppressionFilter.
	// completed=true means the request finished inside the resolver.
	ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (result *SuppressionFilterResult, completed bool, err error)
}

// LocalSuppressionPreflightInput mirrors resolveLocalSuppressionFilter's
// input.
type LocalSuppressionPreflightInput struct {
	Req                        *gatewaypreauth.GatewayRequest
	UsageContext               gatewaypreauth.GatewayFailureUsageContext
	AuditCapture               AuditCapture
	StartedAt                  int64
	Accounts                   []AccountCandidate
	SystemAccountID            string
	APIKeyID                   string
	GroupID                    string
	ServerRetryBudget          *gatewaypreauth.ServerRetryBudget
	RouteCoordinationBudget    *gatewayrouting.RouteCoordinationBudget
	GatewayRequestWallBudget   *gatewayrouting.GatewayRequestWallBudget
	RouteCoordinator           gatewayrouting.GatewayRouteCoordinatorOwner
	Signal                     context.Context
}

// DegradationOrder mirrors the orderGatewayAccountsByRuntimeDegradation
// result.
type DegradationOrder struct {
	Accounts           []AccountCandidate
	Applied            bool
	DegradedCount      int
	DegradedAccountIDs []string
	BypassedAllDegraded bool
}

// DegradationPort mirrors orderGatewayAccountsByRuntimeDegradation.
type DegradationPort interface {
	OrderGatewayAccountsByRuntimeDegradation(accounts []AccountCandidate, modelRankByAccountID map[string]int) DegradationOrder
	// OrderWithLaneAsync mirrors the engine prologue:
	// orderGatewayAccountsByRuntimeDegradation(await
	// orderAccountsForRequestLaneAsync(...)).
	OrderWithLaneAsync(ctx context.Context, accounts []AccountCandidate, requestLane string, policy *gatewayruntimecache.GroupSchedulingPolicy, priority *ModelPriority) (DegradationOrder, error)
	// OrderSync mirrors the degradation-only reorder used by the wait cycle.
	OrderSync(accounts []AccountCandidate, priority *ModelPriority) DegradationOrder
}

// ---------------------------------------------------------------------------
// Latency degradation (runtime/normal-route-latency-degradation.service.ts)
// ---------------------------------------------------------------------------

// LatencyDegradationOrder mirrors the ordering result.
type LatencyDegradationOrder struct {
	Accounts            []AccountCandidate
	Applied             bool
	DegradedAccountIDs  []string
	BypassedAllDegraded bool
}

// LatencyDegradationPort mirrors
// orderGatewayAccountsByNormalRouteLatencyDegradationAsync.
type LatencyDegradationPort interface {
	OrderAsync(ctx context.Context, accounts []AccountCandidate, scope *LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, modelPriority *ModelPriority) (LatencyDegradationOrder, error)
}

// LatencyScopeInput mirrors normalRouteLatencyDegradationScope.
type LatencyScopeInput struct {
	SystemAccountID string
	RouteStrategyID string
	GroupID         string
}

// ---------------------------------------------------------------------------
// Proxy health (runtime/proxy-health.service.ts)
// ---------------------------------------------------------------------------

// ProxyHealthOrder mirrors the ordering result.
type ProxyHealthOrder struct {
	Accounts            []AccountCandidate
	Applied             bool
	AvoidedBucketKeys   []string
	AvoidedProxyKeys    []string
	AvoidedAccountIDs   []string
	HalfOpenBucketKeys  []string
	HalfOpenAccountIDs  []string
	BypassedAllAvoided  bool
}

// ProxyHealthPort mirrors the consumed proxy-health surface.
type ProxyHealthPort interface {
	// OrderAsync mirrors orderGatewayAccountsByUpstreamBucketHealthAsync.
	OrderAsync(ctx context.Context, accounts []AccountCandidate, modelPriority *ModelPriority) (ProxyHealthOrder, error)
	// RecordFailureAsync mirrors recordGatewayProxyFailureAsync.
	RecordFailureAsync(ctx context.Context, account AccountCandidate, message string) error
}

// ---------------------------------------------------------------------------
// Client IP / client source avoidance
// ---------------------------------------------------------------------------

// AvoidanceOrder is the shared shape of the avoidance ordering results.
type AvoidanceOrder struct {
	Accounts           []AccountCandidate
	Applied            bool
	AvoidedAccountIDs  []string
	BypassedAllAvoided bool
	FailureCount       int
	ThresholdReached   bool
}

// ClientIPAvoidancePort mirrors
// runtime/client-ip-account-avoidance.service.ts
// orderOpenAIAccountsByClientIpAccountAvoidanceAsync.
type ClientIPAvoidancePort interface {
	OrderAsync(ctx context.Context, accounts []AccountCandidate, scope ClientIPAvoidanceScope, modelPriority *ModelPriority) (AvoidanceOrder, error)
}

// ClientIPAvoidanceScope mirrors the scope input.
type ClientIPAvoidanceScope struct {
	SystemAccountID string
	APIKeyID        string
	GroupID         string
	ClientIP        string
}

// ClientSourceAvoidancePort mirrors
// client-profiles/client-source-avoidance.service.ts
// orderOpenAIAccountsByClientSourceAvoidanceAsync.
type ClientSourceAvoidancePort interface {
	OrderAsync(ctx context.Context, accounts []AccountCandidate, clientStrategy gatewaypreauth.ClientStrategyContext, modelPriority *ModelPriority) (AvoidanceOrder, error)
}

// ---------------------------------------------------------------------------
// Hot quality (runtime/hot-quality-runtime.service.ts +
// hot-quality-attempt-lifecycle.ts, G12)
// ---------------------------------------------------------------------------

// HotQualityOrder mirrors the ordering result.
type HotQualityOrder struct {
	Accounts                       []AccountCandidate
	DispatchIntent                 string
	SelectedAccountID              string
	ExplorationStatus              string
	QualityReorderedTierKeys       []string
	LatencyDegradedOverrideApplied bool
	ExplorationReservation         *HotQualityReservation
	SettleExplorationAfterDispatch func(ctx context.Context, outcome string) error
}

// HotQualityReservation mirrors GatewayHotQualityExplorationReservation
// (the G05 frozen struct).
type HotQualityReservation = gatewaypreauth.HotQualityExplorationReservation

// HotQualityPort mirrors orderGatewayAccountsByHotQualityAsync.
type HotQualityPort interface {
	OrderAsync(ctx context.Context, input HotQualityOrderInput) (HotQualityOrder, error)
}

// HotQualityOrderInput mirrors the ordering input.
type HotQualityOrderInput struct {
	Accounts                     []AccountCandidate
	ModelPriority                *ModelPriority
	Mode                         string // 'cost_first' | 'speed_first'
	SystemAccountID              string
	RouteStrategyID              string
	GroupID                      string
	RequestLane                  string
	Model                        string
	RequestID                    string
	LatencyDegradedAccountIDs    map[string]struct{}
	EligibleFirstPrimaryDispatch bool
}

// Hot-quality modes.
const (
	HotQualityModeCostFirst  = "cost_first"
	HotQualityModeSpeedFirst = "speed_first"
)

// ---------------------------------------------------------------------------
// Session affinity (runtime/session-affinity.service.ts, G14)
// ---------------------------------------------------------------------------

// AffinityScope mirrors the affinity coordination scope.
type AffinityScope struct {
	SystemAccountID string
	APIKeyID        string
	GroupID         string
}

// SessionAffinityPort mirrors the consumed session-affinity surface.
type SessionAffinityPort interface {
	// OrderAsync mirrors orderOpenAIAccountsBySessionAffinityAsync.
	OrderAsync(ctx context.Context, accounts []AccountCandidate, sessionAffinityKey string, options AffinityOrderingOptions) ([]AccountCandidate, error)
	// ClaimAsync mirrors claimOpenAIAccountForSessionAsync.
	ClaimAsync(ctx context.Context, sessionAffinityKey, proposedAccountID string, scope AffinityScope) (string, bool)
	// RememberAsync mirrors rememberOpenAIAccountForSessionAsync.
	RememberAsync(ctx context.Context, sessionAffinityKey, accountID string, scope AffinityScope)
	// ForgetAsync mirrors forgetOpenAIAccountForSessionAsync.
	ForgetAsync(ctx context.Context, sessionAffinityKey, accountID string) error
	// AreHighConcurrencyAccountsBusyForLaneAsync mirrors
	// areOpenAIHighConcurrencyAccountsBusyForLaneAsync.
	AreHighConcurrencyAccountsBusyForLaneAsync(ctx context.Context, accounts []AccountCandidate, options HighConcurrencyBusyOptions) (bool, error)
}

// AffinityOrderingOptions mirrors OpenAIAccountDispatchOrderingOptions.
type AffinityOrderingOptions struct {
	GroupType             string
	SchedulingPolicy      *gatewayruntimecache.GroupSchedulingPolicy
	ModelPriority         *ModelPriority
	TrafficMigrationScope *AffinityScope
}

// HighConcurrencyBusyOptions mirrors the options with requestLane.
type HighConcurrencyBusyOptions struct {
	AffinityOrderingOptions
	RequestLane string
}

// ---------------------------------------------------------------------------
// High-concurrency queue + client-ip concurrency
// ---------------------------------------------------------------------------

// QueueWaitResult mirrors waitForHighConcurrencyGroupCapacity's result.
type QueueWaitResult struct {
	Ready      bool
	Reason     string // 'timeout' | ''
	WaitedMs   int64
	QueueSize  int
}

// HighConcurrencyWaiter mirrors runtime/high-concurrency-queue.service.ts.
type HighConcurrencyWaiter interface {
	WaitForCapacity(ctx context.Context, input HighConcurrencyWaitInput) (QueueWaitResult, error)
}

// HighConcurrencyWaitInput mirrors the wait input.
type HighConcurrencyWaitInput struct {
	SystemAccountID           string
	GroupID                   string
	APIKeyID                  string
	AccountIDs                []string
	AccountConcurrencyLimits  map[string]int
	Lane                      string
	Policy                    *gatewayruntimecache.GroupSchedulingPolicy
	MaxWaitMs                 int64
}

// ClientIPConcurrencyDecision mirrors ClientIpConcurrencyDecision.
type ClientIPConcurrencyDecision struct {
	Enabled                bool
	Acquired               bool
	Reason                 string
	Current                int
	Limit                  int
	WaitedMs               int64
	Queued                 bool
	QueueSizeBeforeAcquire int
	QueueSize              int
	// Release mirrors decision.release (nil when not acquired).
	Release func()
}

// ClientIPConcurrencyAcquirer mirrors
// runtime/client-ip-concurrency.service.ts acquireHighConcurrencyClientIpSlot.
type ClientIPConcurrencyAcquirer interface {
	Acquire(ctx context.Context, input ClientIPConcurrencyInput) (ClientIPConcurrencyDecision, error)
}

// ClientIPConcurrencyInput mirrors the acquire input.
type ClientIPConcurrencyInput struct {
	SystemAccountID string
	GroupID         string
	APIKeyID        string
	ClientIP        string
	Policy          *gatewayruntimecache.GroupSchedulingPolicy
	Signal          context.Context
}

// ---------------------------------------------------------------------------
// Quota + concurrency + runtime cache + locks
// ---------------------------------------------------------------------------

// QuotaDecision mirrors the per-account quota decision.
type QuotaDecision struct {
	Allowed           bool
	RetryAfterSeconds *int64
}

// AuthorizationQuotaChecker mirrors
// quota/authorization-quota.service.ts checkGatewayAuthorizationQuotaBatchAsync.
type AuthorizationQuotaChecker interface {
	CheckBatchAsync(ctx context.Context, groupAccess gatewayruntimecache.GroupUsageAccessMetadata, accounts []AccountCandidate) (map[string]QuotaDecision, error)
}

// ConcurrencySlot mirrors AccountConcurrencySlot.
type ConcurrencySlot struct {
	Acquired   bool
	Current    int
	Limit      int
	Lane       string
	LaneCurrent int
	LaneLimit  int
	// Release mirrors slot.release.
	Release func()
	// MarkFirstOutput mirrors slot.markFirstOutput (image-lane probe
	// accounting); nil-safe.
	MarkFirstOutput func()
}

// AccountConcurrencyAcquireOptions mirrors AccountConcurrencyAcquireOptions.
type AccountConcurrencyAcquireOptions struct {
	Lane     string
	LaneLimit *int
}

// AccountConcurrencyStore mirrors shared/account-concurrency.ts consumed
// surface.
type AccountConcurrencyStore interface {
	// LoadCurrentAsync mirrors loadAccountCurrentConcurrencyByIdsAsync(ids).
	LoadCurrentAsync(ctx context.Context, accountIDs []string) (map[string]int, error)
	// LoadCurrentByLaneAsync mirrors loadAccountCurrentConcurrencyByIdsAsync(ids, lane).
	LoadCurrentByLaneAsync(ctx context.Context, accountIDs []string, lane string) (map[string]int, error)
	// TryAcquireAsync mirrors tryAcquireAccountConcurrencyAsync.
	TryAcquireAsync(ctx context.Context, accountID string, concurrencyLimit int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error)
}

// RuntimeCachePort mirrors the runtime-cache loaders the pipeline consumes
// (runtime/runtime-cache.service.ts).
type RuntimeCachePort interface {
	// ListCachedOpenAIAccountsForGroupAsync mirrors
	// listCachedOpenAIAccountsForGroupAsync.
	ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options CachedAccountsOptions) ([]AccountCandidate, error)
	// ResolveCachedGroupUsageAccessMetadataAsync mirrors
	// resolveCachedGroupUsageAccessMetadataAsync.
	ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (gatewayruntimecache.GroupUsageAccessMetadata, bool, error)
	// LoadApiKeyTransientStatesForDispatch mirrors
	// loadGatewayAccountApiKeyTransientStatesForDispatch.
	LoadApiKeyTransientStatesForDispatch(ctx context.Context, accountID string, fingerprints []string) ([]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, error)
}

// CachedAccountsOptions mirrors the loader options.
type CachedAccountsOptions struct {
	RequestedModel           string
	RequestedEndpointFamily  string
}

// AccountLockObservation mirrors AccountLockObservation.
type AccountLockObservation struct {
	Generation int64
	IncidentID string
	LeaseID    *string
}

// AccountLockStateView mirrors the lock state row the dispatch reads.
type AccountLockStateView struct {
	Generation int64
	IncidentID string
	BlocksCrossAccount bool
}

// LockLeaseAcquire mirrors acquireAccountLockRetryLeaseAsync's result.
type LockLeaseAcquire struct {
	Allowed bool
	LeaseID string
	WaitMs  int64
}

// AccountLocks mirrors storage/account-lock.repository.ts consumed surface.
type AccountLocks interface {
	FindStateAsync(ctx context.Context, accountID string) (*AccountLockStateView, error)
	AcquireRetryLeaseAsync(ctx context.Context, accountID string, configuredDelayMs int64) (LockLeaseAcquire, error)
	ConsumeRetryLeaseAsync(ctx context.Context, accountID, leaseID string) (bool, error)
	ReleaseRetryLeaseAsync(ctx context.Context, input ReleaseRetryLeaseInput) (bool, error)
	AbandonRetryReservationAsync(ctx context.Context, lease AccountLockRetryLease) error
	RecordFailureAsync(ctx context.Context, accountID, reason string, observation *AccountLockObservation) error
	SettleDeadlineAsync(ctx context.Context, accountID string, nowMs int64, observation *AccountLockObservation) error
	ListStatesAsync(ctx context.Context, accountIDs []string) (map[string]AccountLockStateView, error)
}

// AccountLockRetryLease mirrors { accountId, leaseId }.
type AccountLockRetryLease struct {
	AccountID string
	LeaseID   string
}

// ReleaseRetryLeaseInput mirrors releaseAccountLockRetryLeaseAsync's input.
type ReleaseRetryLeaseInput struct {
	AccountID       string
	LeaseID         string
	GlobalDelayMs   int64
	ScheduleNextRetry bool
}

// accountLockBlocksCrossAccount mirrors accountLockBlocksCrossAccount(state).
func accountLockBlocksCrossAccount(state AccountLockStateView) bool {
	return state.BlocksCrossAccount
}

// ---------------------------------------------------------------------------
// Recoverable wait (runtime/recoverable-unavailable-wait.ts, G11)
// ---------------------------------------------------------------------------

// RecoverableSuppressionWaiter mirrors waitForRecoverableUnavailableState
// specialized to the suppression filter state.
type RecoverableSuppressionWaiter interface {
	WaitForState(ctx context.Context, input SuppressionWaitInput) (SuppressionFilterResult, error)
}

// SuppressionWaitInput mirrors the wait input.
type SuppressionWaitInput struct {
	ScopeKey             string
	Reason               string
	Refresh              func(ctx context.Context) (SuppressionFilterResult, error)
	IsReady              func(state SuppressionFilterResult) bool
	NextRetryAfterMs     func(state SuppressionFilterResult) *int64
	AuditCapture         AuditCapture
	MaxWaitMs            int64
	RequestStartedAtMs   int64
	DeadlineAtMs         int64
	RouteCoordinationBudget *gatewayrouting.RouteCoordinationBudget
	GatewayRequestWallBudget *gatewayrouting.GatewayRequestWallBudget
	Signal               context.Context
}

// ---------------------------------------------------------------------------
// Codex bridge (codex-responses/chat-bridge-state.ts, G18)
// ---------------------------------------------------------------------------

// CodexBridgeState mirrors getCodexResponsesContextState's consumed fields.
type CodexBridgeState struct {
	PreviousResponseID string
}

// CodexBridgeCompletionHandler mirrors the per-request completion handler.
type CodexBridgeCompletionHandler interface{}

// CodexBridgePort mirrors the consumed chat-bridge-state surface.
type CodexBridgePort interface {
	// GetContextState mirrors getCodexResponsesContextState.
	GetContextState(req *gatewaypreauth.GatewayRequest) *CodexBridgeState
	// CompletionHandlerForRequest mirrors
	// codexResponsesChatBridgeCompletionHandlerForRequest (nil = none).
	CompletionHandlerForRequest(req *gatewaypreauth.GatewayRequest, account AccountCandidate) CodexBridgeCompletionHandler
	// PrepareContextForAccount mirrors prepareCodexResponsesContextForAccount.
	PrepareContextForAccount(req *gatewaypreauth.GatewayRequest, account AccountCandidate) error
}

// ---------------------------------------------------------------------------
// Account state mutations (runtime/account-effects +
// account-side-effects + account-api-key-effects, G13)
// ---------------------------------------------------------------------------

// AccountStateMutations mirrors the consumed account state mutation surface.
type AccountStateMutations interface {
	// SuppressLocally mirrors suppressGatewayAccountLocally.
	SuppressLocally(account AccountCandidate, settings gatewayruntimecache.GatewaySettings, message string) LocalSuppression
	// RecordFailureForPrecheck mirrors recordGatewayAccountFailureForPrecheck.
	RecordFailureForPrecheck(ctx context.Context, account AccountCandidate, settings gatewayruntimecache.GatewaySettings, input PrecheckFailureInput)
	// ApplyErrorHandlingWithCacheInvalidation mirrors
	// applyAccountErrorHandlingWithCacheInvalidation.
	ApplyErrorHandlingWithCacheInvalidation(ctx context.Context, account AccountCandidate, input AccountErrorInput) error
	// MarkTemporaryUnavailableWithCacheInvalidation mirrors
	// markGatewayAccountTemporaryUnavailableWithCacheInvalidation.
	MarkTemporaryUnavailableWithCacheInvalidation(ctx context.Context, account AccountCandidate, message, reason string) (bool, error)
}

// LocalSuppression mirrors the suppression result.
type LocalSuppression struct {
	Action  string // 'precheck_required' | ''
	DelayMs int64
}

// PrecheckFailureInput mirrors GatewayAccountFailurePrecheckInput.
type PrecheckFailureInput struct {
	SystemAccountID         string
	GroupID                 string
	APIKeyID                string
	ClientIP                string
	Endpoint                string
	Reason                  string
	ForcePrecheck           bool
	LocalSuppressionDelayMs int64
}

// AccountErrorInput mirrors applyAccountErrorHandlingWithCacheInvalidation's
// input.
type AccountErrorInput struct {
	Success       bool
	ErrorMessage  string
	Settings      gatewayruntimecache.GatewaySettings
	TrafficSource string
}

// APIKeyEffectsPort mirrors runtime/account-api-key-effects.service.ts.
type APIKeyEffectsPort interface {
	// CaptureFailureObservation mirrors
	// captureGatewayAccountApiKeyFailureObservation.
	CaptureFailureObservation(account AccountCandidate) string
	// RecordFailure mirrors recordGatewayAccountApiKeyFailure.
	RecordFailure(ctx context.Context, account AccountCandidate, input RecordAPIKeyFailureInput) error
}

// RecordAPIKeyFailureInput mirrors the record input.
type RecordAPIKeyFailureInput struct {
	Status           string
	StatusCode       int
	ErrorCode        string
	ErrorMessage     string
	MutationContext  map[string]any
	ObservationEpoch string
	TraceID          string
	CooldownUntil    *string
	TrafficSource    string
	ClientIP         string
	APIKeyID         string
	Source           string
}

// KeyModelAdmission mirrors runtime/key-model-attempt.ts
// prepareGatewayKeyModelAttempt (gatewayaccounteffects owns the attempt and
// preparation types).
type KeyModelAdmission interface {
	Prepare(ctx context.Context, store gatewayaccounteffects.KeyModelRuntimeStore, input gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput) (gatewayaccounteffects.GatewayKeyModelAttemptPreparation, error)
}

// Compile-time assertions: the exported pipeline satisfies the frozen G05
// port, ready for G20 assembly.
var (
	_ gatewaypreauth.CandidatePipeline = (*CandidatePipeline)(nil)
)
