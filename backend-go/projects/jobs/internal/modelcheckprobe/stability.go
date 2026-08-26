package modelcheckprobe

import (
	"context"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

type StabilityProbeInput struct {
	Model    string
	Protocol modelcheckprofile.Protocol
	Prefix   string
	Stream   bool
	RunProbe func(context.Context, Request) (ProbeResult, error)
}

// RunStabilityProbeSet mirrors Node's three sequential VECTOR requests. A
// terminal non-200 stops the remaining rounds while preserving the partial
// evidence for scoring.
func RunStabilityProbeSet(ctx context.Context, input StabilityProbeInput) (EvaluationItem, error) {
	if strings.TrimSpace(input.Model) == "" || input.RunProbe == nil {
		return EvaluationItem{}, fmt.Errorf("stability probe input is invalid")
	}
	results := make([]ProbeResult, 0, 3)
	for index := 0; index < 3; index++ {
		request, err := BuildBasic(input.Protocol, input.Model, "Reply with exactly one uppercase word: VECTOR", BasicOptions{MaxOutputTokens: 16, Stream: input.Stream})
		if err != nil {
			return EvaluationItem{}, err
		}
		result, err := input.RunProbe(ctx, request)
		if err != nil {
			return EvaluationItem{}, err
		}
		results = append(results, result)
		if isTerminalProbeResult(result) {
			break
		}
	}
	return EvaluateStabilityProbe(results, input.Model, suitePrefix(input.Prefix)), nil
}

func EvaluateStabilityProbe(results []ProbeResult, expectedModel, prefix string) EvaluationItem {
	observations := make([]map[string]any, 0, len(results))
	successCount, okCount, modelMatchCount := 0, 0, 0
	modelMismatch := false
	for _, result := range results {
		matched := modelMatches(result.Response.Model, expectedModel)
		mismatch := result.Response.Model != "" && !matched
		ok := result.Success && strings.TrimSpace(result.Response.OutputText) == "VECTOR"
		if result.Success {
			successCount++
			if ok {
				okCount++
			}
			if matched {
				modelMatchCount++
			}
		}
		modelMismatch = modelMismatch || mismatch
		observations = append(observations, map[string]any{
			"traceId": result.TraceID, "ok": ok, "success": result.Success,
			"requestFailure": !result.Success, "attemptCount": maxInt(result.RetryAttemptCount+1, 1),
			"httpStatus": result.HTTPStatusCode, "errorMessage": result.Response.ErrorMessage,
			"outputPreview": boundedText(result.Response.OutputText, 256), "responseModel": result.Response.Model,
			"matchedModel": matched, "modelMismatch": mismatch,
		})
	}
	if successCount == 0 {
		return EvaluationItem{ItemKey: prefix + ".stability", ItemType: "stability", Status: "skipped", Evidence: map[string]any{
			"message": "稳定性探针请求均失败，未形成稳定性证据", "expectedModel": expectedModel,
			"probeCount": len(results), "requestFailure": true, "excludedFromScoring": true,
			"observations": observations,
		}}
	}
	okRate := float64(okCount) / float64(successCount)
	modelRate := float64(modelMatchCount) / float64(successCount)
	score := 0
	if modelMismatch {
		score = int(okRate*4 + 0.5)
	} else {
		score = int((okRate*0.75+modelRate*0.25)*15 + 0.5)
	}
	if score > 15 {
		score = 15
	}
	status := "failed"
	if modelMismatch {
		status = "failed"
	} else if okRate == 1 && modelRate == 1 {
		status = "passed"
		if successCount < len(results) {
			status = "warning"
		}
	} else if score >= 8 {
		status = "warning"
	}
	return EvaluationItem{ItemKey: prefix + ".stability", ItemType: "stability", Status: status, Score: score, MaxScore: 15, DurationMS: results[len(results)-1].DurationMS, TraceID: results[len(results)-1].TraceID, Evidence: map[string]any{
		"message":       ternary(modelMismatch, "三轮稳定性探针返回模型与请求模型不一致", ternary(okRate == 1 && modelRate == 1 && successCount < len(results), "稳定性可用证据通过，部分轮次请求失败未计入评分", ternary(okCount == len(results), "三轮稳定性探针通过", "三轮稳定性探针未完全通过"))),
		"expectedModel": expectedModel, "probeCount": len(results), "passedCount": okCount,
		"matchedModel": modelMatchCount > 0, "modelMismatch": modelMismatch,
		"okRate": okRate, "modelMatchRate": modelRate, "successRate": float64(successCount) / float64(maxInt(len(results), 1)),
		"requestSuccessCount": successCount, "requestFailureCount": len(results) - successCount,
		"observations": observations,
	}}
}
