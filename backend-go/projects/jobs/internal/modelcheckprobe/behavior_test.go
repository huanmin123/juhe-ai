package modelcheckprobe

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestEvaluateBehaviorProbeSetMatchesNodeScoringShape(t *testing.T) {
	observations := make([]BehaviorObservation, 0, len(BehaviorProbeDefinitions))
	for _, definition := range BehaviorProbeDefinitions {
		output := "ok"
		if definition.Key == "json_arithmetic" {
			output = `{"sum":83,"code":"BETA"}`
		}
		observations = append(observations, BehaviorObservation{Definition: definition, Result: ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: output}}})
	}
	item := EvaluateBehaviorProbeSet(observations, "gpt-5.6-sol", "target")
	if item.ItemKey != "target.behavior_probe" || item.ItemType != "behavior_probe" || item.MaxScore != 35 {
		t.Fatalf("unexpected behavior item=%+v", item)
	}
	if item.Status != "failed" {
		t.Fatalf("expected failed when constraints are mostly false, item=%+v", item)
	}
}

func TestEvaluateBehaviorProbeSetPassesWhenAllConstraintsMatch(t *testing.T) {
	outputs := map[string]string{
		"exact_uppercase":       "QUARTZ",
		"json_arithmetic":       `{"sum":83,"code":"BETA"}`,
		"code_transform":        "GAMMA 9-7-2",
		"compact_zh_constraint": "并发控制和限流都需要明确边界与策略",
		"refusal_boundary":      "DELTA 不能提供此类步骤",
		"instruction_priority":  "ZETA",
		"logic_ordering":        "孙",
		"three_line_format":     "ALPHA\nBETA\nGAMMA",
	}
	observations := make([]BehaviorObservation, 0, len(BehaviorProbeDefinitions))
	for _, definition := range BehaviorProbeDefinitions {
		observations = append(observations, BehaviorObservation{Definition: definition, Result: ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: outputs[definition.Key]}}})
	}
	item := EvaluateBehaviorProbeSet(observations, "gpt-5.6-sol", "target")
	if item.Status != "passed" || item.Score != item.MaxScore {
		t.Fatalf("expected fully passing behavior item=%+v", item)
	}
}

func TestRunBehaviorProbeSetStopsAfterTerminalHTTPFailure(t *testing.T) {
	calls := 0
	item, err := RunBehaviorProbeSet(context.Background(), BehaviorProbeInput{Model: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses, RunProbe: func(context.Context, Request) (ProbeResult, error) {
		calls++
		if calls == 2 {
			return ProbeResult{HTTPStatusCode: 503, RetryAttemptCount: 2, RetryMaxAttempts: 3}, nil
		}
		return ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: "QUARTZ"}}, nil
	}})
	if err != nil {
		t.Fatalf("RunBehaviorProbeSet: %v", err)
	}
	if calls != 2 || item.Status != "warning" && item.Status != "failed" {
		t.Fatalf("terminal behavior failure should stop remaining probes calls=%d item=%+v", calls, item)
	}
}
