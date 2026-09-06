package gatewaydispatch

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// Transport-layer tests: upstream/request.ts + upstream/body.ts semantics
// against a local mock upstream.

func TestRequestUpstreamSuccessAndBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1"}`))
	}))
	defer server.Close()

	response, err := RequestUpstream(context.Background(), server.URL+"/v1/chat/completions", UpstreamRequestOptions{
		Method: http.MethodPost,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   []byte(`{"model":"gpt-test"}`),
	}, TransportDeps{})
	if err != nil {
		t.Fatalf("RequestUpstream: %v", err)
	}
	if !response.OK() || response.Status() != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Status())
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	_ = response.Body.Close()
	if string(body) != `{"id":"chatcmpl-1"}` {
		t.Fatalf("body = %q", string(body))
	}
}

func TestRequestUpstreamGzipDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		gzipped := gzipBytes(t, []byte(`{"ok":true}`))
		_, _ = w.Write(gzipped)
	}))
	defer server.Close()

	response, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{Method: http.MethodGet}, TransportDeps{})
	if err != nil {
		t.Fatalf("RequestUpstream: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("decoded body = %q", string(body))
	}
}

func TestRequestUpstreamUnsupportedEncoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "zstd")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{Method: http.MethodGet}, TransportDeps{})
	var unsupported *UnsupportedUpstreamResponseEncodingError
	if !errorsAs(err, &unsupported) {
		t.Fatalf("expected UnsupportedUpstreamResponseEncodingError, got %v", err)
	}
	if unsupported.Message != "不支持的上游响应压缩编码: zstd" {
		t.Fatalf("message = %q", unsupported.Message)
	}
}

// br 在归档 Node 中显式支持（request.ts createUpstreamResponseDecoder ->
// createBrotliDecompress）；Go 用同一 andybalholm/brotli 库解压。
func TestRequestUpstreamBrotliDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(brotliBytes(t, []byte(`{"ok":true}`)))
	}))
	defer server.Close()

	response, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{Method: http.MethodGet}, TransportDeps{})
	if err != nil {
		t.Fatalf("RequestUpstream: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("decoded body = %q", string(body))
	}
}

// 双层编码按列表逆序解压（归档 Node decodeUpstreamResponseBody 同样
// [...encodings].reverse() 逐层套 decoder；"gzip, br" 表示先 gzip 再 br 编码）。
func TestRequestUpstreamGzipThenBrotliDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip, br")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(brotliBytes(t, gzipBytes(t, []byte(`{"ok":"double"}`))))
	}))
	defer server.Close()

	response, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{Method: http.MethodGet}, TransportDeps{})
	if err != nil {
		t.Fatalf("RequestUpstream: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != `{"ok":"double"}` {
		t.Fatalf("decoded body = %q", string(body))
	}
}

func TestRequestUpstreamAbortsBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RequestUpstream(ctx, "http://127.0.0.1:1/x", UpstreamRequestOptions{Method: http.MethodGet}, TransportDeps{})
	var aborted *UpstreamRequestAbortedError
	if !errorsAs(err, &aborted) || aborted.UpstreamRequestStarted {
		t.Fatalf("expected pre-start abort, got %v", err)
	}
}

func TestRequestUpstreamSocketTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	timeoutMs := int64(50)
	start := time.Now()
	_, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{
		Method:    http.MethodGet,
		TimeoutMs: &timeoutMs,
	}, TransportDeps{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var timeoutErr *UpstreamRequestTimeoutError
	if !errorsAs(err, &timeoutErr) || timeoutErr.Message != "上游请求超时" {
		t.Fatalf("expected UpstreamRequestTimeoutError, got %v", err)
	}
	var started *StartedTransportError
	if !errorsAs(err, &started) {
		t.Fatalf("expected started transport error, got %T", err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("timeout fired too early: %v", elapsed)
	}
}

func TestRequestUpstreamFirstByteDeadlineHandler(t *testing.T) {
	t.Run("continue keeps the request alive", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			_, _ = w.Write([]byte(`{"late":true}`))
		}))
		defer server.Close()

		deadlineMs := int64(30)
		response, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{
			Method:              http.MethodGet,
			FirstByteDeadlineMs: &deadlineMs,
			OnFirstByteDeadline: func(input FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
				return FirstByteDeadlineActionContinue
			},
		}, TransportDeps{})
		if err != nil {
			t.Fatalf("expected deadline-continue success, got %v", err)
		}
		_, _ = io.ReadAll(response.Body)
		_ = response.Body.Close()
	})

	t.Run("abort fails with configured deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(400 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		deadlineMs := int64(30)
		_, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{
			Method:              http.MethodGet,
			FirstByteDeadlineMs: &deadlineMs,
			OnFirstByteDeadline: func(input FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
				return FirstByteDeadlineActionAbort
			},
		}, TransportDeps{})
		if !IsGatewayFirstByteTimeoutError(err) {
			t.Fatalf("expected GatewayFirstByteTimeoutError, got %v", err)
		}
		var deadlineErr *GatewayFirstByteTimeoutError
		errorsAs(err, &deadlineErr)
		if deadlineErr.Source != FirstByteTimeoutSourceConfiguredDeadline {
			t.Fatalf("source = %v", deadlineErr.Source)
		}
		if deadlineErr.TimeoutMs != 30 {
			t.Fatalf("timeoutMs = %d", deadlineErr.TimeoutMs)
		}
	})
}

// 回归（第五轮审查项 5）：OnFirstByteDeadline 回调 panic 必须转成本地终止
// 错误路径（Node request.ts:266-282 .catch ->
// normalizeFirstByteDeadlineHandlerError），不能带崩 AfterFunc goroutine。
func TestRequestUpstreamFirstByteDeadlineHandlerPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deadlineMs := int64(30)
	_, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{
		Method:              http.MethodGet,
		FirstByteDeadlineMs: &deadlineMs,
		OnFirstByteDeadline: func(input FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
			panic("决策崩溃")
		},
	}, TransportDeps{})
	if err == nil {
		t.Fatal("expected the handler panic to fail the request")
	}
	// 非 error panic 值归一为「网关首字截止决策失败」（deadlineHandlerPanic）。
	if err.Error() != "网关首字截止决策失败" {
		t.Fatalf("error = %v", err)
	}
	// 本地终止错误不标记 upstreamRequestStarted（Node
	// markLocallyTerminatedUpstreamRequestError）。
	var started *StartedTransportError
	if errorsAs(err, &started) {
		t.Fatalf("locally terminated error must not be marked started: %T", err)
	}
}

func TestRequestUpstreamSignalAbortAfterStart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := RequestUpstream(ctx, server.URL, UpstreamRequestOptions{Method: http.MethodGet}, TransportDeps{})
	var aborted *UpstreamRequestAbortedError
	if !errorsAs(err, &aborted) || !aborted.UpstreamRequestStarted {
		t.Fatalf("expected started abort, got %v", err)
	}
}

func TestRequestUpstreamClientPool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":1}`))
	}))
	defer server.Close()

	pool := sharedupstreamhttp.NewClientPool()
	defer pool.CloseIdleConnections()
	response, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{Method: http.MethodGet}, TransportDeps{ClientPool: pool})
	if err != nil {
		t.Fatalf("RequestUpstream with pool: %v", err)
	}
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
}

func TestCopySafeUpstreamRequestHeaders(t *testing.T) {
	input := http.Header{}
	input.Set("Authorization", "Bearer client")
	input.Set("X-Api-Key", "secret")
	input.Set("X-Forwarded-For", "10.0.0.1")
	input.Set("X-Openai-Subagent", "codex")
	input.Set("Accept", "text/event-stream")
	input.Set("Openai-Beta", "responses=experimental")

	copied := CopySafeUpstreamRequestHeaders(input, CopySafeUpstreamHeadersOptions{})
	for _, name := range []string{"Authorization", "X-Api-Key", "X-Forwarded-For"} {
		if copied.Get(name) != "" {
			t.Fatalf("%s should be skipped", name)
		}
	}
	if copied.Get("Accept") != "text/event-stream" {
		t.Fatalf("Accept = %q", copied.Get("Accept"))
	}

	// Codex OAuth allowlist keeps the attestation + subagent + lite marker.
	preserved := CopySafeUpstreamRequestHeaders(input, CopySafeUpstreamHeadersOptions{PreserveOpenAIOAuthCodexClientHeaders: true})
	if preserved.Get("X-Openai-Subagent") != "codex" {
		t.Fatalf("codex allowlist should preserve x-openai-subagent")
	}
}

func TestBuildUpstreamHeadersCodex(t *testing.T) {
	account := UpstreamHeaderAccount{
		ID:       "acc-1",
		APIKey:   "oauth-token",
		Type:     "oauth",
		Credentials: map[string]any{"account_id": "chatgpt-account"},
	}
	headers := BuildUpstreamHeaders(http.Header{}, account)
	if headers.Get("Authorization") != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q", headers.Get("Authorization"))
	}
	if headers.Get("Accept") != "text/event-stream" {
		t.Fatalf("codex accept = %q", headers.Get("Accept"))
	}
	if headers.Get("Content-Type") != "application/json" {
		t.Fatalf("codex content-type = %q", headers.Get("Content-Type"))
	}
	if headers.Get("Originator") != OpenAICodexOriginator {
		t.Fatalf("originator = %q", headers.Get("Originator"))
	}
	if headers.Get("Chatgpt-Account-Id") != "chatgpt-account" {
		t.Fatalf("chatgpt-account-id = %q", headers.Get("Chatgpt-Account-Id"))
	}
}

func TestCopyResponseHeadersSkipsHopByHop(t *testing.T) {
	upstream := &GatewayUpstreamResponse{
		Header: http.Header{},
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Connection", "keep-alive, x-drop-token")
	upstream.Header.Set("Set-Cookie", "session=1")
	upstream.Header.Set("X-Drop-Token", "nope")
	upstream.Header.Set("X-Kong-Request-Id", "kong")
	upstream.Header.Set("X-Keep", "yes")

	set := map[string]string{}
	CopyResponseHeaders(upstream, func(name, value string) { set[name] = value })
	if _, ok := set["Connection"]; ok {
		t.Fatal("connection header must be skipped")
	}
	if _, ok := set["Set-Cookie"]; ok {
		t.Fatal("set-cookie must be skipped")
	}
	if _, ok := set["X-Drop-Token"]; ok {
		t.Fatal("connection token header must be skipped")
	}
	if _, ok := set["X-Kong-Request-Id"]; ok {
		t.Fatal("gateway prefix header must be skipped")
	}
	if set["X-Keep"] != "yes" {
		t.Fatalf("x-keep = %q", set["X-Keep"])
	}
}

func TestUpstreamSocketTimeoutMs(t *testing.T) {
	profile := gatewayTimeoutProfileForTest()
	if value := UpstreamSocketTimeoutMs(nil, profile, nil); value == nil || *value != 30_000 {
		t.Fatalf("non-stream socket timeout = %v", value)
	}
	if value := UpstreamSocketTimeoutMs(nil, profile, nil); value == nil || *value < profile.FirstResponseTimeoutMs {
		t.Fatalf("stream socket timeout floor violated: %v", value)
	}
	if value := UpstreamRequestTimeoutMs(profile); value == nil || *value != profile.FirstResponseTimeoutMs {
		t.Fatalf("request timeout = %v", value)
	}
	disabled := profile
	disabled.TimeoutsDisabled = true
	if value := UpstreamSocketTimeoutMs(nil, disabled, nil); value != nil {
		t.Fatalf("disabled socket timeout = %v", value)
	}
}

func TestReadUpstreamBodyLimitedTruncation(t *testing.T) {
	payload := strings.Repeat("a", 100)
	reader := strings.NewReader(payload)
	maxBytes := int64(10)
	result, err := ReadUpstreamBodyLimited(context.Background(), reader, LimitedBodyReadInput{MaxBytes: &maxBytes})
	if err != nil {
		t.Fatalf("ReadUpstreamBodyLimited: %v", err)
	}
	if !result.Truncated {
		t.Fatal("expected truncation")
	}
	if result.DiagnosticBodyText != strings.Repeat("a", 10)+"\n[truncated]" {
		t.Fatalf("diagnostic = %q", result.DiagnosticBodyText)
	}
}

func TestPipeNonStreamUpstreamResponse(t *testing.T) {
	payload := "chunk-one|chunk-two"
	reader := strings.NewReader(payload)
	var downstream strings.Builder
	startedAt := NowMs()
	result, err := PipeNonStreamUpstreamResponse(context.Background(), reader, &downstream, NonStreamPipeInput{
		StartedAt:  startedAt,
		OnFirstByte: func() {},
	})
	if err != nil {
		t.Fatalf("PipeNonStreamUpstreamResponse: %v", err)
	}
	if downstream.String() != payload {
		t.Fatalf("downstream = %q", downstream.String())
	}
	if result.TransferredBytes != len(payload) {
		t.Fatalf("transferred = %d", result.TransferredBytes)
	}
	if result.FirstByteMs == nil || *result.FirstByteMs < 0 {
		t.Fatalf("firstByteMs = %v", result.FirstByteMs)
	}
	if result.CapturedBodyText == nil || *result.CapturedBodyText != payload {
		t.Fatalf("captured = %v", result.CapturedBodyText)
	}
}

func TestPipeNonStreamUpstreamResponseAbortMidway(t *testing.T) {
	reader := &slowAbortReader{ctx: newCancelledContext()}
	var downstream strings.Builder
	_, err := PipeNonStreamUpstreamResponse(context.Background(), reader, &downstream, NonStreamPipeInput{
		StartedAt: NowMs(),
	})
	var aborted *UpstreamRequestAbortedError
	if !errorsAs(err, &aborted) {
		t.Fatalf("expected abort error, got %v", err)
	}
}

func TestPipeNonStreamUpstreamResponseForInspection(t *testing.T) {
	payload := "hello-inspection-world"
	reader := strings.NewReader(payload)
	var downstream strings.Builder
	var inspectionBody []byte
	result, err := PipeNonStreamUpstreamResponseForInspection(context.Background(), reader, &downstream, InspectableNonStreamPipeInput{
		NonStreamPipeInput: NonStreamPipeInput{StartedAt: NowMs()},
		InspectBytes:       6,
		BeforeDownstreamCommit: func(body []byte) error {
			inspectionBody = append([]byte(nil), body...)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("inspection pipe: %v", err)
	}
	if string(inspectionBody) != "hello-" {
		t.Fatalf("inspection body = %q", string(inspectionBody))
	}
	if result.FullyBuffered {
		t.Fatal("expected streaming inspection (body exceeds inspect bytes)")
	}
	if downstream.String() != payload {
		t.Fatalf("downstream = %q", downstream.String())
	}
}

type slowAbortReader struct{ ctx context.Context }

func newCancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func (r *slowAbortReader) Read(p []byte) (int, error) {
	<-r.ctx.Done()
	return 0, &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}
}

func TestIsProvenUpstreamBodyTransportError(t *testing.T) {
	wrapped := &UpstreamBodyReadIncompleteError{Cause: &StartedBodyTransportError{Err: context.Canceled}}
	if !IsProvenUpstreamBodyTransportError(wrapped) {
		t.Fatal("incomplete body with started cause should be proven")
	}
	if IsProvenUpstreamBodyTransportError(context.Canceled) {
		t.Fatal("raw context error is not proven")
	}
	pipeErr := &NonStreamUpstreamBodyPipeError{OriginalError: &StartedBodyTransportError{Err: context.Canceled}}
	if !IsProvenUpstreamBodyTransportError(pipeErr) {
		t.Fatal("pipe error with started cause should be proven")
	}
}

// shared test helpers

func gatewayTimeoutProfileForTest() gatewayrouting.GatewayTimeoutProfile {
	return gatewayrouting.GatewayTimeoutProfile{
		FirstResponseTimeoutMs: 5_000,
		FirstByteTimeoutMs:     5_000,
		IdleTimeoutMs:          10_000,
	}
}

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	_, _ = writer.Write(payload)
	_ = writer.Close()
	return buffer.Bytes()
}

func brotliBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := brotli.NewWriter(&buffer)
	_, _ = writer.Write(payload)
	_ = writer.Close()
	return buffer.Bytes()
}

// 回归测试（P0）：响应体必须在 RequestUpstream 返回后仍可完整读取。
// 历史缺陷：defer requestCancel() 在返回时取消请求上下文，超过传输内部
// 缓冲（~4KB）的响应体在后续 Read 时报 context canceled——大响应与长
// SSE 流被截断。Node 语义是 headers 到达只停定时器，request handle 在
// 响应结束时销毁（body Close/EOF）。
func TestRequestUpstreamLargeBodyReadableAfterReturn(t *testing.T) {
	const payloadSize = 64 * 1024
	payload := strings.Repeat("x", payloadSize)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-big","content":"`+payload+`"}`)
	}))
	defer server.Close()

	response, err := RequestUpstream(context.Background(), server.URL+"/v1/chat/completions", UpstreamRequestOptions{
		Method: http.MethodPost,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   []byte(`{"model":"gpt-test"}`),
	}, TransportDeps{})
	if err != nil {
		t.Fatalf("RequestUpstream: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("large body must read fully after return: %v", err)
	}
	_ = response.Body.Close()
	if len(body) != len(`{"id":"chatcmpl-big","content":"`+payload+`"}`) {
		t.Fatalf("body truncated: got %d bytes", len(body))
	}
}

// SSE 长流同理：RequestUpstream 返回后事件流必须继续可读。
func TestRequestUpstreamSSEStreamReadableAfterReturn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 40; i++ {
			_, _ = io.WriteString(w, "data: {\"chunk\":"+strconv.Itoa(i)+",\"pad\":\""+strings.Repeat("y", 512)+"\"}\n\n")
			flusher.Flush()
		}
	}))
	defer server.Close()

	response, err := RequestUpstream(context.Background(), server.URL+"/v1/chat/completions", UpstreamRequestOptions{
		Method: http.MethodPost,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   []byte(`{"model":"gpt-test","stream":true}`),
	}, TransportDeps{})
	if err != nil {
		t.Fatalf("RequestUpstream: %v", err)
	}
	reader := bufio.NewReader(response.Body)
	events := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(line, "data: ") {
			events++
		}
	}
	_ = response.Body.Close()
	if events != 40 {
		t.Fatalf("stream truncated after return: got %d events, want 40", events)
	}
}

// 回归（第五轮审查项 6，Node request.setTimeout 语义）：响应头之后 body 中段
// 空闲 TimeoutMs 必须销毁请求，Read 以 UpstreamRequestTimeoutError 失败，
// 而不是永久阻塞。流式管道的秒级 idle 超时（ReadStreamChunkWithIdleTimeout）
// 之上，这是 Node socket idle timer 的等价兜底。
func TestRequestUpstreamBodyIdleWatchFailsSilentUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"first\":true}\n\n")
		w.(http.Flusher).Flush()
		// 之后既不发数据也不关闭：Node request.setTimeout 的 destroy 点。
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()

	timeoutMs := int64(80)
	response, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{
		Method:    http.MethodGet,
		TimeoutMs: &timeoutMs,
	}, TransportDeps{})
	if err != nil {
		t.Fatalf("RequestUpstream: %v", err)
	}
	buffer := make([]byte, 256)
	n, err := response.Body.Read(buffer)
	if err != nil {
		t.Fatalf("first read failed before any idle: %v", err)
	}
	if !strings.Contains(string(buffer[:n]), "first") {
		t.Fatalf("first chunk = %q", string(buffer[:n]))
	}

	start := time.Now()
	idleErr := (*UpstreamRequestTimeoutError)(nil)
	for i := 0; i < 5; i++ {
		_, err = response.Body.Read(buffer)
		if err == nil {
			continue
		}
		if errorsAs(err, &idleErr) {
			break
		}
	}
	_ = response.Body.Close()
	if idleErr == nil {
		t.Fatalf("expected UpstreamRequestTimeoutError after idle, last err=%v", err)
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("idle watch fired too early: %v", elapsed)
	}
}

// DisableTimeouts 同时关闭 body idle watch（Node disableTimeouts 跳过
// request.setTimeout）。
func TestRequestUpstreamBodyIdleWatchDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"first\":true}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"second\":true}\n\n")
	}))
	defer server.Close()

	timeoutMs := int64(50)
	response, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{
		Method:          http.MethodGet,
		TimeoutMs:       &timeoutMs,
		DisableTimeouts: true,
	}, TransportDeps{})
	if err != nil {
		t.Fatalf("RequestUpstream: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body with timeouts disabled: %v", err)
	}
	if !strings.Contains(string(body), "second") {
		t.Fatalf("body = %q", string(body))
	}
}
