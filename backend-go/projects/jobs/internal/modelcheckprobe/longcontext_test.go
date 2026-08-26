package modelcheckprobe

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestLongContextDefinitionsAndPromptContainMarker(t *testing.T) {
	count := func(value string) int { return len([]rune(value)) }
	definitions := LongContextDefinitions(8000)
	if len(definitions) != 3 || definitions[0].Level != "low" || definitions[2].Level != "high" {
		t.Fatalf("unexpected definitions=%+v", definitions)
	}
	for _, definition := range definitions {
		prompt, err := BuildLongContextPrompt(definition, count)
		if err != nil {
			t.Fatalf("BuildLongContextPrompt(%s): %v", definition.Key, err)
		}
		if !strings.Contains(prompt, definition.Marker) || !strings.Contains(prompt, "前置") || !strings.Contains(prompt, "后置") {
			t.Fatalf("prompt missing marker/filler for %s", definition.Key)
		}
	}
}

func TestEvaluateLongContextProbeSet(t *testing.T) {
	definitions := LongContextDefinitions(8000)
	observations := make([]LongContextObservation, 0, len(definitions))
	for _, definition := range definitions {
		observations = append(observations, LongContextObservation{Definition: definition, Result: ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: definition.Marker, Usage: map[string]any{"input_tokens": float64(definition.TargetInputTokens)}}}})
	}
	item := EvaluateLongContextProbeSet(observations, "gpt-5.6-sol", "target")
	if item.Status != "passed" || item.Score != item.MaxScore || item.ItemKey != "target.long_context" {
		t.Fatalf("unexpected long context item=%+v", item)
	}
}

func TestRunLongContextStopsAfterTerminalFailure(t *testing.T) {
	calls := 0
	_, err := RunLongContextProbeSet(context.Background(), LongContextInput{Model: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses, ModelLimit: 8000, CountTokens: func(value string) int { return len([]rune(value)) }, RunProbe: func(context.Context, Request) (ProbeResult, error) {
		calls++
		if calls == 1 {
			return ProbeResult{HTTPStatusCode: 503, RetryAttemptCount: 2, RetryMaxAttempts: 3}, nil
		}
		return ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: "ok"}}, nil
	}})
	if err != nil {
		t.Fatalf("RunLongContextProbeSet: %v", err)
	}
	if calls != 1 {
		t.Fatalf("terminal long-context failure must stop remaining probes, calls=%d", calls)
	}
}
