package main

// 账户电路运行态就绪装配测试（设计契约 docs/functions/AI账户短窗口热质量与
// 精准切号设计.md §125 的装配收口）：persist 配置齐备时 ServiceOptions 必须
// 挂上 IsRuntimeStateReady / EnsureRuntimeStateReady 双 hook 并委托 bridge
// （ledger → 运行态懒恢复）；配置不齐（persist hook nil）时两个 hook 保持
// nil，行为与既有"恒 ready"完全一致。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
)

// TestChainAccountCircuitServiceOptionsRuntimeReadyHooks 覆盖装配两侧：
// gate 未就绪 → 三 hook 全 nil（不 fail-closed）；gate 就绪 → 三 hook 非 nil
// 且委托 bridge——冷启动未就绪，ensure 经 ledger 按账户权威查询后标记就绪，
// 并把 ledger 中的 SUSPECT 行 restore 进同一运行态 store。
func TestChainAccountCircuitServiceOptionsRuntimeReadyHooks(t *testing.T) {
	store, err := gatewaycircuit.NewMemoryStore(gatewaycircuit.MemoryStoreOptions{Capacity: 8})
	if err != nil {
		t.Fatalf("create memory store: %v", err)
	}
	// gate 未就绪（零配置）：OnMutation 与双 hook 全部保持 nil。
	options, closeOptions, hookErr := newChainAccountCircuitServiceOptions(store, chainAccountCircuitPersistConfig{}, nil)
	if hookErr != nil {
		t.Fatalf("zero config options: %v", hookErr)
	}
	defer closeOptions()
	if options.OnMutation != nil || options.IsRuntimeStateReady != nil || options.EnsureRuntimeStateReady != nil {
		t.Fatalf("gate 未就绪不得挂任何 hook: onMutation=%v isReady=%v ensure=%v",
			options.OnMutation != nil, options.IsRuntimeStateReady != nil, options.EnsureRuntimeStateReady != nil)
	}

	// gate 就绪：预置 ledger SUSPECT 行（acc / dispatch_revision=1，与
	// w17eOpenBusinessSQLite 预置的 accounts 行对齐 ListByRuntimeKeys 的
	// dispatch revision 围栏）。
	db := w17eOpenBusinessSQLite(t)
	nowMs := time.Now().UnixMilli()
	scope := gatewaycircuit.Scope{Kind: gatewaycircuit.ScopeKindAccount, AccountRuntimeKey: "acc"}
	if _, err := db.Exec(`INSERT INTO account_circuit_incidents (
		circuit_scope_key, account_id, account_runtime_key, scope_kind, incident_id,
		child_incident_ids_json, state, generation, dispatch_revision, ledger_revision,
		projected_ledger_revision, transition_id, cooldown_observation_generation,
		upstream_attempt_observed, backoff_level, consecutive_failures,
		confirmation_failures_required, confirmation_failure_evidence_keys_json,
		recovering_successes, next_transition_at_ms, created_at_ms, updated_at_ms)
		VALUES (?, 'acc', 'acc', 'account', 'inc-seed', '[]', 'SUSPECT', 1, 1, 1,
		0, 't-seed', 0, 0, 0, 1, 2, '[]', 0, ?, ?, ?)`,
		gatewaycircuit.MustScopeKey(scope), nowMs+int64((10*time.Minute)/time.Millisecond), nowMs, nowMs); err != nil {
		t.Fatalf("seed suspect incident: %v", err)
	}
	options, closeOptions, hookErr = newChainAccountCircuitServiceOptions(store, w17eGateReadyConfig(db), nil)
	if hookErr != nil {
		t.Fatalf("gate ready options: %v", hookErr)
	}
	defer closeOptions()
	if options.OnMutation == nil || options.IsRuntimeStateReady == nil || options.EnsureRuntimeStateReady == nil {
		t.Fatalf("gate 就绪必须挂齐三 hook: onMutation=%v isReady=%v ensure=%v",
			options.OnMutation != nil, options.IsRuntimeStateReady != nil, options.EnsureRuntimeStateReady != nil)
	}
	if options.IsRuntimeStateReady("acc") {
		t.Fatal("冷启动（未重建/未加载）账户不得就绪")
	}
	ctx := context.Background()
	ready, ensureErr := options.EnsureRuntimeStateReady(ctx, "acc")
	if ensureErr != nil || !ready {
		t.Fatalf("EnsureRuntimeStateReady = (%v, %v), want ready", ready, ensureErr)
	}
	if !options.IsRuntimeStateReady("acc") {
		t.Fatal("账户加载后必须就绪")
	}
	// ledger 的 SUSPECT 行已 restore 进传入的同一运行态 store。
	state, getErr := store.Get(ctx, scope, int64PtrOf(nowMs))
	if getErr != nil {
		t.Fatalf("store get: %v", getErr)
	}
	if state.Phase != gatewaycircuit.PhaseSuspect {
		t.Fatalf("restored phase = %s, want SUSPECT", state.Phase)
	}
}
