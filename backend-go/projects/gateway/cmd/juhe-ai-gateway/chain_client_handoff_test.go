package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// renderClientHandoff 的统一交接：流式请求必须先提交 SSE 头再断开；底层
// 不支持 Hijack 时才回退 503。锁死 hold 之外的 client_handoff 出口共用此方法。

func TestRenderClientHandoffStreamingCommitsSSEThenDisconnects(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.req = switchTargetRequest(t, "/v1/chat/completions",
		`{"model":"gpt-test","stream":true}`, "gpt-test", true)
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
	loop.res = gatewaypreauth.NewTrackingWriter(fake)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit

	if settled := loop.renderClientHandoff(streamServerRetryExhaustedInput{
		message:      "网关请求协调预算已到，请客户端重试并重新选择可用账户",
		retryReason:  "pre_commit_stream_failure",
		errorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
		usageContext: gatewaypreauth.GatewayFailureUsageContext{},
	}); !settled {
		t.Fatal("client handoff 必须终结请求")
	}
	if !loop.res.HeadersSent() {
		t.Fatal("流式交接必须先提交 SSE 响应头")
	}
	if loop.res.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", loop.res.Header().Get("Content-Type"))
	}
	if fake.hijackCalls != 1 || conn.closeCalls != 1 {
		t.Fatalf("hijackCalls=%d closeCalls=%d，期望提交后真断开一次", fake.hijackCalls, conn.closeCalls)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("流式真断开时不得再写 503：%#v", sink.inputs)
	}
	payload := audit.payloadOf("gateway_client_handoff_transport")
	if payload["handoff"] != "stream_committed_disconnect" {
		t.Fatalf("handoff = %#v", payload["handoff"])
	}
}

func TestRenderClientHandoffFallsBackTo503WhenHijackUnavailable(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.req = switchTargetRequest(t, "/v1/chat/completions",
		`{"model":"gpt-test","stream":true}`, "gpt-test", true)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit

	if settled := loop.renderClientHandoff(streamServerRetryExhaustedInput{
		message:      "网关请求协调预算已到，请客户端重试并重新选择可用账户",
		retryReason:  "pre_commit_stream_failure",
		errorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
		usageContext: gatewaypreauth.GatewayFailureUsageContext{},
	}); !settled {
		t.Fatal("client handoff 必须终结请求")
	}
	if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Hijack 不可用必须回退 503：%#v", sink.inputs)
	}
	payload := audit.payloadOf("gateway_client_handoff_transport")
	if payload["handoff"] != "http_503_fallback" {
		t.Fatalf("handoff = %#v", payload["handoff"])
	}
}

// 等待心跳已提交 200/text/event-stream（语义未提交）时，交接仍必须真断开：
// 不能再写响应头，但要在已提交的流上 Hijack+Close，让客户端走网络错误重试。
func TestRenderClientHandoffAfterHeartbeatCommitsStreaming(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.req = switchTargetRequest(t, "/v1/chat/completions",
		`{"model":"gpt-test","stream":true}`, "gpt-test", true)
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
	loop.res = gatewaypreauth.NewTrackingWriter(fake)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	// 模拟等待心跳写出的传输提交。
	loop.res.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	loop.res.WriteHeader(http.StatusOK)

	if settled := loop.renderClientHandoff(streamServerRetryExhaustedInput{
		message:      "账户处于锁死窗口，窗口内不切换其他账户，请客户端重试",
		retryReason:  "account_lock_hold",
		errorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
		usageContext: gatewaypreauth.GatewayFailureUsageContext{},
	}); !settled {
		t.Fatal("client handoff 必须终结请求")
	}
	if fake.hijackCalls != 1 || conn.closeCalls != 1 {
		t.Fatalf("hijackCalls=%d closeCalls=%d，心跳已提交的流式请求仍必须真断开", fake.hijackCalls, conn.closeCalls)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("真断开时不得再写 503：%#v", sink.inputs)
	}
	payload := audit.payloadOf("gateway_client_handoff_transport")
	if payload["handoff"] != "stream_committed_disconnect" {
		t.Fatalf("handoff = %#v", payload["handoff"])
	}
}

// 切组被锁死门否决时，stream-retry 耗尽出口不得落终态 503，必须交接断连。
func TestSettleResponseStreamServerRetryLockedFallbackHandsOff(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
	loop.res = gatewaypreauth.NewTrackingWriter(fake)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	loop.c.engine = &gatewaydispatch.Engine{Locks: &chainLockHoldLocks{states: map[string]gatewaydispatch.AccountLockStateView{
		"a-1": {BlocksCrossAccount: true},
	}}}
	// 耗尽集里的 a-1 锁死；本次成功尝试的 a-2 未锁 → 走后备分组门。
	loop.exhaustedAccounts = map[string]struct{}{"a-1": {}}
	handling := gatewayresponse.UpstreamResponseHandlingResult{
		RetryReason:           gatewayresponse.StreamServerRetryResponseInspection,
		ErrorCode:             "policy_blocked",
		ExcludeCurrentAccount: true,
		ResponseInspection:    &gatewayresponse.ResponseInspectionDecision{PolicyID: "policy-1"},
	}
	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "a-2"},
	}

	if settled := loop.settleResponseStreamServerRetry(context.Background(), dispatched, handling); !settled {
		t.Fatal("锁死门否决切组后必须交接结算")
	}
	if fake.hijackCalls != 1 || conn.closeCalls != 1 {
		t.Fatalf("hijackCalls=%d closeCalls=%d，锁死 hold 必须真断开", fake.hijackCalls, conn.closeCalls)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("真断开时不得写终态 503：%#v", sink.inputs)
	}
	if !audit.has("account_lock_fallback_denied") {
		t.Fatalf("缺少 account_lock_fallback_denied 审计：%v", audit.labels)
	}
	payload := audit.payloadOf("gateway_client_handoff_transport")
	if payload["handoff"] != "connection_interrupt" {
		t.Fatalf("handoff = %#v", payload["handoff"])
	}
}

// 全部上游账户临时隔离（local suppression 耗尽）是非终态：route action 的
// 503 upstream_retryable_error 不得写给客户端，必须交接断开；Hijack 不可用
// 才回退同一 503（Retry-After 语义保留）。
func TestFinalizeRouteActionTemporarilyBlockedHandsOff(t *testing.T) {
	retryAfterMs := int64(1500)
	newAction := func() *gatewaypreauth.RouteAction {
		return &gatewaypreauth.RouteAction{
			Coordination: gatewaypreauth.RouteActionCoordination{
				Outcome: "temporarily_blocked", Reason: "upstream_retryable_error",
			},
			Failure: &gatewayrouting.GatewayRouteFinalFailure{
				StatusCode:   http.StatusServiceUnavailable,
				Message:      "所有上游账户正在临时隔离，请稍后重试",
				ErrorType:    "service_unavailable",
				ErrorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
				ErrorPhase:   "dispatch",
				RetryAfterMs: &retryAfterMs,
			},
		}
	}
	t.Run("真断开", func(t *testing.T) {
		sink := &recordingFailureSink{}
		loop := newV1TestLoop(t, sink)
		conn := &interruptFakeConn{}
		fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
		loop.res = gatewaypreauth.NewTrackingWriter(fake)
		audit := &chainLockHoldAudit{}
		loop.auditCapture = audit

		loop.finalizeRouteAction(newAction())
		if fake.hijackCalls != 1 || conn.closeCalls != 1 {
			t.Fatalf("hijackCalls=%d closeCalls=%d，临时隔离必须真断开", fake.hijackCalls, conn.closeCalls)
		}
		if len(sink.inputs) != 0 {
			t.Fatalf("真断开时不得再写 503：%#v", sink.inputs)
		}
		payload := audit.payloadOf("route_action_temporarily_blocked_handoff")
		if payload["handoff"] != "connection_interrupt" {
			t.Fatalf("handoff = %#v", payload["handoff"])
		}
		if payload["retryAfterMs"] != retryAfterMs {
			t.Fatalf("retryAfterMs = %#v", payload["retryAfterMs"])
		}
	})
	t.Run("无Hijack回退503", func(t *testing.T) {
		sink := &recordingFailureSink{}
		loop := newV1TestLoop(t, sink)
		audit := &chainLockHoldAudit{}
		loop.auditCapture = audit

		loop.finalizeRouteAction(newAction())
		if len(sink.inputs) != 1 {
			t.Fatalf("inputs=%d", len(sink.inputs))
		}
		input := sink.inputs[0]
		if input.StatusCode != http.StatusServiceUnavailable ||
			input.ResponsePayload.Error.Code != gatewaypreauth.GatewayStreamClientRetryErrorCode {
			t.Fatalf("回退 503 契约 = %+v", input)
		}
		payload := audit.payloadOf("route_action_temporarily_blocked_handoff")
		if payload["handoff"] != "http_503_fallback" {
			t.Fatalf("handoff = %#v", payload["handoff"])
		}
	})
}

// 已提交非 SSE 响应（JSON 等）不得被交接截断成"正常 EOF"：拒绝断连并回退
// 既有 503 契约。
func TestRenderClientHandoffRejectsCommittedNonSSEHeaders(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.req = switchTargetRequest(t, "/v1/chat/completions",
		`{"model":"gpt-test","stream":true}`, "gpt-test", true)
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
	loop.res = gatewaypreauth.NewTrackingWriter(fake)
	loop.res.Header().Set("Content-Type", "application/json")
	loop.res.WriteHeader(http.StatusOK)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit

	loop.renderClientHandoff(streamServerRetryExhaustedInput{
		message:      "账户处于锁死窗口，窗口内不切换其他账户，请客户端重试",
		retryReason:  "account_lock_hold",
		errorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
		usageContext: gatewaypreauth.GatewayFailureUsageContext{},
	})
	if fake.hijackCalls != 0 {
		t.Fatalf("已提交非 SSE 响应不得 Hijack（hijackCalls=%d）", fake.hijackCalls)
	}
	payload := audit.payloadOf("gateway_client_handoff_transport")
	if payload["handoff"] != "http_503_fallback" {
		t.Fatalf("handoff = %#v", payload["handoff"])
	}
}

// settleDispatchError 的 attempt 分支：切组被锁死门否决时必须交接，不得
// 落入终态耗尽 503。
func TestSettleDispatchErrorAttemptLockedFallbackHandsOff(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
	loop.res = gatewaypreauth.NewTrackingWriter(fake)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	loop.c.engine = &gatewaydispatch.Engine{Locks: &chainLockHoldLocks{states: map[string]gatewaydispatch.AccountLockStateView{
		"a-1": {BlocksCrossAccount: true},
	}}}

	attempt := &gatewaydispatch.UpstreamAttemptError{
		Message:          "upstream failed",
		FailedAccountIDs: []string{"a-1"},
	}
	if settled := loop.settleDispatchError(context.Background(), attempt); !settled {
		t.Fatal("attempt 错误必须结算")
	}
	if fake.hijackCalls != 1 || conn.closeCalls != 1 {
		t.Fatalf("hijackCalls=%d closeCalls=%d，锁死 hold 必须真断开", fake.hijackCalls, conn.closeCalls)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("真断开时不得写终态 503：%#v", sink.inputs)
	}
	if !audit.has("account_lock_fallback_denied") {
		t.Fatalf("缺少 account_lock_fallback_denied 审计：%v", audit.labels)
	}
}

// 响应检查锁死门读取失败：未观察到锁行 fail-open（继续换号），已观察到
// 锁行 fail-closed（交接）。
func TestSettleResponseStreamServerRetryLockReadFailureM4Narrowing(t *testing.T) {
	newLoop := func(t *testing.T, observation *gatewaydispatch.AccountLockObservation) (*v1DispatchLoop, *recordingFailureSink) {
		sink := &recordingFailureSink{}
		loop := newV1TestLoop(t, sink)
		loop.c.engine = &gatewaydispatch.Engine{Locks: &chainLockHoldLocks{err: context.DeadlineExceeded}}
		loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "a-1"}, {ID: "a-2"}}
		loop.streamRetryExcludedAccounts = map[string]struct{}{"a-1": {}}
		return loop, sink
	}
	handling := gatewayresponse.UpstreamResponseHandlingResult{
		RetryReason:           gatewayresponse.StreamServerRetryResponseInspection,
		ErrorCode:             "policy_blocked",
		ExcludeCurrentAccount: true,
		ResponseInspection:    &gatewayresponse.ResponseInspectionDecision{PolicyID: "policy-1"},
	}
	dispatched := func(observation *gatewaydispatch.AccountLockObservation) gatewaydispatch.UpstreamDispatchResult {
		return gatewaydispatch.UpstreamDispatchResult{
			Account:                gatewaydispatch.AccountCandidate{ID: "a-1"},
			AccountLockObservation: observation,
		}
	}

	t.Run("未观察锁行 fail-open", func(t *testing.T) {
		loop, _ := newLoop(t, nil)
		if settled := loop.settleResponseStreamServerRetry(context.Background(), dispatched(nil), handling); settled {
			t.Fatal("未观察到锁行时读取失败必须 fail-open（继续换号）")
		}
	})
	t.Run("已观察锁行 fail-closed 交接", func(t *testing.T) {
		loop, sink := newLoop(t, nil)
		conn := &interruptFakeConn{}
		fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
		loop.res = gatewaypreauth.NewTrackingWriter(fake)
		audit := &chainLockHoldAudit{}
		loop.auditCapture = audit
		if settled := loop.settleResponseStreamServerRetry(context.Background(), dispatched(&gatewaydispatch.AccountLockObservation{}), handling); !settled {
			t.Fatal("已观察锁行时读取失败必须 fail-closed 交接")
		}
		if fake.hijackCalls != 1 || conn.closeCalls != 1 {
			t.Fatalf("hijackCalls=%d closeCalls=%d", fake.hijackCalls, conn.closeCalls)
		}
		if len(sink.inputs) != 0 {
			t.Fatalf("真断开时不得写 503：%#v", sink.inputs)
		}
	})
}

// speed_first 结算期锁死门否决速度切号：交接断连，不落终态耗尽 503。
func TestSettleSpeedFirstCutoverLockedHandsOff(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
	loop.res = gatewaypreauth.NewTrackingWriter(fake)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	loop.c.engine = &gatewaydispatch.Engine{Locks: &chainLockHoldLocks{states: map[string]gatewaydispatch.AccountLockStateView{
		"a-1": {BlocksCrossAccount: true},
	}}}

	if settled := loop.settleSpeedFirstCutoverError(context.Background(), &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID: "a-1", AccountName: "a-1", Message: "首字超时",
	}, gatewayproxyhealth.LatencyDimensionFirstByte, 0); !settled {
		t.Fatal("锁死窗口内速度切号必须交接结算")
	}
	if fake.hijackCalls != 1 || conn.closeCalls != 1 {
		t.Fatalf("hijackCalls=%d closeCalls=%d", fake.hijackCalls, conn.closeCalls)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("真断开时不得写 503：%#v", sink.inputs)
	}
	if !audit.has("account_lock_cutover_denied") {
		t.Fatalf("缺少 account_lock_cutover_denied 审计：%v", audit.labels)
	}
}

// SSE 传输头已提交但底层 Hijack 运行期失败：无法回退状态码，审计必须记录
// 降级（不得声称写入 503）。
func TestRenderClientHandoffRecordsCommittedHijackFailure(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.req = switchTargetRequest(t, "/v1/chat/completions",
		`{"model":"gpt-test","stream":true}`, "gpt-test", true)
	fake := &interruptFakeWriter{conn: &interruptFakeConn{}, sink: &strings.Builder{}, hijackErr: errors.New("hijack refused")}
	loop.res = gatewaypreauth.NewTrackingWriter(fake)
	loop.res.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	loop.res.WriteHeader(http.StatusOK)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit

	loop.renderClientHandoff(streamServerRetryExhaustedInput{
		message:      "账户处于锁死窗口，窗口内不切换其他账户，请客户端重试",
		retryReason:  "account_lock_hold",
		errorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
		usageContext: gatewaypreauth.GatewayFailureUsageContext{},
	})
	payload := audit.payloadOf("gateway_client_handoff_transport")
	if payload["handoff"] != "stream_headers_committed_hijack_failed" {
		t.Fatalf("handoff = %#v，已提交 SSE 头后 Hijack 失败必须记录降级", payload["handoff"])
	}
}

// speed_first 结算期锁状态读取失败：已观察锁行时 fail-closed 交接。
func TestSettleSpeedFirstCutoverLockReadFailureFailClosedWhenObserved(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn, sink: &strings.Builder{}}
	loop.res = gatewaypreauth.NewTrackingWriter(fake)
	audit := &chainLockHoldAudit{}
	loop.auditCapture = audit
	loop.c.engine = &gatewaydispatch.Engine{Locks: &chainLockHoldLocks{err: context.DeadlineExceeded}}
	loop.accountLockObservation = &gatewaydispatch.AccountLockObservation{}

	if settled := loop.settleSpeedFirstCutoverError(context.Background(), &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID: "a-1", AccountName: "a-1", Message: "首字超时",
	}, gatewayproxyhealth.LatencyDimensionFirstByte, 0); !settled {
		t.Fatal("已观察锁行时读取失败必须 fail-closed 交接")
	}
	if fake.hijackCalls != 1 || conn.closeCalls != 1 {
		t.Fatalf("hijackCalls=%d closeCalls=%d", fake.hijackCalls, conn.closeCalls)
	}
	payload := audit.payloadOf("account_lock_cutover_denied")
	if payload["readFailed"] != true {
		t.Fatalf("readFailed = %#v", payload["readFailed"])
	}
}
