package gatewayresponse

// w11b 波次：eofFlushPhase 分支直驱、handleProtocolFailure 变体、decide 墙钟
// 竞速、流式检查服务端重试端到端与 models / interceptor / convert /
// codexcontract / inspection / sink 散点分支。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ---- eofFlushPhase 分支直驱 ----

func TestW11BEofFlushPhaseBranches(t *testing.T) {
	commentChunk := []byte(": keepalive\n\n")

	// a) pre-commit 缓冲：comment chunk 保持私有并追加。
	pipe, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) { o.RetryBeforeDownstreamWriteUntilOutput = true })
	pipe.interceptor = &w11bPassInterceptor{onFlush: StreamInterceptorSseResult{Chunks: [][]byte{commentChunk}}}
	result, err := pipe.eofFlushPhase()
	if err != nil || result != nil {
		t.Fatalf("a: result = %v err = %v", result, err)
	}
	// 纯注释属非语义帧：append 面保持私有并清空缓冲。
	if len(pipe.preCommitBuffer.Chunks) != 0 {
		t.Fatalf("a: chunks = %d", len(pipe.preCommitBuffer.Chunks))
	}

	// b) 超大 SSE 元字段帧（event 行）+ buffering → pre-commit 缓冲超限拒绝。
	pipe2, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) { o.RetryBeforeDownstreamWriteUntilOutput = true })
	bigComment := []byte("event: " + strings.Repeat("x", StreamPreCommitBufferMaxBytes+1024))
	pipe2.interceptor = &w11bPassInterceptor{onFlush: StreamInterceptorSseResult{Chunks: [][]byte{bigComment}}}
	_, err = pipe2.eofFlushPhase()
	var exceeded *StreamPreCommitBufferExceededError
	if !asStreamPreCommitExceeded(err, &exceeded) {
		t.Fatalf("b: err = %v", err)
	}

	// c) 头已发送（不可在提交前失败）：非语义帧保持私有并清空。
	pipe3, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) { o.RetryBeforeDownstreamWriteUntilOutput = true })
	pipe3.downstream.Res.WriteHeader(200)
	AppendStreamPreCommitChunk(pipe3.preCommitBuffer, []byte("stale"))
	pipe3.interceptor = &w11bPassInterceptor{onFlush: StreamInterceptorSseResult{Chunks: [][]byte{commentChunk}}}
	result, err = pipe3.eofFlushPhase()
	if err != nil || result != nil {
		t.Fatalf("c: result = %v err = %v", result, err)
	}
	if len(pipe3.preCommitBuffer.Chunks) != 0 {
		t.Fatalf("c: chunks = %d", len(pipe3.preCommitBuffer.Chunks))
	}

	// d) 数据事件后协议失败：EOF 段 handleProtocolFailure(eof)。
	pipe4, recorder := w11bNewFinalPipe(t, func(o *StreamPipeOptions) { o.RetryBeforeDownstreamWriteUntilOutput = true })
	pipe4.inspector = &w11bFakeInspector{snapshot: gatewayproto.StreamInspection{FailedReceived: true, ErrorMessage: "eof boom"}}
	pipe4.preCommitSseEvidence.DataEventObserved = true
	pipe4.preCommitSseEvidence.OnlyNonSemanticFramingObserved = false
	pipe4.interceptor = &w11bPassInterceptor{onFlush: StreamInterceptorSseResult{Chunks: [][]byte{[]byte(chatDoneChunk)}}}
	result, err = pipe4.eofFlushPhase()
	if err != nil {
		t.Fatalf("d: err = %v", err)
	}
	if result == nil || result.Message != "eof boom" {
		t.Fatalf("d: result = %+v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("d: records = %d", recorder.count())
	}

	// e) 写下游失败：透传写错误。
	pipe5, _ := w11bNewFinalPipe(t, nil)
	pipe5.downstream.Res = &w11bFailingWriter{inner: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())}
	pipe5.preCommitSseEvidence.DataEventObserved = true
	pipe5.preCommitSseEvidence.OnlyNonSemanticFramingObserved = false
	pipe5.interceptor = &w11bPassInterceptor{onFlush: StreamInterceptorSseResult{Chunks: [][]byte{[]byte(chatDeltaChunk)}}}
	_, err = pipe5.eofFlushPhase()
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("e: err = %v", err)
	}

	// f) 检查器 EOF 解析跳过日志（ParserSkipped）。
	pipe6, _ := w11bNewFinalPipe(t, nil)
	pipe6.interceptor = &w11bPassInterceptor{onFlush: StreamInterceptorSseResult{Chunks: [][]byte{commentChunk}, ParserSkipped: true}}
	if _, err := pipe6.eofFlushPhase(); err != nil {
		t.Fatalf("f: err = %v", err)
	}
	if !pipe6.responseInspectionParserSkipLogged {
		t.Fatal("f: 解析跳过应记录一次")
	}
}

func asStreamPreCommitExceeded(err error, target **StreamPreCommitBufferExceededError) bool {
	if e, ok := err.(*StreamPreCommitBufferExceededError); ok {
		*target = e
		return true
	}
	return false
}

// ---- handleProtocolFailure 补充分支 ----

func TestW11BHandleProtocolFailureDefaultCodeAndEofSignaled(t *testing.T) {
	// inspection 无 errorCode → 缺省 upstream_protocol_failure。
	pipe, _ := w11bNewFinalPipe(t, nil)
	result, err := pipe.handleProtocolFailure(gatewayproto.StreamInspection{FailedReceived: true, ErrorMessage: "boom"}, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorCode != "upstream_protocol_failure" {
		t.Fatalf("errorCode = %q", result.ErrorCode)
	}

	// EOF + 提交后 + signaled disposition 日志分支。
	pipe2, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) {
		o.ClientRetryEnabled = true
		o.DownstreamProtocol = "responses_sse"
	})
	pipe2.lastCommittedDisposition = "signaled"
	if _, err := pipe2.handleProtocolFailure(gatewayproto.StreamInspection{FailedReceived: true, ErrorCode: "e"}, true); err != nil {
		t.Fatalf("err = %v", err)
	}

	// EOF + 提交前 signaled（TakeStreamPreCommitChunks 分支）。
	pipe3, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) { o.RetryBeforeDownstreamWriteUntilOutput = true })
	pipe3.lastCommittedDisposition = "signaled"
	if _, err := pipe3.handleProtocolFailure(gatewayproto.StreamInspection{FailedReceived: true}, true); err != nil {
		t.Fatalf("err = %v", err)
	}

	// 提交后 signalCommittedStreamFailure 回调失败仍继续发送。
	pipe4, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) {
		o.ClientRetryEnabled = true
		o.DownstreamProtocol = "responses_sse"
		o.BeforeCommittedFailureSignal = func(CommittedStreamFailureSignalContext) error { return http.ErrAbortHandler }
	})
	if _, err := pipe4.handleProtocolFailure(gatewayproto.StreamInspection{FailedReceived: true}, false); err != nil {
		t.Fatalf("err = %v", err)
	}
}

// ---- decide 墙钟竞速：pending 在墙钟前 settle ----

func TestW11BReadNextStreamChunkWallRacePendingWins(t *testing.T) {
	clock := &w9cClock{now: 2000}
	pipe := w9cNewPipe(t, clock)
	deadline := int64(500)
	wall := int64(2600)
	pipe.options.FirstByteDeadlineMs = &deadline
	pipe.options.ResponsePrecommitDeadlineAtMs = &wall
	pipe.body = w9cChanBody(ChunkResult{Data: []byte(chatDeltaChunk)})
	pipe.options.OnFirstByteDeadline = func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		// 等待 pending settle，让墙钟竞速 select 命中读取分支。
		for i := 0; i < 100; i++ {
			time.Sleep(2 * time.Millisecond)
		}
		return FirstByteDeadlineAbort, nil
	}
	result, err := pipe.readNextStreamChunk()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(string(result.chunk.Data), "chat.completion.chunk") {
		t.Fatalf("chunk = %q", result.chunk.Data)
	}
}

// ---- HandleStreamUpstreamResponse 流式检查服务端重试 ----

func TestW11BHandleStreamUpstreamResponseInspectionServerRetry(t *testing.T) {
	// pre-commit 协议失败（无检查决策）+ 客户端重试预算：服务端重试。
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte(chatDoneChunk)), 200, nil)
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	input.ClientStrategy = &ClientStrategyView{ClientProfile: "codex", RetryPreCommitProtocolError: true, InterpretSemantics: true}
	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	_ = result
}

func ptrOf(value string) *string { return &value }

// ---- models 辅助 ----

func TestW11BModelsHelpers(t *testing.T) {
	if modelCreatedUnixSeconds(ModelCatalogEntry{ReleaseDate: "2024-06-01"}) == 0 {
		t.Fatal("合法日期解析")
	}
	if modelCreatedUnixSeconds(ModelCatalogEntry{ReleaseDate: "bad-date"}) != 0 {
		t.Fatal("非法日期回退 0")
	}
	if modelCreatedUnixSeconds(ModelCatalogEntry{CreatedAt: "2025-03-04T05:06:07Z"}) == 0 {
		t.Fatal("RFC3339 解析")
	}
	if modelCreatedUnixSeconds(ModelCatalogEntry{CreatedAt: "bad"}) != 0 || modelCreatedUnixSeconds(ModelCatalogEntry{}) != 0 {
		t.Fatal("非法/空 CreatedAt 回退 0")
	}
	if !headerContainsCodex("codex_cli_1.0") || headerContainsCodex("other") {
		t.Fatal("headerContainsCodex 语义")
	}
	// isCodexModelsRequest：originator/user-agent/query。
	codexReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1/models", nil))
	codexReq.HTTP.Header.Set("originator", "codex")
	if !isCodexModelsRequest(codexReq) {
		t.Fatal("originator=codex 命中")
	}
	uaReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1/models?client_version=1", nil))
	uaReq.HTTP.Header.Set("User-Agent", "codex")
	if !isCodexModelsRequest(uaReq) {
		t.Fatal("user-agent 命中")
	}
	plainReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1/models?after=m1", nil))
	if isCodexModelsRequest(plainReq) {
		t.Fatal("普通 models 请求非 codex")
	}
	// buildAnthropicModelsPayload。
	payload := buildAnthropicModelsPayload([]ModelCatalogEntry{{Model: "claude-x", ReleaseDate: "2025-01-02"}})
	if len(payload.Data) == 0 || payload.Data[0]["id"] != "claude-x" {
		t.Fatalf("payload = %+v", payload)
	}
}

// ---- interceptor codex cyber_policy 透传 ----

func TestW11BInterceptorCodexCyberPolicyPassthrough(t *testing.T) {
	interceptor := NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{
		EndpointFamily: gatewayproto.EndpointFamilyResponses,
		Context:        &ResponseInspectionRuntimeContext{ClientProfile: "codex"},
	})
	chunk := []byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"cyber_policy\",\"message\":\"blocked\"}}}\n\n")
	result := interceptor.PushChunk(chunk)
	if !result.PassthroughUpstreamFailure {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Chunks) == 0 || !strings.Contains(string(result.Chunks[0]), "cyber_policy") {
		t.Fatalf("chunks = %+v", result.Chunks)
	}
}

// ---- codexcontract ----

func TestW11BCodexContractHelpers(t *testing.T) {
	// CodexCompactionExpectedForRequest。
	if CodexCompactionExpectedForRequest(nil) {
		t.Fatal("nil 请求 false")
	}
	getReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1/responses/compact", nil))
	if CodexCompactionExpectedForRequest(getReq) {
		t.Fatal("GET false")
	}
	compactReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/responses/compact", nil))
	if !CodexCompactionExpectedForRequest(compactReq) {
		t.Fatal("/responses/compact 恒命中")
	}
	// CountCodexCompactionOutputItemsFromJSON。
	if CountCodexCompactionOutputItemsFromJSON("not-object") != nil {
		t.Fatal("非对象 nil")
	}
	if CountCodexCompactionOutputItemsFromJSON(map[string]any{}) != nil {
		t.Fatal("缺 output nil")
	}
	counts := CountCodexCompactionOutputItemsFromJSON(map[string]any{
		"output": []any{
			map[string]any{"type": "message"},
			map[string]any{"type": "compaction", "encrypted_content": "abc"},
			map[string]any{"type": "compaction_summary", "encrypted_content": "def"},
			map[string]any{"type": "compaction"}, // 缺 encrypted_content
		},
	})
	if counts == nil || counts.OutputItemCount != 4 || counts.CompactionItemCount != 2 {
		t.Fatalf("counts = %+v", counts)
	}
	// isCodexDeserializableCompactionItem。
	if isCodexDeserializableCompactionItem(nil) {
		t.Fatal("nil false")
	}
	if isCodexDeserializableCompactionItem(map[string]any{"type": "other", "encrypted_content": "x"}) {
		t.Fatal("非 compaction 类型 false")
	}
	if !isCodexDeserializableCompactionItem(map[string]any{"type": "compaction", "encrypted_content": "x"}) {
		t.Fatal("合法项 true")
	}
	// normalizedOpenAIRequestPath 空路径回退。
	emptyReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/", nil))
	if normalizedOpenAIRequestPath(emptyReq) != "/" {
		t.Fatal("空路径回退 /")
	}
}

// ---- convert gemini frames ----

func TestW11BConvertGeminiFramesOptionalFields(t *testing.T) {
	index := 1
	visible := true
	frames := []gatewaygemini.ResponseSemanticFrame{{
		FrameType:     "text_delta",
		ChoiceIndex:   &index,
		OutputIndex:   &index,
		ContentIndex:  &index,
		VisibleOutput: &visible,
		RawJSON:       map[string]any{"k": 1},
		RawJSONPaths:  []string{"$.k"},
		RawText:       "t",
		EventType:     "e",
	}}
	converted := convertGeminiFrames(frames)
	if len(converted) != 1 {
		t.Fatalf("converted = %+v", converted)
	}
	if converted[0].ChoiceIndex != 1 || !converted[0].VisibleOutput {
		t.Fatalf("converted[0] = %+v", converted[0])
	}
	if convertGeminiFrames(nil) != nil {
		t.Fatal("nil 入参 nil")
	}
}

// ---- sink 协议 ----

func TestW11BSinkWriteJSON(t *testing.T) {
	// kernelWriteJSON marshal 失败 → 500。
	recorder := httptest.NewRecorder()
	kernelWriteJSON(gatewaypreauth.NewTrackingWriter(recorder), 200, w11bUnmarshalable{})
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", recorder.Code)
	}
	// 成功路径补齐 content-type。
	recorder2 := httptest.NewRecorder()
	kernelWriteJSON(gatewaypreauth.NewTrackingWriter(recorder2), 200, map[string]string{"ok": "1"})
	if recorder2.Code != 200 || recorder2.Header().Get("Content-Type") == "" {
		t.Fatalf("code = %d ct = %q", recorder2.Code, recorder2.Header().Get("Content-Type"))
	}
}

// ---- pipehelpers signalAborted / errorDiagnosticMessage ----

func TestW11BPipeHelperEdges(t *testing.T) {
	if signalAborted(nil) {
		t.Fatal("nil signal false")
	}
	closed := make(chan struct{})
	close(closed)
	if !signalAborted(chanSignal{ch: closed}) {
		t.Fatal("关闭信号 true")
	}
	if signalAborted(chanSignal{ch: make(chan struct{})}) {
		t.Fatal("开放信号 false")
	}
	if errorDiagnosticMessage(nil) != "上游流式响应已中断" {
		t.Fatal("nil 错误缺省消息")
	}
	pipe, _ := w11bNewFinalPipe(t, nil)
	if pipe.signalAborted() {
		t.Fatal("staticSignal 未取消")
	}
	// interruptResponse 对已结束下游 no-op。
	pipe.downstream.End()
	pipe.interruptResponse()
	// signalAborted pipe + 已关闭 signal。
	closedPipe, _ := w11bNewFinalPipe(t, nil)
	closedPipe.input.Signal = chanSignal{ch: closed}
	if !closedPipe.signalAborted() {
		t.Fatal("关闭信号 pipe 视角 true")
	}
}
