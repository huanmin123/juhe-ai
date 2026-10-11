package modelcheckprobe

import (
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// w16g：protocol_basic 输出预算回归（BUG-0308，信封统一设计 §4.3）。
// 2026-10-11 生产事故：某中转强制输出最低 100，旧预算 16 被 400 拒绝，
// 模型检测整轮误判"不可检测 0/100"。回归钉死：protocol_basic 家族四条
// 构建路径（目标/流式/可信对比目标/可信对比对照）的输出预算必须等于
// protocolBasicOutputTokens（1024 ≥ 事故上游最低值 100）。
func TestW16gProtocolBasicBudgetMatchesUpstreamMinimums(t *testing.T) {
	const model = "gpt-6.1-sol"
	cases := []struct {
		name     string
		build    func() (Request, error)
		field    string
		protocol modelcheckprofile.Protocol
	}{
		{"responses_basic", func() (Request, error) {
			suite := Suite{Model: model, Protocol: modelcheckprofile.ProtocolOpenAIResponses}
			return suite.tunedBasic(model, "Reply with exactly: OK-MODEL-CHECK", modelcheckprofile.EndpointModeResponsesSSE, true, protocolBasicOutputTokens, 0)
		}, "max_output_tokens", modelcheckprofile.ProtocolOpenAIResponses},
		{"chat_basic", func() (Request, error) {
			suite := Suite{Model: model, Protocol: modelcheckprofile.ProtocolOpenAIChat}
			return suite.tunedBasic(model, "Reply with exactly: OK-MODEL-CHECK", modelcheckprofile.EndpointModeChatSSE, true, protocolBasicOutputTokens, 0)
		}, "max_tokens", modelcheckprofile.ProtocolOpenAIChat},
		{"anthropic_basic", func() (Request, error) {
			suite := Suite{Model: model, Protocol: modelcheckprofile.ProtocolAnthropic}
			return suite.tunedBasic(model, "Reply with exactly: OK-MODEL-CHECK", modelcheckprofile.EndpointModeMessagesSSE, true, protocolBasicOutputTokens, 0)
		}, "max_tokens", modelcheckprofile.ProtocolAnthropic},
		{"gemini_basic", func() (Request, error) {
			suite := Suite{Model: model, Protocol: modelcheckprofile.ProtocolGeminiNative}
			return suite.tunedBasic(model, "Reply with exactly: OK-MODEL-CHECK", modelcheckprofile.EndpointModeGenerateContentSSE, true, protocolBasicOutputTokens, 0)
		}, "maxOutputTokens", modelcheckprofile.ProtocolGeminiNative},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request, err := test.build()
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(request.Body, &payload); err != nil {
				t.Fatal(err)
			}
			var budget any
			switch test.protocol {
			case modelcheckprofile.ProtocolGeminiNative:
				generation, _ := payload["generationConfig"].(map[string]any)
				budget = generation["maxOutputTokens"]
			default:
				budget = payload[test.field]
			}
			value, ok := budget.(float64)
			if !ok {
				t.Fatalf("预算字段缺失: %v", payload)
			}
			if int(value) != protocolBasicOutputTokens {
				t.Fatalf("protocol_basic 输出预算 = %v, 期望 %d（≥ 事故上游最低值 100）", budget, protocolBasicOutputTokens)
			}
		})
	}
}
