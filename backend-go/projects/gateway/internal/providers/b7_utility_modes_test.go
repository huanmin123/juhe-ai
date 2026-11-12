package providers

import (
	"encoding/json"
	"testing"
)

func TestCustomModelUtilityModes(t *testing.T) {
	for _, mode := range []string{"embedding", "rerank", "moderation"} {
		modeCopy := mode
		input := customProviderModelUpsertInput{Mode: &modeCopy, Status: "active", InputUsdPer1M: ptrFloat64(1)}
		if message := validateCustomModelPricing("gpt", input); message != "" {
			t.Fatalf("%s 模型只填输入价应通过，得到 %q", mode, message)
		}
		missing := customProviderModelUpsertInput{Mode: &modeCopy, Status: "active", OutputUsdPer1M: ptrFloat64(1)}
		if message := validateCustomModelPricing("gpt", missing); message == "" {
			t.Fatalf("%s 模型缺输入价应拒绝", mode)
		}
	}
	for _, mode := range []string{"realtime", "unknown"} {
		modeCopy := mode
		input := customProviderModelUpsertInput{Mode: &modeCopy, Status: "active", InputUsdPer1M: ptrFloat64(1)}
		if _, err := normalizeCustomModelCapabilities("gpt", customModelCapabilityInput{Mode: input.Mode}); err == nil {
			t.Fatalf("%s 不是合法自定义模型 mode", mode)
		}
	}
}

// TestCustomModelUtilityProtocolsParse 锁定 B7 协议词表：rerank / moderation
// 必须同时被 customModelProtocolEnum（解析层）与 customProviderModelProtocolSet
// （归一化层）接受，否则前端可选但 API 保存失败。
func TestCustomModelUtilityProtocolsParse(t *testing.T) {
	for _, protocol := range []string{"rerank", "moderation"} {
		body := map[string]json.RawMessage{
			"mode":                  json.RawMessage(`"` + protocol + `"`),
			"supportedApiProtocols": json.RawMessage(`["` + protocol + `"]`),
		}
		parsed, ok := parseCustomModelBody(body, customModelParseOptions{})
		if !ok || parsed == nil {
			t.Fatalf("%s 协议应被解析层接受", protocol)
		}
		if parsed.values.Mode == nil || *parsed.values.Mode != protocol {
			t.Fatalf("%s mode 解析 = %+v", protocol, parsed.values.Mode)
		}
		if len(parsed.values.SupportedAPIProtocols) != 1 || parsed.values.SupportedAPIProtocols[0] != protocol {
			t.Fatalf("%s 协议解析 = %+v", protocol, parsed.values.SupportedAPIProtocols)
		}
	}
}
