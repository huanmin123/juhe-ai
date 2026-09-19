package openaicompat

import (
	"context"
	"encoding/base64"
	"regexp"
	"strings"
)

// ---------------------------------------------------------------------------
// OpenAI Chat -> Anthropic Messages (openai-anthropic-bridge.ts)
// ---------------------------------------------------------------------------

// BuildOpenAIChatToAnthropicMessagesBody mirrors chatBodyToAnthropicMessages +
// baseAnthropicBody + tools/thinking application.
func BuildOpenAIChatToAnthropicMessagesBody(clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	model := options.ModelOverride
	if model == "" {
		model = bridgeStringValue(clientBody["model"])
	}
	if model == "" {
		return nil, bridgeValidationError("OpenAI 到 Anthropic 桥接请求缺少 model", "openai_anthropic_bridge_missing_model")
	}
	if err := validateOpenAIToAnthropicLegacyChatFunctionFields(FamilyChatCompletions, clientBody); err != nil {
		return nil, err
	}
	if err := validateOpenAIToAnthropicReasoningOptions(clientBody); err != nil {
		return nil, err
	}

	messages := []any{}
	systemParts := []string{}
	toolHistory := newOpenAIToolResultHistory()
	inputMessages, _ := bridgeIsArray(clientBody["messages"])
	for _, item := range inputMessages {
		message := bridgeObjectValue(item)
		if message == nil {
			continue
		}
		role := bridgeStringValue(message["role"])
		switch role {
		case "system", "developer":
			appendSystemText(&systemParts, openAIContentToText(message["content"]))
			continue
		case "tool":
			callID, err := validateOpenAIToolResultHistory(toolHistory, firstNonEmpty(
				bridgeStringValue(message["tool_call_id"]), bridgeStringValue(message["id"])))
			if err != nil {
				return nil, err
			}
			messages = appendAnthropicMessage(messages, map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type":        "tool_result",
					"tool_use_id": callID,
					"content":     openAIContentToText(message["content"]),
				}},
			})
			continue
		case "user", "assistant":
		default:
			continue
		}
		content, err := openAIChatContentToAnthropicBlocks(message["content"], &options)
		if err != nil {
			return nil, err
		}
		content = withOpenAIChatMessageNamePrefix(content, bridgeStringValue(message["name"]))
		if role == "assistant" {
			toolUseBlocks := chatToolCallsToAnthropicToolUseBlocks(message["tool_calls"])
			rememberAnthropicToolUseBlocks(toolHistory, toolUseBlocks)
			content = append(content, toolUseBlocks...)
		}
		if len(content) == 0 {
			content = []any{map[string]any{"type": "text", "text": ""}}
		}
		messages = appendAnthropicMessage(messages, map[string]any{"role": role, "content": content})
	}
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
	tools, toolChoice, err := chatToolsToAnthropicTools(clientBody["tools"], hasOwnKey(clientBody, "tool_choice"), clientBody["tool_choice"])
	if err != nil {
		return nil, err
	}
	applyTools(output, tools, toolChoice, clientBody["parallel_tool_calls"] == false)
	if err := validateAnthropicThinkingToolChoiceCompatibility(output); err != nil {
		return nil, err
	}
	return output, nil
}

// baseAnthropicBody mirrors baseAnthropicBody.
func baseAnthropicBody(body map[string]any, model string, options BridgeRequestBodyOptions) map[string]any {
	output := map[string]any{}
	output["model"] = model
	maxTokens := int64(0)
	foundMaxTokens := false
	for _, candidate := range []any{body["max_tokens"], body["max_completion_tokens"], body["max_output_tokens"]} {
		if value, ok := bridgeIntegerValue(candidate); ok {
			maxTokens = value
			foundMaxTokens = true
			break
		}
	}
	if !foundMaxTokens {
		if options.DefaultMaxTokens != nil {
			maxTokens = *options.DefaultMaxTokens
		} else {
			maxTokens = defaultAnthropicMaxTokens
		}
	}
	output["max_tokens"] = maxTokens
	output["stream"] = body["stream"] == true
	if temperature, ok := bridgeNumberValue(body["temperature"]); ok {
		output["temperature"] = temperature
	}
	if topP, ok := bridgeNumberValue(body["top_p"]); ok {
		output["top_p"] = topP
	}
	if stopSequences := stopSequencesValue(body["stop"]); len(stopSequences) > 0 {
		output["stop_sequences"] = stopSequences
	}
	user := firstNonEmpty(bridgeStringValue(body["safety_identifier"]), bridgeStringValue(body["user"]))
	if user != "" {
		output["metadata"] = map[string]any{"user_id": user}
	}
	applyAnthropicThinkingFromOpenAIReasoning(output, body)
	return output
}

// reasoningEffortFromOpenAIBody mirrors reasoningEffortFromOpenAIBody: the
// responses reasoning.effort object wins over the chat reasoning_effort field.
func reasoningEffortFromOpenAIBody(body map[string]any) string {
	if reasoning := bridgeObjectValue(body["reasoning"]); reasoning != nil {
		if effort := bridgeStringValue(reasoning["effort"]); effort != "" {
			return effort
		}
	}
	return bridgeStringValue(body["reasoning_effort"])
}

func hasOpenAIReasoningEffortRequest(body map[string]any) bool {
	if reasoning := bridgeObjectValue(body["reasoning"]); reasoning != nil && hasOwnKey(reasoning, "effort") {
		return true
	}
	return hasOwnKey(body, "reasoning_effort")
}

func reasoningSummaryFromOpenAIBody(body map[string]any) string {
	if reasoning := bridgeObjectValue(body["reasoning"]); reasoning != nil {
		return bridgeStringValue(reasoning["summary"])
	}
	return ""
}

func hasOpenAIReasoningSummaryRequest(body map[string]any) bool {
	if reasoning := bridgeObjectValue(body["reasoning"]); reasoning != nil {
		return hasOwnKey(reasoning, "summary")
	}
	return false
}

// validateOpenAIToAnthropicReasoningOptions mirrors the same Node validator.
func validateOpenAIToAnthropicReasoningOptions(body map[string]any) error {
	effort := reasoningEffortFromOpenAIBody(body)
	if hasOpenAIReasoningEffortRequest(body) && (effort == "" || !supportedAnthropicBridgeReasoningEfforts[effort]) {
		return bridgeValidationError(
			"OpenAI 到 Anthropic 桥接只支持 reasoning.effort / reasoning_effort 为 none、minimal、low、medium、high、xhigh、max",
			"openai_anthropic_bridge_reasoning_effort_unsupported")
	}
	summary := reasoningSummaryFromOpenAIBody(body)
	if hasOpenAIReasoningSummaryRequest(body) && (summary == "" || !supportedAnthropicBridgeReasoningSummaries[summary]) {
		return bridgeValidationError(
			"OpenAI 到 Anthropic 桥接当前只支持 reasoning.summary 为 auto、concise、detailed、none；未知 summary 会导致客户端误判 reasoning 输出形态",
			"openai_anthropic_bridge_reasoning_summary_unsupported")
	}
	return nil
}

// applyAnthropicThinkingFromOpenAIReasoning mirrors the same Node helper.
func applyAnthropicThinkingFromOpenAIReasoning(output, body map[string]any) {
	effort := reasoningEffortFromOpenAIBody(body)
	if effort == "" || effort == "none" {
		return
	}
	switch effort {
	case "low", "medium", "high", "xhigh", "max":
		output["thinking"] = map[string]any{"type": "adaptive"}
		outputConfig := bridgeObjectValue(output["output_config"])
		if outputConfig == nil {
			outputConfig = map[string]any{}
		}
		outputConfig["effort"] = effort
		output["output_config"] = outputConfig
		return
	}
	budgetTokens := anthropicThinkingBudgetTokens(effort)
	if budgetTokens == 0 {
		return
	}
	maxTokens := defaultAnthropicMaxTokens
	if value, ok := bridgeIntegerValue(output["max_tokens"]); ok {
		maxTokens = value
	}
	if maxTokens <= budgetTokens {
		output["max_tokens"] = budgetTokens + 1024
	}
	output["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budgetTokens}
}

func anthropicThinkingBudgetTokens(effort string) int64 {
	switch strings.ToLower(effort) {
	case "minimal":
		return 1024
	case "low":
		return 2048
	case "medium":
		return 4096
	case "high":
		return 8192
	default:
		return 0
	}
}

// validateOpenAIToAnthropicLegacyChatFunctionFields mirrors the same Node validator.
func validateOpenAIToAnthropicLegacyChatFunctionFields(sourceEndpointFamily string, body map[string]any) error {
	if sourceEndpointFamily != FamilyChatCompletions {
		return nil
	}
	if hasOwnKey(body, "functions") || hasOwnKey(body, "function_call") {
		return bridgeValidationError(
			"OpenAI Chat legacy functions/function_call 已移除；请使用 tools/tool_choice",
			"openai_anthropic_bridge_legacy_chat_functions_unsupported")
	}
	messages, _ := bridgeIsArray(body["messages"])
	for _, item := range messages {
		message := bridgeObjectValue(item)
		if message == nil {
			continue
		}
		if bridgeStringValue(message["role"]) == "function" || hasOwnKey(message, "function_call") {
			return bridgeValidationError(
				"OpenAI Chat legacy role=function / assistant.function_call 已移除；请使用 role=tool / assistant.tool_calls",
				"openai_anthropic_bridge_legacy_chat_function_messages_unsupported")
		}
	}
	return nil
}

// openAIContentToText mirrors openAIContentToText.
func openAIContentToText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	items, ok := bridgeIsArray(value)
	if !ok {
		return ""
	}
	parts := []string{}
	for _, item := range items {
		part := bridgeObjectValue(item)
		if part == nil {
			continue
		}
		partType := bridgeStringValue(part["type"])
		if partType == "text" || partType == "input_text" || partType == "output_text" || partType == "" {
			if text := bridgeStringValue(part["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// openAIChatContentToAnthropicBlocks mirrors openAIChatContentToAnthropicBlocks
// (string / text part / image_url part / audio unsupported / others rejected;
// file parts resolve through the FileResolver port when present).
func openAIChatContentToAnthropicBlocks(value any, options *BridgeRequestBodyOptions) ([]any, error) {
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
		case "text":
			if text := bridgeStringValue(part["text"]); text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
		case "image_url":
			imageURL := imageURLFromOpenAIImagePart(part["image_url"])
			if imageURL == "" {
				return nil, bridgeValidationError(
					"Chat image_url 缺少可桥接的 url，当前 OpenAI 到 Anthropic 桥接只支持 URL 或 data URL 图片输入",
					"openai_anthropic_bridge_unsupported_image_reference")
			}
			block, err := anthropicImageBlockFromURL(imageURL)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		case "file", "input_file":
			block, err := anthropicDocumentBlockFromOpenAIFilePart(part, options)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		case "input_audio", "audio":
			return nil, bridgeValidationError(
				"Chat input_audio 当前不能桥接到 Anthropic Messages；请使用可消费音频输入的原生 OpenAI 上游，或先在客户端 / 本地运行时转写为文本",
				"openai_anthropic_bridge_audio_input_unsupported")
		default:
			typeLabel := bridgeStringValue(part["type"])
			if typeLabel == "" {
				typeLabel = "unknown"
			}
			return nil, bridgeValidationError(
				"Chat content block "+typeLabel+" 当前没有 OpenAI 到 Anthropic Messages 的等价映射；请先补能力矩阵和 mock 回归后再启用",
				"openai_anthropic_bridge_unsupported_content_part")
		}
	}
	return blocks, nil
}

func imageURLFromOpenAIImagePart(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if object := bridgeObjectValue(value); object != nil {
		return bridgeStringValue(object["url"])
	}
	return ""
}

var bridgeDataURLPattern = regexp.MustCompile(`(?is)^data:([^;,]+)(?:;[^,]*)?;base64,(.+)$`)

var anthropicSupportedImageMediaTypes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true,
}

// anthropicImageBlockFromURL mirrors anthropicImageBlockFromUrl.
func anthropicImageBlockFromURL(url string) (map[string]any, error) {
	if match := bridgeDataURLPattern.FindStringSubmatch(url); match != nil {
		mediaType := strings.ToLower(match[1])
		if !anthropicSupportedImageMediaTypes[mediaType] {
			return nil, bridgeValidationError(
				"OpenAI 到 Anthropic 桥接当前只支持 image/jpeg、image/png、image/gif、image/webp 图片 data URL，收到 "+match[1],
				"openai_anthropic_bridge_unsupported_image_media_type")
		}
		base64Data := strings.TrimSpace(match[2])
		if base64Data == "" {
			return nil, bridgeValidationError("OpenAI 图片 data URL 不是合法 base64 数据",
				"openai_anthropic_bridge_invalid_image_base64")
		}
		if _, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(base64Data), "")); err != nil {
			return nil, bridgeValidationError("OpenAI 图片 data URL 不是合法 base64 数据",
				"openai_anthropic_bridge_invalid_image_base64")
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mediaType,
				"data":       base64Data,
			},
		}, nil
	}
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type": "url",
			"url":  url,
		},
	}, nil
}

// anthropicDocumentBlockFromOpenAIFilePart mirrors
// anthropicDocumentBlockFromOpenAIFilePart for the file_id / file_data shapes.
// Remote file URL parts and resolver misses surface the Node bridge errors.
func anthropicDocumentBlockFromOpenAIFilePart(part map[string]any, options *BridgeRequestBodyOptions) (map[string]any, error) {
	if fileID := bridgeStringValue(part["file_id"]); fileID != "" {
		return anthropicDocumentBlockFromOpenAIFileID(fileID, options)
	}
	if fileData := bridgeObjectValue(part["file_data"]); fileData != nil {
		return anthropicDocumentBlockFromOpenAIFileData(fileData)
	}
	return nil, bridgeValidationError(
		"OpenAI file part 缺少 file_id 或 file_data，当前 OpenAI 到 Anthropic 桥接只支持已上传文件与内联文件数据",
		"openai_anthropic_bridge_unsupported_file_reference")
}

func anthropicDocumentBlockFromOpenAIFileID(fileID string, options *BridgeRequestBodyOptions) (map[string]any, error) {
	if options == nil || options.FileResolver == nil {
		return nil, bridgeValidationError(
			"OpenAI file_id 引用需要网关文件解析器；当前上游不能解析该文件引用",
			"openai_anthropic_bridge_file_resolver_unavailable")
	}
	resolved, err := options.FileResolver.ResolveFile(context.Background(), FileResolveInput{FileID: fileID})
	if err != nil {
		return nil, err
	}
	if resolved == nil {
		return nil, bridgeValidationError(
			"OpenAI file_id "+fileID+" 不存在或当前凭据不可见",
			"openai_anthropic_bridge_file_not_found")
	}
	return anthropicDocumentBlockFromResolvedOpenAIFile(resolved)
}

func anthropicDocumentBlockFromOpenAIFileData(fileData map[string]any) (map[string]any, error) {
	filename := bridgeStringValue(fileData["filename"])
	mediaType := firstNonEmpty(bridgeStringValue(fileData["format"]), bridgeStringValue(fileData["mime_type"]))
	if strings.HasPrefix(mediaType, ".") {
		mediaType = bridgeMediaTypeFromExtension(mediaType)
	}
	if data := bridgeStringValue(fileData["file_data"]); data != "" {
		block, err := anthropicDocumentBlockFromBase64(filename, mediaType, data)
		if err != nil {
			return nil, err
		}
		return block, nil
	}
	return nil, bridgeValidationError(
		"OpenAI file_data 缺少可桥接的文件内容",
		"openai_anthropic_bridge_invalid_file_data")
}

func anthropicDocumentBlockFromResolvedOpenAIFile(resolved *ResolvedFile) (map[string]any, error) {
	if resolved.ContentBase64 != "" {
		return anthropicDocumentBlockFromBase64(resolved.Filename, resolved.MediaType, resolved.ContentBase64)
	}
	return anthropicDocumentBlockFromText(resolved.Filename, resolved.ContentText)
}

func anthropicDocumentBlockFromBase64(filename, mediaType, data string) (map[string]any, error) {
	source := map[string]any{"type": "base64", "data": strings.Join(strings.Fields(data), "")}
	if mediaType != "" {
		source["media_type"] = mediaType
	}
	block := map[string]any{"type": "document", "source": source}
	if filename != "" {
		block["title"] = filename
	}
	return block, nil
}

func anthropicDocumentBlockFromText(filename, text string) (map[string]any, error) {
	source := map[string]any{"type": "text", "data": text}
	if mediaType := "text/plain"; mediaType != "" {
		source["media_type"] = mediaType
	}
	block := map[string]any{"type": "document", "source": source}
	if filename != "" {
		block["title"] = filename
	}
	return block, nil
}

func bridgeMediaTypeFromExtension(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "pdf":
		return "application/pdf"
	case "txt", "text":
		return "text/plain"
	case "md":
		return "text/markdown"
	case "csv":
		return "text/csv"
	case "json":
		return "application/json"
	default:
		return ""
	}
}

// chatToolCallsToAnthropicToolUseBlocks mirrors chatToolCallsToAnthropicToolUseBlocks.
func chatToolCallsToAnthropicToolUseBlocks(value any) []any {
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil
	}
	blocks := []any{}
	for _, item := range items {
		call := bridgeObjectValue(item)
		if call == nil {
			continue
		}
		fn := bridgeObjectValue(call["function"])
		name := ""
		if fn != nil {
			name = bridgeStringValue(fn["name"])
		}
		id := bridgeStringValue(call["id"])
		if name == "" || id == "" {
			continue
		}
		blocks = append(blocks, map[string]any{
			"type":  "tool_use",
			"id":    id,
			"name":  name,
			"input": anthropicToolInputFromOpenAIArguments(fn["arguments"]),
		})
	}
	return blocks
}

// anthropicToolInputFromOpenAIArguments mirrors the same Node helper: parse the
// JSON arguments string, falling back to `{}` / `{_raw}`.
func anthropicToolInputFromOpenAIArguments(value any) any {
	text, _ := value.(string)
	return bridgeParseToolArguments(text)
}

// chatToolsToAnthropicTools mirrors chatToolsToAnthropicTools + allowed
// function tool filtering (tool_choice type=allowed_tools keeps the direct
// names only; namespace-qualified responses tools are not part of chat).
func chatToolsToAnthropicTools(value any, hasToolChoice bool, toolChoiceValue any) ([]any, any, error) {
	items, ok := bridgeIsArray(value)
	if !ok {
		return nil, nil, nil
	}
	allowed := allowedOpenAIFunctionTools(toolChoiceValue)
	tools := []any{}
	for _, item := range items {
		tool := bridgeObjectValue(item)
		if tool == nil || bridgeStringValue(tool["type"]) != "function" {
			return nil, nil, unsupportedOpenAIToolError(item)
		}
		fn := bridgeObjectValue(tool["function"])
		name := ""
		if fn != nil {
			name = bridgeStringValue(fn["name"])
		}
		if name == "" {
			return nil, nil, bridgeValidationError("function tool 缺少 name", "openai_anthropic_bridge_invalid_tool")
		}
		if !openAIFunctionToolAllowed(allowed, name) {
			continue
		}
		tools = append(tools, anthropicToolFromFunctionDefinition(name, fn["description"], fn["parameters"]))
	}
	choice := anthropicToolChoiceFromOpenAI(toolChoiceValue, hasToolChoice)
	return tools, choice, nil
}

func anthropicToolFromFunctionDefinition(name string, description, parameters any) map[string]any {
	descriptionText := bridgeStringValue(description)
	schema, ok := parameters.(map[string]any)
	if !ok {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{
		"name":         name,
		"description":  descriptionText,
		"input_schema": schema,
	}
}

// applyTools mirrors applyTools.
func applyTools(output map[string]any, tools []any, toolChoice any, disableParallelToolUse bool) {
	if len(tools) == 0 {
		return
	}
	output["tools"] = tools
	if choice, ok := toolChoice.(map[string]any); ok && len(choice) > 0 {
		if disableParallelToolUse && bridgeStringValue(choice["type"]) != "none" {
			choice["disable_parallel_tool_use"] = true
		}
		output["tool_choice"] = choice
	} else if disableParallelToolUse {
		output["tool_choice"] = map[string]any{"type": "auto", "disable_parallel_tool_use": true}
	}
}

// anthropicToolChoiceFromOpenAI mirrors anthropicToolChoiceFromOpenAI.
func anthropicToolChoiceFromOpenAI(value any, hasToolChoice bool) any {
	if !hasToolChoice || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string:
		switch typed {
		case "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "none"}
		case "required":
			return map[string]any{"type": "any"}
		default:
			return nil
		}
	case map[string]any:
		switch bridgeStringValue(typed["type"]) {
		case "function":
			fn := bridgeObjectValue(typed["function"])
			name := ""
			if fn != nil {
				name = bridgeStringValue(fn["name"])
			}
			if name == "" {
				name = bridgeStringValue(typed["name"])
			}
			if name == "" {
				return nil
			}
			return map[string]any{"type": "tool", "name": name}
		case "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "none"}
		case "required":
			return map[string]any{"type": "any"}
		case "allowed_tools":
			if bridgeStringValue(typed["mode"]) == "required" {
				return map[string]any{"type": "any"}
			}
			return map[string]any{"type": "auto"}
		default:
			return nil
		}
	default:
		return nil
	}
}

// allowedOpenAIFunctionTools mirrors allowedOpenAIFunctionTools.
type openAIAllowedFunctionTools struct {
	directNames map[string]bool
}

func allowedOpenAIFunctionTools(toolChoice any) *openAIAllowedFunctionTools {
	choice, ok := toolChoice.(map[string]any)
	if !ok || bridgeStringValue(choice["type"]) != "allowed_tools" {
		return nil
	}
	allowed := &openAIAllowedFunctionTools{directNames: map[string]bool{}}
	tools, _ := bridgeIsArray(choice["tools"])
	for _, tool := range tools {
		if name, ok := tool.(string); ok {
			allowed.directNames[name] = true
			continue
		}
		toolMap := bridgeObjectValue(tool)
		if toolMap == nil || bridgeStringValue(toolMap["type"]) != "function" {
			continue
		}
		fn := bridgeObjectValue(toolMap["function"])
		name := bridgeStringValue(toolMap["name"])
		if name == "" && fn != nil {
			name = bridgeStringValue(fn["name"])
		}
		if name != "" {
			allowed.directNames[name] = true
		}
	}
	return allowed
}

func openAIFunctionToolAllowed(allowed *openAIAllowedFunctionTools, name string) bool {
	if allowed == nil {
		return true
	}
	return allowed.directNames[name]
}

func unsupportedOpenAIToolError(tool any) error {
	toolMap, _ := tool.(map[string]any)
	typeLabel := ""
	if toolMap != nil {
		typeLabel = bridgeStringValue(toolMap["type"])
	}
	if typeLabel == "" {
		typeLabel = "unknown"
	}
	return bridgeValidationError(
		"OpenAI tool 类型 "+typeLabel+" 当前没有 OpenAI 到 Anthropic Messages 的等价映射；请移除该工具或改用原生 OpenAI 上游",
		"openai_anthropic_bridge_unsupported_tool")
}

// validateAnthropicThinkingToolChoiceCompatibility mirrors the same Node validator.
func validateAnthropicThinkingToolChoiceCompatibility(output map[string]any) error {
	if !bridgeIsPlainObject(output["thinking"]) {
		return nil
	}
	choice, _ := output["tool_choice"].(map[string]any)
	choiceType := ""
	if choice != nil {
		choiceType = bridgeStringValue(choice["type"])
	}
	if choiceType != "any" && choiceType != "tool" {
		return nil
	}
	return bridgeValidationError(
		"Anthropic Messages 不支持同时启用 thinking 和强制工具调用；请关闭 reasoning / thinking，或把 tool_choice 改为 auto / none",
		"openai_anthropic_bridge_thinking_forced_tool_choice_unsupported")
}

// withOpenAIChatMessageNamePrefix mirrors withOpenAIChatMessageNamePrefix.
func withOpenAIChatMessageNamePrefix(blocks []any, rawName string) []any {
	name := normalizeBridgeWhitespace(rawName)
	if name == "" {
		return blocks
	}
	prefix := "参与者: " + name
	if len(blocks) == 0 {
		return []any{map[string]any{"type": "text", "text": prefix}}
	}
	first := bridgeObjectValue(blocks[0])
	if first != nil && bridgeStringValue(first["type"]) == "text" {
		text := bridgeStringValue(first["text"])
		if text != "" {
			first["text"] = prefix + "\n" + text
		} else {
			first["text"] = prefix
		}
		return blocks
	}
	return append([]any{map[string]any{"type": "text", "text": prefix}}, blocks...)
}
