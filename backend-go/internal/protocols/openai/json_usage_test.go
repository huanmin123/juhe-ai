package openai

import (
	"errors"
	"testing"
)

func TestParseJSONUsageMatchesStreamUsageFields(t *testing.T) {
	usage, err := ParseJSONUsage([]byte(`{"usage":{"input_tokens":7,"output_tokens":3,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":1}}}`), 1024)
	if err != nil {
		t.Fatalf("ParseJSONUsage() error = %v", err)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 7 || usage.OutputTokens == nil || *usage.OutputTokens != 3 || usage.CacheReadTokens == nil || *usage.CacheReadTokens != 2 || usage.ThinkingTokens == nil || *usage.ThinkingTokens != 1 {
		t.Fatalf("usage = %#v", usage)
	}
	if _, err := ParseJSONUsage([]byte(`{"usage":{}} trailing`), 1024); !errors.Is(err, ErrJSONUsageInvalid) {
		t.Fatalf("trailing JSON error = %v", err)
	}
	if _, err := ParseJSONUsage([]byte(`{"usage":{}}`), 4); !errors.Is(err, ErrJSONUsageTooLarge) {
		t.Fatalf("body limit error = %v", err)
	}
}
