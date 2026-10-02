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
	maxScore, rawScore, failed, quizDeduction := 0, 0, 0, 0
	for _, item := range checks {
		originalKind := item.Kind
		item.Kind = unscopedKind(item.Kind)
		if strings.HasPrefix(originalKind, "trusted_comparison.") && item.Kind != "comparison" && item.Kind != "distribution_similarity" {
			continue
		}
		if item.Kind == "custom_quiz" {
			// Custom quiz items never enter the shared denominator. A graded
			// failure deducts its full remaining weight from the normalized
			// score instead; terminal/request-failure items stay excluded.
			if item.Status == "failed" {
				failed++
				if item.MaxScore > 0 && !evidenceBool(item.Evidence, "excludedFromScoring") && !evidenceBool(item.Evidence, "requestFailure") {
					quizDeduction += item.MaxScore - item.Score
				}
			}
			continue
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
	score -= quizDeduction
	if score < 0 {
		score = 0
	}
	for _, item := range checks {
		item.Kind = unscopedKind(item.Kind)
		if item.Evidence != nil && evidenceBool(item.Evidence, "modelMismatch") {
			return SummaryResult{"suspicious", score, 100, "响应模型字段与请求模型不一致，目标链路疑似被替换或降级"}
		}
	}
	// §16/§17 质量短路：identity_extraction 命中篡改（context_tampering）或
	// 采样统计出现 mixing_detected / systematic_divergence 任一，整轮
	// suspicious（处罚路径既有）。token_distribution 的 JSD 判分已于
	// 2026-10-02 降级为 evidence_only，distribution_mismatch /
	// distribution_divergent 不再产生也不短路。可信对照账户自身的同名项不
	// 进入目标短路，其异常走 comparison 聚合路径。
	for index := range checks {
		if strings.HasPrefix(checks[index].Kind, "trusted_comparison.") {
			continue
		}
		switch unscopedKind(checks[index].Kind) {
		case "identity_extraction":
			if evidenceBool(checks[index].Evidence, "contextTampering") {
				return SummaryResult{"suspicious", score, 100, "复读指令中出现与请求模型不符的身份声明，检测到上下文篡改，目标链路疑似被替换"}
			}
		case "sampling_statistics":
			if hasSamplingShortCircuitReason(checks[index].Evidence) {
				return SummaryResult{"suspicious", score, 100, "采样统计发现输出分布、采样一致性或配对表现异常，目标链路疑似被替换、降级或混用"}
			}
		}
	}
	terminalCoreFailure := hasTerminalCoreProbeFailure(checks)
	checks = unscopedEvaluations(checks)
	basic := findEvaluation(checks, "protocol_basic")
	// Node marks any basic probe without a successful response as unavailable,
	// even when other core probes still answered; unavailable never authorizes
	// enforcement, and terminal failures carry no health-failure fact either
	// (the runtime suppresses the projection on terminal evidence).
	if basic != nil && (basic.Status == "failed" || !evidenceBool(basic.Evidence, "success")) {
		return SummaryResult{"unavailable", score, 100, "目标模型链路不可检测或上游不可用"}
	}
	// §1.2 contract: the third consecutive non-200 on ANY required core
	// protocol probe is terminal for the whole run, not only for basic. The
	// suite already truncated the remaining probes and kept the terminal
	// request-failure evidence, so the partial family must resolve to
	// unavailable instead of entering the ordinary score ladder.
	if terminalCoreFailure {
		return SummaryResult{"unavailable", score, 100, "目标模型链路不可检测或上游不可用"}
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
	// The Node oracle short-circuits only when the trusted-comparison aggregate
	// itself is skipped. A warning/failed aggregate remains score-bearing
	// evidence and must continue through the ordinary confidence ladder.
	if trustedComparison && hasStatusAny(checks, "skipped", "comparison") {
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
	// before granting the highest confidence level. The self cross-model check
	// retired with the universal suite (v5): without a trusted comparison the
	// highest achievable level for a full run is likely, because the highest
	// confidence requires comparison evidence from an independent account.
	crossModelSatisfied := trustedComparison
	trustedOK := !trustedComparison || (hasStatusAny(checks, "passed", "comparison") && hasStatusAny(checks, "passed", "distribution_similarity", "distribution"))
	behaviorPassed := behavior != nil && behavior.Status == "passed"
	stabilityPassed := stability != nil && stability.Status == "passed"
	longPassed := long != nil && long.Status == "passed"
	if score >= 92 && failed == 0 && trustedOK && crossModelSatisfied && behaviorPassed && stabilityPassed && longPassed {
		return SummaryResult{"high_confidence", score, 100, "目标模型链路高可信，强诊断协议、行为指纹、长上下文、稳定性和可信对照证据均通过"}
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

// hasTerminalCoreProbeFailure reports whether any of the target's required
// core protocol probes carries terminal request-failure evidence (the
// requestFailureEvaluation convention: requestFailure plus terminalFailure).
// It must inspect the raw, still-scoped items so a trusted-comparison
// account's own terminal failure stays on the comparison-aggregate uncertain
// path instead of marking the target unavailable. Custom-quiz items are not
// core protocol probes and cannot enter this gate; a quiz family terminal
// failure does carry terminalFailure and is handled by the runtime's
// any-family suppression (§5.7: the whole run fails without penalty).
func hasTerminalCoreProbeFailure(items []Evaluation) bool {
	for _, item := range items {
		if strings.HasPrefix(item.Kind, "trusted_comparison.") {
			continue
		}
		switch unscopedKind(item.Kind) {
		case "protocol_basic", "protocol_stream", "responses_stream", "structured_output", "tool_calling":
			if evidenceBool(item.Evidence, "requestFailure") && evidenceBool(item.Evidence, "terminalFailure") {
				return true
			}
		}
	}
	return false
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

func evidenceBool(e map[string]any, key string) bool { v, _ := e[key].(bool); return v }

// hasSamplingShortCircuitReason 检查 sampling_statistics 证据中的
// reasonCodes 是否包含任一整轮短路码（§17 评分契约；token_distribution
// 判分已降级，distribution 两码不再短路）。
func hasSamplingShortCircuitReason(evidence map[string]any) bool {
	if evidence == nil {
		return false
	}
	for _, key := range []string{"mixing_detected", "systematic_divergence"} {
		if samplingReasonCodesContain(evidence, key) {
			return true
		}
	}
	return false
}

func samplingReasonCodesContain(evidence map[string]any, reason string) bool {
	codes, ok := evidence["reasonCodes"].([]string)
	if ok {
		for _, code := range codes {
			if code == reason {
				return true
			}
		}
	}
	values, ok := evidence["reasonCodes"].([]any)
	if ok {
		for _, value := range values {
			if text, _ := value.(string); text == reason {
				return true
			}
		}
	}
	return false
}
