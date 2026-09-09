package openaicompat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tests for the gemini-native-target bridge request sources ported from
// openai-anthropic-gemini-native-bridge.ts (responsesBodyToGemini /
// anthropicMessagesBodyToGemini). Guidance error codes and copy are asserted
// verbatim against the archive.

func requireGeminiGuidance(t *testing.T, err error, wantCode, wantMessage string, wantProtocol GeminiNativeDownstreamProtocol, wantStream bool, wantModel string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected guidance error %s, got nil", wantCode)
	}
	guidance, ok := err.(*BridgeGuidanceError)
	if !ok {
		t.Fatalf("expected *BridgeGuidanceError, got %T: %v", err, err)
	}
	if guidance.Code != wantCode {
		t.Errorf("code = %q, want %q", guidance.Code, wantCode)
	}
	if guidance.Message != wantMessage {
		t.Errorf("message = %q, want %q", guidance.Message, wantMessage)
	}
	if guidance.Protocol != wantProtocol {
		t.Errorf("protocol = %q, want %q", guidance.Protocol, wantProtocol)
	}
	if guidance.Stream != wantStream {
		t.Errorf("stream = %v, want %v", guidance.Stream, wantStream)
	}
	if guidance.Model != wantModel {
		t.Errorf("model = %q, want %q", guidance.Model, wantModel)
	}
}

func requireBridgeValidation(t *testing.T, err error, wantCode string) *BridgeRequestError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected validation error %s, got nil", wantCode)
	}
	validation, ok := err.(*BridgeRequestError)
	if !ok {
		t.Fatalf("expected *BridgeRequestError, got %T: %v", err, err)
	}
	if validation.Code != wantCode {
		t.Errorf("code = %q, want %q", validation.Code, wantCode)
	}
	if validation.StatusCode != 400 || validation.Type != "invalid_request_error" {
		t.Errorf("status/type = %d/%q", validation.StatusCode, validation.Type)
	}
	return validation
}

// ---- responses source: normal paths ----

func TestBuildOpenAIResponsesToGeminiBodyStringInputAndConfig(t *testing.T) {
	body := map[string]any{
		"model":        "gpt-x",
		"instructions": "be terse",
		"input":        "hello",
		"temperature":  float64(0.4),
		"top_p":        float64(0.9),
		"max_tokens":   float64(512.7),
		"stop":         []any{"END"},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"schema": map[string]any{"type": "object"},
			},
		},
		"tools": []any{
			map[string]any{
				"type":        "function",
				"name":        "lookup",
				"description": "find it",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		"tool_choice": "required",
	}
	result, err := BuildOpenAIResponsesToGeminiBody(body, BridgeRequestBodyOptions{ModelOverride: "gemini-x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["model"] != "gemini-x" {
		t.Errorf("model = %v (archive finalizeGeminiBody rides the upstream model)", result["model"])
	}
	contents, _ := result["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents = %s", mustJSON(t, result))
	}
	first := contents[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("role = %v", first["role"])
	}
	systemInstruction := result["systemInstruction"].(map[string]any)
	if systemInstruction["role"] != "user" {
		t.Errorf("systemInstruction.role = %v", systemInstruction["role"])
	}
	systemParts := systemInstruction["parts"].([]any)
	if systemParts[0].(map[string]any)["text"] != "be terse" {
		t.Errorf("systemInstruction = %s", mustJSON(t, systemInstruction))
	}
	generationConfig := result["generationConfig"].(map[string]any)
	if generationConfig["temperature"] != float64(0.4) || generationConfig["topP"] != float64(0.9) {
		t.Errorf("generationConfig = %s", mustJSON(t, generationConfig))
	}
	if generationConfig["maxOutputTokens"] != int64(512) {
		t.Errorf("maxOutputTokens = %v (Math.trunc)", generationConfig["maxOutputTokens"])
	}
	stopSequences := generationConfig["stopSequences"].([]any)
	if len(stopSequences) != 1 || stopSequences[0] != "END" {
		t.Errorf("stopSequences = %s", mustJSON(t, generationConfig))
	}
	if generationConfig["responseMimeType"] != "application/json" {
		t.Errorf("responseMimeType missing: %s", mustJSON(t, generationConfig))
	}
	if generationConfig["responseSchema"] == nil {
		t.Errorf("responseSchema missing: %s", mustJSON(t, generationConfig))
	}
	tools := result["tools"].([]any)
	declarations := tools[0].(map[string]any)["functionDeclarations"].([]any)
	if len(declarations) != 1 {
		t.Fatalf("tools = %s", mustJSON(t, result))
	}
	declaration := declarations[0].(map[string]any)
	if declaration["name"] != "lookup" || declaration["description"] != "find it" {
		t.Errorf("declaration = %s", mustJSON(t, declaration))
	}
	toolConfig := result["toolConfig"].(map[string]any)
	functionCallingConfig := toolConfig["functionCallingConfig"].(map[string]any)
	if functionCallingConfig["mode"] != "ANY" {
		t.Errorf("toolConfig = %s", mustJSON(t, result))
	}
}

func TestBuildOpenAIResponsesToGeminiBodyInputArray(t *testing.T) {
	body := map[string]any{
		"model": "gpt-x",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "look"},
					map[string]any{"type": "refusal", "refusal": "no"},
					map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"},
				},
			},
			map[string]any{
				"type":      "function_call",
				"name":      "lookup",
				"arguments": `{"q":"hi"}`,
			},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call_9",
				"output":  "found",
			},
		},
	}
	result, err := BuildOpenAIResponsesToGeminiBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	contents, _ := result["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("contents = %s", mustJSON(t, contents))
	}
	userContent := contents[0].(map[string]any)
	parts := userContent["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("user parts (refusal skipped) = %s", mustJSON(t, parts))
	}
	inlineData := parts[1].(map[string]any)["inlineData"].(map[string]any)
	if inlineData["mimeType"] != "image/png" || inlineData["data"] != "AAAA" {
		t.Errorf("inlineData = %s", mustJSON(t, parts[1]))
	}
	modelContent := contents[1].(map[string]any)
	if modelContent["role"] != "model" {
		t.Errorf("function_call role = %v", modelContent["role"])
	}
	functionCall := modelContent["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	if functionCall["name"] != "lookup" {
		t.Errorf("functionCall = %s", mustJSON(t, functionCall))
	}
	args := functionCall["args"].(map[string]any)
	if args["q"] != "hi" {
		t.Errorf("arguments not parsed to object: %s", mustJSON(t, functionCall))
	}
	resultContent := contents[2].(map[string]any)
	if resultContent["role"] != "user" {
		t.Errorf("function_call_output role = %v", resultContent["role"])
	}
	functionResponse := resultContent["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if functionResponse["name"] != "call_9" {
		t.Errorf("functionResponse = %s", mustJSON(t, functionResponse))
	}
	response := functionResponse["response"].(map[string]any)
	if response["content"] != "found" {
		t.Errorf("functionResponse.response = %s", mustJSON(t, functionResponse))
	}
}

func TestBuildOpenAIResponsesToGeminiBodyHTTPImageURL(t *testing.T) {
	body := map[string]any{
		"model": "gpt-x",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type":      "input_image",
						"image_url": "https://example.com/cat.jpg?size=l",
					},
				},
			},
		},
	}
	result, err := BuildOpenAIResponsesToGeminiBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	contents, _ := result["contents"].([]any)
	parts := contents[0].(map[string]any)["parts"].([]any)
	fileData := parts[0].(map[string]any)["fileData"].(map[string]any)
	if fileData["fileUri"] != "https://example.com/cat.jpg?size=l" || fileData["mimeType"] != "image/jpeg" {
		t.Errorf("fileData = %s", mustJSON(t, parts[0]))
	}
}

// ---- responses source: guidance / validation rejects (verbatim) ----

func TestBuildOpenAIResponsesToGeminiBodyRejects(t *testing.T) {
	t.Run("openai_request_controls", func(t *testing.T) {
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model": "gpt-x",
			"input": "hi",
			"reasoning": map[string]any{
				"effort": "high",
			},
		}, BridgeRequestBodyOptions{Stream: true})
		requireGeminiGuidance(t, err,
			"unsupported_openai_request_controls_for_gemini_native",
			"Gemini native 上游不能保真映射 OpenAI 的 reasoning 字段。请移除这些请求控制，或改用支持对应字段的原生上游。",
			GeminiNativeProtocolResponses, true, "gpt-x")
	})
	t.Run("responses_state_previous_response_id", func(t *testing.T) {
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model":                "gpt-x",
			"input":                "hi",
			"previous_response_id": "resp_1",
		}, BridgeRequestBodyOptions{})
		requireGeminiGuidance(t, err,
			"unsupported_responses_state_for_gemini_native",
			"Gemini native 上游不能保真承载 OpenAI Responses 的 previous_response_id / context_management 状态链。请改用真实 Responses 上游，或移除状态链字段后重试。",
			GeminiNativeProtocolResponses, false, "gpt-x")
	})
	t.Run("responses_state_context_management", func(t *testing.T) {
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model":              "gpt-x",
			"input":              "hi",
			"context_management": nil,
		}, BridgeRequestBodyOptions{})
		if err == nil {
			t.Fatal("context_management key presence must reject even when null")
		}
	})
	t.Run("responses_truncation_values", func(t *testing.T) {
		for _, truncation := range []any{"disabled", "auto"} {
			if _, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
				"model":      "gpt-x",
				"input":      "hi",
				"truncation": truncation,
			}, BridgeRequestBodyOptions{}); err != nil {
				t.Fatalf("truncation %v must pass: %v", truncation, err)
			}
		}
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model":      "gpt-x",
			"input":      "hi",
			"truncation": "auto_epochs",
		}, BridgeRequestBodyOptions{})
		requireGeminiGuidance(t, err,
			"unsupported_responses_truncation_for_gemini_native",
			"Gemini native 上游不能保真承载当前 Responses truncation 设置。请改用真实 Responses 上游，或移除该字段后重试。",
			GeminiNativeProtocolResponses, false, "gpt-x")
	})
	t.Run("responses_reasoning_item", func(t *testing.T) {
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model": "gpt-x",
			"input": []any{
				map[string]any{"type": "reasoning", "summary": []any{}},
			},
		}, BridgeRequestBodyOptions{})
		requireGeminiGuidance(t, err,
			"unsupported_responses_reasoning_for_gemini_native",
			"当前 Gemini native 上游不能保真承载 Responses reasoning item。请改用真实 Responses 上游，或移除 reasoning item 后重试。",
			GeminiNativeProtocolResponses, false, "gpt-x")
	})
	t.Run("responses_tool_whitelist", func(t *testing.T) {
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model": "gpt-x",
			"input": "hi",
			"tools": []any{
				map[string]any{"type": "web_search"},
			},
		}, BridgeRequestBodyOptions{GuidanceProviderName: "Acme"})
		requireGeminiGuidance(t, err,
			"unsupported_responses_tool_for_gemini_native",
			"当前 Acme Gemini native 上游只支持 Responses function tool。请改用本地可执行工具运行时或移除 web_search tool 后重试。",
			GeminiNativeProtocolResponses, false, "gpt-x")
	})
	t.Run("openai_content_part", func(t *testing.T) {
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model": "gpt-x",
			"input": []any{
				map[string]any{
					"type":    "message",
					"role":    "user",
					"content": []any{map[string]any{"type": "audio", "audio": ""}},
				},
			},
		}, BridgeRequestBodyOptions{})
		requireGeminiGuidance(t, err,
			"unsupported_openai_content_part_for_gemini_native",
			"当前 Gemini native 上游不能保真承载 OpenAI content part: audio。请移除该 part 后重试。",
			GeminiNativeProtocolResponses, false, "gpt-x")
	})
	t.Run("openai_image_reference", func(t *testing.T) {
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model": "gpt-x",
			"input": []any{
				map[string]any{
					"type":    "message",
					"role":    "user",
					"content": []any{map[string]any{"type": "input_image", "file_id": "file-1"}},
				},
			},
		}, BridgeRequestBodyOptions{})
		requireGeminiGuidance(t, err,
			"unsupported_openai_image_reference_for_gemini_native",
			"当前 Gemini native 上游不能直接读取 OpenAI file_id 图片引用。请提供 data URL、公开 HTTPS 图片 URL 或先上传到 Gemini Files 后重试。",
			GeminiNativeProtocolResponses, false, "gpt-x")
	})
	t.Run("logprobs", func(t *testing.T) {
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model":        "gpt-x",
			"input":        "hi",
			"top_logprobs": float64(3),
		}, BridgeRequestBodyOptions{})
		requireGeminiGuidance(t, err,
			"unsupported_logprobs_for_gemini_native",
			"当前 Gemini native 上游不能保真承载 OpenAI logprobs。请移除 logprobs 后重试。",
			GeminiNativeProtocolResponses, false, "gpt-x")
	})
	t.Run("invalid_input", func(t *testing.T) {
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model": "gpt-x",
			"input": float64(7),
		}, BridgeRequestBodyOptions{})
		validation := requireBridgeValidation(t, err, "invalid_responses_gemini_bridge_input")
		if validation.Message != "Responses 到 Gemini native 桥接要求 input 是字符串或数组" {
			t.Errorf("message = %q", validation.Message)
		}
	})
	t.Run("invalid_content", func(t *testing.T) {
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model": "gpt-x",
			"input": []any{
				map[string]any{"type": "message", "role": "user", "content": float64(7)},
			},
		}, BridgeRequestBodyOptions{})
		requireBridgeValidation(t, err, "invalid_openai_gemini_bridge_content")
	})
	t.Run("empty_contents", func(t *testing.T) {
		_, err := BuildOpenAIResponsesToGeminiBody(map[string]any{
			"model": "gpt-x",
			"input": []any{},
		}, BridgeRequestBodyOptions{})
		validation := requireBridgeValidation(t, err, "empty_gemini_target_bridge_contents")
		if validation.Message != "Gemini native 目标桥接至少需要一条可发送内容" {
			t.Errorf("message = %q", validation.Message)
		}
	})
}

// ---- anthropic source: normal paths ----

func TestBuildAnthropicMessagesToGeminiBodyNormal(t *testing.T) {
	body := map[string]any{
		"model":  "claude-x",
		"system": "stay terse",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "see this"},
					map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": "image/png",
							"data":       "AAAA",
						},
					},
				},
			},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_1",
						"name":  "lookup",
						"input": map[string]any{"q": "hi"},
					},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_1",
						"content":     map[string]any{"ok": true},
					},
				},
			},
		},
		"tools": []any{
			map[string]any{
				"name":        "lookup",
				"description": "find it",
				"input_schema": map[string]any{
					"type": "object", "properties": map[string]any{},
				},
			},
		},
		"tool_choice": "any",
	}
	result, err := BuildAnthropicMessagesToGeminiBody(body, BridgeRequestBodyOptions{ModelOverride: "gemini-x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["model"] != "gemini-x" {
		t.Errorf("model = %v", result["model"])
	}
	systemInstruction := result["systemInstruction"].(map[string]any)
	if systemInstruction["parts"].([]any)[0].(map[string]any)["text"] != "stay terse" {
		t.Errorf("systemInstruction = %s", mustJSON(t, systemInstruction))
	}
	contents, _ := result["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("contents = %s", mustJSON(t, contents))
	}
	if contents[0].(map[string]any)["role"] != "user" || contents[1].(map[string]any)["role"] != "model" {
		t.Errorf("roles = %s", mustJSON(t, contents))
	}
	imagePart := contents[0].(map[string]any)["parts"].([]any)[1].(map[string]any)
	inlineData := imagePart["inlineData"].(map[string]any)
	if inlineData["mimeType"] != "image/png" || inlineData["data"] != "AAAA" {
		t.Errorf("image part = %s", mustJSON(t, imagePart))
	}
	functionCall := contents[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	if functionCall["name"] != "lookup" {
		t.Errorf("functionCall = %s", mustJSON(t, functionCall))
	}
	functionResponse := contents[2].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if functionResponse["name"] != "toolu_1" {
		t.Errorf("functionResponse = %s", mustJSON(t, functionResponse))
	}
	if response := functionResponse["response"].(map[string]any); response["ok"] != true {
		t.Errorf("functionResponse.response = %s", mustJSON(t, functionResponse))
	}
	toolConfig := result["toolConfig"].(map[string]any)
	if toolConfig["functionCallingConfig"].(map[string]any)["mode"] != "ANY" {
		t.Errorf("toolConfig = %s", mustJSON(t, result))
	}
}

func TestBuildAnthropicMessagesToGeminiBodyURLImageAndSystemBlocks(t *testing.T) {
	body := map[string]any{
		"model": "claude-x",
		"system": []any{
			map[string]any{"type": "text", "text": "line one"},
			map[string]any{"type": "text", "text": "line two"},
		},
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "image",
						"source": map[string]any{
							"type": "url",
							"url":  "https://example.com/dog.webp",
						},
					},
				},
			},
		},
	}
	result, err := BuildAnthropicMessagesToGeminiBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	systemText := result["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]
	if systemText != "line one\nline two" {
		t.Errorf("system text = %v", systemText)
	}
	parts := result["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	fileData := parts[0].(map[string]any)["fileData"].(map[string]any)
	if fileData["mimeType"] != "image/webp" {
		t.Errorf("fileData = %s", mustJSON(t, parts[0]))
	}
}

// ---- anthropic source: guidance / validation rejects (verbatim) ----

func TestBuildAnthropicMessagesToGeminiBodyRejects(t *testing.T) {
	t.Run("anthropic_state", func(t *testing.T) {
		_, err := BuildAnthropicMessagesToGeminiBody(map[string]any{
			"model":    "claude-x",
			"thinking": map[string]any{"type": "enabled"},
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		}, BridgeRequestBodyOptions{GuidanceProviderName: "Acme", Stream: true})
		requireGeminiGuidance(t, err,
			"unsupported_anthropic_state_for_gemini_native",
			"当前 Acme Gemini native 上游不能保真承载 Anthropic thinking / cache_control。请改用真实 Anthropic Messages 上游，或移除该能力后重试。",
			GeminiNativeProtocolMessages, true, "claude-x")
	})
	t.Run("anthropic_content_block", func(t *testing.T) {
		_, err := BuildAnthropicMessagesToGeminiBody(map[string]any{
			"model": "claude-x",
			"messages": []any{
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{"type": "document"},
					},
				},
			},
		}, BridgeRequestBodyOptions{})
		requireGeminiGuidance(t, err,
			"unsupported_anthropic_content_part_for_gemini_native",
			"当前 Gemini native 上游不能保真承载 Anthropic content block: document。请移除该 block 后重试。",
			GeminiNativeProtocolMessages, false, "claude-x")
	})
	t.Run("anthropic_system_block", func(t *testing.T) {
		_, err := BuildAnthropicMessagesToGeminiBody(map[string]any{
			"model": "claude-x",
			"system": []any{
				map[string]any{"type": "image"},
			},
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		}, BridgeRequestBodyOptions{})
		requireGeminiGuidance(t, err,
			"unsupported_anthropic_system_part_for_gemini_native",
			"当前 Gemini native 上游只支持 Anthropic system text block。请移除非 text block 后重试。",
			GeminiNativeProtocolMessages, false, "claude-x")
	})
	t.Run("anthropic_image_source", func(t *testing.T) {
		_, err := BuildAnthropicMessagesToGeminiBody(map[string]any{
			"model": "claude-x",
			"messages": []any{
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{
							"type": "image",
							"source": map[string]any{
								"type":    "file",
								"file_id": "file-1",
							},
						},
					},
				},
			},
		}, BridgeRequestBodyOptions{})
		requireGeminiGuidance(t, err,
			"unsupported_anthropic_image_source_for_gemini_native",
			"当前 Gemini native 上游不能保真承载该 Anthropic image source。请使用 base64 或 URL 图片后重试。",
			GeminiNativeProtocolMessages, false, "claude-x")
	})
	t.Run("invalid_messages", func(t *testing.T) {
		_, err := BuildAnthropicMessagesToGeminiBody(map[string]any{
			"model": "claude-x",
		}, BridgeRequestBodyOptions{})
		validation := requireBridgeValidation(t, err, "invalid_messages_gemini_bridge_messages")
		if validation.Message != "Anthropic Messages 到 Gemini native 桥接要求 messages 是数组" {
			t.Errorf("message = %q", validation.Message)
		}
	})
}

// ---- dispatch surface: the two migrated pairs bridge for real ----

func TestBuildBridgeRequestBodyGeminiNativePairs(t *testing.T) {
	responsesBody := map[string]any{
		"model": "gpt-x",
		"input": "hello",
		"tools": []any{
			map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}},
		},
	}
	result, err := BuildBridgeRequestBody(FamilyResponses, FamilyGeminiStreamGenerate, responsesBody, BridgeRequestBodyOptions{ModelOverride: "gemini-x"})
	if err != nil {
		t.Fatalf("responses->gemini: %v", err)
	}
	if result["contents"] == nil || result["tools"] == nil {
		t.Errorf("responses->gemini body = %s", mustJSON(t, result))
	}
	anthropicBody := map[string]any{
		"model":    "claude-x",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}
	result, err = BuildBridgeRequestBody("messages", FamilyGeminiGenerateContent, anthropicBody, BridgeRequestBodyOptions{ModelOverride: "gemini-x"})
	if err != nil {
		t.Fatalf("anthropic->gemini (stored token): %v", err)
	}
	if result["contents"] == nil {
		t.Errorf("anthropic->gemini body = %s", mustJSON(t, result))
	}
	// Unknown source keeps the Node sourceBodyToGeminiGenerateContentBody
	// fallback, not the generic family-pair error.
	_, err = BuildBridgeRequestBody(FamilyGeminiStreamGenerate, FamilyGeminiGenerateContent, map[string]any{}, BridgeRequestBodyOptions{})
	validation := requireBridgeValidation(t, err, "unsupported_gemini_target_bridge_source")
	if validation.Message != "当前下游协议不能桥接到 Gemini native GenerateContent" {
		t.Errorf("message = %q", validation.Message)
	}
}

// ---- end-to-end over an httptest fake upstream (construct -> send -> render) ----

func TestGeminiNativeBridgeRoundTripThroughFakeUpstream(t *testing.T) {
	type upstreamCall struct {
		path        string
		contentType string
		body        map[string]any
	}
	var calls []upstreamCall
	geminiJSON := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{
				"role": "model",
				"parts": []any{
					map[string]any{"text": "found 1"},
				},
			},
			"finishReason": "STOP",
		}},
		"usageMetadata": map[string]any{
			"promptTokenCount":     float64(6),
			"candidatesTokenCount": float64(2),
			"totalTokenCount":      float64(8),
		},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		parsed := map[string]any{}
		_ = json.Unmarshal(raw, &parsed)
		calls = append(calls, upstreamCall{
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			body:        parsed,
		})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(geminiJSON)
	}))
	defer upstream.Close()

	for _, tc := range []struct {
		name             string
		sourceFamily     string
		clientBody       map[string]any
		wantProtocol     GeminiNativeDownstreamProtocol
		wantModel        string
		wantContentsRole string
	}{
		{
			name:         "responses_to_gemini",
			sourceFamily: FamilyResponses,
			clientBody: map[string]any{
				"model":        "gpt-x",
				"instructions": "be terse",
				"input":        "hello",
			},
			wantProtocol:     GeminiNativeProtocolResponses,
			wantModel:        "gemini-x",
			wantContentsRole: "user",
		},
		{
			name:         "anthropic_messages_to_gemini",
			sourceFamily: FamilyAnthropicMessages,
			clientBody: map[string]any{
				"model":    "claude-x",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
			},
			wantProtocol:     GeminiNativeProtocolMessages,
			wantModel:        "gemini-x",
			wantContentsRole: "user",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 1. Construct through the dispatch surface.
			bridgeBody, err := BuildBridgeRequestBody(tc.sourceFamily, FamilyGeminiGenerateContent, tc.clientBody, BridgeRequestBodyOptions{ModelOverride: tc.wantModel})
			if err != nil {
				t.Fatalf("construct: %v", err)
			}
			encoded, err := json.Marshal(bridgeBody)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}

			// 2. Send to the fake gemini upstream.
			req, err := http.NewRequest(http.MethodPost, upstream.URL+"/v1beta/models/gemini-x:generateContent", bytes.NewReader(encoded))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			raw, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("upstream status = %d body=%s", resp.StatusCode, raw)
			}
			if len(calls) == 0 || calls[len(calls)-1].body["contents"] == nil {
				t.Fatalf("upstream never received the bridged contents: %s", mustJSON(t, calls))
			}
			lastCall := calls[len(calls)-1]
			contents, _ := lastCall.body["contents"].([]any)
			first := contents[0].(map[string]any)
			if first["role"] != tc.wantContentsRole {
				t.Errorf("bridged role = %v", first["role"])
			}

			// 3. Render the upstream gemini JSON back to the downstream protocol.
			rendered := TransformGeminiJSONBufferToDownstreamJSON(raw, tc.wantProtocol, tc.wantModel)
			downstream := map[string]any{}
			if err := json.Unmarshal(rendered, &downstream); err != nil {
				t.Fatalf("rendered json: %v (%s)", err, rendered)
			}
			switch tc.wantProtocol {
			case GeminiNativeProtocolResponses:
				if downstream["object"] != "response" {
					t.Errorf("object = %v (%s)", downstream["object"], rendered)
				}
				output, _ := downstream["output"].([]any)
				if len(output) == 0 || output[0].(map[string]any)["type"] != "message" {
					t.Errorf("output = %s", rendered)
				}
				usage := downstream["usage"].(map[string]any)
				if usage["input_tokens"] != float64(6) || usage["output_tokens"] != float64(2) {
					t.Errorf("usage = %s", mustJSON(t, usage))
				}
			case GeminiNativeProtocolMessages:
				if downstream["type"] != "message" || downstream["role"] != "assistant" {
					t.Errorf("message = %s", rendered)
				}
				content := downstream["content"].([]any)
				if len(content) == 0 || content[0].(map[string]any)["text"] != "found 1" {
					t.Errorf("content = %s", rendered)
				}
				usage := downstream["usage"].(map[string]any)
				if usage["input_tokens"] != float64(6) || usage["output_tokens"] != float64(2) {
					t.Errorf("usage = %s", mustJSON(t, usage))
				}
			}
		})
	}
}

func TestGeminiNativeBridgeSseRoundTripToAnthropicAndResponses(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"你好"}]}}],"modelVersion":"gemini-x"}`,
		"",
		`data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":2,"totalTokenCount":8}}`,
		"",
	}, "\n")

	anthropicRendered := string(TransformGeminiSseBufferToDownstreamSse([]byte(sse), GeminiNativeProtocolMessages, "gemini-x"))
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		`"text_delta"`,
		`"text":"你好"`,
		"event: message_delta",
		`"stop_reason":"end_turn"`,
		"event: message_stop",
	} {
		if !strings.Contains(anthropicRendered, want) {
			t.Errorf("anthropic sse missing %s in %s", want, anthropicRendered)
		}
	}

	responsesRendered := string(TransformGeminiSseBufferToDownstreamSse([]byte(sse), GeminiNativeProtocolResponses, "gemini-x"))
	for _, want := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		`"delta":"你好"`,
		"event: response.completed",
	} {
		if !strings.Contains(responsesRendered, want) {
			t.Errorf("responses sse missing %s in %s", want, responsesRendered)
		}
	}
}
