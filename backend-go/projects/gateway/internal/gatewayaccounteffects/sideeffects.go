package gatewayaccounteffects

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Side effect retention: 10 minutes.
const SideEffectRetentionMs = int64(10 * 60_000)

// Failure storm bookkeeping constants (runtime defaults, not configurable).
const (
	FailureStormWindowMs               = int64(5 * 60_000)
	FailureStormThresholdCount         = 5
	FailureStormDistinctIPThreshold    = 2
	FailureStormMinObservationMs       = int64(60_000)
	FailureStormRecentSuccessGraceMs   = int64(5_000)
	FailureStormFailureRatioThreshold  = 0.9
)

// SideEffectsConfig mirrors the runtimeConfig.gateway inputs of
// account-side-effects.service.ts with the Node defaults.
type SideEffectsConfig struct {
	// RuntimeStateDriver mirrors runtimeConfig.runtimeStateDriver:
	// "memory" (process-local) or "redis" (distributed).
	RuntimeStateDriver string
	QueueMaxLength     int   // JUHE_AI_GATEWAY_ACCOUNT_SIDE_EFFECT_QUEUE_MAX_LENGTH (5000)
	RetryInitialDelayMs int64 // JUHE_AI_GATEWAY_ACCOUNT_SIDE_EFFECT_RETRY_INITIAL_DELAY_MS (500)
	RetryMaxDelayMs     int64 // JUHE_AI_GATEWAY_ACCOUNT_SIDE_EFFECT_RETRY_MAX_DELAY_MS (30000)
}

func (c *SideEffectsConfig) fill() {
	if c.QueueMaxLength == 0 {
		c.QueueMaxLength = 5_000
	}
	if c.RetryInitialDelayMs == 0 {
		c.RetryInitialDelayMs = 500
	}
	if c.RetryMaxDelayMs == 0 {
		c.RetryMaxDelayMs = 30_000
	}
}

// IsRedisDriver mirrors runtimeConfig.runtimeStateDriver === 'redis'.
func (c *SideEffectsConfig) IsRedisDriver() bool { return c.RuntimeStateDriver == "redis" }

// AccountErrorHandlingResult mirrors AccountErrorHandlingResult consumed here:
// changed plus the resulting account status.
type AccountErrorHandlingResult struct {
	Changed       bool
	AccountStatus string
}

// SideEffectWriter is the requestGatewayDbService port for the queued
// operation. The db-service adapter (SQLite/PostgreSQL dual mode) lives in
// the composition root; this package only sees the operation contract.
type SideEffectWriter interface {
	ApplyAccountErrorHandling(ctx context.Context, operation AccountSideEffectOperation) (AccountErrorHandlingResult, error)
}

// WriterFunc adapts a function to SideEffectWriter.
type WriterFunc func(ctx context.Context, operation AccountSideEffectOperation) (AccountErrorHandlingResult, error)

// ApplyAccountErrorHandling implements SideEffectWriter.
func (f WriterFunc) ApplyAccountErrorHandling(ctx context.Context, operation AccountSideEffectOperation) (AccountErrorHandlingResult, error) {
	return f(ctx, operation)
}

// RecoveryProbeScheduleInput mirrors the scheduleGatewayAccountRecoveryProbe
// input. The probe state machine itself (timers, half-open leases, precheck
// promotion) is owned by the account-circuit slice (G11); this package only
// forwards the scheduling decision with the recorded storm evidence.
type RecoveryProbeScheduleInput struct {
	RuntimeKey             string
	Account                gatewayruntimecache.OpenAIAccountSecret
	Settings               *gatewayruntimecache.GatewaySettings
	SystemAccountID        string
	GroupID                string
	Reason                 string
	FailureCount           int
	DistinctClientIPCount  int
	DistinctAPIKeyCount    int
	PrecheckRequested      bool
	LocalSuppressionDelayMs *int64
	FirstSeenAtMs          int64
	NowMs                  int64
}

// FailureStormEntry mirrors FailureStormEntry.
type FailureStormEntry struct {
	FirstSeenMs int64
	LastSeenMs  int64
	FailureCount int
	ClientIPs   map[string]struct{}
	APIKeyIDs   map[string]struct{}
}

// SuccessObservationEntry mirrors SuccessObservationEntry.
type SuccessObservationEntry struct {
	FirstSeenMs int64
	LastSeenMs  int64
	SuccessCount int
}

// FailureStormPrecheckDecision mirrors FailureStormPrecheckDecision.
type FailureStormPrecheckDecision struct {
	Trigger        bool
	SuccessCount   int
	FailureRatio   float64
	SkippedReason  string // below_threshold | observation_window | recent_success | failure_ratio
}

// Skipped reasons.
const (
	StormSkippedBelowThreshold     = "below_threshold"
	StormSkippedObservationWindow  = "observation_window"
	StormSkippedRecentSuccess      = "recent_success"
	StormSkippedFailureRatio       = "failure_ratio"
)

// GatewayAccountFailurePrecheckInput mirrors GatewayAccountFailurePrecheckInput.
type GatewayAccountFailurePrecheckInput struct {
	SystemAccountID          string
	GroupID                  string
	APIKeyID                 string
	ClientIP                 string
	Endpoint                 string
	Reason                   string
	StatusCode               *int64
	ForcePrecheck            bool
	LocalSuppressionDelayMs  *int64
}

// GatewayAccountRuntimeClearResult mirrors GatewayAccountRuntimeClearResult.
type GatewayAccountRuntimeClearResult struct {
	Cleared    bool
	ClearedKeys []string
	FailedKeys  []string
}

// SideEffectDeps wires the service ports.
type SideEffectDeps struct {
	Clock     Clock
	Random    func() float64
	Scheduler Scheduler
	Logger    Logger
	// Writer executes the queued operation (db-service port).
	Writer SideEffectWriter
	// ClearRuntimeAvailabilityLocal mirrors
	// clearGatewayAccountRuntimeAvailabilityLocal (G11 suppression store +
	// precheck state): returns true when something was cleared.
	ClearRuntimeAvailabilityLocal func(runtimeKey string) bool
	// ClearDistributedRuntimeAvailability mirrors clearDistributedRecoveryProbeState
	// for the redis runtime state driver.
	ClearDistributedRuntimeAvailability func(ctx context.Context, runtimeKey string) error
	// InvalidateRuntimeCache mirrors clearGatewayRuntimeCache().
	InvalidateRuntimeCache func()
	// ScheduleRecoveryProbe mirrors scheduleGatewayAccountRecoveryProbe for the
	// process-local driver (G11 probe machinery).
	ScheduleRecoveryProbe func(input RecoveryProbeScheduleInput)
	// RecordDistributedFailureForPrecheck mirrors
	// recordDistributedGatewayAccountFailureForPrecheck for the redis driver.
	RecordDistributedFailureForPrecheck func(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, input GatewayAccountFailurePrecheckInput)
	// CleanupLocalSuppressions mirrors cleanupExpiredLocalSuppressions.
	CleanupLocalSuppressions func()
}

// SideEffectsService is the queue-driven half of
// account-side-effects.service.ts.
type SideEffectsService struct {
	config SideEffectsConfig
	clock  Clock
	random func() float64
	sched  Scheduler
	logger Logger
	deps   SideEffectDeps

	mu                  sync.Mutex
	queue               *AccountSideEffectQueue
	epochs              *AccountSideEffectEpochRegistry
	failureStorms       map[string]*FailureStormEntry
	successObservations map[string]*SuccessObservationEntry

	processing             bool
	drainTimer             SchedulerHandle
	drainTimerDueAtMs      *int64
	enqueuedCount          int64
	completedCount         int64
	coalescedCount         int64
	canceledBySuccessCount int64
	skippedHealthySuccessCount int64
	failedAttemptCount     int64
	droppedCount           int64
	expiredCount           int64
	staleCount             int64
	evictedFailureForSuccessCount int64
}

// NewSideEffectsService builds the service; the epoch registry uses the Node
// default capacity (20000).
func NewSideEffectsService(config SideEffectsConfig, deps SideEffectDeps) (*SideEffectsService, error) {
	config.fill()
	if deps.Writer == nil {
		return nil, errors.New("gatewayaccounteffects 需要 SideEffectDeps.Writer")
	}
	registry, err := NewAccountSideEffectEpochRegistry(0)
	if err != nil {
		return nil, err
	}
	clock := deps.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	scheduler := deps.Scheduler
	if scheduler == nil {
		scheduler = RealScheduler{}
	}
	logger := deps.Logger
	if logger == nil {
		logger = NopLogger{}
	}
	random := deps.Random
	if random == nil {
		random = mathRandom
	}
	return &SideEffectsService{
		config:              config,
		clock:               clock,
		random:              random,
		sched:               scheduler,
		logger:              logger,
		deps:                deps,
		queue:               NewAccountSideEffectQueue(),
		epochs:              registry,
		failureStorms:       map[string]*FailureStormEntry{},
		successObservations: map[string]*SuccessObservationEntry{},
	}, nil
}

func mathRandom() float64 { return rand.Float64() }

// sideEffectRetryDelayMs mirrors retryDelayMs(exponentialRetryPolicy(...), n).
func (s *SideEffectsService) sideEffectRetryDelayMs(retryNumber int) int64 {
	initial := s.config.RetryInitialDelayMs
	maxDelay := s.config.RetryMaxDelayMs
	n := retryNumber
	if n < 1 {
		n = 1
	}
	delay := int64(math.Min(float64(initial)*math.Pow(2, float64(n-1)), float64(maxDelay)))
	return delay
}

// retryDueAtMs mirrors retryDueAtMs: now + passiveScheduleDelayMs(delay).
func (s *SideEffectsService) retryDueAtMs(retryNumber int, nowMs int64) int64 {
	return nowMs + passiveScheduleDelayMs(s.sideEffectRetryDelayMs(retryNumber), s.random)
}

// normalizedObservedAccountSideEffectOperation mirrors the normalize helper.
func normalizedObservedAccountSideEffectOperation(operation AccountSideEffectOperation) (AccountSideEffectOperation, error) {
	_, observedAt, err := requiredRfc3339Instant(operation.Input.ObservedAt, "account side effect observedAt")
	if err != nil {
		return AccountSideEffectOperation{}, err
	}
	dispatchRevision := operation.Input.DispatchRevision
	if dispatchRevision == nil {
		dispatchRevision = operation.Account.DispatchRevision
	}
	normalized := operation
	normalized.Type = AccountSideEffectOperationType
	normalized.Input.ObservedAt = observedAt
	normalized.Input.DispatchRevision = normalizedDispatchRevision(dispatchRevision)
	return normalized, nil
}

// EnqueueGatewayAccountErrorHandlingSideEffect mirrors
// enqueueGatewayAccountErrorHandlingSideEffect.
func (s *SideEffectsService) EnqueueGatewayAccountErrorHandlingSideEffect(ctx context.Context, operation AccountSideEffectOperation) error {
	if operation.Input.TrafficSource == "gateway" && !operation.Input.Success && operation.Input.PolicyDecision == nil {
		return nil
	}
	observedOperation, err := normalizedObservedAccountSideEffectOperation(operation)
	if err != nil {
		return err
	}
	runtimeKey, err := AccountErrorHandlingOperationRuntimeKey(observedOperation)
	if err != nil {
		return err
	}

	s.mu.Lock()
	// 容量预检与入队之间没有 await；先拒绝无法入队的观测，避免它把已排队 watermark 变成 stale。
	if !s.canAdmitAccountSideEffectWithoutDroppingLocked(observedOperation, runtimeKey) {
		s.droppedCount++
		s.logDroppedAccountSideEffectLocked(observedOperation)
		s.mu.Unlock()
		return nil
	}
	observation, err := s.epochs.Observe(runtimeKey, EpochObservation{
		ObservedAt:       observedOperation.Input.ObservedAt,
		Success:          observedOperation.Input.Success,
		DispatchRevision: observedOperation.Input.DispatchRevision,
		Retain:           true,
	})
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if !observation.Accepted {
		s.staleCount++
		s.mu.Unlock()
		return nil
	}
	canceledCount := 0
	if observedOperation.Input.Success {
		s.recordGatewayAccountSuccessObservationLocked(runtimeKey)
		canceledCount = s.cancelQueuedAccountErrorHandlingSideEffectsForRuntimeKeyLocked(runtimeKey)
		if canceledCount > 0 {
			s.canceledBySuccessCount += int64(canceledCount)
		}
	} else if s.coalesceQueuedAccountErrorHandlingSideEffectLocked(observedOperation, observation.Epoch) {
		s.mu.Unlock()
		return nil
	}
	s.enqueueAccountSideEffectLocked(observedOperation, observation.Epoch)
	s.mu.Unlock()

	if observedOperation.Input.Success {
		if _, err := s.clearGatewayAccountRuntimeAvailabilityForRuntimeKey(ctx, runtimeKey); err != nil {
			return err
		}
	}
	return nil
}

func (s *SideEffectsService) canAdmitAccountSideEffectWithoutDroppingLocked(operation AccountSideEffectOperation, runtimeKey string) bool {
	if s.queue.Len() < s.config.QueueMaxLength {
		return true
	}
	if s.queue.HasRuntimeKey(runtimeKey) {
		return true
	}
	return operation.Input.Success && s.queue.HasFailures()
}

func (s *SideEffectsService) logDroppedAccountSideEffectLocked(operation AccountSideEffectOperation) {
	if s.droppedCount > 10 && s.droppedCount%100 != 0 {
		return
	}
	s.logger.Warn(map[string]any{
		"event":         "gateway_account_side_effect_queue_full",
		"operationType": operation.Type,
		"accountId":     operation.Account.ID,
		"queueLength":   s.queue.Len(),
		"droppedCount":  s.droppedCount,
	}, "网关账号副作用队列已满，已丢弃本次副作用")
}

func (s *SideEffectsService) recordGatewayAccountSuccessObservationLocked(runtimeKey string) {
	if !s.canUseProcessLocalGatewayAccountRuntimeStateLocked() {
		return
	}
	s.cleanupExpiredFailureStormsLocked()
	now := NowMs(s.clock)
	current := s.successObservations[runtimeKey]
	var entry *SuccessObservationEntry
	if current != nil && now-current.FirstSeenMs <= FailureStormWindowMs {
		entry = current
	} else {
		entry = &SuccessObservationEntry{FirstSeenMs: now, LastSeenMs: now}
	}
	entry.LastSeenMs = now
	entry.SuccessCount++
	s.successObservations[runtimeKey] = entry
}

func (s *SideEffectsService) cancelQueuedAccountErrorHandlingSideEffectsForRuntimeKeyLocked(runtimeKey string) int {
	canceled := s.queue.RemoveRuntimeKey(runtimeKey)
	for _, item := range canceled {
		s.epochs.Release(item.Epoch)
	}
	return len(canceled)
}

func (s *SideEffectsService) coalesceQueuedAccountErrorHandlingSideEffectLocked(operation AccountSideEffectOperation, epoch AccountSideEffectEpoch) bool {
	index := s.queue.FindIndexByRuntimeKey(s.mustRuntimeKey(operation))
	if index < 0 {
		return false
	}
	now := NowMs(s.clock)
	replaced := s.queue.ReplaceAt(index, &QueuedAccountSideEffect{
		Operation:       operation,
		Epoch:           epoch,
		Attempts:        0,
		EnqueuedAtMs:    now,
		NextAttemptAtMs: now,
		ExpiresAtMs:     now + SideEffectRetentionMs,
	})
	if replaced == nil {
		return false
	}
	s.epochs.Release(replaced.Epoch)
	s.coalescedCount++
	s.scheduleSideEffectDrainLocked(0)
	return true
}

func (s *SideEffectsService) mustRuntimeKey(operation AccountSideEffectOperation) string {
	key, err := AccountErrorHandlingOperationRuntimeKey(operation)
	if err != nil {
		return operation.Account.ID
	}
	return key
}

func (s *SideEffectsService) enqueueAccountSideEffectLocked(operation AccountSideEffectOperation, epoch AccountSideEffectEpoch) {
	if s.queue.Len() >= s.config.QueueMaxLength {
		var evictedFailure *QueuedAccountSideEffect
		if operation.Input.Success {
			evictedFailure = s.queue.RemoveOldestFailure()
		}
		if evictedFailure != nil {
			s.epochs.Release(evictedFailure.Epoch)
			s.droppedCount++
			s.evictedFailureForSuccessCount++
			s.logger.Warn(map[string]any{
				"event":                        "gateway_account_side_effect_failure_evicted_for_success",
				"evictedAccountId":             evictedFailure.Operation.Account.ID,
				"successAccountId":             operation.Account.ID,
				"queueLength":                  s.queue.Len(),
				"evictedFailureForSuccessCount": s.evictedFailureForSuccessCount,
			}, "网关账号副作用队列已满，已为成功 watermark 淘汰最早失败写入")
		} else {
			s.epochs.Release(epoch)
			s.droppedCount++
			s.logDroppedAccountSideEffectLocked(operation)
			return
		}
	}
	now := NowMs(s.clock)
	s.queue.Push(&QueuedAccountSideEffect{
		Operation:       operation,
		Epoch:           epoch,
		Attempts:        0,
		EnqueuedAtMs:    now,
		NextAttemptAtMs: now,
		ExpiresAtMs:     now + SideEffectRetentionMs,
	})
	s.enqueuedCount++
	s.scheduleSideEffectDrainLocked(0)
}

// scheduleSideEffectDrainLocked mirrors scheduleSideEffectDrain.
func (s *SideEffectsService) scheduleSideEffectDrainLocked(delayMs int64) {
	now := NowMs(s.clock)
	dueAtMs := now + maxInt64(0, delayMs)
	if s.drainTimer != nil {
		if s.drainTimerDueAtMs != nil && *s.drainTimerDueAtMs <= dueAtMs {
			return
		}
		s.drainTimer.Cancel()
		s.drainTimer = nil
		s.drainTimerDueAtMs = nil
	}
	s.drainTimerDueAtMs = &dueAtMs
	s.drainTimer = s.sched.After(maxInt64(0, delayMs), func() {
		s.mu.Lock()
		s.drainTimer = nil
		s.drainTimerDueAtMs = nil
		s.mu.Unlock()
		// Node: `void drainSideEffectQueue()` — the timer callback runs the
		// drain inline; the processing flag keeps it single-flight.
		s.Drain(context.Background())
	})
}

// Drain mirrors drainSideEffectQueue: single-flight; due items execute in
// order with retry/expiry/stale terminal handling.
func (s *SideEffectsService) Drain(ctx context.Context) {
	s.mu.Lock()
	if s.processing {
		s.mu.Unlock()
		return
	}
	s.processing = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.processing = false
		s.scheduleNextDrainIfNeededLocked()
		s.mu.Unlock()
	}()

	for {
		s.mu.Lock()
		if s.queue.Len() == 0 {
			s.mu.Unlock()
			return
		}
		now := NowMs(s.clock)
		item := s.queue.Peek()
		if item == nil {
			s.mu.Unlock()
			return
		}
		dueAtMs := minI64(item.NextAttemptAtMs, item.ExpiresAtMs)
		if dueAtMs > now {
			s.scheduleSideEffectDrainLocked(dueAtMs - now)
			s.mu.Unlock()
			return
		}
		s.queue.Pop()
		if item.ExpiresAtMs <= now {
			s.epochs.Release(item.Epoch)
			s.expiredCount++
			s.mu.Unlock()
			continue
		}
		if !s.epochs.IsCurrent(item.Epoch) {
			s.epochs.Release(item.Epoch)
			s.staleCount++
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()

		err := s.executeAccountSideEffect(ctx, item)

		s.mu.Lock()
		if err == nil {
			s.epochs.Release(item.Epoch)
			s.completedCount++
			s.mu.Unlock()
			continue
		}
		s.failedAttemptCount++
		if !s.epochs.IsCurrent(item.Epoch) {
			s.epochs.Release(item.Epoch)
			s.staleCount++
			s.mu.Unlock()
			continue
		}
		if NowMs(s.clock) >= item.ExpiresAtMs {
			s.epochs.Release(item.Epoch)
			s.expiredCount++
			s.logger.Warn(map[string]any{
				"event":         "gateway_account_side_effect_expired",
				"operationType": item.Operation.Type,
				"accountId":     item.Operation.Account.ID,
				"attempts":      item.Attempts + 1,
			}, "网关账号副作用写入超过重试窗口，已丢弃")
			s.mu.Unlock()
			continue
		}
		item.Attempts++
		item.NextAttemptAtMs = s.retryDueAtMs(item.Attempts, NowMs(s.clock))
		s.queue.Push(item)
		s.logger.Warn(map[string]any{
			"event":         "gateway_account_side_effect_retry_scheduled",
			"operationType": item.Operation.Type,
			"accountId":     item.Operation.Account.ID,
			"attempts":      item.Attempts,
			"retryAt":       canonicalRFC3339(time.UnixMilli(item.NextAttemptAtMs)),
		}, "网关账号副作用写入失败，已加入重试")
		s.scheduleSideEffectDrainLocked(minI64(item.NextAttemptAtMs, item.ExpiresAtMs) - NowMs(s.clock))
		s.mu.Unlock()
		return
	}
}

// executeAccountSideEffect mirrors executeAccountSideEffect.
func (s *SideEffectsService) executeAccountSideEffect(ctx context.Context, item *QueuedAccountSideEffect) error {
	result, err := s.deps.Writer.ApplyAccountErrorHandling(ctx, item.Operation)
	if err != nil {
		return err
	}
	runtimeKey, keyErr := GatewayAccountRuntimeKeyForSecret(item.Operation.Account)
	if keyErr != nil {
		runtimeKey = item.Operation.Account.ID
	}
	if result.Changed {
		if _, clearErr := s.clearGatewayAccountRuntimeAvailabilityForRuntimeKey(ctx, runtimeKey); clearErr != nil {
			return clearErr
		}
		s.invalidateRuntimeCache()
	} else if item.Operation.Input.Success && result.AccountStatus == "active" {
		if _, clearErr := s.clearGatewayAccountRuntimeAvailabilityForRuntimeKey(ctx, runtimeKey); clearErr != nil {
			return clearErr
		}
	}
	return nil
}

// scheduleNextDrainIfNeededLocked mirrors scheduleNextDrainIfNeeded.
func (s *SideEffectsService) scheduleNextDrainIfNeededLocked() {
	if s.processing || s.drainTimer != nil || s.queue.Len() == 0 {
		return
	}
	item := s.queue.Peek()
	if item != nil {
		now := NowMs(s.clock)
		s.scheduleSideEffectDrainLocked(maxInt64(0, minI64(item.NextAttemptAtMs, item.ExpiresAtMs)-now))
	}
}

// clearGatewayAccountRuntimeAvailabilityForRuntimeKey mirrors the internal
// helper of the same name.
func (s *SideEffectsService) clearGatewayAccountRuntimeAvailabilityForRuntimeKey(ctx context.Context, runtimeKey string) (bool, error) {
	if s.config.IsRedisDriver() {
		if s.deps.ClearDistributedRuntimeAvailability != nil {
			if err := s.deps.ClearDistributedRuntimeAvailability(ctx, runtimeKey); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	return s.clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey), nil
}

// clearGatewayAccountRuntimeAvailabilityLocal delegates to the G11 port.
func (s *SideEffectsService) clearGatewayAccountRuntimeAvailabilityLocal(runtimeKey string) bool {
	if !s.canUseProcessLocalGatewayAccountRuntimeState() {
		return false
	}
	if s.deps.ClearRuntimeAvailabilityLocal == nil {
		return false
	}
	return s.deps.ClearRuntimeAvailabilityLocal(runtimeKey)
}

// canUseProcessLocalGatewayAccountRuntimeStateLocked mirrors
// canUseProcessLocalGatewayAccountRuntimeState for locked contexts.
func (s *SideEffectsService) canUseProcessLocalGatewayAccountRuntimeStateLocked() bool {
	if !s.config.IsRedisDriver() {
		return true
	}
	s.failureStorms = map[string]*FailureStormEntry{}
	s.successObservations = map[string]*SuccessObservationEntry{}
	return false
}

// canUseProcessLocalGatewayAccountRuntimeState mirrors the unlocked variant.
func (s *SideEffectsService) canUseProcessLocalGatewayAccountRuntimeState() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.canUseProcessLocalGatewayAccountRuntimeStateLocked()
}

func (s *SideEffectsService) cleanupExpiredFailureStormsLocked() {
	if !s.canUseProcessLocalGatewayAccountRuntimeStateLocked() {
		return
	}
	now := NowMs(s.clock)
	for runtimeKey, entry := range s.failureStorms {
		if now-entry.LastSeenMs > FailureStormWindowMs {
			delete(s.failureStorms, runtimeKey)
		}
	}
	for runtimeKey, entry := range s.successObservations {
		if now-entry.LastSeenMs > FailureStormWindowMs {
			delete(s.successObservations, runtimeKey)
		}
	}
}

// RecordGatewayAccountFailureForPrecheck mirrors
// recordGatewayAccountFailureForPrecheck (runPrecheck=true).
func (s *SideEffectsService) RecordGatewayAccountFailureForPrecheck(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, input GatewayAccountFailurePrecheckInput) {
	s.recordGatewayAccountFailureForPrecheckInternal(ctx, account, input, true)
}

// RecordGatewayAccountFailureObservation mirrors
// recordGatewayAccountFailureForPrecheckForTest: bookkeeping without
// scheduling.
func (s *SideEffectsService) RecordGatewayAccountFailureObservation(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, input GatewayAccountFailurePrecheckInput) {
	s.recordGatewayAccountFailureForPrecheckInternal(ctx, account, input, false)
}

func (s *SideEffectsService) recordGatewayAccountFailureForPrecheckInternal(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, input GatewayAccountFailurePrecheckInput, runPrecheck bool) {
	if s.config.IsRedisDriver() {
		if runPrecheck && s.deps.RecordDistributedFailureForPrecheck != nil {
			go func() {
				s.deps.RecordDistributedFailureForPrecheck(context.WithoutCancel(ctx), account, input)
			}()
		}
		return
	}
	if !s.canUseProcessLocalGatewayAccountRuntimeState() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredFailureStormsLocked()
	if s.deps.CleanupLocalSuppressions != nil {
		s.deps.CleanupLocalSuppressions()
	}
	runtimeKey := mustSecretRuntimeKey(account)
	now := NowMs(s.clock)
	current := s.failureStorms[runtimeKey]
	var entry *FailureStormEntry
	if current != nil && now-current.FirstSeenMs <= FailureStormWindowMs {
		entry = current
	} else {
		entry = &FailureStormEntry{FirstSeenMs: now, LastSeenMs: now, ClientIPs: map[string]struct{}{}, APIKeyIDs: map[string]struct{}{}}
	}
	entry.LastSeenMs = now
	entry.FailureCount++
	if input.ClientIP != "" {
		entry.ClientIPs[input.ClientIP] = struct{}{}
	}
	if input.APIKeyID != "" {
		entry.APIKeyIDs[input.APIKeyID] = struct{}{}
	}
	s.failureStorms[runtimeKey] = entry

	if runPrecheck && s.deps.ScheduleRecoveryProbe != nil {
		s.deps.ScheduleRecoveryProbe(RecoveryProbeScheduleInput{
			RuntimeKey:              runtimeKey,
			Account:                 account,
			SystemAccountID:         input.SystemAccountID,
			GroupID:                 input.GroupID,
			Reason:                  input.Reason,
			FailureCount:            entry.FailureCount,
			DistinctClientIPCount:   len(entry.ClientIPs),
			DistinctAPIKeyCount:     len(entry.APIKeyIDs),
			PrecheckRequested:       input.ForcePrecheck,
			LocalSuppressionDelayMs: input.LocalSuppressionDelayMs,
			FirstSeenAtMs:           entry.FirstSeenMs,
			NowMs:                   now,
		})
	}
}

// ShouldTriggerFailureStormPrecheck mirrors shouldTriggerFailureStormPrecheck
// against the live registries.
func (s *SideEffectsService) ShouldTriggerFailureStormPrecheck(runtimeKey string, entry FailureStormEntry, forcePrecheck bool, now int64) FailureStormPrecheckDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return shouldTriggerFailureStormPrecheck(s.successObservations[runtimeKey], entry, forcePrecheck, now)
}

func shouldTriggerFailureStormPrecheck(successObservation *SuccessObservationEntry, entry FailureStormEntry, forcePrecheck bool, now int64) FailureStormPrecheckDecision {
	successCount := 0
	if successObservation != nil {
		successCount = successObservation.SuccessCount
	}
	total := entry.FailureCount + successCount
	failureRatio := 1.0
	if total > 0 {
		failureRatio = float64(entry.FailureCount) / float64(total)
	}
	if forcePrecheck {
		if now-entry.FirstSeenMs >= FailureStormMinObservationMs {
			return FailureStormPrecheckDecision{Trigger: true, SuccessCount: successCount, FailureRatio: failureRatio}
		}
		return FailureStormPrecheckDecision{Trigger: false, SuccessCount: successCount, FailureRatio: failureRatio, SkippedReason: StormSkippedObservationWindow}
	}
	if entry.FailureCount < FailureStormThresholdCount || len(entry.ClientIPs) < FailureStormDistinctIPThreshold {
		return FailureStormPrecheckDecision{Trigger: false, SuccessCount: successCount, FailureRatio: failureRatio, SkippedReason: StormSkippedBelowThreshold}
	}
	if now-entry.FirstSeenMs < FailureStormMinObservationMs {
		return FailureStormPrecheckDecision{Trigger: false, SuccessCount: successCount, FailureRatio: failureRatio, SkippedReason: StormSkippedObservationWindow}
	}
	if successObservation != nil && now-successObservation.LastSeenMs <= FailureStormRecentSuccessGraceMs {
		return FailureStormPrecheckDecision{Trigger: false, SuccessCount: successCount, FailureRatio: failureRatio, SkippedReason: StormSkippedRecentSuccess}
	}
	if failureRatio < FailureStormFailureRatioThreshold {
		return FailureStormPrecheckDecision{Trigger: false, SuccessCount: successCount, FailureRatio: failureRatio, SkippedReason: StormSkippedFailureRatio}
	}
	return FailureStormPrecheckDecision{Trigger: true, SuccessCount: successCount, FailureRatio: failureRatio}
}

// SuppressGatewayAccountLocally mirrors suppressGatewayAccountLocally: the
// local avoidance decision is Redis-managed, so the result records the intent
// without process-local counters.
func (s *SideEffectsService) SuppressGatewayAccountLocally(account gatewayruntimecache.OpenAIAccountSecret, reason string) GatewayAccountLocalSuppressionResult {
	if reason == "" {
		reason = "上游账号请求失败"
	}
	runtimeKey, err := GatewayAccountRuntimeKeyForSecret(account)
	if err != nil {
		runtimeKey = account.ID
	}
	return GatewayAccountLocalSuppressionResult{
		RuntimeKey:        runtimeKey,
		Action:            "redis_managed",
		Reason:            reason,
		LocalFailureCount: 0,
	}
}

// GatewayAccountLocalSuppressionResult mirrors GatewayAccountLocalSuppressionResult.
type GatewayAccountLocalSuppressionResult struct {
	RuntimeKey        string
	Action            string
	Reason            string
	LocalFailureCount int
}

// GatewayAccountSideEffectState mirrors GatewayAccountSideEffectState.
type GatewayAccountSideEffectState struct {
	QueueLength                    int
	Processing                     bool
	EnqueuedCount                  int64
	CompletedCount                 int64
	CoalescedCount                 int64
	CanceledBySuccessCount         int64
	SkippedHealthySuccessCount     int64
	FailedAttemptCount             int64
	DroppedCount                   int64
	ExpiredCount                   int64
	StaleCount                     int64
	EvictedFailureForSuccessCount  int64
	LocalSuppressedAccountCount    int64
	DegradedAccountCount           int64
	PrecheckPendingAccountCount    int64
	RecoveryProbePendingAccountCount int64
	NextAttemptAt                  string
}

// GetState mirrors getGatewayAccountSideEffectState.
func (s *SideEffectsService) GetState(localSuppressedCount, degradedCount int64) GatewayAccountSideEffectState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.config.IsRedisDriver() && s.deps.CleanupLocalSuppressions != nil {
		s.deps.CleanupLocalSuppressions()
	}
	state := GatewayAccountSideEffectState{
		QueueLength:                    s.queue.Len(),
		Processing:                     s.processing,
		EnqueuedCount:                  s.enqueuedCount,
		CompletedCount:                 s.completedCount,
		CoalescedCount:                 s.coalescedCount,
		CanceledBySuccessCount:         s.canceledBySuccessCount,
		SkippedHealthySuccessCount:     s.skippedHealthySuccessCount,
		FailedAttemptCount:             s.failedAttemptCount,
		DroppedCount:                   s.droppedCount,
		ExpiredCount:                   s.expiredCount,
		StaleCount:                     s.staleCount,
		EvictedFailureForSuccessCount:  s.evictedFailureForSuccessCount,
		LocalSuppressedAccountCount:    localSuppressedCount,
		DegradedAccountCount:           degradedCount,
	}
	if s.config.IsRedisDriver() {
		state.PrecheckPendingAccountCount = 0
		state.RecoveryProbePendingAccountCount = 0
	}
	if peek := s.queue.Peek(); peek != nil {
		state.NextAttemptAt = canonicalRFC3339(time.UnixMilli(peek.NextAttemptAtMs))
	}
	return state
}

// Flush mirrors flushGatewayAccountSideEffects: drain until the queue is
// empty (bounded at 100 attempts with 10ms gaps).
func (s *SideEffectsService) Flush(ctx context.Context) {
	s.mu.Lock()
	if s.drainTimer != nil {
		s.drainTimer.Cancel()
		s.drainTimer = nil
		s.drainTimerDueAtMs = nil
	}
	s.mu.Unlock()

	for attempt := 0; attempt < 100; attempt++ {
		s.mu.Lock()
		processing := s.processing
		s.mu.Unlock()
		if !processing {
			s.Drain(ctx)
		}
		s.mu.Lock()
		processing = s.processing
		length := s.queue.Len()
		s.mu.Unlock()
		if !processing && length == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ClearForTest mirrors clearGatewayAccountSideEffectQueueForTest.
func (s *SideEffectsService) ClearForTest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue.Clear()
	s.epochs.Clear()
	if s.drainTimer != nil {
		s.drainTimer.Cancel()
		s.drainTimer = nil
		s.drainTimerDueAtMs = nil
	}
}

func (s *SideEffectsService) invalidateRuntimeCache() {
	if s.deps.InvalidateRuntimeCache != nil {
		s.deps.InvalidateRuntimeCache()
	}
}

func minI64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func mustSecretRuntimeKey(account gatewayruntimecache.OpenAIAccountSecret) string {
	key, err := GatewayAccountRuntimeKeyForSecret(account)
	if err != nil {
		return account.ID
	}
	return key
}
