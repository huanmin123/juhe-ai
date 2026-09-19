package openaicompatbridge

import "strings"

// ---------------------------------------------------------------------------
// OpenAI Chat -> Gemini GenerateContent (gemini native upstream direction)
// ---------------------------------------------------------------------------

// BuildOpenAIChatToGeminiBody mirrors the openai-anthropic-gemini-native /
// gemini-openai-chat reverse surface: a chat completions request body mapped
// onto a Gemini generateContent body (contents / systemInstruction /
// generationConfig / tools.functionDeclarations / toolConfig).
func BuildOpenAIChatToGeminiBody(clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	model := options.ModelOverride
	if model == "" {
		model = bridgeStringValue(clientBody["model"])
	}
	if model == "" {
		model = options.DefaultModel
	}
	contents := []any{}
	var systemParts []string
	inputMessages, _ := bridgeIsArray(clientBody["messages"])
	for _, item := range inputMessages {
		message := bridgeObjectValue(item)
		if message == nil {
			continue
		}
		role := bridgeStringValue(message["role"])
		text := openAIContentToText(message["content"])
		switch role {
		case "system", "developer":
			if text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		case "tool":
			// Gemini carries tool results as user functionResponse parts.
			toolResult := map[string]any{
				"role": "user",
				"parts": []any{map[string]any{
					"functionResponse": map[string]any{
						"name":     bridgeStringValue(message["tool_call_id"]),
						"response": map[string]any{"result": text},
					},
				}},
			}
			contents = append(contents, toolResult)
			continue
		case "user", "assistant":
		default:
			continue
		}
		geminiRole := "user"
		if role == "assistant" {
			geminiRole = "model"
		}
		parts := []any{}
		if text != "" {
			parts = append(parts, map[string]any{"text": text})
		}
		if role == "assistant" {
			for _, block := range chatToolCallsToGeminiFunctionCalls(message["tool_calls"]) {
				parts = append(parts, block)
			}
		}
		if len(parts) == 0 {
			parts = append(parts, map[string]any{"text": ""})
		}
		contents = append(contents, map[string]any{"role": geminiRole, "parts": parts})
	}
	if len(contents) == 0 {
		contents = append(contents, map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": ""}},
		})
	}

	output := map[string]any{"contents": contents}
	if len(systemParts) > 0 {
		output["systemInstruction"] = map[string]any{
			"parts": []any{map[string]any{"text": strings.Join(systemParts, "\n\n")}},
		}
	}
	generationConfig := map[string]any{}
	if temperature, ok := bridgeNumberValue(clientBody["temperature"]); ok {
		generationConfig["temperature"] = temperature
	}
	if topP, ok := bridgeNumberValue(clientBody["top_p"]); ok {
		generationConfig["topP"] = topP
	}
	maxTokens := int64(0)
	if value, ok := bridgeIntegerValue(clientBody["max_tokens"]); ok {
		maxTokens = value
	} else if value, ok := bridgeIntegerValue(clientBody["max_completion_tokens"]); ok {
		maxTokens = value
	} else if options.MaxTokens != nil {
		maxTokens = *options.MaxTokens
	}
	if maxTokens > 0 {
		generationConfig["maxOutputTokens"] = maxTokens
	}
	if stop := stopSequencesValue(clientBody["stop"]); len(stop) > 0 {
		generationConfig["stopSequences"] = stop
	}
	if len(generationConfig) > 0 {
		output["generationConfig"] = generationConfig
	}
	if tools := chatToolsToGeminiFunctionDeclarations(clientBody["tools"]); len(tools) > 0 {
		output["tools"] = []any{map[string]any{"functionDeclarations": tools}}
		if choice := chatToolChoiceToGeminiToolConfig(clientBody["tool_choice"], hasOwnKey(clientBody, "tool_choice")); choice != nil {
			output["toolConfig"] = choice
		}
	}
	_ = model
	return output, nil
}

func chatToolCallsToGeminiFunctionCalls(value any) []any {
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil
	}
	parts := []any{}
	for _, item := range items {
		call := bridgeObjectValue(item)
		if call == nil {
			continue
		}
		fn := bridgeObjectValue(call["function"])
		if fn == nil || bridgeStringValue(fn["name"]) == "" {
			continue
		}
		parts = append(parts, map[string]any{
			"functionCall": map[string]any{
				"name": bridgeStringValue(fn["name"]),
				"args": anthropicToolInputFromOpenAIArguments(fn["arguments"]),
			},
		})
	}
	return parts
}

func chatToolsToGeminiFunctionDeclarations(value any) []any {
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil
	}
	declarations := []any{}
	for _, item := range items {
		tool := bridgeObjectValue(item)
		if tool == nil || bridgeStringValue(tool["type"]) != "function" {
			continue
		}
		fn := bridgeObjectValue(tool["function"])
		if fn == nil {
			continue
		}
		name := bridgeStringValue(fn["name"])
		if name == "" {
			continue
		}
		parameters, ok := fn["parameters"].(map[string]any)
		if !ok {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		declarations = append(declarations, map[string]any{
			"name":        name,
			"description": bridgeStringValue(fn["description"]),
			"parameters":  parameters,
		})
	}
	return declarations
}

func chatToolChoiceToGeminiToolConfig(value any, hasToolChoice bool) any {
	if !hasToolChoice || value == nil {
		return nil
	}
	mode := "AUTO"
	allowedName := ""
	switch typed := value.(type) {
	case string:
		switch typed {
		case "none":
			mode = "NONE"
		case "required":
			mode = "ANY"
		}
	case map[string]any:
		switch bridgeStringValue(typed["type"]) {
		case "none":
			mode = "NONE"
		case "required":
			mode = "ANY"
		case "function":
			mode = "ANY"
			fn := bridgeObjectValue(typed["function"])
			if fn != nil {
				allowedName = bridgeStringValue(fn["name"])
			}
		}
	}
	functionCallingConfig := map[string]any{"mode": mode}
	if allowedName != "" {
		functionCallingConfig["allowedFunctionNames"] = []string{allowedName}
	}
	return map[string]any{"functionCallingConfig": functionCallingConfig}
}
