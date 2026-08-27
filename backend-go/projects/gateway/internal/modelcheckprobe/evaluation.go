package modelcheckprobe

import (
	"encoding/json"
	"strings"
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
	matched := result.ObservedModel == "" || modelMatches(result.ObservedModel, expectedModel)
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
	return Evaluation{Kind: "protocol_basic", Status: status, Score: score, MaxScore: 10, Evidence: map[string]any{"success": result.Success, "expectedModel": expectedModel, "responseModel": result.ObservedModel, "outputMatches": outputMatches, "modelMismatch": !matched}}
}

func EvaluateStructured(result Result, expectedModel string) Evaluation {
	value := safeStructured(parseJSONObject(result.Output))
	valid := value["status"] == "ok" && value["value"] == float64(7)
	matched := result.ObservedModel == "" || modelMatches(result.ObservedModel, expectedModel)
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
	return Evaluation{Kind: "structured_output", Status: status, Score: score, MaxScore: 15, Evidence: map[string]any{"success": result.Success, "expectedModel": expectedModel, "responseModel": result.ObservedModel, "outputJson": value, "valid": valid, "modelMismatch": !matched}}
}

func EvaluateTool(result Result, expectedModel string) Evaluation {
	called := hasFunctionCall(result.JSON, "record_model_check")
	matched := result.ObservedModel == "" || modelMatches(result.ObservedModel, expectedModel)
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
	return Evaluation{Kind: "tool_calling", Status: status, Score: score, MaxScore: 15, Evidence: map[string]any{"success": result.Success, "expectedModel": expectedModel, "responseModel": result.ObservedModel, "called": called, "modelMismatch": !matched}}
}

func EvaluateUsage(results []Result) Evaluation {
	for _, result := range results {
		if result.Success && len(result.Usage) > 0 {
			return Evaluation{Kind: "usage_shape", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true, "usage": safeUsage(result.Usage)}}
		}
	}
	return Evaluation{Kind: "usage_shape", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true}}
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
	return actual == "" || actual == expected || strings.HasPrefix(actual, expected+"-")
}
