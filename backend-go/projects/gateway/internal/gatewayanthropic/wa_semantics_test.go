package gatewayanthropic

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Anthropic 响应端点族契约（对齐 Node anthropicResponseEndpointFamilyFromPath）。
func TestWAResponseEndpointFamilyFromPath(t *testing.T) {
	cases := []struct {
		path   string
		family EndpointFamily
	}{
		{"/v1/messages", EndpointFamilyMessages},
		{"/messages?beta=true", EndpointFamilyMessages},
		{"/v1/messages/count_tokens", EndpointFamilyMessageTokenCount},
		{"/messages/count_tokens", EndpointFamilyMessageTokenCount},
		{"/v1/models", EndpointFamilyModels},
		{"/models", EndpointFamilyModels},
		{"messages", EndpointFamilyMessages}, // 无前导斜杠自动补齐
		{"/", EndpointFamilyMessages},
		{"/v1", EndpointFamilyMessages},              // 剥离 /v1 后为空，回退 /
		{"/v1beta/messages", EndpointFamilyMessages}, // /v1beta 不匹配 /v1 前缀规则
	}
	for _, tc := range cases {
		if got := ResponseEndpointFamilyFromPath(tc.path); got != tc.family {
			t.Fatalf("ResponseEndpointFamilyFromPath(%q) = %q，期望 %q", tc.path, got, tc.family)
		}
	}
}

// 缓冲 JSON 语义帧：错误根、messages 文本/thinking/tool_use、usage、raw 兜底帧。
func TestWAExtractJSONSemanticFrames(t *testing.T) {
	t.Run("非对象输入返回 nil", func(t *testing.T) {
		if frames := ExtractJSONSemanticFrames([]any{1}, EndpointFamilyMessages); frames != nil {
			t.Fatalf("数组输入应返回 nil，得到 %+v", frames)
		}
	})
	t.Run("error 根对象", func(t *testing.T) {
		root := waParseJSONObject(t, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)
		frames := ExtractJSONSemanticFrames(root, EndpointFamilyMessages)
		if len(frames) == 0 {
			t.Fatal("应至少产生一帧")
		}
		first := frames[0]
		if first.FrameType != FrameTypeError {
			t.Fatalf("首帧类型 = %q，期望 error", first.FrameType)
		}
		if first.ErrorCode != "invalid_request_error" || first.ErrorType != "invalid_request_error" || first.ErrorMessage != "bad" {
			t.Fatalf("错误帧字段 = %+v", first)
		}
		if stringSliceEqual(first.RawJSONPaths, []string{"error"}) {
			// 期望路径 ["error"]
		} else {
			t.Fatalf("RawJSONPaths = %v，期望 [error]", first.RawJSONPaths)
		}
		if first.Transport != TransportJSON {
			t.Fatalf("Transport = %q，期望 json", first.Transport)
		}
		if first.RawJSON == nil {
			t.Fatal("错误帧应回填 RawJSON 根对象")
		}
	})
	t.Run("messages 内容与 usage 与 raw 帧", func(t *testing.T) {
		root := waParseJSONObject(t, `{
			"content":[
				{"type":"text","text":"答案"},
				{"type":"thinking","thinking":"推理过程"},
				{"type":"tool_use","id":"t1","name":"f"},
				42
			],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":3,"output_tokens":4}
		}`)
		frames := ExtractJSONSemanticFrames(root, EndpointFamilyMessages)
		var textDone, thinkingDone, toolUse, completed, usageFrame *ResponseSemanticFrame
		for index := range frames {
			frame := &frames[index]
			switch {
			case frame.FrameType == FrameTypeOutputTextDone && frame.Text == "答案":
				textDone = frame
			case frame.FrameType == FrameTypeOutputTextDone && frame.Text == "推理过程":
				thinkingDone = frame
			case frame.FrameType == FrameTypeRawJSONPath && stringSliceEqual(frame.RawJSONPaths, []string{"content.2"}):
				toolUse = frame
			case frame.FrameType == FrameTypeCompleted:
				completed = frame
			case frame.FrameType == FrameTypeUsage:
				usageFrame = frame
			}
		}
		if textDone == nil || textDone.ContentIndex == nil || *textDone.ContentIndex != 0 || textDone.VisibleOutput == nil || !*textDone.VisibleOutput {
			t.Fatalf("文本帧缺失或字段不符: %+v", textDone)
		}
		if textDone.FinishReason != "tool_use" || textDone.Status != "tool_use" {
			t.Fatalf("文本帧 stop_reason 传播错误: %+v", textDone)
		}
		if thinkingDone == nil || thinkingDone.VisibleOutput == nil || *thinkingDone.VisibleOutput {
			t.Fatalf("thinking 帧应存在且不可见: %+v", thinkingDone)
		}
		if toolUse == nil || toolUse.VisibleOutput == nil || *toolUse.VisibleOutput {
			t.Fatalf("tool_use 帧应存在且不可见: %+v", toolUse)
		}
		if completed == nil || completed.FinishReason != "tool_use" {
			t.Fatalf("completed 帧缺失: %+v", completed)
		}
		if usageFrame == nil || usageFrame.Usage == nil || usageFrame.Usage.OutputTokens == nil || *usageFrame.Usage.OutputTokens != 4 {
			t.Fatalf("usage 帧缺失: %+v", usageFrame)
		}
		if !stringSliceEqual(usageFrame.RawJSONPaths, []string{"usage"}) {
			t.Fatalf("usage 帧路径 = %v", usageFrame.RawJSONPaths)
		}
		last := frames[len(frames)-1]
		if last.FrameType != FrameTypeRawJSONPath || last.RawJSON == nil {
			t.Fatalf("末帧应为 raw 兜底帧: %+v", last)
		}
	})
	t.Run("models 族只产生 usage 与 raw 帧", func(t *testing.T) {
		root := waParseJSONObject(t, `{"data":[],"usage":{"input_tokens":1}}`)
		frames := ExtractJSONSemanticFrames(root, EndpointFamilyModels)
		for _, frame := range frames {
			if frame.FrameType == FrameTypeOutputTextDone {
				t.Fatalf("models 族不应产生文本帧: %+v", frame)
			}
		}
	})
	t.Run("无 stop_reason 不产生 completed 帧", func(t *testing.T) {
		root := waParseJSONObject(t, `{"content":[{"type":"text","text":"hi"}]}`)
		for _, frame := range ExtractJSONSemanticFrames(root, EndpointFamilyMessages) {
			if frame.FrameType == FrameTypeCompleted {
				t.Fatalf("无 stop_reason 不应有 completed 帧: %+v", frame)
			}
		}
	})
}

// 流式语义帧：content_block_start/delta、message_delta/message_stop、错误事件。
func TestWAExtractSSESemanticFrames(t *testing.T) {
	decode := func(text string) StreamEvent {
		return ParseSSEEventData(text, "", "", 0)
	}
	t.Run("data 为 nil 时无帧", func(t *testing.T) {
		event := ParseSSEEventData("", "message_stop", "", 0)
		if frames := ExtractSSESemanticFrames(event, EndpointFamilyMessages); len(frames) != 0 {
			t.Fatalf("data 缺省应无帧: %+v", frames)
		}
	})
	t.Run("content_block_delta 文本", func(t *testing.T) {
		event := decode(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你"}}`)
		frames := ExtractSSESemanticFrames(event, EndpointFamilyMessages)
		var delta *ResponseSemanticFrame
		for index := range frames {
			if frames[index].FrameType == FrameTypeOutputTextDelta {
				delta = &frames[index]
			}
		}
		if delta == nil || delta.Text != "你" {
			t.Fatalf("文本 delta 帧缺失: %+v", delta)
		}
		if delta.ContentIndex == nil || *delta.ContentIndex != 0 {
			t.Fatalf("ContentIndex = %+v", delta)
		}
		if !stringSliceEqual(delta.RawJSONPaths, []string{"delta.text"}) {
			t.Fatalf("路径 = %v，期望 [delta.text]", delta.RawJSONPaths)
		}
		if delta.VisibleOutput == nil || !*delta.VisibleOutput {
			t.Fatal("文本 delta 应可见")
		}
		if delta.EventType != "content_block_delta" {
			t.Fatalf("EventType = %q", delta.EventType)
		}
	})
	t.Run("input_json_delta 与 thinking_delta 路径与可见性", func(t *testing.T) {
		jsonEvent := decode(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`)
		frames := ExtractSSESemanticFrames(jsonEvent, EndpointFamilyMessages)
		found := false
		for index := range frames {
			if frames[index].FrameType == FrameTypeOutputTextDelta {
				found = true
				if !stringSliceEqual(frames[index].RawJSONPaths, []string{"delta.partial_json"}) {
					t.Fatalf("input_json_delta 路径 = %v", frames[index].RawJSONPaths)
				}
				if frames[index].VisibleOutput == nil || !*frames[index].VisibleOutput {
					t.Fatal("input_json_delta 应可见")
				}
			}
		}
		if !found {
			t.Fatal("input_json_delta 应产生文本帧")
		}
		thinkingEvent := decode(`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"推理"}}`)
		thinkingFound := false
		for _, frame := range ExtractSSESemanticFrames(thinkingEvent, EndpointFamilyMessages) {
			if frame.FrameType == FrameTypeOutputTextDelta {
				thinkingFound = true
				if !stringSliceEqual(frame.RawJSONPaths, []string{"delta.thinking"}) {
					t.Fatalf("thinking_delta 路径 = %v", frame.RawJSONPaths)
				}
				if frame.VisibleOutput == nil || *frame.VisibleOutput {
					t.Fatal("thinking_delta 不可见")
				}
			}
		}
		if !thinkingFound {
			t.Fatal("thinking_delta 应产生文本帧")
		}
	})
	t.Run("content_block_start tool_use", func(t *testing.T) {
		event := decode(`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","name":"f"}}`)
		frames := ExtractSSESemanticFrames(event, EndpointFamilyMessages)
		var raw *ResponseSemanticFrame
		for index := range frames {
			if frames[index].FrameType == FrameTypeRawJSONPath && stringSliceEqual(frames[index].RawJSONPaths, []string{"content_block"}) {
				raw = &frames[index]
			}
		}
		if raw == nil || raw.ContentIndex == nil || *raw.ContentIndex != 2 {
			t.Fatalf("tool_use 起始帧缺失: %+v", raw)
		}
		if raw.VisibleOutput == nil || *raw.VisibleOutput {
			t.Fatal("tool_use 起始帧不可见")
		}
	})
	t.Run("message_delta stop_reason 与 message_stop", func(t *testing.T) {
		deltaEvent := decode(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
		frames := ExtractSSESemanticFrames(deltaEvent, EndpointFamilyMessages)
		var completed *ResponseSemanticFrame
		for index := range frames {
			if frames[index].FrameType == FrameTypeCompleted {
				completed = &frames[index]
			}
		}
		if completed == nil || completed.FinishReason != "end_turn" {
			t.Fatalf("message_delta completed 帧缺失: %+v", completed)
		}
		stopEvent := decode(`{"type":"message_stop"}`)
		frames = ExtractSSESemanticFrames(stopEvent, EndpointFamilyMessages)
		found := false
		for _, frame := range frames {
			if frame.FrameType == FrameTypeCompleted && frame.FinishReason == "message_stop" {
				found = true
			}
		}
		if !found {
			t.Fatal("message_stop 应产生 completed 帧")
		}
	})
	t.Run("error 事件", func(t *testing.T) {
		event := decode(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
		frames := ExtractSSESemanticFrames(event, EndpointFamilyMessages)
		if frames[0].FrameType != FrameTypeError {
			t.Fatalf("首帧 = %+v，期望 error", frames[0])
		}
		if frames[0].ErrorCode != "overloaded_error" || frames[0].ErrorMessage != "Overloaded" {
			t.Fatalf("错误帧 = %+v", frames[0])
		}
	})
	t.Run("message_start 的 message.usage 提取", func(t *testing.T) {
		event := decode(`{"type":"message_start","message":{"usage":{"input_tokens":25}}}`)
		frames := ExtractSSESemanticFrames(event, EndpointFamilyMessages)
		var usageFrame *ResponseSemanticFrame
		for index := range frames {
			if frames[index].FrameType == FrameTypeUsage {
				usageFrame = &frames[index]
			}
		}
		if usageFrame == nil || usageFrame.Usage == nil || usageFrame.Usage.InputTokens == nil || *usageFrame.Usage.InputTokens != 25 {
			t.Fatalf("message.usage 帧缺失: %+v", usageFrame)
		}
		if !stringSliceEqual(usageFrame.RawJSONPaths, []string{"message.usage"}) {
			t.Fatalf("usage 路径 = %v", usageFrame.RawJSONPaths)
		}
	})
	t.Run("原始文本与事件名回填", func(t *testing.T) {
		raw := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\n"
		event := ParseSSEEventText(raw)
		frames := ExtractSSESemanticFrames(event, EndpointFamilyMessages)
		for _, frame := range frames {
			if frame.EventType == "" || frame.RawText == "" {
				t.Fatalf("帧未回填 RawText/EventType: %+v", frame)
			}
		}
	})
}

func TestWAExtractStreamEventError(t *testing.T) {
	if ExtractStreamEventError(waParseJSONObject(t, `{"type":"content_block_delta"}`), "content_block_delta", "") != nil {
		t.Fatal("普通事件不应判为错误")
	}
	// type=error 且无 error 子对象时整包视为错误对象。
	direct := ExtractStreamEventError(waParseJSONObject(t, `{"type":"error","code":5,"message":"x"}`), "", "")
	if direct == nil || direct["code"] == nil {
		t.Fatalf("type=error 无子对象时应返回整包: %+v", direct)
	}
	// eventName 命中即可。
	if ExtractStreamEventError(waParseJSONObject(t, `{"error":{"message":"m"}}`), "content_block_delta", "error") == nil {
		t.Fatal("eventName=error 应判为错误")
	}
}

// 请求路径工具：归一化去 query、补斜杠、剥 /v1。
func TestWANormalizedAnthropicPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/v1/messages?beta=true", "/messages"},
		{"messages", "/messages"},
		{"/v1beta/messages", "/v1beta/messages"},
		{"/v1", "/"},
		{"/v1abc", "/v1abc"},
		{"", "/"},
	}
	for _, tc := range cases {
		if got := normalizedAnthropicPath(tc.in); got != tc.want {
			t.Fatalf("normalizedAnthropicPath(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestWAPathHelpers(t *testing.T) {
	parts := splitPathAndQuery("/a/b?x=1")
	if parts.Path != "/a/b" || parts.Query != "?x=1" {
		t.Fatalf("splitPathAndQuery = %+v", parts)
	}
	if stringValue("") != "" || stringValue(3) != "" || stringValue("v") != "v" {
		t.Fatal("stringValue 语义错误")
	}
	if numberPtr(float64(2)) == nil || *numberPtr(float64(2)) != 2 {
		t.Fatal("numberPtr(float64) 语义错误")
	}
	if numberPtr("3") == nil || *numberPtr("3") != 3 {
		t.Fatal("numberPtr(字符串数字) 语义错误")
	}
	if numberPtr("abc") != nil || numberPtr(nil) != nil {
		t.Fatal("numberPtr 非法输入应为 nil")
	}
	if orString("", "a", "b") != "a" || orString("", "") != "" {
		t.Fatal("orString 语义错误")
	}
	if stringFieldValue(" x ") != "x" || stringFieldValue(float64(3)) != "3" || stringFieldValue(float64(1.5)) != "1.5" || stringFieldValue(true) != "true" || stringFieldValue(nil) != "" {
		t.Fatal("stringFieldValue 宽松字符串语义错误")
	}
}

func TestWAIsNativeAndModelsRequest(t *testing.T) {
	cases := []struct {
		method, target string
		native         bool
		models         bool
	}{
		{"POST", "/v1/messages", true, false},
		{"POST", "/messages?beta=true", true, false},
		{"POST", "/v1/messages/count_tokens", true, false},
		{"GET", "/v1/models?limit=1", true, true},
		{"GET", "/models", true, true},
		{"POST", "/v1/models", false, false},
		{"GET", "/v1/messages", false, false},
		{"PUT", "/v1/models", false, false},
		{"GET", "/v1/models/claude-3", false, false},
	}
	for _, tc := range cases {
		request := httptest.NewRequest(tc.method, tc.target, nil)
		if got := IsNativeRequest(request); got != tc.native {
			t.Fatalf("IsNativeRequest(%s %s) = %v，期望 %v", tc.method, tc.target, got, tc.native)
		}
		if got := IsModelsRequest(request); got != tc.models {
			t.Fatalf("IsModelsRequest(%s %s) = %v，期望 %v", tc.method, tc.target, got, tc.models)
		}
	}
	if IsNativeRequest(nil) || IsModelsRequest(nil) {
		t.Fatal("nil 请求应返回 false")
	}
}

func TestWAIsMessagesPostRequest(t *testing.T) {
	request := httptest.NewRequest("POST", "/other", nil)
	if !IsMessagesPostRequest(request, "v1/messages?x=1") {
		t.Fatal("显式 messages 目标路径应命中")
	}
	if IsMessagesPostRequest(request, "/v1/other") {
		t.Fatal("非 messages 目标路径不应命中")
	}
	if IsMessagesPostRequest(request, "") {
		t.Fatal("原始非 messages 路径不应命中")
	}
	getRequest := httptest.NewRequest("GET", "/v1/messages", nil)
	if IsMessagesPostRequest(getRequest, "") {
		t.Fatal("GET 请求不应命中")
	}
	if IsMessagesPostRequest(nil, "/v1/messages") {
		t.Fatal("nil 请求不应命中")
	}
}

func TestWAEndpointModeForRequestShape(t *testing.T) {
	cases := []struct {
		endpoint string
		stream   bool
		want     string
	}{
		{"/v1/messages", false, EndpointModeMessagesJSON},
		{"/v1/messages", true, EndpointModeMessagesSSE},
		{"/V1/MESSAGES", true, EndpointModeMessagesSSE},
		{"/v1/messages/count_tokens", false, EndpointModeMessageTokenCounting},
		{"/v1/models", false, ""},
		{"unknown", false, ""},
	}
	for _, tc := range cases {
		if got := EndpointModeForRequestShape(tc.endpoint, tc.stream); got != tc.want {
			t.Fatalf("EndpointModeForRequestShape(%q,%v) = %q，期望 %q", tc.endpoint, tc.stream, got, tc.want)
		}
	}
}

func TestWABuildUpstreamURL(t *testing.T) {
	cases := []struct {
		name, base, path, want string
		wantErr                bool
	}{
		{"base 自动补 /v1", "https://api.anthropic.com", "/v1/messages?beta=true", "https://api.anthropic.com/v1/messages?beta=true", false},
		{"base 已含 /v1 去重", "https://api.anthropic.com/v1/", "/messages", "https://api.anthropic.com/v1/messages", false},
		{"空 base 报错", "   ", "/messages", "", true},
		{"根路径只保留 query", "https://api.anthropic.com/v1", "/?a=1", "https://api.anthropic.com/v1?a=1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildUpstreamURL(tc.base, tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，得到 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildUpstreamURL(%q,%q) error: %v", tc.base, tc.path, err)
			}
			if got != tc.want {
				t.Fatalf("BuildUpstreamURL(%q,%q) = %q，期望 %q", tc.base, tc.path, got, tc.want)
			}
		})
	}
}

func TestWABuildUpstreamURLsForAccount(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/messages?beta=true", nil)
	t.Run("api_key 账户", func(t *testing.T) {
		urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "api_key", BaseURL: "https://up.example"}, request)
		if len(urls) != 1 || !strings.HasSuffix(urls[0], "/v1/messages?beta=true") {
			t.Fatalf("api_key URL = %v", urls)
		}
		// Claude Code 兼容会在 messages 上追加 beta=true（该请求已带）。
	})
	t.Run("oauth 账户画像头触发补 beta", func(t *testing.T) {
		plain := httptest.NewRequest("POST", "/v1/messages", nil)
		plain.Header.Set(GatewayClientProfileHeader, "claude_code")
		urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "oauth", BaseURL: "https://up.example"}, plain)
		if len(urls) != 1 || !strings.HasSuffix(urls[0], "/messages?beta=true") {
			t.Fatalf("oauth URL = %v，期望补 beta=true", urls)
		}
	})
	t.Run("无兼容信号不补 beta", func(t *testing.T) {
		plain := httptest.NewRequest("POST", "/v1/messages", nil)
		urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "oauth", BaseURL: "https://up.example"}, plain)
		if len(urls) != 1 || strings.Contains(urls[0], "beta=") {
			t.Fatalf("无兼容信号 URL = %v，不应追加 beta", urls)
		}
	})
	t.Run("非支持账户类型", func(t *testing.T) {
		if urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "gemini_key", BaseURL: "https://up.example"}, request); urls != nil {
			t.Fatalf("非 anthropic 账户类型应返回 nil: %v", urls)
		}
	})
	t.Run("非原生请求", func(t *testing.T) {
		other := httptest.NewRequest("GET", "/v1/other", nil)
		if urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "api_key", BaseURL: "https://up.example"}, other); urls != nil {
			t.Fatalf("非原生请求应返回 nil: %v", urls)
		}
	})
	t.Run("非法 base URL", func(t *testing.T) {
		if urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "api_key", BaseURL: "  "}, request); urls != nil {
			t.Fatalf("空 base URL 应返回 nil: %v", urls)
		}
	})
}

func TestWABuildModelsResponse(t *testing.T) {
	t.Run("releaseDate 优先", func(t *testing.T) {
		response, err := BuildModelsResponse([]ModelCatalogItem{{Model: "claude-x", ReleaseDate: "2025-03-01"}})
		if err != nil {
			t.Fatalf("BuildModelsResponse error: %v", err)
		}
		if len(response.Data) != 1 {
			t.Fatalf("Data 长度 = %d", len(response.Data))
		}
		item := response.Data[0]
		if item.Type != "model" || item.ID != "claude-x" || item.DisplayName != "claude-x" {
			t.Fatalf("模型项 = %+v", item)
		}
		if item.CreatedAt != "2025-03-01T00:00:00.000Z" {
			t.Fatalf("CreatedAt = %q", item.CreatedAt)
		}
		if response.FirstID == nil || *response.FirstID != "claude-x" || response.LastID == nil || *response.LastID != "claude-x" {
			t.Fatalf("FirstID/LastID = %v/%v", response.FirstID, response.LastID)
		}
	})
	t.Run("createdAt 规范化", func(t *testing.T) {
		response, err := BuildModelsResponse([]ModelCatalogItem{
			{Model: "m1", CreatedAt: "2025-01-01T00:00:00+08:00"},
			{Model: "m2"},
		})
		if err != nil {
			t.Fatalf("BuildModelsResponse error: %v", err)
		}
		if response.Data[0].CreatedAt != "2024-12-31T16:00:00.000Z" {
			t.Fatalf("createdAt 规范化 = %q", response.Data[0].CreatedAt)
		}
		if response.Data[1].CreatedAt != "" {
			t.Fatalf("空目录时间应为空: %q", response.Data[1].CreatedAt)
		}
		if response.FirstID == nil || *response.FirstID != "m1" || response.LastID == nil || *response.LastID != "m2" {
			t.Fatalf("FirstID/LastID = %v/%v", response.FirstID, response.LastID)
		}
	})
	t.Run("空目录", func(t *testing.T) {
		response, err := BuildModelsResponse(nil)
		if err != nil {
			t.Fatalf("BuildModelsResponse error: %v", err)
		}
		if len(response.Data) != 0 || response.FirstID != nil || response.LastID != nil {
			t.Fatalf("空目录响应 = %+v", response)
		}
	})
	t.Run("非法 releaseDate", func(t *testing.T) {
		if _, err := BuildModelsResponse([]ModelCatalogItem{{Model: "m", ReleaseDate: "2025/03/01"}}); err == nil {
			t.Fatal("非法 releaseDate 应报错")
		}
	})
	t.Run("非法 createdAt", func(t *testing.T) {
		if _, err := BuildModelsResponse([]ModelCatalogItem{{Model: "m", CreatedAt: "2025-03-01 10:00:00"}}); err == nil {
			t.Fatal("缺 offset 的 createdAt 应报错")
		}
	})
}

func TestWAWithQueryParamIfMissing(t *testing.T) {
	if got := withQueryParamIfMissing("/v1/messages", "beta", "true"); got != "/v1/messages?beta=true" {
		t.Fatalf("补参 = %q", got)
	}
	if got := withQueryParamIfMissing("/v1/messages?beta=true&a=1", "beta", "true"); got != "/v1/messages?a=1&beta=true" {
		t.Fatalf("已有参数不重复且排序输出 = %q", got)
	}
	if got := withQueryParamIfMissing("", "beta", "true"); got != "/?beta=true" {
		t.Fatalf("空路径补斜杠 = %q", got)
	}
	// 非法 query 解析失败时重置再补参。
	if got := withQueryParamIfMissing("/v1/messages?%zz=1", "beta", "true"); got != "/v1/messages?beta=true" {
		t.Fatalf("非法 query 应回退 = %q", got)
	}
}

// SSE 原始行拆分契约：兼容 \r\n、\r、\n 三种换行。
func TestWASplitSSELines(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		lines []string
	}{
		{"LF", "a\nb", []string{"a", "b"}},
		{"CRLF", "a\r\nb", []string{"a", "b"}},
		{"CR", "a\rb", []string{"a", "b"}},
		{"结尾换行", "a\n", []string{"a", ""}},
		{"无换行", "a", []string{"a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitSSELines(tc.raw)
			if !stringSliceEqual(got, tc.lines) {
				t.Fatalf("splitSSELines(%q) = %q，期望 %q", tc.raw, got, tc.lines)
			}
		})
	}
}

func stringSliceEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
