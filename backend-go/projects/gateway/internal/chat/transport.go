package chat

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
)

// Transport plumbing ported from chat-transport.ts, chat-system-instructions.ts,
// chat-prompt-cache.ts and the parameter slices of
// chat-generation-parameters.ts.
//
// 主对话协议按绑定账户供应商主推直连（PLAN-20261012T121500000Z D4）：
// responses 与 chat_completions 两形态并存，解析入口见 request_protocol.go；
// 站内模型工具体系已整体退场（同计划 D2），无 function tools 注入。

// ChatTransportProtocol mirrors ChatTransportProtocol.
type ChatTransportProtocol string

const (
	ProtocolChatCompletions ChatTransportProtocol = "chat_completions"
	ProtocolResponses       ChatTransportProtocol = "responses"
)

// ChatTransportMessage mirrors ChatTransportMessage.
type ChatTransportMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string | []chatContentPart
}

// ChatTransportInputBlock mirrors ChatTransportInputBlock.
type ChatTransportInputBlock struct {
	Type    string `json:"type"` // input_text|input_image
	Text    string `json:"text,omitempty"`
	DataURL string `json:"dataUrl,omitempty"`
}

// ChatTransportAccount mirrors ChatTransportAccount (runtime account snapshot
// subset consumed by the transport selection). ProviderProtocolProfileID /
// ProtocolCode 是协议解析（request_protocol.go）的最小必要投影：按账户真实
// protocol profile 选择请求形态，不按供应商名字猜测。
type ChatTransportAccount struct {
	ID                         string                      `json:"id"`
	Type                       string                      `json:"type"`
	ProviderCode               string                      `json:"providerCode"`
	ProviderProtocolProfileID  string                      `json:"providerProtocolProfileId,omitempty"`
	ProtocolCode               string                      `json:"protocolCode,omitempty"`
	SupportedEndpointModes     []string                    `json:"supportedEndpointModes,omitempty"`
	SupportedModels            []string                    `json:"supportedModels,omitempty"`
	ModelMappings              []ChatTransportModelMapping `json:"modelMappings,omitempty"`
}

// ChatTransportModelMapping mirrors the account model mapping subset.
type ChatTransportModelMapping struct {
	Enabled                *bool  `json:"enabled,omitempty"`
	SourceModel            string `json:"sourceModel"`
	UpstreamModel          string `json:"upstreamModel,omitempty"`
	SourceEndpointFamily   string `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily string `json:"upstreamEndpointFamily,omitempty"`
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

// transportGenerationParameters mirrors transportGenerationParameters（主对话恒
// chat_completions，仅保留该协议的参数切片）。
func transportGenerationParameters(input *ChatGenerationParameters) map[string]any {
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
	set("frequency_penalty", input.FrequencyPenalty)
	set("presence_penalty", input.PresencePenalty)
	set("max_completion_tokens", input.MaxOutputTokens)
	set("seed", input.Seed)
	return out
}

// toolDefinition 已随站内模型工具体系退场（PLAN-20261012T121500000Z D2）：
// 无 function tools 注入，工具上下文/编排器/工具续答一并删除。

// chatContentPart 渲染 Chat Completions 多模态 content 数组块：文本块与图片
// 输入块（image_url，data URL）。契约 §11.3：图片输入按请求形态渲染，Chat 形态
// 用 image_url，Responses 形态用 input_image（responsesInputItems）。
func chatContentParts(blocks []ChatTransportInputBlock) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "input_image" {
			out = append(out, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": block.DataURL},
			})
			continue
		}
		out = append(out, map[string]any{"type": "text", "text": block.Text})
	}
	return out
}

// buildChatTransportRequest mirrors buildChatTransportRequest（chat_completions
// 形态）。当前输入含图片块时 user content 渲染为多模态数组；system 提示进首条
// system message。
func buildChatTransportRequest(input ChatTransportRequestInput) (string, map[string]any) {
	messages := []any{map[string]any{"role": "system", "content": input.Instructions}}
	for _, message := range input.History {
		messages = append(messages, map[string]any{"role": message.Role, "content": message.Content})
	}
	if len(input.CurrentBlocks) > 0 {
		messages = append(messages, map[string]any{"role": "user", "content": chatContentParts(input.CurrentBlocks)})
	} else {
		messages = append(messages, map[string]any{"role": "user", "content": input.CurrentContent})
	}
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
	for key, value := range transportGenerationParameters(input.GenerationParameters) {
		body[key] = value
	}
	if input.PromptCacheKey != "" {
		body["prompt_cache_key"] = input.PromptCacheKey
	}
	if len(input.HostedTools) > 0 {
		body["tools"] = input.HostedTools
	}
	return "/v1/chat/completions", body
}

// buildResponsesTransportRequest 构造 Responses 请求体（D4 请求构造契约）：
// 顶层 `instructions` 承载 system 提示（chat-system 注入既有契约），`input`
// 承载历史 + 当前轮的 input_text/input_image 内容块数组，顺序与 Chat 形态的
// messages 完全一致（历史图片说明、压缩 checkpoint 均以文本形式保序进入）。
// 流式 + reasoning summary 与既有契约一致；hosted tools（web_search）仅在
// 调用方携带时注入（D3，请求级联网开关）。
func buildResponsesTransportRequest(input ChatTransportRequestInput) (string, map[string]any) {
	inputItems := []any{}
	for _, message := range input.History {
		inputItems = append(inputItems, responsesInputItem(message.Role, message.Content))
	}
	if len(input.CurrentBlocks) > 0 {
		inputItems = append(inputItems, responsesInputItem("user", responsesUserContentFromBlocks(input.CurrentBlocks)))
	} else {
		inputItems = append(inputItems, responsesInputItem("user", input.CurrentContent))
	}
	body := map[string]any{
		"model":        input.Model,
		"instructions": input.Instructions,
		"input":        inputItems,
		"stream":       true,
		"reasoning":    map[string]any{"summary": "auto"},
	}
	if input.ReasoningEffort != "" {
		body["reasoning_effort"] = input.ReasoningEffort
	}
	if input.ServiceTier != "" {
		body["service_tier"] = input.ServiceTier
	}
	for key, value := range transportGenerationParameters(input.GenerationParameters) {
		body[key] = value
	}
	if input.PromptCacheKey != "" {
		body["prompt_cache_key"] = input.PromptCacheKey
	}
	if len(input.HostedTools) > 0 {
		body["tools"] = input.HostedTools
	}
	return "/v1/responses", body
}

// responsesInputItem 把中性历史消息渲染为 Responses input 项：字符串 content
// 直接作为 text，多模态数组按既有形态映射为 input_text/input_image 内容块。
func responsesInputItem(role string, content any) map[string]any {
	return map[string]any{"role": role, "content": responsesContentValue(content)}
}

// responsesContentValue 折叠中性 content 为 Responses 形态：字符串原样；
// Chat 多模态数组（text/image_url）转 input_text/input_image；其他 JSON 值
// 序列化为文本（checkpoint 载荷等纯数据）。
func responsesContentValue(content any) any {
	switch typed := content.(type) {
	case string:
		return typed
	case []map[string]any:
		return responsesBlocksFromGeneric(typed)
	case []any:
		parts := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if block, ok := item.(map[string]any); ok {
				parts = append(parts, responsesContentPartFromGeneric(block))
			}
		}
		return parts
	default:
		payload, err := json.Marshal(content)
		if err != nil {
			return ""
		}
		return string(payload)
	}
}

// responsesUserContentFromBlocks 把当前轮输入块渲染为 Responses 内容数组：
// input_text 保持文本，input_image 用 data URL（序与 Chat 形态一致）。
func responsesUserContentFromBlocks(blocks []ChatTransportInputBlock) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "input_image" {
			out = append(out, map[string]any{"type": "input_image", "image_url": block.DataURL, "detail": "auto"})
			continue
		}
		out = append(out, map[string]any{"type": "input_text", "text": block.Text})
	}
	return out
}

// responsesBlocksFromGeneric 把历史里已存的 Chat 多模态块（map 形态）映射为
// Responses 内容块。
func responsesBlocksFromGeneric(blocks []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, responsesContentPartFromGeneric(block))
	}
	return out
}

func responsesContentPartFromGeneric(block map[string]any) map[string]any {
	blockType, _ := block["type"].(string)
	if blockType == "image_url" {
		imageURL := ""
		if wrapper, ok := block["image_url"].(map[string]any); ok {
			imageURL, _ = wrapper["url"].(string)
		} else if value, ok := block["image_url"].(string); ok {
			imageURL = value
		}
		return map[string]any{"type": "input_image", "image_url": imageURL, "detail": "auto"}
	}
	text, _ := block["text"].(string)
	return map[string]any{"type": "input_text", "text": text}
}

// ChatTransportRequestInput mirrors buildChatTransportRequest input（两形态
// 共用：InternalTools/ToolContinuation 已随工具体系退场，HostedTools 是 D3
// 请求级联网开关注入的 hosted 工具载荷）。
type ChatTransportRequestInput struct {
	Instructions         string
	Model                string
	History              []ChatTransportMessage
	CurrentContent       string
	CurrentBlocks        []ChatTransportInputBlock
	HostedTools          []map[string]any
	ReasoningEffort      string
	ServiceTier          string
	GenerationParameters *ChatGenerationParameters
	PromptCacheKey       string
}

// --- system instructions (chat-system-instructions.ts) ---

const chatSystemInstructionsVersion = "chat-system-v5"

const instructionPriority = "用户明确要求的语言、格式、长度和交付形态优先于以下默认偏好。"
const responseDefaults = "默认使用用户当前使用的语言回答；无法判断时使用简体中文。仅在有助于阅读时使用 Markdown，简单回答不强制使用标题、表格或代码块。"
const strictFormats = "用户明确要求 JSON、CSV、XML、YAML、纯文本、仅代码、完整文件或补丁时，严格按要求的格式输出，不增加无关说明，也不擅自添加 Markdown 围栏。"
const truthfulness = "区分已知事实、合理推断和不确定信息；不声称使用当前未提供的工具或能力。"
const reliability = "所有结论只依据用户提供的信息、当前对话、可用工具或环境证据以及可验证的可靠知识；严格区分事实、推断、假设和未知，禁止猜测、伪造或脑补未知内容，不虚构业务数据、规则、来源、工具结果或已执行操作，不私自添加用户未提及的场景、数据、规则或条件。"
const missingInformation = "若信息不全、缺少关键条件或无法据此产出有效结果：明确告知信息不足、当前无法完成需求；逐项列明缺失的具体信息和其影响；引导用户补齐对应内容。不得强行拼凑、模糊敷衍作答或把未经确认的假设写成事实；在关键信息补齐前，只能交付明确标注边界的部分结果。"
const richOutput = "用户未指定冲突格式且图形确实提升理解时，关系、流程与结构优先使用 Mermaid，数学表达使用 LaTeX；用户要求视觉原型或矢量图时可输出完整 fenced `svg`，不把裸 HTML 当作 SVG 预览。"
const imagePreference = "用户要求生成位图且当前提供真实图像工具时优先调用该工具，不用 ASCII 文本画图代替；普通解释请求不强制生成图片。"

// buildChatSystemInstructions mirrors buildChatSystemInstructions. 生图偏好段
// 与工具纪律段已随站内工具体系退场（PLAN-20261012T121500000Z D2），版本升级
// 为 chat-system-v5（Prompt Cache 稳定前缀随之自然切换）。
func buildChatSystemInstructions() (version, text, hash string) {
	blocks := []string{instructionPriority, responseDefaults, strictFormats, truthfulness, reliability, missingInformation, richOutput, imagePreference}
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
	// SupportedToolsByProtocol 是目录「协议 × 工具」矩阵的多行聚合（工具体系
	// 设计 6.4）：每个协议键下的工具集为全部目录行的交集。一维 SupportedTools
	// 已随阶段 2 退场，能力判定一律按矩阵读取。
	SupportedToolsByProtocol map[string][]string
	GenerationParameters     []ChatGenerationParameterCapability
}

// ChatGenerationParameterCapability mirrors ChatGenerationParameterCapability.
type ChatGenerationParameterCapability struct {
	Parameter    string  `json:"parameter"`
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
	Step         float64 `json:"step"`
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
	// SupportedToolsByProtocol 是「协议 × 工具」矩阵（键为协议枚举、值为该协议
	// 下可用工具集）。一维 SupportedTools 已退场（工具体系设计 6.4/§2.10）。
	SupportedToolsByProtocol map[string][]string `json:"supportedToolsByProtocol,omitempty"`
	// GenerationParameterCapabilities mirrors generationParameterCapabilities
	// (BUG-0175 D-185). Nil rows derive the same table from
	// providerCode/model/maxOutputTokens, exactly like the archive catalog
	// snapshot assembly.
	GenerationParameterCapabilities map[string][]ChatGenerationParameterCapability `json:"generationParameterCapabilities,omitempty"`
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
		toolMatrices := make([]map[string][]string, 0, len(items))
		for _, item := range items {
			toolMatrices = append(toolMatrices, item.SupportedToolsByProtocol)
		}
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
			SupportedToolsByProtocol:  intersectToolsByProtocolMaps(toolMatrices),
			// BUG-0175 D-185: the per-model generation parameter capability
			// list (chat-model-options.ts:108 flattenGenerationParameters).
			GenerationParameters: flattenGenerationParameters(items),
		}
		if maxInputTokens != nil && *maxInputTokens > 0 {
			option.MaxInputTokens = maxInputTokens
		}
		out = append(out, option)
	}
	return out
}

// intersectToolsByProtocolMaps 聚合多行目录的「协议 × 工具」矩阵：每个协议键
// 下的工具集取全部行的交集（行缺该键视为空集，与一维 intersect 语义同型）；
// 键按字典序输出保证确定性，值内顺序保持第一行该键列表的相对顺序。
func intersectToolsByProtocolMaps(maps []map[string][]string) map[string][]string {
	out := map[string][]string{}
	if len(maps) == 0 {
		return out
	}
	for key := range maps[0] {
		lists := make([][]string, 0, len(maps))
		for _, matrix := range maps {
			lists = append(lists, nilToEmpty(matrix[key]))
		}
		intersected := intersectStringCapabilityLists(lists)
		if len(intersected) > 0 {
			out[key] = intersected
		}
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

