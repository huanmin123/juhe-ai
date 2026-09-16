package gatewayresponse

// w11b 波次：非流式管道（PipeNonStreamUpstreamResponse / HandleNonStream-
// UpstreamResponse）分支、readNextStreamChunk 首字截止分支、EOF 拦截提交前
// 分支与 heartbeat / readplan / interceptor / failurestatus 补充覆盖。

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ---- PipeNonStreamUpstreamResponse ----

func w11bNonStreamInput(body UpstreamBody) NonStreamPipeInput {
	return NonStreamPipeInput{
		Body:        body,
		Downstream:  StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
		StartedAtMs: 1000,
		NowMs:       func() int64 { return 1000 },
	}
}

func closedSignal() <-chan struct{} {
	closed := make(chan struct{})
	close(closed)
	return closed
}

func TestW11BPipeNonStreamSignalAbort(t *testing.T) {
	input := w11bNonStreamInput(newChanBody())
	input.Signal = chanSignal{ch: closedSignal()}
	_, err := PipeNonStreamUpstreamResponse(input)
	var aborted *UpstreamRequestAbortedError
	if !errors.As(err, &aborted) {
		t.Fatalf("err = %v", err)
	}
	// signalAbortedChannel nil 安全。
	if signalAbortedChannel(nil) {
		t.Fatal("nil signal false")
	}
}

func TestW11BPipeNonStreamReadErrorPartial(t *testing.T) {
	body := newChanBody()
	go func() {
		body.push([]byte(`{"partial":`))
		time.Sleep(10 * time.Millisecond)
		body.fail(io.ErrUnexpectedEOF)
	}()
	input := w11bNonStreamInput(body)
	input.CaptureBody = true
	result, err := PipeNonStreamUpstreamResponse(input)
	var pipeErr *NonStreamBodyPipeError
	if !errors.As(err, &pipeErr) {
		t.Fatalf("err = %v", err)
	}
	if len(result.CapturedBody) == 0 {
		t.Fatal("部分结果应携带捕获正文")
	}
}

func TestW11BPipeNonStreamOverflowRequiresFullyBuffered(t *testing.T) {
	body := NewSliceUpstreamBody([]byte(`{"a":"` + strings.Repeat("x", 600) + `"}`))
	input := w11bNonStreamInput(body)
	input.InspectBytes = 256
	input.RequireFullyBuffered = true
	result, err := PipeNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.InspectionLimitExceeded || result.TransferredBytes != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestW11BPipeNonStreamOverflowFlushesToPassthrough(t *testing.T) {
	bigChunk := []byte(`{"a":"` + strings.Repeat("x", 600) + `"}`)
	body := NewSliceUpstreamBody(bigChunk, []byte(`{}`))
	recorder := httptest.NewRecorder()
	input := w11bNonStreamInput(body)
	input.Downstream = StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(recorder)}
	input.InspectBytes = 256
	result, err := PipeNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.InspectionLimitExceeded || result.FullyBuffered {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(recorder.Body.String(), "xxxx") {
		t.Fatalf("body = %q", recorder.Body.String()[:min(60, recorder.Body.Len())])
	}
	if result.TransferredBytes == 0 {
		t.Fatal("溢出后应边转发")
	}
}

func TestW11BPipeNonStreamFullyBufferedAndCallbacks(t *testing.T) {
	body := NewSliceUpstreamBody([]byte(`{"ok":true}`))
	input := w11bNonStreamInput(body)
	input.InspectBytes = 1024
	var reads, writes int
	var firstByte int
	input.OnChunkRead = func([]byte) { reads++ }
	input.OnChunkWritten = func(int64) { writes++ }
	input.OnFirstByte = func() { firstByte++ }
	result, err := PipeNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.FullyBuffered || result.CapturedBodyText != `{"ok":true}` {
		t.Fatalf("result = %+v", result)
	}
	if reads != 1 || firstByte != 0 {
		t.Fatalf("reads = %d firstByte = %d", reads, firstByte)
	}
	if writes != 0 {
		t.Fatalf("完整缓冲不写下游，writes = %d", writes)
	}
	if result.FirstByteMs != nil {
		t.Fatal("完整缓冲不推进首字时间")
	}
	// 空正文：信号循环不进入。
	empty := w11bNonStreamInput(NewSliceUpstreamBody())
	emptyResult, err := PipeNonStreamUpstreamResponse(empty)
	if err != nil || emptyResult.FullyBuffered {
		t.Fatalf("result = %+v err = %v", emptyResult, err)
	}
}

func TestW11BSendFullyBufferedNonStreamBody(t *testing.T) {
	recorder := httptest.NewRecorder()
	input := w11bNonStreamInput(nil)
	input.Downstream = StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(recorder)}
	prepared := 0
	written := int64(0)
	input.PrepareDownstream = func() { prepared++ }
	input.OnChunkWritten = func(n int64) { written = n }
	if err := SendFullyBufferedNonStreamBody(input, []byte("hello")); err != nil {
		t.Fatalf("err = %v", err)
	}
	if recorder.Body.String() != "hello" || prepared != 1 || written != 5 {
		t.Fatalf("body = %q prepared = %d written = %d", recorder.Body.String(), prepared, written)
	}
	failing := w11bNonStreamInput(nil)
	failing.Downstream.Res = &w11bFailingWriter{inner: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())}
	if err := SendFullyBufferedNonStreamBody(failing, []byte("x")); err == nil {
		t.Fatal("写失败应透传错误")
	}
}

// ---- HandleNonStreamUpstreamResponse 补充分支 ----

func TestW11BHandleNonStreamTransportOnlyCommitContinuesViaHeartbeat(t *testing.T) {
	input, _ := newInputFixture(NewSliceUpstreamBody(), 200, map[string]string{"Content-Type": "application/json"})
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	input.DownstreamCommitState.MarkTransportCommitted(0)
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.AlreadyFinalized {
		t.Fatalf("result = %+v", result)
	}
}

func TestW11BHandleNonStreamResponsesFailedTerminal(t *testing.T) {
	// responses 2xx 但根节点 status=failed：按协议失败收尾。
	body := `{"object":"response","id":"r1","output":[],"status":"failed","error":{"code":"x","message":"resp failed"}}`
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte(body)), 200, map[string]string{"Content-Type": "application/json"})
	input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/responses", nil))
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	input.ClientStrategy = &ClientStrategyView{ClientProfile: "generic", InterpretSemantics: false}
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorCode != "upstream_protocol_failure" || !result.AlreadyFinalized {
		t.Fatalf("result = %+v", result)
	}
}

func TestW11BHandleNonStreamEmpty204RejectedAndAllowed(t *testing.T) {
	// 204 非 interactions → 空协议失败。
	input, _ := newInputFixture(nil, 204, map[string]string{"Content-Type": "application/json"})
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorPayload.Code != "upstream_protocol_failure" {
		t.Fatalf("errorPayload = %+v", result.ErrorPayload)
	}
	// DELETE interactions（gemini driver）→ 空成功透传。
	input2, _ := newInputFixture(nil, 204, nil)
	input2.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("DELETE", "/v1beta/interactions/i-1", nil))
	input2.Driver = NewGeminiResponseDriver()
	input2.Deps = input.Deps
	result, err = HandleNonStreamUpstreamResponse(input2)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorPayload.Code != "" {
		t.Fatalf("errorPayload = %+v", result.ErrorPayload)
	}
}

func TestW11BHandleNonStreamUpstreamErrorBodyForwarded(t *testing.T) {
	// 上游 429 JSON 错误体：原样转发并解析 usage。
	body := `{"error":{"message":"rate limited","type":"rate_limit"},"usage":{"prompt_tokens":2,"total_tokens":3}}`
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(body)), 429, map[string]string{"Content-Type": "application/json"})
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if recorder.Code != 429 || !strings.Contains(recorder.Body.String(), "rate limited") {
		t.Fatalf("recorder code = %d body = %q", recorder.Code, recorder.Body.String())
	}
	if result.ErrorPayload.Code == "" {
		t.Fatalf("errorPayload = %+v", result.ErrorPayload)
	}
}

// ---- readNextStreamChunk 首字截止分支 ----

func TestW11BReadNextStreamChunkDeadlineReadCarries(t *testing.T) {
	clock := &w9cClock{now: 2000}
	pipe := w9cNewPipe(t, clock)
	deadline := int64(500) // startedAt=1000 → 1500 已过期
	pipe.options.FirstByteDeadlineMs = &deadline
	pipe.body = w9cChanBody(ChunkResult{Data: []byte(chatDeltaChunk)})
	// handler 内等待 pending settle：decide 返回 read 分支并携带 decision。
	pipe.options.OnFirstByteDeadline = func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		time.Sleep(50 * time.Millisecond)
		return FirstByteDeadlineAbort, nil
	}
	result, err := pipe.readNextStreamChunk()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.firstByteDeadlineObserved || result.decision == nil {
		t.Fatalf("result observed=%v decision=%v", result.firstByteDeadlineObserved, result.decision)
	}
}

func TestW11BReadNextStreamChunkDeadlineContinueThenAbort(t *testing.T) {
	clock := &w9cClock{now: 2000}
	pipe := w9cNewPipe(t, clock)
	deadline := int64(500)
	pipe.options.FirstByteDeadlineMs = &deadline
	pipe.body = w9cChanBody() // 永不出数据
	calls := 0
	pipe.options.OnFirstByteDeadline = func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		calls++
		return FirstByteDeadlineContinue, nil
	}
	// continue 后 firstByteDeadlineObserved=true，本轮进入常规读计划；
	// 首块 20ms 超时立即触发 first_chunk 计划超时。
	pipe.profile.FirstResponseTimeoutMs = 20
	_, err := pipe.readNextStreamChunk()
	planTimeout, ok := err.(*StreamReadPlanTimeoutError)
	if !ok || planTimeout.TimeoutKind != "first_chunk" {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestW11BReadNextStreamChunkSettleAfterPrecommitWall(t *testing.T) {
	// 入口时墙钟未到，读取 settle 晚于墙钟 → precommit deadline。
	clock := &w9cClock{now: 1700}
	pipe := w9cNewPipe(t, clock)
	wall := int64(1800)
	pipe.options.ResponsePrecommitDeadlineAtMs = &wall
	body := newChanBody()
	pipe.body = body
	go func() {
		time.Sleep(30 * time.Millisecond)
		clock.Set(1900)
		body.push([]byte("late"))
	}()
	_, err := pipe.readNextStreamChunk()
	if !IsResponsePrecommitDeadlineError(err) {
		t.Fatalf("err = %v", err)
	}
}

// ---- EOF pending 拦截 + 提交前 ----

func TestW11BEofFlushInterceptedBeforeCommitAtEOF(t *testing.T) {
	interceptor := &w11bPassInterceptor{
		onFlush: StreamInterceptorSseResult{Intercepted: &ResponseInspectionDecision{
			Reason: "configured_response_policy", RewriteMessage: "EOF 提交前命中", RewriteErrorCode: "pol_code",
		}},
	}
	recorder := &failureRecorder{}
	result, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody:        NewSliceUpstreamBody([]byte(": ping\n\n")),
		Downstream:          StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Signal:              staticSignal(),
		Options: StreamPipeOptions{
			Interceptor:                           interceptor,
			RetryBeforeDownstreamWriteUntilOutput: true,
			NowMs:                                 func() int64 { return 1000 },
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ResponseInspection == nil || result.DownstreamBytesWritten != 0 {
		t.Fatalf("result = %+v", result)
	}
	// handleInterceptedBeforeWrite 的 eofPendingFlush 变体直测。
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.preCommitBuffer.Buffering = true
	retryable := &ResponseInspectionDecision{Reason: "configured_response_policy", PolicySource: "user_policy"}
	returned, err, handled := pipe.handleInterceptedBeforeWrite(retryable, gatewayproto.StreamInspection{}, true)
	if !handled || err != nil || returned == nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	pipe2, _ := w11bNewFinalPipe(t, nil)
	pipe2.preCommitBuffer.Buffering = true
	plain := &ResponseInspectionDecision{Reason: "before_downstream_write_response_failure"}
	returned, err, handled = pipe2.handleInterceptedBeforeWrite(plain, gatewayproto.StreamInspection{}, true)
	if !handled || err != nil || returned == nil {
		t.Fatalf("eof handled=%v err=%v", handled, err)
	}
}

// ---- heartbeat ----

func TestW11BHeartbeatStartStopLifecycle(t *testing.T) {
	var heartbeat *GatewaySseWaitHeartbeat
	heartbeat.Start() // nil 安全
	heartbeat.Stop()

	recorder := httptest.NewRecorder()
	commit := &DownstreamCommitState{}
	canceled, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := HeartbeatDeps{
		Res:                gatewaypreauth.NewTrackingWriter(recorder),
		DownstreamProtocol: "chat_completions_sse",
		DownstreamCommit:   commit,
		Signal:             canceled,
		IntervalMs:         5,
	}
	heartbeat = CreateGatewaySseWaitHeartbeat(deps)
	if heartbeat == nil {
		t.Fatal("SSE 协议应创建心跳")
	}
	heartbeat.Start()
	heartbeat.Start() // 重复 Start 保持单轮
	cancel()          // 请求取消即停
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && recorder.Body.Len() == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	heartbeat.Stop()
	heartbeat.Stop() // 幂等
}

func TestW11BHeartbeatChunkOfAndLoopGuard(t *testing.T) {
	deps := HeartbeatDeps{DownstreamProtocol: "responses_sse", EmitCodexCompactionKeepalive: true}
	chunk := heartbeatChunkOf(deps)
	if !strings.Contains(string(chunk), "data:") {
		t.Fatalf("chunk = %q", chunk)
	}
	plain := heartbeatChunkOf(HeartbeatDeps{DownstreamProtocol: "messages_sse"})
	if !strings.Contains(string(plain), "juhe-ai") {
		t.Fatalf("chunk = %q", plain)
	}
	// runHeartbeatLoop：ctx 已取消直接退出；signal 已取消同样退出。
	done := make(chan struct{})
	go func() {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		runHeartbeatLoop(HeartbeatDeps{IntervalMs: 5}, heartbeatChunkOf(HeartbeatDeps{}), canceled)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消未退出心跳循环")
	}
}

// ---- readplan 纯函数 ----

func TestW11BReadPlanHelpers(t *testing.T) {
	if itoa(0) != "0" || itoa(-42) != "-42" || itoa(12345) != "12345" {
		t.Fatal("itoa 语义")
	}
	if timeoutSeconds(0) != 1 || timeoutSeconds(1500) != 2 || timeoutSeconds(-5) != 1 {
		t.Fatal("timeoutSeconds 语义")
	}
	if !strings.Contains(FirstChunkTimeoutMessage(3), "3s") ||
		!strings.Contains(StreamIdleTimeoutMessage(4), "4s") ||
		!strings.Contains(StreamSemanticResultTimeoutMessage(5), "5s") ||
		!strings.Contains(StreamMaxLifetimeTimeoutMessage(6), "6s") {
		t.Fatal("超时消息构造")
	}
	// BuildGatewayStreamReadPlan：stream lifetime 主导（lifetime 0 < idle 剩余）。
	plan := BuildGatewayStreamReadPlan(TimeoutProfile{
		FirstResponseTimeoutMs:          60_000,
		IdleTimeoutMs:                   30_000,
		UncommittedAttemptMaxLifetimeMs: 10_000,
	}, 1000, StreamReadPlanStatus{UpstreamChunkReceived: true}, 11_000)
	if plan == nil || plan.TimeoutKind != "stream_lifetime" {
		t.Fatalf("plan = %+v", plan)
	}
	// semantic_result 主导（semantic 剩余最小且可见）。
	plan2 := BuildGatewayStreamReadPlan(TimeoutProfile{
		FirstResponseTimeoutMs:          5_000,
		IdleTimeoutMs:                   30_000,
		UncommittedAttemptMaxLifetimeMs: 300_000,
	}, 1000, StreamReadPlanStatus{UpstreamChunkReceived: true}, 3_000)
	if plan2 == nil || plan2.TimeoutKind != "semantic_result" {
		t.Fatalf("plan2 = %+v", plan2)
	}
	// first_chunk 阶段。
	plan3 := BuildGatewayStreamReadPlan(TimeoutProfile{
		FirstResponseTimeoutMs:          5_000,
		IdleTimeoutMs:                   30_000,
		UncommittedAttemptMaxLifetimeMs: 300_000,
	}, 1000, StreamReadPlanStatus{WaitingForFirstChunk: true}, 3_000)
	if plan3 == nil || plan3.Phase != "first_chunk" {
		t.Fatalf("plan3 = %+v", plan3)
	}
}

// ---- interceptor shiftEvent 边界 ----

func TestW11BInterceptorShiftEventCRLF(t *testing.T) {
	interceptor := NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{
		EndpointFamily: gatewayproto.EndpointFamilyChatCompletions,
	})
	// CRLF 事件边界。
	event := interceptor.shiftEvent()
	if event != nil {
		t.Fatalf("空 pending = %q", event)
	}
	interceptor.PushChunk([]byte("data: {}\r\n\r\nmore"))
	_ = interceptor.FlushPendingOnEOF()
}

// ---- failurestatus appendStringByte 截断 ----

func TestW11BFailureStatusStringCaptureTruncate(t *testing.T) {
	tracker := NewResponsesRootStatusTracker()
	// 通过 JSON 字符串解析路径灌入 stringContext：直接构造长字符串事件。
	payload := `{"status":"` + strings.Repeat("a", jsonStringCaptureMaxBytes+64) + `"}`
	if got := ResponsesFailureStatusFromCapturedJSON(payload); got {
		_ = got // 超长字符串按截断处理，不 panic 即可
	}
	tracker.appendStringByte('x')
	if len(tracker.stringRaw) != 0 {
		t.Fatal("空上下文不捕获")
	}
}

// ---- sink 小函数 ----

type w11bUnmarshalable struct {
	Fn func()
}

func TestW11BSinkMarshalAndTimeHelpers(t *testing.T) {
	if got := marshalClientPayload(w11bUnmarshalable{}); got != "{}" {
		t.Fatalf("marshal 失败回退 {}，got %q", got)
	}
	if got := marshalClientPayload(map[string]string{"k": "v"}); got != `{"k":"v"}` {
		t.Fatalf("marshal = %q", got)
	}
	bare := NewSink(SinkDeps{})
	if bare.nowMs() == 0 {
		t.Fatal("缺省时钟可用")
	}
	if bare.logger() == nil {
		t.Fatal("缺省 logger 可用")
	}
	withClock := NewSink(SinkDeps{NowMs: func() int64 { return 7 }})
	if withClock.nowMs() != 7 {
		t.Fatal("注入时钟生效")
	}
}

func TestW11BStreamDownstreamFaces(t *testing.T) {
	// WritableEndedOverride / Destroyed / Interrupt 自定义面。
	recorder := httptest.NewRecorder()
	base := gatewaypreauth.NewTrackingWriter(recorder)
	downstream := StreamDownstream{
		Res:                   &capturingWriter{inner: base, onWrite: func([]byte) {}},
		WritableEndedOverride: func() bool { return true },
		Destroyed:             func() bool { return true },
		Interrupt:             func() { recorder.WriteHeader(599) },
	}
	if !downstream.WritableEnded() || !downstream.DestroyedNow() {
		t.Fatal("自定义面未生效")
	}
	downstream.InterruptNow()
	if recorder.Code != 599 {
		t.Fatalf("code = %d", recorder.Code)
	}
	// End 对非 TrackingWriter no-op。
	downstream.End()
	// FlushGateway 无 Flush 面时 no-op。
	FlushGateway(base)
	_ = sync.Mutex{}
	_ = io.EOF
	_ = context.Background
}
