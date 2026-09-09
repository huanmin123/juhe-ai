package accounts

import (
	"context"
	"strings"
)

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
)

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

// ---- protocol pools (hybrid providers, account-model-normalization.ts) ----

// protocolPoolForMapping mirrors sourceModelPoolForMapping /
// upstreamProtocolModelPoolForMapping: the protocol pair behind the endpoint
// family.
func protocolPoolForMapping(endpointFamily string) (protocolCode, protocolVersion string) {
	switch endpointFamily {
	case mappingFamilyMessages:
		return anthropicProtocolCodeConstant, anthropicProtocolVersionConstant
	case mappingFamilyGenerateContent, mappingFamilyStreamGenerateContent:
		return geminiProtocolCodeConstant, geminiProtocolVersionConstant
	}
	return openAIProtocolCode, openAIProtocolVersion
}

// assertMappingModelsInProtocolPools ports assertMappingModelsInProtocolPools
// (Async 合并为单实现): hybrid 账户的映射来源/目标模型必须分别落在源端点族
// 与上游端点族对应的协议模型池中（该协议全部启用供应商的目录模型按
// supportedApiProtocols 过滤）。nil 目录端口保持 no-op（self-contained 约定，
// 与 assertAccountModelMappingsInProviderCatalog 一致）。
func (s *Store) assertMappingModelsInProtocolPools(ctx context.Context, q queryer, systemAccountID string, mappings []ModelMapping) error {
	if s.modelCatalog == nil {
		return nil
	}
	// Node 每条映射重建池；Go 按端点族缓存（同一次校验内池内容不变，行为等价）。
	poolCache := map[string]map[string]bool{}
	poolFor := func(endpointFamily string) (map[string]bool, error) {
		if pool, ok := poolCache[endpointFamily]; ok {
			return pool, nil
		}
		protocolCode, protocolVersion := protocolPoolForMapping(endpointFamily)
		codes, err := s.listProtocolProviderCodes(ctx, q, protocolCode, protocolVersion)
		if err != nil {
			return nil, err
		}
		pool := map[string]bool{}
		for _, code := range codes {
			items, err := s.modelCatalog.ListAccountModelCatalog(ctx, code, systemAccountID, false)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				if containsString(item.SupportedAPIProtocols, endpointFamily) {
					pool[strings.ToLower(strings.TrimSpace(item.Model))] = true
				}
			}
		}
		poolCache[endpointFamily] = pool
		return pool, nil
	}
	invalidSourceModels := []string{}
	invalidUpstreamModels := []string{}
	for _, mapping := range mappings {
		sourcePool, err := poolFor(mapping.SourceEndpointFamily)
		if err != nil {
			return err
		}
		if !sourcePool[strings.ToLower(strings.TrimSpace(mapping.SourceModel))] {
			invalidSourceModels = append(invalidSourceModels, mapping.SourceModel)
		}
		upstreamPool, err := poolFor(mapping.UpstreamEndpointFamily)
		if err != nil {
			return err
		}
		if !upstreamPool[strings.ToLower(strings.TrimSpace(mapping.UpstreamModel))] {
			invalidUpstreamModels = append(invalidUpstreamModels, mapping.UpstreamModel)
		}
	}
	if len(invalidSourceModels) > 0 {
		return &ValidationError{Message: "账号模型别名来源模型不在对应协议模型池中：" + joinMappingModelSample(invalidSourceModels)}
	}
	if len(invalidUpstreamModels) > 0 {
		return &ValidationError{Message: "账号模型别名目标模型不在对应上游协议模型池中：" + joinMappingModelSample(invalidUpstreamModels)}
	}
	return nil
}

// listProtocolProviderCodes mirrors the listXxxProtocolProviderCodes family
// (storage/provider.repository.ts): the enabled provider codes carrying an
// enabled profile on the protocol pair. 查询守卫与 isProtocolProviderCode
// 同表同条件（DISTINCT 列表形态）。
func (s *Store) listProtocolProviderCodes(ctx context.Context, q queryer, protocolCode, protocolVersion string) ([]string, error) {
	rows, err := q.QueryContext(ctx, s.bind(`SELECT DISTINCT p.code
		FROM `+s.table("providers")+` p
		INNER JOIN `+s.table("provider_protocol_profiles")+` ppp ON ppp.provider_code = p.code
		WHERE p.enabled = 1 AND ppp.enabled = 1
			AND ppp.protocol_code = ? AND ppp.protocol_version = ?
		ORDER BY p.code ASC`), protocolCode, protocolVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	codes := []string{}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

// ---- supported models catalog gate (normalizeAccountSupportedModelsForProvider) ----

// providerModelSupportsProtocolProfile mirrors the same-named function
// (account-model-normalization.ts:93). 归档写入链路的 profile 形态
// （protocolProfileFromRow / requireEnabled 档案）不带 endpointFamilies，恒走
// 协议类别兜底集合；gpt 供应商与空协议数组的模型直接放行。
func providerModelSupportsProtocolProfile(modelProtocols []string, profile protocolPredicateInput) bool {
	if len(modelProtocols) == 0 {
		return true
	}
	if isGptVendorCodeToken(profile.providerCode) {
		return true
	}
	var profileProtocols []string
	switch {
	case isOpenAIProtocolProfileOf(profile):
		profileProtocols = []string{mappingFamilyChatCompletions, mappingFamilyResponses}
	case isAnthropicProtocolProfileOf(profile):
		profileProtocols = []string{mappingFamilyMessages}
	case isGeminiProtocolProfileOf(profile):
		profileProtocols = []string{
			mappingFamilyGenerateContent, mappingFamilyStreamGenerateContent,
			"count_tokens", "embed_content", "interactions",
		}
	default:
		return false
	}
	for _, protocol := range modelProtocols {
		for _, candidate := range profileProtocols {
			if protocol == candidate {
				return true
			}
		}
	}
	return false
}

// assertAccountSupportedModelsInProviderCatalog ports the catalog segment of
// normalizeAccountSupportedModelsForProviderAsync: 支持模型必须落在当前供应
// 商模型目录中且模型声明支持当前协议档案（providerModelSupportsProtocolProfile）。
// hybrid 供应商直通（归档 :69）；空集直通（归档 :42）；目录读取带
// includeUnpriced: true（归档 :48，与映射池的默认 false 不同）。归档的
// filterIncompatibleDefaults 在唯一消费点（patch :1520）为 false，Go 消费链
// 不需要该参数。
func (s *Store) assertAccountSupportedModelsInProviderCatalog(ctx context.Context, q queryer, models []string, providerCode, systemAccountID string, profile protocolPredicateInput) error {
	if len(models) == 0 || isHybridProviderCodeToken(providerCode) {
		return nil
	}
	if s.modelCatalog == nil {
		return nil
	}
	catalog, err := s.modelCatalog.ListAccountModelCatalog(ctx, providerCode, systemAccountID, true)
	if err != nil {
		return err
	}
	providerModels := map[string]bool{}
	for _, item := range catalog {
		if providerModelSupportsProtocolProfile(item.SupportedAPIProtocols, profile) {
			providerModels[strings.ToLower(strings.TrimSpace(item.Model))] = true
		}
	}
	invalidModels := []string{}
	for _, model := range models {
		if !providerModels[strings.ToLower(strings.TrimSpace(model))] {
			invalidModels = append(invalidModels, model)
		}
	}
	if len(invalidModels) > 0 {
		return &ValidationError{Message: "账户支持模型不在供应商模型目录中：" + joinMappingModelSample(invalidModels)}
	}
	return nil
}
