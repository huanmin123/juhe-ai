package modelcheckprobe

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// EvaluateStability evaluates the three-round VECTOR contract without
// persisting provider response text. A partial round set remains warning or
// skipped evidence and cannot be treated as a formed quality fact.
func EvaluateStability(results []Result, expectedModel string) Evaluation {
	observations := make([]map[string]any, 0, len(results))
	successCount, okCount, modelMatchCount := 0, 0, 0
	modelMismatch := false
	for _, result := range results {
		matched := result.ObservedModel == "" || modelMatches(result.ObservedModel, expectedModel)
		ok := result.Success && strings.TrimSpace(result.Output) == "VECTOR"
		if result.Success {
			successCount++
			if ok {
				okCount++
			}
			if matched {
				modelMatchCount++
			}
		}
		modelMismatch = modelMismatch || !matched
		observations = append(observations, map[string]any{"success": result.Success, "ok": ok, "httpStatus": result.HTTPStatus, "responseModel": result.ObservedModel, "matchedModel": matched, "error": result.ErrorMessage})
	}
	if successCount == 0 {
		return Evaluation{Kind: "stability", Status: "skipped", Evidence: map[string]any{"requestFailure": true, "excludedFromScoring": true, "probeCount": len(results), "observations": observations}}
	}
	okRate := float64(okCount) / float64(successCount)
	modelRate := float64(modelMatchCount) / float64(successCount)
	score := int((okRate*0.75 + modelRate*0.25) * 15)
	if modelMismatch {
		score = int(okRate * 4)
	}
	status := "failed"
	if !modelMismatch && okRate == 1 && modelRate == 1 {
		status = "passed"
		if successCount < len(results) {
			status = "warning"
		}
	} else if !modelMismatch && score >= 8 {
		status = "warning"
	}
	return Evaluation{Kind: "stability", Status: status, Score: score, MaxScore: 15, Evidence: map[string]any{"expectedModel": expectedModel, "probeCount": len(results), "requestSuccessCount": successCount, "passedCount": okCount, "okRate": okRate, "modelMatchRate": modelRate, "modelMismatch": modelMismatch, "partial": successCount < len(results), "observations": observations}}
}

// StabilityRequest returns the frozen protocol-specific VECTOR request.
func StabilityRequest(protocol modelcheckprofile.Protocol, model string, stream bool) (Request, error) {
	return BuildBasic(protocol, model, "Reply with exactly one uppercase word: VECTOR", stream)
}
