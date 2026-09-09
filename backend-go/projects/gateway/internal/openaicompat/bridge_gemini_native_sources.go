package openaicompat

import (
	"encoding/json"
	"regexp"
	"strings"
)

// B-4 gemini-native-target bridge request sources (D-149/D-157). Faithful port
// of the archived Node conversion core in
// providers/drivers/_shared/openai-anthropic-gemini-native-bridge.ts for the
// two sources the Go revision was still rejecting outright:
//
//	responsesBodyToGemini          (input string/array, instructions system,
//	                               function_call / function_call_output items,
//	                               reasoning rejection, tools whitelist)
//	anthropicMessagesBodyToGemini  (system text blocks, content blocks,
//	                               thinking / cache_control rejection,
//	                               base64 / URL image sources, tools)
//
// The chat_completions source stays on the already-ported
// BuildOpenAIChatToGeminiBody (bridge_request.go). validateCommonUnsupported-
// Fields guidance rejects surface the Node GatewayAgentGuidanceResponse
// semantics (200 agent_guidance) through BridgeGuidanceError; the structural
// GatewayRequestValidationError rejects stay bridgeValidationError (400
// invalid_request_error). Error codes and Chinese copy are verbatim against
// the archive, including the providerLabel(providerName) interpolation.

// BridgeGuidanceError carries the Node GatewayAgentGuidanceResponse payload
// (request/validation-error.ts: 200 agent_guidance, account-scoped) for the
// gemini-native-target builders. openaicompat cannot import gatewaypreauth
// (the gatewaypreauth -> gatewayopenai -> openaicompat edge closes a cycle),
// so the composition / dispatch layer converts this error onto
// gatewaypreauth.GatewayAgentGuidanceResponse where the engine's errors.As
// recognition lives.
type BridgeGuidanceError struct {
	Message  string
	Code     string
	Protocol GeminiNativeDownstreamProtocol
	Stream   bool
	Model    string
}

// Error implements error with the verbatim message.
func (e *BridgeGuidanceError) Error() string { return e.Message }

// geminiNativeGuidanceError mirrors guidance(req, mapping, message, code): the
// agent_guidance payload carrying the downstream protocol, the effective
// stream flag and the source model. The builder entries prime the guidance
// context on the options bag before any child conversion runs.
func geminiNativeGuidanceError(options BridgeRequestBodyOptions, message, code string) *BridgeGuidanceError {
	return &BridgeGuidanceError{
		Message:  message,
		Code:     code,
		Protocol: GeminiNativeDownstreamProtocolForMapping(options.geminiNativeSourceFamily),
		Stream:   options.Stream,
		Model:    options.geminiNativeSourceModel,
	}
}

// primeGeminiNativeGuidanceContext records the downstream protocol family and
// the source model (mapping.sourceModel: the client-requested model before the
// mapping rewrite) for the guidance payloads.
func primeGeminiNativeGuidanceContext(options *BridgeRequestBodyOptions, sourceFamily string, clientBody map[string]any) {
	options.geminiNativeSourceFamily = sourceFamily
	options.geminiNativeSourceModel = bridgeStringValue(clientBody["model"])
}

// geminiNativePresentFields mirrors
// `['a','b'].filter((field) => body[field] !== undefined && body[field] !== null)`.
func geminiNativePresentFields(body map[string]any, fields ...string) []string {
	present := []string{}
	for _, field := range fields {
		if value, ok := body[field]; ok && value != nil {
			present = append(present, field)
		}
	}
	return present
}

// validateGeminiNativeOpenAIRequestControls mirrors the chat/responses half of
// validateCommonUnsupportedFields (code
// unsupported_openai_request_controls_for_gemini_native).
func validateGeminiNativeOpenAIRequestControls(clientBody map[string]any, options BridgeRequestBodyOptions) error {
	protectedFields := geminiNativePresentFields(clientBody, "service_tier", "reasoning", "reasoning_effort", "thinking")
	if len(protectedFields) > 0 {
		return geminiNativeGuidanceError(options,
			"Gemini native 上游不能保真映射 OpenAI 的 "+strings.Join(protectedFields, "、")+" 字段。请移除这些请求控制，或改用支持对应字段的原生上游。",
			"unsupported_openai_request_controls_for_gemini_native")
	}
	return nil
}

// validateGeminiNativeResponsesStateControls mirrors the responses half of
// validateCommonUnsupportedFields: the previous_response_id /
// context_management state chain and the truncation setting (both checks are
// `!== undefined`, so JSON null rejects as well).
func validateGeminiNativeResponsesStateControls(clientBody map[string]any, options BridgeRequestBodyOptions) error {
	_, hasPrevious := clientBody["previous_response_id"]
	_, hasContextManagement := clientBody["context_management"]
	if hasPrevious || hasContextManagement {
		return geminiNativeGuidanceError(options,
			"Gemini native 上游不能保真承载 OpenAI Responses 的 previous_response_id / context_management 状态链。请改用真实 Responses 上游，或移除状态链字段后重试。",
			"unsupported_responses_state_for_gemini_native")
	}
	if truncation, has := clientBody["truncation"]; has {
		if text, ok := truncation.(string); !ok || (text != "disabled" && text != "auto") {
			return geminiNativeGuidanceError(options,
				"Gemini native 上游不能保真承载当前 Responses truncation 设置。请改用真实 Responses 上游，或移除该字段后重试。",
				"unsupported_responses_truncation_for_gemini_native")
		}
	}
	return nil
}

// validateGeminiNativeAnthropicStateControls mirrors the anthropic half of
// validateCommonUnsupportedFields (thinking / cache_control).
func validateGeminiNativeAnthropicStateControls(clientBody map[string]any, options BridgeRequestBodyOptions) error {
	_, hasThinking := clientBody["thinking"]
	_, hasCacheControl := clientBody["cache_control"]
	if hasThinking || hasCacheControl {
		return geminiNativeGuidanceError(options,
			"当前"+providerLabel(options.GuidanceProviderName)+" Gemini native 上游不能保真承载 Anthropic thinking / cache_control。请改用真实 Anthropic Messages 上游，或移除该能力后重试。",
			"unsupported_anthropic_state_for_gemini_native")
	}
	return nil
}

// ---------------------------------------------------------------------------
// OpenAI Responses -> Gemini GenerateContent
// ---------------------------------------------------------------------------

// BuildOpenAIResponsesToGeminiBody mirrors responsesBodyToGemini +
// validateCommonUnsupportedFields (responses half): input string/array,
// instructions system, function_call / function_call_output items, reasoning
// rejection, the responses function-tool whitelist and the shared
// finalizeGeminiBody / generationConfig surface.
func BuildOpenAIResponsesToGeminiBody(clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	primeGeminiNativeGuidanceContext(&options, FamilyResponses, clientBody)
	if err := validateGeminiNativeOpenAIRequestControls(clientBody, options); err != nil {
		return nil, err
	}
	if err := validateGeminiNativeResponsesStateControls(clientBody, options); err != nil {
		return nil, err
	}
	systemTexts := []string{}
	if instructions, ok := clientBody["instructions"].(string); ok && strings.TrimSpace(instructions) != "" {
		systemTexts = append(systemTexts, instructions)
	}
	contents := []any{}
	switch typed := clientBody["input"].(type) {
	case string:
		contents = append(contents, map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": typed}},
		})
	case []any:
		for _, value := range typed {
			item := bridgeObjectValue(value)
			if item == nil {
				continue
			}
			if err := appendResponsesInputItemToGemini(&contents, item, options); err != nil {
				return nil, err
			}
		}
	default:
		return nil, bridgeValidationError(
			"Responses 到 Gemini native 桥接要求 input 是字符串或数组",
			"invalid_responses_gemini_bridge_input")
	}
	tools, err := responsesToolsToGeminiTools(clientBody["tools"], options)
	if err != nil {
		return nil, err
	}
	return finalizeGeminiNativeBody(clientBody, options, contents, strings.Join(systemTexts, "\n"), tools)
}

// appendResponsesInputItemToGemini mirrors appendResponsesInputItem.
func appendResponsesInputItemToGemini(output *[]any, item map[string]any, options BridgeRequestBodyOptions) error {
	itemType := bridgeStringValue(item["type"])
	if itemType == "message" || item["role"] != nil {
		role := "user"
		if bridgeStringValue(item["role"]) == "assistant" {
			role = "model"
		}
		parts, err := openAIContentToGeminiPartsForGeminiNative(item["content"], options)
		if err != nil {
			return err
		}
		if len(parts) > 0 {
			*output = append(*output, map[string]any{"role": role, "parts": parts})
		}
		return nil
	}
	switch itemType {
	case "function_call":
		name, ok := item["name"].(string)
		if !ok {
			name = "function_call"
		}
		*output = append(*output, map[string]any{
			"role": "model",
			"parts": []any{map[string]any{
				"functionCall": map[string]any{
					"name": name,
					"args": geminiJSONObjectFromUnknown(item["arguments"]),
				},
			}},
		})
	case "function_call_output":
		name, ok := item["name"].(string)
		if !ok {
			if callID, okCallID := item["call_id"].(string); okCallID {
				name = callID
			} else {
				name = "function_result"
			}
		}
		*output = append(*output, map[string]any{
			"role":  "user",
			"parts": []any{geminiFunctionResponsePart(name, item["output"])},
		})
	case "reasoning":
		return geminiNativeGuidanceError(options,
			"当前"+providerLabel(options.GuidanceProviderName)+" Gemini native 上游不能保真承载 Responses reasoning item。请改用真实 Responses 上游，或移除 reasoning item 后重试。",
			"unsupported_responses_reasoning_for_gemini_native")
	}
	return nil
}

// openAIContentToGeminiPartsForGeminiNative mirrors openAIContentToGeminiParts.
func openAIContentToGeminiPartsForGeminiNative(value any, options BridgeRequestBodyOptions) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return nil, nil
		}
		return []any{map[string]any{"text": text}}, nil
	}
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil, bridgeValidationError("OpenAI content 必须是字符串或数组", "invalid_openai_gemini_bridge_content")
	}
	parts := []any{}
	for _, raw := range items {
		item := bridgeObjectValue(raw)
		if item == nil {
			continue
		}
		switch bridgeStringValue(item["type"]) {
		case "text", "input_text", "output_text":
			if text, ok := item["text"].(string); ok && text != "" {
				parts = append(parts, map[string]any{"text": text})
			}
		case "image_url":
			if imageUrl := bridgeObjectValue(item["image_url"]); imageUrl != nil {
				if url, ok := imageUrl["url"].(string); ok && url != "" {
					part, err := imageUrlToGeminiPartForGeminiNative(url, options)
					if err != nil {
						return nil, err
					}
					parts = append(parts, part)
				}
			}
		case "input_image":
			url, ok := item["image_url"].(string)
			if !ok {
				url, _ = item["file_id"].(string)
			}
			if url != "" {
				part, err := imageUrlToGeminiPartForGeminiNative(url, options)
				if err != nil {
					return nil, err
				}
				parts = append(parts, part)
			}
		case "refusal":
			// Node skips refusal parts in place.
		default:
			partType := bridgeStringValue(item["type"])
			if partType == "" {
				partType = "unknown"
			}
			return nil, geminiNativeGuidanceError(options,
				"当前"+providerLabel(options.GuidanceProviderName)+" Gemini native 上游不能保真承载 OpenAI content part: "+partType+"。请移除该 part 后重试。",
				"unsupported_openai_content_part_for_gemini_native")
		}
	}
	return parts, nil
}

// responsesToolsToGeminiTools mirrors responsesToolsToGeminiTools.
func responsesToolsToGeminiTools(value any, options BridgeRequestBodyOptions) ([]any, error) {
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil, nil
	}
	output := []any{}
	for _, raw := range items {
		tool := bridgeObjectValue(raw)
		if tool == nil {
			continue
		}
		toolType := bridgeStringValue(tool["type"])
		if toolType != "function" {
			if toolType == "" {
				toolType = "unknown"
			}
			return nil, geminiNativeGuidanceError(options,
				"当前"+providerLabel(options.GuidanceProviderName)+" Gemini native 上游只支持 Responses function tool。请改用本地可执行工具运行时或移除 "+toolType+" tool 后重试。",
				"unsupported_responses_tool_for_gemini_native")
		}
		if declaration := functionDeclarationFromOpenAIToolForGeminiNative(tool); declaration != nil {
			output = append(output, declaration)
		}
	}
	return output, nil
}

// functionDeclarationFromOpenAIToolForGeminiNative mirrors
// functionDeclarationFromOpenAITool.
func functionDeclarationFromOpenAIToolForGeminiNative(value map[string]any) map[string]any {
	name := bridgeStringValue(value["name"])
	if name == "" {
		return nil
	}
	parameters, ok := value["parameters"].(map[string]any)
	if !ok {
		parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{
		"name":        name,
		"description": bridgeStringValue(value["description"]),
		"parameters":  parameters,
	}
}

// ---------------------------------------------------------------------------
// Anthropic Messages -> Gemini GenerateContent
// ---------------------------------------------------------------------------

// BuildAnthropicMessagesToGeminiBody mirrors anthropicMessagesBodyToGemini +
// validateCommonUnsupportedFields (anthropic half): system text blocks,
// messages content blocks (text / image / tool_use / tool_result),
// thinking / cache_control rejection and the anthropic tool surface.
func BuildAnthropicMessagesToGeminiBody(clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	primeGeminiNativeGuidanceContext(&options, FamilyAnthropicMessages, clientBody)
	if err := validateGeminiNativeAnthropicStateControls(clientBody, options); err != nil {
		return nil, err
	}
	messages, ok := bridgeIsArray(clientBody["messages"])
	if !ok {
		return nil, bridgeValidationError(
			"Anthropic Messages 到 Gemini native 桥接要求 messages 是数组",
			"invalid_messages_gemini_bridge_messages")
	}
	contents := []any{}
	for _, value := range messages {
		message := bridgeObjectValue(value)
		if message == nil {
			continue
		}
		role := "user"
		if bridgeStringValue(message["role"]) == "assistant" {
			role = "model"
		}
		parts, err := anthropicContentToGeminiPartsForGeminiNative(message["content"], options)
		if err != nil {
			return nil, err
		}
		if len(parts) > 0 {
			contents = append(contents, map[string]any{"role": role, "parts": parts})
		}
	}
	system, err := anthropicSystemToTextForGeminiNative(clientBody["system"], options)
	if err != nil {
		return nil, err
	}
	tools := anthropicToolsToGeminiToolsForGeminiNative(clientBody["tools"])
	return finalizeGeminiNativeBody(clientBody, options, contents, system, tools)
}

// anthropicContentToGeminiPartsForGeminiNative mirrors anthropicContentToGeminiParts.
func anthropicContentToGeminiPartsForGeminiNative(value any, options BridgeRequestBodyOptions) ([]any, error) {
	if text, ok := value.(string); ok {
		if text == "" {
			return nil, nil
		}
		return []any{map[string]any{"text": text}}, nil
	}
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil, nil
	}
	parts := []any{}
	for _, raw := range items {
		item := bridgeObjectValue(raw)
		if item == nil {
			continue
		}
		switch bridgeStringValue(item["type"]) {
		case "text":
			if text, ok := item["text"].(string); ok && text != "" {
				parts = append(parts, map[string]any{"text": text})
			}
		case "image":
			if source := bridgeObjectValue(item["source"]); source != nil {
				part, err := anthropicImageSourceToGeminiPartForGeminiNative(source, options)
				if err != nil {
					return nil, err
				}
				parts = append(parts, part)
			}
		case "tool_use":
			name, ok := item["name"].(string)
			if !ok {
				name = "tool_use"
			}
			parts = append(parts, map[string]any{
				"functionCall": map[string]any{
					"name": name,
					"args": geminiJSONObjectFromUnknown(item["input"]),
				},
			})
		case "tool_result":
			name, ok := item["name"].(string)
			if !ok {
				if toolUseID, okToolUseID := item["tool_use_id"].(string); okToolUseID {
					name = toolUseID
				} else {
					name = "tool_result"
				}
			}
			parts = append(parts, geminiFunctionResponsePart(name, item["content"]))
		default:
			blockType := bridgeStringValue(item["type"])
			if blockType == "" {
				blockType = "unknown"
			}
			return nil, geminiNativeGuidanceError(options,
				"当前"+providerLabel(options.GuidanceProviderName)+" Gemini native 上游不能保真承载 Anthropic content block: "+blockType+"。请移除该 block 后重试。",
				"unsupported_anthropic_content_part_for_gemini_native")
		}
	}
	return parts, nil
}

// anthropicSystemToTextForGeminiNative mirrors anthropicSystemToText: an empty
// string renders the Node undefined (no system instruction).
func anthropicSystemToTextForGeminiNative(value any, options BridgeRequestBodyOptions) (string, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	items, ok := bridgeIsArray(value)
	if !ok {
		return "", nil
	}
	texts := []string{}
	for _, raw := range items {
		item := bridgeObjectValue(raw)
		if item == nil {
			continue
		}
		if bridgeStringValue(item["type"]) != "text" {
			return "", geminiNativeGuidanceError(options,
				"当前"+providerLabel(options.GuidanceProviderName)+" Gemini native 上游只支持 Anthropic system text block。请移除非 text block 后重试。",
				"unsupported_anthropic_system_part_for_gemini_native")
		}
		if text, ok := item["text"].(string); ok && text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n"), nil
}

// anthropicToolsToGeminiToolsForGeminiNative mirrors anthropicToolsToGeminiTools.
func anthropicToolsToGeminiToolsForGeminiNative(value any) []any {
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil
	}
	output := []any{}
	for _, raw := range items {
		tool := bridgeObjectValue(raw)
		if tool == nil {
			continue
		}
		name := bridgeStringValue(tool["name"])
		if name == "" {
			continue
		}
		parameters, ok := tool["input_schema"].(map[string]any)
		if !ok {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		output = append(output, map[string]any{
			"name":        name,
			"description": bridgeStringValue(tool["description"]),
			"parameters":  parameters,
		})
	}
	return output
}

// anthropicImageSourceToGeminiPartForGeminiNative mirrors
// anthropicImageSourceToGeminiPart.
func anthropicImageSourceToGeminiPartForGeminiNative(source map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	switch bridgeStringValue(source["type"]) {
	case "base64":
		mediaType := bridgeStringValue(source["media_type"])
		if mediaType == "" {
			mediaType = "image/png"
		}
		if data := bridgeStringValue(source["data"]); data != "" {
			return map[string]any{
				"inlineData": map[string]any{
					"mimeType": mediaType,
					"data":     data,
				},
			}, nil
		}
	case "url":
		if url := bridgeStringValue(source["url"]); url != "" {
			return imageUrlToGeminiPartForGeminiNative(url, options)
		}
	}
	return nil, geminiNativeGuidanceError(options,
		"当前"+providerLabel(options.GuidanceProviderName)+" Gemini native 上游不能保真承载该 Anthropic image source。请使用 base64 或 URL 图片后重试。",
		"unsupported_anthropic_image_source_for_gemini_native")
}

// ---------------------------------------------------------------------------
// Shared finalize / generationConfig (finalizeGeminiBody +
// generationConfigFromSource + the per-source option readers)
// ---------------------------------------------------------------------------

// finalizeGeminiNativeBody mirrors finalizeGeminiBody. The upstream model rides
// the body `model` field exactly like the archive (the earlier chat-source Go
// revision in bridge_request.go drops it; the two sources ported here keep
// Node parity).
func finalizeGeminiNativeBody(clientBody map[string]any, options BridgeRequestBodyOptions, contents []any, systemText string, tools []any) (map[string]any, error) {
	if len(contents) == 0 {
		return nil, bridgeValidationError(
			"Gemini native 目标桥接至少需要一条可发送内容",
			"empty_gemini_target_bridge_contents")
	}
	generationConfig, err := generationConfigFromSourceForGeminiNative(clientBody, options)
	if err != nil {
		return nil, err
	}
	output := map[string]any{
		"contents":         contents,
		"generationConfig": generationConfig,
	}
	if upstreamModel := options.ModelOverride; upstreamModel != "" {
		output["model"] = upstreamModel
	}
	if strings.TrimSpace(systemText) != "" {
		output["systemInstruction"] = map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": strings.TrimSpace(systemText)}},
		}
	}
	if len(tools) > 0 {
		output["tools"] = []any{map[string]any{"functionDeclarations": tools}}
		if toolConfig := geminiToolConfigFromSourceForGeminiNative(clientBody); toolConfig != nil {
			output["toolConfig"] = toolConfig
		}
	}
	if serviceTier := geminiServiceTierFromSourceForGeminiNative(clientBody); serviceTier != "" {
		output["service_tier"] = serviceTier
	}
	return output, nil
}

// generationConfigFromSourceForGeminiNative mirrors generationConfigFromSource.
func generationConfigFromSourceForGeminiNative(clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	config := map[string]any{}
	if temperature, ok := bridgeNumberValue(clientBody["temperature"]); ok {
		config["temperature"] = temperature
	}
	if topP, ok := bridgeNumberValue(clientBody["top_p"]); ok {
		config["topP"] = topP
	}
	if maxTokens, ok := geminiNativeMaxOutputTokens(clientBody); ok {
		config["maxOutputTokens"] = maxTokens
	}
	if stop, ok := geminiNativeStopValue(clientBody); ok {
		config["stopSequences"] = stop
	}
	if responseMimeType := responseMimeTypeFromSourceForGeminiNative(clientBody); responseMimeType != "" {
		config["responseMimeType"] = responseMimeType
	}
	if schema := responseSchemaFromSourceForGeminiNative(clientBody); schema != nil {
		config["responseSchema"] = schema
	}
	if thinkingLevel := geminiThinkingLevelFromSourceForGeminiNative(clientBody); thinkingLevel != "" {
		config["thinkingConfig"] = map[string]any{"thinkingLevel": thinkingLevel}
	}
	if _, hasLogprobs := clientBody["logprobs"]; hasLogprobs {
		return nil, geminiNativeGuidanceError(options,
			"当前"+providerLabel(options.GuidanceProviderName)+" Gemini native 上游不能保真承载 OpenAI logprobs。请移除 logprobs 后重试。",
			"unsupported_logprobs_for_gemini_native")
	}
	if _, hasTopLogprobs := clientBody["top_logprobs"]; hasTopLogprobs {
		return nil, geminiNativeGuidanceError(options,
			"当前"+providerLabel(options.GuidanceProviderName)+" Gemini native 上游不能保真承载 OpenAI logprobs。请移除 logprobs 后重试。",
			"unsupported_logprobs_for_gemini_native")
	}
	return config, nil
}

// geminiNativeMaxOutputTokens mirrors
// `numberValue(body.max_tokens) ?? numberValue(body.max_completion_tokens) ??
// numberValue(body.max_output_tokens)` followed by Math.trunc.
func geminiNativeMaxOutputTokens(clientBody map[string]any) (int64, bool) {
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if value, ok := bridgeNumberValue(clientBody[key]); ok {
			return int64(value), true
		}
	}
	return 0, false
}

// geminiNativeStopValue mirrors `const stop = body.stop ?? body.stop_sequences`
// plus the string / array shaping (an array keeps the filtered string items
// even when empty, mirroring the archive).
func geminiNativeStopValue(clientBody map[string]any) ([]any, bool) {
	stop, has := clientBody["stop"]
	if !has || stop == nil {
		stop, has = clientBody["stop_sequences"]
		if !has {
			return nil, false
		}
	}
	switch typed := stop.(type) {
	case string:
		return []any{typed}, true
	case []any:
		filtered := []any{}
		for _, item := range typed {
			if text, ok := item.(string); ok {
				filtered = append(filtered, text)
			}
		}
		return filtered, true
	}
	return nil, false
}

// geminiThinkingLevelFromSourceForGeminiNative mirrors
// geminiThinkingLevelFromSource.
func geminiThinkingLevelFromSourceForGeminiNative(clientBody map[string]any) string {
	effort := ""
	if reasoning := bridgeObjectValue(clientBody["reasoning"]); reasoning != nil {
		effort = bridgeStringValue(reasoning["effort"])
	}
	if effort == "" {
		effort = bridgeStringValue(clientBody["reasoning_effort"])
	}
	switch effort {
	case "minimal", "low", "medium", "high":
		return effort
	}
	return ""
}

// geminiServiceTierFromSourceForGeminiNative mirrors geminiServiceTierFromSource.
func geminiServiceTierFromSourceForGeminiNative(clientBody map[string]any) string {
	switch tier := bridgeStringValue(clientBody["service_tier"]); tier {
	case "priority", "flex":
		return tier
	case "default":
		return "standard"
	}
	return ""
}

// responseMimeTypeFromSourceForGeminiNative mirrors responseMimeTypeFromSource.
func responseMimeTypeFromSourceForGeminiNative(clientBody map[string]any) string {
	if responseFormat := bridgeObjectValue(clientBody["response_format"]); responseFormat != nil {
		switch bridgeStringValue(responseFormat["type"]) {
		case "json_object", "json_schema":
			return "application/json"
		}
	}
	if text := bridgeObjectValue(clientBody["text"]); text != nil {
		if textFormat := bridgeObjectValue(text["format"]); textFormat != nil {
			switch bridgeStringValue(textFormat["type"]) {
			case "json_object", "json_schema":
				return "application/json"
			}
		}
	}
	return ""
}

// responseSchemaFromSourceForGeminiNative mirrors responseSchemaFromSource.
func responseSchemaFromSourceForGeminiNative(clientBody map[string]any) map[string]any {
	if responseFormat := bridgeObjectValue(clientBody["response_format"]); responseFormat != nil {
		if jsonSchema := bridgeObjectValue(responseFormat["json_schema"]); jsonSchema != nil {
			if schema := bridgeObjectValue(jsonSchema["schema"]); schema != nil {
				return schema
			}
		}
	}
	if text := bridgeObjectValue(clientBody["text"]); text != nil {
		if textFormat := bridgeObjectValue(text["format"]); textFormat != nil {
			if textJsonSchema := bridgeObjectValue(textFormat["json_schema"]); textJsonSchema != nil {
				return bridgeObjectValue(textJsonSchema["schema"])
			}
		}
	}
	return nil
}

// geminiToolConfigFromSourceForGeminiNative mirrors geminiToolConfigFromSource.
func geminiToolConfigFromSourceForGeminiNative(clientBody map[string]any) map[string]any {
	toolChoice, has := clientBody["tool_choice"]
	if !has || toolChoice == nil {
		return nil
	}
	if text, ok := toolChoice.(string); ok {
		switch text {
		case "auto":
			return nil
		case "none":
			return map[string]any{"functionCallingConfig": map[string]any{"mode": "NONE"}}
		case "required", "any":
			return map[string]any{"functionCallingConfig": map[string]any{"mode": "ANY"}}
		}
		return nil
	}
	choice := bridgeObjectValue(toolChoice)
	if choice == nil {
		return nil
	}
	if fn := bridgeObjectValue(choice["function"]); fn != nil {
		if name := bridgeStringValue(fn["name"]); name != "" {
			return map[string]any{
				"functionCallingConfig": map[string]any{
					"mode":                 "ANY",
					"allowedFunctionNames": []string{name},
				},
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Image part shaping (imageUrlToGeminiPart + parseDataUrl + guessImageMimeType)
// ---------------------------------------------------------------------------

var (
	geminiNativeDataURLPattern = regexp.MustCompile(`(?is)^data:([^;,]+)(?:;[^,]*)?;base64,(.+)$`)
	geminiNativeJPEGURLPattern = regexp.MustCompile(`(?i)\.jpe?g(?:[?#]|$)`)
	geminiNativeWebPURLPattern = regexp.MustCompile(`(?i)\.webp(?:[?#]|$)`)
	geminiNativeGIFURLPattern  = regexp.MustCompile(`(?i)\.gif(?:[?#]|$)`)
)

// imageUrlToGeminiPartForGeminiNative mirrors imageUrlToGeminiPart.
func imageUrlToGeminiPartForGeminiNative(url string, options BridgeRequestBodyOptions) (map[string]any, error) {
	if match := geminiNativeDataURLPattern.FindStringSubmatch(url); match != nil {
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": match[1],
				"data":     match[2],
			},
		}, nil
	}
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(url, "gs://") {
		return map[string]any{
			"fileData": map[string]any{
				"fileUri":  url,
				"mimeType": guessImageMimeTypeForGeminiNative(url),
			},
		}, nil
	}
	return nil, geminiNativeGuidanceError(options,
		"当前"+providerLabel(options.GuidanceProviderName)+" Gemini native 上游不能直接读取 OpenAI file_id 图片引用。请提供 data URL、公开 HTTPS 图片 URL 或先上传到 Gemini Files 后重试。",
		"unsupported_openai_image_reference_for_gemini_native")
}

// guessImageMimeTypeForGeminiNative mirrors guessImageMimeType.
func guessImageMimeTypeForGeminiNative(url string) string {
	if geminiNativeJPEGURLPattern.MatchString(url) {
		return "image/jpeg"
	}
	if geminiNativeWebPURLPattern.MatchString(url) {
		return "image/webp"
	}
	if geminiNativeGIFURLPattern.MatchString(url) {
		return "image/gif"
	}
	return "image/png"
}

// geminiFunctionResponsePart mirrors geminiFunctionResponsePart.
func geminiFunctionResponsePart(name string, value any) map[string]any {
	responseValue := value
	if !bridgeIsPlainObject(value) {
		if value == nil {
			responseValue = ""
		}
		responseValue = map[string]any{"content": responseValue}
	}
	return map[string]any{
		"functionResponse": map[string]any{
			"name":     name,
			"response": responseValue,
		},
	}
}

// geminiJSONObjectFromUnknown mirrors jsonObjectFromUnknown: plain objects pass
// through, JSON-object strings parse, everything else degrades to {}.
func geminiJSONObjectFromUnknown(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	if text, ok := value.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err == nil {
			if object, ok := parsed.(map[string]any); ok {
				return object
			}
		}
	}
	return map[string]any{}
}
