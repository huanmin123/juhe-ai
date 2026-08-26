package modelcheckprobe

import "testing"

func TestEvaluateTrustedComparisonQuickPassed(t *testing.T) {
	target := []EvaluationItem{{ItemKey: "target.responses_basic", ItemType: "responses_basic", Status: "passed", Score: 10, MaxScore: 10, TraceID: "target-trace", Evidence: map[string]any{"success": true, "modelMismatch": false}}}
	comparison := []EvaluationItem{{ItemKey: "trusted_comparison.responses_basic", ItemType: "responses_basic", Status: "passed", Score: 10, MaxScore: 10, TraceID: "comparison-trace", Evidence: map[string]any{"success": true, "modelMismatch": false}}}
	item := EvaluateTrustedComparison(target, comparison, "quick")
	if item.Status != "passed" || item.Score != 10 || item.MaxScore != 10 || item.TraceID != "comparison-trace" {
		t.Fatalf("item=%#v", item)
	}
}

func TestEvaluateTrustedComparisonSkipsRequestFailure(t *testing.T) {
	target := []EvaluationItem{{ItemKey: "target.responses_basic", ItemType: "responses_basic", Status: "failed", Evidence: map[string]any{"success": false}}}
	comparison := []EvaluationItem{{ItemKey: "trusted_comparison.responses_basic", ItemType: "responses_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}}}
	item := EvaluateTrustedComparison(target, comparison, "quick")
	if item.Status != "skipped" || item.MaxScore != 0 || item.Evidence["requestFailure"] != true {
		t.Fatalf("item=%#v", item)
	}
}
