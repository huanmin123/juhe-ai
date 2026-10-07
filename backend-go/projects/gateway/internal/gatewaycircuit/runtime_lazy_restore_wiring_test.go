package gatewaycircuit

// 端到端装配语义回归（设计契约 docs/functions/AI账户短窗口热质量与精准切号
// 设计.md §125）：ServiceOptions 的 IsRuntimeStateReady /
// EnsureRuntimeStateReady 委托 bridge 时——
//  1. 运行态缺失 + ledger 存在 SUSPECT 行的账户，首次 PrepareAttempt 触发
//     按账户权威查询（ListByRuntimeKeys）→ restore 进运行态 → 按 SUSPECT
//     语义阻塞派发；
//  2. 后续请求走 bridge 进程内就绪短路，不再查询 ledger，仍按 SUSPECT 阻塞；
//  3. ledger 无任何 incident 的正常账户，一次权威查询后即就绪放行。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func newBridgeBackedCircuitService(t *testing.T, db *wbControlPlaneDB, store Store) *CircuitService {
	t.Helper()
	bridge := wbNewBridgeForTest(t, db, store)
	service, err := NewCircuitService(store, ServiceOptions{
		IsRuntimeStateReady: func(accountRuntimeKey string) bool {
			ready, err := bridge.IsAccountReady(accountRuntimeKey)
			return err == nil && ready
		},
		EnsureRuntimeStateReady: bridge.EnsureAccountReady,
	})
	if err != nil {
		t.Fatalf("NewCircuitService: %v", err)
	}
	return service
}

func TestPrepareAttemptRestoresAccountRuntimeStateViaBridge(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := wbNewFakeDB()
	db.byRuntimeKeys = []IncidentRecord{wbAccountIncident("acc")}
	service := newBridgeBackedCircuitService(t, db, store)
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID:                        "acc",
		DispatchRevision:          int64Ptr(1),
		ProviderProtocolProfileID: "profile",
	}
	prepare := func() PrepareResult {
		t.Helper()
		result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
			Account:                     account,
			RequestLane:                 LaneText,
			ConfirmationLeaseDurationMs: 30_000,
		})
		if err != nil {
			t.Fatalf("PrepareAttempt: %v", err)
		}
		return result
	}

	// 首次请求：ensure 从 ledger 恢复 SUSPECT 账户态并按 SUSPECT 阻塞。
	result := prepare()
	if result.Outcome != PrepareBlocked || result.State == nil || result.State.Phase != PhaseSuspect {
		t.Fatalf("first prepare = (%s, %+v), want blocked on restored SUSPECT", result.Outcome, result.State)
	}
	if db.loadKeyCalls != 1 {
		t.Fatalf("first prepare ledger account load calls = %d, want 1", db.loadKeyCalls)
	}
	// 运行态已被 restore（账户 scope 读到 SUSPECT）。
	restored, err := store.Get(context.Background(), accountScope("acc"), int64Ptr(1_000))
	if err != nil || restored.Phase != PhaseSuspect {
		t.Fatalf("restored runtime state = (phase %s, err %v), want SUSPECT", restored.Phase, err)
	}

	// 第二次请求：就绪短路，不再查询 ledger，仍按 SUSPECT 阻塞。
	result = prepare()
	if result.Outcome != PrepareBlocked || result.State == nil || result.State.Phase != PhaseSuspect {
		t.Fatalf("second prepare = (%s, %+v), want blocked", result.Outcome, result.State)
	}
	if db.loadKeyCalls != 1 {
		t.Fatalf("ready 短路后不得再次查询 ledger: %d", db.loadKeyCalls)
	}
}

func TestPrepareAttemptReadyPathForAccountWithoutIncidents(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := wbNewFakeDB()
	service := newBridgeBackedCircuitService(t, db, store)
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID:                        "plain",
		DispatchRevision:          int64Ptr(1),
		ProviderProtocolProfileID: "profile",
	}
	input := PrepareAttemptInput{
		Account:                     account,
		RequestLane:                 LaneText,
		ConfirmationLeaseDurationMs: 30_000,
	}
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := service.PrepareAttempt(context.Background(), input)
		if err != nil {
			t.Fatalf("PrepareAttempt #%d: %v", attempt, err)
		}
		if result.Outcome != PrepareDispatchable || result.Attempt == nil {
			t.Fatalf("PrepareAttempt #%d = (%s, attempt %v), want dispatchable", attempt, result.Outcome, result.Attempt != nil)
		}
	}
	// 正常账户只有首次请求触发一次按账户权威查询，之后进程内就绪短路。
	if db.loadKeyCalls != 1 {
		t.Fatalf("ledger account load calls = %d, want 1（一次权威查询后缓存就绪）", db.loadKeyCalls)
	}
}
