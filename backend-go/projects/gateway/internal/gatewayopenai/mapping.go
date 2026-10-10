package gatewayopenai

import (
	"net/url"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// AccountModelMapping mirrors AccountModelMapping: an account-level model
// mapping row.
type AccountModelMapping struct {
	SourceModel            string
	SourceEndpointFamily   string
	UpstreamModel          string
	UpstreamEndpointFamily string
	// Enabled mirrors enabled !== false; nil counts as enabled.
	Enabled            *bool
	RuntimeSource      string
	RuntimeRouteRuleID string
}

// RuntimeAccount mirrors OpenAIModelMappingRuntimeAccount.
type RuntimeAccount struct {
	ModelMappings             []AccountModelMapping
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
}

// sourceEndpointFamilies are the families eligible for model mapping
// (isAccountModelMappingSourceEndpointFamily).
func isAccountModelMappingSourceEndpointFamily(value string) bool {
	switch value {
	case FamilyChatCompletions,
		FamilyResponses,
		FamilyAnthropicMessages,
		FamilyGeminiGenerateContent,
		FamilyGeminiStreamGenerate,
		// M4b 媒体映射族（媒体设计 §9 hybrid 行）：/v1/videos 创建形态解析
		// video_generation 源映射、/v1/audio/speech 形态解析 tts 源映射（同族
		// 模型名改写，isOpenAIModelMappingRuntimeConversionSupported 的
		// source==upstream 分支放行）。
		FamilyVideoGeneration,
		FamilyTts:
		return true
	}
	return false
}

// isOpenAIProtocolProfile mirrors isOpenAIProtocolProfile.
func isOpenAIProtocolProfile(account *RuntimeAccount) bool {
	return gatewayNormalize(account.ProtocolCode) == ProtocolCode &&
		gatewayNormalize(account.ProtocolVersion) == ProtocolVersion
}

func isHybridProviderCode(account *RuntimeAccount) bool {
	return gatewayNormalize(account.ProviderCode) == "hybrid"
}

// isProfileExcludedSourceFamily 是协议档案排除的唯一判定源：Gemini
// OpenAI-chat 档案账户不接受 anthropic messages 源族映射（账户模型映射
// 代码侧第③道判定，先于恒等拒绝与转换矩阵）。
func isProfileExcludedSourceFamily(account *RuntimeAccount, sourceEndpointFamily string) bool {
	return account != nil &&
		account.ProviderProtocolProfileID == GeminiOpenAIChatProfileID &&
		sourceEndpointFamily == FamilyAnthropicMessages
}

func gatewayNormalize(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized
}

// ResolveAccountModelMapping mirrors resolveOpenAIAccountModelMapping.
func ResolveAccountModelMapping(account *RuntimeAccount, requestedModel, sourceEndpointFamily string) *gatewayproto.ResolvedModelMapping {
	model := strings.TrimSpace(requestedModel)
	if model == "" || sourceEndpointFamily == "" {
		return nil
	}
	if !isAccountModelMappingSourceEndpointFamily(sourceEndpointFamily) {
		return nil
	}
	if isProfileExcludedSourceFamily(account, sourceEndpointFamily) {
		return nil
	}
	mappings := accountMappings(account)
	var mapping *AccountModelMapping
	for index := range mappings {
		item := &mappings[index]
		if item.Enabled != nil && !*item.Enabled {
			continue
		}
		if item.SourceModel == model && item.SourceEndpointFamily == sourceEndpointFamily {
			mapping = item
			break
		}
	}
	if mapping == nil {
		return nil
	}
	// 命中行之后的三道判定（恒等拒绝、转换矩阵，连同已满足的启用、源族
	// 可接受集、档案排除）统一走 IsAdmissibleModelMapping 唯一函数源；
	// 请求级预检与循环内启用跳过保持既有早退语义不变。
	if !IsAdmissibleModelMapping(*mapping, account) {
		return nil
	}
	resolved := &gatewayproto.ResolvedModelMapping{
		SourceModel:            mapping.SourceModel,
		SourceEndpointFamily:   mapping.SourceEndpointFamily,
		UpstreamModel:          mapping.UpstreamModel,
		UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
		RuntimeSource:          mapping.RuntimeSource,
		RuntimeRouteRuleID:     mapping.RuntimeRouteRuleID,
	}
	return resolved
}

// IsAdmissibleModelMapping 判定单条账户模型映射行在给定账户（模型事实主体）
// 上是否为运行时可执行的映射：启用 + 源端点族在可接受集 + 协议档案排除 +
// 非恒等改写 + 运行时转换矩阵支持（网关模型列表账户并集设计 4.2）。与
// ResolveAccountModelMapping 内联的同一组判定共享唯一函数源；矩阵演化时
// 两侧自动同步，禁止在任何调用方复刻第二份判定。判定只依赖映射行与账户
// 协议档案/provider，可静态执行；account 为 nil 时无模型事实主体，与
// ResolveAccountModelMapping（nil 账户解析不到任何行）保持一致返回 false。
func IsAdmissibleModelMapping(mapping AccountModelMapping, account *RuntimeAccount) bool {
	if account == nil {
		return false
	}
	if mapping.Enabled != nil && !*mapping.Enabled {
		return false
	}
	if !isAccountModelMappingSourceEndpointFamily(mapping.SourceEndpointFamily) {
		return false
	}
	if isProfileExcludedSourceFamily(account, mapping.SourceEndpointFamily) {
		return false
	}
	if mapping.UpstreamModel == mapping.SourceModel && mapping.UpstreamEndpointFamily == mapping.SourceEndpointFamily {
		return false
	}
	return isOpenAIModelMappingRuntimeConversionSupported(&mapping, account)
}

func accountMappings(account *RuntimeAccount) []AccountModelMapping {
	if account == nil {
		return nil
	}
	return account.ModelMappings
}

// isOpenAIModelMappingRuntimeConversionSupported mirrors
// isOpenAIModelMappingRuntimeConversionSupported.
func isOpenAIModelMappingRuntimeConversionSupported(mapping *AccountModelMapping, account *RuntimeAccount) bool {
	source := mapping.SourceEndpointFamily
	upstream := mapping.UpstreamEndpointFamily
	if source == upstream ||
		(source == FamilyGeminiStreamGenerate && upstream == FamilyGeminiGenerateContent) {
		return true
	}
	if source == FamilyResponses && upstream == FamilyChatCompletions && isOpenAIProtocolProfile(account) {
		return true
	}
	if !isHybridProviderCode(account) {
		return false
	}
	switch {
	case source == FamilyResponses && upstream == FamilyChatCompletions:
		return true
	case source == FamilyAnthropicMessages && upstream == FamilyChatCompletions:
		return true
	case (source == FamilyGeminiGenerateContent || source == FamilyGeminiStreamGenerate) && upstream == FamilyChatCompletions:
		return true
	case source == FamilyChatCompletions && upstream == FamilyAnthropicMessages:
		return true
	case source == FamilyResponses && upstream == FamilyAnthropicMessages:
		return true
	case (source == FamilyGeminiGenerateContent || source == FamilyGeminiStreamGenerate) && upstream == FamilyAnthropicMessages:
		return true
	case source == FamilyChatCompletions && upstream == FamilyGeminiGenerateContent:
		return true
	case source == FamilyResponses && upstream == FamilyGeminiGenerateContent:
		return true
	case source == FamilyAnthropicMessages && upstream == FamilyGeminiGenerateContent:
		return true
	}
	return false
}

// isOpenAIResponsesToChatCompletionsModelMapping mirrors the same-named Node
// helper.
func isOpenAIResponsesToChatCompletionsModelMapping(mapping *gatewayproto.ResolvedModelMapping) bool {
	return mapping != nil &&
		mapping.SourceEndpointFamily == FamilyResponses &&
		mapping.UpstreamEndpointFamily == FamilyChatCompletions
}

// isAnthropicMessagesToChatCompletionsModelMapping mirrors the Node helper.
func isAnthropicMessagesToChatCompletionsModelMapping(mapping *gatewayproto.ResolvedModelMapping) bool {
	return mapping != nil &&
		mapping.SourceEndpointFamily == FamilyAnthropicMessages &&
		mapping.UpstreamEndpointFamily == FamilyChatCompletions
}

// isGeminiGenerateContentToChatCompletionsModelMapping mirrors the Node helper.
func isGeminiGenerateContentToChatCompletionsModelMapping(mapping *gatewayproto.ResolvedModelMapping) bool {
	if mapping == nil {
		return false
	}
	sourceIsGemini := mapping.SourceEndpointFamily == FamilyGeminiGenerateContent ||
		mapping.SourceEndpointFamily == FamilyGeminiStreamGenerate
	return sourceIsGemini && mapping.UpstreamEndpointFamily == FamilyChatCompletions
}

// isCrossProtocolBridgeToAnthropicMessagesModelMapping mirrors
// isOpenAIToAnthropicMessagesModelMapping (openai-anthropic-bridge.ts:410-416):
// an openai chat/responses source mapped onto the anthropic messages upstream
// family.
func isCrossProtocolBridgeToAnthropicMessagesModelMapping(mapping *gatewayproto.ResolvedModelMapping) bool {
	if mapping == nil {
		return false
	}
	source := mapping.SourceEndpointFamily
	return (source == FamilyChatCompletions || source == FamilyResponses) &&
		NormalizeAnthropicFamily(mapping.UpstreamEndpointFamily)
}

// NormalizeAnthropicFamily reports whether the token is the anthropic
// messages upstream family (either protocol name or the stored row token).
func NormalizeAnthropicFamily(family string) bool {
	return family == "anthropic_messages" || family == "messages"
}

// ModelMappedUpstreamPathAndQuery mirrors openAIModelMappedUpstreamPathAndQuery
// for the chat_completions-targeted rewrites, plus the cross-protocol
// anthropic target rewrite (Node openAIToAnthropicBridgeUpstreamPath:
// /messages + the client query; audit B-10 openAIToAnthropicBridgeUpstreamPath).
// Exported for the chain URL builder so the upstream path carries the same
// mapping rewrite as the driver body transform (BUG-0178).
func ModelMappedUpstreamPathAndQuery(originalPathAndQuery string, mapping *gatewayproto.ResolvedModelMapping) (string, bool) {
	_, query := SplitPathAndQuery(originalPathAndQuery)
	switch {
	case isOpenAIResponsesToChatCompletionsModelMapping(mapping):
		return "/chat/completions" + query, true
	case isAnthropicMessagesToChatCompletionsModelMapping(mapping):
		return "/chat/completions" + query, true
	case isGeminiGenerateContentToChatCompletionsModelMapping(mapping):
		return "/chat/completions" + geminiGenerateContentBridgeQuery(query), true
	case isCrossProtocolBridgeToAnthropicMessagesModelMapping(mapping):
		return "/messages" + query, true
	}
	return originalPathAndQuery, false
}

// geminiGenerateContentBridgeQuery mirrors geminiGenerateContentBridgeQuery:
// drop alt/key, keep the rest.
func geminiGenerateContentBridgeQuery(query string) string {
	if query == "" {
		return ""
	}
	values, err := url.ParseQuery(strings.TrimPrefix(query, "?"))
	if err != nil {
		return ""
	}
	values.Del("alt")
	values.Del("key")
	encoded := values.Encode()
	if encoded == "" {
		return ""
	}
	return "?" + encoded
}
