package modelcheckprobe

import "testing"

func TestJuiceScopeAndStrongAnomaly(t *testing.T) {
	if !ShouldRunJuice("gpt-5.6-sol", "full", "openai_responses") || ShouldRunJuice("gpt-5.6-sol", "quick", "openai_responses") {
		t.Fatal("juice scope mismatch")
	}
	results := []Result{{Success: true, Output: "32"}, {Success: true, Output: "32"}, {Success: true, Output: "32"}, {Success: true, Output: "32"}, {Success: true, Output: "48"}, {Success: true, Output: "77777"}}
	item := EvaluateJuice("gpt-5.6-sol", results, "77777")
	if item.Status != "failed" || item.Evidence["scorePenalty"] != JuiceStrongPenalty || item.Evidence["strongAnomaly"] != true {
		t.Fatalf("juice=%#v", item)
	}
}
