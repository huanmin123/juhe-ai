package openaicompat

import (
	"encoding/json"
	"testing"
)

func float64Ptr(v float64) *float64 { return &v }
func int64Ptr(v int64) *int64    { return &v }

func TestBuildOpenAIChatBridgeBody(t *testing.T) {
	tests := []struct {
		name       string
		clientBody map[string]any
		options    BridgeRequestBodyOptions
		wantModel  string
		wantErr    bool
	}{
		{
			name: "basic chat body",
			clientBody: map[string]any{
				"model":    "gpt-4",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
			},
			options: BridgeRequestBodyOptions{
				ModelOverride: "claude-3-opus",
			},
			wantModel: "claude-3-opus",
		},
		{
			name: "with temperature and max_tokens",
			clientBody: map[string]any{
				"model":      "gpt-4",
				"messages":   []any{map[string]any{"role": "user", "content": "hello"}},
				"temperature": 0.7,
				"max_tokens":  1000,
			},
			options: BridgeRequestBodyOptions{
				ModelOverride: "claude-3-opus",
				Temperature:   float64Ptr(0.7),
				MaxTokens:     int64Ptr(1000),
			},
			wantModel: "claude-3-opus",
		},
		{
			name: "with system message",
			clientBody: map[string]any{
				"model": "gpt-4",
				"messages": []any{
					map[string]any{"role": "system", "content": "You are helpful"},
					map[string]any{"role": "user", "content": "hello"},
				},
			},
			options: BridgeRequestBodyOptions{
				ModelOverride: "claude-3-opus",
			},
			wantModel: "claude-3-opus",
		},
		{
			name: "with tools",
			clientBody: map[string]any{
				"model": "gpt-4",
				"messages": []any{
					map[string]any{"role": "user", "content": "check files"},
				},
				"tools": []any{
					map[string]any{
						"type": "function",
						"function": map[string]any{
							"name": "search_files",
							"description": "Search files",
							"parameters": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"query": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
			options: BridgeRequestBodyOptions{
				ModelOverride: "claude-3-opus",
			},
			wantModel: "claude-3-opus",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := BuildOpenAIChatBridgeBody(tc.clientBody, tc.options)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result["model"] != tc.wantModel {
				t.Errorf("model = %v, want %v", result["model"], tc.wantModel)
			}
			// Verify model is set
			if model, ok := result["model"].(string); !ok || model == "" {
				t.Error("model not set in result")
			}
		})
	}
}

func TestBuildAnthropicMessagesBody(t *testing.T) {
	tests := []struct {
		name       string
		clientBody map[string]any
		options    BridgeRequestBodyOptions
		wantModel  string
	}{
		{
			name: "basic conversation",
			clientBody: map[string]any{
				"model": "gpt-4",
				"messages": []any{
					map[string]any{"role": "system", "content": "Be helpful"},
					map[string]any{"role": "user", "content": "Hello"},
				},
			},
			options: BridgeRequestBodyOptions{
				ModelOverride: "claude-3-opus-20240229",
			},
			wantModel: "claude-3-opus-20240229",
		},
		{
			name: "with tool calls",
			clientBody: map[string]any{
				"model": "gpt-4",
				"messages": []any{
					map[string]any{
						"role": "assistant",
						"tool_calls": []any{
							map[string]any{
								"id": "call_123",
								"function": map[string]any{
									"name": "get_weather",
									"arguments": `{"location":"San Francisco"}`,
								},
							},
						},
					},
				},
			},
			options: BridgeRequestBodyOptions{
				ModelOverride: "claude-3-opus-20240229",
			},
			wantModel: "claude-3-opus-20240229",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := BuildAnthropicMessagesBody(tc.clientBody, tc.options)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result["model"] != tc.wantModel {
				t.Errorf("model = %v, want %v", result["model"], tc.wantModel)
			}
		})
	}
}

func TestTransformGeminiGenerateContentToOpenAIChatResponse(t *testing.T) {
	tests := []struct {
		name     string
		response map[string]any
		opts     BridgeTransformResponseOptions
		want     map[string]any
	}{
		{
			name: "basic response",
			response: map[string]any{
				"candidates": []any{
					map[string]any{
						"content": map[string]any{
							"text": "Hello from Gemini",
							"role":  "model",
						},
						"finishReason": "FINISH_REASON_UNSPECIFIED",
					},
				},
				"usageMetadata": map[string]any{
					"promptTokenCount":    100,
					"candidatesTokenCount": 50,
					"totalTokenCount":      150,
				},
			},
			opts: BridgeTransformResponseOptions{
				Enabled:                true,
				SourceEndpointFamily:   "gemini_generate_content",
				UpstreamEndpointFamily: "chat_completions",
			},
			want: map[string]any{
				"choices": []any{
					map[string]any{
						"message": map[string]any{
							"role":    "assistant",
							"content": "Hello from Gemini",
						},
						"finish_reason": "stop",
					},
				},
				"usage": map[string]any{
					"prompt_tokens":     100.0,
					"completion_tokens": 50.0,
					"total_tokens":      150.0,
				},
			},
		},
		{
			name: "disabled transformation returns original",
			response: map[string]any{
				"candidates": []any{map[string]any{"content": "test"}},
			},
			opts: BridgeTransformResponseOptions{
				Enabled: false,
			},
			want: map[string]any{
				"candidates": []any{map[string]any{"content": "test"}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := TransformGeminiGenerateContentToOpenAIChatResponse(tc.response, tc.opts)
			if tc.want != nil {
				// Compare JSON output for deep equality
				resultJSON, _ := json.Marshal(result)
				wantJSON, _ := json.Marshal(tc.want)
				if string(resultJSON) != string(wantJSON) {
					t.Errorf("result = %s, want %s", resultJSON, wantJSON)
				}
			}
		})
	}
}

func TestTransformOpenAIAnthropicBridgeUpstreamResponse(t *testing.T) {
	tests := []struct {
		name     string
		response map[string]any
		opts     BridgeTransformResponseOptions
	}{
		{
			name: "transforms usage",
			response: map[string]any{
				"usage": map[string]any{
					"input_tokens":  100.0,
					"output_tokens": 50.0,
				},
				"content": "Hello from Anthropic",
			},
			opts: BridgeTransformResponseOptions{
				Enabled:              true,
				SourceEndpointFamily: "anthropic_messages",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := TransformOpenAIAnthropicBridgeUpstreamResponse(tc.response, tc.opts)
			if result == nil {
				t.Fatal("expected non-nil result")
			}
		})
	}
}

func TestIsCrossProtocolBridgeRequired(t *testing.T) {
	tests := []struct {
		source   string
		upstream string
		want     bool
	}{
		{"chat_completions", "anthropic_messages", true},
		{"chat_completions", "gemini_generate_content", true},
		{"chat_completions", "gemini_stream_generate", true},
		{"gemini_generate_content", "chat_completions", true},
		{"gemini_stream_generate", "chat_completions", true},
		{"chat_completions", "chat_completions", false},
		{"anthropic_messages", "anthropic_messages", false},
		{"gemini_generate_content", "gemini_generate_content", false},
	}

	for _, tc := range tests {
		name := tc.source + "->" + tc.upstream
		if got := IsCrossProtocolBridgeRequired(tc.source, tc.upstream); got != tc.want {
			t.Errorf("%s: isCrossProtocolBridgeRequired = %v, want %v", name, got, tc.want)
		}
	}
}

