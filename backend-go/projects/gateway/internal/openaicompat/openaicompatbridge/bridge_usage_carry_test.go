package openaicompatbridge

import (
	"strings"
	"testing"
)

// T9b：Chat 上游 → Anthropic 客户端流式桥接。message_start 在首个内容 delta
// 时就必须发出，此时上游 usage chunk 尚未到达，只能写 input_tokens=0 占位；
// 收尾的 message_delta 必须回填真实 input，否则客户端与网关两侧合并 usage
// 后 input 恒为 0（docs/待办与阻塞事项.md T9b）。
func TestAnthropicFromChatStreamMessageDeltaCarriesInputTokens(t *testing.T) {
	fixture := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":270139,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":100864}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	state := NewAnthropicFromChatStreamState("claude-test")
	out := &strings.Builder{}
	if err := PumpBridgeSseTransform(strings.NewReader(fixture), out, func(event string) []string {
		return ProcessChatCompletionsSseEventAsAnthropic(state, event)
	}, nil); err != nil {
		t.Fatalf("pump: %v", err)
	}
	rendered := out.String()
	deltaIndex := strings.Index(rendered, "event: message_delta")
	if deltaIndex < 0 {
		t.Fatalf("anthropic SSE missing message_delta:\n%s", rendered)
	}
	delta := rendered[deltaIndex:]
	if !strings.Contains(delta, `"input_tokens":270139`) {
		t.Fatalf("message_delta missing real input_tokens (placeholder never corrected):\n%s", delta)
	}
	if !strings.Contains(delta, `"cache_read_input_tokens":100864`) {
		t.Fatalf("message_delta missing cache_read_input_tokens:\n%s", delta)
	}
	if !strings.Contains(delta, `"output_tokens":2`) {
		t.Fatalf("message_delta missing output_tokens:\n%s", delta)
	}
}

// T9b 镜像方向：上游把 Chat 口径填 0 占位、真实值放在 Responses 口径时，
// 桥接出的客户端 usage 不得跟着归零（契约见
// docs/functions/模型价格与用量统计口径.md §3.2）。
func TestBridgeUsageTokenPrefersNonZeroAcrossShapes(t *testing.T) {
	usage := map[string]any{
		"prompt_tokens":     float64(0),
		"input_tokens":      float64(270139),
		"completion_tokens": float64(0),
		"output_tokens":     float64(148),
	}

	anthropic := chatUsageToAnthropicUsage(usage)
	if got := anthropic["input_tokens"]; got != float64(270139) {
		t.Fatalf("anthropic input_tokens = %v, want 270139", got)
	}
	if got := anthropic["output_tokens"]; got != float64(148) {
		t.Fatalf("anthropic output_tokens = %v, want 148", got)
	}

	gemini := chatUsageToGeminiUsage(usage)
	if got := gemini["promptTokenCount"]; got != float64(270139) {
		t.Fatalf("gemini promptTokenCount = %v, want 270139", got)
	}
	if got := gemini["candidatesTokenCount"]; got != float64(148) {
		t.Fatalf("gemini candidatesTokenCount = %v, want 148", got)
	}

	codex := codexChatUsageToResponsesUsage(usage)
	if got := codex["input_tokens"]; got != float64(270139) {
		t.Fatalf("responses input_tokens = %v, want 270139", got)
	}
	if got := codex["output_tokens"]; got != float64(148) {
		t.Fatalf("responses output_tokens = %v, want 148", got)
	}

	// 两套都非零时维持 Chat 口径优先（既有行为不变）。
	both := map[string]any{
		"prompt_tokens": float64(20),
		"input_tokens":  float64(25),
	}
	if got := chatUsageToAnthropicUsage(both)["input_tokens"]; got != float64(20) {
		t.Fatalf("both non-zero must keep chat-shape precedence, got %v", got)
	}
}
