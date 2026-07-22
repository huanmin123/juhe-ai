package gemini

import (
	"errors"
	"testing"
)

func int64p(value int64) *int64 { return &value }

func TestParseJSONUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		body  string
		usage Usage
	}{
		{
			name: "generate content usage",
			body: `{"service_tier":"standard","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":7,"cachedContentTokenCount":3,"thoughtsTokenCount":2}}`,
			usage: Usage{
				ReportedServiceTier: "standard",
				InputTokens:         int64p(12),
				OutputTokens:        int64p(9),
				CacheReadTokens:     int64p(3),
				ThinkingTokens:      int64p(2),
			},
		},
		{
			name: "interactions nested usage",
			body: `{"id":"interaction-1","service_tier":"priority","metadata":{"total_usage":{"input_tokens":"7","output_tokens":3,"thought_tokens":2,"cached_tokens":1}}}`,
			usage: Usage{
				ReportedServiceTier: "priority",
				InputTokens:         int64p(7),
				OutputTokens:        int64p(5),
				CacheReadTokens:     int64p(1),
				ThinkingTokens:      int64p(2),
			},
		},
		{
			name: "interaction object usage and tier",
			body: `{"interaction":{"id":"interaction-2","service_tier":"flex","usage":{"total_input_tokens":4,"total_output_tokens":5,"total_thought_tokens":1}}}`,
			usage: Usage{
				ReportedServiceTier: "flex",
				InputTokens:         int64p(4),
				OutputTokens:        int64p(6),
				ThinkingTokens:      int64p(1),
			},
		},
		{
			name:  "invalid values are ignored",
			body:  `{"service_tier":" priority ","usageMetadata":{"promptTokenCount":-1,"candidatesTokenCount":1.5,"thoughtsTokenCount":"NaN"}}`,
			usage: Usage{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseJSON([]byte(tt.body), JSONOptions{})
			if err != nil {
				t.Fatalf("ParseJSON() error = %v", err)
			}
			assertUsageEqual(t, got.Usage, tt.usage)
			if !got.Terminal {
				t.Fatal("a complete JSON response must be terminal")
			}
		})
	}
}

func TestParseJSONTerminalMetadata(t *testing.T) {
	t.Parallel()

	completed, err := ParseJSON([]byte(`{"interaction":{"id":"interaction-1","status":"completed"}}`), JSONOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Failed || completed.Status != "completed" || completed.InteractionID != "interaction-1" {
		t.Fatalf("completed result = %#v", completed)
	}

	failed, err := ParseJSON([]byte(`{"error":{"status":"UNAVAILABLE","message":"temporary"}}`), JSONOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !failed.Failed || failed.Status != "failed" || failed.ErrorCode != "UNAVAILABLE" || failed.ErrorMessage != "temporary" {
		t.Fatalf("failed result = %#v", failed)
	}
}

func TestParseJSONBoundsAndSyntax(t *testing.T) {
	t.Parallel()

	if _, err := ParseJSON([]byte(`{"usageMetadata":`), JSONOptions{}); !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("syntax error = %v", err)
	}
	if _, err := ParseJSON([]byte(`{}`), JSONOptions{MaxBytes: MaxJSONBytes + 1}); !errors.Is(err, ErrInvalidMaxBytes) {
		t.Fatalf("max error = %v", err)
	}
	if _, err := ParseJSON([]byte(`12345`), JSONOptions{MaxBytes: 4}); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("size error = %v", err)
	}
	if _, err := ParseJSON([]byte(`[]`), JSONOptions{}); !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("root error = %v", err)
	}
}

func TestSSEParserIncrementalUsageAndTerminal(t *testing.T) {
	t.Parallel()

	parser := NewSSEParser(SSEOptions{})
	chunks := []string{
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hel",
		"lo\"}]}}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1}}\r\n\r\n",
		"data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"interaction-1\",\"status\":\"completed\",\"service_tier\":\"standard\",\"usage\":{\"total_input_tokens\":7,\"total_output_tokens\":3,\"total_thought_tokens\":2}}}\n\n",
	}
	for _, chunk := range chunks {
		if err := parser.Push([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := parser.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Terminal || got.Failed || got.Status != "completed" || got.InteractionID != "interaction-1" {
		t.Fatalf("terminal result = %#v", got)
	}
	assertUsageEqual(t, got.Usage, Usage{
		ReportedServiceTier: "standard",
		InputTokens:         int64p(7),
		OutputTokens:        int64p(5),
		ThinkingTokens:      int64p(2),
	})
	if got.Events != 2 || got.MalformedEvents != 0 || got.Pending {
		t.Fatalf("event facts = %#v", got)
	}
}

func TestSSEParserTerminalSignalsAndFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stream    string
		terminal  bool
		failed    bool
		status    string
		errorCode string
	}{
		{name: "done sentinel", stream: "data: [DONE]\n\n", terminal: true, status: "completed"},
		{name: "event done", stream: "event: done\ndata: {}\n\n", terminal: true, status: "completed"},
		{name: "candidate finish", stream: "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n", terminal: true, status: "STOP"},
		{name: "interaction failed", stream: "data: {\"event_type\":\"interaction.failed\",\"interaction\":{\"status\":\"failed\",\"error\":{\"status\":\"UNAVAILABLE\",\"message\":\"retry\"}}}\n\n", terminal: true, failed: true, status: "failed", errorCode: "UNAVAILABLE"},
		{name: "truncated output is not terminal", stream: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n", terminal: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parser := NewSSEParser(SSEOptions{})
			if err := parser.Push([]byte(tt.stream)); err != nil {
				t.Fatal(err)
			}
			got, err := parser.Finish()
			if err != nil {
				t.Fatal(err)
			}
			if got.Terminal != tt.terminal || got.Failed != tt.failed || got.Status != tt.status || got.ErrorCode != tt.errorCode {
				t.Fatalf("result = %#v", got)
			}
		})
	}
}

func TestSSEParserHandlesMalformedAndEOFEvent(t *testing.T) {
	t.Parallel()

	parser := NewSSEParser(SSEOptions{})
	if err := parser.Push([]byte("data: {bad}\n\ndata: [DONE]")); err != nil {
		t.Fatal(err)
	}
	got, err := parser.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Terminal || got.MalformedEvents != 1 || got.Events != 2 {
		t.Fatalf("result = %#v", got)
	}
}

func TestSSEParserRejectsOversizedEvent(t *testing.T) {
	t.Parallel()

	parser := NewSSEParser(SSEOptions{MaxEventBytes: 8})
	if err := parser.Push([]byte("data: 123456789")); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("error = %v", err)
	}
	if _, err := parser.Finish(); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("finish error = %v", err)
	}
}

func TestSSEParserRejectsOversizedUnknownLine(t *testing.T) {
	t.Parallel()

	parser := NewSSEParser(SSEOptions{MaxEventBytes: 8})
	if err := parser.Push([]byte("ignored: 123456789\n")); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func assertUsageEqual(t *testing.T, got, want Usage) {
	t.Helper()
	if got.ReportedServiceTier != want.ReportedServiceTier || !equalInt64Pointer(got.InputTokens, want.InputTokens) ||
		!equalInt64Pointer(got.OutputTokens, want.OutputTokens) || !equalInt64Pointer(got.CacheReadTokens, want.CacheReadTokens) ||
		!equalInt64Pointer(got.ThinkingTokens, want.ThinkingTokens) {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
}

func equalInt64Pointer(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
