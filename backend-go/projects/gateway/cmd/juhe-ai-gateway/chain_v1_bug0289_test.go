package main

// BUG-0289 回归测试：chain 派发循环对 codex 加密上下文流内恢复 verdict 的消费
// ——恢复臂设置一次性 pending 状态、下一轮 newRequestCoordination 注入
// RequestBodyOverride / SameAccountRetry / SemanticRetryID 后清空；恢复轮在
// post-verdict 结算分类中保持中性（不得记成 circuit ReportFramingComplete
// 治愈证据）；耗尽兜底 reason 命名齐备。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
)

const bug0289RecoveredBody = `{"model":"gpt-test","input":"cleaned"}`
const bug0289SemanticRetryID = "codex_encrypted_content_cleanup:encrypted_context_invalid"

func bug0289RecoveryHandling() gatewayresponse.UpstreamResponseHandlingResult {
	return gatewayresponse.UpstreamResponseHandlingResult{
		RetryUpstream:               true,
		RetryReason:                 gatewayresponse.StreamServerRetryCodexEncryptedContentRecovery,
		ExcludeCurrentAccount:       false,
		SameAccountRetryEligible:    false,
		CompatibilityRecoverySignal: gatewaycodex.SignalEncryptedContextInvalid,
		RecoveryBody:                []byte(bug0289RecoveredBody),
		RecoverySemanticRetryID:     bug0289SemanticRetryID,
		RecoveryMetadata: &gatewaycodex.CodexEncryptedContentRecoveryMetadata{
			Strategy:                              "codex_encrypted_content_cleanup",
			Signal:                                gatewaycodex.SignalEncryptedContextInvalid,
			RemovedReasoningEncryptedContentCount: 1,
			BodyBytesBefore:                       128,
			BodyBytesAfter:                        96,
		},
	}
}

// TestBug0289SettleRecoveryArmPinsSameAccountReplay：恢复臂返回 false 继续派发，
// pending 状态就位、metadata 在、当前账户不进 streamRetryExcludedAccounts；
// 下一轮 newRequestCoordination 注入 override/钉住/语义重试 ID 后一次性清空。
func TestBug0289SettleRecoveryArmPinsSameAccountReplay(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = capture
	account := gatewaydispatch.AccountCandidate{ID: "acc_rec", Name: "恢复账户"}
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{account}
	dispatched := gatewaydispatch.UpstreamDispatchResult{Account: account}

	if settled := loop.settleResponseStreamServerRetry(context.Background(), dispatched, bug0289RecoveryHandling()); settled {
		t.Fatal("恢复臂必须返回 false 继续派发")
	}
	if loop.pendingCodexRecovery == nil {
		t.Fatal("pending 恢复状态必须就位")
	}
	if loop.pendingCodexRecovery.AccountID != "acc_rec" {
		t.Fatalf("pending accountId = %q", loop.pendingCodexRecovery.AccountID)
	}
	if string(loop.pendingCodexRecovery.Body) != bug0289RecoveredBody {
		t.Fatalf("pending body = %s", loop.pendingCodexRecovery.Body)
	}
	if loop.pendingCodexRecovery.SemanticRetryID != bug0289SemanticRetryID {
		t.Fatalf("pending semanticRetryID = %q", loop.pendingCodexRecovery.SemanticRetryID)
	}
	if _, excluded := loop.streamRetryExcludedAccounts["acc_rec"]; excluded {
		t.Fatal("恢复重放不得把当前账户加入 streamRetryExcludedAccounts")
	}
	metadata := capture.byLabel("codex_encrypted_content_recovery_retry")
	if metadata == nil {
		t.Fatalf("恢复臂 metadata 缺失: %v", capture.labels)
	}
	if metadata["accountId"] != "acc_rec" || metadata["signal"] != gatewaycodex.SignalEncryptedContextInvalid {
		t.Fatalf("metadata = %+v", metadata)
	}
	if _, hasRetryCount := metadata["retryCount"]; !hasRetryCount {
		t.Fatalf("metadata 缺 retryCount 键: %+v", metadata)
	}
	if _, hasRemoved := metadata["removedReasoningEncryptedContentCount"]; !hasRemoved {
		t.Fatalf("metadata 缺 removed 计数键: %+v", metadata)
	}

	coordination := loop.newRequestCoordination()
	if coordination.RequestBodyOverride == nil {
		t.Fatal("下一轮 coordination 必须携带 RequestBodyOverride")
	}
	if coordination.RequestBodyOverride.AccountID != "acc_rec" {
		t.Fatalf("override accountId = %q", coordination.RequestBodyOverride.AccountID)
	}
	if string(coordination.RequestBodyOverride.Body) != bug0289RecoveredBody {
		t.Fatalf("override body = %s", coordination.RequestBodyOverride.Body)
	}
	if coordination.SameAccountRetry == nil || coordination.SameAccountRetry.Account.ID != "acc_rec" {
		t.Fatalf("下一轮必须同账户钉住: %+v", coordination.SameAccountRetry)
	}
	if coordination.SemanticRetryID != bug0289SemanticRetryID {
		t.Fatalf("semanticRetryID = %q", coordination.SemanticRetryID)
	}
	if loop.pendingCodexRecovery != nil {
		t.Fatal("pending 状态必须在注入后一次性清空")
	}
}

// TestBug0289ClassifyPostVerdictRecoveryNeutral：恢复重放轮的分类保持中性
// ——不产生传输失败、也不构成帧完成治愈证据（对齐既有注释表的口径：
// neutral → circuit ReportUnknown，而非 ReportFramingComplete）。
func TestBug0289ClassifyPostVerdictRecoveryNeutral(t *testing.T) {
	dispatched := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_rec"}}
	class := classifyPostVerdictOutcome(dispatched, bug0289RecoveryHandling())
	if class.transportFailure != nil {
		t.Fatalf("恢复轮不得合成传输失败: %+v", class.transportFailure)
	}
	if !class.neutralSchedulingTermination {
		t.Fatal("恢复轮必须命中中性与调度终止分类（circuit ReportUnknown 口径）")
	}
	if class.explicitUserPolicyRetry || class.requestLocalProtocolFailure || class.protocolValidatedSuccess {
		t.Fatalf("恢复轮不得落入其他分类臂: %+v", class)
	}
}

// TestBug0289StreamServerRetryFallbackReason：耗尽兜底 reason 对新恢复原因
// 有显式命名（防御性，正常流不走到）。
func TestBug0289StreamServerRetryFallbackReason(t *testing.T) {
	if got := streamServerRetryFallbackReason(gatewayresponse.StreamServerRetryCodexEncryptedContentRecovery); got != "codex_encrypted_content_recovery" {
		t.Fatalf("fallbackReason = %q", got)
	}
}
