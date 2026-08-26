package modelcheckprobe

// EvaluateTrustedComparison mirrors the Node trusted-comparison item without
// retaining upstream response text. It compares the scored probe outcomes of
// two independently executed account suites; raw outputs remain outside the
// durable result boundary.
func EvaluateTrustedComparison(target, comparison []EvaluationItem, profile string) EvaluationItem {
	targetBasic := findBasicItem(target)
	comparisonBasic := findBasicItem(comparison)
	targetFailure := targetBasic == nil || !evidenceBool(targetBasic.Evidence, "success") || hasSkippedScoredItem(target)
	comparisonFailure := comparisonBasic == nil || !evidenceBool(comparisonBasic.Evidence, "success") || hasSkippedScoredItem(comparison)
	targetBehavior := findItem(target, "target.behavior_probe")
	comparisonBehavior := findItem(comparison, "trusted_comparison.behavior_probe")
	if profile == "full" {
		targetFailure = targetFailure || targetBehavior == nil || targetBehavior.Status == "skipped"
		comparisonFailure = comparisonFailure || comparisonBehavior == nil || comparisonBehavior.Status == "skipped"
	}
	if targetFailure || comparisonFailure {
		return EvaluationItem{ItemKey: "trusted_comparison.comparison", ItemType: "trusted_comparison", Status: "skipped", Evidence: map[string]any{
			"message":                  "可信对比核心探针请求失败，未形成可比模型证据",
			"requestFailure":           true,
			"excludedFromScoring":      true,
			"targetBasicSuccess":       targetBasic != nil && evidenceBool(targetBasic.Evidence, "success"),
			"comparisonBasicSuccess":   comparisonBasic != nil && evidenceBool(comparisonBasic.Evidence, "success"),
			"targetBehaviorStatus":     itemStatus(targetBehavior),
			"comparisonBehaviorStatus": itemStatus(comparisonBehavior),
		}}
	}
	targetQuality, targetMax := suiteScore(target)
	comparisonQuality, comparisonMax := suiteScore(comparison)
	targetMismatch := evidenceBool(targetBasic.Evidence, "modelMismatch")
	comparisonMismatch := evidenceBool(comparisonBasic.Evidence, "modelMismatch")
	targetOK := !targetMismatch && suiteComparable(targetQuality, targetMax, targetBehavior, profile)
	comparisonOK := !comparisonMismatch && suiteComparable(comparisonQuality, comparisonMax, comparisonBehavior, profile)
	comparable := targetOK && comparisonOK
	status, score := "failed", 0
	if !targetMismatch && !comparisonMismatch {
		switch {
		case comparable:
			status, score = "passed", 10
		case comparisonOK:
			status, score = "warning", 4
		}
	}
	message := "可信对比未形成完整可比结果"
	if comparisonMismatch {
		message = "可信对比账户基础探针返回模型不匹配，不能作为可信对比基准"
	} else if targetMismatch {
		message = "目标账户基础探针返回模型不匹配，可信对比未形成完整可比结果"
	} else if comparable {
		message = "目标链路和可信对比链路均完成核心探针"
	}
	return EvaluationItem{ItemKey: "trusted_comparison.comparison", ItemType: "trusted_comparison", Status: status, Score: score, MaxScore: 10, TraceID: comparisonBasic.TraceID, Evidence: map[string]any{
		"message":                      message,
		"targetQualityScore":           targetQuality,
		"targetQualityMax":             targetMax,
		"comparisonQualityScore":       comparisonQuality,
		"comparisonQualityMax":         comparisonMax,
		"targetBasicModelMismatch":     targetMismatch,
		"comparisonBasicModelMismatch": comparisonMismatch,
		"targetBehaviorPassed":         targetBehavior != nil && targetBehavior.Status == "passed",
		"comparisonBehaviorPassed":     comparisonBehavior != nil && comparisonBehavior.Status == "passed",
	}}
}

func findBasicItem(items []EvaluationItem) *EvaluationItem {
	for index := range items {
		if items[index].ItemType == "responses_basic" || items[index].ItemType == "protocol_basic" {
			return &items[index]
		}
	}
	return nil
}

func hasSkippedScoredItem(items []EvaluationItem) bool {
	for _, item := range items {
		if item.MaxScore > 0 && item.Status == "skipped" && item.ItemType != "cross_model" {
			return true
		}
	}
	return false
}

func suiteScore(items []EvaluationItem) (int, int) {
	score, maxScore := 0, 0
	for _, item := range items {
		if item.MaxScore <= 0 || item.ItemType == "cross_model" {
			continue
		}
		score += item.Score
		maxScore += item.MaxScore
	}
	return score, maxScore
}

func suiteComparable(score, maxScore int, behavior *EvaluationItem, profile string) bool {
	if maxScore <= 0 || score != maxScore {
		return false
	}
	return profile != "full" || behavior != nil && behavior.Status == "passed"
}

func itemStatus(item *EvaluationItem) string {
	if item == nil {
		return ""
	}
	return item.Status
}
