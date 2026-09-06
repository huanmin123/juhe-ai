package main

// 第六轮审查（chain_v1 / transport 批次）回归测试：
//
//  1. 耗尽出口文案分流（Node sendStreamServerRetryExhaustedResponse
//     routes.ts:2966-3046 + sendPreCommitStreamRetryExhaustedResponse:3109）：
//     客户端 payload 只允许两段固定文案，内部策略/管道原文只进 audit。
//  2. client-IP 回避终态确认接线（Node
//     confirmCurrentClientIpAccountAvoidanceAfterFinalFailure，routes.ts:2332 /
//     :2374 / :2576+2619）：四个终态位点必须把 tracker 的 pending failures
//     落成回避条目。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
)

// recordingMetadataCapture 记录 AddGatewayMetadata 调用的 capture stub。
type recordingMetadataCapture struct {
	labels   []string
	metadata []map[string]any
}

func (c *recordingMetadataCapture) BindContext(gatewaypreauth.AuditGatewayContext) {}
func (c *recordingMetadataCapture) AddGatewayMetadata(label string, metadata map[string]any) {
	c.labels = append(c.labels, label)
	c.metadata = append(c.metadata, metadata)
}
func (c *recordingMetadataCapture) Finalize(gatewaypreauth.AuditFinalizeInput) {}

func (c *recordingMetadataCapture) byLabel(label string) map[string]any {
	for i, candidate := range c.labels {
		if candidate == label {
			return c.metadata[i]
		}
	}
	return nil
}

// newAvoidanceTestLoop 构造带真实 client-IP avoidance 服务的 dispatch loop。
func newAvoidanceTestLoop(t *testing.T, sink *recordingFailureSink, capture *recordingMetadataCapture) (*v1DispatchLoop, *gatewayclientip.Avoidance, *gatewayclientip.AvoidanceTracker) {
	t.Helper()
	observability := newSlogObservability(slog.New(slog.NewTextHandler(io.Discard, nil)), gatewaypreauth.SystemClock{})
	avoidance := mustAvoidance(t)
	service := &gatewaypreauth.Service{
		Responses:        sink,
		Observability:    observability,
		Clock:            gatewaypreauth.SystemClock{},
		AccountAvoidance: avoidance,
	}
	chain := &gatewayChain{preauth: service, observability: observability}
	loop := newV1TestLoop(t, sink)
	loop.c = chain
	loop.auditCapture = capture
	tracker := avoidance.CreateAvoidanceTracker(gatewayclientip.AvoidanceScopeInput{
		SystemAccountID: "sys_1",
		APIKeyID:        "key_1",
		ClientIP:        "203.0.113.9",
	})
	loop.current.ClientIPAccountAvoidance = tracker
	return loop, avoidance, tracker
}

func mustAvoidance(t *testing.T) *gatewayclientip.Avoidance {
	t.Helper()
	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{})
	if err != nil {
		t.Fatalf("new avoidance: %v", err)
	}
	t.Cleanup(avoidance.Close)
	return avoidance
}

// rememberAvoidanceFailure 在 tracker 上挂一条 pending 账户失败。
func rememberAvoidanceFailure(avoidance *gatewayclientip.Avoidance, tracker *gatewayclientip.AvoidanceTracker, accountID string) {
	avoidance.RememberPendingFailure(tracker, accountID, "账户"+accountID, gatewayclientip.AccountFailure{
		ErrorPhase:   "stream",
		ErrorCode:    "upstream_retryable_error",
		ErrorMessage: "上游流中断",
	})
}

func avoidanceRow(avoidance *gatewayclientip.Avoidance, accountID string) (gatewayclientip.AvoidanceSnapshotRow, bool) {
	for _, row := range avoidance.SnapshotForTest() {
		if row.AccountID == accountID {
			return row, true
		}
	}
	return gatewayclientip.AvoidanceSnapshotRow{}, false
}

// TestV1ExhaustedExitFixedCopyProtocolSignal：preCommitFailureSignal =
// protocol_error_event → 客户端 payload 是「上游流式响应在输出前失败，请重试」
// （responses.ts:190 gatewayStreamClientRetryMessage），内部原文只进 audit；
// audit 走 sendPreCommit 的 stream_failed/stream 形态并补 errorCode。
func TestV1ExhaustedExitFixedCopyProtocolSignal(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = capture
	loop.current.ClientStrategy = gatewaypreauth.ClientStrategyContext{
		ClientProfile:      "codex_cli",
		DownstreamProtocol: "responses_sse",
		Opaque: gatewaycodex.OpenAIGatewayClientStrategyContext{
			RetryCoordination: gatewaycodex.GatewayClientRetryCoordination{
				PreCommitFailureSignal: gatewaycodex.FailureSignalProtocolErrorEvent,
			},
		},
	}

	loop.sendStreamServerRetryExhaustedResponse(streamServerRetryExhaustedInput{
		message:        "响应管道在提交前中断：account:acc_9",
		retryReason:    gatewayresponse.StreamServerRetryUpstreamProtocolFailure,
		errorCode:      "",
		usageContext:   loop.current.UsageContext,
		clientStrategy: &loop.current.ClientStrategy,
	})

	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	exit := sink.inputs[0]
	if exit.ResponsePayload.Error.Message != "上游流式响应在输出前失败，请重试" {
		t.Fatalf("client payload = %q", exit.ResponsePayload.Error.Message)
	}
	if strings.Contains(exit.ResponsePayload.Error.Message, "account:acc_9") {
		t.Fatal("internal pipeline copy leaked to the client payload")
	}
	if exit.Audit.Outcome != gatewaypreauth.AuditOutcomeStreamFailed || exit.Audit.ErrorPhase != "stream" {
		t.Fatalf("audit shape = %s/%s", exit.Audit.Outcome, exit.Audit.ErrorPhase)
	}
	if exit.Audit.ErrorMessage != "响应管道在提交前中断：account:acc_9" {
		t.Fatalf("audit errorMessage = %q", exit.Audit.ErrorMessage)
	}
	metadata := capture.byLabel("stream_server_retry_exhausted")
	if metadata == nil {
		t.Fatal("stream_server_retry_exhausted metadata missing")
	}
	if metadata["retryReason"] != gatewayresponse.StreamServerRetryUpstreamProtocolFailure {
		t.Fatalf("retryReason not forwarded: %v", metadata["retryReason"])
	}
	if metadata["errorCode"] != gatewaypreauth.GatewayStreamClientRetryErrorCode {
		t.Fatalf("errorCode fallback missing: %v", metadata["errorCode"])
	}
	if metadata["responseMode"] != "pre_commit_http_error" {
		t.Fatalf("responseMode = %v", metadata["responseMode"])
	}
}

// TestV1ExhaustedExitFixedCopyWithoutSignal：无 signal → 客户端固定
// 「上游暂时不可用，请重试」，空 message 时 audit 回退
// 「服务端流式重试未找到可用账号」，metadata 透传实际 retryReason 与
// upstreamErrorCode。
func TestV1ExhaustedExitFixedCopyWithoutSignal(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = capture

	loop.sendStreamServerRetryExhaustedResponse(streamServerRetryExhaustedInput{
		message:        "",
		retryReason:    gatewayresponse.StreamServerRetryResponseInspection,
		errorCode:      "inspected_overloaded",
		usageContext:   loop.current.UsageContext,
		clientStrategy: &loop.current.ClientStrategy,
	})

	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	exit := sink.inputs[0]
	if exit.ResponsePayload.Error.Message != "上游暂时不可用，请重试" {
		t.Fatalf("client payload = %q", exit.ResponsePayload.Error.Message)
	}
	if exit.Audit.Outcome != gatewaypreauth.AuditOutcomeUpstreamFailed || exit.Audit.ErrorPhase != "dispatch" {
		t.Fatalf("audit shape = %s/%s", exit.Audit.Outcome, exit.Audit.ErrorPhase)
	}
	if exit.Audit.ErrorMessage != "服务端流式重试未找到可用账号" {
		t.Fatalf("audit fallback message = %q", exit.Audit.ErrorMessage)
	}
	metadata := capture.byLabel("stream_server_retry_exhausted")
	if metadata == nil {
		t.Fatal("stream_server_retry_exhausted metadata missing")
	}
	if metadata["retryReason"] != gatewayresponse.StreamServerRetryResponseInspection {
		t.Fatalf("retryReason not forwarded: %v", metadata["retryReason"])
	}
	if metadata["upstreamErrorCode"] != "inspected_overloaded" {
		t.Fatalf("upstreamErrorCode missing: %v", metadata["upstreamErrorCode"])
	}
}

// TestV1AvoidanceConfirmedAtNoDispatchChange：位点 1（routes.ts:2332）——
// response_inspection no_dispatch_change 终态把 pending failure 落成回避条目。
func TestV1AvoidanceConfirmedAtNoDispatchChange(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop, avoidance, tracker := newAvoidanceTestLoop(t, sink, capture)
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	rememberAvoidanceFailure(avoidance, tracker, "acc_9")

	settled := loop.settleResponseStreamServerRetry(context.Background(),
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		inspectionRetryHandling(false))
	if !settled {
		t.Fatal("no-dispatch-change retry must settle the request")
	}
	row, ok := avoidanceRow(avoidance, "acc_9")
	if !ok {
		t.Fatalf("pending failure not confirmed at the no_dispatch_change site: %v", avoidance.SnapshotForTest())
	}
	if row.FailureCount != 1 || !strings.Contains(row.ClientIP, "203.0.113.9") {
		t.Fatalf("unexpected entry: %+v", row)
	}
	// Node routes.ts:2716-2723: the confirm lands on the audit surface.
	if metadata := capture.byLabel("client_ip_account_avoidance_update"); metadata == nil {
		t.Fatal("client_ip_account_avoidance_update metadata missing")
	} else if metadata["reason"] != "response_inspection_no_dispatch_change" {
		t.Fatalf("reason = %v", metadata["reason"])
	}
	// The pending list is drained (Node confirmTrackerPendingFailures clears it).
	if len(tracker.PendingFailures) != 0 {
		t.Fatalf("pending failures not cleared: %v", tracker.PendingFailures)
	}
}

// TestV1AvoidanceConfirmedAtStreamServerRetryExhausted：位点 2（routes.ts:2374）
// ——换号不可行的耗尽终态同样确认。
func TestV1AvoidanceConfirmedAtStreamServerRetryExhausted(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop, avoidance, tracker := newAvoidanceTestLoop(t, sink, capture)
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}}
	rememberAvoidanceFailure(avoidance, tracker, "acc_9")

	settled := loop.settleResponseStreamServerRetry(context.Background(),
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		inspectionRetryHandling(true))
	if !settled {
		t.Fatal("exhausted retry must settle the request")
	}
	metadata := capture.byLabel("client_ip_account_avoidance_update")
	if metadata == nil {
		t.Fatal("confirm metadata missing at the stream_server_retry_exhausted site")
	}
	if metadata["reason"] != "stream_server_retry_exhausted" {
		t.Fatalf("reason = %v", metadata["reason"])
	}
	if _, ok := avoidanceRow(avoidance, "acc_9"); !ok {
		t.Fatalf("pending failure not confirmed: %v", avoidance.SnapshotForTest())
	}
}

// TestV1AvoidanceConfirmedAtDispatchFailureExits：位点 3+4（routes.ts:2576+
// 2619 的 Go 等价终态）——dispatch 耗尽与 unexpected failure 渲染前确认；
// 连续两次终态失败把条目推到激活阈值（FailureCount=2）。
func TestV1AvoidanceConfirmedAtDispatchFailureExits(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop, avoidance, tracker := newAvoidanceTestLoop(t, sink, capture)
	rememberAvoidanceFailure(avoidance, tracker, "acc_9")

	loop.renderDispatchExhausted(context.Background(), &gatewaydispatch.UpstreamAttemptError{
		Message:          "failed",
		LastAttempt:      &gatewaydispatch.UpstreamAttempt{AccountID: "acc_1", UpstreamURL: "https://upstream", HasStatus: true, Status: 500},
		FailedAccountIDs: []string{"acc_1"},
	})
	row, ok := avoidanceRow(avoidance, "acc_9")
	if !ok || row.FailureCount != 1 {
		t.Fatalf("dispatch-exhausted confirm missing: %+v ok=%v", avoidance.SnapshotForTest(), ok)
	}

	rememberAvoidanceFailure(avoidance, tracker, "acc_9")
	loop.renderUnexpectedDispatchFailure(context.Background(), errors.New("意外的调度故障"))
	row, ok = avoidanceRow(avoidance, "acc_9")
	if !ok || row.FailureCount != 2 || !row.Active {
		t.Fatalf("unexpected-failure confirm missing or threshold not reached: %+v ok=%v", row, ok)
	}
	if metadata := capture.byLabel("client_ip_account_avoidance_update"); metadata == nil {
		t.Fatal("confirm metadata missing at the gateway_failure_response sites")
	} else if metadata["reason"] != "gateway_failure_response" {
		t.Fatalf("reason = %v", metadata["reason"])
	}
}

// TestV1AvoidanceConfirmSkipsDiagnosticTraffic：Node routes.ts:2695 —— 诊断
// 流量不写 client-IP 回避状态。
func TestV1AvoidanceConfirmSkipsDiagnosticTraffic(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop, avoidance, tracker := newAvoidanceTestLoop(t, sink, capture)
	loop.current.UsageContext.TrafficSource = "manual_account_test"
	rememberAvoidanceFailure(avoidance, tracker, "acc_9")

	loop.renderUnexpectedDispatchFailure(context.Background(), errors.New("意外的调度故障"))
	if rows := avoidance.SnapshotForTest(); len(rows) != 0 {
		t.Fatalf("diagnostic traffic must not confirm avoidance: %v", rows)
	}
	if metadata := capture.byLabel("client_ip_account_avoidance_update"); metadata != nil {
		t.Fatalf("diagnostic traffic wrote confirm metadata: %v", metadata)
	}
}

// TestV1AvoidanceConfirmRequiresClientIP：无 client IP 的 scope（normalizeScope
// 返回 nil）不产生条目，终态照常渲染。
func TestV1AvoidanceConfirmRequiresClientIP(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop, avoidance, _ := newAvoidanceTestLoop(t, sink, capture)
	tracker := avoidance.CreateAvoidanceTracker(gatewayclientip.AvoidanceScopeInput{ClientIP: ""})
	loop.current.ClientIPAccountAvoidance = tracker
	rememberAvoidanceFailure(avoidance, tracker, "acc_9")

	loop.renderUnexpectedDispatchFailure(context.Background(), errors.New("意外的调度故障"))
	if rows := avoidance.SnapshotForTest(); len(rows) != 0 {
		t.Fatalf("scopeless tracker must not confirm: %v", rows)
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("terminal render missing: inputs=%d", len(sink.inputs))
	}
	if sink.inputs[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", sink.inputs[0].StatusCode)
	}
}

// TestV1AvoidanceConfirmIgnoredWithoutService：loop 未装配 avoidance 服务
// （compose 测试的 stub 链）时终态照常渲染，不 panic。
func TestV1AvoidanceConfirmIgnoredWithoutService(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = capture

	loop.renderUnexpectedDispatchFailure(context.Background(), errors.New("意外的调度故障"))
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	if capture.byLabel("client_ip_account_avoidance_update") != nil {
		t.Fatal("no service must mean no confirm metadata")
	}
}
