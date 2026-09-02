package modelcheckprobe

import (
	"strings"
)

// EvaluateStability evaluates the three-round VECTOR contract without
// persisting provider response text. A partial round set remains warning or
// skipped evidence and cannot be treated as a formed quality fact.
func EvaluateStability(results []Result, expectedModel string) Evaluation {
	observations := make([]map[string]any, 0, len(results))
	successCount, okCount, modelMatchCount := 0, 0, 0
	modelMismatch := false
	for _, result := range results {
		_, matched, mismatch := matchProbeResponseModel(result, expectedModel)
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
		// An omitted response model is neutral in the Node oracle: it lowers
		// model-match evidence but is not proof of model substitution.
		modelMismatch = modelMismatch || (result.Success && mismatch)
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
	return Evaluation{Kind: "stability", Status: status, Score: score, MaxScore: 15, Evidence: map[string]any{"expectedModel": expectedModel, "probeCount": len(results), "requestSuccessCount": successCount, "passedCount": okCount, "okRate": okRate, "modelMatchRate": modelRate, "modelMismatch": modelMismatch, "requestFailureCount": len(results) - successCount, "scoringProbeCount": successCount, "partial": successCount < len(results), "observations": observations}}
}
