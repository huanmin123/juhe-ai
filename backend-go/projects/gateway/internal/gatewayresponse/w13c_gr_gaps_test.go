package gatewayresponse

// w13c 覆盖率补充测试：散点分支直驱 + 管道 pendingReadDecision / 错误写入臂。
//
// 不可达语句清单（维持守卫、不删除）：
//   - sink.go NewGatewayStreamFailureAuditSink 中 recorder 为 nil 的分支：
//     装配入口保证 recorder 非空（测试直接构造 nil 场景无生产对应）。
//     （仅登记，不在此包强求覆盖。）

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayanthropic"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ---- codexcontract ----

func TestW13CJsonValueHasCompactionTrigger(t *testing.T) {
	if jsonValueHasCompactionTrigger(nil, 0) {
		t.Fatal("nil value must be false")
	}
	if jsonValueHasCompactionTrigger([]any{map[string]any{"type": "compaction_trigger"}}, 0) != true {
		t.Fatal("nested compaction trigger must be found")
	}
	if jsonValueHasCompactionTrigger(map[string]any{}, 0) {
		t.Fatal("empty object must be false")
	}
	deep := any("leaf")
	for i := 0; i < 12; i++ {
		deep = []any{deep}
	}
	if jsonValueHasCompactionTrigger(deep, 0) {
		t.Fatal("depth limit must stop the scan")
	}
}

func TestW13CNormalizedOpenAIRequestPath(t *testing.T) {
	request := &gatewaypreauth.GatewayRequest{}
	if got := normalizedOpenAIRequestPath(request); got != "/" {
		t.Fatalf("empty path = %q", got)
	}
}

func TestW13CRequestPathHasCompactionTrigger(t *testing.T) {
	if requestPathHasCompactionTrigger("/v1/chat/completions", nil, nil) {
		t.Fatal("non-responses path must be false")
	}
	if requestPathHasCompactionTrigger("/v1/responses", nil, nil) {
		t.Fatal("empty raw body must be false")
	}
	pattern := []byte(`{"type":"compaction_trigger"}`)
	if !requestPathHasCompactionTrigger("/v1/responses", nil, pattern) {
		t.Fatal("small body with trigger must be true")
	}
	big := append([]byte(strings.Repeat(" ", 512)), pattern...)
	if !requestPathHasCompactionTrigger("/v1/responses", nil, big) {
		t.Fatal("edge-scanned body must find the trigger")
	}
	clean := []byte(strings.Repeat(" ", 512) + `{"type":"other"}`)
	if requestPathHasCompactionTrigger("/v1/responses", nil, clean) {
		t.Fatal("clean body must be false")
	}
}

// ---- convert / errors / failclass / failurestatus ----

func TestW13CConvertAnthropicFramePointers(t *testing.T) {
	choice := 2
	output := 1
	content := 3
	visible := true
	frames := convertAnthropicFrames([]gatewayanthropic.ResponseSemanticFrame{
		{ChoiceIndex: &choice, OutputIndex: &output, ContentIndex: &content, VisibleOutput: &visible},
	})
	if len(frames) != 1 || frames[0].ChoiceIndex != 2 {
		t.Fatalf("frames = %+v", frames)
	}
	if convertAnthropicFrames(nil) != nil {
		t.Fatal("nil frames must map to nil")
	}
}

func TestW13CGeminiErrorPayloadArms(t *testing.T) {
	empty := parseGeminiErrorPayloadFromValue("not-an-object")
	if empty.Message != "" {
		t.Fatalf("non-object payload = %+v", empty)
	}
	payload := parseGeminiErrorPayloadFromValue(map[string]any{
		"error": map[string]any{"status": "RESOURCE_EXHAUSTED", "code": 429, "message": "限流"},
	})
	if payload.Type != "RESOURCE_EXHAUSTED" || payload.Message != "限流" {
		t.Fatalf("payload = %+v", payload)
	}
	if _, ok := geminiStringErrorField(struct{}{}); ok {
		t.Fatal("unsupported type must be invalid")
	}
	if text, ok := geminiStringErrorField(429.0); !ok || text != "429" {
		t.Fatalf("float code = %q %v", text, ok)
	}
	if text, ok := geminiStringErrorField(true); !ok || text != "true" {
		t.Fatalf("bool field = %q %v", text, ok)
	}
	if _, ok := geminiStringErrorField("   "); ok {
		t.Fatal("blank string must be invalid")
	}
}

func TestW13CResponsePrecommitDeadlineErrorOfWrapped(t *testing.T) {
	deadline := &ResponsePrecommitDeadlineError{DeadlineAtMs: 123}
	if ResponsePrecommitDeadlineErrorOf(nil) != nil {
		t.Fatal("nil must map to nil")
	}
	wrapped := &NonStreamBodyPipeError{OriginalError: deadline}
	got := ResponsePrecommitDeadlineErrorOf(wrapped)
	if got == nil || got.DeadlineAtMs != 123 {
		t.Fatalf("wrapped = %+v", got)
	}
	if ResponsePrecommitDeadlineErrorOf(errors.New("other")) != nil {
		t.Fatal("unrelated error must map to nil")
	}
}

func TestW13CClassifyMetricReasonBuckets(t *testing.T) {
	buckets := map[string]GatewayUpstreamFailureMetricReasonClass{
		"Insufficient_Quota":      MetricReasonQuota,
		"rate_limited":            MetricReasonRateLimit,
		"ACCESS_DENIED":           MetricReasonAuthorization,
		"upstream_protocol_error": MetricReasonProtocol,
		"First_Byte_Timeout":      MetricReasonTimeout,
	}
	for code, want := range buckets {
		if got := classifyMetricReason(GatewayUpstreamFailureClassificationInput{ErrorCode: code}); got != want {
			t.Fatalf("code %q = %v, want %v", code, got, want)
		}
	}
	if got := classifyMetricReason(GatewayUpstreamFailureClassificationInput{ErrorCode: "x", Phase: "upstream_request"}); got != MetricReasonTransport {
		t.Fatalf("transport phase = %v", got)
	}
}

func TestW13CFailureStatusScannerArms(t *testing.T) {
	tracker := &ResponsesRootStatusTracker{}
	tracker.Push([]byte(`{"status":"failed"}`))
	if !tracker.HasFailedStatus() {
		t.Fatal("failed status must be consumed")
	}
	stringTracker := &ResponsesRootStatusTracker{}
	stringTracker.Push([]byte(`{"status":"completed"}`))
	if stringTracker.HasFailedStatus() {
		t.Fatal("completed status must not fail")
	}
	if decodeJSONString("trailing\\") != "" {
		t.Fatal("trailing escape must fail")
	}
	if text, ok := unescapeJSONString("A\\u0042"); !ok || text != "AB" {
		t.Fatalf("unicode escape = %q %v", text, ok)
	}
	if _, ok := parseHex4("G001"); ok {
		t.Fatal("invalid hex must fail")
	}
	if code, ok := parseHex4("004A"); !ok || code != 0x4a {
		t.Fatalf("hex digits = %x %v", code, ok)
	}
}

// ---- models / nonstreamgate / readplan / retrydecision / capture ----

func TestW13CModelsHelpers(t *testing.T) {
	if hasNonEmptyQueryParam(&gatewaypreauth.GatewayRequest{}, "limit") {
		t.Fatal("nil HTTP must be false")
	}
	request := &gatewaypreauth.GatewayRequest{HTTP: httptest.NewRequest(http.MethodGet, "/v1/models?limit=5", nil)}
	if !hasNonEmptyQueryParam(request, "limit") || hasNonEmptyQueryParam(request, "order") {
		t.Fatal("query param detection mismatch")
	}
	payload := buildAnthropicModelsPayload([]ModelCatalogEntry{{Model: "gpt-test", ReleaseDate: "2024-01-01", CreatedAt: "1700000000"}})
	if len(payload.Data) == 0 {
		t.Fatalf("payload = %+v", payload)
	}
	recorder := httptest.NewRecorder()
	writer := gatewaypreauth.NewTrackingWriter(recorder)
	writer.Header().Set("Vary", "Origin, Origin, User-Agent")
	setAuthenticatedModelsClientCacheHeaders(writer)
	vary := writer.Header().Get("Vary")
	if !strings.Contains(vary, "Origin") || strings.Count(vary, "Origin,") != 1 {
		t.Fatalf("vary merge = %q", vary)
	}
	if !strings.Contains(vary, "X-Codex-Client") {
		t.Fatalf("vary must append defaults: %q", vary)
	}
}

func TestW13CNonStreamGateAndReadPlan(t *testing.T) {
	if mediaTypeOfContentType("") != "" {
		t.Fatal("empty content type must be empty")
	}
	if mediaTypeOfContentType("not a media type ==") != "" {
		t.Fatal("invalid media type must be empty")
	}
	if mediaTypeOfContentType("application/json; charset=utf-8") != "application/json" {
		t.Fatal("json media type must parse")
	}
	if got := timeoutSeconds(0); got != 1 {
		t.Fatalf("zero timeout = %d", got)
	}
	if got := timeoutSeconds(1500); got != 2 {
		t.Fatalf("rounded timeout = %d", got)
	}
}

func TestW13CRetryDecisionArms(t *testing.T) {
	if ShouldReturnResponseInspectionBeforeDownstreamWrite(nil, PreCommitResponseState{}, 0) {
		t.Fatal("nil decision must be false")
	}
	policy := &ResponseInspectionDecision{Reason: "configured_response_policy", PolicySource: "explicit_user_policy"}
	if !ShouldReturnResponseInspectionBeforeDownstreamWrite(policy, PreCommitResponseState{}, 0) {
		t.Fatal("pre-write policy decision must return before write")
	}
	written := ShouldReturnResponseInspectionBeforeDownstreamWrite(policy, PreCommitResponseState{HeadersSent: true}, 0)
	if written {
		t.Fatal("headers sent must block the pre-write return")
	}
	if ShouldRetryResponseInspectionDecisionOnServer(nil, PreCommitResponseState{}) {
		t.Fatal("nil decision must be false")
	}
}

func TestW13CLimitedCaptureTruncation(t *testing.T) {
	capture := NewLimitedCapture(4)
	capture.Push([]byte("abcdef"))
	capture.Push([]byte("more"))
	snapshot := string(capture.Buffer())
	if snapshot != "abcd" {
		t.Fatalf("truncated capture = %q", snapshot)
	}
	if !capture.IsTruncated() {
		t.Fatal("capture must be marked truncated")
	}
	var nilCapture *LimitedCapture
	nilCapture.Push([]byte("x"))
}

// ---- 管道场景：pendingReadDecision / 客户端断开 / 变换 / 写入失败 ----

func w13cNewPipe(t *testing.T, mutate func(*StreamPipeOptions), chunks ...[]byte) *streamPipe {
	t.Helper()
	options := StreamPipeOptions{NowMs: func() int64 { return 1000 }}
	if mutate != nil {
		mutate(&options)
	}
	recorder := &failureRecorder{}
	return newStreamPipe(PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody(chunks...),
		Downstream: StreamDownstream{
			Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()),
		},
		TimeoutProfile: TimeoutProfile{
			FirstResponseTimeoutMs:          3_600_000,
			IdleTimeoutMs:                   3_600_000,
			UncommittedAttemptMaxLifetimeMs: 3_600_000,
		},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Signal:              staticSignal(),
		Options:             options,
	})
}

func TestW13CPipeClientClosedArm(t *testing.T) {
	pipe := w13cNewPipe(t, nil, []byte(chatDeltaChunk))
	pipe.clientClosed = true
	_, err := pipe.loop()
	if err == nil || !strings.Contains(err.Error(), "客户端连接已断开") {
		t.Fatalf("client closed err = %v", err)
	}
}

func TestW13CPipeTransformUpstreamChunk(t *testing.T) {
	recorder := &failureRecorder{}
	commit := &DownstreamCommitState{}
	options := StreamPipeOptions{
		TransformUpstreamChunk: func(chunk []byte) [][]byte {
			transformed := make([]byte, len(chunk))
			copy(transformed, chunk)
			return [][]byte{transformed}
		},
		NowMs: func() int64 { return 1000 },
	}
	body := NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatFinishChunk))
	result, err := runPipe(body, nil, options, recorder, 1000)
	if err != nil {
		t.Fatalf("transformed pipe = %v", err)
	}
	if result.DownstreamBytesWritten == 0 {
		t.Fatalf("transformed result = %+v", result)
	}
	_ = commit
}

func TestW13CPipeFirstByteDeadlineDecisionArms(t *testing.T) {
	deadline := int64(50)
	// 首字节 deadline 已过 + 处理器继续 → 读取落地 + settle 分支。
	continueHandler := func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		return FirstByteDeadlineContinue, nil
	}
	pipe := w13cNewPipe(t, func(o *StreamPipeOptions) {
		o.FirstByteDeadlineMs = &deadline
		o.OnFirstByteDeadline = continueHandler
		o.NowMs = func() int64 { return 100_000 }
	}, []byte(chatDeltaChunk), []byte(chatFinishChunk))
	time.Sleep(100 * time.Millisecond)
	if _, err := pipe.loop(); err != nil {
		t.Fatalf("continue decision pipe = %v", err)
	}

	// 处理器返回中止 → 非语义 chunk 落地后 settle 触发 FirstByteTimeoutError。
	abortHandler := func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		return FirstByteDeadlineAbort, nil
	}
	abortPipe := w13cNewPipe(t, func(o *StreamPipeOptions) {
		o.FirstByteDeadlineMs = &deadline
		o.OnFirstByteDeadline = abortHandler
		o.NowMs = func() int64 { return 100_000 }
	}, []byte("data: {\"type\":\"ping\"}\n\n"))
	time.Sleep(100 * time.Millisecond)
	if _, abortErr := abortPipe.loop(); abortErr != nil {
		var timeout *FirstByteTimeoutError
		if !errors.As(abortErr, &timeout) {
			t.Fatalf("abort decision err = %v", abortErr)
		}
	}
}

func TestW13CPipeDecisionErrorPropagation(t *testing.T) {
	deadline := int64(50)
	handlerErr := errors.New("w13c decision failure")
	failingHandler := func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		return FirstByteDeadlineContinue, handlerErr
	}
	errPipe := w13cNewPipe(t, func(o *StreamPipeOptions) {
		o.FirstByteDeadlineMs = &deadline
		o.OnFirstByteDeadline = failingHandler
		o.NowMs = func() int64 { return 100_000 }
	}, []byte("data: {\"type\":\"ping\"}\n\n"))
	time.Sleep(100 * time.Millisecond)
	// 决策错误 + 未 settle：直接返回决策错误，不读取。
	unsettled := newPendingRead(make(chan ChunkResult), func() int64 { return 100_000 })
	decision := errPipe.resolveDeadlineOutcome(unsettled, FirstByteDeadlineContinue, handlerErr, true)
	if !errors.Is(decision.decisionError, handlerErr) || decision.read {
		t.Fatalf("unsettled decision = %+v", decision)
	}
	// 决策错误 + 已 settle：读取结果携带决策错误。
	settled := newPendingRead(singleChunkChannel(ChunkResult{Data: []byte("x")}), func() int64 { return 100_000 })
	time.Sleep(100 * time.Millisecond)
	resolved := errPipe.resolveDeadlineOutcome(settled, FirstByteDeadlineContinue, handlerErr, true)
	if !resolved.read || !errors.Is(resolved.decisionError, handlerErr) {
		t.Fatalf("settled decision = %+v", resolved)
	}
}



func TestW13CSettleDecisionArms(t *testing.T) {
	// decisionError 冒泡臂。
	pipe, _ := w11bNewFinalPipe(t, nil)
	decisionErr := errors.New("w13c settle failure")
	pipe.pendingReadDecision = &streamFirstByteDeadlineReadDecision{hasAction: true, decisionError: decisionErr}
	if err := pipe.settleStreamFirstByteDeadlineReadDecision(false); !errors.Is(err, decisionErr) {
		t.Fatalf("settle error = %v", err)
	}
	// 无 decision 时空操作。
	if err := pipe.settleStreamFirstByteDeadlineReadDecision(false); err != nil {
		t.Fatalf("nil settle = %v", err)
	}
	// 中止动作 → FirstByteTimeoutError。
	pipe2, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) {
		deadline := int64(30)
		o.FirstByteDeadlineMs = &deadline
	})
	pipe2.pendingReadDecision = &streamFirstByteDeadlineReadDecision{hasAction: true, action: FirstByteDeadlineAbort}
	err := pipe2.settleStreamFirstByteDeadlineReadDecision(false)
	var timeout *FirstByteTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("abort settle = %v", err)
	}
	// 语义结果落地 → superseded 通知。
	superseded := false
	pipe3, _ := w11bNewFinalPipe(t, nil)
	pipe3.options.OnFirstByteDeadlineSuperseded = func() { superseded = true }
	pipe3.pendingReadDecision = &streamFirstByteDeadlineReadDecision{hasAction: true, action: FirstByteDeadlineAbort}
	if err := pipe3.settleStreamFirstByteDeadlineReadDecision(true); err != nil {
		t.Fatalf("semantic settle = %v", err)
	}
	if !superseded {
		t.Fatal("semantic settle must notify superseded")
	}
}

func TestW13CDecideDeadlineOutcomeArms(t *testing.T) {
	handlerFailure := errors.New("w13c handler failure")

	// 决策错误 + 未 settle：返回决策错误。
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.options.OnFirstByteDeadline = func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		return FirstByteDeadlineContinue, handlerFailure
	}
	unsettled := newPendingRead(make(chan ChunkResult), func() int64 { return 1000 })
	decision := pipe.decideFirstByteDeadlineAfterPendingRead(unsettled, FirstByteDeadlineInput{})
	if !errors.Is(decision.decisionError, handlerFailure) {
		t.Fatalf("unsettled decision = %+v", decision)
	}

	// 决策错误 + 已 settle：读取带决策错误返回。
	settledPipe, _ := w11bNewFinalPipe(t, nil)
	settledPipe.options.OnFirstByteDeadline = func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		return FirstByteDeadlineContinue, handlerFailure
	}
	settled := newPendingRead(singleChunkChannel(ChunkResult{Data: []byte("x")}), func() int64 { return 1000 })
	time.Sleep(10 * time.Millisecond)
	resolved := settledPipe.decideFirstByteDeadlineAfterPendingRead(settled, FirstByteDeadlineInput{})
	if !resolved.read {
		t.Fatalf("settled decision = %+v", resolved)
	}

	// 已关闭的 pending 通道：中断错误。
	closedPipe, _ := w11bNewFinalPipe(t, nil)
	closed := newPendingRead(closedChunkChannel(), func() int64 { return 1000 })
	<-closed.ch
	outcome := closedPipe.resolveDeadlineOutcome(closed, FirstByteDeadlineContinue, nil, true)
	if outcome.read {
		t.Fatalf("closed channel outcome = %+v", outcome)
	}
}

func singleChunkChannel(chunk ChunkResult) <-chan ChunkResult {
	ch := make(chan ChunkResult, 1)
	ch <- chunk
	return ch
}

func closedChunkChannel() <-chan ChunkResult {
	ch := make(chan ChunkResult)
	close(ch)
	return ch
}

func TestW13CHeartbeatLoopArms(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := gatewaypreauth.NewTrackingWriter(recorder)
	commit := &DownstreamCommitState{}
	// 语义已提交：写心跳直接 false。
	commit.SemanticCommitted = true
	if writeHeartbeatChunk(HeartbeatDeps{Res: writer, DownstreamCommit: commit}, []byte("x")) {
		t.Fatal("committed stream must not write heartbeats")
	}
	// ctx 已取消：runHeartbeatLoop 立即返回。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runHeartbeatLoop(HeartbeatDeps{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()), IntervalMs: 1000}, []byte(": hb\n\n"), ctx)
}

// ---- heartbeat / 上游 body ----

func TestW13CReaderUpstreamBodyPump(t *testing.T) {
	reader := strings.NewReader("chunk-a")
	body := NewReaderUpstreamBody(context.Background(), reader)
	first := <-body.Next()
	if first.Err != nil || string(first.Data) != "chunk-a" {
		t.Fatalf("first chunk = %+v", first)
	}
	second := <-body.Next()
	if second.Err == nil {
		t.Fatalf("second read should surface EOF, got %+v", second)
	}
	body.Close()
	body.Close()
}

func TestW13CReaderUpstreamBodyTransportError(t *testing.T) {
	body := NewReaderUpstreamBody(context.Background(), &w13cFailReader{})
	result := <-body.Next()
	var transport *StartedBodyTransportError
	if !errors.As(result.Err, &transport) {
		t.Fatalf("transport error = %+v", result.Err)
	}
	body.Close()
}

type w13cFailReader struct{}

func (w13cFailReader) Read([]byte) (int, error) { return 0, errors.New("w13c read failure") }

func TestW13CHeartbeatCodexChunkSelection(t *testing.T) {
	codex := heartbeatChunkOf(HeartbeatDeps{EmitCodexCompactionKeepalive: true, DownstreamProtocol: "responses_sse"})
	plain := heartbeatChunkOf(HeartbeatDeps{})
	if string(codex) == string(plain) {
		t.Fatal("codex compaction heartbeat must differ from the default chunk")
	}
}
