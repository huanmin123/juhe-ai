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

		t.Run(name+" no output and not completed stays untouched", func(t *testing.T) {
			next, estimated, _, _ := driver.ApplyStreamUsageFallback(
				facts, gatewayproto.EmptyUsage(), UsageFallbackInput{})
			if estimated {
				t.Fatal("interrupted stream without visible output must not be estimated")
			}
			if next.InputTokens != nil || next.OutputTokens != nil {
				t.Fatalf("usage = %+v, want empty", next)
			}
		})
	}

	// OpenAI 口径：收到成功终止事件但无可见输出时仍估算输入 token（上游已
	// 处理 prompt），Anthropic/Gemini 口径则以可见输出为门槛——这是既有
	// 协议实现的分歧，适配器必须原样透传，不得统一。
	t.Run("openai completed without output estimates input only", func(t *testing.T) {
		next, estimated, estimatedInput, estimatedOutput := NewOpenAIResponseDriver().ApplyStreamUsageFallback(
			facts, gatewayproto.EmptyUsage(), UsageFallbackInput{Completed: true})
		if !estimated || estimatedInput == nil || *estimatedInput <= 0 {
			t.Fatalf("estimated = %v, input = %v", estimated, estimatedInput)
		}
		if estimatedOutput != nil || next.OutputTokens != nil {
			t.Fatalf("completed without output must not estimate output: %v / %+v", estimatedOutput, next)
		}
	})
	t.Run("anthropic completed without output stays untouched", func(t *testing.T) {
		for _, driver := range []ResponseDriverPort{NewAnthropicResponseDriver(), NewGeminiResponseDriver()} {
			_, estimated, _, _ := driver.ApplyStreamUsageFallback(
				facts, gatewayproto.EmptyUsage(), UsageFallbackInput{Completed: true})
			if estimated {
				t.Fatal("anthropic/gemini fallback requires visible output by contract")
			}
		}
	})
}
