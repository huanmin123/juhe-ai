package gatewayquota

// T93-1（C3-H1）+ T93-4（C3-H8）请求级在途预留测试：
//   - vendor/模型正确时按估算成本预留（API Key scope 累加）；
//   - 并发第 N 个请求被在途额度拒绝；
//   - 完成后按既有 inflight 生命周期释放（Complete → 延迟释放 / 泄漏兜底）；
//   - 无额度配置时不预留；
//   - 授权/团队 scope 预留、拒绝与释放（真 PG 夹具，未授权时 SKIP）。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fixedEstimator 返回固定估算成本（模拟目录命中）。
type fixedEstimator struct {
	cost float64
	ok   bool
}

func (e fixedEstimator) EstimateCatalogCostUSD(context.Context, CatalogCostInput) (float64, bool) {
	return e.cost, e.ok
}

func newRequestInflightReserver(t *testing.T, estimator CostEstimator, dbService DBServiceClient, queue *manualTimerQueue, authz *AuthorizationQuotaService) *RequestInflightReserver {
	t.Helper()
	inflight := newInflightService(t, estimator, dbService, nil, queue, nil)
	return NewRequestInflightReserver(inflight, nil, authz)
}

func requestInflightInput(limits string) RequestInflightReserveInput {
	return RequestInflightReserveInput{
		APIKey:               APIKeyRow{ID: "ak_t93", SystemAccountID: "sys_t93", QuotaLimitsJSON: limits},
		ProviderCode:         "openai",
		SystemAccountID:      "sys_t93",
		Model:                "gpt-test",
		ServiceTier:          "default",
		RawBodyBytes:         400,
		HasMaxOutputTokens:   true,
		MaxOutputTokens:      128,
		GroupAuthorizationID: "",
	}
}

func TestT93RequestInflightReservesAPIKeyScope(t *testing.T) {
	queue := &manualTimerQueue{}
	dbService := &mockDBService{readCosts: RequestQuotaCosts{Total: 1}}
	reserver := newRequestInflightReserver(t, fixedEstimator{cost: 2, ok: true}, dbService, queue, nil)

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
	reserver := newRequestInflightReserver(t, fixedEstimator{cost: 1, ok: true}, dbService, queue, nil)
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
	reserver := newRequestInflightReserver(t, fixedEstimator{cost: 2, ok: true}, &mockDBService{}, queue, nil)

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
	reserver := newRequestInflightReserver(t, nil, &mockDBService{}, queue, nil)
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
	reserver := newRequestInflightReserver(t, fixedEstimator{cost: 2, ok: true}, nil, queue, nil)
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
	reserver := newRequestInflightReserver(t, fixedEstimator{cost: 2, ok: true}, &mockDBService{readCostsErr: errors.New("db down")}, queue, nil)
	outcome, err := reserver.ReserveRequest(context.Background(), requestInflightInput(`{"total":{"enabled":true,"limit":10}}`))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// 与既有 ReserveGatewayCost 同语义：精确成本读取失败按保护策略拒绝，不放行。
	if outcome.Allowed || outcome.DeniedKind != InflightDeniedAPIKey {
		t.Fatalf("精确成本读取失败必须保护性拒绝: %+v", outcome)
	}
}

// TestT93RequestInflightAuthorizationScopes 用真 PG 夹具（pgtestkit，未授权
// JUHE_AI_TEST_EXTERNAL=1 时 SKIP）钉住授权/团队 scope 的预留、拒绝与释放。
func TestT93RequestInflightAuthorizationScopes(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	fixture := newAuthzFixture(t, Modes{}, clock)
	ctx := context.Background()
	// 分组授权 ga1：owner sysA / grantee sysB，日额度 10。
	seedAuthzRow(t, fixture.business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")
	seedCost(t, fixture.statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 5})

	queue := &manualTimerQueue{}
	inflight := newInflightService(t, fixedEstimator{cost: 4, ok: true}, nil, nil, queue, nil)
	reserver := NewRequestInflightReserver(inflight, nil, fixture.service)

	input := RequestInflightReserveInput{
		APIKey:               APIKeyRow{ID: "ak_t93", SystemAccountID: "sysB"},
		ProviderCode:         "openai",
		SystemAccountID:      "sysB",
		Model:                "gpt-test",
		RawBodyBytes:         400,
		GroupAuthorizationID: "ga1",
		Now:                  clock.Now(),
	}
	outcome, err := reserver.ReserveRequest(ctx, input)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if !outcome.Allowed || outcome.Reservation == nil {
		t.Fatalf("授权额度内必须先预留: %+v", outcome)
	}
	scopes := inflight.ScopeSnapshot()
	if len(scopes) != 1 || scopes[0].ReservedCostUsd != 4 {
		t.Fatalf("authorization scope reservation = %+v", scopes)
	}
	if len(inflight.Snapshot()) != 0 {
		t.Fatalf("API Key 快照不得混入授权 scope: %+v", inflight.Snapshot())
	}

	// 授权 scope 释放复用同一 lifecycle。
	outcome.Reservation.Complete()
	queue.fireAll()
	if scopes := inflight.ScopeSnapshot(); len(scopes) != 0 {
		t.Fatalf("complete must release authorization scopes: %+v", scopes)
	}

	// 已结算成本 9 + 估算 4 > 10 → 拒绝，且拒绝类别为 authorization。
	if _, err := fixture.statsDB.Exec(`UPDATE juhe_stats.usage_stats_daily SET success_cost_usd = 9
		WHERE system_account_id = 'sysA' AND scope_type = 'group_authorization' AND scope_id = 'ga1'`); err != nil {
		t.Fatalf("update settled cost: %v", err)
	}
	denied, err := reserver.ReserveRequest(ctx, input)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if denied.Allowed || denied.DeniedKind != InflightDeniedAuthorization {
		t.Fatalf("authorization overage = %+v", denied)
	}
	if scopes := inflight.ScopeSnapshot(); len(scopes) != 0 {
		t.Fatalf("拒绝不得留下部分预留: %+v", scopes)
	}
}
