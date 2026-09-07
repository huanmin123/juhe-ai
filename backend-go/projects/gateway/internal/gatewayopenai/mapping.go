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
		FamilyGeminiStreamGenerate:
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

func gatewayNormalize(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized
}

// NormalizeModelKey returns the case-insensitive lookup key used for model
// matching. The stored model string remains canonical and is never rewritten
// by this helper; callers use the configured catalog/account value when they
// build the upstream request.
func NormalizeModelKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// ModelsEqual reports whether two model IDs refer to the same logical model
// for gateway matching. Model IDs are trimmed and compared case-insensitively,
// while their original spelling remains available for upstream forwarding.
func ModelsEqual(left, right string) bool {
	leftKey := NormalizeModelKey(left)
	return leftKey != "" && leftKey == NormalizeModelKey(right)
}

// CanonicalModel returns the first configured model whose lookup key matches
// requestedModel. The returned value preserves the configured spelling so the
// upstream receives the account/catalog canonical model ID.
func CanonicalModel(requestedModel string, configured []string) string {
	for _, candidate := range configured {
		if ModelsEqual(requestedModel, candidate) {
			return strings.TrimSpace(candidate)
		}
	}
	return ""
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
	if account != nil &&
		account.ProviderProtocolProfileID == GeminiOpenAIChatProfileID &&
		sourceEndpointFamily == FamilyAnthropicMessages {
		return nil
	}
	mappings := accountMappings(account)
	var mapping *AccountModelMapping
	for index := range mappings {
		item := &mappings[index]
		if item.Enabled != nil && !*item.Enabled {
			continue
		}
		if ModelsEqual(item.SourceModel, model) && item.SourceEndpointFamily == sourceEndpointFamily {
			mapping = item
			break
		}
	}
	if mapping == nil {
		return nil
	}
	if mapping.UpstreamModel == mapping.SourceModel && mapping.UpstreamEndpointFamily == mapping.SourceEndpointFamily {
		return nil
	}
	if mapping.RuntimeSource == RuntimeSourceExplicitHybridRoute {
		return nil
	}
	if !isOpenAIModelMappingRuntimeConversionSupported(mapping, account) {
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

// modelMappedUpstreamPathAndQuery mirrors openAIModelMappedUpstreamPathAndQuery
// for the chat_completions-targeted rewrites. Cross-protocol upstream
// families (anthropic/gemini native) belong to the conversion slices and are
// rejected by the driver before this helper is consulted.
func modelMappedUpstreamPathAndQuery(originalPathAndQuery string, mapping *gatewayproto.ResolvedModelMapping) (string, bool) {
	_, query := SplitPathAndQuery(originalPathAndQuery)
	switch {
	case isOpenAIResponsesToChatCompletionsModelMapping(mapping):
		return "/chat/completions" + query, true
	case isAnthropicMessagesToChatCompletionsModelMapping(mapping):
		return "/chat/completions" + query, true
	case isGeminiGenerateContentToChatCompletionsModelMapping(mapping):
		return "/chat/completions" + geminiGenerateContentBridgeQuery(query), true
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
