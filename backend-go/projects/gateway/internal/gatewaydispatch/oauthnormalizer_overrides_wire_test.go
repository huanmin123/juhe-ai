package gatewaydispatch

import (
	"encoding/json"
	"testing"
)

// B-6（audit batch ag deviation note）wire 覆盖分支补齐测试：
// anthropic_messages 写 output_config.effort；gemini_generate_content 写
// generationConfig.thinkingConfig.thinkingLevel（Node
// provider-request-overrides.ts:62-84）。

func TestApplyGptAccountRequestOverridesBodyAnthropicWire(t *testing.T) {
	body := map[string]any{"model": "m", "messages": []any{}}
	out, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		Credentials:    map[string]any{"reasoning_effort_override": "high"},
		EndpointFamily: "anthropic_messages",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{
			SupportedReasoningEfforts: []string{"high"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outputConfig, ok := out["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "high" {
		t.Fatalf("output_config = %v", out["output_config"])
	}
	// 已有 output_config 键合并而不是覆盖。
	body = map[string]any{"output_config": map[string]any{"existing": 1}}
	out, err = ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		Credentials:    map[string]any{"reasoning_effort_override": "low"},
		EndpointFamily: "messages",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{
			SupportedReasoningEfforts: []string{"low"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outputConfig = out["output_config"].(map[string]any)
	if outputConfig["effort"] != "low" {
		t.Fatalf("output_config merge = %v", outputConfig)
	}
	if _, hasExisting := outputConfig["existing"]; !hasExisting {
		t.Fatalf("existing key lost: %v", outputConfig)
	}
}

func TestApplyGptAccountRequestOverridesBodyGeminiWire(t *testing.T) {
	body := map[string]any{"model": "m", "contents": []any{}}
	out, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		Credentials:    map[string]any{"reasoning_effort_override": "medium"},
		EndpointFamily: "gemini_generate_content",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{
			SupportedReasoningEfforts: []string{"medium"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	generationConfig := out["generationConfig"].(map[string]any)
	thinkingConfig := generationConfig["thinkingConfig"].(map[string]any)
	if thinkingConfig["thinkingLevel"] != "medium" {
		t.Fatalf("thinkingLevel = %v", thinkingConfig["thinkingLevel"])
	}
	// stored row vocabulary + 已有 thinkingConfig 键合并。
	body = map[string]any{
		"generationConfig": map[string]any{
			"thinkingConfig": map[string]any{"includeThoughts": true},
			"temperature":    0.5,
		},
	}
	out, err = ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		Credentials:    map[string]any{"reasoning_effort_override": "xhigh"},
		EndpointFamily: "stream_generate_content",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{
			SupportedReasoningEfforts: []string{"xhigh"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	generationConfig = out["generationConfig"].(map[string]any)
	thinkingConfig = generationConfig["thinkingConfig"].(map[string]any)
	if thinkingConfig["thinkingLevel"] != "xhigh" || thinkingConfig["includeThoughts"] != true {
		t.Fatalf("thinkingConfig merge = %v", thinkingConfig)
	}
	if generationConfig["temperature"] != 0.5 {
		t.Fatalf("temperature lost: %v", generationConfig)
	}
	encoded, _ := json.Marshal(out)
	_ = encoded
}

func TestApplyGptAccountRequestOverridesWireInertWithoutCapability(t *testing.T) {
	// 能力门未通过时跨协议 wire 分支保持惰性。
	body := map[string]any{"model": "m"}
	out, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		Credentials:       map[string]any{"reasoning_effort_override": "high"},
		EndpointFamily:    "anthropic_messages",
		ModelCapabilities: nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, has := out["output_config"]; has {
		t.Fatalf("output_config should stay absent: %v", out)
	}
	// compact 请求不写 reasoning effort。
	out, err = ApplyGptAccountRequestOverridesBody(map[string]any{"model": "m"}, GptAccountOverrideInput{
		Credentials:    map[string]any{"reasoning_effort_override": "high"},
		EndpointFamily: "anthropic_messages",
		Compact:        true,
		ModelCapabilities: &GptRequestOverrideModelCapabilities{
			SupportedReasoningEfforts: []string{"high"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, has := out["output_config"]; has {
		t.Fatalf("compact request should skip effort: %v", out)
	}
}
