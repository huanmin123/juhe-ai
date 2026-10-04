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
//
// 工具体系设计（docs/functions/AI问答工具体系与主子模型设计.md §11）：主对话
// 协议恒 chat_completions——工具驱动的协议偏好与 Responses 传输分支已整体
// 删除，联网搜索/生图改为会话级绑定的子模型经进程内 /v1 链执行（见
// generation_websearch.go 与 stream_execute.go 的组装）。

// ChatTransportProtocol mirrors ChatTransportProtocol. 主对话恒
// chat_completions；responses 仅作为工具绑定候选过滤的目录矩阵键保留
// （supportedToolsByProtocol 的协议枚举）。
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

// toolDefinition mirrors ChatInternalToolDefinition minus the executor (the
// executor lives in generation_tools.go). Kind 区分执行方式（契约 §5）：
// code=后端代码直接执行；model=会话级绑定的子模型执行（web_search /
// generate_image）。两类工具对主模型的注入完全一致（function 定义），主模型
// 不感知后端执行方式。
type toolDefinition struct {
	ID          string
	Version     string
	ModelName   string
	Kind        string // "code" | "model"
	Description string
	InputSchema map[string]any
	// MaxArgumentBytes / MaxResultBytes / TimeoutMs bound one call（模型工具的
	// TimeoutMs 是子调用整体超时上限，执行侧另有 context 超时）。
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
	// ConstrainImageModel 把主模型自选的生图模型收敛到会话生图绑定账户实际
	// 可路由的集合内（BUG-0230）：组装侧按 bindings 注入；nil 表示无约束
	//（未绑定等场景由发送预检拦截）。
	ConstrainImageModel func(model string) string
	ImageGeneration     func(input ChatImageGenerationRequest) (ChatImageGenerationToolResult, error)
	ArtifactSink        ChatGeneratedImageArtifactSink
	// WebSearch 是 web_search 模型工具的执行端口：组装侧（stream_execute）按
	// 会话绑定解析——未绑定返回 chatToolBindingRequiredError，已绑定经进程内
	// /v1 链固定派发到绑定「账户+模型」（generation_websearch.go）。
	WebSearch func(query string) (chatToolExecutionResult, error)
	// M7 问答音视频工具（问答音视频工具设计 §4，2026-10-04）：VideoGeneration
	// 创建异步视频任务（POST /v1/videos，立即返回 jobId）；AudioGeneration 同步
	// 合成音频并落资产（POST /v1/audio/speech）；组装侧（stream_execute）按会话
	// 绑定解析——未绑定返回 chatToolBindingRequiredError；Constrain*Model 沿
	// BUG-0230 模式把主模型自选模型收敛到绑定账户可路由集合。
	DefaultVideoModel   string
	DefaultAudioModel   string
	ConstrainVideoModel func(model string) string
	ConstrainAudioModel func(model string) string
	VideoGeneration     func(input ChatVideoCreationRequest) (ChatVideoCreationResult, error)
	AudioGeneration     func(input ChatAudioGenerationRequest) (ChatMediaArtifactResult, error)
	MediaArtifactSink   ChatMediaArtifactSink
	// ToolProgress 是当前工具调用的过程增量端口（契约 §10.3 子代理过程区）：
	// orchestrator 在每次 executeCall 前按 callID 绑定（经内容块投影通道以
	// item.progress 渐进下发），执行器（web_search 流式解析器）节流上报；
	// 串行执行无并发。
	ToolProgress func(progress chatWebSearchProgress)
}

// compileChatInternalTools mirrors compileChatInternalTools（恒 chat_completions
// 形状：{type:"function", function:{...}}）。
func compileChatInternalTools(tools []*toolDefinition) []map[string]any {
	out := []map[string]any{}
	for _, tool := range tools {
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

// buildChatToolContinuation mirrors buildChatToolContinuation（恒 chat_completions
// 形状：tool 角色消息）。
func buildChatToolContinuation(continuationItems []any, outputs []ChatToolExecutionOutput) []any {
	out := append([]any{}, continuationItems...)
	for _, output := range outputs {
		out = append(out, map[string]any{
			"role":         "tool",
			"tool_call_id": output.CallID,
			"content":      output.ModelOutput,
		})
	}
	return out
}

// chatContentPart 渲染 Chat Completions 多模态 content 数组块：文本块与图片
// 输入块（image_url，data URL）。契约 §11.3：图片输入从 Responses 迁到 Chat
// Completions 多模态格式。
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

// buildChatTransportRequest mirrors buildChatTransportRequest（恒
// chat_completions）。当前输入含图片块时 user content 渲染为多模态数组。
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
	messages = append(messages, input.ToolContinuation...)
	internalTools := compileChatInternalTools(input.InternalTools)
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
	if len(internalTools) > 0 {
		body["tools"] = internalTools
		body["tool_choice"] = "auto"
		body["parallel_tool_calls"] = false
	}
	return "/v1/chat/completions", body
}

// ChatTransportRequestInput mirrors buildChatTransportRequest input.
type ChatTransportRequestInput struct {
	Instructions         string
	Model                string
	History              []ChatTransportMessage
	CurrentContent       string
	CurrentBlocks        []ChatTransportInputBlock
	InternalTools        []*toolDefinition
	ToolContinuation     []any
	ReasoningEffort      string
	ServiceTier          string
	GenerationParameters *ChatGenerationParameters
	PromptCacheKey       string
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
func buildChatSystemInstructions(internalToolNames []string) (version, text, hash string) {
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
	if len(internalNames) > 0 {
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
	// SupportedToolsByProtocol 是目录「协议 × 工具」矩阵的多行聚合（工具体系
	// 设计 6.4）：每个协议键下的工具集为全部目录行的交集。一维 SupportedTools
	// 已随阶段 2 退场，能力判定一律按矩阵读取。
	SupportedToolsByProtocol map[string][]string
	GenerationParameters     []ChatGenerationParameterCapability
}

// supportsTool 报告矩阵任一协议下是否声明了 tool（function_calling 判定口径：
// 主对话恒 chat_completions，但目录矩阵可能只在 responses 键下声明
// function_calling——按「任一协议」口径判定，避免误杀仅声明在 responses 下的
// 能力）。
func (o *ChatModelOption) supportsTool(tool string) bool {
	for _, tools := range o.SupportedToolsByProtocol {
		if containsString(tools, tool) {
			return true
		}
	}
	return false
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

var sizePattern = regexp.MustCompile(`^(\d{2,4})x(\d{2,4})$`)
