package gatewayquota

// T93-1（C3-H1）+ T93-4（C3-H8）请求级在途预留测试：
//   - vendor/模型正确时按估算成本预留（API Key scope 累加）；
//   - 并发第 N 个请求被在途额度拒绝；
//   - 完成后按既有 inflight 生命周期释放（Complete → 延迟释放 / 泄漏兜底）；
//   - 无额度配置时不预留。
//   （授权/团队 scope 预留面随授权额度域退役删除。）

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fixedEstimator 返回固定估算成本（模拟目录命中）。
type fixedEstimator struct {
	cost float64
	ok   bool
}

func (e fixedEstimator) EstimateCatalogCostUSD(context.Context, CatalogCostInput) (float64, bool) {
	return e.cost, e.ok
}

func newRequestInflightReserver(t *testing.T, estimator CostEstimator, dbService DBServiceClient, queue *manualTimerQueue) *RequestInflightReserver {
	t.Helper()
	inflight := newInflightService(t, estimator, dbService, nil, queue, nil)
	return NewRequestInflightReserver(inflight, nil)
}

func requestInflightInput(limits string) RequestInflightReserveInput {
	return RequestInflightReserveInput{
		APIKey:             APIKeyRow{ID: "ak_t93", SystemAccountID: "sys_t93", QuotaLimitsJSON: limits},
		ProviderCode:       "openai",
		SystemAccountID:    "sys_t93",
		Model:              "gpt-test",
		ServiceTier:        "default",
		RawBodyBytes:       400,
		HasMaxOutputTokens: true,
		MaxOutputTokens:    128,
	}
}

func TestT93RequestInflightReservesAPIKeyScope(t *testing.T) {
	queue := &manualTimerQueue{}
	dbService := &mockDBService{readCosts: RequestQuotaCosts{Total: 1}}
	reserver := newRequestInflightReserver(t, fixedEstimator{cost: 2, ok: true}, dbService, queue)

	outcome, err := reserver.ReserveRequest(context.Background(), requestInflightInput(`{"total":{"enabled":true,"limit":10}}`))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if !outcome.Allowed || !outcome.HasEstimatedCostUsd || outcome.EstimatedCostUsd != 2 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Reservation == nil {
		t.Fatal("额度内必须先预留再放行")
	}
	if dbService.readCostsCalls != 1 {
		t.Fatalf("exact cost fallback calls = %d, want 1", dbService.readCostsCalls)
	}
	if snapshot := reserver.inflight.Snapshot(); len(snapshot) != 1 || snapshot[0].ReservedCostUsd != 2 {
		t.Fatalf("api key scope reservation = %+v", snapshot)
	}

	// Complete → 延迟释放（既有 inflight 生命周期，不新造第二套）。
	outcome.Reservation.Complete()
	queue.fireAll()
	if snapshot := reserver.inflight.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("complete must release the api key scope: %+v", snapshot)
	}
}

func TestT93RequestInflightDeniesNthConcurrentRequest(t *testing.T) {
	queue := &manualTimerQueue{}
	dbService := &mockDBService{readCosts: RequestQuotaCosts{}}
	reserver := newRequestInflightReserver(t, fixedEstimator{cost: 1, ok: true}, dbService, queue)
	input := requestInflightInput(`{"total":{"enabled":true,"limit":3}}`)

	const workers = 6
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	var deniedOutcome RequestInflightReserveOutcome
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcome, err := reserver.ReserveRequest(context.Background(), input)
			if err != nil {
				t.Errorf("reserve: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if outcome.Allowed {
				allowed++
				return
			}
			deniedOutcome = outcome
		}()
	}
	wg.Wait()
	if allowed != 3 {
		t.Fatalf("allowed = %d, want 3 (limit 3, strict > projection)", allowed)
	}
	if deniedOutcome.DeniedKind != InflightDeniedAPIKey {
		t.Fatalf("denied kind = %q, want %q", deniedOutcome.DeniedKind, InflightDeniedAPIKey)
	}
	if snapshot := reserver.inflight.Snapshot(); len(snapshot) != 1 || snapshot[0].ReservedCostUsd != 3 {
		t.Fatalf("reserved state = %+v", snapshot)
	}
}

func TestT93RequestInflightSkipsWithoutLimits(t *testing.T) {
	queue := &manualTimerQueue{}
	reserver := newRequestInflightReserver(t, fixedEstimator{cost: 2, ok: true}, &mockDBService{}, queue)

	outcome, err := reserver.ReserveRequest(context.Background(), requestInflightInput(""))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if !outcome.Allowed || outcome.Reservation != nil {
		t.Fatalf("无额度配置不得预留: %+v", outcome)
	}
	if snapshot := reserver.inflight.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("state = %+v", snapshot)
	}
}

func TestT93RequestInflightSkipsWithoutEstimator(t *testing.T) {
	queue := &manualTimerQueue{}
	reserver := newRequestInflightReserver(t, nil, &mockDBService{}, queue)
	outcome, err := reserver.ReserveRequest(context.Background(), requestInflightInput(`{"total":{"enabled":true,"limit":10}}`))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if !outcome.Allowed || outcome.Reservation != nil {
		t.Fatalf("无估算时不预留也不拒绝: %+v", outcome)
	}
}

func TestT93RequestInflightFailsClosedWhenExactCostUnavailable(t *testing.T) {
	queue := &manualTimerQueue{}
	reserver := newRequestInflightReserver(t, fixedEstimator{cost: 2, ok: true}, nil, queue)
	outcome, err := reserver.ReserveRequest(context.Background(), requestInflightInput(`{"total":{"enabled":true,"limit":10}}`))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if outcome.Allowed || outcome.DeniedKind != InflightDeniedAPIKey {
		t.Fatalf("缺少成本快照必须保护性拒绝: %+v", outcome)
	}
}

func TestT93RequestInflightExactCostErrorDeniesProtectively(t *testing.T) {
	queue := &manualTimerQueue{}
	reserver := newRequestInflightReserver(t, fixedEstimator{cost: 2, ok: true}, &mockDBService{readCostsErr: errors.New("db down")}, queue)
	outcome, err := reserver.ReserveRequest(context.Background(), requestInflightInput(`{"total":{"enabled":true,"limit":10}}`))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// 与既有 ReserveGatewayCost 同语义：精确成本读取失败按保护策略拒绝，不放行。
	if outcome.Allowed || outcome.DeniedKind != InflightDeniedAPIKey {
		t.Fatalf("精确成本读取失败必须保护性拒绝: %+v", outcome)
	}
}
