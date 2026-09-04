package gatewayhotquality

import (
	"context"
	"errors"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Speed-first cutover reservation mirroring
// backend/src/modules/gateway/runtime/speed-first-cutover-reservation.service.ts.
// The Node account-concurrency slot acquirer (shared infra, not yet migrated)
// is a port here; effectiveImageLaneConcurrencyLimit is mirrored locally off
// gatewayruntimecache.GroupSchedulingPolicy (same map shape as
// domain/types.ts GroupSchedulingPolicy).

// AccountConcurrencyLane mirrors AccountConcurrencyLane.
type AccountConcurrencyLane = string

// Lanes (mirror the Node union).
const (
	AccountConcurrencyLaneText  AccountConcurrencyLane = "text"
	AccountConcurrencyLaneImage AccountConcurrencyLane = "image"
)

// GatewayAccountConcurrencyLimitIdentity mirrors
// GatewayAccountConcurrencyLimitIdentity (dispatch/account-concurrency-identity).
type GatewayAccountConcurrencyLimitIdentity struct {
	ID                        string
	CredentialSourceAccountID string
	ConcurrencyLimit          int
}

// AccountConcurrencySlot is the port view of a held concurrency slot (Node
// AccountConcurrencySlot: acquired + release + lane metadata).
type AccountConcurrencySlot struct {
	Key     string
	Lane    AccountConcurrencyLane
	Release func()
}

// AccountConcurrencyAcquireRequest carries the lane request (Node passes the
// lane object as third argument).
type AccountConcurrencyAcquireRequest struct {
	Lane           AccountConcurrencyLane
	ImageLaneLimit int // only meaningful when Lane == image
}

// SpeedFirstCutoverSlotAcquirer mirrors tryAcquireAccountConcurrencyAsync.
// Acquired=false means the target is at capacity; the returned slot is only
// meaningful when acquired.
type SpeedFirstCutoverSlotAcquirer func(
	ctx context.Context,
	accountConcurrencyAccountID string,
	concurrencyLimit int,
	request AccountConcurrencyAcquireRequest,
) (AccountConcurrencySlot, bool, error)

var cutoverSlotAcquirerForTest SpeedFirstCutoverSlotAcquirer

// SetSpeedFirstCutoverSlotAcquirerForTest mirrors
// setSpeedFirstCutoverSlotAcquirerForTest (overrides the input acquirer).
func SetSpeedFirstCutoverSlotAcquirerForTest(acquirer SpeedFirstCutoverSlotAcquirer) {
	cutoverSlotAcquirerForTest = acquirer
}

// SpeedFirstCutoverReservationInput mirrors the
// reserveSpeedFirstCutoverTarget argument object. SlotAcquirer replaces the
// Node default import of tryAcquireAccountConcurrencyAsync (composition-root
// wiring).
type SpeedFirstCutoverReservationInput struct {
	SystemAccountID       string
	RouteStrategyID       string
	GroupID               string
	SlowAccountID         string
	Targets               []GatewayAccountConcurrencyLimitIdentity
	Lane                  AccountConcurrencyLane
	GroupSchedulingPolicy gatewayruntimecache.GroupSchedulingPolicy
	SlotAcquirer          SpeedFirstCutoverSlotAcquirer
}

// SpeedFirstCutoverReservation mirrors SpeedFirstCutoverReservation.
type SpeedFirstCutoverReservation struct {
	targetAccountID string

	mu             sync.Mutex
	consumed       bool
	released       bool
	underlyingSlot AccountConcurrencySlot
	reservedSlot   AccountConcurrencySlot
}

// TargetAccountID mirrors the readonly targetAccountId field.
func (reservation *SpeedFirstCutoverReservation) TargetAccountID() string {
	return reservation.targetAccountID
}

// Consumed mirrors the `consumed` getter.
func (reservation *SpeedFirstCutoverReservation) Consumed() bool {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	return reservation.consumed
}

// TakeForAccount mirrors takeForAccount: one-shot slot transfer to the target
// account only.
func (reservation *SpeedFirstCutoverReservation) TakeForAccount(account GatewayAccountConcurrencyLimitIdentity) (AccountConcurrencySlot, bool) {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.consumed || reservation.released || account.ID != reservation.targetAccountID {
		return AccountConcurrencySlot{}, false
	}
	reservation.consumed = true
	return reservation.reservedSlot, true
}

// Release mirrors release (idempotent): it releases the underlying acquired
// slot exactly once, no matter whether the caller uses the reservation or the
// transferred slot handle.
func (reservation *SpeedFirstCutoverReservation) Release() {
	reservation.mu.Lock()
	if reservation.released {
		reservation.mu.Unlock()
		return
	}
	reservation.released = true
	underlyingRelease := reservation.underlyingSlot.Release
	reservation.mu.Unlock()
	if underlyingRelease != nil {
		underlyingRelease()
	}
}

// ReserveSpeedFirstCutoverTarget mirrors reserveSpeedFirstCutoverTarget.
func ReserveSpeedFirstCutoverTarget(ctx context.Context, input SpeedFirstCutoverReservationInput) (*SpeedFirstCutoverReservation, error) {
	slotAcquirer := cutoverSlotAcquirerForTest
	if slotAcquirer == nil {
		slotAcquirer = input.SlotAcquirer
	}
	if slotAcquirer == nil {
		return nil, errors.New("热质量切换预留缺少并发槽获取器")
	}
	var acquiredSlot *AccountConcurrencySlot
	ownershipTransferred := false
	defer func() {
		if !ownershipTransferred && acquiredSlot != nil && acquiredSlot.Release != nil {
			acquiredSlot.Release()
		}
	}()
	for _, target := range input.Targets {
		request := AccountConcurrencyAcquireRequest{Lane: input.Lane}
		if input.Lane == AccountConcurrencyLaneImage {
			request.ImageLaneLimit = EffectiveImageLaneConcurrencyLimit(target.ConcurrencyLimit, input.GroupSchedulingPolicy)
		}
		slot, acquired, err := slotAcquirer(ctx, gatewayAccountConcurrencyAccountID(target), target.ConcurrencyLimit, request)
		if err != nil {
			return nil, err
		}
		if !acquired {
			continue
		}
		acquiredSlot = &slot
		reservation := createCutoverReservation(target, slot)
		ownershipTransferred = true
		return reservation, nil
	}
	return nil, nil
}

// ClearSpeedFirstCutoverReservationsForTest mirrors
// clearSpeedFirstCutoverReservationsForTest.
func ClearSpeedFirstCutoverReservationsForTest() {
	cutoverSlotAcquirerForTest = nil
}

// SpeedFirstCutoverBudgetSnapshot mirrors speedFirstCutoverBudgetSnapshot
// (kept for regression consumers that assert no process-local cutover gate
// remains).
func SpeedFirstCutoverBudgetSnapshot() []struct {
	Key    string
	Active int
} {
	return nil
}

func createCutoverReservation(
	target GatewayAccountConcurrencyLimitIdentity,
	slot AccountConcurrencySlot,
) *SpeedFirstCutoverReservation {
	reservation := &SpeedFirstCutoverReservation{targetAccountID: target.ID}
	reservation.underlyingSlot = slot
	reservation.reservedSlot = AccountConcurrencySlot{
		Key:     slot.Key,
		Lane:    slot.Lane,
		Release: reservation.Release,
	}
	return reservation
}

// gatewayAccountConcurrencyAccountID mirrors gatewayAccountConcurrencyAccountId
// (dispatch/account-concurrency-identity): the credential source account wins
// over the account id (normalized).
func gatewayAccountConcurrencyAccountID(target GatewayAccountConcurrencyLimitIdentity) string {
	normalized := trimSpace(target.CredentialSourceAccountID)
	if normalized != "" {
		return normalized
	}
	return target.ID
}

var defaultHighConcurrencyImageLaneMaxConcurrency = 0

// EffectiveImageLaneConcurrencyLimit mirrors
// effectiveImageLaneConcurrencyLimit (domain/group-scheduling.ts): the image
// lane never exceeds the account hard limit and never exceeds the configured
// imageLaneMaxConcurrency when positive.
func EffectiveImageLaneConcurrencyLimit(accountConcurrencyLimit int, policy gatewayruntimecache.GroupSchedulingPolicy) int {
	hardLimit := boundedPositiveInteger(accountConcurrencyLimit, 1, 1_000_000)
	configured := defaultHighConcurrencyImageLaneMaxConcurrency
	if policy != nil {
		if value, ok := policy["imageLaneMaxConcurrency"]; ok && value != nil {
			configured = numericPolicyValue(value, configured)
		}
	}
	if configured > 0 {
		return mini(hardLimit, maxi(1, configured))
	}
	return hardLimit
}

func boundedPositiveInteger(value int, fallback int, maximum int) int {
	if value < 1 || value > maximum {
		return fallback
	}
	return value
}

func numericPolicyValue(value interface{}, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	}
	return fallback
}

func mini(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxi(left int, right int) int {
	if left > right {
		return left
	}
	return right
}
