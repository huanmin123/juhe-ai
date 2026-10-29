package gatewaycircuit

import (
	"context"
	"strings"
	"testing"
)

// W36（2026-10-28 生产取证定案）：CompleteConfirmation 的 transport 失败升级
// 记录在账户态缺席的各返回臂（not_found/stale_*/state_mismatch/recorded）会把
// store 层 closedAccountState 占位（transitionId 与 incidentId 均空）当
// AccountState 交给持久化桥，真实 store 的 CAS 输入校验恒拒
// 「incident id is required」并被桥的重试循环空转至耗尽（2026-10-28 上游失败
// 风暴中单日 1324 条「持久化重试耗尽」告警即此形态）。守卫契约：
// service 层升级事件不得外泄无持久化身份的占位状态；账户态真实存在（含升级
// 新开）时事件照常发射。

func w36DriveTransportFailureConfirmations(t *testing.T, scopeCount int) []MutationEvent {
	now := int64(0)
	clock := &now
	store, _ := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	var mutations []MutationEvent
	service, _ := newTestService(t, store, ServiceOptions{
		Now: func() int64 { return *clock },
		OnMutation: func(_ context.Context, event MutationEvent) error {
			mutations = append(mutations, event)
			return nil
		},
	})
	ctx := context.Background()
	account := testAccount()
	revision := revisionOf(t, account)

	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: protocolModelScope(account, LaneText, strPtr("gpt-4o")), dispatchRevision: revision,
		confirmationFailuresRequired: int64Ptr(2), reason: "transport:down",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	models := []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-3.5-turbo"}
	for index := 0; index < scopeCount; index++ {
		model := strPtr(models[index%len(models)])
		scope := protocolModelScope(account, LaneText, model)
		if index > 0 {
			if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
				scope: scope, dispatchRevision: revision,
				confirmationFailuresRequired: int64Ptr(2), reason: "transport:down",
			}); err != nil {
				t.Fatalf("suspect %d: %v", index, err)
			}
		}
		*clock = int64(10_000*(index+1)) + int64(index)
		result, err := service.PrepareAttempt(ctx, w11cPrepareInput(account, model, strings.Repeat("a", 64)))
		if err != nil {
			t.Fatalf("prepare %d: %v", index, err)
		}
		if result.Outcome != PrepareDispatchable || !result.Attempt.IsConfirmation() {
			t.Fatalf("prepare %d = (%s, confirmation=%v)", index, result.Outcome, result.Attempt.IsConfirmation())
		}
		decision, err := result.Attempt.ReportTransportFailure(ctx, TransportFailure{Kind: TransportFailureKindTransport, Reason: "w36 upstream down"})
		if err != nil {
			t.Fatalf("report %d: %v", index, err)
		}
		if decision.Outcome != DecisionBlocked {
			t.Fatalf("report %d outcome = %s", index, decision.Outcome)
		}
	}
	return mutations
}

// recorded 臂（低于 distinct scope 阈值且账户态缺席）：占位状态不得外泄。
func TestW36EscalationRecordedArmDropsPlaceholderAccountEvent(t *testing.T) {
	mutations := w36DriveTransportFailureConfirmations(t, 1)
	for _, event := range mutations {
		if event.Operation == OperationRecordParentEvidence && durableIncidentID(event.State) == "" {
			t.Fatalf("占位账户状态外泄到持久化桥: %+v", event)
		}
	}
}

// escalated 臂（达到 distinct scope 阈值、账户态由升级真实创建）：账户态
// 必须带持久化身份（AccountTransitionID 流入 TransitionID）——service 层守卫
// 依赖该形态放行升级事件，不得误伤升级新开。
func TestW36EscalationEscalatedAccountStateCarriesIdentity(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	clock := &now
	store := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.Now = func() int64 { return *clock } })

	protocols := []Scope{
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "w36-esc", ProtocolProfile: "profile-1", RequestLane: LaneText, ModelBucket: "gpt"},
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "w36-esc", ProtocolProfile: "profile-2", RequestLane: LaneText, ModelBucket: "gpt"},
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "w36-esc", ProtocolProfile: "profile-3", RequestLane: LaneText, ModelBucket: "gpt"},
	}
	for index, protocol := range protocols {
		w14eRestoreOpen(t, store, protocol, "5", "p-open-"+string(rune('1'+index)), clock)
	}
	input := func(protocol Scope, evidenceID, accountTransitionID string) ProtocolModelOpenEvidenceInput {
		return ProtocolModelOpenEvidenceInput{
			Scope: protocol, Generation: 1, DispatchRevision: "5", EvidenceID: evidenceID,
			AccountTransitionID: accountTransitionID, Reason: "w36 escalation",
			ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 8, NowMs: clock,
		}
	}
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, input(protocols[0], "ev-a", "")); err != nil {
		t.Fatalf("first evidence: %v", err)
	}
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, input(protocols[1], "ev-b", "")); err != nil {
		t.Fatalf("second evidence: %v", err)
	}
	escalated, err := store.RecordProtocolModelOpenEvidence(ctx, input(protocols[2], "ev-c", "w36-account-transition"))
	if err != nil || escalated.Status != EscalationEscalated {
		t.Fatalf("escalation = (%s, %v)", escalated.Status, err)
	}
	if escalated.AccountState.TransitionID != "w36-account-transition" {
		t.Fatalf("升级账户态 TransitionID = %q, want w36-account-transition（守卫依赖该身份放行）", escalated.AccountState.TransitionID)
	}
	if escalated.AccountState.Phase != PhaseOpen {
		t.Fatalf("升级账户态 phase = %s", escalated.AccountState.Phase)
	}
}
