package gatewayhotquality

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// Memory same-tier exploration store mirroring
// backend/src/modules/gateway/runtime/same-tier-exploration-memory-store.ts.

// MemorySameTierExplorationStoreOptions mirrors the Node options object.
type MemorySameTierExplorationStoreOptions struct {
	Now          func() int64
	StateTtlMs   *int64
	PoolCapacity *int
}

// MemorySameTierExplorationStore mirrors MemorySameTierExplorationStore.
type MemorySameTierExplorationStore struct {
	mu           sync.Mutex
	states       map[string]*SameTierExplorationState
	now          func() int64
	stateTtlMs   int64
	poolCapacity int
}

// NewMemorySameTierExplorationStore mirrors the Node constructor.
func NewMemorySameTierExplorationStore(options MemorySameTierExplorationStoreOptions) (*MemorySameTierExplorationStore, error) {
	now := options.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	stateTtlMs := SameTierExplorationStateTTLMS
	if options.StateTtlMs != nil {
		stateTtlMs = *options.StateTtlMs
	}
	poolCapacity := SameTierExplorationPoolCapacity
	if options.PoolCapacity != nil {
		poolCapacity = *options.PoolCapacity
	}
	normalizedTtl, err := explorationPositiveInteger(stateTtlMs, "stateTtlMs")
	if err != nil {
		return nil, err
	}
	if poolCapacity <= 0 {
		return nil, fmt.Errorf("poolCapacity 必须是正整数")
	}
	return &MemorySameTierExplorationStore{
		states:       make(map[string]*SameTierExplorationState),
		now:          now,
		stateTtlMs:   normalizedTtl,
		poolCapacity: poolCapacity,
	}, nil
}

// Get mirrors get.
func (store *MemorySameTierExplorationStore) Get(ctx context.Context, input SameTierExplorationGetInput) (*SameTierExplorationState, error) {
	nowMs, err := explorationNormalizedNow(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, err := store.load(input.PoolKey, nowMs)
	if err != nil {
		return nil, err
	}
	return CloneSameTierExplorationState(*state), nil
}

// Accrue mirrors accrue.
func (store *MemorySameTierExplorationStore) Accrue(ctx context.Context, input SameTierExplorationAccrueInput) (*SameTierExplorationState, error) {
	nowMs, err := explorationNormalizedNow(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	token, err := explorationRequiredKey(input.AccrualToken, "accrualToken")
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, err := store.load(input.PoolKey, nowMs)
	if err != nil {
		return nil, err
	}
	if _, exists := store.states[state.PoolKey]; !exists {
		return CloneSameTierExplorationState(*state), nil
	}
	if input.Eligible && !containsString(state.AccruedTokens, token) {
		// Keep a rolling idempotency window; a full window must not freeze a
		// hot pool forever.
		if len(state.AccruedTokens) >= SameTierExplorationIdentityCapacity {
			state.AccruedTokens = append([]string{}, state.AccruedTokens[len(state.AccruedTokens)-(SameTierExplorationIdentityCapacity-1):]...)
		}
		credit := math.Min(SameTierExplorationCreditCap, state.Credit+SameTierExplorationCreditIncrement)
		state.Credit = credit
		state.AccruedTokens = append(state.AccruedTokens, token)
	}
	state.ExpiresAtMs = nowMs + store.stateTtlMs
	store.states[state.PoolKey] = state
	return CloneSameTierExplorationState(*state), nil
}

// Reserve mirrors reserve.
func (store *MemorySameTierExplorationStore) Reserve(ctx context.Context, input SameTierExplorationReserveInput) (*SameTierExplorationReserveResult, error) {
	nowMs, err := explorationNormalizedNow(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	reservationId, err := explorationRequiredKey(input.ReservationID, "reservationId")
	if err != nil {
		return nil, err
	}
	accountRuntimeKey, err := explorationRequiredKey(input.AccountRuntimeKey, "accountRuntimeKey")
	if err != nil {
		return nil, err
	}
	leaseUntilMs, err := explorationNormalizedNow(input.LeaseUntilMs)
	if err != nil {
		return nil, err
	}
	if leaseUntilMs <= nowMs {
		return nil, errors.New("leaseUntilMs 必须晚于 nowMs")
	}
	if leaseUntilMs > nowMs+store.stateTtlMs {
		return nil, errors.New("leaseUntilMs 不得晚于 pool TTL")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, err := store.load(input.PoolKey, nowMs)
	if err != nil {
		return nil, err
	}
	for _, existing := range state.Reservations {
		if existing.ReservationID == reservationId {
			status := ExplorationReservationReservationConflict
			var reservation *SameTierExplorationReservation
			if existing.AccountRuntimeKey == accountRuntimeKey {
				status = ExplorationReservationReserved
				cloned := existing
				reservation = &cloned
			}
			return &SameTierExplorationReserveResult{
				Status:      status,
				State:       *CloneSameTierExplorationState(*state),
				Reservation: reservation,
			}, nil
		}
	}
	if containsString(state.SettledReservationIDs, reservationId) {
		return &SameTierExplorationReserveResult{
			Status: ExplorationReservationReservationConflict,
			State:  *CloneSameTierExplorationState(*state),
		}, nil
	}
	if state.Credit < SameTierExplorationCreditCost {
		return &SameTierExplorationReserveResult{
			Status: ExplorationReservationCreditUnavailable,
			State:  *CloneSameTierExplorationState(*state),
		}, nil
	}
	if len(state.Reservations) > 0 {
		return &SameTierExplorationReserveResult{
			Status: ExplorationReservationPoolBusy,
			State:  *CloneSameTierExplorationState(*state),
		}, nil
	}
	if state.CooldownUntilMsByRuntimeKey[accountRuntimeKey] > nowMs {
		return &SameTierExplorationReserveResult{
			Status: ExplorationReservationTargetCooldown,
			State:  *CloneSameTierExplorationState(*state),
		}, nil
	}
	if _, inCooldown := state.CooldownUntilMsByRuntimeKey[accountRuntimeKey]; !inCooldown &&
		len(state.CooldownUntilMsByRuntimeKey) >= SameTierExplorationIdentityCapacity {
		return &SameTierExplorationReserveResult{
			Status: ExplorationReservationTargetCooldown,
			State:  *CloneSameTierExplorationState(*state),
		}, nil
	}
	reservation := SameTierExplorationReservation{
		ReservationID:     reservationId,
		AccountRuntimeKey: accountRuntimeKey,
		LeaseUntilMs:      leaseUntilMs,
	}
	state.Reservations = append(append([]SameTierExplorationReservation{}, state.Reservations...), reservation)
	state.ExpiresAtMs = nowMs + store.stateTtlMs
	store.states[state.PoolKey] = state
	cloned := reservation
	return &SameTierExplorationReserveResult{
		Status:      ExplorationReservationReserved,
		State:       *CloneSameTierExplorationState(*state),
		Reservation: &cloned,
	}, nil
}

// Settle mirrors settle.
func (store *MemorySameTierExplorationStore) Settle(ctx context.Context, input SameTierExplorationSettleInput) (*SameTierExplorationSettleResult, error) {
	nowMs, err := explorationNormalizedNow(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	reservationId, err := explorationRequiredKey(input.ReservationID, "reservationId")
	if err != nil {
		return nil, err
	}
	accountRuntimeKey, err := explorationRequiredKey(input.AccountRuntimeKey, "accountRuntimeKey")
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, err := store.load(input.PoolKey, nowMs)
	if err != nil {
		return nil, err
	}
	if containsString(state.SettledReservationIDs, reservationId) {
		return &SameTierExplorationSettleResult{
			Status: ExplorationSettlementIdempotent,
			State:  *CloneSameTierExplorationState(*state),
		}, nil
	}
	foundIndex := -1
	for index, item := range state.Reservations {
		if item.ReservationID == reservationId {
			foundIndex = index
			break
		}
	}
	if foundIndex < 0 {
		return &SameTierExplorationSettleResult{
			Status: ExplorationSettlementReservationNotFound,
			State:  *CloneSameTierExplorationState(*state),
		}, nil
	}
	if state.Reservations[foundIndex].AccountRuntimeKey != accountRuntimeKey {
		return &SameTierExplorationSettleResult{
			Status: ExplorationSettlementReservationConflict,
			State:  *CloneSameTierExplorationState(*state),
		}, nil
	}
	remaining := make([]SameTierExplorationReservation, 0, len(state.Reservations))
	for index, item := range state.Reservations {
		if index != foundIndex {
			remaining = append(remaining, item)
		}
	}
	state.Reservations = remaining
	state.SettledReservationIDs = append(append([]string{}, state.SettledReservationIDs...), reservationId)
	if len(state.SettledReservationIDs) > SameTierExplorationIdentityCapacity {
		state.SettledReservationIDs = state.SettledReservationIDs[len(state.SettledReservationIDs)-SameTierExplorationIdentityCapacity:]
	}
	if input.Outcome == "dispatched" {
		credit := state.Credit - SameTierExplorationCreditCost
		if credit < 0 {
			credit = 0
		}
		state.Credit = credit
		if state.Cursor == maxSafeInteger {
			state.Cursor = 0
		} else {
			state.Cursor = state.Cursor + 1
		}
		cooldown := make(map[string]int64, len(state.CooldownUntilMsByRuntimeKey)+1)
		for key, value := range state.CooldownUntilMsByRuntimeKey {
			cooldown[key] = value
		}
		cooldown[accountRuntimeKey] = nowMs + SameTierExplorationTargetCooldownMS
		state.CooldownUntilMsByRuntimeKey = cooldown
	}
	state.ExpiresAtMs = nowMs + store.stateTtlMs
	store.states[state.PoolKey] = state
	return &SameTierExplorationSettleResult{
		Status: ExplorationSettlementApplied,
		State:  *CloneSameTierExplorationState(*state),
	}, nil
}

func (store *MemorySameTierExplorationStore) load(poolKey string, nowMs int64) (*SameTierExplorationState, error) {
	normalizedPoolKey, err := explorationRequiredKey(poolKey, "poolKey")
	if err != nil {
		return nil, err
	}
	current, exists := store.states[normalizedPoolKey]
	if exists && current.ExpiresAtMs <= nowMs {
		delete(store.states, normalizedPoolKey)
		exists = false
		current = nil
	}
	if !exists || current.ExpiresAtMs <= nowMs {
		empty, err := EmptySameTierExplorationState(normalizedPoolKey, nowMs)
		if err != nil {
			return nil, err
		}
		empty.ExpiresAtMs = nowMs + store.stateTtlMs
		if !store.reservePoolCapacity(nowMs) {
			return empty, nil
		}
		store.states[normalizedPoolKey] = empty
		return empty, nil
	}
	normalized, err := NormalizeSameTierExplorationState(*current, nowMs)
	if err != nil {
		return nil, err
	}
	normalized.ExpiresAtMs = nowMs + store.stateTtlMs
	store.states[normalizedPoolKey] = normalized
	return normalized, nil
}

func (store *MemorySameTierExplorationStore) reservePoolCapacity(nowMs int64) bool {
	if len(store.states) < store.poolCapacity {
		return true
	}
	for poolKey, state := range store.states {
		if state.ExpiresAtMs <= nowMs {
			delete(store.states, poolKey)
		}
	}
	return len(store.states) < store.poolCapacity
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
