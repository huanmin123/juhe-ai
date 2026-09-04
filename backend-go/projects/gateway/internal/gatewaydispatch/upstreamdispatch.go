package gatewaydispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// fetchFirstAvailableUpstream, migrated from dispatch/upstream-dispatch.ts.
// The candidate loop, attempt lifecycle, retry classification, same-account
// retry, account-lock lease wait, API-key rotation budget, wall-budget
// assertion, capacity queue wait and recoverable-suppression wait mirror the
// Node implementation; every user-facing message is byte-identical.

// UpstreamDispatchResult mirrors OpenAIUpstreamDispatchResult.
type UpstreamDispatchResult struct {
	Account                    AccountCandidate
	Response                   *GatewayUpstreamResponse
	RequestBody                []byte
	UpstreamURL                string
	AuditAttemptID             string
	AttemptStartedAt           int64
	EffectiveServiceTier       string
	TimeoutProfile             gatewayrouting.GatewayTimeoutProfile
	ReleaseConcurrency         func()
	MarkFirstOutput            func()
	ConfirmSameAccountApiKeyFailures func() error
	ConfirmHalfOpenSuccess     func() bool
	ReleaseHalfOpenLease       func() bool
	HotQualityAttempt          *hotQualityAttemptHandle
	NormalRouteFirstByteDeadline *gatewayrouting.NormalRouteAttemptFirstByteDeadline
	ResponsePrecommitDeadlineAtMs *int64
	OnFirstByteDeadline        FirstByteDeadlineHandler
	FirstByteDeadlineCoordinator *NormalRouteFirstByteAttemptCoordinator
	AccountLockObservation     *AccountLockObservation
	AccountLockRetryLease      *AccountLockRetryLease
	ReleaseAccountLockRetryLease func(scheduleNextRetry bool) bool
}

// RequestCoordinationContext mirrors GatewayUpstreamRequestCoordinationContext.
type RequestCoordinationContext struct {
	Scope                    string // 'gateway_request' | 'internal_hybrid_auxiliary'
	Reason                   string
	TimeoutPolicy            string // 'codex_compaction_unbounded' | ''
	ServerRetryBudget        *gatewaypreauth.ServerRetryBudget
	GatewayRequestWallBudget *gatewayrouting.GatewayRequestWallBudget
	RouteCoordinationBudget  *gatewayrouting.RouteCoordinationBudget
	RequestAttemptTracker    *gatewayrouting.GatewayRequestAttemptTracker
	SameAccountRetry         *SameAccountRetry
	AccountLockRetryLease    *AccountLockRetryLease
	SemanticRetryID          string
	RequestBodyOverride      *RequestBodyOverride
	NormalRouteFirstByteConfig *gatewayrouting.NormalRouteFirstByteRuntimeConfig
	OnNormalRouteFirstByteDeadline func(input FirstByteDeadlineDecisionInput, account AccountCandidate, deadline gatewayrouting.NormalRouteAttemptFirstByteDeadline, coordinator *NormalRouteFirstByteAttemptCoordinator) FirstByteDeadlineAction
	// OnUpstreamAttemptStarted is invoked once per upstream attempt start.
	OnUpstreamAttemptStarted func(account AccountCandidate, upstreamURL string)
}

// Coordination scopes.
const (
	CoordinationScopeGatewayRequest        = "gateway_request"
	CoordinationScopeInternalHybridAuxiliary = "internal_hybrid_auxiliary"
)

// Timeout policies.
const (
	TimeoutPolicyCodexCompactionUnbounded = "codex_compaction_unbounded"
)

// SameAccountRetry mirrors the same-account retry carry.
type SameAccountRetry struct {
	RetryID           string
	Account           AccountCandidate
	AccountLockLeaseID string
}

// RequestBodyOverride mirrors requestBodyOverride.
type RequestBodyOverride struct {
	AccountID string
	Body      []byte
}

// NormalRouteFirstByteAttemptCoordinator mirrors the coordinator class: a
// reservation covers exactly one physical upstream attempt.
type NormalRouteFirstByteAttemptCoordinator struct {
	mu          sync.Mutex
	state       string // 'active' | 'superseded' | 'transferred'
	reservation *SpeedFirstCutoverReservationView
}

// SpeedFirstCutoverReservationView is the reservation view the engine owns
// (gatewayhotquality.SpeedFirstCutoverReservation projected for the engine).
type SpeedFirstCutoverReservationView struct {
	TargetAccountIDValue string
	ReleaseFunc          func()
	ConsumedValue        bool
}

// AttachReservation mirrors attachReservation.
func (c *NormalRouteFirstByteAttemptCoordinator) AttachReservation(reservation *SpeedFirstCutoverReservationView) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "active" {
		if reservation != nil && reservation.ReleaseFunc != nil {
			reservation.ReleaseFunc()
		}
		return false
	}
	previous := c.reservation
	c.reservation = reservation
	if previous != nil && previous.ReleaseFunc != nil {
		previous.ReleaseFunc()
	}
	return true
}

// ReleaseReservation mirrors releaseReservation.
func (c *NormalRouteFirstByteAttemptCoordinator) ReleaseReservation() {
	c.mu.Lock()
	reservation := c.reservation
	c.reservation = nil
	c.mu.Unlock()
	if reservation != nil && reservation.ReleaseFunc != nil {
		reservation.ReleaseFunc()
	}
}

// Supersede mirrors supersede().
func (c *NormalRouteFirstByteAttemptCoordinator) Supersede() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "active" {
		return
	}
	c.state = "superseded"
	reservation := c.reservation
	c.reservation = nil
	if reservation != nil && reservation.ReleaseFunc != nil {
		reservation.ReleaseFunc()
	}
}

// CanCutover mirrors the canCutover getter.
func (c *NormalRouteFirstByteAttemptCoordinator) CanCutover() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == "active" && c.reservation != nil
}

// ReservedTargetAccountID mirrors the reservedTargetAccountId getter.
func (c *NormalRouteFirstByteAttemptCoordinator) ReservedTargetAccountID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == "active" && c.reservation != nil {
		return c.reservation.TargetAccountIDValue
	}
	return ""
}

// TransferForCutover mirrors transferForCutover().
func (c *NormalRouteFirstByteAttemptCoordinator) TransferForCutover() *SpeedFirstCutoverReservationView {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "active" {
		return nil
	}
	c.state = "transferred"
	reservation := c.reservation
	c.reservation = nil
	return reservation
}

// hotQualityAttemptHandle mirrors the lazy GatewayHotQualityAttemptLifecycle
// wrapper (created on first terminal/first-byte use).
type hotQualityAttemptHandle struct {
	once    sync.Once
	attempt *attemptLifecycleFacade
	input   HotQualityLifecycleInput
}

// HotQualityLifecycleInput carries the lifecycle construction inputs.
type HotQualityLifecycleInput struct {
	AttemptID   string
	AccountID   string
	RequestLane string
	Model       string
}

// attemptLifecycleFacade mirrors the lifecycle surface the engine consumes.
type attemptLifecycleFacade struct {
	MarkFirstByteFunc  func(firstByteMs *float64)
	RecordTerminalFunc func(ctx context.Context, terminal HotQualityTerminal)
}

// HotQualityTerminal mirrors the recordTerminal input.
type HotQualityTerminal struct {
	OutcomeClass string
	FailureScope string
	Source       string
}

// Terminal outcome classes mirror the Node union.
const (
	HotQualityOutcomeUnknown            = "unknown"
	HotQualityOutcomeTimeout            = "timeout"
	HotQualityOutcomeTransportFailure   = "transport_failure"
	HotQualityOutcomeReadInterruption   = "read_interruption"
	HotQualityOutcomeIncompleteResponse = "incomplete_response"
	HotQualityOutcomeClientCancellation = "client_cancellation"
	HotQualityOutcomeExplicitPolicyFailure = "explicit_policy_failure"
	HotQualityOutcomeUpstreamResponseFailure = "upstream_response_failure"
)

func (h *hotQualityAttemptHandle) lifecycle() *attemptLifecycleFacade {
	h.once.Do(func() {
		if h.attempt == nil {
			// Hot-quality attempt creation is delegated to G20's runtime
			// wiring; the engine keeps the lifecycle contract neutral when
			// the runtime is absent.
			h.attempt = &attemptLifecycleFacade{
				MarkFirstByteFunc:  func(*float64) {},
				RecordTerminalFunc: func(context.Context, HotQualityTerminal) {},
			}
		}
	})
	return h.attempt
}

// MarkFirstByte mirrors markFirstByte.
func (h *hotQualityAttemptHandle) MarkFirstByte(firstByteMs *float64) {
	h.lifecycle().MarkFirstByteFunc(firstByteMs)
}

// RecordTerminal mirrors recordTerminal.
func (h *hotQualityAttemptHandle) RecordTerminal(ctx context.Context, terminal HotQualityTerminal) {
	h.lifecycle().RecordTerminalFunc(ctx, terminal)
}

// FetchFirstAvailableUpstreamArgs mirrors the fetchFirstAvailableUpstream
// parameter list (Go groups them; the doc comments name each Node arg).
type FetchFirstAvailableUpstreamArgs struct {
	Req                            *gatewaypreauth.GatewayRequest
	Accounts                       []AccountCandidate
	Settings                       gatewayruntimecache.GatewaySettings
	UsageContext                   gatewaypreauth.GatewayFailureUsageContext
	AuditCapture                   AuditCapture
	SessionAffinityKey             string
	Signal                         context.Context
	ClientIPAccountAvoidanceTracker gatewaypreauth.ClientIPAccountAvoidanceTracker
	RequestLane                    string // default 'text'
	GroupSchedulingPolicy          *gatewayruntimecache.GroupSchedulingPolicy
	AccountStateMutationEnabled    bool
	RequestClientCompatibility     string
	ModelPriority                  *gatewayrouting.GatewayAccountModelPriority
	PreAcquiredConcurrency         *SpeedFirstCutoverReservationHandle
	AllowPrecheckHalfOpen          bool
	RequestCoordination            *RequestCoordinationContext
	InterpretUpstreamResponseSemantics bool
	WaitForRecoverableFailures     bool
	AccountCircuitConfirmation     *gatewaycircuit.Confirmation
	BypassKeyModelAdmission        bool
}

// SpeedFirstCutoverReservationHandle mirrors preAcquiredConcurrency.
type SpeedFirstCutoverReservationHandle struct {
	TakeForAccount func(account AccountCandidate) (ConcurrencySlot, bool)
}

// FetchFirstAvailableUpstream mirrors fetchFirstAvailableUpstream: the
// candidate dispatch loop.
func (e *Engine) FetchFirstAvailableUpstream(ctx context.Context, args FetchFirstAvailableUpstreamArgs) (UpstreamDispatchResult, error) {
	coordination := args.RequestCoordination
	if coordination == nil {
		return UpstreamDispatchResult{}, errMissingCoordination
	}
	requestLane := args.RequestLane
	if requestLane == "" {
		requestLane = "text"
	}
	signal := args.Signal
	if signal == nil {
		signal = ctx
	}
	usageContext := args.UsageContext
	auditCapture := args.AuditCapture
	settings := args.Settings
	serverRetryBudget := coordination.ServerRetryBudget
	gatewayRequestWallBudget := coordination.GatewayRequestWallBudget
	requestAttemptTracker := coordination.RequestAttemptTracker
	semanticRetryID := coordination.SemanticRetryID

	compactionTimeoutsDisabled := coordination.TimeoutPolicy == TimeoutPolicyCodexCompactionUnbounded ||
		e.codexCompactionExpectedForRequest(args.Req)
	timeoutProfile := gatewayrouting.GatewayTimeoutProfileForLane(gatewayrouting.GatewayTimeoutSettings{
		TextFirstResponseTimeoutSeconds:           settings.TextFirstResponseTimeoutSeconds,
		TextStreamIdleTimeoutSeconds:              settings.TextStreamIdleTimeoutSeconds,
		TextUncommittedAttemptMaxLifetimeSeconds:  settings.TextUncommittedAttemptMaxLifetimeSeconds,
		ImageFirstResponseTimeoutSeconds:          settings.ImageFirstResponseTimeoutSeconds,
		ImageStreamIdleTimeoutSeconds:             settings.ImageStreamIdleTimeoutSeconds,
		ImageUncommittedAttemptMaxLifetimeSeconds: settings.ImageUncommittedAttemptMaxLifetimeSeconds,
		NoAvailableAccountWaitTimeoutSeconds:      settings.NoAvailableAccountWaitTimeoutSeconds,
	}, gatewayprotoLane(requestLane), compactionTimeoutsDisabled)

	accountCircuitFailureEvidenceKey := e.gatewayForegroundAccountCircuitFailureEvidenceKey(args.Req, usageContext)
	automaticAccountStateMutationAllowed := args.AccountStateMutationEnabled && isAccountProbeTrafficSource(usageContext.TrafficSource)
	accountLockTrafficEnabled := args.AccountStateMutationEnabled && usageContext.TrafficSource == "gateway"

	var lastAttempt *UpstreamAttempt
	var agentGuidanceResponse *gatewaypreauth.GatewayAgentGuidanceResponse
	auditAttemptIndex := 0
	concurrencyRetryWaitBudgetMs := e.Config.AccountConcurrencyRetryBudgetMs
	highConcurrencyDispatchQueueWaitCount := 0
	failedProxyDispatchKeys := map[string]string{}
	failedAccountIDs := map[string]struct{}{}
	keyModelFailureBudget := gatewayaccounteffects.NewGatewayKeyModelFailureBudget()
	recoverableFailedAccountIDs := map[string]struct{}{}
	bypassLocalSuppression := isAccountProbeTrafficSource(usageContext.TrafficSource)

	confirmationLeaseDurationMs := maxInt64(
		timeoutProfile.FirstResponseTimeoutMs,
		timeoutProfile.UncommittedAttemptMaxLifetimeMs,
	) + 5_000

	degradation, err := e.Degradation.OrderWithLaneAsync(ctx, args.Accounts, requestLane, args.GroupSchedulingPolicy, args.ModelPriority)
	if err != nil {
		return UpstreamDispatchResult{}, err
	}
	dispatchAccounts := degradation.Accounts
	if coordination.SameAccountRetry != nil {
		dispatchAccounts = []AccountCandidate{coordination.SameAccountRetry.Account}
	}
	if args.AccountCircuitConfirmation != nil {
		confirmationRuntimeKey := args.AccountCircuitConfirmation.AccountRuntimeKey
		filtered := make([]AccountCandidate, 0, len(dispatchAccounts))
		for _, account := range dispatchAccounts {
			if gatewayAccountRuntimeKey(account) == confirmationRuntimeKey {
				filtered = append(filtered, account)
			}
		}
		dispatchAccounts = filtered
	}
	{
		filtered := make([]AccountCandidate, 0, len(dispatchAccounts))
		snapshot := requestAttemptTracker.Snapshot()
		attemptedFingerprints := map[string]struct{}{}
		for _, fingerprint := range snapshot.AttemptedKeyFingerprints {
			attemptedFingerprints[fingerprint] = struct{}{}
		}
		for _, account := range dispatchAccounts {
			registration, regErr := requestAttemptTracker.CanAttemptAccount(gatewayrouting.CanAttemptAccountInput{
				AccountRuntimeKey:     gatewayAccountRuntimeKey(account),
				PhysicalCredentialKey: accountPhysicalCredentialKey(account),
				MatchingConfirmation:  args.AccountCircuitConfirmation != nil && args.AccountCircuitConfirmation.AccountRuntimeKey == gatewayAccountRuntimeKey(account),
				SemanticRetryID:       semanticRetryID,
			})
			if regErr != nil {
				return UpstreamDispatchResult{}, regErr
			}
			sameAccountCarry := coordination.SameAccountRetry != nil && coordination.SameAccountRetry.Account.ID == account.ID
			if registration.Allowed || sameAccountCarry {
				filtered = append(filtered, account)
			}
			_ = attemptedFingerprints
		}
		dispatchAccounts = filtered
	}

	primaryDispatchTier := ""
	if len(dispatchAccounts) > 0 {
		primaryDispatchTier = gatewayAccountDispatchPriorityTier(dispatchAccounts[0], args.ModelPriority)
	}
	observedEscapedTiers := map[string]struct{}{}
	snapshot := requestAttemptTracker.Snapshot()
	requestApiKeyAttemptCount := len(snapshot.AttemptedKeyFingerprints)
	activeSameAccountRetryID := ""
	if coordination.SameAccountRetry != nil {
		activeSameAccountRetryID = coordination.SameAccountRetry.RetryID
	}
	activeAccountLockRetryLease := coordination.AccountLockRetryLease
	if activeAccountLockRetryLease == nil && coordination.SameAccountRetry != nil && coordination.SameAccountRetry.AccountLockLeaseID != "" {
		activeAccountLockRetryLease = &AccountLockRetryLease{
			AccountID: coordination.SameAccountRetry.Account.ID,
			LeaseID:   coordination.SameAccountRetry.AccountLockLeaseID,
		}
	}
	var activeAccountLockObservation *AccountLockObservation
	maxSameAccountRetries := int(minInt64(2, settings.TemporaryUnschedulableRetryAttempts))
	releaseActiveAccountLockLease := func(scheduleNextRetry bool) error {
		lease := activeAccountLockRetryLease
		if lease == nil {
			return nil
		}
		activeAccountLockRetryLease = nil
		_, err := e.Locks.ReleaseRetryLeaseAsync(ctx, ReleaseRetryLeaseInput{
			AccountID:         lease.AccountID,
			LeaseID:           lease.LeaseID,
			GlobalDelayMs:     settings.TemporaryUnschedulableRetryIntervalSeconds * 1000,
			ScheduleNextRetry: scheduleNextRetry,
		})
		return err
	}
	abandonActiveAccountLockReservation := func(fallback *AccountLockRetryLease) error {
		lease := activeAccountLockRetryLease
		if lease == nil && fallback != nil && fallback.LeaseID != "" {
			lease = fallback
		}
		if lease == nil {
			return nil
		}
		if activeAccountLockRetryLease != nil && activeAccountLockRetryLease.LeaseID == lease.LeaseID {
			activeAccountLockRetryLease = nil
		}
		return e.Locks.AbandonRetryReservationAsync(ctx, *lease)
	}
	createAccountLockLeaseRelease := func() func(scheduleNextRetry bool) bool {
		lease := activeAccountLockRetryLease
		activeAccountLockRetryLease = nil
		var releaseOnce sync.Once
		return func(scheduleNextRetry bool) bool {
			released := true
			releaseOnce.Do(func() {
				if lease == nil {
					return
				}
				_, err := e.Locks.ReleaseRetryLeaseAsync(ctx, ReleaseRetryLeaseInput{
					AccountID:         lease.AccountID,
					LeaseID:           lease.LeaseID,
					GlobalDelayMs:     settings.TemporaryUnschedulableRetryIntervalSeconds * 1000,
					ScheduleNextRetry: scheduleNextRetry,
				})
				_ = err
			})
			return released
		}
	}

	reserveSameAccountRetry := func(identity gatewayrouting.GatewayDispatchAttemptIdentity, reason, accountID string) (string, error) {
		configuredDelayMs := maxInt64(0, settings.TemporaryUnschedulableRetryIntervalSeconds*1000)
		retryWindowMs := gatewayRequestWallBudget.RemainingMs(NowMs()) - gatewayrouting.DefaultGatewayFinalResponseReserveMs
		if retryWindowMs < configuredDelayMs {
			auditCapture.AddGatewayMetadata("same_account_retry_exhausted", map[string]any{
				"accountId": identity.AccountRuntimeKey,
				"retryReason": reason,
				"reason":    "gateway_request_wall_budget_exhausted",
			})
			return "", nil
		}
		reservation, err := requestAttemptTracker.TryReserveSameAccountRetry(gatewayrouting.GatewaySameAccountRetryReservationInput{
			GatewayDispatchAttemptIdentity: identity,
			MaxRetries:                     maxSameAccountRetries,
		})
		if err != nil {
			return "", err
		}
		if !reservation.Reserved {
			auditCapture.AddGatewayMetadata("same_account_retry_exhausted", map[string]any{
				"accountId":   identity.AccountRuntimeKey,
				"retryReason": reason,
				"retryNumber": reservation.Remaining,
				"reason":      reservation.Reason,
			})
			return "", nil
		}
		if accountLockTrafficEnabled && accountID != "" && activeAccountLockRetryLease != nil && activeAccountLockRetryLease.AccountID == accountID {
			if err := releaseActiveAccountLockLease(true); err != nil {
				return "", err
			}
		}
		if accountLockTrafficEnabled && accountID != "" && reason == "upstream_transport_failure" {
			if err := e.Locks.RecordFailureAsync(ctx, accountID, reason, activeAccountLockObservation); err != nil {
				return "", err
			}
		}
		lockRetryScheduled := false
		if accountLockTrafficEnabled && accountID != "" {
			lockLease, err := e.Locks.AcquireRetryLeaseAsync(ctx, accountID, configuredDelayMs)
			if err != nil {
				return "", err
			}
			if !lockLease.Allowed && lockLease.WaitMs > 0 {
				waitResult, err := e.waitForAccountLockDelay(signal, accountID, lockLease.LeaseID, lockLease.WaitMs, gatewayRequestWallBudget, coordination.RouteCoordinationBudget)
				if err != nil {
					return "", err
				}
				if waitResult != accountLockWaitCompleted {
					switch waitResult {
					case accountLockWaitAborted:
						return "", nil
					case accountLockWaitWall:
						return "", &GatewayRequestWallBudgetExhaustedError{
							WallRemainingMs:            gatewayRequestWallBudget.RemainingMs(NowMs()),
							MinimumMeaningfulAttemptMs: lockLease.WaitMs,
							BudgetKind:                 WallBudgetKindWall,
						}
					default:
						return "", &GatewayRequestWallBudgetExhaustedError{
							WallRemainingMs: gatewayRequestWallBudget.RemainingMs(NowMs()),
							BudgetKind:      WallBudgetKindCoordination,
						}
					}
				}
				if lockLease.LeaseID == "" {
					lockLease, err = e.Locks.AcquireRetryLeaseAsync(ctx, accountID, configuredDelayMs)
					if err != nil {
						return "", err
					}
					if !lockLease.Allowed || lockLease.LeaseID == "" || lockLease.WaitMs > 0 {
						return "", nil
					}
				}
				consumed, err := e.Locks.ConsumeRetryLeaseAsync(ctx, accountID, lockLease.LeaseID)
				if err != nil {
					return "", err
				}
				if !consumed {
					return "", nil
				}
				lockRetryScheduled = true
				if lockLease.LeaseID != "" {
					activeAccountLockRetryLease = &AccountLockRetryLease{AccountID: accountID, LeaseID: lockLease.LeaseID}
				}
			}
			if !lockRetryScheduled {
				if !lockLease.Allowed {
					return "", nil
				}
				handoff, err := gatewayRequestWallBudget.HandoffRequired(gatewayrouting.GatewayRequestWallBudgetDecision{
					FinalResponseReserveMs:     ptrInt64(gatewayrouting.DefaultGatewayFinalResponseReserveMs),
					MinimumMeaningfulAttemptMs: ptrInt64(lockLease.WaitMs),
				})
				if err != nil {
					return "", err
				}
				if handoff {
					if err := abandonActiveAccountLockReservation(&AccountLockRetryLease{AccountID: accountID, LeaseID: lockLease.LeaseID}); err != nil {
						return "", err
					}
					return "", nil
				}
				if lockLease.WaitMs > 0 {
					waitResult, err := e.waitForAccountLockDelay(signal, accountID, lockLease.LeaseID, lockLease.WaitMs, gatewayRequestWallBudget, coordination.RouteCoordinationBudget)
					if err != nil {
						return "", err
					}
					if waitResult != accountLockWaitCompleted {
						abandonLease := &AccountLockRetryLease{AccountID: accountID, LeaseID: lockLease.LeaseID}
						switch waitResult {
						case accountLockWaitAborted:
							if err := abandonActiveAccountLockReservation(abandonLease); err != nil {
								return "", err
							}
							return "", nil
						case accountLockWaitWall:
							if err := abandonActiveAccountLockReservation(abandonLease); err != nil {
								return "", err
							}
							return "", &GatewayRequestWallBudgetExhaustedError{
								WallRemainingMs:            gatewayRequestWallBudget.RemainingMs(NowMs()),
								MinimumMeaningfulAttemptMs: lockLease.WaitMs,
								BudgetKind:                 WallBudgetKindWall,
							}
						default:
							if err := abandonActiveAccountLockReservation(abandonLease); err != nil {
								return "", err
							}
							return "", &GatewayRequestWallBudgetExhaustedError{
								WallRemainingMs: gatewayRequestWallBudget.RemainingMs(NowMs()),
								BudgetKind:      WallBudgetKindCoordination,
							}
						}
					}
				}
				if lockLease.LeaseID != "" {
					consumed, err := e.Locks.ConsumeRetryLeaseAsync(ctx, accountID, lockLease.LeaseID)
					if err != nil {
						return "", err
					}
					if !consumed {
						return "", nil
					}
					lockRetryScheduled = true
					activeAccountLockRetryLease = &AccountLockRetryLease{AccountID: accountID, LeaseID: lockLease.LeaseID}
				}
			}
		}
		if !lockRetryScheduled && configuredDelayMs > 0 {
			if err := waitForDelayMs(signal, configuredDelayMs); err != nil {
				return "", &UpstreamRequestAbortedError{Message: "请求已取消"}
			}
		}
		auditCapture.AddGatewayMetadata("same_account_retry_dispatch", map[string]any{
			"accountId":                  identity.AccountRuntimeKey,
			"retryNumber":                reservation.RetryNumber,
			"remainingSameAccountRetries": reservation.Remaining,
			"retryReason":                reason,
			"delayMs":                    configuredDelayMs,
		})
		return reservation.RetryID, nil
	}

	defer func() {
		_ = releaseActiveAccountLockLease(false)
	}()

	var capacityLimitFailures []AccountCapacityLimitFailure
	var cycleRecoverableAccountIDs map[string]struct{}
	var pendingApiKeyFailures []PendingAccountApiKeyFailure

	for len(dispatchAccounts) > 0 {
		cycleRecoverableAccountIDs = map[string]struct{}{}
		capacityLimitFailures = nil
		skipRestOfCycle := false

		for _, originalAccount := range dispatchAccounts {
			if err := throwIfRequestAborted(signal); err != nil {
				return UpstreamDispatchResult{}, err
			}
			var accountCircuitAttempt *gatewaycircuitAttemptFacade
			if e.Circuits != nil && coordination.SameAccountRetry == nil {
				model := requestModelOrEmpty(args.Req)
				confirmationEligible := !compactionTimeoutsDisabled
				var confirmationFailuresRequired *int64
				if e.Config.AccountCircuitConfirmationFailuresRequired != nil {
					confirmationFailuresRequired = e.Config.AccountCircuitConfirmationFailuresRequired
				} else {
					confirmationFailuresRequired = ptrInt64(settings.AccountCircuitConfirmationFailuresRequired)
				}
				preparation, err := e.Circuits.PrepareAttempt(ctx, gatewaycircuit.PrepareAttemptInput{
					Account:                      originalAccount,
					RequestLane:                  requestLane,
					Model:                        stringPtrOrNil(model),
					ConfirmationLeaseDurationMs:  confirmationLeaseDurationMs,
					ConfirmationEligible:         &confirmationEligible,
					ConfirmationFailuresRequired: confirmationFailuresRequired,
					Confirmation:                 args.AccountCircuitConfirmation,
					FailureEvidenceKey:           ptrString(accountCircuitFailureEvidenceKey),
				})
				if err != nil {
					return UpstreamDispatchResult{}, err
				}
				if preparation.Outcome == gatewaycircuit.PrepareBlocked {
					lastAttempt = accountCircuitBlockedAttempt(originalAccount, preparation.State.Phase)
					failedAccountIDs[originalAccount.ID] = struct{}{}
					continue
				}
				accountCircuitAttempt = preparation.Attempt
			}
			accountCircuitAttemptTransferred := false
			kind, singleResult, loopErr := e.dispatchSingleAccount(ctx, dispatchSingleAccountInput{
				args:                        &args,
				coordination:                coordination,
				originalAccount:             originalAccount,
				usageContext:                &usageContext,
				auditCapture:                auditCapture,
				settings:                    settings,
				timeoutProfile:              timeoutProfile,
				signal:                      signal,
				requestLane:                 requestLane,
				semanticRetryID:             semanticRetryID,
				bypassLocalSuppression:      bypassLocalSuppression,
				automaticAccountStateMutationAllowed: automaticAccountStateMutationAllowed,
				accountLockTrafficEnabled:   accountLockTrafficEnabled,
				compactionTimeoutsDisabled:  compactionTimeoutsDisabled,
				requestApiKeyAttemptCount:   &requestApiKeyAttemptCount,
				activeSameAccountRetryID:    &activeSameAccountRetryID,
				activeAccountLockRetryLease: &activeAccountLockRetryLease,
				activeAccountLockObservation: &activeAccountLockObservation,
				primaryDispatchTier:         primaryDispatchTier,
				observedEscapedTiers:        observedEscapedTiers,
								failedProxyDispatchKeys:     failedProxyDispatchKeys,
				failedAccountIDs:            failedAccountIDs,
				recoverableFailedAccountIDs: recoverableFailedAccountIDs,
				cycleRecoverableAccountIDs:  cycleRecoverableAccountIDs,
				capacityLimitFailures:       &capacityLimitFailures,
				pendingApiKeyFailures:       &pendingApiKeyFailures,
				lastAttempt:                 &lastAttempt,
				agentGuidanceResponse:       &agentGuidanceResponse,
				auditAttemptIndex:           &auditAttemptIndex,
				concurrencyRetryWaitBudgetMs: &concurrencyRetryWaitBudgetMs,
				keyModelFailureBudget:       keyModelFailureBudget,
				accountCircuitAttempt:       accountCircuitAttempt,
				setAccountCircuitAttemptTransferred: func() { accountCircuitAttemptTransferred = true },
				reserveSameAccountRetry:     reserveSameAccountRetry,
				createAccountLockLeaseRelease: func(bool) func(bool) bool { return createAccountLockLeaseRelease() },
			})
			if loopErr != nil {
				return UpstreamDispatchResult{}, loopErr
			}
			switch kind {
			case dispatchResultSelected:
				return *singleResult, nil
			case dispatchResultSkipRestOfCycle:
				skipRestOfCycle = true
			}
			_ = accountCircuitAttemptTransferred
			if skipRestOfCycle {
				break
			}
		}
		_ = skipRestOfCycle

		if len(capacityLimitFailures) > 0 && args.GroupSchedulingPolicy != nil {
			queueWaitStartedAtMs := NowMs()
			serverRetryBudget.BeginNoAvailableWait(&queueWaitStartedAtMs)
			queueWait, err := func() (QueueWaitResult, error) {
				defer serverRetryBudget.PauseNoAvailableWait(&queueWaitStartedAtMs)
				return e.HighConcurrencyQueue.WaitForCapacity(ctx, HighConcurrencyWaitInput{
					SystemAccountID:          usageContext.SystemAccountID,
					GroupID:                  usageContext.GroupID,
					APIKeyID:                 usageContext.APIKeyID,
					AccountIDs:               gatewaySessionConcurrencyIDs(dispatchAccounts),
					AccountConcurrencyLimits: GatewayAccountConcurrencyLimitsByAccountID(dispatchAccounts),
					Lane:                     requestLane,
					Policy:                   args.GroupSchedulingPolicy,
					MaxWaitMs:                serverRetryBudget.RemainingMs(&queueWaitStartedAtMs),
				})
			}()
			if err != nil {
				return UpstreamDispatchResult{}, err
			}
			highConcurrencyDispatchQueueWaitCount++
			auditCapture.AddGatewayMetadata("high_concurrency_dispatch_queue", map[string]any{
				"ready":     queueWait.Ready,
				"reason":    queueWait.Reason,
				"waitedMs":  queueWait.WaitedMs,
				"queueSize": queueWait.QueueSize,
				"lane":      requestLane,
				"waitCount": highConcurrencyDispatchQueueWaitCount,
				"source":    "account_concurrency_acquire",
			})
			if err := throwIfRequestAborted(signal); err != nil {
				return UpstreamDispatchResult{}, err
			}
			if queueWait.Ready {
				concurrencyRetryWaitBudgetMs = e.Config.AccountConcurrencyRetryBudgetMs
				reordered, err := e.Degradation.OrderWithLaneAsync(ctx, dispatchAccounts, requestLane, args.GroupSchedulingPolicy, args.ModelPriority)
				if err != nil {
					return UpstreamDispatchResult{}, err
				}
				dispatchAccounts = reordered.Accounts
				continue
			}
			if !serverRetryBudget.HandoffRequired(gatewaypreauth.AvailabilityRecoverableLater, nil) {
				if queueWait.Reason != "timeout" {
					retryDelayMs := minInt64(1000, serverRetryBudget.RemainingMs(nil))
					serverRetryBudget.BeginNoAvailableWait(nil)
					if err := waitForDelayMs(signal, retryDelayMs); err != nil {
						serverRetryBudget.PauseNoAvailableWait(nil)
						if err == context.Canceled {
							return UpstreamDispatchResult{}, &UpstreamRequestAbortedError{Message: "请求已取消"}
						}
						return UpstreamDispatchResult{}, err
					}
					serverRetryBudget.PauseNoAvailableWait(nil)
				}
				concurrencyRetryWaitBudgetMs = e.Config.AccountConcurrencyRetryBudgetMs
				continue
			}
			if failure := lastCapacityLimitFailure(capacityLimitFailures); failure != nil {
				auditAttemptIndex++
				if err := e.recordAccountCapacityLimitFailure(ctx, usageContext, failure.account, failure.message, auditCapture, auditAttemptIndex); err != nil {
					return UpstreamDispatchResult{}, err
				}
			}
		} else if len(capacityLimitFailures) > 0 {
			if !serverRetryBudget.HandoffRequired(gatewaypreauth.AvailabilityRecoverableLater, nil) {
				retryDelayMs := minInt64(500, serverRetryBudget.RemainingMs(nil))
				serverRetryBudget.BeginNoAvailableWait(nil)
				if err := waitForDelayMs(signal, retryDelayMs); err != nil {
					serverRetryBudget.PauseNoAvailableWait(nil)
					if err == context.Canceled {
						return UpstreamDispatchResult{}, &UpstreamRequestAbortedError{Message: "请求已取消"}
					}
					return UpstreamDispatchResult{}, err
				}
				serverRetryBudget.PauseNoAvailableWait(nil)
				concurrencyRetryWaitBudgetMs = e.Config.AccountConcurrencyRetryBudgetMs
				continue
			}
			if failure := lastCapacityLimitFailure(capacityLimitFailures); failure != nil {
				auditAttemptIndex++
				if err := e.recordAccountCapacityLimitFailure(ctx, usageContext, failure.account, failure.message, auditCapture, auditAttemptIndex); err != nil {
					return UpstreamDispatchResult{}, err
				}
			}
		}

		// Post-cycle suppression re-check + recoverable wait (Node
		// postCycleSuppressionFilter ... waitForRecoverableUnavailableState).
		var postCycleSuppressionFilter SuppressionFilterResult
		if bypassLocalSuppression {
			postCycleSuppressionFilter = localSuppressionBypassResult(dispatchAccounts)
		} else {
			filter, err := e.Suppression.FilterAsync(ctx, dispatchAccounts, SuppressionFilterOptions{})
			if err != nil {
				return UpstreamDispatchResult{}, err
			}
			postCycleSuppressionFilter = filter
		}
		recoverableAccountIDs := map[string]struct{}{}
		for _, id := range postCycleSuppressionFilter.SuppressedAccountIDs {
			recoverableAccountIDs[id] = struct{}{}
		}
		for _, id := range postCycleSuppressionFilter.PrecheckSuppressedAccountIDs {
			recoverableAccountIDs[id] = struct{}{}
		}
		for id := range cycleRecoverableAccountIDs {
			recoverableAccountIDs[id] = struct{}{}
		}
		var recoverableAccounts []AccountCandidate
		for _, account := range dispatchAccounts {
			if _, recoverable := recoverableAccountIDs[account.ID]; recoverable {
				recoverableAccounts = append(recoverableAccounts, account)
			}
		}
		if len(recoverableAccounts) == 0 || !args.WaitForRecoverableFailures {
			break
		}
		var suppressionFilter SuppressionFilterResult
		if len(recoverableAccounts) == len(dispatchAccounts) {
			suppressionFilter = postCycleSuppressionFilter
		} else {
			filter, err := e.Suppression.FilterAsync(ctx, recoverableAccounts, SuppressionFilterOptions{})
			if err != nil {
				return UpstreamDispatchResult{}, err
			}
			suppressionFilter = filter
		}
		if !suppressionFilter.AllSuppressed {
			if serverRetryBudget.HandoffRequired(gatewaypreauth.AvailabilityRecoverableLater, nil) {
				break
			}
			retryDelayMs := minInt64(3000, serverRetryBudget.RemainingMs(nil))
			accountIDs := make([]string, 0, len(suppressionFilter.Accounts))
			for _, account := range suppressionFilter.Accounts {
				accountIDs = append(accountIDs, account.ID)
			}
			auditCapture.AddGatewayMetadata("recoverable_upstream_failure_dispatch_wait", map[string]any{
				"accountIds":           accountIDs,
				"retryDelayMs":         retryDelayMs,
				"remainingWaitBudgetMs": serverRetryBudget.RemainingMs(nil),
			})
			serverRetryBudget.BeginNoAvailableWait(nil)
			if err := waitForDelayMs(signal, retryDelayMs); err != nil {
				serverRetryBudget.PauseNoAvailableWait(nil)
				if err == context.Canceled {
					return UpstreamDispatchResult{}, &UpstreamRequestAbortedError{Message: "请求已取消"}
				}
				return UpstreamDispatchResult{}, err
			}
			serverRetryBudget.PauseNoAvailableWait(nil)
			if serverRetryBudget.HandoffRequired(gatewaypreauth.AvailabilityRecoverableLater, nil) {
				break
			}
			for key := range failedProxyDispatchKeys {
				delete(failedProxyDispatchKeys, key)
			}
			degradation := e.Degradation.OrderSync(suppressionFilter.Accounts, args.ModelPriority)
			reordered := degradation.Accounts
			dispatchAccounts = reordered
			continue
		}
		precheckRuntimeScopes := suppressionFilter.PrecheckSuppressedRuntimeScopes
		allBlockedByPrecheck := len(precheckRuntimeScopes) > 0 &&
			suppressionFilter.PrecheckSuppressedAccountIDs != nil &&
			len(suppressionFilter.PrecheckSuppressedAccountIDs) == len(dispatchAccounts)
		waitStartedAtMs := NowMs()
		deadlineAtMs := serverRetryBudget.DeadlineAtMs(&waitStartedAtMs)
		scopeCandidates := make([]string, 0, len(precheckRuntimeScopes))
		for _, scope := range precheckRuntimeScopes {
			scopeCandidates = append(scopeCandidates, scope.RuntimeKey+"@"+formatInt64(scope.Generation))
		}
		sort.Strings(scopeCandidates)
		scopeKey := recoverableDispatchSuppressionScopeKey(
			usageContext.SystemAccountID,
			usageContext.APIKeyID,
			usageContext.GroupID,
			requestModelOrEmpty(args.Req),
			strings.Join(scopeCandidates, ","),
		)
		var waitState SuppressionFilterResult
		if e.RecoverableWait != nil {
			waitErr := func() error {
				defer serverRetryBudget.PauseNoAvailableWait(&waitStartedAtMs)
				state, err := e.RecoverableWait.WaitForState(ctx, SuppressionWaitInput{
					ScopeKey:  scopeKey,
					Reason:    waitReason(allBlockedByPrecheck),
					Refresh: func(ctx context.Context) (SuppressionFilterResult, error) {
						return e.Suppression.FilterAsync(ctx, recoverableAccounts, SuppressionFilterOptions{})
					},
					IsReady: func(state SuppressionFilterResult) bool { return !state.AllSuppressed },
					NextRetryAfterMs: func(state SuppressionFilterResult) *int64 { return state.NextRetryAfterMs },
					AuditCapture:             auditCapture,
					MaxWaitMs:                serverRetryBudget.RemainingMs(&waitStartedAtMs),
					RequestStartedAtMs:       waitStartedAtMs,
					DeadlineAtMs:             deadlineAtMs,
					RouteCoordinationBudget:  coordination.RouteCoordinationBudget,
					GatewayRequestWallBudget: gatewayRequestWallBudget,
					Signal:                   signal,
				})
				if err != nil {
					return err
				}
				waitState = state
				return nil
			}()
			if waitErr != nil {
				if waitErr == context.Canceled {
					return UpstreamDispatchResult{}, &UpstreamRequestAbortedError{Message: "请求已取消"}
				}
				return UpstreamDispatchResult{}, waitErr
			}
		} else {
			serverRetryBudget.PauseNoAvailableWait(&waitStartedAtMs)
			waitState = suppressionFilter
		}
		if !waitState.AllSuppressed {
			for key := range failedProxyDispatchKeys {
				delete(failedProxyDispatchKeys, key)
			}
			degradationWait := e.Degradation.OrderSync(waitState.Accounts, args.ModelPriority)
			reordered := degradationWait.Accounts
			dispatchAccounts = reordered
			continue
		}

		auditCapture.AddGatewayMetadata("local_account_suppression_dispatch_exhausted", map[string]any{
			"suppressedCount": suppressionFilter.SuppressedCount,
			"accountCount":    len(args.Accounts),
		})
		if err := throwIfRequestAborted(signal); err != nil {
			return UpstreamDispatchResult{}, err
		}
		first := firstAccountOr(args.Accounts)
		lastAttempt = &UpstreamAttempt{
			AccountID:                 firstAccountIDOr(args.Accounts, "local_suppression"),
			AccountName:               firstAccountNameOr(args.Accounts),
			ProviderCode:              first.ProviderCode,
			ProviderProtocolProfileID: first.ProviderProtocolProfileID,
			ProtocolCode:              first.ProtocolCode,
			ProtocolVersion:           first.ProtocolVersion,
			UpstreamURL:               "account:locally_suppressed",
			Message:                   "所有上游账户仍处于本地短期屏蔽",
		}
		break
	}

	return UpstreamDispatchResult{}, &UpstreamAttemptError{
		Message:               buildUpstreamAttemptFailureMessage(len(args.Accounts), lastAttempt),
		LastAttempt:           lastAttempt,
		FailedAccountIDs:      setToSlice(failedAccountIDs),
		AgentGuidanceResponse: agentGuidanceResponse,
		RecoverableAccountIDs: setToSlice(recoverableFailedAccountIDs),
	}
}

// waitReason mirrors the reason ternary.
func waitReason(allBlockedByPrecheck bool) string {
	if allBlockedByPrecheck {
		return "precheck_half_open"
	}
	return "local_account_suppression_dispatch"
}

// Account lock wait outcomes.
const (
	accountLockWaitCompleted   = "completed"
	accountLockWaitWall        = "wall"
	accountLockWaitCoordination = "coordination"
	accountLockWaitAborted     = "aborted"
)

// waitForAccountLockDelay mirrors waitForAccountLockDelay.
func (e *Engine) waitForAccountLockDelay(
	signal context.Context,
	accountID, leaseID string,
	delayMs int64,
	gatewayRequestWallBudget *gatewayrouting.GatewayRequestWallBudget,
	routeCoordinationBudget *gatewayrouting.RouteCoordinationBudget,
) (string, error) {
	delayMs = maxInt64(0, delayMs)
	if delayMs <= 0 {
		return accountLockWaitCompleted, nil
	}
	nowMs := NowMs()
	wallRemainingMs, err := gatewayRequestWallBudget.AvailableDecisionMs(gatewayrouting.GatewayRequestWallBudgetDecision{
		NowMs:                  &nowMs,
		FinalResponseReserveMs: ptrInt64(gatewayrouting.DefaultGatewayFinalResponseReserveMs),
	})
	if err != nil {
		return "", err
	}
	coordinationRemainingMs := routeCoordinationBudget.RemainingMs(nowMs)
	if delayMs > wallRemainingMs {
		return accountLockWaitWall, nil
	}
	if delayMs > coordinationRemainingMs {
		return accountLockWaitCoordination, nil
	}
	snapshot := routeCoordinationBudget.Snapshot(nowMs)
	waitToken := "account-lock:" + accountID + ":" + firstNonEmpty(leaseID, uuid4String())
	started, err := routeCoordinationBudget.BeginWait(gatewayrouting.RouteCoordinationBudgetTransitionInput{
		WaitToken:       waitToken,
		ExpectedVersion: snapshot.Version,
		NowMs:           &nowMs,
	})
	if err != nil {
		return "", err
	}
	if started.Outcome != gatewayrouting.BudgetTransitionApplied {
		return accountLockWaitCoordination, nil
	}
	defer func() {
		pauseNow := NowMs()
		_, _ = routeCoordinationBudget.PauseWait(gatewayrouting.RouteCoordinationBudgetTransitionInput{
			WaitToken:       waitToken,
			ExpectedVersion: started.Snapshot.Version,
			NowMs:           &pauseNow,
		})
	}()
	if err := waitForDelayMs(signal, delayMs); err != nil {
		return accountLockWaitAborted, nil
	}
	return accountLockWaitCompleted, nil
}

// AccountCapacityLimitFailure mirrors the local type.
type AccountCapacityLimitFailure struct {
	account AccountCandidate
	message string
}

func lastCapacityLimitFailure(failures []AccountCapacityLimitFailure) *AccountCapacityLimitFailure {
	if len(failures) == 0 {
		return nil
	}
	return &failures[len(failures)-1]
}

// recordAccountCapacityLimitFailure mirrors recordAccountCapacityLimitFailure.
func (e *Engine) recordAccountCapacityLimitFailure(
	ctx context.Context,
	usageContext gatewaypreauth.GatewayFailureUsageContext,
	account AccountCandidate,
	message string,
	auditCapture AuditCapture,
	auditAttemptIndex int,
) error {
	attemptStartedAt := NowMs()
	if e.Usage != nil {
		if err := e.Usage.RecordFailedUpstreamAttempt(ctx, nil, usageContext, account, FailedAttemptRecord{
			UpstreamURL:        "concurrency:limit",
			StartedAt:          attemptStartedAt,
			ErrorMessage:       message,
			FailureAttribution: "gateway_capacity",
		}); err != nil {
			return err
		}
	}
	auditCapture.RecordFailedDispatchAttempt(FailedDispatchAttemptInput{
		Account:       account,
		AttemptIndex:  auditAttemptIndex,
		UpstreamURL:   "concurrency:limit",
		StartedAtMs:   attemptStartedAt,
		ErrorPhase:    "dispatch",
		ErrorCode:     "account_concurrency_limit",
		ErrorMessage:  message,
	})
	return nil
}

// buildUpstreamAttemptFailureMessage mirrors buildUpstreamAttemptFailureMessage.
func buildUpstreamAttemptFailureMessage(accountCount int, lastAttempt *UpstreamAttempt) string {
	prefix := "所有上游账户均失败"
	if accountCount == 1 {
		prefix = "上游账户请求失败"
	}
	if lastAttempt == nil {
		return prefix
	}
	result := lastAttempt.Message
	if result == "" && lastAttempt.HasStatus {
		result = formatInt64(int64(lastAttempt.Status))
	}
	if result == "" {
		result = "未知错误"
	}
	upstreamURL := lastAttempt.UpstreamURL
	return prefix + "；最后一次尝试 " + lastAttempt.AccountName + " " + upstreamURL + " 返回 " + result
}

// recoverableDispatchSuppressionScopeKey mirrors
// recoverableDispatchSuppressionScopeKey.
func recoverableDispatchSuppressionScopeKey(systemAccountID, apiKeyID, groupID, model, candidates string) string {
	return strings.Join([]string{systemAccountID, apiKeyID, groupID, model, candidates}, ":")
}

// accountPhysicalCredentialKey mirrors accountPhysicalCredentialKey.
func accountPhysicalCredentialKey(account AccountCandidate) string {
	if account.CredentialSourceAccountID != nil {
		if trimmed := trimString(*account.CredentialSourceAccountID); trimmed != "" {
			return trimmed
		}
	}
	return account.ID
}

// gatewayAccountRuntimeKey mirrors gatewayAccountRuntimeKey
// (runtime/account-runtime-keys.ts).
func gatewayAccountRuntimeKey(account AccountCandidate) string {
	return strings.Join([]string{
		account.SystemAccountID,
		account.AccountOwnerSystemAccountID,
		account.ProviderCode,
		account.ProviderProtocolProfileID,
		account.ID,
	}, ":")
}

// gatewayAccountDispatchPriorityTier adapts the gatewayproxyhealth tier.
func gatewayAccountDispatchPriorityTier(account AccountCandidate, priority *gatewayrouting.GatewayAccountModelPriority) string {
	priorityValue := float64(account.Priority)
	return gatewayproxyhealthTierOf(gatewayproxyhealthViewOf(account), priorityValue, priority)
}

// gatewayForegroundAccountCircuitFailureEvidenceKey mirrors
// gatewayForegroundAccountCircuitFailureEvidenceKey.
func (e *Engine) gatewayForegroundAccountCircuitFailureEvidenceKey(req *gatewaypreauth.GatewayRequest, usageContext gatewaypreauth.GatewayFailureUsageContext) string {
	if session := e.explicitAccountCircuitSessionIdentity(req); session.source != "" && session.value != "" {
		return accountCircuitEvidenceDigest(map[string]any{
			"source":          "explicit_session",
			"systemAccountId": usageContext.SystemAccountID,
			"session":         session.value,
		})
	}
	clientIP := strings.ToLower(trimString(usageContext.ClientIP))
	if clientIP != "" {
		return accountCircuitEvidenceDigest(map[string]any{
			"source":          "gateway_caller",
			"systemAccountId": usageContext.SystemAccountID,
			"clientIp":        clientIP,
		})
	}
	return accountCircuitEvidenceDigest(map[string]any{
		"source":          "gateway_unknown_caller",
		"systemAccountId": usageContext.SystemAccountID,
	})
}

type circuitSessionIdentity struct {
	source string
	value  string
}

func (e *Engine) explicitAccountCircuitSessionIdentity(req *gatewaypreauth.GatewayRequest) circuitSessionIdentity {
	if e.SessionIdentity == nil {
		return circuitSessionIdentity{}
	}
	identity := e.SessionIdentity(req)
	value := trimString(identity.SessionID)
	if value == "" {
		return circuitSessionIdentity{}
	}
	source := identity.SemanticNamespace
	if source == "" {
		source = "gateway_session"
	}
	return circuitSessionIdentity{source: source, value: value}
}

// SessionIdentityView mirrors the consumed session identity fields.
type SessionIdentityView struct {
	SessionID        string
	SemanticNamespace string
}

func accountCircuitEvidenceDigest(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func setToSlice(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}

func firstAccountOr(accounts []AccountCandidate) AccountCandidate {
	if len(accounts) > 0 {
		return accounts[0]
	}
	return AccountCandidate{}
}

func firstAccountIDOr(accounts []AccountCandidate, fallback string) string {
	if len(accounts) > 0 {
		return accounts[0].ID
	}
	return fallback
}

func firstAccountNameOr(accounts []AccountCandidate) string {
	if len(accounts) == 1 {
		if accounts[0].Name != "" {
			return accounts[0].Name
		}
		return "上游账户"
	}
	return "上游账户"
}

func formatInt64(value int64) string {
	return int64ToString(value)
}

func int64ToString(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func ptrInt64(value int64) *int64 { return &value }

func ptrString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

var errMissingCoordination = &upstreamDispatchConfigError{message: "fetchFirstAvailableUpstream requires shared request coordination context"}

type upstreamDispatchConfigError struct{ message string }

func (e *upstreamDispatchConfigError) Error() string { return e.message }

// isTransientSameAccountHttpStatus mirrors isTransientSameAccountHttpStatus.
func isTransientSameAccountHttpStatus(status int) bool {
	return status == 408 || status == 425 || status == 429 || (status >= 500 && status <= 599)
}
