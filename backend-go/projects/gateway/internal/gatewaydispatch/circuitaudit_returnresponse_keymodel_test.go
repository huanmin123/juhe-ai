package gatewaydispatch

// circuitaudit_returnresponse_keymodel_test.go — BUG-0267 追加验收：
// ReturnResponse 透传分支对 key-model 准入尝试的 unknown 结算。
//
// 修复前：keyModel 结算位于 ReturnResponse 分支 return 之后的公共路径，
// 对透传路径不可达——准入前台许可（foreground permit）在 settle 前由
// renewal 持续续租，等于永久泄漏该 capability 的前台准入位。
// 修复后：与 circuit 同构就地 ReportUnknown（settle-once 幂等，Node :1876
// reportUpstreamNotComplete 同分支语义）。
//
// 断言核心是许可真实释放：分支返回后同一 capability 必须立即可重新
// AdmitForeground（ForegroundAdmitted）；若回归为不结算，准入返回 busy，
// 泄漏被本测试拦截。层级选择与 circuitaudit_returnresponse_unknown_test.go
// 相同（单元级直驱 handleUpstreamAttemptResponse，引擎级 fixture 不装配
// key-model 准入面）。

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
)

func TestReturnResponseBranchSettlesKeyModelPermit(t *testing.T) {
	h := newAttemptErrorHarness(t)
	kstore := gatewayaccounteffects.NewInMemoryKeyModelRuntimeStore(gatewayaccounteffects.SystemClock{})
	route := gatewayaccounteffects.GatewayKeyModelCapability{
		AccountID: "km-rr-acc",
		Capability: gatewayaccounteffects.CapabilityKey{
			CredentialSourceAccountID: "km-rr-source",
			KeyFingerprint:            "km-rr-fp",
			ClientModel:               "gpt-test",
			ClientEndpointFamily:      "chat_completions",
			FinalUpstreamModel:        "gpt-upstream",
			UpstreamEndpointMode:      "chat_json",
			DispatchRevision:          3,
		},
	}
	preparation, err := gatewayaccounteffects.PrepareGatewayKeyModelAttempt(context.Background(), kstore,
		gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput{
			Route:         route,
			RequestID:     "km-rr-req",
			AttemptID:     "km-rr-1",
			FailureBudget: gatewayaccounteffects.NewGatewayKeyModelFailureBudget(),
			Scheduler:     gatewayaccounteffects.NewManualScheduler(),
			Logger:        gatewayaccounteffects.NopLogger{},
		})
	if err != nil {
		t.Fatalf("PrepareGatewayKeyModelAttempt: %v", err)
	}
	if preparation.Status != gatewayaccounteffects.AttemptPreparationAdmitted || preparation.Attempt == nil {
		t.Fatalf("preparation = %s, want admitted+attempt", preparation.Status)
	}

	// fake dispatcher 配置为 ReturnResponse：失败响应直接透传。
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
		auditAttemptID:                "km-rr-1",
		hotQualityAttempt:             &hotQualityAttemptHandle{},
		keyModelAttempt:               preparation.Attempt,
		firstByteDeadlineTriggeredRef: ptrBool(false),
	}
	kind, stop, err := h.engine.handleUpstreamAttemptResponse(context.Background(), responseCtx)
	if err != nil {
		t.Fatalf("handleUpstreamAttemptResponse: %v", err)
	}
	if kind != responseKindSelected || stop != responseStopNone {
		t.Fatalf("kind=%v stop=%v, want (selected, none)", kind, stop)
	}
	if result == nil || result.Response == nil || result.Response.Status() != http.StatusServiceUnavailable {
		t.Fatalf("透传结果 = %+v, want 503 响应经 resultRef 返回", result)
	}

	// 断言 1：keyModel 尝试已结算（settle-once 终态信号关闭）。
	select {
	case <-preparation.Attempt.WaitTerminal():
	default:
		t.Fatal("ReturnResponse 分支必须结算 keyModel 尝试（WaitTerminal 未关闭，许可续租泄漏）")
	}

	// 断言 2：前台许可已真实释放——同一 capability 立即可再次准入；
	// 不结算时 renewal 持续持锁，此处会拿到 busy。
	readmission, err := kstore.AdmitForeground(context.Background(), route.Capability, "km-rr-probe-2")
	if err != nil {
		t.Fatalf("AdmitForeground(回归探针): %v", err)
	}
	if readmission.Status != gatewayaccounteffects.ForegroundAdmitted {
		t.Fatalf("回归准入 = %s, want admitted（许可未释放，透传路径泄漏前台准入位）", readmission.Status)
	}
}
