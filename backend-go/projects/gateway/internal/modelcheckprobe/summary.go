package modelcheckprobe

import "strings"

// SummaryResult is the run-level quality decision. It keeps the Gateway
// decision ladder aligned with the Node oracle while remaining storage-neutral.
type SummaryResult struct {
	Level    string
	Score    int
	MaxScore int
	Message  string
}

func SummarizeChecks(checks []Evaluation, trustedComparison bool, profile string) SummaryResult {
	maxScore, rawScore, failed, juicePenalty := 0, 0, 0, 0
	for _, item := range checks {
		originalKind := item.Kind
		item.Kind = unscopedKind(item.Kind)
		if strings.HasPrefix(originalKind, "trusted_comparison.") && item.Kind != "comparison" && item.Kind != "distribution_similarity" {
			continue
		}
		if item.Evidence != nil {
			if value, ok := item.Evidence["scorePenalty"].(int); ok {
				juicePenalty += value
			}
			if value, ok := item.Evidence["scorePenalty"].(float64); ok {
				juicePenalty += int(value)
			}
		}
		if item.MaxScore <= 0 || item.Status == "skipped" {
			continue
		}
		maxScore += item.MaxScore
		rawScore += item.Score
		if item.Status == "failed" {
			failed++
		}
	}
	score := 0
	if maxScore > 0 {
		score = (rawScore*100 + maxScore/2) / maxScore
	}
	score -= juicePenalty
	if score < 0 {
		score = 0
	}
	for _, item := range checks {
		item.Kind = unscopedKind(item.Kind)
		if item.Evidence != nil && evidenceBool(item.Evidence, "modelMismatch") {
			return SummaryResult{"suspicious", score, 100, "响应模型字段与请求模型不一致，目标链路疑似被替换或降级"}
		}
		if item.Kind == "juice" && item.Evidence != nil && evidenceBool(item.Evidence, "hardAnomaly") {
			return SummaryResult{"suspicious", score, 100, "GPT-5.6 Juice 专项探针发现疑似混用或响应替换，建议结合其他证据复核"}
		}
	}
	checks = unscopedEvaluations(checks)
	basic := findEvaluation(checks, "protocol_basic")
	if basic != nil && (basic.Status == "failed" || !evidenceBool(basic.Evidence, "success")) && coreEvidenceUnavailable(checks) {
		return SummaryResult{"unavailable", score, 100, "目标模型链路不可检测或上游不可用"}
	}
	if basic != nil && (basic.Status == "failed" || !evidenceBool(basic.Evidence, "success")) {
		return SummaryResult{"uncertain", score, 100, "基础协议探针未形成完整证据，但其他核心能力仍有响应"}
	}
	long := findEvaluation(checks, "long_context")
	if long != nil && long.Status == "failed" {
		return SummaryResult{"suspicious", score, 100, "长上下文探针未通过，目标链路可能在长输入下被降级或上下文能力不足"}
	}
	if long != nil && (long.Status == "warning" || long.Status == "skipped") {
		return SummaryResult{"uncertain", score, 100, "长上下文探针未形成完整模型证据"}
	}
	behavior, stability := findEvaluation(checks, "behavior_probe"), findEvaluation(checks, "stability")
	if behavior != nil && behavior.Status == "skipped" {
		return SummaryResult{"uncertain", score, 100, "关键行为或稳定性探针未形成完整模型可信度证据"}
	}
	if stability != nil && stability.Status == "skipped" {
		return SummaryResult{"uncertain", score, 100, "关键行为或稳定性探针未形成完整模型可信度证据"}
	}
	if (behavior != nil && behavior.Status == "warning" && evidenceBool(behavior.Evidence, "requestFailure")) || (stability != nil && stability.Status == "warning" && evidenceBool(stability.Evidence, "requestFailure")) {
		return SummaryResult{"uncertain", score, 100, "关键行为或稳定性探针存在请求失败，未形成完整模型可信度证据"}
	}
	if trustedComparison && (hasRequestFailureAny(checks, "distribution_similarity", "distribution") || hasRequestFailureAny(checks, "comparison", "cross_model")) {
		return SummaryResult{"uncertain", score, 100, "可信对比探针请求失败，未形成完整可比模型证据"}
	}
	if trustedComparison && trustedComparisonEvidenceIssue(checks) {
		return SummaryResult{"uncertain", score, 100, "可信对比账户存在失败或不完整证据，未形成完整可比模型结论"}
	}
	if profile == "quick" {
		if score >= 78 && failed <= 1 {
			return SummaryResult{"likely", score, 100, "快速检测未发现明显异常，仅形成初步估计；需要更高准确度请开启深度检测"}
		}
		if score >= 50 {
			return SummaryResult{"uncertain", score, 100, "快速检测存在不确定项，建议开启深度检测复核"}
		}
		return SummaryResult{"suspicious", score, 100, "快速检测发现明显异常，建议检查上游配置并使用深度检测复核"}
	}
	// A similar output distribution is supporting evidence only. Node requires
	// the independently resolved trusted-comparison aggregate itself to pass
	// before granting the highest confidence level.
	trustedOK := !trustedComparison || (hasStatusAny(checks, "passed", "comparison", "cross_model") && hasStatusAny(checks, "passed", "distribution_similarity", "distribution"))
	behaviorPassed := behavior != nil && behavior.Status == "passed"
	stabilityPassed := stability != nil && stability.Status == "passed"
	longPassed := long != nil && long.Status == "passed"
	if score >= 92 && failed == 0 && trustedOK && behaviorPassed && stabilityPassed && longPassed {
		return SummaryResult{"high_confidence", score, 100, "目标模型链路高可信，强诊断协议、行为指纹、长上下文、稳定性和辅助模型对照均通过"}
	}
	if score >= 78 && failed <= 1 {
		return SummaryResult{"likely", score, 100, "目标模型链路较可信，仍建议结合多次检测结果观察"}
	}
	if score >= 50 {
		return SummaryResult{"uncertain", score, 100, "目标模型链路存在不确定项，建议复查上游账号和代理配置"}
	}
	return SummaryResult{"suspicious", score, 100, "多个质量校验未通过，目标模型链路疑似不符"}
}

func findEvaluation(items []Evaluation, kind string) *Evaluation {
	for i := range items {
		if strings.TrimSpace(items[i].Kind) == kind {
			return &items[i]
		}
	}
	return nil
}

func coreEvidenceUnavailable(items []Evaluation) bool {
	for _, kind := range []string{"protocol_basic", "structured_output", "tool_calling"} {
		item := findEvaluation(items, kind)
		if item != nil && evidenceBool(item.Evidence, "success") {
			return false
		}
	}
	return true
}

func unscopedKind(kind string) string {
	if index := strings.LastIndex(kind, "."); index >= 0 {
		return kind[index+1:]
	}
	return kind
}

// UnscopedKindForOwner exposes the stable family name to the owner package
// without coupling it to the summary implementation details.
func UnscopedKindForOwner(kind string) string { return unscopedKind(kind) }
func unscopedEvaluations(items []Evaluation) []Evaluation {
	result := make([]Evaluation, len(items))
	copy(result, items)
	for i := range result {
		result[i].Kind = unscopedKind(result[i].Kind)
	}
	return result
}
func hasStatus(items []Evaluation, kind, status string) bool {
	item := findEvaluation(items, kind)
	return item != nil && item.Status == status
}

func hasRequestFailure(items []Evaluation, kind string) bool {
	item := findEvaluation(items, kind)
	return item != nil && (evidenceBool(item.Evidence, "requestFailure") || evidenceBool(item.Evidence, "evidenceInsufficient"))
}

func hasStatusAny(items []Evaluation, status string, kinds ...string) bool {
	for _, kind := range kinds {
		if hasStatus(items, kind, status) {
			return true
		}
	}
	return false
}

func hasRequestFailureAny(items []Evaluation, kinds ...string) bool {
	for _, kind := range kinds {
		if hasRequestFailure(items, kind) {
			return true
		}
	}
	return false
}

func trustedComparisonEvidenceIssue(items []Evaluation) bool {
	for _, item := range items {
		kind := unscopedKind(item.Kind)
		trusted := strings.HasPrefix(strings.TrimSpace(item.Kind), "trusted_comparison.")
		if trusted {
			switch kind {
			case "juice":
				if item.Status == "skipped" && strings.Contains(fmtEvidenceReason(item.Evidence), "juice_scope_not_applicable") {
					continue
				}
			case "cross_model", "distribution":
				// The trusted full suite intentionally has no nested trusted
				// comparison. Its own cross-model/distribution placeholders are
				// excluded; the unscoped summaries are checked below.
				continue
			}
			if item.Status == "failed" || item.Status == "skipped" || item.Status == "warning" {
				return true
			}
			continue
		}
		if kind == "comparison_evidence" || kind == "comparison" || kind == "distribution_similarity" {
			if item.Status != "passed" {
				return true
			}
		}
	}
	return false
}

func fmtEvidenceReason(evidence map[string]any) string {
	if evidence == nil {
		return ""
	}
	if reason, ok := evidence["reason"].(string); ok {
		return reason
	}
	return ""
}

func evidenceBool(e map[string]any, key string) bool { v, _ := e[key].(bool); return v }
