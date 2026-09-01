package modelcheckprobe

import "testing"

func TestEvaluateDistributionAndCrossModel(t *testing.T) {
	pair := DistributionPair{Definition: distributionDefinitions[1], Target: Result{Success: true, Output: `{"result":83,"tag":"SIGMA"}`}, Comparison: Result{Success: true, Output: `{"result":83,"tag":"SIGMA"}`}}
	item := EvaluateDistribution([]DistributionPair{pair})
	if item.Status != "passed" || item.Score != 14 {
		t.Fatalf("distribution=%#v", item)
	}
	cross := EvaluateCrossModel(Result{Success: true, ObservedModel: "gpt-5.6-sol", Output: "OK-MODEL-CHECK"}, Result{Success: true, ObservedModel: "gpt-5.6-sol", Output: "CROSS-MODEL-OK"}, "gpt-5.6-sol")
	if cross.Status != "passed" || cross.Score != 10 {
		t.Fatalf("cross=%#v", cross)
	}
}

func TestEvaluateCrossModelMissingResponseModelsIsWarningEvidence(t *testing.T) {
	item := EvaluateCrossModelPair(
		Result{Success: true, Output: "OK-MODEL-CHECK"},
		Result{Success: true, Output: "CROSS-MODEL-OK"},
		"gpt-5.6-terra", "gpt-5.6-terra",
	)
	if item.Status != "warning" || item.Score != 2 || item.Evidence["modelMismatch"] != false {
		t.Fatalf("missing response models=%#v", item)
	}
}

func TestEvaluateCrossModelTerminalFailureIsMarkedForUnverifiedGate(t *testing.T) {
	item := EvaluateCrossModelPair(
		Result{Success: true, ObservedModel: "gpt-5.6-sol", Output: "OK-MODEL-CHECK"},
		Result{HTTPStatus: 503, ErrorMessage: "upstream unavailable", RetryAttemptCount: 2, RetryMaxAttempts: 3, AttemptStatusCodes: []int{503, 503, 503}},
		"gpt-5.6-sol", "gpt-5.6-terra",
	)
	if item.Status != "skipped" || item.Evidence["requestFailure"] != true || item.Evidence["terminalFailure"] != true {
		t.Fatalf("terminal cross-model evidence=%#v", item)
	}
}

func TestEvaluateDistributionUsesOnlySuccessfulPairsForBothRates(t *testing.T) {
	pairs := []DistributionPair{
		{Definition: distributionDefinitions[0], Target: Result{Success: true, Output: "向量数据库召回率与相关性说明"}, Comparison: Result{Success: false}},
		{Definition: distributionDefinitions[0], Target: Result{Success: true, Output: "向量数据库召回率与相关性说明"}, Comparison: Result{Success: true, Output: "向量数据库召回率与相关性说明"}},
	}
	item := EvaluateDistribution(pairs)
	if item.Status != "warning" || item.Score != 14 || item.Evidence["targetConstraintRate"] != 1.0 || item.Evidence["comparisonConstraintRate"] != 1.0 {
		t.Fatalf("mixed-success rates must use successful pairs only: %#v", item)
	}
	if item.Evidence["successfulPairCount"] != 1 || item.Evidence["requestFailureCount"] != 1 {
		t.Fatalf("mixed-success evidence=%#v", item.Evidence)
	}
}

func TestEvaluateDistributionMixedSuccessPairIsExcludedFailClosed(t *testing.T) {
	item := EvaluateDistribution([]DistributionPair{{
		Definition: distributionDefinitions[1],
		Target:     Result{Success: true, Output: `{"result":83,"tag":"SIGMA"}`},
		Comparison: Result{Success: false, ErrorMessage: "upstream failure"},
	}})
	if item.Status != "skipped" || item.Score != 0 || item.MaxScore != 0 {
		t.Fatalf("mixed-success pair must not score: %#v", item)
	}
}

func TestEvaluateDistributionNodeGoldenFiveSamples(t *testing.T) {
	pairs := make([]DistributionPair, 0, 5)
	for sample := 0; sample < 5; sample++ {
		pairs = append(pairs, DistributionPair{
			Definition: distributionDefinitions[1],
			Target: Result{Success: true, Output: `{"result":83,"tag":"SIGMA"}`, Usage: map[string]any{
				"input_tokens": 8, "output_tokens": 2,
			}},
			Comparison: Result{Success: true, Output: `{"result":83,"tag":"SIGMA"}`, Usage: map[string]any{
				"prompt_tokens": 5, "completion_tokens": 5,
			}},
		})
	}
	item := EvaluateDistribution(pairs)
	if item.Status != "passed" || item.Score != 15 || item.MaxScore != 15 {
		t.Fatalf("five-sample Node golden=%#v", item)
	}
	if item.Evidence["successfulPairCount"] != 5 || item.Evidence["averageSimilarity"] != 1.0 || item.Evidence["averageLengthRatio"] != 1.0 || item.Evidence["averageUsageRatio"] != 1.0 {
		t.Fatalf("five-sample weighted evidence=%#v", item.Evidence)
	}
}

func TestDistributionPairScoreMatchesNodeSimilarityLengthAndUsage(t *testing.T) {
	score := scoreDistributionPair(DistributionPair{
		Definition: distributionDefinitions[0],
		Target: Result{Success: true, Output: "向量数据库召回率与相关性说明", Usage: map[string]any{
			"input_tokens": 3, "output_tokens": 1,
		}},
		Comparison: Result{Success: true, Output: "向量数据库召回率与相关性说明优化", Usage: map[string]any{
			"totalTokenCount": 8,
		}},
	})
	if !score.successful || !score.targetConstraintPassed || !score.comparisonConstraintPassed {
		t.Fatalf("pair constraint score=%#v", score)
	}
	if score.similarity <= 0 || score.similarity >= 1 || score.lengthRatio <= 0 || score.lengthRatio >= 1 || score.usageRatio == nil || *score.usageRatio != 0.5 {
		t.Fatalf("pair weighted metrics=%#v", score)
	}
}
