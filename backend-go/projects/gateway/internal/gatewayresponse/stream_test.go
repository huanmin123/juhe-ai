package gatewayresponse

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ---- mock 上游体 ----

type chanBody struct {
	ch   chan ChunkResult
	once sync.Once
}

func newChanBody() *chanBody {
	return &chanBody{ch: make(chan ChunkResult)}
}

func (b *chanBody) push(data []byte) { b.ch <- ChunkResult{Data: data} }

func (b *chanBody) fail(err error) { b.ch <- ChunkResult{Err: err} }

func (b *chanBody) end() {
	b.once.Do(func() { close(b.ch) })
}

func (b *chanBody) Next() <-chan ChunkResult { return b.ch }

func (b *chanBody) Close() { b.end() }

// ---- mock 失败记录 ----

type failureRecord struct {
	message   string
	errorCode string
	context   StreamFailureContext
}

type failureRecorder struct {
	mu     sync.Mutex
	values []failureRecord
}

func (r *failureRecorder) handle(message string, errorCode string, context StreamFailureContext) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, failureRecord{message: message, errorCode: errorCode, context: context})
	return nil
}

func (r *failureRecorder) last() failureRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.values) == 0 {
		return failureRecord{}
	}
	return r.values[len(r.values)-1]
}

func (r *failureRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.values)
}

func newDownstream() (*httptest.ResponseRecorder, StreamDownstream) {
	recorder := httptest.NewRecorder()
	tracking := gatewaypreauth.NewTrackingWriter(recorder)
	return recorder, StreamDownstream{Res: tracking}
}

func runPipe(body UpstreamBody, signal <-chan struct{}, options StreamPipeOptions, recorder *failureRecorder, startedAtMs int64) (StreamPipeResult, error) {
	if options.NowMs == nil {
		options.NowMs = func() int64 { return startedAtMs }
	}
	return PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody: body,
		Downstream: StreamDownstream{
			Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()),
		},
		TimeoutProfile: TimeoutProfile{
			FirstResponseTimeoutMs:          60_000,
			IdleTimeoutMs:                   30_000,
			UncommittedAttemptMaxLifetimeMs: 300_000,
		},
		StartedAtMs:         startedAtMs,
		HandleStreamFailure: recorder.handle,
		Signal:              chanSignal{ch: signal},
		Options:             options,
	})
}

type chanSignal struct{ ch <-chan struct{} }

func (s chanSignal) Done() <-chan struct{} { return s.ch }
func (s chanSignal) Err() error            { return nil }

// ---- 场景 ----

const chatDeltaChunk = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你好\"}}]}\n\n"

const chatFinishChunk = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":7,\"total_tokens\":12}}\n\n"

const chatDoneChunk = "data: [DONE]\n\n"

func TestPipeUpstreamStreamOpenAIChatSuccess(t *testing.T) {
	recorder := &failureRecorder{}
	body := NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatFinishChunk), []byte(chatDoneChunk))
	result, err := runPipe(body, nil, StreamPipeOptions{}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed || result.Message != "已完成" {
		t.Fatalf("result = %+v", result)
	}
	if !result.ProtocolValidated {
		t.Fatal("complete valid frame sequence should validate")
	}
	if result.OutputReceived != true {
		t.Fatal("output should be received")
	}
	if result.Usage.InputTokens == nil || *result.Usage.InputTokens != 5 ||
		result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.FirstTokenMs == nil {
		t.Fatal("first token should be marked")
	}
	if result.DownstreamBytesWritten == 0 || !result.TransportCommitted || !result.SemanticCommitted {
		t.Fatalf("downstream commit = %+v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("unexpected failure records = %+v", recorder.values)
	}
}

func TestPipeUpstreamStreamPassthroughByteOrder(t *testing.T) {
	chunks := []string{chatDeltaChunk, chatDeltaChunk, chatFinishChunk, chatDoneChunk}
	recorder := &failureRecorder{}
	_, downstream := newDownstream()
	var written []byte
	var mu sync.Mutex
	options := StreamPipeOptions{
		OnFirstOutput: func() {},
		NowMs:         func() int64 { return 1000 },
	}
	input := PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody([]byte(chunks[0]), []byte(chunks[1]), []byte(chunks[2]), []byte(chunks[3])),
		Downstream: StreamDownstream{
			Res: &capturingWriter{
				inner: downstream.Res,
				onWrite: func(data []byte) {
					mu.Lock()
					written = append(written, data...)
					mu.Unlock()
				},
			},
		},
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Options:             options,
	}
	result, err := PipeUpstreamStream(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	_ = result
	want := strings.Join(chunks, "")
	if string(written) != want {
		t.Fatalf("downstream bytes differ:\n got %q\nwant %q", written, want)
	}
}

type capturingWriter struct {
	inner   gatewaypreauth.GatewayResponseWriter
	onWrite func([]byte)
}

func (w *capturingWriter) Header() http.Header { return w.inner.Header() }

func (w *capturingWriter) Write(data []byte) (int, error) {
	w.onWrite(data)
	return w.inner.Write(data)
}

func (w *capturingWriter) WriteHeader(status int) { w.inner.WriteHeader(status) }
func (w *capturingWriter) HeadersSent() bool      { return w.inner.HeadersSent() }
func (w *capturingWriter) StatusCode() int        { return w.inner.StatusCode() }

func TestPipeUpstreamStreamResponsesFailureTerminalBeforeCommit(t *testing.T) {
	failureChunk := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"provider_error\",\"message\":\"供应商失败\"}}}\n\n"
	recorder := &failureRecorder{}
	body := NewSliceUpstreamBody([]byte(failureChunk))
	result, err := runPipe(body, nil, StreamPipeOptions{}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("result = %+v", result)
	}
	last := recorder.last()
	if last.message != "供应商失败" {
		t.Fatalf("message = %q", last.message)
	}
	if last.errorCode != "provider_error" {
		t.Fatalf("errorCode = %q", last.errorCode)
	}
	if last.context.ProtocolFailureEventReceived != true || last.context.AvailabilityProbeEligible != true {
		t.Fatalf("context = %+v", last.context)
	}
	if result.DownstreamBytesWritten != 0 {
		t.Fatalf("failure before commit must not write downstream, bytes = %d", result.DownstreamBytesWritten)
	}
}

func TestPipeUpstreamStreamMissingTerminal(t *testing.T) {
	recorder := &failureRecorder{}
	body := NewSliceUpstreamBody([]byte(chatDeltaChunk))
	result, err := runPipe(body, nil, StreamPipeOptions{}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed || result.Message != "上游流在协议终止事件前结束" {
		t.Fatalf("result = %+v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("failure records = %d", recorder.count())
	}
	if recorder.last().errorCode != gatewaypreauth.GatewayStreamFailureCode("上游流在协议终止事件前结束") {
		t.Fatalf("errorCode = %q", recorder.last().errorCode)
	}
}

func TestPipeUpstreamStreamClientAbortBeforeTerminal(t *testing.T) {
	recorder := &failureRecorder{}
	body := newChanBody()
	signal := make(chan struct{})
	options := StreamPipeOptions{}
	options.NowMs = func() int64 { return 1000 }

	type runOutcome struct {
		result StreamPipeResult
		err    error
	}
	outcomeCh := make(chan runOutcome, 1)
	go func() {
		result, err := runPipe(body, signal, options, recorder, 1000)
		outcomeCh <- runOutcome{result, err}
	}()
	body.push([]byte(chatDeltaChunk))
	time.Sleep(50 * time.Millisecond)
	close(signal) // 客户端断开
	body.end()
	select {
	case outcome := <-outcomeCh:
		if !IsUpstreamRequestAbortedError(outcome.err) {
			t.Fatalf("err = %v, want abort", outcome.err)
		}
		if recorder.last().message != "" {
			t.Fatalf("failure records = %+v", recorder.values)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestPipeUpstreamStreamClientAbortAfterTerminalSucceeds(t *testing.T) {
	recorder := &failureRecorder{}
	body := newChanBody()
	signal := make(chan struct{})
	options := StreamPipeOptions{NowMs: func() int64 { return 1000 }}
	outcomeCh := make(chan struct {
		result StreamPipeResult
		err    error
	}, 1)
	go func() {
		result, err := runPipe(body, signal, options, recorder, 1000)
		outcomeCh <- struct {
			result StreamPipeResult
			err    error
		}{result, err}
	}()
	body.push([]byte(chatFinishChunk))
	body.push([]byte(chatDoneChunk))
	time.Sleep(50 * time.Millisecond)
	close(signal)
	body.end()
	select {
	case outcome := <-outcomeCh:
		// [DONE] 已写出：abort 分支按成功收尾（gateway_stream_client_closed_after_terminal）。
		if outcome.err != nil {
			t.Fatalf("err = %v", outcome.err)
		}
		if !outcome.result.Completed || outcome.result.Message != "已完成" {
			t.Fatalf("result = %+v", outcome.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestPipeUpstreamStreamUpstreamBodyInterrupted(t *testing.T) {
	recorder := &failureRecorder{}
	body := newChanBody()
	options := StreamPipeOptions{NowMs: func() int64 { return 1000 }}
	outcomeCh := make(chan struct {
		result StreamPipeResult
		err    error
	}, 1)
	go func() {
		result, err := runPipe(body, nil, options, recorder, 1000)
		outcomeCh <- struct {
			result StreamPipeResult
			err    error
		}{result, err}
	}()
	body.push([]byte(chatDeltaChunk))
	time.Sleep(20 * time.Millisecond)
	body.fail(&StartedBodyTransportError{Err: io.ErrUnexpectedEOF, Name: "ReadError", Code: "ECONNRESET"})
	select {
	case outcome := <-outcomeCh:
		if outcome.err != nil {
			t.Fatalf("err = %v", outcome.err)
		}
		if outcome.result.Completed {
			t.Fatalf("result = %+v", outcome.result)
		}
		if outcome.result.Message != "上游流式响应读取未完成" {
			t.Fatalf("message = %q", outcome.result.Message)
		}
		if outcome.result.TransportFailure == nil || outcome.result.TransportFailure.Kind != "read_incomplete" {
			t.Fatalf("transport failure = %+v", outcome.result.TransportFailure)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestPipeUpstreamStreamAnthropicSuccess(t *testing.T) {
	chunks := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"你好\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	recorder := &failureRecorder{}
	options := StreamPipeOptions{
		Driver: anthropicStreamDriver{},
		NowMs:  func() int64 { return 1000 },
	}
	result, err := runPipe(NewSliceUpstreamBody([]byte(chunks[0]), []byte(chunks[1]), []byte(chunks[2]), []byte(chunks[3])), nil, options, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed || result.Message != "已完成" {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 9 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if recorder.count() != 0 {
		t.Fatalf("failure records = %+v", recorder.values)
	}
}

func TestPipeUpstreamStreamGeminiSuccess(t *testing.T) {
	chunks := []string{
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"你好\"}]}}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":4}}\n\n",
		"data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":4,\"totalTokenCount\":7}}\n\n",
	}
	recorder := &failureRecorder{}
	options := StreamPipeOptions{
		Driver: geminiStreamDriver{},
		NowMs:  func() int64 { return 1000 },
	}
	result, err := runPipe(NewSliceUpstreamBody([]byte(chunks[0]), []byte(chunks[1])), nil, options, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed || result.Message != "已完成" {
		t.Fatalf("result = %+v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("failure records = %+v", recorder.values)
	}
}

func TestPipeUpstreamStreamPreCommitKeepsFramingPrivate(t *testing.T) {
	// 仅注释帧 + 协议失败：注释保持 pre-commit 私有并被丢弃，下游零字节。
	failureChunk := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"e1\",\"message\":\"失败\"}}}\n\n"
	recorder := &failureRecorder{}
	recorder2, downstream2 := newDownstream()
	_ = recorder2
	options := StreamPipeOptions{
		RetryBeforeDownstreamWriteUntilOutput: true,
		NowMs:                          func() int64 { return 1000 },
	}
	result, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody([]byte(": comment only\n\n"), []byte(failureChunk)),
		Downstream:   downstream2,
		TimeoutProfile: TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Options:             options,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.UncommittedResponseBody != nil {
		t.Fatalf("comment-only framing should stay private, got %q", result.UncommittedResponseBody)
	}
	if result.DownstreamBytesWritten != 0 {
		t.Fatalf("downstream bytes = %d", result.DownstreamBytesWritten)
	}

}

func TestPipeUpstreamStreamReadPlanTimeout(t *testing.T) {
	recorder := &failureRecorder{}
	body := newChanBody()
	options := StreamPipeOptions{
		NowMs: func() int64 { return 1000 },
	}
	// 首块窗口 25ms；body 永不出数据 → stream_lifetime / first_chunk 超时。
	done := make(chan struct {
		result StreamPipeResult
		err    error
	}, 1)
	go func() {
		result, err := PipeUpstreamStream(PipeUpstreamStreamInput{
			UpstreamBody: body,
			Downstream:   StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
			TimeoutProfile: TimeoutProfile{
				FirstResponseTimeoutMs:          25,
				IdleTimeoutMs:                   25,
				UncommittedAttemptMaxLifetimeMs: 40,
			},
			StartedAtMs:         1000,
			HandleStreamFailure: recorder.handle,
			Options:             options,
		})
		done <- struct {
			result StreamPipeResult
			err    error
		}{result, err}
	}()
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("err = %v", outcome.err)
		}
		if outcome.result.Completed {
			t.Fatalf("result = %+v", outcome.result)
		}
		if !strings.Contains(outcome.result.Message, "内未返回首段数据") {
			t.Fatalf("message = %q", outcome.result.Message)
		}
		if outcome.result.TransportFailure == nil || outcome.result.TransportFailure.Kind != "timeout" {
			t.Fatalf("transport failure = %+v", outcome.result.TransportFailure)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestFirstByteDeadlineConfiguredDeadline(t *testing.T) {
	recorder := &failureRecorder{}
	body := newChanBody()
	options := StreamPipeOptions{
		FirstByteDeadlineMs: int64PtrOf(20),
		NowMs:               func() int64 { return 1000 },
	}
	done := make(chan struct {
		result StreamPipeResult
		err    error
	}, 1)
	go func() {
		result, err := runPipe(body, nil, options, recorder, 1000)
		done <- struct {
			result StreamPipeResult
			err    error
		}{result, err}
	}()
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("err = %v", outcome.err)
		}
		// configured_deadline 属网关本地失败，catch 归一为失败 result。
		if outcome.result.Message != "上游流式响应 1s 后仍未返回首个有效输出" {
			t.Fatalf("message = %q", outcome.result.Message)
		}
		if outcome.result.ErrorCode != FirstByteTimeoutErrorCode {
			t.Fatalf("errorCode = %q", outcome.result.ErrorCode)
		}
		if recorder.last().errorCode != FirstByteTimeoutErrorCode {
			t.Fatalf("recorded errorCode = %q", recorder.last().errorCode)
		}
		// Node：first-byte timeout 不属于 gateway local failure。
		if outcome.result.GatewayLocalFailure {
			t.Fatal("first-byte timeout is not a gateway local failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestResponsePrecommitDeadlineAfterFirstByte(t *testing.T) {
	recorder := &failureRecorder{}
	body := newChanBody()
	now := int64(1000)
	options := StreamPipeOptions{
		ResponsePrecommitDeadlineAtMs: int64PtrOf(1050),
		NowMs:                         func() int64 { now += 60; return now },
	}
	done := make(chan struct {
		result StreamPipeResult
		err    error
	}, 1)
	go func() {
		result, err := runPipe(body, nil, options, recorder, 1000)
		done <- struct {
			result StreamPipeResult
			err    error
		}{result, err}
	}()
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("err = %v", outcome.err)
		}
		if outcome.result.Message != "网关请求墙钟已到，响应尚未产生可提交的语义结果" {
			t.Fatalf("message = %q", outcome.result.Message)
		}
		if outcome.result.ErrorCode != GatewayRequestWallBudgetExhaustedCode {
			t.Fatalf("errorCode = %q", outcome.result.ErrorCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestSSEInterceptorInterceptsBeforeDownstreamWrite(t *testing.T) {
	// 上下文超限错误在写入前命中系统默认策略 → 拦截并交由上层换号重试。
	policy := RuntimeResponseInspectionPolicy{
		ID: "default_openai_context_window_error", Source: PolicySourceSystemDefault, Name: "上下文超限",
		Enabled: true, ExecutionMode: "enforce", DataHandling: "replace_with_failure", RetryEnabled: true,
		ScopeType: "provider", ProviderCode: "openai", AccountSwitch: "request_next_account",
		Match: gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{"context_length_exceeded"}},
	}
	chunk := "data: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"上下文长度超限\"}}\n\n"
	recorder := &failureRecorder{}
	interceptor := NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{
		Policies:      []RuntimeResponseInspectionPolicy{policy},
		EndpointFamily: gatewayproto.EndpointFamilyChatCompletions,
		Context:       &ResponseInspectionRuntimeContext{ClientProfile: "codex"},
	})
	_, interceptorDownstream := newDownstream()
	result, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody([]byte(chunk)),
		Downstream:   interceptorDownstream,
		TimeoutProfile: TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Options: StreamPipeOptions{
			ClientRetryEnabled:                    true,
			RetryBeforeDownstreamWriteUntilOutput: true,
			Interceptor:                    interceptor,
			ResponseInspectionPolicies:     []RuntimeResponseInspectionPolicy{policy},
			ResponseInspectionContext:      &ResponseInspectionRuntimeContext{ClientProfile: "codex"},
			NowMs:                          func() int64 { return 1000 },
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.DownstreamBytesWritten != 0 {
		t.Fatalf("interception must happen before downstream write, bytes = %d", result.DownstreamBytesWritten)
	}
	if result.ResponseInspection == nil || result.ResponseInspection.PolicyID != "default_openai_context_window_error" {
		t.Fatalf("inspection = %+v", result.ResponseInspection)
	}
	if result.ErrorCode != gatewayprotoRetryable() {
		t.Fatalf("errorCode = %q", result.ErrorCode)
	}
}

func gatewayprotoRetryable() string { return "upstream_retryable_error" }

func TestCompressionMiddlewareSSEBypass(t *testing.T) {
	// SSE 响应即使客户端接受 gzip 也不压缩（kernel 压缩 Writer 的 SSE 旁路），
	// 且管道分片逐字节透传并即时 flush。
	handlerCalled := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { handlerCalled <- struct{}{} }()
		tracking := gatewaypreauth.NewTrackingWriter(w)
		_, err := PipeUpstreamStream(PipeUpstreamStreamInput{
			UpstreamBody: NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatDoneChunk)),
			Downstream:   StreamDownstream{Res: tracking},
			TimeoutProfile: TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
			StartedAtMs:         1000,
			HandleStreamFailure: func(string, string, StreamFailureContext) error { return nil },
			Options:             StreamPipeOptions{NowMs: func() int64 { return 1000 }},
		})
		if err != nil {
			t.Errorf("pipe err = %v", err)
		}
	})
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	kernel.CompressionMiddleware(handler).ServeHTTP(recorder, request)
	<-handlerCalled
	if encoding := recorder.Header().Get("Content-Encoding"); encoding == "gzip" {
		t.Fatal("SSE must bypass gzip compression")
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "你好") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("body = %q", body)
	}
}

func TestCompressionMiddlewareGzipsLargeJSON(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := `{"data":"` + strings.Repeat("x", 4096) + `"}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})
	request := httptest.NewRequest("GET", "/v1/models", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	kernel.CompressionMiddleware(handler).ServeHTTP(recorder, request)
	if recorder.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("large JSON should be gzipped")
	}
}
