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

// 探针家族规范满分（评分契约单源）：本文件的 Evaluate* 与超时处罚分母映射
// timeoutFamilyMaxScore 共用同一组常量，防止字面量漂移。满分锚定在其他实现
// 文件的家族（identityprobe/behavior/longcontext/stability）也在此登记常量，
// 供 timeoutFamilyMaxScore 引用；改动这些家族的满分时必须同步本处。
const (
	ProbeMaxScoreProtocolBasic    = 10
	ProbeMaxScoreStream           = 15 // protocol_stream / responses_stream 共用
	ProbeMaxScoreStructuredOutput = 15
	ProbeMaxScoreToolCalling      = 15
	ProbeMaxScoreUsageShape       = 10
	ProbeMaxScoreIdentityAnchor   = 10 // 满分锚定 identityprobe.go
	ProbeMaxScoreStability        = 15 // 满分锚定 stability.go
	ProbeMaxScoreBehaviorProbe    = 35 // 满分锚定 behavior.go
	ProbeMaxScoreLongContext      = 15 // 满分锚定 longcontext.go
)

// timeoutFamilyMaxScore 返回非题库家族超时项进入归一化分母的规范满分
// （超时处罚契约：开关开启时非题库超时项进分母、得 0 分）。家族失败项按
// requestFailure 约定不带满分（MaxScore=0），summary 据此取规范值。取值与
// 各 Evaluate* 的 MaxScore 原值一一对应；"responses_basic" 与 protocol_basic
// 同为 10（owner 核心探针清单中的别名，探针层 basic 项恒为 protocol_basic）。
// 证据性家族（sampling_statistics、identity_selfreport、identity_extraction）
// 与动态满分家族（token_integrity）规范分值为 0，未知 Kind 一律 0：这些超时
// 项不稀释分数，但 failed 计数与 unavailable 处罚触发链路照常生效。
func timeoutFamilyMaxScore(kind string) int {
	switch kind {
	case "protocol_basic", "responses_basic":
		return ProbeMaxScoreProtocolBasic
	case "protocol_stream", "responses_stream":
		return ProbeMaxScoreStream
	case "structured_output":
		return ProbeMaxScoreStructuredOutput
	case "tool_calling":
		return ProbeMaxScoreToolCalling
	case "usage_shape":
		return ProbeMaxScoreUsageShape
	case "identity_anchor":
		return ProbeMaxScoreIdentityAnchor
	case "stability":
		return ProbeMaxScoreStability
	case "behavior_probe":
		return ProbeMaxScoreBehaviorProbe
	case "long_context":
		return ProbeMaxScoreLongContext
	default:
		return 0
	}
}

func EvaluateBasic(result Result, expectedModel string) Evaluation {
	if !result.Success {
		return requestFailureEvaluation("protocol_basic", result, expectedModel, ProbeMaxScoreProtocolBasic)
	}
	responseModel, matched, modelMismatch := matchProbeResponseModel(result, expectedModel)
	outputMatches := strings.TrimSpace(result.Output) == "OK-MODEL-CHECK"
	score := 0
	if modelMismatch {
		// Keep the Node oracle's reduced mismatch score. A valid response
		// shape is still diagnostic evidence, but it must not retain normal
		// model-identity credit.
		if outputMatches {
			score = 1
		}
	} else {
		if matched {
			score += 3
		}
		if outputMatches {
			score += 7
		}
	}
	status := "failed"
	if result.Success && modelMismatch {
		status = "failed"
	} else if result.Success && matched && outputMatches {
		status = "passed"
	} else if result.Success && score >= 3 {
		status = "warning"
	}
	evidence := probeModelEvidence(result, expectedModel, responseModel, matched, modelMismatch)
	evidence["success"], evidence["outputMatches"] = result.Success, outputMatches
	return withRetryEvidence(Evaluation{Kind: "protocol_basic", Status: status, Score: score, MaxScore: ProbeMaxScoreProtocolBasic, Evidence: evidence}, result)
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
		return requestFailureEvaluation(kind, result, expectedModel, ProbeMaxScoreStream)
	}
	responseModel, matchedModel, modelMismatch := matchProbeResponseModel(result, expectedModel)
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
	evidence := probeModelEvidence(result, expectedModel, responseModel, matchedModel, modelMismatch)
	evidence["success"], evidence["expectedOutput"], evidence["outputMatches"] = result.Success, "STREAM-OK", outputMatches
	return withRetryEvidence(Evaluation{Kind: kind, Status: status, Score: score, MaxScore: ProbeMaxScoreStream, Evidence: evidence}, result)
}

// EvaluateStream is retained as the Responses-specific convenience used by
// callers that do not need to select a protocol item type.
func EvaluateStream(result Result, expectedModel string) Evaluation {
	return EvaluateProtocolStream(result, expectedModel, modelcheckprofile.ProtocolOpenAIResponses)
}

func EvaluateStructured(result Result, expectedModel string) Evaluation {
	if !result.Success {
		return requestFailureEvaluation("structured_output", result, expectedModel, ProbeMaxScoreStructuredOutput)
	}
	value := safeStructured(parseJSONObject(result.Output))
	valid := value["status"] == "ok" && value["value"] == float64(7)
	responseModel, matched, modelMismatch := matchProbeResponseModel(result, expectedModel)
	score := 0
	if modelMismatch {
		score = 4
		if valid {
			score++
		}
	} else {
		if result.Success {
			score = 8
		}
		if matched {
			score += 3
		}
		if valid {
			score += 4
		}
	}
	status := "failed"
	if modelMismatch {
		status = "failed"
	} else if matched && score >= 13 {
		status = "passed"
	} else if score >= 8 {
		status = "warning"
	}
	evidence := probeModelEvidence(result, expectedModel, responseModel, matched, modelMismatch)
	evidence["success"], evidence["outputJson"], evidence["valid"] = result.Success, value, valid
	return withRetryEvidence(Evaluation{Kind: "structured_output", Status: status, Score: score, MaxScore: ProbeMaxScoreStructuredOutput, Evidence: evidence}, result)
}

func EvaluateTool(result Result, expectedModel string) Evaluation {
	if !result.Success {
		return requestFailureEvaluation("tool_calling", result, expectedModel, ProbeMaxScoreToolCalling)
	}
	called := hasFunctionCall(result.JSON, "record_model_check")
	responseModel, matched, modelMismatch := matchProbeResponseModel(result, expectedModel)
	score := 0
	if modelMismatch {
		score = 4
		if called {
			score++
		}
	} else {
		if result.Success {
			score = 8
		}
		if matched {
			score += 3
		}
		if called {
			score += 4
		}
	}
	status := "failed"
	if modelMismatch {
		status = "failed"
	} else if matched && score >= 13 {
		status = "passed"
	} else if score >= 8 {
		status = "warning"
	}
	evidence := probeModelEvidence(result, expectedModel, responseModel, matched, modelMismatch)
	evidence["success"], evidence["called"] = result.Success, called
	return withRetryEvidence(Evaluation{Kind: "tool_calling", Status: status, Score: score, MaxScore: ProbeMaxScoreToolCalling, Evidence: evidence}, result)
}

func EvaluateUsage(results []Result) Evaluation {
	for _, result := range results {
		if result.Success && len(result.Usage) > 0 {
			return Evaluation{Kind: "usage_shape", Status: "passed", Score: ProbeMaxScoreUsageShape, MaxScore: ProbeMaxScoreUsageShape, Evidence: map[string]any{"success": true, "usage": safeUsage(result.Usage)}}
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
	if isTerminalProbeFailure(result) {
		evidence["terminalFailure"] = true
	}
	if result.RetryAttemptCount > 0 {
		evidence["retryAttemptCount"] = result.RetryAttemptCount
		evidence["retryMaxAttempts"] = result.RetryMaxAttempts
		evidence["attemptStatusCodes"] = append([]int(nil), result.AttemptStatusCodes...)
		evidence["retryWaitMilliseconds"] = retryWaitMilliseconds(result.RetryWaitDurations)
		evidence["attempts"] = safeAttemptDetails(result.AttemptDetails)
	}
	// BUG-0292：Incomplete 200 属传输面失败，保持 skipped + 请求失败证据
	// 路径（terminalFailure 由上方 isTerminalProbeFailure 分支附加，进而
	// 触发 summary.hasTerminalCoreProbeFailure 的整轮 unavailable 口径），
	// 不得剥离后按计分 failed 输出；本分支仅服务于完整读取 200 的质量失败。
	if result.HTTPStatus == http.StatusOK && !result.Incomplete && !IsModelUnavailable(result, expectedModel) {
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

// matchProbeResponseModel mirrors Node's configured-mapping contract. A
// response may legitimately echo either the mapped upstream model or the
// public request model when Business explicitly configured that mapping.
func matchProbeResponseModel(result Result, expectedModel string) (responseModel string, matched, mismatch bool) {
	responseModel = strings.TrimSpace(result.ObservedModel)
	if responseModel == "" {
		return responseModel, false, false
	}
	matched = modelMatches(responseModel, expectedModel)
	if !matched && result.ModelMappingApplied && strings.TrimSpace(result.RequestModel) != "" {
		matched = modelMatches(responseModel, result.RequestModel)
	}
	return responseModel, matched, !matched
}

func probeModelEvidence(result Result, expectedModel, responseModel string, matched, mismatch bool) map[string]any {
	evidence := map[string]any{"expectedModel": expectedModel, "responseModel": responseModel, "matchedModel": matched, "modelMismatch": mismatch}
	if result.ModelMappingApplied {
		evidence["modelMappingApplied"] = true
	}
	if requestModel := strings.TrimSpace(result.RequestModel); requestModel != "" {
		evidence["requestModel"] = requestModel
	}
	return evidence
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
