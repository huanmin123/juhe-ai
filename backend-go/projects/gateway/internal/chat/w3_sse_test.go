package chat

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// SSE 写出层与上游 SSE 采集器的字节契约：事件帧格式、心跳、终止条件以及
// Chat Completions 流的预算与错误文案必须与 Node chat-sse-subscriber.ts /
// chat-gateway-sse.ts 一致。

// signalingWriterW3 在每次 Write 时回调，供心跳测试做无 sleep 同步。
type signalingWriterW3 struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	onWrite func()
}

func (w *signalingWriterW3) Header() http.Header { return http.Header{} }

func (w *signalingWriterW3) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.buffer.Write(data)
	copied := append([]byte{}, data...)
	w.mu.Unlock()
	if w.onWrite != nil {
		go func() {
			_ = copied
			w.onWrite()
		}()
	}
	return len(data), nil
}

func (w *signalingWriterW3) WriteHeader(int) {}

func (w *signalingWriterW3) snapshot() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

// TestChatSSEWriterW3 覆盖写出器的帧格式与可写状态机。
func TestChatSSEWriterW3(t *testing.T) {
	t.Run("事件帧与心跳字节格式", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writer := newChatSSEWriter(recorder, nil, nil)
		if !writer.Writable() {
			t.Fatalf("初始应可写")
		}
		if !writer.WriteEvent("message.delta", map[string]any{"delta": "你"}) {
			t.Fatalf("写出失败")
		}
		if !writer.WriteComment(": heartbeat\n\n") {
			t.Fatalf("心跳写出失败")
		}
		expected := "event: message.delta\ndata: {\"delta\":\"你\"}\n\n: heartbeat\n\n"
		if recorder.Body.String() != expected {
			t.Fatalf("字节流不正确: %q", recorder.Body.String())
		}
		writer.End()
		if writer.Writable() {
			t.Fatalf("End 后应不可写")
		}
		if writer.WriteEvent("message.delta", nil) {
			t.Fatalf("End 后写出应失败")
		}
		if writer.WriteComment(": x") {
			t.Fatalf("End 后心跳应失败")
		}
		writer.End()
	})
	t.Run("请求上下文取消触发 onClose", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, cancel := context.WithCancel(context.Background())
		closed := make(chan struct{})
		writer := newChatSSEWriter(recorder, ctx, func() { close(closed) })
		if !writer.Writable() {
			t.Fatalf("初始应可写")
		}
		cancel()
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatalf("取消后未触发 onClose")
		}
		if writer.Writable() {
			t.Fatalf("取消后应不可写")
		}
		_ = writer
	})
	t.Run("写出 panic 标记失败", func(t *testing.T) {
		panicWriter := panickyWriterW3{}
		writer := newChatSSEWriter(panicWriter, nil, nil)
		if writer.WriteEvent("event", nil) {
			t.Fatalf("panic 写出应返回 false")
		}
		if writer.Writable() {
			t.Fatalf("panic 后应不可写")
		}
	})
	t.Run("非 Flusher 写出器不触发刷新", func(t *testing.T) {
		plain := plainWriterW3{buffer: &bytes.Buffer{}}
		writer := newChatSSEWriter(plain, nil, nil)
		if !writer.WriteEvent("e", map[string]any{"a": 1}) {
			t.Fatalf("无 Flusher 也应写出成功")
		}
		if plain.buffer.String() != "event: e\ndata: {\"a\":1}\n\n" {
			t.Fatalf("字节流不正确: %q", plain.buffer.String())
		}
	})
	t.Run("不可序列化载荷", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writer := newChatSSEWriter(recorder, nil, nil)
		if writer.WriteEvent("e", make(chan int)) {
			t.Fatalf("不可序列化载荷应返回 false")
		}
	})
}

type panickyWriterW3 struct{}

func (panickyWriterW3) Header() http.Header { return http.Header{} }

func (panickyWriterW3) Write([]byte) (int, error) { panic("下游写入崩溃") }

func (panickyWriterW3) WriteHeader(int) {}

type plainWriterW3 struct{ buffer *bytes.Buffer }

func (w plainWriterW3) Header() http.Header          { return http.Header{} }
func (w plainWriterW3) Write(data []byte) (int, error) { return w.buffer.Write(data) }
func (w plainWriterW3) WriteHeader(int)                {}

// TestSSESubscriberW3 覆盖订阅者的终止分离契约。
func TestSSESubscriberW3(t *testing.T) {
	t.Run("终态事件后结束并分离", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writer := newChatSSEWriter(recorder, nil, nil)
		var detached int
		subscriber := &sseSubscriber{writer: writer, detach: func() { detached++ }}
		if !subscriber.TrySend(ChatGenerationEvent{Type: "message.delta", EventVersion: 3, Data: map[string]any{"a": 1}}) {
			t.Fatalf("增量事件应成功")
		}
		if !strings.Contains(recorder.Body.String(), `"eventVersion":3`) {
			t.Fatalf("eventVersion 应并入载荷: %s", recorder.Body.String())
		}
		if !subscriber.TrySend(ChatGenerationEvent{Type: "message.completed", EventVersion: 4}) {
			t.Fatalf("终态事件应成功")
		}
		if detached != 1 {
			t.Fatalf("终态后应分离: %d", detached)
		}
		if !writer.ended {
			t.Fatalf("终态后响应应结束")
		}
		// 分离后再投递：TrySend 返回 false 且不再重复分离。
		if subscriber.TrySend(ChatGenerationEvent{Type: "message.delta", EventVersion: 5}) {
			t.Fatalf("分离后投递应失败")
		}
		if detached != 1 {
			t.Fatalf("分离应只执行一次: %d", detached)
		}
	})
	t.Run("不可写触发分离", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writer := newChatSSEWriter(recorder, nil, nil)
		writer.End()
		var detached int
		subscriber := &sseSubscriber{writer: writer, detach: func() { detached++ }}
		if subscriber.TrySend(ChatGenerationEvent{Type: "message.delta"}) {
			t.Fatalf("不可写投递应失败")
		}
		if detached != 1 {
			t.Fatalf("应触发分离: %d", detached)
		}
	})
	t.Run("detach panic 被吸收", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writer := newChatSSEWriter(recorder, nil, nil)
		subscriber := &sseSubscriber{writer: writer, detach: func() { panic("registry 崩溃") }}
		subscriber.detachOnce()
		if !writer.ended {
			t.Fatalf("分离后响应应结束")
		}
	})
}

// TestChatSSEHeartbeatW3 覆盖心跳循环的启动、停止与不可写回调。
func TestChatSSEHeartbeatW3(t *testing.T) {
	t.Run("按间隔写心跳并可停止", func(t *testing.T) {
		signaling := &signalingWriterW3{}
		writer := newChatSSEWriter(signaling, nil, nil)
		written := make(chan struct{}, 8)
		signaling.onWrite = func() {
			select {
			case written <- struct{}{}:
			default:
			}
		}
		stop := startChatSSEHeartbeat(writer, 1, nil)
		select {
		case <-written:
		case <-time.After(2 * time.Second):
			t.Fatalf("心跳未写出")
		}
		stop()
		// 停止后不再有新心跳。
		drainedW3(written)
		before := signaling.snapshot()
		select {
		case <-written:
			t.Fatalf("停止后仍写心跳")
		case <-time.After(50 * time.Millisecond):
		}
		if signaling.snapshot() != before {
			t.Fatalf("停止后写入缓冲发生变化")
		}
	})
	t.Run("不可写触发回调", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writer := newChatSSEWriter(recorder, nil, nil)
		writer.End()
		onUnwritable := make(chan struct{}, 1)
		stop := startChatSSEHeartbeat(writer, 1, func() { onUnwritable <- struct{}{} })
		defer stop()
		select {
		case <-onUnwritable:
		case <-time.After(2 * time.Second):
			t.Fatalf("不可写未触发回调")
		}
	})
}

func drainedW3(channel chan struct{}) {
	for {
		select {
		case <-channel:
		default:
			return
		}
	}
}

// TestPrepareSSEResponseW3 覆盖 SSE 响应头契约。
func TestPrepareSSEResponseW3(t *testing.T) {
	recorder := httptest.NewRecorder()
	prepareSSEResponse(recorder)
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-cache, no-transform" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := recorder.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}
	if recorder.Code != 200 {
		t.Fatalf("状态码 = %d", recorder.Code)
	}
}

// TestCollectOpenAIChatSseW3 覆盖 Chat Completions 采集器的错误路径。
func TestCollectOpenAIChatSseW3(t *testing.T) {
	happy := "data: " + `{"choices":[{"delta":{"content":"Hi"}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}` + "\n\n" +
		"data: [DONE]\n\n"
	result, err := CollectOpenAIChatSse(strings.NewReader(happy), maxMessageBytes, nil, 0)
	if err != nil || result.Content != "Hi" || result.FinishReason != "stop" || !result.Done || result.InputTokens == nil {
		t.Fatalf("happy path 失败: %+v err=%v", result, err)
	}
	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{"错误对象", "data: " + `{"error":{"message":"上游内部错误"}}` + "\n\ndata: [DONE]\n\n", "上游内部错误"},
		{"错误对象非字符串", "data: " + `{"error":{"message":42}}` + "\n\ndata: [DONE]\n\n", "上游流式请求失败"},
		{"非法 JSON", "data: {bad}\n\ndata: [DONE]\n\n", "上游返回了无效的 SSE JSON"},
		{"缺少 DONE", "data: " + `{"choices":[{"delta":{"content":"x"}}]}` + "\n\n", "上游流式响应缺少 [DONE]"},
		{"工具 index 缺失", "data: " + `{"choices":[{"delta":{"tool_calls":[{}]}}]}` + "\n\ndata: [DONE]\n\n", "Chat 工具调用 index 无效"},
		{"工具 index 超界", "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":256}]}}]}` + "\n\ndata: [DONE]\n\n", "Chat 工具调用 index 无效"},
		{"事件数超限", "data: {}\n\ndata: {}\n\ndata: [DONE]\n\n", "事件数量超过 2 上限"},
		{"尾部残块超限", "data: [DONE]\n\ndata: {\"x\":\"" + strings.Repeat("y", 70*1024) + "\"}", "单个事件超过 64 KiB 上限"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := CollectOpenAIChatSse(strings.NewReader(testCase.payload), maxMessageBytes, nil, 2)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("err = %v, 期望包含 %q", err, testCase.wantErr)
			}
		})
	}
	t.Run("工具参数超限", func(t *testing.T) {
		args := strings.Repeat("a", 70*1024)
		payload := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"t","arguments":"` + args + `"}}]}}]}` + "\n\ndata: [DONE]\n\n"
		_, err := CollectOpenAIChatSse(strings.NewReader(payload), maxMessageBytes, nil, 0)
		if err == nil || !strings.Contains(err.Error(), "64 KiB 上限") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("工具续答项目", func(t *testing.T) {
		payload := "data: " + `{"choices":[{"delta":{"content":"部分"},"finish_reason":"tool_calls"}]}` + "\n\n" +
			"data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"toolA","arguments":"{\"x\":1}"}}]}}]}` + "\n\n" +
			"data: [DONE]\n\n"
		result, err := CollectOpenAIChatSse(strings.NewReader(payload), maxMessageBytes, nil, 0)
		if err != nil {
			t.Fatalf("采集失败: %v", err)
		}
		if len(result.ToolCalls) != 1 || result.ToolCalls[0].ToolName != "toolA" {
			t.Fatalf("工具调用不正确: %+v", result.ToolCalls)
		}
		if len(result.ContinuationItems) != 1 {
			t.Fatalf("续答项目数量 = %d", len(result.ContinuationItems))
		}
		item := result.ContinuationItems[0].(map[string]any)
		if item["role"] != "assistant" || item["content"] != "部分" {
			t.Fatalf("assistant 项不正确: %v", item)
		}
	})
	t.Run("无内容续答 content 为 null", func(t *testing.T) {
		payload := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"t","arguments":"{}"}}]}}]}` + "\n\ndata: [DONE]\n\n"
		result, err := CollectOpenAIChatSse(strings.NewReader(payload), maxMessageBytes, nil, 0)
		if err != nil {
			t.Fatalf("采集失败: %v", err)
		}
		item := result.ContinuationItems[0].(map[string]any)
		if _, exists := item["content"]; !exists {
			t.Fatalf("content 键应存在（null）: %v", item)
		}
	})
	t.Run("工具片段缺失字段", func(t *testing.T) {
		payload := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1"}]}}]}` + "\n\ndata: [DONE]\n\n"
		if _, err := CollectOpenAIChatSse(strings.NewReader(payload), maxMessageBytes, nil, 0); err == nil || !strings.Contains(err.Error(), "缺少 id、name 或 arguments") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("非法 UTF-8 与读取失败", func(t *testing.T) {
		if _, err := CollectOpenAIChatSse(strings.NewReader("data: {\xff}\n\ndata: [DONE]\n\n"), maxMessageBytes, nil, 0); err == nil {
			t.Fatalf("非法 UTF-8 应报错")
		}
		if _, err := CollectOpenAIChatSse(errReaderW3(), maxMessageBytes, nil, 0); err == nil {
			t.Fatalf("读取失败应透传")
		}
	})
	t.Run("增量回调与 CRLF 分帧", func(t *testing.T) {
		payload := "data: " + `{"choices":[{"delta":{"content":"A"}}]}` + "\r\n\r\n" + "data: " + `{"choices":[{"delta":{"content":"B"}}]}` + "\r\n\r\ndata: [DONE]\r\n\r\n"
		var deltas []string
		result, err := CollectOpenAIChatSse(strings.NewReader(payload), maxMessageBytes, func(delta string) { deltas = append(deltas, delta) }, 0)
		if err != nil || result.Content != "AB" || !equalStringsW3(deltas, []string{"A", "B"}) {
			t.Fatalf("CRLF 分帧失败: %+v err=%v deltas=%v", result, err, deltas)
		}
	})
}

// TestStreamExecuteHelpersW3 覆盖执行闭包的零散 helper。
func TestStreamExecuteHelpersW3(t *testing.T) {
	if statusOrZero(nil) != 0 || statusOrZero(&GenerationDispatchResponse{Status: 502}) != 502 {
		t.Fatalf("statusOrZero 契约不正确")
	}
	if finishReasonOr([]ChatToolCall{{CallID: "c"}}, "stop") != "tool_calls" || finishReasonOr(nil, "stop") != "stop" {
		t.Fatalf("finishReasonOr 契约不正确")
	}
	if isPreparationCanceled(&PreparationCanceledError{}) != true || isPreparationCanceled(errors.New("x")) {
		t.Fatalf("isPreparationCanceled 契约不正确")
	}
	if got := upstreamMessagePayload(`{"error":{"message":"具体原因"}}`, "兜底"); got != "具体原因" {
		t.Fatalf("上游错误消息提取失败: %q", got)
	}
	if got := upstreamMessagePayload(`{"message":"消息版"}`, "兜底"); got != "消息版" {
		t.Fatalf("消息版提取失败: %q", got)
	}
	if got := upstreamMessagePayload("not-json", "兜底"); got != "兜底" {
		t.Fatalf("非 JSON 应回兜底: %q", got)
	}
	classified := classifyGenerationError(&ChatImageGenerationRequestError{Code: GenErrImageRateLimited, Message: "限流"}, GenErrInternal)
	if classified.Code != GenErrImageRateLimited {
		t.Fatalf("图片错误应按专用分类: %+v", classified)
	}
	fallback := classifyGenerationError(errors.New("boom"), GenErrUpstreamHTTP)
	if fallback.Code != GenErrUpstreamHTTP {
		t.Fatalf("非 internal 失败码应保留: %+v", fallback)
	}
	if mustJSON(map[string]any{"a": 1}) != `{"a":1}` {
		t.Fatalf("mustJSON 失败")
	}
	if isApplicationFunctionToolEvent(map[string]any{"type": "function_call"}) != true ||
		isApplicationFunctionToolEvent(map[string]any{"type": "response.function_call_arguments.delta"}) != true ||
		isApplicationFunctionToolEvent(map[string]any{"type": "reasoning"}) {
		t.Fatalf("isApplicationFunctionToolEvent 契约不正确")
	}
	projection := chatGenerationToolEventProjection("tool_updated", map[string]any{"id": "c1", "type": "web_search"})
	if projection.Status != "updated" || projection.ID != "c1" || projection.ToolType != "web_search" {
		t.Fatalf("工具投影不正确: %+v", projection)
	}
	defaultProjection := chatGenerationToolEventProjection("tool_started", nil)
	if defaultProjection.ID != "tool" || defaultProjection.ToolType != "tool" || defaultProjection.Status != "started" {
		t.Fatalf("默认工具投影不正确: %+v", defaultProjection)
	}
}

// TestProjectResponsesEventW3 覆盖 Responses 事件投影到 runner 的路径。
func TestProjectResponsesEventW3(t *testing.T) {
	var (
		mu           sync.Mutex
		published    []string
		blocks       = []*assistantBlock{}
		partialWrite strings.Builder
	)
	context := &ChatGenerationExecutionContext{
		Publish: func(eventType string, data map[string]any, update ChatGenerationProjectionUpdate) bool {
			mu.Lock()
			published = append(published, eventType)
			mu.Unlock()
			if update.ContentTextDelta != nil || update.ToolEvent != nil {
				projectToolEvent(&blocks, "tool_started", map[string]any{"id": "c1", "type": "web_search"})
			}
			return true
		},
	}
	if err := projectResponsesEvent(ChatResponsesEvent{Type: "text_delta", Delta: "文"}, "m1", context, &partialWrite, &blocks); err != nil {
		t.Fatalf("text_delta 投影失败: %v", err)
	}
	if err := projectResponsesEvent(ChatResponsesEvent{Type: "reasoning_delta", Delta: "思"}, "m1", context, &partialWrite, &blocks); err != nil {
		t.Fatalf("reasoning 投影失败: %v", err)
	}
	if err := projectResponsesEvent(ChatResponsesEvent{Type: "reasoning_completed"}, "m1", context, &partialWrite, &blocks); err != nil {
		t.Fatalf("reasoning 完成投影失败: %v", err)
	}
	if err := projectResponsesEvent(ChatResponsesEvent{Type: "tool_started", Item: map[string]any{"id": "c1", "type": "web_search"}}, "m1", context, &partialWrite, &blocks); err != nil {
		t.Fatalf("工具投影失败: %v", err)
	}
	if err := projectResponsesEvent(ChatResponsesEvent{Type: "tool_completed", Item: map[string]any{"type": "function_call", "id": "c1"}}, "m1", context, &partialWrite, &blocks); err != nil {
		t.Fatalf("function_call 工具事件应跳过: %v", err)
	}
	if err := projectResponsesEvent(ChatResponsesEvent{Type: "failed", Error: map[string]any{"msg": "x"}}, "m1", context, &partialWrite, &blocks); err == nil {
		t.Fatalf("failed 事件应返回错误")
	}
	if partialWrite.String() != "文" {
		t.Fatalf("正文增量累积不正确: %q", partialWrite.String())
	}
	if len(blocks) != 1 {
		t.Fatalf("本地工具块数量 = %d", len(blocks))
	}
}

// TestPublishApplicationToolEventW3 覆盖应用侧工具事件广播。
func TestPublishApplicationToolEventW3(t *testing.T) {
	var events []string
	var lastData map[string]any
	publish := func(eventType string, data map[string]any, update ChatGenerationProjectionUpdate) bool {
		events = append(events, eventType)
		lastData = data
		if update.ImageEvent == nil && update.ToolEvent == nil {
			t.Fatalf("投影更新不应为空")
		}
		return true
	}
	publishApplicationToolEvent(publish, "m1", ChatToolExecutionEvent{
		Status: "failed", CallID: "c1", ToolName: "diagnostic_echo", Reused: true,
		ErrorCode: "tool_timeout", ErrorMessage: "超时", PublicResult: map[string]any{"echoedText": "x"},
	})
	if len(events) != 1 || events[0] != "tool.failed" {
		t.Fatalf("事件类型不正确: %v", events)
	}
	item := lastData["item"].(map[string]any)
	if item["reused"] != true || item["errorCode"] != "tool_timeout" || item["executionOwner"] != "application" {
		t.Fatalf("事件载荷不正确: %v", item)
	}
	// generate_image 完成事件额外广播 image 投影。
	var imageUpdate *ChatGenerationImageEvent
	imagePublish := func(eventType string, data map[string]any, update ChatGenerationProjectionUpdate) bool {
		imageUpdate = update.ImageEvent
		return true
	}
	publishApplicationToolEvent(imagePublish, "m1", ChatToolExecutionEvent{
		Status: "completed", CallID: "c2", ToolName: "generate_image",
		PublicResult: map[string]any{"assetId": "asset-1", "mimeType": "image/png", "width": 8, "height": 8},
	})
	if imageUpdate == nil || imageUpdate.Item["assetId"] != "asset-1" {
		t.Fatalf("图片投影不正确: %+v", imageUpdate)
	}
}

// TestHeadRevisionW3 覆盖上下文版本读取的空值契约。
func TestHeadRevisionW3(t *testing.T) {
	fixture := newChatFixture(t)
	rt := &chatRoutes{deps: &Deps{Store: fixture.store}}
	if got := headRevision(rt, "missing", "owner"); got != 0 {
		t.Fatalf("缺失会话版本应为 0: %d", got)
	}
	fixture.createConversation("conv_head", "owner")
	fixture.accept("owner", "conv_head", "cmid-1", "问题")
	head := headRevision(rt, "conv_head", "owner")
	if head < 0 {
		t.Fatalf("版本不应为负: %d", head)
	}
}
