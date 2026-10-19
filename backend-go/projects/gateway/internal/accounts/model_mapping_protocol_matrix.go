package accounts

// 账户模型映射协议矩阵（写侧校验域）。对照归档实现：
//
//   - storage/account-model-mapping-protocol-matrix.ts：普通账户 6 条源→上游
//     转换白名单（accountModelMappingProtocolRules）、混合供应商账户 15 条跨
//     协议矩阵（hybridAccountModelMappingProtocolRules）、单条映射配对断言
//     （assertSupportedAccountModelMappingEndpointFamilyConversion）、按供应
//     商协议档案的映射断言（assertAccountModelMappingProtocolAllowed）、
//     hybrid 映射断言（assertHybridAccountModelMappingProtocolAllowed）、端
//     点族展示标签（accountModelMappingEndpointFamilyLabel）与上游端点能力
//     判定（hasAccountModelMappingUpstreamEndpointFamilyCapability）。
//   - storage/account-model-normalization.ts：hybrid 协议模型池校验
//     （assertMappingModelsInProtocolPools(Async)，verdict-ao 登记的
//     model_catalog_validation.go hybrid 跳过段）、协议档案目录支持判定
//     （providerModelSupportsProtocolProfile）与支持模型目录归属断言
//     （normalizeAccountSupportedModelsForProviderAsync 的目录段；归档唯一
//     消费点 account-management-patch.repository.ts:1520
//     normalizedSupportedModelsForPatch，filterIncompatibleDefaults=false）。
//
// 端点族枚举与归档 AccountModelMappingEndpointFamily 值一致
// （write.go accountSourceEndpointFamilies/accountUpstreamEndpointFamilies）。

// ---- endpoint family tokens ----

const (
	mappingFamilyChatCompletions       = "chat_completions"
	mappingFamilyResponses             = "responses"
	mappingFamilyMessages              = "messages"
	mappingFamilyGenerateContent       = "generate_content"
	mappingFamilyStreamGenerateContent = "stream_generate_content"
	// M4b 媒体映射族（媒体设计 §9 hybrid 行）：与 schema 端点族 seed 同码
	// （openai_v1_video_generation / openai_v1_tts，pg_schema.go）。媒体映射
	// 是同族模型名改写（source 对外媒体模型名 → upstream 该中转真实媒体
	// 模型名），执行走 OpenAI 形态媒体端点直连真实上游。
	mappingFamilyVideoGeneration = "video_generation"
	mappingFamilyTts             = "tts"
)

// isMediaMappingEndpointFamily 报告端点族是否媒体映射族（M4b）。媒体族的
// 上游面恒为 OpenAI 形态媒体端点（/v1/videos、/v1/audio/speech），只允许
// OpenAI 兼容协议档案声明。
func isMediaMappingEndpointFamily(value string) bool {
	return value == mappingFamilyVideoGeneration || value == mappingFamilyTts
}

// geminiNativeV1BetaProfileID mirrors GEMINI_NATIVE_V1BETA_PROFILE_ID
// (provider-protocol.ts). 归档 domain 模块被裁剪，字面量以 schema 种子的
// Gemini native 档案 ID 为准（maintenance pg_schema.go profile_gemini_native_v1beta）。
const geminiNativeV1BetaProfileID = "profile_gemini_native_v1beta"

// ---- conversion rule tables ----

// protocolConversionRule mirrors ProtocolConversionRule.
type protocolConversionRule struct {
	source                  string
	upstream                string
	upstreamProfile         string // "openai" | "anthropic" | "gemini"
	requiresNativeResponses bool
}

// accountModelMappingProtocolRules mirrors the same-named table: 普通账户的
// 源→上游协议转换白名单（同协议为主，responses→chat_completions 桥接与
// stream_generate_content→generate_content 归一）。
var accountModelMappingProtocolRules = []protocolConversionRule{
	{source: mappingFamilyChatCompletions, upstream: mappingFamilyChatCompletions, upstreamProfile: "openai"},
	{source: mappingFamilyResponses, upstream: mappingFamilyChatCompletions, upstreamProfile: "openai"},
	{source: mappingFamilyResponses, upstream: mappingFamilyResponses, upstreamProfile: "openai", requiresNativeResponses: true},
	{source: mappingFamilyMessages, upstream: mappingFamilyMessages, upstreamProfile: "anthropic"},
	{source: mappingFamilyGenerateContent, upstream: mappingFamilyGenerateContent, upstreamProfile: "gemini"},
	{source: mappingFamilyStreamGenerateContent, upstream: mappingFamilyGenerateContent, upstreamProfile: "gemini"},
	// M4b 媒体映射（媒体设计 §9 hybrid 行）：video_generation / tts 同族
	// 模型名改写，上游面是 OpenAI 形态媒体端点，仅 openai 档案供应商可声明
	//（能力门要求账户启用 video_create / audio_speech 端点模式）。
	{source: mappingFamilyVideoGeneration, upstream: mappingFamilyVideoGeneration, upstreamProfile: "openai"},
	{source: mappingFamilyTts, upstream: mappingFamilyTts, upstreamProfile: "openai"},
}

// hybridAccountModelMappingProtocolRules mirrors the same-named table: 混合
// 供应商账户的 15 条跨协议转换矩阵（3 个上游协议族，Go hybrid 账户已可用，
// 见 endpoint_modes.go 的 hybrid provider/endpoint-mode 家族）。
var hybridAccountModelMappingProtocolRules = []protocolConversionRule{
	{source: mappingFamilyChatCompletions, upstream: mappingFamilyChatCompletions, upstreamProfile: "openai"},
	{source: mappingFamilyResponses, upstream: mappingFamilyChatCompletions, upstreamProfile: "openai"},
	{source: mappingFamilyMessages, upstream: mappingFamilyChatCompletions, upstreamProfile: "openai"},
	{source: mappingFamilyGenerateContent, upstream: mappingFamilyChatCompletions, upstreamProfile: "openai"},
	{source: mappingFamilyStreamGenerateContent, upstream: mappingFamilyChatCompletions, upstreamProfile: "openai"},
	{source: mappingFamilyMessages, upstream: mappingFamilyMessages, upstreamProfile: "anthropic"},
	{source: mappingFamilyChatCompletions, upstream: mappingFamilyMessages, upstreamProfile: "anthropic"},
	{source: mappingFamilyResponses, upstream: mappingFamilyMessages, upstreamProfile: "anthropic"},
	{source: mappingFamilyGenerateContent, upstream: mappingFamilyMessages, upstreamProfile: "anthropic"},
	{source: mappingFamilyStreamGenerateContent, upstream: mappingFamilyMessages, upstreamProfile: "anthropic"},
	{source: mappingFamilyGenerateContent, upstream: mappingFamilyGenerateContent, upstreamProfile: "gemini"},
	{source: mappingFamilyStreamGenerateContent, upstream: mappingFamilyGenerateContent, upstreamProfile: "gemini"},
	{source: mappingFamilyChatCompletions, upstream: mappingFamilyGenerateContent, upstreamProfile: "gemini"},
	{source: mappingFamilyResponses, upstream: mappingFamilyGenerateContent, upstreamProfile: "gemini"},
	{source: mappingFamilyMessages, upstream: mappingFamilyGenerateContent, upstreamProfile: "gemini"},
	// M4b 媒体映射（媒体设计 §9 hybrid 行）：hybrid 聚合中转的媒体模型映射
	// ——source 对外视频/语音模型名 → upstream 该中转的真实媒体模型名，同族
	// 改写（媒体上游面恒为 OpenAI 形态媒体端点，无跨协议矩阵需求）。
	{source: mappingFamilyVideoGeneration, upstream: mappingFamilyVideoGeneration, upstreamProfile: "openai"},
	{source: mappingFamilyTts, upstream: mappingFamilyTts, upstreamProfile: "openai"},
}

func findProtocolConversionRule(rules []protocolConversionRule, source, upstream string) *protocolConversionRule {
	for index := range rules {
		if rules[index].source == source && rules[index].upstream == upstream {
			return &rules[index]
		}
	}
	return nil
}

// ---- labels & capability helpers ----

// accountModelMappingEndpointFamilyLabel mirrors the same-named function: the
// display label used by the matrix error copy.
func accountModelMappingEndpointFamilyLabel(value string) string {
	switch value {
	case mappingFamilyResponses:
		return "Responses"
	case mappingFamilyMessages:
		return "Messages"
	case mappingFamilyGenerateContent:
		return "Gemini GenerateContent"
	case mappingFamilyStreamGenerateContent:
		return "Gemini StreamGenerateContent"
	case mappingFamilyVideoGeneration:
		return "Video Generation"
	case mappingFamilyTts:
		return "TTS"
	}
	return "Chat Completions"
}

// isGeminiGenerateContentMappingSource mirrors the same-named function.
func isGeminiGenerateContentMappingSource(value string) bool {
	return value == mappingFamilyGenerateContent || value == mappingFamilyStreamGenerateContent
}

// hasAccountModelMappingUpstreamEndpointFamilyCapability mirrors the
// same-named function: the enabled upstream family requires at least one
// matching supported_endpoint_mode on the account credentials.
func hasAccountModelMappingUpstreamEndpointFamilyCapability(upstream string, modes []string) bool {
	var allowed []string
	switch upstream {
	case mappingFamilyChatCompletions:
		allowed = []string{"chat_json", "chat_sse"}
	case mappingFamilyResponses:
		allowed = []string{"responses_json", "responses_sse"}
	case mappingFamilyMessages:
		allowed = []string{"messages_json", "messages_sse"}
	case mappingFamilyVideoGeneration:
		// M4b：视频映射上游面要求账户可承接 /v1/videos 创建（候选过滤的
		// opt-in 模式，与 dispatch 侧 requiredSupportedEndpointMode 同词表）。
		allowed = []string{"video_create"}
	case mappingFamilyTts:
		// M4b：语音映射上游面要求账户可承接 /v1/audio/speech。
		allowed = []string{"audio_speech"}
	default:
		allowed = []string{"generate_content_json", "generate_content_sse"}
	}
	for _, mode := range modes {
		for _, candidate := range allowed {
			if mode == candidate {
				return true
			}
		}
	}
	return false
}

// mappingEnabled mirrors the `mapping.enabled !== false` tri-state: nil stays
// enabled (the persisted default).
func mappingEnabled(mapping ModelMapping) bool {
	return mapping.Enabled == nil || *mapping.Enabled
}

// ---- error copy ----

func unsupportedProtocolConversionMessage(source, upstream string) string {
	if source == mappingFamilyMessages {
		return "账号模型别名不支持 Anthropic Messages 跨协议映射，请改用混合供应商账户"
	}
	if isGeminiGenerateContentMappingSource(source) {
		return "账号模型别名不支持 Gemini GenerateContent 跨协议映射，请改用混合供应商账户"
	}
	return "账号模型别名只支持同协议映射；跨协议 " + accountModelMappingEndpointFamilyLabel(source) +
		" 到 " + accountModelMappingEndpointFamilyLabel(upstream) + " 请改用混合供应商账户"
}

func unsupportedHybridProtocolConversionMessage(source, upstream string) string {
	return "混合供应商账户暂不支持 " + accountModelMappingEndpointFamilyLabel(source) +
		" 到 " + accountModelMappingEndpointFamilyLabel(upstream) + " 的协议转换"
}

func missingUpstreamEndpointFamilyCapabilityMessage(upstream string) string {
	return "启用的模型映射上游协议 " + accountModelMappingEndpointFamilyLabel(upstream) +
		" 要求账户至少启用一种对应的上游接口能力"
}

// ---- assertions ----

// assertSupportedAccountModelMappingEndpointFamilyConversion mirrors the
// same-named function: the single-mapping source→upstream pairing must exist
// in the non-hybrid whitelist.
func assertSupportedAccountModelMappingEndpointFamilyConversion(source, upstream string) error {
	if findProtocolConversionRule(accountModelMappingProtocolRules, source, upstream) == nil {
		return &ValidationError{Message: unsupportedProtocolConversionMessage(source, upstream)}
	}
	return nil
}

// assertAccountModelMappingProtocolAllowed mirrors the same-named function:
// the per-provider-protocol-profile mapping gate (openai/anthropic/gemini
// profile family + the Gemini OpenAI Chat / native profile special cases +
// the rule whitelist + the upstream endpoint-mode capability gate).
// profile 承载归档 ProviderProtocolProfileDefinition 的判定子集
// （protocolCode/protocolVersion/providerProtocolProfileID），归档写入链路
// 的 profile 形态（protocolProfileFromRow / requireEnabled 档案）不含
// endpointFamilies，故此处不需要该字段。
func assertAccountModelMappingProtocolAllowed(mapping ModelMapping, profile protocolPredicateInput, supportedEndpointModes []string) error {
	openAIProfile := isOpenAIProtocolProfileOf(profile)
	anthropicProfile := isAnthropicProtocolProfileOf(profile)
	geminiProfile := isGeminiProtocolProfileOf(profile)
	profileID := profile.providerProtocolProfileID
	geminiOpenAIChatProfile := profileID == geminiOpenAIChatV1BetaProfileID
	geminiNativeProfile := profileID == geminiNativeV1BetaProfileID

	if !openAIProfile && !anthropicProfile && !geminiProfile {
		return &ValidationError{Message: "当前供应商协议不支持模型映射"}
	}
	if geminiOpenAIChatProfile &&
		mapping.SourceEndpointFamily != mappingFamilyChatCompletions &&
		mapping.SourceEndpointFamily != mappingFamilyResponses {
		return &ValidationError{Message: "Gemini OpenAI Chat 档案的账号模型别名只能使用 Chat Completions 或 Responses 到 Chat bridge"}
	}
	if geminiOpenAIChatProfile && mapping.UpstreamEndpointFamily != mappingFamilyChatCompletions {
		return &ValidationError{Message: "Gemini OpenAI Chat 档案的账号模型别名上游协议只能是 Chat Completions"}
	}
	if geminiProfile && !geminiNativeProfile && !geminiOpenAIChatProfile {
		return &ValidationError{Message: "当前 Gemini 协议档案暂不支持账号模型别名"}
	}

	rule := findProtocolConversionRule(accountModelMappingProtocolRules, mapping.SourceEndpointFamily, mapping.UpstreamEndpointFamily)
	if rule == nil {
		return &ValidationError{Message: unsupportedProtocolConversionMessage(mapping.SourceEndpointFamily, mapping.UpstreamEndpointFamily)}
	}

	if (mapping.SourceEndpointFamily == mappingFamilyChatCompletions || mapping.SourceEndpointFamily == mappingFamilyResponses) && !openAIProfile {
		return &ValidationError{Message: "当前供应商协议不支持 OpenAI 账号模型别名"}
	}
	// M4b：媒体映射族（video_generation/tts）的上游面恒为 OpenAI 形态媒体
	// 端点，仅 openai 档案供应商可声明（anthropic/gemini 档案无对应执行面）。
	if isMediaMappingEndpointFamily(mapping.SourceEndpointFamily) && !openAIProfile {
		return &ValidationError{Message: "当前供应商协议不支持媒体账号模型别名"}
	}
	if mapping.SourceEndpointFamily == mappingFamilyMessages && !anthropicProfile {
		return &ValidationError{Message: "当前供应商协议不支持 Anthropic Messages 账号模型别名"}
	}
	if isGeminiGenerateContentMappingSource(mapping.SourceEndpointFamily) && !geminiNativeProfile {
		return &ValidationError{Message: "当前供应商协议不支持 Gemini native 账号模型别名"}
	}
	if mappingEnabled(mapping) && !hasAccountModelMappingUpstreamEndpointFamilyCapability(mapping.UpstreamEndpointFamily, supportedEndpointModes) {
		if rule.requiresNativeResponses {
			return &ValidationError{Message: "上游协议 Responses 只能用于账号真实支持 Responses API 的原生上游"}
		}
		return &ValidationError{Message: missingUpstreamEndpointFamilyCapabilityMessage(mapping.UpstreamEndpointFamily)}
	}
	return nil
}

// assertHybridAccountModelMappingProtocolAllowed mirrors the same-named
// function: the hybrid cross-protocol matrix gate. 归档签名里的
// options.providerProfile 在函数体内不被消费，Go 签名省略该参数。
func assertHybridAccountModelMappingProtocolAllowed(mapping ModelMapping, supportedEndpointModes []string) error {
	rule := findProtocolConversionRule(hybridAccountModelMappingProtocolRules, mapping.SourceEndpointFamily, mapping.UpstreamEndpointFamily)
	if rule == nil {
		return &ValidationError{Message: unsupportedHybridProtocolConversionMessage(mapping.SourceEndpointFamily, mapping.UpstreamEndpointFamily)}
	}
	if mappingEnabled(mapping) && !hasAccountModelMappingUpstreamEndpointFamilyCapability(mapping.UpstreamEndpointFamily, supportedEndpointModes) {
		if rule.requiresNativeResponses {
			return &ValidationError{Message: "上游协议 Responses 只能用于账号真实支持 Responses API 的原生上游"}
		}
		return &ValidationError{Message: missingUpstreamEndpointFamilyCapabilityMessage(mapping.UpstreamEndpointFamily)}
	}
	return nil
}

// ---- protocol pools（hybrid 协议池段已随目录断言切 models 表拆除）----
//
// 旧 assertMappingModelsInProtocolPools / listProtocolProviderCodes /
// protocolPoolForMapping 读 provider_protocol_profiles/providers 旧表并按
// supportedApiProtocols 建池（account-model-normalization.ts 移植段）；目标
// 口径下映射 target 的 models 身份与 §5.3 联动由
// assertAccountModelMappingsInProviderCatalog（models 身份解析）与写入面
// resolveEntryMappingTargets 承载，协议池段无目标形态对应物，整段删除
