package gatewayanthropic

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewayusage"
)

func TestParseJSONExtractsAnthropicUsageAndError(t *testing.T) {
	result, err := ParseJSON([]byte(`{
		"type":"message",
		"stop_reason":"end_turn",
		"usage":{
			"speed":"standard",
			"input_tokens":9007199254740993,
			"output_tokens":"42.9",
			"cache_read_input_tokens":12,
			"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":4},
			"output_tokens_details":{"thinking_tokens":5}
		}
	}`), JSONOptions{})
	if err != nil {
		t.Fatalf("ParseJSON() error = %v", err)
	}
	assertUsageFacts(t, result.Usage, gatewayusage.UsageFacts{
		ReportedServiceTier: "standard",
		InputTokens:         int64Pointer(9007199254740993),
		OutputTokens:        int64Pointer(42),
		CacheReadTokens:     int64Pointer(12),
		CacheWriteTokens:    int64Pointer(7),
		CacheWrite1hTokens:  int64Pointer(4),
		ThinkingTokens:      int64Pointer(5),
	})
	if !result.Terminal || result.Failed || result.Status != "end_turn" {
		t.Fatalf("result = %#v", result)
	}

	failed, err := ParseJSON([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`), JSONOptions{})
	if err != nil {
		t.Fatalf("ParseJSON(error) error = %v", err)
	}
	if !failed.Terminal || !failed.Failed || failed.ErrorCode != "overloaded_error" || failed.ErrorMessage != "busy" {
		t.Fatalf("failed result = %#v", failed)
	}
}

func TestParseJSONExplicitCacheTotalWinsAndPreservesZero(t *testing.T) {
	result, err := ParseJSON([]byte(`{"usage":{
		"input_tokens":0,
		"output_tokens":"0",
		"cache_creation_input_tokens":2,
		"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":40},
		"speed":" flex "
	}}`), JSONOptions{})
	if err != nil {
		t.Fatalf("ParseJSON() error = %v", err)
	}
	assertUsageFacts(t, result.Usage, gatewayusage.UsageFacts{
		InputTokens:        int64Pointer(0),
		OutputTokens:       int64Pointer(0),
		CacheWriteTokens:   int64Pointer(2),
		CacheWrite1hTokens: int64Pointer(40),
	})
}

func TestParseJSONRejectsInvalidAndBoundedPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "malformed", body: "{"},
		{name: "multiple", body: "{} {}"},
		{name: "array", body: "[]"},
		{name: "null", body: "null"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseJSON([]byte(test.body), JSONOptions{})
			if !errors.Is(err, ErrInvalidJSON) {
				t.Fatalf("error = %v, want ErrInvalidJSON", err)
			}
		})
	}

	_, err := ParseJSON([]byte(`{"padding":"`+strings.Repeat("x", 64)+`"}`), JSONOptions{MaxBytes: 32})
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("error = %v, want ErrPayloadTooLarge", err)
	}
	_, err = ParseJSON([]byte(`{}`), JSONOptions{MaxBytes: MaxJSONBytes + 1})
	if !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("error = %v, want ErrInvalidLimit", err)
	}
}

func TestSSEParserMergesMessageUsageAcrossArbitraryChunks(t *testing.T) {
	parser, err := NewSSEParser(SSEOptions{})
	if err != nil {
		t.Fatalf("NewSSEParser() error = %v", err)
	}
	stream := "event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":2,\"cache_creation_input_tokens\":3}}}\r\n\r\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, chunk := range chunkEvery([]byte(stream), 7) {
		if err := parser.Push(chunk); err != nil {
			t.Fatalf("Push() error = %v", err)
		}
	}
	result, err := parser.Finish()
	if err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	assertUsageFacts(t, result.Usage, gatewayusage.UsageFacts{
		InputTokens:      int64Pointer(10),
		OutputTokens:     int64Pointer(7),
		CacheReadTokens:  int64Pointer(2),
		CacheWriteTokens: int64Pointer(3),
	})
	if !result.Terminal || result.Failed || result.Status != "end_turn" || result.Events != 3 || result.MalformedEvents != 0 || result.Pending {
		t.Fatalf("result = %#v", result)
	}
}

func TestSSEParserFirstTerminalWins(t *testing.T) {
	parser, err := NewSSEParser(SSEOptions{})
	if err != nil {
		t.Fatalf("NewSSEParser() error = %v", err)
	}
	stream := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"slow down\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":999}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	if err := parser.Push([]byte(stream)); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	result, err := parser.Finish()
	if err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if !result.Terminal || !result.Failed || result.ErrorCode != "rate_limit_error" || result.ErrorMessage != "slow down" || result.Usage.OutputTokens != nil || result.Events != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSSEParserMalformedErrorIsTerminalFailure(t *testing.T) {
	parser, err := NewSSEParser(SSEOptions{})
	if err != nil {
		t.Fatalf("NewSSEParser() error = %v", err)
	}
	if err := parser.Push([]byte("event: error\ndata: {bad\n\n")); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	result, err := parser.Finish()
	if err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if !result.Terminal || !result.Failed || result.ErrorCode != "invalid_sse_error_payload" || result.MalformedEvents != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSSEParserEnforcesLineEventAndTotalBoundsWithStickyErrors(t *testing.T) {
	tests := []struct {
		name    string
		options SSEOptions
		input   string
		want    error
	}{
		{name: "line", options: SSEOptions{MaxLineBytes: 8, MaxEventBytes: 64, MaxTotalBytes: 128}, input: "data: 1234", want: ErrLineTooLarge},
		{name: "event", options: SSEOptions{MaxLineBytes: 64, MaxEventBytes: 12, MaxTotalBytes: 128}, input: "data: 12345678\n", want: ErrEventTooLarge},
		{name: "total", options: SSEOptions{MaxLineBytes: 64, MaxEventBytes: 64, MaxTotalBytes: 8}, input: "data: 123", want: ErrStreamTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser, err := NewSSEParser(test.options)
			if err != nil {
				t.Fatalf("NewSSEParser() error = %v", err)
			}
			err = parser.Push([]byte(test.input))
			if !errors.Is(err, test.want) {
				t.Fatalf("Push() error = %v, want %v", err, test.want)
			}
			if nextErr := parser.Push([]byte("x")); !errors.Is(nextErr, test.want) {
				t.Fatalf("sticky Push() error = %v, want %v", nextErr, test.want)
			}
			if _, finishErr := parser.Finish(); !errors.Is(finishErr, test.want) {
				t.Fatalf("Finish() error = %v, want %v", finishErr, test.want)
			}
		})
	}
}

func TestSSEParserRejectsInvalidLimitsAndWritesAfterFinish(t *testing.T) {
	if _, err := NewSSEParser(SSEOptions{MaxLineBytes: -1}); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("NewSSEParser() error = %v, want ErrInvalidLimit", err)
	}
	parser, err := NewSSEParser(SSEOptions{})
	if err != nil {
		t.Fatalf("NewSSEParser() error = %v", err)
	}
	if _, err := parser.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if err := parser.Push([]byte("data: {}\n\n")); !errors.Is(err, ErrParserFinished) {
		t.Fatalf("Push() error = %v, want ErrParserFinished", err)
	}
}

func assertUsageFacts(t *testing.T, got, want gatewayusage.UsageFacts) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
}

func int64Pointer(value int64) *int64 { return &value }

func chunkEvery(value []byte, size int) [][]byte {
	chunks := make([][]byte, 0, (len(value)+size-1)/size)
	for len(value) > 0 {
		length := min(size, len(value))
		chunks = append(chunks, value[:length])
		value = value[length:]
	}
	return chunks
}
