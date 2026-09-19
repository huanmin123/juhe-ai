package openaicompatbridge

import "strings"

// ---------------------------------------------------------------------------
// Gemini GenerateContent -> Chat Completions (gemini-openai-chat-bridge.ts)
// ---------------------------------------------------------------------------

// BuildGeminiGenerateContentToChatCompletionsBody mirrors
// geminiGenerateContentBodyToChatCompletionsBody +
// validateGeminiGenerateContentChatBridgeBody.
func BuildGeminiGenerateContentToChatCompletionsBody(body map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	if _, ok := bridgeIsArray(body["contents"]); !ok {
		return nil, bridgeValidationError(
			"Gemini GenerateContent 到 Chat Completions 桥接要求 contents 是数组",
			"invalid_gemini_chat_bridge_contents")
	}
	model := options.ModelOverride
	if model == "" {
		model = options.DefaultModel
	}
	if hasOwnKey(body, "cachedContent") && body["cachedContent"] != nil {
		return nil, bridgeValidationError(
			"当前"+providerLabel(options.GuidanceProviderName)+" Chat Completions 上游不能保真承载 Gemini cachedContent。请客户端改用真实支持 cachedContent 的 Gemini 原生上游，或移除 cachedContent 后重试。",
			"unsupported_gemini_cached_content")
	}
	if generationConfig := bridgeObjectValue(body["generationConfig"]); generationConfig != nil &&
		hasOwnKey(generationConfig, "thinkingConfig") && generationConfig["thinkingConfig"] != nil {
		return nil, bridgeValidationError(
			"当前"+providerLabel(options.GuidanceProviderName)+" Chat Completions 上游不能保真承载 Gemini thinkingConfig。请移除思考配置，或改用支持该字段的 Gemini 原生上游。",
			"unsupported_gemini_thinking_config")
	}

	toolCallState := newGeminiToolCallIDState()
	messages := []any{}
	if err := appendGeminiSystemInstruction(&messages, body["systemInstruction"]); err != nil {
		return nil, err
	}
	contents, _ := bridgeIsArray(body["contents"])
	for _, item := range contents {
		content := bridgeObjectValue(item)
		if content == nil {
			continue
		}
		if err := appendGeminiContent(&messages, content, toolCallState); err != nil {
			return nil, err
		}
	}

	output := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   options.Stream,
	}
	applyGeminiGenerationConfig(output, bridgeObjectValue(body["generationConfig"]))
	tools, err := geminiToolsToChatTools(body["tools"])
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		output["tools"] = tools
		if choice := geminiToolChoiceToChatToolChoice(bridgeObjectValue(body["toolConfig"])); choice != nil {
			output["tool_choice"] = choice
		}
	}
	return output, nil
}

func appendGeminiSystemInstruction(output *[]any, value any) error {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		if strings.TrimSpace(text) != "" {
			*output = append(*output, map[string]any{"role": "system", "content": text})
		}
		return nil
	}
	instruction, ok := value.(map[string]any)
	if !ok {
		return bridgeValidationError(
			"Gemini systemInstruction 必须是字符串或 Content 对象",
			"invalid_gemini_chat_bridge_system_instruction")
	}
	parts, _ := bridgeIsArray(instruction["parts"])
	texts := []string{}
	for _, partValue := range parts {
		part := bridgeObjectValue(partValue)
		if part == nil {
			continue
		}
		text, ok := part["text"].(string)
		if !ok {
			return bridgeValidationError(
				"当前 Chat Completions 上游只支持把 Gemini systemInstruction text part 转换为 system 消息。请移除 systemInstruction 中的非 text part 后重试。",
				"unsupported_gemini_system_instruction_part")
		}
		if text != "" {
			texts = append(texts, text)
		}
	}
	if text := strings.Join(texts, "\n"); text != "" {
		*output = append(*output, map[string]any{"role": "system", "content": text})
	}
	return nil
}

type geminiToolCallIDState struct {
	idsByName map[string]string
	nextIndex int
}

func newGeminiToolCallIDState() *geminiToolCallIDState {
	return &geminiToolCallIDState{idsByName: map[string]string{}}
}

func (s *geminiToolCallIDState) idForName(name string) string {
	if existing, ok := s.idsByName[name]; ok {
		return existing
	}
	id := "call_" + sanitizeGeminiToolCallIDName(name) + "_" + int64ToText(int64(s.nextIndex))
	s.nextIndex++
	s.idsByName[name] = id
	return id
}

func sanitizeGeminiToolCallIDName(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	out := builder.String()
	if len(out) > 48 {
		out = out[:48]
	}
	if out == "" {
		return "tool"
	}
	return out
}

func appendGeminiContent(output *[]any, content map[string]any, toolCallState *geminiToolCallIDState) error {
	role := "user"
	if text, ok := content["role"].(string); ok && text != "" {
		role = text
	}
	parts, _ := bridgeIsArray(content["parts"])
	if len(parts) == 0 {
		return nil
	}
	for _, partValue := range parts {
		if part := bridgeObjectValue(partValue); part != nil && part["functionResponse"] != nil {
			return appendGeminiFunctionResponses(output, parts, toolCallState)
		}
	}
	if role == "model" {
		converted, err := geminiModelContentToChatAssistantMessage(parts, toolCallState)
		if err != nil {
			return err
		}
		*output = append(*output, converted)
		return nil
	}
	if role != "user" && role != "function" {
		return bridgeValidationError(
			"Gemini GenerateContent 到 Chat Completions 桥接只支持 user、model 和 function 角色",
			"invalid_gemini_chat_bridge_role")
	}
	contentValue, err := geminiUserPartsToChatContent(parts)
	if err != nil {
		return err
	}
	*output = append(*output, map[string]any{"role": "user", "content": contentValue})
	return nil
}

func appendGeminiFunctionResponses(output *[]any, parts []any, toolCallState *geminiToolCallIDState) error {
	var pendingUserParts []any
	flush := func() {
		if len(pendingUserParts) == 0 {
			return
		}
		*output = append(*output, map[string]any{"role": "user", "content": chatUserContentFromParts(pendingUserParts)})
		pendingUserParts = nil
	}
	for _, partValue := range parts {
		part := bridgeObjectValue(partValue)
		if part == nil {
			continue
		}
		if response := bridgeObjectValue(part["functionResponse"]); response != nil {
			flush()
			name := bridgeStringValue(response["name"])
			if name == "" {
				return bridgeValidationError(
					"Gemini functionResponse 缺少 name",
					"invalid_gemini_chat_bridge_function_response")
			}
			responseValue := response["response"]
			if responseValue == nil {
				responseValue = map[string]any{}
			}
			*output = append(*output, map[string]any{
				"role":         "tool",
				"tool_call_id": toolCallState.idForName(name),
				"content":      bridgeJSONStringify(responseValue),
			})
			continue
		}
		if err := appendGeminiPartToUserParts(&pendingUserParts, part); err != nil {
			return err
		}
	}
	flush()
	return nil
}

func geminiModelContentToChatAssistantMessage(parts []any, toolCallState *geminiToolCallIDState) (map[string]any, error) {
	textParts := []string{}
	toolCalls := []any{}
	for _, partValue := range parts {
		part := bridgeObjectValue(partValue)
		if part == nil {
			continue
		}
		if text, ok := part["text"].(string); ok {
			textParts = append(textParts, text)
			continue
		}
		if call := bridgeObjectValue(part["functionCall"]); call != nil {
			name := bridgeStringValue(call["name"])
			if name == "" {
				return nil, bridgeValidationError(
					"Gemini functionCall 缺少 name",
					"invalid_gemini_chat_bridge_function_call")
			}
			args := call["args"]
			if args == nil {
				args = map[string]any{}
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   toolCallState.idForName(name),
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": bridgeJSONStringify(args),
				},
			})
			continue
		}
		return nil, unsupportedGeminiPartError(part)
	}
	contentValue := strings.Join(textParts, "")
	output := map[string]any{"role": "assistant"}
	if len(toolCalls) > 0 && contentValue == "" {
		output["content"] = nil
	} else {
		output["content"] = contentValue
	}
	if len(toolCalls) > 0 {
		output["tool_calls"] = toolCalls
	}
	return output, nil
}

func geminiUserPartsToChatContent(parts []any) (any, error) {
	output := []any{}
	for _, partValue := range parts {
		part := bridgeObjectValue(partValue)
		if part == nil {
			continue
		}
		if err := appendGeminiPartToUserParts(&output, part); err != nil {
			return nil, err
		}
	}
	return chatUserContentFromParts(output), nil
}

func appendGeminiPartToUserParts(output *[]any, part map[string]any) error {
	if text, ok := part["text"].(string); ok {
		*output = append(*output, map[string]any{"type": "text", "text": text})
		return nil
	}
	if inlineData := bridgeObjectValue(part["inlineData"]); inlineData != nil {
		imageURL, err := geminiInlineDataToChatImageURL(inlineData)
		if err != nil {
			return err
		}
		*output = append(*output, map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL}})
		return nil
	}
	if fileData := bridgeObjectValue(part["fileData"]); fileData != nil {
		imageURL, err := geminiFileDataToChatImageURL(fileData)
		if err != nil {
			return err
		}
		*output = append(*output, map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL}})
		return nil
	}
	if part["functionCall"] != nil || part["functionResponse"] != nil {
		return bridgeValidationError(
			"Gemini functionCall/functionResponse 只能出现在 model 或 function 响应消息中",
			"invalid_gemini_chat_bridge_function_part")
	}
	return unsupportedGeminiPartError(part)
}

func geminiInlineDataToChatImageURL(inlineData map[string]any) (string, error) {
	mimeType := firstNonEmpty(bridgeStringValue(inlineData["mimeType"]), bridgeStringValue(inlineData["mime_type"]), "application/octet-stream")
	data := bridgeStringValue(inlineData["data"])
	if data == "" {
		return "", bridgeValidationError(
			"Gemini inlineData 缺少 data",
			"invalid_gemini_chat_bridge_inline_data")
	}
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return "", bridgeValidationError(
			"当前 Chat Completions 上游只支持把 Gemini inlineData 图片转换为 image_url。请移除非图片 inlineData，或改用真实支持该输入的 Gemini 原生上游。",
			"unsupported_gemini_inline_data")
	}
	return "data:" + mimeType + ";base64," + data, nil
}

func geminiFileDataToChatImageURL(fileData map[string]any) (string, error) {
	mimeType := firstNonEmpty(bridgeStringValue(fileData["mimeType"]), bridgeStringValue(fileData["mime_type"]))
	fileURI := firstNonEmpty(bridgeStringValue(fileData["fileUri"]), bridgeStringValue(fileData["file_uri"]))
	if fileURI == "" {
		return "", bridgeValidationError(
			"Gemini fileData 缺少 fileUri",
			"invalid_gemini_chat_bridge_file_data")
	}
	lowerURI := strings.ToLower(fileURI)
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") ||
		!(strings.HasPrefix(lowerURI, "http://") || strings.HasPrefix(lowerURI, "https://")) {
		return "", bridgeValidationError(
			"当前 Chat Completions 上游只支持可公开访问的图片 fileData。请改用 https 图片 URL、inlineData 图片，或选择 Gemini 原生上游。",
			"unsupported_gemini_file_data")
	}
	return fileURI, nil
}

func unsupportedGeminiPartError(part map[string]any) error {
	for _, key := range []string{"executableCode", "codeExecutionResult", "videoMetadata", "thoughtSignature"} {
		if _, ok := part[key]; ok {
			return bridgeValidationError(
				"当前 Chat Completions 上游不支持 Gemini part："+key+"。请客户端改用真实支持该 part 的 Gemini 原生上游，或在本地 agent 中先转换/执行后再发起请求。",
				"unsupported_gemini_content_part")
		}
	}
	kind := "unknown"
	for key := range part {
		if key != "thought" {
			kind = key
			break
		}
	}
	return bridgeValidationError(
		"当前 Chat Completions 上游不支持 Gemini part："+kind+"。请客户端改用真实支持该 part 的 Gemini 原生上游，或在本地 agent 中先转换/执行后再发起请求。",
		"unsupported_gemini_content_part")
}

func applyGeminiGenerationConfig(output, generationConfig map[string]any) {
	if generationConfig == nil {
		return
	}
	if temperature, ok := bridgeNumberValue(generationConfig["temperature"]); ok {
		output["temperature"] = temperature
	}
	if topP, ok := bridgeNumberValue(generationConfig["topP"]); ok {
		output["top_p"] = topP
	}
	if maxTokens, ok := bridgeIntegerValue(generationConfig["maxOutputTokens"]); ok {
		output["max_tokens"] = maxTokens
	}
	if stop := anthropicStopSequencesToChatStop(generationConfig["stopSequences"]); stop != nil {
		output["stop"] = stop
	}
	if candidateCount, ok := bridgeIntegerValue(generationConfig["candidateCount"]); ok && candidateCount > 0 {
		output["n"] = candidateCount
	}
	if responseMimeType := bridgeStringValue(generationConfig["responseMimeType"]); responseMimeType != "" && responseMimeType != "text/plain" {
		if responseMimeType == "application/json" {
			output["response_format"] = map[string]any{"type": "json_object"}
		}
	}
}

func geminiToolsToChatTools(value any) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil, bridgeValidationError(
			"Gemini tools 必须是数组",
			"invalid_gemini_chat_bridge_tools")
	}
	output := []any{}
	for _, item := range items {
		tool := bridgeObjectValue(item)
		if tool == nil {
			continue
		}
		unsupported := []string{}
		for key, toolValue := range tool {
			if key != "functionDeclarations" && toolValue != nil {
				unsupported = append(unsupported, key)
			}
		}
		if len(unsupported) > 0 {
			return nil, bridgeValidationError(
				"当前 Chat Completions 上游不支持 Gemini 原生工具："+strings.Join(unsupported, "、")+"。请客户端改用 functionDeclarations 或改用真实支持这些工具的 Gemini 原生上游。",
				"unsupported_gemini_native_tools")
		}
		declarations, _ := bridgeIsArray(tool["functionDeclarations"])
		for _, declarationValue := range declarations {
			declaration := bridgeObjectValue(declarationValue)
			if declaration == nil {
				continue
			}
			name := bridgeStringValue(declaration["name"])
			if name == "" {
				return nil, bridgeValidationError(
					"Gemini functionDeclaration 缺少 name",
					"invalid_gemini_chat_bridge_function_declaration")
			}
			parameters, ok := declaration["parameters"].(map[string]any)
			if !ok {
				parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			output = append(output, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        name,
					"description": bridgeStringValue(declaration["description"]),
					"parameters":  parameters,
				},
			})
		}
	}
	return output, nil
}

func geminiToolChoiceToChatToolChoice(toolConfig map[string]any) any {
	functionCallingConfig := bridgeObjectValue(toolConfig["functionCallingConfig"])
	if functionCallingConfig == nil {
		return nil
	}
	mode := strings.ToUpper(bridgeStringValue(functionCallingConfig["mode"]))
	if mode == "" {
		mode = "AUTO"
	}
	switch mode {
	case "NONE":
		return "none"
	case "AUTO":
		return "auto"
	case "ANY":
		allowed := []string{}
		if names, ok := bridgeIsArray(functionCallingConfig["allowedFunctionNames"]); ok {
			for _, name := range names {
				if text, ok := name.(string); ok && strings.TrimSpace(text) != "" {
					allowed = append(allowed, text)
				}
			}
		}
		if len(allowed) == 1 {
			return map[string]any{"type": "function", "function": map[string]any{"name": allowed[0]}}
		}
		return "required"
	default:
		return nil
	}
}
