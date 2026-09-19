package openaicompatbridge

import "strings"

// ---------------------------------------------------------------------------
// Codex Responses -> Chat Completions (codex-responses-chat-bridge.ts)
// ---------------------------------------------------------------------------

// BuildCodexResponsesToChatCompletionsBody mirrors buildCodexResponsesChatBridgeBody
// for the function tool surface: custom / apply_patch tools surface the same
// unsupported-tool system message guidance, and tool_choice follows the Node
// mapping (auto / none / required / named function).
func BuildCodexResponsesToChatCompletionsBody(body map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	if err := validateCodexResponsesChatBridgeBody(body); err != nil {
		return nil, err
	}
	model := options.ModelOverride
	if model == "" {
		model = bridgeStringValue(body["model"])
	}
	if model == "" {
		model = options.DefaultModel
	}
	toolPlan := responsesToolsToChatToolPlan(effectiveResponsesToolsForChatBridge(body))
	toolChoice := responsesToolChoiceToChatToolChoice(body["tool_choice"], toolPlan)
	chatBody := map[string]any{
		"model":    model,
		"messages": responsesInputToChatMessages(body, toolPlan),
		"stream":   true,
	}
	if options.Stream != true {
		// Node pins stream: true for this bridge (the upstream chat request is
		// always SSE); options.Stream only mirrors the forced flag.
		chatBody["stream"] = true
	}
	if promptCacheKey := bridgeStringValue(body["prompt_cache_key"]); promptCacheKey != "" {
		chatBody["prompt_cache_key"] = promptCacheKey
	}
	if serviceTier, ok := body["service_tier"].(string); ok && strings.TrimSpace(serviceTier) != "" {
		chatBody["service_tier"] = strings.TrimSpace(serviceTier)
	}
	if reasoning := bridgeObjectValue(body["reasoning"]); reasoning != nil {
		if effort := bridgeStringValue(reasoning["effort"]); effort != "" {
			chatBody["reasoning_effort"] = effort
		}
	}
	if len(toolPlan.chatTools) > 0 {
		chatBody["tools"] = toolPlan.chatTools
		if toolChoice != nil {
			chatBody["tool_choice"] = toolChoice
		}
		if parallel, ok := bridgeBoolValue(body["parallel_tool_calls"]); ok {
			chatBody["parallel_tool_calls"] = parallel
		}
	}
	if maxTokens, ok := bridgeIntegerValue(body["max_output_tokens"]); ok {
		chatBody["max_tokens"] = maxTokens
	} else if maxTokens, ok := bridgeIntegerValue(body["max_completion_tokens"]); ok {
		chatBody["max_tokens"] = maxTokens
	}
	if temperature, ok := bridgeNumberValue(body["temperature"]); ok {
		chatBody["temperature"] = temperature
	}
	if topP, ok := bridgeNumberValue(body["top_p"]); ok {
		chatBody["top_p"] = topP
	}
	if unsupported := unsupportedToolsSystemMessage(toolPlan.unsupportedTools); unsupported != nil {
		messages, _ := bridgeIsArray(chatBody["messages"])
		chatBody["messages"] = append([]any{unsupported}, messages...)
	}
	return chatBody, nil
}

func validateCodexResponsesChatBridgeBody(body map[string]any) error {
	if _, ok := body["input"]; !ok {
		return bridgeValidationError(
			"Codex Responses 到 Chat Completions 桥接要求请求体包含 input",
			"invalid_codex_chat_bridge_input")
	}
	return nil
}

func effectiveResponsesToolsForChatBridge(body map[string]any) []any {
	tools, _ := bridgeIsArray(body["tools"])
	return tools
}

type codexChatToolPlan struct {
	chatTools        []any
	unsupportedTools []string
	namesByChatName  map[string]string
	// adaptersByChatName 携带响应侧身份构造所需（chatName -> kind /
	// responsesName），对齐 Node
	// codexResponsesChatBridgeToolAdaptersByChatName 请求期存储。
	adaptersByChatName map[string]CodexBridgeToolAdapter
}

func responsesToolsToChatToolPlan(tools []any) *codexChatToolPlan {
	plan := &codexChatToolPlan{
		namesByChatName:    map[string]string{},
		adaptersByChatName: map[string]CodexBridgeToolAdapter{},
	}
	used := map[string]bool{}
	for _, item := range tools {
		tool := bridgeObjectValue(item)
		if tool == nil {
			continue
		}
		switch bridgeStringValue(tool["type"]) {
		case "function":
			name := bridgeStringValue(tool["name"])
			if name == "" {
				continue
			}
			chatName := uniqueChatToolName(name, used)
			parameters, ok := tool["parameters"].(map[string]any)
			if !ok {
				parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			plan.chatTools = append(plan.chatTools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        chatName,
					"description": bridgeStringValue(tool["description"]),
					"parameters":  parameters,
				},
			})
			plan.namesByChatName[chatName] = name
			plan.adaptersByChatName[chatName] = CodexBridgeToolAdapter{
				Kind:          "function",
				ChatName:      chatName,
				ResponsesName: name,
			}
		default:
			label := bridgeStringValue(tool["type"])
			if label == "" {
				label = "unknown"
			}
			plan.unsupportedTools = append(plan.unsupportedTools, label)
		}
	}
	return plan
}

// CodexResponsesChatBridgeToolAdaptersFromClientBody rebuilds the chat-name
// tool adapters the response face needs. Node stores the request-time plan on
// the express request (codexResponsesChatBridgeToolAdaptersByChatName); Go
// re-derives the same mapping from the same client body at response time.
func CodexResponsesChatBridgeToolAdaptersFromClientBody(body map[string]any) map[string]CodexBridgeToolAdapter {
	if body == nil {
		return map[string]CodexBridgeToolAdapter{}
	}
	tools, _ := bridgeIsArray(body["tools"])
	return responsesToolsToChatToolPlan(tools).adaptersByChatName
}

func uniqueChatToolName(base string, used map[string]bool) string {
	name := base
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		name = "tool"
	}
	if !used[name] {
		used[name] = true
		return name
	}
	suffix := 2
	for {
		candidate := name + "_" + int64ToText(int64(suffix))
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
		suffix++
	}
}

func responsesToolChoiceToChatToolChoice(value any, plan *codexChatToolPlan) any {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string:
		switch typed {
		case "auto":
			return "auto"
		case "none":
			return "none"
		case "required":
			return "required"
		default:
			return nil
		}
	case map[string]any:
		switch bridgeStringValue(typed["type"]) {
		case "function":
			name := bridgeStringValue(typed["name"])
			if name == "" {
				return nil
			}
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		default:
			return nil
		}
	default:
		return nil
	}
}

func unsupportedToolsSystemMessage(unsupported []string) map[string]any {
	if len(unsupported) == 0 {
		return nil
	}
	return map[string]any{
		"role":    "system",
		"content": "当前请求携带的上游不支持工具将被忽略：" + strings.Join(unsupported, "、"),
	}
}

// responsesInputToChatMessages mirrors responsesInputToChatMessages for the
// message / function_call / function_call_output / reasoning item surface.
func responsesInputToChatMessages(body map[string]any, plan *codexChatToolPlan) []any {
	messages := []any{}
	toolCallIDByName := map[string]string{}
	pendingToolCalls := map[string]bool{}
	input := body["input"]
	switch typed := input.(type) {
	case string:
		messages = append(messages, map[string]any{"role": "user", "content": typed})
	case []any:
		for _, item := range typed {
			record := bridgeObjectValue(item)
			if record == nil {
				continue
			}
			switch bridgeStringValue(record["type"]) {
			case "message":
				if converted := responsesMessageItemAsChatMessage(record); converted != nil {
					messages = append(messages, converted)
				}
			case "function_call":
				name := bridgeStringValue(record["name"])
				callID := firstNonEmpty(bridgeStringValue(record["call_id"]), bridgeStringValue(record["id"]))
				arguments := record["arguments"]
				if arguments == nil {
					arguments = ""
				}
				argsText, _ := arguments.(string)
				messages = append(messages, map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []any{map[string]any{
						"id":   firstNonEmpty(callID, name),
						"type": "function",
						"function": map[string]any{
							"name":      name,
							"arguments": argsText,
						},
					}},
				})
				chatName := name
				for candidate, responsesName := range plan.namesByChatName {
					if responsesName == name {
						chatName = candidate
					}
				}
				if callID != "" {
					toolCallIDByName[name] = callID
					pendingToolCalls[callID] = false
				}
				_ = chatName
			case "function_call_output":
				callID := bridgeStringValue(record["call_id"])
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": callID,
					"content":      responsesTextFromValue(record["output"]),
				})
				pendingToolCalls[callID] = true
			case "reasoning":
				if text := responsesReasoningTextFromItem(record); text != "" {
					messages = append(messages, map[string]any{
						"role":    "user",
						"content": "[reasoning] " + text,
					})
				}
			}
		}
	}
	return coalesceAdjacentSystemMessages(messages)
}

func responsesMessageItemAsChatMessage(item map[string]any) map[string]any {
	role := chatRoleForResponsesRole(item["role"])
	if role == "" {
		return nil
	}
	return map[string]any{"role": role, "content": responsesContentFromValue(item["content"])}
}

func chatRoleForResponsesRole(role any) string {
	switch text, _ := role.(string); text {
	case "user":
		return "user"
	case "assistant":
		return "assistant"
	case "system", "developer":
		return "system"
	default:
		return ""
	}
}

func responsesContentFromValue(value any) any {
	if text, ok := value.(string); ok {
		return text
	}
	if text := responsesTextFromValue(value); text != "" {
		return text
	}
	return ""
}

func coalesceAdjacentSystemMessages(messages []any) []any {
	output := []any{}
	for _, message := range messages {
		messageMap := bridgeObjectValue(message)
		if messageMap == nil {
			continue
		}
		if len(output) > 0 {
			if previous := bridgeObjectValue(output[len(output)-1]); previous != nil &&
				bridgeStringValue(previous["role"]) == "system" && bridgeStringValue(messageMap["role"]) == "system" {
				previous["content"] = appendBridgeText(previous["content"], messageMap["content"])
				continue
			}
		}
		output = append(output, message)
	}
	return output
}

func appendBridgeText(base, next any) any {
	baseText, _ := base.(string)
	nextText, _ := next.(string)
	if baseText == "" {
		return nextText
	}
	if nextText == "" {
		return baseText
	}
	return baseText + "\n\n" + nextText
}
