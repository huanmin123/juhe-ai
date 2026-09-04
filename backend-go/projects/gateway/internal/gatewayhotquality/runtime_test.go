package gatewayhotquality

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
)

func TestGatewayHotQualityRuntimeSingletonIdentity(t *testing.T) {
	t.Cleanup(ResetGatewayHotQualityRuntimeForTest)
	ResetGatewayHotQualityRuntimeForTest()
	ctx := context.Background()

	runtime, err := GetGatewayHotQualityRuntime(ctx, RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "memory", RedisNamespace: "dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	again, err := GetGatewayHotQualityRuntime(ctx, RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "memory", RedisNamespace: "dev"})
	if err != nil || again != runtime {
		t.Fatalf("singleton broken: %v", err)
	}
	if _, err := GetGatewayHotQualityRuntime(ctx, RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "redis"}); err == nil || err.Error() != "standalone 热质量要求 memory runtime state driver" {
		t.Fatalf("err = %v", err)
	}
	if _, err := GetGatewayHotQualityRuntime(ctx, RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "memory"}); err == nil || err.Error() != "performance 热质量要求 redis runtime state driver" {
		t.Fatalf("err = %v", err)
	}
	if _, err := GetGatewayHotQualityRuntime(ctx, RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "redis"}); err == nil || err.Error() != "performance 热质量缺少 JUHE_AI_REDIS_STATE_URL" {
		t.Fatalf("err = %v", err)
	}
	identity, identityErr := gatewayHotQualityRuntimeIdentity(RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "redis", RedisStateURL: "redis://x"})
	if identityErr != nil || !strings.HasPrefix(identity, "performance:redis:") {
		t.Fatalf("identity = %v, err = %v", identity, identityErr)
	}
	ResetGatewayHotQualityRuntimeForTest()
	if runtime2, err := GetGatewayHotQualityRuntime(ctx, RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "memory"}); err != nil || runtime2 == runtime {
		t.Fatalf("reset must rebuild the singleton")
	}
	ResetGatewayHotQualityRuntimeForTest()
}

func TestGatewayHotQualityRouteScopeKeyAndPoolKey(t *testing.T) {
	key, err := GatewayHotQualityRouteScopeKey(gatewayHotQualityRouteScopeKeyInput{
		SystemAccountID: "sys", RouteStrategyID: "  ", GroupID: "g1", ProtocolProfile: "openai:2024", RequestLane: "image",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "3:sys|6:direct|2:g1|11:openai:2024|5:image" {
		t.Fatalf("key = %s", key)
	}
	poolKey, err := SameTierExplorationPoolKey(key, "model=0|fallback=0|super=0|priority=3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPoolKey := fmt.Sprintf("%d:%s|%d:%s", len(key), key, len("model=0|fallback=0|super=0|priority=3"), "model=0|fallback=0|super=0|priority=3")
	if poolKey != wantPoolKey {
		t.Fatalf("poolKey = %s, want %s", poolKey, wantPoolKey)
	}
	if _, err := SameTierExplorationPoolKey(" ", "tier"); err == nil || err.Error() != "routeScopeKey不能为空" {
		t.Fatalf("err = %v", err)
	}
	if _, err := GatewayHotQualityRouteScopeKey(gatewayHotQualityRouteScopeKeyInput{GroupID: "g"}); err == nil || err.Error() != "systemAccountId不能为空" {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayHotQualityModelFamilyBucketing(t *testing.T) {
	known := gatewayModelFamilyFor(t, "gpt-5")
	if known == HotQualityUnknownModelFamily {
		t.Fatalf("known model must map into a bucket")
	}
	// deterministic
	if gatewayModelFamilyFor(t, "gpt-5") != known {
		t.Fatalf("bucketing not deterministic")
	}
	if gatewayModelFamilyFor(t, "  GPT-5  ") != known {
		t.Fatalf("normalization mismatch: %s vs %s", gatewayModelFamilyFor(t, "  GPT-5  "), known)
	}
	if gatewayModelFamilyForNil() != HotQualityUnknownModelFamily {
		t.Fatalf("nil model must be unknown")
	}
	long := strings.Repeat("m", 257)
	if gatewayModelFamilyFor(t, long) != HotQualityUnknownModelFamily {
		t.Fatalf("oversized model must be unknown")
	}
	control := "bad\x01model"
	if gatewayModelFamilyFor(t, control) != HotQualityUnknownModelFamily {
		t.Fatalf("control-character model must be unknown")
	}
	// bucket stays within the 256 family catalog
	if len(known) != len("model-bucket-00") {
		t.Fatalf("family = %s", known)
	}
}

type mockExplorationStore struct {
	t *testing.T

	accrueCalls   []SameTierExplorationAccrueInput
	accrueResults []*SameTierExplorationState

	reserveCalls   []SameTierExplorationReserveInput
	reserveResults []*SameTierExplorationReserveResult

	settleCalls  []SameTierExplorationSettleInput
	settleResult *SameTierExplorationSettleResult
	settleErr    error
}

func (m *mockExplorationStore) Get(ctx context.Context, input SameTierExplorationGetInput) (*SameTierExplorationState, error) {
	return &SameTierExplorationState{PoolKey: input.PoolKey}, nil
}

func (m *mockExplorationStore) Accrue(ctx context.Context, input SameTierExplorationAccrueInput) (*SameTierExplorationState, error) {
	m.accrueCalls = append(m.accrueCalls, input)
	if len(m.accrueResults) == 0 {
		return &SameTierExplorationState{PoolKey: input.PoolKey}, nil
	}
	result := m.accrueResults[0]
	m.accrueResults = m.accrueResults[1:]
	return result, nil
}

func (m *mockExplorationStore) Reserve(ctx context.Context, input SameTierExplorationReserveInput) (*SameTierExplorationReserveResult, error) {
	m.reserveCalls = append(m.reserveCalls, input)
	if len(m.reserveResults) == 0 {
		return &SameTierExplorationReserveResult{Status: ExplorationReservationReserved, State: SameTierExplorationState{PoolKey: input.PoolKey}, Reservation: &SameTierExplorationReservation{
			ReservationID:     input.ReservationID,
			AccountRuntimeKey: input.AccountRuntimeKey,
			LeaseUntilMs:      input.LeaseUntilMs,
		}}, nil
	}
	result := m.reserveResults[0]
	m.reserveResults = m.reserveResults[1:]
	return result, nil
}

func (m *mockExplorationStore) Settle(ctx context.Context, input SameTierExplorationSettleInput) (*SameTierExplorationSettleResult, error) {
	m.settleCalls = append(m.settleCalls, input)
	if m.settleErr != nil {
		return nil, m.settleErr
	}
	return m.settleResult, nil
}

func testAccount(id string, priority int) GatewayHotQualityAccountView {
	return GatewayHotQualityAccountView{
		ID: id, ProviderProtocolProfileID: "openai:2024", ProtocolCode: "openai", ProtocolVersion: "2024",
		FallbackEnabled: false, SuperPriorityEnabled: false, Priority: priority,
	}
}

func baseView(account GatewayHotQualityAccountView) GatewayHotQualityAccountView { return account }

func snapshotFor(completed int64, transportFailures int64) *HotQualitySnapshot {
	return CreateHotQualitySnapshot(HotQualitySnapshotState{
		ScopeKey:    "sk",
		Scope:       HotQualityScope{AccountRuntimeKey: "a", ProtocolProfile: "p", RequestLane: "text", ModelFamily: "unknown"},
		Buckets:     []HotQualityBucketState{{MinuteStartedAtMs: 16 * 60_000, HotQualityCounters: HotQualityCounters{CompletedResponses: completed, LocalTransportFailures: transportFailures}}},
		ExpiresAtMs: 30 * 60_000,
	}, 17*60_000)
}

func TestOrderGatewayAccountsByHotQualityEmpty(t *testing.T) {
	store := &mockHotQualityStore{t: t}
	runtime, _, _ := newTestRuntime(store, &mockExplorationStore{t: t})
	result, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]{
		Accounts:    []GatewayHotQualityAccountView{},
		Base:        baseView,
		Mode:        gatewayhybrid.HotQualityModeSpeedFirst,
		RequestLane: "text",
		NowMs:       int64Ptr(1_000_000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Accounts) != 0 || result.DispatchIntent != "primary_service" || result.ExplorationStatus != "no_candidate" {
		t.Fatalf("result = %+v", result)
	}
}

func TestOrderGatewayAccountsByHotQualityExplorationReservation(t *testing.T) {
	accA := testAccount("acc-a", 0)
	accB := testAccount("acc-b", 0)
	accounts := []GatewayHotQualityAccountView{accA, accB}
	store := &mockHotQualityStore{t: t, snapshotOf: func(scope HotQualityScope) (*HotQualitySnapshot, error) {
		if scope.AccountRuntimeKey == "acc-a" {
			return snapshotFor(10, 0), nil
		}
		return snapshotFor(0, 10), nil
	}}
	exploration := &mockExplorationStore{t: t}
	// full credit → reservation succeeds
	exploration.accrueResults = []*SameTierExplorationState{{PoolKey: "pool", Credit: 1, Cursor: 2}}
	runtime, observer, _ := newTestRuntime(store, exploration)

	result, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]{
		Accounts:                     accounts,
		Base:                         baseView,
		Mode:                         gatewayhybrid.HotQualityModeSpeedFirst,
		SystemAccountID:              "sys",
		GroupID:                      "g1",
		RequestLane:                  "text",
		RequestID:                    "req-1",
		EligibleFirstPrimaryDispatch: true,
		NowMs:                        int64Ptr(1_000_000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.DispatchIntent != "same_tier_exploration" || result.ExplorationStatus != "reserved" {
		t.Fatalf("result = %+v", result)
	}
	if result.ExplorationReservation == nil || result.ExplorationReservation.AccountRuntimeKey != result.SelectedAccountID || result.SelectedAccountID == "" {
		t.Fatalf("reservation = %+v selected = %s", result.ExplorationReservation, result.SelectedAccountID)
	}
	// the selected exploration candidate leads the ordered accounts
	if len(result.Accounts) != 2 || baseView(result.Accounts[0]).ID != result.SelectedAccountID {
		t.Fatalf("accounts = %+v selected = %s", result.Accounts, result.SelectedAccountID)
	}
	// accrual token is requestId:protocolProfile
	if len(exploration.accrueCalls) != 1 || exploration.accrueCalls[0].AccrualToken != "req-1:openai:2024" {
		t.Fatalf("accrue calls = %+v", exploration.accrueCalls)
	}
	if !exploration.accrueCalls[0].Eligible {
		t.Fatalf("eligible flag lost")
	}
	// lease = nowMs + 15s
	if exploration.reserveCalls[0].LeaseUntilMs != 1_015_000 {
		t.Fatalf("lease = %d", exploration.reserveCalls[0].LeaseUntilMs)
	}
	// settle closure settles and observes
	if err := result.SettleExplorationAfterDispatch(context.Background(), "dispatched"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if len(exploration.settleCalls) != 1 || exploration.settleCalls[0].AccountRuntimeKey != result.SelectedAccountID || exploration.settleCalls[0].Outcome != "dispatched" {
		t.Fatalf("settle calls = %+v", exploration.settleCalls)
	}
	outcomes := []string{}
	for _, event := range observer.events {
		if event.Kind == "exploration" {
			outcomes = append(outcomes, event.Outcome)
		}
	}
	if strings.Join(outcomes, ",") != "reserved,dispatched" {
		t.Fatalf("exploration outcomes = %v", outcomes)
	}
}

func TestOrderGatewayAccountsByHotQualityExplorationContended(t *testing.T) {
	accA := testAccount("acc-a", 0)
	accB := testAccount("acc-b", 0)
	store := &mockHotQualityStore{t: t, snapshotOf: func(scope HotQualityScope) (*HotQualitySnapshot, error) {
		if scope.AccountRuntimeKey == "acc-a" {
			return snapshotFor(10, 0), nil
		}
		return snapshotFor(0, 10), nil
	}}
	exploration := &mockExplorationStore{t: t}
	exploration.accrueResults = []*SameTierExplorationState{{PoolKey: "pool", Credit: 1}}
	exploration.reserveResults = []*SameTierExplorationReserveResult{{
		Status: ExplorationReservationPoolBusy,
		State:  SameTierExplorationState{PoolKey: "pool"},
	}}
	runtime, observer, _ := newTestRuntime(store, exploration)

	result, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]{
		Accounts:                     []GatewayHotQualityAccountView{accA, accB},
		Base:                         baseView,
		Mode:                         gatewayhybrid.HotQualityModeSpeedFirst,
		SystemAccountID:              "sys",
		GroupID:                      "g1",
		RequestLane:                  "text",
		RequestID:                    "req-2",
		EligibleFirstPrimaryDispatch: true,
		NowMs:                        int64Ptr(1_000_000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.DispatchIntent != "primary_service" || result.ExplorationStatus != "reservation_pool_busy" {
		t.Fatalf("result = %+v", result)
	}
	// falls back to the quality-ordered candidates (healthy account first)
	if len(result.Accounts) != 2 || baseView(result.Accounts[0]).ID != "acc-a" {
		t.Fatalf("accounts = %+v", result.Accounts)
	}
	found := false
	for _, event := range observer.events {
		if event.Kind == "exploration" && event.Outcome == "contended" {
			found = true
		}
	}
	if !found {
		t.Fatalf("contended observation missing: %+v", observer.events)
	}
	if result.SettleExplorationAfterDispatch != nil {
		t.Fatalf("contended path must not carry a settle closure")
	}
}

func TestOrderGatewayAccountsByHotQualityPrimaryWithoutCredit(t *testing.T) {
	accA := testAccount("acc-a", 0)
	store := &mockHotQualityStore{t: t, snapshotOf: func(scope HotQualityScope) (*HotQualitySnapshot, error) {
		return nil, nil
	}}
	exploration := &mockExplorationStore{t: t}
	exploration.accrueResults = []*SameTierExplorationState{{PoolKey: "pool", Credit: 0}}
	runtime, _, _ := newTestRuntime(store, exploration)

	result, err := OrderGatewayAccountsByHotQuality(context.Background(), runtime, GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]{
		Accounts:        []GatewayHotQualityAccountView{accA},
		Base:            baseView,
		Mode:            gatewayhybrid.HotQualityModeCostFirst,
		SystemAccountID: "sys",
		GroupID:         "g1",
		RequestLane:     "text",
		RequestID:       "req-3",
		NowMs:           int64Ptr(1_000_000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// exploration decision runs but has no eligible target → primary service
	if result.DispatchIntent != "primary_service" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Accounts) != 1 || baseView(result.Accounts[0]).ID != "acc-a" {
		t.Fatalf("accounts = %+v", result.Accounts)
	}
}

func TestOnceGatewayHotQualityExplorationSettlement(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	once := OnceGatewayHotQualityExplorationSettlement(func(ctx context.Context, outcome string) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	})
	if err := once(context.Background(), "dispatched"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := once(context.Background(), "not_dispatched"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}
