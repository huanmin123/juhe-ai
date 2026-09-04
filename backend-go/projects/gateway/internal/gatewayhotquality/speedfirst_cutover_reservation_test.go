package gatewayhotquality

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

type cutoverAcquireCall struct {
	accountID        string
	concurrencyLimit int
	request          AccountConcurrencyAcquireRequest
}

func TestReserveSpeedFirstCutoverTargetAcquiresFirstAvailable(t *testing.T) {
	t.Cleanup(ClearSpeedFirstCutoverReservationsForTest)
	ctx := context.Background()
	var mu sync.Mutex
	var calls []cutoverAcquireCall
	released := 0
	acquirer := func(ctx context.Context, accountID string, concurrencyLimit int, request AccountConcurrencyAcquireRequest) (AccountConcurrencySlot, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, cutoverAcquireCall{accountID, concurrencyLimit, request})
		if accountID == "credential-2" {
			return AccountConcurrencySlot{Key: "target-2", Lane: AccountConcurrencyLaneText, Release: func() { mu.Lock(); released++; mu.Unlock() }}, true, nil
		}
		return AccountConcurrencySlot{}, false, nil
	}
	targets := []GatewayAccountConcurrencyLimitIdentity{
		{ID: "target-1", ConcurrencyLimit: 5},
		{ID: "target-2", CredentialSourceAccountID: "credential-2", ConcurrencyLimit: 7},
	}
	reservation, err := ReserveSpeedFirstCutoverTarget(ctx, SpeedFirstCutoverReservationInput{
		SlowAccountID: "slow",
		Targets:       targets,
		Lane:          AccountConcurrencyLaneText,
		SlotAcquirer:  acquirer,
	})
	if err != nil || reservation == nil {
		t.Fatalf("reservation = %+v, err = %v", reservation, err)
	}
	// the first target is tried via its credential-source account key
	mu.Lock()
	if len(calls) != 2 || calls[0].accountID != "target-1" || calls[1].accountID != "credential-2" || calls[1].concurrencyLimit != 7 {
		mu.Unlock()
		t.Fatalf("calls = %+v", calls)
	}
	mu.Unlock()
	if reservation.TargetAccountID() != "target-2" {
		t.Fatalf("target = %s", reservation.TargetAccountID())
	}

	// one-shot slot transfer to the matching account only
	slot, ok := reservation.TakeForAccount(GatewayAccountConcurrencyLimitIdentity{ID: "other"})
	if ok {
		t.Fatalf("wrong account must not take the slot")
	}
	slot, ok = reservation.TakeForAccount(GatewayAccountConcurrencyLimitIdentity{ID: "target-2"})
	if !ok || slot.Key != "target-2" || slot.Lane != AccountConcurrencyLaneText {
		t.Fatalf("slot = %+v ok = %v", slot, ok)
	}
	if _, ok := reservation.TakeForAccount(GatewayAccountConcurrencyLimitIdentity{ID: "target-2"}); ok {
		t.Fatalf("slot must be single-use")
	}
	if !reservation.Consumed() {
		t.Fatalf("consumed flag must be set")
	}

	// reservation release is idempotent and shared with the transferred slot
	reservation.Release()
	reservation.Release()
	mu.Lock()
	defer mu.Unlock()
	if released != 1 {
		t.Fatalf("released = %d", released)
	}
}

func TestReserveSpeedFirstCutoverTargetImageLaneLimit(t *testing.T) {
	t.Cleanup(ClearSpeedFirstCutoverReservationsForTest)
	ctx := context.Background()
	var mu sync.Mutex
	var request AccountConcurrencyAcquireRequest
	SetSpeedFirstCutoverSlotAcquirerForTest(func(ctx context.Context, accountID string, concurrencyLimit int, req AccountConcurrencyAcquireRequest) (AccountConcurrencySlot, bool, error) {
		mu.Lock()
		request = req
		mu.Unlock()
		return AccountConcurrencySlot{Key: accountID, Lane: AccountConcurrencyLaneImage, Release: func() {}}, true, nil
	})
	reservation, err := ReserveSpeedFirstCutoverTarget(ctx, SpeedFirstCutoverReservationInput{
		Targets:               []GatewayAccountConcurrencyLimitIdentity{{ID: "t", ConcurrencyLimit: 10}},
		Lane:                  AccountConcurrencyLaneImage,
		GroupSchedulingPolicy: gatewayruntimecache.GroupSchedulingPolicy(map[string]any{"imageLaneMaxConcurrency": 3}),
	})
	if err != nil || reservation == nil {
		t.Fatalf("reservation = %+v, err = %v", reservation, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if request.Lane != AccountConcurrencyLaneImage || request.ImageLaneLimit != 3 {
		t.Fatalf("request = %+v", request)
	}
}

func TestReserveSpeedFirstCutoverTargetNoAcquisition(t *testing.T) {
	t.Cleanup(ClearSpeedFirstCutoverReservationsForTest)
	ctx := context.Background()
	acquirer := func(ctx context.Context, accountID string, concurrencyLimit int, request AccountConcurrencyAcquireRequest) (AccountConcurrencySlot, bool, error) {
		return AccountConcurrencySlot{}, false, nil
	}
	reservation, err := ReserveSpeedFirstCutoverTarget(ctx, SpeedFirstCutoverReservationInput{
		Targets:      []GatewayAccountConcurrencyLimitIdentity{{ID: "t", ConcurrencyLimit: 1}},
		Lane:         AccountConcurrencyLaneText,
		SlotAcquirer: acquirer,
	})
	if err != nil || reservation != nil {
		t.Fatalf("reservation = %+v, err = %v", reservation, err)
	}
}

func TestReserveSpeedFirstCutoverTargetAcquirerError(t *testing.T) {
	t.Cleanup(ClearSpeedFirstCutoverReservationsForTest)
	ctx := context.Background()
	released := 0
	SetSpeedFirstCutoverSlotAcquirerForTest(func(ctx context.Context, accountID string, concurrencyLimit int, request AccountConcurrencyAcquireRequest) (AccountConcurrencySlot, bool, error) {
		if accountID == "boom" {
			return AccountConcurrencySlot{}, false, errors.New("redis down")
		}
		return AccountConcurrencySlot{Key: accountID, Release: func() { released++ }}, true, nil
	})
	_, err := ReserveSpeedFirstCutoverTarget(ctx, SpeedFirstCutoverReservationInput{
		Targets: []GatewayAccountConcurrencyLimitIdentity{{ID: "boom"}},
		Lane:    AccountConcurrencyLaneText,
	})
	if err == nil || err.Error() != "redis down" {
		t.Fatalf("err = %v", err)
	}
	if released != 0 {
		t.Fatalf("failed acquisition must not own a slot: released = %d", released)
	}
}

func TestReserveSpeedFirstCutoverTargetMissingAcquirer(t *testing.T) {
	t.Cleanup(ClearSpeedFirstCutoverReservationsForTest)
	if _, err := ReserveSpeedFirstCutoverTarget(context.Background(), SpeedFirstCutoverReservationInput{
		Targets: []GatewayAccountConcurrencyLimitIdentity{{ID: "t"}},
		Lane:    AccountConcurrencyLaneText,
	}); err == nil || err.Error() != "热质量切换预留缺少并发槽获取器" {
		t.Fatalf("err = %v", err)
	}
}

func TestSpeedFirstCutoverBudgetSnapshotAlwaysEmpty(t *testing.T) {
	if snapshot := SpeedFirstCutoverBudgetSnapshot(); len(snapshot) != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestEffectiveImageLaneConcurrencyLimit(t *testing.T) {
	testCases := []struct {
		name   string
		limit  int
		policy gatewayruntimecache.GroupSchedulingPolicy
		want   int
	}{
		{"no policy uses hard limit", 10, nil, 10},
		{"zero configured uses hard limit", 10, map[string]any{"imageLaneMaxConcurrency": 0}, 10},
		{"configured caps the hard limit", 10, map[string]any{"imageLaneMaxConcurrency": 3}, 3},
		{"configured above hard limit is clamped", 5, map[string]any{"imageLaneMaxConcurrency": 20}, 5},
		{"fractional configured truncates", 10, map[string]any{"imageLaneMaxConcurrency": 2.9}, 2},
		{"invalid hard limit falls back", 0, nil, 1},
		{"oversized hard limit falls back", 2_000_000, nil, 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := EffectiveImageLaneConcurrencyLimit(testCase.limit, testCase.policy); got != testCase.want {
				t.Fatalf("limit = %d, want %d", got, testCase.want)
			}
		})
	}
}
