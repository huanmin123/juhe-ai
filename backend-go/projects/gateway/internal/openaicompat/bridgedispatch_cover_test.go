package openaicompat

// BuildBridgeRequestBody 分发表与 Gemini -> Anthropic Messages 桥的补充覆盖
// 测试。分发表对应 Node driver buildUpstreamRequestParts 的协议对选择；
// gemini-anthropic-messages-bridge.ts 的请求核心在此逐字段验证。

import (
	"testing"
)

func TestCovBuildBridgeRequestBodyDispatch(t *testing.T) {
	body := map[string]any{
		"model":    "m",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"input":    "hi",
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
	}
	cases := []struct {
		name    string
		source  string
		up      string
		check   func(t *testing.T, output map[string]any)
		errCode string
	}{
		{
			name:   "chat -> anthropic_messages",
			source: FamilyChatCompletions, up: FamilyAnthropicMessages,
			check: func(t *testing.T, output map[string]any) {
				if _, exists := output["max_tokens"]; !exists {
					t.Errorf("anthropic 体应带 max_tokens：%v", output)
				}
			},
		},
		{
			name:   "responses -> anthropic_messages",
			source: FamilyResponses, up: FamilyAnthropicMessages,
			check: func(t *testing.T, output map[string]any) {
				messages := covSlice(t, output["messages"])
				if len(messages) != 1 {
					t.Errorf("responses input 应转 1 条消息：%v", messages)
				}
			},
		},
		{
			name:   "gemini_generate_content -> anthropic_messages",
			source: FamilyGeminiGenerateContent, up: FamilyAnthropicMessages,
			check: func(t *testing.T, output map[string]any) {
				messages := covSlice(t, output["messages"])
				if covText(t, covMap(t, messages[0])["role"]) != "user" {
					t.Errorf("gemini -> anthropic 首条消息 = %v", messages[0])
				}
			},
		},
		{
			name:   "gemini_stream_generate -> anthropic_messages",
			source: FamilyGeminiStreamGenerate, up: FamilyAnthropicMessages,
			check: func(t *testing.T, output map[string]any) {
				if _, exists := output["messages"]; !exists {
					t.Errorf("应输出 messages：%v", output)
				}
			},
		},
		{
			name:   "anthropic_messages -> chat_completions",
			source: FamilyAnthropicMessages, up: FamilyChatCompletions,
			check: func(t *testing.T, output map[string]any) {
				if _, exists := output["messages"]; !exists {
					t.Errorf("应输出 chat messages：%v", output)
				}
			},
		},
		{
			name:   "gemini -> chat_completions",
			source: FamilyGeminiGenerateContent, up: FamilyChatCompletions,
			check: func(t *testing.T, output map[string]any) {
				if _, exists := output["messages"]; !exists {
					t.Errorf("应输出 chat messages：%v", output)
				}
			},
		},
		{
			name:   "responses -> chat_completions（codex）",
			source: FamilyResponses, up: FamilyChatCompletions,
			check: func(t *testing.T, output map[string]any) {
				if output["stream"] != true {
					t.Errorf("codex 桥应固定 stream true：%v", output)
				}
			},
		},
		{
			name:   "chat -> gemini_generate_content",
			source: FamilyChatCompletions, up: FamilyGeminiGenerateContent,
			check: func(t *testing.T, output map[string]any) {
				if _, exists := output["contents"]; !exists {
					t.Errorf("gemini 体应带 contents：%v", output)
				}
			},
		},
		{
			name:   "responses -> gemini_stream_generate",
			source: FamilyResponses, up: FamilyGeminiStreamGenerate,
			check: func(t *testing.T, output map[string]any) {
				if _, exists := output["contents"]; !exists {
					t.Errorf("gemini 体应带 contents：%v", output)
				}
			},
		},
		{
			name:   "anthropic_messages -> gemini_generate_content",
			source: FamilyAnthropicMessages, up: FamilyGeminiGenerateContent,
			check: func(t *testing.T, output map[string]any) {
				if _, exists := output["contents"]; !exists {
					t.Errorf("gemini 体应带 contents：%v", output)
				}
			},
		},
		{
			name:   "存储别名 messages 归一化",
			source: "messages", up: FamilyChatCompletions,
			check: func(t *testing.T, output map[string]any) {
				if _, exists := output["messages"]; !exists {
					t.Errorf("别名 messages 应路由到 anthropic->chat：%v", output)
				}
			},
		},
		{
			name:   "存储别名 generate_content 归一化",
			source: FamilyChatCompletions, up: "generate_content",
			check: func(t *testing.T, output map[string]any) {
				if _, exists := output["contents"]; !exists {
					t.Errorf("别名 generate_content 应路由 chat->gemini：%v", output)
				}
			},
		},
		{
			name:   "存储别名 stream_generate_content 归一化",
			source: FamilyChatCompletions, up: "stream_generate_content",
			check: func(t *testing.T, output map[string]any) {
				if _, exists := output["contents"]; !exists {
					t.Errorf("别名 stream_generate_content 应路由 chat->gemini：%v", output)
				}
			},
		},
		{
			name:   "同族组合不支持",
			source: FamilyChatCompletions, up: FamilyChatCompletions,
			errCode: "bridge_unsupported_family_pair",
		},
		{
			name:   "gemini 目标不接受 gemini 源",
			source: FamilyGeminiGenerateContent, up: FamilyGeminiGenerateContent,
			errCode: "unsupported_gemini_target_bridge_source",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output, err := BuildBridgeRequestBody(tc.source, tc.up, body, BridgeRequestBodyOptions{})
			if tc.errCode != "" {
				requireBridgeValidation(t, err, tc.errCode)
				return
			}
			if err != nil {
				t.Fatalf("分发失败：%v", err)
			}
			tc.check(t, output)
		})
	}
}

func TestCovGeminiToAnthropicBridge(t *testing.T) {
	t.Run("完整请求", func(t *testing.T) {
		body := map[string]any{
			"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "守则"}}},
			"contents": []any{
				map[string]any{"role": "user", "parts": []any{map[string]any{"text": "看图"}}},
				map[string]any{"role": "model", "parts": []any{
					map[string]any{"text": "调用"},
					map[string]any{"functionCall": map[string]any{"name": "lookup", "args": map[string]any{"q": "x"}}},
				}},
				map[string]any{"role": "user", "parts": []any{
					map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "QQ=="}},
					map[string]any{"functionResponse": map[string]any{"name": "lookup", "response": map[string]any{"ok": 1}}},
				}},
			},
			"generationConfig": map[string]any{
				"temperature": 0.6, "topP": 0.5, "maxOutputTokens": float64(80),
				"stopSequences": []any{"E"},
			},
			"tools": []any{map[string]any{"functionDeclarations": []any{
				map[string]any{"name": "lookup", "description": "d", "parameters": map[string]any{"type": "object"}},
			}}},
		}
		output, err := BuildGeminiToAnthropicMessagesBody(body, BridgeRequestBodyOptions{DefaultModel: "claude-x"})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		if covText(t, output["model"]) != "claude-x" {
			t.Errorf("model = %v，期望 DefaultModel 兜底", output["model"])
		}
		covInt(t, output["max_tokens"], 80)
		if output["temperature"] != 0.6 || output["top_p"] != 0.5 {
			t.Errorf("采样 = %v/%v", output["temperature"], output["top_p"])
		}
		if stops, ok := output["stop_sequences"].([]string); !ok || len(stops) != 1 || stops[0] != "E" {
			t.Errorf("stop_sequences = %v", output["stop_sequences"])
		}
		if covText(t, output["system"]) != "守则" {
			t.Errorf("system = %v", output["system"])
		}
		messages := covSlice(t, output["messages"])
		// user(看图) -> model(assistant 文本+tool_use) -> user(image+tool_result 合并)。
		if len(messages) != 3 {
			t.Fatalf("期望 3 条消息，实际 %d：%v", len(messages), messages)
		}
		assistant := covMap(t, messages[1])
		toolUses := covToolUseBlocks(t, covSlice(t, assistant["content"]))
		if len(toolUses) != 1 {
			t.Fatalf("期望 1 个 tool_use：%v", assistant)
		}
		if covText(t, toolUses[0]["id"]) != "toolu_lookup_0" {
			t.Errorf("gemini 工具 id 应稳定：= %v", toolUses[0]["id"])
		}
		last := covMap(t, messages[2])
		blocks := covSlice(t, last["content"])
		if len(blocks) != 2 {
			t.Fatalf("末条消息应合并 tool_result + image，实际 %v", blocks)
		}
		// functionResponse 先落成 user tool_result 消息，随后 inlineData 的
		// image block 与之同角色合并，所以 tool_result 在前。
		result := covMap(t, blocks[0])
		if covText(t, result["tool_use_id"]) != "toolu_lookup_0" {
			t.Errorf("tool_result 应复用稳定 id：%v", result)
		}
		if covText(t, result["content"]) != `{"ok":1}` {
			t.Errorf("tool_result content = %v", result["content"])
		}
		if covText(t, covMap(t, blocks[1])["type"]) != "image" {
			t.Errorf("第二个 block 应为 image：%v", blocks[1])
		}
		tools := covSlice(t, output["tools"])
		tool := covMap(t, tools[0])
		if covText(t, tool["name"]) != "lookup" || covText(t, covMap(t, tool["input_schema"])["type"]) != "object" {
			t.Errorf("tools = %v", tools)
		}
	})
	t.Run("错误分支", func(t *testing.T) {
		base := func(mutate func(map[string]any)) map[string]any {
			body := map[string]any{"contents": []any{}}
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
			{"contents 不是数组", func() map[string]any { body := base(nil); body["contents"] = "x"; return body }(), "invalid_gemini_anthropic_bridge_contents"},
			{"functionCall 缺 name", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{}}}}}
			}), "invalid_gemini_anthropic_bridge_function_call"},
			{"functionResponse 缺 name", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{}}}}}
			}), "invalid_gemini_anthropic_bridge_function_response"},
			{"inlineData 缺 data", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{
					map[string]any{"inlineData": map[string]any{"mimeType": "image/png"}},
				}}}
			}), "invalid_gemini_anthropic_bridge_inline_data"},
			{"inlineData 媒体类型不支持", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{
					map[string]any{"inlineData": map[string]any{"mimeType": "image/tiff", "data": "QQ=="}},
				}}}
			}), "unsupported_gemini_anthropic_image_media_type"},
			{"未知 part", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{map[string]any{"weird": 1}}}}
			}), "unsupported_gemini_anthropic_content_part"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := BuildGeminiToAnthropicMessagesBody(tc.body, BridgeRequestBodyOptions{})
				requireBridgeValidation(t, err, tc.code)
			})
		}
	})
	t.Run("空 contents 兜底消息与 systemInstruction 字符串", func(t *testing.T) {
		output, err := BuildGeminiToAnthropicMessagesBody(map[string]any{
			"contents":          []any{},
			"systemInstruction": "字符串守则",
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		if covText(t, output["system"]) != "字符串守则" {
			t.Errorf("system = %v", output["system"])
		}
		messages := covSlice(t, output["messages"])
		if len(messages) != 1 {
			t.Fatalf("期望兜底 1 条消息，实际 %v", messages)
		}
	})
	t.Run("geminiFunctionCallID 同名复用", func(t *testing.T) {
		ids := map[string]string{}
		first := geminiFunctionCallID(ids, "f")
		second := geminiFunctionCallID(ids, "f")
		third := geminiFunctionCallID(ids, "g")
		if first != second {
			t.Errorf("同名应复用 id：%v/%v", first, second)
		}
		if first == third {
			t.Errorf("不同名 id 不应相同：%v", first)
		}
	})
}
