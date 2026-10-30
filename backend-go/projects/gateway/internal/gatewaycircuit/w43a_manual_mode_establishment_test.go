package gatewaycircuit

import (
	"context"
	"testing"
)

// W43-A（后台自动探测总开关 W3）：escalation/incident 建立面与既有恢复入口的
// 手动模式开关位行为。契约源 docs/functions/AI账户后台自动探测总开关设计.md §3：
// 手动账户不建 SUSPECT incident；开关读取失败失败关闭（2026-10-30 复审修订：
// 读失败按手动模式跳过建立，避免给手动账户建出无恢复探针的滞留 incident）；
// 恢复入口按 skipped 计数。

// 手动账户（开关关闭）：SuspectForegroundFailure 返回 observer_neutral，store
// 账户级与 protocol-model 级均不建立 incident（保持 CLOSED）。
func TestW43AManualAccountSuspectForegroundFailureSkipsIncident(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	calls := 0
	service, _ := newTestService(t, store, ServiceOptions{
		BackgroundProbeDisabled: func(ctx context.Context, accountRuntimeKey string) (bool, error) {
			calls++
			return true, nil
		},
	})
	scope := protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o"))
	decision, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope:                        scope,
		dispatchRevision:             revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason:                       "transport:connect failed",
	})
	if err != nil {
		t.Fatalf("suspect: %v", err)
	}
	if calls != 1 {
		t.Fatalf("开关谓词调用数 = %d，期望 1（每条 transport 失败恰好一次）", calls)
	}
	if decision.Outcome != DecisionObserverNeutral {
		t.Fatalf("手动账户 decision.Outcome = %q，期望 %q", decision.Outcome, DecisionObserverNeutral)
	}
	accountState, err := store.Get(context.Background(), Scope{Kind: ScopeKindAccount, AccountRuntimeKey: scope.AccountRuntimeKey}, nowPtr(0))
	if err != nil {
		t.Fatal(err)
	}
	if accountState.Phase != PhaseClosed {
		t.Fatalf("账户级 phase = %q，期望 CLOSED（不建 incident）", accountState.Phase)
	}
	scopeState, err := store.Get(context.Background(), scope, nowPtr(0))
	if err != nil {
		t.Fatal(err)
	}
	if scopeState.Phase != PhaseClosed {
		t.Fatalf("protocol-model 级 phase = %q，期望 CLOSED（不建 incident）", scopeState.Phase)
	}
}

// 开关开启（默认路径）：同一入口照常建立 SUSPECT（flag=1 行为逐字节不变）。
func TestW43AEnabledAccountSuspectForegroundFailureStillEstablishes(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{
		BackgroundProbeDisabled: func(ctx context.Context, accountRuntimeKey string) (bool, error) {
			return false, nil
		},
	})
	scope := protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o"))
	decision, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope:                        scope,
		dispatchRevision:             revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason:                       "transport:connect failed",
	})
	if err != nil {
		t.Fatalf("suspect: %v", err)
	}
	if decision.Outcome != DecisionSuspected {
		t.Fatalf("默认路径 decision.Outcome = %q，期望 %q", decision.Outcome, DecisionSuspected)
	}
}

// 开关读取失败失败关闭（2026-10-30 复审修订）：读失败按手动模式跳过建立，
// 不给账户建出无恢复探针的 incident；不因单次读开关失败上抛影响请求失败结算。
func TestW43ASwitchLookupFailureFailsClosed(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{
		BackgroundProbeDisabled: func(ctx context.Context, accountRuntimeKey string) (bool, error) {
			return false, context.Canceled
		},
	})
	scope := protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o"))
	decision, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope:                        scope,
		dispatchRevision:             revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason:                       "transport:connect failed",
	})
	if err != nil {
		t.Fatalf("开关读取失败必须失败关闭不上抛: %v", err)
	}
	if decision.Outcome != DecisionObserverNeutral {
		t.Fatalf("fail-closed decision.Outcome = %q，期望 %q", decision.Outcome, DecisionObserverNeutral)
	}
	accountState, err := store.Get(context.Background(), Scope{Kind: ScopeKindAccount, AccountRuntimeKey: scope.AccountRuntimeKey}, nowPtr(0))
	if err != nil {
		t.Fatal(err)
	}
	if accountState.Phase != PhaseClosed {
		t.Fatalf("读取失败窗口 phase = %q，期望 CLOSED（不建 incident）", accountState.Phase)
	}
}

// 既有恢复入口开关位：手动账户的到期状态跳过租约获取与探针，按 skipped 计数。
func TestW43AManualAccountRecoveryEntrySkipsProbe(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	scope := protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o"))
	dispatchRevision := revisionOf(t, testAccount())
	service, _ := newTestService(t, store, ServiceOptions{})
	// 先建立一条 SUSPECT（模拟开关翻转前的残留 incident）。
	if _, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope:            scope,
		dispatchRevision: dispatchRevision,
		reason:           "transport:connect failed",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	due, err := store.ListDue(context.Background(), 3_000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) == 0 {
		t.Fatal("夹具前置失败：SUSPECT 状态未进入到期列表")
	}

	probes := 0
	// SUSPECT 的 RetryAtMs = 0 + 3000（suspect 确认间隔默认 3000），sweep 时钟
	// 必须取 3000 才能扫到到期项。
	now := int64(3_000)
	resolver := func(ctx context.Context, state State) (RecoveryProbeTarget, bool, error) {
		probes++
		return RecoveryProbeTarget{DispatchRevision: dispatchRevision, Probe: func(ctx context.Context) (TransportProbeOutcome, error) {
			return TransportProbeOutcome{Kind: RecoveryProbeOutcomeFramingComplete}, nil
		}}, true, nil
	}
	manualService, err := NewRecoveryService(store, resolver, RecoveryServiceOptions{
		NowMS:   func() int64 { return now },
		BatchSize: 10,
		Concurrency: 1,
		BackgroundProbeDisabled: func(ctx context.Context, accountRuntimeKey string) (bool, error) {
			if accountRuntimeKey == "" {
				t.Fatalf("恢复入口开关位收到空 accountRuntimeKey")
			}
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, sweepErr := manualService.Sweep(context.Background())
	if sweepErr != nil {
		t.Fatalf("sweep: %v", sweepErr)
	}
	if probes != 0 {
		t.Fatalf("手动账户恢复探针执行数 = %d，期望 0", probes)
	}
	if result.SkippedCount != len(due) {
		t.Fatalf("skipped=%d 期望 %d（全部到期项按 skipped 计数，不领租约）", result.SkippedCount, len(due))
	}
	if result.LeasedCount != 0 {
		t.Fatalf("leased=%d 期望 0（跳过发生在租约获取之前）", result.LeasedCount)
	}
}
