package gatewayresponse

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// T9c：三个协议适配器都必须把 ApplyStreamUsageFallback 暴露到 finalization
// 端口上——生产组合根正是通过该端口运行估算兜底（此前端口未接线，估算
// 分支对生产流量恒空转，见 docs/待办与阻塞事项.md T9c）。
func TestResponseDriverApplyStreamUsageFallback(t *testing.T) {
	body := map[string]any{
		"model": "mock-model",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello world, please estimate me"},
		},
	}
	facts := UsageFallbackFacts{JSONBody: body, JSONParsed: true}
	drivers := map[string]ResponseDriverPort{
		"openai":    NewOpenAIResponseDriver(),
		"anthropic": NewAnthropicResponseDriver(),
		"gemini":    NewGeminiResponseDriver(),
	}
	for name, driver := range drivers {
		t.Run(name+" estimates when usage missing", func(t *testing.T) {
			next, estimated, estimatedInput, estimatedOutput := driver.ApplyStreamUsageFallback(
				facts,
				gatewayproto.EmptyUsage(),
				UsageFallbackInput{
					Completed:             true,
					OutputReceived:        true,
					EstimatedOutputTokens: 5,
					HasEstimatedOutput:    true,
				},
			)
			if !estimated {
				t.Fatal("fallback did not estimate missing usage")
			}
			if estimatedInput == nil || *estimatedInput <= 0 {
				t.Fatalf("estimated input = %v, want positive", estimatedInput)
			}
			if estimatedOutput == nil || *estimatedOutput != 5 {
				t.Fatalf("estimated output = %v, want 5", estimatedOutput)
			}
			if next.InputTokens == nil || *next.InputTokens != *estimatedInput {
				t.Fatalf("usage input = %v, want %d", next.InputTokens, *estimatedInput)
			}
			if next.OutputTokens == nil || *next.OutputTokens != 5 {
				t.Fatalf("usage output = %v, want 5", next.OutputTokens)
			}
		})

		t.Run(name+" keeps real usage", func(t *testing.T) {
			input, output := 20, 6
			usage := gatewayproto.ParsedUsage{InputTokens: &input, OutputTokens: &output}
			next, estimated, _, _ := driver.ApplyStreamUsageFallback(
				facts, usage, UsageFallbackInput{
					Completed:             true,
					OutputReceived:        true,
					EstimatedOutputTokens: 5,
					HasEstimatedOutput:    true,
				})
			if estimated {
				t.Fatal("complete usage must not be overwritten by estimation")
			}
			if next.InputTokens == nil || *next.InputTokens != 20 || next.OutputTokens == nil || *next.OutputTokens != 6 {
				t.Fatalf("usage = %+v, want input 20 / output 6", next)
			}
		})
	}
}
