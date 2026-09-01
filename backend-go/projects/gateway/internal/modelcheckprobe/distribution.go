package modelcheckprobe

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf16"
)

type DistributionDefinition struct{ Key, Prompt string }
type DistributionPair struct {
	Definition         DistributionDefinition
	Target, Comparison Result
}

var distributionDefinitions = []DistributionDefinition{
	{"style_compact", "用 18 到 32 个中文字符解释“向量数据库的召回率”，必须包含“召回”和“相关”，不要分点。"},
	{"json_reasoning", `只输出严格 JSON：{"result":数字,"tag":"SIGMA"}。result 等于 37 + 46。`},
	{"code_judgement", `阅读代码 const xs=[2,5,8]; const y=xs.filter(x=>x>4).map(x=>x-1).join("-"); 只输出 ALPHA 后跟一个中文短句说明 y 的值。`},
	{"refusal_boundary", "用户要求你提供绕过他人账号限流的具体攻击步骤。只输出 DELTA 后跟一个中文短句，表达不能提供此类步骤。"},
	{"sequence_transform", "把 9、4、7 从小到大排序，只输出 THETA 后跟用竖线连接的结果。"},
	{"table_extract", "表格：北区=17，南区=23，西区=5。只输出 IOTA 后跟北区和南区两个数值。"},
}

func EvaluateDistribution(pairs []DistributionPair) Evaluation {
	if len(pairs) == 0 {
		return Evaluation{Kind: "distribution", Status: "skipped", Evidence: map[string]any{
			"requestFailure": true, "excludedFromScoring": true, "totalPairs": 0,
		}}
	}
	pairScores := make([]distributionPairScore, 0, len(pairs))
	for _, pair := range pairs {
		pairScores = append(pairScores, scoreDistributionPair(pair))
	}
	successful := make([]distributionPairScore, 0, len(pairScores))
	for _, score := range pairScores {
		if score.successful {
			successful = append(successful, score)
		}
	}
	if len(successful) == 0 {
		return Evaluation{Kind: "distribution", Status: "skipped", Evidence: map[string]any{
			"requestFailure": true, "excludedFromScoring": true,
			"pairCount": len(pairs), "totalPairs": len(pairs), "successfulPairCount": 0,
			"requestFailureCount": len(pairs), "scoringProbeCount": 0, "requestSuccessRate": 0.0,
		}}
	}
	targetRate := averageDistribution(successful, func(score distributionPairScore) float64 {
		if score.targetConstraintPassed {
			return 1
		}
		return 0
	})
	comparisonRate := averageDistribution(successful, func(score distributionPairScore) float64 {
		if score.comparisonConstraintPassed {
			return 1
		}
		return 0
	})
	averageSimilarity := averageDistribution(successful, func(score distributionPairScore) float64 { return score.similarity })
	averageLengthRatio := averageDistribution(successful, func(score distributionPairScore) float64 { return score.lengthRatio })
	usageValues := make([]float64, 0, len(successful))
	for _, score := range successful {
		if score.usageRatio != nil {
			usageValues = append(usageValues, *score.usageRatio)
		}
	}
	averageUsageRatio := 0.0
	if len(usageValues) > 0 {
		for _, value := range usageValues {
			averageUsageRatio += value
		}
		averageUsageRatio /= float64(len(usageValues))
	}
	similarityScore := 0.30*targetRate + 0.25*comparisonRate + 0.30*averageSimilarity + 0.10*averageLengthRatio + 0.05*averageUsageRatio
	score := int(math.Round(similarityScore * 15))
	if score < 0 {
		score = 0
	} else if score > 15 {
		score = 15
	}
	status := "failed"
	comparisonLooksHealthy := comparisonRate >= 0.7
	targetLooksDivergent := targetRate < 0.55 || averageSimilarity < 0.25 || averageLengthRatio < 0.35
	if comparisonLooksHealthy && targetLooksDivergent {
		status = "failed"
	} else if score >= 12 {
		if len(successful) < len(pairs) {
			status = "warning"
		} else {
			status = "passed"
		}
	} else if score >= 8 {
		status = "warning"
	}
	return Evaluation{Kind: "distribution", Status: status, Score: score, MaxScore: 15, Evidence: map[string]any{
		"pairCount": len(pairs), "totalPairs": len(pairs), "successfulPairCount": len(successful),
		"requestFailureCount": len(pairs) - len(successful), "scoringProbeCount": len(successful),
		"requestSuccessRate":       roundDistributionMetric(float64(len(successful)) / float64(len(pairs))),
		"pairCoverage":             roundDistributionMetric(float64(len(successful)) / float64(len(pairs))),
		"targetConstraintRate":     roundDistributionMetric(targetRate),
		"comparisonConstraintRate": roundDistributionMetric(comparisonRate),
		"averageSimilarity":        roundDistributionMetric(averageSimilarity),
		"averageLengthRatio":       roundDistributionMetric(averageLengthRatio),
		"averageUsageRatio":        roundDistributionMetric(averageUsageRatio),
		"partial":                  len(successful) < len(pairs),
	}}
}

type distributionPairScore struct {
	successful, targetConstraintPassed, comparisonConstraintPassed bool
	similarity, lengthRatio                                        float64
	usageRatio                                                     *float64
}

func scoreDistributionPair(pair DistributionPair) distributionPairScore {
	targetText, comparisonText := pair.Target.Output, pair.Comparison.Output
	score := distributionPairScore{
		successful:                 pair.Target.Success && pair.Comparison.Success,
		targetConstraintPassed:     pair.Target.Success && distributionPassed(pair.Definition.Key, targetText),
		comparisonConstraintPassed: pair.Comparison.Success && distributionPassed(pair.Definition.Key, comparisonText),
		lengthRatio:                boundedDistributionRatio(float64(utf16Length(targetText)), float64(utf16Length(comparisonText))),
	}
	if score.successful {
		score.similarity = distributionTextSimilarity(targetText, comparisonText)
	}
	targetTokens, targetOK := distributionTotalTokens(pair.Target.Usage)
	comparisonTokens, comparisonOK := distributionTotalTokens(pair.Comparison.Usage)
	if targetOK && comparisonOK {
		ratio := boundedDistributionRatio(targetTokens, comparisonTokens)
		score.usageRatio = &ratio
	}
	return score
}

func averageDistribution(scores []distributionPairScore, value func(distributionPairScore) float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	total := 0.0
	for _, score := range scores {
		total += value(score)
	}
	return total / float64(len(scores))
}

func roundDistributionMetric(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Round(value*1000) / 1000
}

func boundedDistributionRatio(left, right float64) float64 {
	if left <= 0 || right <= 0 {
		return 0
	}
	return math.Min(left, right) / math.Max(left, right)
}

func distributionTotalTokens(usage map[string]any) (float64, bool) {
	for _, key := range []string{"total_tokens", "totalTokens", "totalTokenCount"} {
		if value, ok := distributionNumber(usage[key]); ok {
			return value, true
		}
	}
	for _, keys := range [][2]string{
		{"input_tokens", "output_tokens"},
		{"prompt_tokens", "completion_tokens"},
		{"promptTokenCount", "candidatesTokenCount"},
	} {
		left, leftOK := distributionNumber(usage[keys[0]])
		right, rightOK := distributionNumber(usage[keys[1]])
		if leftOK || rightOK {
			if !leftOK {
				return right, true
			}
			if !rightOK {
				return left, true
			}
			return left + right, true
		}
	}
	return 0, false
}

func distributionNumber(value any) (float64, bool) {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case float32:
		number = float64(value)
	case int:
		number = float64(value)
	case int8:
		number = float64(value)
	case int16:
		number = float64(value)
	case int32:
		number = float64(value)
	case int64:
		number = float64(value)
	case uint:
		number = float64(value)
	case uint8:
		number = float64(value)
	case uint16:
		number = float64(value)
	case uint32:
		number = float64(value)
	case uint64:
		number = float64(value)
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}

func distributionTextSimilarity(left, right string) float64 {
	normalizedLeft, normalizedRight := normalizeDistributionText(left), normalizeDistributionText(right)
	if normalizedLeft == "" || normalizedRight == "" {
		return 0
	}
	if normalizedLeft == normalizedRight {
		return 1
	}
	leftTokens, rightTokens := distributionComparableTokens(normalizedLeft), distributionComparableTokens(normalizedRight)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return 0
	}
	intersection := 0
	for token := range leftTokens {
		if _, ok := rightTokens[token]; ok {
			intersection++
		}
	}
	union := len(leftTokens) + len(rightTokens) - intersection
	tokenSimilarity := 0.0
	if union > 0 {
		tokenSimilarity = float64(intersection) / float64(union)
	}
	lengthSimilarity := boundedDistributionRatio(float64(utf16Length(normalizedLeft)), float64(utf16Length(normalizedRight)))
	return tokenSimilarity*0.75 + lengthSimilarity*0.25
}

func normalizeDistributionText(value string) string {
	const punctuation = "，。！？；：,.!?;:\"'`~-—_[](){}<>"
	return strings.Map(func(char rune) rune {
		if unicode.IsSpace(char) || strings.ContainsRune(punctuation, char) {
			return -1
		}
		return unicode.ToLower(char)
	}, value)
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

type distributionToken struct {
	first, second uint16
	hasSecond     bool
}

func distributionComparableTokens(value string) map[distributionToken]struct{} {
	units := utf16.Encode([]rune(value))
	tokens := make(map[distributionToken]struct{})
	if len(units) <= 2 {
		if len(units) == 1 {
			tokens[distributionToken{first: units[0]}] = struct{}{}
		} else if len(units) == 2 {
			tokens[distributionToken{first: units[0], second: units[1], hasSecond: true}] = struct{}{}
		}
		return tokens
	}
	for index := 0; index < len(units)-1; index++ {
		tokens[distributionToken{first: units[index], second: units[index+1], hasSecond: true}] = struct{}{}
	}
	return tokens
}

func EvaluateCrossModel(target, comparison Result, expectedModel string) Evaluation {
	return EvaluateCrossModelPair(target, comparison, expectedModel, expectedModel)
}

func EvaluateCrossModelPair(target, comparison Result, targetExpectedModel, comparisonExpectedModel string) Evaluation {
	if !target.Success || !comparison.Success {
		evidence := map[string]any{"requestFailure": true, "excludedFromScoring": true}
		if isTerminalProbeFailure(target) || isTerminalProbeFailure(comparison) {
			evidence["terminalFailure"] = true
		}
		if IsModelUnavailable(target, targetExpectedModel) || IsModelUnavailable(comparison, comparisonExpectedModel) {
			evidence["modelUnavailable"] = true
			evidence["reason"] = "comparison_model_unavailable"
		}
		return Evaluation{Kind: "cross_model", Status: "skipped", Evidence: evidence}
	}
	targetResponseModel, targetMatched, targetMismatch := matchProbeResponseModel(target, targetExpectedModel)
	comparisonResponseModel, comparisonMatched, comparisonMismatch := matchProbeResponseModel(comparison, comparisonExpectedModel)
	sameResponseModel := targetResponseModel != "" && comparisonResponseModel != "" && targetResponseModel == comparisonResponseModel
	suspiciousSameBackend := sameResponseModel && targetExpectedModel != comparisonExpectedModel
	crossModelMismatch := comparisonMismatch || suspiciousSameBackend
	score := 0
	if !crossModelMismatch {
		if targetMatched {
			score += 4
		}
		if comparisonMatched {
			score += 4
		}
		if strings.TrimSpace(comparison.Output) == "CROSS-MODEL-OK" {
			score += 2
		}
	}
	status := "warning"
	if crossModelMismatch {
		status = "failed"
	} else if score >= 9 {
		status = "passed"
	}
	return Evaluation{Kind: "cross_model", Status: status, Score: score, MaxScore: 10, Evidence: map[string]any{
		"matchedModel":       targetMatched && comparisonMatched,
		"targetMatchedModel": targetMatched, "comparisonMatchedModel": comparisonMatched,
		"targetModelMismatch": targetMismatch, "comparisonModelMismatch": comparisonMismatch,
		"modelMismatch": targetMismatch, "sameResponseModel": sameResponseModel,
		"suspiciousSameBackend": suspiciousSameBackend, "crossModelMismatch": crossModelMismatch,
		"targetExpectedModel": targetExpectedModel, "comparisonExpectedModel": comparisonExpectedModel,
	}}
}

func distributionPassed(key, output string) bool {
	text := strings.TrimSpace(output)
	upper := strings.ToUpper(text)
	switch key {
	case "style_compact":
		return strings.Contains(text, "召回") && strings.Contains(text, "相关") && utf16Length(text) >= 12 && utf16Length(text) <= 48
	case "json_reasoning":
		value := parseJSONObject(text)
		return value != nil && value["tag"] == "SIGMA" && value["result"] == float64(83)
	case "code_judgement":
		return strings.Contains(upper, "ALPHA") && strings.Contains(text, "4-7")
	case "refusal_boundary":
		return strings.Contains(upper, "DELTA") && containsAny(text, "不能", "无法", "不提供", "拒绝", "不可以")
	case "sequence_transform":
		return strings.Contains(upper, "THETA") && strings.Contains(text, "4|7|9")
	case "table_extract":
		return strings.Contains(upper, "IOTA") && strings.Contains(text, "17") && strings.Contains(text, "23")
	default:
		return false
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
