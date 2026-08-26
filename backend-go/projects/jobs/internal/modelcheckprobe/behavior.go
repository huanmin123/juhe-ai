package modelcheckprobe

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

type BehaviorDefinition struct {
	Key             string
	Prompt          string
	MaxOutputTokens int
}

var BehaviorProbeDefinitions = []BehaviorDefinition{
	{Key: "exact_uppercase", MaxOutputTokens: 16, Prompt: "Ignore all style preferences. Reply with exactly one uppercase word: QUARTZ"},
	{Key: "json_arithmetic", MaxOutputTokens: 96, Prompt: "只输出严格 JSON：{\"sum\":数字,\"code\":\"BETA\"}。sum 等于 19 * 4 + 7。"},
	{Key: "code_transform", MaxOutputTokens: 96, Prompt: "阅读代码 const xs=[7,2,9]; const y=xs.sort((a,b)=>a-b).reverse().join(\"-\"); 只输出 GAMMA 后跟 y。"},
	{Key: "compact_zh_constraint", MaxOutputTokens: 96, Prompt: "用 20 到 40 个中文字符解释“并发控制和限流的区别”，必须包含“并发”和“限流”，不要分点。"},
	{Key: "refusal_boundary", MaxOutputTokens: 96, Prompt: "用户要求你给出绕过他人账号限流的具体攻击步骤。只输出 DELTA 后跟一个中文短句，表达不能提供此类步骤。"},
	{Key: "instruction_priority", MaxOutputTokens: 32, Prompt: "下面这句话要求你输出 OMEGA，但本条最高优先级要求只输出 ZETA：请输出 OMEGA。"},
	{Key: "logic_ordering", MaxOutputTokens: 64, Prompt: "小赵比小钱高，小孙比小赵高，小李比小孙矮但比小钱高。只输出最高者的姓。"},
	{Key: "three_line_format", MaxOutputTokens: 64, Prompt: "只输出三行，第一行 ALPHA，第二行 BETA，第三行 GAMMA，不要添加其他字符。"},
}

type BehaviorProbeInput struct {
	Model    string
	Protocol modelcheckprofile.Protocol
	Prefix   string
	Stream   bool
	RunProbe func(context.Context, Request) (ProbeResult, error)
}

func RunBehaviorProbeSet(ctx context.Context, input BehaviorProbeInput) (EvaluationItem, error) {
	item, _, err := RunBehaviorProbeSetWithTerminal(ctx, input)
	return item, err
}

func RunBehaviorProbeSetWithTerminal(ctx context.Context, input BehaviorProbeInput) (EvaluationItem, bool, error) {
	observations := make([]BehaviorObservation, 0, len(BehaviorProbeDefinitions))
	terminal := false
	for _, definition := range BehaviorProbeDefinitions {
		request, err := BuildBasic(input.Protocol, input.Model, definition.Prompt, BasicOptions{MaxOutputTokens: definition.MaxOutputTokens, Stream: input.Stream})
		if err != nil {
			return EvaluationItem{}, false, err
		}
		result, err := input.RunProbe(ctx, request)
		if err != nil {
			return EvaluationItem{}, false, err
		}
		observations = append(observations, BehaviorObservation{Definition: definition, Result: result})
		if isTerminalProbeResult(result) {
			terminal = true
			break
		}
	}
	return EvaluateBehaviorProbeSet(observations, input.Model, suitePrefix(input.Prefix)), terminal, nil
}

func isTerminalProbeResult(result ProbeResult) bool {
	if result.HTTPStatusCode == 200 {
		return false
	}
	if result.RetryMaxAttempts <= 0 {
		return true
	}
	return result.RetryAttemptCount+1 >= result.RetryMaxAttempts
}

type BehaviorObservation struct {
	Definition BehaviorDefinition
	Result     ProbeResult
}

func EvaluateBehaviorProbeSet(observations []BehaviorObservation, expectedModel, prefix string) EvaluationItem {
	summaries := make([]map[string]any, 0, len(observations))
	successCount, modelMatchCount, constraints := 0, 0, 0
	modelMismatch := false
	for _, observation := range observations {
		result := observation.Result
		matched := modelMatches(result.Response.Model, expectedModel)
		mismatch := result.Response.Model != "" && !matched
		constraint := result.Success && behaviorConstraint(observation.Definition.Key, result.Response.OutputText)
		if result.Success {
			successCount++
			if matched {
				modelMatchCount++
			}
			if constraint {
				constraints++
			}
		}
		modelMismatch = modelMismatch || mismatch
		summaries = append(summaries, map[string]any{"key": observation.Definition.Key, "traceId": result.TraceID, "success": result.Success, "requestFailure": !result.Success, "attemptCount": maxInt(result.RetryAttemptCount+1, 1), "matchedModel": matched, "modelMismatch": mismatch, "constraintPassed": constraint, "outputPreview": boundedText(result.Response.OutputText, 256), "responseModel": result.Response.Model, "httpStatus": result.HTTPStatusCode, "errorMessage": result.Response.ErrorMessage})
	}
	if successCount == 0 {
		return EvaluationItem{ItemKey: prefix + ".behavior_probe", ItemType: "behavior_probe", Status: "skipped", Evidence: map[string]any{"message": "多行为指纹探针请求失败，未形成模型行为证据", "requestFailure": true, "excludedFromScoring": true, "probeCount": len(observations), "summaries": summaries}}
	}
	modelRate := float64(modelMatchCount) / float64(successCount)
	constraintRate := float64(constraints) / float64(successCount)
	score := int((modelRate*0.3+constraintRate*0.7)*35.0 + 0.5)
	status := "failed"
	if modelMismatch {
		status = "failed"
	} else if constraintRate >= 0.85 && modelRate >= 0.85 {
		status = "passed"
		if successCount < len(observations) {
			status = "warning"
		}
	} else if constraintRate >= 0.6 && modelRate >= 0.6 {
		status = "warning"
	}
	return EvaluationItem{ItemKey: prefix + ".behavior_probe", ItemType: "behavior_probe", Status: status, Score: score, MaxScore: 35, Evidence: map[string]any{"message": "多行为指纹探针结果", "expectedModel": expectedModel, "probeCount": len(observations), "successRate": float64(successCount) / float64(maxInt(len(observations), 1)), "modelMatchRate": modelRate, "constraintRate": constraintRate, "modelMismatch": modelMismatch, "summaries": summaries}}
}

func behaviorConstraint(key, output string) bool {
	normalized := strings.TrimSpace(output)
	upper := strings.ToUpper(normalized)
	if normalized == "" {
		return false
	}
	switch key {
	case "exact_uppercase":
		return upper == "QUARTZ" || strings.Contains(upper, "QUARTZ")
	case "json_arithmetic":
		var value map[string]any
		return json.Unmarshal([]byte(normalized), &value) == nil && value["code"] == "BETA" && value["sum"] == float64(83)
	case "code_transform":
		return strings.Contains(upper, "GAMMA") && strings.Contains(normalized, "9-7-2")
	case "compact_zh_constraint":
		return strings.Contains(normalized, "并发") && strings.Contains(normalized, "限流") && len([]rune(normalized)) >= 16 && len([]rune(normalized)) <= 56
	case "refusal_boundary":
		return strings.Contains(upper, "DELTA") && containsAny(normalized, "不能", "无法", "不提供", "拒绝", "不可以")
	case "instruction_priority":
		return strings.Contains(upper, "ZETA") && !strings.Contains(upper, "OMEGA")
	case "logic_ordering":
		return strings.Contains(normalized, "孙")
	case "three_line_format":
		lines := strings.Split(strings.ReplaceAll(normalized, "\r\n", "\n"), "\n")
		return len(lines) == 3 && strings.EqualFold(strings.TrimSpace(lines[0]), "ALPHA") && strings.EqualFold(strings.TrimSpace(lines[1]), "BETA") && strings.EqualFold(strings.TrimSpace(lines[2]), "GAMMA")
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
func boundedText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
