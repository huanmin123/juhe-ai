package playground

// POST /chat/stream 测试（真实 Key 化修订）：Mock 派发执行器回放固定上游 SSE
// 字节流，断言归一事件序列（只保留 delta/usage/error——round/tool_start/
// tool_end/done 随服务端工具循环退役）、出站请求构造（system 首位、
// stream_options.include_usage、无服务端工具注入、无 purpose 审计头、按所选
// Key 派发）与错误映射（400/404/502）。SSE 流经真实 handler 产出，不测私有
// 方法捷径。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// parseSSEFrames 把演练场响应字节按 data: 帧切成 JSON map 列表。
func parseSSEFrames(t *testing.T, body string) []map[string]any {
	t.Helper()
	frames := []map[string]any{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatalf("SSE 帧不是合法 JSON: %v（原文 %q）", err, line)
		}
		frames = append(frames, frame)
	}
	return frames
}

// frameTypes 提取事件类型序列（断言主链）。
func frameTypes(frames []map[string]any) []string {
	types := make([]string, 0, len(frames))
	for _, frame := range frames {
		types = append(types, frame["type"].(string))
	}
	return types
}

func chatStreamBody(apiKeyID, modelID string, stream *bool) []byte {
	streamValue := true
	if stream != nil {
		streamValue = *stream
	}
	payload, _ := json.Marshal(map[string]any{
		"apiKeyId": apiKeyID, "modelId": modelID,
		"messages":    []map[string]any{{"role": "user", "content": "介绍一下这个页面"}},
		"system":      "你是演练场助手",
		"stream":      streamValue,
		"temperature": 0.7,
	})
	return payload
}

// TestChatStreamHappyPath 单轮对话：delta 透传、usage 归一（done 事件已随工具
// 循环退役，流关闭即结束）、出站请求体形状（system 首条、
// stream_options.include_usage、无工具注入、无 purpose 审计头）与派发归属
// （APIKeyID = 所选 Key）。
func TestChatStreamHappyPath(t *testing.T) {
	deps := playgroundFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: sseBody(
			`data: {"choices":[{"delta":{"content":"你好"}}]}`,
			`data: {"choices":[{"delta":{"content":"，世界"}}]}`,
			`data: {"choices":[{"delta":{}}],"usage":{"prompt_tokens":11,"completion_tokens":4,"total_tokens":15}}`,
			`data: [DONE]`,
		),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody(playgroundKeyModelsFixtureID, "gpt-5.4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body %s）", recorder.Code, recorder.Body.String())
	}
	frames := parseSSEFrames(t, recorder.Body.String())
	if got := frameTypes(frames); strings.Join(got, ",") != "delta,delta,usage" {
		t.Fatalf("事件序列 = %v，期望 delta,delta,usage", got)
	}
	if frames[0]["text"] != "你好" || frames[1]["text"] != "，世界" {
		t.Fatalf("delta 文本不符: %v", frames[0:2])
	}
	if frames[2]["promptTokens"].(float64) != 11 || frames[2]["completionTokens"].(float64) != 4 || frames[2]["totalTokens"].(float64) != 15 {
		t.Fatalf("usage 归一不符: %v", frames[2])
	}
	if len(dispatch.calls) != 1 {
		t.Fatalf("派发次数 = %d，期望 1", len(dispatch.calls))
	}
	call := dispatch.calls[0]
	if call.Request.Path != "/v1/chat/completions" || call.Request.Method != "POST" {
		t.Fatalf("派发目标 = %s %s，期望 POST /v1/chat/completions", call.Request.Method, call.Request.Path)
	}
	// 派发头钉测：仅内容协商两键，无任何审计/身份/用途注入头（退役面收敛）。
	if len(call.Request.Headers) != 2 || call.Request.Headers["content-type"] != "application/json" || call.Request.Headers["accept"] != "text/event-stream" {
		t.Fatalf("派发头应仅内容协商两键: %v", call.Request.Headers)
	}
	if call.Request.APIKeyID != playgroundKeyModelsFixtureID || call.Request.OwnerID != "sys_user_1" {
		t.Fatalf("派发归属不符: apiKey=%q owner=%q", call.Request.APIKeyID, call.Request.OwnerID)
	}
	var outbound struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
		Tools       []map[string]any `json:"tools"`
		Temperature *float64         `json:"temperature"`
	}
	if err := json.Unmarshal(call.Request.Body, &outbound); err != nil {
		t.Fatalf("出站请求体不是合法 JSON: %v", err)
	}
	if outbound.Model != "gpt-5.4" {
		t.Fatalf("出站 model = %q", outbound.Model)
	}
	if len(outbound.Messages) != 2 || outbound.Messages[0].Role != "system" || outbound.Messages[0].Content != "你是演练场助手" || outbound.Messages[1].Role != "user" {
		t.Fatalf("出站消息形状不符: %+v", outbound.Messages)
	}
	if outbound.StreamOptions == nil || !outbound.StreamOptions.IncludeUsage {
		t.Fatalf("缺少 stream_options.include_usage")
	}
	if outbound.Tools != nil {
		t.Fatalf("服务端 fetch_url 工具注入已退役，不得注入工具: %v", outbound.Tools)
	}
	if outbound.Temperature == nil || *outbound.Temperature != 0.7 {
		t.Fatalf("temperature 透传不符: %v", outbound.Temperature)
	}
}

// TestChatStreamModelToolCallsPassThrough 上游发起 tool_calls（服务端工具循环
// 已退役）：无正文增量透出、不执行工具、不发 round/tool 事件，流按既有正文
// 收敛（usage 照常透出）。
func TestChatStreamModelToolCallsPassThrough(t *testing.T) {
	deps := playgroundFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: sseBody(
			`data: {"choices":[{"delta":{"content":"直接回答"}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"fetch_url","arguments":"{\"url\":\"https://example.com/a\"}"}}]}}],"usage":{"prompt_tokens":20,"completion_tokens":6}}`,
			`data: [DONE]`,
		),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody(playgroundKeyModelsFixtureID, "gpt-5.4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	frames := parseSSEFrames(t, recorder.Body.String())
	if got := frameTypes(frames); strings.Join(got, ",") != "delta,usage" {
		t.Fatalf("事件序列 = %v，期望 delta,usage（无工具事件/无 done）", got)
	}
	if len(dispatch.calls) != 1 {
		t.Fatalf("派发次数 = %d，期望 1（无重派发）", len(dispatch.calls))
	}
	if strings.Contains(string(dispatch.calls[0].Request.Body), `"tools"`) {
		t.Fatalf("出站体不得携带工具定义")
	}
}

// TestChatStreamDispatchFailureMaps502 派发上游 500：SSE 未开始时落 502 JSON
// （code=playground_dispatch_failed，message 带上游状态码与上游 message 摘要）。
func TestChatStreamDispatchFailureMaps502(t *testing.T) {
	deps := playgroundFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Status: http.StatusInternalServerError,
		Body:   []byte(`{"error":{"message":"上游炸了","type":"upstream_error"}}`),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody(playgroundKeyModelsFixtureID, "gpt-5.4", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502", recorder.Code)
	}
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("错误载荷不是合法 JSON: %v", err)
	}
	if payload.Code != "playground_dispatch_failed" {
		t.Fatalf("code = %q", payload.Code)
	}
	if !strings.Contains(payload.Message, "HTTP 500") || !strings.Contains(payload.Message, "上游炸了") {
		t.Fatalf("message 缺少状态码/上游摘要: %q", payload.Message)
	}
}

// TestChatStreamMidStreamErrorFrame SSE 中途失败：此前 delta 已透出，以 error
// 帧收尾（流关闭即结束信号）。
func TestChatStreamMidStreamErrorFrame(t *testing.T) {
	deps := playgroundFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{Body: sseBody(
		`data: {"choices":[{"delta":{"content":"前半句"}}]}`,
		`data: {"error":{"message":"上游流断了"}}`,
		`data: [DONE]`,
	)}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody(playgroundKeyModelsFixtureID, "gpt-5.4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("SSE 已开始，状态码应为 200，实际 %d", recorder.Code)
	}
	frames := parseSSEFrames(t, recorder.Body.String())
	types := frameTypes(frames)
	if types[len(types)-1] != "error" {
		t.Fatalf("末帧应为 error，实际 %v", types)
	}
	if frames[len(frames)-1]["code"] != "playground_dispatch_failed" {
		t.Fatalf("error.code = %v", frames[len(frames)-1]["code"])
	}
	if types[0] != "delta" || !containsType(types, "delta") {
		t.Fatalf("error 前应已有 delta: %v", types)
	}
	if containsType(types, "done") || containsType(types, "round") {
		t.Fatalf("round/done 事件已退役: %v", types)
	}
}

func containsType(types []string, want string) bool {
	for _, item := range types {
		if item == want {
			return true
		}
	}
	return false
}

// TestChatStreamValidationErrors 校验面：model 不在候选（400
// playground_model_not_available）、Key 不存在/非本人（404
// playground_key_not_found）、stream=false（400）、messages 越界（400）、
// role 越界（400）。
func TestChatStreamValidationErrors(t *testing.T) {
	deps := playgroundFixtureDeps(t)
	mux, _ := newTestRoutes(deps)

	cases := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantCode   string
	}{
		{
			name:       "模型不在候选",
			body:       map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gpt-6.1-terra", "messages": []map[string]any{{"role": "user", "content": "hi"}}, "stream": true},
			wantStatus: http.StatusBadRequest, wantCode: "playground_model_not_available",
		},
		{
			name:       "Key 不存在或非本人",
			body:       map[string]any{"apiKeyId": "key_other", "modelId": "gpt-5.4", "messages": []map[string]any{{"role": "user", "content": "hi"}}, "stream": true},
			wantStatus: http.StatusNotFound, wantCode: "playground_key_not_found",
		},
		{
			name:       "stream=false",
			body:       map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gpt-5.4", "messages": []map[string]any{{"role": "user", "content": "hi"}}, "stream": false},
			wantStatus: http.StatusBadRequest, wantCode: "playground_invalid_request",
		},
		{
			name:       "messages 为空",
			body:       map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gpt-5.4", "messages": []map[string]any{}, "stream": true},
			wantStatus: http.StatusBadRequest, wantCode: "playground_invalid_request",
		},
		{
			name:       "role 越界",
			body:       map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gpt-5.4", "messages": []map[string]any{{"role": "developer", "content": "hi"}}, "stream": true},
			wantStatus: http.StatusBadRequest, wantCode: "playground_invalid_request",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload, _ := json.Marshal(testCase.body)
			recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", payload)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d（body %s）", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			var payloadOut struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payloadOut); err != nil {
				t.Fatalf("错误载荷解析失败: %v", err)
			}
			if payloadOut.Code != testCase.wantCode {
				t.Fatalf("code = %q，期望 %q", payloadOut.Code, testCase.wantCode)
			}
		})
	}
}

// TestChatStreamPassThroughWithoutRequireSession：EnsureSession 缺位（组合测试
// 直挂）时 ScopeResolver 仍被调用；此处仅回归 wrap 透传不吞请求。
func TestChatStreamPassThroughWithoutRequireSession(t *testing.T) {
	deps := playgroundFixtureDeps(t)
	deps.RequireSession = nil
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{Body: sseBody(`data: [DONE]`)}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody(playgroundKeyModelsFixtureID, "gpt-5.4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	if len(dispatch.calls) != 1 {
		t.Fatalf("派发次数 = %d", len(dispatch.calls))
	}
}

// TestChatStreamHeartbeatPingFrames SSE 心跳（sse.go）：上游流内停顿超过注入
// 的心跳间隔时写出 : ping 注释帧保活；注释帧不进 data 事件序列（前端消费端
// 跳过 `:` 帧）。
func TestChatStreamHeartbeatPingFrames(t *testing.T) {
	previousInterval := playgroundSSEHeartbeatInterval
	playgroundSSEHeartbeatInterval = 10 * time.Millisecond
	t.Cleanup(func() { playgroundSSEHeartbeatInterval = previousInterval })

	deps := playgroundFixtureDeps(t)
	base := deps.Dispatch.(*mockDispatch)
	base.script = []scriptedResponse{{Body: sseBody(
		`data: {"choices":[{"delta":{"content":"前半"}}]}`,
		`data: {"choices":[{"delta":{"content":"后半"}}]}`,
		`data: [DONE]`,
	)}}
	// 首个 delta 读出后停顿 60ms（> 注入间隔）：心跳注释帧只在帧头提交后
	// 写出，停顿须落在首事件之后。
	deps.Dispatch = pausedBodyDispatch{inner: base, pause: 60 * time.Millisecond}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody(playgroundKeyModelsFixtureID, "gpt-5.4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, ": ping\n\n") {
		t.Fatalf("流内停顿应产出 : ping 心跳注释帧: %q", body)
	}
	frames := parseSSEFrames(t, body)
	if got := frameTypes(frames); strings.Join(got, ",") != "delta,delta" {
		t.Fatalf("心跳帧不得进入事件序列: %v（body %q）", got, body)
	}
}

// TestPlaygroundSSEWriterStopStopsHeartbeat Stop 同步等待心跳 goroutine 退出：
// Stop 后不再产出任何新字节（含 error/panic 路径由 runChatStream defer 承接）。
func TestPlaygroundSSEWriterStopStopsHeartbeat(t *testing.T) {
	previousInterval := playgroundSSEHeartbeatInterval
	playgroundSSEHeartbeatInterval = time.Millisecond
	t.Cleanup(func() { playgroundSSEHeartbeatInterval = previousInterval })

	recorder := httptest.NewRecorder()
	writer := newPlaygroundSSEWriter(recorder)
	writer.writeEvent(sseEventFrame{Type: "delta", Text: "x"})
	writer.Stop()
	frozen := recorder.Body.Len()
	time.Sleep(5 * time.Millisecond)
	if recorder.Body.Len() != frozen {
		t.Fatalf("Stop 后仍写出新字节（len %d → %d）", frozen, recorder.Body.Len())
	}
	if !strings.Contains(recorder.Body.String(), `data: {"type":"delta","text":"x"}`) {
		t.Fatalf("事件帧缺失: %q", recorder.Body.String())
	}
}

// pausedBodyDispatch 把派发响应体包装为慢流：首个上游事件行交付后滞留剩余
// 字节并停顿（心跳窗口制造用：停顿落在首个 delta 已解析、后续 delta 未到达
// 之间，帧头已提交）。
type pausedBodyDispatch struct {
	inner DispatchPort
	pause time.Duration
}

func (p pausedBodyDispatch) Dispatch(ctx context.Context, req DispatchRequest) (*DispatchResponse, error) {
	response, err := p.inner.Dispatch(ctx, req)
	if err != nil || response == nil {
		return response, err
	}
	response.Body = &pausedReadCloser{inner: response.Body, pause: p.pause}
	return response, nil
}

// pausedReadCloser 首个换行字节交付后滞留本次读到的剩余字节并停顿一次
// （上游内存体单次读即整包返回，必须截断滞留才能在事件间制造真实停顿）。
type pausedReadCloser struct {
	inner  io.ReadCloser
	pause  time.Duration
	done   bool
	rest   []byte
	restAt int
}

func (p *pausedReadCloser) Read(b []byte) (int, error) {
	if p.rest != nil {
		time.Sleep(p.pause)
		n := copy(b, p.rest[p.restAt:])
		p.restAt += n
		if p.restAt >= len(p.rest) {
			p.rest = nil
		}
		return n, nil
	}
	n, err := p.inner.Read(b)
	if !p.done {
		p.done = true
		if i := bytes.IndexByte(b[:n], 10); i >= 0 { // 10 = LF：首个事件行结尾
			p.rest = append([]byte(nil), b[i+1:n]...)
			return i + 1, nil
		}
	}
	return n, err
}

func (p *pausedReadCloser) Close() error { return p.inner.Close() }
