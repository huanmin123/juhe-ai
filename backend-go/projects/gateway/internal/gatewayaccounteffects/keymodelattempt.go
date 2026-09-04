package gatewayaccounteffects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"
)

// keyModelFailureIntentLimit mirrors keyModelFailureIntentLimit.
const keyModelFailureIntentLimit = 8

// GatewayKeyModelFailureBudget mirrors GatewayKeyModelFailureBudget.
type GatewayKeyModelFailureBudget struct {
	mu        sync.Mutex
	submitted map[string]struct{}
}

// NewGatewayKeyModelFailureBudget builds an empty budget.
func NewGatewayKeyModelFailureBudget() *GatewayKeyModelFailureBudget {
	return &GatewayKeyModelFailureBudget{submitted: map[string]struct{}{}}
}

// Claim mirrors claim.
func (b *GatewayKeyModelFailureBudget) Claim(hash string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.submitted == nil {
		b.submitted = map[string]struct{}{}
	}
	if _, ok := b.submitted[hash]; ok {
		return false
	}
	if len(b.submitted) >= keyModelFailureIntentLimit {
		return false
	}
	b.submitted[hash] = struct{}{}
	return true
}

// GatewayKeyModelCapability mirrors GatewayKeyModelCapability minus the
// request resolution: the caller (G02/G15 model mapping) resolves the
// capability from the request and hands it in.
type GatewayKeyModelCapability struct {
	AccountID    string
	Capability   CapabilityKey
	IsMainProbe  bool
}

// AttemptPreparationStatus mirrors the preparation union tags.
type AttemptPreparationStatus string

// Preparation statuses.
const (
	AttemptPreparationDisabled AttemptPreparationStatus = "disabled"
	AttemptPreparationBusy     AttemptPreparationStatus = "busy"
	AttemptPreparationBlocked  AttemptPreparationStatus = "blocked"
	AttemptPreparationAdmitted AttemptPreparationStatus = "admitted"
)

// GatewayKeyModelAttemptPreparation mirrors GatewayKeyModelAttemptPreparation.
type GatewayKeyModelAttemptPreparation struct {
	Status         AttemptPreparationStatus
	WakeSequence   int64
	CapabilityHash string
	Attempt        *GatewayKeyModelAttempt
}

// PrepareGatewayKeyModelAttemptInput mirrors PrepareGatewayKeyModelAttemptInput.
type PrepareGatewayKeyModelAttemptInput struct {
	Route         GatewayKeyModelCapability
	RequestID     string
	AttemptID     string
	FailureBudget *GatewayKeyModelFailureBudget
	// RecoveryTarget mirrors the Node getRequestContext() read; the request
	// context lives with the dispatch slices, so the caller supplies it.
	RecoveryTarget *KeyModelRecoveryTarget
	// Clock injects time for the failure observation timestamp.
	Clock Clock
	// Scheduler injects the renewal timer source (tests use a manual one).
	Scheduler Scheduler
	// Logger injects the failure logger.
	Logger Logger
}

// PrepareGatewayKeyModelAttempt mirrors prepareGatewayKeyModelAttempt. Both
// runtime modes use the same state-machine contract; the store selection is
// the caller's (SelectKeyModelRuntimeStore).
func PrepareGatewayKeyModelAttempt(ctx context.Context, store KeyModelRuntimeStore, input PrepareGatewayKeyModelAttemptInput) (GatewayKeyModelAttemptPreparation, error) {
	hash, err := CapabilityHash(input.Route.Capability)
	if err != nil {
		return GatewayKeyModelAttemptPreparation{}, err
	}
	admission, err := store.AdmitForeground(ctx, input.Route.Capability, input.AttemptID)
	if err != nil {
		return GatewayKeyModelAttemptPreparation{}, err
	}
	if admission.Status != ForegroundAdmitted {
		return GatewayKeyModelAttemptPreparation{Status: AttemptPreparationStatus(admission.Status), WakeSequence: admission.WakeSequence, CapabilityHash: hash}, nil
	}
	clock := input.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	scheduler := input.Scheduler
	if scheduler == nil {
		scheduler = RealScheduler{}
	}
	logger := input.Logger
	if logger == nil {
		logger = NopLogger{}
	}
	return GatewayKeyModelAttemptPreparation{
		Status:         AttemptPreparationAdmitted,
		CapabilityHash: hash,
		Attempt: newGatewayKeyModelAttempt(store, input.Route, hash, *admission.Permit, input.RequestID, input.AttemptID, input.FailureBudget, input.RecoveryTarget, clock, scheduler, logger),
	}, nil
}

// AccountHealthCheckDispatcher mirrors the dispatchAccountHealthCheck port.
type AccountHealthCheckDispatcher interface {
	DispatchAccountHealthCheck(accountID string, reason string, fence *KeyModelFenceReference)
}

// GatewayKeyModelAttempt mirrors GatewayKeyModelAttempt.
type GatewayKeyModelAttempt struct {
	store          KeyModelRuntimeStore
	route          GatewayKeyModelCapability
	capabilityHash string
	requestID      string
	attemptID      string
	failureBudget  *GatewayKeyModelFailureBudget
	recoveryTarget *KeyModelRecoveryTarget
	dispatcher     AccountHealthCheckDispatcher
	scheduler      Scheduler
	logger         Logger
	clock          Clock
	onPermitLost   func()

	mu             sync.Mutex
	permit         KeyModelForegroundPermit
	renewalTimer   SchedulerHandle
	released       bool
	permitLost     bool
	lostSignal     chan struct{}
	lostOnce       sync.Once
	terminal       chan struct{}
	terminalOnce   sync.Once
}

func newGatewayKeyModelAttempt(store KeyModelRuntimeStore, route GatewayKeyModelCapability, capabilityHash string, admission KeyModelForegroundPermit, requestID string, attemptID string, failureBudget *GatewayKeyModelFailureBudget, recoveryTarget *KeyModelRecoveryTarget, clock Clock, scheduler Scheduler, logger Logger) *GatewayKeyModelAttempt {
	attempt := &GatewayKeyModelAttempt{
		store:          store,
		route:          route,
		capabilityHash: capabilityHash,
		requestID:      requestID,
		attemptID:      attemptID,
		failureBudget:  failureBudget,
		recoveryTarget: recoveryTarget,
		permit:         admission,
		lostSignal:     make(chan struct{}),
		terminal:       make(chan struct{}),
		logger:         logger,
		scheduler:      scheduler,
		clock:          clock,
	}
	attempt.scheduleRenewal()
	return attempt
}

// SetDispatcher wires the health check dispatcher (optional).
func (a *GatewayKeyModelAttempt) SetDispatcher(dispatcher AccountHealthCheckDispatcher) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dispatcher = dispatcher
}

// SetObservability wires scheduler/logger/permit-lost hook for tests.
func (a *GatewayKeyModelAttempt) SetObservability(scheduler Scheduler, logger Logger) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scheduler = scheduler
	a.logger = logger
}

// SetPermitLostCallback wires the abort hook: Node cancels the request signal
// when the foreground permit is lost.
func (a *GatewayKeyModelAttempt) SetPermitLostCallback(onPermitLost func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onPermitLost = onPermitLost
}

// CapabilityHash mirrors the capabilityHash getter.
func (a *GatewayKeyModelAttempt) CapabilityHash() string { return a.capabilityHash }

// PermitLost returns a channel closed when the foreground permit is lost
// (renewal failure); transport code merges it into the request context.
func (a *GatewayKeyModelAttempt) PermitLost() <-chan struct{} { return a.lostSignal }

func (a *GatewayKeyModelAttempt) scheduleRenewal() {
	a.mu.Lock()
	a.renewalTimer = a.scheduler.After(KeyModelForegroundLeaseRenewMs, a.renew)
	a.mu.Unlock()
}

func (a *GatewayKeyModelAttempt) renew() {
	a.mu.Lock()
	if a.released || a.terminalClosed() {
		a.mu.Unlock()
		return
	}
	permit := a.permit
	a.mu.Unlock()
	renewed, err := a.store.RenewForeground(context.Background(), permit)
	if err != nil {
		a.mu.Lock()
		a.logger.Warn(a.failureLogFields("foreground_permit_renew_failed", err, ""), "Key-model state 操作失败")
		a.mu.Unlock()
		a.losePermit()
		return
	}
	if renewed == nil {
		a.losePermit()
		return
	}
	a.mu.Lock()
	a.permit = *renewed
	a.mu.Unlock()
	a.scheduleRenewal()
}

func (a *GatewayKeyModelAttempt) terminalClosed() bool {
	select {
	case <-a.terminal:
		return true
	default:
		return false
	}
}

func (a *GatewayKeyModelAttempt) losePermit() {
	a.mu.Lock()
	a.permitLost = true
	if a.renewalTimer != nil {
		a.renewalTimer.Cancel()
		a.renewalTimer = nil
	}
	callback := a.onPermitLost
	a.mu.Unlock()
	a.lostOnce.Do(func() { close(a.lostSignal) })
	if callback != nil {
		callback()
	}
}

func (a *GatewayKeyModelAttempt) stopRenewal() {
	a.mu.Lock()
	if a.renewalTimer != nil {
		a.renewalTimer.Cancel()
		a.renewalTimer = nil
	}
	a.mu.Unlock()
}

// MarkPrecommit mirrors markPrecommit.
func (a *GatewayKeyModelAttempt) MarkPrecommit() {
	a.stopRenewal()
	go func() {
		if err := a.release(); err != nil {
			a.mu.Lock()
			a.logger.Warn(a.failureLogFields("foreground_precommit_release_failed", err, ""), "Key-model state 操作失败")
			a.mu.Unlock()
		}
	}()
}

// ReportCompleteSuccess mirrors reportCompleteSuccess.
func (a *GatewayKeyModelAttempt) ReportCompleteSuccess(ctx context.Context) error {
	return a.settle(ctx, KeyModelOutcomeCompleteSuccess)
}

// ReportUpstreamNotComplete mirrors reportUpstreamNotComplete.
func (a *GatewayKeyModelAttempt) ReportUpstreamNotComplete(ctx context.Context) error {
	return a.settle(ctx, KeyModelOutcomeUpstreamNotComplete)
}

// ReportUnknown mirrors reportUnknown.
func (a *GatewayKeyModelAttempt) ReportUnknown(ctx context.Context) error {
	return a.settle(ctx, KeyModelOutcomeUnknown)
}

// WaitTerminal exposes the settle-once signal for tests.
func (a *GatewayKeyModelAttempt) WaitTerminal() <-chan struct{} { return a.terminal }

func (a *GatewayKeyModelAttempt) settle(ctx context.Context, outcome KeyModelOutcome) error {
	var settleErr error
	a.terminalOnce.Do(func() {
		a.mu.Lock()
		effective := outcome
		if a.permitLost {
			effective = KeyModelOutcomeUnknown
		}
		a.mu.Unlock()
		settleErr = a.settleOnce(ctx, effective)
		close(a.terminal)
	})
	// Node memoizes the terminal promise: every reporter observes the same
	// settled outcome.
	<-a.terminal
	return settleErr
}

func (a *GatewayKeyModelAttempt) settleOnce(ctx context.Context, outcome KeyModelOutcome) error {
	a.stopRenewal()
	if outcome != KeyModelOutcomeUpstreamNotComplete {
		a.releaseSafely(ctx, outcome)
		return nil
	}
	if a.route.IsMainProbe {
		if err := a.store.RecordMainProbeFailure(ctx, a.route.Capability, a.snapshotPermit()); err != nil {
			a.releaseSafely(ctx, KeyModelOutcomeUnknown)
			a.mu.Lock()
			a.logger.Warn(a.failureLogFields("main_probe_fence_write_failed", err, ""), "Key-model state 操作失败")
			a.mu.Unlock()
			return nil
		}
		a.markReleased()
		a.dispatchHealthCheck(a.route.AccountID, "request_failure", &KeyModelFenceReference{
			CapabilityHash:   a.capabilityHash,
			KeyFingerprint:   a.route.Capability.KeyFingerprint,
			DispatchRevision: a.route.Capability.DispatchRevision,
			OwnerID:          a.attemptID,
		})
		return nil
	}
	if !a.failureBudget.Claim(a.capabilityHash) {
		a.releaseSafely(ctx, KeyModelOutcomeUnknown)
		return nil
	}
	result, err := a.store.RecordFailure(ctx, KeyModelFailureIntent{
		IntentID:       a.requestID + ":" + a.attemptID,
		RequestID:      a.requestID,
		AttemptID:      a.attemptID,
		Capability:     a.route.Capability,
		ObservedAtMs:   a.nowMs(),
		Outcome:        KeyModelOutcomeUpstreamNotComplete,
		SourceFence:    sourceFence(a.route),
		Permit:         a.snapshotPermitPtr(),
		RecoveryTarget: a.recoveryTarget,
	})
	if err != nil {
		a.releaseSafely(ctx, KeyModelOutcomeUnknown)
		a.mu.Lock()
		a.logger.Warn(a.failureLogFields("key_model_failure_intent_write_failed", err, ""), "Key-model state 操作失败")
		a.mu.Unlock()
		return nil
	}
	if result.Status == KeyModelMutationApplied {
		claimed, claimErr := a.store.ClaimJ1Confirmation(ctx, a.route.Capability.CredentialSourceAccountID, a.route.Capability.DispatchRevision)
		if claimErr == nil && claimed {
			a.dispatchHealthCheck(a.route.Capability.CredentialSourceAccountID, "request_failure", nil)
		}
	}
	a.markReleased()
	return nil
}

func (a *GatewayKeyModelAttempt) nowMs() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return NowMs(a.clock)
}

func (a *GatewayKeyModelAttempt) snapshotPermit() KeyModelForegroundPermit {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.permit
}

func (a *GatewayKeyModelAttempt) snapshotPermitPtr() *KeyModelForegroundPermit {
	a.mu.Lock()
	defer a.mu.Unlock()
	permit := a.permit
	return &permit
}

func (a *GatewayKeyModelAttempt) markReleased() {
	a.mu.Lock()
	a.released = true
	a.mu.Unlock()
}

func (a *GatewayKeyModelAttempt) dispatchHealthCheck(accountID, reason string, fence *KeyModelFenceReference) {
	a.mu.Lock()
	dispatcher := a.dispatcher
	a.mu.Unlock()
	if dispatcher != nil {
		dispatcher.DispatchAccountHealthCheck(accountID, reason, fence)
	}
}

func (a *GatewayKeyModelAttempt) release() error {
	a.mu.Lock()
	if a.released {
		a.mu.Unlock()
		return nil
	}
	permit := a.permit
	a.mu.Unlock()
	if _, err := a.store.ReleaseForeground(context.Background(), permit); err != nil {
		return err
	}
	a.markReleased()
	return nil
}

func (a *GatewayKeyModelAttempt) releaseSafely(ctx context.Context, outcome KeyModelOutcome) {
	if err := a.release(); err != nil {
		a.mu.Lock()
		a.logger.Warn(a.failureLogFields("foreground_permit_release_failed", err, outcome), "Key-model state 操作失败")
		a.mu.Unlock()
	}
}

func (a *GatewayKeyModelAttempt) failureLogFields(event string, err error, outcome KeyModelOutcome) map[string]any {
	fields := map[string]any{
		"event":           event,
		"requestId":       a.requestID,
		"attemptId":       a.attemptID,
		"capabilityHash":  a.capabilityHash,
		"dispatchRevision": a.route.Capability.DispatchRevision,
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	if outcome != "" {
		fields["outcome"] = string(outcome)
	}
	return fields
}

// sourceFence mirrors sourceFence(route).
func sourceFence(route GatewayKeyModelCapability) string {
	digest := sha256.Sum256([]byte(route.Capability.CredentialSourceAccountID + ":" + strconv.FormatInt(route.Capability.DispatchRevision, 10)))
	return hex.EncodeToString(digest[:])
}

// KeyModelRuntimeStoreSelector mirrors getKeyModelRuntimeStore: one store per
// runtime state driver, rebuilt when the driver changes.
type KeyModelRuntimeStoreSelector struct {
	mu           sync.Mutex
	driver       string
	memoryStore  *InMemoryKeyModelRuntimeStore
	redisFactory func() (*RedisKeyModelRuntimeStore, error)
	redisStore   *RedisKeyModelRuntimeStore
}

// NewKeyModelRuntimeStoreSelector builds the selector; redisFactory is
// invoked lazily on the first redis request.
func NewKeyModelRuntimeStoreSelector(memoryStore *InMemoryKeyModelRuntimeStore, redisFactory func() (*RedisKeyModelRuntimeStore, error)) *KeyModelRuntimeStoreSelector {
	if memoryStore == nil {
		memoryStore = NewInMemoryKeyModelRuntimeStore(nil)
	}
	return &KeyModelRuntimeStoreSelector{memoryStore: memoryStore, redisFactory: redisFactory}
}

// Select mirrors getKeyModelRuntimeStore.
func (s *KeyModelRuntimeStoreSelector) Select(driver string) (KeyModelRuntimeStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.driver == driver {
		if driver == "redis" {
			if s.redisStore != nil {
				return s.redisStore, nil
			}
		} else {
			return s.memoryStore, nil
		}
	}
	s.driver = driver
	if driver == "redis" {
		if s.redisFactory == nil {
			return nil, fmt.Errorf("启用 Key-model Redis state 必须配置 JUHE_AI_REDIS_STATE_URL")
		}
		store, err := s.redisFactory()
		if err != nil {
			s.driver = ""
			return nil, err
		}
		s.redisStore = store
		return store, nil
	}
	return s.memoryStore, nil
}
