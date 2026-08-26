package modelcheckprobe

import (
	"context"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

type LongContextDefinition struct {
	Key               string
	Marker            string
	TargetInputTokens int
	MarkerTokenRatio  float64
	MaxOutputTokens   int
	Level             string
}

func LongContextDefinitions(modelLimit int) []LongContextDefinition {
	if modelLimit < 8000 {
		modelLimit = 64000
	}
	safeLimit := maxInt(4000, modelLimit-minInt(2048, modelLimit/50))
	high := minInt(safeLimit, maxInt(4000, modelLimit/4))
	medium := minInt(high-1, maxInt(2000, minInt(60000, high*3/5)))
	low := minInt(medium-1, maxInt(1000, minInt(8000, medium/2)))
	return []LongContextDefinition{
		longContextDefinition("low", low, 0.58, 40),
		longContextDefinition("medium", medium, 0.67, 48),
		longContextDefinition("high", high, 0.74, 48),
	}
}

func BuildLongContextPrompt(definition LongContextDefinition, countTokens func(string) int) (string, error) {
	if countTokens == nil || definition.TargetInputTokens <= 0 || definition.Marker == "" {
		return "", fmt.Errorf("long context prompt input is invalid")
	}
	markerTarget := maxInt(512, int(float64(definition.TargetInputTokens)*definition.MarkerTokenRatio))
	answerRule := fmt.Sprintf("回答规则：只输出 key 为 %s 的隐藏标记本身，不要解释，不要输出其他文字。", definition.Key)
	markerLine := fmt.Sprintf("关键记录 %s: 本次检测的隐藏标记是 %s，只在回答中输出这个标记。", definition.Key, definition.Marker)
	lines := []string{
		fmt.Sprintf("阅读以下上下文，找到 key 为 %s 的隐藏标记。本窗口目标输入长度为 %d tokens。", definition.Key, definition.TargetInputTokens),
		"每一段都是干扰上下文，只有关键记录行包含最终答案。",
	}
	for countTokens(strings.Join(append(lines, markerLine, answerRule), "\n")) < markerTarget-128 {
		lines = append(lines, longContextFiller(definition, len(lines)+1, "前置"))
	}
	lines = append(lines, markerLine)
	for countTokens(strings.Join(append(append([]string{}, lines...), answerRule), "\n")) < definition.TargetInputTokens-128 {
		lines = append(lines, longContextFiller(definition, len(lines)+1, "后置"))
	}
	lines = append(lines, answerRule)
	prompt := strings.Join(lines, "\n")
	if deficit := definition.TargetInputTokens - countTokens(prompt); deficit > 0 {
		prompt += strings.Repeat("测", deficit)
	}
	return prompt, nil
}

type LongContextObservation struct {
	Definition LongContextDefinition
	Result     ProbeResult
}

type LongContextInput struct {
	Model       string
	Protocol    modelcheckprofile.Protocol
	Stream      bool
	ModelLimit  int
	CountTokens func(string) int
	RunProbe    func(context.Context, Request) (ProbeResult, error)
}

func RunLongContextProbeSet(ctx context.Context, input LongContextInput) (EvaluationItem, error) {
	item, _, err := RunLongContextProbeSetWithTerminal(ctx, input)
	return item, err
}

func RunLongContextProbeSetWithTerminal(ctx context.Context, input LongContextInput) (EvaluationItem, bool, error) {
	if input.RunProbe == nil || input.CountTokens == nil || input.Model == "" {
		return EvaluationItem{}, false, fmt.Errorf("long context probe input is invalid")
	}
	observations := make([]LongContextObservation, 0, 3)
	terminal := false
	for _, definition := range LongContextDefinitions(input.ModelLimit) {
		prompt, err := BuildLongContextPrompt(definition, input.CountTokens)
		if err != nil {
			return EvaluationItem{}, false, err
		}
		request, err := BuildBasic(input.Protocol, input.Model, prompt, BasicOptions{MaxOutputTokens: definition.MaxOutputTokens, Stream: input.Stream})
		if err != nil {
			return EvaluationItem{}, false, err
		}
		result, err := input.RunProbe(ctx, request)
		if err != nil {
			return EvaluationItem{}, false, err
		}
		observations = append(observations, LongContextObservation{Definition: definition, Result: result})
		if isTerminalProbeResult(result) {
			terminal = true
			break
		}
	}
	return EvaluateLongContextProbeSet(observations, input.Model, "target"), terminal, nil
}

func EvaluateLongContextProbeSet(observations []LongContextObservation, expectedModel, prefix string) EvaluationItem {
	success, modelMatched, needleFound := 0, 0, 0
	summaries := make([]map[string]any, 0, len(observations))
	for _, observation := range observations {
		result := observation.Result
		matched := modelMatches(result.Response.Model, expectedModel)
		needle := result.Success && strings.Contains(strings.ToUpper(result.Response.OutputText), strings.ToUpper(observation.Definition.Marker))
		if result.Success {
			success++
			if matched {
				modelMatched++
			}
			if needle {
				needleFound++
			}
		}
		summaries = append(summaries, map[string]any{"key": observation.Definition.Key, "targetInputTokens": observation.Definition.TargetInputTokens, "traceId": result.TraceID, "success": result.Success, "requestFailure": !result.Success, "matchedModel": matched, "modelMismatch": result.Response.Model != "" && !matched, "foundNeedle": needle, "reportedInputTokens": usageInt(result.Response.Usage, "input_tokens", "prompt_tokens"), "outputPreview": boundedText(result.Response.OutputText, 256), "responseModel": result.Response.Model, "httpStatus": result.HTTPStatusCode, "errorMessage": result.Response.ErrorMessage})
	}
	if success == 0 {
		return EvaluationItem{ItemKey: prefix + ".long_context", ItemType: "long_context", Status: "skipped", Evidence: map[string]any{"message": "长上下文探针请求均失败，未形成长上下文模型证据", "requestFailure": true, "excludedFromScoring": true, "probeCount": len(observations), "summaries": summaries}}
	}
	modelRate := float64(modelMatched) / float64(success)
	needleRate := float64(needleFound) / float64(success)
	score := int((modelRate*0.3+needleRate*0.7)*15.0 + 0.5)
	status := "failed"
	if modelRate >= 0.85 && needleRate >= 0.85 {
		status = "passed"
	} else if modelRate >= 0.6 && needleRate >= 0.6 {
		status = "warning"
	}
	return EvaluationItem{ItemKey: prefix + ".long_context", ItemType: "long_context", Status: status, Score: score, MaxScore: 15, Evidence: map[string]any{"message": "长上下文探针结果", "expectedModel": expectedModel, "probeCount": len(observations), "successRate": float64(success) / float64(maxInt(len(observations), 1)), "modelMatchRate": modelRate, "needleRate": needleRate, "summaries": summaries}}
}

func longContextDefinition(level string, target int, ratio float64, maxOutput int) LongContextDefinition {
	return LongContextDefinition{Key: "context_" + level, Marker: "NEEDLE-" + strings.ToUpper(level) + "-" + fmt.Sprint(target), TargetInputTokens: target, MarkerTokenRatio: ratio, MaxOutputTokens: maxOutput, Level: level}
}
func longContextFiller(definition LongContextDefinition, index int, phase string) string {
	return fmt.Sprintf("段落 %s-%s-%05d: 这是一段用于模型检测的普通上下文，包含编号、中文文本、重复业务术语、干扰词和无关约束，但不包含最终答案。", definition.Key, phase, index)
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
