package gatewayhotquality

import (
	"context"
	"sync"
	"testing"
)

func mustState(t *testing.T, state *SameTierExplorationState, err error) *SameTierExplorationState {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return state
}

func TestNormalizeSameTierExplorationState(t *testing.T) {
	now := int64(10_000)
	t.Run("empty state invariants", func(t *testing.T) {
		state, err := EmptySameTierExplorationState(" pool ", now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if state.PoolKey != "pool" || state.Credit != 0 || state.Cursor != 0 {
			t.Fatalf("state = %+v", state)
		}
		if state.ExpiresAtMs != now+SameTierExplorationStateTTLMS {
			t.Fatalf("expiresAtMs = %d", state.ExpiresAtMs)
		}
	})

	t.Run("expired reservations become fencing tombstones", func(t *testing.T) {
		normalized, err := NormalizeSameTierExplorationState(SameTierExplorationState{
			PoolKey: "pool",
			Credit:  1,
			Cursor:  3,
			Reservations: []SameTierExplorationReservation{
				{ReservationID: "live", AccountRuntimeKey: "acc", LeaseUntilMs: now + 1},
				{ReservationID: "dead", AccountRuntimeKey: "acc", LeaseUntilMs: now},
			},
			SettledReservationIDs: []string{"old"},
			ExpiresAtMs:           now + 100,
		}, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(normalized.Reservations) != 1 || normalized.Reservations[0].ReservationID != "live" {
			t.Fatalf("reservations = %+v", normalized.Reservations)
		}
		// settled window keeps the prior id plus the new tombstone
		if len(normalized.SettledReservationIDs) != 2 {
			t.Fatalf("settled = %v", normalized.SettledReservationIDs)
		}
		found := false
		for _, id := range normalized.SettledReservationIDs {
			if id == "dead" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expired reservation id missing tombstone: %v", normalized.SettledReservationIDs)
		}
	})

	t.Run("cooldown and credit filtering", func(t *testing.T) {
		normalized, err := NormalizeSameTierExplorationState(SameTierExplorationState{
			PoolKey:                     "pool",
			Credit:                      0.9999994999,
			Cursor:                      1,
			CooldownUntilMsByRuntimeKey: map[string]int64{"hot": now + 500, "cold": now},
			AccruedTokens:               []string{"a", "b", "a"},
			ExpiresAtMs:                 5,
		}, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, has := normalized.CooldownUntilMsByRuntimeKey["cold"]; has {
			t.Fatalf("expired cooldown kept")
		}
		if normalized.Credit != 0.999999 {
			t.Fatalf("credit = %v (must round to 6 decimals)", normalized.Credit)
		}
		if len(normalized.AccruedTokens) != 2 {
			t.Fatalf("accruedTokens = %v", normalized.AccruedTokens)
		}
		if normalized.ExpiresAtMs != now+1 {
			t.Fatalf("expiresAtMs = %d (floor now+1)", normalized.ExpiresAtMs)
		}
	})

	t.Run("identity window keeps most recent", func(t *testing.T) {
		var tokens []string
		for i := 0; i < SameTierExplorationIdentityCapacity+5; i++ {
			tokens = append(tokens, string(rune('a'+i%26))+string(rune('0'+i/26)))
		}
		normalized, err := NormalizeSameTierExplorationState(SameTierExplorationState{
			PoolKey:       "pool",
			AccruedTokens: tokens,
			ExpiresAtMs:   now + 1,
		}, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(normalized.AccruedTokens) != SameTierExplorationIdentityCapacity {
			t.Fatalf("tokens = %d", len(normalized.AccruedTokens))
		}
		// oldest entries dropped, newest kept in order
		last := normalized.AccruedTokens[len(normalized.AccruedTokens)-1]
		if last != tokens[len(tokens)-1] {
			t.Fatalf("newest token dropped: %v vs %v", last, tokens[len(tokens)-1])
		}
	})

	t.Run("validation errors", func(t *testing.T) {
		if _, err := NormalizeSameTierExplorationState(SameTierExplorationState{PoolKey: " ", ExpiresAtMs: now}, now); err == nil || err.Error() != "poolKey 必须是 1 到 512 字符" {
			t.Fatalf("err = %v", err)
		}
		if _, err := NormalizeSameTierExplorationState(SameTierExplorationState{PoolKey: "p", Credit: 1.5, ExpiresAtMs: now}, now); err == nil || err.Error() != "credit 超出范围" {
			t.Fatalf("err = %v", err)
		}
		if _, err := NormalizeSameTierExplorationState(SameTierExplorationState{PoolKey: "p", Cursor: -1, ExpiresAtMs: now}, now); err == nil || err.Error() != "cursor 必须是非负安全整数" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCloneSameTierExplorationStateIsolation(t *testing.T) {
	source := SameTierExplorationState{
		PoolKey:                     "pool",
		Credit:                      1,
		Cursor:                      2,
		Reservations:                []SameTierExplorationReservation{{ReservationID: "r", AccountRuntimeKey: "a", LeaseUntilMs: 10}},
		CooldownUntilMsByRuntimeKey: map[string]int64{"a": 5},
		AccruedTokens:               []string{"t"},
		SettledReservationIDs:       []string{"s"},
		ExpiresAtMs:                 100,
	}
	cloned := CloneSameTierExplorationState(source)
	source.Reservations[0].ReservationID = "mutated"
	source.CooldownUntilMsByRuntimeKey["a"] = 99
	source.AccruedTokens[0] = "mutated"
	source.SettledReservationIDs[0] = "mutated"
	if cloned.Reservations[0].ReservationID != "r" || cloned.CooldownUntilMsByRuntimeKey["a"] != 5 ||
		cloned.AccruedTokens[0] != "t" || cloned.SettledReservationIDs[0] != "s" {
		t.Fatalf("clone aliases source: %+v", cloned)
	}
}

func newTestExplorationMemoryStore(t *testing.T, mutate func(*MemorySameTierExplorationStoreOptions)) *MemorySameTierExplorationStore {
	t.Helper()
	options := MemorySameTierExplorationStoreOptions{Now: func() int64 { return 1_000 }}
	if mutate != nil {
		mutate(&options)
	}
	store, err := NewMemorySameTierExplorationStore(options)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return store
}

func TestMemoryExplorationStoreAccrueCreditBoundaries(t *testing.T) {
	testCases := []struct {
		name       string
		accruals   int
		wantCredit float64
	}{
		{"one accrual", 1, 0.05},
		{"ten accruals", 10, 0.5},
		{"twenty accruals hit cap", 20, 1.0},
		{"cap holds beyond twenty", 30, 1.0},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newTestExplorationMemoryStore(t, nil)
			ctx := context.Background()
			var state *SameTierExplorationState
			for i := 0; i < testCase.accruals; i++ {
				var err error
				state, err = store.Accrue(ctx, SameTierExplorationAccrueInput{
					PoolKey:      "pool",
					AccrualToken: string(rune('a'+i%26)) + string(rune('0'+i/26)),
					Eligible:     true,
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if state.Credit != testCase.wantCredit {
				t.Fatalf("credit = %v, want %v", state.Credit, testCase.wantCredit)
			}
		})
	}

	t.Run("ineligible and duplicate tokens do not accrue", func(t *testing.T) {
		store := newTestExplorationMemoryStore(t, nil)
		ctx := context.Background()
		if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "pool", AccrualToken: "t1", Eligible: false}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		state, err := store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "pool", AccrualToken: "t1", Eligible: true})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if state.Credit != 0.05 || len(state.AccruedTokens) != 1 {
			t.Fatalf("state = %+v", state)
		}
	})

	t.Run("pool capacity constructor guard", func(t *testing.T) {
		if _, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{PoolCapacity: intPtr(0)}); err == nil || err.Error() != "poolCapacity 必须是正整数" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("reserve boundary credit 0.99 refuses and 1.0 spends", func(t *testing.T) {
		store := newTestExplorationMemoryStore(t, nil)
		ctx := context.Background()
		// 19 accruals → 0.95; seed the 20th via a direct accrual set below.
		for i := 0; i < 19; i++ {
			if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{
				PoolKey: "pool", AccrualToken: string(rune('a'+i%26)) + string(rune('0'+i/26)), Eligible: true,
			}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}
		state, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "pool"})
		if err != nil || state.Credit != 0.95 {
			t.Fatalf("credit = %v, err = %v", state.Credit, err)
		}
		result, err := store.Reserve(ctx, SameTierExplorationReserveInput{
			PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc", LeaseUntilMs: 2_000,
		})
		if err != nil || result.Status != ExplorationReservationCreditUnavailable {
			t.Fatalf("0.95 credit must refuse: %+v, %v", result, err)
		}
		// push to exactly 1.0 via the twentieth accrual
		state, err = store.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "pool", AccrualToken: "final", Eligible: true})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if state.Credit != 1.0 {
			t.Fatalf("credit = %v, want 1.0", state.Credit)
		}
		result, err = store.Reserve(ctx, SameTierExplorationReserveInput{
			PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc", LeaseUntilMs: 2_000,
		})
		if err != nil || result.Status != ExplorationReservationReserved || result.Reservation == nil {
			t.Fatalf("1.0 credit must reserve: %+v, %v", result, err)
		}
	})
}

func TestMemoryExplorationStoreReserveAndSettle(t *testing.T) {
	store := newTestExplorationMemoryStore(t, nil)
	ctx := context.Background()
	// bring credit to 1.0
	for i := 0; i < 20; i++ {
		if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{
			PoolKey: "pool", AccrualToken: string(rune('a'+i%26)) + string(rune('0'+i/26)), Eligible: true,
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	lease := int64(2_000)

	t.Run("reservation lifecycle", func(t *testing.T) {
		result, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc1", LeaseUntilMs: lease})
		if err != nil || result.Status != ExplorationReservationReserved {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		// idempotent same-id re-reserve
		again, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc1", LeaseUntilMs: lease})
		if err != nil || again.Status != ExplorationReservationReserved || again.Reservation == nil {
			t.Fatalf("result = %+v, err = %v", again, err)
		}
		// same id different account → conflict
		conflict, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc2", LeaseUntilMs: lease})
		if err != nil || conflict.Status != ExplorationReservationReservationConflict || conflict.Reservation != nil {
			t.Fatalf("result = %+v, err = %v", conflict, err)
		}
		// pool busy while a reservation is live
		busy, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "pool", ReservationID: "r2", AccountRuntimeKey: "acc2", LeaseUntilMs: lease})
		if err != nil || busy.Status != ExplorationReservationPoolBusy {
			t.Fatalf("result = %+v, err = %v", busy, err)
		}
		// target cooldown after dispatched settlement
		settled, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc1", Outcome: "dispatched"})
		if err != nil || settled.Status != ExplorationSettlementApplied {
			t.Fatalf("result = %+v, err = %v", settled, err)
		}
		if settled.State.Credit != 0 || settled.State.Cursor != 1 {
			t.Fatalf("state after dispatch = %+v", settled.State)
		}
		if settled.State.CooldownUntilMsByRuntimeKey["acc1"] != 1_000+SameTierExplorationTargetCooldownMS {
			t.Fatalf("cooldown = %+v", settled.State.CooldownUntilMsByRuntimeKey)
		}
		// credit gone
		noCredit, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "pool", ReservationID: "r3", AccountRuntimeKey: "acc2", LeaseUntilMs: lease})
		if err != nil || noCredit.Status != ExplorationReservationCreditUnavailable {
			t.Fatalf("result = %+v, err = %v", noCredit, err)
		}
		// settled id cannot be reused
		reuse, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc1", LeaseUntilMs: lease})
		if err != nil || reuse.Status != ExplorationReservationReservationConflict {
			t.Fatalf("result = %+v, err = %v", reuse, err)
		}
	})

	t.Run("settle statuses", func(t *testing.T) {
		// re-arm credit and reservation
		for i := 0; i < 20; i++ {
			if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{
				PoolKey: "pool", AccrualToken: "x" + string(rune('a'+i%26)), Eligible: true,
			}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if _, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "pool", ReservationID: "r9", AccountRuntimeKey: "accX", LeaseUntilMs: lease}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		idempotent, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "pool", ReservationID: "r1", AccountRuntimeKey: "acc1", Outcome: "dispatched"})
		if err != nil || idempotent.Status != ExplorationSettlementIdempotent {
			t.Fatalf("result = %+v, err = %v", idempotent, err)
		}
		missing, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "pool", ReservationID: "nope", AccountRuntimeKey: "accX", Outcome: "not_dispatched"})
		if err != nil || missing.Status != ExplorationSettlementReservationNotFound {
			t.Fatalf("result = %+v, err = %v", missing, err)
		}
		conflict, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "pool", ReservationID: "r9", AccountRuntimeKey: "other", Outcome: "not_dispatched"})
		if err != nil || conflict.Status != ExplorationSettlementReservationConflict {
			t.Fatalf("result = %+v, err = %v", conflict, err)
		}
		// not_dispatched keeps credit and cursor
		applied, err := store.Settle(ctx, SameTierExplorationSettleInput{PoolKey: "pool", ReservationID: "r9", AccountRuntimeKey: "accX", Outcome: "not_dispatched"})
		if err != nil || applied.Status != ExplorationSettlementApplied {
			t.Fatalf("result = %+v, err = %v", applied, err)
		}
		if applied.State.Credit != 1.0 || applied.State.Cursor != 1 || len(applied.State.CooldownUntilMsByRuntimeKey) != 1 {
			t.Fatalf("state = %+v", applied.State)
		}
	})

	t.Run("target cooldown blocks new reservation", func(t *testing.T) {
		// credit available again (accX not dispatched left 1.0)
		blocked, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "pool", ReservationID: "r10", AccountRuntimeKey: "acc1", LeaseUntilMs: lease})
		if err != nil || blocked.Status != ExplorationReservationTargetCooldown {
			t.Fatalf("result = %+v, err = %v", blocked, err)
		}
		// different target is fine
		ok, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "pool", ReservationID: "r11", AccountRuntimeKey: "accFresh", LeaseUntilMs: lease})
		if err != nil || ok.Status != ExplorationReservationReserved {
			t.Fatalf("result = %+v, err = %v", ok, err)
		}
	})

	t.Run("lease validation", func(t *testing.T) {
		if _, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "pool", ReservationID: "r12", AccountRuntimeKey: "a", LeaseUntilMs: 1_000}); err == nil || err.Error() != "leaseUntilMs 必须晚于 nowMs" {
			t.Fatalf("err = %v", err)
		}
		if _, err := store.Reserve(ctx, SameTierExplorationReserveInput{PoolKey: "pool", ReservationID: "r12", AccountRuntimeKey: "a", LeaseUntilMs: 1_000 + SameTierExplorationStateTTLMS + 1}); err == nil || err.Error() != "leaseUntilMs 不得晚于 pool TTL" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestMemoryExplorationStoreExpiryAndCapacity(t *testing.T) {
	clock := int64(1_000)
	store := newTestExplorationMemoryStore(t, func(options *MemorySameTierExplorationStoreOptions) {
		options.Now = func() int64 { return clock }
	})
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{
			PoolKey: "pool", AccrualToken: string(rune('a'+i%26)) + string(rune('0'+i/26)), Eligible: true,
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	// expire the pool
	clock += SameTierExplorationStateTTLMS + 1
	state, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "pool"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Credit != 0 || state.Cursor != 0 || len(state.AccruedTokens) != 0 {
		t.Fatalf("expired pool must reset: %+v", state)
	}

	t.Run("pool capacity refusal", func(t *testing.T) {
		capacity := 1
		capacityStore, err := NewMemorySameTierExplorationStore(MemorySameTierExplorationStoreOptions{
			Now: func() int64 { return 1_000 }, PoolCapacity: &capacity,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		first, err := capacityStore.Get(ctx, SameTierExplorationGetInput{PoolKey: "p1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if first.PoolKey != "p1" {
			t.Fatalf("state = %+v", first)
		}
		// second pool cannot be created; reads fall back to an ephemeral empty state
		second, err := capacityStore.Get(ctx, SameTierExplorationGetInput{PoolKey: "p2"})
		if err != nil || second.PoolKey != "p2" || second.Credit != 0 {
			t.Fatalf("state = %+v, err = %v", second, err)
		}
		// and it is not persisted: mutating p2 must not accrue
		after, err := capacityStore.Accrue(ctx, SameTierExplorationAccrueInput{PoolKey: "p2", AccrualToken: "t", Eligible: true})
		if err != nil || after.Credit != 0 {
			t.Fatalf("capacity refusal must not accrue: %+v, %v", after, err)
		}
	})
}

func TestMemoryExplorationStoreConcurrentReserveRace(t *testing.T) {
	store := newTestExplorationMemoryStore(t, nil)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if _, err := store.Accrue(ctx, SameTierExplorationAccrueInput{
			PoolKey: "pool", AccrualToken: string(rune('a'+i%26)) + string(rune('0'+i/26)), Eligible: true,
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	const racers = 16
	var wg sync.WaitGroup
	statuses := make([]SameTierExplorationReservationStatus, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := store.Reserve(ctx, SameTierExplorationReserveInput{
				PoolKey:           "pool",
				ReservationID:     "race-" + string(rune('A'+index)),
				AccountRuntimeKey: "acc",
				LeaseUntilMs:      2_000,
			})
			if err != nil {
				statuses[index] = "error"
				return
			}
			statuses[index] = result.Status
		}(i)
	}
	wg.Wait()
	reserved := 0
	for _, status := range statuses {
		switch status {
		case ExplorationReservationReserved:
			reserved++
		case ExplorationReservationPoolBusy, ExplorationReservationCreditUnavailable, ExplorationReservationTargetCooldown:
		default:
			t.Fatalf("unexpected status %q", status)
		}
	}
	// single credit + single reservation slot → at most one winner per account
	state, err := store.Get(ctx, SameTierExplorationGetInput{PoolKey: "pool"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	liveReservations := len(state.Reservations)
	if reserved > 1 || liveReservations > 1 {
		t.Fatalf("racing reserves must not double-spend: reserved=%d live=%d", reserved, liveReservations)
	}
}
