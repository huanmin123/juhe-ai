package gatewayrouting

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"
)

// Route coordination defaults (route-coordination.ts).
const (
	DefaultGatewayRequestWallBudgetMs    = int64(270_000)
	DefaultGatewayFinalResponseReserveMs = int64(2_000)
	DefaultRouteCoordinationBudgetMs     = int64(3_000)
)

// ClientHandoffReason mirrors the ClientHandoffReason union.
const (
	ClientHandoffWallBudgetExhausted    = "gateway_request_wall_budget_exhausted"
	ClientHandoffPrecommitExhausted     = "precommit_budget_exhausted"
	ClientHandoffServerRetryExhausted   = "server_retry_wait_budget_exhausted"
)

// GatewayDispatchAttemptRejectionReason values mirror the Node union.
const (
	RejectAccountRuntimeAlreadyAttempted   = "account_runtime_already_attempted"
	RejectPhysicalCredentialAlreadyTried   = "physical_credential_already_attempted"
	RejectKeyFingerprintAlreadyAttempted   = "key_fingerprint_already_attempted"
	RejectProtocolModelAlreadyAttempted    = "protocol_model_already_attempted"
	RejectConfirmationAlreadyAttempted     = "confirmation_already_attempted"
	RejectSemanticRetryAlreadyAttempted    = "semantic_retry_already_attempted"
	RejectSameAccountRetryNotRegistered    = "same_account_retry_not_registered"
	RejectSameAccountRetryAlreadyAttempted = "same_account_retry_already_attempted"
	RejectSameAccountRetryIdentityMismatch = "same_account_retry_identity_mismatch"
	RejectSameAccountRetryModeConflict     = "same_account_retry_mode_conflict"
	RejectKeyRotationNotApplicable         = "key_rotation_not_applicable"
)

// Same-account retry reservation failure reasons.
const (
	SameAccountRetryBudgetExhausted = "same_account_retry_budget_exhausted"
	SameAccountRetryNotApplicable   = "same_account_retry_not_applicable"
)

// Route coordination outcomes for RouteCoordinationResult.
const (
	RouteCoordinationDispatchable       = "dispatchable"
	RouteCoordinationTemporarilyBlocked = "temporarily_blocked"
	RouteCoordinationHardExhausted      = "hard_exhausted"
	RouteCoordinationRequestExhausted   = "request_exhausted"
	RouteCoordinationClientHandoff      = "client_handoff"
)

// Error message constants shared with the Node implementation.
const (
	msgKeyMustNotBeEmpty        = "route coordination key must not be empty"
	msgDurationPositive         = "route coordination duration must be a positive finite number"
	msgDurationNonNegative      = "route coordination duration must be a non-negative finite number"
	msgVersionNonNegative       = "route coordination version must be a non-negative integer"
	msgMaxRetriesRange          = "same-account retry maxRetries must be an integer between 0 and 10"
	msgTargetsMustNotBeEmpty    = "route plan orderedAllowedTargets must not be empty"
)

// GatewayRequestAttemptSnapshot mirrors GatewayRequestAttemptSnapshot.
type GatewayRequestAttemptSnapshot struct {
	AttemptedAccountRuntimeKeys      []string
	AttemptedPhysicalCredentialKeys  []string
	AttemptedKeyFingerprints         []string
	AttemptedProtocolModelKeys       []string
}

// GatewayDispatchAttemptIdentity mirrors GatewayDispatchAttemptIdentity.
type GatewayDispatchAttemptIdentity struct {
	ProtocolModelKey      string
	AccountRuntimeKey     string
	PhysicalCredentialKey string
	KeyFingerprint        string
}

// GatewayDispatchAttemptRegistration mirrors
// GatewayDispatchAttemptRegistration: Allowed=false carries the rejection
// reason.
type GatewayDispatchAttemptRegistration struct {
	Allowed bool
	Reason  string
}

// GatewaySameAccountRetryReservation mirrors
// GatewaySameAccountRetryReservation.
type GatewaySameAccountRetryReservation struct {
	Reserved    bool
	Reason      string
	RetryID     string
	RetryNumber int
	Remaining   int
}

// GatewaySameAccountRetryReservationInput mirrors
// GatewaySameAccountRetryReservationInput.
type GatewaySameAccountRetryReservationInput struct {
	GatewayDispatchAttemptIdentity
	MaxRetries int
}

// RouteCoordinationResult mirrors RouteCoordinationResult<TAccount>.
type RouteCoordinationResult[TAccount any] struct {
	Outcome string

	// dispatchable
	Accounts []TAccount

	// temporarily_blocked
	Reason                   string
	EarliestRetryAtMs        *int64
	ConfirmationInFlight     bool
	BlockedAccountIDs        []string
	WaitableByCurrentRequest bool
	LeaseSource              string // 'self_request' | 'capacity_event' | ''
	WakeSource               string // 'capacity_event' | ''
	ForeignLeaseInFlight     bool

	// request_exhausted
	Attempts GatewayRequestAttemptSnapshot

	// client_handoff
	HandoffReason                     string
	RemainingUntriedCandidatesPossible bool
	WallRemainingMs                   int64
	ServerRetryRemainingMs            int64
}

// GatewayRouteFallbackDecision mirrors GatewayRouteFallbackDecision<TContext>
// with the context carried as any (Go cannot express the generic callback
// boundary more precisely without owning the dispatch layer).
type GatewayRouteFallbackDecision struct {
	Attempted bool
	Context   any
}

// GatewayRouteFinalFailure mirrors GatewayRouteFinalFailure.
type GatewayRouteFinalFailure struct {
	StatusCode          int
	Message             string
	ErrorType           string
	ErrorCode           string
	ErrorPhase          string // 'quota' | 'dispatch'
	FailureAttribution  string // 'gateway_capacity' | ''
	RetryAfterMs        *int64
}

// GatewayRouteCoordinatorOwner mirrors GatewayRouteCoordinatorOwner<TContext>
// (context is transported as any).
type GatewayRouteCoordinatorOwner interface {
	RequestFallback(ctx context.Context, reason string) (GatewayRouteFallbackDecision, error)
	CompleteFailure(ctx context.Context, failure GatewayRouteFinalFailure) error
}

// RoutingObserver mirrors the observeGatewayRouting events this package can
// emit ({kind: 'budget', outcome: 'precommit_clipped'}). Nil observers are
// no-ops.
type RoutingObserver interface {
	ObserveRouting(kind, outcome string, nowMs int64)
}

// ---------------------------------------------------------------------------
// GatewayRequestWallBudget
// ---------------------------------------------------------------------------

// GatewayRequestWallBudgetOptions mirrors GatewayRequestWallBudgetOptions.
type GatewayRequestWallBudgetOptions struct {
	RequestAcceptedAtMs int64
	BudgetMs            *int64
	Unbounded           bool
	Now                 func() int64
}

// GatewayRequestWallBudgetDecision mirrors GatewayRequestWallBudgetDecision.
type GatewayRequestWallBudgetDecision struct {
	NowMs                      *int64
	FinalResponseReserveMs     *int64
	MinimumMeaningfulAttemptMs *int64
}

// PrecommitBudgetInput mirrors GatewayRequestPrecommitBudgetInput.
type PrecommitBudgetInput struct {
	NowMs                        *int64
	RequestPrecommitDeadlineAtMs *int64
	FinalResponseReserveMs       *int64
}

// FirstByteDeadlineClipInput mirrors GatewayFirstByteDeadlineClipInput.
type FirstByteDeadlineClipInput struct {
	NowMs                          *int64
	FirstByteDeadlineMs            int64
	RequestPrecommitDeadlineAtMs   *int64
	FinalResponseReserveMs         *int64
	UncommittedAttemptDeadlineAtMs *int64
}

// GatewayRequestWallBudget mirrors the GatewayRequestWallBudget class. The
// unbounded remaining time uses math.MaxInt64 in place of JS +Infinity.
type GatewayRequestWallBudget struct {
	RequestAcceptedAtMs int64
	BudgetMs            int64
	DeadlineAtMs        int64
	Unbounded           bool

	now      func() int64
	observer RoutingObserver
}

// NewGatewayRequestWallBudget mirrors the constructor; normalization errors
// mirror the Node RangeError throws.
func NewGatewayRequestWallBudget(options GatewayRequestWallBudgetOptions, observer RoutingObserver) (*GatewayRequestWallBudget, error) {
	if options.Now == nil {
		options.Now = defaultUnixMillis
	}
	acceptedAt := normalizedCoordinationTimestamp(options.RequestAcceptedAtMs)
	budget := &GatewayRequestWallBudget{
		RequestAcceptedAtMs: acceptedAt,
		Unbounded:           options.Unbounded,
		now:                 options.Now,
		observer:            observer,
	}
	if options.Unbounded {
		// JS: Number.MAX_SAFE_INTEGER - requestAcceptedAtMs. Go clamps the
		// negative-accepted overflow to MaxInt64 (epoch timestamps are
		// non-negative in every real caller).
		if acceptedAt <= 0 {
			budget.BudgetMs = math.MaxInt64
		} else {
			budget.BudgetMs = math.MaxInt64 - acceptedAt
		}
	} else {
		normalized, err := normalizedPositiveMsOrDefault(options.BudgetMs, DefaultGatewayRequestWallBudgetMs)
		if err != nil {
			return nil, err
		}
		budget.BudgetMs = normalized
	}
	budget.DeadlineAtMs = acceptedAt + budget.BudgetMs
	return budget, nil
}

func defaultUnixMillis() int64 { return time.Now().UnixMilli() }

// Now exposes the injected clock (Node Date.now default).
func (b *GatewayRequestWallBudget) Now() int64 { return b.now() }

// WithMinimumBudgetMs mirrors withMinimumBudgetMs. Node's
// normalizedPositiveMs throws for a non-positive minimum (the fallback only
// applies to undefined), so the error carries the original RangeError text.
func (b *GatewayRequestWallBudget) WithMinimumBudgetMs(minimumBudgetMs int64) (*GatewayRequestWallBudget, error) {
	if b.Unbounded {
		return b, nil
	}
	if minimumBudgetMs <= 0 {
		return nil, &RangeError{Message: msgDurationPositive}
	}
	if minimumBudgetMs <= b.BudgetMs {
		return b, nil
	}
	return &GatewayRequestWallBudget{
		RequestAcceptedAtMs: b.RequestAcceptedAtMs,
		BudgetMs:            minimumBudgetMs,
		DeadlineAtMs:        b.RequestAcceptedAtMs + minimumBudgetMs,
		now:                 b.now,
		observer:            b.observer,
	}, nil
}

// WithoutLimit mirrors withoutLimit.
func (b *GatewayRequestWallBudget) WithoutLimit() *GatewayRequestWallBudget {
	if b.Unbounded {
		return b
	}
	clone := *b
	clone.Unbounded = true
	if b.RequestAcceptedAtMs <= 0 {
		clone.BudgetMs = math.MaxInt64
	} else {
		clone.BudgetMs = math.MaxInt64 - b.RequestAcceptedAtMs
	}
	clone.DeadlineAtMs = b.RequestAcceptedAtMs + clone.BudgetMs
	return &clone
}

// ElapsedMs mirrors elapsedMs(nowMs).
func (b *GatewayRequestWallBudget) ElapsedMs(nowMs int64) int64 {
	if delta := normalizedCoordinationTimestamp(nowMs) - b.RequestAcceptedAtMs; delta > 0 {
		return delta
	}
	return 0
}

// RemainingMs mirrors remainingMs(nowMs); unbounded returns MaxInt64 (JS
// +Infinity).
func (b *GatewayRequestWallBudget) RemainingMs(nowMs int64) int64 {
	if b.Unbounded {
		return math.MaxInt64
	}
	if remaining := b.DeadlineAtMs - normalizedCoordinationTimestamp(nowMs); remaining > 0 {
		return remaining
	}
	return 0
}

// AvailableDecisionMs mirrors availableDecisionMs(input); the error mirrors
// the Node RangeError for a negative final-response reserve.
func (b *GatewayRequestWallBudget) AvailableDecisionMs(input GatewayRequestWallBudgetDecision) (int64, error) {
	if b.Unbounded {
		return math.MaxInt64, nil
	}
	reserveMs, err := normalizedFinalResponseReserveMs(input.FinalResponseReserveMs)
	if err != nil {
		return 0, err
	}
	nowMs := b.nowMsOr(input.NowMs)
	remaining := b.RemainingMs(nowMs)
	if available := remaining - reserveMs; available > 0 {
		return available, nil
	}
	return 0, nil
}

// HandoffRequired mirrors handoffRequired(input).
func (b *GatewayRequestWallBudget) HandoffRequired(input GatewayRequestWallBudgetDecision) (bool, error) {
	if b.Unbounded {
		return false, nil
	}
	minimumMeaningfulAttemptMs := int64(0)
	if input.MinimumMeaningfulAttemptMs != nil {
		normalized, err := normalizedNonNegativeMs(input.MinimumMeaningfulAttemptMs)
		if err != nil {
			return false, err
		}
		minimumMeaningfulAttemptMs = normalized
	}
	available, err := b.AvailableDecisionMs(input)
	if err != nil {
		return false, err
	}
	return available <= minimumMeaningfulAttemptMs, nil
}

// PrecommitRemainingMs mirrors precommitRemainingMs(input); the error
// mirrors the Node RangeError for a negative final-response reserve.
func (b *GatewayRequestWallBudget) PrecommitRemainingMs(input PrecommitBudgetInput) (int64, error) {
	if b.Unbounded {
		return math.MaxInt64, nil
	}
	reserveMs, err := normalizedFinalResponseReserveMs(input.FinalResponseReserveMs)
	if err != nil {
		return 0, err
	}
	nowMs := b.nowMsOr(input.NowMs)
	precommitDeadlineAtMs := b.DeadlineAtMs
	if input.RequestPrecommitDeadlineAtMs != nil {
		precommitDeadlineAtMs = normalizedCoordinationTimestamp(*input.RequestPrecommitDeadlineAtMs)
	}
	deadline := precommitDeadlineAtMs
	if b.DeadlineAtMs < deadline {
		deadline = b.DeadlineAtMs
	}
	if remaining := deadline - nowMs - reserveMs; remaining > 0 {
		return remaining, nil
	}
	return 0, nil
}

// ClipFirstByteDeadlineMs mirrors clipFirstByteDeadlineMs(input); the error
// mirrors the Node RangeError normalization throws (negative configured
// deadline or negative final-response reserve).
func (b *GatewayRequestWallBudget) ClipFirstByteDeadlineMs(input FirstByteDeadlineClipInput) (int64, error) {
	configuredFirstByteDeadlineMs, err := normalizedNonNegativeMs(&input.FirstByteDeadlineMs)
	if err != nil {
		return 0, err
	}
	if b.Unbounded {
		return configuredFirstByteDeadlineMs, nil
	}
	nowMs := b.nowMsOr(input.NowMs)
	reserveMs, err := normalizedFinalResponseReserveMs(input.FinalResponseReserveMs)
	if err != nil {
		return 0, err
	}
	precommitRemaining, err := b.PrecommitRemainingMs(PrecommitBudgetInput{
		NowMs:                        &nowMs,
		RequestPrecommitDeadlineAtMs: input.RequestPrecommitDeadlineAtMs,
		FinalResponseReserveMs:       &reserveMs,
	})
	if err != nil {
		return 0, err
	}
	clipped := configuredFirstByteDeadlineMs
	if precommitRemaining < clipped {
		clipped = precommitRemaining
	}
	if input.UncommittedAttemptDeadlineAtMs != nil {
		uncommittedAt := normalizedCoordinationTimestamp(*input.UncommittedAttemptDeadlineAtMs)
		remaining := uncommittedAt - nowMs
		if remaining < 0 {
			remaining = 0
		}
		if remaining < clipped {
			clipped = remaining
		}
	}
	if clipped < 0 {
		clipped = 0
	}
	if clipped < configuredFirstByteDeadlineMs && b.observer != nil {
		b.observer.ObserveRouting("budget", "precommit_clipped", nowMs)
	}
	return clipped, nil
}

func (b *GatewayRequestWallBudget) nowMsOr(value *int64) int64 {
	if value != nil {
		return normalizedCoordinationTimestamp(*value)
	}
	return normalizedCoordinationTimestamp(b.now())
}

// ---------------------------------------------------------------------------
// RouteCoordinationBudget
// ---------------------------------------------------------------------------

// RouteCoordinationBudgetSnapshot mirrors RouteCoordinationBudgetSnapshot.
type RouteCoordinationBudgetSnapshot struct {
	RequestID      string
	BudgetID       string
	Version        int
	RemainingMs    int64
	ActiveSinceMs  *int64
	LastWaitToken  string
}

// RouteCoordinationBudgetOptions mirrors RouteCoordinationBudgetOptions.
type RouteCoordinationBudgetOptions struct {
	RequestID string
	BudgetID  string
	BudgetMs  *int64
	Now       func() int64
}

// RouteCoordinationBudgetTransitionInput mirrors
// RouteCoordinationBudgetTransitionInput.
type RouteCoordinationBudgetTransitionInput struct {
	WaitToken       string
	ExpectedVersion int
	NowMs           *int64
}

// Route coordination budget transition outcomes.
const (
	BudgetTransitionApplied         = "applied"
	BudgetTransitionIdempotentReplay = "idempotent_replay"
	BudgetTransitionVersionConflict = "version_conflict"
	BudgetTransitionInvalid         = "invalid_transition"
)

// RouteCoordinationBudgetTransitionResult mirrors
// RouteCoordinationBudgetTransitionResult.
type RouteCoordinationBudgetTransitionResult struct {
	Outcome  string
	Snapshot RouteCoordinationBudgetSnapshot
}

// RouteCoordinationBudget mirrors the RouteCoordinationBudget class.
type RouteCoordinationBudget struct {
	RequestID string
	BudgetID  string
	BudgetMs  int64

	mu                    sync.Mutex
	version               int
	storedRemainingMs     int64
	activeSinceMs         *int64
	lastWaitToken         string
	observedWaitTokens    map[string]struct{}
	completedWaitTokens   map[string]struct{}
	now                   func() int64
}

// NewRouteCoordinationBudget mirrors the constructor; errors mirror the Node
// TypeError/RangeError throws.
func NewRouteCoordinationBudget(options RouteCoordinationBudgetOptions) (*RouteCoordinationBudget, error) {
	requestID, err := normalizedRequiredKey(options.RequestID)
	if err != nil {
		return nil, err
	}
	budgetID := requestID + ":route-coordination"
	if normalized, err := normalizedOptionalKey(options.BudgetID); err != nil {
		return nil, err
	} else if normalized != "" {
		budgetID = normalized
	}
	budgetMs, err := normalizedPositiveMsOrDefault(options.BudgetMs, DefaultRouteCoordinationBudgetMs)
	if err != nil {
		return nil, err
	}
	if options.Now == nil {
		options.Now = defaultUnixMillis
	}
	return &RouteCoordinationBudget{
		RequestID:        requestID,
		BudgetID:         budgetID,
		BudgetMs:         budgetMs,
		storedRemainingMs: budgetMs,
		observedWaitTokens: make(map[string]struct{}),
		completedWaitTokens: make(map[string]struct{}),
		now:              options.Now,
	}, nil
}

// RemainingMs mirrors remainingMs(nowMs).
func (b *RouteCoordinationBudget) RemainingMs(nowMs int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.remainingMsLocked(normalizedCoordinationTimestamp(nowMs))
}

func (b *RouteCoordinationBudget) remainingMsLocked(nowMs int64) int64 {
	if b.activeSinceMs == nil {
		return b.storedRemainingMs
	}
	elapsed := nowMs - *b.activeSinceMs
	if elapsed < 0 {
		elapsed = 0
	}
	if remaining := b.storedRemainingMs - elapsed; remaining > 0 {
		return remaining
	}
	return 0
}

// Exhausted mirrors exhausted(nowMs).
func (b *RouteCoordinationBudget) Exhausted(nowMs int64) bool {
	return b.RemainingMs(nowMs) <= 0
}

// Snapshot mirrors snapshot(nowMs).
func (b *RouteCoordinationBudget) Snapshot(nowMs int64) RouteCoordinationBudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked(normalizedCoordinationTimestamp(nowMs))
}

func (b *RouteCoordinationBudget) snapshotLocked(nowMs int64) RouteCoordinationBudgetSnapshot {
	return RouteCoordinationBudgetSnapshot{
		RequestID:     b.RequestID,
		BudgetID:      b.BudgetID,
		Version:       b.version,
		RemainingMs:   b.remainingMsLocked(nowMs),
		ActiveSinceMs: b.activeSinceMs,
		LastWaitToken: b.lastWaitToken,
	}
}

// BeginWait mirrors beginWait(input).
func (b *RouteCoordinationBudget) BeginWait(input RouteCoordinationBudgetTransitionInput) (RouteCoordinationBudgetTransitionResult, error) {
	waitToken, err := normalizedRequiredKey(input.WaitToken)
	if err != nil {
		return RouteCoordinationBudgetTransitionResult{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	nowMs := normalizedCoordinationTimestamp(input.nowMsOr(b.now))
	if _, replayed := b.observedWaitTokens[waitToken]; replayed {
		return b.transitionResult(BudgetTransitionIdempotentReplay, nowMs), nil
	}
	if err := normalizedVersion(input.ExpectedVersion); err != nil {
		return RouteCoordinationBudgetTransitionResult{}, err
	}
	if input.ExpectedVersion != b.version {
		return b.transitionResult(BudgetTransitionVersionConflict, nowMs), nil
	}
	if b.activeSinceMs != nil || b.remainingMsLocked(nowMs) <= 0 {
		return b.transitionResult(BudgetTransitionInvalid, nowMs), nil
	}

	b.observedWaitTokens[waitToken] = struct{}{}
	b.lastWaitToken = waitToken
	activeSince := nowMs
	b.activeSinceMs = &activeSince
	b.version++
	return b.transitionResult(BudgetTransitionApplied, nowMs), nil
}

// PauseWait mirrors pauseWait(input).
func (b *RouteCoordinationBudget) PauseWait(input RouteCoordinationBudgetTransitionInput) (RouteCoordinationBudgetTransitionResult, error) {
	waitToken, err := normalizedRequiredKey(input.WaitToken)
	if err != nil {
		return RouteCoordinationBudgetTransitionResult{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	nowMs := normalizedCoordinationTimestamp(input.nowMsOr(b.now))
	if _, completed := b.completedWaitTokens[waitToken]; completed {
		return b.transitionResult(BudgetTransitionIdempotentReplay, nowMs), nil
	}
	if err := normalizedVersion(input.ExpectedVersion); err != nil {
		return RouteCoordinationBudgetTransitionResult{}, err
	}
	if input.ExpectedVersion != b.version {
		return b.transitionResult(BudgetTransitionVersionConflict, nowMs), nil
	}
	if b.activeSinceMs == nil || b.lastWaitToken != waitToken {
		return b.transitionResult(BudgetTransitionInvalid, nowMs), nil
	}

	b.storedRemainingMs = b.remainingMsLocked(nowMs)
	b.activeSinceMs = nil
	b.completedWaitTokens[waitToken] = struct{}{}
	b.version++
	return b.transitionResult(BudgetTransitionApplied, nowMs), nil
}

func (i RouteCoordinationBudgetTransitionInput) nowMsOr(now func() int64) int64 {
	if i.NowMs != nil {
		return *i.NowMs
	}
	return now()
}

func (b *RouteCoordinationBudget) transitionResult(outcome string, nowMs int64) RouteCoordinationBudgetTransitionResult {
	// Caller holds b.mu.
	return RouteCoordinationBudgetTransitionResult{Outcome: outcome, Snapshot: b.snapshotLocked(nowMs)}
}

// ---------------------------------------------------------------------------
// GatewayRequestAttemptTracker
// ---------------------------------------------------------------------------

// CanAttemptAccountInput mirrors canAttemptAccount's input.
type CanAttemptAccountInput struct {
	AccountRuntimeKey     string
	PhysicalCredentialKey string
	MatchingConfirmation  bool
	SemanticRetryID       string
}

// GatewayDispatchAttemptRecordInput mirrors tryRecordDispatchAttempt's
// input.
type GatewayDispatchAttemptRecordInput struct {
	GatewayDispatchAttemptIdentity
	MatchingConfirmation bool
	AllowKeyRotation     bool
	SemanticRetryID      string
	SameAccountRetryID   string
}

// GatewayDispatchAttemptIdentityKey mirrors dispatchAttemptIdentityKey:
// identity scoped to the (accountRuntimeKey, physicalCredentialKey) pair.
type GatewayDispatchAttemptIdentityKey struct {
	AccountRuntimeKey     string
	PhysicalCredentialKey string
}

// confirmationAttemptKey mirrors confirmationAttemptKey (same pair scope).
type confirmationAttemptKey = GatewayDispatchAttemptIdentityKey

// semanticRetryAttemptKey mirrors semanticRetryAttemptKey.
type semanticRetryAttemptKey struct {
	SemanticRetryID       string
	AccountRuntimeKey     string
	PhysicalCredentialKey string
}

// sameAccountRetryReservationRecord mirrors the reservation record.
type sameAccountRetryReservationRecord struct {
	identity    GatewayDispatchAttemptIdentity
	retryNumber int
	consumed    bool
}

// stringSet is an insertion-ordered string set mirroring JS Set semantics.
type stringSet struct {
	members map[string]struct{}
	order   []string
}

func newStringSet(values []string) (stringSet, error) {
	set := stringSet{members: make(map[string]struct{})}
	for _, value := range values {
		if _, err := set.add(value); err != nil {
			return stringSet{}, err
		}
	}
	return set, nil
}

func (s *stringSet) add(value string) (bool, error) {
	normalized, err := normalizedRequiredKey(value)
	if err != nil {
		return false, err
	}
	if _, ok := s.members[normalized]; ok {
		return false, nil
	}
	s.members[normalized] = struct{}{}
	s.order = append(s.order, normalized)
	return true, nil
}

func (s *stringSet) has(value string) (bool, error) {
	normalized, err := normalizedRequiredKey(value)
	if err != nil {
		return false, err
	}
	_, ok := s.members[normalized]
	return ok, nil
}

func (s *stringSet) values() []string {
	return append([]string(nil), s.order...)
}

func (s *stringSet) contains(value string) bool {
	_, ok := s.members[value]
	return ok
}

// GatewayRequestAttemptTracker mirrors the GatewayRequestAttemptTracker
// class.
type GatewayRequestAttemptTracker struct {
	accountRuntimeKeys             stringSet
	physicalCredentialKeys         stringSet
	keyFingerprints                stringSet
	protocolModelKeys              stringSet
	physicalCredentialRuntimeKeys  map[string]map[string]struct{}
	confirmationAttemptKeys        map[confirmationAttemptKey]struct{}
	confirmationPhysicalCredential map[string]string
	semanticRetryAttemptKeys       map[semanticRetryAttemptKey]struct{}
	registeredDispatchIdentities   map[GatewayDispatchAttemptIdentityKey]GatewayDispatchAttemptIdentity
	sameAccountRetryReservations   map[string]*sameAccountRetryReservationRecord
	sameAccountRetryLimits         map[GatewayDispatchAttemptIdentityKey]int
	sameAccountRetryCounts         map[GatewayDispatchAttemptIdentityKey]int
}

// NewGatewayRequestAttemptTracker mirrors the constructor; pre-seeded keys
// are normalized exactly like Node (trim + empty rejection).
func NewGatewayRequestAttemptTracker(initial *GatewayRequestAttemptSnapshot) (*GatewayRequestAttemptTracker, error) {
	tracker := &GatewayRequestAttemptTracker{
		physicalCredentialRuntimeKeys:  make(map[string]map[string]struct{}),
		confirmationAttemptKeys:        make(map[confirmationAttemptKey]struct{}),
		confirmationPhysicalCredential: make(map[string]string),
		semanticRetryAttemptKeys:       make(map[semanticRetryAttemptKey]struct{}),
		registeredDispatchIdentities:   make(map[GatewayDispatchAttemptIdentityKey]GatewayDispatchAttemptIdentity),
		sameAccountRetryReservations:   make(map[string]*sameAccountRetryReservationRecord),
		sameAccountRetryLimits:         make(map[GatewayDispatchAttemptIdentityKey]int),
		sameAccountRetryCounts:         make(map[GatewayDispatchAttemptIdentityKey]int),
	}
	var err error
	build := func(values []string) (stringSet, error) {
		if values == nil {
			values = []string{}
		}
		return newStringSet(values)
	}
	if tracker.accountRuntimeKeys, err = build(initialKeyValues(initial, func(s *GatewayRequestAttemptSnapshot) []string { return s.AttemptedAccountRuntimeKeys })); err != nil {
		return nil, err
	}
	if tracker.physicalCredentialKeys, err = build(initialKeyValues(initial, func(s *GatewayRequestAttemptSnapshot) []string { return s.AttemptedPhysicalCredentialKeys })); err != nil {
		return nil, err
	}
	if tracker.keyFingerprints, err = build(initialKeyValues(initial, func(s *GatewayRequestAttemptSnapshot) []string { return s.AttemptedKeyFingerprints })); err != nil {
		return nil, err
	}
	if tracker.protocolModelKeys, err = build(initialKeyValues(initial, func(s *GatewayRequestAttemptSnapshot) []string { return s.AttemptedProtocolModelKeys })); err != nil {
		return nil, err
	}
	return tracker, nil
}

func initialKeyValues(initial *GatewayRequestAttemptSnapshot, pick func(*GatewayRequestAttemptSnapshot) []string) []string {
	if initial == nil {
		return nil
	}
	return pick(initial)
}

// RecordAccountRuntimeKey mirrors recordAccountRuntimeKey.
func (t *GatewayRequestAttemptTracker) RecordAccountRuntimeKey(key string) (bool, error) {
	return t.accountRuntimeKeys.add(key)
}

// RecordPhysicalCredentialKey mirrors recordPhysicalCredentialKey.
func (t *GatewayRequestAttemptTracker) RecordPhysicalCredentialKey(key string) (bool, error) {
	return t.physicalCredentialKeys.add(key)
}

// RecordKeyFingerprint mirrors recordKeyFingerprint.
func (t *GatewayRequestAttemptTracker) RecordKeyFingerprint(key string) (bool, error) {
	return t.keyFingerprints.add(key)
}

// RecordProtocolModelKey mirrors recordProtocolModelKey.
func (t *GatewayRequestAttemptTracker) RecordProtocolModelKey(key string) (bool, error) {
	return t.protocolModelKeys.add(key)
}

// HasAccountRuntimeKey mirrors hasAccountRuntimeKey.
func (t *GatewayRequestAttemptTracker) HasAccountRuntimeKey(key string) (bool, error) {
	return t.accountRuntimeKeys.has(key)
}

// HasPhysicalCredentialKey mirrors hasPhysicalCredentialKey.
func (t *GatewayRequestAttemptTracker) HasPhysicalCredentialKey(key string) (bool, error) {
	return t.physicalCredentialKeys.has(key)
}

// HasKeyFingerprint mirrors hasKeyFingerprint.
func (t *GatewayRequestAttemptTracker) HasKeyFingerprint(key string) (bool, error) {
	return t.keyFingerprints.has(key)
}

// HasProtocolModelKey mirrors hasProtocolModelKey.
func (t *GatewayRequestAttemptTracker) HasProtocolModelKey(key string) (bool, error) {
	return t.protocolModelKeys.has(key)
}

// TryReserveSameAccountRetry mirrors tryReserveSameAccountRetry.
func (t *GatewayRequestAttemptTracker) TryReserveSameAccountRetry(input GatewaySameAccountRetryReservationInput) (GatewaySameAccountRetryReservation, error) {
	if input.MaxRetries < 0 || input.MaxRetries > 10 {
		return GatewaySameAccountRetryReservation{}, &RangeError{Message: msgMaxRetriesRange}
	}
	identity, err := normalizedDispatchAttemptIdentity(input.GatewayDispatchAttemptIdentity)
	if err != nil {
		return GatewaySameAccountRetryReservation{}, err
	}
	retryKey := GatewayDispatchAttemptIdentityKey{
		AccountRuntimeKey:     identity.AccountRuntimeKey,
		PhysicalCredentialKey: identity.PhysicalCredentialKey,
	}
	if current, ok := t.sameAccountRetryLimits[retryKey]; !ok || input.MaxRetries < current {
		t.sameAccountRetryLimits[retryKey] = input.MaxRetries
	}

	registered, registeredOK := t.registeredDispatchIdentities[retryKey]
	remaining := t.sameAccountRetryRemaining(retryKey)
	if !registeredOK || !sameDispatchAttemptIdentity(registered, identity) {
		return GatewaySameAccountRetryReservation{Reserved: false, Reason: SameAccountRetryNotApplicable, Remaining: remaining}, nil
	}
	if remaining <= 0 {
		return GatewaySameAccountRetryReservation{Reserved: false, Reason: SameAccountRetryBudgetExhausted, Remaining: 0}, nil
	}

	retryNumber := t.sameAccountRetryCounts[retryKey] + 1
	retryID := "same-account-retry:" + uuid4String()
	t.sameAccountRetryCounts[retryKey] = retryNumber
	t.sameAccountRetryReservations[retryID] = &sameAccountRetryReservationRecord{
		identity:    identity,
		retryNumber: retryNumber,
	}
	return GatewaySameAccountRetryReservation{
		Reserved:    true,
		RetryID:     retryID,
		RetryNumber: retryNumber,
		Remaining:   t.sameAccountRetryRemaining(retryKey),
	}, nil
}

// CanAttemptAccount mirrors canAttemptAccount.
func (t *GatewayRequestAttemptTracker) CanAttemptAccount(input CanAttemptAccountInput) (GatewayDispatchAttemptRegistration, error) {
	accountRuntimeKey, err := normalizedRequiredKey(input.AccountRuntimeKey)
	if err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	physicalCredentialKey, err := normalizedRequiredKey(input.PhysicalCredentialKey)
	if err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	runtimeKeys := t.physicalCredentialRuntimeKeys[physicalCredentialKey]
	physicalHas, err := t.physicalCredentialKeys.has(physicalCredentialKey)
	if err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	accountHas, err := t.accountRuntimeKeys.has(accountRuntimeKey)
	if err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	physicalAttemptedByAnotherRuntime := false
	if runtimeKeys != nil {
		_, attempted := runtimeKeys[accountRuntimeKey]
		physicalAttemptedByAnotherRuntime = !attempted
	} else {
		physicalAttemptedByAnotherRuntime = physicalHas && !accountHas
	}
	if physicalAttemptedByAnotherRuntime {
		return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectPhysicalCredentialAlreadyTried}, nil
	}
	if input.SemanticRetryID != "" {
		key := semanticRetryAttemptKey{
			SemanticRetryID:       input.SemanticRetryID,
			AccountRuntimeKey:     accountRuntimeKey,
			PhysicalCredentialKey: physicalCredentialKey,
		}
		if _, attempted := t.semanticRetryAttemptKeys[key]; attempted {
			return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectSemanticRetryAlreadyAttempted}, nil
		}
		return GatewayDispatchAttemptRegistration{Allowed: true}, nil
	}
	if input.MatchingConfirmation {
		if existing, ok := t.confirmationPhysicalCredential[accountRuntimeKey]; ok && existing != physicalCredentialKey {
			return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectPhysicalCredentialAlreadyTried}, nil
		}
		key := confirmationAttemptKey{
			AccountRuntimeKey:     accountRuntimeKey,
			PhysicalCredentialKey: physicalCredentialKey,
		}
		if _, attempted := t.confirmationAttemptKeys[key]; attempted {
			return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectConfirmationAlreadyAttempted}, nil
		}
		return GatewayDispatchAttemptRegistration{Allowed: true}, nil
	}
	if physicalHas {
		return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectPhysicalCredentialAlreadyTried}, nil
	}
	if accountHas {
		return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectAccountRuntimeAlreadyAttempted}, nil
	}
	return GatewayDispatchAttemptRegistration{Allowed: true}, nil
}

// TryRecordDispatchAttempt mirrors tryRecordDispatchAttempt.
func (t *GatewayRequestAttemptTracker) TryRecordDispatchAttempt(input GatewayDispatchAttemptRecordInput) (GatewayDispatchAttemptRegistration, error) {
	identity, err := normalizedDispatchAttemptIdentity(input.GatewayDispatchAttemptIdentity)
	if err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	if input.SameAccountRetryID != "" {
		return t.tryRecordSameAccountRetry(identity, input)
	}
	accountDecision, err := t.CanAttemptAccount(CanAttemptAccountInput{
		AccountRuntimeKey:     identity.AccountRuntimeKey,
		PhysicalCredentialKey: identity.PhysicalCredentialKey,
		MatchingConfirmation:  input.MatchingConfirmation,
		SemanticRetryID:       input.SemanticRetryID,
	})
	if err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}

	switch {
	case input.SemanticRetryID != "":
		if !accountDecision.Allowed {
			return accountDecision, nil
		}
		t.semanticRetryAttemptKeys[semanticRetryAttemptKey{
			SemanticRetryID:       input.SemanticRetryID,
			AccountRuntimeKey:     identity.AccountRuntimeKey,
			PhysicalCredentialKey: identity.PhysicalCredentialKey,
		}] = struct{}{}
	case input.MatchingConfirmation:
		if accountDecision.Allowed {
			t.confirmationAttemptKeys[confirmationAttemptKey{
				AccountRuntimeKey:     identity.AccountRuntimeKey,
				PhysicalCredentialKey: identity.PhysicalCredentialKey,
			}] = struct{}{}
			t.confirmationPhysicalCredential[identity.AccountRuntimeKey] = identity.PhysicalCredentialKey
		} else {
			if !input.AllowKeyRotation || accountDecision.Reason != RejectConfirmationAlreadyAttempted {
				return accountDecision, nil
			}
			keyRotationDecision, err := t.canRotateKey(identity)
			if err != nil {
				return GatewayDispatchAttemptRegistration{}, err
			}
			if !keyRotationDecision.Allowed {
				return keyRotationDecision, nil
			}
		}
	case input.AllowKeyRotation:
		keyRotationDecision, err := t.canRotateKey(identity)
		if err != nil {
			return GatewayDispatchAttemptRegistration{}, err
		}
		if !keyRotationDecision.Allowed {
			return keyRotationDecision, nil
		}
	default:
		if !accountDecision.Allowed {
			return accountDecision, nil
		}
		if t.protocolModelKeys.contains(identity.ProtocolModelKey) {
			return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectProtocolModelAlreadyAttempted}, nil
		}
		if identity.KeyFingerprint != "" {
			fingerprintAttempted, err := t.keyFingerprints.has(identity.KeyFingerprint)
			if err != nil {
				return GatewayDispatchAttemptRegistration{}, err
			}
			if fingerprintAttempted {
				return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectKeyFingerprintAlreadyAttempted}, nil
			}
		}
	}

	if _, err := t.accountRuntimeKeys.add(identity.AccountRuntimeKey); err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	if _, err := t.physicalCredentialKeys.add(identity.PhysicalCredentialKey); err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	if _, err := t.protocolModelKeys.add(identity.ProtocolModelKey); err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	if identity.KeyFingerprint != "" {
		if _, err := t.keyFingerprints.add(identity.KeyFingerprint); err != nil {
			return GatewayDispatchAttemptRegistration{}, err
		}
	}
	runtimeKeys := t.physicalCredentialRuntimeKeys[identity.PhysicalCredentialKey]
	if runtimeKeys == nil {
		runtimeKeys = make(map[string]struct{})
		t.physicalCredentialRuntimeKeys[identity.PhysicalCredentialKey] = runtimeKeys
	}
	runtimeKeys[identity.AccountRuntimeKey] = struct{}{}
	t.registeredDispatchIdentities[GatewayDispatchAttemptIdentityKey{
		AccountRuntimeKey:     identity.AccountRuntimeKey,
		PhysicalCredentialKey: identity.PhysicalCredentialKey,
	}] = identity
	return GatewayDispatchAttemptRegistration{Allowed: true}, nil
}

// Snapshot mirrors snapshot().
func (t *GatewayRequestAttemptTracker) Snapshot() GatewayRequestAttemptSnapshot {
	return GatewayRequestAttemptSnapshot{
		AttemptedAccountRuntimeKeys:     t.accountRuntimeKeys.values(),
		AttemptedPhysicalCredentialKeys: t.physicalCredentialKeys.values(),
		AttemptedKeyFingerprints:        t.keyFingerprints.values(),
		AttemptedProtocolModelKeys:      t.protocolModelKeys.values(),
	}
}

func (t *GatewayRequestAttemptTracker) sameAccountRetryRemaining(retryKey GatewayDispatchAttemptIdentityKey) int {
	limit := t.sameAccountRetryLimits[retryKey]
	count := t.sameAccountRetryCounts[retryKey]
	remaining := limit - count
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (t *GatewayRequestAttemptTracker) canRotateKey(identity GatewayDispatchAttemptIdentity) (GatewayDispatchAttemptRegistration, error) {
	runtimeKeys := t.physicalCredentialRuntimeKeys[identity.PhysicalCredentialKey]
	_, runtimeAttempted := runtimeKeys[identity.AccountRuntimeKey]
	accountAttempted, err := t.accountRuntimeKeys.has(identity.AccountRuntimeKey)
	if err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	physicalAttempted, err := t.physicalCredentialKeys.has(identity.PhysicalCredentialKey)
	if err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	if identity.KeyFingerprint == "" || !accountAttempted || !physicalAttempted || !runtimeAttempted {
		return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectKeyRotationNotApplicable}, nil
	}
	fingerprintAttempted, err := t.keyFingerprints.has(identity.KeyFingerprint)
	if err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	if fingerprintAttempted {
		return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectKeyFingerprintAlreadyAttempted}, nil
	}
	return GatewayDispatchAttemptRegistration{Allowed: true}, nil
}

func (t *GatewayRequestAttemptTracker) tryRecordSameAccountRetry(identity GatewayDispatchAttemptIdentity, input GatewayDispatchAttemptRecordInput) (GatewayDispatchAttemptRegistration, error) {
	if input.MatchingConfirmation || input.AllowKeyRotation || input.SemanticRetryID != "" {
		return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectSameAccountRetryModeConflict}, nil
	}
	retryID, err := normalizedRequiredKey(input.SameAccountRetryID)
	if err != nil {
		return GatewayDispatchAttemptRegistration{}, err
	}
	reservation, ok := t.sameAccountRetryReservations[retryID]
	if !ok {
		return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectSameAccountRetryNotRegistered}, nil
	}
	if reservation.consumed {
		return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectSameAccountRetryAlreadyAttempted}, nil
	}
	if !sameDispatchAttemptIdentity(reservation.identity, identity) {
		return GatewayDispatchAttemptRegistration{Allowed: false, Reason: RejectSameAccountRetryIdentityMismatch}, nil
	}
	reservation.consumed = true
	return GatewayDispatchAttemptRegistration{Allowed: true}, nil
}

// ---------------------------------------------------------------------------
// Protocol model key + route plan snapshots
// ---------------------------------------------------------------------------

// GatewayAttemptProtocolModelKey mirrors gatewayAttemptProtocolModelKey.
func GatewayAttemptProtocolModelKey(accountRuntimeKey, protocolCode, protocolVersion, model string) (string, error) {
	account, err := normalizedRequiredKey(accountRuntimeKey)
	if err != nil {
		return "", err
	}
	protocol := "unknown_protocol"
	if normalized, err := normalizedOptionalKey(protocolCode); err != nil {
		return "", err
	} else if normalized != "" {
		protocol = normalized
	}
	version := "unknown_version"
	if normalized, err := normalizedOptionalKey(protocolVersion); err != nil {
		return "", err
	} else if normalized != "" {
		version = normalized
	}
	modelKey := "unknown_model"
	if normalized, err := normalizedOptionalKey(model); err != nil {
		return "", err
	} else if normalized != "" {
		modelKey = normalized
	}
	encoded, err := json.Marshal([]string{account, protocol, version, modelKey})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// RoutePlanSnapshot mirrors GatewayRoutePlanSnapshot<TTarget>.
type RoutePlanSnapshot[TTarget any] struct {
	RoutePlanID                    string
	Mode                           string
	RequestAcceptedAtMs            int64
	GatewayRequestWallBudgetMs     int64
	GatewayRequestWallDeadlineAtMs int64
	FirstByteDeadlineMs            *int64
	RequestPrecommitDeadlineAtMs   int64
	FinalResponseReserveMs         int64
	UncommittedAttemptDeadlineAtMs *int64
	OrderedAllowedTargets          []TTarget
	Cursor                         int
	WeightedDecisionToken          string
	HybridScoreDecision            any
}

// CreateGatewayRoutePlanSnapshotInput mirrors
// CreateGatewayRoutePlanSnapshotInput<TTarget>; optional JS fields stay
// pointers / empty strings.
type CreateGatewayRoutePlanSnapshotInput[TTarget any] struct {
	RoutePlanID                    string
	Mode                           string
	RequestAcceptedAtMs            int64
	GatewayRequestWallBudgetMs     *int64
	FirstByteDeadlineMs            *int64
	RequestPrecommitDeadlineAtMs   *int64
	FinalResponseReserveMs         *int64
	UncommittedAttemptDeadlineAtMs *int64
	OrderedAllowedTargets          []TTarget
	Cursor                         *int
	WeightedDecisionToken          string
	HybridScoreDecision            any
}

// CreateGatewayRoutePlanSnapshot mirrors createGatewayRoutePlanSnapshot;
// errors mirror the Node TypeError/RangeError throws with identical texts.
func CreateGatewayRoutePlanSnapshot[TTarget any](input CreateGatewayRoutePlanSnapshotInput[TTarget]) (RoutePlanSnapshot[TTarget], error) {
	routePlanID, err := normalizedRequiredKey(input.RoutePlanID)
	if err != nil {
		return RoutePlanSnapshot[TTarget]{}, err
	}
	requestAcceptedAtMs := normalizedCoordinationTimestamp(input.RequestAcceptedAtMs)
	gatewayRequestWallBudgetMs, err := normalizedPositiveMsOrDefault(input.GatewayRequestWallBudgetMs, DefaultGatewayRequestWallBudgetMs)
	if err != nil {
		return RoutePlanSnapshot[TTarget]{}, err
	}
	gatewayRequestWallDeadlineAtMs := requestAcceptedAtMs + gatewayRequestWallBudgetMs
	orderedAllowedTargets := append([]TTarget(nil), input.OrderedAllowedTargets...)
	if len(orderedAllowedTargets) == 0 {
		return RoutePlanSnapshot[TTarget]{}, &RangeError{Message: msgTargetsMustNotBeEmpty}
	}
	cursor := 0
	if input.Cursor != nil {
		cursor = *input.Cursor
	}
	cursor, err = normalizedCursor(cursor, len(orderedAllowedTargets))
	if err != nil {
		return RoutePlanSnapshot[TTarget]{}, err
	}
	requestPrecommitDeadlineAtMs := gatewayRequestWallDeadlineAtMs
	if input.RequestPrecommitDeadlineAtMs != nil {
		requestPrecommitDeadlineAtMs = normalizedCoordinationTimestamp(*input.RequestPrecommitDeadlineAtMs)
	}
	finalResponseReserveMs, err := normalizedFinalResponseReserveMs(input.FinalResponseReserveMs)
	if err != nil {
		return RoutePlanSnapshot[TTarget]{}, err
	}
	weightedDecisionToken := ""
	if normalized, err := normalizedOptionalKey(input.WeightedDecisionToken); err != nil {
		return RoutePlanSnapshot[TTarget]{}, err
	} else if normalized != "" {
		weightedDecisionToken = normalized
	}
	if requestPrecommitDeadlineAtMs > gatewayRequestWallDeadlineAtMs {
		requestPrecommitDeadlineAtMs = gatewayRequestWallDeadlineAtMs
	}
	var firstByteDeadlineMs *int64
	if input.FirstByteDeadlineMs != nil {
		normalized, err := normalizedNonNegativeMs(input.FirstByteDeadlineMs)
		if err != nil {
			return RoutePlanSnapshot[TTarget]{}, err
		}
		firstByteDeadlineMs = &normalized
	}
	var uncommittedAttemptDeadlineAtMs *int64
	if input.UncommittedAttemptDeadlineAtMs != nil {
		normalized := normalizedCoordinationTimestamp(*input.UncommittedAttemptDeadlineAtMs)
		uncommittedAttemptDeadlineAtMs = &normalized
	}
	return RoutePlanSnapshot[TTarget]{
		RoutePlanID:                    routePlanID,
		Mode:                           input.Mode,
		RequestAcceptedAtMs:            requestAcceptedAtMs,
		GatewayRequestWallBudgetMs:     gatewayRequestWallBudgetMs,
		GatewayRequestWallDeadlineAtMs: gatewayRequestWallDeadlineAtMs,
		FirstByteDeadlineMs:            firstByteDeadlineMs,
		RequestPrecommitDeadlineAtMs:   requestPrecommitDeadlineAtMs,
		FinalResponseReserveMs:         finalResponseReserveMs,
		UncommittedAttemptDeadlineAtMs: uncommittedAttemptDeadlineAtMs,
		OrderedAllowedTargets:          orderedAllowedTargets,
		Cursor:                         cursor,
		WeightedDecisionToken:          weightedDecisionToken,
		HybridScoreDecision:            input.HybridScoreDecision,
	}, nil
}

// AdvanceGatewayRoutePlanCursor mirrors advanceGatewayRoutePlanCursor; Node
// defaults nextCursor to plan.cursor + 1, so Go callers pass that value
// explicitly.
func AdvanceGatewayRoutePlanCursor[TTarget any](plan RoutePlanSnapshot[TTarget], nextCursor int) (RoutePlanSnapshot[TTarget], error) {
	return CreateGatewayRoutePlanSnapshot[TTarget](CreateGatewayRoutePlanSnapshotInput[TTarget]{
		RoutePlanID:                    plan.RoutePlanID,
		Mode:                           plan.Mode,
		RequestAcceptedAtMs:            plan.RequestAcceptedAtMs,
		GatewayRequestWallBudgetMs:     &plan.GatewayRequestWallBudgetMs,
		FirstByteDeadlineMs:            plan.FirstByteDeadlineMs,
		RequestPrecommitDeadlineAtMs:   &plan.RequestPrecommitDeadlineAtMs,
		FinalResponseReserveMs:         &plan.FinalResponseReserveMs,
		UncommittedAttemptDeadlineAtMs: plan.UncommittedAttemptDeadlineAtMs,
		OrderedAllowedTargets:          plan.OrderedAllowedTargets,
		Cursor:                         &nextCursor,
		WeightedDecisionToken:          plan.WeightedDecisionToken,
		HybridScoreDecision:            plan.HybridScoreDecision,
	})
}

// ---------------------------------------------------------------------------
// Normalization helpers (mirror the private Node helpers)
// ---------------------------------------------------------------------------

func normalizedCursor(value int, targetCount int) (int, error) {
	if value < 0 || value >= targetCount {
		return 0, &RangeError{Message: fmt.Sprintf("route plan cursor %s is outside ordered target range", strconv.Itoa(value))}
	}
	return value, nil
}

func normalizedVersion(value int) error {
	if value < 0 {
		return &RangeError{Message: msgVersionNonNegative}
	}
	return nil
}

func normalizedDispatchAttemptIdentity(identity GatewayDispatchAttemptIdentity) (GatewayDispatchAttemptIdentity, error) {
	protocolModelKey, err := normalizedRequiredKey(identity.ProtocolModelKey)
	if err != nil {
		return GatewayDispatchAttemptIdentity{}, err
	}
	accountRuntimeKey, err := normalizedRequiredKey(identity.AccountRuntimeKey)
	if err != nil {
		return GatewayDispatchAttemptIdentity{}, err
	}
	physicalCredentialKey, err := normalizedRequiredKey(identity.PhysicalCredentialKey)
	if err != nil {
		return GatewayDispatchAttemptIdentity{}, err
	}
	keyFingerprint, err := normalizedOptionalKey(identity.KeyFingerprint)
	if err != nil {
		return GatewayDispatchAttemptIdentity{}, err
	}
	return GatewayDispatchAttemptIdentity{
		ProtocolModelKey:      protocolModelKey,
		AccountRuntimeKey:     accountRuntimeKey,
		PhysicalCredentialKey: physicalCredentialKey,
		KeyFingerprint:        keyFingerprint,
	}, nil
}

func sameDispatchAttemptIdentity(left, right GatewayDispatchAttemptIdentity) bool {
	return left.ProtocolModelKey == right.ProtocolModelKey &&
		left.AccountRuntimeKey == right.AccountRuntimeKey &&
		left.PhysicalCredentialKey == right.PhysicalCredentialKey &&
		left.KeyFingerprint == right.KeyFingerprint
}

func normalizedRequiredKey(value string) (string, error) {
	normalized := trimSpace(value)
	if normalized == "" {
		return "", &TypeError{Message: msgKeyMustNotBeEmpty}
	}
	return normalized, nil
}

func normalizedOptionalKey(value string) (string, error) {
	normalized := trimSpace(value)
	return normalized, nil
}

// normalizedPositiveMsOrDefault mirrors normalizedPositiveMs(value, fallback).
func normalizedPositiveMsOrDefault(value *int64, fallback int64) (int64, error) {
	if value == nil {
		return fallback, nil
	}
	if *value <= 0 {
		return 0, &RangeError{Message: msgDurationPositive}
	}
	return *value, nil
}

func normalizedNonNegativeMs(value *int64) (int64, error) {
	if value == nil {
		return 0, nil
	}
	if *value < 0 {
		return 0, &RangeError{Message: msgDurationNonNegative}
	}
	return *value, nil
}

func normalizedFinalResponseReserveMs(value *int64) (int64, error) {
	if value == nil {
		return DefaultGatewayFinalResponseReserveMs, nil
	}
	return normalizedNonNegativeMs(value)
}

// normalizedCoordinationTimestamp mirrors the coordination-level
// normalizedTimestamp: JS rejects non-finite numbers; Go int64 values are
// always finite, so this is the identity function kept for parity comments.
func normalizedCoordinationTimestamp(value int64) int64 {
	return value
}

// uuid4String mirrors Node randomUUID() (v4, lowercase, dashed).
func uuid4String() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
