package playground

// POST /chat/stream 测试：Mock 派发执行器回放固定上游 SSE 字节流，断言归一
// 事件序列（round/delta/tool_start/tool_end/usage/done/error）、工具循环轮次
// 预算（第 3 轮后不注入工具）、fetch 失败回喂、出站请求构造（system 首位、
// include_usage、fetch_url 工具注入、purpose 审计头）与错误映射
// （400/404/502）。SSE 流经真实 handler 产出，不测私有方法捷径。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// playgroundFixtureDeps 装配「一个 gpt 账户 + chat 目录行」的最小依赖（chat
// 行带 function_calling 声明由参数控制）。
func playgroundFixtureDeps(functionCalling bool) *Deps {
	tools := map[string][]string{}
	if functionCalling {
		tools["chat_completions"] = []string{"function_calling"}
	}
	transportView := TransportAccount{
		ID:                     "acc_1",
		Type:                   "api_key",
		ProviderCode:           "gpt",
		SupportedEndpointModes: []string{"chat_sse", "video_create", "audio_speech"},
	}
	return &Deps{
		Dispatch:          &mockDispatch{},
		Accounts:          &staticAccounts{options: []AccountOption{{ID: "acc_1", Name: "神影-克隆", ProviderCode: "gpt", Status: "active"}}},
		AccountLookup:     &staticLookup{refs: map[string]*AccountRef{"acc_1": {ID: "acc_1", Name: "神影-克隆", ProviderCode: "gpt", Enabled: true, EnabledGroupIDs: []string{"grp_1"}}}},
		TransportAccounts: &staticTransport{views: map[string][]TransportAccount{"acc_1": {transportView}}},
		ProviderCatalog:   &staticCatalog{byProvider: map[string][]CatalogItem{"gpt": {{Model: "gpt-5.4", SupportedAPIProtocols: []string{"chat_completions"}, SupportedToolsByProtocol: tools}}}},
		AccountCatalog:    &staticCatalog{},
		FetchURL: func(context.Context, string) (FetchResult, error) {
			return FetchResult{Text: "正文首行\n第二行"}, nil
		},
	}
}

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

func chatStreamBody(accountID, modelID string, stream *bool) []byte {
	streamValue := true
	if stream != nil {
		streamValue = *stream
	}
	payload, _ := json.Marshal(map[string]any{
		"accountId": accountID, "modelId": modelID,
		"messages":    []map[string]any{{"role": "user", "content": "介绍一下这个页面"}},
		"system":      "你是演练场助手",
		"stream":      streamValue,
		"temperature": 0.7,
	})
	return payload
}

// TestChatStreamHappyPath 单轮对话：delta 透传、usage 归一、done 收尾、出站
// 请求体形状（system 首条、stream_options.include_usage、无工具注入）。
func TestChatStreamHappyPath(t *testing.T) {
	deps := playgroundFixtureDeps(false)
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
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody("acc_1", "gpt-5.4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body %s）", recorder.Code, recorder.Body.String())
	}
	frames := parseSSEFrames(t, recorder.Body.String())
	if got := frameTypes(frames); strings.Join(got, ",") != "round,delta,delta,usage,done" {
		t.Fatalf("事件序列 = %v，期望 round,delta,delta,usage,done", got)
	}
	if frames[1]["text"] != "你好" || frames[2]["text"] != "，世界" {
		t.Fatalf("delta 文本不符: %v", frames[1:3])
	}
	if frames[3]["promptTokens"].(float64) != 11 || frames[3]["completionTokens"].(float64) != 4 || frames[3]["totalTokens"].(float64) != 15 {
		t.Fatalf("usage 归一不符: %v", frames[3])
	}
	if frames[4]["rounds"].(float64) != 1 {
		t.Fatalf("done.rounds = %v，期望 1", frames[4]["rounds"])
	}
	if len(dispatch.calls) != 1 {
		t.Fatalf("派发次数 = %d，期望 1", len(dispatch.calls))
	}
	call := dispatch.calls[0]
	if call.Request.Path != "/v1/chat/completions" || call.Request.Method != "POST" {
		t.Fatalf("派发目标 = %s %s，期望 POST /v1/chat/completions", call.Request.Method, call.Request.Path)
	}
	if call.Request.Headers["x-juhe-ai-purpose"] != "playground_chat" {
		t.Fatalf("purpose 头 = %q，期望 playground_chat", call.Request.Headers["x-juhe-ai-purpose"])
	}
	if call.Request.OwnerID != "sys_user_1" || call.Request.AccountID != "acc_1" {
		t.Fatalf("派发归属不符: account=%q owner=%q", call.Request.AccountID, call.Request.OwnerID)
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
		t.Fatalf("未声明 function_calling 的模型不应注入工具: %v", outbound.Tools)
	}
	if outbound.Temperature == nil || *outbound.Temperature != 0.7 {
		t.Fatalf("temperature 透传不符: %v", outbound.Temperature)
	}
}

// TestChatStreamToolLoopTwoRounds 工具循环 2 轮：round1 发起 fetch_url → 后端
// 执行（tool_start/tool_end）→ 回喂 role:tool 重派发 → round2 出正文与 usage
// → done.rounds=2；续传体含 assistant tool_calls 与 tool 结果（标注来源 URL）。
func TestChatStreamToolLoopTwoRounds(t *testing.T) {
	deps := playgroundFixtureDeps(true)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{
		{Body: sseBody(
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"fetch_url","arguments":"{\"url\":\"https://example.com/a\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
		)},
		{Body: sseBody(
			`data: {"choices":[{"delta":{"content":"根据网页内容回答"}}]}`,
			`data: {"choices":[{"delta":{}}],"usage":{"prompt_tokens":20,"completion_tokens":6}}`,
			`data: [DONE]`,
		)},
	}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody("acc_1", "gpt-5.4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	frames := parseSSEFrames(t, recorder.Body.String())
	if got := frameTypes(frames); strings.Join(got, ",") != "round,tool_start,tool_end,round,delta,usage,done" {
		t.Fatalf("事件序列 = %v", got)
	}
	if frames[1]["callId"] != "call_1" || frames[1]["name"] != "fetch_url" {
		t.Fatalf("tool_start 形状不符: %v", frames[1])
	}
	if frames[2]["ok"] != true || frames[2]["summary"] != "正文首行" {
		t.Fatalf("tool_end 形状不符（summary 应为首行）: %v", frames[2])
	}
	if frames[6]["rounds"].(float64) != 2 {
		t.Fatalf("done.rounds = %v，期望 2", frames[6]["rounds"])
	}
	// usage totalTokens 缺省按和补齐（20+6=26）。
	if frames[5]["totalTokens"].(float64) != 26 {
		t.Fatalf("usage.totalTokens = %v，期望 26（缺省按和补齐）", frames[5]["totalTokens"])
	}
	if len(dispatch.calls) != 2 {
		t.Fatalf("派发次数 = %d，期望 2", len(dispatch.calls))
	}
	// 首次派发注入 fetch_url 工具定义；第二次派发同样注入（预算 3 轮内）。
	for index, call := range dispatch.calls {
		if !strings.Contains(string(call.Request.Body), `"tools"`) || !strings.Contains(string(call.Request.Body), fetchURLToolName) {
			t.Fatalf("第 %d 次派发未注入 fetch_url 工具", index+1)
		}
	}
	var continuation struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []any  `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(dispatch.calls[1].Request.Body, &continuation); err != nil {
		t.Fatalf("续传请求体解析失败: %v", err)
	}
	foundAssistantToolCalls, foundToolResult := false, false
	for _, message := range continuation.Messages {
		switch message.Role {
		case "assistant":
			if len(message.ToolCalls) == 1 {
				foundAssistantToolCalls = true
			}
		case "tool":
			content, _ := message.Content.(string)
			if message.ToolCallID == "call_1" && strings.HasPrefix(content, "URL: https://example.com/a") && strings.Contains(content, "正文首行") {
				foundToolResult = true
			}
		}
	}
	if !foundAssistantToolCalls || !foundToolResult {
		t.Fatalf("续传体缺少 assistant tool_calls 或 tool 结果消息: %+v", continuation.Messages)
	}
}

// TestChatStreamFetchFailureFeedsBack fetch 失败不产生 HTTP 错误：「抓取失败:
// <原因>」作为 tool 结果回喂，tool_end ok=false。
func TestChatStreamFetchFailureFeedsBack(t *testing.T) {
	deps := playgroundFixtureDeps(true)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{
		{Body: sseBody(
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"fetch_url","arguments":"{\"url\":\"https://example.com/broken\"}"}}]}}]}`,
			`data: [DONE]`,
		)},
		{Body: sseBody(`data: {"choices":[{"delta":{"content":"网页取不到，我直接回答"}}]}`, `data: [DONE]`)},
	}
	deps.FetchURL = func(context.Context, string) (FetchResult, error) {
		return FetchResult{}, errors.New("DNS 解析失败")
	}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody("acc_1", "gpt-5.4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（fetch 失败不应产生 HTTP 错误）", recorder.Code)
	}
	frames := parseSSEFrames(t, recorder.Body.String())
	if got := frameTypes(frames); strings.Join(got, ",") != "round,tool_start,tool_end,round,delta,done" {
		t.Fatalf("事件序列 = %v", got)
	}
	if frames[2]["ok"] != false || !strings.Contains(frames[2]["summary"].(string), "DNS 解析失败") {
		t.Fatalf("tool_end 未携带失败摘要: %v", frames[2])
	}
	var continuation struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(dispatch.calls[1].Request.Body, &continuation); err != nil {
		t.Fatalf("续传请求体解析失败: %v", err)
	}
	found := false
	for _, message := range continuation.Messages {
		if message.Role == "tool" && strings.Contains(message.Content, "抓取失败: DNS 解析失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("续传体未把「抓取失败:<原因>」回喂模型: %+v", continuation.Messages)
	}
}

// TestChatStreamToolBudgetExhausted 3 轮工具预算耗尽：第 3 轮返回 tool_calls
// 后，第 4 次派发不再注入工具（强制收尾）。
func TestChatStreamToolBudgetExhausted(t *testing.T) {
	deps := playgroundFixtureDeps(true)
	dispatch := deps.Dispatch.(*mockDispatch)
	toolRound := sseBody(
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_x","function":{"name":"fetch_url","arguments":"{\"url\":\"https://example.com/x\"}"}}]}}]}`,
		`data: [DONE]`,
	)
	dispatch.script = []scriptedResponse{
		{Body: toolRound}, {Body: toolRound}, {Body: toolRound},
		{Body: sseBody(`data: {"choices":[{"delta":{"content":"收尾回答"}}]}`, `data: [DONE]`)},
	}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody("acc_1", "gpt-5.4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	if len(dispatch.calls) != 4 {
		t.Fatalf("派发次数 = %d，期望 4（3 轮工具 + 收尾）", len(dispatch.calls))
	}
	for index, call := range dispatch.calls {
		hasTools := strings.Contains(string(call.Request.Body), `"tools"`)
		if index < 3 && !hasTools {
			t.Fatalf("第 %d 次派发应注入工具", index+1)
		}
		if index == 3 && hasTools {
			t.Fatalf("第 4 次派发（收尾）不应注入工具")
		}
	}
	frames := parseSSEFrames(t, recorder.Body.String())
	if frameTypes(frames)[len(frames)-1] != "done" {
		t.Fatalf("收尾事件缺失: %v", frames)
	}
}

// TestChatStreamDispatchFailureMaps502 首次派发上游 500：SSE 未开始时落
// 502 JSON（code=playground_dispatch_failed，message 带上游状态码与上游
// message 摘要）。
func TestChatStreamDispatchFailureMaps502(t *testing.T) {
	deps := playgroundFixtureDeps(false)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Status: http.StatusInternalServerError,
		Body:   []byte(`{"error":{"message":"上游炸了","type":"upstream_error"}}`),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody("acc_1", "gpt-5.4", nil))
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
// 帧收尾（不再发 done）。
func TestChatStreamMidStreamErrorFrame(t *testing.T) {
	deps := playgroundFixtureDeps(false)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{Body: sseBody(
		`data: {"choices":[{"delta":{"content":"前半句"}}]}`,
		`data: {"error":{"message":"上游流断了"}}`,
		`data: [DONE]`,
	)}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody("acc_1", "gpt-5.4", nil))
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
	if frameTypes(frames)[0] != "round" || !containsType(types, "delta") {
		t.Fatalf("error 前应已有 round/delta: %v", types)
	}
	if containsType(types, "done") {
		t.Fatalf("error 收尾不应再发 done: %v", types)
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
// playground_model_not_available）、账户范围外（404
// playground_account_not_found）、stream=false（400）、messages 越界（400）、
// role 越界含 tool（400——工具上下文由服务端循环内部持有）。
func TestChatStreamValidationErrors(t *testing.T) {
	deps := playgroundFixtureDeps(false)
	mux, _ := newTestRoutes(deps)

	cases := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantCode   string
	}{
		{
			name:       "模型不在候选",
			body:       map[string]any{"accountId": "acc_1", "modelId": "gpt-6.1-terra", "messages": []map[string]any{{"role": "user", "content": "hi"}}, "stream": true},
			wantStatus: http.StatusBadRequest, wantCode: "playground_model_not_available",
		},
		{
			name:       "账户范围外",
			body:       map[string]any{"accountId": "acc_other", "modelId": "gpt-5.4", "messages": []map[string]any{{"role": "user", "content": "hi"}}, "stream": true},
			wantStatus: http.StatusNotFound, wantCode: "playground_account_not_found",
		},
		{
			name:       "stream=false",
			body:       map[string]any{"accountId": "acc_1", "modelId": "gpt-5.4", "messages": []map[string]any{{"role": "user", "content": "hi"}}, "stream": false},
			wantStatus: http.StatusBadRequest, wantCode: "playground_invalid_request",
		},
		{
			name:       "messages 为空",
			body:       map[string]any{"accountId": "acc_1", "modelId": "gpt-5.4", "messages": []map[string]any{}, "stream": true},
			wantStatus: http.StatusBadRequest, wantCode: "playground_invalid_request",
		},
		{
			name:       "role 越界",
			body:       map[string]any{"accountId": "acc_1", "modelId": "gpt-5.4", "messages": []map[string]any{{"role": "developer", "content": "hi"}}, "stream": true},
			wantStatus: http.StatusBadRequest, wantCode: "playground_invalid_request",
		},
		{
			name:       "role tool 拒绝",
			body:       map[string]any{"accountId": "acc_1", "modelId": "gpt-5.4", "messages": []map[string]any{{"role": "tool", "content": "工具结果回放"}}, "stream": true},
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

// TestChatStreamRoundBudgetEventShape 工具事件参数回显：tool_start.args 原样
// 携带模型产出的 arguments JSON。
func TestChatStreamRoundBudgetEventShape(t *testing.T) {
	deps := playgroundFixtureDeps(true)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: sseBody(
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"fetch_url","arguments":"{\"url\":\"https://example.com/p\"}"}}]}}]}`,
			`data: [DONE]`,
		)},
		{Body: sseBody(`data: {"choices":[{"delta":{"content":"ok"}}]}`, `data: [DONE]`)},
	}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody("acc_1", "gpt-5.4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", recorder.Code)
	}
	frames := parseSSEFrames(t, recorder.Body.String())
	if frames[1]["args"] != `{"url":"https://example.com/p"}` {
		t.Fatalf("tool_start.args = %v", frames[1]["args"])
	}
}

// TestChatStreamRequireSessionNotExposed：EnsureSession 缺位（组合测试直挂）
// 时 ScopeResolver 仍被调用；此处仅回归 wrap 透传不吞请求。
func TestChatStreamPassThroughWithoutRequireSession(t *testing.T) {
	deps := playgroundFixtureDeps(false)
	deps.RequireSession = nil
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{Body: sseBody(`data: [DONE]`)}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody("acc_1", "gpt-5.4", nil))
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

	deps := playgroundFixtureDeps(false)
	base := deps.Dispatch.(*mockDispatch)
	base.script = []scriptedResponse{{Body: sseBody(
		`data: {"choices":[{"delta":{"content":"前半"}}]}`,
		`data: {"choices":[{"delta":{"content":"后半"}}]}`,
		`data: [DONE]`,
	)}}
	// 首次读上游流前停顿 60ms（> 注入间隔），在 round 帧后制造心跳窗口。
	deps.Dispatch = pausedBodyDispatch{inner: base, pause: 60 * time.Millisecond}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/chat/stream", chatStreamBody("acc_1", "gpt-5.4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, ": ping\n\n") {
		t.Fatalf("流内停顿应产出 : ping 心跳注释帧: %q", body)
	}
	frames := parseSSEFrames(t, body)
	if got := frameTypes(frames); strings.Join(got, ",") != "round,delta,delta,done" {
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

// pausedBodyDispatch 把派发响应体包装为首次读前停顿的慢流（心跳窗口制造用）。
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

// pausedReadCloser 首次 Read 前停顿一次。
type pausedReadCloser struct {
	inner  io.ReadCloser
	pause  time.Duration
	paused bool
}

func (p *pausedReadCloser) Read(b []byte) (int, error) {
	if !p.paused {
		p.paused = true
		time.Sleep(p.pause)
	}
	return p.inner.Read(b)
}

func (p *pausedReadCloser) Close() error { return p.inner.Close() }
