package modelcheckprobe

import "testing"

func TestSummarizeChecksReportsHighConfidenceOnlyWhenDiagnosticFamiliesFormed(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "cross_model", Status: "passed", Score: 10, MaxScore: 10},
	}
	if got := SummarizeChecks(checks, false, "full"); got.Level != "high_confidence" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksRequiresCrossModelForHighConfidenceWithoutTrustedComparison(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
	}
	if got := SummarizeChecks(checks, false, "full"); got.Level == "high_confidence" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksTrustedComparisonCanSatisfyHighConfidenceWithoutSelfCrossModel(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "comparison", Status: "passed", Score: 10, MaxScore: 10},
		{Kind: "distribution_similarity", Status: "passed", Score: 15, MaxScore: 15},
	}
	if got := SummarizeChecks(checks, true, "full"); got.Level != "high_confidence" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksFailsClosedOnJuiceAnomaly(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "juice", Status: "failed", Evidence: map[string]any{"hardAnomaly": true}},
	}
	if got := SummarizeChecks(checks, false, "quick"); got.Level != "suspicious" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksMarksRequestFailureUncertain(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "warning", Score: 20, MaxScore: 35, Evidence: map[string]any{"requestFailure": true}},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
	}
	if got := SummarizeChecks(checks, false, "full"); got.Level != "uncertain" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksKeepsPartialCoreEvidenceUncertain(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "skipped", Evidence: map[string]any{"success": false, "requestFailure": true}},
		{Kind: "structured_output", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
		{Kind: "tool_calling", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
	}
	if got := SummarizeChecks(checks, false, "quick"); got.Level != "uncertain" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksMarksTrustedComparisonFailureUncertain(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "comparison", Status: "skipped", Evidence: map[string]any{"requestFailure": true}},
	}
	if got := SummarizeChecks(checks, true, "full"); got.Level != "uncertain" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksLetsTrustedComparisonWarningUseScoreLadder(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "distribution_similarity", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "comparison", Status: "warning", Score: 8, MaxScore: 10, Evidence: map[string]any{"evidenceInsufficient": true}},
	}
	if got := SummarizeChecks(checks, true, "full"); got.Level != "likely" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksRequiresTrustedComparisonAggregateForHighConfidence(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "distribution_similarity", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "comparison", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true}},
	}
	if got := SummarizeChecks(checks, true, "full"); got.Level == "high_confidence" {
		t.Fatalf("summary=%+v", got)
	}
}
