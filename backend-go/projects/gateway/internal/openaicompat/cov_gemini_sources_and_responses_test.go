package openaicompat

// gemini-native 目标桥的两个源（responses / anthropic）分支补充覆盖，以及
// anthropic messages -> responses 目标的流式/缓冲响应转换补充覆盖。

import (
	"strings"
	"testing"
)

func TestCovResponsesToGeminiNativeBranches(t *testing.T) {
	guidanceOptions := BridgeRequestBodyOptions{GuidanceProviderName: "某供应商"}
	t.Run("guidance 错误载荷与 Error()", func(t *testing.T) {
		err := geminiNativeGuidanceError(guidanceOptions, "提示", "code-x")
		if err.Error() != "提示" {
			t.Errorf("Error() = %q", err.Error())
		}
		if err.Code != "code-x" || err.Protocol != GeminiNativeProtocolChatCompletions {
			t.Errorf("payload = %+v", err)
		}
	})
	t.Run("input 数组各 item 形态", func(t *testing.T) {
		body := map[string]any{
			"instructions": "系统指引",
			"input": []any{
				nil,
				map[string]any{"type": "message", "role": "assistant", "content": "回答"},
				map[string]any{"type": "message", "role": "user", "content": ""},
				map[string]any{"type": "function_call", "arguments": `{"a":1}`},
				map[string]any{"type": "function_call", "name": "named", "arguments": "not-json"},
				map[string]any{"type": "function_call_output", "name": "named", "output": map[string]any{"ok": 1}},
				map[string]any{"type": "function_call_output", "call_id": "c9", "output": nil},
				map[string]any{"type": "function_call_output", "output": "文本"},
			},
			"tools": []any{
				nil,
				map[string]any{"type": "function"},
				map[string]any{"type": "function", "name": "lookup", "description": "d", "parameters": "bad"},
			},
		}
		output, err := BuildOpenAIResponsesToGeminiBody(body, BridgeRequestBodyOptions{ModelOverride: "gemini-up"})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		if output["model"] != "gemini-up" {
			t.Errorf("model = %v，期望 ModelOverride 透传", output["model"])
		}
		instruction := covMap(t, output["systemInstruction"])
		parts := covSlice(t, instruction["parts"])
		if covText(t, covMap(t, parts[0])["text"]) != "系统指引" {
			t.Errorf("systemInstruction = %v", instruction)
		}
		contents := covSlice(t, output["contents"])
		// assistant(回答) + function_call 默认名 + named call + 三个 function_call_output。
		if len(contents) != 6 {
			t.Fatalf("期望 6 个 content，实际 %d：%v", len(contents), contents)
		}
		model := covMap(t, contents[0])
		if covText(t, model["role"]) != "model" {
			t.Errorf("assistant -> model role：%v", model)
		}
		if covText(t, covMap(t, covSlice(t, model["parts"])[0])["text"]) != "回答" {
			t.Errorf("assistant 文本 part = %v", model["parts"])
		}
		defaultCall := covMap(t, covSlice(t, covMap(t, contents[1])["parts"])[0])
		if covText(t, covMap(t, defaultCall["functionCall"])["name"]) != "function_call" {
			t.Errorf("缺 name 的 function_call 应回退：%v", defaultCall)
		}
		named := covMap(t, covSlice(t, covMap(t, contents[2])["parts"])[0])
		args := covMap(t, covMap(t, named["functionCall"])["args"])
		if len(args) != 0 {
			t.Errorf("非 JSON arguments 应折叠 {}：%v", args)
		}
		namedOutput := covMap(t, covSlice(t, covMap(t, contents[3])["parts"])[0])
		response := covMap(t, covMap(t, namedOutput["functionResponse"])["response"])
		if response["ok"] != 1 {
			t.Errorf("function_call_output 应包对象 response：%v", namedOutput)
		}
		callIDOutput := covMap(t, covSlice(t, covMap(t, contents[4])["parts"])[0])
		if covText(t, covMap(t, callIDOutput["functionResponse"])["name"]) != "c9" {
			t.Errorf("call_id 应作为 functionResponse name：%v", callIDOutput)
		}
		fallback := covMap(t, covSlice(t, covMap(t, contents[5])["parts"])[0])
		if covText(t, covMap(t, fallback["functionResponse"])["name"]) != "function_result" {
			t.Errorf("无 name/call_id 应回退：%v", fallback)
		}
		tools := covSlice(t, output["tools"])
		declarations := covSlice(t, covMap(t, tools[0])["functionDeclarations"])
		if len(declarations) != 1 {
			t.Fatalf("无名 function 应跳过：%v", tools)
		}
		declared := covMap(t, declarations[0])
		schema := covMap(t, declared["parameters"])
		if covText(t, schema["type"]) != "object" {
			t.Errorf("非对象 parameters 应回退默认 schema：%v", declared)
		}
	})
	t.Run("错误与拒绝分支", func(t *testing.T) {
		base := func(mutate func(map[string]any)) map[string]any {
			body := map[string]any{"input": "hi"}
			if mutate != nil {
				mutate(body)
			}
			return body
		}
		cases := []struct {
			name string
			body map[string]any
			code string
		}{
			{"service_tier 拒绝", base(func(body map[string]any) { body["service_tier"] = "priority" }), "unsupported_openai_request_controls_for_gemini_native"},
			{"reasoning 拒绝", base(func(body map[string]any) { body["reasoning"] = map[string]any{} }), "unsupported_openai_request_controls_for_gemini_native"},
			{"thinking 拒绝", base(func(body map[string]any) { body["thinking"] = map[string]any{} }), "unsupported_openai_request_controls_for_gemini_native"},
			{"previous_response_id 拒绝", base(func(body map[string]any) { body["previous_response_id"] = nil }), "unsupported_responses_state_for_gemini_native"},
			{"context_management 拒绝", base(func(body map[string]any) { body["context_management"] = map[string]any{} }), "unsupported_responses_state_for_gemini_native"},
			{"truncation 非法值", base(func(body map[string]any) { body["truncation"] = "other" }), "unsupported_responses_truncation_for_gemini_native"},
			{"input 类型非法", base(func(body map[string]any) { body["input"] = 5 }), "invalid_responses_gemini_bridge_input"},
			{"content 类型非法", base(func(body map[string]any) {
				body["input"] = []any{map[string]any{"type": "message", "role": "user", "content": 5}}
			}), "invalid_openai_gemini_bridge_content"},
			{"未知 content part", base(func(body map[string]any) {
				body["input"] = []any{map[string]any{"type": "message", "role": "user", "content": []any{
					map[string]any{"type": "audio"},
				}}}
			}), "unsupported_openai_content_part_for_gemini_native"},
			{"无名 content part", base(func(body map[string]any) {
				body["input"] = []any{map[string]any{"type": "message", "role": "user", "content": []any{
					map[string]any{},
				}}}
			}), "unsupported_openai_content_part_for_gemini_native"},
			{"非 function tool", base(func(body map[string]any) {
				body["tools"] = []any{map[string]any{"type": "web_search"}}
			}), "unsupported_responses_tool_for_gemini_native"},
			{"无类型 tool", base(func(body map[string]any) {
				body["tools"] = []any{map[string]any{"name": "x"}}
			}), "unsupported_responses_tool_for_gemini_native"},
			{"reasoning item 拒绝", base(func(body map[string]any) {
				body["input"] = []any{map[string]any{"type": "reasoning", "summary": "s"}}
			}), "unsupported_responses_reasoning_for_gemini_native"},
			{"file_id 图片引用", base(func(body map[string]any) {
				body["input"] = []any{map[string]any{"type": "message", "role": "user", "content": []any{
					map[string]any{"type": "input_image", "image_url": "file-1"},
				}}}
			}), "unsupported_openai_image_reference_for_gemini_native"},
			{"logprobs 拒绝", base(func(body map[string]any) { body["logprobs"] = true }), "unsupported_logprobs_for_gemini_native"},
			{"top_logprobs 拒绝", base(func(body map[string]any) { body["top_logprobs"] = 2 }), "unsupported_logprobs_for_gemini_native"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := BuildOpenAIResponsesToGeminiBody(tc.body, BridgeRequestBodyOptions{GuidanceProviderName: "某供应商"})
				guidance, ok := err.(*BridgeGuidanceError)
				if ok {
					if guidance.Code != tc.code {
						t.Fatalf("code = %q，期望 %q", guidance.Code, tc.code)
					}
					return
				}
				requireBridgeValidation(t, err, tc.code)
			})
		}
		t.Run("truncation 合法值放行", func(t *testing.T) {
			for _, value := range []string{"disabled", "auto"} {
				body := map[string]any{"input": "hi", "truncation": value}
				if _, err := BuildOpenAIResponsesToGeminiBody(body, BridgeRequestBodyOptions{}); err != nil {
					t.Errorf("truncation=%s 不应拒绝：%v", value, err)
				}
			}
		})
	})
	t.Run("finalize 与 generationConfig 形态", func(t *testing.T) {
		// 注意：service_tier 属于 OpenAI 请求控制保护字段，会被前置校验拒绝，
		// 其 tier 映射行为改由 geminiServiceTierFromSourceForGeminiNative 直连验证。
		body := map[string]any{
			"input":       "hi",
			"temperature": 0.3,
			"top_p":       0.7,
			"max_tokens":  128.9,
			"stop":        "END",
			"response_format": map[string]any{
				"type":        "json_schema",
				"json_schema": map[string]any{"schema": map[string]any{"type": "object"}},
			},
			"tools":       []any{map[string]any{"type": "function", "name": "f"}},
			"tool_choice": map[string]any{"function": map[string]any{"name": "f"}},
		}
		output, err := BuildOpenAIResponsesToGeminiBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		config := covMap(t, output["generationConfig"])
		covInt(t, config["maxOutputTokens"], 128)
		if stops, ok := config["stopSequences"].([]any); !ok || len(stops) != 1 || stops[0] != "END" {
			t.Errorf("stopSequences = %v", config["stopSequences"])
		}
		if config["responseMimeType"] != "application/json" {
			t.Errorf("responseMimeType = %v", config["responseMimeType"])
		}
		schema := covMap(t, config["responseSchema"])
		if covText(t, schema["type"]) != "object" {
			t.Errorf("responseSchema = %v", config["responseSchema"])
		}
		toolConfig := covMap(t, output["toolConfig"])
		calling := covMap(t, toolConfig["functionCallingConfig"])
		if covText(t, calling["mode"]) != "ANY" {
			t.Errorf("toolConfig = %v", toolConfig)
		}
	})
	t.Run("tool_choice 形态", func(t *testing.T) {
		build := func(mutate func(map[string]any)) map[string]any {
			body := map[string]any{"input": "hi", "tools": []any{map[string]any{"type": "function", "name": "f"}}}
			if mutate != nil {
				mutate(body)
			}
			output, err := BuildOpenAIResponsesToGeminiBody(body, BridgeRequestBodyOptions{})
			if err != nil {
				t.Fatalf("构造失败：%v", err)
			}
			return output
		}
		if got := build(func(body map[string]any) { body["tool_choice"] = "none" })["toolConfig"]; got == nil {
			t.Error("none 应产出 NONE toolConfig")
		} else if !strings.Contains(got.(map[string]any)["functionCallingConfig"].(map[string]any)["mode"].(string), "NONE") {
			t.Errorf("toolConfig = %v", got)
		}
		if got := build(func(body map[string]any) { body["tool_choice"] = "any" })["toolConfig"]; got == nil {
			t.Error("any 应产出 ANY toolConfig")
		}
		if got := build(func(body map[string]any) { body["tool_choice"] = "auto" })["toolConfig"]; got != nil {
			t.Errorf("auto 不应产出 toolConfig：%v", got)
		}
		if got := build(func(body map[string]any) { body["tool_choice"] = "bogus" })["toolConfig"]; got != nil {
			t.Errorf("未知字符串不应产出 toolConfig：%v", got)
		}
		if got := build(func(body map[string]any) { body["tool_choice"] = map[string]any{"type": "function"} })["toolConfig"]; got != nil {
			t.Errorf("function 缺 name 不应产出 toolConfig：%v", got)
		}
		if got := build(func(body map[string]any) { body["tool_choice"] = 7 })["toolConfig"]; got != nil {
			t.Errorf("数字 tool_choice 不应产出：%v", got)
		}
	})
	t.Run("service_tier tier 映射（直连）", func(t *testing.T) {
		for tier, want := range map[string]string{"priority": "priority", "flex": "flex", "default": "standard"} {
			if got := geminiServiceTierFromSourceForGeminiNative(map[string]any{"service_tier": tier}); got != want {
				t.Errorf("service_tier %s -> %q，期望 %s", tier, got, want)
			}
		}
		if got := geminiServiceTierFromSourceForGeminiNative(map[string]any{"service_tier": "other"}); got != "" {
			t.Errorf("未知 tier 应为空：%q", got)
		}
		if got := geminiServiceTierFromSourceForGeminiNative(map[string]any{}); got != "" {
			t.Errorf("缺省应为空：%q", got)
		}
	})
	// 注意：reasoning / reasoning_effort 属于受保护请求控制字段，公开构造器
	// 会在前置校验直接拒绝，thinkingLevel 提取只能直连验证（当前行为）。
	t.Run("stop_sequences 与 thinkingLevel 提取", func(t *testing.T) {
		output, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"input":          "hi",
			"stop_sequences": []any{"A", 2, "B"},
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		config := covMap(t, output["generationConfig"])
		if stops, ok := config["stopSequences"].([]any); !ok || len(stops) != 2 {
			t.Errorf("stopSequences 应过滤非字符串：%v", config["stopSequences"])
		}
		if _, exists := config["thinkingConfig"]; exists {
			t.Errorf("无 reasoning 输入不应产出 thinkingConfig：%v", config)
		}
		if got := geminiThinkingLevelFromSourceForGeminiNative(map[string]any{
			"reasoning": map[string]any{"effort": "high"},
		}); got != "high" {
			t.Errorf("reasoning.effort 应优先：%q", got)
		}
		if got := geminiThinkingLevelFromSourceForGeminiNative(map[string]any{"reasoning_effort": "low"}); got != "low" {
			t.Errorf("reasoning_effort 回退 = %q", got)
		}
		if got, ok := geminiNativeStopValue(map[string]any{"stop_sequences": "S"}); !ok || got[0] != "S" {
			t.Error("stop_sequences 字符串应命中")
		}
		if got, ok := geminiNativeStopValue(map[string]any{"stop": 5}); ok || got != nil {
			t.Errorf("非法 stop 类型应 miss：%v", got)
		}
		if _, ok := geminiNativeStopValue(map[string]any{}); ok {
			t.Error("无 stop 应 miss")
		}
		if got := geminiThinkingLevelFromSourceForGeminiNative(map[string]any{"reasoning_effort": "ultra"}); got != "" {
			t.Errorf("未知 effort 应为空：%q", got)
		}
		if got := geminiJSONObjectFromUnknown("文本"); len(got) != 0 {
			t.Errorf("非 JSON 文本应折叠 {}：%v", got)
		}
		if got := guessImageMimeTypeForGeminiNative("https://x/a.webp?raw=1"); got != "image/webp" {
			t.Errorf("webp 识别 = %q", got)
		}
		if got := guessImageMimeTypeForGeminiNative("https://x/b.gif#f"); got != "image/gif" {
			t.Errorf("gif 识别 = %q", got)
		}
		if got := guessImageMimeTypeForGeminiNative("https://x/c.jpeg"); got != "image/jpeg" {
			t.Errorf("jpeg 识别 = %q", got)
		}
		if got := guessImageMimeTypeForGeminiNative("https://x/d.bin"); got != "image/png" {
			t.Errorf("缺省 png = %q", got)
		}
	})
}

func TestCovAnthropicToGeminiNativeBranches(t *testing.T) {
	t.Run("content 各 block 形态", func(t *testing.T) {
		body := map[string]any{
			"system": []any{
				nil,
				map[string]any{"type": "text", "text": "S1"},
				map[string]any{"type": "text", "text": "S2"},
			},
			"messages": []any{
				nil,
				map[string]any{"role": "user", "content": ""},
				map[string]any{"role": "user", "content": 5},
				map[string]any{"role": "assistant", "content": []any{
					nil,
					map[string]any{"type": "text", "text": "说明"},
					map[string]any{"type": "tool_use", "input": map[string]any{"a": 1}},
					map[string]any{"type": "tool_use", "name": "named"},
					map[string]any{"type": "tool_result", "content": "文本结果"},
					map[string]any{"type": "tool_result", "name": "res", "content": nil},
				}},
			},
			"tools": []any{nil, map[string]any{"input_schema": "bad"}, map[string]any{"name": "lookup"}},
		}
		output, err := BuildAnthropicMessagesToGeminiBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		instruction := covMap(t, output["systemInstruction"])
		parts := covSlice(t, instruction["parts"])
		if covText(t, covMap(t, parts[0])["text"]) != "S1\nS2" {
			t.Errorf("systemInstruction = %v", instruction)
		}
		if covText(t, instruction["role"]) != "user" {
			t.Errorf("systemInstruction.role = %v，期望 user", instruction["role"])
		}
		contents := covSlice(t, output["contents"])
		// 空串与非数组内容跳过，只剩 assistant 一条。
		if len(contents) != 1 {
			t.Fatalf("期望 1 个 content，实际 %d：%v", len(contents), contents)
		}
		modelParts := covSlice(t, covMap(t, contents[0])["parts"])
		if len(modelParts) != 5 {
			t.Fatalf("期望 5 个 parts，实际 %d：%v", len(modelParts), modelParts)
		}
		defaultCall := covMap(t, covMap(t, modelParts[1])["functionCall"])
		if covText(t, defaultCall["name"]) != "tool_use" {
			t.Errorf("缺 name 的 tool_use 应回退：%v", defaultCall)
		}
		namedCall := covMap(t, modelParts[2])
		if covMap(t, namedCall["functionCall"])["args"] == nil {
			t.Errorf("tool_use input 应透传：%v", namedCall)
		}
		defaultResult := covMap(t, modelParts[3])
		if covText(t, covMap(t, defaultResult["functionResponse"])["name"]) != "tool_result" {
			t.Errorf("缺 name 的 tool_result 应回退：%v", defaultResult)
		}
		wrapped := covMap(t, covMap(t, modelParts[4])["functionResponse"])
		if wrapped["response"] == nil {
			t.Errorf("非对象 content 应包 content 键：%v", modelParts[4])
		}
		tools := covSlice(t, output["tools"])
		declarations := covSlice(t, covMap(t, tools[0])["functionDeclarations"])
		if len(declarations) != 1 {
			t.Fatalf("应只剩 named tool：%v", tools)
		}
		schema := covMap(t, covMap(t, declarations[0])["parameters"])
		if covText(t, schema["type"]) != "object" {
			t.Errorf("默认 schema = %v", schema)
		}
	})
	t.Run("拒绝与错误分支", func(t *testing.T) {
		base := func(mutate func(map[string]any)) map[string]any {
			body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
			if mutate != nil {
				mutate(body)
			}
			return body
		}
		cases := []struct {
			name string
			body map[string]any
			code string
		}{
			{"thinking 拒绝", base(func(body map[string]any) { body["thinking"] = map[string]any{} }), "unsupported_anthropic_state_for_gemini_native"},
			{"cache_control 拒绝", base(func(body map[string]any) { body["cache_control"] = map[string]any{} }), "unsupported_anthropic_state_for_gemini_native"},
			{"messages 非数组", base(func(body map[string]any) { body["messages"] = 3 }), "invalid_messages_gemini_bridge_messages"},
			{"system 非 text block", base(func(body map[string]any) {
				body["system"] = []any{map[string]any{"type": "image"}}
			}), "unsupported_anthropic_system_part_for_gemini_native"},
			{"未知 content block", base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "document"},
				}}}
			}), "unsupported_anthropic_content_part_for_gemini_native"},
			{"无名 content block", base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{},
				}}}
			}), "unsupported_anthropic_content_part_for_gemini_native"},
			{"image source 缺 data", base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "image", "source": map[string]any{"type": "base64"}},
				}}}
			}), "unsupported_anthropic_image_source_for_gemini_native"},
			{"image source 未知类型", base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "image", "source": map[string]any{"type": "file"}},
				}}}
			}), "unsupported_anthropic_image_source_for_gemini_native"},
			{"url 图片缺 url", base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "image", "source": map[string]any{"type": "url"}},
				}}}
			}), "unsupported_anthropic_image_source_for_gemini_native"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := BuildAnthropicMessagesToGeminiBody(tc.body, BridgeRequestBodyOptions{GuidanceProviderName: "某供应商"})
				guidance, ok := err.(*BridgeGuidanceError)
				if ok {
					if guidance.Code != tc.code {
						t.Fatalf("code = %q，期望 %q", guidance.Code, tc.code)
					}
					return
				}
				requireBridgeValidation(t, err, tc.code)
			})
		}
	})
	t.Run("图片与 system 合法形态", func(t *testing.T) {
		body := map[string]any{
			"system": "系统字符串",
			"messages": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "QQ=="}},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/gif", "data": "Rg=="}},
				map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://x/a.png"}},
			}}},
		}
		output, err := BuildAnthropicMessagesToGeminiBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		contents := covSlice(t, output["contents"])
		parts := covSlice(t, covMap(t, contents[0])["parts"])
		if len(parts) != 3 {
			t.Fatalf("期望 3 个图片 part：%v", parts)
		}
		defaultMime := covMap(t, parts[0])
		if covText(t, covMap(t, defaultMime["inlineData"])["mimeType"]) != "image/png" {
			t.Errorf("缺 media_type 应回退 png：%v", defaultMime)
		}
		gif := covMap(t, parts[1])
		if covText(t, covMap(t, gif["inlineData"])["mimeType"]) != "image/gif" {
			t.Errorf("gif mime = %v", gif)
		}
		fileData := covMap(t, parts[2])
		if covText(t, covMap(t, fileData["fileData"])["fileUri"]) != "https://x/a.png" {
			t.Errorf("url 图片应走 fileData：%v", fileData)
		}
	})
	t.Run("空 contents 报错", func(t *testing.T) {
		_, err := BuildAnthropicMessagesToGeminiBody(map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": ""}},
		}, BridgeRequestBodyOptions{})
		requireBridgeValidation(t, err, "empty_gemini_target_bridge_contents")
	})
}

func TestCovAnthropicSseAsResponsesFull(t *testing.T) {
	state := NewAnthropicResponsesStreamState("claude-3", "prev_1")
	events := []string{
		"event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_9","model":"claude-9","usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n",
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"思考"}}` + "\n\n",
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n",
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":1,"content_block":{"type":"text"}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"答案"}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{}}` + "\n\n",
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t1","name":"lookup","input":{"q":1}}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"1}"}}` + "\n\n",
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":3,"content_block":{"type":"image"}}` + "\n\n",
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":3}` + "\n\n",
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":1}` + "\n\n",
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":2}` + "\n\n",
		"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":8,"cache_read_input_tokens":3,"thinking_tokens":5}}` + "\n\n",
		"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n",
	}
	var collected []string
	for _, eventText := range events {
		collected = append(collected, state.ProcessAnthropicEvent(eventText)...)
	}
	joined := strings.Join(collected, "")
	for _, want := range []string{
		"event: response.created",
		"event: response.in_progress",
		`"type":"reasoning"`,
		"response.output_text.delta",
		`"delta":"答案"`,
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"event: response.completed",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("流缺少 %s：\n%s", want, joined)
		}
	}
	// thinking block 在 stop 时收尾为 reasoning item（JSON 键按字母序）。
	if !strings.Contains(joined, `"summary":[{"text":"思考","type":"summary_text"}]`) {
		t.Errorf("thinking 应在 stop 时收尾为 reasoning item：\n%s", joined)
	}
	if state.ResponseID != "resp_msg_9" {
		t.Errorf("message_start 应刷新 ResponseID，实际 %q", state.ResponseID)
	}
	if state.Model != "claude-9" {
		t.Errorf("model = %q", state.Model)
	}
	// message_delta 的 usage 覆盖：input 总额 = input + cache_creation + cache_read
	// = 0 + 0 + 3 = 3（Node 同语义，cache_read 计入输入侧）。
	usage := covMap(t, state.Usage)
	if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(8) {
		t.Errorf("usage = %v", usage)
	}
	details := covMap(t, usage["output_tokens_details"])
	if details["reasoning_tokens"] != float64(5) {
		t.Errorf("reasoning tokens = %v", details)
	}
	if state.StopReason != "tool_use" {
		t.Errorf("StopReason = %q", state.StopReason)
	}
	t.Run("完成后幂等与 estimated usage", func(t *testing.T) {
		if again := state.ProcessAnthropicEvent(events[0]); again != nil {
			t.Errorf("完成后应忽略事件：%v", again)
		}
		fresh := NewAnthropicResponsesStreamState("m", "")
		fresh.OutputItems = []any{
			map[string]any{"type": "function_call", "arguments": `{"a":1}`},
			map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "hello world"}}},
			nil,
		}
		estimated := anthropicResponsesEstimatedUsage(fresh.OutputItems)
		if estimated["output_tokens"] == float64(0) {
			t.Errorf("estimated output 应大于 0：%v", estimated)
		}
	})
	t.Run("error 事件与中断", func(t *testing.T) {
		fresh := NewAnthropicResponsesStreamState("m", "")
		out := fresh.ProcessAnthropicEvent("event: error\ndata: " + `{"error":{"message":"坏","type":"overloaded"}}` + "\n\n")
		joined := strings.Join(out, "")
		if !strings.Contains(joined, `"code":"overloaded"`) || !strings.Contains(joined, "response.failed") {
			t.Fatalf("error 事件应转 failed：%s", joined)
		}
		fresh2 := NewAnthropicResponsesStreamState("m", "")
		out2 := fresh2.ProcessAnthropicEvent("data: {坏\n\n")
		if !strings.Contains(strings.Join(out2, ""), "upstream_stream_parse_error") {
			t.Fatalf("解析失败应终止：%v", out2)
		}
		fresh3 := NewAnthropicResponsesStreamState("m", "")
		if out3 := fresh3.ProcessAnthropicEvent("event: ping\n\n"); out3 != nil {
			t.Errorf("无 data 应返回 nil：%v", out3)
		}
		fresh4 := NewAnthropicResponsesStreamState("m", "")
		interrupted := fresh4.FinishAnthropicResponsesStream()
		if !strings.Contains(strings.Join(interrupted, ""), "upstream_stream_interrupted") {
			t.Fatalf("中断应失败流：%v", interrupted)
		}
		if again := fresh4.FinishAnthropicResponsesStream(); again != nil {
			t.Errorf("失败后 Finish 应幂等：%v", again)
		}
	})
	t.Run("buffer JSON 变体", func(t *testing.T) {
		got := TransformAnthropicMessagesJSONBufferToResponsesJSON([]byte(
			`{"id":"msg_1","model":"claude-x","usage":{"input_tokens":3,"output_tokens":2},
			  "content":[
				{"type":"text","text":"正文"},
				{"type":"thinking","thinking":"推演"},
				{"type":"thinking"},
				{"type":"tool_use","id":"t","name":"lookup","input":{"k":1}},
				{"type":"tool_use","name":"noid"},
				null
			  ]}`), "fallback", "prev_9")
		text := string(got)
		for _, want := range []string{
			`"status":"completed"`,
			`"text":"正文"`,
			`"type":"reasoning"`,
			`"name":"lookup"`,
			`"input_tokens":3`,
			`"previous_response_id":"prev_9"`,
		} {
			if !strings.Contains(text, want) {
				t.Errorf("JSON 输出缺少 %s：%s", want, text)
			}
		}
		broken := TransformAnthropicMessagesJSONBufferToResponsesJSON([]byte("{坏"), "m", "")
		if !strings.Contains(string(broken), `"status":"completed"`) {
			t.Errorf("坏 JSON 应回退空响应：%s", broken)
		}
	})
	t.Run("buffer SSE 与 usage 回退", func(t *testing.T) {
		got := TransformAnthropicMessagesSseBufferToResponsesSse([]byte(
			"event: message_start\ndata: "+`{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":2}}}`+"\n\n"+
				"event: message_stop\ndata: "+`{"type":"message_stop"}`+"\n\n"), "m", "")
		if !strings.Contains(string(got), "response.completed") {
			t.Fatalf("buffer SSE 应完整收尾：%s", got)
		}
		// previous 快照回退：output_tokens_details.reasoning_tokens。
		merged := anthropicUsageToResponsesUsage(
			map[string]any{"output_tokens": float64(4)},
			map[string]any{"input_tokens": float64(9), "output_tokens_details": map[string]any{"reasoning_tokens": float64(6)}, "input_tokens_details": map[string]any{"cached_tokens": float64(2)}},
		)
		if merged["input_tokens"] != float64(9) || merged["output_tokens"] != float64(4) {
			t.Errorf("合并 usage = %v", merged)
		}
		if covMap(t, merged["output_tokens_details"])["reasoning_tokens"] != float64(6) {
			t.Errorf("previous reasoning_tokens 应回退：%v", merged)
		}
		if covMap(t, merged["input_tokens_details"])["cached_tokens"] != float64(2) {
			t.Errorf("previous cached_tokens 应回退：%v", merged)
		}
	})
}
