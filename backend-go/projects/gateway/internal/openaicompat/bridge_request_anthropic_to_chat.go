package openaicompat

import "strings"

// ---------------------------------------------------------------------------
// Anthropic Messages -> Chat Completions (anthropic-openai-chat-bridge.ts)
// ---------------------------------------------------------------------------

// BuildAnthropicMessagesToChatCompletionsBody mirrors
// anthropicMessagesBodyToChatCompletionsBody + validateAnthropicMessagesChatBridgeBody.
func BuildAnthropicMessagesToChatCompletionsBody(body map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	if _, ok := bridgeIsArray(body["messages"]); !ok {
		return nil, bridgeValidationError(
			"Anthropic Messages 到 Chat Completions 桥接要求 messages 是数组",
			"invalid_anthropic_chat_bridge_messages")
	}
	model := options.ModelOverride
	if model == "" {
		model = bridgeStringValue(body["model"])
	}
	if model == "" {
		model = options.DefaultModel
	}
	if err := validateAnthropicMessagesChatBridgeBody(body); err != nil {
		return nil, err
	}
	chatMessages := []any{}
	if err := appendSystemMessages(&chatMessages, body["system"]); err != nil {
		return nil, err
	}
	inputMessages, _ := bridgeIsArray(body["messages"])
	for _, item := range inputMessages {
		message := bridgeObjectValue(item)
		if message == nil {
			continue
		}
		if err := appendAnthropicToChatMessage(&chatMessages, message); err != nil {
			return nil, err
		}
	}

	output := map[string]any{
		"model":    model,
		"messages": chatMessages,
		"stream":   options.Stream,
	}
	if maxTokens, ok := bridgeIntegerValue(body["max_tokens"]); ok {
		output["max_tokens"] = maxTokens
	}
	if temperature, ok := bridgeNumberValue(body["temperature"]); ok {
		output["temperature"] = temperature
	}
	if topP, ok := bridgeNumberValue(body["top_p"]); ok {
		output["top_p"] = topP
	}
	if stop := anthropicStopSequencesToChatStop(body["stop_sequences"]); stop != nil {
		output["stop"] = stop
	}
	if metadata := bridgeObjectValue(body["metadata"]); metadata != nil {
		if user := bridgeStringValue(metadata["user_id"]); user != "" {
			output["user"] = user
		}
	}
	tools, err := anthropicToolsToChatTools(body["tools"])
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		output["tools"] = tools
		choice, err := anthropicToolChoiceToChatToolChoice(body["tool_choice"])
		if err != nil {
			return nil, err
		}
		if choice.value != nil {
			output["tool_choice"] = choice.value
		}
		if choice.parallelToolCalls != nil {
			output["parallel_tool_calls"] = *choice.parallelToolCalls
		}
	}
	return output, nil
}

// validateAnthropicMessagesChatBridgeBody mirrors the same Node validator.
func validateAnthropicMessagesChatBridgeBody(body map[string]any) error {
	for _, field := range []string{"thinking", "container", "context_management", "mcp_servers", "service_tier", "top_k"} {
		if hasOwnKey(body, field) && body[field] != nil {
			return bridgeValidationError(
				"当前 Chat Completions 上游不支持 Anthropic Messages 的 "+field+" 字段。请客户端改用真实支持这些能力的上游，或在本地 agent / MCP 中提供对应能力后再发起请求。",
				"unsupported_anthropic_messages_chat_bridge_fields")
		}
	}
	if hasAnthropicMessagesCacheControl(body) {
		return bridgeValidationError(
			"当前 Chat Completions 上游不能保真承载 Anthropic cache_control。请客户端改用支持 prompt caching 的 Anthropic Messages 上游，或移除 cache_control 后重试。",
			"unsupported_anthropic_messages_cache_control")
	}
	return nil
}

func appendSystemMessages(output *[]any, value any) error {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		if strings.TrimSpace(text) != "" {
			*output = append(*output, map[string]any{"role": "system", "content": text})
		}
		return nil
	}
	items, ok := bridgeIsArray(value)
	if !ok {
		return bridgeValidationError(
			"Anthropic Messages 到 Chat Completions 桥接要求 system 是字符串或 text block 数组",
			"invalid_anthropic_chat_bridge_system")
	}
	parts := []string{}
	for _, item := range items {
		block := bridgeObjectValue(item)
		if block == nil {
			continue
		}
		if bridgeStringValue(block["type"]) != "text" {
			return bridgeValidationError(
				"当前 Chat Completions 上游只支持把 system text block 转换为 system 消息。请客户端移除 system 中的非 text block，或改用支持该 Anthropic 能力的上游。",
				"unsupported_anthropic_messages_system_block")
		}
		if text := bridgeStringValue(block["text"]); text != "" {
			parts = append(parts, text)
		}
	}
	if text := strings.Join(parts, "\n"); text != "" {
		*output = append(*output, map[string]any{"role": "system", "content": text})
	}
	return nil
}

func appendAnthropicToChatMessage(output *[]any, message map[string]any) error {
	role := bridgeStringValue(message["role"])
	if role != "user" && role != "assistant" {
		return bridgeValidationError(
			"Anthropic Messages 到 Chat Completions 桥接只支持 user 和 assistant 消息",
			"invalid_anthropic_chat_bridge_message_role")
	}
	if role == "assistant" {
		converted, err := anthropicAssistantMessageToChatMessage(message)
		if err != nil {
			return err
		}
		*output = append(*output, converted)
		return nil
	}
	return appendAnthropicUserMessage(output, message)
}

func appendAnthropicUserMessage(output *[]any, message map[string]any) error {
	content := message["content"]
	if text, ok := content.(string); ok {
		*output = append(*output, map[string]any{"role": "user", "content": text})
		return nil
	}
	blocks, ok := bridgeIsArray(content)
	if !ok {
		return bridgeValidationError(
			"Anthropic Messages 到 Chat Completions 桥接要求 user content 是字符串或 block 数组",
			"invalid_anthropic_chat_bridge_user_content")
	}
	var pendingParts []any
	flush := func() {
		if len(pendingParts) == 0 {
			return
		}
		*output = append(*output, map[string]any{"role": "user", "content": chatUserContentFromParts(pendingParts)})
		pendingParts = nil
	}
	for _, blockValue := range blocks {
		block := bridgeObjectValue(blockValue)
		if block == nil {
			continue
		}
		switch bridgeStringValue(block["type"]) {
		case "text":
			pendingParts = append(pendingParts, map[string]any{"type": "text", "text": bridgeStringValue(block["text"])})
		case "image":
			imageURL, err := anthropicImageBlockToChatImageURL(block)
			if err != nil {
				return err
			}
			pendingParts = append(pendingParts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": imageURL},
			})
		case "tool_result":
			flush()
			toolUseID := bridgeStringValue(block["tool_use_id"])
			if toolUseID == "" {
				return bridgeValidationError(
					"Anthropic tool_result block 缺少 tool_use_id",
					"invalid_anthropic_chat_bridge_tool_result")
			}
			*output = append(*output, map[string]any{
				"role":         "tool",
				"tool_call_id": toolUseID,
				"content":      anthropicContentText(block["content"]),
			})
		default:
			typeLabel := bridgeStringValue(block["type"])
			if typeLabel == "" {
				typeLabel = "unknown"
			}
			return bridgeValidationError(
				"当前 Chat Completions 上游不支持 Anthropic content block："+typeLabel+"。请客户端改用真实支持该 block 的上游，或在本地 agent 中先转换/执行后再发起请求。",
				"unsupported_anthropic_messages_content_block")
		}
	}
	flush()
	return nil
}

func anthropicImageBlockToChatImageURL(block map[string]any) (string, error) {
	source := bridgeObjectValue(block["source"])
	sourceType := ""
	if source != nil {
		sourceType = bridgeStringValue(source["type"])
	}
	switch sourceType {
	case "base64":
		mediaType := "application/octet-stream"
		if source != nil && bridgeStringValue(source["media_type"]) != "" {
			mediaType = bridgeStringValue(source["media_type"])
		}
		data := ""
		if source != nil {
			data = bridgeStringValue(source["data"])
		}
		if data == "" {
			return "", bridgeValidationError(
				"Anthropic image source 缺少 base64 data",
				"invalid_anthropic_chat_bridge_image")
		}
		return "data:" + mediaType + ";base64," + data, nil
	case "url":
		if source != nil {
			if url := bridgeStringValue(source["url"]); url != "" {
				return url, nil
			}
		}
	}
	return "", bridgeValidationError(
		"当前 Chat Completions 上游只支持 Anthropic base64/url image block 转换。请客户端改用支持该图片 source 的上游，或把图片转成 base64/url 后重试。",
		"unsupported_anthropic_messages_image_source")
}

func anthropicAssistantMessageToChatMessage(message map[string]any) (map[string]any, error) {
	content := message["content"]
	if text, ok := content.(string); ok {
		return map[string]any{"role": "assistant", "content": text}, nil
	}
	blocks, ok := bridgeIsArray(content)
	if !ok {
		return nil, bridgeValidationError(
			"Anthropic Messages 到 Chat Completions 桥接要求 assistant content 是字符串或 block 数组",
			"invalid_anthropic_chat_bridge_assistant_content")
	}
	textParts := []string{}
	toolCalls := []any{}
	for _, blockValue := range blocks {
		block := bridgeObjectValue(blockValue)
		if block == nil {
			continue
		}
		switch bridgeStringValue(block["type"]) {
		case "text":
			textParts = append(textParts, bridgeStringValue(block["text"]))
		case "tool_use":
			name := bridgeStringValue(block["name"])
			id := bridgeStringValue(block["id"])
			if name == "" || id == "" {
				return nil, bridgeValidationError(
					"Anthropic tool_use block 缺少 id 或 name",
					"invalid_anthropic_chat_bridge_tool_use")
			}
			input := block["input"]
			if input == nil {
				input = map[string]any{}
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": bridgeJSONStringify(input),
				},
			})
		case "thinking", "redacted_thinking":
			return nil, bridgeValidationError(
				"当前 Chat Completions 上游不能保真承载 Anthropic thinking block。请客户端改用支持 thinking 的 Anthropic Messages 上游，或移除 thinking 内容后重试。",
				"unsupported_anthropic_messages_thinking_block")
		default:
			typeLabel := bridgeStringValue(block["type"])
			if typeLabel == "" {
				typeLabel = "unknown"
			}
			return nil, bridgeValidationError(
				"当前 Chat Completions 上游不支持 Anthropic content block："+typeLabel+"。请客户端改用真实支持该 block 的上游，或在本地 agent 中先转换/执行后再发起请求。",
				"unsupported_anthropic_messages_content_block")
		}
	}
	joined := strings.Join(textParts, "")
	output := map[string]any{"role": "assistant"}
	if len(toolCalls) > 0 && joined == "" {
		output["content"] = nil
	} else {
		output["content"] = joined
	}
	if len(toolCalls) > 0 {
		output["tool_calls"] = toolCalls
	}
	return output, nil
}

func anthropicToolsToChatTools(value any) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil, bridgeValidationError(
			"Anthropic Messages 到 Chat Completions 桥接要求 tools 是数组",
			"invalid_anthropic_chat_bridge_tools")
	}
	output := []any{}
	for _, item := range items {
		tool := bridgeObjectValue(item)
		if tool == nil {
			continue
		}
		if explicitType := bridgeStringValue(tool["type"]); explicitType != "" && explicitType != "custom" {
			return nil, bridgeValidationError(
				"当前 Chat Completions 上游不支持 Anthropic server tool："+explicitType+"。请客户端配置本地 MCP 或改用真实支持该工具的上游。",
				"unsupported_anthropic_messages_server_tool")
		}
		name := bridgeStringValue(tool["name"])
		if name == "" {
			return nil, bridgeValidationError(
				"Anthropic tool 缺少 name",
				"invalid_anthropic_chat_bridge_tool")
		}
		parameters, ok := tool["input_schema"].(map[string]any)
		if !ok {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		output = append(output, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": bridgeStringValue(tool["description"]),
				"parameters":  parameters,
			},
		})
	}
	return output, nil
}

type anthropicToolChoiceResult struct {
	value             any
	parallelToolCalls *bool
}

func anthropicToolChoiceToChatToolChoice(value any) (anthropicToolChoiceResult, error) {
	if value == nil {
		return anthropicToolChoiceResult{}, nil
	}
	choice, ok := value.(map[string]any)
	if !ok {
		return anthropicToolChoiceResult{}, bridgeValidationError(
			"Anthropic tool_choice 必须是对象",
			"invalid_anthropic_chat_bridge_tool_choice")
	}
	var parallelToolCalls *bool
	if disable, _ := bridgeBoolValue(choice["disable_parallel_tool_use"]); disable {
		flag := false
		parallelToolCalls = &flag
	}
	switch choiceType := bridgeStringValue(choice["type"]); choiceType {
	case "", "auto":
		return anthropicToolChoiceResult{value: "auto", parallelToolCalls: parallelToolCalls}, nil
	case "any":
		return anthropicToolChoiceResult{value: "required", parallelToolCalls: parallelToolCalls}, nil
	case "none":
		return anthropicToolChoiceResult{value: "none", parallelToolCalls: parallelToolCalls}, nil
	case "tool":
		name := bridgeStringValue(choice["name"])
		if name == "" {
			return anthropicToolChoiceResult{}, bridgeValidationError(
				"Anthropic tool_choice.type=tool 缺少 name",
				"invalid_anthropic_chat_bridge_tool_choice")
		}
		return anthropicToolChoiceResult{
			value:             map[string]any{"type": "function", "function": map[string]any{"name": name}},
			parallelToolCalls: parallelToolCalls,
		}, nil
	default:
		return anthropicToolChoiceResult{}, bridgeValidationError(
			"当前 Chat Completions 上游不支持 Anthropic tool_choice："+choiceType+"。请客户端改用 auto/any/tool/none，或选择支持该工具选择能力的上游。",
			"unsupported_anthropic_messages_tool_choice")
	}
}

func anthropicStopSequencesToChatStop(value any) any {
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil
	}
	stops := []string{}
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			stops = append(stops, text)
		}
	}
	if len(stops) == 0 {
		return nil
	}
	if len(stops) == 1 {
		return stops[0]
	}
	return stops
}

// chatUserContentFromParts mirrors chatUserContentFromParts.
func chatUserContentFromParts(parts []any) any {
	hasNonText := false
	for _, part := range parts {
		if partMap := bridgeObjectValue(part); partMap != nil && bridgeStringValue(partMap["type"]) != "text" {
			hasNonText = true
			break
		}
	}
	if hasNonText {
		return parts
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		text := ""
		if partMap := bridgeObjectValue(part); partMap != nil {
			text = bridgeStringValue(partMap["text"])
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, "")
}

// anthropicContentText mirrors anthropicContentText.
func anthropicContentText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	items, ok := bridgeIsArray(value)
	if !ok {
		return ""
	}
	parts := []string{}
	for _, item := range items {
		block := bridgeObjectValue(item)
		if block == nil {
			continue
		}
		if bridgeStringValue(block["type"]) == "text" {
			if text := bridgeStringValue(block["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// hasAnthropicMessagesCacheControl mirrors hasAnthropicMessagesCacheControl.
func hasAnthropicMessagesCacheControl(body map[string]any) bool {
	if contentBlocksHaveCacheControl(body["system"]) {
		return true
	}
	if tools, ok := bridgeIsArray(body["tools"]); ok {
		for _, tool := range tools {
			if toolMap := bridgeObjectValue(tool); toolMap != nil && hasOwnKey(toolMap, "cache_control") {
				return true
			}
		}
	}
	messages, ok := bridgeIsArray(body["messages"])
	if !ok {
		return false
	}
	for _, item := range messages {
		message := bridgeObjectValue(item)
		if message == nil {
			continue
		}
		if hasOwnKey(message, "cache_control") || contentBlocksHaveCacheControl(message["content"]) {
			return true
		}
	}
	return false
}

func contentBlocksHaveCacheControl(value any) bool {
	items, ok := bridgeIsArray(value)
	if !ok {
		return false
	}
	for _, item := range items {
		block := bridgeObjectValue(item)
		if block == nil {
			continue
		}
		if hasOwnKey(block, "cache_control") || contentBlocksHaveCacheControl(block["content"]) {
			return true
		}
	}
	return false
}
