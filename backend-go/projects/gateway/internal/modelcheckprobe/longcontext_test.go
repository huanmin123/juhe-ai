package modelcheckprobe

import "testing"

func TestEvaluateLongContextNeedleAndModel(t *testing.T) {
	result := EvaluateLongContext([]LongContextObservation{{Key: "context_low", Marker: "NEEDLE-LOW", Result: Result{Success: true, ObservedModel: "gpt-5.6-sol", Output: "NEEDLE-LOW"}}}, "gpt-5.6-sol")
	if result.Kind != "long_context" || result.Status != "passed" || result.Score != 15 {
		t.Fatalf("result=%#v", result)
	}
}
