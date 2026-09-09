package openaicompat

import (
	"strings"
)

// BuildBridgeRequestBody dispatches the B-4 cross-protocol bridge by source /
// upstream endpoint family (the mapping license surface from
// openaicompat.IsCrossProtocolBridgeRequired). It mirrors the Node wiring in
// providers/drivers/*/driver.ts buildUpstreamRequestParts:
//
//	chat_completions -> anthropic_messages   openai-anthropic-bridge
//	responses        -> anthropic_messages   openai-anthropic-bridge (responses input)
//	chat_completions -> gemini_*             openai-anthropic-gemini-native / chat->gemini
//	responses        -> gemini_*             openai-anthropic-gemini-native (responses input)
//	anthropic_messages -> gemini_*           openai-anthropic-gemini-native (anthropic input)
//	anthropic_messages -> chat_completions   anthropic-openai-chat-bridge
//	gemini_*           -> chat_completions   gemini-openai-chat-bridge
//	gemini_*           -> anthropic_messages gemini-anthropic-messages-bridge
//	codex responses  -> chat_completions     codex-responses-chat-bridge
//	(through BuildCodexResponsesToChatCompletionsBody)

// BuildBridgeRequestBody builds the upstream body for one cross-protocol pair.
func BuildBridgeRequestBody(sourceFamily, upstreamFamily string, clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	sourceFamily = NormalizeEndpointFamily(sourceFamily)
	upstreamFamily = NormalizeEndpointFamily(upstreamFamily)
	switch {
	case upstreamFamily == FamilyAnthropicMessages:
		switch sourceFamily {
		case FamilyChatCompletions:
			return BuildOpenAIChatToAnthropicMessagesBody(clientBody, options)
		case FamilyResponses:
			return BuildOpenAIResponsesToAnthropicMessagesBody(clientBody, options)
		case FamilyGeminiGenerateContent, FamilyGeminiStreamGenerate:
			return BuildGeminiToAnthropicMessagesBody(clientBody, options)
		}
	case upstreamFamily == FamilyChatCompletions:
		switch sourceFamily {
		case FamilyAnthropicMessages:
			return BuildAnthropicMessagesToChatCompletionsBody(clientBody, options)
		case FamilyGeminiGenerateContent, FamilyGeminiStreamGenerate:
			return BuildGeminiGenerateContentToChatCompletionsBody(clientBody, options)
		case FamilyResponses:
			return BuildCodexResponsesToChatCompletionsBody(clientBody, options)
		}
	case upstreamFamily == FamilyGeminiGenerateContent || upstreamFamily == FamilyGeminiStreamGenerate:
		switch sourceFamily {
		case FamilyChatCompletions:
			return BuildOpenAIChatToGeminiBody(clientBody, options)
		case FamilyResponses:
			return BuildOpenAIResponsesToGeminiBody(clientBody, options)
		case FamilyAnthropicMessages:
			return BuildAnthropicMessagesToGeminiBody(clientBody, options)
		default:
			// Node sourceBodyToGeminiGenerateContentBody fall-through.
			return nil, bridgeValidationError(
				"当前下游协议不能桥接到 Gemini native GenerateContent",
				"unsupported_gemini_target_bridge_source")
		}
	}
	return nil, bridgeValidationError(
		"不支持的跨协议桥接组合："+sourceFamily+" -> "+upstreamFamily,
		"bridge_unsupported_family_pair")
}

// ---------------------------------------------------------------------------
// Gemini GenerateContent -> Anthropic Messages (gemini-anthropic-messages-bridge.ts)
// ---------------------------------------------------------------------------

// BuildGeminiToAnthropicMessagesBody mirrors the gemini-anthropic-messages
// bridge request core: contents roles user/model, parts text / inlineData /
// functionCall / functionResponse, systemInstruction text parts, and the
// generationConfig / tools surface.
func BuildGeminiToAnthropicMessagesBody(body map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	if _, ok := bridgeIsArray(body["contents"]); !ok {
		return nil, bridgeValidationError(
			"Gemini 到 Anthropic Messages 桥接要求 contents 是数组",
			"invalid_gemini_anthropic_bridge_contents")
	}
	model := options.ModelOverride
	if model == "" {
		model = options.DefaultModel
	}

	messages := []any{}
	systemParts := []string{}
	toolUseHistory := map[string]bool{} // anthropic tool_use ids seen so far
	toolNamesByID := map[string]string{}
	geminiFunctionNames := map[string]string{} // call name -> stable id
	if instruction := geminiSystemInstructionText(body["systemInstruction"]); instruction != "" {
		systemParts = append(systemParts, instruction)
	}
	contents, _ := bridgeIsArray(body["contents"])
	for _, item := range contents {
		content := bridgeObjectValue(item)
		if content == nil {
			continue
		}
		role := "user"
		if text, ok := content["role"].(string); ok && text != "" {
			role = text
		}
		parts, _ := bridgeIsArray(content["parts"])
		blocks := []any{}
		for _, partValue := range parts {
			part := bridgeObjectValue(partValue)
			if part == nil {
				continue
			}
			if text, ok := part["text"].(string); ok {
				if text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": text})
				}
				continue
			}
			if call := bridgeObjectValue(part["functionCall"]); call != nil {
				name := bridgeStringValue(call["name"])
				if name == "" {
					return nil, bridgeValidationError(
						"Gemini functionCall 缺少 name",
						"invalid_gemini_anthropic_bridge_function_call")
				}
				callID := geminiFunctionCallID(geminiFunctionNames, name)
				args := call["args"]
				if !bridgeIsPlainObject(args) {
					args = map[string]any{}
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    callID,
					"name":  name,
					"input": args,
				})
				toolUseHistory[callID] = true
				toolNamesByID[callID] = name
				continue
			}
			if response := bridgeObjectValue(part["functionResponse"]); response != nil {
				name := bridgeStringValue(response["name"])
				if name == "" {
					return nil, bridgeValidationError(
						"Gemini functionResponse 缺少 name",
						"invalid_gemini_anthropic_bridge_function_response")
				}
				callID := geminiFunctionCallID(geminiFunctionNames, name)
				responseValue := response["response"]
				if responseValue == nil {
					responseValue = map[string]any{}
				}
				messages = appendAnthropicMessage(messages, map[string]any{
					"role": "user",
					"content": []any{map[string]any{
						"type":        "tool_result",
						"tool_use_id": callID,
						"content":     bridgeJSONStringify(responseValue),
					}},
				})
				continue
			}
			if inlineData := bridgeObjectValue(part["inlineData"]); inlineData != nil {
				block, err := geminiInlineDataToAnthropicImage(inlineData)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, block)
				continue
			}
			kind := "unknown"
			for key := range part {
				if key != "thought" {
					kind = key
					break
				}
			}
			return nil, bridgeValidationError(
				"当前 Anthropic Messages 上游不支持 Gemini part："+kind+"。请客户端改用真实支持该 part 的 Gemini 原生上游，或在本地 agent 中先转换/执行后再发起请求。",
				"unsupported_gemini_anthropic_content_part")
		}
		if len(blocks) == 0 {
			continue
		}
		anthropicRole := "user"
		if role == "model" {
			anthropicRole = "assistant"
		}
		messages = appendAnthropicMessage(messages, map[string]any{
			"role":    anthropicRole,
			"content": blocks,
		})
	}
	if len(messages) == 0 {
		messages = appendAnthropicMessage(messages, map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": ""}},
		})
	}

	output := map[string]any{
		"model":      model,
		"max_tokens": defaultAnthropicMaxTokens,
		"stream":     options.Stream,
	}
	if generationConfig := bridgeObjectValue(body["generationConfig"]); generationConfig != nil {
		if temperature, ok := bridgeNumberValue(generationConfig["temperature"]); ok {
			output["temperature"] = temperature
		}
		if topP, ok := bridgeNumberValue(generationConfig["topP"]); ok {
			output["top_p"] = topP
		}
		if maxTokens, ok := bridgeIntegerValue(generationConfig["maxOutputTokens"]); ok {
			output["max_tokens"] = maxTokens
		}
		if stop := stopSequencesValue(generationConfig["stopSequences"]); len(stop) > 0 {
			output["stop_sequences"] = stop
		}
	}
	if len(systemParts) > 0 {
		output["system"] = strings.Join(systemParts, "\n\n")
	}
	output["messages"] = messages
	if tools := geminiToolsToAnthropicTools(body["tools"]); len(tools) > 0 {
		output["tools"] = tools
	}
	return output, nil
}

func geminiFunctionCallID(idsByName map[string]string, name string) string {
	if id, ok := idsByName[name]; ok {
		return id
	}
	id := "toolu_" + safeBridgeIDSegment(name) + "_" + int64ToText(int64(len(idsByName)))
	idsByName[name] = id
	return id
}

func geminiSystemInstructionText(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	instruction := bridgeObjectValue(value)
	if instruction == nil {
		return ""
	}
	parts, _ := bridgeIsArray(instruction["parts"])
	texts := []string{}
	for _, part := range parts {
		if partMap := bridgeObjectValue(part); partMap != nil {
			if text, ok := partMap["text"].(string); ok && text != "" {
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n")
}

func geminiInlineDataToAnthropicImage(inlineData map[string]any) (map[string]any, error) {
	mimeType := firstNonEmpty(bridgeStringValue(inlineData["mimeType"]), bridgeStringValue(inlineData["mime_type"]))
	data := bridgeStringValue(inlineData["data"])
	if data == "" {
		return nil, bridgeValidationError(
			"Gemini inlineData 缺少 data",
			"invalid_gemini_anthropic_bridge_inline_data")
	}
	if !anthropicSupportedImageMediaTypes[strings.ToLower(mimeType)] {
		return nil, bridgeValidationError(
			"Gemini 到 Anthropic Messages 桥接只支持 image/jpeg、image/png、image/gif、image/webp inlineData 图片，收到 "+mimeType,
			"unsupported_gemini_anthropic_image_media_type")
	}
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": strings.ToLower(mimeType),
			"data":       data,
		},
	}, nil
}

func geminiToolsToAnthropicTools(value any) []any {
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil
	}
	tools := []any{}
	for _, item := range items {
		tool := bridgeObjectValue(item)
		if tool == nil {
			continue
		}
		declarations, _ := bridgeIsArray(tool["functionDeclarations"])
		for _, declarationValue := range declarations {
			declaration := bridgeObjectValue(declarationValue)
			if declaration == nil {
				continue
			}
			name := bridgeStringValue(declaration["name"])
			if name == "" {
				continue
			}
			parameters, ok := declaration["parameters"].(map[string]any)
			if !ok {
				parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{
				"name":         name,
				"description":  bridgeStringValue(declaration["description"]),
				"input_schema": parameters,
			})
		}
	}
	return tools
}
