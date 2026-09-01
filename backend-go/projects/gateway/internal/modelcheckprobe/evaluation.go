package modelcheckprobe

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// Evaluation is credential-free evidence suitable for durable projection.
type Evaluation struct {
	Kind     string
	Status   string
	Score    int
	MaxScore int
	Evidence map[string]any
}

func EvaluateBasic(result Result, expectedModel string) Evaluation {
	if !result.Success {
		return requestFailureEvaluation("protocol_basic", result, expectedModel, 10)
	}
	matched := strings.TrimSpace(result.ObservedModel) != "" && modelMatches(result.ObservedModel, expectedModel)
	outputMatches := strings.TrimSpace(result.Output) == "OK-MODEL-CHECK"
	score := 0
	if matched {
		score += 3
	}
	if outputMatches {
		score += 7
	}
	status := "failed"
	if result.Success && matched && outputMatches {
		status = "passed"
	} else if result.Success && matched {
		status = "warning"
	}
	return withRetryEvidence(Evaluation{Kind: "protocol_basic", Status: status, Score: score, MaxScore: 10, Evidence: map[string]any{"success": result.Success, "expectedModel": expectedModel, "responseModel": result.ObservedModel, "outputMatches": outputMatches, "modelMismatch": !matched}}, result)
}

// EvaluateProtocolStream evaluates the independent STREAM-OK probe that is
// issued when the resolved account health-check mode is streaming. It is kept
// separate from the basic probe: a successful basic response does not prove
// that the provider can emit the stream framing required by the target.
func EvaluateProtocolStream(result Result, expectedModel string, protocol modelcheckprofile.Protocol) Evaluation {
	kind := "protocol_stream"
	if protocol == modelcheckprofile.ProtocolOpenAIResponses {
		kind = "responses_stream"
	}
	if !result.Success {
		return requestFailureEvaluation(kind, result, expectedModel, 15)
	}
	responseModel := strings.TrimSpace(result.ObservedModel)
	matchedModel := responseModel != "" && modelMatches(responseModel, expectedModel)
	modelMismatch := responseModel == "" || !matchedModel
	outputMatches := strings.TrimSpace(result.Output) == "STREAM-OK"
	score := 0
	if modelMismatch {
		score = 4
		if outputMatches {
			score++
		}
	} else {
		score = 8
		if matchedModel {
			score += 3
		}
		if outputMatches {
			score += 4
		}
	}
	status := "failed"
	if !modelMismatch && score >= 13 {
		status = "passed"
	} else if !modelMismatch && score >= 8 {
		status = "warning"
	}
	return withRetryEvidence(Evaluation{Kind: kind, Status: status, Score: score, MaxScore: 15, Evidence: map[string]any{
		"success": result.Success, "expectedModel": expectedModel, "responseModel": result.ObservedModel,
		"expectedOutput": "STREAM-OK", "outputMatches": outputMatches, "modelMismatch": modelMismatch,
	}}, result)
}

// EvaluateStream is retained as the Responses-specific convenience used by
// callers that do not need to select a protocol item type.
func EvaluateStream(result Result, expectedModel string) Evaluation {
	return EvaluateProtocolStream(result, expectedModel, modelcheckprofile.ProtocolOpenAIResponses)
}

func EvaluateStructured(result Result, expectedModel string) Evaluation {
	if !result.Success {
		return requestFailureEvaluation("structured_output", result, expectedModel, 15)
	}
	value := safeStructured(parseJSONObject(result.Output))
	valid := value["status"] == "ok" && value["value"] == float64(7)
	matched := strings.TrimSpace(result.ObservedModel) != "" && modelMatches(result.ObservedModel, expectedModel)
	score := 0
	if result.Success {
		score = 8
	}
	if matched {
		score += 3
	}
	if valid {
		score += 4
	}
	status := "failed"
	if matched && score >= 13 {
		status = "passed"
	} else if matched && score >= 8 {
		status = "warning"
	}
	return withRetryEvidence(Evaluation{Kind: "structured_output", Status: status, Score: score, MaxScore: 15, Evidence: map[string]any{"success": result.Success, "expectedModel": expectedModel, "responseModel": result.ObservedModel, "outputJson": value, "valid": valid, "modelMismatch": !matched}}, result)
}

func EvaluateTool(result Result, expectedModel string) Evaluation {
	if !result.Success {
		return requestFailureEvaluation("tool_calling", result, expectedModel, 15)
	}
	called := hasFunctionCall(result.JSON, "record_model_check")
	matched := strings.TrimSpace(result.ObservedModel) != "" && modelMatches(result.ObservedModel, expectedModel)
	score := 0
	if result.Success {
		score = 8
	}
	if matched {
		score += 3
	}
	if called {
		score += 4
	}
	status := "failed"
	if matched && score >= 13 {
		status = "passed"
	} else if matched && score >= 8 {
		status = "warning"
	}
	return withRetryEvidence(Evaluation{Kind: "tool_calling", Status: status, Score: score, MaxScore: 15, Evidence: map[string]any{"success": result.Success, "expectedModel": expectedModel, "responseModel": result.ObservedModel, "called": called, "modelMismatch": !matched}}, result)
}

func EvaluateUsage(results []Result) Evaluation {
	for _, result := range results {
		if result.Success && len(result.Usage) > 0 {
			return Evaluation{Kind: "usage_shape", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true, "usage": safeUsage(result.Usage)}}
		}
	}
	return Evaluation{Kind: "usage_shape", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true}}
}

func requestFailureEvaluation(kind string, result Result, expectedModel string, maxScore int) Evaluation {
	evidence := map[string]any{
		"success":              false,
		"requestFailure":       true,
		"excludedFromScoring":  true,
		"evidenceInsufficient": true,
		"expectedModel":        expectedModel,
		"responseModel":        result.ObservedModel,
		"httpStatus":           result.HTTPStatus,
	}
	if result.ErrorMessage != "" {
		evidence["error"] = result.ErrorMessage
	}
	if IsModelUnavailable(result, expectedModel) {
		evidence["modelUnavailable"] = true
		evidence["reason"] = "model_unavailable"
	}
	if result.RetryAttemptCount > 0 {
		evidence["retryAttemptCount"] = result.RetryAttemptCount
		evidence["retryMaxAttempts"] = result.RetryMaxAttempts
		evidence["attemptStatusCodes"] = append([]int(nil), result.AttemptStatusCodes...)
		evidence["retryWaitMilliseconds"] = retryWaitMilliseconds(result.RetryWaitDurations)
		evidence["attempts"] = safeAttemptDetails(result.AttemptDetails)
	}
	if result.HTTPStatus == http.StatusOK && !IsModelUnavailable(result, expectedModel) {
		delete(evidence, "requestFailure")
		delete(evidence, "excludedFromScoring")
		delete(evidence, "evidenceInsufficient")
		return Evaluation{Kind: kind, Status: "failed", MaxScore: maxScore, Evidence: evidence}
	}
	return Evaluation{Kind: kind, Status: "skipped", Evidence: evidence}
}

func withRetryEvidence(item Evaluation, result Result) Evaluation {
	if result.RetryAttemptCount > 0 {
		item.Evidence["retryAttemptCount"] = result.RetryAttemptCount
		item.Evidence["retryMaxAttempts"] = result.RetryMaxAttempts
		item.Evidence["attemptStatusCodes"] = append([]int(nil), result.AttemptStatusCodes...)
		item.Evidence["retryWaitMilliseconds"] = retryWaitMilliseconds(result.RetryWaitDurations)
		item.Evidence["attempts"] = safeAttemptDetails(result.AttemptDetails)
	}
	return item
}

func retryWaitMilliseconds(waits []time.Duration) []int64 {
	result := make([]int64, 0, len(waits))
	for _, wait := range waits {
		result = append(result, wait.Milliseconds())
	}
	return result
}

func safeAttemptDetails(details []AttemptDetail) []map[string]any {
	result := make([]map[string]any, 0, len(details))
	for _, detail := range details {
		result = append(result, map[string]any{"startedAt": detail.StartedAt.UTC().Format(time.RFC3339Nano), "durationMilliseconds": detail.Duration.Milliseconds(), "httpStatus": detail.HTTPStatus, "error": detail.Error})
	}
	return result
}

func parseJSONObject(output string) map[string]any {
	output = strings.TrimSpace(output)
	var value map[string]any
	if json.Unmarshal([]byte(output), &value) == nil {
		return value
	}
	start, end := strings.Index(output, "{"), strings.LastIndex(output, "}")
	if start >= 0 && end > start && json.Unmarshal([]byte(output[start:end+1]), &value) == nil {
		return value
	}
	return nil
}

func safeStructured(value map[string]any) map[string]any {
	result := make(map[string]any)
	if value == nil {
		return result
	}
	if status, ok := value["status"].(string); ok {
		result["status"] = status
	}
	if number, ok := value["value"].(float64); ok {
		result["value"] = number
	}
	return result
}

func safeUsage(value map[string]any) map[string]any {
	result := make(map[string]any)
	for _, key := range []string{"input_tokens", "output_tokens", "total_tokens", "prompt_tokens", "completion_tokens", "promptTokenCount", "candidatesTokenCount", "totalTokenCount"} {
		if number, ok := value[key].(float64); ok {
			result[key] = number
		}
	}
	return result
}

func hasFunctionCall(payload map[string]any, name string) bool {
	if payload == nil {
		return false
	}
	for _, entry := range list(payload["output"]) {
		record := asRecord(entry)
		if record["type"] == "function_call" && record["name"] == name {
			return argumentsMatch(record["arguments"])
		}
	}
	for _, entry := range list(payload["choices"]) {
		message := asRecord(asRecord(entry)["message"])
		for _, call := range list(message["tool_calls"]) {
			fn := asRecord(asRecord(call)["function"])
			if fn["name"] == name && argumentsMatch(fn["arguments"]) {
				return true
			}
		}
	}
	for _, entry := range list(payload["content"]) {
		record := asRecord(entry)
		if record["type"] == "tool_use" && record["name"] == name && argumentsMatch(record["input"]) {
			return true
		}
	}
	for _, candidate := range list(payload["candidates"]) {
		content := asRecord(asRecord(candidate)["content"])
		for _, part := range list(content["parts"]) {
			call := asRecord(asRecord(part)["functionCall"])
			if call["name"] == name && argumentsMatch(call["args"]) {
				return true
			}
		}
	}
	return false
}

func argumentsMatch(value any) bool {
	record := asRecord(value)
	if record == nil {
		record = parseJSONObject(asText(value))
	}
	return asText(record["code"]) == "ok" && record["count"] == float64(1)
}

func list(value any) []any              { values, _ := value.([]any); return values }
func asRecord(value any) map[string]any { record, _ := value.(map[string]any); return record }
func asText(value any) string           { text, _ := value.(string); return text }
func modelMatches(actual, expected string) bool {
	actual, expected = strings.TrimSpace(actual), strings.TrimSpace(expected)
	if actual == "" || expected == "" {
		return false
	}
	if actual == expected {
		return true
	}
	prefix := expected + "-"
	if !strings.HasPrefix(actual, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(actual, prefix)
	if len(suffix) < 10 || suffix[4] != '-' || suffix[7] != '-' {
		return false
	}
	for index, value := range suffix[:10] {
		if index == 4 || index == 7 {
			continue
		}
		if value < '0' || value > '9' {
			return false
		}
	}
	return len(suffix) == 10 || strings.ContainsRune("._-", rune(suffix[10]))
}
