package gatewayhotquality

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
)

// Same-tier exploration store contracts mirroring
// backend/src/modules/gateway/runtime/same-tier-exploration-store.ts.
// The credit/cursor constants re-use the gatewayhybrid selection-layer
// literals (same Node values, single source of truth in Go).

const (
	// SameTierExplorationStateTTLMS mirrors SAME_TIER_EXPLORATION_STATE_TTL_MS (40 min).
	SameTierExplorationStateTTLMS = int64(40 * 60_000)
	// SameTierExplorationPoolCapacity mirrors SAME_TIER_EXPLORATION_POOL_CAPACITY.
	SameTierExplorationPoolCapacity = 10_000
	// SameTierExplorationIdentityCapacity mirrors SAME_TIER_EXPLORATION_IDENTITY_CAPACITY.
	SameTierExplorationIdentityCapacity = 2_048
	// SameTierExplorationCreditIncrement mirrors SAME_TIER_EXPLORATION_CREDIT_INCREMENT
	// (same value as gatewayhybrid.SameTierExplorationCreditPerEligibleDispatch).
	SameTierExplorationCreditIncrement = gatewayhybrid.SameTierExplorationCreditPerEligibleDispatch
	// SameTierExplorationCreditCost mirrors SAME_TIER_EXPLORATION_CREDIT_COST.
	SameTierExplorationCreditCost = gatewayhybrid.SameTierExplorationCreditCost
	// SameTierExplorationCreditCap mirrors SAME_TIER_EXPLORATION_CREDIT_CAP.
	SameTierExplorationCreditCap = gatewayhybrid.SameTierExplorationCreditCap
	// SameTierExplorationTargetCooldownMS mirrors SAME_TIER_EXPLORATION_TARGET_COOLDOWN_MS.
	SameTierExplorationTargetCooldownMS = gatewayhybrid.SameTierExplorationTargetCooldownMs
)

// SameTierExplorationReservation mirrors SameTierExplorationReservation.
type SameTierExplorationReservation struct {
	ReservationID     string `json:"reservationId"`
	AccountRuntimeKey string `json:"accountRuntimeKey"`
	LeaseUntilMs      int64  `json:"leaseUntilMs"`
}

// SameTierExplorationState mirrors same-tier-exploration-store.ts
// SameTierExplorationState (the *stored* pool state; the gatewayhybrid
// SameTierExplorationState is the decision-input view built from it).
type SameTierExplorationState struct {
	PoolKey                     string                           `json:"poolKey"`
	Credit                      float64                          `json:"credit"`
	Cursor                      int64                            `json:"cursor"`
	Reservations                []SameTierExplorationReservation `json:"reservations"`
	CooldownUntilMsByRuntimeKey map[string]int64                 `json:"cooldownUntilMsByRuntimeKey"`
	AccruedTokens               []string                         `json:"accruedTokens"`
	SettledReservationIDs       []string                         `json:"settledReservationIds"`
	ExpiresAtMs                 int64                            `json:"expiresAtMs"`
}

// Same-tier exploration reservation statuses (mirror the Node union).
const (
	ExplorationReservationReserved            = "reserved"
	ExplorationReservationCreditUnavailable   = "credit_unavailable"
	ExplorationReservationPoolBusy            = "pool_busy"
	ExplorationReservationTargetCooldown      = "target_cooldown"
	ExplorationReservationReservationConflict = "reservation_conflict"
)

// SameTierExplorationReservationStatus mirrors the Node union type.
type SameTierExplorationReservationStatus = string

// Same-tier exploration settlement statuses (mirror the Node union).
const (
	ExplorationSettlementApplied             = "applied"
	ExplorationSettlementIdempotent          = "idempotent"
	ExplorationSettlementReservationNotFound = "reservation_not_found"
	ExplorationSettlementReservationConflict = "reservation_conflict"
)

// SameTierExplorationSettlementStatus mirrors the Node union type.
type SameTierExplorationSettlementStatus = string

// SameTierExplorationGetInput mirrors the get argument object.
type SameTierExplorationGetInput struct {
	PoolKey string
	NowMs   *int64
}

// SameTierExplorationAccrueInput mirrors the accrue argument object.
type SameTierExplorationAccrueInput struct {
	PoolKey      string
	AccrualToken string
	Eligible     bool
	NowMs        *int64
}

// SameTierExplorationReserveInput mirrors the reserve argument object.
type SameTierExplorationReserveInput struct {
	PoolKey           string
	ReservationID     string
	AccountRuntimeKey string
	LeaseUntilMs      int64
	NowMs             *int64
}

// SameTierExplorationReserveResult mirrors the reserve result object.
type SameTierExplorationReserveResult struct {
	Status      SameTierExplorationReservationStatus
	State       SameTierExplorationState
	Reservation *SameTierExplorationReservation
}

// SameTierExplorationSettleInput mirrors the settle argument object.
type SameTierExplorationSettleInput struct {
	PoolKey           string
	ReservationID     string
	AccountRuntimeKey string
	Outcome           string // 'dispatched' | 'not_dispatched'
	NowMs             *int64
}

// SameTierExplorationSettleResult mirrors the settle result object.
type SameTierExplorationSettleResult struct {
	Status SameTierExplorationSettlementStatus
	State  SameTierExplorationState
}

// SameTierExplorationStore mirrors SameTierExplorationStore.
type SameTierExplorationStore interface {
	Get(ctx context.Context, input SameTierExplorationGetInput) (*SameTierExplorationState, error)
	Accrue(ctx context.Context, input SameTierExplorationAccrueInput) (*SameTierExplorationState, error)
	Reserve(ctx context.Context, input SameTierExplorationReserveInput) (*SameTierExplorationReserveResult, error)
	Settle(ctx context.Context, input SameTierExplorationSettleInput) (*SameTierExplorationSettleResult, error)
}

// EmptySameTierExplorationState mirrors emptySameTierExplorationState.
func EmptySameTierExplorationState(poolKey string, nowMs int64) (*SameTierExplorationState, error) {
	normalizedPoolKey, err := explorationRequiredKey(poolKey, "poolKey")
	if err != nil {
		return nil, err
	}
	return &SameTierExplorationState{
		PoolKey:     normalizedPoolKey,
		Credit:      0,
		Cursor:      0,
		ExpiresAtMs: nowMs + SameTierExplorationStateTTLMS,
	}, nil
}

// NormalizeSameTierExplorationState mirrors normalizeSameTierExplorationState.
// Expired lease IDs are retained as fencing tombstones so a late owner cannot
// reuse its reservation ID and settle a newer lease.
func NormalizeSameTierExplorationState(input SameTierExplorationState, nowMs int64) (*SameTierExplorationState, error) {
	poolKey, err := explorationRequiredKey(input.PoolKey, "poolKey")
	if err != nil {
		return nil, err
	}
	credit, err := finiteRange(input.Credit, 0, SameTierExplorationCreditCap, "credit")
	if err != nil {
		return nil, err
	}
	cursor, err := nonNegativeSafeInteger(input.Cursor, "cursor")
	if err != nil {
		return nil, err
	}
	var normalizedReservations []SameTierExplorationReservation
	for _, reservation := range input.Reservations {
		if reservation == (SameTierExplorationReservation{}) {
			continue
		}
		reservationID, err := explorationRequiredKey(reservation.ReservationID, "reservationId")
		if err != nil {
			return nil, err
		}
		accountRuntimeKey, err := explorationRequiredKey(reservation.AccountRuntimeKey, "accountRuntimeKey")
		if err != nil {
			return nil, err
		}
		leaseUntilMs, err := nonNegativeSafeInteger(reservation.LeaseUntilMs, "leaseUntilMs")
		if err != nil {
			return nil, err
		}
		normalizedReservations = append(normalizedReservations, SameTierExplorationReservation{
			ReservationID:     reservationID,
			AccountRuntimeKey: accountRuntimeKey,
			LeaseUntilMs:      leaseUntilMs,
		})
	}
	var reservations []SameTierExplorationReservation
	var expiredReservationIDs []string
	for _, reservation := range normalizedReservations {
		if reservation.LeaseUntilMs > nowMs {
			reservations = append(reservations, reservation)
		} else {
			expiredReservationIDs = append(expiredReservationIDs, reservation.ReservationID)
		}
	}
	cooldownUntilMsByRuntimeKey := make(map[string]int64)
	for runtimeKey, untilMs := range input.CooldownUntilMsByRuntimeKey {
		if untilMs <= nowMs {
			continue
		}
		normalizedKey, err := explorationRequiredKey(runtimeKey, "cooldown accountRuntimeKey")
		if err != nil {
			return nil, err
		}
		normalizedUntilMs, err := nonNegativeSafeInteger(untilMs, "cooldownUntilMs")
		if err != nil {
			return nil, err
		}
		cooldownUntilMsByRuntimeKey[normalizedKey] = normalizedUntilMs
	}
	accruedTokens, err := uniqueBoundedKeys(input.AccruedTokens, SameTierExplorationIdentityCapacity)
	if err != nil {
		return nil, err
	}
	settledReservationIds, err := uniqueBoundedKeys(
		append(append([]string{}, input.SettledReservationIDs...), expiredReservationIDs...),
		SameTierExplorationIdentityCapacity)
	if err != nil {
		return nil, err
	}
	expiresAtMs, err := nonNegativeSafeInteger(input.ExpiresAtMs, "expiresAtMs")
	if err != nil {
		return nil, err
	}
	if nowMs+1 > expiresAtMs {
		expiresAtMs = nowMs + 1
	}
	return &SameTierExplorationState{
		PoolKey:                     poolKey,
		Credit:                      credit,
		Cursor:                      cursor,
		Reservations:                reservations,
		CooldownUntilMsByRuntimeKey: cooldownUntilMsByRuntimeKey,
		AccruedTokens:               accruedTokens,
		SettledReservationIDs:       settledReservationIds,
		ExpiresAtMs:                 expiresAtMs,
	}, nil
}

// CloneSameTierExplorationState mirrors cloneSameTierExplorationState.
func CloneSameTierExplorationState(input SameTierExplorationState) *SameTierExplorationState {
	cloned := input
	cloned.Reservations = make([]SameTierExplorationReservation, len(input.Reservations))
	copy(cloned.Reservations, input.Reservations)
	cloned.CooldownUntilMsByRuntimeKey = make(map[string]int64, len(input.CooldownUntilMsByRuntimeKey))
	for key, value := range input.CooldownUntilMsByRuntimeKey {
		cloned.CooldownUntilMsByRuntimeKey[key] = value
	}
	cloned.AccruedTokens = append([]string{}, input.AccruedTokens...)
	cloned.SettledReservationIDs = append([]string{}, input.SettledReservationIDs...)
	return &cloned
}

// uniqueBoundedKeys mirrors the private Node helper: scan newest-first,
// dedupe, keep the most recent identities up to the limit.
func uniqueBoundedKeys(values []string, limit int) ([]string, error) {
	var result []string
	seen := make(map[string]struct{})
	reversed := make([]string, len(values))
	copy(reversed, values)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	for _, value := range reversed {
		normalized, err := explorationRequiredKey(value, "状态 identity")
		if err != nil {
			return nil, err
		}
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		result = append([]string{normalized}, result...)
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func explorationRequiredKey(value string, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" || len(normalized) > 512 {
		return "", fmt.Errorf("%s 必须是 1 到 512 字符", name)
	}
	return normalized, nil
}

func finiteRange(value float64, minimum float64, maximum float64, name string) (float64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s 超出范围", name)
	}
	return math.Round(value*1_000_000) / 1_000_000, nil
}

func nonNegativeSafeInteger(value int64, name string) (int64, error) {
	if value < 0 || value > maxSafeInteger {
		return 0, fmt.Errorf("%s 必须是非负安全整数", name)
	}
	return value, nil
}

// explorationNormalizedNow mirrors the exploration normalizedNow helper
// (message differs from the hot-quality one).
func explorationNormalizedNow(value int64) (int64, error) {
	if value < 0 || value > maxSafeInteger {
		return 0, errors.New("时间必须是非负安全整数")
	}
	return value, nil
}

func explorationPositiveInteger(value int64, name string) (int64, error) {
	if value <= 0 || value > maxSafeInteger {
		return 0, fmt.Errorf("%s 必须是正整数", name)
	}
	return value, nil
}
