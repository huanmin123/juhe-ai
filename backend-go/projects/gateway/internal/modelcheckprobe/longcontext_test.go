package modelcheckprobe

import "testing"

func TestEvaluateLongContextNeedleAndModel(t *testing.T) {
	result := EvaluateLongContext([]LongContextObservation{{Key: "context_low", Marker: "NEEDLE-LOW", Result: Result{Success: true, ObservedModel: "gpt-5.6-sol", Output: "NEEDLE-LOW"}}}, "gpt-5.6-sol")
	if result.Kind != "long_context" || result.Status != "passed" || result.Score != 15 {
		t.Fatalf("result=%#v", result)
	}
}

func TestEvaluateLongContextMissingModelIsNeutralButExplicitMismatchFails(t *testing.T) {
	missing := EvaluateLongContext([]LongContextObservation{{Key: "context_low", Marker: "NEEDLE-LOW", Result: Result{Success: true, Output: "NEEDLE-LOW"}}}, "gpt-5.6-sol")
	if missing.Status != "warning" || missing.Evidence["modelMismatch"] != false {
		t.Fatalf("missing response model=%#v", missing)
	}
	mismatch := EvaluateLongContext([]LongContextObservation{{Key: "context_low", Marker: "NEEDLE-LOW", Result: Result{Success: true, ObservedModel: "gpt-5.5", Output: "NEEDLE-LOW"}}}, "gpt-5.6-sol")
	if mismatch.Status != "failed" || mismatch.Evidence["modelMismatch"] != true {
		t.Fatalf("explicit response model mismatch=%#v", mismatch)
	}
}
