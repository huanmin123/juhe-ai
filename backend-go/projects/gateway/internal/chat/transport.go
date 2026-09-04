package chat

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Transport plumbing ported from chat-transport.ts, chat-tools.ts,
// chat-system-instructions.ts, chat-prompt-cache.ts and the parameter slices
// of chat-generation-parameters.ts.

// ChatTransportProtocol mirrors ChatTransportProtocol.
type ChatTransportProtocol string

const (
	ProtocolChatCompletions ChatTransportProtocol = "chat_completions"
	ProtocolResponses       ChatTransportProtocol = "responses"
)

// ChatHostedTool mirrors ChatHostedTool.
type ChatHostedTool = string

const HostedToolWebSearch = "web_search"

// normalizeChatHostedTools mirrors normalizeChatHostedTools.
func normalizeChatHostedTools(input []string) []string {
	selected := map[string]bool{}
	for _, value := range input {
		if value == "web_search" || value == "image_generation" {
			selected[value] = true
		}
	}
	out := []string{}
	for _, tool := range []string{"web_search", "image_generation"} {
		if selected[tool] {
			out = append(out, tool)
		}
	}
	return out
}

// mapChatHostedToolsToResponses mirrors mapChatHostedToolsToResponses.
func mapChatHostedToolsToResponses(input []string) []map[string]any {
	out := []map[string]any{}
	for _, tool := range normalizeChatHostedTools(input) {
		out = append(out, map[string]any{"type": tool})
	}
	return out
}

// ChatTransportMessage mirrors ChatTransportMessage.
type ChatTransportMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string | []transportInputBlock | raw continuation items
}

// ChatTransportInputBlock mirrors ChatTransportInputBlock.
type ChatTransportInputBlock struct {
	Type    string `json:"type"` // input_text|input_image
	Text    string `json:"text,omitempty"`
	DataURL string `json:"dataUrl,omitempty"`
}

// resolveChatBudgetContent mirrors resolveChatBudgetContent.
func resolveChatBudgetContent(protocol ChatTransportProtocol, currentContent string, blocks []ChatTransportInputBlock) string {
	if protocol != ProtocolResponses || len(blocks) == 0 {
		return currentContent
	}
	texts := []string{}
	for _, block := range blocks {
		if block.Type == "input_text" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// selectChatTransport mirrors selectChatTransport.
func selectChatTransport(supportedProtocols []ChatTransportProtocol, preferResponses bool) ChatTransportProtocol {
	has := func(protocol ChatTransportProtocol) bool {
		for _, candidate := range supportedProtocols {
			if candidate == protocol {
				return true
			}
		}
		return false
	}
	if preferResponses && has(ProtocolResponses) {
		return ProtocolResponses
	}
	if has(ProtocolChatCompletions) {
		return ProtocolChatCompletions
	}
	if has(ProtocolResponses) {
		return ProtocolResponses
	}
	return ProtocolChatCompletions
}

// ChatTransportAccount mirrors ChatTransportAccount (runtime account snapshot
// subset consumed by the transport selection).
type ChatTransportAccount struct {
	ID                     string                      `json:"id"`
	Type                   string                      `json:"type"`
	ProviderCode           string                      `json:"providerCode"`
	SupportedEndpointModes []string                    `json:"supportedEndpointModes,omitempty"`
	SupportedModels        []string                    `json:"supportedModels,omitempty"`
	ModelMappings          []ChatTransportModelMapping `json:"modelMappings,omitempty"`
}

// ChatTransportModelMapping mirrors the account model mapping subset.
type ChatTransportModelMapping struct {
	Enabled                *bool  `json:"enabled,omitempty"`
	SourceModel            string `json:"sourceModel"`
	UpstreamModel          string `json:"upstreamModel,omitempty"`
	SourceEndpointFamily   string `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily string `json:"upstreamEndpointFamily,omitempty"`
}

// resolveChatSupportedProtocols mirrors resolveChatSupportedProtocols.
func resolveChatSupportedProtocols(groupIDs []string, model string, loadAccounts func(groupID, model, endpointFamily string) []ChatTransportAccount) []ChatTransportProtocol {
	protocolOrder := []ChatTransportProtocol{ProtocolChatCompletions, ProtocolResponses}
	supported := map[ChatTransportProtocol]bool{}
	groups := uniqueStrings(groupIDs)
	for _, groupID := range groups {
		for _, protocol := range protocolOrder {
			if supported[protocol] {
				continue
			}
			accounts := loadAccounts(groupID, model, string(protocol))
			for _, account := range accounts {
				if chatTransportAccountSupportsProtocol(account, model, protocol) {
					supported[protocol] = true
					break
				}
			}
		}
		if supported[ProtocolChatCompletions] && supported[ProtocolResponses] {
			break
		}
	}
	out := []ChatTransportProtocol{}
	for _, protocol := range protocolOrder {
		if supported[protocol] {
			out = append(out, protocol)
		}
	}
	return out
}

// chatTransportAccountSupportsProtocol mirrors chatTransportAccountSupportsProtocol.
func chatTransportAccountSupportsProtocol(account ChatTransportAccount, model string, protocol ChatTransportProtocol) bool {
	var mapping *ChatTransportModelMapping
	for index := range account.ModelMappings {
		item := &account.ModelMappings[index]
		if item.Enabled != nil && !*item.Enabled {
			continue
		}
		if item.SourceModel == model && (item.SourceEndpointFamily == "" || item.SourceEndpointFamily == string(protocol)) {
			mapping = item
			break
		}
	}
	supportedModels := account.SupportedModels
	if len(supportedModels) > 0 {
		routedModel := model
		if mapping != nil && mapping.UpstreamModel != "" {
			routedModel = mapping.UpstreamModel
		}
		found := false
		for _, candidate := range supportedModels {
			if candidate == routedModel {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	upstreamProtocol := protocol
	if mapping != nil && mapping.UpstreamEndpointFamily != "" {
		upstreamProtocol = ChatTransportProtocol(mapping.UpstreamEndpointFamily)
	}
	requiredMode := ""
	switch upstreamProtocol {
	case ProtocolResponses:
		requiredMode = "responses_sse"
	case ProtocolChatCompletions:
		requiredMode = "chat_sse"
	case "messages":
		requiredMode = "messages_sse"
	case "generate_content":
		requiredMode = "generate_content_sse"
	}
	if requiredMode == "" {
		return false
	}
	for _, mode := range account.SupportedEndpointModes {
		if mode == requiredMode {
			return true
		}
	}
	return false
}

// ChatGenerationParameters mirrors ChatGenerationParameters.
type ChatGenerationParameters struct {
	Temperature      *float64
	TopP             *float64
	FrequencyPenalty *float64
	PresencePenalty  *float64
	MaxOutputTokens  *float64
	Seed             *float64
}

// transportGenerationParameters mirrors transportGenerationParameters.
func transportGenerationParameters(protocol ChatTransportProtocol, input *ChatGenerationParameters) map[string]any {
	out := map[string]any{}
	if input == nil {
		return out
	}
	set := func(key string, value *float64) {
		if value != nil {
			out[key] = *value
		}
	}
	set("temperature", input.Temperature)
	set("top_p", input.TopP)
	if protocol == ProtocolResponses {
		set("max_output_tokens", input.MaxOutputTokens)
		return out
	}
	set("frequency_penalty", input.FrequencyPenalty)
	set("presence_penalty", input.PresencePenalty)
	set("max_completion_tokens", input.MaxOutputTokens)
	set("seed", input.Seed)
	return out
}

// toolDefinition mirrors ChatInternalToolDefinition minus the executor (the
// executor lives in generation_tools.go).
type toolDefinition struct {
	ID                             string
	Version                        string
	ModelName                      string
	Description                    string
	InputSchema                    map[string]any
	MaxArgumentBytes               int
	MaxResultBytes                 int
	TimeoutMs                      int64
	RequiresInternalToolsEnabled   bool
	RequiresImageGenerationEnabled bool
	Environments                   []string
	DuplicatePolicy                string // reuse_exact | allow_repeat
	Execute                        func(input map[string]any, ctx *chatToolExecutionContext) (chatToolExecutionResult, error)
}

// chatToolExecutionContext mirrors ChatToolExecutionContext (transport subset).
type chatToolExecutionContext struct {
	OwnerID                 string
	ConversationID          string
	TurnID                  string
	AssistantMessageID      string
	TraceID                 string
	APIKey                  string
	DefaultImageModel       string
	Aborted                 func() bool
	LoadImageEditReferences func(assetIDs []string) ([]ChatImageEditReference, error)
	ImageGeneration         func(input ChatImageGenerationRequest) (ChatImageGenerationToolResult, error)
	ArtifactSink            ChatGeneratedImageArtifactSink
}

// compileChatInternalTools mirrors compileChatInternalTools.
func compileChatInternalTools(protocol ChatTransportProtocol, tools []*toolDefinition) []map[string]any {
	out := []map[string]any{}
	for _, tool := range tools {
		if protocol == ProtocolResponses {
			out = append(out, map[string]any{
				"type":        "function",
				"name":        tool.ModelName,
				"description": tool.Description,
				"parameters":  tool.InputSchema,
				"strict":      false,
			})
			continue
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.ModelName,
				"description": tool.Description,
				"parameters":  tool.InputSchema,
				"strict":      false,
			},
		})
	}
	return out
}

// buildChatToolContinuation mirrors buildChatToolContinuation.
func buildChatToolContinuation(protocol ChatTransportProtocol, continuationItems []any, outputs []ChatToolExecutionOutput) []any {
	out := append([]any{}, continuationItems...)
	for _, output := range outputs {
		if protocol == ProtocolResponses {
			out = append(out, map[string]any{
				"type":    "function_call_output",
				"call_id": output.CallID,
				"output":  output.ModelOutput,
			})
			continue
		}
		out = append(out, map[string]any{
			"role":         "tool",
			"tool_call_id": output.CallID,
			"content":      output.ModelOutput,
		})
	}
	return out
}

// buildChatTransportRequest mirrors buildChatTransportRequest.
func buildChatTransportRequest(input ChatTransportRequestInput) (string, map[string]any) {
	if input.Protocol == ProtocolResponses {
		internalTools := compileChatInternalTools(ProtocolResponses, input.InternalTools)
		tools := append(mapChatHostedToolsToResponses(input.EffectiveTools), internalTools...)
		currentBlocks := input.CurrentBlocks
		if len(currentBlocks) == 0 {
			currentBlocks = []ChatTransportInputBlock{{Type: "input_text", Text: input.CurrentContent}}
		}
		responsesInput := []any{}
		for _, message := range input.History {
			responsesInput = append(responsesInput, map[string]any{
				"role":    message.Role,
				"content": toResponsesMessageContent(message),
			})
		}
		currentContentBlocks := []any{}
		for _, block := range currentBlocks {
			if block.Type == "input_image" {
				currentContentBlocks = append(currentContentBlocks, map[string]any{"type": "input_image", "image_url": block.DataURL, "detail": "high"})
				continue
			}
			currentContentBlocks = append(currentContentBlocks, map[string]any{"type": "input_text", "text": block.Text})
		}
		responsesInput = append(responsesInput, map[string]any{"role": "user", "content": currentContentBlocks})
		responsesInput = append(responsesInput, input.ToolContinuation...)
		body := map[string]any{
			"model":        input.Model,
			"instructions": input.Instructions,
			"input":        responsesInput,
			"stream":       true,
		}
		if input.ReasoningEffort != "" {
			body["reasoning"] = map[string]any{"effort": input.ReasoningEffort, "summary": "auto"}
		}
		if input.ServiceTier != "" {
			body["service_tier"] = input.ServiceTier
		}
		for key, value := range transportGenerationParameters(ProtocolResponses, input.GenerationParameters) {
			body[key] = value
		}
		if input.PromptCacheKey != "" {
			body["prompt_cache_key"] = input.PromptCacheKey
		}
		if len(tools) > 0 {
			body["tools"] = tools
			body["tool_choice"] = "auto"
		}
		if len(internalTools) > 0 {
			body["parallel_tool_calls"] = false
		}
		return "/v1/responses", body
	}
	messages := []any{map[string]any{"role": "system", "content": input.Instructions}}
	for _, message := range input.History {
		messages = append(messages, map[string]any{"role": message.Role, "content": message.Content})
	}
	messages = append(messages, map[string]any{"role": "user", "content": input.CurrentContent})
	messages = append(messages, input.ToolContinuation...)
	internalTools := compileChatInternalTools(ProtocolChatCompletions, input.InternalTools)
	body := map[string]any{
		"model":          input.Model,
		"messages":       messages,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if input.ReasoningEffort != "" {
		body["reasoning_effort"] = input.ReasoningEffort
	}
	if input.ServiceTier != "" {
		body["service_tier"] = input.ServiceTier
	}
	for key, value := range transportGenerationParameters(ProtocolChatCompletions, input.GenerationParameters) {
		body[key] = value
	}
	if input.PromptCacheKey != "" {
		body["prompt_cache_key"] = input.PromptCacheKey
	}
	if len(internalTools) > 0 {
		body["tools"] = internalTools
		body["tool_choice"] = "auto"
		body["parallel_tool_calls"] = false
	}
	return "/v1/chat/completions", body
}

// ChatTransportRequestInput mirrors buildChatTransportRequest input.
type ChatTransportRequestInput struct {
	Protocol             ChatTransportProtocol
	Instructions         string
	Model                string
	History              []ChatTransportMessage
	CurrentContent       string
	CurrentBlocks        []ChatTransportInputBlock
	EffectiveTools       []string
	InternalTools        []*toolDefinition
	ToolContinuation     []any
	ReasoningEffort      string
	ServiceTier          string
	GenerationParameters *ChatGenerationParameters
	PromptCacheKey       string
}

func toResponsesMessageContent(message ChatTransportMessage) any {
	switch content := message.Content.(type) {
	case string:
		if message.Role == "user" {
			return toResponsesBlocks([]ChatTransportInputBlock{{Type: "input_text", Text: content}})
		}
		return content
	case []ChatTransportInputBlock:
		return toResponsesBlocks(content)
	default:
		return message.Content
	}
}

func toResponsesBlocks(blocks []ChatTransportInputBlock) []any {
	out := make([]any, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "input_image" {
			out = append(out, map[string]any{"type": "input_image", "image_url": block.DataURL, "detail": "high"})
			continue
		}
		out = append(out, map[string]any{"type": "input_text", "text": block.Text})
	}
	return out
}

// --- system instructions (chat-system-instructions.ts) ---

const chatSystemInstructionsVersion = "chat-system-v4"

const instructionPriority = "用户明确要求的语言、格式、长度和交付形态优先于以下默认偏好。"
const responseDefaults = "默认使用用户当前使用的语言回答；无法判断时使用简体中文。仅在有助于阅读时使用 Markdown，简单回答不强制使用标题、表格或代码块。"
const strictFormats = "用户明确要求 JSON、CSV、XML、YAML、纯文本、仅代码、完整文件或补丁时，严格按要求的格式输出，不增加无关说明，也不擅自添加 Markdown 围栏。"
const truthfulness = "区分已知事实、合理推断和不确定信息；不声称使用当前未提供的工具或能力。"
const reliability = "所有结论只依据用户提供的信息、当前对话、可用工具或环境证据以及可验证的可靠知识；严格区分事实、推断、假设和未知，禁止猜测、伪造或脑补未知内容，不虚构业务数据、规则、来源、工具结果或已执行操作，不私自添加用户未提及的场景、数据、规则或条件。"
const missingInformation = "若信息不全、缺少关键条件或无法据此产出有效结果：明确告知信息不足、当前无法完成需求；逐项列明缺失的具体信息和其影响；引导用户补齐对应内容。不得强行拼凑、模糊敷衍作答或把未经确认的假设写成事实；在关键信息补齐前，只能交付明确标注边界的部分结果。"
const richOutput = "用户未指定冲突格式且图形确实提升理解时，关系、流程与结构优先使用 Mermaid，数学表达使用 LaTeX；用户要求视觉原型或矢量图时可输出完整 fenced `svg`，不把裸 HTML 当作 SVG 预览。"
const imagePreference = "用户要求生成位图且当前提供真实图像工具时优先调用该工具，不用 ASCII 文本画图代替；普通解释请求不强制生成图片。"
const imageGenerationPreference = "调用图片生成工具时，如果用户没有明确指定宽高或分辨率，应根据图片用途、内容和构图需要自行选择合适的常规尺寸与宽高比例，不得自行选择 2K、4K 或其他超大尺寸。用户明确指定宽高、分辨率、画面比例或输出格式时应优先遵循；“高清、精致、细节丰富”等质量描述不等于要求更大的图片尺寸。用户要求基于既有图片进行二次编辑时，必须从当前输入图片标记或会话图像谱系索引中选择明确的 assetId，并用 reference_asset_ids 调用图片工具；如果存在多张候选图且无法唯一判断，先询问用户，不得猜测目标图片。"
const toolDiscipline = "避免重复调用名称相同且参数等价的工具；前次调用失败、结果可能过期或用户明确要求刷新时允许再次调用。"

// buildChatSystemInstructions mirrors buildChatSystemInstructions.
func buildChatSystemInstructions(effectiveTools []string, internalToolNames []string) (version, text, hash string) {
	internalNames := map[string]bool{}
	for _, name := range internalToolNames {
		trimmed := trimSpace(name)
		if trimmed != "" {
			internalNames[trimmed] = true
		}
	}
	blocks := []string{instructionPriority, responseDefaults, strictFormats, truthfulness, reliability, missingInformation, richOutput, imagePreference}
	if internalNames["generate_image"] {
		blocks = append(blocks, imageGenerationPreference)
	}
	if len(normalizeChatHostedTools(effectiveTools)) > 0 || len(internalNames) > 0 {
		blocks = append(blocks, toolDiscipline)
	}
	text = strings.Join(blocks, "\n\n")
	digest := sha256.Sum256([]byte(text))
	hash = hexEncode(digest[:])
	return chatSystemInstructionsVersion, text, hash
}

// buildChatPromptCacheKey mirrors buildChatPromptCacheKey.
func buildChatPromptCacheKey(systemAccountID, apiKeyID, conversationID string) string {
	payload, _ := json.Marshal([]string{"juhe-ai-chat-prompt-cache-v1", systemAccountID, apiKeyID, conversationID})
	digest := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// model option plumbing (chat-model-options.ts subset consumed by routes).

// ChatModelOption mirrors ChatModelOption.
type ChatModelOption struct {
	ID                        string
	SupportsPromptCaching     bool
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    string
	SupportedServiceTiers     []string
	ContextWindowTokens       *int64
	MaxInputTokens            *int64
	MaxOutputTokens           *int64
	SupportedAPIProtocols     []string
	InputModalities           []string
	OutputModalities          []string
	SupportedTools            []string
	GenerationParameters      []ChatGenerationParameterCapability
}

// ChatGenerationParameterCapability mirrors ChatGenerationParameterCapability.
type ChatGenerationParameterCapability struct {
	Parameter    string  `json:"parameter"`
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
	DefaultValue float64 `json:"defaultValue"`
}

// ProviderModelCatalogItem mirrors ProviderModelCatalogItem (catalog subset).
type ProviderModelCatalogItem struct {
	Model                     string   `json:"model"`
	ProviderCode              string   `json:"providerCode"`
	SupportsPromptCaching     *bool    `json:"supportsPromptCaching,omitempty"`
	SupportedReasoningEfforts []string `json:"supportedReasoningEfforts,omitempty"`
	DefaultReasoningEffort    *string  `json:"defaultReasoningEffort,omitempty"`
	SupportedServiceTiers     []string `json:"supportedServiceTiers,omitempty"`
	ContextWindowTokens       *int64   `json:"contextWindowTokens,omitempty"`
	MaxInputTokens            *int64   `json:"maxInputTokens,omitempty"`
	MaxOutputTokens           *int64   `json:"maxOutputTokens,omitempty"`
	SupportedAPIProtocols     []string `json:"supportedApiProtocols,omitempty"`
	InputModalities           []string `json:"inputModalities,omitempty"`
	OutputModalities          []string `json:"outputModalities,omitempty"`
	SupportedTools            []string `json:"supportedTools,omitempty"`
}

var chatReasoningEffortSet = map[string]bool{"minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
var chatServiceTierSet = map[string]bool{"default": true, "priority": true, "flex": true}

// buildChatModelOptions mirrors buildChatModelOptions.
func buildChatModelOptions(modelIDs []string, catalog []ProviderModelCatalogItem) []*ChatModelOption {
	byModel := map[string][]ProviderModelCatalogItem{}
	for _, item := range catalog {
		byModel[item.Model] = append(byModel[item.Model], item)
	}
	seen := map[string]bool{}
	ordered := []string{}
	for _, id := range modelIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ordered = append(ordered, id)
	}
	out := make([]*ChatModelOption, 0, len(ordered))
	for _, id := range ordered {
		items := byModel[id]
		supportedReasoning := intersectStringCapabilityLists(mapItems(items, func(item ProviderModelCatalogItem) []string {
			return filterStrings(item.SupportedReasoningEfforts, func(value string) bool { return chatReasoningEffortSet[value] })
		}))
		defaultReasoning := commonReasoningDefault(items, supportedReasoning)
		catalogServiceTiers := intersectStringCapabilityLists(mapItems(items, func(item ProviderModelCatalogItem) []string {
			return filterStrings(item.SupportedServiceTiers, func(value string) bool { return chatServiceTierSet[value] })
		}))
		supportedServiceTiers := []string{}
		if len(catalogServiceTiers) > 0 {
			seenTiers := map[string]bool{"default": true}
			supportedServiceTiers = append(supportedServiceTiers, "default")
			for _, tier := range catalogServiceTiers {
				if !seenTiers[tier] {
					seenTiers[tier] = true
					supportedServiceTiers = append(supportedServiceTiers, tier)
				}
			}
		}
		contextWindowTokens := minimumKnownCapability(mapIntItems(items, func(item ProviderModelCatalogItem) *int64 { return item.ContextWindowTokens }))
		maxOutputTokens := minimumKnownCapability(mapIntItems(items, func(item ProviderModelCatalogItem) *int64 { return item.MaxOutputTokens }))
		maxInputTokens := minimumKnownCapability(mapIntItems(items, func(item ProviderModelCatalogItem) *int64 {
			if item.MaxInputTokens != nil {
				return item.MaxInputTokens
			}
			if item.ContextWindowTokens != nil && item.MaxOutputTokens != nil {
				derived := *item.ContextWindowTokens - *item.MaxOutputTokens
				return &derived
			}
			return nil
		}))
		option := &ChatModelOption{
			ID: id,
			SupportsPromptCaching: len(items) > 0 && allMatch(items, func(item ProviderModelCatalogItem) bool {
				return item.SupportsPromptCaching != nil && *item.SupportsPromptCaching
			}),
			SupportedReasoningEfforts: supportedReasoning,
			DefaultReasoningEffort:    defaultReasoning,
			SupportedServiceTiers:     supportedServiceTiers,
			ContextWindowTokens:       contextWindowTokens,
			MaxOutputTokens:           maxOutputTokens,
			SupportedAPIProtocols:     intersectStringCapabilityLists(mapItems(items, func(item ProviderModelCatalogItem) []string { return nilToEmpty(item.SupportedAPIProtocols) })),
			InputModalities:           intersectStringCapabilityLists(mapItems(items, func(item ProviderModelCatalogItem) []string { return nilToEmpty(item.InputModalities) })),
			OutputModalities:          intersectStringCapabilityLists(mapItems(items, func(item ProviderModelCatalogItem) []string { return nilToEmpty(item.OutputModalities) })),
			SupportedTools:            intersectStringCapabilityLists(mapItems(items, func(item ProviderModelCatalogItem) []string { return nilToEmpty(item.SupportedTools) })),
		}
		if maxInputTokens != nil && *maxInputTokens > 0 {
			option.MaxInputTokens = maxInputTokens
		}
		out = append(out, option)
	}
	return out
}

func mapIntItems(items []ProviderModelCatalogItem, project func(ProviderModelCatalogItem) *int64) []*int64 {
	out := make([]*int64, 0, len(items))
	for _, item := range items {
		out = append(out, project(item))
	}
	return out
}

func mapItems(items []ProviderModelCatalogItem, project func(ProviderModelCatalogItem) []string) [][]string {
	out := make([][]string, 0, len(items))
	for _, item := range items {
		out = append(out, project(item))
	}
	return out
}

func allMatch(items []ProviderModelCatalogItem, predicate func(ProviderModelCatalogItem) bool) bool {
	for _, item := range items {
		if !predicate(item) {
			return false
		}
	}
	return true
}

func filterStrings(values []string, predicate func(string) bool) []string {
	out := []string{}
	for _, value := range values {
		if predicate(value) {
			out = append(out, value)
		}
	}
	return out
}

func nilToEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func intersectStringCapabilityLists(lists [][]string) []string {
	if len(lists) == 0 {
		return []string{}
	}
	first := lists[0]
	seen := map[string]bool{}
	orderedFirst := []string{}
	for _, value := range first {
		if !seen[value] {
			seen[value] = true
			orderedFirst = append(orderedFirst, value)
		}
	}
	out := []string{}
	for _, value := range orderedFirst {
		all := true
		for _, other := range lists[1:] {
			if !containsString(other, value) {
				all = false
				break
			}
		}
		if all {
			out = append(out, value)
		}
	}
	return out
}

func minimumKnownCapability(values []*int64) *int64 {
	if len(values) == 0 {
		return nil
	}
	minimum := int64(0)
	for _, value := range values {
		if value == nil || *value <= 0 {
			return nil
		}
		if minimum == 0 || *value < minimum {
			minimum = *value
		}
	}
	return &minimum
}

func commonReasoningDefault(items []ProviderModelCatalogItem, supported []string) string {
	if len(items) == 0 {
		return ""
	}
	first := items[0].DefaultReasoningEffort
	if first == nil || !chatReasoningEffortSet[*first] || !containsString(supported, *first) {
		return ""
	}
	for _, item := range items {
		if item.DefaultReasoningEffort == nil || *item.DefaultReasoningEffort != *first {
			return ""
		}
	}
	return *first
}

// sortCatalogModels orders model ids deterministically for list output.
func sortCatalogModels(models []string) []string {
	out := append([]string{}, models...)
	sort.Strings(out)
	return out
}

var sizePattern = regexp.MustCompile(`^(\d{2,4})x(\d{2,4})$`)
