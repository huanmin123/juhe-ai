package gatewayquota

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func TestNormalizedCost(t *testing.T) {
	tests := []struct {
		name  string
		input float64
		want  float64
	}{
		{name: "plain", input: 1.5, want: 1.5},
		{name: "rounds to 10 decimals", input: 0.123456789123, want: 0.1234567891},
		{name: "negative clamps to zero", input: -3, want: 0},
		{name: "NaN collapses to zero", input: math.NaN(), want: 0},
		{name: "positive inf collapses to zero", input: math.Inf(1), want: 0},
		{name: "negative inf collapses to zero", input: math.Inf(-1), want: 0},
		{name: "zero stays zero", input: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedCost(tt.input); got != tt.want {
				t.Fatalf("normalizedCost(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func newInflightService(t *testing.T, estimator CostEstimator, dbService DBServiceClient, apiKeys *APIKeyQuotaService, queue *manualTimerQueue, log *logRecorder) *InflightQuotaService {
	t.Helper()
	var logHook LogHook
	if log != nil {
		logHook = log.hook
	}
	service, err := NewInflightQuotaService(InflightQuotaConfig{
		APIKeys:   apiKeys,
		DBService: dbService,
		Estimator: estimator,
		Log:       logHook,
		Timer:     queue.schedule,
	})
	if err != nil {
		t.Fatalf("NewInflightQuotaService: %v", err)
	}
	return service
}

func TestInflightReserveBoundaries(t *testing.T) {
	queue := &manualTimerQueue{}
	service := newInflightService(t, nil, nil, nil, queue, nil)
	limits, err := ParseRequestQuotaLimitsJSON(`{"daily":{"enabled":true,"limit":10}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Within quota: reserve with a reservation handle.
	decision := service.Reserve(ReserveInput{
		APIKeyID: "ak", Limits: limits, CurrentCosts: RequestQuotaCosts{Daily: 5}, EstimatedCostUsd: 2,
	})
	if !decision.Allowed || !decision.HasEstimatedCostUsd || decision.EstimatedCostUsd != 2 || decision.Reservation == nil {
		t.Fatalf("within quota decision = %+v", decision)
	}
	if len(service.Snapshot()) != 1 || service.Snapshot()[0].ReservedCostUsd != 2 {
		t.Fatalf("reserved state = %+v", service.Snapshot())
	}

	// Projection including the reservation: 5 + 2 + 3 == 10 stays allowed
	// (strict > comparison), 5 + 2 + 3.01 denies.
	decision = service.Reserve(ReserveInput{
		APIKeyID: "ak", Limits: limits, CurrentCosts: RequestQuotaCosts{Daily: 5}, EstimatedCostUsd: 3,
	})
	if !decision.Allowed {
		t.Fatalf("projected exactly at limit must allow: %+v", decision)
	}
	decision = service.Reserve(ReserveInput{
		APIKeyID: "ak", Limits: limits, CurrentCosts: RequestQuotaCosts{Daily: 5}, EstimatedCostUsd: 3.01,
	})
	if decision.Allowed || !decision.HasEstimatedCostUsd || decision.EstimatedCostUsd != 3.01 {
		t.Fatalf("projected over limit must deny with estimate: %+v", decision)
	}

	// Non-positive and non-finite estimates bypass reservation entirely.
	for _, estimate := range []float64{0, -1, math.NaN()} {
		decision = service.Reserve(ReserveInput{APIKeyID: "ak2", Limits: limits, EstimatedCostUsd: estimate})
		if !decision.Allowed || decision.HasEstimatedCostUsd || decision.Reservation != nil {
			t.Fatalf("estimate %v must short-circuit allowed: %+v", estimate, decision)
		}
	}
	if len(service.Snapshot()) != 1 {
		t.Fatalf("short-circuits must not reserve: %+v", service.Snapshot())
	}
}

func TestInflightReservationLifecycle(t *testing.T) {
	queue := &manualTimerQueue{}
	service := newInflightService(t, nil, nil, nil, queue, nil)
	limits, _ := ParseRequestQuotaLimitsJSON(`{"total":{"enabled":true,"limit":100}}`)

	decision := service.Reserve(ReserveInput{APIKeyID: "ak", Limits: limits, EstimatedCostUsd: 4})
	decision.Reservation.Complete()
	// The leak timer was cancelled at Complete; only the 65s release remains.
	fired := queue.fireAll()
	if fired != 1 {
		t.Fatalf("complete must schedule exactly one release timer, fired=%d", fired)
	}
	if got := service.Snapshot(); len(got) != 0 {
		t.Fatalf("reservation must release after the delay, state=%+v", got)
	}

	// Without Complete, the 30-minute leak timer releases the reservation.
	decision = service.Reserve(ReserveInput{APIKeyID: "ak2", Limits: limits, EstimatedCostUsd: 6})
	if !decision.Allowed {
		t.Fatalf("reserve ak2: %+v", decision)
	}
	queue.fireAll()
	if got := service.Snapshot(); len(got) != 0 {
		t.Fatalf("leak timer must release, state=%+v", got)
	}

	// Over-release clamps at zero and drops the state entry.
	decision = service.Reserve(ReserveInput{APIKeyID: "ak3", Limits: limits, EstimatedCostUsd: 5})
	service.release("ak3", 9)
	if got := service.Snapshot(); len(got) != 0 {
		t.Fatalf("over-release must drop the state: %+v", got)
	}
	decision.Reservation.Complete()
	queue.fireAll()
	if got := service.Snapshot(); len(got) != 0 {
		t.Fatalf("release after clamp is a no-op: %+v", got)
	}

	// Complete is idempotent: only one release timer is scheduled. Running
	// total: ak(1 leak + 1 release) + ak2(1 leak) + ak3(1 leak + 1 release)
	// + ak4(1 leak + 1 release) = 7 scheduled timers.
	decision = service.Reserve(ReserveInput{APIKeyID: "ak4", Limits: limits, EstimatedCostUsd: 5})
	before := queue.len()
	decision.Reservation.Complete()
	decision.Reservation.Complete()
	if queue.len() != before+1 {
		t.Fatalf("complete must be idempotent, timers before=%d after=%d", before, queue.len())
	}
}

func TestInflightConcurrentReserveRelease(t *testing.T) {
	queue := &manualTimerQueue{}
	service := newInflightService(t, nil, nil, nil, queue, nil)
	limits, _ := ParseRequestQuotaLimitsJSON(`{"total":{"enabled":true,"limit":15}}`)

	const workers = 40
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			decision := service.Reserve(ReserveInput{APIKeyID: "ak", Limits: limits, EstimatedCostUsd: 1})
			if decision.Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	// Exactly 15 units fit below the strict > limit projection.
	if allowed != 15 {
		t.Fatalf("allowed = %d, want 15", allowed)
	}
	if state := service.Snapshot(); len(state) != 1 || state[0].ReservedCostUsd != 15 {
		t.Fatalf("reserved state = %+v", state)
	}

	// Releasing everything drains the state.
	service.release("ak", 15)
	if state := service.Snapshot(); len(state) != 0 {
		t.Fatalf("state must drain to empty: %+v", state)
	}
}

type stubEstimator struct {
	cost float64
	ok   bool
	got  CatalogCostInput
}

func (s *stubEstimator) EstimateCatalogCostUSD(_ context.Context, input CatalogCostInput) (float64, bool) {
	s.got = input
	return s.cost, s.ok
}

func TestEstimateGatewayRequestCostUsd(t *testing.T) {
	estimator := &stubEstimator{cost: 0.5, ok: true}
	cost, ok := EstimateGatewayRequestCostUsd(context.Background(), estimator, EstimateRequestInput{
		ProviderCode: "openai", SystemAccountID: "sys", Model: "gpt-x", RawBodyBytes: 1000,
	})
	if !ok || cost != 0.5 {
		t.Fatalf("estimate = (%v, %v)", cost, ok)
	}
	// inputTokens = max(1, ceil(1000/4)) = 250; outputTokens default 4096;
	// serviceTier default "default".
	if estimator.got.InputTokens != 250 || estimator.got.OutputTokens != DefaultEstimatedOutputTokens || estimator.got.ServiceTier != "default" {
		t.Fatalf("catalog input = %+v", estimator.got)
	}
	// Byte rounding: 1001 bytes -> ceil(250.25) = 251.
	_, _ = EstimateGatewayRequestCostUsd(context.Background(), estimator, EstimateRequestInput{RawBodyBytes: 1001})
	if estimator.got.InputTokens != 251 {
		t.Fatalf("ceil bytes/4 mismatch: %d", estimator.got.InputTokens)
	}
	// Zero bytes clamp at 1 token.
	_, _ = EstimateGatewayRequestCostUsd(context.Background(), estimator, EstimateRequestInput{RawBodyBytes: 0})
	if estimator.got.InputTokens != 1 {
		t.Fatalf("zero-byte clamp mismatch: %d", estimator.got.InputTokens)
	}
	// Explicit overrides flow through.
	_, _ = EstimateGatewayRequestCostUsd(context.Background(), estimator, EstimateRequestInput{
		RawBodyBytes: 4, ServiceTier: "flex", HasMaxOutputTokens: true, MaxOutputTokens: 128,
	})
	if estimator.got.InputTokens != 1 || estimator.got.OutputTokens != 128 || estimator.got.ServiceTier != "flex" {
		t.Fatalf("override mismatch: %+v", estimator.got)
	}
}

func TestReserveGatewayCost(t *testing.T) {
	queue := &manualTimerQueue{}
	logs := &logRecorder{}
	ctx := context.Background()
	limitsKey := APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: `{"daily":{"enabled":true,"limit":10}}`}

	t.Run("no limits allows without estimator", func(t *testing.T) {
		service := newInflightService(t, nil, nil, nil, queue, logs)
		decision, err := service.ReserveGatewayCost(ctx, GatewayReserveInput{APIKey: APIKeyRow{ID: "free"}, ProviderCode: "openai"})
		if err != nil || !decision.Allowed {
			t.Fatalf("no limits must allow: (%+v, %v)", decision, err)
		}
	})

	t.Run("disabled or zero estimate allows", func(t *testing.T) {
		estimator := &stubEstimator{cost: 0, ok: true}
		service := newInflightService(t, estimator, nil, nil, queue, logs)
		decision, err := service.ReserveGatewayCost(ctx, GatewayReserveInput{APIKey: limitsKey, ProviderCode: "openai"})
		if err != nil || !decision.Allowed {
			t.Fatalf("zero estimate must allow: (%+v, %v)", decision, err)
		}
		estimator.ok = false
		decision, err = service.ReserveGatewayCost(ctx, GatewayReserveInput{APIKey: limitsKey, ProviderCode: "openai"})
		if err != nil || !decision.Allowed {
			t.Fatalf("absent estimate must allow: (%+v, %v)", decision, err)
		}
	})

	t.Run("snapshot cost feeds the reservation", func(t *testing.T) {
		clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
		snapshot, err := NewSnapshotCache(Modes{}, nil, clock.Now, nil)
		if err != nil {
			t.Fatalf("NewSnapshotCache: %v", err)
		}
		apiKeyService, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
			Modes: Modes{}, Stats: nil, Timezone: mustTZ(t, time.UTC), Snapshot: snapshot, Now: clock.Now,
		})
		if err != nil {
			t.Fatalf("NewAPIKeyQuotaService: %v", err)
		}
		// Snapshot entry for the api key scope (daily-only window shape).
		doc := GatewayQuotaSnapshot{
			GeneratedAt: "2026-09-04T00:00:00.000Z",
			CostEntries: []QuotaCostSnapshotEntry{{
				SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak",
				Costs: RequestQuotaCosts{Daily: 9},
			}},
		}
		if err := snapshot.ReplaceGatewayQuotaSnapshot(doc); err != nil {
			t.Fatalf("replace: %v", err)
		}
		estimator := &stubEstimator{cost: 0.5, ok: true}
		service := newInflightService(t, estimator, nil, apiKeyService, queue, logs)
		// Projected daily = 9 + 0.5 = 9.5 < 10 -> allowed.
		decision, err := service.ReserveGatewayCost(ctx, GatewayReserveInput{APIKey: limitsKey, ProviderCode: "openai"})
		if err != nil || !decision.Allowed || decision.EstimatedCostUsd != 0.5 {
			t.Fatalf("snapshot-fed reserve: (%+v, %v)", decision, err)
		}
		// 9 + 0.5 reserved -> another 0.5 projects to 10 (allowed), 1 denies.
		decision, err = service.ReserveGatewayCost(ctx, GatewayReserveInput{APIKey: limitsKey, ProviderCode: "openai"})
		if err != nil || !decision.Allowed {
			t.Fatalf("second reserve at boundary: (%+v, %v)", decision, err)
		}
		estimator.cost = 1
		decision, err = service.ReserveGatewayCost(ctx, GatewayReserveInput{APIKey: limitsKey, ProviderCode: "openai"})
		if err != nil || decision.Allowed || decision.EstimatedCostUsd != 1 {
			t.Fatalf("projection over limit must deny: (%+v, %v)", decision, err)
		}
	})

	t.Run("snapshot miss with db-service failure denies protectively", func(t *testing.T) {
		logs := &logRecorder{}
		dbService := &mockDBService{readCostsErr: errors.New("ipc down")}
		service := newInflightService(t, &stubEstimator{cost: 1, ok: true}, dbService, nil, queue, logs)
		decision, err := service.ReserveGatewayCost(ctx, GatewayReserveInput{APIKey: limitsKey, ProviderCode: "openai"})
		if err != nil || decision.Allowed || !decision.HasEstimatedCostUsd || decision.EstimatedCostUsd != 1 {
			t.Fatalf("protective denial: (%+v, %v)", decision, err)
		}
		if !logs.has("gateway_api_key_inflight_quota_exact_cost_failed|") {
			t.Fatalf("expected warn event, logs=%v", logs.items)
		}
	})

	t.Run("snapshot miss with db-service costs reserves", func(t *testing.T) {
		dbService := &mockDBService{readCosts: RequestQuotaCosts{Daily: 3}}
		service := newInflightService(t, &stubEstimator{cost: 1, ok: true}, dbService, nil, queue, logs)
		decision, err := service.ReserveGatewayCost(ctx, GatewayReserveInput{APIKey: limitsKey, ProviderCode: "openai"})
		if err != nil || !decision.Allowed {
			t.Fatalf("db-service-fed reserve: (%+v, %v)", decision, err)
		}
	})
}
