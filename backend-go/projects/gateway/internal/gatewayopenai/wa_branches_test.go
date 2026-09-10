package gatewayopenai

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// Driver EndpointModeForRequestShape 门面。
func TestWAOpenAIDriverEndpointMode(t *testing.T) {
	driver := NewDriver()
	cases := []struct {
		shape     gatewayproto.RequestShape
		want      gatewayproto.EndpointMode
		wantFound bool
	}{
		{gatewayproto.RequestShape{Path: "/v1/chat/completions", Stream: true}, gatewayproto.EndpointModeChatSSE, true},
		{gatewayproto.RequestShape{Path: "/v1/responses", Stream: false}, gatewayproto.EndpointModeResponsesJSON, true},
		{gatewayproto.RequestShape{Path: "/v1/embeddings"}, "", false},
	}
	for index, tc := range cases {
		got, found := driver.EndpointModeForRequestShape(tc.shape)
		if found != tc.wantFound || got != tc.want {
			t.Fatalf("用例 %d = %q/%v，期望 %q/%v", index, got, found, tc.want, tc.wantFound)
		}
	}
}

// textFromOpenAITextValue：数组内容与非字符串条目。
func TestWATextFromOpenAITextValue(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"数组拼接", `{"content":[{"text":"a"},{"extra":1},{"text":"b"}]}`, "ab"},
		{"空文本条目跳过", `{"content":[{"text":""},{"text":"x"}]}`, "x"},
		{"字符串直返", `{"content":"s"}`, "s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := mustParseJSON(t, tc.text)
			if got := textFromOpenAITextValue(value["content"]); got != tc.want {
				t.Fatalf("textFromOpenAITextValue = %q，期望 %q", got, tc.want)
			}
		})
	}
	if got := textFromOpenAITextValue(""); got != "" {
		t.Fatalf("空字符串 = %q", got)
	}
}

// errorRawPaths：无嵌套 error 对象但带 code/message 时回退 ["error"]。
func TestWAErrorRawPathsFallback(t *testing.T) {
	event := ParseStreamEventData(`{"type":"error","code":"c1","message":"m1"}`, "", "", 0)
	frames := ExtractSseSemanticFrames(event, gatewayproto.EndpointFamilyChatCompletions)
	if frames[0].FrameType != gatewayproto.FrameTypeError {
		t.Fatalf("首帧 = %+v", frames[0])
	}
	if !waOSliceEqual(frames[0].RawJSONPaths, []string{"error"}) {
		t.Fatalf("回退路径 = %v", frames[0].RawJSONPaths)
	}
	// response.error 嵌套路径。
	nested := ParseStreamEventData(`{"response":{"error":{"code":"c2"}}}`, "", "", 0)
	frames = ExtractSseSemanticFrames(nested, gatewayproto.EndpointFamilyResponses)
	if !waOSliceEqual(frames[0].RawJSONPaths, []string{"response.error"}) {
		t.Fatalf("嵌套路径 = %v", frames[0].RawJSONPaths)
	}
}

// inspectRawEventBuffer 拦截/丢弃路径（经 FlushPendingOnEOF 驱动）。
func TestWABufferFlushInterceptAndDiscard(t *testing.T) {
	t.Run("EOF 事件被拦截", func(t *testing.T) {
		buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
			Policies: []InspectionPolicy{func(event ParsedStreamEvent, _ []gatewayproto.SemanticFrame) *InspectionDecision {
				if event.Data != nil {
					if _, has := event.Data["error"]; has {
						return &InspectionDecision{Action: DecisionIntercept, ErrorCode: "eof_blocked"}
					}
				}
				return nil
			}},
		})
		buffer.PushChunk([]byte("data: {\"error\":{\"code\":\"x\"}}"))
		result := buffer.FlushPendingOnEOF()
		if result.Intercepted == nil || result.Intercepted.ErrorCode != "eof_blocked" {
			t.Fatalf("EOF 拦截 = %+v", result.Intercepted)
		}
		if len(result.Chunks) != 1 || !strings.Contains(string(result.Chunks[0]), "response.failed") {
			t.Fatalf("EOF 拦截应注入失败事件: %+v", result.Chunks)
		}
	})
	t.Run("EOF 事件被丢弃", func(t *testing.T) {
		buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
			Policies: []InspectionPolicy{func(event ParsedStreamEvent, _ []gatewayproto.SemanticFrame) *InspectionDecision {
				if event.Data != nil {
					if _, has := event.Data["error"]; has {
						return &InspectionDecision{Action: DecisionDiscardEvent}
					}
				}
				return nil
			}},
		})
		buffer.PushChunk([]byte("data: {\"error\":{\"code\":\"x\"}}\n\n"))
		result := buffer.FlushPendingOnEOF()
		if result.Intercepted != nil || len(result.Chunks) != 0 {
			t.Fatalf("EOF 丢弃 = %+v", result)
		}
	})
	t.Run("EOF 延迟 noop 清空", func(t *testing.T) {
		buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{ClientRetryEnabled: true})
		buffer.PushChunk([]byte("data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"a\"}}]}"))
		result := buffer.FlushPendingOnEOF()
		if result.Intercepted != nil || len(result.Chunks) != 0 {
			t.Fatalf("EOF noop 延迟 = %+v", result)
		}
	})
}

// 多块累积后 compactConsumedChunks 触发（headIndex 超过 64 且过半）。
func TestWAPendingBufferCompaction(t *testing.T) {
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{ClientRetryEnabled: true})
	event := "data: {\"n\":1}\n\n"
	for index := 0; index < 70; index++ {
		if result := buffer.PushChunk([]byte(event)); len(result.Chunks) != 1 {
			t.Fatalf("第 %d 个事件应透传: %+v", index, result.Chunks)
		}
	}
}

// 逐字节喂入：覆盖跨 chunk 边界检测的尾部追踪（trailingBytes /
// findCrossChunkBoundaryEnd 的小 chunk 分支）。
func TestWABufferByteWiseBoundary(t *testing.T) {
	calls := 0
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies: []InspectionPolicy{func(event ParsedStreamEvent, _ []gatewayproto.SemanticFrame) *InspectionDecision {
			calls++
			return nil
		}},
	})
	payload := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"
	var forwarded []byte
	for index := 0; index < len(payload); index++ {
		result := buffer.PushChunk([]byte(payload[index : index+1]))
		for _, chunk := range result.Chunks {
			forwarded = append(forwarded, chunk...)
		}
	}
	if calls != 1 {
		t.Fatalf("逐字节喂入应切出 1 个事件: %d", calls)
	}
	if string(forwarded) != payload {
		t.Fatalf("透传内容应完整: %q", forwarded)
	}
}

// text 选择字段的事件也属于「仅可见输出」直通范围（快路径关闭时）。
func TestWABufferChatTextChoicePassThrough(t *testing.T) {
	calls := 0
	buffer := NewResponseInspectionBuffer(ResponseInspectionBufferOptions{
		Policies:                            []InspectionPolicy{countingPolicy(&calls)},
		RequiresVisibleOutputTextInspection: []bool{false},
	})
	result := buffer.PushChunk([]byte("data: {\"choices\":[{\"text\":\"plain\"}]}\n\n"))
	if calls != 0 {
		t.Fatalf("text 选择事件应直通: %d", calls)
	}
	if len(result.Chunks) != 1 {
		t.Fatalf("直通输出: %+v", result.Chunks)
	}
}
