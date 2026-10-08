package main

// BUG-0298 优化批次回归测试：
// ① 信号未命中换号被接受时，记录来源级短 TTL 避让（同一会话后续请求把失败
//    账户排到同层候选之后）并解除其会话亲和；
// ② 连续两次换号失败且上游错误码+文案签名一致判定同源，停止级联候选，走
//    既有耗尽契约。

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// bug0298SwitchHandling 构造信号未命中换号 verdict（生产漂移形状：上游
// invalid_request 通用参数文案）。
func bug0298SwitchHandling() gatewayresponse.UpstreamResponseHandlingResult {
	return gatewayresponse.UpstreamResponseHandlingResult{
		RetryUpstream:         true,
		RetryReason:           gatewayresponse.StreamServerRetryPreCommitStreamFailure,
		ExcludeCurrentAccount: true,
		ErrorCode:             "upstream_retryable_error",
		Message:               "上游流式响应在输出前失败，请重试",
		ResponseInspection: &gatewayresponse.ResponseInspectionDecision{
			PolicyID:             "default_openai_response_error",
			PolicyProtocolCode:   "openai",
			UpstreamErrorCode:    "invalid_request",
			UpstreamErrorMessage: "The request could not be processed. Please check the request parameters.",
		},
	}
}

// TestBug0298SameSignatureSwitchStops：同签名连续换号即停——第一次换号继续
//（返回 false），第二次同签名失败不再级联候选（返回 true 并下发耗尽契约）；
// 不同签名的失败不受影响照常继续。
func TestBug0298SameSignatureSwitchStops(t *testing.T) {
	ctx := context.Background()
	sink := &recordingFailureSink{}
	l := newV1TestLoop(t, sink)
	l.current.Accounts = []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-c"}, {ID: "acc-d"}}

	handling := bug0298SwitchHandling()
	first := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc-a"}, AuditAttemptID: "audit-1"}
	if settled := l.settleResponseStreamServerRetry(ctx, first, handling); settled {
		t.Fatalf("首次换号必须继续候选循环: %+v", l.streamRetryExcludedAccounts)
	}
	if l.lastPreCommitSwitchSignature == "" {
		t.Fatal("首次换号必须记录失败签名")
	}
	if _, excluded := l.streamRetryExcludedAccounts["acc-a"]; !excluded {
		t.Fatalf("首次换号必须排除当前账户: %v", l.streamRetryExcludedAccounts)
	}

	// 同签名第二跳：同源判定，停止级联并下发耗尽契约；停止发生在记账前，
	// acc-b 不得进入排除集。
	second := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc-b"}, AuditAttemptID: "audit-2"}
	if settled := l.settleResponseStreamServerRetry(ctx, second, handling); !settled {
		t.Fatal("同签名连续换号必须即停")
	}
	if _, excluded := l.streamRetryExcludedAccounts["acc-b"]; excluded {
		t.Fatal("同签名即停必须发生在排除记账之前")
	}
	if len(sink.inputs) == 0 {
		t.Fatal("即停必须下发耗尽契约响应")
	}

	// 不同签名（不同文案）：不判同源，照常继续。
	other := bug0298SwitchHandling()
	other.ResponseInspection = &gatewayresponse.ResponseInspectionDecision{
		PolicyID:             "default_openai_response_error",
		PolicyProtocolCode:   "openai",
		UpstreamErrorCode:    "upstream_timeout",
		UpstreamErrorMessage: "upstream timed out",
	}
	third := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc-c"}, AuditAttemptID: "audit-3"}
	if settled := l.settleResponseStreamServerRetry(ctx, third, other); settled {
		t.Fatal("不同签名的失败照常继续候选循环")
	}
}

// TestBug0298SignatureGateSkipsInspectionNilSwitches：检查未命中换号
//（ResponseInspection == nil）不进签名/避让门——连续两次照常继续，锁定
// 该面行为不变。
func TestBug0298SignatureGateSkipsInspectionNilSwitches(t *testing.T) {
	ctx := context.Background()
	sink := &recordingFailureSink{}
	l := newV1TestLoop(t, sink)
	l.current.Accounts = []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-c"}, {ID: "acc-d"}}
	handling := bug0298SwitchHandling()
	handling.ResponseInspection = nil
	for i, accountID := range []string{"acc-a", "acc-b"} {
		dispatched := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: accountID}, AuditAttemptID: fmt.Sprintf("audit-nil-%d", i+1)}
		if settled := l.settleResponseStreamServerRetry(ctx, dispatched, handling); settled {
			t.Fatalf("检查未命中换号第 %d 跳必须照常继续: %+v", i+1, l.streamRetryExcludedAccounts)
		}
		if l.lastPreCommitSwitchSignature != "" {
			t.Fatalf("检查未命中换号不得记录签名: %q", l.lastPreCommitSwitchSignature)
		}
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("检查未命中换号不得触发耗尽契约: %v", sink.inputs)
	}
}

// TestBug0298StreamSwitchRecordsSourceAvoidance：换号被接受时记录来源级避让
//（阈值激活后同层候选把失败账户后置）并对未装配协作方的组合保持降级。
func TestBug0298StreamSwitchRecordsSourceAvoidance(t *testing.T) {
	ctx := context.Background()
	turnRetry := &gatewaycodex.TurnRetryService{Secret: "bug0298-turn-secret"}
	dispatcher := &chainFailureDispatcher{
		clientStrategy: &gatewaycodex.ClientStrategyDeps{Source: &gatewaycodex.SourceIdentityResolver{Secret: "bug0298-source-secret"}},
		turnRetry:      turnRetry,
	}
	request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{}`))
	request.Header.Set("session-id", "bug0298-session")
	request.Header.Set("x-codex-turn-metadata", `{"installation_id":"i1","session_id":"bug0298-session","thread_id":"bug0298-session","turn_id":"t1"}`)
	req := gatewaypreauth.NewGatewayRequest(request)
	usageContext := gatewaypreauth.GatewayFailureUsageContext{
		TrafficSource:   "gateway",
		SystemAccountID: "sysacc-1",
		APIKeyID:        "key-1",
		GroupID:         "grp-1",
		Endpoint:        "responses",
		ClientIP:        "203.0.113.9",
	}
	account := gatewaydispatch.AccountCandidate{ID: "acc-a", ProviderCode: "openai"}

	identity := gatewaycodex.ClientStrategyIdentity{
		SystemAccountID: usageContext.SystemAccountID,
		APIKeyID:        usageContext.APIKeyID,
		GroupID:         usageContext.GroupID,
		Endpoint:        usageContext.Endpoint,
		ProviderCode:    account.ProviderCode,
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		ClientIP:        usageContext.ClientIP,
	}
	// 未装配 Source 的解析器不得 panic，且避让保持关闭（未写记录：独立
	// TurnRetryService 上排序不激活）。
	bareTurnRetry := &gatewaycodex.TurnRetryService{Secret: "bug0298-bare-secret"}
	bare := &chainFailureDispatcher{turnRetry: bareTurnRetry}
	bare.scheduleStreamSwitchClientSourceAvoidance(ctx, req, usageContext, "aff-1", account, "invalid_request", "x", "audit-0")
	bareOrder, err := (&chainClientSourceAvoidance{turnRetry: bareTurnRetry}).OrderAsync(ctx, []gatewaydispatch.AccountCandidate{
		{ID: "acc-a", Priority: 10},
		{ID: "acc-fresh", Priority: 10},
	}, gatewaypreauth.ClientStrategyContext{Opaque: gatewaycodex.OpenAIGatewayClientStrategyContext{}}, nil)
	if err != nil {
		t.Fatalf("降级排序失败: %v", err)
	}
	if bareOrder.Applied || len(bareOrder.AvoidedAccountIDs) != 0 {
		t.Fatalf("未装配协作方时避让必须保持关闭: %+v", bareOrder)
	}

	// 记录两次（不同观测 ID，对齐生产每次 attempt 唯一）达到激活阈值：换号
	// 入口解除亲和并记录来源级失败。
	for i := 0; i < 2; i++ {
		dispatcher.scheduleStreamSwitchClientSourceAvoidance(ctx, req, usageContext, "aff-1", account, "invalid_request", "The request could not be processed. Please check the request parameters.", fmt.Sprintf("audit-%d", i+1))
	}

	strategy := dispatcher.clientStrategy.ResolveOpenAIGatewayClientStrategy(req, identity)
	if !strategy.AllowClientSourceAccountAvoidance {
		t.Fatalf("codex 会话必须产生来源避让键: %+v", strategy)
	}
	adapter := &chainClientSourceAvoidance{turnRetry: turnRetry}
	ordered, err := adapter.OrderAsync(ctx, []gatewaydispatch.AccountCandidate{
		{ID: "acc-a", Priority: 10},
		{ID: "acc-fresh", Priority: 10},
	}, gatewaypreauth.ClientStrategyContext{Opaque: strategy}, nil)
	if err != nil {
		t.Fatalf("排序失败: %v", err)
	}
	if !ordered.Applied {
		t.Fatalf("阈值激活后必须应用来源避让: %+v", ordered)
	}
	if len(ordered.AvoidedAccountIDs) != 1 || ordered.AvoidedAccountIDs[0] != "acc-a" {
		t.Fatalf("失败账户必须被避让: %v", ordered.AvoidedAccountIDs)
	}
	if ordered.Accounts[0].ID != "acc-fresh" {
		t.Fatalf("同层内失败账户必须后置: %v", ordered.Accounts)
	}
}
