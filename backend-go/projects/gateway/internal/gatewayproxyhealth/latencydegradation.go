package gatewayproxyhealth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// Ports runtime/normal-route-latency-degradation.service.ts: normal-route
// speed-first latency degradation (slow sampling, bounded degradation TTL,
// two-probe recovery rounds, generation-scoped indexes and claims).

// LatencyDegradationScope mirrors NormalRouteLatencyDegradationScope.
type LatencyDegradationScope struct {
	SystemAccountID string `json:"systemAccountId"`
	RouteStrategyID string `json:"routeStrategyId"`
	GroupID         string `json:"groupId"`
}

// SpeedFirstRuntimeConfig mirrors NormalRouteSpeedFirstRuntimeConfig
// (RouteStrategySpeedFirstConfig + firstByteDeadlineMs). The JSON tags match
// the Node state payload exactly.
type SpeedFirstRuntimeConfig struct {
	FirstByteDeadlineMs           int64 `json:"firstByteDeadlineMs"`
	SlowTriggerCount              int64 `json:"slowTriggerCount"`
	SlowWindowSeconds             int64 `json:"slowWindowSeconds"`
	RecoverySuccessCount          int64 `json:"recoverySuccessCount"`
	ProbeIntervalSeconds          int64 `json:"probeIntervalSeconds"`
	DegradedTTLSeconds            int64 `json:"degradedTtlSeconds"`
	MaxFirstByteRetriesPerRequest int64 `json:"maxFirstByteRetriesPerRequest"`
}

// LatencyDegradationOrderResult mirrors
// NormalRouteLatencyDegradationOrderResult; the generic accounts are the
// caller's original elements.
type LatencyDegradationOrderResult[T any] struct {
	Accounts            []T
	Applied             bool
	DegradedAccountIDs  []string
	BypassedAllDegraded bool
}

// LatencySlowResult mirrors NormalRouteLatencySlowResult.
type LatencySlowResult struct {
	AccountID            string
	SlowCount            int64
	Degraded             bool
	DegradedUntil        *string
	RecoverySuccessCount int64
	NextProbeAt          *string
}

// LatencySuccessResult mirrors NormalRouteLatencySuccessResult.
type LatencySuccessResult struct {
	AccountID                    string
	Cleared                      bool
	RecoverySuccessCount         int64
	RequiredRecoverySuccessCount int64
}

// LatencyProbeCandidate mirrors NormalRouteLatencyProbeCandidate.
type LatencyProbeCandidate struct {
	StateKey                       string
	Generation                     string
	AccountID                      string
	AccountName                    *string
	RuntimeKey                     string
	Scope                          LatencyDegradationScope
	Config                         SpeedFirstRuntimeConfig
	DegradationEventID             *string
	DegradedUntil                  string
	NextProbeAt                    string
	RecoverySuccessCount           int64
	RecoveryProbeRoundAttemptCount int64
	RecoveryProbeRoundSuccessCount int64
}

// LatencyProbeClaim mirrors NormalRouteLatencyProbeClaim.
type LatencyProbeClaim struct {
	StateKey   string
	Generation string
	LockKey    string
	Token      string
}

// DegradedRuntimeItem mirrors NormalRouteLatencyDegradedRuntimeItem: the
// admin-facing runtime item without internal coordination fields.
type DegradedRuntimeItem struct {
	AccountID                      string
	AccountName                    *string
	ScopeRouteStrategyID           string
	ScopeGroupID                   string
	SlowCount                      int64
	SlowTriggerCount               int64
	SlowWindowSeconds              int64
	DegradedUntil                  string
	NextProbeAt                    *string
	RecoverySuccessCount           int64
	RequiredRecoverySuccessCount   int64
	RecoveryProbeRoundAttemptCount int64
	RecoveryProbeRoundSuccessCount int64
	Reason                         string
}

// LatencyGenerationEvent mirrors NormalRouteLatencyGenerationEvent.
type LatencyGenerationEvent struct {
	Version     string `json:"version"`
	PublishedAt string `json:"publishedAt"`
}

// latencyState mirrors NormalRouteLatencyState with Node JSON field names.
type latencyState struct {
	Generation                     string                  `json:"generation"`
	AccountID                      string                  `json:"accountId"`
	AccountName                    *string                 `json:"accountName,omitempty"`
	RuntimeKey                     string                  `json:"runtimeKey"`
	Scope                          LatencyDegradationScope `json:"scope"`
	Config                         SpeedFirstRuntimeConfig `json:"config"`
	FirstSlowAtMs                  int64                   `json:"firstSlowAtMs"`
	LastSlowAtMs                   int64                   `json:"lastSlowAtMs"`
	SlowCount                      int64                   `json:"slowCount"`
	DegradationEventID             *string                 `json:"degradationEventId,omitempty"`
	DegradedUntilMs                *int64                  `json:"degradedUntilMs,omitempty"`
	SuccessCount                   int64                   `json:"successCount"`
	RecoveryProbeRoundAttemptCount *int64                  `json:"recoveryProbeRoundAttemptCount,omitempty"`
	RecoveryProbeRoundSuccessCount *int64                  `json:"recoveryProbeRoundSuccessCount,omitempty"`
	NextProbeAtMs                  *int64                  `json:"nextProbeAtMs,omitempty"`
	Reason                         string                  `json:"reason"`
}

func (s *latencyState) clone() latencyState {
	copy := *s
	return copy
}

type latencyStateLock struct {
	key     string
	lockKey string
	token   string
}

// LatencyDegradationLogFunc receives the Node logger payloads.
type LatencyDegradationLogFunc func(fields map[string]any, message string)

// LatencyDegradationOptions carries the fixed Node constants plus the two
// environment-derived values (concurrency.globalMax drives the exact-clear
// worker pool).
type LatencyDegradationOptions struct {
	BackgroundClearConcurrency int // runtimeConfig.concurrency.globalMax (5000)
	// LockRetryDelay overrides the lock-acquire backoff sleep
	// (min(100ms, 20+5*attempt)); tests inject a no-op.
	LockRetryDelay func(attempt int)
	// Random overrides the passive-schedule jitter random source.
	Random func() float64
}

const (
	latencyStateVersion                = "v1"
	latencyStateGenerationKey          = latencyStateVersion + ":generation"
	latencyStateAllIndexKey            = latencyStateVersion + ":all-index"
	latencyStateProbeIndexKey          = latencyStateVersion + ":probe-index"
	latencyStateAllIndexLockKey        = latencyStateVersion + ":all-index-lock"
	latencyStateProbeIndexLockKey      = latencyStateVersion + ":probe-index-lock"
	latencyStateIndexMaxKeys           = 10_000
	latencyStateIndexTTLMs             = int64(24 * 60 * 60 * 1000)
	latencyStateGenerationTTLMs        = int64(48 * 60 * 60 * 1000)
	latencyStateLockAcquireMaxAttempts = 50
	latencyStateLockAcquireMaxDelayMs  = 100
	latencyStateMutationLockTTLMs      = int64(2*latencyStateLockAcquireMaxAttempts*latencyStateLockAcquireMaxDelayMs) + 5000
	latencyStateIndexLockTTLMs         = latencyStateMutationLockTTLMs
	latencyStateGenerationCASMax       = 8
	latencyStateIndexCASMax            = 8
	normalRouteRecoveryProbeRoundSize  = int64(2)
	normalRouteRecoveryProbeIntervalMs = int64(5_000)
	latencyProbeClaimTTLMs             = int64(2 * 60 * 1000)
	// NormalRouteLatencyProbeClaimRenewIntervalMs mirrors the exported Node
	// constant (30s probe claim renewal cadence).
	NormalRouteLatencyProbeClaimRenewIntervalMs = int64(30_000)
)

var latencyStateInitialGenerationEvent = LatencyGenerationEvent{
	Version:     "initial",
	PublishedAt: "1970-01-01T00:00:00.000Z",
}

// LatencyDegradationService is the normal-route latency degradation service.
// store mirrors createRuntimeStateStore('gateway-normal-route-latency-degradation').
type LatencyDegradationService struct {
	store  RuntimeStateStore
	clock  Clock
	opts   LatencyDegradationOptions
	random func() float64

	clearSlots chan struct{}
}

// NewLatencyDegradationService builds the service.
func NewLatencyDegradationService(store RuntimeStateStore, clock Clock, opts LatencyDegradationOptions) *LatencyDegradationService {
	if opts.BackgroundClearConcurrency <= 0 {
		opts.BackgroundClearConcurrency = 5_000
	}
	random := opts.Random
	return &LatencyDegradationService{
		store:      store,
		clock:      clock,
		opts:       opts,
		random:     random,
		clearSlots: make(chan struct{}, opts.BackgroundClearConcurrency),
	}
}

func (s *LatencyDegradationService) nowMs() int64 { return ClockNowMs(s.clock) }

func (s *LatencyDegradationService) jitterRandom() func() float64 {
	if s.random != nil {
		return s.random
	}
	return nil
}

func (s *LatencyDegradationService) lockRetryDelay(attempt int) {
	if s.opts.LockRetryDelay != nil {
		s.opts.LockRetryDelay(attempt)
		return
	}
	delay := time.Duration(minInt(latencyStateLockAcquireMaxDelayMs, 20+attempt*5)) * time.Millisecond
	time.Sleep(delay)
}

// ---------------------------------------------------------------------------
// Public API.

// NormalRouteLatencyDegradationScope mirrors normalRouteLatencyDegradationScope.
func NormalRouteLatencyDegradationScope(systemAccountID, routeStrategyID, groupID string) *LatencyDegradationScope {
	systemAccountID = strings.TrimSpace(systemAccountID)
	routeStrategyID = strings.TrimSpace(routeStrategyID)
	groupID = strings.TrimSpace(groupID)
	if systemAccountID == "" || routeStrategyID == "" || groupID == "" {
		return nil
	}
	return &LatencyDegradationScope{SystemAccountID: systemAccountID, RouteStrategyID: routeStrategyID, GroupID: groupID}
}

// OrderGatewayAccountsByNormalRouteLatencyDegradation mirrors the generic
// ordering entry point; accountOf projects each element onto
// SuppressibleGatewayAccount, and elements keep their identity on output.
func OrderGatewayAccountsByNormalRouteLatencyDegradation[T any](
	ctx context.Context,
	s *LatencyDegradationService,
	accounts []T,
	accountOf func(T) SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
	config *SpeedFirstRuntimeConfig,
	_ *gatewayrouting.GatewayAccountModelPriority,
) (LatencyDegradationOrderResult[T], error) {
	if scope == nil || config == nil || len(accounts) == 0 {
		return LatencyDegradationOrderResult[T]{Accounts: accounts, DegradedAccountIDs: []string{}}, nil
	}

	generation, err := s.loadLatencyStateGeneration(ctx)
	if err != nil {
		return LatencyDegradationOrderResult[T]{}, err
	}
	now := s.nowMs()
	type accountState struct {
		account T
		state   *latencyState
	}
	states := make([]accountState, 0, len(accounts))
	for _, account := range accounts {
		view := accountOf(account)
		state, err := s.loadLatencyState(ctx, accountLatencyStateKey(*scope, view), generation)
		if err != nil {
			return LatencyDegradationOrderResult[T]{}, err
		}
		states = append(states, accountState{account: account, state: state})
	}
	var normalAccounts, degradedAccounts []T
	degradedAccountIDs := make([]string, 0)
	for _, item := range states {
		if item.state != nil && item.state.DegradedUntilMs != nil && *item.state.DegradedUntilMs > now {
			degradedAccounts = append(degradedAccounts, item.account)
			degradedAccountIDs = append(degradedAccountIDs, accountOf(item.account).ID)
		} else {
			normalAccounts = append(normalAccounts, item.account)
		}
	}

	if len(degradedAccounts) == 0 {
		return LatencyDegradationOrderResult[T]{Accounts: accounts, Applied: false, DegradedAccountIDs: []string{}, BypassedAllDegraded: false}, nil
	}

	if len(normalAccounts) == 0 {
		return LatencyDegradationOrderResult[T]{Accounts: accounts, Applied: false, DegradedAccountIDs: degradedAccountIDs, BypassedAllDegraded: true}, nil
	}

	ordered := append(append([]T(nil), normalAccounts...), degradedAccounts...)
	return LatencyDegradationOrderResult[T]{
		Accounts:            ordered,
		Applied:             true,
		DegradedAccountIDs:  degradedAccountIDs,
		BypassedAllDegraded: false,
	}, nil
}

// RecordNormalRouteFirstByteSlow mirrors recordNormalRouteFirstByteSlowAsync.
// An empty reason falls back to the Node default.
func (s *LatencyDegradationService) RecordNormalRouteFirstByteSlow(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
	config *SpeedFirstRuntimeConfig,
	reason string,
) (*LatencySlowResult, error) {
	if scope == nil || config == nil {
		return nil, nil
	}
	if reason == "" {
		reason = "普通路由速度优先首字等待超时"
	}
	key, err := accountLatencyStateKeyChecked(*scope, account)
	if err != nil {
		return nil, err
	}
	generation, err := s.loadLatencyStateGeneration(ctx)
	if err != nil {
		return nil, err
	}
	var result *LatencySlowResult
	ok, err := s.withLatencyStateMutationLock(ctx, key, generation, func() (bool, error) {
		value, err := s.recordNormalRouteFirstByteSlowLocked(ctx, account, *scope, *config, reason, key, generation)
		if err != nil {
			return false, err
		}
		result = value
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return result, nil
}

func (s *LatencyDegradationService) recordNormalRouteFirstByteSlowLocked(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope LatencyDegradationScope,
	config SpeedFirstRuntimeConfig,
	reason string,
	key string,
	generation string,
) (*LatencySlowResult, error) {
	now := s.nowMs()
	current, err := s.loadLatencyState(ctx, key, generation)
	if err != nil {
		return nil, err
	}
	slowWindowMs := maxInt64(60, config.SlowWindowSeconds) * 1000
	withinWindow := current != nil && now-current.FirstSlowAtMs <= slowWindowMs
	slowCount := int64(1)
	if withinWindow {
		slowCount = current.SlowCount + 1
	}
	currentStillDegraded := current != nil && current.DegradedUntilMs != nil && *current.DegradedUntilMs > now
	triggeredDegraded := slowCount >= config.SlowTriggerCount
	degraded := triggeredDegraded || currentStillDegraded
	// A degradation event has one bounded TTL. Repeated slow samples while it
	// is already degraded must not keep moving the recovery deadline forward.
	var degradedUntilMs *int64
	if triggeredDegraded {
		if currentStillDegraded {
			degradedUntilMs = current.DegradedUntilMs
		} else {
			degradedUntilMs = int64Ptr(now + maxInt64(60, config.DegradedTTLSeconds)*1000)
		}
	} else if current != nil {
		degradedUntilMs = current.DegradedUntilMs
	}
	var degradationEventID *string
	if degraded {
		if currentStillDegraded && current.DegradationEventID != nil {
			degradationEventID = current.DegradationEventID
		} else {
			degradationEventID = stringPtr(NewUUID())
		}
	}
	var nextProbeAtMs *int64
	if triggeredDegraded {
		nextProbeAtMs = int64Ptr(now + s.nextRecoveryProbeDelayMs())
	} else if current != nil {
		nextProbeAtMs = current.NextProbeAtMs
	}
	runtimeKey, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		return nil, err
	}
	state := latencyState{
		Generation:                     generation,
		AccountID:                      account.ID,
		AccountName:                    optionalAccountName(account),
		RuntimeKey:                     runtimeKey,
		Scope:                          scope,
		Config:                         config,
		FirstSlowAtMs:                  now,
		LastSlowAtMs:                   now,
		SlowCount:                      slowCount,
		DegradationEventID:             degradationEventID,
		DegradedUntilMs:                degradedUntilMs,
		SuccessCount:                   0,
		RecoveryProbeRoundAttemptCount: int64Ptr(0),
		RecoveryProbeRoundSuccessCount: int64Ptr(0),
		NextProbeAtMs:                  nextProbeAtMs,
		Reason:                         reason,
	}
	if withinWindow {
		state.FirstSlowAtMs = current.FirstSlowAtMs
	}
	var ttlMs int64
	if degraded {
		if currentStillDegraded {
			ttlMs = latencyStateRemainingTTLMs(degradedUntilMs, now)
		} else {
			ttlMs = latencyStateTTLMs(config, true)
		}
	} else {
		ttlMs = latencyStateTTLMs(config, false)
	}
	var previous *latencyState
	if current != nil {
		previousCopy := *current
		previous = &previousCopy
	}
	if err := s.writeLatencyStateAndIndexesStrict(ctx, key, previous, state, ttlMs, degraded); err != nil {
		return nil, err
	}
	return &LatencySlowResult{
		AccountID:            account.ID,
		SlowCount:            slowCount,
		Degraded:             degraded,
		DegradedUntil:        isoPtr(degradedUntilMs),
		RecoverySuccessCount: 0,
		NextProbeAt:          isoPtr(nextProbeAtMs),
	}, nil
}

// RecordNormalRouteFirstByteSuccess mirrors recordNormalRouteFirstByteSuccessAsync.
func (s *LatencyDegradationService) RecordNormalRouteFirstByteSuccess(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
	config *SpeedFirstRuntimeConfig,
	firstByteMs *int64,
) (*LatencySuccessResult, error) {
	if scope == nil || config == nil || firstByteMs == nil || *firstByteMs > config.FirstByteDeadlineMs {
		return nil, nil
	}
	key, err := accountLatencyStateKeyChecked(*scope, account)
	if err != nil {
		return nil, err
	}
	generation, err := s.loadLatencyStateGeneration(ctx)
	if err != nil {
		return nil, err
	}
	var result *LatencySuccessResult
	ok, err := s.withLatencyStateMutationLock(ctx, key, generation, func() (bool, error) {
		value, err := s.recordNormalRouteFirstByteSuccessLocked(ctx, account, *config, key, generation, false, nil)
		if err != nil {
			return false, err
		}
		result = value
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return result, nil
}

// RecordNormalRouteRecoveryProbeSuccess mirrors
// recordNormalRouteRecoveryProbeSuccessAsync: two short-spaced background
// health checks restore priority without waiting for user traffic.
func (s *LatencyDegradationService) RecordNormalRouteRecoveryProbeSuccess(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	candidate LatencyProbeCandidate,
	firstByteMs *int64,
) (*LatencySuccessResult, error) {
	if firstByteMs == nil || *firstByteMs > candidate.Config.FirstByteDeadlineMs {
		return nil, nil
	}
	runtimeKey, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		return nil, err
	}
	if account.ID != candidate.AccountID || runtimeKey != candidate.RuntimeKey {
		return nil, nil
	}
	var result *LatencySuccessResult
	ok, err := s.withLatencyStateMutationLock(ctx, candidate.StateKey, candidate.Generation, func() (bool, error) {
		value, err := s.recordNormalRouteFirstByteSuccessLocked(ctx, account, candidate.Config, candidate.StateKey, candidate.Generation, true, &candidate)
		if err != nil {
			return false, err
		}
		result = value
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return result, nil
}

func (s *LatencyDegradationService) recordNormalRouteFirstByteSuccessLocked(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	config SpeedFirstRuntimeConfig,
	key string,
	generation string,
	clearOnSuccess bool,
	candidate *LatencyProbeCandidate,
) (*LatencySuccessResult, error) {
	current, err := s.loadLatencyState(ctx, key, generation)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, nil
	}
	if candidate != nil && !latencyProbeCandidateMatchesState(*candidate, *current) {
		return nil, nil
	}
	now := s.nowMs()
	if current.DegradedUntilMs == nil || *current.DegradedUntilMs <= now {
		if err := s.deleteLatencyStateAndIndexesStrict(ctx, key); err != nil {
			return nil, err
		}
		return &LatencySuccessResult{
			AccountID:                    account.ID,
			Cleared:                      true,
			RecoverySuccessCount:         0,
			RequiredRecoverySuccessCount: config.RecoverySuccessCount,
		}, nil
	}
	successCount := current.SuccessCount
	if !clearOnSuccess {
		successCount = current.SuccessCount + 1
	}
	if clearOnSuccess {
		recoveryProbeRoundAttemptCount := recoveryProbeRoundAttempts(*current) + 1
		recoveryProbeRoundSuccessCount := recoveryProbeRoundSuccesses(*current) + 1
		if recoveryProbeRoundAttemptCount >= normalRouteRecoveryProbeRoundSize &&
			recoveryProbeRoundSuccessCount == normalRouteRecoveryProbeRoundSize {
			if err := s.deleteLatencyStateAndIndexesStrict(ctx, key); err != nil {
				return nil, err
			}
			return &LatencySuccessResult{
				AccountID:                    account.ID,
				Cleared:                      true,
				RecoverySuccessCount:         recoveryProbeRoundSuccessCount,
				RequiredRecoverySuccessCount: normalRouteRecoveryProbeRoundSize,
			}, nil
		}
		next := current.clone()
		if recoveryProbeRoundAttemptCount >= normalRouteRecoveryProbeRoundSize {
			next.RecoveryProbeRoundAttemptCount = int64Ptr(0)
			next.RecoveryProbeRoundSuccessCount = int64Ptr(0)
		} else {
			next.RecoveryProbeRoundAttemptCount = int64Ptr(recoveryProbeRoundAttemptCount)
			next.RecoveryProbeRoundSuccessCount = int64Ptr(recoveryProbeRoundSuccessCount)
		}
		// A user request and a background health probe are separate recovery
		// mechanisms. Only the latter contributes to this two-probe window.
		next.SuccessCount = current.SuccessCount
		next.NextProbeAtMs = int64Ptr(now + s.nextRecoveryProbeDelayMs())
		if err := s.store.SetJSON(ctx, key, next, latencyStateRemainingTTLMs(current.DegradedUntilMs, now)); err != nil {
			return nil, err
		}
		return &LatencySuccessResult{
			AccountID:                    account.ID,
			Cleared:                      false,
			RecoverySuccessCount:         recoveryProbeRoundSuccessCount,
			RequiredRecoverySuccessCount: normalRouteRecoveryProbeRoundSize,
		}, nil
	}
	requiredRecoverySuccessCount := config.RecoverySuccessCount
	recovered := successCount >= config.RecoverySuccessCount
	if recovered {
		if err := s.deleteLatencyStateAndIndexesStrict(ctx, key); err != nil {
			return nil, err
		}
		return &LatencySuccessResult{
			AccountID:                    account.ID,
			Cleared:                      true,
			RecoverySuccessCount:         successCount,
			RequiredRecoverySuccessCount: requiredRecoverySuccessCount,
		}, nil
	}
	var nextProbeAtMs *int64
	if recoveryProbeRoundAttempts(*current) > 0 {
		nextProbeAtMs = current.NextProbeAtMs
	} else {
		nextProbeAtMs = int64Ptr(now + s.nextProbeDelayMs(config))
	}
	next := current.clone()
	next.SuccessCount = successCount
	next.NextProbeAtMs = nextProbeAtMs
	if err := s.store.SetJSON(ctx, key, next, latencyStateRemainingTTLMs(current.DegradedUntilMs, now)); err != nil {
		return nil, err
	}
	return &LatencySuccessResult{
		AccountID:                    account.ID,
		Cleared:                      false,
		RecoverySuccessCount:         successCount,
		RequiredRecoverySuccessCount: requiredRecoverySuccessCount,
	}, nil
}

// IsNormalRouteAccountLatencyDegraded mirrors isNormalRouteAccountLatencyDegradedAsync.
func (s *LatencyDegradationService) IsNormalRouteAccountLatencyDegraded(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
) (bool, error) {
	if scope == nil {
		return false, nil
	}
	generation, err := s.loadLatencyStateGeneration(ctx)
	if err != nil {
		return false, err
	}
	state, err := s.loadLatencyState(ctx, accountLatencyStateKey(*scope, account), generation)
	if err != nil {
		return false, err
	}
	return state != nil && state.DegradedUntilMs != nil && *state.DegradedUntilMs > s.nowMs(), nil
}

// ListNormalRouteLatencyProbeCandidates mirrors listNormalRouteLatencyProbeCandidatesAsync.
func (s *LatencyDegradationService) ListNormalRouteLatencyProbeCandidates(
	ctx context.Context,
	limit *int,
	now *int64,
) ([]LatencyProbeCandidate, error) {
	normalizedLimit := 20
	if limit != nil {
		normalizedLimit = normalizePositiveInteger(limit, 20, 1, 100)
	}
	nowMs := s.nowMs()
	if now != nil {
		nowMs = *now
	}
	keys, err := s.loadLatencyStateIndexKeys(ctx, latencyStateProbeIndexKey)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return []LatencyProbeCandidate{}, nil
	}

	generation, err := s.loadLatencyStateGeneration(ctx)
	if err != nil {
		return nil, err
	}
	type candidateWithOrder struct {
		candidate     LatencyProbeCandidate
		nextProbeAtMs int64
	}
	var candidates []candidateWithOrder
	for _, key := range keys {
		state, err := s.loadLatencyState(ctx, key, generation)
		if err != nil {
			return nil, err
		}
		if state == nil || state.DegradedUntilMs == nil || *state.DegradedUntilMs <= nowMs {
			continue
		}
		if state.NextProbeAtMs == nil || *state.NextProbeAtMs > nowMs {
			continue
		}
		candidate, ok := probeCandidateFromState(key, *state)
		if ok {
			candidates = append(candidates, candidateWithOrder{candidate: candidate, nextProbeAtMs: *state.NextProbeAtMs})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.nextProbeAtMs != right.nextProbeAtMs {
			return left.nextProbeAtMs < right.nextProbeAtMs
		}
		return strings.Compare(left.candidate.AccountID, right.candidate.AccountID) < 0
	})
	if len(candidates) > normalizedLimit {
		candidates = candidates[:normalizedLimit]
	}
	output := make([]LatencyProbeCandidate, 0, len(candidates))
	for _, item := range candidates {
		output = append(output, item.candidate)
	}
	return output, nil
}

// ListNormalRouteLatencyDegradedRuntimeInput mirrors the runtime query input.
type ListNormalRouteLatencyDegradedRuntimeInput struct {
	SystemAccountID  *string
	RouteStrategyIDs []string
	Now              *int64
}

// ListNormalRouteLatencyDegradedRuntime mirrors
// listNormalRouteLatencyDegradedRuntimeAsync: reads strictly from the probe
// index so observation-period or expired states are never surfaced.
func (s *LatencyDegradationService) ListNormalRouteLatencyDegradedRuntime(
	ctx context.Context,
	input ListNormalRouteLatencyDegradedRuntimeInput,
) ([]DegradedRuntimeItem, error) {
	var systemAccountID string
	if input.SystemAccountID != nil {
		systemAccountID = strings.TrimSpace(*input.SystemAccountID)
	}
	routeStrategyIDs := normalizedRouteStrategyIDs(input.RouteStrategyIDs)
	if len(routeStrategyIDs) == 0 {
		return []DegradedRuntimeItem{}, nil
	}
	if len(routeStrategyIDs) > 50 {
		return nil, errors.New("普通路由速度优先运行态查询最多支持 50 个策略路由")
	}

	keys, err := s.loadLatencyStateIndexKeys(ctx, latencyStateProbeIndexKey)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return []DegradedRuntimeItem{}, nil
	}

	generation, err := s.loadLatencyStateGeneration(ctx)
	if err != nil {
		return nil, err
	}
	nowMs := s.nowMs()
	if input.Now != nil {
		nowMs = *input.Now
	}
	routeStrategyIDSet := make(map[string]struct{}, len(routeStrategyIDs))
	for _, id := range routeStrategyIDs {
		routeStrategyIDSet[id] = struct{}{}
	}
	rawStates, err := s.store.GetJSONMany(ctx, keys)
	if err != nil {
		return nil, err
	}
	items := make([]DegradedRuntimeItem, 0)
	for _, raw := range rawStates {
		if raw == nil {
			continue
		}
		state, ok := decodeLatencyState(raw)
		if !ok {
			continue
		}
		if !isCurrentLatencyState(state, generation) {
			continue
		}
		if state.DegradedUntilMs == nil || *state.DegradedUntilMs <= nowMs {
			continue
		}
		if systemAccountID != "" && state.Scope.SystemAccountID != systemAccountID {
			continue
		}
		if _, matched := routeStrategyIDSet[state.Scope.RouteStrategyID]; !matched {
			continue
		}
		item := DegradedRuntimeItem{
			AccountID:                      state.AccountID,
			AccountName:                    state.AccountName,
			ScopeRouteStrategyID:           state.Scope.RouteStrategyID,
			ScopeGroupID:                   state.Scope.GroupID,
			SlowCount:                      state.SlowCount,
			SlowTriggerCount:               state.Config.SlowTriggerCount,
			SlowWindowSeconds:              state.Config.SlowWindowSeconds,
			DegradedUntil:                  ISOStringMs(*state.DegradedUntilMs),
			NextProbeAt:                    isoPtr(state.NextProbeAtMs),
			RecoverySuccessCount:           state.SuccessCount,
			RequiredRecoverySuccessCount:   state.Config.RecoverySuccessCount,
			RecoveryProbeRoundAttemptCount: recoveryProbeRoundAttempts(state),
			RecoveryProbeRoundSuccessCount: recoveryProbeRoundSuccesses(state),
			Reason:                         state.Reason,
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if c := strings.Compare(left.DegradedUntil, right.DegradedUntil); c != 0 {
			return c < 0
		}
		if c := strings.Compare(left.AccountID, right.AccountID); c != 0 {
			return c < 0
		}
		if c := strings.Compare(left.ScopeRouteStrategyID, right.ScopeRouteStrategyID); c != 0 {
			return c < 0
		}
		return strings.Compare(left.ScopeGroupID, right.ScopeGroupID) < 0
	})
	return items, nil
}

// ---------------------------------------------------------------------------
// Probe claims.

// AcquireNormalRouteLatencyProbeClaim mirrors acquireNormalRouteLatencyProbeClaimAsync.
func (s *LatencyDegradationService) AcquireNormalRouteLatencyProbeClaim(
	ctx context.Context,
	candidate LatencyProbeCandidate,
) (*LatencyProbeClaim, error) {
	token := NewUUID()
	lockKey := normalRouteLatencyProbeClaimLockKey(candidate)
	acquired, err := s.store.AcquireLock(ctx, lockKey, latencyProbeClaimTTLMs, token)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, nil
	}
	return &LatencyProbeClaim{
		StateKey:   candidate.StateKey,
		Generation: candidate.Generation,
		LockKey:    lockKey,
		Token:      token,
	}, nil
}

// RenewNormalRouteLatencyProbeClaim mirrors renewNormalRouteLatencyProbeClaimAsync.
func (s *LatencyDegradationService) RenewNormalRouteLatencyProbeClaim(ctx context.Context, claim LatencyProbeClaim) (bool, error) {
	return s.store.RenewLock(ctx, claim.LockKey, latencyProbeClaimTTLMs, claim.Token)
}

// ReleaseNormalRouteLatencyProbeClaim mirrors releaseNormalRouteLatencyProbeClaimAsync.
func (s *LatencyDegradationService) ReleaseNormalRouteLatencyProbeClaim(ctx context.Context, claim LatencyProbeClaim) error {
	return s.store.ReleaseLock(ctx, claim.LockKey, claim.Token)
}

// RecordNormalRouteProbeFailure mirrors recordNormalRouteProbeFailureAsync.
func (s *LatencyDegradationService) RecordNormalRouteProbeFailure(
	ctx context.Context,
	candidate LatencyProbeCandidate,
	reason string,
) (*LatencySlowResult, error) {
	if reason == "" {
		reason = "普通路由速度优先恢复探针未达标"
	}
	var result *LatencySlowResult
	ok, err := s.withLatencyStateMutationLock(ctx, candidate.StateKey, candidate.Generation, func() (bool, error) {
		value, err := s.recordNormalRouteProbeFailureLocked(ctx, candidate, reason)
		if err != nil {
			return false, err
		}
		result = value
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return result, nil
}

func (s *LatencyDegradationService) recordNormalRouteProbeFailureLocked(
	ctx context.Context,
	candidate LatencyProbeCandidate,
	reason string,
) (*LatencySlowResult, error) {
	current, err := s.loadLatencyState(ctx, candidate.StateKey, candidate.Generation)
	if err != nil {
		return nil, err
	}
	now := s.nowMs()
	if current == nil {
		return nil, nil
	}
	if !latencyProbeCandidateMatchesState(candidate, *current) {
		return nil, nil
	}
	if current.DegradedUntilMs == nil || *current.DegradedUntilMs <= now {
		if err := s.deleteLatencyStateAndIndexesStrict(ctx, candidate.StateKey); err != nil {
			return nil, err
		}
		return nil, nil
	}
	config := current.Config
	recoveryProbeRoundAttemptCount := recoveryProbeRoundAttempts(*current) + 1
	recoveryProbeRoundSuccessCount := recoveryProbeRoundSuccesses(*current)
	recoveryProbeRoundComplete := recoveryProbeRoundAttemptCount >= normalRouteRecoveryProbeRoundSize
	// Only FF renews the lease. A mixed pair is deliberately discarded, so it
	// cannot be combined with a later result to manufacture a double failure.
	degradedUntilMs := current.DegradedUntilMs
	if recoveryProbeRoundComplete && recoveryProbeRoundSuccessCount == 0 {
		degradedUntilMs = int64Ptr(maxInt64(*current.DegradedUntilMs, now+maxInt64(60, config.DegradedTTLSeconds)*1000))
	}
	nextProbeAtMs := int64Ptr(now + s.nextRecoveryProbeDelayMs())
	slowCount := maxInt64(current.SlowCount, config.SlowTriggerCount)
	state := current.clone()
	state.Config = config
	state.LastSlowAtMs = now
	state.SlowCount = slowCount
	state.DegradedUntilMs = degradedUntilMs
	// Background probe outcomes must not alter the three-request user-traffic
	// debounce. They have an independent, exactly-two-attempt round.
	state.SuccessCount = current.SuccessCount
	if recoveryProbeRoundComplete {
		state.RecoveryProbeRoundAttemptCount = int64Ptr(0)
		state.RecoveryProbeRoundSuccessCount = int64Ptr(0)
	} else {
		state.RecoveryProbeRoundAttemptCount = int64Ptr(recoveryProbeRoundAttemptCount)
		state.RecoveryProbeRoundSuccessCount = int64Ptr(recoveryProbeRoundSuccessCount)
	}
	state.NextProbeAtMs = nextProbeAtMs
	state.Reason = reason
	if err := s.writeLatencyStateAndIndexesStrict(ctx, candidate.StateKey, current, state, latencyStateRemainingTTLMs(degradedUntilMs, now), true); err != nil {
		return nil, err
	}
	return &LatencySlowResult{
		AccountID:            current.AccountID,
		SlowCount:            slowCount,
		Degraded:             true,
		DegradedUntil:        stringPtr(ISOStringMs(*degradedUntilMs)),
		RecoverySuccessCount: current.SuccessCount,
		NextProbeAt:          stringPtr(ISOStringMs(*nextProbeAtMs)),
	}, nil
}

// DeferNormalRouteLatencyProbeCandidate mirrors deferNormalRouteLatencyProbeCandidateAsync.
func (s *LatencyDegradationService) DeferNormalRouteLatencyProbeCandidate(
	ctx context.Context,
	candidate LatencyProbeCandidate,
) (bool, error) {
	deferred := false
	ok, err := s.withLatencyStateMutationLock(ctx, candidate.StateKey, candidate.Generation, func() (bool, error) {
		current, err := s.loadLatencyState(ctx, candidate.StateKey, candidate.Generation)
		if err != nil {
			return false, err
		}
		now := s.nowMs()
		if current == nil {
			return true, nil
		}
		if !latencyProbeCandidateMatchesState(candidate, *current) {
			return true, nil
		}
		if current.DegradedUntilMs == nil || *current.DegradedUntilMs <= now {
			if err := s.deleteLatencyStateAndIndexesStrict(ctx, candidate.StateKey); err != nil {
				return false, err
			}
			return true, nil
		}
		config := current.Config
		state := current.clone()
		state.Config = config
		state.RecoveryProbeRoundAttemptCount = int64Ptr(0)
		state.RecoveryProbeRoundSuccessCount = int64Ptr(0)
		state.NextProbeAtMs = int64Ptr(now + s.nextRecoveryProbeDelayMs())
		if err := s.writeLatencyStateAndIndexesStrict(ctx, candidate.StateKey, current, state, latencyStateRemainingTTLMs(state.DegradedUntilMs, now), true); err != nil {
			return false, err
		}
		deferred = true
		return true, nil
	})
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return deferred, nil
}

// DiscardNormalRouteLatencyProbeCandidate mirrors discardNormalRouteLatencyProbeCandidateAsync.
func (s *LatencyDegradationService) DiscardNormalRouteLatencyProbeCandidate(
	ctx context.Context,
	candidate LatencyProbeCandidate,
) error {
	_, err := s.withLatencyStateMutationLock(ctx, candidate.StateKey, candidate.Generation, func() (bool, error) {
		current, err := s.loadLatencyState(ctx, candidate.StateKey, candidate.Generation)
		if err != nil {
			return false, err
		}
		if current == nil {
			return true, nil
		}
		if !latencyProbeCandidateMatchesState(candidate, *current) {
			return true, nil
		}
		if err := s.deleteLatencyStateAndIndexesStrict(ctx, candidate.StateKey); err != nil {
			return false, err
		}
		return true, nil
	})
	return err
}

// ---------------------------------------------------------------------------
// Clears.

// ClearNormalRouteLatencyDegradationForRouteStrategy mirrors
// clearNormalRouteLatencyDegradationForRouteStrategyAsync.
func (s *LatencyDegradationService) ClearNormalRouteLatencyDegradationForRouteStrategy(
	ctx context.Context,
	routeStrategyID string,
) (int64, error) {
	normalizedRouteStrategyID := strings.TrimSpace(routeStrategyID)
	if normalizedRouteStrategyID == "" {
		return 0, nil
	}
	keys, err := s.loadLatencyStateIndexKeys(ctx, latencyStateAllIndexKey)
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	generation, err := s.loadLatencyStateGeneration(ctx)
	if err != nil {
		return 0, err
	}
	return s.clearCurrentGenerationLatencyStateKeys(ctx, keys, generation, func(state latencyState) bool {
		return state.Scope.RouteStrategyID == normalizedRouteStrategyID
	})
}

// ClearAllNormalRouteLatencyDegradation mirrors clearAllNormalRouteLatencyDegradationAsync.
// The bool result is false when the generation CAS attempts are exhausted
// (Node returns false without throwing).
func (s *LatencyDegradationService) ClearAllNormalRouteLatencyDegradation(
	ctx context.Context,
	event LatencyGenerationEvent,
) (bool, error) {
	normalizedEvent, err := normalizeLatencyGenerationEvent(event)
	if err != nil {
		return false, err
	}
	for attempt := 0; attempt < latencyStateGenerationCASMax; attempt++ {
		current, currentRaw, err := s.loadLatencyGenerationEventRaw(ctx)
		if err != nil {
			return false, err
		}
		if current != nil && compareLatencyGenerationEvents(normalizedEvent, *current) <= 0 {
			applied, err := s.store.CompareSetJSON(ctx, latencyStateGenerationKey, currentRaw, current, latencyStateGenerationTTLMs)
			if err != nil {
				return false, err
			}
			if applied {
				return true, nil
			}
			continue
		}
		applied, err := s.store.CompareSetJSON(ctx, latencyStateGenerationKey, currentRaw, normalizedEvent, latencyStateGenerationTTLMs)
		if err != nil {
			return false, err
		}
		if applied {
			return true, nil
		}
	}
	return false, nil
}

// ClearNormalRouteLatencyDegradationForAccountBindingInput mirrors the
// account-binding clear input.
type ClearNormalRouteLatencyDegradationForAccountBindingInput struct {
	SystemAccountID string
	AccountID       string
	GroupIDs        []*string
}

// ClearNormalRouteLatencyDegradationForAccountBinding mirrors
// clearNormalRouteLatencyDegradationForAccountBindingAsync.
func (s *LatencyDegradationService) ClearNormalRouteLatencyDegradationForAccountBinding(
	ctx context.Context,
	input ClearNormalRouteLatencyDegradationForAccountBindingInput,
) (int64, error) {
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	accountID := strings.TrimSpace(input.AccountID)
	groupIDs := make(map[string]struct{})
	for _, groupID := range input.GroupIDs {
		if groupID == nil {
			continue
		}
		trimmed := strings.TrimSpace(*groupID)
		if trimmed == "" {
			continue
		}
		groupIDs[trimmed] = struct{}{}
	}
	if systemAccountID == "" || accountID == "" || len(groupIDs) == 0 {
		return 0, nil
	}
	keys, err := s.loadLatencyStateIndexKeys(ctx, latencyStateAllIndexKey)
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	generation, err := s.loadLatencyStateGeneration(ctx)
	if err != nil {
		return 0, err
	}
	return s.clearCurrentGenerationLatencyStateKeys(ctx, keys, generation, func(state latencyState) bool {
		if _, matched := groupIDs[state.Scope.GroupID]; !matched {
			return false
		}
		return state.Scope.SystemAccountID == systemAccountID &&
			(state.AccountID == accountID || RuntimeAccountIDFromKey(state.RuntimeKey) == accountID)
	})
}

// ClearNormalRouteLatencyDegradationForAccountInput mirrors the account clear
// input.
type ClearNormalRouteLatencyDegradationForAccountInput struct {
	SystemAccountID string
	AccountID       string
}

// ClearNormalRouteLatencyDegradationForAccount mirrors
// clearNormalRouteLatencyDegradationForAccountAsync: account-level manual
// cleanup must not depend on the currently listed binding group, otherwise
// other groups keep scheduling against latency_degraded.
func (s *LatencyDegradationService) ClearNormalRouteLatencyDegradationForAccount(
	ctx context.Context,
	input ClearNormalRouteLatencyDegradationForAccountInput,
) (int64, error) {
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	accountID := strings.TrimSpace(input.AccountID)
	if systemAccountID == "" || accountID == "" {
		return 0, nil
	}
	keys, err := s.loadLatencyStateIndexKeys(ctx, latencyStateAllIndexKey)
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	generation, err := s.loadLatencyStateGeneration(ctx)
	if err != nil {
		return 0, err
	}
	return s.clearCurrentGenerationLatencyStateKeys(ctx, keys, generation, func(state latencyState) bool {
		return state.Scope.SystemAccountID == systemAccountID &&
			(state.AccountID == accountID || RuntimeAccountIDFromKey(state.RuntimeKey) == accountID)
	})
}

// ---------------------------------------------------------------------------
// Generation handling.

func (s *LatencyDegradationService) loadLatencyStateGeneration(ctx context.Context) (string, error) {
	event, err := s.loadOrCreateLatencyGenerationEvent(ctx)
	if err != nil {
		return "", err
	}
	return latencyGenerationToken(&event), nil
}

// loadLatencyGenerationEventRaw returns the normalized event together with
// the raw bytes it was read from, so CAS callers resend the exact stored
// payload as the expected value (single read, no re-read race).
func (s *LatencyDegradationService) loadLatencyGenerationEventRaw(ctx context.Context) (*LatencyGenerationEvent, json.RawMessage, error) {
	for attempt := 0; attempt < latencyStateGenerationCASMax; attempt++ {
		raw, err := s.store.GetJSON(ctx, latencyStateGenerationKey)
		if err != nil {
			return nil, nil, err
		}
		if raw == nil {
			return nil, nil, nil
		}
		var event LatencyGenerationEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, nil, errors.New("普通路由速度优先 runtime-state generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		if !isNormalRouteLatencyGenerationEvent(event) {
			return nil, nil, errors.New("普通路由速度优先 runtime-state generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		normalizedEvent, err := normalizeLatencyGenerationEvent(event)
		if err != nil {
			return nil, nil, err
		}
		if rawJSONEqual(raw, mustMarshalJSON(normalizedEvent)) {
			return &normalizedEvent, raw, nil
		}
		applied, err := s.store.CompareSetJSON(ctx, latencyStateGenerationKey, raw, normalizedEvent, latencyStateGenerationTTLMs)
		if err != nil {
			return nil, nil, err
		}
		if applied {
			return &normalizedEvent, mustMarshalJSON(normalizedEvent), nil
		}
	}
	return nil, nil, fmt.Errorf("普通路由速度优先 generation marker canonical CAS 重试耗尽（%d 次）", latencyStateGenerationCASMax)
}

func (s *LatencyDegradationService) loadLatencyGenerationEvent(ctx context.Context) (*LatencyGenerationEvent, error) {
	event, _, err := s.loadLatencyGenerationEventRaw(ctx)
	return event, err
}

func (s *LatencyDegradationService) loadOrCreateLatencyGenerationEvent(ctx context.Context) (LatencyGenerationEvent, error) {
	for attempt := 0; attempt < latencyStateGenerationCASMax; attempt++ {
		current, err := s.loadLatencyGenerationEvent(ctx)
		if err != nil {
			return LatencyGenerationEvent{}, err
		}
		if current != nil {
			return *current, nil
		}
		applied, err := s.store.CompareSetJSON(ctx, latencyStateGenerationKey, nil, latencyStateInitialGenerationEvent, latencyStateGenerationTTLMs)
		if err != nil {
			return LatencyGenerationEvent{}, err
		}
		if applied {
			return latencyStateInitialGenerationEvent, nil
		}
	}
	return LatencyGenerationEvent{}, fmt.Errorf("普通路由速度优先 generation marker CAS 初始化重试耗尽（%d 次）", latencyStateGenerationCASMax)
}

func (s *LatencyDegradationService) renewLatencyStateGeneration(ctx context.Context, generation string) (bool, error) {
	current, currentRaw, err := s.loadLatencyGenerationEventRaw(ctx)
	if err != nil {
		return false, err
	}
	if current == nil || latencyGenerationToken(current) != generation {
		return false, nil
	}
	return s.store.CompareSetJSON(ctx, latencyStateGenerationKey, currentRaw, current, latencyStateGenerationTTLMs)
}

func normalizeLatencyGenerationEvent(event LatencyGenerationEvent) (LatencyGenerationEvent, error) {
	version := strings.TrimSpace(event.Version)
	if version == "" {
		return LatencyGenerationEvent{}, errors.New("普通路由速度优先 generation event 缺少 version")
	}
	publishedAt, ok := CanonicalizeRfc3339Instant(event.PublishedAt)
	if !ok {
		return LatencyGenerationEvent{}, errors.New("普通路由速度优先 generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return LatencyGenerationEvent{Version: version, PublishedAt: publishedAt}, nil
}

func compareLatencyGenerationEvents(left, right LatencyGenerationEvent) int {
	leftPublishedAtMs, leftOK := Rfc3339InstantMilliseconds(left.PublishedAt)
	rightPublishedAtMs, rightOK := Rfc3339InstantMilliseconds(right.PublishedAt)
	if !leftOK || !rightOK {
		panic("普通路由速度优先 generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	if difference := leftPublishedAtMs - rightPublishedAtMs; difference != 0 {
		if difference < 0 {
			return -1
		}
		return 1
	}
	if left.Version == right.Version {
		return 0
	}
	if left.Version > right.Version {
		return 1
	}
	return -1
}

func latencyGenerationToken(event *LatencyGenerationEvent) string {
	if event == nil {
		return "initial"
	}
	publishedAtMs, ok := Rfc3339InstantMilliseconds(event.PublishedAt)
	if !ok {
		panic("普通路由速度优先 generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return string(mustMarshalJSON([]any{publishedAtMs, event.Version}))
}

func isNormalRouteLatencyGenerationEvent(value LatencyGenerationEvent) bool {
	if strings.TrimSpace(value.Version) == "" {
		return false
	}
	_, ok := Rfc3339InstantMilliseconds(value.PublishedAt)
	return ok
}

// ---------------------------------------------------------------------------
// State load/validate.

func (s *LatencyDegradationService) loadLatencyState(ctx context.Context, key string, generation string) (*latencyState, error) {
	raw, err := s.store.GetJSON(ctx, key)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	state, ok := decodeLatencyState(raw)
	if !ok {
		return nil, nil
	}
	if !isCurrentLatencyState(state, generation) {
		return nil, nil
	}
	return &state, nil
}

func isCurrentLatencyState(state latencyState, generation string) bool {
	return state.Generation == generation
}

func decodeLatencyState(raw json.RawMessage) (latencyState, bool) {
	var state latencyState
	if err := json.Unmarshal(raw, &state); err != nil {
		return latencyState{}, false
	}
	if !isNormalRouteLatencyState(state) {
		return latencyState{}, false
	}
	return state, true
}

func isNormalRouteLatencyState(state latencyState) bool {
	if state.Generation == "" || state.AccountID == "" || state.RuntimeKey == "" {
		return false
	}
	if !isNormalRouteLatencyDegradationScope(state.Scope) {
		return false
	}
	if !isRouteStrategySpeedFirstConfig(state.Config) {
		return false
	}
	if state.RecoveryProbeRoundAttemptCount != nil && *state.RecoveryProbeRoundAttemptCount < 0 {
		return false
	}
	if state.RecoveryProbeRoundSuccessCount != nil && *state.RecoveryProbeRoundSuccessCount < 0 {
		return false
	}
	return true
}

func isNormalRouteLatencyDegradationScope(scope LatencyDegradationScope) bool {
	return strings.TrimSpace(scope.SystemAccountID) != "" &&
		strings.TrimSpace(scope.RouteStrategyID) != "" &&
		strings.TrimSpace(scope.GroupID) != ""
}

func isRouteStrategySpeedFirstConfig(config SpeedFirstRuntimeConfig) bool {
	return isFinitePositiveInt(config.FirstByteDeadlineMs) &&
		isFinitePositiveInt(config.SlowTriggerCount) &&
		isFinitePositiveInt(config.SlowWindowSeconds) &&
		isFinitePositiveInt(config.RecoverySuccessCount) &&
		isFinitePositiveInt(config.ProbeIntervalSeconds) &&
		isFinitePositiveInt(config.DegradedTTLSeconds) &&
		isFinitePositiveInt(config.MaxFirstByteRetriesPerRequest)
}

func isFinitePositiveInt(value int64) bool { return value > 0 }

func accountLatencyStateKey(scope LatencyDegradationScope, account SuppressibleGatewayAccount) string {
	runtimeKey, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		runtimeKey = account.ID
	}
	return strings.Join([]string{
		latencyStateVersion,
		sanitizeKeyPart(scope.SystemAccountID),
		sanitizeKeyPart(scope.RouteStrategyID),
		sanitizeKeyPart(scope.GroupID),
		sanitizeKeyPart(runtimeKey),
	}, ":")
}

func accountLatencyStateKeyChecked(scope LatencyDegradationScope, account SuppressibleGatewayAccount) (string, error) {
	runtimeKey, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		latencyStateVersion,
		sanitizeKeyPart(scope.SystemAccountID),
		sanitizeKeyPart(scope.RouteStrategyID),
		sanitizeKeyPart(scope.GroupID),
		sanitizeKeyPart(runtimeKey),
	}, ":"), nil
}

func sanitizeKeyPart(value string) string {
	normalized := strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range normalized {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == ':' || r == '_' || r == '-' {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte('_')
	}
	if builder.Len() == 0 {
		return "_"
	}
	return builder.String()
}

func probeCandidateFromState(key string, state latencyState) (LatencyProbeCandidate, bool) {
	if state.DegradedUntilMs == nil || state.NextProbeAtMs == nil {
		return LatencyProbeCandidate{}, false
	}
	return LatencyProbeCandidate{
		StateKey:                       key,
		Generation:                     state.Generation,
		AccountID:                      state.AccountID,
		AccountName:                    state.AccountName,
		RuntimeKey:                     state.RuntimeKey,
		Scope:                          state.Scope,
		Config:                         state.Config,
		DegradationEventID:             state.DegradationEventID,
		DegradedUntil:                  ISOStringMs(*state.DegradedUntilMs),
		NextProbeAt:                    ISOStringMs(*state.NextProbeAtMs),
		RecoverySuccessCount:           state.SuccessCount,
		RecoveryProbeRoundAttemptCount: recoveryProbeRoundAttempts(state),
		RecoveryProbeRoundSuccessCount: recoveryProbeRoundSuccesses(state),
	}, true
}

func latencyProbeCandidateMatchesState(candidate LatencyProbeCandidate, state latencyState) bool {
	var candidateNextProbeAt *string
	if state.NextProbeAtMs != nil {
		candidateNextProbeAt = stringPtr(ISOStringMs(*state.NextProbeAtMs))
	}
	var candidateDegradedUntil *string
	if state.DegradedUntilMs != nil {
		candidateDegradedUntil = stringPtr(ISOStringMs(*state.DegradedUntilMs))
	}
	nextProbeAt := candidate.NextProbeAt
	degradedUntil := candidate.DegradedUntil
	return candidate.AccountID == state.AccountID &&
		candidate.RuntimeKey == state.RuntimeKey &&
		candidate.Scope.SystemAccountID == state.Scope.SystemAccountID &&
		candidate.Scope.RouteStrategyID == state.Scope.RouteStrategyID &&
		candidate.Scope.GroupID == state.Scope.GroupID &&
		stringPtrEqual(candidate.DegradationEventID, state.DegradationEventID) &&
		candidate.RecoveryProbeRoundAttemptCount == recoveryProbeRoundAttempts(state) &&
		candidate.RecoveryProbeRoundSuccessCount == recoveryProbeRoundSuccesses(state) &&
		stringPtrEqual(&nextProbeAt, candidateNextProbeAt) &&
		stringPtrEqual(&degradedUntil, candidateDegradedUntil)
}

func stringPtr(value string) *string { return &value }

func stringPtrEqual(left, right *string) bool {
	if left == nil && right == nil {
		return true
	}
	if left == nil || right == nil {
		return false
	}
	return *left == *right
}

func optionalAccountName(account SuppressibleGatewayAccount) *string {
	if trimmed := strings.TrimSpace(account.Name); trimmed != "" {
		return &trimmed
	}
	return nil
}

func isoPtr(ms *int64) *string {
	if ms == nil {
		return nil
	}
	return stringPtr(ISOStringMs(*ms))
}

func recoveryProbeRoundAttempts(state latencyState) int64 {
	if state.RecoveryProbeRoundAttemptCount == nil {
		return 0
	}
	return *state.RecoveryProbeRoundAttemptCount
}

func recoveryProbeRoundSuccesses(state latencyState) int64 {
	if state.RecoveryProbeRoundSuccessCount == nil {
		return 0
	}
	return *state.RecoveryProbeRoundSuccessCount
}

func latencyStateTTLMs(config SpeedFirstRuntimeConfig, degraded bool) int64 {
	seconds := config.SlowWindowSeconds
	if degraded {
		seconds = config.DegradedTTLSeconds
	}
	return maxInt64(1, seconds) * 1000
}

func latencyStateRemainingTTLMs(degradedUntilMs *int64, now int64) int64 {
	if degradedUntilMs == nil {
		return 1
	}
	return maxInt64(1, *degradedUntilMs-now)
}

func (s *LatencyDegradationService) nextProbeDelayMs(config SpeedFirstRuntimeConfig) int64 {
	baseMs := maxInt64(10, config.ProbeIntervalSeconds) * 1000
	return PassiveScheduleDelayMs(baseMs, s.jitterRandom())
}

func (s *LatencyDegradationService) nextRecoveryProbeDelayMs() int64 {
	return PassiveScheduleDelayMs(normalRouteRecoveryProbeIntervalMs, s.jitterRandom())
}

// ---------------------------------------------------------------------------
// Index maintenance.

func (s *LatencyDegradationService) loadLatencyStateIndexKeys(ctx context.Context, indexKey string) ([]string, error) {
	snapshot, err := s.loadLatencyStateIndexSnapshot(ctx, indexKey)
	if err != nil {
		return nil, err
	}
	return snapshot.keys, nil
}

type latencyIndexSnapshot struct {
	value json.RawMessage
	keys  []string
}

func (s *LatencyDegradationService) loadLatencyStateIndexSnapshot(ctx context.Context, indexKey string) (latencyIndexSnapshot, error) {
	value, err := s.store.GetJSON(ctx, indexKey)
	if err != nil {
		return latencyIndexSnapshot{}, err
	}
	var keys []string
	if value != nil {
		var index struct {
			Keys []string `json:"keys"`
		}
		if err := json.Unmarshal(value, &index); err == nil {
			keys = index.Keys
		}
	}
	return latencyIndexSnapshot{value: value, keys: normalizeLatencyStateIndexKeys(keys)}, nil
}

func (s *LatencyDegradationService) addLatencyStateAllIndexKey(ctx context.Context, key string) error {
	return s.addLatencyStateIndexKey(ctx, latencyStateAllIndexKey, latencyStateAllIndexLockKey, key)
}

func (s *LatencyDegradationService) addLatencyStateProbeIndexKey(ctx context.Context, key string) error {
	return s.addLatencyStateIndexKey(ctx, latencyStateProbeIndexKey, latencyStateProbeIndexLockKey, key)
}

func (s *LatencyDegradationService) addLatencyStateIndexKey(ctx context.Context, indexKey, lockKey, key string) error {
	return s.mutateLatencyStateIndexKeys(ctx, indexKey, lockKey, func(keys []string) []string {
		for _, existing := range keys {
			if existing == key {
				return keys
			}
		}
		keys = append(keys, key)
		if len(keys) > latencyStateIndexMaxKeys {
			keys = keys[len(keys)-latencyStateIndexMaxKeys:]
		}
		return keys
	})
}

func (s *LatencyDegradationService) removeLatencyStateIndexKeysStrict(ctx context.Context, keysToRemove []string) error {
	if len(keysToRemove) == 0 {
		return nil
	}
	removeSet := make(map[string]struct{}, len(keysToRemove))
	for _, key := range keysToRemove {
		removeSet[key] = struct{}{}
	}
	if err := s.mutateLatencyStateIndexKeys(ctx, latencyStateProbeIndexKey, latencyStateProbeIndexLockKey, func(keys []string) []string {
		return filterKeys(keys, removeSet)
	}); err != nil {
		return err
	}
	return s.mutateLatencyStateIndexKeys(ctx, latencyStateAllIndexKey, latencyStateAllIndexLockKey, func(keys []string) []string {
		return filterKeys(keys, removeSet)
	})
}

func filterKeys(keys []string, removeSet map[string]struct{}) []string {
	output := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, removed := removeSet[key]; removed {
			continue
		}
		output = append(output, key)
	}
	return output
}

func (s *LatencyDegradationService) mutateLatencyStateIndexKeys(
	ctx context.Context,
	indexKey, lockKey string,
	mutator func(keys []string) []string,
) error {
	token := NewUUID()
	locked, err := s.acquireLatencyStateIndexLock(ctx, lockKey, token)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("普通路由速度优先索引锁获取失败：%s", indexKey)
	}
	defer func() { _ = s.store.ReleaseLock(ctx, lockKey, token) }()
	for attempt := 0; attempt < latencyStateIndexCASMax; attempt++ {
		current, err := s.loadLatencyStateIndexSnapshot(ctx, indexKey)
		if err != nil {
			return err
		}
		next := map[string]any{"keys": normalizeLatencyStateIndexKeys(mutator(current.keys))}
		applied, err := s.store.CompareSetJSON(ctx, indexKey, current.value, next, latencyStateIndexTTLMs)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
	return fmt.Errorf("普通路由速度优先索引 CAS 重试耗尽（%d 次）：%s", latencyStateIndexCASMax, indexKey)
}

// writeLatencyStateAndIndexesStrict mirrors writeLatencyStateAndIndexesStrictAsync
// including the rollback path with its aggregate error.
func (s *LatencyDegradationService) writeLatencyStateAndIndexesStrict(
	ctx context.Context,
	key string,
	previous *latencyState,
	state latencyState,
	ttlMs int64,
	addProbeIndex bool,
) error {
	if err := s.store.SetJSON(ctx, key, state, ttlMs); err != nil {
		return err
	}
	allIndexApplied := false
	probeIndexApplied := false
	var writeErr error
	if err := s.addLatencyStateAllIndexKey(ctx, key); err != nil {
		writeErr = err
	} else {
		allIndexApplied = true
		if addProbeIndex {
			if err := s.addLatencyStateProbeIndexKey(ctx, key); err != nil {
				writeErr = err
			} else {
				probeIndexApplied = true
			}
		}
	}
	if writeErr == nil {
		return nil
	}
	var rollbackErrors []error
	stateRolledBack := false
	var rollbackErr error
	if previous != nil {
		previousConfigTTL := latencyStateTTLMs(previous.Config, previous.DegradedUntilMs != nil)
		stateRolledBack, rollbackErr = s.store.CompareSetJSON(ctx, key, mustMarshalJSON(state), *previous, previousConfigTTL)
	} else {
		stateRolledBack, rollbackErr = s.store.CompareDeleteJSON(ctx, key, mustMarshalJSON(state))
	}
	if rollbackErr != nil {
		rollbackErrors = append(rollbackErrors, rollbackErr)
	}
	if !stateRolledBack && rollbackErr == nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("普通路由速度优先 state rollback CAS 失败：%s", key))
	}
	if stateRolledBack && previous == nil && (allIndexApplied || probeIndexApplied) {
		if err := s.removeLatencyStateIndexKeysStrict(ctx, []string{key}); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	if len(rollbackErrors) > 0 {
		return fmt.Errorf("普通路由速度优先 state/index 写入失败且回滚存在 %d 个错误: %w", len(rollbackErrors), errors.Join(rollbackErrors...))
	}
	return writeErr
}

func (s *LatencyDegradationService) withLatencyStateMutationLock(
	ctx context.Context,
	key string,
	generation string,
	operation func() (bool, error),
) (bool, error) {
	lock, ok, err := s.acquireLatencyStateMutationLock(ctx, key, latencyStateMutationLockTTLMs)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	defer func() { _ = s.store.ReleaseLock(ctx, lock.lockKey, lock.token) }()
	renewed, err := s.renewLatencyStateGeneration(ctx, generation)
	if err != nil {
		return false, err
	}
	if !renewed {
		return false, nil
	}
	return operation()
}

func (s *LatencyDegradationService) clearCurrentGenerationLatencyStateKeys(
	ctx context.Context,
	keys []string,
	generation string,
	predicate func(latencyState) bool,
) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	var nextIndex atomic.Int64
	var totalCleared atomic.Int64
	workers := minInt(s.opts.BackgroundClearConcurrency, len(keys))
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				index := nextIndex.Add(1) - 1
				if index >= int64(len(keys)) {
					return
				}
				key := keys[index]
				cleared, err := s.runWithBackgroundSlot(ctx, func() (int64, error) {
					return s.clearCurrentGenerationLatencyStateKey(ctx, key, generation, predicate)
				})
				if err != nil {
					errCh <- err
					return
				}
				totalCleared.Add(cleared)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return 0, fmt.Errorf("普通路由速度优先逐 key 精确清理存在 %d 个失败: %w", len(errs), errors.Join(errs...))
	}
	return totalCleared.Load(), nil
}

func (s *LatencyDegradationService) runWithBackgroundSlot(ctx context.Context, task func() (int64, error)) (int64, error) {
	select {
	case s.clearSlots <- struct{}{}:
		defer func() { <-s.clearSlots }()
		return task()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (s *LatencyDegradationService) clearCurrentGenerationLatencyStateKey(
	ctx context.Context,
	key string,
	generation string,
	predicate func(latencyState) bool,
) (int64, error) {
	lock, ok, err := s.acquireLatencyStateMutationLock(ctx, key, latencyStateMutationLockTTLMs)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("普通路由速度优先状态 mutation lock 获取失败：%s", key)
	}
	defer func() { _ = s.store.ReleaseLock(ctx, lock.lockKey, lock.token) }()
	renewed, err := s.renewLatencyStateGeneration(ctx, generation)
	if err != nil {
		return 0, err
	}
	if !renewed {
		return 0, nil
	}
	raw, err := s.store.GetJSON(ctx, key)
	if err != nil {
		return 0, err
	}
	if raw == nil {
		if err := s.removeLatencyStateIndexKeysStrict(ctx, []string{key}); err != nil {
			return 0, err
		}
		return 0, nil
	}
	state, valid := decodeLatencyState(raw)
	if !valid {
		return 0, nil
	}
	if state.Generation != generation || !predicate(state) {
		return 0, nil
	}
	if err := s.deleteLatencyStateAndIndexesStrict(ctx, key); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *LatencyDegradationService) deleteLatencyStateAndIndexesStrict(ctx context.Context, key string) error {
	if err := s.store.Delete(ctx, key); err != nil {
		return err
	}
	return s.removeLatencyStateIndexKeysStrict(ctx, []string{key})
}

func (s *LatencyDegradationService) acquireLatencyStateMutationLock(ctx context.Context, key string, ttlMs int64) (*latencyStateLock, bool, error) {
	lockKey := latencyStateMutationLockKey(key)
	token := NewUUID()
	locked, err := s.acquireLatencyStateLock(ctx, lockKey, token, ttlMs)
	if err != nil {
		return nil, false, err
	}
	if !locked {
		return nil, false, nil
	}
	return &latencyStateLock{key: key, lockKey: lockKey, token: token}, true, nil
}

func latencyStateMutationLockKey(key string) string {
	return latencyStateVersion + ":mutation-lock:" + key
}

func normalRouteLatencyProbeClaimLockKey(candidate LatencyProbeCandidate) string {
	return latencyStateVersion + ":probe-claim:" + candidate.Generation + ":" + candidate.StateKey
}

func (s *LatencyDegradationService) acquireLatencyStateIndexLock(ctx context.Context, lockKey, token string) (bool, error) {
	return s.acquireLatencyStateLock(ctx, lockKey, token, latencyStateIndexLockTTLMs)
}

func (s *LatencyDegradationService) acquireLatencyStateLock(ctx context.Context, lockKey, token string, ttlMs int64) (bool, error) {
	for attempt := 0; attempt < latencyStateLockAcquireMaxAttempts; attempt++ {
		locked, err := s.store.AcquireLock(ctx, lockKey, ttlMs, token)
		if err != nil {
			return false, err
		}
		if locked {
			return true, nil
		}
		s.lockRetryDelay(attempt)
	}
	return false, nil
}

func normalizeLatencyStateIndexKeys(value []string) []string {
	if value == nil {
		return []string{}
	}
	seen := make(map[string]struct{}, len(value))
	keys := make([]string, 0, len(value))
	for _, item := range value {
		key := strings.TrimSpace(item)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) > latencyStateIndexMaxKeys {
		keys = keys[len(keys)-latencyStateIndexMaxKeys:]
	}
	return keys
}

func normalizedRouteStrategyIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func mustMarshalJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return encoded
}
