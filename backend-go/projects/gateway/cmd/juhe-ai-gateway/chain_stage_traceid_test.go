package main

// 本任务三条接线的定向回归（既有断言零改动）：
//  1. body.speed_first_admission 发射点 fields 带 kernel 上下文 traceId；
//  2. audit.finalize 经 newAuditCapture（唯一生产构造点）接线的
//     chainAuditStageLogger 入库 kernel 请求累积器；
//  3. upstream.dispatch.failed 阶段起点取本轮派发起点 roundStartedAtMs，
//     未记录（0）时回落请求 startedAt（旧行为）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// stageTraceGatewayRequest 让 req.HTTP 经过 kernel.RequestContextMiddleware，
// 携带 X-Trace-Id 派生的固定 traceId（/v1 链入口同款绑定方式）。
func stageTraceGatewayRequest(t *testing.T, method, target, traceID string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	var captured *gatewaypreauth.GatewayRequest
	handler := kernel.RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = gatewaypreauth.NewGatewayRequest(r)
	}))
	request := httptest.NewRequest(method, target, strings.NewReader(`{"model":"gpt-test"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Trace-Id", traceID)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if captured == nil {
		t.Fatal("kernel 中间件未产出请求")
	}
	return captured
}

// TestChainSpeedFirstAdmissionStageCarriesKernelTraceID 锁定
// body.speed_first_admission 发射点的 traceId 字段（skipped 与 success 两个
// 代表发射点；同一 traceID 变量贯穿其余发射点）。
func TestChainSpeedFirstAdmissionStageCarriesKernelTraceID(t *testing.T) {
	const traceID = "trace_speed_kernel"
	t.Run("skipped 发射点", func(t *testing.T) {
		gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
		defer gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
		obs := &chainCapturedObservability{}
		gate := newChainSpeedFirstGateForTest(obs)
		req := stageTraceGatewayRequest(t, http.MethodPost, "/v1/chat/completions", traceID)
		res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())

		outcome, err := gate.AdmitBody(context.Background(), req, res, gatewayproto.LaneText)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		if outcome.Handled || outcome.Release != nil {
			t.Fatalf("outcome = %+v, want a plain pass-through", outcome)
		}
		for _, stage := range obs.snapshotStages() {
			if stage.stage == "body.speed_first_admission" && stage.outcome == "skipped" {
				if stage.fields["traceId"] != traceID {
					t.Fatalf("skipped fields traceId = %v, want %q", stage.fields["traceId"], traceID)
				}
				return
			}
		}
		t.Fatalf("missing skipped stage: %+v", obs.snapshotStages())
	})
	t.Run("success 发射点", func(t *testing.T) {
		gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
		defer gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
		obs := &chainCapturedObservability{}
		gate := newChainSpeedFirstGateForTest(obs)
		req := stageTraceGatewayRequest(t, http.MethodPost, "/v1/chat/completions", traceID)
		req.Runtime = speedFirstRuntime(nil, nil, nil, nil, 1)
		res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())

		outcome, err := gate.AdmitBody(context.Background(), req, res, gatewayproto.LaneText)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		if outcome.Handled || outcome.Release == nil {
			t.Fatalf("outcome = %+v, want an admitted lease", outcome)
		}
		defer outcome.Release()
		for _, stage := range obs.snapshotStages() {
			if stage.outcome == "success" && stage.fields["acquired"] == true {
				if stage.fields["traceId"] != traceID {
					t.Fatalf("success fields traceId = %v, want %q", stage.fields["traceId"], traceID)
				}
				return
			}
		}
		t.Fatalf("missing success stage: %+v", obs.snapshotStages())
	})
}

// TestNewAuditCaptureWiresAuditFinalizeStageIntoAccumulator 锁定
// newAuditCapture 已接线 chainAuditStageLogger：即使未装配 settings 的停用
// capture，Finalize 的 audit.finalize|skipped 也必须经共享发射面入库
// kernel 请求累积器（fields 自带 capture traceId）。
func TestNewAuditCaptureWiresAuditFinalizeStageIntoAccumulator(t *testing.T) {
	const traceID = "trace_audit_finalize_wiring"
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	req := gatewaypreauth.NewGatewayRequest(request)
	chain := &gatewayChain{}
	capture := chain.newAuditCapture(req, traceID, 1728000000000)
	t.Cleanup(func() { gatewaypreauth.CancelAuditCapture(capture) })

	recorder := &kernel.RequestContext{TraceID: traceID, StartedAt: time.Now().Add(-time.Second)}
	chainRegisterRequestStageRecorder(traceID, recorder)
	defer chainUnregisterRequestStageRecorder(traceID, recorder)

	capture.Finalize(gatewaypreauth.AuditFinalizeInput{})

	accumulation := recorder.RequestStageAccumulation()
	for _, stage := range accumulation.Stages {
		if stage.Stage == "audit.finalize" {
			if stage.Outcome != "skipped" {
				t.Fatalf("audit.finalize outcome = %q, want skipped（未装配 settings）", stage.Outcome)
			}
			return
		}
	}
	t.Fatalf("audit.finalize 未入库: %+v", accumulation)
}

// TestRenderDispatchExhaustedStageUsesRoundStartedAt 锁定耗尽埋点的阶段起点
// 口径：roundStartedAtMs 已记录时以本轮派发起点为基准；未记录（0）回落请求
// startedAt（旧行为，harness 固定 2024 起点产生巨量 durationMs）。
func TestRenderDispatchExhaustedStageUsesRoundStartedAt(t *testing.T) {
	const traceID = "trace_dispatch_round"
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.traceID = traceID
	recorder := &kernel.RequestContext{TraceID: traceID, StartedAt: time.Now().Add(-10 * time.Second)}
	chainRegisterRequestStageRecorder(traceID, recorder)
	defer chainUnregisterRequestStageRecorder(traceID, recorder)

	// roundStartedAtMs 生效：阶段起点=本轮派发起点（ctx 起点后 5 秒），
	// durationMs 约 5s 量级。
	loop.roundStartedAtMs = time.Now().Add(-5 * time.Second).UnixMilli()
	loop.renderDispatchExhausted(context.Background(), &gatewaydispatch.UpstreamAttemptError{Message: "上游账户请求失败"})
	stages := recorder.RequestStageAccumulation().Stages
	if len(stages) == 0 {
		t.Fatal("upstream.dispatch.failed 未入库")
	}
	roundBased := stages[len(stages)-1]
	if roundBased.Stage != "upstream.dispatch.failed" {
		t.Fatalf("stage = %q, want upstream.dispatch.failed", roundBased.Stage)
	}
	if roundBased.StartedOffsetMs < 4_000 || roundBased.StartedOffsetMs > 6_000 {
		t.Fatalf("round 生效时 StartedOffsetMs = %d, want ~5000（本轮派发起点）", roundBased.StartedOffsetMs)
	}
	if roundBased.DurationMs < 4_000 || roundBased.DurationMs > 60_000 {
		t.Fatalf("round 生效时 DurationMs = %d, want ~5000（本轮派发起点的实测时长）", roundBased.DurationMs)
	}

	// roundStartedAtMs = 0：回落请求 startedAt（2024 起点）→ offset 被钳到 0、
	// durationMs 为巨量值（旧口径）。
	loop.roundStartedAtMs = 0
	loop.renderDispatchExhausted(context.Background(), &gatewaydispatch.UpstreamAttemptError{Message: "上游账户请求失败"})
	stages = recorder.RequestStageAccumulation().Stages
	fallback := stages[len(stages)-1]
	if fallback.Stage != "upstream.dispatch.failed" {
		t.Fatalf("stage = %q, want upstream.dispatch.failed", fallback.Stage)
	}
	if fallback.StartedOffsetMs != 0 {
		t.Fatalf("round=0 回落请求 startedAt 时 StartedOffsetMs = %d, want 0（负偏移钳制）", fallback.StartedOffsetMs)
	}
	if fallback.DurationMs <= 1_000_000_000 {
		t.Fatalf("round=0 回落时 DurationMs = %d, want 巨量值（2024 起点）", fallback.DurationMs)
	}
}
