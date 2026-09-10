package gatewaygemini

import (
	"encoding/json"
	"testing"
)

func waParseGeminiJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("解析测试 JSON 失败: %v（原文 %q）", err, text)
	}
	return value
}

// 语义帧契约：interactions 与 generateContent 两族、JSON 与 SSE 两种传输。
func TestWAExtractJSONSemanticFrames(t *testing.T) {
	t.Run("非对象输入", func(t *testing.T) {
		if frames := ExtractJSONSemanticFrames("x", EndpointFamilyInteractions); frames != nil {
			t.Fatalf("非对象应返回 nil: %+v", frames)
		}
	})
	t.Run("error 根对象", func(t *testing.T) {
		root := waParseGeminiJSON(t, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"bad"}}`)
		frames := ExtractJSONSemanticFrames(root, EndpointFamilyGenerateContent)
		if frames[0].FrameType != FrameTypeError {
			t.Fatalf("首帧 = %+v", frames[0])
		}
		if frames[0].ErrorCode != "400" || frames[0].ErrorType != "INVALID_ARGUMENT" || frames[0].ErrorMessage != "bad" {
			t.Fatalf("错误帧 = %+v", frames[0])
		}
		if !waSliceEqual(frames[0].RawJSONPaths, []string{"error"}) {
			t.Fatalf("路径 = %v", frames[0].RawJSONPaths)
		}
	})
	t.Run("interactions steps 文本与状态", func(t *testing.T) {
		root := waParseGeminiJSON(t, `{
			"status":"completed",
			"steps":[
				{"type":"message","content":[{"text":"答案","other":1},{"no_text":true}]},
				{"type":"thought","content":[{"text":"思考"}]},
				"not-an-object"
			]
		}`)
		frames := ExtractJSONSemanticFrames(root, EndpointFamilyInteractions)
		var answer, thought, completed *ResponseSemanticFrame
		for index := range frames {
			frame := &frames[index]
			switch {
			case frame.FrameType == FrameTypeOutputTextDone && frame.Text == "答案":
				answer = frame
			case frame.FrameType == FrameTypeOutputTextDone && frame.Text == "思考":
				thought = frame
			case frame.FrameType == FrameTypeCompleted:
				completed = frame
			}
		}
		if answer == nil || answer.StepIndex == nil || *answer.StepIndex != 0 || answer.ContentIndex == nil || *answer.ContentIndex != 0 {
			t.Fatalf("答案帧 = %+v", answer)
		}
		if answer.VisibleOutput == nil || !*answer.VisibleOutput {
			t.Fatal("message step 文本应可见")
		}
		if !waSliceEqual(answer.RawJSONPaths, []string{"steps.0.content.0.text"}) {
			t.Fatalf("路径 = %v", answer.RawJSONPaths)
		}
		if thought == nil || thought.VisibleOutput == nil || *thought.VisibleOutput {
			t.Fatal("thought step 文本不可见")
		}
		if completed == nil || completed.FinishReason != "completed" {
			t.Fatalf("completed 帧 = %+v", completed)
		}
	})
	t.Run("无 status 不产生 completed", func(t *testing.T) {
		root := waParseGeminiJSON(t, `{"steps":[]}`)
		for _, frame := range ExtractJSONSemanticFrames(root, EndpointFamilyInteractions) {
			if frame.FrameType == FrameTypeCompleted {
				t.Fatalf("无 status 不应有 completed 帧: %+v", frame)
			}
		}
	})
	t.Run("generateContent 候选与 functionCall", func(t *testing.T) {
		root := waParseGeminiJSON(t, `{
			"candidates":[
				{
					"content":{"parts":[{"text":"你好"},{"functionCall":{"name":"f"}},{"thought":true,"text":"隐藏"}]},
					"finishReason":"STOP"
				}
			],
			"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":6}
		}`)
		frames := ExtractJSONSemanticFrames(root, EndpointFamilyGenerateContent)
		var text, toolCall, hidden, completed, usage *ResponseSemanticFrame
		for index := range frames {
			frame := &frames[index]
			switch {
			case frame.FrameType == FrameTypeOutputTextDone && frame.Text == "你好":
				text = frame
			case frame.FrameType == FrameTypeRawJSONPath && waHasPath(frame.RawJSONPaths, "candidates.0.content.parts.1"):
				toolCall = frame
			case frame.FrameType == FrameTypeOutputTextDone && frame.Text == "隐藏":
				hidden = frame
			case frame.FrameType == FrameTypeCompleted:
				completed = frame
			case frame.FrameType == FrameTypeUsage:
				usage = frame
			}
		}
		if text == nil || text.FrameType != FrameTypeOutputTextDone || text.ChoiceIndex == nil || *text.ChoiceIndex != 0 {
			t.Fatalf("文本帧 = %+v", text)
		}
		if text.FinishReason != "STOP" || text.Status != "STOP" {
			t.Fatalf("文本帧 finishReason = %+v", text)
		}
		if toolCall == nil || toolCall.VisibleOutput == nil || *toolCall.VisibleOutput {
			t.Fatalf("functionCall 帧 = %+v", toolCall)
		}
		if hidden == nil || hidden.VisibleOutput == nil || *hidden.VisibleOutput {
			t.Fatal("thought part 文本不可见")
		}
		if completed == nil || completed.ChoiceIndex == nil || *completed.ChoiceIndex != 0 {
			t.Fatalf("completed 帧 = %+v", completed)
		}
		if usage == nil || usage.Usage == nil || usage.Usage.InputTokens == nil || *usage.Usage.InputTokens != 5 {
			t.Fatalf("usage 帧 = %+v", usage)
		}
		if !waSliceEqual(usage.RawJSONPaths, []string{"usageMetadata"}) {
			t.Fatalf("usage 路径 = %v", usage.RawJSONPaths)
		}
	})
	t.Run("interactions usage 路径", func(t *testing.T) {
		root := waParseGeminiJSON(t, `{"metadata":{"total_usage":{"total_input_tokens":3}}}`)
		frames := ExtractJSONSemanticFrames(root, EndpointFamilyInteractions)
		found := false
		for _, frame := range frames {
			if frame.FrameType == FrameTypeUsage {
				found = true
				if !waSliceEqual(frame.RawJSONPaths, []string{"metadata.total_usage"}) {
					t.Fatalf("interactions usage 路径 = %v", frame.RawJSONPaths)
				}
			}
		}
		if !found {
			t.Fatal("interactions usage 帧缺失")
		}
	})
	t.Run("无法识别的 family 回退 generate_content", func(t *testing.T) {
		if got := ResponseEndpointFamilyFromPath("/unknown"); got != EndpointFamilyGenerateContent {
			t.Fatalf("回退 = %q", got)
		}
		if got := ResponseEndpointFamilyFromPath("/v1beta/models/m:counttokens"); got != EndpointFamilyCountTokens {
			t.Fatalf("counttokens = %q", got)
		}
	})
}

// SSE 语义帧：step.delta、interaction.completed/failed 与 message 回退。
func TestWAExtractSSESemanticFrames(t *testing.T) {
	decode := func(text string) StreamEvent {
		return ParseSSEEventData(text, "", "", 0)
	}
	t.Run("data 为 nil 无帧", func(t *testing.T) {
		event := ParseSSEEventData("", "", "", 0)
		if frames := ExtractSSESemanticFrames(event, EndpointFamilyInteractions); len(frames) != 0 {
			t.Fatalf("空事件应无帧: %+v", frames)
		}
	})
	t.Run("step.delta 可见与思考", func(t *testing.T) {
		visible := ExtractSSESemanticFrames(decode(`{"type":"step.delta","delta":{"type":"text","text":"hi"}}`), EndpointFamilyInteractions)
		var delta *ResponseSemanticFrame
		for index := range visible {
			if visible[index].FrameType == FrameTypeOutputTextDelta {
				delta = &visible[index]
			}
		}
		if delta == nil || delta.Text != "hi" || delta.VisibleOutput == nil || !*delta.VisibleOutput {
			t.Fatalf("可见 delta = %+v", delta)
		}
		thought := ExtractSSESemanticFrames(decode(`{"type":"step.delta","delta":{"type":"thought","text":"t"}}`), EndpointFamilyInteractions)
		for _, frame := range thought {
			if frame.FrameType == FrameTypeOutputTextDelta && (frame.VisibleOutput == nil || *frame.VisibleOutput) {
				t.Fatalf("thought delta 应不可见: %+v", frame)
			}
		}
	})
	t.Run("interaction.completed 与 failed", func(t *testing.T) {
		done := ExtractSSESemanticFrames(decode(`{"type":"interaction.completed","interaction":{"status":"completed"}}`), EndpointFamilyInteractions)
		var completed *ResponseSemanticFrame
		for index := range done {
			if done[index].FrameType == FrameTypeCompleted {
				completed = &done[index]
			}
		}
		if completed == nil || completed.FinishReason != "completed" {
			t.Fatalf("completed 帧 = %+v", completed)
		}
		failed := ExtractSSESemanticFrames(decode(`{"type":"interaction.failed","interaction":{"error":{"status":"FAILED","message":"x"}}}`), EndpointFamilyInteractions)
		var errorFrame, completedFailed *ResponseSemanticFrame
		for index := range failed {
			if failed[index].FrameType == FrameTypeError {
				errorFrame = &failed[index]
			}
			if failed[index].FrameType == FrameTypeCompleted {
				completedFailed = &failed[index]
			}
		}
		if errorFrame == nil || errorFrame.ErrorType != "FAILED" || !waSliceEqual(errorFrame.RawJSONPaths, []string{"interaction.error"}) {
			t.Fatalf("failed 错误帧 = %+v", errorFrame)
		}
		if completedFailed == nil || completedFailed.FinishReason != "failed" {
			t.Fatalf("failed completed 帧 = %+v", completedFailed)
		}
	})
	t.Run("generateContent SSE 文本为 delta 帧", func(t *testing.T) {
		event := decode(`{"candidates":[{"content":{"parts":[{"text":"a"}]}}]}`)
		frames := ExtractSSESemanticFrames(event, EndpointFamilyGenerateContent)
		found := false
		for _, frame := range frames {
			if frame.FrameType == FrameTypeOutputTextDelta && frame.Text == "a" {
				found = true
				if frame.Transport != TransportSSE {
					t.Fatal("SSE 传输标识错误")
				}
			}
		}
		if !found {
			t.Fatal("SSE 文本帧缺失")
		}
	})
	t.Run("error 事件提取", func(t *testing.T) {
		// SSE 路径要求事件显式标记 error（type=error 或事件名 error）。
		event := decode(`{"type":"error","error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"慢"}}`)
		frames := ExtractSSESemanticFrames(event, EndpointFamilyGenerateContent)
		if frames[0].FrameType != FrameTypeError || frames[0].ErrorCode != "429" {
			t.Fatalf("错误帧 = %+v", frames[0])
		}
		// 无显式 error 标记的普通事件不产生错误帧。
		plain := decode(`{"error":{"code":400}}`)
		for _, frame := range ExtractSSESemanticFrames(plain, EndpointFamilyGenerateContent) {
			if frame.FrameType == FrameTypeError {
				t.Fatalf("无显式标记不应产生错误帧: %+v", frame)
			}
		}
	})
}

func TestWAExtractStreamEventError(t *testing.T) {
	cases := []struct {
		name        string
		data        string
		eventType   string
		eventName   string
		wantError   bool
		wantMessage string
	}{
		{"type=error 平铺", `{"code":1,"message":"m"}`, "error", "", true, "m"},
		{"eventName=error", `{"message":"m2"}`, "other", "error", true, "m2"},
		{"interaction.failed", `{"interaction":{"error":{"message":"m3"}}}`, "interaction.failed", "", true, "m3"},
		{"interaction 整体兜底", `{"interaction":{"status":"failed"}}`, "interaction.failed", "", true, ""},
		{"interaction.error 优先", `{"interaction":{"error":{"message":"inner"}},"error":{"message":"outer"}}`, "interaction.failed", "", true, "inner"},
		{"data 兜底", `{"type":"error"}`, "", "", true, ""},
		{"普通事件", `{"candidates":[]}`, "x", "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractStreamEventError(waParseGeminiJSON(t, tc.data), tc.eventType, tc.eventName)
			if (got != nil) != tc.wantError {
				t.Fatalf("ExtractStreamEventError = %+v，期望错误 %v", got, tc.wantError)
			}
			if got != nil && tc.wantMessage != "" && stringField(got["message"]) != tc.wantMessage {
				t.Fatalf("错误消息 = %q，期望 %q", stringField(got["message"]), tc.wantMessage)
			}
		})
	}
}

// usage 提取契约：嵌套包裹层级（metadata.total_usage / interaction.usage）与
// 两套字段别名。
func TestWAExtractUsage(t *testing.T) {
	t.Run("Gemini 字段别名", func(t *testing.T) {
		usage := ExtractUsage(waParseGeminiJSON(t, `{
			"promptTokenCount": 10,
			"candidatesTokenCount": 4,
			"thoughtsTokenCount": 3,
			"cachedContentTokenCount": 2,
			"service_tier": "default"
		}`))
		waAssertGeminiToken(t, usage.InputTokens, 10, "input")
		waAssertGeminiToken(t, usage.OutputTokens, 7, "output = candidates + thoughts")
		waAssertGeminiToken(t, usage.CacheReadTokens, 2, "cacheRead")
		waAssertGeminiToken(t, usage.ThinkingTokens, 3, "thinking")
		if usage.ServiceTier != "default" {
			t.Fatalf("ServiceTier = %q", usage.ServiceTier)
		}
	})
	t.Run("OpenAI 风格别名", func(t *testing.T) {
		usage := ExtractUsage(waParseGeminiJSON(t, `{
			"total_input_tokens": 5,
			"total_output_tokens": 6,
			"total_cached_tokens": 1,
			"total_thought_tokens": 2
		}`))
		waAssertGeminiToken(t, usage.InputTokens, 5, "input")
		waAssertGeminiToken(t, usage.OutputTokens, 8, "output = total + thought")
		waAssertGeminiToken(t, usage.CacheReadTokens, 1, "cacheRead")
		waAssertGeminiToken(t, usage.ThinkingTokens, 2, "thinking")
	})
	t.Run("metadata.total_usage 嵌套下钻", func(t *testing.T) {
		usage := ExtractUsage(waParseGeminiJSON(t, `{"metadata":{"total_usage":{"promptTokenCount":7}}}`))
		waAssertGeminiToken(t, usage.InputTokens, 7, "input（下钻）")
	})
	t.Run("interaction.usage 嵌套与 service_tier 继承", func(t *testing.T) {
		usage := ExtractUsage(waParseGeminiJSON(t, `{"interaction":{"service_tier":"priority","usage":{"promptTokenCount":8}}}`))
		waAssertGeminiToken(t, usage.InputTokens, 8, "input（interaction.usage）")
		if usage.ServiceTier != "priority" {
			t.Fatalf("ServiceTier = %q", usage.ServiceTier)
		}
	})
	t.Run("顶层 usage/usageMetadata 候选", func(t *testing.T) {
		usage := ExtractUsage(waParseGeminiJSON(t, `{"usageMetadata":{"promptTokenCount":9}}`))
		waAssertGeminiToken(t, usage.InputTokens, 9, "input（usageMetadata）")
	})
	t.Run("非对象输入", func(t *testing.T) {
		if usage := ExtractUsage(42); HasAnyUsageValue(usage) {
			t.Fatalf("标量输入应为空 usage: %+v", usage)
		}
	})
}

func TestWAGeminiParseUsageEntries(t *testing.T) {
	t.Run("JSONBuffer 完整文档", func(t *testing.T) {
		usage := ParseUsageFromJSONBuffer([]byte(`{"usageMetadata":{"promptTokenCount":4}}`))
		waAssertGeminiToken(t, usage.InputTokens, 4, "input")
		if usage := ParseUsageFromJSONBuffer(nil); HasAnyUsageValue(usage) {
			t.Fatal("空 buffer 应为空 usage")
		}
	})
	t.Run("JSONValue", func(t *testing.T) {
		usage := ParseUsageFromJSONValue(waParseGeminiJSON(t, `{"usageMetadata":{"promptTokenCount":6}}`))
		waAssertGeminiToken(t, usage.InputTokens, 6, "input")
	})
	t.Run("片段扫描 service_tier + usageMetadata", func(t *testing.T) {
		text := `data: {"usageMetadata":{"promptTokenCount":11},"service_tier":"priority"}` + "\n"
		usage := ParseUsageFromJSONTextFragment(text, true)
		waAssertGeminiToken(t, usage.InputTokens, 11, "input")
		if usage.ServiceTier != "priority" {
			t.Fatalf("ServiceTier = %q", usage.ServiceTier)
		}
	})
	t.Run("完整文档解析优先", func(t *testing.T) {
		usage := ParseUsageFromJSONTextFragment(`{"usageMetadata":{"promptTokenCount":13}}`, false)
		waAssertGeminiToken(t, usage.InputTokens, 13, "input")
	})
	t.Run("只有 service_tier 的片段", func(t *testing.T) {
		usage := ParseUsageFromJSONTextFragment(`prefix "service_tier":"flex" suffix`, true)
		if usage.ServiceTier != "flex" {
			t.Fatalf("仅 tier 片段 = %+v", usage)
		}
		if usage.InputTokens != nil {
			t.Fatalf("不应有 token 值: %+v", usage)
		}
	})
	t.Run("usage 值存在但全空时回退后续属性", func(t *testing.T) {
		// usage 对象无有效数值 → usageHasDefinedValue false → 继续找下一个属性。
		text := `{"usage":{},"usageMetadata":{"promptTokenCount":15}}`
		usage := ParseUsageFromJSONTextFragment(text, true)
		waAssertGeminiToken(t, usage.InputTokens, 15, "input（第二个属性）")
	})
	t.Run("usage 无值且无 tier", func(t *testing.T) {
		if usage := ParseUsageFromJSONTextFragment(`{"usage":{}}`, true); HasAnyUsageValue(usage) {
			t.Fatalf("空 usage 应为空: %+v", usage)
		}
	})
	t.Run("空文本", func(t *testing.T) {
		if usage := ParseUsageFromJSONTextFragment("", false); HasAnyUsageValue(usage) {
			t.Fatal("空文本应为空 usage")
		}
	})
	t.Run("片段属性名不匹配", func(t *testing.T) {
		if usage := ParseUsageFromJSONTextFragment(`{"usageX":{}}`, true); HasAnyUsageValue(usage) {
			t.Fatalf("无匹配属性应为空: %+v", usage)
		}
	})
}

func waAssertGeminiToken(t *testing.T, value *int, want int, label string) {
	t.Helper()
	if value == nil {
		t.Fatalf("%s：得到 nil，期望 %d", label, want)
	}
	if *value != want {
		t.Fatalf("%s = %d，期望 %d", label, *value, want)
	}
}

func waSliceEqual(got, want []string) bool {
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

func waHasPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func TestWAGeminiStringFieldAndHelpers(t *testing.T) {
	if stringField("x") != "x" || stringField("") != "" {
		t.Fatal("stringField 字符串语义错误")
	}
	if stringField(float64(3)) != "3" || stringField(float64(1.5)) != "1.5" {
		t.Fatal("stringField 数字语义错误")
	}
	if stringField(true) != "true" || stringField(false) != "false" {
		t.Fatal("stringField 布尔语义错误")
	}
	if stringField(nil) != "" || stringField([]any{1}) != "" {
		t.Fatal("stringField 其他类型应为空")
	}
	if orString("", "a") != "a" || orString("", "") != "" {
		t.Fatal("orString 语义错误")
	}
}
