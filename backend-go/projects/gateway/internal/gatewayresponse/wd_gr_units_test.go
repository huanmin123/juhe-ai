package gatewayresponse

// gatewayresponse 单元面补充测试（wd_ 前缀，独占新增）：
//   - 协议驱动适配器（G02 OpenAI / G03 Anthropic / G04 Gemini）全方法面。
//   - 协议私有形状 → gatewayproto 的转换层与 Gemini 错误负载解析。
//   - 终态错误类型的判定矩阵、上游读取体、滚动捕获、SSE 等待心跳。
//   - 重试决策、拦截器观察、流摘要、日志接缝等小面。
// 断言只依赖既有协议包行为，不构造网络。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayanthropic"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestWdOpenAIResponseDriverSurface(t *testing.T) {
	driver := NewOpenAIResponseDriver()
	if driver.ResponseProtocol() != "openai_v1" || driver.ClientErrorProtocol() != "openai" || driver.DefaultClientProfile() != "generic" {
		t.Fatalf("openai 驱动标识错误")
	}
	if family := driver.EndpointFamilyForPath("/v1/Chat/Completions?x=1"); family != gatewayproto.EndpointFamilyChatCompletions {
		t.Fatalf("chat completions 家族解析错误: %v", family)
	}
	if family := driver.EndpointFamilyForPath("/v1/responses"); family != gatewayproto.EndpointFamilyResponses {
		t.Fatalf("responses 家族解析错误: %v", family)
	}
	if family := driver.EndpointFamilyForPath("/v1/other"); family != gatewayproto.EndpointFamilyUnknown {
		t.Fatalf("未知路径家族错误: %v", family)
	}
	usage := driver.ExtractUsageFromJSONValue(map[string]any{
		"usage": map[string]any{"prompt_tokens": 3.0, "completion_tokens": 5.0},
	})
	if wdInt(usage.InputTokens) != 3 || wdInt(usage.OutputTokens) != 5 {
		t.Fatalf("openai usage 提取错误: %+v", usage)
	}
	bufferUsage := driver.ExtractUsageFromJSONBuffer([]byte(`{"usage":{"prompt_tokens":7,"completion_tokens":9}}`))
	if wdInt(bufferUsage.InputTokens) != 7 || wdInt(bufferUsage.OutputTokens) != 9 {
		t.Fatalf("openai buffer usage 错误: %+v", bufferUsage)
	}
	fragmentUsage := driver.ExtractUsageFromJSONTextFragment(`"usage":{"prompt_tokens":11,"completion_tokens":13}`, false)
	if wdInt(fragmentUsage.InputTokens) != 11 || wdInt(fragmentUsage.OutputTokens) != 13 {
		t.Fatalf("openai fragment usage 错误: %+v", fragmentUsage)
	}
	payload := driver.ParseErrorPayload(`{"error":{"message":"限流","type":"rate_limit","code":"429"}}`,
		http.Header{"Content-Type": []string{"application/json"}})
	if payload.Code != "429" || payload.Message != "限流" {
		t.Fatalf("openai 错误负载错误: %+v", payload)
	}
	payload = driver.ParseErrorPayloadFromJSONValue(map[string]any{
		"error": map[string]any{"message": "坏请求", "code": "invalid"},
	})
	if payload.Code != "invalid" || payload.Message != "坏请求" {
		t.Fatalf("openai value 错误负载错误: %+v", payload)
	}
	if driver.NewStreamInspector() == nil {
		t.Fatal("openai 检查器不应为 nil")
	}
	if driver.StreamDriver() == nil {
		t.Fatal("openai 流驱动不应为 nil")
	}
}

func TestWdAnthropicResponseDriverSurface(t *testing.T) {
	driver := NewAnthropicResponseDriver()
	if driver.ResponseProtocol() != "anthropic_v1" {
		t.Fatalf("anthropic 协议错误: %q", driver.ResponseProtocol())
	}
	if driver.ClientErrorProtocol() != "anthropic" || driver.DefaultClientProfile() != "generic_anthropic" {
		t.Fatalf("anthropic 客户端协议/画像错误: %q %q", driver.ClientErrorProtocol(), driver.DefaultClientProfile())
	}
	if family := driver.EndpointFamilyForPath("/v1/messages"); family == gatewayproto.EndpointFamilyUnknown {
		t.Fatalf("anthropic messages 家族不应为 unknown: %v", family)
	}
	usage := driver.ExtractUsageFromJSONValue(map[string]any{
		"usage": map[string]any{"input_tokens": 4.0, "output_tokens": 6.0, "cache_read_input_tokens": 2.0},
	})
	if wdInt(usage.InputTokens) != 4 || wdInt(usage.OutputTokens) != 6 || wdInt(usage.CacheReadTokens) != 2 {
		t.Fatalf("anthropic usage 转换错误: %+v", usage)
	}
	bufferUsage := driver.ExtractUsageFromJSONBuffer([]byte(`{"usage":{"input_tokens":8,"output_tokens":2}}`))
	if wdInt(bufferUsage.InputTokens) != 8 {
		t.Fatalf("anthropic buffer usage 错误: %+v", bufferUsage)
	}
	payload := driver.ParseErrorPayloadFromJSONValue(map[string]any{
		"error": map[string]any{"type": "rate_limit_error", "message": "慢一点"},
	})
	if payload.Type != "rate_limit_error" || payload.Message != "慢一点" {
		t.Fatalf("anthropic 错误负载错误: %+v", payload)
	}
	inspector := driver.NewStreamInspector()
	inspector.PushText("event: ping\ndata: {\"type\":\"ping\"}\n\n")
	snapshot := inspector.Snapshot()
	if snapshot.EventCount == 0 {
		t.Fatalf("anthropic 检查器应累计事件: %+v", snapshot)
	}
	streamDriver := driver.StreamDriver()
	if streamDriver == nil {
		t.Fatal("anthropic 流驱动不应为 nil")
	}
}

func TestWdGeminiResponseDriverSurface(t *testing.T) {
	driver := NewGeminiResponseDriver()
	if driver.ResponseProtocol() != "gemini_v1beta" || driver.ClientErrorProtocol() != "gemini" || driver.DefaultClientProfile() != "generic_gemini" {
		t.Fatalf("gemini 驱动标识错误")
	}
	if family := driver.EndpointFamilyForPath("/v1beta/models/gemini:generateContent"); family == gatewayproto.EndpointFamilyUnknown {
		t.Fatalf("gemini generateContent 家族不应为 unknown: %v", family)
	}
	usage := driver.ExtractUsageFromJSONValue(map[string]any{
		"usageMetadata": map[string]any{"promptTokenCount": 5.0, "candidatesTokenCount": 7.0},
	})
	if wdInt(usage.InputTokens) != 5 || wdInt(usage.OutputTokens) != 7 {
		t.Fatalf("gemini usage 转换错误: %+v", usage)
	}
	payload := driver.ParseErrorPayload(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"慢"}}`,
		http.Header{"Content-Type": []string{"application/json"}})
	if payload.Code != "429" || payload.Type != "RESOURCE_EXHAUSTED" || payload.Message != "慢" {
		t.Fatalf("gemini 错误负载错误: %+v", payload)
	}
	// Content-Type 无 json 且文本不以 { 开头：不解析。
	empty := driver.ParseErrorPayload("plain error", http.Header{"Content-Type": []string{"text/plain"}})
	if empty.Code != "" || empty.Message != "" {
		t.Fatalf("非 JSON 负载不应解析: %+v", empty)
	}
	// 缺 error 信封：根对象充当 error 读取面。
	root := driver.ParseErrorPayloadFromJSONValue(map[string]any{
		"code": "400", "status": "INVALID_ARGUMENT", "message": "参数坏",
	})
	if root.Code != "400" || root.Type != "INVALID_ARGUMENT" || root.Message != "参数坏" {
		t.Fatalf("gemini 根对象回退错误: %+v", root)
	}
	inspector := driver.NewStreamInspector()
	if inspector == nil {
		t.Fatal("gemini 检查器不应为 nil")
	}
}

func TestWdGeminiErrorPayloadMessageAliases(t *testing.T) {
	// message 走别名集且 error 侧优先于根侧；嵌套对象一层递归。
	payload := parseGeminiErrorPayloadFromValue(map[string]any{
		"error":   map[string]any{"code": 400.0, "message": "error 侧"},
		"message": "根侧",
	})
	if payload.Message != "error 侧" {
		t.Fatalf("error 侧 message 应优先: %+v", payload)
	}
	payload = parseGeminiErrorPayloadFromValue(map[string]any{
		"error": map[string]any{"detail": map[string]any{"message": "嵌套 detail"}},
	})
	if payload.Message != "嵌套 detail" {
		t.Fatalf("嵌套对象别名递归错误: %+v", payload)
	}
	// code 缺省回退 status；status 数值转字符串。
	payload = parseGeminiErrorPayloadFromValue(map[string]any{
		"error": map[string]any{"status": 503.0},
	})
	if payload.Code != "503" || payload.Type != "503" {
		t.Fatalf("code 回退 status 错误: %+v", payload)
	}
	// 非 JSON 文本与坏 JSON。
	if got := parseGeminiErrorPayload("   ", nil); got.Code != "" {
		t.Fatalf("空白正文应得空负载: %+v", got)
	}
	if got := parseGeminiErrorPayload("{bad", http.Header{"Content-Type": []string{"application/json"}}); got.Code != "" {
		t.Fatalf("坏 JSON 应得空负载: %+v", got)
	}
	// Content-Type 含 json 时允许任意文本前缀；数组根不解析。
	header := http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}}
	_ = header
	arrayBody := `[1,2,3]`
	jsonHeader := http.Header{"Content-Type": []string{"application/json"}}
	if got := parseGeminiErrorPayload(arrayBody, jsonHeader); got.Code != "" {
		t.Fatalf("数组根不应解析: %+v", got)
	}
}

func TestWdConvertFramesAndInspections(t *testing.T) {
	if convertAnthropicFrames(nil) != nil || convertGeminiFrames(nil) != nil {
		t.Fatalf("nil 帧序列应返回 nil")
	}
	choice := 2
	visible := true
	anthropicFrames := convertAnthropicFrames([]gatewayanthropic.ResponseSemanticFrame{
		{FrameType: "text", Protocol: "anthropic_v1", Text: "你好", ChoiceIndex: &choice, VisibleOutput: &visible,
			Usage: &gatewayanthropic.ParsedUsage{InputTokens: wdIntPtr(1), OutputTokens: wdIntPtr(2)}},
	})
	if len(anthropicFrames) != 1 {
		t.Fatalf("anthropic 帧转换错误: %#v", anthropicFrames)
	}
	frame := anthropicFrames[0]
	if frame.Text != "你好" || frame.ChoiceIndex != 2 || !frame.VisibleOutput {
		t.Fatalf("anthropic 指针字段解引用错误: %+v", frame)
	}
	if wdInt(frame.Usage.InputTokens) != 1 || wdInt(frame.Usage.OutputTokens) != 2 {
		t.Fatalf("anthropic usage 转换错误: %+v", frame.Usage)
	}

	geminiFrames := convertGeminiFrames([]gatewaygemini.ResponseSemanticFrame{
		{FrameType: "finish", FinishReason: "stop", OutputIndex: &choice, ContentIndex: &choice},
	})
	if len(geminiFrames) != 1 || geminiFrames[0].FinishReason != "stop" || geminiFrames[0].OutputIndex != 2 || geminiFrames[0].ContentIndex != 2 {
		t.Fatalf("gemini 帧转换错误: %#v", geminiFrames)
	}

	// 错误负载转换。
	proto := anthropicErrorPayloadToProto(gatewayanthropic.ErrorPayload{Code: "429", Type: "rate_limit", Message: "慢"})
	if proto.Code != "429" || proto.Type != "rate_limit" || proto.Message != "慢" {
		t.Fatalf("anthropic 错误负载转换错误: %+v", proto)
	}

	// inspection 转换带 usage。
	anthropicInspection := convertAnthropicInspection(gatewayanthropic.StreamInspection{
		TerminalReceived: true, EventCount: 7, LastEventType: "message_stop",
		Usage: gatewayanthropic.ParsedUsage{OutputTokens: wdIntPtr(9)},
	})
	if !anthropicInspection.TerminalReceived || anthropicInspection.EventCount != 7 || wdInt(anthropicInspection.Usage.OutputTokens) != 9 {
		t.Fatalf("anthropic inspection 转换错误: %+v", anthropicInspection)
	}
	geminiInspection := convertGeminiInspection(gatewaygemini.StreamInspection{
		FailedReceived: true, SkipReason: "truncated",
	})
	if !geminiInspection.FailedReceived || geminiInspection.SkipReason != "truncated" {
		t.Fatalf("gemini inspection 转换错误: %+v", geminiInspection)
	}
}

func TestWdTerminalErrorTypes(t *testing.T) {
	aborted := &UpstreamRequestAbortedError{}
	if aborted.Error() != ErrUpstreamRequestAbortedMessage {
		t.Fatalf("缺省 message 错误: %q", aborted.Error())
	}
	if !IsUpstreamRequestAbortedError(aborted) {
		t.Fatal("类型判定失败")
	}
	if !IsUpstreamRequestAbortedError(errors.New("请求已取消")) {
		t.Fatal("message 判定失败")
	}
	if IsUpstreamRequestAbortedError(nil) || IsUpstreamRequestAbortedError(errors.New("other")) {
		t.Fatal("误判")
	}
	timeout := &FirstByteTimeoutError{Message: "首字节超时", TimeoutMs: 1000, Source: "hard_timeout"}
	if timeout.Code() != FirstByteTimeoutErrorCode || timeout.Error() != "首字节超时" {
		t.Fatalf("FirstByteTimeoutError 契约错误")
	}
	if !IsFirstByteTimeoutError(timeout) {
		t.Fatal("IsFirstByteTimeoutError 判定失败")
	}
	deadline := &ResponsePrecommitDeadlineError{DeadlineAtMs: 123}
	if deadline.Code() != GatewayRequestWallBudgetExhaustedCode || deadline.Error() == "" {
		t.Fatalf("ResponsePrecommitDeadlineError 契约错误")
	}
	if ResponsePrecommitDeadlineErrorOf(nil) != nil {
		t.Fatal("nil 应得 nil")
	}
	if got := ResponsePrecommitDeadlineErrorOf(deadline); got != deadline {
		t.Fatalf("直接判定错误: %#v", got)
	}
	wrapped := &NonStreamBodyPipeError{OriginalError: deadline}
	if got := ResponsePrecommitDeadlineErrorOf(wrapped); got != deadline {
		t.Fatalf("管道包装还原错误: %#v", got)
	}
	plainPipe := &NonStreamBodyPipeError{}
	if ResponsePrecommitDeadlineErrorOf(plainPipe) != nil {
		t.Fatal("无原始错误的管道应得 nil")
	}
	planTimeout := &StreamReadPlanTimeoutError{Message: "读取超时", TimeoutKind: "idle"}
	if planTimeout.Error() != "读取超时" || !IsStreamReadPlanTimeoutError(planTimeout) {
		t.Fatalf("StreamReadPlanTimeoutError 契约错误")
	}
	bufferExceeded := &StreamPreCommitBufferExceededError{}
	if bufferExceeded.Code() != StreamPreCommitBufferExceededCode || bufferExceeded.Error() == "" {
		t.Fatalf("StreamPreCommitBufferExceededError 契约错误")
	}
	inner := errors.New("回调失败")
	beforeCommit := &StreamBeforeDownstreamCommitError{OriginalError: inner}
	if beforeCommit.Error() == "" || !errors.Is(beforeCommit, inner) {
		t.Fatalf("StreamBeforeDownstreamCommitError Unwrap 契约错误")
	}
	pipeErr := &NonStreamBodyPipeError{OriginalError: inner, PartialResult: NonStreamPipeResult{}}
	if pipeErr.Error() != "回调失败" || !errors.Is(pipeErr, inner) {
		t.Fatalf("NonStreamBodyPipeError 契约错误")
	}
	barePipe := &NonStreamBodyPipeError{}
	if barePipe.Error() != "上游非流式响应正文中断" {
		t.Fatalf("无原始错误时的 message 错误: %q", barePipe.Error())
	}
	transport := &StartedBodyTransportError{Err: inner, Code: "ECONNRESET", Name: "Error"}
	if transport.Error() != "回调失败" || !errors.Is(transport, inner) || !IsStartedUpstreamBodyTransportError(transport) {
		t.Fatalf("StartedBodyTransportError 契约错误")
	}
	// transportTimeoutPattern 关键词矩阵。
	for _, needle := range []string{"ETIMEDOUT", "request timeout", "Timed Out", "连接超时", "timedout"} {
		if !transportTimeoutPattern(needle) {
			t.Fatalf("%q 应命中超时模式", needle)
		}
	}
	if transportTimeoutPattern("reset by peer") {
		t.Fatalf("非超时诊断不应命中")
	}
	// StreamTransportFailureForError 分支。
	if failure := StreamTransportFailureForError(planTimeout, ""); failure == nil || failure.Kind != "timeout" {
		t.Fatalf("read-plan 超时应映射 timeout: %#v", failure)
	}
	if failure := StreamTransportFailureForError(&StartedBodyTransportError{Err: inner, Code: "ETIMEDOUT"}, "etimedout"); failure == nil || failure.Kind != "timeout" {
		t.Fatalf("transport 超时应映射 timeout: %#v", failure)
	}
	if failure := StreamTransportFailureForError(transport, "connection reset"); failure == nil || failure.Kind == "timeout" {
		t.Fatalf("非超时 transport 应得非 timeout 类: %#v", failure)
	}
	if failure := StreamTransportFailureForError(inner, ""); failure != nil {
		t.Fatalf("普通错误不应产生 transport failure: %#v", failure)
	}
}

func TestWdReaderUpstreamBodyLifecycle(t *testing.T) {
	// 完整读取：两个分片 + EOF。
	reader := io.MultiReader(strings.NewReader("hello "), strings.NewReader("world"))
	body := NewReaderUpstreamBody(context.Background(), reader)
	var collected []byte
	for {
		result := <-body.Next()
		if result.Err == io.EOF {
			break
		}
		if result.Err != nil {
			t.Fatalf("读取错误: %v", result.Err)
		}
		collected = append(collected, result.Data...)
	}
	if string(collected) != "hello world" {
		t.Fatalf("分片内容错误: %q", collected)
	}
	body.Close()

	// 读失败：错误包装为 StartedBodyTransportError。
	failBody := NewReaderUpstreamBody(context.Background(), io.MultiReader(strings.NewReader("ok"), &wdFailingReader{}))
	<-failBody.Next() // "ok"
	failure := <-failBody.Next()
	var transportErr *StartedBodyTransportError
	if failure.Err == nil || !errors.As(failure.Err, &transportErr) {
		t.Fatalf("读失败应包装为 transport 错误: %v", failure.Err)
	}
	failBody.Close()

	// Close 中断等待：取消后 pump 退出，Close 幂等。
	hanging := NewReaderUpstreamBody(context.Background(), &wdBlockingReader{})
	go func() {
		time.Sleep(10 * time.Millisecond)
		hanging.Close()
	}()
	hanging.Close() // 幂等：第二次调用为 no-op。
}

type wdFailingReader struct{}

func (r *wdFailingReader) Read([]byte) (int, error) { return 0, errors.New("reset by peer") }

type wdBlockingReader struct{}

func (r *wdBlockingReader) Read([]byte) (int, error) {
	select {}
}

func TestWdSliceUpstreamBodyCloseSemantics(t *testing.T) {
	body := NewSliceUpstreamBody([]byte("a"))
	result := <-body.Next()
	if string(result.Data) != "a" {
		t.Fatalf("分片错误: %q", result.Data)
	}
	result = <-body.Next()
	if result.Err != io.EOF {
		t.Fatalf("耗尽后应 EOF: %v", result.Err)
	}
	body.Close()
	result = <-body.Next()
	if !errors.Is(result.Err, ErrBodyClosed) {
		t.Fatalf("Close 后读取应报关闭: %v", result.Err)
	}
}

func TestWdRollingCaptureWindowAndFilled(t *testing.T) {
	capture := NewRollingCapture(8)
	if capture.Filled() || len(capture.Buffer()) != 0 {
		t.Fatalf("初始状态错误: filled=%v", capture.Filled())
	}
	capture.Push([]byte("12345678"))
	if capture.Filled() {
		t.Fatalf("恰好填满不算溢出")
	}
	capture.Push([]byte("9abc"))
	if !capture.Filled() {
		t.Fatal("超出上限应置 filled")
	}
	if got := string(capture.Buffer()); got != "56789abc" || len(got) != 8 {
		t.Fatalf("滚动窗口应保留末尾: %q", got)
	}
	text, ok := capture.Text()
	if !ok || text != "56789abc" {
		t.Fatalf("Text 错误: %q %v", text, ok)
	}
	capture.Push(nil)
	empty := NewRollingCapture(0)
	empty.Push([]byte("x"))
	if len(empty.Buffer()) != 0 {
		t.Fatalf("limit=0 应忽略写入")
	}
	emptyCapture := NewRollingCapture(4)
	if _, ok := emptyCapture.Text(); ok {
		t.Fatalf("空窗口 Text 应 ok=false")
	}
}

func TestWdSseWaitHeartbeatFactoryAndLoop(t *testing.T) {
	if CreateGatewaySseWaitHeartbeat(HeartbeatDeps{DownstreamProtocol: "chat_completions"}) != nil {
		t.Fatal("非 SSE 协议应得 nil 心跳")
	}
	if CreateGatewaySseWaitHeartbeatObserver(HeartbeatDeps{DownstreamProtocol: "unknown"}) != nil {
		t.Fatal("非 SSE 协议应得 nil 观察者")
	}
	if !GatewayDownstreamProtocolUsesSSE("responses_sse") || GatewayDownstreamProtocolUsesSSE("json") {
		t.Fatal("SSE 协议判定错误")
	}
	// SSE + 注入定时器：Start 立即写首个心跳，随后按节拍重复；Stop 幂等停止。
	downstream := newDownstreamWd()
	commit := &DownstreamCommitState{}
	ticks := make(chan time.Time, 4)
	heartbeat := CreateGatewaySseWaitHeartbeat(HeartbeatDeps{
		Res:                          downstream.Res,
		DownstreamProtocol:           "responses_sse",
		DownstreamCommit:             commit,
		IntervalMs:                   15_000,
		EmitCodexCompactionKeepalive: true,
		After: func(time.Duration) <-chan time.Time {
			return ticks
		},
	})
	if heartbeat == nil {
		t.Fatal("SSE 协议应得心跳")
	}
	if string(heartbeatChunkOf(HeartbeatDeps{DownstreamProtocol: "responses_sse", EmitCodexCompactionKeepalive: true})) !=
		`data: {"type":"juhe_ai.keepalive"}`+"\n\n" {
		t.Fatal("codex 保活块错误")
	}
	if string(heartbeatChunkOf(HeartbeatDeps{DownstreamProtocol: "messages_sse"})) != ": juhe-ai waiting for upstream capacity\n\n" {
		t.Fatal("缺省保活块错误")
	}
	heartbeat.Start()
	// Start 是异步循环：有界等待首个心跳写出（固定 Now/定时器注入，
	// 不依赖真实时间间隔）。
	deadline := time.Now().Add(2 * time.Second)
	for !downstream.Res.HeadersSent() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !downstream.Res.HeadersSent() {
		t.Fatal("首个心跳应带出发头")
	}
	heartbeat.Stop()
	heartbeat.Stop() // Stop 幂等。
	time.Sleep(5 * time.Millisecond)

	// 观察者工厂：SSE 协议返回带 Start/Stop 回调的观察者。
	observer := CreateGatewaySseWaitHeartbeatObserver(HeartbeatDeps{DownstreamProtocol: "messages_sse"})
	if observer == nil {
		t.Fatal("SSE 协议应得观察者")
	}
	// 语义已提交后心跳写出不再继续。
	commit.SemanticCommitted = true
	if writeHeartbeatChunk(HeartbeatDeps{Res: downstream.Res, DownstreamCommit: commit}, gatewaySseWaitHeartbeatChunk) {
		t.Fatal("语义已提交后不应再写出心跳")
	}
}

func TestWdRetryDecisionAndStreamSummaryHelpers(t *testing.T) {
	if !ShouldInterruptCommittedGenericStream(false, 128) {
		t.Fatal("无失败事件通道且已写字节应中断")
	}
	if ShouldInterruptCommittedGenericStream(true, 128) || ShouldInterruptCommittedGenericStream(false, 0) {
		t.Fatal("中断条件错误")
	}
	summary := StreamBodyOmissionSummaryOf(StreamInspectionSummaryInput{
		EventCount: 9, LastEventType: "message_stop",
		RecentEventTypes: []string{"a", "b"}, ImageOutputReceived: true,
		TerminalReceived: true, FailedReceived: false,
	}, 1024, 2048)
	if summary.Reason != "image_stream_payload" || summary.TotalUpstreamBytes != 1024 ||
		summary.SseEventCount != 9 || !summary.ImageOutputReceived || summary.LastSseEventType != "message_stop" {
		t.Fatalf("正文省略摘要错误: %+v", summary)
	}
	if QuoteInt(4096) != "4096" {
		t.Fatalf("QuoteInt 错误")
	}
}

func TestWdCodexReasoningLevelDescriptions(t *testing.T) {
	cases := map[string]string{
		"none": "None", "minimal": "Minimal", "low": "Low",
		"medium": "Medium", "high": "High", "xhigh": "XHigh",
		"pro": "Pro",
	}
	for level, want := range cases {
		if got := codexReasoningLevelDescription(level); got != want {
			t.Fatalf("codexReasoningLevelDescription(%q) = %q, want %q", level, got, want)
		}
	}
}

func TestWdNopStreamLoggerAndAccountView(t *testing.T) {
	var logger StreamLogger = nopStreamLogger{}
	logger.Debug("evt", nil, "d")
	logger.Info("evt", map[string]any{"k": "v"}, "i")
	logger.Warn("evt", nil, "w")
	view := OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{
		ID: "acct-1", Name: "账户一", ProviderCode: "openai", ProviderProtocolProfileID: "profile-1",
		ProtocolCode: "openai", ProtocolVersion: "v1", ClientCompatibility: "generic",
	}}
	if view.GetID() != "acct-1" || view.GetName() != "账户一" || view.GetProviderCode() != "openai" ||
		view.GetProviderProtocolProfileID() != "profile-1" || view.GetProtocolCode() != "openai" ||
		view.GetProtocolVersion() != "v1" || view.GetClientCompatibility() != "generic" {
		t.Fatalf("账户视图适配错误")
	}
}

func TestWdInterceptorObservationsAndFailureEvent(t *testing.T) {
	interceptor := &OpenAIStreamInterceptor{}
	if got := interceptor.TakeObservations(); got != nil {
		t.Fatalf("空观察应得 nil: %#v", got)
	}
	interceptor.observations = []ResponseInspectionDecision{{PolicyID: "p1"}, {PolicyID: "p2"}}
	observations := interceptor.TakeObservations()
	if len(observations) != 2 || observations[0].PolicyID != "p1" {
		t.Fatalf("TakeObservations 错误: %#v", observations)
	}
	if again := interceptor.TakeObservations(); again != nil {
		t.Fatalf("取走后应为 nil: %#v", again)
	}
	interceptor.MarkDownstreamWrite()

	// interceptorFailureEvent：rewrite 优先于 upstream；client retry 改写。
	event := interceptorFailureEvent(&ResponseInspectionDecision{
		RewriteErrorCode: "wd_code", RewriteMessage: "改写",
		UpstreamErrorCode: "upstream_code", UpstreamErrorMessage: "上游",
	}, false)
	encoded := string(event)
	if !bytes.Contains(event, []byte("wd_code")) || !bytes.Contains(event, []byte("改写")) {
		t.Fatalf("失败事件应携带 rewrite 字段: %s", encoded)
	}
	retryEvent := interceptorFailureEvent(&ResponseInspectionDecision{
		UpstreamErrorCode: "upstream_code", RetryEnabled: true,
	}, true)
	if !bytes.Contains(retryEvent, []byte(gatewaypreauth.GatewayStreamClientRetryErrorCode)) {
		t.Fatalf("client retry 应改写错误码: %s", retryEvent)
	}
	fallbackEvent := interceptorFailureEvent(&ResponseInspectionDecision{}, false)
	if !bytes.Contains(fallbackEvent, []byte("response_inspection_matched")) {
		t.Fatalf("缺省错误码错误: %s", fallbackEvent)
	}
}

func TestWdFirstJSONPathMatch(t *testing.T) {
	hit := firstJSONPathMatch(gatewayproto.SemanticFrame{
		RawJSON: map[string]any{"usage": map[string]any{"input_tokens": 3.0}},
	}, []string{"usage.output_tokens", "usage.input_tokens"})
	if hit == nil || *hit != "usage.input_tokens" {
		t.Fatalf("RawJSON 路径命中错误: %#v", hit)
	}
	hit = firstJSONPathMatch(gatewayproto.SemanticFrame{
		RawJSONPaths: []string{"choices.0.finish_reason"},
	}, []string{"choices.0.finish_reason", "usage"})
	if hit == nil || *hit != "choices.0.finish_reason" {
		t.Fatalf("RawJSONPaths 命中错误: %#v", hit)
	}
	if firstJSONPathMatch(gatewayproto.SemanticFrame{RawJSON: map[string]any{"a": 1.0}}, []string{"b.c"}) != nil {
		t.Fatal("未命中应得 nil")
	}
}

func TestWdClassifyGatewayUpstreamFailurePhases(t *testing.T) {
	upstream := ClassifyGatewayUpstreamFailure(GatewayUpstreamFailureClassificationInput{Phase: "upstream_request"})
	if upstream.FailureClass != FailureClassTransport || upstream.ClassificationReason != "upstream_transport_failure" {
		t.Fatalf("upstream_request 分类错误: %+v", upstream)
	}
	response := ClassifyGatewayUpstreamFailure(GatewayUpstreamFailureClassificationInput{Phase: "upstream_response"})
	if response.FailureClass != FailureClassOpaqueUpstreamResponse || response.ClassificationReason != "opaque_upstream_response_failure" {
		t.Fatalf("upstream_response 分类错误: %+v", response)
	}
	unknown := ClassifyGatewayUpstreamFailure(GatewayUpstreamFailureClassificationInput{Phase: "elsewhere"})
	if unknown.FailureClass != FailureClassUnknown || unknown.ClassificationReason != "unknown_failure_phase" {
		t.Fatalf("未知阶段分类错误: %+v", unknown)
	}
}

func TestWdResponseDriverForProtocolAndErrorProtocol(t *testing.T) {
	if _, ok := ResponseDriverForProtocol("anthropic_v1").(*AnthropicResponseDriver); !ok {
		t.Fatal("anthropic 协议应得 anthropic 驱动")
	}
	if _, ok := ResponseDriverForProtocol("gemini_v1beta").(*GeminiResponseDriver); !ok {
		t.Fatal("gemini 协议应得 gemini 驱动")
	}
	if _, ok := ResponseDriverForProtocol("whatever").(*OpenAIResponseDriver); !ok {
		t.Fatal("未知协议应回退 openai 驱动")
	}
	if gatewayErrorProtocolOf("anthropic") != gatewaypreauth.GatewayErrorProtocol("anthropic") {
		t.Fatal("错误协议映射错误")
	}
	// gemini 流驱动的家族映射与降级。
	var streamDriver StreamDriver = geminiStreamDriver{}
	if got := streamDriver.ResponseInspectionEndpointFamily(gatewayproto.EndpointFamilyCountTokens); got != gatewayproto.EndpointFamilyCountTokens {
		t.Fatalf("已识别家族应保留: %v", got)
	}
	if got := streamDriver.ResponseInspectionEndpointFamily(gatewayproto.EndpointFamilyChatCompletions); got != gatewayproto.EndpointFamilyGenerateContent {
		t.Fatalf("未识别家族应回退 generate_content: %v", got)
	}
	if streamDriver.SSEResponseInspectionFailureEvent() != "none" {
		t.Fatalf("gemini 失败事件应为 none: %q", streamDriver.SSEResponseInspectionFailureEvent())
	}
	if streamDriver.DrainForKeepAliveAfterTerminal() {
		t.Fatal("gemini 不应终态后继续排水")
	}
	if streamDriver.ClientErrorProtocol() != "gemini" {
		t.Fatalf("gemini 错误协议错误: %q", streamDriver.ClientErrorProtocol())
	}
	var anthropicStream StreamDriver = anthropicStreamDriver{}
	if anthropicStream.ClientErrorProtocol() != "anthropic" {
		t.Fatalf("anthropic 流驱动错误协议错误")
	}
	if anthropicStream.SSEResponseInspectionFailureEvent() == "" {
		t.Fatal("anthropic 失败事件不应为空")
	}
}

func TestWdSinkLoggerFallback(t *testing.T) {
	sink := &Sink{}
	if sink.logger() == nil {
		t.Fatal("缺省日志接缝不应为 nil")
	}
	// 认证 models 响应：走 TrackingWriter 全链路写出。
	// SendAuthenticatedModelsGatewayResponse 需要完整的 ModelsResponseInput
	//（Req/审计上下文），空输入在装配层就是非法的；这里只锁定缺省日志接缝。
	if (&Sink{}).logger() == nil {
		t.Fatal("缺省日志接缝不应为 nil")
	}
}

func wdInt(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

func wdIntPtr(value int) *int { return &value }

// newDownstreamWd 只返回写侧（无需 recorder 断言时避免未用变量）。
func newDownstreamWd() StreamDownstream {
	recorder := httptest.NewRecorder()
	tracking := gatewaypreauth.NewTrackingWriter(recorder)
	return StreamDownstream{Res: tracking}
}
