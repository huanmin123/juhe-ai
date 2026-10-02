package main

// Node 迁移接线对照族的回归测试（BUG-0266 排查发现的同型断线，对齐 Node
// routes.ts 的三处链面接线）：
//
//   - clientStrategyViewOf 投影：InterpretSemantics 三画像门
//     （client-profiles/strategy.ts 的 gatewayClientAllowsUpstreamSemantic
//     Interpretation）+ CodexCompactionExpected / RetryPreCommitProtocolError /
//     AllowClientSourceAccountAvoidance 从 G18 Opaque 上下文投影；
//   - codex 压缩 SSE 的派发等待期保活（routes.ts:1229-1239：10s 间隔
//     compaction 保活块，startCompactSseWaitHeartbeat）；
//   - 半开探测租约的成功确认（routes.ts:2484 confirmHalfOpenSuccess）。
//
// 释放兜底（routes.ts:2520 releaseHalfOpenLease）在 run 循环响应轮 defer 内
// 内联，engine 为具体类型不可 mock、run 级测试需整套账户/上游 fixture——
// 当前无断言级覆盖，由成功臂 confirm 测试 + 引擎回调幂等语义
//（suppression.go 的 leaseID 匹配释放，二次调用 no-op）共同保证行为面。

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// TestV1WaitForRecoverableFailures：等待偏好回正（Node routes.ts:1281-1283）
// ——多分组未切完默认 false（引擎立即上抛、链面先切备用分组）；单分组与
// 切组跳数达上限时 true（无可切、只能等）；强制等待置位覆盖一切。
func TestV1WaitForRecoverableFailures(t *testing.T) {
	multiGroupContext := func(bindings int) *gatewaypreauth.DispatchContext {
		rows := make([]gatewayruntimecache.GatewayAPIKeyGroupBindingRow, bindings)
		return &gatewaypreauth.DispatchContext{
			APIKeyRecord: &gatewayruntimecache.GatewayAPIKeyRow{GroupBindings: rows},
		}
	}

	t.Run("multi_group_unswitched_defaults_to_switch_first", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		if loop.waitForRecoverableFailures(multiGroupContext(2)) {
			t.Fatal("多分组未切完必须默认先切组（引擎不等待）")
		}
	})

	t.Run("single_group_waits", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		if !loop.waitForRecoverableFailures(multiGroupContext(1)) {
			t.Fatal("单分组无可切必须等待")
		}
	})

	t.Run("no_api_key_record_waits", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		if !loop.waitForRecoverableFailures(&gatewaypreauth.DispatchContext{}) {
			t.Fatal("无 API Key 记录（bindings=0）必须等待（Node ?? 0 同义）")
		}
	})

	t.Run("fallback_switches_reaching_limit_waits", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		loop.fallbackSwitches = 1
		if !loop.waitForRecoverableFailures(multiGroupContext(2)) {
			t.Fatal("切组跳数已达分组上限（bindings-1）后无可切必须等待")
		}
	})

	t.Run("force_flag_overrides", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		loop.forceRecoverableFailureWait = true
		if !loop.waitForRecoverableFailures(multiGroupContext(5)) {
			t.Fatal("强制等待置位必须覆盖切组优先")
		}
	})
}

// TestV1ClientStrategyViewProjection：投影源必须是 preflight 冻结的 G18 上下文
// （strategy.Opaque），语义解释门收窄到 codex / claude_code / gemini_cli 三画像
// （此前恒 true 的死逻辑），Opaque 缺失时压缩期望与重试信号 fail-closed。
func TestV1ClientStrategyViewProjection(t *testing.T) {
	opaqueContext := func(compactionExpected bool, signal string) gatewaycodex.OpenAIGatewayClientStrategyContext {
		return gatewaycodex.OpenAIGatewayClientStrategyContext{
			ClientProfile:                     "codex",
			CodexCompactionExpected:           compactionExpected,
			AllowClientSourceAccountAvoidance: true,
			RetryCoordination: gatewaycodex.GatewayClientRetryCoordination{
				PreCommitFailureSignal: signal,
			},
		}
	}

	t.Run("codex_compaction_full_projection", func(t *testing.T) {
		view := clientStrategyViewOf(&gatewaypreauth.DispatchContext{
			ClientStrategy: gatewaypreauth.ClientStrategyContext{
				ClientProfile:      "codex",
				DownstreamProtocol: "responses_sse",
				Opaque:             opaqueContext(true, gatewaycodex.FailureSignalProtocolErrorEvent),
			},
		})
		if !view.InterpretSemantics || !view.CodexCompactionExpected ||
			!view.RetryPreCommitProtocolError || !view.AllowClientSourceAccountAvoidance {
			t.Fatalf("G18 上下文字段必须完整投影: %+v", view)
		}
	})

	t.Run("interpretation_gate_three_profiles_only", func(t *testing.T) {
		for _, profile := range []string{"codex", "claude_code", "gemini_cli"} {
			view := clientStrategyViewOf(&gatewaypreauth.DispatchContext{
				ClientStrategy: gatewaypreauth.ClientStrategyContext{ClientProfile: profile},
			})
			if !view.InterpretSemantics {
				t.Fatalf("画像 %s 必须允许上游语义解释", profile)
			}
		}
		for _, profile := range []string{"", "openai", "unknown_client"} {
			view := clientStrategyViewOf(&gatewaypreauth.DispatchContext{
				ClientStrategy: gatewaypreauth.ClientStrategyContext{ClientProfile: profile},
			})
			if view.InterpretSemantics {
				t.Fatalf("画像 %q 不得允许上游语义解释（三画像门）", profile)
			}
		}
	})

	t.Run("non_protocol_error_signal_not_projected", func(t *testing.T) {
		view := clientStrategyViewOf(&gatewaypreauth.DispatchContext{
			ClientStrategy: gatewaypreauth.ClientStrategyContext{
				ClientProfile: "codex",
				Opaque:        opaqueContext(false, gatewaycodex.FailureSignalHTTPError),
			},
		})
		if view.RetryPreCommitProtocolError {
			t.Fatal("http_error 信号不得映射为预提交协议错误重试")
		}
	})

	t.Run("missing_opaque_fails_closed", func(t *testing.T) {
		view := clientStrategyViewOf(&gatewaypreauth.DispatchContext{
			ClientStrategy: gatewaypreauth.ClientStrategyContext{ClientProfile: "codex"},
		})
		if view.CodexCompactionExpected || view.RetryPreCommitProtocolError ||
			view.AllowClientSourceAccountAvoidance {
			t.Fatalf("Opaque 缺失时必须 fail-closed: %+v", view)
		}
		if !view.InterpretSemantics {
			t.Fatal("codex 画像的解释门按 ClientProfile 判定，不依赖 Opaque")
		}
	})
}

// signalingResponseWriter 是线程安全的心跳写观察器：Write 加互斥并计数/发
// 信号（心跳 goroutine 与测试 goroutine 的同步点），替代对非线程安全
// httptest.ResponseRecorder 的直接轮询（-race 下必挂的竞态写法）。
type signalingResponseWriter struct {
	mu      sync.Mutex
	written int
	wrote   chan struct{}
	header  http.Header
}

func newSignalingResponseWriter() *signalingResponseWriter {
	return &signalingResponseWriter{wrote: make(chan struct{}, 16), header: http.Header{}}
}

func (w *signalingResponseWriter) Header() http.Header { return w.header }

func (w *signalingResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.written += len(p)
	select {
	case w.wrote <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (w *signalingResponseWriter) WriteHeader(int) {}

func (w *signalingResponseWriter) totalWritten() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.written
}

// TestV1CompactSseWaitHeartbeat：压缩等待保活的挂载条件（routes.ts:2939-2948
// shouldKeepCodexCompactSseAliveDuringUpstreamWait）与生命周期（Start 立即写
// 首个保活块，Stop 置空；run 收尾 defer 兜底）。
func TestV1CompactSseWaitHeartbeat(t *testing.T) {
	compactionContext := func() *gatewaypreauth.DispatchContext {
		return &gatewaypreauth.DispatchContext{
			ClientStrategy: gatewaypreauth.ClientStrategyContext{
				ClientProfile:      "codex",
				DownstreamProtocol: "responses_sse",
				Opaque: gatewaycodex.OpenAIGatewayClientStrategyContext{
					ClientProfile:           "codex",
					CodexCompactionExpected: true,
				},
			},
		}
	}
	streamRequest := func(t *testing.T) *gatewaypreauth.GatewayRequest {
		t.Helper()
		return bodyAttachedRequest(t, http.MethodPost, "/v1/responses",
			`{"model":"gpt-test","stream":true,"input":[]}`)
	}

	t.Run("compaction_sse_starts_and_writes_keepalive", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		loop.req = streamRequest(t)
		writer := newSignalingResponseWriter()
		loop.res = gatewaypreauth.NewTrackingWriter(writer)
		loop.waitCommitState = &gatewayresponse.DownstreamCommitState{}

		loop.startCompactSseWaitHeartbeat(t.Context(), compactionContext())
		if loop.compactWaitHeartbeat == nil {
			t.Fatal("codex 压缩 SSE 等待保活必须挂载")
		}
		// Start 首个保活块异步写出：经信号 channel 等待（同步点，无竞态）。
		select {
		case <-writer.wrote:
		case <-time.After(2 * time.Second):
			t.Fatal("等待保活必须在等待期写出保活块")
		}
		loop.stopCompactSseWaitHeartbeat()
		if loop.compactWaitHeartbeat != nil {
			t.Fatal("停止后心跳实例必须置空")
		}
		if writer.totalWritten() == 0 {
			t.Fatal("停止前必须已有保活块写出")
		}
	})

	t.Run("non_compaction_request_not_mounted", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		loop.req = streamRequest(t)
		loop.waitCommitState = &gatewayresponse.DownstreamCommitState{}
		nonCompaction := compactionContext()
		nonCompaction.ClientStrategy.Opaque = gatewaycodex.OpenAIGatewayClientStrategyContext{
			ClientProfile: "codex",
		}
		loop.startCompactSseWaitHeartbeat(t.Context(), nonCompaction)
		if loop.compactWaitHeartbeat != nil {
			t.Fatal("codexCompactionExpected=false 不得挂载等待保活")
		}
	})

	t.Run("non_stream_request_not_mounted", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		loop.req = bodyAttachedRequest(t, http.MethodPost, "/v1/responses",
			`{"model":"gpt-test","stream":false,"input":[]}`)
		loop.startCompactSseWaitHeartbeat(t.Context(), compactionContext())
		if loop.compactWaitHeartbeat != nil {
			t.Fatal("非流式请求不得挂载等待保活")
		}
	})

	t.Run("non_codex_profile_not_mounted", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		loop.req = streamRequest(t)
		other := compactionContext()
		other.ClientStrategy.ClientProfile = "openai"
		loop.startCompactSseWaitHeartbeat(t.Context(), other)
		if loop.compactWaitHeartbeat != nil {
			t.Fatal("非 codex 画像不得挂载压缩等待保活")
		}
	})

	t.Run("non_responses_sse_protocol_not_mounted", func(t *testing.T) {
		loop := newV1TestLoop(t, &recordingFailureSink{})
		loop.req = streamRequest(t)
		jsonDownstream := compactionContext()
		jsonDownstream.ClientStrategy.DownstreamProtocol = "openai_json"
		loop.startCompactSseWaitHeartbeat(t.Context(), jsonDownstream)
		if loop.compactWaitHeartbeat != nil {
			t.Fatal("非 responses_sse 下游协议不得挂载压缩等待保活")
		}
	})
}

// TestV1HalfOpenLeaseConfirmOnProtocolSuccess：协议验证成功时必须调用引擎带出
// 的半开成功确认（routes.ts:2484）；未验证成功不触碰。
func TestV1HalfOpenLeaseConfirmOnProtocolSuccess(t *testing.T) {
	newLoop := func() *v1DispatchLoop {
		return newV1TestLoop(t, &recordingFailureSink{})
	}

	t.Run("protocol_success_confirms_half_open", func(t *testing.T) {
		loop := newLoop()
		confirmed := false
		loop.confirmProtocolSuccessSideEffects(t.Context(),
			gatewaydispatch.UpstreamDispatchResult{ConfirmHalfOpenSuccess: func() bool { confirmed = true; return true }},
			gatewayresponse.UpstreamResponseHandlingResult{ProtocolValidatedSuccess: true})
		if !confirmed {
			t.Fatal("协议验证成功必须执行半开成功确认")
		}
	})

	t.Run("non_validated_success_skips_confirm", func(t *testing.T) {
		loop := newLoop()
		confirmed := false
		loop.confirmProtocolSuccessSideEffects(t.Context(),
			gatewaydispatch.UpstreamDispatchResult{ConfirmHalfOpenSuccess: func() bool { confirmed = true; return true }},
			gatewayresponse.UpstreamResponseHandlingResult{ProtocolValidatedSuccess: false})
		if confirmed {
			t.Fatal("未协议验证成功不得执行半开成功确认")
		}
	})
}
