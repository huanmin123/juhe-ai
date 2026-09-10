package openaicompat

// 桥接请求反向与旁支方向的补充覆盖测试：
//
//	Anthropic Messages -> Chat Completions（anthropic-openai-chat-bridge.ts）
//	Gemini GenerateContent -> Chat Completions（gemini-openai-chat-bridge.ts）
//	OpenAI Chat -> Gemini GenerateContent（gemini native 上游）
//	Codex Responses -> Chat Completions（codex-responses-chat-bridge.ts）
//
// 全部为纯函数转换，输入内联 map，输出逐字段断言。

import (
	"strings"
	"testing"
)

func TestCovAnthropicToChatErrors(t *testing.T) {
	base := func(mutate func(map[string]any)) map[string]any {
		body := map[string]any{
			"model": "claude-3",
			"messages": []any{
				map[string]any{"role": "user", "content": "hi"},
			},
		}
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
		{
			name: "messages 不是数组",
			body: func() map[string]any { body := base(nil); body["messages"] = "no"; return body }(),
			code: "invalid_anthropic_chat_bridge_messages",
		},
		{
			name: "不支持 thinking 字段",
			body: base(func(body map[string]any) { body["thinking"] = map[string]any{} }),
			code: "unsupported_anthropic_messages_chat_bridge_fields",
		},
		{
			name: "不支持 top_k 字段",
			body: base(func(body map[string]any) { body["top_k"] = float64(3) }),
			code: "unsupported_anthropic_messages_chat_bridge_fields",
		},
		{
			name: "system block 带 cache_control",
			body: base(func(body map[string]any) {
				body["system"] = []any{map[string]any{"type": "text", "text": "a", "cache_control": map[string]any{}}}
			}),
			code: "unsupported_anthropic_messages_cache_control",
		},
		{
			name: "tools 带 cache_control",
			body: base(func(body map[string]any) {
				body["tools"] = []any{map[string]any{"name": "f", "cache_control": map[string]any{}}}
			}),
			code: "unsupported_anthropic_messages_cache_control",
		},
		{
			name: "消息 content 带 cache_control",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "text", "text": "a", "cache_control": map[string]any{}},
				}}}
			}),
			code: "unsupported_anthropic_messages_cache_control",
		},
		{
			name: "system 非字符串非数组",
			body: base(func(body map[string]any) { body["system"] = 7 }),
			code: "invalid_anthropic_chat_bridge_system",
		},
		{
			name: "system 数组含非 text block",
			body: base(func(body map[string]any) {
				body["system"] = []any{map[string]any{"type": "image", "source": map[string]any{}}}
			}),
			code: "unsupported_anthropic_messages_system_block",
		},
		{
			name: "非法消息 role",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "system", "content": "x"}}
			}),
			code: "invalid_anthropic_chat_bridge_message_role",
		},
		{
			name: "user content 非字符串非数组",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": 9}}
			}),
			code: "invalid_anthropic_chat_bridge_user_content",
		},
		{
			name: "user 未知 block",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "document", "source": map[string]any{}},
				}}}
			}),
			code: "unsupported_anthropic_messages_content_block",
		},
		{
			name: "tool_result 缺 tool_use_id",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "tool_result", "content": "x"},
				}}}
			}),
			code: "invalid_anthropic_chat_bridge_tool_result",
		},
		{
			name: "image source 缺 base64 data",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png"}},
				}}}
			}),
			code: "invalid_anthropic_chat_bridge_image",
		},
		{
			name: "image source 类型不支持",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "image", "source": map[string]any{"type": "file", "file_id": "f"}},
				}}}
			}),
			code: "unsupported_anthropic_messages_image_source",
		},
		{
			name: "image source 缺 url",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "image", "source": map[string]any{"type": "url"}},
				}}}
			}),
			code: "unsupported_anthropic_messages_image_source",
		},
		{
			name: "assistant 缺少 content",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "assistant", "content": 5}}
			}),
			code: "invalid_anthropic_chat_bridge_assistant_content",
		},
		{
			name: "assistant thinking block 拒绝",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "thinking", "thinking": "zzz"},
				}}}
			}),
			code: "unsupported_anthropic_messages_thinking_block",
		},
		{
			name: "assistant 未知 block 拒绝",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "image"},
				}}}
			}),
			code: "unsupported_anthropic_messages_content_block",
		},
		{
			name: "assistant tool_use 缺 id",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "f", "input": map[string]any{}},
				}}}
			}),
			code: "invalid_anthropic_chat_bridge_tool_use",
		},
		{
			name: "server tool 拒绝",
			body: base(func(body map[string]any) {
				body["tools"] = []any{map[string]any{"type": "web_search", "name": "s"}}
			}),
			code: "unsupported_anthropic_messages_server_tool",
		},
		{
			name: "tool 缺 name",
			body: base(func(body map[string]any) {
				body["tools"] = []any{map[string]any{"input_schema": map[string]any{}}}
			}),
			code: "invalid_anthropic_chat_bridge_tool",
		},
		{
			name: "tools 非数组",
			body: base(func(body map[string]any) { body["tools"] = "no" }),
			code: "invalid_anthropic_chat_bridge_tools",
		},
		{
			name: "tool_choice 非对象",
			body: base(func(body map[string]any) {
				body["tools"] = []any{map[string]any{"name": "f"}}
				body["tool_choice"] = "auto"
			}),
			code: "invalid_anthropic_chat_bridge_tool_choice",
		},
		{
			name: "tool_choice type=tool 缺 name",
			body: base(func(body map[string]any) {
				body["tools"] = []any{map[string]any{"name": "f"}}
				body["tool_choice"] = map[string]any{"type": "tool"}
			}),
			code: "invalid_anthropic_chat_bridge_tool_choice",
		},
		{
			name: "tool_choice 类型不支持",
			body: base(func(body map[string]any) {
				body["tools"] = []any{map[string]any{"name": "f"}}
				body["tool_choice"] = map[string]any{"type": "bogus"}
			}),
			code: "unsupported_anthropic_messages_tool_choice",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildAnthropicMessagesToChatCompletionsBody(tc.body, BridgeRequestBodyOptions{})
			requireBridgeValidation(t, err, tc.code)
		})
	}
}

func TestCovAnthropicToChatFullShape(t *testing.T) {
	body := map[string]any{
		"model":          "claude-3",
		"max_tokens":     float64(300),
		"temperature":    0.2,
		"top_p":          0.8,
		"stop_sequences": []any{"A", "B"},
		"metadata":       map[string]any{"user_id": "u-1"},
		"system":         []any{map[string]any{"type": "text", "text": "S1"}, map[string]any{"type": "text", "text": "S2"}},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "看"},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "QQ=="}},
				map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://x/y.png"}},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "调工具"},
				map[string]any{"type": "tool_use", "id": "t1", "name": "lookup", "input": map[string]any{"k": "v"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": []any{
					map[string]any{"type": "text", "text": "r1"},
					map[string]any{"type": "text", "text": "r2"},
				}},
			}},
		},
		"tools":       []any{map[string]any{"name": "lookup", "description": "d", "input_schema": map[string]any{"type": "object"}}},
		"tool_choice": map[string]any{"type": "auto", "disable_parallel_tool_use": true},
	}
	output, err := BuildAnthropicMessagesToChatCompletionsBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if covText(t, output["model"]) != "claude-3" {
		t.Errorf("model = %v", output["model"])
	}
	covInt(t, output["max_tokens"], 300)
	if covNumber(t, output["temperature"]) != 0.2 || covNumber(t, output["top_p"]) != 0.8 {
		t.Errorf("采样参数 = %v/%v", output["temperature"], output["top_p"])
	}
	// 单元素 stop 折叠为字符串、多元素为 []string；这里有两个。
	stop, ok := output["stop"].([]string)
	if !ok || len(stop) != 2 || stop[0] != "A" || stop[1] != "B" {
		t.Errorf("stop = %v，期望 [A B]", output["stop"])
	}
	if covText(t, output["user"]) != "u-1" {
		t.Errorf("user = %v，期望 metadata.user_id 透传", output["user"])
	}
	messages := covSlice(t, output["messages"])
	if len(messages) != 4 {
		t.Fatalf("期望 4 条消息（system/user 图文段/assistant/tool），实际 %d：%v", len(messages), messages)
	}
	systemMessage := covMap(t, messages[0])
	if covText(t, systemMessage["role"]) != "system" || covText(t, systemMessage["content"]) != "S1\nS2" {
		t.Errorf("system 消息 = %v", systemMessage)
	}
	// user 图文段：非文本存在 -> content 保持数组。
	imageMessage := covMap(t, messages[1])
	parts := covSlice(t, imageMessage["content"])
	if len(parts) != 3 {
		t.Fatalf("user 图文段期望 3 个 part，实际 %v", parts)
	}
	imagePart := covMap(t, parts[1])
	if covText(t, imagePart["type"]) != "image_url" {
		t.Fatalf("part1 = %v，期望 image_url", imagePart)
	}
	if covText(t, covMap(t, imagePart["image_url"])["url"]) != "data:image/png;base64,QQ==" {
		t.Errorf("base64 图 URL = %v", imagePart["image_url"])
	}
	// assistant：文本 + tool_calls。
	assistant := covMap(t, messages[2])
	if covText(t, assistant["content"]) != "调工具" {
		t.Errorf("assistant content = %v", assistant["content"])
	}
	toolCalls := covSlice(t, assistant["tool_calls"])
	call := covMap(t, toolCalls[0])
	if covText(t, call["id"]) != "t1" || covText(t, call["type"]) != "function" {
		t.Fatalf("tool_call = %v", call)
	}
	fn := covMap(t, call["function"])
	if covText(t, fn["name"]) != "lookup" || covText(t, fn["arguments"]) != `{"k":"v"}` {
		t.Errorf("tool_call.function = %v", fn)
	}
	toolMessage := covMap(t, messages[3])
	if covText(t, toolMessage["role"]) != "tool" || covText(t, toolMessage["tool_call_id"]) != "t1" {
		t.Fatalf("tool 消息 = %v", toolMessage)
	}
	// tool_result 数组文本折叠为 "r1\nr2"。
	if covText(t, toolMessage["content"]) != "r1\nr2" {
		t.Errorf("tool content = %v，期望 r1\\nr2", toolMessage["content"])
	}
	if _, exists := output["parallel_tool_calls"]; !exists {
		t.Fatalf("disable_parallel_tool_use 应输出 parallel_tool_calls")
	}
	if output["parallel_tool_calls"] != false {
		t.Errorf("parallel_tool_calls = %v，期望 false", output["parallel_tool_calls"])
	}
	tools := covSlice(t, output["tools"])
	tool := covMap(t, tools[0])
	if covText(t, covText(t, covMap(t, tool["function"])["name"])) != "lookup" {
		t.Errorf("tools = %v", tools)
	}
	choice := covText(t, output["tool_choice"])
	if choice != "auto" {
		t.Errorf("tool_choice = %v，期望 auto", output["tool_choice"])
	}
}

func TestCovAnthropicToChatVariants(t *testing.T) {
	t.Run("system 字符串与纯文本块合并", func(t *testing.T) {
		output, err := BuildAnthropicMessagesToChatCompletionsBody(map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			"system":   "直接字符串",
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		if covText(t, covMap(t, messages[0])["content"]) != "直接字符串" {
			t.Errorf("system 消息 = %v", messages[0])
		}
	})
	t.Run("纯文本 user 块折叠为字符串", func(t *testing.T) {
		output, err := BuildAnthropicMessagesToChatCompletionsBody(map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "a"},
				map[string]any{"type": "text", "text": "b"},
			}}},
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		if covText(t, covMap(t, messages[0])["content"]) != "ab" {
			t.Errorf("纯文本块应折叠为字符串，实际 %v", messages[0])
		}
	})
	t.Run("assistant 纯 tool_calls 时 content 为 null", func(t *testing.T) {
		output, err := BuildAnthropicMessagesToChatCompletionsBody(map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "q"},
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "id": "t", "name": "f", "input": nil},
				}},
			},
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		assistant := covMap(t, messages[1])
		if assistant["content"] != nil {
			t.Errorf("assistant content = %v，期望 nil", assistant["content"])
		}
		toolCalls := covSlice(t, assistant["tool_calls"])
		fn := covMap(t, covMap(t, toolCalls[0])["function"])
		if covText(t, fn["arguments"]) != "{}" {
			t.Errorf("缺 input 应折叠为 {}，实际 %v", fn["arguments"])
		}
	})
	t.Run("tool_choice 各形态", func(t *testing.T) {
		mk := func(choice any) map[string]any {
			output, err := BuildAnthropicMessagesToChatCompletionsBody(map[string]any{
				"messages":    []any{map[string]any{"role": "user", "content": "hi"}},
				"tools":       []any{map[string]any{"name": "f", "input_schema": "bad-schema"}},
				"tool_choice": choice,
			}, BridgeRequestBodyOptions{})
			if err != nil {
				t.Fatalf("构造失败：%v", err)
			}
			return output
		}
		if got := mk(map[string]any{"type": "any"})["tool_choice"]; got != "required" {
			t.Errorf("any -> required，实际 %v", got)
		}
		if got := mk(map[string]any{"type": "none"})["tool_choice"]; got != "none" {
			t.Errorf("none -> none，实际 %v", got)
		}
		if got := mk(map[string]any{})["tool_choice"]; got != "auto" {
			t.Errorf("缺 type -> auto，实际 %v", got)
		}
		toolChoice := covMap(t, mk(map[string]any{"type": "tool", "name": "f"})["tool_choice"])
		if covText(t, covMap(t, toolChoice["function"])["name"]) != "f" {
			t.Errorf("tool 选择映射 = %v", toolChoice)
		}
		// input_schema 非对象时替换默认 schema。
		tools := covSlice(t, mk(nil)["tools"])
		schema := covMap(t, covMap(t, covMap(t, tools[0])["function"])["parameters"])
		if covText(t, schema["type"]) != "object" {
			t.Errorf("默认 schema = %v", schema)
		}
	})
	t.Run("stop 单值折叠与 DefaultModel", func(t *testing.T) {
		output, err := BuildAnthropicMessagesToChatCompletionsBody(map[string]any{
			"messages":       []any{map[string]any{"role": "user", "content": "hi"}},
			"stop_sequences": []any{"A"},
		}, BridgeRequestBodyOptions{DefaultModel: "claude-fallback"})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		if output["model"] != "claude-fallback" {
			t.Errorf("model = %v，期望 DefaultModel 兜底", output["model"])
		}
		if output["stop"] != "A" {
			t.Errorf("单 stop 应折叠为字符串，实际 %v", output["stop"])
		}
	})
	t.Run("assistant 空字符串 content", func(t *testing.T) {
		output, err := BuildAnthropicMessagesToChatCompletionsBody(map[string]any{
			"messages": []any{map[string]any{"role": "assistant", "content": ""}},
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		if covText(t, covMap(t, messages[0])["content"]) != "" {
			t.Errorf("assistant 字符串 content 应原样保留，实际 %v", messages[0])
		}
	})
}

func TestCovGeminiToChatFlow(t *testing.T) {
	t.Run("完整请求", func(t *testing.T) {
		body := map[string]any{
			"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "S1"}, map[string]any{"text": "S2"}}},
			"contents": []any{
				map[string]any{"role": "user", "parts": []any{
					map[string]any{"text": "看图"},
					map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "QQ=="}},
					map[string]any{"fileData": map[string]any{"mimeType": "image/jpeg", "fileUri": "https://x/a.jpg"}},
				}},
				map[string]any{"role": "model", "parts": []any{
					map[string]any{"text": "调"},
					map[string]any{"functionCall": map[string]any{"name": "lookup", "args": map[string]any{"q": "x"}}},
				}},
				map[string]any{"role": "user", "parts": []any{
					map[string]any{"functionResponse": map[string]any{"name": "lookup", "response": map[string]any{"ok": 1}}},
				}},
			},
			"generationConfig": map[string]any{
				"temperature": 0.4, "topP": 0.7, "maxOutputTokens": float64(90),
				"stopSequences": []any{"END"}, "candidateCount": float64(2), "responseMimeType": "application/json",
			},
			"tools":      []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "lookup", "description": "d"}}}},
			"toolConfig": map[string]any{"functionCallingConfig": map[string]any{"mode": "ANY", "allowedFunctionNames": []any{"lookup"}}},
		}
		output, err := BuildGeminiGenerateContentToChatCompletionsBody(body, BridgeRequestBodyOptions{DefaultModel: "g-fallback"})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		if len(messages) != 4 {
			t.Fatalf("期望 4 条消息，实际 %d：%v", len(messages), messages)
		}
		system := covMap(t, messages[0])
		if covText(t, system["content"]) != "S1\nS2" {
			t.Errorf("system = %v", system)
		}
		userParts := covSlice(t, covMap(t, messages[1])["content"])
		if len(userParts) != 3 {
			t.Fatalf("user parts = %v", userParts)
		}
		inline := covMap(t, userParts[1])
		if covText(t, covMap(t, inline["image_url"])["url"]) != "data:image/png;base64,QQ==" {
			t.Errorf("inlineData URL = %v", inline)
		}
		filePart := covMap(t, userParts[2])
		if covText(t, covMap(t, filePart["image_url"])["url"]) != "https://x/a.jpg" {
			t.Errorf("fileData URL = %v", filePart)
		}
		assistant := covMap(t, messages[2])
		calls := covSlice(t, assistant["tool_calls"])
		callFn := covMap(t, covMap(t, calls[0])["function"])
		if covText(t, callFn["name"]) != "lookup" || covText(t, callFn["arguments"]) != `{"q":"x"}` {
			t.Errorf("functionCall -> tool_call = %v", callFn)
		}
		if !strings.HasPrefix(covText(t, calls[0].(map[string]any)["id"]), "call_lookup_") {
			t.Errorf("tool_call id = %v，期望 call_lookup_ 前缀", calls[0])
		}
		toolMessage := covMap(t, messages[3])
		if covText(t, toolMessage["role"]) != "tool" {
			t.Fatalf("functionResponse -> tool 消息 = %v", toolMessage)
		}
		if covText(t, toolMessage["content"]) != `{"ok":1}` {
			t.Errorf("tool content = %v，期望 JSON 串", toolMessage["content"])
		}
		if output["temperature"] != 0.4 || output["top_p"] != 0.7 {
			t.Errorf("generationConfig 采样 = %v/%v", output["temperature"], output["top_p"])
		}
		covInt(t, output["max_tokens"], 90)
		covInt(t, output["n"], 2)
		format := covMap(t, output["response_format"])
		if covText(t, format["type"]) != "json_object" {
			t.Errorf("response_format = %v", output["response_format"])
		}
		tools := covSlice(t, output["tools"])
		declaredFn := covMap(t, covMap(t, tools[0])["function"])
		if covText(t, declaredFn["name"]) != "lookup" {
			t.Errorf("tools = %v", tools)
		}
		// ANY + 单个 allowedFunctionNames -> 具名 function。
		choice := covMap(t, output["tool_choice"])
		if covText(t, covMap(t, choice["function"])["name"]) != "lookup" {
			t.Errorf("tool_choice = %v", output["tool_choice"])
		}
	})
	t.Run("generationConfig 文本 mime 不产出 response_format", func(t *testing.T) {
		output, err := BuildGeminiGenerateContentToChatCompletionsBody(map[string]any{
			"contents":         []any{},
			"generationConfig": map[string]any{"responseMimeType": "text/plain", "candidateCount": float64(0)},
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		if _, exists := output["response_format"]; exists {
			t.Errorf("text/plain 不应产出 response_format：%v", output)
		}
		if _, exists := output["n"]; exists {
			t.Errorf("candidateCount=0 不应产出 n：%v", output)
		}
	})
	t.Run("toolConfig 模式映射", func(t *testing.T) {
		cases := []struct {
			name string
			mode any
			want any
		}{
			{"NONE", "NONE", "none"},
			{"AUTO 缺省", nil, "auto"},
			{"ANY 空 allowed", "ANY", "required"},
			{"ANY 多 allowed", "ANY", "required"},
			{"未知模式", "BOGUS", nil},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				config := map[string]any{"mode": tc.mode}
				if tc.name == "ANY 多 allowed" {
					config["allowedFunctionNames"] = []any{"a", "b"}
				}
				body := map[string]any{
					"contents":   []any{},
					"tools":      []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "a"}}}},
					"toolConfig": map[string]any{"functionCallingConfig": config},
				}
				output, err := BuildGeminiGenerateContentToChatCompletionsBody(body, BridgeRequestBodyOptions{})
				if err != nil {
					t.Fatalf("构造失败：%v", err)
				}
				if tc.want == nil {
					if _, exists := output["tool_choice"]; exists {
						t.Errorf("未知模式不应输出 tool_choice：%v", output["tool_choice"])
					}
					return
				}
				if output["tool_choice"] != tc.want {
					t.Errorf("mode=%v -> tool_choice %v，期望 %v", tc.mode, output["tool_choice"], tc.want)
				}
			})
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
			{"contents 不是数组", func() map[string]any { body := base(nil); body["contents"] = 1; return body }(), "invalid_gemini_chat_bridge_contents"},
			{"cachedContent", base(func(body map[string]any) { body["cachedContent"] = "c" }), "unsupported_gemini_cached_content"},
			{"thinkingConfig", base(func(body map[string]any) {
				body["generationConfig"] = map[string]any{"thinkingConfig": map[string]any{}}
			}), "unsupported_gemini_thinking_config"},
			{"systemInstruction 类型非法", base(func(body map[string]any) {
				body["systemInstruction"] = 5
			}), "invalid_gemini_chat_bridge_system_instruction"},
			{"systemInstruction 非 text part", base(func(body map[string]any) {
				body["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{}}}}
			}), "unsupported_gemini_system_instruction_part"},
			{"非法 role", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "assistant", "parts": []any{map[string]any{"text": "x"}}}}
			}), "invalid_gemini_chat_bridge_role"},
			{"user 携带 functionCall", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{
					map[string]any{"functionCall": map[string]any{"name": "f"}},
				}}}
			}), "invalid_gemini_chat_bridge_function_part"},
			{"user 未知 part", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{
					map[string]any{"customField": 1},
				}}}
			}), "unsupported_gemini_content_part"},
			{"model 携带 executableCode", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "model", "parts": []any{
					map[string]any{"executableCode": map[string]any{}},
				}}}
			}), "unsupported_gemini_content_part"},
			{"functionCall 缺 name", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "model", "parts": []any{
					map[string]any{"functionCall": map[string]any{}},
				}}}
			}), "invalid_gemini_chat_bridge_function_call"},
			{"functionResponse 缺 name", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{
					map[string]any{"functionResponse": map[string]any{}},
				}}}
			}), "invalid_gemini_chat_bridge_function_response"},
			{"inlineData 缺 data", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{
					map[string]any{"inlineData": map[string]any{"mimeType": "image/png"}},
				}}}
			}), "invalid_gemini_chat_bridge_inline_data"},
			{"inlineData 非图片", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{
					map[string]any{"inlineData": map[string]any{"mimeType": "audio/wav", "data": "QQ=="}},
				}}}
			}), "unsupported_gemini_inline_data"},
			{"fileData 缺 fileUri", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{
					map[string]any{"fileData": map[string]any{"mimeType": "image/png"}},
				}}}
			}), "invalid_gemini_chat_bridge_file_data"},
			{"fileData 非图片", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{
					map[string]any{"fileData": map[string]any{"mimeType": "text/plain", "fileUri": "https://x/a.txt"}},
				}}}
			}), "unsupported_gemini_file_data"},
			{"fileData 非公开 URL", base(func(body map[string]any) {
				body["contents"] = []any{map[string]any{"role": "user", "parts": []any{
					map[string]any{"fileData": map[string]any{"mimeType": "image/png", "fileUri": "file:///x/a.png"}},
				}}}
			}), "unsupported_gemini_file_data"},
			{"原生工具不支持", base(func(body map[string]any) {
				body["contents"] = []any{}
				body["tools"] = []any{map[string]any{"googleSearch": map[string]any{}}}
			}), "unsupported_gemini_native_tools"},
			{"tools 非数组", base(func(body map[string]any) {
				body["contents"] = []any{}
				body["tools"] = 3
			}), "invalid_gemini_chat_bridge_tools"},
			{"declaration 缺 name", base(func(body map[string]any) {
				body["contents"] = []any{}
				body["tools"] = []any{map[string]any{"functionDeclarations": []any{map[string]any{}}}}
			}), "invalid_gemini_chat_bridge_function_declaration"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := BuildGeminiGenerateContentToChatCompletionsBody(tc.body, BridgeRequestBodyOptions{})
				requireBridgeValidation(t, err, tc.code)
			})
		}
	})
	t.Run("functionResponse 与相邻用户段混排", func(t *testing.T) {
		body := map[string]any{
			"contents": []any{map[string]any{"role": "user", "parts": []any{
				map[string]any{"text": "前文"},
				map[string]any{"functionResponse": map[string]any{"name": "lookup", "response": nil}},
				map[string]any{"text": "后文"},
			}}},
		}
		output, err := BuildGeminiGenerateContentToChatCompletionsBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		// 前文(user) -> tool -> 后文(user)。
		if len(messages) != 3 {
			t.Fatalf("期望 3 条消息，实际 %d：%v", len(messages), messages)
		}
		if covText(t, covMap(t, messages[0])["content"]) != "前文" {
			t.Errorf("flush 前文 = %v", messages[0])
		}
		tool := covMap(t, messages[1])
		if covText(t, tool["content"]) != "{}" {
			t.Errorf("response 缺省折叠 {}，实际 %v", tool["content"])
		}
		if covText(t, covMap(t, messages[2])["content"]) != "后文" {
			t.Errorf("flush 后文 = %v", messages[2])
		}
	})
}

func TestCovChatToGeminiFlow(t *testing.T) {
	t.Run("完整请求", func(t *testing.T) {
		body := map[string]any{
			"model": "gpt-4",
			"messages": []any{
				map[string]any{"role": "system", "content": "系统"},
				map[string]any{"role": "developer", "content": "开发"},
				map[string]any{"role": "user", "content": "问题"},
				map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
					map[string]any{"id": "c1", "function": map[string]any{"name": "lookup", "arguments": `{"q":"x"}`}},
					map[string]any{"id": "c2", "function": map[string]any{"name": "", "arguments": "{}"}},
				}},
				map[string]any{"role": "tool", "tool_call_id": "lookup", "content": "结果"},
			},
			"temperature": 0.1,
			"top_p":       0.6,
			"stop":        []any{"S"},
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "description": "d", "parameters": map[string]any{"type": "object"}}},
				map[string]any{"type": "function", "function": map[string]any{}},
			},
			"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}},
		}
		output, err := BuildOpenAIChatToGeminiBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		if covText(t, covMap(t, output["systemInstruction"])["parts"].([]any)[0].(map[string]any)["text"].(string)) != "系统\n\n开发" {
			t.Errorf("systemInstruction = %v", output["systemInstruction"])
		}
		contents := covSlice(t, output["contents"])
		if len(contents) != 3 {
			t.Fatalf("期望 3 个 content（user/model/user-functionResponse），实际 %d：%v", len(contents), contents)
		}
		modelContent := covMap(t, contents[1])
		if covText(t, modelContent["role"]) != "model" {
			t.Fatalf("assistant -> model role，实际 %v", modelContent)
		}
		parts := covSlice(t, modelContent["parts"])
		call := covMap(t, covMap(t, parts[0])["functionCall"])
		if covText(t, call["name"]) != "lookup" {
			t.Errorf("functionCall = %v", parts[0])
		}
		toolContent := covMap(t, contents[2])
		response := covMap(t, covMap(t, covSlice(t, toolContent["parts"])[0])["functionResponse"])
		if covText(t, response["name"]) != "lookup" || covText(t, covMap(t, response["response"])["result"]) != "结果" {
			t.Errorf("tool -> functionResponse = %v", toolContent)
		}
		config := covMap(t, output["generationConfig"])
		if _, exists := config["maxOutputTokens"]; exists {
			t.Errorf("无 max_tokens 不应产出 maxOutputTokens：%v", config)
		}
		if config["temperature"] != 0.1 || config["topP"] != 0.6 {
			t.Errorf("generationConfig = %v", config)
		}
		if stops, ok := config["stopSequences"].([]string); !ok || len(stops) != 1 || stops[0] != "S" {
			t.Errorf("stopSequences = %v", config["stopSequences"])
		}
		declaredTools := covSlice(t, output["tools"])
		declarations := covSlice(t, covMap(t, declaredTools[0])["functionDeclarations"])
		if len(declarations) != 1 {
			t.Fatalf("无名 function 应被跳过，实际 %v", declaredTools)
		}
		toolConfig := covMap(t, output["toolConfig"])
		callingConfig := covMap(t, toolConfig["functionCallingConfig"])
		if covText(t, callingConfig["mode"]) != "ANY" {
			t.Errorf("toolConfig = %v", toolConfig)
		}
		if allowed, ok := callingConfig["allowedFunctionNames"].([]string); !ok || len(allowed) != 1 || allowed[0] != "lookup" {
			t.Errorf("allowedFunctionNames = %v", callingConfig["allowedFunctionNames"])
		}
	})
	t.Run("max_tokens 优先级与 MaxTokens 选项", func(t *testing.T) {
		pick := int64(123)
		cases := []struct {
			name string
			body map[string]any
			want int64
		}{
			{"max_tokens 优先", map[string]any{"messages": []any{}, "max_tokens": float64(11), "max_completion_tokens": float64(22)}, 11},
			{"max_completion_tokens 次之", map[string]any{"messages": []any{}, "max_completion_tokens": float64(22)}, 22},
			{"options.MaxTokens 兜底", map[string]any{"messages": []any{}}, 123},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				output, err := BuildOpenAIChatToGeminiBody(tc.body, BridgeRequestBodyOptions{MaxTokens: &pick})
				if err != nil {
					t.Fatalf("构造失败：%v", err)
				}
				config := covMap(t, output["generationConfig"])
				covInt(t, config["maxOutputTokens"], tc.want)
			})
		}
	})
	t.Run("tool_choice 字符串映射", func(t *testing.T) {
		cases := []struct {
			in   string
			want string
		}{{"none", "NONE"}, {"required", "ANY"}, {"auto", "AUTO"}}
		for _, tc := range cases {
			body := map[string]any{
				"messages":    []any{},
				"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "f"}}},
				"tool_choice": tc.in,
			}
			output, err := BuildOpenAIChatToGeminiBody(body, BridgeRequestBodyOptions{})
			if err != nil {
				t.Fatalf("构造失败：%v", err)
			}
			config := covMap(t, covMap(t, output["toolConfig"])["functionCallingConfig"])
			if covText(t, config["mode"]) != tc.want {
				t.Errorf("tool_choice %s -> mode %v，期望 %s", tc.in, config["mode"], tc.want)
			}
		}
	})
	t.Run("空消息兜底", func(t *testing.T) {
		output, err := BuildOpenAIChatToGeminiBody(map[string]any{"messages": []any{}}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		contents := covSlice(t, output["contents"])
		if len(contents) != 1 {
			t.Fatalf("期望兜底 1 个 content，实际 %v", contents)
		}
		parts := covSlice(t, covMap(t, contents[0])["parts"])
		if covText(t, covMap(t, parts[0])["text"]) != "" {
			t.Errorf("兜底 part = %v", parts[0])
		}
		if _, exists := output["systemInstruction"]; exists {
			t.Errorf("无 system 不应产出 systemInstruction：%v", output)
		}
	})
	t.Run("assistant 纯文本", func(t *testing.T) {
		output, err := BuildOpenAIChatToGeminiBody(map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "q"},
				map[string]any{"role": "assistant", "content": "a"},
			},
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		contents := covSlice(t, output["contents"])
		model := covMap(t, contents[1])
		parts := covSlice(t, model["parts"])
		if covText(t, covMap(t, parts[0])["text"]) != "a" {
			t.Errorf("assistant 文本 part = %v", parts[0])
		}
	})
}

func TestCovCodexToChatFlow(t *testing.T) {
	t.Run("完整请求", func(t *testing.T) {
		longName := strings.Repeat("n", 70)
		body := map[string]any{
			"model": "codex",
			"input": []any{
				map[string]any{"type": "message", "role": "developer", "content": "规则一"},
				map[string]any{"type": "message", "role": "system", "content": "规则二"},
				map[string]any{"type": "message", "role": "user", "content": []any{
					map[string]any{"type": "input_text", "text": "问题"},
				}},
				map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{"q":1}`},
				map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "输出"},
				map[string]any{"type": "reasoning", "summary": "推理"},
				map[string]any{"type": "unknown_item", "x": 1},
			},
			"tools": []any{
				map[string]any{"type": "function", "name": "lookup", "description": "d", "parameters": map[string]any{"type": "object"}},
				map[string]any{"type": "function", "name": "lookup"},
				map[string]any{"type": "function", "name": longName},
				map[string]any{"type": "browser"},
			},
			"tool_choice":         map[string]any{"type": "function", "name": "lookup"},
			"parallel_tool_calls": true,
			"prompt_cache_key":    "cache-1",
			"service_tier":        "  priority  ",
			"reasoning":           map[string]any{"effort": "high"},
			"max_output_tokens":   float64(321),
			"temperature":         0.3,
			"top_p":               0.4,
		}
		output, err := BuildCodexResponsesToChatCompletionsBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		if covText(t, output["model"]) != "codex" {
			t.Errorf("model = %v", output["model"])
		}
		if output["stream"] != true {
			t.Errorf("codex 桥固定 stream true，实际 %v", output["stream"])
		}
		if output["prompt_cache_key"] != "cache-1" {
			t.Errorf("prompt_cache_key = %v", output["prompt_cache_key"])
		}
		if output["service_tier"] != "priority" {
			t.Errorf("service_tier 应去空白，实际 %v", output["service_tier"])
		}
		if output["reasoning_effort"] != "high" {
			t.Errorf("reasoning_effort = %v", output["reasoning_effort"])
		}
		covInt(t, output["max_tokens"], 321)
		if output["temperature"] != 0.3 || output["top_p"] != 0.4 {
			t.Errorf("采样 = %v/%v", output["temperature"], output["top_p"])
		}
		messages := covSlice(t, output["messages"])
		// 首条是 browser 工具的系统提示；developer+system 相邻合并。
		first := covMap(t, messages[0])
		if covText(t, first["role"]) != "system" || !strings.Contains(covText(t, first["content"]), "browser") {
			t.Fatalf("首条应为不支持工具系统提示：%v", first)
		}
		second := covMap(t, messages[1])
		if covText(t, second["content"]) != "规则一\n\n规则二" {
			t.Errorf("相邻 system 应合并：=%v", second)
		}
		var assistant, tool map[string]any
		for _, item := range messages {
			record := covMap(t, item)
			switch covText(t, record["role"]) {
			case "assistant":
				assistant = record
			case "tool":
				tool = record
			}
		}
		if assistant == nil || tool == nil {
			t.Fatalf("缺少 assistant/tool 消息：%v", messages)
		}
		calls := covSlice(t, assistant["tool_calls"])
		callFn := covMap(t, covMap(t, calls[0])["function"])
		if covText(t, callFn["name"]) != "lookup" || covText(t, callFn["arguments"]) != `{"q":1}` {
			t.Errorf("function_call -> tool_calls = %v", callFn)
		}
		if covText(t, tool["content"]) != "输出" {
			t.Errorf("function_call_output -> tool content = %v", tool["content"])
		}
		var reasoningUser map[string]any
		for _, item := range messages {
			record := covMap(t, item)
			if text, ok := record["content"].(string); ok && strings.HasPrefix(text, "[reasoning] 推理") {
				reasoningUser = record
			}
		}
		if reasoningUser == nil {
			t.Errorf("reasoning item 应转 user 提示：%v", messages)
		}
		tools := covSlice(t, output["tools"])
		if len(tools) != 3 {
			t.Fatalf("期望 3 个 chat 工具（重名去重 + 截断），实际 %d：%v", len(tools), tools)
		}
		names := []string{}
		for _, item := range tools {
			names = append(names, covText(t, covMap(t, covMap(t, item)["function"])["name"]))
		}
		if names[1] != "lookup_2" {
			t.Errorf("重名工具应加后缀，实际 %v", names)
		}
		if names[2] != longName[:64] {
			t.Errorf("超长名应截断 64，实际 %q", names[2])
		}
		if output["parallel_tool_calls"] != true {
			t.Errorf("parallel_tool_calls = %v", output["parallel_tool_calls"])
		}
		choice := covMap(t, output["tool_choice"])
		if covText(t, covMap(t, choice["function"])["name"]) != "lookup" {
			t.Errorf("tool_choice = %v", output["tool_choice"])
		}
	})
	t.Run("缺 input 报错", func(t *testing.T) {
		_, err := BuildCodexResponsesToChatCompletionsBody(map[string]any{}, BridgeRequestBodyOptions{})
		requireBridgeValidation(t, err, "invalid_codex_chat_bridge_input")
	})
	t.Run("tool_choice 形态", func(t *testing.T) {
		build := func(choice any) map[string]any {
			output, err := BuildCodexResponsesToChatCompletionsBody(map[string]any{
				"input":       "hi",
				"tools":       []any{map[string]any{"type": "function", "name": "f"}},
				"tool_choice": choice,
			}, BridgeRequestBodyOptions{})
			if err != nil {
				t.Fatalf("构造失败：%v", err)
			}
			return output
		}
		if got := build("none")["tool_choice"]; got != "none" {
			t.Errorf("none = %v", got)
		}
		if got := build("required")["tool_choice"]; got != "required" {
			t.Errorf("required = %v", got)
		}
		if got := build("bogus")["tool_choice"]; got != nil {
			t.Errorf("未知字符串应忽略，实际 %v", got)
		}
		if got := build(map[string]any{"type": "function"})["tool_choice"]; got != nil {
			t.Errorf("function 缺 name 应忽略，实际 %v", got)
		}
		if got := build(42)["tool_choice"]; got != nil {
			t.Errorf("非字符串非对象应忽略，实际 %v", got)
		}
	})
	t.Run("max_completion_tokens 兜底与字符串 input", func(t *testing.T) {
		output, err := BuildCodexResponsesToChatCompletionsBody(map[string]any{
			"input":                 "直接字符串",
			"max_completion_tokens": float64(64),
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		if covText(t, covMap(t, messages[0])["content"]) != "直接字符串" {
			t.Errorf("字符串 input = %v", messages[0])
		}
		covInt(t, output["max_tokens"], 64)
	})
	t.Run("function_call 无 call_id 时以 name 兜底", func(t *testing.T) {
		output, err := BuildCodexResponsesToChatCompletionsBody(map[string]any{
			"input": []any{
				map[string]any{"type": "function_call", "name": "solo", "arguments": nil},
			},
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		assistant := covMap(t, messages[0])
		calls := covSlice(t, assistant["tool_calls"])
		if covText(t, covMap(t, calls[0])["id"]) != "solo" {
			t.Errorf("call_id 兜底 name，实际 %v", calls[0])
		}
	})
	t.Run("CodexResponsesChatBridgeToolAdaptersFromClientBody", func(t *testing.T) {
		if adapters := CodexResponsesChatBridgeToolAdaptersFromClientBody(nil); len(adapters) != 0 {
			t.Errorf("nil body 应返回空映射，实际 %v", adapters)
		}
		adapters := CodexResponsesChatBridgeToolAdaptersFromClientBody(map[string]any{
			"tools": []any{
				map[string]any{"type": "function", "name": "lookup"},
				map[string]any{"type": "web_search"},
			},
		})
		adapter, ok := adapters["lookup"]
		if !ok {
			t.Fatalf("缺少 lookup 适配器：%v", adapters)
		}
		if adapter.Kind != "function" || adapter.ResponsesName != "lookup" || adapter.ChatName != "lookup" {
			t.Errorf("lookup 适配器 = %+v", adapter)
		}
	})
	t.Run("appendBridgeText 组合", func(t *testing.T) {
		if got, ok := appendBridgeText("", "b").(string); !ok || got != "b" {
			t.Errorf("base 空 -> next，实际 %v", appendBridgeText("", "b"))
		}
		if got, ok := appendBridgeText("a", "").(string); !ok || got != "a" {
			t.Errorf("next 空 -> base，实际 %v", appendBridgeText("a", ""))
		}
		if got, ok := appendBridgeText("a", "b").(string); !ok || got != "a\n\nb" {
			t.Errorf("拼接 = %v，期望 a\\n\\nb", appendBridgeText("a", "b"))
		}
	})
}
