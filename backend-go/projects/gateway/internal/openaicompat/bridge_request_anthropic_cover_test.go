package openaicompat

// OpenAI（chat_completions / responses）-> Anthropic Messages 桥接请求构造的
// 补充覆盖测试。语义对照 Node 归档实现 openai-anthropic-bridge.ts
// （migration-backup/node/final-archive），错误码与默认值逐项断言。

import (
	"context"
	"strings"
	"testing"
)

// covFakeResolver 构造可注入的 FileResolver 假实现（Mock 边界）。
type covFakeResolver struct {
	resolved  *ResolvedFile
	err       error
	nilResult bool
}

func (f *covFakeResolver) ResolveFile(ctx context.Context, input FileResolveInput) (*ResolvedFile, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.nilResult {
		return nil, nil
	}
	return f.resolved, nil
}

func TestCovChatToAnthropicErrors(t *testing.T) {
	base := func(mutate func(map[string]any)) map[string]any {
		body := map[string]any{
			"model": "gpt-4",
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
			name: "缺少 model",
			body: map[string]any{"messages": []any{}},
			code: "openai_anthropic_bridge_missing_model",
		},
		{
			name: "legacy functions 字段",
			body: base(func(body map[string]any) { body["functions"] = []any{} }),
			code: "openai_anthropic_bridge_legacy_chat_functions_unsupported",
		},
		{
			name: "legacy function_call 字段",
			body: base(func(body map[string]any) { body["function_call"] = "auto" }),
			code: "openai_anthropic_bridge_legacy_chat_functions_unsupported",
		},
		{
			name: "role=function 消息",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "function", "content": "x"}}
			}),
			code: "openai_anthropic_bridge_legacy_chat_function_messages_unsupported",
		},
		{
			name: "assistant function_call 消息",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{
					"role":          "assistant",
					"function_call": map[string]any{"name": "f"},
				}}
			}),
			code: "openai_anthropic_bridge_legacy_chat_function_messages_unsupported",
		},
		{
			name: "不支持的 reasoning_effort",
			body: base(func(body map[string]any) { body["reasoning_effort"] = "ultra" }),
			code: "openai_anthropic_bridge_reasoning_effort_unsupported",
		},
		{
			name: "reasoning.effort 为空",
			body: base(func(body map[string]any) {
				body["reasoning"] = map[string]any{"effort": ""}
			}),
			code: "openai_anthropic_bridge_reasoning_effort_unsupported",
		},
		{
			name: "不支持的 reasoning.summary",
			body: base(func(body map[string]any) {
				body["reasoning"] = map[string]any{"summary": "bogus"}
			}),
			code: "openai_anthropic_bridge_reasoning_summary_unsupported",
		},
		{
			name: "孤儿 tool 结果",
			body: base(func(body map[string]any) {
				body["messages"] = []any{
					map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "x"},
				}
			}),
			code: "openai_anthropic_bridge_orphan_tool_result",
		},
		{
			name: "tool 结果缺少 tool_call_id",
			body: base(func(body map[string]any) {
				body["messages"] = []any{
					map[string]any{"role": "assistant", "tool_calls": []any{
						map[string]any{"id": "call_1", "function": map[string]any{"name": "f", "arguments": "{}"}},
					}},
					map[string]any{"role": "tool", "content": "x"},
				}
			}),
			code: "openai_anthropic_bridge_tool_result_missing_call_id",
		},
		{
			name: "重复 tool 结果",
			body: base(func(body map[string]any) {
				body["messages"] = []any{
					map[string]any{"role": "assistant", "tool_calls": []any{
						map[string]any{"id": "call_1", "function": map[string]any{"name": "f", "arguments": "{}"}},
					}},
					map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "x"},
					map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "y"},
				}
			}),
			code: "openai_anthropic_bridge_duplicate_tool_result",
		},
		{
			name: "audio content part",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "input_audio", "input_audio": map[string]any{}},
				}}}
			}),
			code: "openai_anthropic_bridge_audio_input_unsupported",
		},
		{
			name: "未知 content part",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "hologram"},
				}}}
			}),
			code: "openai_anthropic_bridge_unsupported_content_part",
		},
		{
			name: "不支持的工具类型",
			body: base(func(body map[string]any) {
				body["tools"] = []any{map[string]any{"type": "web_search"}}
			}),
			code: "openai_anthropic_bridge_unsupported_tool",
		},
		{
			name: "function tool 缺少 name",
			body: base(func(body map[string]any) {
				body["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{}}}
			}),
			code: "openai_anthropic_bridge_invalid_tool",
		},
		{
			name: "图片 data URL 媒体类型不支持",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/tiff;base64,AAAA"}},
				}}}
			}),
			code: "openai_anthropic_bridge_unsupported_image_media_type",
		},
		{
			// data URL 携带空白 base64 段：正则命中但 TrimSpace 后为空，判非法。
			name: "图片 data URL 缺少 base64 数据",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,   "}},
				}}}
			}),
			code: "openai_anthropic_bridge_invalid_image_base64",
		},
		{
			name: "图片 data URL 非法 base64",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,!!!not-base64!!!"}},
				}}}
			}),
			code: "openai_anthropic_bridge_invalid_image_base64",
		},
		{
			name: "image_url 缺少 url",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{}},
				}}}
			}),
			code: "openai_anthropic_bridge_unsupported_image_reference",
		},
		{
			name: "file part 缺少 file_id 与 file_data",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "file"},
				}}}
			}),
			code: "openai_anthropic_bridge_unsupported_file_reference",
		},
		{
			name: "file_id 缺少解析器",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "file", "file_id": "file-1"},
				}}}
			}),
			code: "openai_anthropic_bridge_file_resolver_unavailable",
		},
		{
			name: "file_data 缺少内容",
			body: base(func(body map[string]any) {
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "file", "file_data": map[string]any{"filename": "a.txt"}},
				}}}
			}),
			code: "openai_anthropic_bridge_invalid_file_data",
		},
		{
			// 注意：tool_choice 只在携带 tools 时才落到输出体，所以冲突用例必须带 tools。
			name: "thinking 与强制 tool_choice 冲突",
			body: base(func(body map[string]any) {
				body["reasoning_effort"] = "high"
				body["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "f"}}}
				body["tool_choice"] = "required"
			}),
			code: "openai_anthropic_bridge_thinking_forced_tool_choice_unsupported",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildOpenAIChatToAnthropicMessagesBody(tc.body, BridgeRequestBodyOptions{})
			validation := requireBridgeValidation(t, err, tc.code)
			if validation.StatusCode != 400 {
				t.Errorf("状态码 = %d，期望 400", validation.StatusCode)
			}
		})
	}
}

func TestCovChatToAnthropicBasicShape(t *testing.T) {
	body := map[string]any{
		"model":                 "gpt-4",
		"messages":              []any{},
		"temperature":           0.5,
		"top_p":                 0.9,
		"stop":                  "END",
		"safety_identifier":     "user-42",
		"max_completion_tokens": float64(512),
		"stream":                true,
	}
	output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if covText(t, output["model"]) != "gpt-4" {
		t.Errorf("model = %v，期望 gpt-4", output["model"])
	}
	covInt(t, output["max_tokens"], 512)
	if output["stream"] != true {
		t.Errorf("stream = %v，期望 true", output["stream"])
	}
	if covNumber(t, output["temperature"]) != 0.5 {
		t.Errorf("temperature = %v，期望 0.5", output["temperature"])
	}
	if covNumber(t, output["top_p"]) != 0.9 {
		t.Errorf("top_p = %v，期望 0.9", output["top_p"])
	}
	stop, ok := output["stop_sequences"].([]string)
	if !ok || len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop_sequences = %v，期望 [END]", output["stop_sequences"])
	}
	metadata := covMap(t, output["metadata"])
	if covText(t, metadata["user_id"]) != "user-42" {
		t.Errorf("metadata.user_id = %v，期望 user-42", metadata["user_id"])
	}
	// messages 为空时补一条空 user 消息（Node 行为：避免上游空 messages 报错）。
	messages := covSlice(t, output["messages"])
	if len(messages) != 1 {
		t.Fatalf("期望 1 条兜底消息，实际 %d", len(messages))
	}
	first := covMap(t, messages[0])
	if covText(t, first["role"]) != "user" {
		t.Errorf("兜底消息 role = %v，期望 user", first["role"])
	}
	blocks := covSlice(t, first["content"])
	if len(blocks) != 1 || covBlockText(t, blocks, 0) != "" {
		t.Errorf("兜底消息 content = %v，期望空 text block", first["content"])
	}
}

func TestCovChatToAnthropicSystemAndMerge(t *testing.T) {
	body := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "system", "content": "系统提示"},
			map[string]any{"role": "developer", "content": []any{
				map[string]any{"type": "input_text", "text": "开发指引"},
			}},
			map[string]any{"role": "user", "content": "第一句"},
			map[string]any{"role": "user", "content": "第二句"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "回答"},
			}},
			// 未知 role 走 default 分支直接跳过（Node 行为；role=function 会被
			// legacy 校验直接拒绝，见 TestCovChatToAnthropicErrors）。
			map[string]any{"role": "boss", "content": "x"},
			nil,
		},
	}
	output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if covText(t, output["system"]) != "系统提示\n\n开发指引" {
		t.Errorf("system = %v，期望合并文本", output["system"])
	}
	messages := covSlice(t, output["messages"])
	if len(messages) != 2 {
		t.Fatalf("期望 2 条消息（连续 user 合并），实际 %d：%v", len(messages), messages)
	}
	first := covMap(t, messages[0])
	if covText(t, first["role"]) != "user" {
		t.Fatalf("第一条 role = %v，期望 user", first["role"])
	}
	merged := covSlice(t, first["content"])
	if len(merged) != 2 || covBlockText(t, merged, 0) != "第一句" || covBlockText(t, merged, 1) != "第二句" {
		t.Errorf("合并 content = %v，期望两条 text", merged)
	}
	if covText(t, covMap(t, messages[1])["role"]) != "assistant" {
		t.Errorf("第二条 role = %v，期望 assistant", covMap(t, messages[1])["role"])
	}
}

func TestCovChatToAnthropicToolFlow(t *testing.T) {
	body := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "user", "content": "天气如何"},
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
				map[string]any{
					"id": "call_1",
					"function": map[string]any{
						"name":      "get_weather",
						"arguments": `{"city":"北京"}`,
					},
				},
				// 缺 id/name 的 tool_call 被忽略。
				map[string]any{"id": "call_2", "function": map[string]any{"name": "", "arguments": "{}"}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "晴"},
		},
	}
	output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	messages := covSlice(t, output["messages"])
	if len(messages) != 3 {
		t.Fatalf("期望 3 条消息，实际 %d：%v", len(messages), messages)
	}
	assistant := covMap(t, messages[1])
	blocks := covSlice(t, assistant["content"])
	toolUses := covToolUseBlocks(t, blocks)
	if len(toolUses) != 1 {
		t.Fatalf("期望 1 个 tool_use block，实际 %d", len(toolUses))
	}
	if covText(t, toolUses[0]["id"]) != "call_1" || covText(t, toolUses[0]["name"]) != "get_weather" {
		t.Errorf("tool_use = %v，期望 call_1/get_weather", toolUses[0])
	}
	input := covMap(t, toolUses[0]["input"])
	if covText(t, input["city"]) != "北京" {
		t.Errorf("tool_use.input = %v，期望 city=北京", input)
	}
	toolResult := covMap(t, messages[2])
	blocks = covSlice(t, toolResult["content"])
	resultBlock := covMap(t, blocks[0])
	if covText(t, resultBlock["type"]) != "tool_result" || covText(t, resultBlock["tool_use_id"]) != "call_1" {
		t.Errorf("tool_result block = %v，期望 call_1", resultBlock)
	}
	if covText(t, resultBlock["content"]) != "晴" {
		t.Errorf("tool_result.content = %v，期望 晴", resultBlock["content"])
	}
}

func TestCovChatToAnthropicContentParts(t *testing.T) {
	dataURL := "data:image/png;base64," + "iVBORw0KGgo="
	body := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "看图"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL}},
				map[string]any{"type": "image_url", "image_url": "https://example.com/cat.png"},
				// 非对象、无 text 的 part 被跳过。
				nil,
				map[string]any{"type": "text", "text": ""},
			}},
		},
	}
	output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	messages := covSlice(t, output["messages"])
	blocks := covSlice(t, covMap(t, messages[0])["content"])
	if len(blocks) != 3 {
		t.Fatalf("期望 3 个 block，实际 %d：%v", len(blocks), blocks)
	}
	if covBlockText(t, blocks, 0) != "看图" {
		t.Errorf("block0 = %v，期望 看图", blocks[0])
	}
	base64Block := covMap(t, blocks[1])
	if covText(t, base64Block["type"]) != "image" {
		t.Fatalf("block1 类型 = %v，期望 image", base64Block["type"])
	}
	source := covMap(t, base64Block["source"])
	if covText(t, source["type"]) != "base64" || covText(t, source["media_type"]) != "image/png" {
		t.Errorf("base64 图片 source = %v", source)
	}
	urlBlock := covMap(t, blocks[2])
	urlSource := covMap(t, urlBlock["source"])
	if covText(t, urlSource["type"]) != "url" || covText(t, urlSource["url"]) != "https://example.com/cat.png" {
		t.Errorf("url 图片 source = %v", urlSource)
	}
}

func TestCovChatToAnthropicFileParts(t *testing.T) {
	t.Run("file_data 内联 base64", func(t *testing.T) {
		body := map[string]any{
			"model": "gpt-4",
			"messages": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "file", "file_data": map[string]any{
					"filename":  "doc.pdf",
					"format":    ".pdf",
					"file_data": "AAAA",
				}},
			}}},
		}
		output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		blocks := covSlice(t, covMap(t, covSlice(t, output["messages"])[0])["content"])
		document := covMap(t, blocks[0])
		if covText(t, document["type"]) != "document" || covText(t, document["title"]) != "doc.pdf" {
			t.Fatalf("document block = %v", document)
		}
		source := covMap(t, document["source"])
		if covText(t, source["media_type"]) != "application/pdf" || covText(t, source["data"]) != "AAAA" {
			t.Errorf("document source = %v", source)
		}
	})
	t.Run("file_id 走解析器 base64", func(t *testing.T) {
		resolver := &covFakeResolver{resolved: &ResolvedFile{
			FileID: "file-1", Filename: "b.txt", MediaType: "application/pdf",
			ContentBase64: "QQ==",
		}}
		body := map[string]any{
			"model": "gpt-4",
			"messages": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "file", "file_id": "file-1"},
			}}},
		}
		output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{FileResolver: resolver})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		blocks := covSlice(t, covMap(t, covSlice(t, output["messages"])[0])["content"])
		source := covMap(t, covMap(t, blocks[0])["source"])
		if covText(t, source["type"]) != "base64" || covText(t, source["data"]) != "QQ==" {
			t.Errorf("解析后的 document source = %v", source)
		}
	})
	t.Run("file_id 解析为文本", func(t *testing.T) {
		resolver := &covFakeResolver{resolved: &ResolvedFile{
			FileID: "file-2", Filename: "c.txt", MediaType: "text/plain",
			ContentText: "正文",
		}}
		body := map[string]any{
			"model": "gpt-4",
			"messages": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_file", "file_id": "file-2"},
			}}},
		}
		output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{FileResolver: resolver})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		blocks := covSlice(t, covMap(t, covSlice(t, output["messages"])[0])["content"])
		source := covMap(t, covMap(t, blocks[0])["source"])
		if covText(t, source["type"]) != "text" || covText(t, source["data"]) != "正文" {
			t.Errorf("文本 document source = %v", source)
		}
	})
	t.Run("file_id 不存在", func(t *testing.T) {
		resolver := &covFakeResolver{nilResult: true}
		body := map[string]any{
			"model": "gpt-4",
			"messages": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "file", "file_id": "file-404"},
			}}},
		}
		_, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{FileResolver: resolver})
		requireBridgeValidation(t, err, "openai_anthropic_bridge_file_not_found")
	})
}

func TestCovChatToAnthropicReasoningAndTools(t *testing.T) {
	t.Run("effort medium 映射 adaptive thinking", func(t *testing.T) {
		body := map[string]any{
			"model":     "gpt-4",
			"messages":  []any{map[string]any{"role": "user", "content": "hi"}},
			"reasoning": map[string]any{"effort": "medium", "summary": "auto"},
		}
		output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		thinking := covMap(t, output["thinking"])
		if covText(t, thinking["type"]) != "adaptive" {
			t.Errorf("thinking = %v，期望 adaptive", thinking)
		}
		config := covMap(t, output["output_config"])
		if covText(t, config["effort"]) != "medium" {
			t.Errorf("output_config.effort = %v，期望 medium", config["effort"])
		}
	})
	t.Run("minimal 预算并抬高 max_tokens", func(t *testing.T) {
		body := map[string]any{
			"model":            "gpt-4",
			"messages":         []any{map[string]any{"role": "user", "content": "hi"}},
			"max_tokens":       float64(512),
			"reasoning_effort": "minimal",
		}
		output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		// budget 1024 > max_tokens 512：Node 会抬到 budget+1024。
		covInt(t, output["max_tokens"], 2048)
		thinking := covMap(t, output["thinking"])
		covInt(t, thinking["budget_tokens"], 1024)
	})
	t.Run("DefaultMaxTokens 选项兜底", func(t *testing.T) {
		body := map[string]any{
			"model":    "gpt-4",
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		}
		fallback := int64(777)
		output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{DefaultMaxTokens: &fallback})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		covInt(t, output["max_tokens"], 777)
	})
	t.Run("tool_choice 与 allowed_tools", func(t *testing.T) {
		body := map[string]any{
			"model":    "gpt-4",
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "f1", "description": "d", "parameters": map[string]any{"type": "object"}}},
				map[string]any{"type": "function", "function": map[string]any{"name": "f2"}},
				map[string]any{"type": "function", "function": map[string]any{"name": "f3", "parameters": "not-object"}},
			},
			"tool_choice": map[string]any{
				"type": "allowed_tools", "mode": "required",
				"tools": []any{"f1", map[string]any{"type": "function", "name": "f3"}, map[string]any{"type": "web_search"}},
			},
			"parallel_tool_calls": false,
		}
		output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		tools := covSlice(t, output["tools"])
		if len(tools) != 2 {
			t.Fatalf("allowed_tools 应过滤为 2 个，实际 %d：%v", len(tools), tools)
		}
		choice := covMap(t, output["tool_choice"])
		if covText(t, choice["type"]) != "any" {
			t.Errorf("mode=required 的 allowed_tools 应映射 any，实际 %v", choice)
		}
		if choice["disable_parallel_tool_use"] != true {
			t.Errorf("parallel_tool_calls=false 应写入 disable_parallel_tool_use，实际 %v", choice)
		}
		third := covMap(t, tools[1])
		schema := covMap(t, third["input_schema"])
		if covText(t, schema["type"]) != "object" {
			t.Errorf("非对象 parameters 应替换为空 object schema，实际 %v", third)
		}
	})
	t.Run("tool_choice 字符串映射", func(t *testing.T) {
		for _, tc := range []struct {
			in   string
			want string
		}{{"auto", "auto"}, {"none", "none"}, {"required", "any"}} {
			body := map[string]any{
				"model":       "gpt-4",
				"messages":    []any{map[string]any{"role": "user", "content": "hi"}},
				"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "f1"}}},
				"tool_choice": tc.in,
			}
			output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
			if err != nil {
				t.Fatalf("%s 构造失败：%v", tc.in, err)
			}
			choice := covMap(t, output["tool_choice"])
			if covText(t, choice["type"]) != tc.want {
				t.Errorf("tool_choice %s 映射 = %v，期望 %s", tc.in, choice, tc.want)
			}
		}
	})
	t.Run("tool_choice 指定函数", func(t *testing.T) {
		body := map[string]any{
			"model":       "gpt-4",
			"messages":    []any{map[string]any{"role": "user", "content": "hi"}},
			"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "f1"}}},
			"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "f1"}},
		}
		output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		choice := covMap(t, output["tool_choice"])
		if covText(t, choice["type"]) != "tool" || covText(t, choice["name"]) != "f1" {
			t.Errorf("tool_choice = %v，期望 {tool f1}", choice)
		}
	})
	t.Run("未知 tool_choice 值不透传", func(t *testing.T) {
		body := map[string]any{
			"model":       "gpt-4",
			"messages":    []any{map[string]any{"role": "user", "content": "hi"}},
			"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "f1"}}},
			"tool_choice": "bogus",
		}
		output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		if _, exists := output["tool_choice"]; exists {
			t.Errorf("未知 tool_choice 不应输出，实际 %v", output["tool_choice"])
		}
	})
	t.Run("消息 name 前缀", func(t *testing.T) {
		body := map[string]any{
			"model": "gpt-4",
			"messages": []any{
				map[string]any{"role": "user", "name": "张   三", "content": "你好"},
			},
		}
		output, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		blocks := covSlice(t, covMap(t, covSlice(t, output["messages"])[0])["content"])
		if got := covBlockText(t, blocks, 0); got != "参与者: 张 三\n你好" {
			t.Errorf("name 前缀文本 = %q，期望 %q", got, "参与者: 张 三\n你好")
		}
	})
}

func TestCovResponsesToAnthropicFlow(t *testing.T) {
	t.Run("instructions 与字符串 input", func(t *testing.T) {
		body := map[string]any{
			"model":        "gpt-4",
			"instructions": "保持简洁",
			"input":        "你好",
		}
		output, err := BuildOpenAIResponsesToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		if covText(t, output["system"]) != "保持简洁" {
			t.Errorf("system = %v，期望 保持简洁", output["system"])
		}
		messages := covSlice(t, output["messages"])
		if len(messages) != 1 || covBlockText(t, covSlice(t, covMap(t, messages[0])["content"]), 0) != "你好" {
			t.Errorf("messages = %v，期望单条 user 你好", messages)
		}
	})
	t.Run("previous_response_id 拒绝", func(t *testing.T) {
		body := map[string]any{
			"model":                "gpt-4",
			"previous_response_id": "resp_1",
			"input":                "hi",
		}
		_, err := BuildOpenAIResponsesToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		requireBridgeValidation(t, err, "openai_anthropic_bridge_previous_response_state_unavailable")
	})
	t.Run("input 数组全 item 形态", func(t *testing.T) {
		body := map[string]any{
			"model": "gpt-4",
			"input": []any{
				map[string]any{"type": "message", "role": "developer", "content": "开发指引"},
				map[string]any{"type": "message", "role": "user", "content": []any{
					map[string]any{"type": "input_text", "text": "问题"},
				}},
				map[string]any{"type": "function_call", "call_id": "call_9", "name": "lookup", "arguments": `{"q":"x"}`},
				map[string]any{"type": "function_call_output", "call_id": "call_9", "output": map[string]any{"text": "结果"}},
				map[string]any{"type": "reasoning", "summary": "推理摘要"},
				map[string]any{"type": "reasoning", "encrypted_content": "zzz", "summary": "不应出现"},
				map[string]any{"type": "compaction", "content": "上下文摘要"},
				// 孤儿 function_call_output：校验失败但被静默跳过（Node 行为）。
				map[string]any{"type": "function_call_output", "call_id": "call_missing", "output": "孤儿"},
				map[string]any{"type": "message", "role": "tool", "content": "未知 role 跳过"},
				nil,
			},
		}
		output, err := BuildOpenAIResponsesToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		system := covText(t, output["system"])
		if !strings.Contains(system, "开发指引") || !strings.Contains(system, "历史推理摘要：\n推理摘要") ||
			!strings.Contains(system, "上下文摘要") {
			t.Errorf("system = %q，期望包含 developer / reasoning / compaction 文本", system)
		}
		if strings.Contains(system, "不应出现") {
			t.Errorf("encrypted_content reasoning 不应进入 system：%q", system)
		}
		messages := covSlice(t, output["messages"])
		// user 文本 -> assistant function_call -> user tool_result。
		if len(messages) != 3 {
			t.Fatalf("期望 3 条消息，实际 %d：%v", len(messages), messages)
		}
		assistant := covMap(t, messages[1])
		toolUses := covToolUseBlocks(t, covSlice(t, assistant["content"]))
		if len(toolUses) != 1 || covText(t, toolUses[0]["name"]) != "lookup" {
			t.Fatalf("assistant tool_use = %v", toolUses)
		}
		resultBlock := covMap(t, covSlice(t, covMap(t, messages[2])["content"])[0])
		if covText(t, resultBlock["content"]) != "结果" {
			t.Errorf("function_call_output 内容 = %v，期望 结果", resultBlock["content"])
		}
	})
	t.Run("input_image 形态", func(t *testing.T) {
		body := map[string]any{
			"model": "gpt-4",
			"input": []any{map[string]any{"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_image", "image_url": "https://example.com/x.png"},
			}}},
		}
		output, err := BuildOpenAIResponsesToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		blocks := covSlice(t, covMap(t, covSlice(t, output["messages"])[0])["content"])
		source := covMap(t, covMap(t, blocks[0])["source"])
		if covText(t, source["type"]) != "url" {
			t.Errorf("input_image source = %v，期望 url", source)
		}
	})
	// 行为存疑：appendResponsesInputItemAsAnthropicMessage 对 message item 的
	// content 转换错误做静默吞掉（return），与 chat 路径直接向上抛错不一致；
	// 按当前实际行为断言：非法 content part 的整条消息被丢弃，请求仍成功。
	t.Run("非法 content part 的消息被静默丢弃", func(t *testing.T) {
		for _, part := range []any{
			map[string]any{"type": "input_image"},
			map[string]any{"type": "input_audio"},
			map[string]any{"type": "video"},
		} {
			body := map[string]any{
				"model": "gpt-4",
				"input": []any{map[string]any{"type": "message", "role": "user", "content": []any{part}}},
			}
			output, err := BuildOpenAIResponsesToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
			if err != nil {
				t.Fatalf("part %v 不应报错：%v", part, err)
			}
			messages := covSlice(t, output["messages"])
			if len(messages) != 1 {
				t.Fatalf("part %v：期望兜底 1 条消息，实际 %d", part, len(messages))
			}
			fallback := covSlice(t, covMap(t, messages[0])["content"])
			if covBlockText(t, fallback, 0) != "" {
				t.Errorf("part %v：兜底消息应为空 text，实际 %v", part, fallback)
			}
		}
	})
	t.Run("responsesContentToAnthropicBlocks 错误语义（直连）", func(t *testing.T) {
		options := &BridgeRequestBodyOptions{}
		cases := []struct {
			name string
			part map[string]any
			code string
		}{
			{"input_image 缺 image_url 与 file_id", map[string]any{"type": "input_image"}, "openai_anthropic_bridge_invalid_image_input"},
			{"input_audio 拒绝", map[string]any{"type": "input_audio"}, "openai_anthropic_bridge_audio_input_unsupported"},
			{"未知 content part", map[string]any{"type": "video"}, "openai_anthropic_bridge_unsupported_content_part"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := responsesContentToAnthropicBlocks([]any{tc.part}, options)
				requireBridgeValidation(t, err, tc.code)
			})
		}
		t.Run("input_image 走 file_id 解析", func(t *testing.T) {
			resolver := &covFakeResolver{resolved: &ResolvedFile{Filename: "d.txt", MediaType: "text/plain", ContentText: "正文"}}
			_, err := responsesContentToAnthropicBlocks([]any{
				map[string]any{"type": "input_image", "file_id": "file-3"},
			}, &BridgeRequestBodyOptions{FileResolver: resolver})
			if err != nil {
				t.Fatalf("file_id 解析不应报错：%v", err)
			}
		})
	})
	t.Run("hosted 工具降级进 system 约束", func(t *testing.T) {
		body := map[string]any{
			"model": "gpt-4",
			"input": "hi",
			"tools": []any{
				map[string]any{"type": "code_interpreter"},
				// Node：无名无类型的 shapeless 记录按 legacy function 处理需要 name；
				// 这里以 web_search 验证非注册表类型走原始 label 降级。
				map[string]any{"type": "web_search"},
				map[string]any{"name": "legacy_fn", "parameters": map[string]any{"type": "object"}},
				map[string]any{"type": "function", "name": "direct_fn", "strict": true},
			},
		}
		output, err := BuildOpenAIResponsesToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		system := covText(t, output["system"])
		if !strings.Contains(system, "OpenAI hosted tools unavailable in this Anthropic bridge: code_interpreter, web_search.") {
			t.Errorf("system 缺少 hosted 工具约束：%q", system)
		}
		tools := covSlice(t, output["tools"])
		if len(tools) != 2 {
			t.Fatalf("期望 legacy + function 两个工具，实际 %d：%v", len(tools), tools)
		}
		strict := covMap(t, tools[1])
		if strict["strict"] != true {
			t.Errorf("strict 字段应保留：%v", strict)
		}
	})
	t.Run("reject 模式的 hosted 工具报错", func(t *testing.T) {
		body := map[string]any{
			"model": "gpt-4",
			"input": "hi",
			"tools": []any{map[string]any{"type": "code_interpreter"}},
		}
		_, err := BuildOpenAIResponsesToAnthropicMessagesBody(body, BridgeRequestBodyOptions{
			HostedToolModes: OpenAIHostedToolRuntimeModes{CodeInterpreter: "reject"},
		})
		requireBridgeValidation(t, err, "openai_anthropic_bridge_unsupported_tool")
	})
	t.Run("function 工具缺少 name 报错", func(t *testing.T) {
		body := map[string]any{
			"model": "gpt-4",
			"input": "hi",
			"tools": []any{map[string]any{"type": "function"}},
		}
		_, err := BuildOpenAIResponsesToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		requireBridgeValidation(t, err, "openai_anthropic_bridge_invalid_tool")
	})
	t.Run("空 input 输出兜底消息", func(t *testing.T) {
		body := map[string]any{"model": "gpt-4", "input": []any{}}
		output, err := BuildOpenAIResponsesToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		if len(messages) != 1 {
			t.Fatalf("期望兜底 1 条消息，实际 %d", len(messages))
		}
	})
}

func TestCovBridgeRequestBodyHelpers(t *testing.T) {
	t.Run("stopSequencesValue", func(t *testing.T) {
		if got := stopSequencesValue("A"); len(got) != 1 || got[0] != "A" {
			t.Errorf("字符串 stop = %v，期望 [A]", got)
		}
		if got := stopSequencesValue(""); got != nil {
			t.Errorf("空字符串 stop = %v，期望 nil", got)
		}
		if got := stopSequencesValue([]any{"a", "", 3}); len(got) != 1 || got[0] != "a" {
			t.Errorf("数组 stop = %v，期望 [a]", got)
		}
		if got := stopSequencesValue(42); got != nil {
			t.Errorf("数字 stop = %v，期望 nil", got)
		}
	})
	t.Run("anthropicThinkingBudgetTokens", func(t *testing.T) {
		for effort, want := range map[string]int64{
			"minimal": 1024, "low": 2048, "medium": 4096, "high": 8192, "bogus": 0,
		} {
			if got := anthropicThinkingBudgetTokens(effort); got != want {
				t.Errorf("budget(%s) = %d，期望 %d", effort, got, want)
			}
		}
	})
	t.Run("bridgeMediaTypeFromExtension", func(t *testing.T) {
		for ext, want := range map[string]string{
			".pdf": "application/pdf", ".TXT": "text/plain", "text": "text/plain",
			".md": "text/markdown", ".csv": "text/csv", ".json": "application/json",
			".xyz": "",
		} {
			if got := bridgeMediaTypeFromExtension(ext); got != want {
				t.Errorf("mediaType(%s) = %q，期望 %q", ext, got, want)
			}
		}
	})
	t.Run("openAIContentToText", func(t *testing.T) {
		if got := openAIContentToText([]any{
			map[string]any{"type": "output_text", "text": "a"},
			map[string]any{"type": "image_url"},
			"not-object",
			map[string]any{"text": "b"},
		}); got != "a\nb" {
			t.Errorf("openAIContentToText = %q，期望 a\\nb", got)
		}
		if got := openAIContentToText(42); got != "" {
			t.Errorf("非数组内容应返回空串，实际 %q", got)
		}
	})
	t.Run("withOpenAIChatMessageNamePrefix 空块与非文本首块", func(t *testing.T) {
		empty := withOpenAIChatMessageNamePrefix(nil, "甲")
		if len(empty) != 1 || covText(t, covMap(t, empty[0])["text"]) != "参与者: 甲" {
			t.Errorf("空块前缀 = %v", empty)
		}
		untouched := withOpenAIChatMessageNamePrefix(nil, "  ")
		if untouched != nil {
			t.Errorf("空 name 不应改写块，实际 %v", untouched)
		}
		leading := []any{map[string]any{"type": "image", "source": map[string]any{}}}
		withPrefix := withOpenAIChatMessageNamePrefix(leading, "乙")
		if len(withPrefix) != 2 || covText(t, covMap(t, withPrefix[0])["text"]) != "参与者: 乙" {
			t.Errorf("非文本首块应前置前缀块，实际 %v", withPrefix)
		}
	})
	t.Run("anthropicImageBlockFromURL data URL 空白清洗", func(t *testing.T) {
		block, err := anthropicImageBlockFromURL("data:image/jpeg;base64, QQ== ")
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		source := covMap(t, block["source"])
		if covText(t, source["data"]) != "QQ==" {
			t.Errorf("base64 数据应清洗空白，实际 %q", source["data"])
		}
	})
}
