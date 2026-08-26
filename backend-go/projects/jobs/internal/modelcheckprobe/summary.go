package modelcheckprobe

import "strings"

// SummaryResult is the credential-free run-level quality summary derived from
// item evidence. It intentionally has no persistence or enforcement side
// effects; those belong to the future Go quality projector.
type SummaryResult struct {
	Level    string `json:"level"`
	Score    int    `json:"score"`
	MaxScore int    `json:"maxScore"`
	Message  string `json:"message"`
}

// SummarizeChecks mirrors the Node run-level decision ladder for the evidence
// currently available in modelcheckprobe. Scores are normalized to 0..100;
// excluded/skipped evidence never becomes a passing quality signal.
func SummarizeChecks(checks []EvaluationItem, trustedComparison bool, profile string) SummaryResult {
	maxScore, rawScore, failedCount := 0, 0, 0
	for _, item := range checks {
		if item.MaxScore <= 0 || !(strings.HasPrefix(item.ItemKey, "target.") || item.ItemKey == "trusted_comparison.comparison" || item.ItemKey == "trusted_comparison.distribution_similarity") {
			continue
		}
		maxScore += item.MaxScore
		rawScore += item.Score
		if item.Status == "failed" {
			failedCount++
		}
	}
	score := 0
	if maxScore > 0 {
		score = (rawScore*100 + maxScore/2) / maxScore
	}
	modelMismatch := false
	for _, item := range checks {
		if strings.HasPrefix(item.ItemKey, "target.") && evidenceBool(item.Evidence, "modelMismatch") {
			modelMismatch = true
			break
		}
	}
	if modelMismatch {
		return SummaryResult{Level: "suspicious", Score: score, MaxScore: 100, Message: "响应模型字段与请求模型不一致，目标链路疑似被替换或降级"}
	}
	for _, item := range checks {
		if item.ItemKey == "target.gpt56_juice" && item.ItemType == "gpt56_juice" && evidenceBool(item.Evidence, "hardAnomaly") {
			return SummaryResult{Level: "suspicious", Score: score, MaxScore: 100, Message: "GPT-5.6 Juice 专项探针发现疑似混用或响应替换，建议结合其他证据复核"}
		}
	}
	basic := findItem(checks, "target.responses_basic", "target.protocol_basic")
	if basic != nil && (basic.Status == "failed" || !evidenceBool(basic.Evidence, "success")) {
		return SummaryResult{Level: "unavailable", Score: score, MaxScore: 100, Message: "目标模型链路不可检测或上游不可用"}
	}
	longContext := findItem(checks, "target.long_context")
	if longContext != nil {
		switch longContext.Status {
		case "failed":
			return SummaryResult{Level: "suspicious", Score: score, MaxScore: 100, Message: "长上下文探针未通过，目标链路可能在长输入下被降级或上下文能力不足"}
		case "warning":
			if evidenceInt(longContext.Evidence, "requestFailureCount") > 0 {
				return SummaryResult{Level: "uncertain", Score: score, MaxScore: 100, Message: "长上下文部分窗口请求失败，未形成完整长上下文模型证据"}
			}
			return SummaryResult{Level: "uncertain", Score: score, MaxScore: 100, Message: "长上下文探针仅部分通过，建议重点排查中转是否按上下文长度切换模型"}
		case "skipped":
			return SummaryResult{Level: "uncertain", Score: score, MaxScore: 100, Message: "长上下文探针请求失败，未形成足够长上下文模型证据"}
		}
	}
	behavior := findItem(checks, "target.behavior_probe")
	stability := findItem(checks, "target.stability")
	if (behavior != nil && behavior.Status == "skipped") || (stability != nil && stability.Status == "skipped") {
		return SummaryResult{Level: "uncertain", Score: score, MaxScore: 100, Message: "部分关键探针请求失败，未形成足够模型可信度证据"}
	}
	if (behavior != nil && behavior.Status == "warning" && evidenceInt(behavior.Evidence, "requestFailureCount") > 0) || (stability != nil && stability.Status == "warning" && evidenceInt(stability.Evidence, "requestFailureCount") > 0) {
		return SummaryResult{Level: "uncertain", Score: score, MaxScore: 100, Message: "关键行为或稳定性探针存在请求失败，未形成完整模型可信度证据"}
	}
	trustedItem := findItem(checks, "trusted_comparison.comparison")
	trustedPassed := !trustedComparison || hasItemTypeStatus(checks, "trusted_comparison", "passed")
	if trustedComparison && trustedItem != nil && trustedItem.Status == "skipped" {
		return SummaryResult{Level: "uncertain", Score: score, MaxScore: 100, Message: "可信对比探针请求失败，未形成完整可比模型证据"}
	}
	if profile == "quick" {
		if score >= 78 && failedCount <= 1 {
			return SummaryResult{Level: "likely", Score: score, MaxScore: 100, Message: "快速检测未发现明显异常，仅形成初步估计；需要更高准确度请开启深度检测"}
		}
		if score >= 50 {
			return SummaryResult{Level: "uncertain", Score: score, MaxScore: 100, Message: "快速检测存在不确定项，建议开启深度检测复核"}
		}
		if score >= 25 {
			return SummaryResult{Level: "suspicious", Score: score, MaxScore: 100, Message: "快速检测发现明显异常，建议检查上游配置并使用深度检测复核"}
		}
		return SummaryResult{Level: "suspicious", Score: score, MaxScore: 100, Message: "快速检测多个 HTTP 200 质量校验未通过，目标链路疑似不符"}
	}
	crossModelPassed := hasStatus(checks, "target.cross_model", "passed")
	behaviorPassed := behavior != nil && behavior.Status == "passed"
	longPassed := longContext != nil && longContext.Status == "passed"
	stabilityPassed := stability != nil && stability.Status == "passed"
	if score >= 92 && failedCount == 0 && behaviorPassed && longPassed && stabilityPassed && trustedPassed && (trustedComparison || crossModelPassed) {
		message := "目标模型链路高可信，强诊断协议、行为指纹、长上下文、稳定性和辅助模型对照均通过"
		if trustedComparison {
			message = "目标模型链路高可信，强诊断协议、行为指纹、长上下文、稳定性和可信对比均通过"
		}
		return SummaryResult{Level: "high_confidence", Score: score, MaxScore: 100, Message: message}
	}
	if score >= 78 && failedCount <= 1 {
		return SummaryResult{Level: "likely", Score: score, MaxScore: 100, Message: "目标模型链路较可信，仍建议结合多次检测结果观察"}
	}
	if score >= 50 {
		return SummaryResult{Level: "uncertain", Score: score, MaxScore: 100, Message: "目标模型链路存在不确定项，建议复查上游账号和代理配置"}
	}
	if score >= 25 {
		return SummaryResult{Level: "suspicious", Score: score, MaxScore: 100, Message: "目标模型链路疑似不符，多个关键探针未通过"}
	}
	return SummaryResult{Level: "suspicious", Score: score, MaxScore: 100, Message: "多个 HTTP 200 质量校验未通过，目标模型链路疑似不符"}
}

func findItem(items []EvaluationItem, keys ...string) *EvaluationItem {
	for _, key := range keys {
		for i := range items {
			if items[i].ItemKey == key {
				return &items[i]
			}
		}
	}
	return nil
}

func hasStatus(items []EvaluationItem, key, status string) bool {
	for _, item := range items {
		if item.ItemKey == key && item.Status == status {
			return true
		}
	}
	return false
}

func hasItemTypeStatus(items []EvaluationItem, itemType, status string) bool {
	for _, item := range items {
		if item.ItemType == itemType && item.Status == status {
			return true
		}
	}
	return false
}

func evidenceBool(evidence map[string]any, key string) bool {
	value, _ := evidence[key].(bool)
	return value
}

func evidenceInt(evidence map[string]any, key string) int {
	switch value := evidence[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
