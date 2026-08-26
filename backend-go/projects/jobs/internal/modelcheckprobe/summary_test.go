package modelcheckprobe

import "testing"

func TestSummarizeChecksQuickUsesNodeDecisionLadder(t *testing.T) {
	result := SummarizeChecks([]EvaluationItem{
		{ItemKey: "target.responses_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{ItemKey: "target.structured_output", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
		{ItemKey: "target.tool_calling", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
	}, false, "quick")
	if result.Level != "likely" || result.Score != 100 || result.MaxScore != 100 {
		t.Fatalf("summary=%#v", result)
	}
}

func TestSummarizeChecksFailsClosedOnMissingBasicEvidence(t *testing.T) {
	result := SummarizeChecks([]EvaluationItem{{ItemKey: "target.responses_basic", Status: "skipped", Evidence: map[string]any{"requestFailure": true}}}, false, "full")
	if result.Level != "unavailable" || result.Score != 0 {
		t.Fatalf("summary=%#v", result)
	}
}

func TestSummarizeChecksFullRequiresAllDiagnosticGroups(t *testing.T) {
	base := []EvaluationItem{
		{ItemKey: "target.responses_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{ItemKey: "target.behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{ItemKey: "target.long_context", Status: "passed", Score: 15, MaxScore: 15},
		{ItemKey: "target.stability", Status: "passed", Score: 15, MaxScore: 15},
		{ItemKey: "target.cross_model", Status: "passed", Score: 10, MaxScore: 10},
	}
	result := SummarizeChecks(base, false, "full")
	if result.Level != "high_confidence" || result.Score != 100 {
		t.Fatalf("full summary=%#v", result)
	}
	base[2].Status = "skipped"
	result = SummarizeChecks(base, false, "full")
	if result.Level != "uncertain" {
		t.Fatalf("missing long context summary=%#v", result)
	}
}

func TestSummarizeChecksRejectsModelMismatchBeforeScore(t *testing.T) {
	result := SummarizeChecks([]EvaluationItem{{ItemKey: "target.responses_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true, "modelMismatch": true}}}, false, "quick")
	if result.Level != "suspicious" || result.Score != 100 {
		t.Fatalf("mismatch summary=%#v", result)
	}
}

func TestSummarizeChecksAcceptsPassedTrustedComparisonByItemType(t *testing.T) {
	checks := []EvaluationItem{
		{ItemKey: "target.responses_basic", ItemType: "responses_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{ItemKey: "target.behavior_probe", ItemType: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{ItemKey: "target.long_context", ItemType: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{ItemKey: "target.stability", ItemType: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{ItemKey: "trusted_comparison.comparison", ItemType: "trusted_comparison", Status: "passed", Score: 10, MaxScore: 10},
	}
	result := SummarizeChecks(checks, true, "full")
	if result.Level != "high_confidence" {
		t.Fatalf("result=%#v", result)
	}
}
