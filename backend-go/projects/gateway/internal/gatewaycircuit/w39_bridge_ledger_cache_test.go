package gatewaycircuit

import (
	"testing"
)

// W39（2026-10-29 生产取证）：jobs 对 CLOSED 墓碑的 retained_until 过期清理会物理
// 删除 account_circuit_incidents 行，而 gateway 进程内 ledgerRevisions 缓存仍持有
// 旧 revision。原实现只在 CASConflict 且 Incident 非 nil 时对账/回填，缓存残留使
// 后续每轮都以「期望已存在」的 revision 发起 CAS，恒 cas_conflict 直至重试耗尽
// （当日 62 条 persist_failed 全部此形态，集中于反复失败-恢复的
// deepseek-v4.1-flash scope）。契约：store 明确回报行不存在（Conflict + Incident
// nil）时必须清除本地 ledger 缓存，下一轮以 nil 期望（新行插入围栏）重试。

func TestW39ConflictWithoutStoredIncidentClearsLedgerCache(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := protocolScope("w38-cache")
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, nil)

	state := ClosedState(scope, "5", 1, "t1", 900)
	state.Phase = PhaseSuspect
	first := IncidentRecord{
		CircuitScopeKey:   MustScopeKey(scope),
		AccountRuntimeKey: scope.AccountRuntimeKey,
		ScopeKind:         ScopeKindProtocolModel,
		ProtocolCode:      strPtr(scope.ProtocolProfile),
		RequestLane:       strPtr(scope.RequestLane),
		ModelFamily:       strPtr(scope.ModelBucket),
		IncidentID:        "t1",
		State:             IncidentStateSuspect,
		Generation:        1,
		DispatchRevision:  5,
		LedgerRevision:    3,
		TransitionID:      "t1",
		UpdatedAtMs:       900,
	}
	second := first
	second.Generation = 2
	second.TransitionID = "t2"
	second.UpdatedAtMs = 1000
	second.LedgerRevision = 4

	db.casResponses = []CompareAndSetIncidentResult{
		// 第一次：正常落账，bridge 缓存 ledgerRevision=3。
		{Status: CASApplied, CurrentDispatchRevision: 5, Incident: &first},
		// 第二次：jobs 已把 CLOSED 墓碑清理掉——本地期望 3 存在、store 无行。
		{Status: CASConflict, CurrentDispatchRevision: 5, Incident: nil},
		// 清除缓存后按新行插入围栏重试，成功。
		{Status: CASApplied, CurrentDispatchRevision: 5, Incident: &second},
	}

	bridge.Observe(scope, state)
	waitForBridgeIdle(t, bridge)
	if len(db.casCalls) != 1 {
		t.Fatalf("首次 Observe cas calls = %d", len(db.casCalls))
	}

	next := ClosedState(scope, "5", 2, "t2", 1000)
	next.Phase = PhaseSuspect
	bridge.Observe(scope, next)
	waitForBridgeIdle(t, bridge)

	if len(db.casCalls) != 3 {
		t.Fatalf("清理行后的重试链应产生 3 次 CAS，实际 %d", len(db.casCalls))
	}
	if got := db.casCalls[1].ExpectedLedgerRevision; got == nil || *got != 3 {
		t.Fatalf("冲突轮应携带缓存 revision=3，实际 %v", got)
	}
	if got := db.casCalls[2].ExpectedLedgerRevision; got != nil {
		t.Fatalf("行已不存在的冲突后必须以 nil 期望重试（新行插入围栏），实际 %v", *got)
	}
}
