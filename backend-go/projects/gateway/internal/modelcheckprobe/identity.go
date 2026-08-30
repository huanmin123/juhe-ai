package modelcheckprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

const (
	IdentityFeatureVersion = "identity-features-v2-seven-categories"
	IdentityProbeVersion   = "generated-canary-v2-seven-categories"
)

type identityCanary struct {
	Key, Category, Prompt string
}

// RunIdentity executes the stable seven-category canary set. Only bounded
// previews and pass/fail counters leave this function; provider output is
// never returned as durable evidence.
func RunIdentity(ctx context.Context, protocol modelcheckprofile.Protocol, model string, run func(context.Context, Request) (Result, error), endpointModes ...string) (Evaluation, error) {
	if strings.TrimSpace(model) == "" || run == nil {
		return Evaluation{}, fmt.Errorf("J3b identity input is invalid")
	}
	nonceBytes := [8]byte{}
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return Evaluation{}, fmt.Errorf("J3b identity nonce: %w", err)
	}
	tag := "CANARY-" + strings.ToUpper(hex.EncodeToString(nonceBytes[:])[:6])
	canaries := []identityCanary{
		{"constraint_json", "constraint", fmt.Sprintf(`只输出严格 JSON：{"result":数字,"tag":"%s"}。result 等于 23 + 19。`, tag)},
		{"code_patch", "code", fmt.Sprintf(`只输出一行 TypeScript 表达式，把 [2,7,9] 过滤为大于 2 的值并升序，行尾注释必须是 %s。`, tag)},
		{"reasoning_order", "reasoning", fmt.Sprintf(`只输出严格 JSON：{"largest":数字,"tag":"%s"}。largest 是 2、11、21 中第二大值加 4。`, tag)},
		{"error_recovery", "error_recovery", fmt.Sprintf(`中间结论错误地声称 23+19=43。请纠正，只输出严格 JSON：{"correct":数字,"tag":"%s"}。`, tag)},
		{"multilingual_consistency", "multilingual", fmt.Sprintf(`只输出严格 JSON：{"zh":"队列超时","en":"queue timeout","tag":"%s"}。`, tag)},
		{"tool_schema", "tool_schema", fmt.Sprintf(`按工具参数 schema 生成且只输出 JSON：必填 action 枚举只能是 inspect，payload 必须含 ids 数组 [2,7,9] 和布尔值 dryRun=true，tag="%s"。`, tag)},
		{"knowledge_window", "knowledge_window", fmt.Sprintf(`知识截止 2024-10。只输出严格 JSON：{"version":"B","tag":"%s"}。`, tag)},
	}
	models := []string{model}
	if strings.HasPrefix(model, "gpt-") {
		models = uniqueModels(model, "gpt-5.6-sol", "gpt-5.6-terra")
	}
	passed, success, total := 0, 0, 0
	for _, canary := range canaries {
		for _, candidate := range models {
			endpointMode := ""
			if len(endpointModes) > 0 {
				endpointMode = endpointModes[0]
			}
			request, err := buildBasicWithEndpointMode(protocol, candidate, canary.Prompt, modelcheckprofile.EndpointModeIsStreaming(endpointMode), endpointMode)
			if err != nil {
				return Evaluation{}, err
			}
			result, err := run(ctx, request)
			if err != nil {
				return Evaluation{}, err
			}
			total++
			if result.Success {
				success++
			}
			if result.Success && identityPassed(canary.Key, result.Output, tag) {
				passed++
			}
		}
	}
	if success == 0 {
		return Evaluation{Kind: "identity_observation", Status: "skipped", Evidence: map[string]any{"requestFailure": true, "excludedFromScoring": true, "featureVersion": IdentityFeatureVersion, "probeVersion": IdentityProbeVersion, "observationCount": total}}, nil
	}
	rate := float64(passed) / float64(success)
	status := "failed"
	if success == total && passed == total {
		status = "passed"
	} else if rate >= 0.6 {
		status = "warning"
	}
	score := int(rate * 10)
	return Evaluation{Kind: "identity_observation", Status: status, Score: score, MaxScore: 10, Evidence: map[string]any{"featureVersion": IdentityFeatureVersion, "probeVersion": IdentityProbeVersion, "observationCount": total, "successCount": success, "constraintPassedCount": passed, "constraintRate": rate, "partial": success < total}}, nil
}

func uniqueModels(models ...string) []string {
	seen := make(map[string]bool, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		if model = strings.TrimSpace(model); model != "" && !seen[model] {
			seen[model] = true
			result = append(result, model)
		}
	}
	return result
}

func identityPassed(key, output, tag string) bool {
	output = strings.TrimSpace(output)
	if key == "code_patch" {
		return strings.Contains(output, ".filter(") && strings.Contains(output, ".sort(") && strings.Contains(strings.ToUpper(output), strings.ToUpper(tag))
	}
	var value map[string]any
	if json.Unmarshal([]byte(output), &value) != nil {
		return false
	}
	switch key {
	case "constraint_json":
		return value["result"] == float64(42) && value["tag"] == tag
	case "error_recovery":
		return value["correct"] == float64(42) && value["tag"] == tag
	case "reasoning_order":
		return value["largest"] == float64(15) && value["tag"] == tag
	case "multilingual_consistency":
		return value["zh"] == "队列超时" && value["en"] == "queue timeout" && value["tag"] == tag
	case "tool_schema":
		payload, _ := value["payload"].(map[string]any)
		ids, _ := payload["ids"].([]any)
		return value["action"] == "inspect" && value["tag"] == tag && payload["dryRun"] == true && len(ids) == 3
	case "knowledge_window":
		return value["version"] == "B" && value["tag"] == tag
	default:
		return false
	}
}
