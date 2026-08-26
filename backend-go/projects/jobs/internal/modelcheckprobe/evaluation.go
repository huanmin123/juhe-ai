package modelcheckprobe

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	expectedBasicOutput  = "OK-MODEL-CHECK"
	expectedStreamOutput = "STREAM-OK"
)

// ProbeResult is the normalized, credential-free evidence passed from a
// future direct probe transport into the J3b evaluator.
type ProbeResult struct {
	HTTPStatusCode       int
	Success              bool
	DurationMS           int64
	TraceID              string
	FirstTokenMS         *int64
	RequestModel         string
	ExpectedModel        string
	UpstreamModel        string
	ModelMappingApplied  *bool
	ModelMappingSource   string
	SourceEndpointFamily string
	UpstreamEndpoint     string
	UpstreamStatusCode   *int
	RateLimited          bool
	ResponseTruncated    bool
	Response             ParsedResponse
}

// EvaluationItem is deliberately storage-neutral. The jobs runtime will map
// it to an immutable outcome before it writes model_check_items.
type EvaluationItem struct {
	ItemKey      string
	ItemType     string
	Status       string
	Score        int
	MaxScore     int
	DurationMS   int64
	TraceID      string
	Evidence     map[string]any
	ErrorCode    string
	ErrorMessage string
}

type ProtocolEvaluationOptions struct {
	ItemKey        string
	ItemType       string
	SuccessMessage string
	FailurePrefix  string
}

func EvaluateBasicResponses(result ProbeResult, expectedModel, prefix string, stream bool) EvaluationItem {
	transport := "非流式"
	if stream {
		transport = "流式"
	}
	return EvaluateBasicProtocol(result, expectedModel, ProtocolEvaluationOptions{
		ItemKey:        prefix + ".responses_basic",
		ItemType:       "responses_basic",
		SuccessMessage: "Responses " + transport + "调用可用",
		FailurePrefix:  "Responses " + transport + "调用失败",
	})
}

func EvaluateBasicProtocol(result ProbeResult, expectedModel string, options ProtocolEvaluationOptions) EvaluationItem {
	if !result.Success {
		return requestFailureItem(options.ItemKey, options.ItemType, result, first(result.Response.ErrorMessage, fmt.Sprintf("%s，HTTP %d", options.FailurePrefix, result.HTTPStatusCode)), map[string]any{"expectedModel": expectedModel})
	}
	model := buildModelEvidence(result, expectedModel)
	outputMatches := strings.TrimSpace(result.Response.OutputText) == expectedBasicOutput
	score := 0
	if model.ModelMismatch {
		if outputMatches {
			score = 1
		}
	} else {
		if model.MatchedModel {
			score += 3
		}
		if outputMatches {
			score += 7
		}
	}
	status := "failed"
	if !model.ModelMismatch && outputMatches && model.MatchedModel {
		status = "passed"
	} else if !model.ModelMismatch && score >= 3 {
		status = "warning"
	}
	evidence := model.asMap()
	evidence["message"] = first(model.mismatchMessage(), ternary(outputMatches, options.SuccessMessage, "基础协议响应未按固定契约返回 OK-MODEL-CHECK"))
	evidence["expectedOutput"] = expectedBasicOutput
	evidence["outputMatches"] = outputMatches
	return item(options.ItemKey, options.ItemType, status, score, 10, result, evidence)
}

func EvaluateStream(result ProbeResult, expectedModel, prefix string) EvaluationItem {
	return EvaluateProtocolStream(result, expectedModel, ProtocolEvaluationOptions{
		ItemKey:        prefix + ".responses_stream",
		ItemType:       "responses_stream",
		SuccessMessage: "Responses 流式调用可用",
		FailurePrefix:  "Responses 流式调用失败",
	})
}

func EvaluateProtocolStream(result ProbeResult, expectedModel string, options ProtocolEvaluationOptions) EvaluationItem {
	if !result.Success {
		evidence := map[string]any{"expectedModel": expectedModel}
		if result.FirstTokenMS != nil {
			evidence["firstTokenMs"] = *result.FirstTokenMS
		}
		return requestFailureItem(options.ItemKey, options.ItemType, result, first(result.Response.ErrorMessage, fmt.Sprintf("%s，HTTP %d", options.FailurePrefix, result.HTTPStatusCode)), evidence)
	}
	model := buildModelEvidence(result, expectedModel)
	outputMatches := strings.TrimSpace(result.Response.OutputText) == expectedStreamOutput
	score := 0
	if model.ModelMismatch {
		score = 4
		if outputMatches {
			score++
		}
	} else {
		score = 8
		if model.MatchedModel {
			score += 3
		}
		if outputMatches {
			score += 4
		}
	}
	status := "failed"
	if !model.ModelMismatch && score >= 13 {
		status = "passed"
	} else if !model.ModelMismatch && score >= 8 {
		status = "warning"
	}
	evidence := model.asMap()
	evidence["message"] = first(model.mismatchMessage(), ternary(result.Success, options.SuccessMessage, first(result.Response.ErrorMessage, fmt.Sprintf("%s，HTTP %d", options.FailurePrefix, result.HTTPStatusCode))))
	evidence["expectedOutput"] = expectedStreamOutput
	evidence["outputMatches"] = outputMatches
	if result.FirstTokenMS != nil {
		evidence["firstTokenMs"] = *result.FirstTokenMS
	}
	return item(options.ItemKey, options.ItemType, status, score, 15, result, evidence)
}

func EvaluateStructuredOutput(result ProbeResult, expectedModel, prefix string) EvaluationItem {
	key := prefix + ".structured_output"
	if !result.Success {
		return requestFailureItem(key, "structured_output", result, first(result.Response.ErrorMessage, fmt.Sprintf("结构化输出调用失败，HTTP %d", result.HTTPStatusCode)), map[string]any{"expectedModel": expectedModel})
	}
	output := safeStructuredOutput(parseFirstJSONObject(result.Response.OutputText))
	valid := text(output["status"]) == "ok" && numberEquals(output["value"], 7)
	model := buildModelEvidence(result, expectedModel)
	score := structuredScore(result.Success, model, valid)
	status := structuredStatus(model, score)
	evidence := model.asMap()
	evidence["message"] = first(model.mismatchMessage(), ternary(valid, "结构化输出符合预期", "结构化输出未完全符合预期"))
	evidence["outputJson"] = output
	return item(key, "structured_output", status, score, 15, result, evidence)
}

func EvaluateToolCalling(result ProbeResult, expectedModel, prefix string) EvaluationItem {
	key := prefix + ".tool_calling"
	if !result.Success {
		return requestFailureItem(key, "tool_calling", result, first(result.Response.ErrorMessage, fmt.Sprintf("工具调用检测失败，HTTP %d", result.HTTPStatusCode)), map[string]any{"expectedModel": expectedModel})
	}
	called := hasExpectedFunctionCall(result.Response.JSON, "record_model_check", map[string]any{"code": "ok", "count": float64(1)})
	model := buildModelEvidence(result, expectedModel)
	score := structuredScore(result.Success, model, called)
	status := structuredStatus(model, score)
	evidence := model.asMap()
	evidence["message"] = first(model.mismatchMessage(), ternary(called, "工具调用结构符合预期", "未观察到预期工具调用结构"))
	evidence["called"] = called
	return item(key, "tool_calling", status, score, 15, result, evidence)
}

func EvaluateUsageShape(results []ProbeResult, prefix string) EvaluationItem {
	key := prefix + ".usage_shape"
	var successful []ProbeResult
	for _, result := range results {
		if result.Success {
			successful = append(successful, result)
		}
	}
	base := ProbeResult{}
	if len(successful) > 0 {
		base = successful[0]
		for _, result := range successful {
			if result.Response.Usage != nil {
				base = result
				break
			}
		}
	} else if len(results) > 0 {
		base = results[0]
	}
	if len(successful) == 0 {
		return requestFailureItem(key, "usage_shape", base, "探针请求失败，未形成 usage 字段证据", map[string]any{"requestFailureCount": len(results), "probeCount": len(results)})
	}
	var usage map[string]any
	for _, result := range successful {
		if result.Response.Usage != nil {
			usage = result.Response.Usage
			break
		}
	}
	safeUsage := safeUsage(usage)
	valid := len(safeUsage) > 0
	evidence := map[string]any{"message": ternary(valid, "usage 字段结构可用", "未观察到可验证 usage 字段，作为证据不足处理"), "usage": safeUsage}
	if !valid {
		evidence["evidenceInsufficient"] = true
		evidence["excludedFromScoring"] = true
	}
	if valid {
		return item(key, "usage_shape", "passed", 10, 10, base, evidence)
	}
	return item(key, "usage_shape", "skipped", 0, 0, base, evidence)
}

type modelEvidence struct {
	ExpectedModel       string
	RequestModel        string
	UpstreamModel       string
	ModelMappingApplied *bool
	ModelMappingSource  string
	SourceEndpoint      string
	UpstreamEndpoint    string
	ResponseModel       string
	MatchedModel        bool
	ModelMismatch       bool
}

func buildModelEvidence(result ProbeResult, expected string) modelEvidence {
	request := strings.TrimSpace(result.RequestModel)
	matchRequest := request
	if matchRequest == "" {
		matchRequest = expected
	}
	response := strings.TrimSpace(result.Response.Model)
	mappingApplied := result.ModelMappingApplied != nil && *result.ModelMappingApplied
	matched := modelMatches(response, expected) || (mappingApplied && modelMatches(response, matchRequest))
	return modelEvidence{ExpectedModel: expected, RequestModel: request, UpstreamModel: result.UpstreamModel, ModelMappingApplied: result.ModelMappingApplied, ModelMappingSource: result.ModelMappingSource, SourceEndpoint: result.SourceEndpointFamily, UpstreamEndpoint: result.UpstreamEndpoint, ResponseModel: response, MatchedModel: matched, ModelMismatch: response != "" && !matched}
}

func (e modelEvidence) asMap() map[string]any {
	result := map[string]any{"expectedModel": e.ExpectedModel, "responseModel": e.ResponseModel, "matchedModel": e.MatchedModel, "modelMismatch": e.ModelMismatch}
	putString(result, "requestModel", e.RequestModel)
	putString(result, "upstreamModel", e.UpstreamModel)
	if e.ModelMappingApplied != nil {
		result["modelMappingApplied"] = *e.ModelMappingApplied
	}
	putString(result, "modelMappingSource", e.ModelMappingSource)
	putString(result, "sourceEndpointFamily", e.SourceEndpoint)
	putString(result, "upstreamEndpointFamily", e.UpstreamEndpoint)
	return result
}

func (e modelEvidence) mismatchMessage() string {
	if !e.ModelMismatch || e.ResponseModel == "" {
		return ""
	}
	if e.ModelMappingApplied != nil && *e.ModelMappingApplied && e.RequestModel != "" {
		return fmt.Sprintf("上游返回模型 %s，与映射上游模型 %s 不一致（请求模型 %s）", e.ResponseModel, e.ExpectedModel, e.RequestModel)
	}
	return fmt.Sprintf("上游返回模型 %s，与请求模型 %s 不一致", e.ResponseModel, e.ExpectedModel)
}

func structuredScore(success bool, model modelEvidence, valid bool) int {
	if model.ModelMismatch {
		score := 0
		if success {
			score = 4
		}
		if valid {
			score++
		}
		return score
	}
	score := 0
	if success {
		score = 8
	}
	if model.MatchedModel {
		score += 3
	}
	if valid {
		score += 4
	}
	return score
}

func structuredStatus(model modelEvidence, score int) string {
	if model.ModelMismatch {
		return "failed"
	}
	if score >= 13 {
		return "passed"
	}
	if score >= 8 {
		return "warning"
	}
	return "failed"
}

func requestFailureItem(key, kind string, result ProbeResult, message string, evidence map[string]any) EvaluationItem {
	evidence["message"] = message
	evidence["requestFailure"] = true
	evidence["excludedFromScoring"] = true
	return item(key, kind, "skipped", 0, 0, result, evidence)
}

func item(key, kind, status string, score, maxScore int, result ProbeResult, evidence map[string]any) EvaluationItem {
	evidence["httpStatus"] = result.HTTPStatusCode
	evidence["success"] = result.Success
	putString(evidence, "requestModel", result.RequestModel)
	putString(evidence, "expectedModel", result.ExpectedModel)
	putString(evidence, "upstreamModel", result.UpstreamModel)
	if result.ModelMappingApplied != nil {
		evidence["modelMappingApplied"] = *result.ModelMappingApplied
	}
	putString(evidence, "modelMappingSource", result.ModelMappingSource)
	putString(evidence, "sourceEndpointFamily", result.SourceEndpointFamily)
	putString(evidence, "upstreamEndpointFamily", result.UpstreamEndpoint)
	putString(evidence, "responseModel", result.Response.Model)
	if result.FirstTokenMS != nil {
		evidence["firstTokenMs"] = *result.FirstTokenMS
	}
	if result.UpstreamStatusCode != nil {
		evidence["upstreamStatusCode"] = *result.UpstreamStatusCode
	}
	evidence["rateLimited"] = result.RateLimited
	evidence["responseTruncated"] = result.ResponseTruncated
	output := EvaluationItem{ItemKey: key, ItemType: kind, Status: status, Score: score, MaxScore: maxScore, DurationMS: result.DurationMS, TraceID: result.TraceID, Evidence: evidence}
	if !result.Success {
		output.ErrorCode = fmt.Sprintf("http_%d", result.HTTPStatusCode)
		output.ErrorMessage = result.Response.ErrorMessage
	}
	return output
}

func hasExpectedFunctionCall(payload map[string]any, name string, expected map[string]any) bool {
	for _, arguments := range functionCallArguments(payload, name) {
		matched := true
		for key, value := range expected {
			if arguments[key] != value {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func functionCallArguments(payload map[string]any, name string) []map[string]any {
	var matches []map[string]any
	for _, entry := range list(payload["output"]) {
		entryRecord := record(entry)
		if text(entryRecord["type"]) == "function_call" && text(entryRecord["name"]) == name {
			matches = append(matches, argumentRecord(entryRecord["arguments"]))
		}
	}
	for _, entry := range list(payload["choices"]) {
		message := record(record(entry)["message"])
		for _, toolCall := range list(message["tool_calls"]) {
			fn := record(record(toolCall)["function"])
			if text(fn["name"]) == name {
				matches = append(matches, argumentRecord(fn["arguments"]))
			}
		}
	}
	for _, entry := range list(payload["content"]) {
		entryRecord := record(entry)
		if text(entryRecord["type"]) == "tool_use" && text(entryRecord["name"]) == name {
			matches = append(matches, record(entryRecord["input"]))
		}
	}
	for _, candidate := range list(payload["candidates"]) {
		content := record(record(candidate)["content"])
		for _, part := range list(content["parts"]) {
			call := record(record(part)["functionCall"])
			if text(call["name"]) == name {
				matches = append(matches, record(call["args"]))
			}
		}
	}
	return matches
}

func argumentRecord(value any) map[string]any {
	if direct := record(value); direct != nil {
		return direct
	}
	return parseFirstJSONObject(text(value))
}

func parseFirstJSONObject(value string) map[string]any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(value), &parsed) == nil {
		return parsed
	}
	start, end := strings.Index(value, "{"), strings.LastIndex(value, "}")
	if start < 0 || end <= start || json.Unmarshal([]byte(value[start:end+1]), &parsed) != nil {
		return nil
	}
	return parsed
}

func safeUsage(usage map[string]any) map[string]any {
	result := make(map[string]any)
	for _, key := range []string{"input_tokens", "output_tokens", "total_tokens", "prompt_tokens", "completion_tokens", "promptTokenCount", "candidatesTokenCount", "totalTokenCount"} {
		if isNumber(usage[key]) {
			result[key] = usage[key]
		}
	}
	return result
}

func safeStructuredOutput(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any)
	if status := text(value["status"]); status != "" {
		result["status"] = status
	}
	if isNumber(value["value"]) {
		result["value"] = value["value"]
	}
	return result
}

func modelMatches(actual, expected string) bool {
	actual, expected = strings.TrimSpace(actual), strings.TrimSpace(expected)
	if actual == "" || expected == "" {
		return false
	}
	if actual == expected {
		return true
	}
	suffix := strings.TrimPrefix(actual, expected+"-")
	if suffix == actual || len(suffix) < len("2000-01-01") {
		return false
	}
	return isDatePrefix(suffix)
}

func isDatePrefix(value string) bool {
	if len(value) < 10 || value[4] != '-' || value[7] != '-' {
		return false
	}
	for _, index := range []int{0, 1, 2, 3, 5, 6, 8, 9} {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return len(value) == 10 || value[10] == '.' || value[10] == '_' || value[10] == '-'
}

func putString(values map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		values[key] = strings.TrimSpace(value)
	}
}

func ternary(condition bool, yes, no string) string {
	if condition {
		return yes
	}
	return no
}

func numberEquals(value any, expected float64) bool {
	result, ok := value.(float64)
	return ok && result == expected
}

func isNumber(value any) bool {
	_, ok := value.(float64)
	return ok
}
