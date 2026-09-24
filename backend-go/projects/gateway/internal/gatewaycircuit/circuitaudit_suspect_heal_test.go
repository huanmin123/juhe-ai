package gatewaycircuit

// circuitaudit_suspect_heal_test.go — R2 验收：SUSPECT 不被失败 HTTP 响应
// "治愈"，成功响应仍推进恢复（原复现：失败响应治愈 SUSPECT）。
//
// 已修复的缺陷：dispatch 引擎对一次拿到 confirmation 租约的尝试，只要收到
// 完整的失败 HTTP 响应（4xx/5xx，Action==SkipAccount 分支），原会调用
// Attempt.ReportFramingComplete 结算 confirmation：
//
//	projects/gateway/internal/gatewaydispatch/attemptoutcomes.go（修复前）
//	    if in.accountCircuitAttempt != nil {
//	        _, _ = in.accountCircuitAttempt.ReportFramingComplete(ctx)
//	    }
//
// 而 store 侧 complete_confirmation 的语义是（circuitstate/lua.go:464-467、
// store_memory.go:298-303）：outcome=="framing_complete" 且 disposition 非
// "closed" 时一律 enter_recovering —— 不校验响应是否成功。结果：一个返回
// 429/500 的账号，其 SUSPECT 状态被它自己的失败响应"确认恢复"，推进到
// RECOVERING（失败证据清零、回到 canary 正轨），熔断计数被失败上游反向
// 重置；observer 尝试更被失败响应直接关成 CLOSED。
//
// 修复：dispatch 失败分支改调 ReportUnknown（unknown 结算：租约立即释放、
// 保持 SUSPECT、不新增失败证据、退避后再认领——lua.go:495-502 /
// store_memory.go:304-315，两侧一致）；ReportFramingComplete 只保留给成功
// 分支（response.OK()，真成功 → RECOVERING，原 API 语义在该位置正确）。
//
// 本测试用 memory store + 注入假时钟走完整 service 流程验证两个方向：
//
//  1. 失败方向：confirmation 尝试调 ReportUnknown（即修复后失败分支的调用）
//     → phase 仍 SUSPECT、confirmation 租约立即释放（state.Lease==nil，
//     不等 30s leaseUntil；退避 retryAt=+3s 到期后下一次 PrepareAttempt
//     能立即重新认领 confirmation）、failure evidence 计数不增；
//  2. 成功方向：confirmation 尝试调 ReportFramingComplete（即修复后成功
//     分支的调用）→ phase RECOVERING（保住原设计意图）。

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// gatewayruntimecacheAccountForCircuitAudit 构造一个最小 OpenAIAccountSecret：
// DispatchRevision 为 nil 时 AccountCircuitDispatchRevision 走凭据摘要派生
//（service.go:1161-1188），同一账号确定性产出同一 revision。
func gatewayruntimecacheAccountForCircuitAudit(id string) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:              id,
		ProviderCode:    "openai",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		Type:            "api_key",
		Status:          "active",
		APIKey:          "sk-circuit-audit-" + id,
	}
}

// suspectWithConfirmation 驱动到"SUSPECT 到期并持有 confirmation 租约"的
// 状态，返回 confirmation 尝试句柄。scope 与 PrepareAttempt 内部一致
//（GatewayAccountProtocolModelScope：账号 × 协议桶 × 模型）。
func suspectWithConfirmation(t *testing.T, service *CircuitService, store *MemoryStore, account gatewayruntimecache.OpenAIAccountSecret, model string, clock *int64) (*Attempt, Scope) {
	t.Helper()
	ctx := context.Background()

	scope, err := GatewayAccountProtocolModelScope(account, LaneText, &model)
	if err != nil {
		t.Fatalf("GatewayAccountProtocolModelScope: %v", err)
	}

	prepare, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account:                     account,
		RequestLane:                 LaneText,
		Model:                       &model,
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil || prepare.Outcome != PrepareDispatchable || prepare.Attempt == nil {
		t.Fatalf("第一次 PrepareAttempt = (%s, %v)", prepare.Outcome, err)
	}
	if prepare.Attempt.IsConfirmation() {
		t.Fatal("CLOSED 账号的第一次尝试不应携带 confirmation 租约")
	}
	decision, err := prepare.Attempt.ReportTransportFailure(ctx, TransportFailure{
		Kind:   TransportFailureKindTransport,
		Reason: "circuit-audit: connection reset by peer",
	})
	if err != nil {
		t.Fatalf("ReportTransportFailure: %v", err)
	}
	if decision.Outcome != DecisionSuspected || decision.State.Phase != PhaseSuspect {
		t.Fatalf("transport 失败后 decision = (%s, %s), want (suspected, SUSPECT)", decision.Outcome, decision.State.Phase)
	}

	// SUSPECT 进入形状（store_memory.go Suspect）：evidence 恰 1 条、失败
	// 计数 0、retryAt = now + 3000（AccountCircuitSuspectConfirmationIntervalMs）。
	suspectState, err := store.Get(ctx, scope, nil)
	if err != nil {
		t.Fatalf("store.Get(SUSPECT): %v", err)
	}
	if suspectState.Phase != PhaseSuspect {
		t.Fatalf("store phase = %s, want SUSPECT", suspectState.Phase)
	}
	if len(suspectState.FailureEvidenceKeys) != 1 || suspectState.ConfirmationFailureCount == nil || *suspectState.ConfirmationFailureCount != 0 {
		t.Fatalf("SUSPECT 进入形状: evidence=%d count=%v, want (1, 0)",
			len(suspectState.FailureEvidenceKeys), suspectState.ConfirmationFailureCount)
	}

	// 假时钟推进越过 retryAt（默认 Settings 的 confirmation 间隔 3000ms），
	// 再认领 confirmation（第二个独立请求，failure evidence 必须不同于首次
	// 失败）。
	*clock += 3_001
	confirmationEvidence := strings.Repeat("a", 64)
	confirmPrepare, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account:                     account,
		RequestLane:                 LaneText,
		Model:                       &model,
		ConfirmationLeaseDurationMs: 30_000,
		FailureEvidenceKey:          &confirmationEvidence,
	})
	if err != nil || confirmPrepare.Outcome != PrepareDispatchable || confirmPrepare.Attempt == nil {
		t.Fatalf("confirmation PrepareAttempt = (%s, %v)", confirmPrepare.Outcome, err)
	}
	if !confirmPrepare.Attempt.IsConfirmation() {
		t.Fatal("到期 SUSPECT 的新尝试必须携带 confirmation 租约")
	}
	return confirmPrepare.Attempt, scope
}

func TestCircuitAuditR2FailedHTTPResponseUnknownKeepsSuspectAndReleasesLease(t *testing.T) {
	clock := int64(2_000_000)
	now := func() int64 { return clock }
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: now})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	service, err := NewCircuitService(store, ServiceOptions{Now: now})
	if err != nil {
		t.Fatalf("NewCircuitService: %v", err)
	}
	ctx := context.Background()

	account := gatewayruntimecacheAccountForCircuitAudit("circuit-audit-heal-acct")
	model := "gpt-test"
	confirmation, scope := suspectWithConfirmation(t, service, store, account, model, &clock)

	// 步骤 3：对该 confirmation 句柄调 ReportUnknown —— 这是修复后 dispatch
	// 层收到 HTTP 4xx/5xx 失败响应（SkipAccount 分支）时的调用。
	result, err := confirmation.ReportUnknown(ctx)
	if err != nil {
		t.Fatalf("ReportUnknown: %v", err)
	}
	if result == nil {
		t.Fatal("confirmation 尝试的 ReportUnknown 必须产生结算结果")
	}

	// 契约 2：状态不进入 RECOVERING/CLOSED —— 保持 SUSPECT。
	if result.State.Phase != PhaseSuspect {
		t.Fatalf("失败响应结算后 phase = %s, want SUSPECT（失败响应不得治愈熔断）", result.State.Phase)
	}
	// 契约 1：confirmation 租约立即释放（不等 30s leaseUntil）。
	if result.State.Lease != nil {
		t.Fatalf("失败响应结算后 lease = %+v, want nil（unknown 结算必须立即释放租约）", result.State.Lease)
	}
	healed, err := store.Get(ctx, scope, nil)
	if err != nil {
		t.Fatalf("store.Get(结算后): %v", err)
	}
	if healed.Phase != PhaseSuspect {
		t.Fatalf("结算后 store phase = %s, want SUSPECT", healed.Phase)
	}
	if healed.Lease != nil {
		t.Fatalf("结算后 store lease = %+v, want nil", healed.Lease)
	}
	// 契约 3：不新增 OPEN 方向的失败证据计数 —— evidence 保持 SUSPECT 进入
	// 时的 1 条（首次 transport 失败），confirmation 失败计数保持 0。
	if len(healed.FailureEvidenceKeys) != 1 {
		t.Fatalf("结算后 failureEvidenceKeys = %d, want 1（unknown 结算不得新增失败证据）",
			len(healed.FailureEvidenceKeys))
	}
	if healed.ConfirmationFailureCount == nil || *healed.ConfirmationFailureCount != 0 {
		t.Fatalf("结算后 confirmationFailureCount = %v, want 0", healed.ConfirmationFailureCount)
	}

	// 契约 1 的可观察验收：unknown 结算的退避（backoffAttempt=1 → 精确
	// +3000ms，jitter.go index<4 无抖动）到期后，下一次 PrepareAttempt 能
	// 立即重新认领 confirmation —— 租约不再像原成功侧缺失结算那样挂到
	// 30s leaseUntil 过期。
	clock += 3_001
	nextEvidence := strings.Repeat("b", 64)
	retryPrepare, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account:                     account,
		RequestLane:                 LaneText,
		Model:                       &model,
		ConfirmationLeaseDurationMs: 30_000,
		FailureEvidenceKey:          &nextEvidence,
	})
	if err != nil {
		t.Fatalf("重新 PrepareAttempt: %v", err)
	}
	if retryPrepare.Outcome != PrepareDispatchable || retryPrepare.Attempt == nil || !retryPrepare.Attempt.IsConfirmation() {
		t.Fatalf("退避到期后重新 PrepareAttempt = (%s, confirmation=%v), want 立即重新认领 confirmation",
			retryPrepare.Outcome, retryPrepare.Attempt != nil && retryPrepare.Attempt.IsConfirmation())
	}
}

func TestCircuitAuditR2SuccessfulResponseFramingCompleteStillRecovers(t *testing.T) {
	clock := int64(4_000_000)
	now := func() int64 { return clock }
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: now})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	service, err := NewCircuitService(store, ServiceOptions{Now: now})
	if err != nil {
		t.Fatalf("NewCircuitService: %v", err)
	}
	ctx := context.Background()

	account := gatewayruntimecacheAccountForCircuitAudit("circuit-audit-success-acct")
	model := "gpt-test"
	confirmation, scope := suspectWithConfirmation(t, service, store, account, model, &clock)

	// 对 confirmation 句柄调 ReportFramingComplete —— 修复后这只来自成功
	// 分支（response.OK()），语义是真成功确认恢复。
	result, err := confirmation.ReportFramingComplete(ctx)
	if err != nil {
		t.Fatalf("ReportFramingComplete: %v", err)
	}
	if result == nil {
		t.Fatal("confirmation 尝试的 ReportFramingComplete 必须产生结算结果")
	}

	// 原设计意图保留：真成功 → RECOVERING（判定依据：complete_confirmation
	// 的 framing_complete 分支，circuitstate/lua.go:464-467 /
	// store_memory.go:298-303）。
	if result.State.Phase != PhaseRecovering {
		t.Fatalf("成功结算后 phase = %s, want RECOVERING（真成功确认恢复的原设计意图）", result.State.Phase)
	}
	recovered, err := store.Get(ctx, scope, nil)
	if err != nil {
		t.Fatalf("store.Get(成功结算后): %v", err)
	}
	if recovered.Phase != PhaseRecovering {
		t.Fatalf("成功结算后 store phase = %s, want RECOVERING", recovered.Phase)
	}
	if recovered.Lease != nil {
		t.Fatalf("成功结算后 lease = %+v, want nil（framing_complete 同样立即结算，不再挂 30s 租约）", recovered.Lease)
	}
}
