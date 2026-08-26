package modelcheckprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

const (
	IdentityFeatureVersion = "identity-features-v2-seven-categories"
	IdentityProbeVersion   = "generated-canary-v2-seven-categories"
	IdentityFeatureCount   = 8
)

var IdentityFeatureCategories = []string{"constraint", "code", "reasoning", "error_recovery", "multilingual", "tool_schema", "knowledge_window"}

type IdentityCanary struct {
	Key             string
	Category        string
	Tag             string
	Prompt          string
	MaxOutputTokens int
}

type IdentityObservation struct {
	Definition IdentityCanary
	Model      string
	Result     ProbeResult
	Passed     bool
	Features   [8]float64
}

type IdentityProbeInput struct {
	Model    string
	Protocol modelcheckprofile.Protocol
	RunProbe func(context.Context, Request) (ProbeResult, error)
}

func PairedIdentityModels(model string) []string {
	switch model {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}
	case "gpt-5.5", "gpt-5.4":
		return []string{"gpt-5.5", "gpt-5.4"}
	default:
		return []string{model}
	}
}

func RunIdentityObservation(ctx context.Context, input IdentityProbeInput) (EvaluationItem, []IdentityObservation, error) {
	if input.RunProbe == nil || strings.TrimSpace(input.Model) == "" {
		return EvaluationItem{}, nil, fmt.Errorf("identity observation input is invalid")
	}
	nonce := identityNonce()
	canaries := generatedIdentityCanaries(nonce)
	models := PairedIdentityModels(input.Model)
	observations := make([]IdentityObservation, 0, len(canaries)*len(models))
	for _, canary := range canaries {
		for _, model := range models {
			request, err := BuildBasic(input.Protocol, model, canary.Prompt, BasicOptions{MaxOutputTokens: canary.MaxOutputTokens})
			if err != nil {
				return EvaluationItem{}, nil, err
			}
			result, err := input.RunProbe(ctx, request)
			if err != nil {
				return EvaluationItem{}, nil, err
			}
			passed := result.Success && identityCanaryPassed(canary, result.Response.OutputText)
			features := identityFeatureVector(canary.Category, result.Response.OutputText, result.Response.Usage, passed)
			observations = append(observations, IdentityObservation{Definition: canary, Model: model, Result: result, Passed: passed, Features: features})
			if isTerminalProbeResult(result) {
				return evaluateIdentityObservations(observations, input.Model), observations, nil
			}
		}
	}
	return evaluateIdentityObservations(observations, input.Model), observations, nil
}

func evaluateIdentityObservations(observations []IdentityObservation, targetModel string) EvaluationItem {
	success, passed, targetSuccess, targetPassed := 0, 0, 0, 0
	summaries := make([]map[string]any, 0, len(observations))
	for _, observation := range observations {
		result := observation.Result
		if result.Success {
			success++
		}
		if observation.Passed {
			passed++
		}
		if observation.Model == targetModel {
			if result.Success {
				targetSuccess++
			}
			if observation.Passed {
				targetPassed++
			}
		}
		summaries = append(summaries, map[string]any{"key": observation.Definition.Key, "model": observation.Model, "traceId": result.TraceID, "success": result.Success, "constraintPassed": observation.Passed, "responseModel": result.Response.Model, "httpStatus": result.HTTPStatusCode, "featureVector": observation.Features, "outputPreview": boundedText(result.Response.OutputText, 256), "errorMessage": result.Response.ErrorMessage})
	}
	if success == 0 {
		return EvaluationItem{ItemKey: "target.identity_observation", ItemType: "identity_observation", Status: "skipped", Evidence: map[string]any{"message": "受控生成式身份探针请求失败，未形成完整质量证据", "requestFailure": true, "excludedFromScoring": true, "featureVersion": IdentityFeatureVersion, "probeVersion": IdentityProbeVersion, "summaries": summaries}}
	}
	rate := float64(passed) / float64(success)
	score := minInt(9, int(rate*10))
	status := "failed"
	if success == len(observations) && passed == len(observations) {
		status, score = "passed", 10
	} else if rate >= 0.6 {
		status = "warning"
	}
	return EvaluationItem{ItemKey: "target.identity_observation", ItemType: "identity_observation", Status: status, Score: score, MaxScore: 10, Evidence: map[string]any{"message": "受控生成式身份探针结果", "featureVersion": IdentityFeatureVersion, "probeVersion": IdentityProbeVersion, "modelCount": len(PairedIdentityModels(targetModel)), "observationCount": len(observations), "successCount": success, "constraintPassedCount": passed, "constraintRate": rate, "targetModel": targetModel, "targetSuccessCount": targetSuccess, "targetConstraintPassedCount": targetPassed, "targetConstraintRate": ratioInt(targetPassed, targetSuccess), "summaries": summaries}}
}

func generatedIdentityCanaries(nonce string) []IdentityCanary {
	tag := "CANARY-" + strings.ToUpper(nonce[:6])
	return []IdentityCanary{
		{Key: "constraint_json", Category: "constraint", Tag: tag, MaxOutputTokens: 80, Prompt: fmt.Sprintf("只输出严格 JSON：{\"result\":数字,\"tag\":\"%s\"}。result 等于 23 + 19。", tag)},
		{Key: "code_patch", Category: "code", Tag: tag, MaxOutputTokens: 80, Prompt: fmt.Sprintf("只输出一行 TypeScript 表达式，把 [2,7,9] 过滤为大于 2 的值并升序，行尾注释必须是 %s。", tag)},
		{Key: "reasoning_order", Category: "reasoning", Tag: tag, MaxOutputTokens: 64, Prompt: fmt.Sprintf("只输出严格 JSON：{\"largest\":数字,\"tag\":\"%s\"}。largest 是 2、11、21 中第二大值加 4。", tag)},
		{Key: "error_recovery", Category: "error_recovery", Tag: tag, MaxOutputTokens: 64, Prompt: fmt.Sprintf("中间结论错误地声称 23+19=43。请纠正，只输出严格 JSON：{\"correct\":数字,\"tag\":\"%s\"}。", tag)},
		{Key: "multilingual_consistency", Category: "multilingual", Tag: tag, MaxOutputTokens: 80, Prompt: fmt.Sprintf("只输出严格 JSON：{\"zh\":\"队列超时\",\"en\":\"queue timeout\",\"tag\":\"%s\"}。", tag)},
		{Key: "tool_schema", Category: "tool_schema", Tag: tag, MaxOutputTokens: 96, Prompt: fmt.Sprintf("按工具参数 schema 生成且只输出 JSON：必填 action 枚举只能是 inspect，payload 必须含 ids 数组 [2,7,9] 和布尔值 dryRun=true，tag=\"%s\"。", tag)},
		{Key: "knowledge_window", Category: "knowledge_window", Tag: tag, MaxOutputTokens: 64, Prompt: fmt.Sprintf("知识截止 2024-10。只输出严格 JSON：{\"version\":\"B\",\"tag\":\"%s\"}。", tag)},
	}
}

func identityCanaryPassed(canary IdentityCanary, output string) bool {
	text := strings.TrimSpace(output)
	var value map[string]any
	if canary.Key == "code_patch" {
		return strings.Contains(text, ".filter(") && strings.Contains(text, ".sort(") && strings.Contains(strings.ToUpper(text), strings.ToUpper(canary.Tag))
	}
	if json.Unmarshal([]byte(text), &value) != nil {
		return false
	}
	switch canary.Key {
	case "constraint_json":
		return value["result"] == float64(42) && value["tag"] == canary.Tag
	case "reasoning_order":
		return value["largest"] == float64(15) && value["tag"] == canary.Tag
	case "error_recovery":
		return value["correct"] == float64(42) && value["tag"] == canary.Tag
	case "multilingual_consistency":
		return value["zh"] == "队列超时" && value["en"] == "queue timeout" && value["tag"] == canary.Tag
	case "tool_schema":
		payload, _ := value["payload"].(map[string]any)
		ids, _ := payload["ids"].([]any)
		return value["action"] == "inspect" && value["tag"] == canary.Tag && payload["dryRun"] == true && len(ids) == 3 && ids[0] == float64(2) && ids[1] == float64(7) && ids[2] == float64(9)
	case "knowledge_window":
		return value["version"] == "B" && value["tag"] == canary.Tag
	default:
		return false
	}
}

func identityFeatureVector(category, output string, usage map[string]any, passed bool) [8]float64 {
	var vector [8]float64
	for index, value := range IdentityFeatureCategories {
		if value == category {
			vector[index] = boolFloat(passed)
		}
	}
	if outputTokens := usageInt(usage, "output_tokens", "completion_tokens"); outputTokens != nil {
		vector[7] = minFloat(1, float64(*outputTokens)/256)
	}
	if vector[7] == 0 {
		vector[7] = minFloat(1, float64(len([]rune(strings.TrimSpace(output))))/256)
	}
	return vector
}
func ratioInt(value, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(value) / float64(total)
}
func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func identityNonce() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "jobs-token"
	}
	return hex.EncodeToString(raw[:])
}
