package openaicompat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BridgeRequestBodyOptions provides options for bridge request body construction.
type BridgeRequestBodyOptions struct {
	DefaultModel             string
	GuidanceProviderName     string
	ModelOverride            string
	TargetPathAndQuery       string
	ClientCompatibility      map[string]any
	MaxTokens                *int64
	Temperature              *float64
}

// BuildOpenAIChatBridgeBody builds a Gemini GenerateContent or Anthropic Messages
// body from an OpenAI chat completions request.
func BuildOpenAIChatBridgeBody(
	clientBody map[string]any,
	options BridgeRequestBodyOptions,
) (map[string]any, error) {
	// Build the base body with model
	model := options.ModelOverride
	if model == "" {
		model = options.DefaultModel
	}

	result := make(map[string]any)
	result["model"] = model

	// Transform messages
	if messages, ok := clientBody["messages"].([]any); ok && len(messages) > 0 {
		transformed, err := transformOpenAIMessagesToBridge(messages, options)
		if err != nil {
			return nil, err
		}
		// For Gemini: flatten to single content
		result["contents"] = transformedContents(transformed)
	}

	// Transform tools if present
	if tools, ok := clientBody["tools"].([]any); ok && len(tools) > 0 {
		transformedTools, err := transformOpenAIToolsToBridge(tools)
		if err != nil {
			return nil, err
		}
		if len(transformedTools) > 0 {
			result["tools"] = transformedTools
		}
	}

	// Preserve other optional fields
	if maxTokens, ok := clientBody["max_tokens"]; ok {
		result["max_tokens"] = maxTokens
	}
	if temperature, ok := clientBody["temperature"]; ok {
		result["temperature"] = temperature
	}
	if topP, ok := clientBody["top_p"]; ok {
		result["top_p"] = topP
	}
	if stream, ok := clientBody["stream"]; ok {
		result["stream"] = stream
	}

	return result, nil
}

// transformOpenAIMessagesToBridge converts OpenAI message array to bridge format.
func transformOpenAIMessagesToBridge(messages []any, options BridgeRequestBodyOptions) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}

		role := getStringValue(msgMap, "role")
		if role == "" {
			continue
		}

		// Convert role
		bridgeRole := role
		switch role {
		case "user":
			bridgeRole = "user"
		case "assistant":
			bridgeRole = "model"
		case "system":
			bridgeRole = "system"
		case "tool":
			bridgeRole = "tool"
		}

		// Build content
		contentMap := make(map[string]any)
		contentMap["role"] = bridgeRole

		// Transform content based on role
		if content, ok := msgMap["content"]; ok {
			contentMap["parts"] = transformContentToParts(content)
		}

		// Transform tool calls if present
		if toolCalls, ok := msgMap["tool_calls"]; ok {
			if calls, err := transformToolCalls(toolCalls); err == nil {
				contentMap["function_calls"] = calls
			}
		}

		// Transform tool response
		if toolRole, ok := msgMap["role"].(string); ok && toolRole == "tool" {
			if toolCallID := getStringValue(msgMap, "tool_call_id"); toolCallID != "" {
				contentMap["function_name"] = toolCallID
			}
			if content, ok := msgMap["content"]; ok {
				contentMap["text"] = toString(content)
			}
		}

		result = append(result, contentMap)
	}

	return result, nil
}

func transformContentToParts(content any) []any {
	switch c := content.(type) {
	case string:
		return []any{c}
	case map[string]any:
		// Handle complex content with type
		if text, ok := c["text"].(string); ok {
			return []any{text}
		}
		if parts, ok := c["parts"].([]any); ok {
			return parts
		}
		return []any{fmt.Sprintf("%v", c)}
	case []any:
		return c
	default:
		return []any{fmt.Sprintf("%v", c)}
	}
}

func transformOpenAIToolsToBridge(tools []any) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]any)
		if !ok {
			continue
		}

		// Determine tool type
		if function, ok := toolMap["function"].(map[string]any); ok {
			bridgeTool := make(map[string]any)
			if name, ok := function["name"].(string); ok {
				bridgeTool["name"] = name
			}
			if description, ok := function["description"].(string); ok {
				bridgeTool["description"] = description
			}
			if params, ok := function["parameters"].(map[string]any); ok {
				bridgeTool["parameters"] = params
			}
			if len(bridgeTool) > 0 {
				result = append(result, bridgeTool)
			}
		} else if toolType := getStringValue(toolMap, "type"); toolType == "file_search" {
			// Handle tool-related type annotations
			result = append(result, toolMap)
		}
	}
	return result, nil
}

func transformToolCalls(toolCalls any) ([]map[string]any, error) {
	calls, ok := toolCalls.([]any)
	if !ok {
		return nil, nil
	}

	result := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		callMap, ok := call.(map[string]any)
		if !ok {
			continue
		}

		bridgeCall := make(map[string]any)
		if name := getStringValue(callMap, "id"); name != "" {
			bridgeCall["name"] = name
		}
		if funcName := getStringValue(callMap, "function.name"); funcName != "" {
			bridgeCall["name"] = funcName
		}
		if args, ok := callMap["function.arguments"].(string); ok {
			bridgeCall["args"] = args
		}

		if len(bridgeCall) >= 2 {
			result = append(result, bridgeCall)
		}
	}
	return result, nil
}

func transformOpenAIMessagesToGeminiContents(messages []any) []any {
	// For Gemini: extract text from all messages into a single content array
	var allParts []any
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		if content, ok := msgMap["content"]; ok {
			parts := transformContentToParts(content)
			allParts = append(allParts, parts...)
		}
	}
	return allParts
}

func transformedContents(transformed []map[string]any) []any {
	// For Gemini: flatten to parts
	var parts []any
	for _, msg := range transformed {
		if p, ok := msg["parts"]; ok {
			if partsList, ok := p.([]any); ok {
				parts = append(parts, partsList...)
			}
		}
	}
	return parts
}

func getStringValue(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}

// BuildAnthropicMessagesBody builds an Anthropic-messages body from OpenAI chat completions.
func BuildAnthropicMessagesBody(clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	model := options.ModelOverride
	if model == "" {
		model = options.DefaultModel
	}

	result := make(map[string]any)
	result["model"] = model

	// Transform messages to Anthropic format
	if messages, ok := clientBody["messages"].([]any); ok && len(messages) > 0 {
		anthropicMessages, err := transformOpenAIMessagesToAnthropicMessages(messages)
		if err != nil {
			return nil, err
		}
		result["messages"] = anthropicMessages
	}

	// Copy other fields
	if maxTokens, ok := clientBody["max_tokens"]; ok {
		result["max_tokens"] = maxTokens
	} else if options.MaxTokens != nil {
		result["max_tokens"] = *options.MaxTokens
	}
	if temperature, ok := clientBody["temperature"]; ok {
		result["temperature"] = temperature
	} else if options.Temperature != nil {
		result["temperature"] = *options.Temperature
	}
	if topP, ok := clientBody["top_p"].(float64); ok && topP > 0 {
		result["top_p"] = topP
	}

	return result, nil
}

func transformOpenAIMessagesToAnthropicMessages(messages []any) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}

		anthropicMsg := make(map[string]any)

		// Convert role
		role := getStringValue(msgMap, "role")
		switch role {
		case "user":
			anthropicMsg["role"] = "user"
		case "assistant":
			anthropicMsg["role"] = "assistant"
		case "system":
			anthropicMsg["role"] = "user"
		case "tool":
			anthropicMsg["role"] = "assistant"
		default:
			anthropicMsg["role"] = role
		}

		// Transform content
		if content, ok := msgMap["content"]; ok {
			contentStr := toString(content)
			if strings.Contains(contentStr, "【") && strings.Contains(contentStr, "】") {
				// May contain code blocks - keep as is
			}
			anthropicMsg["content"] = contentStr
		}

		// Transform tool calls
		if toolCalls, ok := msgMap["tool_calls"]; ok {
			if calls, err := transformToolCallsForAnthropic(toolCalls); err == nil {
				anthropicMsg["tool_use_result"] = calls
			}
		}

		// Transform tool response
		if role == "tool" {
			if content, ok := msgMap["content"].(string); ok {
				anthropicMsg["content"] = content
				anthropicMsg["tool_use_result"] = map[string]any{
					"content": content,
				}
			}
		}

		result = append(result, anthropicMsg)
	}

	return result, nil
}

func transformToolCallsForAnthropic(toolCalls any) ([]map[string]any, error) {
	calls, ok := toolCalls.([]any)
	if !ok {
		return nil, nil
	}

	result := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		callMap, ok := call.(map[string]any)
		if !ok {
			continue
		}

		bridgeCall := make(map[string]any)
		if name := getStringValue(callMap, "function.name"); name != "" {
			bridgeCall["name"] = name
		}
		if args, ok := callMap["function.arguments"]; ok {
			// Parse JSON args
			if argsStr, ok := args.(string); ok {
				var parsedArgs map[string]any
				if err := json.Unmarshal([]byte(argsStr), &parsedArgs); err == nil {
					bridgeCall["input"] = parsedArgs
				}
			}
		}
		if callID := getStringValue(callMap, "id"); callID != "" {
			bridgeCall["id"] = callID
		}

		if len(bridgeCall) >= 2 {
			result = append(result, bridgeCall)
		}
	}
	return result, nil
}