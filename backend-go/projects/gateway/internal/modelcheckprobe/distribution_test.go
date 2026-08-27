package modelcheckprobe

import "testing"

func TestEvaluateDistributionAndCrossModel(t *testing.T) {
	pair := DistributionPair{Definition: distributionDefinitions[1], Target: Result{Success: true, Output: `{"result":83,"tag":"SIGMA"}`}, Comparison: Result{Success: true, Output: `{"result":83,"tag":"SIGMA"}`}}
	item := EvaluateDistribution([]DistributionPair{pair})
	if item.Status != "passed" || item.Score != 15 {
		t.Fatalf("distribution=%#v", item)
	}
	cross := EvaluateCrossModel(Result{Success: true, ObservedModel: "gpt-5.6-sol"}, Result{Success: true, ObservedModel: "gpt-5.6-sol"}, "gpt-5.6-sol")
	if cross.Status != "passed" || cross.Score != 10 {
		t.Fatalf("cross=%#v", cross)
	}
}
