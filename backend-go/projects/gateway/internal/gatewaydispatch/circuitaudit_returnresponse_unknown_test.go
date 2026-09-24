package gatewaydispatch

// circuitaudit_returnresponse_unknown_test.go — R2 追加验收：ReturnResponse
// 透传分支对 confirmation 尝试的 unknown 结算。
//
// 场景：FailedResponseActionReturnResponse 是失败响应直接透传给客户端的
// 分支（attemptoutcomes.go；真实触发面为账号诊断流量 chain_ports.go:305 与
// 非网关流量 :314 的 ReturnResponse 配置）。该分支原先对
// in.accountCircuitAttempt 无任何结算，confirmation 租约悬挂至 30s
// leaseUntil 过期；修复后与 SkipAccount 失败分支同构调 ReportUnknown
//（unknown 四条契约：立即释放租约、保持 SUSPECT、不新增失败证据、退避后再
// 认领——gatewaycircuit/circuitaudit_suspect_heal_test.go 已验）。
//
// 层级选择说明：选择单元级直接调用 handleUpstreamAttemptResponse 而非引擎级
//（FetchFirstAvailableUpstream + httptest），原因有二：
//
//  1. 现有引擎级 fixture（newTestEngine）不装配 engine.Circuits，
//     accountCircuitAttempt 恒为 nil，无法观察结算行为；
//  2. 真实装配 Circuits 时，要让尝试携带 confirmation 租约需账号处于
//     SUSPECT 且 retryAt 到期（真实时钟 3 秒）且引擎内部 FailureEvidenceKey
//     恰与 SUSPECT 证据不同——引擎级 fixture 无法注入假时钟也不控制
//     evidence key，改造成本远超本验收所需。
//
// 单元级复用 engineerrorpaths_test.go 的 harness（同一 dispatchSingleAccountInput /
// upstreamAttemptLoopContext 结构），confirmation 句柄挂真实
// gatewaycircuit.CircuitService（注入假时钟），能精确驱动到目标分支并断言
// 结算效果。分支自身行为（透传响应、不切号）同时被断言，防止结算改动破坏
// 分支契约。

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// newReturnResponseAuditConfirmation 驱动真实 CircuitService 到"SUSPECT 到期
// 并持有 confirmation 租约"的状态（假时钟免除 3 秒真实退避），返回
// confirmation 尝试句柄与后续结算断言所需的 service/store/scope。
func newReturnResponseAuditConfirmation(t *testing.T, clock *int64) (*gatewaycircuit.Attempt, *gatewaycircuit.CircuitService, *gatewaycircuit.MemoryStore, gatewaycircuit.Scope) {
	t.Helper()
	ctx := context.Background()
	now := func() int64 { return *clock }
	store, err := gatewaycircuit.NewMemoryStore(gatewaycircuit.MemoryStoreOptions{Capacity: 16, Now: now})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	service, err := gatewaycircuit.NewCircuitService(store, gatewaycircuit.ServiceOptions{Now: now})
	if err != nil {
		t.Fatalf("NewCircuitService: %v", err)
	}
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID:              "circuit-audit-rr-acct",
		ProviderCode:    "openai",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		Type:            "api_key",
		Status:          "active",
		APIKey:          "sk-circuit-audit-rr",
	}
	model := "gpt-test"
	scope, err := gatewaycircuit.GatewayAccountProtocolModelScope(account, gatewaycircuit.LaneText, &model)
	if err != nil {
		t.Fatalf("GatewayAccountProtocolModelScope: %v", err)
	}

	prepare, err := service.PrepareAttempt(ctx, gatewaycircuit.PrepareAttemptInput{
		Account:                     account,
		RequestLane:                 gatewaycircuit.LaneText,
		Model:                       &model,
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil || prepare.Outcome != gatewaycircuit.PrepareDispatchable || prepare.Attempt == nil {
		t.Fatalf("第一次 PrepareAttempt = (%s, %v)", prepare.Outcome, err)
	}
	decision, err := prepare.Attempt.ReportTransportFailure(ctx, gatewaycircuit.TransportFailure{
		Kind:   gatewaycircuit.TransportFailureKindTransport,
		Reason: "circuit-audit: connection reset by peer",
	})
	if err != nil || decision.Outcome != gatewaycircuit.DecisionSuspected {
		t.Fatalf("ReportTransportFailure = (%s, %v), want suspected", decision.Outcome, err)
	}

	// 推进越过 SUSPECT retryAt（+3000ms，精确无抖动），再认领 confirmation。
	*clock += 3_001
	evidence := strings.Repeat("c", 64)
	confirm, err := service.PrepareAttempt(ctx, gatewaycircuit.PrepareAttemptInput{
		Account:                     account,
		RequestLane:                 gatewaycircuit.LaneText,
		Model:                       &model,
		ConfirmationLeaseDurationMs: 30_000,
		FailureEvidenceKey:          &evidence,
	})
	if err != nil || confirm.Outcome != gatewaycircuit.PrepareDispatchable || confirm.Attempt == nil {
		t.Fatalf("confirmation PrepareAttempt = (%s, %v)", confirm.Outcome, err)
	}
	if !confirm.Attempt.IsConfirmation() {
		t.Fatal("到期 SUSPECT 的新尝试必须携带 confirmation 租约")
	}
	return confirm.Attempt, service, store, scope
}

func TestCircuitAuditReturnResponseBranchSettlesConfirmationAsUnknown(t *testing.T) {
	h := newAttemptErrorHarness(t)
	clock := int64(9_000_000)
	confirmation, service, store, scope := newReturnResponseAuditConfirmation(t, &clock)
	h.input.accountCircuitAttempt = confirmation

	// fake dispatcher 配置为 ReturnResponse：失败响应直接透传（诊断/非网关
	// 流量的动作），引擎必须走到目标分支而不是 SkipAccount 切号。
	failed := gatewayupstream.NewGatewayUpstreamResponseForTransform(
		http.StatusServiceUnavailable, http.Header{},
		io.NopCloser(strings.NewReader(`{"error":{"message":"upstream unavailable"}}`)),
	)
	dispatcher := h.engine.FailureDispatcher.(*fakeFailureDispatcher)
	dispatcher.failedResult = FailedUpstreamResponseResult{
		Action:   FailedResponseActionReturnResponse,
		Response: failed,
	}

	retryKey := false
	skipAccount := false
	keepSlot := false
	var result *UpstreamDispatchResult
	attemptIndex := 1
	account := testAccounts("a-1")[0]
	loop := upstreamAttemptLoopContext{
		in:                         h.input,
		account:                    account,
		upstreamUrls:               []string{"https://upstream.example/v1/chat/completions"},
		usageContext:               h.input.usageContext,
		auditCapture:               h.input.auditCapture,
		signal:                     h.input.signal,
		accountApiKeyAttemptCount:  ptrInt(0),
		concurrencySlot:            &ConcurrencySlot{Acquired: true},
		excludedApiKeyFingerprints: map[string]struct{}{},
		keepConcurrencySlotRef:     &keepSlot,
		pendingApiKeyFailuresRef:   h.input.pendingApiKeyFailures,
		retryAccountApiKeyRef:      &retryKey,
		skipAccountRef:             &skipAccount,
		resultRef:                  &result,
	}
	responseCtx := upstreamAttemptResponseContext{
		loop:                          &loop,
		account:                       account,
		response:                      failed,
		upstreamURL:                   "https://upstream.example/v1/chat/completions",
		attemptIndex:                  &attemptIndex,
		attemptStartedAt:              gatewayupstream.NowMs(),
		auditAttemptID:                "circuit-audit-rr-1",
		hotQualityAttempt:             &hotQualityAttemptHandle{},
		firstByteDeadlineTriggeredRef: ptrBool(false),
	}
	kind, stop, err := h.engine.handleUpstreamAttemptResponse(context.Background(), responseCtx)
	if err != nil {
		t.Fatalf("handleUpstreamAttemptResponse: %v", err)
	}
	// 分支契约保持：透传响应被选中，不切号、不继续。
	if kind != responseKindSelected || stop != responseStopNone {
		t.Fatalf("kind=%v stop=%v, want (selected, none)", kind, stop)
	}
	if result == nil || result.Response == nil || result.Response.Status() != http.StatusServiceUnavailable {
		t.Fatalf("透传结果 = %+v, want 503 响应经 resultRef 返回", result)
	}

	// 结算断言 1：分支返回后状态保持 SUSPECT 且租约立即释放（不等 30s
	// leaseUntil；若误走 framing_complete 这里会变成 RECOVERING）。
	state, err := store.Get(context.Background(), scope, nil)
	if err != nil {
		t.Fatalf("store.Get(分支返回后): %v", err)
	}
	if state.Phase != gatewaycircuit.PhaseSuspect {
		t.Fatalf("分支返回后 phase = %s, want SUSPECT（透传失败响应不得推进/关闭熔断）", state.Phase)
	}
	if state.Lease != nil {
		t.Fatalf("分支返回后 lease = %+v, want nil（unknown 结算必须立即释放租约）", state.Lease)
	}

	// 结算断言 2：退避（backoffAttempt=1 → 精确 +3000ms）到期后重新
	// PrepareAttempt 能立即重新认领 confirmation——租约没有悬挂到 30s
	//（同一 service/假时钟，避免与真实时钟混用）。
	clock += 3_001
	nextEvidence := strings.Repeat("d", 64)
	model := "gpt-test"
	retry, err := service.PrepareAttempt(context.Background(), gatewaycircuit.PrepareAttemptInput{
		Account: gatewayruntimecache.OpenAIAccountSecret{
			ID:              "circuit-audit-rr-acct",
			ProviderCode:    "openai",
			ProtocolCode:    "openai",
			ProtocolVersion: "v1",
			Type:            "api_key",
			Status:          "active",
			APIKey:          "sk-circuit-audit-rr",
		},
		RequestLane:                 gatewaycircuit.LaneText,
		Model:                       &model,
		ConfirmationLeaseDurationMs: 30_000,
		FailureEvidenceKey:          &nextEvidence,
	})
	if err != nil {
		t.Fatalf("重新 PrepareAttempt: %v", err)
	}
	if retry.Outcome != gatewaycircuit.PrepareDispatchable || retry.Attempt == nil || !retry.Attempt.IsConfirmation() {
		t.Fatalf("退避到期后重新 PrepareAttempt = (%s, confirmation=%v), want 立即重新认领 confirmation",
			retry.Outcome, retry.Attempt != nil && retry.Attempt.IsConfirmation())
	}
}
