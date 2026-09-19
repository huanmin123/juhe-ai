package openaicompat

import "strings"

// ---------------------------------------------------------------------------
// OpenAI Responses -> Anthropic Messages (responsesBodyToAnthropicMessages)
// ---------------------------------------------------------------------------

// BuildOpenAIResponsesToAnthropicMessagesBody mirrors
// responsesBodyToAnthropicMessages.
func BuildOpenAIResponsesToAnthropicMessagesBody(clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	model := options.ModelOverride
	if model == "" {
		model = bridgeStringValue(clientBody["model"])
	}
	if model == "" {
		return nil, bridgeValidationError("OpenAI 到 Anthropic 桥接请求缺少 model", "openai_anthropic_bridge_missing_model")
	}
	if instructions := bridgeStringValue(clientBody["previous_response_id"]); instructions != "" {
		return nil, bridgeValidationError(
			"previous_response_id 尚未被网关上下文状态层恢复，不能直接转发到 Anthropic Messages",
			"openai_anthropic_bridge_previous_response_state_unavailable")
	}
	if err := validateOpenAIToAnthropicReasoningOptions(clientBody); err != nil {
		return nil, err
	}

	messages := []any{}
	systemParts := []string{}
	appendSystemText(&systemParts, bridgeStringValue(clientBody["instructions"]))
	toolHistory := newOpenAIToolResultHistory()
	appendResponsesInput(&messages, &systemParts, clientBody["input"], &options, toolHistory)
	if len(messages) == 0 {
		messages = appendAnthropicMessage(messages, map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": ""}},
		})
	}

	output := baseAnthropicBody(clientBody, model, options)
	output["messages"] = messages
	if system := strings.TrimSpace(strings.Join(systemParts, "\n\n")); system != "" {
		output["system"] = system
	}
	tools, degradedHostedTools, err := responsesToolsToAnthropicTools(clientBody["tools"], options.HostedToolModes)
	if err != nil {
		return nil, err
	}
	choice := anthropicToolChoiceFromOpenAI(clientBody["tool_choice"], hasOwnKey(clientBody, "tool_choice"))
	applyTools(output, tools, choice, clientBody["parallel_tool_calls"] == false)
	// Hosted tools degraded by the runtime registry surface the internal
	// capability constraint through the system surface (Node
	// appendUnsupportedHostedToolConstraint).
	if constraint := AppendUnsupportedHostedToolConstraintText(degradedHostedTools); constraint != "" {
		appendSystemText(&systemParts, constraint)
		if system := strings.TrimSpace(strings.Join(systemParts, "\n\n")); system != "" {
			output["system"] = system
		}
	}
	if err := validateAnthropicThinkingToolChoiceCompatibility(output); err != nil {
		return nil, err
	}
	return output, nil
}

func appendResponsesInput(messages *[]any, systemParts *[]string, input any, options *BridgeRequestBodyOptions, toolHistory *openAIToolResultHistory) {
	switch typed := input.(type) {
	case string:
		*messages = appendAnthropicMessage(*messages, map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": typed}},
		})
	case []any:
		for _, item := range typed {
			appendResponsesInputItemAsAnthropicMessage(messages, systemParts, item, options, toolHistory)
		}
	case map[string]any:
		appendResponsesInputItemAsAnthropicMessage(messages, systemParts, typed, options, toolHistory)
	}
}

// appendResponsesInputItemAsAnthropicMessage mirrors the same Node helper.
func appendResponsesInputItemAsAnthropicMessage(messages *[]any, systemParts *[]string, item any, options *BridgeRequestBodyOptions, toolHistory *openAIToolResultHistory) {
	record := bridgeObjectValue(item)
	if record == nil {
		return
	}
	switch bridgeStringValue(record["type"]) {
	case "message":
		role := bridgeStringValue(record["role"])
		switch role {
		case "system", "developer":
			appendSystemText(systemParts, responsesContentToText(record["content"]))
			return
		case "user", "assistant":
			blocks, err := responsesContentToAnthropicBlocks(record["content"], options)
			if err != nil {
				return
			}
			*messages = appendAnthropicMessage(*messages, map[string]any{"role": role, "content": blocks})
		}
	case "function_call":
		name := bridgeStringValue(record["name"])
		callID := firstNonEmpty(bridgeStringValue(record["call_id"]), bridgeStringValue(record["id"]))
		if name == "" || callID == "" {
			return
		}
		*messages = appendAnthropicMessage(*messages, map[string]any{
			"role": "assistant",
			"content": []any{map[string]any{
				"type":  "tool_use",
				"id":    callID,
				"name":  name,
				"input": anthropicToolInputFromOpenAIArguments(record["arguments"]),
			}},
		})
		rememberOpenAIToolCall(toolHistory, callID)
	case "function_call_output":
		callID, err := validateOpenAIToolResultHistory(toolHistory, bridgeStringValue(record["call_id"]))
		if err != nil {
			return
		}
		*messages = appendAnthropicMessage(*messages, map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type":        "tool_result",
				"tool_use_id": callID,
				"content":     responsesTextFromValue(record["output"]),
			}},
		})
	case "reasoning":
		if err := validateResponsesReasoningInputItem(record); err != nil {
			return
		}
		if text := responsesReasoningTextFromItem(record); text != "" {
			appendSystemText(systemParts, "历史推理摘要：\n"+text)
		}
	case "compaction", "compaction_summary":
		if summary := responsesCompactionSummaryTextFromItem(record); summary != "" {
			appendSystemText(systemParts, "上下文摘要：\n"+summary)
		}
	}
}

// responsesContentToAnthropicBlocks mirrors responsesContentToAnthropicBlocks.
func responsesContentToAnthropicBlocks(value any, options *BridgeRequestBodyOptions) ([]any, error) {
	if text, ok := value.(string); ok {
		return []any{map[string]any{"type": "text", "text": text}}, nil
	}
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil, nil
	}
	blocks := []any{}
	for _, item := range items {
		part := bridgeObjectValue(item)
		if part == nil {
			continue
		}
		switch bridgeStringValue(part["type"]) {
		case "", "text", "input_text", "output_text":
			if text := bridgeStringValue(part["text"]); text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
		case "input_image":
			imageURL := bridgeStringValue(part["image_url"])
			if imageURL != "" {
				block, err := anthropicImageBlockFromURL(imageURL)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, block)
				continue
			}
			if fileID := bridgeStringValue(part["file_id"]); fileID != "" {
				block, err := anthropicDocumentBlockFromOpenAIFileID(fileID, options)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, block)
				continue
			}
			return nil, bridgeValidationError(
				"Responses input_image 缺少 image_url 或 file_id",
				"openai_anthropic_bridge_invalid_image_input")
		case "input_file", "file":
			block, err := anthropicDocumentBlockFromOpenAIFilePart(part, options)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		case "input_audio", "audio":
			return nil, bridgeValidationError(
				"Responses input_audio 当前不能桥接到 Anthropic Messages；请使用可消费音频输入的原生 OpenAI 上游，或先在客户端 / 本地运行时转写为文本",
				"openai_anthropic_bridge_audio_input_unsupported")
		default:
			typeLabel := bridgeStringValue(part["type"])
			if typeLabel == "" {
				typeLabel = "unknown"
			}
			return nil, bridgeValidationError(
				"Responses content block "+typeLabel+" 当前没有 OpenAI 到 Anthropic Messages 的等价映射；请先补能力矩阵和 mock 回归后再启用",
				"openai_anthropic_bridge_unsupported_content_part")
		}
	}
	return blocks, nil
}

func validateResponsesReasoningInputItem(item map[string]any) error {
	if value, ok := item["encrypted_content"]; ok && value != nil && value != "" {
		return bridgeValidationError(
			"OpenAI 到 Anthropic 桥接不能恢复或验证历史 reasoning.encrypted_content；请移除该 reasoning item、提供可读 summary/content，或改用原生 Responses 上游",
			"openai_anthropic_bridge_encrypted_reasoning_input_unsupported")
	}
	return nil
}

func responsesReasoningTextFromItem(item map[string]any) string {
	if summary, ok := item["summary"].(string); ok && strings.TrimSpace(summary) != "" {
		return summary
	}
	if content := responsesTextFromValue(item["content"]); content != "" {
		return content
	}
	return strings.TrimSpace(bridgeStringValue(item["text"]))
}

func responsesCompactionSummaryTextFromItem(item map[string]any) string {
	if summary := responsesTextFromValue(item["summary"]); summary != "" {
		return summary
	}
	return responsesTextFromValue(item["content"])
}

// responsesTextFromValue mirrors responsesTextFromValue.
func responsesTextFromValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if object := bridgeObjectValue(value); object != nil {
		return bridgeStringValue(object["text"])
	}
	items, ok := bridgeIsArray(value)
	if !ok {
		return ""
	}
	parts := []string{}
	for _, item := range items {
		if object := bridgeObjectValue(item); object != nil {
			if text := bridgeStringValue(object["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func responsesContentToText(value any) string {
	return responsesTextFromValue(value)
}

// responsesToolsToAnthropicTools mirrors responsesToolsToAnthropicTools with
// the hosted tool runtime registry (openai-hosted-tool-runtime-registry.ts):
// function tools map through; hosted tools resolve through
// ResolveOpenAIHostedToolRuntimeDecision — reject (or unknown types) surface
// the Node guidance error, every other mode degrades to the unsupported
// hosted tool system constraint (Node skips the tool and appends
// appendUnsupportedHostedToolConstraint). The mock / local_runtime execution
// loops stay behind the executor slices; the registry decision keeps the
// request alive either way.
func responsesToolsToAnthropicTools(value any, modes OpenAIHostedToolRuntimeModes) ([]any, []string, error) {
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil, nil, nil
	}
	tools := []any{}
	degradedHostedTools := []string{}
	for _, item := range items {
		tool := bridgeObjectValue(item)
		if tool == nil {
			continue
		}
		toolType := bridgeStringValue(tool["type"])
		if toolType != "function" {
			if toolType == "" && tool["name"] != nil {
				// Legacy shapeless function definitions keep the function path.
				toolType = "function"
			} else {
				label, degraded := openAIHostedToolBridgeDecision(toolType, modes)
				if degraded {
					degradedHostedTools = append(degradedHostedTools, label)
					continue
				}
				return nil, nil, unsupportedOpenAIToolError(tool)
			}
		}
		name := bridgeStringValue(tool["name"])
		if name == "" {
			return nil, nil, bridgeValidationError("function tool 缺少 name", "openai_anthropic_bridge_invalid_tool")
		}
		parameters := tool["parameters"]
		description := tool["description"]
		strict, _ := bridgeBoolValue(tool["strict"])
		anthropicTool := anthropicToolFromFunctionDefinition(name, description, parameters)
		if strict {
			anthropicTool["strict"] = true
		}
		tools = append(tools, anthropicTool)
	}
	return tools, degradedHostedTools, nil
}

// openAIHostedToolBridgeDecision mirrors unsupportedOpenAIHostedToolLabel:
// hosted registry types degrade with their label unless the decision is a
// reject; unknown (non-registry) types fall back to the raw type label as the
// degraded tool (Node returns the type when no runtime decision resolves).
func openAIHostedToolBridgeDecision(toolType string, modes OpenAIHostedToolRuntimeModes) (string, bool) {
	decision, hosted := ResolveOpenAIHostedToolRuntimeDecision(toolType, "", modes)
	if !hosted {
		// 非注册表类型（web_search 等）：Node 无 decision 时返回原始 type
		// label，降级进 guidance 约束。
		return toolType, true
	}
	if decision.Mode == OpenAIHostedToolModeReject {
		return "", false
	}
	return string(decision.ToolType), true
}

// ---------------------------------------------------------------------------
// Shared message / history helpers (openai-anthropic-bridge.ts)
// ---------------------------------------------------------------------------

type openAIToolResultHistory struct {
	toolCallIDs          map[string]bool
	completedToolCallIDs map[string]bool
}

func newOpenAIToolResultHistory() *openAIToolResultHistory {
	return &openAIToolResultHistory{
		toolCallIDs:          map[string]bool{},
		completedToolCallIDs: map[string]bool{},
	}
}

func rememberAnthropicToolUseBlocks(history *openAIToolResultHistory, blocks []any) {
	for _, block := range blocks {
		blockMap := bridgeObjectValue(block)
		if blockMap == nil || bridgeStringValue(blockMap["type"]) != "tool_use" {
			continue
		}
		if id := bridgeStringValue(blockMap["id"]); id != "" {
			rememberOpenAIToolCall(history, id)
		}
	}
}

func rememberOpenAIToolCall(history *openAIToolResultHistory, callID string) {
	history.toolCallIDs[callID] = true
}

func validateOpenAIToolResultHistory(history *openAIToolResultHistory, callID string) (string, error) {
	if callID == "" {
		return "", bridgeValidationError(
			"Chat role=tool 缺少 tool_call_id，无法匹配前文 assistant tool_call",
			"openai_anthropic_bridge_tool_result_missing_call_id")
	}
	if !history.toolCallIDs[callID] {
		return "", bridgeValidationError(
			"Chat role=tool 的 tool_call_id "+callID+" 未匹配任何前文 assistant tool_call",
			"openai_anthropic_bridge_orphan_tool_result")
	}
	if history.completedToolCallIDs[callID] {
		return "", bridgeValidationError(
			"Chat role=tool 的 tool_call_id "+callID+" 已经返回过工具结果",
			"openai_anthropic_bridge_duplicate_tool_result")
	}
	history.completedToolCallIDs[callID] = true
	return callID, nil
}
