package openaicompat

import (
	"context"
	"encoding/base64"
	"regexp"
	"strings"
)

// B-4 bridge request builders (D-149/D-157). Faithful ports of the archived
// Node conversion cores:
//
//	openai-anthropic-bridge.ts        buildOpenAIToAnthropicBridgeBody
//	                                  (chatBodyToAnthropicMessages +
//	                                  responsesBodyToAnthropicMessages +
//	                                  baseAnthropicBody + tools/thinking)
//	gemini-openai-chat-bridge.ts      buildGeminiGenerateContentChatBridgeBody
//	anthropic-openai-chat-bridge.ts   buildAnthropicMessagesChatBridgeBody
//	codex-responses-chat-bridge.ts    buildCodexResponsesChatBridgeBody
//	gemini-anthropic-messages path    (chat -> gemini generateContent builder
//	                                  kept on the openai-anthropic-gemini-native
//	                                  surface: BuildOpenAIChatToGeminiBody)
//
// Local-runtime hosted tool emulation (code interpreter / computer use /
// image generation executors) stays behind the existing executor ports and is
// not re-implemented here; unsupported hosted tools surface the same guidance
// error codes as Node.

// BridgeRequestBodyOptions carries the per-request bridge options shared by
// the Node BuildXBridgeBody option bags.
type BridgeRequestBodyOptions struct {
	DefaultModel         string
	GuidanceProviderName string
	ModelOverride        string
	TargetPathAndQuery   string
	ClientCompatibility  map[string]any
	MaxTokens            *int64
	Temperature          *float64
	// Stream mirrors requestStream(req): the effective client stream flag.
	Stream bool
	// DefaultMaxTokens mirrors options.defaultMaxTokens (OpenAI -> Anthropic).
	DefaultMaxTokens *int64
	// FileResolver resolves OpenAI file references for document blocks
	// (openAIToAnthropicBridgeFileResolverForTest ?? options.fileResolver).
	FileResolver FileResolver
	// HostedToolModes carries the hosted tool runtime mode bag (Node
	// runtimeConfig.hostedToolRuntimes); zero value reads as guidance for
	// every type (Node default).
	HostedToolModes OpenAIHostedToolRuntimeModes

	// geminiNativeSourceFamily / geminiNativeSourceModel carry the guidance
	// render context for the gemini-native-target builders (Node
	// guidance(req, mapping) reads downstreamProtocol(mapping) and
	// mapping.sourceModel). The package dispatch and the builder entries set
	// them; direct callers leave the zero values.
	geminiNativeSourceFamily string
	geminiNativeSourceModel  string
}

// bridgeValidationError mirrors bridgeValidationError: 400 invalid_request_error.
func bridgeValidationError(message, code string) *BridgeRequestError {
	return bridgeError(message, code, 400, "invalid_request_error")
}

// providerLabel mirrors providerLabel(providerName).
func providerLabel(name string) string {
	if name == "" {
		return ""
	}
	return " " + name
}

const (
	defaultAnthropicMaxTokens = int64(4096)
	// structuredOutputSyntheticToolName mirrors the same Node constant.
	structuredOutputSyntheticToolName = "emit_structured_output"
)

var supportedAnthropicBridgeReasoningEfforts = map[string]bool{
	"none": true, "minimal": true, "low": true, "medium": true,
	"high": true, "xhigh": true, "max": true,
}

var supportedAnthropicBridgeReasoningSummaries = map[string]bool{
	"auto": true, "concise": true, "detailed": true, "none": true,
}

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

func appendSystemText(parts *[]string, value string) {
	if text := strings.TrimSpace(value); text != "" {
		*parts = append(*parts, text)
	}
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

// appendAnthropicMessage mirrors appendAnthropicMessage: same-role messages
// merge their content blocks.
func appendAnthropicMessage(messages []any, next map[string]any) []any {
	if len(messages) > 0 {
		if previous := bridgeObjectValue(messages[len(messages)-1]); previous != nil &&
			bridgeStringValue(previous["role"]) == bridgeStringValue(next["role"]) {
			previousBlocks, _ := bridgeIsArray(previous["content"])
			nextBlocks, _ := bridgeIsArray(next["content"])
			previous["content"] = append(previousBlocks, nextBlocks...)
			return messages
		}
	}
	return append(messages, next)
}

var bridgeWhitespacePattern = regexp.MustCompile(`\s+`)

func normalizeBridgeWhitespace(value string) string {
	return strings.TrimSpace(bridgeWhitespacePattern.ReplaceAllString(value, " "))
}

// stopSequencesValue mirrors stopSequencesValue: string or string[] -> []string.
func stopSequencesValue(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []any:
		out := []string{}
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func hasOwnKey(object map[string]any, key string) bool {
	if object == nil {
		return false
	}
	_, ok := object[key]
	return ok
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

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

func int64ToText(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := []byte{}
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	if negative {
		digits = append(digits, '-')
	}
	for left, right := 0, len(digits)-1; left < right; left, right = left+1, right-1 {
		digits[left], digits[right] = digits[right], digits[left]
	}
	return string(digits)
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

// ---------------------------------------------------------------------------
// Legacy entry points (kept for the existing driver call surface)
// ---------------------------------------------------------------------------

// BuildOpenAIChatBridgeBody builds the upstream bridge body for an OpenAI chat
// completions client request. The upstream family selects the builder:
// anthropic messages / gemini generateContent bodies from the archived Node
// bridges. The legacy gemini-only flattening of the previous Go revision was
// wrong for both targets and is superseded here.
func BuildOpenAIChatBridgeBody(clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	return BuildOpenAIChatToAnthropicMessagesBody(clientBody, options)
}

// BuildAnthropicMessagesBody keeps its historical name: an Anthropic messages
// body from an OpenAI chat completions request.
func BuildAnthropicMessagesBody(clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	return BuildOpenAIChatToAnthropicMessagesBody(clientBody, options)
}
