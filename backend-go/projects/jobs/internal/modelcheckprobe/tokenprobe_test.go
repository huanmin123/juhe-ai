package modelcheckprobe

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestRunTokenIntegrityStopsOnlyTokenProbeAfterTerminalFailure(t *testing.T) {
	count := func(value string) int {
		tokens := 0
		for index := 0; index+1 < len(value); index++ {
			if value[index] == ' ' && value[index+1] == 'x' {
				tokens++
			}
		}
		return tokens
	}
	calls := 0
	run, err := RunTokenIntegrity(context.Background(), TokenProbeInput{
		Model:       "gpt-5.6-sol",
		Protocol:    modelcheckprofile.ProtocolOpenAIResponses,
		ProfileMode: "full",
		CountTokens: count,
		RunProbe: func(context.Context, Request) (ProbeResult, error) {
			calls++
			if calls == 3 {
				return ProbeResult{HTTPStatusCode: 503, RetryAttemptCount: 3, RetryMaxAttempts: 3}, nil
			}
			return ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{OutputText: "OK", Usage: map[string]any{"input_tokens": float64(calls)}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("RunTokenIntegrity: %v", err)
	}
	if calls != 3 || len(run.Samples) != 3 {
		t.Fatalf("terminal token failure must stop only token matrix calls=%d samples=%d", calls, len(run.Samples))
	}
	if run.Item.Status != "skipped" || run.Item.MaxScore != 0 {
		t.Fatalf("terminal token failure item=%+v", run.Item)
	}
}

func TestUsageIntReadsProtocolDetailCounters(t *testing.T) {
	value := usageInt(map[string]any{
		"input_tokens_details": map[string]any{"cached_tokens": float64(17)},
	}, "cached_tokens")
	if value == nil || *value != 17 {
		t.Fatalf("nested usage counter=%v", value)
	}
}
