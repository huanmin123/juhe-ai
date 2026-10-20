package gatewayopenai

import (
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

func TestExtractUsageChatCompletionsShape(t *testing.T) {
	value := mustParseJSON(t, `{
		"prompt_tokens": 10,
		"completion_tokens": 8,
		"prompt_tokens_details": {"cached_tokens": 4, "image_tokens": 2, "audio_tokens": 1},
		"completion_tokens_details": {"reasoning_tokens": 3, "audio_tokens": 5}
	}`)
	usage := ExtractUsage(value, "")
	assertToken(t, usage.InputTokens, 10, "input")
	assertToken(t, usage.OutputTokens, 8, "output")
	assertToken(t, usage.CacheReadTokens, 4, "cacheRead")
	assertToken(t, usage.InputImageTokens, 2, "inputImage")
	assertToken(t, usage.InputAudioTokens, 1, "inputAudio")
	assertToken(t, usage.OutputAudioTokens, 5, "outputAudio")
	assertToken(t, usage.ThinkingTokens, 3, "thinking")
	if usage.ServiceTier != "" {
		t.Fatalf("service tier = %q, want empty", usage.ServiceTier)
	}
}

func TestExtractUsageResponsesShapeAndCacheWriteChains(t *testing.T) {
	value := mustParseJSON(t, `{
		"input_tokens": 20,
		"output_tokens": 6,
		"input_tokens_details": {
			"cached_tokens": 5,
			"cache_creation": {"ephemeral_5m_input_tokens": 7, "ephemeral_1h_input_tokens": 2}
		},
		"output_tokens_details": {"image_tokens": 4}
	}`)
	usage := ExtractUsage(value, "")
	assertToken(t, usage.InputTokens, 20, "input")
	assertToken(t, usage.OutputTokens, 6, "output")
	assertToken(t, usage.CacheReadTokens, 5, "cacheRead")
	assertToken(t, usage.CacheWriteTokens, 9, "cacheWrite=5m+1h")
	assertToken(t, usage.CacheWrite1hTokens, 2, "cacheWrite1h")
	assertToken(t, usage.OutputImageTokens, 4, "outputImage")
}

func TestExtractUsageDualShapePicksNonZeroToken(t *testing.T) {
	// 生产 trace 491069b3-2457-492d-a483-26b05ae4bdfd：Chat 兼容层中转在
	// input_tokens/output_tokens 里填 0 占位，真实规模只在 prompt_tokens。
	// 契约见 docs/functions/模型价格与用量统计口径.md §3.2。
	production := mustParseJSON(t, `{
		"prompt_tokens": 270139,
		"completion_tokens": 0,
		"total_tokens": 270139,
		"input_tokens": 0,
		"output_tokens": 0,
		"claude_cache_creation_5_m_tokens": 0
	}`)
	usage := ExtractUsage(production, "")
	assertToken(t, usage.InputTokens, 270139, "input (fallback to prompt_tokens when input_tokens=0)")
	assertToken(t, usage.OutputTokens, 0, "output (both shapes zero)")

	cases := []struct {
		name       string
		raw        string
		wantInput  *int
		wantOutput *int
	}{
		{
			name:       "both non-zero keeps Responses shape precedence",
			raw:        `{"input_tokens": 20, "output_tokens": 6, "prompt_tokens": 25, "completion_tokens": 9}`,
			wantInput:  intPtr(20),
			wantOutput: intPtr(6),
		},
		{
			name:       "output_tokens zero falls back to completion_tokens",
			raw:        `{"input_tokens": 20, "output_tokens": 0, "completion_tokens": 9}`,
			wantInput:  intPtr(20),
			wantOutput: intPtr(9),
		},
		{
			name:       "chat shape only",
			raw:        `{"prompt_tokens": 12, "completion_tokens": 4}`,
			wantInput:  intPtr(12),
			wantOutput: intPtr(4),
		},
		{
			name:       "responses shape only",
			raw:        `{"input_tokens": 12, "output_tokens": 4}`,
			wantInput:  intPtr(12),
			wantOutput: intPtr(4),
		},
		{
			name:       "all zero is a real zero fact",
			raw:        `{"input_tokens": 0, "output_tokens": 0, "prompt_tokens": 0, "completion_tokens": 0}`,
			wantInput:  intPtr(0),
			wantOutput: intPtr(0),
		},
		{
			name:       "missing everything stays nil for estimation fallback",
			raw:        `{}`,
			wantInput:  nil,
			wantOutput: nil,
		},
		{
			// primary 缺失 + secondary 在场为 0 必须与「全缺失」区分：
			// 前者是上游明确回报零 token，不能触发估算覆盖。
			name:       "primary missing with secondary zero keeps zero not nil",
			raw:        `{"prompt_tokens": 0}`,
			wantInput:  intPtr(0),
			wantOutput: nil,
		},
		{
			// 部分中转把 token 数按字符串返回，numberValue 需照常解析。
			name:       "numeric strings parse then fall back on zero",
			raw:        `{"input_tokens": "0", "prompt_tokens": "270139", "output_tokens": "0", "completion_tokens": "148"}`,
			wantInput:  intPtr(270139),
			wantOutput: intPtr(148),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractUsage(mustParseJSON(t, tc.raw), "")
			assertOptionalToken(t, got.InputTokens, tc.wantInput, "input")
			assertOptionalToken(t, got.OutputTokens, tc.wantOutput, "output")
		})
	}
}

func intPtr(value int) *int { return &value }

func assertOptionalToken(t *testing.T, got *int, want *int, label string) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("%s = %d, want nil", label, *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("%s = nil, want %d", label, *want)
	}
	if *got != *want {
		t.Fatalf("%s = %d, want %d", label, *got, *want)
	}
}

func TestExtractUsageClaudeCacheFallbackKeys(t *testing.T) {
	value := mustParseJSON(t, `{
		"input_tokens": 9,
		"cache_creation_input_tokens": 11,
		"cache_creation": {"ephemeral_5m_input_tokens": 6, "ephemeral_1h_input_tokens": 5},
		"output_image_count": 3
	}`)
	usage := ExtractUsage(value, "")
	assertToken(t, usage.CacheWriteTokens, 11, "cacheWrite")
	assertToken(t, usage.CacheWrite1hTokens, 5, "cacheWrite1h")
	assertToken(t, usage.OutputImageCount, 3, "outputImageCount")
}

func TestParseUsageFromJSONBufferWithServiceTier(t *testing.T) {
	body := []byte(`{"id":"x","service_tier":"priority","usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	usage := ParseUsageFromJSONBuffer(body)
	assertToken(t, usage.InputTokens, 3, "input")
	assertToken(t, usage.OutputTokens, 2, "output")
	if usage.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want priority", usage.ServiceTier)
	}

	invalid := ParseUsageFromJSONBuffer([]byte(`{"usage":{"prompt_tokens":`))
	if gatewayproto.HasAnyUsageValue(invalid) {
		t.Fatalf("broken json must yield empty usage, got %+v", invalid)
	}
}

func TestParseUsageFromJSONTextFragmentLastObjectWins(t *testing.T) {
	text := `data: {"type":"response.completed","response":{"usage":{"input_tokens":30,"output_tokens":9}}}` +
		`data: {"usage":{"input_tokens":31,"output_tokens":10}}` + "\n"
	usage := ParseUsageFromJSONTextFragment(text)
	assertToken(t, usage.InputTokens, 31, "input (last usage object)")
	assertToken(t, usage.OutputTokens, 10, "output (last usage object)")

	// Deeply nested oversized-event fragment (usage tail scan).
	fragment := `{"type":"response.completed","response":{"id":"r","usage":{"input_tokens":7,"output_tokens":4,"service_tier":"default"}}}`
	usage = ParseUsageFromJSONTextFragment(fragment)
	assertToken(t, usage.InputTokens, 7, "input (fragment)")
	assertToken(t, usage.OutputTokens, 4, "output (fragment)")
	if usage.ServiceTier != "default" {
		t.Fatalf("service tier = %q, want default", usage.ServiceTier)
	}
}

func TestNormalizeServiceTierRules(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"priority", "priority"},
		{"default", "default"},
		{"flex", "flex"},
		{" auto ", ""},
		{"UPPER", "UPPER"},
		{"-invalid", ""},
		{".ok1", ""},
		{"a.b_c-d1", "a.b_c-d1"},
		{123, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := normalizeServiceTier(tc.in); got != tc.want {
			t.Fatalf("normalizeServiceTier(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEstimateTokenCountFromText(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"   ", 0},
		{"abcd", 1},
		{"abcde", 2},
		{"-", 1},
		{"MOCK", 1},
		{"STREAM", 2},
		{"你好", 2},
		{"你a", 2},
		{"é", 1},
	}
	for _, tc := range cases {
		if got := EstimateTokenCountFromText(tc.text); got != tc.want {
			t.Fatalf("EstimateTokenCountFromText(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestEstimateTokensSkipKeysAndBase64(t *testing.T) {
	body := mustParseJSON(t, `{
		"model": "gpt-mock",
		"stream": true,
		"messages": [{"role": "user", "content": "hello world"}],
		"attachments": [{"data": "`+longBase64(600)+`"}]
	}`)
	tokens := EstimateTokensFromRequestValue(body)
	// "model"/"stream" skipped; base64 payload skipped; message text counted.
	if tokens == 0 {
		t.Fatal("expected non-zero estimate")
	}
	if tokens > 20 {
		t.Fatalf("estimate = %d, suspiciously large (base64/model not skipped?)", tokens)
	}
}

func longBase64(length int) string {
	raw := make([]byte, length)
	for index := range raw {
		raw[index] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"[index%64]
	}
	return string(raw)
}

func assertToken(t *testing.T, value *int, want int, label string) {
	t.Helper()
	if value == nil {
		t.Fatalf("%s: nil, want %d", label, want)
	}
	if *value != want {
		t.Fatalf("%s = %d, want %d", label, *value, want)
	}
}

func mustParseJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseUsageFromJSONBufferImageOutputCountFromData(t *testing.T) {
	// images 形：usage 无显式张数键时按 data[] 计数（2026-10-17 volcengine
	// seedream 实弹取证：上游 usage 只带 token 数，按张价 OutputUsdPerImage
	// 需要张数计量，缺此推导时按张计费落 0 成本）。
	images := ParseUsageFromJSONBuffer([]byte(`{"created":1,"data":[{"b64_json":"QUFB"},{"url":"https://img.example/a.png"}],"usage":{"input_tokens":10,"output_tokens":4096,"total_tokens":4106}}`))
	assertToken(t, images.OutputTokens, 4096, "output tokens")
	assertToken(t, images.OutputImageCount, 2, "image count from data[]")

	// embeddings 形（条目为 embedding 键）不误判。
	embeddings := ParseUsageFromJSONBuffer([]byte(`{"data":[{"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`))
	if embeddings.OutputImageCount != nil {
		t.Fatalf("embeddings data[] 不得误判为图片张数, got %v", *embeddings.OutputImageCount)
	}

	// 上游显式张数优先，不覆盖。
	explicit := ParseUsageFromJSONBuffer([]byte(`{"data":[{"url":"https://img.example/a.png"}],"usage":{"output_tokens":16,"output_image_count":3}}`))
	assertToken(t, explicit.OutputImageCount, 3, "explicit count wins")

	// 非对象条目门控：整体不推导。
	mixed := ParseUsageFromJSONBuffer([]byte(`{"data":["junk"],"usage":{"output_tokens":1}}`))
	if mixed.OutputImageCount != nil {
		t.Fatalf("非对象条目不得推导张数, got %v", *mixed.OutputImageCount)
	}
}
