package gatewaydispatch

// circuitaudit_ok_carry_test.go — BUG-0267 post-verdict 结算块的跨层传导
// 验收：OK 臂（response.ok）选中的结果必须把 circuit / keyModel 尝试句柄
// 带出到 UpstreamDispatchResult——链面结算块（chain_v1_loop.go
// settlePostVerdictUpstreamAttempts）按 body 实际结局分类结算的唯一数据源。
// 本断言复现并锁定复审 P0：句柄赋值缺失时链面整个结算块是死代码，且 OK 臂
// 移除前置 ReportFramingComplete 后 confirmation 租约悬挂（R2 回归），
// 而所有行为测试仍全绿（判定在、结构在、接线断——正是本档案病灶模式）。
//
// 层级选择与 circuitaudit_returnresponse_unknown_test.go 相同：单元级直调
// handleUpstreamAttemptResponse（引擎级 fixture 不装配 Circuits，见该文件
// 的层级选择说明）。keyModel 句柄因构造器为跨包私有，以 nil 透传断言
// （同构赋值 + 同型字段，非 nil 路径由类型系统保证）。

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
)

// TestCircuitAuditOkBranchCarriesAttemptHandlesToResult：200 响应选中后，
// result.AccountCircuitAttempt 必须与注入的 confirmation 尝试同指针，
// result.KeyModelAttempt 逐值透传（nil→nil）；且 OK 臂不得再前置结算
//（SUSPECT 状态保持、租约仍由句柄持有——结算权已移交链面）。
func TestCircuitAuditOkBranchCarriesAttemptHandlesToResult(t *testing.T) {
	h := newAttemptErrorHarness(t)
	clock := int64(9_000_000)
	confirmation, service, store, scope := newReturnResponseAuditConfirmation(t, &clock)
	h.input.accountCircuitAttempt = confirmation

	ok := gatewayupstream.NewGatewayUpstreamResponseForTransform(
		http.StatusOK, http.Header{"Content-Type": []string{"application/json"}},
		io.NopCloser(strings.NewReader(`{"id":"r1","choices":[]}`)),
	)

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
		response:                      ok,
		upstreamURL:                   "https://upstream.example/v1/chat/completions",
		attemptIndex:                  &attemptIndex,
		attemptStartedAt:              gatewayupstream.NowMs(),
		auditAttemptID:                "circuit-audit-ok-1",
		hotQualityAttempt:             &hotQualityAttemptHandle{},
		firstByteDeadlineTriggeredRef: ptrBool(false),
	}
	kind, stop, err := h.engine.handleUpstreamAttemptResponse(context.Background(), responseCtx)
	if err != nil {
		t.Fatalf("handleUpstreamAttemptResponse: %v", err)
	}
	if kind != responseKindSelected || stop != responseStopNone {
		t.Fatalf("kind=%v stop=%v, want (selected, none)", kind, stop)
	}
	if result == nil {
		t.Fatal("OK 分支必须产出 result")
	}

	// 传导断言（复审 P0 的回归锁）：句柄逐指针带出，链面结算块的唯一数据源。
	if result.AccountCircuitAttempt != confirmation {
		t.Fatal("OK 分支必须把 confirmation 尝试句柄原样带出（链面 post-verdict 结算依赖）")
	}
	if result.KeyModelAttempt != nil {
		t.Fatal("未参与 key-model 的尝试带出必须为 nil（本用例未注入）")
	}

	// 结算权移交断言：OK 臂不再前置 ReportFramingComplete——状态保持
	// SUSPECT、confirmation 租约仍被句柄持有（等链面按 body 结局分类）。
	state, err := store.Get(context.Background(), scope, nil)
	if err != nil {
		t.Fatalf("store.Get(OK 分支返回后): %v", err)
	}
	if state.Phase != gatewaycircuit.PhaseSuspect {
		t.Fatalf("OK 分支返回后 phase = %s，前置结算会推进到 RECOVERING，结算移交则保持 SUSPECT", state.Phase)
	}
	if state.Lease == nil {
		t.Fatal("结算移交链面后 confirmation 租约必须仍被持有（释放发生在链面 post-verdict 结算）")
	}
	_ = service
}
