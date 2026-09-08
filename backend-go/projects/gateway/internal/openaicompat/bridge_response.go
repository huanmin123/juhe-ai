package openaicompat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// BridgeTransformResponseOptions provides options for response transformation.
type BridgeTransformResponseOptions struct {
	Enabled                bool
	Model                  string
	FileResolver           FileResolver
	FileSearchExecutor     FileSearchExecutor
	SourceEndpointFamily   string
	UpstreamEndpointFamily string
}

// TransformedResponse represents a transformed bridge response.
type TransformedResponse struct {
	Status        int
	Headers       http.Header
	Body          []byte
	Stream        bool
	ResponseMode  string
}

// TransformOpenAIAnthropicBridgeUpstreamResponse transforms an Anthropic response
// back to OpenAI format (reverse direction).
func TransformOpenAIAnthropicBridgeUpstreamResponse(
	response map[string]any,
	options BridgeTransformResponseOptions,
) map[string]any {
	if !options.Enabled || response == nil {
		return response
	}

	result := make(map[string]any)

	// Transform usage
	if usage, ok := response["usage"].(map[string]any); ok {
		result["usage"] = transformAnthropicUsageToOpenAI(usage)
	}

	// Transform content
	if content, ok := response["content"].(string); ok && content != "" {
		if options.SourceEndpointFamily == "chat_completions" {
			result["choices"] = []any{
				map[string]any{
					"message": map[string]any{
						"role":    "assistant",
						"content": content,
					},
					"finish_reason": finishReasonFromStopSequence(response),
				},
			}
		}
	}

	return result
}

// TransformGeminiGenerateContentToOpenAIChatResponse transforms Gemini GenerateContent
// response to OpenAI Chat Completions format.
func TransformGeminiGenerateContentToOpenAIChatResponse(
	response map[string]any,
	options BridgeTransformResponseOptions,
) map[string]any {
	if !options.Enabled || response == nil {
		return response
	}

	result := make(map[string]any)

	// Build choices array
	var choices []map[string]any

	// Handle candidates
	if candidates, ok := response["candidates"].([]any); ok {
		for _, candidate := range candidates {
			if candMap, ok := candidate.(map[string]any); ok {
				choice := make(map[string]any)
				if content, ok := candMap["content"].(map[string]any); ok {
					parts := extractGeminiParts(content)
					if len(parts) == 1 {
						choice["message"] = map[string]any{
							"role":    "assistant",
							"content": parts[0],
						}
					} else {
						// Concatenate multiple parts
						choice["message"] = map[string]any{
							"role":    "assistant",
							"content": strings.Join(parts, ""),
						}
					}
				}
				choice["finish_reason"] = finishReasonFromGeminiCandidate(candMap)
				choices = append(choices, choice)
			}
		}
	}

	if len(choices) > 0 {
		result["choices"] = choices
	}

	// Transform usage
	if usage, ok := response["usageMetadata"].(map[string]any); ok {
		result["usage"] = map[string]any{
			"prompt_tokens":     usage["promptTokenCount"],
			"completion_tokens": usage["candidatesTokenCount"],
			"total_tokens":      usage["totalTokenCount"],
		}
	}

	return result
}

func extractGeminiParts(content map[string]any) []string {
	var parts []string
	if text, ok := content["text"].(string); ok && text != "" {
		parts = append(parts, text)
		return parts
	}
	// Handle inline parts
	if inlineParts, ok := content["inlineParts"].([]any); ok {
		for _, p := range inlineParts {
			if partMap, ok := p.(map[string]any); ok {
				if text, ok := partMap["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "")
	}
	return parts
}

func finishReasonFromStopSequence(response map[string]any) string {
	if stop, ok := response["stop_reason"].(string); ok {
		switch stop {
		case "stop_sequence":
			return "stop"
		case "max_tokens":
			return "length"
		case "safety":
			return "stop"
		default:
			return "stop"
		}
	}
	return "stop"
}

func finishReasonFromGeminiCandidate(candidate map[string]any) string {
	if finish, ok := candidate["finishReason"].(string); ok {
		switch finish {
		case "FINISH_REASON_UNSPECIFIED":
			return "stop"
		case "FINISH_REASON_MAX_TOKENS":
			return "length"
		case "FINISH_REASON_SAFETY":
			return "stop"
		case "FINISH_REASON_REASON":
			return "stop"
		default:
			return "stop"
		}
	}
	return "stop"
}

func transformAnthropicUsageToOpenAI(usage map[string]any) map[string]any {
	result := make(map[string]any)
	if inputTokens, ok := usage["input_tokens"].(float64); ok {
		result["prompt_tokens"] = int(inputTokens)
	}
	if outputTokens, ok := usage["output_tokens"].(float64); ok {
		result["completion_tokens"] = int(outputTokens)
	}
	if cacheRead, ok := usage["cache_read_input_tokens"].(float64); ok {
		result["cache_read_input_tokens"] = int(cacheRead)
	}
	if cacheWrite, ok := usage["cache_write_input_tokens"].(float64); ok {
		result["cache_write_input_tokens"] = int(cacheWrite)
	}

	// Calculate total
	prompt := float64(0)
	if v, ok := result["prompt_tokens"].(int); ok {
		prompt = float64(v)
	}
	completion := float64(0)
	if v, ok := result["completion_tokens"].(int); ok {
		completion = float64(v)
	}
	result["total_tokens"] = int(prompt + completion)

	return result
}

// TransformSSEStreamToGeminiSSE transforms OpenAI SSE stream events to Gemini SSE format.
func TransformSSEStreamToGeminiSSE(eventText string, options BridgeTransformResponseOptions) string {
	if !options.Enabled {
		return eventText
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(eventText), &event); err != nil {
		return eventText
	}

	result := make(map[string]any)

	// Transform data field
	if data, ok := event["data"].(map[string]any); ok {
		// This is typically a chat completion chunk
		if choices, ok := data["choices"].([]any); ok && len(choices) > 0 {
			for _, choice := range choices {
				if choiceMap, ok := choice.(map[string]any); ok {
					if delta, ok := choiceMap["delta"].(map[string]any); ok && delta != nil {
						// Transform to Gemini format
						if content, ok := delta["content"].(string); ok && content != "" {
							result["content"] = content
						}
						if role, ok := delta["role"].(string); ok && role != "" {
							result["role"] = role
						}
					}
					if finishReason, ok := choiceMap["finish_reason"].(string); ok {
						result["finishReason"] = finishReason
					}
				}
			}
		}
		if usage, ok := data["usage"].(map[string]any); ok {
			result["usageMetadata"] = usage
		}
	}

	// Keep event type
	if eventType, ok := event["event"].(string); ok {
		result["type"] = eventType
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return eventText
	}

	return string(encoded)
}

// TransformOpenAIChatToAnthropicStream transforms OpenAI SSE stream to Anthropic format.
func TransformOpenAIChatToAnthropicStream(eventText string, options BridgeTransformResponseOptions) string {
	if !options.Enabled {
		return eventText
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(eventText), &event); err != nil {
		return eventText
	}

	result := make(map[string]any)

	// Transform to Anthropic SSE format
	if data, ok := event["data"].(map[string]any); ok {
		if completion, ok := data["choices"].([]any); ok && len(completion) > 0 {
			for _, c := range completion {
				if cMap, ok := c.(map[string]any); ok {
					if delta, ok := cMap["delta"].(map[string]any); ok {
						// Transform delta to Anthropic format
						result["type"] = "content_block_delta"
						result["index"] = 0
						result["content_block"] = map[string]any{
							"type": "text",
							"text": delta["content"],
						}
						result["usage"] = data["usage"]
					}
					if finishReason, ok := cMap["finish_reason"].(string); ok {
						result["type"] = "finalize"
						result["stop_reason"] = finishReason
					}
				}
			}
		}
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return eventText
	}

	return string(encoded)
}

// BuildGatewayUpstreamResponse builds a wrapped response for bridge transformation.
func BuildGatewayUpstreamResponse(status int, headers map[string]string, body []byte) map[string]any {
	result := map[string]any{
		"status": status,
		"headers": headers,
		"body":    string(body),
	}
	return result
}

// ExtractJSONObject extracts a JSON object from a body.
func ExtractJSONObject(body string) (map[string]any, error) {
	var result map[string]any
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return nil, fmt.Errorf("parse json error: %w", err)
	}
	return result, nil
}

// PrepareBridgeHeaders prepares request headers for protocol bridge.
func PrepareBridgeHeaders(headers map[string]string, reqPath string) map[string]string {
	result := make(map[string]string)
	for k, v := range headers {
		result[k] = v
	}

	// Add bridge-specific headers
	result["Accept"] = "application/json"

	return result
}