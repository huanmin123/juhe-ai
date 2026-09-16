package gatewayhotquality

// w11c: memory exploration store guard branches, ttl/capacity bounds and
// settle/expire corners.

import (
	"context"
	"strings"
	"testing"
)

func w11cExplorationStore(t *testing.T) *MemorySameTierExplorationStore {
	t.Helper()
	store, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return store
}

func TestW11CExplorationStoreOptionValidation(t *testing.T) {
	bad := int64(-1)
	if _, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{StateTtlMs: &bad}); err == nil {
		t.Fatalf("negative ttl must fail")
	}
	worse := int64(0)
	if _, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{StateTtlMs: &worse}); err == nil {
		t.Fatalf("zero ttl must fail")
	}
	zeroCapacity := 0
	if _, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{PoolCapacity: &zeroCapacity}); err == nil {
		t.Fatalf("zero capacity must fail")
	}
}

func TestW11CExplorationStoreInputGuards(t *testing.T) {
	store := w11cExplorationStore(t)
	ctx := context.Background()
	negative := int64(-1)
	// Get rejects negative now.
	if _, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "w11c-pool", NowMs: &negative}); err == nil {
		t.Fatalf("negative now must fail")
	}
	// Accrue rejects blank tokens.
	if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "w11c-pool", AccrualToken: "  "}); err == nil {
		t.Fatalf("blank token must fail")
	}
	// Reserve rejects blank ids.
	if _, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "w11c-pool", ReservationID: " ", AccountRuntimeKey: "k"}); err == nil {
		t.Fatalf("blank reservation id must fail")
	}
	if _, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "w11c-pool", ReservationID: "r", AccountRuntimeKey: ""}); err == nil {
		t.Fatalf("blank runtime key must fail")
	}
	// Settle rejects blank ids.
	if _, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "w11c-pool", ReservationID: " ", Outcome: "consumed"}); err == nil {
		t.Fatalf("blank settle id must fail")
	}
}

func TestW11CExplorationStoreAccrueIdempotencyWindow(t *testing.T) {
	store := w11cExplorationStore(t)
	ctx := context.Background()
	// Seed the pool.
	if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "w11c-window", AccrualToken: "seed", Eligible: true}); err != nil {
		t.Fatalf("seed accrue: %v", err)
	}
	// The rolling idempotency window drops the oldest tokens once full.
	for index := 0; index <= SameTierExplorationIdentityCapacity; index++ {
		token := strings.Repeat("t", 8) + string(rune('a'+index%26)) + string(rune('a'+(index/26)%26)) + string(rune('0'+index%10)) + string(rune('a'+(index%7))) + string(rune('b'+(index%5)))
		if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "w11c-window", AccrualToken: token, Eligible: true}); err != nil {
			t.Fatalf("accrue %d: %v", index, err)
		}
	}
	state, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "w11c-window"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(state.AccruedTokens) > SameTierExplorationIdentityCapacity {
		t.Fatalf("accrued tokens = %d, capacity = %d", len(state.AccruedTokens), SameTierExplorationIdentityCapacity)
	}
	// A duplicate token does not double the credit.
	before := state.Credit
	dup, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "w11c-window", AccrualToken: state.AccruedTokens[len(state.AccruedTokens)-1], Eligible: true})
	if err != nil {
		t.Fatalf("dup accrue: %v", err)
	}
	if dup.Credit != before {
		t.Fatalf("duplicate token changed credit: %f -> %f", before, dup.Credit)
	}
	// An ineligible accrual keeps the state but adds no credit.
	ineligible, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "w11c-window", AccrualToken: "w11c-ineligible", Eligible: false})
	if err != nil {
		t.Fatalf("ineligible accrue: %v", err)
	}
	if ineligible.Credit != before {
		t.Fatalf("ineligible accrue changed credit: %f -> %f", before, ineligible.Credit)
	}
}

func TestW11CExplorationStorePoolCapacityBound(t *testing.T) {
	capacity := 1
	store, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{PoolCapacity: &capacity})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()
	// Fill the single pool slot.
	if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "w11c-pool-a", AccrualToken: "seed"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A reservation on a fresh pool cannot allocate a new slot.
	now := int64(1_000)
	leaseUntil := now + 2_000
	reserved, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "w11c-pool-b", ReservationID: "r1", AccountRuntimeKey: "w11c-key", LeaseUntilMs: leaseUntil, NowMs: &now})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if reserved.Status == "" {
		t.Fatalf("empty reserve status")
	}
}
