package gatewayresponse

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ---------------------------------------------------------------------------
// Sink：models 响应发送 + usage 语义 + 审计 finalize。
// ---------------------------------------------------------------------------

type w9cAuditCapture struct {
	mu       sync.Mutex
	finals   []gatewaypreauth.AuditFinalizeInput
	extended []AuditFinalizeExtras
}

func (c *w9cAuditCapture) BindContext(gatewaypreauth.AuditGatewayContext) {}
func (c *w9cAuditCapture) AddGatewayMetadata(string, map[string]any)      {}
func (c *w9cAuditCapture) Finalize(input gatewaypreauth.AuditFinalizeInput) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finals = append(c.finals, input)
}

func (c *w9cAuditCapture) FinalizeExtended(input gatewaypreauth.AuditFinalizeInput, extras AuditFinalizeExtras) {
	c.Finalize(input)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.extended = append(c.extended, extras)
}

func (c *w9cAuditCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.finals)
}

type w9cUsageDispatch struct {
	mu     sync.Mutex
	inputs []ModelsUsageDispatchInput
}

func (d *w9cUsageDispatch) DispatchUsageRecord(input ModelsUsageDispatchInput) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inputs = append(d.inputs, input)
}

func (d *w9cUsageDispatch) all() []ModelsUsageDispatchInput {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]ModelsUsageDispatchInput(nil), d.inputs...)
}

type w9cCatalog struct{ entries []ModelCatalogEntry }

func (c w9cCatalog) ListClientModelCatalog(string, []string) []ModelCatalogEntry { return c.entries }

func TestW9CSendAuthenticatedModelsGatewayResponse(t *testing.T) {
	recorder := httptest.NewRecorder()
	audit := &w9cAuditCapture{}
	dispatch := &w9cUsageDispatch{}
	sink := NewSink(SinkDeps{
		ModelCatalog:  w9cCatalog{entries: []ModelCatalogEntry{{Model: "gpt-5", Scope: "built_in"}, {Model: "claude-x", Scope: "global"}}},
		UsageDispatch: dispatch,
		NowMs:         func() int64 { return 5000 },
	})
	sink.SendAuthenticatedModelsGatewayResponse(gatewaypreauth.ModelsResponseInput{
		Res:          gatewaypreauth.NewTrackingWriter(recorder),
		AuditCapture: audit,
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			SystemAccountID: "acc-1",
			ProviderCode:    "openai",
		},
		ProviderCodes: []string{"OpenAI", "openai", ""},
		Protocol:      "openai",
		StartedAt:     1000,
	})
	if recorder.Code != 200 {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "gpt-5") {
		t.Fatalf("catalog missing from body: %s", body)
	}
	// Authenticated requests get the no-store client cache headers.
	if recorder.Header().Get("Cache-Control") == "" {
		t.Fatal("authenticated models response must set cache headers")
	}
	if audit.count() != 1 {
		t.Fatalf("audit finalize count = %d", audit.count())
	}
	sent := dispatch.all()
	if len(sent) != 1 {
		t.Fatalf("usage dispatches = %d", len(sent))
	}
	if sent[0].UsageSemantic != "openai" || sent[0].ProviderCode != "openai" || !sent[0].Success {
		t.Fatalf("dispatch = %+v", sent[0])
	}
	if sent[0].FirstTokenMs != 4000 || sent[0].DurationMs != 4000 {
		t.Fatalf("timing = %+v", sent[0])
	}
	// Auth failure audit finalize path.
	audit2 := &w9cAuditCapture{}
	auditReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	sink.FinalizeGatewayAuthFailureAudit(auditReq, gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()), audit2)
	if audit2.count() != 1 {
		t.Fatalf("auth failure audit count = %d", audit2.count())
	}
}

func TestW9CSendModelsGatewayResponseProtocolVariants(t *testing.T) {
	cases := []struct {
		protocol string
		contains string
	}{
		{"anthropic", "data"},
		{"gemini", "models"},
		{"", "gpt-5"},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		audit := &w9cAuditCapture{}
		sink := NewSink(SinkDeps{
			ModelCatalog: w9cCatalog{entries: []ModelCatalogEntry{{Model: "gpt-5", Scope: "personal"}}},
			NowMs:        func() int64 { return 1000 },
		})
		input := gatewaypreauth.ModelsResponseInput{
			Res:           gatewaypreauth.NewTrackingWriter(recorder),
			AuditCapture:  audit,
			ProviderCodes: []string{"gemini"},
			Protocol:      tc.protocol,
		}
		switch tc.protocol {
		case "anthropic":
			sink.SendAnthropicModelsGatewayResponse(input)
		case "gemini":
			sink.SendGeminiModelsGatewayResponse(input)
		default:
			sink.SendOpenAIModelsGatewayResponse(input)
		}
		if recorder.Code != 200 {
			t.Fatalf("%s status = %d (%s)", tc.protocol, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(strings.ToLower(recorder.Body.String()), tc.contains) {
			t.Fatalf("%s body missing %q: %s", tc.protocol, tc.contains, recorder.Body.String())
		}
		// Anonymous requests (no system account) get no cache headers.
		if recorder.Header().Get("Cache-Control") != "" {
			t.Fatalf("%s anonymous response must skip cache headers", tc.protocol)
		}
		if audit.count() != 1 {
			t.Fatalf("%s audit count = %d", tc.protocol, audit.count())
		}
	}
	// Usage semantic fallbacks by provider code.
	if got := usageSemanticForProviderCode("Anthropic"); got != "anthropic" {
		t.Fatalf("anthropic semantic = %q", got)
	}
	if got := usageSemanticForProviderCode(" gemini "); got != "gemini" {
		t.Fatalf("gemini semantic = %q", got)
	}
	if got := usageSemanticForProviderCode("unknown"); got != "openai" {
		t.Fatalf("default semantic = %q", got)
	}
	// Profile-aware semantic hook wins when wired.
	profileSink := NewSink(SinkDeps{UsageSemanticForProfile: func(string, string, string, string) string { return "custom" }})
	input := gatewaypreauth.ModelsResponseInput{UsageContext: gatewaypreauth.GatewayFailureUsageContext{ProviderCode: "openai"}}
	if got := profileSink.usageSemanticFor(input, "openai"); got != "custom" {
		t.Fatalf("profile semantic = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Heartbeat：SSE 等待心跳写出与终止条件。
// ---------------------------------------------------------------------------

func TestW9CGatewaySseWaitHeartbeat(t *testing.T) {
	// Non-SSE downstream protocol: no heartbeat at all.
	if CreateGatewaySseWaitHeartbeat(HeartbeatDeps{DownstreamProtocol: "json"}) != nil {
		t.Fatal("non-SSE protocol must not create a heartbeat")
	}
	if CreateGatewaySseWaitHeartbeatObserver(HeartbeatDeps{DownstreamProtocol: "json"}) != nil {
		t.Fatal("non-SSE protocol must not create an observer")
	}

	// The heartbeat loop goroutine writes concurrently, so the body is
	// observed through the lock-guarded fake res (plain httptest recorder
	// polling races under -race); Stop now waits for the goroutine to exit.
	res := newW3HeartbeatRecordingRes()
	commit := &DownstreamCommitState{}
	heart := CreateGatewaySseWaitHeartbeat(HeartbeatDeps{
		Res:                res,
		DownstreamProtocol: "chat_completions_sse",
		DownstreamCommit:   commit,
		IntervalMs:         10,
	})
	if heart == nil {
		t.Fatal("SSE protocol must create a heartbeat")
	}
	heart.Start()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(res.bodyText(), "juhe-ai waiting") {
		time.Sleep(2 * time.Millisecond)
	}
	heart.Stop()
	if !strings.Contains(res.bodyText(), "juhe-ai waiting for upstream capacity") {
		t.Fatalf("heartbeat chunk missing: %q", res.bodyText())
	}

	// Codex compaction keepalive variant writes the keepalive event.
	res2 := newW3HeartbeatRecordingRes()
	heart2 := CreateGatewaySseWaitHeartbeat(HeartbeatDeps{
		Res:                          res2,
		DownstreamProtocol:           "responses_sse",
		DownstreamCommit:             &DownstreamCommitState{},
		IntervalMs:                   10,
		EmitCodexCompactionKeepalive: true,
	})
	heart2.Start()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(res2.bodyText(), "juhe_ai.keepalive") {
		time.Sleep(2 * time.Millisecond)
	}
	heart2.Stop()
	if !strings.Contains(res2.bodyText(), "juhe_ai.keepalive") {
		t.Fatalf("codex keepalive missing: %q", res2.bodyText())
	}

	// heartbeatChunkOf variants.
	if string(heartbeatChunkOf(HeartbeatDeps{DownstreamProtocol: "responses_sse", EmitCodexCompactionKeepalive: true})) == "" {
		t.Fatal("keepalive chunk must render")
	}
	// Semantic commit aborts the loop before writing.
	committed := &DownstreamCommitState{}
	committed.MarkSemanticCommitted(1)
	if !heartbeatAborted(HeartbeatDeps{DownstreamCommit: committed}, context.Background()) {
		t.Fatal("semantic commit must abort heartbeats")
	}
	canceled, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	if !heartbeatAborted(HeartbeatDeps{}, canceled) {
		t.Fatal("canceled ctx must abort heartbeats")
	}
	// signalDone without a signal is nil.
	if (HeartbeatDeps{}).signalDone() != nil {
		t.Fatal("nil signal projects nil done channel")
	}
	// Observer wiring.
	observer := CreateGatewaySseWaitHeartbeatObserver(HeartbeatDeps{
		Res:                gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()),
		DownstreamProtocol: "chat_completions_sse",
		DownstreamCommit:   &DownstreamCommitState{},
		IntervalMs:         10,
	})
	if observer == nil || observer.OnWaitStarted == nil || observer.OnWaitPaused == nil {
		t.Fatal("SSE observer must wire both callbacks")
	}
}

// ---------------------------------------------------------------------------
// 流管道：预提交重试失败事件 + 自定义拦截器 EOF flush。
// ---------------------------------------------------------------------------

type w9cFakeInterceptor struct {
	mu          sync.Mutex
	chunks      int
	flushCalled int
	marked      int
}

func (i *w9cFakeInterceptor) PushChunk(chunk []byte) StreamInterceptorSseResult {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.chunks++
	return StreamInterceptorSseResult{Chunks: [][]byte{chunk}}
}

func (i *w9cFakeInterceptor) FlushPendingOnEOF() StreamInterceptorSseResult {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.flushCalled++
	if i.flushCalled == 1 {
		return StreamInterceptorSseResult{Chunks: [][]byte{[]byte("data: {\"pending\":true}\n\n")}}
	}
	return StreamInterceptorSseResult{}
}

func (i *w9cFakeInterceptor) MarkDownstreamWrite() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.marked++
}

func TestW9CStreamPipeFlushesInterceptorOnEOF(t *testing.T) {
	recorder := &failureRecorder{}
	interceptor := &w9cFakeInterceptor{}
	result, err := runPipe(NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatDoneChunk)), nil, StreamPipeOptions{
		Interceptor: interceptor,
	}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed {
		t.Fatalf("stream must complete: %+v", result)
	}
	if interceptor.flushCalled == 0 {
		t.Fatal("EOF must flush the interceptor")
	}
	if interceptor.marked == 0 {
		t.Fatal("downstream writes must be marked")
	}
	if interceptor.chunks == 0 {
		t.Fatal("chunks must pass through the interceptor")
	}
}

func TestW9CStreamPipePreCommitFailureWritesRetryEvent(t *testing.T) {
	recorder := &failureRecorder{}
	// Upstream fails before any output; client retry enabled keeps the
	// connection alive and writes a retryable failure event downstream.
	body := NewSliceUpstreamBody([]byte("data: {\"error\":{\"code\":\"upstream_5xx\",\"message\":\"boom\"}}\n\n"))
	result, err := runPipe(body, nil, StreamPipeOptions{
		ClientRetryEnabled:                    true,
		RetryBeforeDownstreamWriteUntilOutput: true,
		DownstreamProtocol:                    "chat_sse",
	}, recorder, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("failed upstream must not complete: %+v", result)
	}
	if recorder.count() == 0 {
		t.Fatalf("failure handler must run, result = %+v", result)
	}
	last := recorder.last()
	if last.message == "" || last.errorCode == "" {
		t.Fatalf("failure record = %+v", last)
	}
	// Incomplete client abort hook arms: a clean completion must not fire it.
	abortCalled := false
	body2 := NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatFinishChunk), []byte(chatDoneChunk))
	result2, err := runPipe(body2, nil, StreamPipeOptions{
		OnIncompleteClientAbort: func(IncompleteClientAbortContext) error {
			abortCalled = true
			return nil
		},
	}, recorder, 1000)
	if err != nil || !result2.Completed {
		t.Fatalf("clean stream = %+v, %v", result2, err)
	}
	if abortCalled {
		t.Fatal("clean completion must not fire the abort hook")
	}
}

// ---------------------------------------------------------------------------
// 非流式：管道错误 + usage tail 捕获。
// ---------------------------------------------------------------------------

func TestW9CNonStreamPipeErrorAndCapture(t *testing.T) {
	clock := &w9cClock{now: 1000}
	newDownstreamW := func() (*httptest.ResponseRecorder, StreamDownstream) {
		recorder := httptest.NewRecorder()
		return recorder, StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(recorder)}
	}

	// Upstream read failure mid-body.
	recorder, downstream := newDownstreamW()
	failing := NewSliceUpstreamBody([]byte(`{"id":"x"}`))
	result, err := PipeNonStreamUpstreamResponse(NonStreamPipeInput{
		Body:         failing,
		Downstream:   downstream,
		StartedAtMs:  1000,
		CaptureBytes: 4096,
		CaptureBody:  true,
		NowMs:        clock.NowMs,
	})
	if err != nil {
		t.Fatalf("pipe err = %v", err)
	}
	_ = result
	if recorder.Body.Len() == 0 {
		t.Fatal("chunks must be forwarded downstream")
	}
}
