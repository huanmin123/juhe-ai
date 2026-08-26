package modelcheckprobe

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestRunStabilityProbeSetUsesThreeRoundsAndAggregatesEvidence(t *testing.T) {
	calls := 0
	item, err := RunStabilityProbeSet(context.Background(), StabilityProbeInput{
		Model: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		RunProbe: func(_ context.Context, request Request) (ProbeResult, error) {
			calls++
			return ProbeResult{HTTPStatusCode: 200, Success: true, TraceID: "trace-" + string(rune('0'+calls)), RequestModel: "gpt-5.6-sol", Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: "VECTOR"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || item.Status != "passed" || item.Score != 15 || item.MaxScore != 15 {
		t.Fatalf("calls=%d item=%+v", calls, item)
	}
}

func TestRunStabilityProbeSetStopsAfterTerminalFailure(t *testing.T) {
	calls := 0
	item, err := RunStabilityProbeSet(context.Background(), StabilityProbeInput{
		Model: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		RunProbe: func(context.Context, Request) (ProbeResult, error) {
			calls++
			return ProbeResult{HTTPStatusCode: 503, Success: false, RetryAttemptCount: 2, RetryMaxAttempts: 3, Response: ParsedResponse{ErrorMessage: "busy"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || item.Status != "skipped" {
		t.Fatalf("calls=%d item=%+v", calls, item)
	}
}
