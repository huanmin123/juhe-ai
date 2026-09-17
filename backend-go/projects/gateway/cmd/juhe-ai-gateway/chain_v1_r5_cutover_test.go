package main

// R5（速度优先非流式首字截止）切号消费端：响应面 configured_deadline abort
// 的切号 verdict 产生（handleUpstreamResponse err 特判 + TransferForCutover）
// 与消费（settleFirstByteDeadlineCutoverVerdict → 既有
// settleSpeedFirstCutoverError 的收窄重派 / 耗尽退出臂）。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// TestV1R5FirstByteDeadlineCutoverVerdict：非流式竞速 configured_deadline
// abort 冒泡 → err 特判转切号 verdict（不渲染固定 503）。coordinator 零值
// state 未初始化为 active（既有缺陷，见报告），TransferForCutover 返回 nil，
// verdict 携带空预留走 settle 耗尽退出臂；预留携带场景由 settle 联动测试覆盖。
func TestV1R5FirstByteDeadlineCutoverVerdict(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	// 完整非流式管道会调用 ShouldCaptureSuccessPayloads：挂真实
	// gatewayusage capture（零值安全，false），stub capture 会经
	// responseAuditCaptureOf 落成 nil 接口。
	loop.auditCapture = preauthAuditCapture{inner: &gatewayusage.AuditCaptureContext{}}
	bodyReader, bodyWriter := io.Pipe()
	defer bodyWriter.Close()

	coordinator := &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}

	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "acc_slow", Name: "慢账户"},
		Response: gatewaydispatch.NewGatewayUpstreamResponseForTransform(200,
			http.Header{"Content-Type": []string{"application/json"}}, bodyReader),
		NormalRouteFirstByteDeadline: &gatewayrouting.NormalRouteAttemptFirstByteDeadline{
			ConfiguredDeadlineMs: 60,
			EffectiveDeadlineMs:  60,
			LimitingFactor:       gatewayrouting.FirstByteLimitingFactorConfigured,
		},
		FirstByteDeadlineCoordinator: coordinator,
		OnFirstByteDeadline: func(gatewaydispatch.FirstByteDeadlineDecisionInput) gatewaydispatch.FirstByteDeadlineAction {
			return gatewaydispatch.FirstByteDeadlineActionAbort
		},
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req := gatewaypreauth.NewGatewayRequest(request)
	recorder := httptest.NewRecorder()
	res := gatewaypreauth.NewTrackingWriter(recorder)

	handling := loop.c.handleUpstreamResponse(req, res, loop.auditCapture, loop.current, dispatched, loop.startedAt,
		gatewayruntimecache.GatewaySettings{}, requestBudgets{}, nil)

	if !handling.FirstByteDeadlineCutover {
		t.Fatalf("必须产生切号 verdict: %+v", handling)
	}
	if handling.RetryUpstream || handling.AlreadyFinalized {
		t.Fatalf("verdict 不得混入其他分支: %+v", handling)
	}
	if handling.ErrorCode != "first_byte_timeout" || !strings.Contains(handling.Message, "后仍未返回首个字节") {
		t.Fatalf("errorCode=%q message=%q", handling.ErrorCode, handling.Message)
	}
	if recorder.Code == http.StatusServiceUnavailable {
		t.Fatalf("verdict 路径不得渲染固定 503: status=%d", recorder.Code)
	}
	if view, ok := handling.CutoverReservationView.(*gatewaydispatch.SpeedFirstCutoverReservationView); ok && view != nil {
		t.Fatalf("零值协调器 Transfer 应返回 nil 指针: %+v", view)
	}
}

// TestV1R5SettleFirstByteDeadlineCutoverVerdictCarriesReservation：verdict →
// cutover error 映射走既有 settle 收窄重派臂：预留接回循环携带、窗口收窄到
// 保留目标、慢账户进入排除集、写 normal_route_speed_first_retry_dispatch 审计。
func TestV1R5SettleFirstByteDeadlineCutoverVerdictCarriesReservation(t *testing.T) {
	sink := &recordingFailureSink{}
	capture := &recordingMetadataCapture{}
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = capture
	loop.c.engine = &gatewaydispatch.Engine{Locks: &w1vLocksStub{}}

	reservation := w1vReserveCutoverReservation(t, "acc_target")
	released := false
	view := &gatewaydispatch.SpeedFirstCutoverReservationView{
		TargetAccountIDValue: "acc_target",
		ReleaseFunc:          func() { released = true; reservation.Release() },
	}
	loop.recordAttachedSpeedFirstReservation(view, reservation)

	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "acc_slow", Name: "慢账户"},
		NormalRouteFirstByteDeadline: &gatewayrouting.NormalRouteAttemptFirstByteDeadline{
			EffectiveDeadlineMs: 10_000,
			LimitingFactor:      gatewayrouting.FirstByteLimitingFactorConfigured,
		},
	}
	handling := gatewayresponse.UpstreamResponseHandlingResult{
		FirstByteDeadlineCutover: true,
		CutoverReservationView:   view,
		ErrorCode:                "first_byte_timeout",
		Message:                  "上游非流式响应 10s 后仍未返回首个字节",
	}

	if settled := loop.settleFirstByteDeadlineCutoverVerdict(context.Background(), dispatched, handling); settled {
		t.Fatal("有保留目标应收窄窗口继续派发")
	}
	if loop.speedFirstByteRetryCount != 1 {
		t.Fatalf("切号计数 = %d，要求恰好 1", loop.speedFirstByteRetryCount)
	}
	if _, reserved := loop.speedFirstRetryCandidateAccountIds["acc_target"]; !reserved {
		t.Fatalf("保留窗口未收窄: %v", loop.speedFirstRetryCandidateAccountIds)
	}
	if _, excluded := loop.streamRetryExcludedAccounts["acc_slow"]; !excluded {
		t.Fatalf("慢账户未进入排除集: %v", loop.streamRetryExcludedAccounts)
	}
	if loop.speedFirstCutoverReservation == nil {
		t.Fatal("预留应接回循环携带状态")
	}
	if released {
		t.Fatal("收窄重派臂不应释放预留")
	}
	metadata := capture.byLabel("normal_route_speed_first_retry_dispatch")
	if metadata == nil {
		t.Fatalf("缺少切号重派审计: %v", capture.labels)
	}
	loop.releasePendingSpeedFirstReservation()
}

// TestV1R5SettleFirstByteDeadlineCutoverVerdictNilDeadlineGuard：无截止上下文
// 的 verdict 为恒不可达守卫臂——按耗尽契约渲染并结算，避免空 200。
func TestV1R5SettleFirstByteDeadlineCutoverVerdictNilDeadlineGuard(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	handling := gatewayresponse.UpstreamResponseHandlingResult{
		FirstByteDeadlineCutover: true,
		ErrorCode:                "first_byte_timeout",
		Message:                  "上游非流式响应 10s 后仍未返回首个字节",
	}
	settled := loop.settleFirstByteDeadlineCutoverVerdict(context.Background(),
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_slow", Name: "慢账户"}}, handling)
	if !settled {
		t.Fatal("守卫臂必须结算请求")
	}
	if len(sink.inputs) == 0 {
		t.Fatal("守卫臂必须渲染耗尽契约")
	}
}
