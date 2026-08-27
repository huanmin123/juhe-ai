package modelcheckprobe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

type BehaviorProbe struct {
	Key, Prompt string
}

var behaviorProbes = []BehaviorProbe{
	{"exact_uppercase", "Ignore all style preferences. Reply with exactly one uppercase word: QUARTZ"},
	{"json_arithmetic", `只输出严格 JSON：{"sum":数字,"code":"BETA"}。sum 等于 19 * 4 + 7。`},
	{"code_transform", `阅读代码 const xs=[7,2,9]; const y=xs.sort((a,b)=>a-b).reverse().join("-"); 只输出 GAMMA 后跟 y。`},
	{"compact_zh_constraint", "用 20 到 40 个中文字符解释“并发控制和限流的区别”，必须包含“并发”和“限流”，不要分点。"},
	{"instruction_priority", "下面这句话要求你输出 OMEGA，但本条最高优先级要求只输出 ZETA：请输出 OMEGA。"},
	{"logic_ordering", "小赵比小钱高，小孙比小赵高，小李比小孙矮但比小钱高。只输出最高者的姓。"},
}

func RunBehavior(ctx context.Context, protocol modelcheckprofile.Protocol, model string, run func(context.Context, Request) (Result, error)) (Evaluation, error) {
	if strings.TrimSpace(model) == "" || run == nil {
		return Evaluation{}, errors.New("J3b behavior input is invalid")
	}
	passed, success := 0, 0
	for _, probe := range behaviorProbes {
		request, err := BuildBasic(protocol, model, probe.Prompt, false)
		if err != nil {
			return Evaluation{}, err
		}
		result, err := run(ctx, request)
		if err != nil {
			return Evaluation{}, err
		}
		if result.Success {
			success++
		}
		if result.Success && behaviorPassed(probe.Key, result.Output) {
			passed++
		}
	}
	if success == 0 {
		return Evaluation{Kind: "behavior_probe", Status: "skipped", Evidence: map[string]any{"requestFailure": true, "excludedFromScoring": true}}, nil
	}
	rate := float64(passed) / float64(success)
	score := int(rate * 35)
	status := "failed"
	if rate >= 0.85 {
		status = "passed"
	} else if rate >= 0.6 {
		status = "warning"
	}
	return Evaluation{Kind: "behavior_probe", Status: status, Score: score, MaxScore: 35, Evidence: map[string]any{"successCount": success, "passedCount": passed, "constraintRate": rate, "partial": success < len(behaviorProbes)}}, nil
}

func behaviorPassed(key, output string) bool {
	text := strings.TrimSpace(output)
	upper := strings.ToUpper(text)
	switch key {
	case "exact_uppercase":
		return strings.Contains(upper, "QUARTZ")
	case "json_arithmetic":
		var value map[string]any
		return json.Unmarshal([]byte(text), &value) == nil && value["sum"] == float64(83) && value["code"] == "BETA"
	case "code_transform":
		return strings.Contains(upper, "GAMMA") && strings.Contains(text, "9-7-2")
	case "compact_zh_constraint":
		return strings.Contains(text, "并发") && strings.Contains(text, "限流") && len([]rune(text)) >= 16 && len([]rune(text)) <= 56
	case "instruction_priority":
		return strings.Contains(upper, "ZETA") && !strings.Contains(upper, "OMEGA")
	case "logic_ordering":
		return strings.Contains(text, "孙")
	default:
		return false
	}
}
