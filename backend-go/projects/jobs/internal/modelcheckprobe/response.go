package modelcheckprobe

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

type ParsedResponse struct {
	JSON                 map[string]any
	OutputText           string
	Model                string
	Usage                map[string]any
	SystemFingerprint    string
	ErrorMessage         string
	StreamFailureMessage string
}

func (p ParsedResponse) Successful(statusCode int) bool {
	return statusCode == 200 && p.ErrorMessage == "" && p.StreamFailureMessage == ""
}

// ParseResponse extracts only the stable evidence consumed by J3b evaluation.
// It accepts a normal JSON response or SSE frames, and never returns original
// response bodies so credentials cannot leak into a durable outcome.
func ParseResponse(protocol modelcheckprofile.Protocol, body []byte) ParsedResponse {
	payloads, stream := parsePayloads(body)
	if len(payloads) == 0 {
		return ParsedResponse{}
	}
	result := ParsedResponse{JSON: payloads[0]}
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		result = parseResponses(payloads)
	case modelcheckprofile.ProtocolOpenAIChat:
		result = parseChat(payloads)
	case modelcheckprofile.ProtocolAnthropic:
		result = parseAnthropic(payloads)
	case modelcheckprofile.ProtocolGeminiNative:
		result = parseGemini(payloads)
	}
	if stream && result.ErrorMessage != "" {
		result.StreamFailureMessage = result.ErrorMessage
	}
	return result
}

func parseResponses(payloads []map[string]any) ParsedResponse {
	result := ParsedResponse{JSON: payloads[0]}
	var parts []string
	for _, payload := range payloads {
		response := record(payload["response"])
		candidate := firstMap(response, payload)
		if result.Model == "" {
			result.Model = text(candidate["model"])
		}
		if result.Usage == nil {
			result.Usage = firstMap(record(candidate["usage"]), record(payload["usage"]))
		}
		if result.SystemFingerprint == "" {
			result.SystemFingerprint = text(candidate["system_fingerprint"])
		}
		parts = append(parts, responseText(candidate))
		parts = append(parts, text(payload["delta"]))
		if result.ErrorMessage == "" {
			result.ErrorMessage = errorText(payload, candidate)
		}
	}
	result.OutputText = join(parts)
	return result
}

func parseChat(payloads []map[string]any) ParsedResponse {
	result := ParsedResponse{JSON: payloads[0]}
	var parts []string
	for _, payload := range payloads {
		if result.Model == "" {
			result.Model = text(payload["model"])
		}
		if result.Usage == nil {
			result.Usage = record(payload["usage"])
		}
		parts = append(parts, chatText(payload))
		if result.ErrorMessage == "" {
			result.ErrorMessage = errorText(payload)
		}
	}
	result.OutputText = join(parts)
	return result
}

func parseAnthropic(payloads []map[string]any) ParsedResponse {
	result := ParsedResponse{JSON: payloads[0]}
	var parts []string
	for _, payload := range payloads {
		message := record(payload["message"])
		candidate := firstMap(message, payload)
		if result.Model == "" {
			result.Model = text(candidate["model"])
		}
		if result.Usage == nil {
			result.Usage = firstMap(record(candidate["usage"]), record(payload["usage"]))
		}
		for _, entry := range list(candidate["content"]) {
			parts = append(parts, text(record(entry)["text"]))
		}
		parts = append(parts, text(record(payload["delta"])["text"]))
		if result.ErrorMessage == "" {
			result.ErrorMessage = errorText(payload, candidate)
		}
	}
	result.OutputText = join(parts)
	return result
}

func parseGemini(payloads []map[string]any) ParsedResponse {
	result := ParsedResponse{JSON: payloads[0]}
	var parts []string
	for _, payload := range payloads {
		if result.Model == "" {
			result.Model = first(text(payload["model"]), text(payload["modelVersion"]))
		}
		if result.Usage == nil {
			result.Usage = record(payload["usageMetadata"])
		}
		for _, candidate := range list(payload["candidates"]) {
			content := record(record(candidate)["content"])
			for _, part := range list(content["parts"]) {
				parts = append(parts, text(record(part)["text"]))
			}
		}
		if result.ErrorMessage == "" {
			result.ErrorMessage = errorText(payload)
		}
	}
	result.OutputText = join(parts)
	return result
}

func parsePayloads(body []byte) ([]map[string]any, bool) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}))
	if parsed := parseJSONObject(trimmed); parsed != nil {
		return []map[string]any{parsed}, false
	}
	var payloads []map[string]any
	var data []string
	flush := func() {
		if len(data) == 0 {
			return
		}
		if parsed := parseJSONObject([]byte(strings.Join(data, "\n"))); parsed != nil {
			payloads = append(payloads, parsed)
		}
		data = nil
	}
	for _, line := range strings.Split(string(trimmed), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	return payloads, len(payloads) > 0
}

func parseJSONObject(value []byte) map[string]any {
	if len(value) == 0 {
		return nil
	}
	var parsed map[string]any
	if json.Unmarshal(value, &parsed) != nil {
		return nil
	}
	return parsed
}

func responseText(payload map[string]any) string {
	if direct := text(payload["output_text"]); direct != "" {
		return direct
	}
	var parts []string
	for _, output := range list(payload["output"]) {
		for _, content := range list(record(output)["content"]) {
			parts = append(parts, text(record(content)["text"]))
		}
	}
	return join(parts)
}

func chatText(payload map[string]any) string {
	var parts []string
	for _, choice := range list(payload["choices"]) {
		item := record(choice)
		for _, field := range []string{"message", "delta"} {
			container := record(item[field])
			parts = append(parts, text(container["content"]), text(container["reasoning_content"]), text(container["refusal"]))
		}
	}
	return join(parts)
}

func errorText(candidates ...map[string]any) string {
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		failure := record(candidate["error"])
		if message := first(text(failure["message"]), text(failure["code"]), text(failure["type"]), text(candidate["message"])); message != "" {
			return message
		}
	}
	return ""
}

func record(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func list(value any) []any {
	result, _ := value.([]any)
	return result
}

func text(value any) string {
	result, _ := value.(string)
	return strings.TrimSpace(result)
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func join(parts []string) string {
	return strings.TrimSpace(strings.Join(parts, ""))
}
