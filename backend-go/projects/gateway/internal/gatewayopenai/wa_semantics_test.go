package gatewayopenai

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

func waOParse(t *testing.T, text string) map[string]any {
	return mustParseJSON(t, text)
}

func waOSliceEqual(got, want []string) bool {
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

// JSON 语义帧：chat choices（含 reasoning_content）、responses（output_text /
// output[].content[].text / status）。
func TestWAExtractJSONSemanticFrames(t *testing.T) {
	t.Run("非对象输入", func(t *testing.T) {
		if frames := ExtractJSONSemanticFrames("x", gatewayproto.EndpointFamilyChatCompletions); frames != nil {
			t.Fatalf("非对象应 nil: %+v", frames)
		}
	})
	t.Run("chat choices", func(t *testing.T) {
		root := waOParse(t, `{
			"choices":[
				{"message":{"content":"答案","reasoning_content":"推理"},"finish_reason":"stop"},
				{"message":{"content":""},"finish_reason":"length"},
				"bad-row"
			]
		}`)
		frames := ExtractJSONSemanticFrames(root, gatewayproto.EndpointFamilyChatCompletions)
		var content, reasoning, completedLength *gatewayproto.SemanticFrame
		completedCount := 0
		for index := range frames {
			frame := &frames[index]
			switch {
			case frame.FrameType == gatewayproto.FrameTypeOutputTextDone && frame.Text == "答案":
				content = frame
			case frame.FrameType == gatewayproto.FrameTypeOutputTextDone && frame.Text == "推理":
				reasoning = frame
			case frame.FrameType == gatewayproto.FrameTypeCompleted:
				completedCount++
				if frame.FinishReason == "length" {
					completedLength = frame
				}
			}
		}
		if content == nil || content.ChoiceIndex != 0 || !content.VisibleOutput {
			t.Fatalf("content 帧 = %+v", content)
		}
		if !waOSliceEqual(content.RawJSONPaths, []string{"choices.0.message.content"}) {
			t.Fatalf("content 路径 = %v", content.RawJSONPaths)
		}
		if reasoning == nil || !waOSliceEqual(reasoning.RawJSONPaths, []string{"choices.0.message.reasoning_content"}) {
			t.Fatalf("reasoning 帧 = %+v", reasoning)
		}
		if completedCount != 2 || completedLength == nil {
			t.Fatalf("completed 帧 = %+v", completedLength)
		}
	})
	t.Run("error 与 response.error 双帧", func(t *testing.T) {
		root := waOParse(t, `{"error":{"code":"e1","message":"m1"},"response":{"error":{"code":"e2","message":"m2"}}}`)
		frames := ExtractJSONSemanticFrames(root, gatewayproto.EndpointFamilyResponses)
		if len(frames) < 2 || frames[0].FrameType != gatewayproto.FrameTypeError || frames[1].FrameType != gatewayproto.FrameTypeError {
			t.Fatalf("错误帧 = %+v", frames)
		}
		if !waOSliceEqual(frames[0].RawJSONPaths, []string{"error"}) || !waOSliceEqual(frames[1].RawJSONPaths, []string{"response.error"}) {
			t.Fatalf("错误帧路径 = %v/%v", frames[0].RawJSONPaths, frames[1].RawJSONPaths)
		}
	})
	t.Run("responses 文本与状态", func(t *testing.T) {
		root := waOParse(t, `{
			"status":"completed",
			"output_text":"顶层文本",
			"output":[
				{"content":[{"text":"条目文本"},{"no_text":1}]},
				"bad"
			],
			"usage":{"input_tokens":2,"output_tokens":3}
		}`)
		frames := ExtractJSONSemanticFrames(root, gatewayproto.EndpointFamilyResponses)
		var topLevel, itemText, completed, usage *gatewayproto.SemanticFrame
		for index := range frames {
			frame := &frames[index]
			switch {
			case frame.FrameType == gatewayproto.FrameTypeOutputTextDone && frame.Text == "顶层文本":
				topLevel = frame
			case frame.FrameType == gatewayproto.FrameTypeOutputTextDone && frame.Text == "条目文本":
				itemText = frame
			case frame.FrameType == gatewayproto.FrameTypeCompleted:
				completed = frame
			case frame.FrameType == gatewayproto.FrameTypeUsage:
				usage = frame
			}
		}
		if topLevel == nil || !waOSliceEqual(topLevel.RawJSONPaths, []string{"output_text"}) || topLevel.FinishReason != "completed" {
			t.Fatalf("顶层文本帧 = %+v", topLevel)
		}
		if itemText == nil || itemText.OutputIndex != 0 || itemText.ContentIndex != 0 {
			t.Fatalf("条目文本帧 = %+v", itemText)
		}
		if completed == nil || completed.FinishReason != "completed" {
			t.Fatalf("completed 帧 = %+v", completed)
		}
		if usage == nil || usage.RawJSONPaths == nil {
			t.Fatalf("usage 帧 = %+v", usage)
		}
	})
	t.Run("未知 family 同时提取两族", func(t *testing.T) {
		root := waOParse(t, `{"choices":[{"message":{"content":"c"},"finish_reason":"stop"}],"status":"completed","output":[]}`)
		frames := ExtractJSONSemanticFrames(root, gatewayproto.EndpointFamilyUnknown)
		chatDone, responsesCompleted := false, false
		for _, frame := range frames {
			if frame.FrameType == gatewayproto.FrameTypeOutputTextDone && frame.Text == "c" {
				chatDone = true
			}
			if frame.FrameType == gatewayproto.FrameTypeCompleted && frame.EndpointFamily == gatewayproto.EndpointFamilyResponses {
				responsesCompleted = true
			}
		}
		if !chatDone || !responsesCompleted {
			t.Fatalf("未知 family 应同时提取: %+v", frames)
		}
	})
	t.Run("usage 帧", func(t *testing.T) {
		root := waOParse(t, `{"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
		frames := ExtractJSONSemanticFrames(root, gatewayproto.EndpointFamilyChatCompletions)
		found := false
		for _, frame := range frames {
			if frame.FrameType == gatewayproto.FrameTypeUsage {
				found = true
				if !waOSliceEqual(frame.RawJSONPaths, []string{"usage"}) {
					t.Fatalf("usage 路径 = %v", frame.RawJSONPaths)
				}
			}
		}
		if !found {
			t.Fatal("usage 帧缺失")
		}
	})
}

// SSE 语义帧：chat delta、responses 事件族、[DONE]、usage 路径选择。
func TestWAExtractSseSemanticFrames(t *testing.T) {
	t.Run("chat delta 三字段", func(t *testing.T) {
		event := ParseStreamEventData(`{"choices":[{"delta":{"content":"a","reasoning_content":"b","refusal":"c"}}]}`, "", "", 0)
		frames := ExtractSseSemanticFrames(event, gatewayproto.EndpointFamilyChatCompletions)
		texts := map[string]string{}
		for _, frame := range frames {
			if frame.FrameType == gatewayproto.FrameTypeOutputTextDelta {
				texts[frame.RawJSONPaths[0]] = frame.Text
			}
		}
		if texts["choices.0.delta.content"] != "a" || texts["choices.0.delta.reasoning_content"] != "b" || texts["choices.0.delta.refusal"] != "c" {
			t.Fatalf("delta 帧 = %+v", frames)
		}
	})
	t.Run("chat finish_reason", func(t *testing.T) {
		event := ParseStreamEventData(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "", "", 0)
		frames := ExtractSseSemanticFrames(event, gatewayproto.EndpointFamilyChatCompletions)
		var completed *gatewayproto.SemanticFrame
		for index := range frames {
			if frames[index].FrameType == gatewayproto.FrameTypeCompleted {
				completed = &frames[index]
			}
		}
		if completed == nil || completed.FinishReason != "stop" || completed.ChoiceIndex != 0 {
			t.Fatalf("completed 帧 = %+v", completed)
		}
	})
	t.Run("responses 文本 delta 与 done", func(t *testing.T) {
		delta := ExtractSseSemanticFrames(ParseStreamEventData(`{"type":"response.output_text.delta","delta":"你"}`, "", "", 0), gatewayproto.EndpointFamilyResponses)
		if len(delta) == 0 || delta[0].FrameType != gatewayproto.FrameTypeOutputTextDelta || delta[0].Text != "你" {
			t.Fatalf("delta 帧 = %+v", delta)
		}
		done := ExtractSseSemanticFrames(ParseStreamEventData(`{"type":"response.output_text.done","text":"完毕"}`, "", "", 0), gatewayproto.EndpointFamilyResponses)
		if len(done) == 0 || done[0].FrameType != gatewayproto.FrameTypeOutputTextDone || done[0].Text != "完毕" {
			t.Fatalf("done 帧 = %+v", done)
		}
	})
	t.Run("response.completed 内嵌 responses 帧", func(t *testing.T) {
		event := ParseStreamEventData(`{"type":"response.completed","response":{"status":"completed","output":[{"content":[{"text":"完整"}]}]}}`, "", "", 0)
		frames := ExtractSseSemanticFrames(event, gatewayproto.EndpointFamilyResponses)
		textFound, completedFound := false, false
		for _, frame := range frames {
			if frame.FrameType == gatewayproto.FrameTypeOutputTextDone && frame.Text == "完整" && frame.Transport == gatewayproto.TransportSSE {
				textFound = true
			}
			if frame.FrameType == gatewayproto.FrameTypeCompleted && frame.FinishReason == "completed" {
				completedFound = true
			}
		}
		if !textFound || !completedFound {
			t.Fatalf("completed 帧 = %+v", frames)
		}
	})
	t.Run("response.done 缺省状态回退事件名", func(t *testing.T) {
		event := ParseStreamEventData(`{"type":"response.done"}`, "", "", 0)
		frames := ExtractSseSemanticFrames(event, gatewayproto.EndpointFamilyResponses)
		var completed *gatewayproto.SemanticFrame
		for index := range frames {
			if frames[index].FrameType == gatewayproto.FrameTypeCompleted {
				completed = &frames[index]
			}
		}
		if completed == nil || completed.FinishReason != "done" {
			t.Fatalf("response.done completed = %+v", completed)
		}
	})
	t.Run("response.failed 缺省状态 failed", func(t *testing.T) {
		event := ParseStreamEventData(`{"type":"response.failed"}`, "", "", 0)
		frames := ExtractSseSemanticFrames(event, gatewayproto.EndpointFamilyResponses)
		failed := false
		for _, frame := range frames {
			if frame.FrameType == gatewayproto.FrameTypeCompleted && frame.FinishReason == "failed" {
				failed = true
			}
		}
		if !failed {
			t.Fatalf("response.failed 帧 = %+v", frames)
		}
	})
	t.Run("[DONE] 与空 data", func(t *testing.T) {
		done := ExtractSseSemanticFrames(ParsedStreamEvent{EventType: "[DONE]"}, gatewayproto.EndpointFamilyChatCompletions)
		if len(done) != 1 || done[0].FrameType != gatewayproto.FrameTypeCompleted || done[0].FinishReason != "[DONE]" {
			t.Fatalf("[DONE] 帧 = %+v", done)
		}
		if frames := ExtractSseSemanticFrames(ParsedStreamEvent{}, gatewayproto.EndpointFamilyResponses); len(frames) != 0 {
			t.Fatalf("空事件应无帧: %+v", frames)
		}
	})
	t.Run("error 事件", func(t *testing.T) {
		event := ParseStreamEventData(`{"error":{"code":"rate_limit","type":"rate_limit_error","message":"慢"}}`, "", "", 0)
		frames := ExtractSseSemanticFrames(event, gatewayproto.EndpointFamilyChatCompletions)
		if frames[0].FrameType != gatewayproto.FrameTypeError || frames[0].ErrorCode != "rate_limit" {
			t.Fatalf("错误帧 = %+v", frames[0])
		}
	})
	t.Run("usage 路径选择", func(t *testing.T) {
		// data.usage 优先。
		event := ParseStreamEventData(`{"usage":{"input_tokens":1},"response":{"usage":{"input_tokens":99}}}`, "", "", 0)
		frames := ExtractSseSemanticFrames(event, gatewayproto.EndpointFamilyChatCompletions)
		usagePath := ""
		for _, frame := range frames {
			if frame.FrameType == gatewayproto.FrameTypeUsage {
				usagePath = frame.RawJSONPaths[0]
				if *frame.Usage.InputTokens != 1 {
					t.Fatalf("usage 应取顶层: %+v", frame.Usage)
				}
			}
		}
		if usagePath != "usage" {
			t.Fatalf("usage 路径 = %q", usagePath)
		}
		// 仅 response.usage。
		event = ParseStreamEventData(`{"response":{"usage":{"input_tokens":5}}}`, "", "", 0)
		frames = ExtractSseSemanticFrames(event, gatewayproto.EndpointFamilyResponses)
		usagePath = ""
		for _, frame := range frames {
			if frame.FrameType == gatewayproto.FrameTypeUsage {
				usagePath = frame.RawJSONPaths[0]
			}
		}
		if usagePath != "response.usage" {
			t.Fatalf("response.usage 路径 = %q", usagePath)
		}
	})
	t.Run("mcp 失败剥离 error 根", func(t *testing.T) {
		event := ParseStreamEventData(`{"type":"response.mcp_call.failed","error":{"code":"x"},"other":1}`, "", "", 0)
		frames := ExtractSseSemanticFrames(event, gatewayproto.EndpointFamilyResponses)
		// rawJSONFrame 末帧的 RawJSON 应不含 error 键。
		raw := frames[len(frames)-1].RawJSON.(map[string]any)
		if _, has := raw["error"]; has {
			t.Fatalf("mcp 失败应剥离 error: %+v", raw)
		}
		if _, has := raw["other"]; !has {
			t.Fatalf("其余键保留: %+v", raw)
		}
	})
	t.Run("未知 family 双提取", func(t *testing.T) {
		event := ParseStreamEventData(`{"choices":[{"delta":{"content":"x"}}]}`, "", "", 0)
		frames := ExtractSseSemanticFrames(event, gatewayproto.EndpointFamilyUnknown)
		chatFound := false
		for _, frame := range frames {
			// 未知 family 下 chat 帧以 unknown 族标记输出（族透传）。
			if frame.FrameType == gatewayproto.FrameTypeOutputTextDelta && frame.Text == "x" && frame.EndpointFamily == gatewayproto.EndpointFamilyUnknown {
				chatFound = true
			}
		}
		if !chatFound {
			t.Fatalf("未知 family 应提取 chat 帧: %+v", frames)
		}
	})
}

// 工具调用 / 结构化输出证据检测。
func TestWAJSONToolCallDetected(t *testing.T) {
	t.Run("chat message tool_calls", func(t *testing.T) {
		if !JSONToolCallDetected(waOParse(t, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"f"}}]}}]}`)) {
			t.Fatal("message.tool_calls 应检出")
		}
		if !JSONToolCallDetected(waOParse(t, `{"choices":[{"tool_calls":[{"function":{"name":"f"}}]}]}`)) {
			t.Fatal("行级 tool_calls 应检出")
		}
		// 行级 tool_calls 只有 id 元数据时不检出（hasMeaningfulDelta 跳过元数据键）。
		if JSONToolCallDetected(waOParse(t, `{"choices":[{"tool_calls":[{"id":"x"}]}]}`)) {
			t.Fatal("仅 id 元数据不检出")
		}
	})
	t.Run("responses 可调用输出类型", func(t *testing.T) {
		if !JSONToolCallDetected(waOParse(t, `{"output":[{"type":"function_call"}]}`)) {
			t.Fatal("function_call 应检出")
		}
		for _, callType := range []string{"custom_tool_call", "computer_call", "web_search_call", "file_search_call", "mcp_call", "code_interpreter_call", "image_generation_call"} {
			if !JSONToolCallDetected(waOParse(t, `{"output":[{"type":"`+callType+`"}]}`)) {
				t.Fatalf("类型 %s 应检出", callType)
			}
		}
		if JSONToolCallDetected(waOParse(t, `{"output":[{"type":"message"}]}`)) {
			t.Fatal("message 类型不检")
		}
	})
	t.Run("非对象与空结构", func(t *testing.T) {
		if JSONToolCallDetected("x") || JSONToolCallDetected(map[string]any{}) {
			t.Fatal("无证据应 false")
		}
	})
}

// 流事件判定谓词（inspector 分类的基础）。
func TestWAStreamEventPredicates(t *testing.T) {
	t.Run("IsStreamFailureEvent", func(t *testing.T) {
		if !IsStreamFailureEvent(ParsedStreamEvent{EventType: "response.failed"}) {
			t.Fatal("response.failed 应失败")
		}
		if !IsStreamFailureEvent(ParsedStreamEvent{EventName: "error"}) {
			t.Fatal("error 事件名应失败")
		}
		if !IsStreamFailureEvent(ParseStreamEventData(`{"error":{"code":"x"}}`, "", "", 0)) {
			t.Fatal("error 负载应失败")
		}
		if IsStreamFailureEvent(ParseStreamEventData(`{"type":"response.mcp_call.failed","error":{}}`, "", "", 0)) {
			t.Fatal("mcp 失败不算流失败")
		}
		if IsStreamFailureEvent(ParsedStreamEvent{EventType: "response.output_text.delta"}) {
			t.Fatal("普通事件不失败")
		}
	})
	t.Run("IsImageStreamEventType", func(t *testing.T) {
		for _, eventType := range []string{"response.image_generation_call.partial", "image_generation.partial_image", "image_generation.completed", "image_generation.failed"} {
			if !IsImageStreamEventType(eventType) {
				t.Fatalf("事件 %s 应为图片流类型", eventType)
			}
		}
		if IsImageStreamEventType("response.output_text.delta") {
			t.Fatal("文本 delta 不是图片流")
		}
	})
	t.Run("streamEventHasImageOutput", func(t *testing.T) {
		data := waOParse(t, `{"item":{"type":"image_generation_call"}}`)
		if !streamEventHasImageOutput(data, "other") {
			t.Fatal("item 图片类型应检出")
		}
		data = waOParse(t, `{"response":{"output":[{"type":"image_generation_call"}]}}`)
		if !streamEventHasImageOutput(data, "other") {
			t.Fatal("response.output 图片类型应检出")
		}
		if streamEventHasImageOutput(waOParse(t, `{"response":{"output":[{"type":"message"}]}}`), "other") {
			t.Fatal("非图片输出不检出")
		}
	})
	t.Run("IsStreamVisibleOutputEvent", func(t *testing.T) {
		if !IsStreamVisibleOutputEvent(ParseStreamEventData(`{"choices":[{"delta":{"content":"a"}}]}`, "", "", 0)) {
			t.Fatal("chat delta 应可见")
		}
		if !IsStreamVisibleOutputEvent(ParseStreamEventData(`{"choices":[{"text":"plain"}]}`, "", "", 0)) {
			t.Fatal("completion text 应可见")
		}
		if IsStreamVisibleOutputEvent(ParseStreamEventData(`{"choices":[{"delta":{}}]}`, "", "", 0)) {
			t.Fatal("空 delta 不可见")
		}
		if !IsStreamVisibleOutputEvent(ParseStreamEventData(`{"delta":"x"}`, "response.output_text.delta", "", 0)) {
			t.Fatal("responses delta 应可见")
		}
		if !IsStreamVisibleOutputEvent(ParseStreamEventData(`{"item":{"type":"function_call"}}`, "response.output_item.added", "", 0)) {
			t.Fatal("output_item callable 应可见")
		}
		// completed 事件：无 callable 输出且无可估文本时不可见。
		if IsStreamVisibleOutputEvent(ParseStreamEventData(`{"response":{"output":[{"type":"message"}]}}`, "response.completed", "", 0)) {
			t.Fatal("无 callable/文本的 completed 不应可见")
		}
		if IsStreamVisibleOutputEvent(ParsedStreamEvent{}) {
			t.Fatal("无 data 不可见")
		}
	})
	t.Run("ToolCallDetected", func(t *testing.T) {
		if !ToolCallDetected(ParseStreamEventData(`{"type":"response.output_text.delta","delta":{"tool_calls":[{"function":{"name":"f"}}]}}`, "", "", 0)) {
			t.Fatal("delta.tool_calls 应检出")
		}
		if !ToolCallDetected(ParseStreamEventData(`{"item":{"type":"code_interpreter_call"}}`, "", "", 0)) {
			t.Fatal("item callable 应检出")
		}
		if !ToolCallDetected(ParseStreamEventData(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"f"}}]}}]}`, "", "", 0)) {
			t.Fatal("choices delta tool_calls 应检出")
		}
		if !ToolCallDetected(ParseStreamEventData(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"f"}}]}}]}`, "", "", 0)) {
			t.Fatal("choices message tool_calls 应检出")
		}
		if ToolCallDetected(ParsedStreamEvent{}) {
			t.Fatal("无 data 不检出")
		}
		if ToolCallDetected(ParseStreamEventData(`{"delta":{"index":1}}`, "response.output_text.delta", "", 0)) {
			t.Fatal("只有 index 元数据不检出")
		}
	})
	t.Run("hasMeaningfulDelta", func(t *testing.T) {
		if hasMeaningfulDelta("") || hasMeaningfulDelta(nil) || hasMeaningfulDelta([]any{}) || hasMeaningfulDelta(map[string]any{"index": 3, "id": "x", "type": "y"}) {
			t.Fatal("空值与纯元数据无意义")
		}
		if !hasMeaningfulDelta(map[string]any{"text": "x"}) || !hasMeaningfulDelta([]any{"", "x"}) {
			t.Fatal("文本应有意义")
		}
		if !hasMeaningfulChoiceDelta(waOParse(t, `{"audio":{"data":"x"}}`)) {
			t.Fatal("audio delta 应有意义")
		}
	})
}

// ClassifyStreamEvent：终止/失败/可见输出/图片输出/usage/估算 token。
func TestWAClassifyStreamEvent(t *testing.T) {
	t.Run("文本 delta", func(t *testing.T) {
		event := ParseStreamEventData(`{"type":"response.output_text.delta","delta":"hello world"}`, "", "", 0)
		classification := ClassifyStreamEvent(event, 0)
		if classification.EventType != "response.output_text.delta" || classification.VisibleOutput != true {
			t.Fatalf("分类 = %+v", classification)
		}
		if classification.EstimatedOutputTokens <= 0 {
			t.Fatalf("估算 token = %d", classification.EstimatedOutputTokens)
		}
		if classification.Terminal || classification.Failed {
			t.Fatalf("delta 非终止/失败: %+v", classification)
		}
	})
	t.Run("chat usage 事件", func(t *testing.T) {
		event := ParseStreamEventData(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`, "", "", 0)
		classification := ClassifyStreamEvent(event, 0)
		if !classification.UsageFound {
			t.Fatalf("usage 应检出: %+v", classification)
		}
		waOAssertToken(t, classification.Usage.InputTokens, 4, "input")
	})
	t.Run("response.usage 优先级", func(t *testing.T) {
		event := ParseStreamEventData(`{"type":"response.completed","response":{"status":"completed","usage":{"output_tokens":6},"output":[]}}`, "", "", 0)
		classification := ClassifyStreamEvent(event, 0)
		if !classification.Terminal {
			t.Fatalf("completed 应终止: %+v", classification)
		}
		waOAssertToken(t, classification.Usage.OutputTokens, 6, "output")
	})
	t.Run("失败事件携带错误字段", func(t *testing.T) {
		event := ParseStreamEventData(`{"type":"response.failed","response":{"error":{"code":"e","message":"m"}}}`, "", "", 0)
		classification := ClassifyStreamEvent(event, 0)
		if !classification.Failed || classification.ErrorCode != "e" || classification.ErrorMessage != "m" {
			t.Fatalf("失败分类 = %+v", classification)
		}
	})
	t.Run("image_generation.completed", func(t *testing.T) {
		event := ParseStreamEventData(`{"type":"image_generation.completed","result":"ok"}`, "", "", 0)
		classification := ClassifyStreamEvent(event, 0)
		if !classification.Terminal || !classification.ImageOutput || !classification.VisibleOutput {
			t.Fatalf("图片完成分类 = %+v", classification)
		}
	})
	t.Run("估算只补一次", func(t *testing.T) {
		event := ParseStreamEventData(`{"type":"response.completed","response":{"status":"completed","output":[{"content":[{"text":"abcd"}]}]}}`, "", "", 0)
		first := ClassifyStreamEvent(event, 0)
		second := ClassifyStreamEvent(event, first.EstimatedOutputTokens)
		if first.EstimatedOutputTokens == 0 || second.EstimatedOutputTokens != 0 {
			t.Fatalf("估算只应发生一次: %d then %d", first.EstimatedOutputTokens, second.EstimatedOutputTokens)
		}
	})
	t.Run("chat 行级 text 与 delta 估算", func(t *testing.T) {
		event := ParseStreamEventData(`{"choices":[{"text":"abcd"},{"delta":{"content":"abcd"}}]}`, "", "", 0)
		classification := ClassifyStreamEvent(event, 0)
		if classification.EstimatedOutputTokens != 2 {
			t.Fatalf("chat 估算 = %d，期望 2", classification.EstimatedOutputTokens)
		}
	})
	t.Run("空事件分类", func(t *testing.T) {
		classification := ClassifyStreamEvent(ParsedStreamEvent{EventType: "[DONE]"}, 0)
		if !classification.Terminal || classification.VisibleOutput {
			t.Fatalf("[DONE] 分类 = %+v", classification)
		}
	})
}
