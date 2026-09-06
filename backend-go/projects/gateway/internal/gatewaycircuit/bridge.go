package gatewaycircuit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Durable incident state names mirror AccountCircuitIncidentState.
const (
	IncidentStateClosed               = "CLOSED"
	IncidentStateSuspect              = "SUSPECT"
	IncidentStateOpen                 = "OPEN"
	IncidentStateHalfOpen             = "HALF_OPEN"
	IncidentStateRecovering           = "RECOVERING"
	IncidentStatePersisting           = "PERSISTING"
	IncidentStateShadowedByPersistent = "SHADOWED_BY_PERSISTENT"
)

// Durable incident scope kinds mirror AccountCircuitScopeKind.
const (
	IncidentScopeKindAccount      = "account"
	IncidentScopeKindKey          = "key"
	IncidentScopeKindProtocolModel = "protocol_model"
	IncidentScopeKindKeyModel     = "key_model"
)

// Failure classes mirror AccountCircuitFailureClass.
const (
	FailureClassConnectFailed      = "connect_failed"
	FailureClassTimeoutBeforeComplete = "timeout_before_complete"
	FailureClassReadInterrupted    = "read_interrupted"
	FailureClassIncompleteResponse = "incomplete_response"
	FailureClassExplicitPolicy     = "explicit_policy"
)

// Lease purposes mirror AccountCircuitLeasePurpose.
const (
	LeasePurposeCooldownRetest   = "cooldown_retest"
	LeasePurposeBackgroundProbe  = "background_probe"
)

// Outbox event types / statuses mirror the repository unions.
const (
	OutboxEventTypeDispatchRevisionChanged = "dispatch_revision_changed"
	OutboxEventTypeIncidentChanged         = "incident_changed"
	OutboxStatusPending                    = "pending"
	OutboxStatusProcessing                 = "processing"
	OutboxStatusDispatched                 = "dispatched"
)

// CAS incident statuses mirror CompareAndSetAccountCircuitIncidentResult['status'].
const (
	CASApplied                = "applied"
	CASIdempotent             = "idempotent"
	CASConflict               = "cas_conflict"
	CASStaleDispatchRevision  = "stale_dispatch_revision"
	CASAccountNotFound        = "account_not_found"
)

// IncidentRecord mirrors AccountCircuitIncidentRecord.
type IncidentRecord struct {
	CircuitScopeKey                 string   `json:"circuitScopeKey"`
	AccountID                       string   `json:"accountId"`
	AccountRuntimeKey               string   `json:"accountRuntimeKey"`
	ScopeKind                       string   `json:"scopeKind"`
	KeyFingerprint                  *string  `json:"keyFingerprint,omitempty"`
	ProtocolCode                    *string  `json:"protocolCode,omitempty"`
	RequestLane                     *string  `json:"requestLane,omitempty"`
	ModelFamily                     *string  `json:"modelFamily,omitempty"`
	ClientModel                     *string  `json:"clientModel,omitempty"`
	CapabilityHash                  *string  `json:"capabilityHash,omitempty"`
	CredentialSourceAccountID       *string  `json:"credentialSourceAccountId,omitempty"`
	ClientEndpointFamily            *string  `json:"clientEndpointFamily,omitempty"`
	FinalUpstreamModel              *string  `json:"finalUpstreamModel,omitempty"`
	UpstreamEndpointMode            *string  `json:"upstreamEndpointMode,omitempty"`
	IncidentID                      string   `json:"incidentId"`
	ParentIncidentID                *string  `json:"parentIncidentId,omitempty"`
	ChildIncidentIDs                []string `json:"childIncidentIds"`
	CausedByTerminalOutcomeID       *string  `json:"causedByTerminalOutcomeId,omitempty"`
	State                           string   `json:"state"`
	FailureScope                    *string  `json:"failureScope,omitempty"`
	Generation                      int64    `json:"generation"`
	DispatchRevision                int64    `json:"dispatchRevision"`
	LedgerRevision                  int64    `json:"ledgerRevision"`
	ProjectedLedgerRevision         int64    `json:"projectedLedgerRevision"`
	TransitionID                    string   `json:"transitionId"`
	CooldownObservationGeneration   int64    `json:"cooldownObservationGeneration"`
	OpenUntilMs                     *int64   `json:"openUntilMs,omitempty"`
	NextTransitionAtMs              *int64   `json:"nextTransitionAtMs,omitempty"`
	LeaseID                         *string  `json:"leaseId,omitempty"`
	LeasePurpose                    *string  `json:"leasePurpose,omitempty"`
	LeaseOwnerRunID                 *string  `json:"leaseOwnerRunId,omitempty"`
	LeaseUntilMs                    *int64   `json:"leaseUntilMs,omitempty"`
	AttemptStartedAtMs              *int64   `json:"attemptStartedAtMs,omitempty"`
	AttemptHardDeadlineMs           *int64   `json:"attemptHardDeadlineMs,omitempty"`
	UpstreamAttemptObserved         bool     `json:"upstreamAttemptObserved"`
	BackoffLevel                    int64    `json:"backoffLevel"`
	ConsecutiveFailures             int64    `json:"consecutiveFailures"`
	ConfirmationFailuresRequired    int64    `json:"confirmationFailuresRequired"`
	ConfirmationFailureEvidenceKeys []string `json:"confirmationFailureEvidenceKeys"`
	RecoveringSuccesses             int64    `json:"recoveringSuccesses"`
	LastFailureClass                *string  `json:"lastFailureClass,omitempty"`
	RetainedUntilMs                 *int64   `json:"retainedUntilMs,omitempty"`
	CreatedAtMs                     int64    `json:"createdAtMs"`
	UpdatedAtMs                     int64    `json:"updatedAtMs"`
}

// CompareAndSetIncidentInput mirrors CompareAndSetAccountCircuitIncidentInput.
type CompareAndSetIncidentInput struct {
	AccountID                       string   `json:"accountId"`
	AccountRuntimeKey               string   `json:"accountRuntimeKey"`
	CircuitScopeKey                 string   `json:"circuitScopeKey"`
	ScopeKind                       string   `json:"scopeKind"`
	KeyFingerprint                  *string  `json:"keyFingerprint,omitempty"`
	ProtocolCode                    *string  `json:"protocolCode,omitempty"`
	RequestLane                     *string  `json:"requestLane,omitempty"`
	ModelFamily                     *string  `json:"modelFamily,omitempty"`
	ClientModel                     *string  `json:"clientModel,omitempty"`
	CapabilityHash                  *string  `json:"capabilityHash,omitempty"`
	CredentialSourceAccountID       *string  `json:"credentialSourceAccountId,omitempty"`
	ClientEndpointFamily            *string  `json:"clientEndpointFamily,omitempty"`
	FinalUpstreamModel              *string  `json:"finalUpstreamModel,omitempty"`
	UpstreamEndpointMode            *string  `json:"upstreamEndpointMode,omitempty"`
	IncidentID                      string   `json:"incidentId"`
	ParentIncidentID                *string  `json:"parentIncidentId,omitempty"`
	ChildIncidentIDs                []string `json:"childIncidentIds,omitempty"`
	CausedByTerminalOutcomeID       *string  `json:"causedByTerminalOutcomeId,omitempty"`
	State                           string   `json:"state"`
	FailureScope                    *string  `json:"failureScope,omitempty"`
	Generation                      int64    `json:"generation"`
	DispatchRevision                int64    `json:"dispatchRevision"`
	ExpectedLedgerRevision          *int64   `json:"expectedLedgerRevision"`
	TransitionID                    string   `json:"transitionId"`
	CooldownObservationGeneration   *int64   `json:"cooldownObservationGeneration,omitempty"`
	OpenUntilMs                     *int64   `json:"openUntilMs,omitempty"`
	NextTransitionAtMs              *int64   `json:"nextTransitionAtMs,omitempty"`
	LeaseID                         *string  `json:"leaseId,omitempty"`
	LeasePurpose                    *string  `json:"leasePurpose,omitempty"`
	LeaseOwnerRunID                 *string  `json:"leaseOwnerRunId,omitempty"`
	LeaseUntilMs                    *int64   `json:"leaseUntilMs,omitempty"`
	AttemptStartedAtMs              *int64   `json:"attemptStartedAtMs,omitempty"`
	AttemptHardDeadlineMs           *int64   `json:"attemptHardDeadlineMs,omitempty"`
	UpstreamAttemptObserved         *bool    `json:"upstreamAttemptObserved,omitempty"`
	BackoffLevel                    *int64   `json:"backoffLevel,omitempty"`
	ConsecutiveFailures             *int64   `json:"consecutiveFailures,omitempty"`
	ConfirmationFailuresRequired    *int64   `json:"confirmationFailuresRequired,omitempty"`
	ConfirmationFailureEvidenceKeys []string `json:"confirmationFailureEvidenceKeys,omitempty"`
	RecoveringSuccesses             *int64   `json:"recoveringSuccesses,omitempty"`
	LastFailureClass                *string  `json:"lastFailureClass,omitempty"`
	RetainedUntilMs                 *int64   `json:"retainedUntilMs,omitempty"`
	StateUpdatedAtMs                *int64   `json:"stateUpdatedAtMs,omitempty"`
	NowMs                           *int64   `json:"nowMs,omitempty"`
}

// CompareAndSetIncidentResult mirrors CompareAndSetAccountCircuitIncidentResult.
type CompareAndSetIncidentResult struct {
	Status                  string          `json:"status"`
	Incident                *IncidentRecord `json:"incident,omitempty"`
	CurrentDispatchRevision int64           `json:"currentDispatchRevision"`
}

// RebuildPageInput mirrors the loadRebuildPage input.
type RebuildPageInput struct {
	NowMs             int64
	AfterUpdatedAtMs  *int64
	AfterCircuitScopeKey *string
	Limit             int
}

// RebuildPage mirrors AccountCircuitIncidentRebuildPage.
type RebuildPage struct {
	Items      []IncidentRecord
	NextCursor *RebuildCursor
}

// RebuildCursor mirrors { updatedAtMs, circuitScopeKey }.
type RebuildCursor struct {
	UpdatedAtMs     int64
	CircuitScopeKey string
}

// OutboxEvent mirrors the claimed AccountCircuitOutboxRecord surface the
// bridge consumes.
type OutboxEvent struct {
	EventID          string
	ProjectionKey    string
	EventType        string
	AccountID        string
	AccountRuntimeKey string
	CircuitScopeKey  *string
	IncidentID       *string
	TransitionID     string
	DispatchRevision int64
	Generation       *int64
	LedgerRevision   *int64
	ClaimToken       *string
}

// AckOutboxInput mirrors ack_account_circuit_outbox input.
type AckOutboxInput struct {
	EventID          string
	ProjectionKey    string
	ClaimToken       string
	AcknowledgedAtMs int64
}

// AckOutboxResult mirrors the ack result.
type AckOutboxResult struct {
	Acknowledged bool
}

// ReleaseOutboxInput mirrors release_account_circuit_outbox_for_replay input.
type ReleaseOutboxInput struct {
	EventID      string
	ClaimToken   string
	ErrorClass   string
	NowMs        int64
	RetryDelayMs int64
}

// ClaimOutboxInput mirrors claim_account_circuit_outbox input.
type ClaimOutboxInput struct {
	OwnerID string
	NowMs   int64
	LeaseMs int64
	Limit   int
}

// ListIncidentsByRuntimeKeysInput mirrors the requestDb operation input.
type ListIncidentsByRuntimeKeysInput struct {
	AccountRuntimeKeys   []string
	IncludeRetainedClosed bool
	NowMs                *int64
}

// ControlPlaneDB is the requestDb port the bridge persists through. The
// durable repository implementation lands with the storage work package;
// tests inject mocks.
type ControlPlaneDB interface {
	CompareAndSetIncident(ctx context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error)
	ListIncidentsForRebuild(ctx context.Context, input RebuildPageInput) (RebuildPage, error)
	ListIncidentsByRuntimeKeys(ctx context.Context, input ListIncidentsByRuntimeKeysInput) ([]IncidentRecord, error)
	GetIncidentByScopeKey(ctx context.Context, circuitScopeKey string) (*IncidentRecord, error)
	ClaimOutbox(ctx context.Context, input ClaimOutboxInput) ([]OutboxEvent, error)
	AckOutbox(ctx context.Context, input AckOutboxInput) (AckOutboxResult, error)
	ReleaseOutboxForReplay(ctx context.Context, input ReleaseOutboxInput) error
}

// Rebuild reasons mirror AccountCircuitControlPlaneRebuildResult['reason'].
const (
	RebuildReasonRebuilding         = "runtime_state_rebuilding"
	RebuildReasonRebuildFailed      = "runtime_state_rebuild_failed"
	RebuildReasonRebuildTimeout     = "runtime_state_rebuild_timeout"
	RebuildReasonInvalidCursor      = "runtime_state_rebuild_invalid_cursor"
	RebuildReasonCapacityExhausted  = "runtime_state_rebuild_capacity_exhausted"
)

// RebuildResult mirrors AccountCircuitControlPlaneRebuildResult.
type RebuildResult struct {
	Loaded  int64
	Blocked bool
	Reason  string
}

// Public summary statuses mirror PublicAccountCircuitSummary.
const (
	PublicSummaryStatusNormal     = "normal"
	PublicSummaryStatusVerifying  = "verifying"
	PublicSummaryStatusAvoided    = "avoided"
	PublicSummaryStatusRecovering = "recovering"
)

// PublicSummary mirrors PublicAccountCircuitSummary.
type PublicSummary struct {
	Status      string  `json:"status"`
	Reason      string  `json:"reason,omitempty"`
	Since       string  `json:"since,omitempty"`
	NextCheckAt string  `json:"nextCheckAt,omitempty"`
}

// BridgeOptions mirrors AccountCircuitControlPlaneBridgeOptions.
type BridgeOptions struct {
	Store                 Store
	DB                    ControlPlaneDB
	OwnerID               string
	RetryDelayMs          int64
	MaxPersistAttempts    int64
	ClosedRetentionMs     int64
	RebuildPageSize       int64
	RebuildMaxPages       int64
	RebuildPageTimeoutMs  int64
	RebuildTotalTimeoutMs int64
	Now                   func() int64
	MonotonicNow          func() time.Duration
	PersistIncident       func(ctx context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error)
	LoadRebuildPage       func(ctx context.Context, input RebuildPageInput) (RebuildPage, error)
	LoadAccountIncidents  func(ctx context.Context, accountRuntimeKey string) ([]IncidentRecord, error)
	// Sleep replaces the retry backoff sleep in tests.
	Sleep func(ctx context.Context, delay time.Duration) error
	// NewTimer overrides retry timer creation (Node setTimeout). The done
	// channel closes after the delay; stop cancels it.
	NewTimer func(delay time.Duration) (done <-chan struct{}, stop func())
}

type observeInput struct {
	scope Scope
	state State
}

type scopeRetryState struct {
	backoff   int64
	timerStop func()
}

// Bridge mirrors AccountCircuitControlPlaneBridge: coalesces runtime
// transitions to the latest state per scope and serializes bounded
// persistence attempts into the DB ledger. Runtime state remains the fast
// path; the outbox projector is the only component that acknowledges durable
// projection progress.
type Bridge struct {
	store                 Store
	db                    ControlPlaneDB
	ownerID               string
	retryDelayMs          int64
	maxPersistAttempts    int64
	closedRetentionMs     int64
	rebuildPageSize       int64
	rebuildMaxPages       int64
	rebuildPageTimeoutMs  int64
	rebuildTotalTimeoutMs int64
	now                   func() int64
	monotonicNow          func() time.Duration
	monotonicBase         time.Time
	persistIncident       func(ctx context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error)
	loadRebuildPage       func(ctx context.Context, input RebuildPageInput) (RebuildPage, error)
	loadAccountIncidents  func(ctx context.Context, accountRuntimeKey string) ([]IncidentRecord, error)
	sleep                 func(ctx context.Context, delay time.Duration) error
	newTimer              func(delay time.Duration) (done <-chan struct{}, stop func())

	mu                       sync.Mutex
	pending                  map[string]observeInput
	workers                  map[string]*scopeWorker
	retryBackoffs            map[string]*scopeRetryState
	persistenceFailures      map[string]string
	ledgerRevisions          map[string]int64
	dispatchRevisions        map[string]int64
	rebuilding               bool
	globallyReady            bool
	readyAccountRuntimeKeys  map[string]struct{}
	accountLoads             map[string]*accountLoad
	rebuildInFlight          *rebuildCall
	reconcileCursor          *RebuildCursor
	stopped                  bool
	stopCh                   chan struct{}
	stopOnce                 sync.Once
}

type scopeWorker struct {
	done chan struct{}
}

type accountLoad struct {
	done chan struct{}
	ready bool
}

type rebuildCall struct {
	done chan struct{}
	result RebuildResult
}

// NewBridge mirrors new AccountCircuitControlPlaneBridge.
func NewBridge(options BridgeOptions) (*Bridge, error) {
	if options.Store == nil {
		return nil, errors.New("control-plane 数值必须为正")
	}
	if options.DB == nil {
		return nil, errors.New("control-plane 数值必须为正")
	}
	ownerID := strings.TrimSpace(options.OwnerID)
	if ownerID == "" {
		ownerID = fmt.Sprintf("circuit-bridge:%s", defaultCreateID())
	}
	retryDelayMs, err := positiveBridgeNumber(options.RetryDelayMs, 1_000)
	if err != nil {
		return nil, err
	}
	maxPersistAttempts, err := positiveBridgeNumber(options.MaxPersistAttempts, 3)
	if err != nil {
		return nil, err
	}
	closedRetentionMs, err := positiveBridgeNumber(options.ClosedRetentionMs, 5*60_000)
	if err != nil {
		return nil, err
	}
	rebuildPageSize, err := positiveBridgeNumber(options.RebuildPageSize, 500)
	if err != nil {
		return nil, err
	}
	rebuildMaxPages, err := positiveBridgeNumber(options.RebuildMaxPages, 200)
	if err != nil {
		return nil, err
	}
	rebuildPageTimeoutMs, err := positiveBridgeNumber(options.RebuildPageTimeoutMs, 2_000)
	if err != nil {
		return nil, err
	}
	rebuildTotalTimeoutMs, err := positiveBridgeNumber(options.RebuildTotalTimeoutMs, 15_000)
	if err != nil {
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = defaultNowMs
	}
	monotonicBase := time.Now()
	monotonicNow := options.MonotonicNow
	if monotonicNow == nil {
		monotonicNow = func() time.Duration { return time.Since(monotonicBase) }
	}
	sleep := options.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	newTimer := options.NewTimer
	if newTimer == nil {
		newTimer = func(delay time.Duration) (<-chan struct{}, func()) {
			done := make(chan struct{})
			timer := time.AfterFunc(delay, func() { close(done) })
			return done, func() { timer.Stop() }
		}
	}
	persistIncident := options.PersistIncident
	if persistIncident == nil {
		persistIncident = func(ctx context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
			return options.DB.CompareAndSetIncident(ctx, input)
		}
	}
	loadRebuildPage := options.LoadRebuildPage
	if loadRebuildPage == nil {
		loadRebuildPage = func(ctx context.Context, input RebuildPageInput) (RebuildPage, error) {
			return options.DB.ListIncidentsForRebuild(ctx, input)
		}
	}
	loadAccountIncidents := options.LoadAccountIncidents
	if loadAccountIncidents == nil {
		loadAccountIncidents = func(ctx context.Context, accountRuntimeKey string) ([]IncidentRecord, error) {
			return options.DB.ListIncidentsByRuntimeKeys(ctx, ListIncidentsByRuntimeKeysInput{
				AccountRuntimeKeys:    []string{accountRuntimeKey},
				IncludeRetainedClosed: true,
				NowMs:                 int64Ptr(now()),
			})
		}
	}
	return &Bridge{
		store:                 options.Store,
		db:                    options.DB,
		ownerID:               ownerID,
		retryDelayMs:          retryDelayMs,
		maxPersistAttempts:    maxPersistAttempts,
		closedRetentionMs:     closedRetentionMs,
		rebuildPageSize:       rebuildPageSize,
		rebuildMaxPages:       rebuildMaxPages,
		rebuildPageTimeoutMs:  rebuildPageTimeoutMs,
		rebuildTotalTimeoutMs: rebuildTotalTimeoutMs,
		now:                   now,
		monotonicNow:          monotonicNow,
		persistIncident:       persistIncident,
		loadRebuildPage:       loadRebuildPage,
		loadAccountIncidents:  loadAccountIncidents,
		sleep:                 sleep,
		newTimer:              newTimer,
		pending:               map[string]observeInput{},
		workers:               map[string]*scopeWorker{},
		retryBackoffs:         map[string]*scopeRetryState{},
		persistenceFailures:   map[string]string{},
		ledgerRevisions:       map[string]int64{},
		dispatchRevisions:     map[string]int64{},
		readyAccountRuntimeKeys: map[string]struct{}{},
		accountLoads:            map[string]*accountLoad{},
		stopCh:                  make(chan struct{}),
	}, nil
}

func positiveBridgeNumber(value, fallback int64) (int64, error) {
	effective := value
	if value == 0 {
		effective = fallback
	}
	if effective <= 0 || math.IsInf(float64(effective), 0) {
		return 0, errors.New("control-plane 数值必须为正")
	}
	return effective, nil
}

// Close stops retry timers; in-flight workers finish their current attempt.
func (b *Bridge) Close() {
	b.stopOnce.Do(func() { close(b.stopCh) })
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = true
	for key, retry := range b.retryBackoffs {
		if retry.timerStop != nil {
			retry.timerStop()
		}
		delete(b.retryBackoffs, key)
	}
}

// IsReady mirrors isReady: the global cold-start gate.
func (b *Bridge) IsReady() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.globallyReady
}

// IsAccountReady mirrors isAccountReady.
func (b *Bridge) IsAccountReady(accountRuntimeKey string) (bool, error) {
	normalized, err := requiredRuntimeKey(accountRuntimeKey)
	if err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ready := b.readyAccountRuntimeKeys[normalized]
	if !b.globallyReady && !ready {
		return false, nil
	}
	return !b.hasAccountPersistenceFailureLocked(normalized), nil
}

func (b *Bridge) hasAccountPersistenceFailureLocked(accountRuntimeKey string) bool {
	for _, failed := range b.persistenceFailures {
		if failed == accountRuntimeKey {
			return true
		}
	}
	return false
}

// EnsureAccountReady mirrors ensureAccountReady.
func (b *Bridge) EnsureAccountReady(ctx context.Context, accountRuntimeKey string) (bool, error) {
	normalized, err := requiredRuntimeKey(accountRuntimeKey)
	if err != nil {
		return false, err
	}
	if ready, err := b.IsAccountReady(normalized); err != nil {
		return false, err
	} else if ready {
		return true, nil
	}
	for {
		b.mu.Lock()
		if load, ok := b.accountLoads[normalized]; ok {
			b.mu.Unlock()
			select {
			case <-load.done:
				return load.ready, nil
			case <-ctx.Done():
				return false, ctx.Err()
			case <-b.stopCh:
				return false, errors.New("账户 circuit runtime key 不能为空")
			}
		}
		load := &accountLoad{done: make(chan struct{})}
		b.accountLoads[normalized] = load
		b.mu.Unlock()

		ready := b.performAccountLoad(ctx, normalized)
		b.mu.Lock()
		load.ready = ready
		if current, ok := b.accountLoads[normalized]; ok && current == load {
			delete(b.accountLoads, normalized)
		}
		b.mu.Unlock()
		close(load.done)
		return ready, nil
	}
}

// Observe mirrors observe: coalesces to the latest state per scope key.
func (b *Bridge) Observe(scope Scope, state State) {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	b.pending[state.ScopeKey] = observeInput{scope: scope, state: state}
	b.mu.Unlock()
	b.startScopeWorker(state.ScopeKey)
}

// Rebuild mirrors rebuild with single-flight semantics.
func (b *Bridge) Rebuild(ctx context.Context) (RebuildResult, error) {
	b.mu.Lock()
	if b.rebuildInFlight != nil {
		call := b.rebuildInFlight
		b.mu.Unlock()
		select {
		case <-call.done:
			return call.result, nil
		case <-ctx.Done():
			return RebuildResult{}, ctx.Err()
		}
	}
	call := &rebuildCall{done: make(chan struct{})}
	b.rebuildInFlight = call
	b.mu.Unlock()

	result := b.performRebuild(ctx)
	b.mu.Lock()
	if b.rebuildInFlight == call {
		b.rebuildInFlight = nil
	}
	b.mu.Unlock()
	call.result = result
	close(call.done)
	return result, nil
}

func (b *Bridge) performRebuild(ctx context.Context) RebuildResult {
	b.mu.Lock()
	b.rebuilding = true
	b.mu.Unlock()
	loaded := int64(0)
	var cursor *RebuildCursor
	hierarchyScopeKeys := map[string]string{}
	var deferredParents []IncidentRecord
	startedAt := b.monotonicNow()
	defer func() {
		b.mu.Lock()
		b.rebuilding = false
		b.mu.Unlock()
	}()
	for pageNumber := int64(1); ; pageNumber++ {
		if pageNumber > b.rebuildMaxPages {
			return rebuildFailure(loaded, RebuildReasonInvalidCursor)
		}
		remaining := b.rebuildTotalTimeoutMs - durationToMs(b.monotonicNow() - startedAt)
		if remaining <= 0 {
			return rebuildFailure(loaded, RebuildReasonRebuildTimeout)
		}
		pageInput := RebuildPageInput{NowMs: b.now(), Limit: int(b.rebuildPageSize)}
		if cursor != nil {
			afterUpdatedAt := cursor.UpdatedAtMs
			afterScopeKey := cursor.CircuitScopeKey
			pageInput.AfterUpdatedAtMs = &afterUpdatedAt
			pageInput.AfterCircuitScopeKey = &afterScopeKey
		}
		page, err := b.withinTimeout(ctx, func(ctx context.Context) (RebuildPage, error) {
			return b.loadRebuildPage(ctx, pageInput)
		}, int64Min(b.rebuildPageTimeoutMs, remaining), RebuildReasonRebuildTimeout)
		if err != nil {
			if rebuildErr, ok := err.(*rebuildError); ok {
				return rebuildFailure(loaded, rebuildErr.reason)
			}
			return rebuildFailure(loaded, RebuildReasonRebuildFailed)
		}
		for _, incident := range page.Items {
			hierarchyScopeKeys[incidentHierarchyKey(incident.AccountRuntimeKey, incident.IncidentID)] = incident.CircuitScopeKey
		}
		for _, incident := range page.Items {
			b.mu.Lock()
			b.ledgerRevisions[incident.CircuitScopeKey] = incident.LedgerRevision
			b.dispatchRevisions[incident.AccountID] = incident.DispatchRevision
			b.mu.Unlock()
			if len(incident.ChildIncidentIDs) > 0 {
				deferredParents = append(deferredParents, incident)
				continue
			}
			restored, err := b.store.Restore(ctx, IncidentToRuntimeState(incident, hierarchyScopeKeys), int64Ptr(b.now()))
			if err != nil {
				return rebuildFailure(loaded, RebuildReasonRebuildFailed)
			}
			b.observeRestoredRelationships(restored)
			if restored.Status == MutationCapacityExhausted {
				return rebuildFailure(loaded, RebuildReasonCapacityExhausted)
			}
			loaded++
		}
		nextCursor := page.NextCursor
		if nextCursor == nil {
			break
		}
		if cursor != nil && compareCursor(nextCursor, cursor) <= 0 {
			return rebuildFailure(loaded, RebuildReasonInvalidCursor)
		}
		cursor = nextCursor
	}
	for _, incident := range deferredParents {
		restored, err := b.store.Restore(ctx, IncidentToRuntimeState(incident, hierarchyScopeKeys), int64Ptr(b.now()))
		if err != nil {
			return rebuildFailure(loaded, RebuildReasonRebuildFailed)
		}
		b.observeRestoredRelationships(restored)
		if restored.Status == MutationCapacityExhausted {
			return rebuildFailure(loaded, RebuildReasonCapacityExhausted)
		}
		loaded++
	}
	remaining := b.rebuildTotalTimeoutMs - durationToMs(b.monotonicNow() - startedAt)
	if remaining <= 0 {
		return rebuildFailure(loaded, RebuildReasonRebuildTimeout)
	}
	if err := b.retryPendingImmediately(ctx, remaining); err != nil {
		if rebuildErr, ok := err.(*rebuildError); ok {
			return rebuildFailure(loaded, rebuildErr.reason)
		}
		return rebuildFailure(loaded, RebuildReasonRebuildFailed)
	}
	b.mu.Lock()
	b.globallyReady = true
	b.mu.Unlock()
	return RebuildResult{Loaded: loaded, Blocked: false}
}

// ReconcileActive mirrors reconcileActive: replays one bounded ledger page
// without closing the readiness gate.
func (b *Bridge) ReconcileActive(ctx context.Context, limit int) (int64, error) {
	b.mu.Lock()
	if b.rebuilding || !b.globallyReady {
		b.mu.Unlock()
		return 0, nil
	}
	cursor := b.reconcileCursor
	b.mu.Unlock()
	pageInput := RebuildPageInput{
		NowMs: b.now(),
		Limit: limit,
	}
	if limit <= 0 {
		return 0, errors.New("control-plane 数值必须为正")
	}
	if cursor != nil {
		afterUpdatedAt := cursor.UpdatedAtMs
		afterScopeKey := cursor.CircuitScopeKey
		pageInput.AfterUpdatedAtMs = &afterUpdatedAt
		pageInput.AfterCircuitScopeKey = &afterScopeKey
	}
	page, err := b.withinTimeout(ctx, func(ctx context.Context) (RebuildPage, error) {
		return b.loadRebuildPage(ctx, pageInput)
	}, b.rebuildPageTimeoutMs, RebuildReasonRebuildTimeout)
	if err != nil {
		if rebuildErr, ok := err.(*rebuildError); ok {
			return 0, errors.New(rebuildErr.reason)
		}
		return 0, err
	}
	var repaired int64
	hierarchyScopeKeys := incidentScopeKeyMap(page.Items)
	for _, incident := range page.Items {
		if hasUnresolvedChildIncident(incident, hierarchyScopeKeys) {
			incidents, err := b.loadAccountIncidents(ctx, incident.AccountRuntimeKey)
			if err != nil {
				return repaired, err
			}
			addIncidentScopeKeys(hierarchyScopeKeys, incidents)
		}
		restored, err := b.store.Restore(ctx, IncidentToRuntimeState(incident, hierarchyScopeKeys), int64Ptr(b.now()))
		if err != nil {
			return repaired, err
		}
		b.observeRestoredRelationships(restored)
		if restored.Status == MutationCapacityExhausted {
			return repaired, errors.New("账户 circuit runtime store 对账容量不足")
		}
		b.mu.Lock()
		b.ledgerRevisions[incident.CircuitScopeKey] = incident.LedgerRevision
		b.dispatchRevisions[incident.AccountID] = incident.DispatchRevision
		b.mu.Unlock()
		repaired++
	}
	b.mu.Lock()
	b.reconcileCursor = page.NextCursor
	b.mu.Unlock()
	return repaired, nil
}

func (b *Bridge) performAccountLoad(ctx context.Context, accountRuntimeKey string) bool {
	incidents, err := b.withinTimeoutList(ctx, func(ctx context.Context) ([]IncidentRecord, error) {
		return b.loadAccountIncidents(ctx, accountRuntimeKey)
	}, b.rebuildPageTimeoutMs, RebuildReasonRebuildTimeout)
	if err != nil {
		return false
	}
	hierarchyScopeKeys := incidentScopeKeyMap(incidents)
	var leafIncidents, parentIncidents []IncidentRecord
	for _, incident := range incidents {
		if len(incident.ChildIncidentIDs) == 0 {
			leafIncidents = append(leafIncidents, incident)
		} else {
			parentIncidents = append(parentIncidents, incident)
		}
	}
	orderedIncidents := append(append([]IncidentRecord{}, leafIncidents...), parentIncidents...)
	for _, incident := range orderedIncidents {
		if incident.AccountRuntimeKey != accountRuntimeKey {
			return false
		}
		restored, err := b.store.Restore(ctx, IncidentToRuntimeState(incident, hierarchyScopeKeys), int64Ptr(b.now()))
		if err != nil {
			return false
		}
		b.observeRestoredRelationships(restored)
		if restored.Status == MutationCapacityExhausted {
			return false
		}
		b.mu.Lock()
		b.ledgerRevisions[incident.CircuitScopeKey] = incident.LedgerRevision
		b.dispatchRevisions[incident.AccountID] = incident.DispatchRevision
		b.mu.Unlock()
	}
	b.mu.Lock()
	b.readyAccountRuntimeKeys[accountRuntimeKey] = struct{}{}
	ready := !b.hasAccountPersistenceFailureLocked(accountRuntimeKey)
	b.mu.Unlock()
	return ready
}

// ProjectPending mirrors projectPending.
func (b *Bridge) ProjectPending(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, errors.New("control-plane 数值必须为正")
	}
	claims, err := b.db.ClaimOutbox(ctx, ClaimOutboxInput{
		OwnerID: b.ownerID,
		NowMs:   b.now(),
		LeaseMs: 30_000,
		Limit:   limit,
	})
	if err != nil {
		return 0, err
	}
	var acknowledged int64
	for _, event := range claims {
		if err := b.projectOutboxEvent(ctx, event); err != nil {
			if releaseErr := b.db.ReleaseOutboxForReplay(ctx, ReleaseOutboxInput{
				EventID:      event.EventID,
				ClaimToken:   derefString(event.ClaimToken),
				ErrorClass:   classifyError(err),
				NowMs:        b.now(),
				RetryDelayMs: b.retryDelayMs,
			}); releaseErr != nil {
				return acknowledged, releaseErr
			}
			continue
		}
		ack, err := b.db.AckOutbox(ctx, AckOutboxInput{
			EventID:          event.EventID,
			ProjectionKey:    event.ProjectionKey,
			ClaimToken:       derefString(event.ClaimToken),
			AcknowledgedAtMs: b.now(),
		})
		if err != nil {
			return acknowledged, err
		}
		if ack.Acknowledged {
			acknowledged++
		}
	}
	return acknowledged, nil
}

func (b *Bridge) projectOutboxEvent(ctx context.Context, event OutboxEvent) error {
	if event.EventType == OutboxEventTypeDispatchRevisionChanged {
		_, err := b.store.ReplaceAccountDispatchRevision(ctx, ReplaceAccountDispatchRevisionInput{
			AccountRuntimeKey: event.AccountRuntimeKey,
			DispatchRevision:  fmt.Sprintf("%d", event.DispatchRevision),
			TransitionID:      event.TransitionID,
			NowMs:             int64Ptr(b.now()),
		})
		return err
	}
	if event.CircuitScopeKey == nil || *event.CircuitScopeKey == "" {
		return errors.New("incident outbox 缺少 circuitScopeKey")
	}
	incident, err := b.db.GetIncidentByScopeKey(ctx, *event.CircuitScopeKey)
	if err != nil {
		return err
	}
	if incident == nil {
		return errors.New("incident outbox 对应 ledger 缺失")
	}
	hierarchyScopeKeys := incidentScopeKeyMap([]IncidentRecord{*incident})
	if hasUnresolvedChildIncident(*incident, hierarchyScopeKeys) {
		incidents, err := b.loadAccountIncidents(ctx, incident.AccountRuntimeKey)
		if err != nil {
			return err
		}
		addIncidentScopeKeys(hierarchyScopeKeys, incidents)
	}
	projected, err := b.store.Restore(ctx, IncidentToRuntimeState(*incident, hierarchyScopeKeys), int64Ptr(b.now()))
	if err != nil {
		return err
	}
	b.observeRestoredRelationships(projected)
	if projected.Status == MutationCapacityExhausted {
		return errors.New("runtime circuit projection capacity exhausted")
	}
	return nil
}

func (b *Bridge) persistWithRetry(ctx context.Context, scope Scope, state State) error {
	delay := b.retryDelayMs
	accountID, err := accountIDFromRuntimeKey(scope.AccountRuntimeKey)
	if err != nil {
		return err
	}
	desiredState := state
	for attempt := int64(1); attempt <= b.maxPersistAttempts; attempt++ {
		refreshed, err := b.refreshDesiredState(ctx, scope, desiredState)
		if err != nil {
			if attempt == b.maxPersistAttempts {
				break
			}
			if sleepErr := b.sleep(ctx, msToDuration(delay)); sleepErr != nil {
				return sleepErr
			}
			delay = int64Min(delay*2, 30_000)
			continue
		}
		if refreshed == nil {
			return nil
		}
		desiredState = *refreshed
		stateDispatchRevision, _ := parseSafeInteger(desiredState.DispatchRevision)
		dispatchRevision := int64(1)
		if stateDispatchRevision > 0 && stateDispatchRevision == math.Trunc(stateDispatchRevision) && stateDispatchRevision <= 9007199254740991 {
			dispatchRevision = int64(stateDispatchRevision)
		} else {
			b.mu.Lock()
			if current, ok := b.dispatchRevisions[accountID]; ok {
				dispatchRevision = current
			}
			b.mu.Unlock()
		}
		b.mu.Lock()
		b.dispatchRevisions[accountID] = dispatchRevision
		expectedLedger := b.ledgerRevisions[desiredState.ScopeKey]
		b.mu.Unlock()
		transitionID := desiredState.TransitionID
		if transitionID == "" {
			transitionID = fmt.Sprintf("rebuild:%s:%d", desiredState.ScopeKey, desiredState.Generation)
		}
		persistInput, err := buildPersistIncidentInput(b, scope, desiredState, accountID, dispatchRevision, expectedLedger, transitionID)
		if err != nil {
			return err
		}
		persisted, err := b.persistIncident(ctx, persistInput)
		if err != nil {
			if attempt == b.maxPersistAttempts {
				break
			}
			if sleepErr := b.sleep(ctx, msToDuration(delay)); sleepErr != nil {
				return sleepErr
			}
			delay = int64Min(delay*2, 30_000)
			continue
		}
		// Physical account cleanup is a terminal outcome for late runtime
		// observations; do not retain a pending item, do not record
		// dispatch/ledger revisions and do not schedule retries.（对齐归档热修
		// migration-backup/node/final-archive/backend/src/modules/gateway/runtime/
		// account-circuit-control-plane-bridge.ts persistWithRetry；jobs 侧
		// internal/circuitstore 同键读面注释互指，跨 module 不可 import。）
		if persisted.Status == CASAccountNotFound {
			return nil
		}
		b.mu.Lock()
		b.dispatchRevisions[accountID] = persisted.CurrentDispatchRevision
		if persisted.Incident != nil {
			b.ledgerRevisions[desiredState.ScopeKey] = persisted.Incident.LedgerRevision
		}
		b.mu.Unlock()
		if persisted.Status == CASStaleDispatchRevision {
			return nil
		}
		if persisted.Status == CASConflict {
			if persisted.Incident != nil {
				runtimeState, err := b.refreshDesiredState(ctx, scope, desiredState)
				if err != nil {
					if attempt == b.maxPersistAttempts {
						break
					}
					if sleepErr := b.sleep(ctx, msToDuration(delay)); sleepErr != nil {
						return sleepErr
					}
					delay = int64Min(delay*2, 30_000)
					continue
				}
				if runtimeState == nil {
					return nil
				}
				if incidentMatchesRuntimeState(persisted.Incident, *runtimeState) {
					return nil
				}
				if incidentIsNewerThanRuntimeState(persisted.Incident, *runtimeState) {
					restored, err := b.store.Restore(ctx, IncidentToRuntimeState(*persisted.Incident, incidentScopeKeyMapFromRuntimeState(*runtimeState)), int64Ptr(b.now()))
					if err != nil {
						if attempt == b.maxPersistAttempts {
							break
						}
						if sleepErr := b.sleep(ctx, msToDuration(delay)); sleepErr != nil {
							return sleepErr
						}
						delay = int64Min(delay*2, 30_000)
						continue
					}
					b.observeRestoredRelationships(restored)
					if restored.Status == MutationCapacityExhausted {
						return errors.New("账户 circuit runtime store 对账容量不足")
					}
					if incidentMatchesRuntimeState(persisted.Incident, restored.State) {
						return nil
					}
					desiredState = restored.State
				} else {
					desiredState = *runtimeState
				}
			}
			if attempt == b.maxPersistAttempts {
				break
			}
			if sleepErr := b.sleep(ctx, msToDuration(int64Min(delay, 250))); sleepErr != nil {
				return sleepErr
			}
			delay = int64Min(delay*2, 30_000)
			continue
		}
		return nil
	}
	return errors.New("账户 circuit control-plane 持久化重试耗尽")
}

func buildPersistIncidentInput(
	b *Bridge,
	scope Scope,
	desiredState State,
	accountID string,
	dispatchRevision int64,
	expectedLedger int64,
	transitionID string,
) (CompareAndSetIncidentInput, error) {
	confirmationFailuresRequired, err := NormalizeConfirmationFailuresRequired(desiredState.ConfirmationFailuresRequired, LegacyConfirmationFailuresRequired)
	if err != nil {
		return CompareAndSetIncidentInput{}, err
	}
	evidenceKeys, err := FailureEvidenceKeysOf(desiredState)
	if err != nil {
		return CompareAndSetIncidentInput{}, err
	}
	consecutiveFailures, err := ConfirmationFailureCountOf(desiredState)
	if err != nil {
		return CompareAndSetIncidentInput{}, err
	}
	var expectedLedgerRevision *int64
	expectedLedgerRevision = &expectedLedger
	input := CompareAndSetIncidentInput{
		AccountID:                       accountID,
		AccountRuntimeKey:               scope.AccountRuntimeKey,
		CircuitScopeKey:                 desiredState.ScopeKey,
		ScopeKind:                       scope.Kind,
		IncidentID:                      durableIncidentID(desiredState),
		ChildIncidentIDs:                []string(desiredState.ChildIncidentIDs),
		State:                           desiredState.Phase,
		Generation:                      desiredState.Generation,
		DispatchRevision:                dispatchRevision,
		ExpectedLedgerRevision:          expectedLedgerRevision,
		TransitionID:                    transitionID,
		NextTransitionAtMs:              desiredState.RetryAtMs,
		OpenUntilMs:                     desiredState.RetryAtMs,
		BackoffLevel:                    int64Ptr(desiredState.BackoffAttempt),
		ConsecutiveFailures:             &consecutiveFailures,
		ConfirmationFailuresRequired:    &confirmationFailuresRequired,
		ConfirmationFailureEvidenceKeys: evidenceKeys,
		RecoveringSuccesses:             int64Ptr(desiredState.RecoverySuccessCount),
		UpstreamAttemptObserved:         boolPtr(true),
		StateUpdatedAtMs:                int64Ptr(desiredState.UpdatedAtMs),
		NowMs:                           int64Ptr(b.now()),
	}
	if scope.Kind == ScopeKindKey {
		input.KeyFingerprint = strPtr(scope.KeyFingerprint)
	}
	if scope.Kind == ScopeKindProtocolModel {
		input.ProtocolCode = strPtr(scope.ProtocolProfile)
		input.RequestLane = strPtr(scope.RequestLane)
		input.ModelFamily = strPtr(scope.ModelBucket)
	}
	if desiredState.ShadowedByIncidentID != nil {
		input.ParentIncidentID = desiredState.ShadowedByIncidentID
	}
	if desiredState.Lease != nil {
		input.LeaseID = strPtr(desiredState.Lease.LeaseID)
		input.LeasePurpose = strPtr(desiredState.Lease.Kind)
		input.LeaseOwnerRunID = strPtr(b.ownerID)
		input.LeaseUntilMs = int64Ptr(desiredState.Lease.LeaseUntilMs)
	}
	if desiredState.FailureReason != nil {
		input.LastFailureClass = strPtr(classifyFailure(*desiredState.FailureReason))
	}
	if desiredState.Phase == PhaseClosed {
		input.RetainedUntilMs = int64Ptr(b.now() + b.closedRetentionMs)
	}
	return input, nil
}

func (b *Bridge) refreshDesiredState(ctx context.Context, scope Scope, observedState State) (*State, error) {
	runtimeState, err := b.store.Get(ctx, scope, int64Ptr(b.now()))
	if err != nil {
		return nil, err
	}
	if runtimeState.DispatchRevision == "" || runtimeState.Generation < observedState.Generation {
		return &observedState, nil
	}
	if runtimeState.DispatchRevision != observedState.DispatchRevision {
		runtimeRevision, runtimeOK := parseSafeInteger(runtimeState.DispatchRevision)
		observedRevision, observedOK := parseSafeInteger(observedState.DispatchRevision)
		if runtimeOK && observedOK {
			if runtimeRevision > observedRevision {
				return nil, nil
			}
			return &observedState, nil
		}
		return nil, nil
	}
	if runtimeState.Generation > observedState.Generation {
		return &runtimeState, nil
	}
	if runtimeState.Generation == observedState.Generation &&
		(runtimeState.UpdatedAtMs > observedState.UpdatedAtMs || runtimeState.TransitionID != observedState.TransitionID) {
		return &runtimeState, nil
	}
	return &observedState, nil
}

func (b *Bridge) observeRestoredRelationships(result MutationResult) {
	for _, state := range result.RelatedStates.slice() {
		b.Observe(state.Scope, state)
	}
}

func (b *Bridge) startScopeWorker(scopeKey string) {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	if _, running := b.workers[scopeKey]; running {
		b.mu.Unlock()
		return
	}
	if retry, scheduled := b.retryBackoffs[scopeKey]; scheduled && retry.timerStop != nil {
		b.mu.Unlock()
		return
	}
	worker := &scopeWorker{done: make(chan struct{})}
	b.workers[scopeKey] = worker
	b.mu.Unlock()

	go func() {
		defer close(worker.done)
		b.drainScope(scopeKey)
		b.mu.Lock()
		if current, ok := b.workers[scopeKey]; ok && current == worker {
			delete(b.workers, scopeKey)
		}
		_, hasPending := b.pending[scopeKey]
		b.mu.Unlock()
		if hasPending {
			b.scheduleScopeRetry(scopeKey)
		}
	}()
}

func (b *Bridge) drainScope(scopeKey string) {
	for {
		b.mu.Lock()
		current, ok := b.pending[scopeKey]
		if !ok {
			b.mu.Unlock()
			return
		}
		delete(b.pending, scopeKey)
		b.mu.Unlock()

		err := b.persistWithRetry(context.Background(), current.scope, current.state)
		if err == nil {
			b.mu.Lock()
			delete(b.retryBackoffs, scopeKey)
			if _, hasPending := b.pending[scopeKey]; !hasPending {
				delete(b.persistenceFailures, scopeKey)
			}
			b.mu.Unlock()
			continue
		}
		b.mu.Lock()
		if _, hasPending := b.pending[scopeKey]; !hasPending {
			b.pending[scopeKey] = current
		}
		b.persistenceFailures[scopeKey] = current.scope.AccountRuntimeKey
		b.mu.Unlock()
		return
	}
}

func (b *Bridge) scheduleScopeRetry(scopeKey string) {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	if retry, ok := b.retryBackoffs[scopeKey]; ok && retry.timerStop != nil {
		b.mu.Unlock()
		return
	}
	delay := b.retryDelayMs
	if retry, ok := b.retryBackoffs[scopeKey]; ok {
		delay = retry.backoff
	}
	nextBackoff := int64Min(delay*2, 30_000)
	select {
	case <-b.stopCh:
		b.mu.Unlock()
		return
	default:
	}
	done, stop := b.newTimer(msToDuration(delay))
	b.retryBackoffs[scopeKey] = &scopeRetryState{backoff: nextBackoff, timerStop: stop}
	b.mu.Unlock()

	go func() {
		select {
		case <-done:
		case <-b.stopCh:
			return
		}
		b.mu.Lock()
		if retry, ok := b.retryBackoffs[scopeKey]; ok && retry.timerStop != nil {
			delete(b.retryBackoffs, scopeKey)
		}
		stopped := b.stopped
		b.mu.Unlock()
		if stopped {
			return
		}
		b.startScopeWorker(scopeKey)
	}()
}

func (b *Bridge) retryPendingImmediately(ctx context.Context, remainingMs int64) error {
	type workerWait struct {
		done chan struct{}
	}
	b.mu.Lock()
	for key, retry := range b.retryBackoffs {
		if retry.timerStop != nil {
			retry.timerStop()
		}
		delete(b.retryBackoffs, key)
	}
	scopeKeys := make([]string, 0, len(b.pending))
	for scopeKey := range b.pending {
		scopeKeys = append(scopeKeys, scopeKey)
	}
	var waits []workerWait
	for _, scopeKey := range scopeKeys {
		if _, running := b.workers[scopeKey]; running {
			continue
		}
		worker := &scopeWorker{done: make(chan struct{})}
		b.workers[scopeKey] = worker
		waits = append(waits, workerWait{done: worker.done})
		go func(key string, w *scopeWorker) {
			defer close(w.done)
			b.drainScope(key)
			b.mu.Lock()
			if current, ok := b.workers[key]; ok && current == w {
				delete(b.workers, key)
			}
			_, hasPending := b.pending[key]
			b.mu.Unlock()
			if hasPending {
				b.scheduleScopeRetry(key)
			}
		}(scopeKey, worker)
	}
	b.mu.Unlock()

	deadline := time.NewTimer(msToDuration(int64Max(remainingMs, 1)))
	defer deadline.Stop()
	for _, wait := range waits {
		select {
		case <-wait.done:
		case <-deadline.C:
			return &rebuildError{reason: RebuildReasonRebuildTimeout}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (b *Bridge) withinTimeout(ctx context.Context, operation func(context.Context) (RebuildPage, error), timeoutMs int64, reason string) (RebuildPage, error) {
	operationCtx, cancel := context.WithTimeout(ctx, msToDuration(int64Max(timeoutMs, 1)))
	defer cancel()
	page, err := operation(operationCtx)
	if err != nil {
		if operationCtx.Err() == context.DeadlineExceeded {
			return RebuildPage{}, &rebuildError{reason: reason}
		}
		return RebuildPage{}, err
	}
	return page, nil
}

func (b *Bridge) withinTimeoutList(ctx context.Context, operation func(context.Context) ([]IncidentRecord, error), timeoutMs int64, reason string) ([]IncidentRecord, error) {
	operationCtx, cancel := context.WithTimeout(ctx, msToDuration(int64Max(timeoutMs, 1)))
	defer cancel()
	incidents, err := operation(operationCtx)
	if err != nil {
		if operationCtx.Err() == context.DeadlineExceeded {
			return nil, &rebuildError{reason: reason}
		}
		return nil, err
	}
	return incidents, nil
}

type rebuildError struct {
	reason string
}

func (e *rebuildError) Error() string { return e.reason }

func rebuildFailure(loaded int64, reason string) RebuildResult {
	return RebuildResult{Loaded: loaded, Blocked: true, Reason: reason}
}

// IncidentToRuntimeState mirrors incidentToRuntimeState.
func IncidentToRuntimeState(incident IncidentRecord, hierarchyScopeKeys map[string]string) State {
	var scope Scope
	switch incident.ScopeKind {
	case IncidentScopeKindAccount:
		scope = Scope{Kind: ScopeKindAccount, AccountRuntimeKey: incident.AccountRuntimeKey}
	case IncidentScopeKindKey:
		scope = Scope{
			Kind:              ScopeKindKey,
			AccountRuntimeKey: incident.AccountRuntimeKey,
			KeyFingerprint:    requiredIncidentPart(incident.KeyFingerprint, "keyFingerprint"),
		}
	default:
		scope = Scope{
			Kind:              ScopeKindProtocolModel,
			AccountRuntimeKey: incident.AccountRuntimeKey,
			ProtocolProfile:   requiredIncidentPart(incident.ProtocolCode, "protocolCode"),
			RequestLane:       requiredIncidentRequestLane(incident.RequestLane),
			ModelBucket:       requiredIncidentPart(incident.ModelFamily, "modelFamily"),
		}
	}
	if MustScopeKey(scope) != incident.CircuitScopeKey {
		panic("持久化账户 circuit scopeKey 与作用域字段不一致")
	}
	leaseKind := ""
	if incident.LeasePurpose != nil {
		switch *incident.LeasePurpose {
		case LeaseKindConfirmation, LeaseKindHalfOpen, LeaseKindRecovery:
			leaseKind = *incident.LeasePurpose
		}
	}
	var childScopeKeys []string
	for _, incidentID := range incident.ChildIncidentIDs {
		if scopeKey, ok := hierarchyScopeKeys[incidentHierarchyKey(incident.AccountRuntimeKey, incidentID)]; ok {
			childScopeKeys = append(childScopeKeys, scopeKey)
		}
	}
	state := State{
		ScopeKey:                     incident.CircuitScopeKey,
		Scope:                        scope,
		Phase:                        runtimePhase(incident.State),
		Generation:                   incident.Generation,
		DispatchRevision:             fmt.Sprintf("%d", incident.DispatchRevision),
		TransitionID:                 incident.TransitionID,
		IncidentID:                   strPtr(incident.IncidentID),
		ChildIncidentIDs:             stringList(append([]string{}, incident.ChildIncidentIDs...)),
		BackoffAttempt:               incident.BackoffLevel,
		RecoverySuccessCount:         incident.RecoveringSuccesses,
		ConfirmationFailuresRequired: int64Ptr(incident.ConfirmationFailuresRequired),
		ConfirmationFailureCount:     int64Ptr(incident.ConsecutiveFailures),
		FailureEvidenceKeys:          stringList(append([]string{}, incident.ConfirmationFailureEvidenceKeys...)),
		UpdatedAtMs:                  incident.UpdatedAtMs,
	}
	if incident.ParentIncidentID != nil {
		state.ShadowedByIncidentID = strPtr(*incident.ParentIncidentID)
	}
	if len(childScopeKeys) > 0 {
		state.ChildScopeKeys = stringList(append([]string{}, childScopeKeys...))
		state.RequiredRecoveryScopeKeys = stringList(append([]string{}, childScopeKeys...))
	}
	if incident.OpenUntilMs != nil {
		state.OpenedAtMs = int64Ptr(incident.UpdatedAtMs)
	}
	if incident.NextTransitionAtMs != nil {
		state.RetryAtMs = int64Ptr(*incident.NextTransitionAtMs)
	}
	if incident.LastFailureClass != nil {
		state.FailureReason = strPtr(*incident.LastFailureClass)
	}
	if leaseKind != "" && incident.LeaseID != nil && incident.LeaseUntilMs != nil {
		state.Lease = &Lease{Kind: leaseKind, LeaseID: *incident.LeaseID, LeaseUntilMs: *incident.LeaseUntilMs}
	}
	if incident.State == IncidentStateHalfOpen && leaseKind == LeaseKindHalfOpen {
		state.HalfOpenOrigin = strPtr(PhaseOpen)
	}
	if incident.State == IncidentStateHalfOpen && leaseKind == LeaseKindRecovery {
		state.HalfOpenOrigin = strPtr(PhaseRecovering)
	}
	return state
}

func incidentMatchesRuntimeState(incident *IncidentRecord, state State) bool {
	return incident.DispatchRevision == stateDispatchRevisionNumber(state) &&
		incident.Generation == state.Generation &&
		runtimePhase(incident.State) == state.Phase &&
		incident.TransitionID == state.TransitionID &&
		incident.IncidentID == durableIncidentID(state) &&
		derefString(incident.ParentIncidentID) == derefString(state.ShadowedByIncidentID) &&
		sameStringSet(incident.ChildIncidentIDs, []string(state.ChildIncidentIDs)) &&
		incident.UpdatedAtMs == state.UpdatedAtMs
}

func stateDispatchRevisionNumber(state State) int64 {
	value, ok := parseSafeInteger(state.DispatchRevision)
	if !ok {
		return math.MinInt64
	}
	return int64(value)
}

func durableIncidentID(state State) string {
	if state.IncidentID != nil && strings.TrimSpace(*state.IncidentID) != "" {
		return *state.IncidentID
	}
	return state.TransitionID
}

func incidentHierarchyKey(accountRuntimeKey, incidentID string) string {
	return fmt.Sprintf("%d:%s|%s", len(accountRuntimeKey), accountRuntimeKey, incidentID)
}

func incidentScopeKeyMap(incidents []IncidentRecord) map[string]string {
	result := map[string]string{}
	addIncidentScopeKeys(result, incidents)
	return result
}

func addIncidentScopeKeys(target map[string]string, incidents []IncidentRecord) {
	for _, incident := range incidents {
		target[incidentHierarchyKey(incident.AccountRuntimeKey, incident.IncidentID)] = incident.CircuitScopeKey
	}
}

func incidentScopeKeyMapFromRuntimeState(state State) map[string]string {
	result := map[string]string{}
	for index, incidentID := range state.ChildIncidentIDs {
		if index < len(state.ChildScopeKeys) {
			scopeKey := state.ChildScopeKeys[index]
			if scopeKey != "" {
				result[incidentHierarchyKey(state.Scope.AccountRuntimeKey, incidentID)] = scopeKey
			}
		}
	}
	return result
}

func hasUnresolvedChildIncident(incident IncidentRecord, hierarchyScopeKeys map[string]string) bool {
	for _, incidentID := range incident.ChildIncidentIDs {
		if _, ok := hierarchyScopeKeys[incidentHierarchyKey(incident.AccountRuntimeKey, incidentID)]; !ok {
			return true
		}
	}
	return false
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := map[string]struct{}{}
	for _, value := range left {
		values[value] = struct{}{}
	}
	if len(values) != len(right) {
		return false
	}
	for _, value := range right {
		if _, ok := values[value]; !ok {
			return false
		}
	}
	return true
}

func incidentIsNewerThanRuntimeState(incident *IncidentRecord, state State) bool {
	runtimeDispatchRevision, ok := parseSafeInteger(state.DispatchRevision)
	if ok && float64(incident.DispatchRevision) != runtimeDispatchRevision {
		return float64(incident.DispatchRevision) > runtimeDispatchRevision
	}
	return incident.Generation > state.Generation
}

func runtimePhase(state string) string {
	if state == IncidentStatePersisting || state == IncidentStateShadowedByPersistent {
		return PhaseOpen
	}
	return state
}

func requiredIncidentPart(value *string, name string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		panic(fmt.Sprintf("账户 circuit incident 缺少 %s", name))
	}
	return strings.TrimSpace(*value)
}

func requiredIncidentRequestLane(value *string) string {
	if value == nil || (*value != LaneText && *value != LaneImage) {
		panic("持久化账户 circuit requestLane 无效")
	}
	return *value
}

// LoadPublicAccountCircuitSummaries mirrors loadPublicAccountCircuitSummaries.
func LoadPublicAccountCircuitSummaries(ctx context.Context, db ControlPlaneDB, accountRuntimeKeys []string) (map[string]PublicSummary, error) {
	keys := boundedRuntimeKeys(accountRuntimeKeys)
	incidents, err := db.ListIncidentsByRuntimeKeys(ctx, ListIncidentsByRuntimeKeysInput{AccountRuntimeKeys: keys})
	if err != nil {
		return nil, err
	}
	return PublicSummariesFromIncidents(keys, incidents), nil
}

// PublicSummariesFromIncidents mirrors publicAccountCircuitSummariesFromIncidents.
func PublicSummariesFromIncidents(accountRuntimeKeys []string, incidents []IncidentRecord) map[string]PublicSummary {
	keys := boundedRuntimeKeys(accountRuntimeKeys)
	grouped := map[string][]IncidentRecord{}
	for _, incident := range incidents {
		grouped[incident.AccountRuntimeKey] = append(grouped[incident.AccountRuntimeKey], incident)
	}
	result := make(map[string]PublicSummary, len(keys))
	for _, key := range keys {
		result[key] = PublicSummaryOf(grouped[key])
	}
	return result
}

// PublicSummaryOf mirrors publicAccountCircuitSummary.
func PublicSummaryOf(incidents []IncidentRecord) PublicSummary {
	if len(incidents) == 0 {
		return PublicSummary{Status: PublicSummaryStatusNormal}
	}
	selected := append([]IncidentRecord{}, incidents...)
	// Highest priority wins; ties break toward the oldest update.
	sort.SliceStable(selected, func(left, right int) bool {
		priorityDiff := incidentStatePriority(selected[left].State) - incidentStatePriority(selected[right].State)
		if priorityDiff != 0 {
			return priorityDiff > 0
		}
		return selected[left].UpdatedAtMs < selected[right].UpdatedAtMs
	})
	head := selected[0]
	status := PublicSummaryStatusVerifying
	if head.State == IncidentStateOpen || head.State == IncidentStatePersisting || head.State == IncidentStateShadowedByPersistent {
		status = PublicSummaryStatusAvoided
	} else if head.State == IncidentStateRecovering {
		status = PublicSummaryStatusRecovering
	}
	var nextCheckMs *int64
	for _, incident := range incidents {
		if incident.NextTransitionAtMs != nil && (nextCheckMs == nil || *incident.NextTransitionAtMs < *nextCheckMs) {
			nextCheckMs = incident.NextTransitionAtMs
		}
	}
	summary := PublicSummary{
		Status: status,
		Since:  msToRFC3339(head.UpdatedAtMs),
	}
	if head.LastFailureClass != nil {
		summary.Reason = *head.LastFailureClass
	}
	if nextCheckMs != nil {
		summary.NextCheckAt = msToRFC3339(*nextCheckMs)
	}
	return summary
}

func incidentStatePriority(state string) int {
	if state == IncidentStateOpen || state == IncidentStatePersisting || state == IncidentStateShadowedByPersistent {
		return 3
	}
	if state == IncidentStateHalfOpen || state == IncidentStateSuspect {
		return 2
	}
	if state == IncidentStateRecovering {
		return 1
	}
	return 0
}

func boundedRuntimeKeys(accountRuntimeKeys []string) []string {
	seen := map[string]struct{}{}
	keys := make([]string, 0, len(accountRuntimeKeys))
	for _, key := range accountRuntimeKeys {
		normalized := strings.TrimSpace(key)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		keys = append(keys, normalized)
		if len(keys) >= 100 {
			break
		}
	}
	return keys
}

func accountIDFromRuntimeKey(runtimeKey string) (string, error) {
	accountID := ""
	if index := strings.Index(runtimeKey, ":"); index >= 0 {
		accountID = runtimeKey[:index]
	} else {
		accountID = runtimeKey
	}
	if strings.TrimSpace(accountID) == "" {
		return "", errors.New("账户 circuit runtime key 缺少 accountId")
	}
	return accountID, nil
}

func classifyFailure(reason string) string {
	value := strings.ToLower(reason)
	if strings.Contains(value, "timeout") {
		return FailureClassTimeoutBeforeComplete
	}
	if strings.Contains(value, "connect") {
		return FailureClassConnectFailed
	}
	if strings.Contains(value, "read") {
		return FailureClassReadInterrupted
	}
	if strings.Contains(value, "policy") {
		return FailureClassExplicitPolicy
	}
	return FailureClassIncompleteResponse
}

// classifyError mirrors classifyError: Node keeps error.name truncated to 64
// characters; Go keeps the concrete type name.
func classifyError(err error) string {
	if err == nil {
		return "projector_error"
	}
	name := reflect.TypeOf(err).String()
	name = strings.TrimPrefix(name, "*")
	if index := strings.LastIndex(name, "."); index >= 0 {
		name = name[index+1:]
	}
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		return "projector_error"
	}
	return name
}

func requiredRuntimeKey(value string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", errors.New("账户 circuit runtime key 不能为空")
	}
	return normalized, nil
}

func compareCursor(left, right *RebuildCursor) int {
	if left.UpdatedAtMs != right.UpdatedAtMs {
		if left.UpdatedAtMs < right.UpdatedAtMs {
			return -1
		}
		return 1
	}
	return strings.Compare(left.CircuitScopeKey, right.CircuitScopeKey)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func boolPtr(value bool) *bool { return &value }

func durationToMs(d time.Duration) int64 {
	return int64(d / time.Millisecond)
}

func msToDuration(ms int64) time.Duration {
	if ms < 0 {
		ms = 0
	}
	return time.Duration(ms) * time.Millisecond
}

func int64Max(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func msToRFC3339(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}
