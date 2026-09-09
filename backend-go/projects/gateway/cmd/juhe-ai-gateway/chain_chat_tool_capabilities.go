package main

// BUG-0175 D-201: the chat ToolCapabilitiesResolver port wired at the
// composition root (chat.Deps.ToolCapabilit, GET
// ${systemApiPrefix}/my-chat/conversations/{id} toolCapabilities payload).
//
// Node authority: chat.routes.ts:1548-1626 loadChatConversationToolCapabilities
// — the web_search / generate_image availability matrix with the per-tool
// unavailable reasons and the catch-branch fallback shape. The resolver rides
// the same generation-wave ports the mount already assembles (ChatKeys +
// GatewayKeys + ModelCatalog); the small transport-selection helpers below
// mirror the archived chat-transport.ts / chat-model-options.ts logic because
// the chat package keeps its ports unexported and this file owns the wiring
// side. Catalog snapshot assembly mirrors the chat package's
// loadChatModelCatalogSnapshot (account fan-out per group + provider catalog
// lists); the archived constrainCatalogItemForAccountTypes only narrows
// service tiers, which the tool matrix never reads.

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

// newChatToolCapabilitiesResolver builds the D-201 resolver over the mounted
// chat Deps ports (nil ports degrade to the Node catch-branch shape at
// resolve time).
func newChatToolCapabilitiesResolver(deps *chat.Deps) chat.ToolCapabilitiesResolver {
	return func(conversation *chat.Conversation, ownerID string) any {
		return resolveChatToolCapabilities(deps, conversation, ownerID)
	}
}

// resolveChatToolCapabilities mirrors loadChatConversationToolCapabilities
// (chat.routes.ts:1548-1626).
func resolveChatToolCapabilities(deps *chat.Deps, conversation *chat.Conversation, ownerID string) any {
	modelValue := chatToolTrimmedModel(conversation)
	model, _ := modelValue.(string)
	unavailable := func(reason string) any {
		return chatToolCapabilitiesPayload(modelValue,
			chatToolEntry("web_search", "网页搜索", false, reason),
			chatToolEntry("generate_image", "图片生成", false, reason))
	}
	if model == "" {
		return unavailable("当前会话尚未选择对话模型")
	}
	// requireOwnedApiKey throw → catch branch ('工具能力状态暂时无法读取'):
	// missing key id, missing provider port, row miss and the empty-secret /
	// non-active guards all throw in the Node route.
	if deps == nil || deps.ChatKeys == nil || conversation == nil || conversation.APIKeyID == nil || strings.TrimSpace(*conversation.APIKeyID) == "" {
		return unavailable(chatToolCapabilitiesCatchReason)
	}
	apiKey, err := deps.ChatKeys.FindChatAPIKey(strings.TrimSpace(*conversation.APIKeyID), ownerID)
	if err != nil || apiKey == nil || apiKey.Secret == "" || apiKey.Status != "active" {
		return unavailable(chatToolCapabilitiesCatchReason)
	}
	if deps.GatewayKeys == nil {
		return unavailable(chatToolCapabilitiesCatchReason)
	}
	gatewayKey, err := deps.GatewayKeys.ValidateGatewayKey(apiKey.Secret)
	if err != nil {
		return unavailable(chatToolCapabilitiesCatchReason)
	}
	if gatewayKey == nil {
		return unavailable("会话绑定的 API Key 不可用")
	}
	// Node maps every group binding unfiltered here (unlike
	// loadChatModelAccessAsync); empty/duplicate ids collapse in the fan-out.
	groupIDs := make([]string, 0, len(gatewayKey.GroupBindings))
	for _, binding := range gatewayKey.GroupBindings {
		groupIDs = append(groupIDs, binding.GroupID)
	}
	_, catalogItems := chatToolCatalogSnapshot(deps, groupIDs, ownerID, model)
	option := chatToolModelOption(model, catalogItems)
	if option == nil {
		return unavailable("当前模型能力信息不可用")
	}
	supportedProtocols := chatToolSupportedProtocols(deps, groupIDs, ownerID, model)
	supportsWebSearch := chatToolContains(option.supportedTools, "web_search")
	protocol := ""
	if len(supportedProtocols) > 0 {
		protocol = string(chatToolSelectTransport(supportedProtocols, supportsWebSearch))
	}
	webSearchAvailable := supportsWebSearch && protocol == string(chat.ProtocolResponses)
	imagePermissionEnabled := gatewayKey.ImageGenerationEnabled
	functionCallingAvailable := chatToolContains(option.supportedTools, "function_calling")
	imageRouteAvailable := chatToolHasImageGenerationRoute(deps, groupIDs, ownerID)
	imageGenerationAvailable := len(supportedProtocols) > 0 && imagePermissionEnabled && functionCallingAvailable && imageRouteAvailable

	webSearchReason := ""
	if !webSearchAvailable {
		switch {
		case len(supportedProtocols) == 0:
			webSearchReason = "当前 API Key 没有可用的对话路由"
		case !supportsWebSearch:
			webSearchReason = "当前模型不支持网页搜索"
		default:
			webSearchReason = "当前路由不支持 Responses 网页搜索"
		}
	}
	imageReason := ""
	if !imageGenerationAvailable {
		switch {
		case len(supportedProtocols) == 0:
			imageReason = "当前 API Key 没有可用的对话路由"
		case !imagePermissionEnabled:
			imageReason = "当前用户未开启图片生成"
		case !functionCallingAvailable:
			imageReason = "当前模型不支持函数工具调用"
		default:
			imageReason = "当前 API Key 路由没有可用的 gpt-image-2 API Key 账户"
		}
	}
	return chatToolCapabilitiesPayload(modelValue,
		chatToolEntry("web_search", "网页搜索", webSearchAvailable, webSearchReason),
		chatToolEntry("generate_image", "图片生成", imageGenerationAvailable, imageReason))
}

// chatToolCapabilitiesCatchReason mirrors the Node catch branch copy.
const chatToolCapabilitiesCatchReason = "工具能力状态暂时无法读取"

// chatToolCapabilitiesPayload renders the {model, tools:[...]} response shape;
// an unavailable tool carries its reason, an available one omits the key
// (Node conditional spread).
func chatToolCapabilitiesPayload(model any, tools ...map[string]any) any {
	if tools == nil {
		tools = []map[string]any{}
	}
	return map[string]any{"model": model, "tools": tools}
}

func chatToolEntry(id, label string, available bool, reason string) map[string]any {
	entry := map[string]any{"id": id, "label": label, "available": available}
	if !available && reason != "" {
		entry["reason"] = reason
	}
	return entry
}

// chatToolTrimmedModel mirrors conversation.lastModel?.trim() for the payload
// (nil when the stored model is absent, matching the chat route fallback).
func chatToolTrimmedModel(conversation *chat.Conversation) any {
	if conversation == nil || conversation.LastModel == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*conversation.LastModel)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

// chatToolModelOption mirrors buildChatModelOptions([model], catalog)[0] for
// the one field the matrix reads: SupportedTools (the capability intersection
// across the model's catalog rows; no rows → empty list, option never nil
// exactly like the archived builder).
func chatToolModelOption(model string, catalog []chat.ProviderModelCatalogItem) *chatToolModelOptionView {
	lists := make([][]string, 0, len(catalog))
	for _, item := range catalog {
		if item.Model != model {
			continue
		}
		lists = append(lists, item.SupportedTools)
	}
	return &chatToolModelOptionView{supportedTools: chatToolIntersectLists(lists)}
}

type chatToolModelOptionView struct {
	supportedTools []string
}

// chatToolCatalogSnapshot mirrors loadChatModelCatalogSnapshot
// (generation_deps.go): per-group account fan-out (requestedModel scoped) then
// one provider catalog list per provider code in first-seen order.
func chatToolCatalogSnapshot(deps *chat.Deps, groupIDs []string, systemAccountID, requestedModel string) ([]chat.ChatTransportAccount, []chat.ProviderModelCatalogItem) {
	if deps == nil || deps.ModelCatalog == nil {
		return nil, nil
	}
	accounts := []chat.ChatTransportAccount{}
	for _, groupID := range chatToolUniqueStrings(groupIDs) {
		accounts = append(accounts, deps.ModelCatalog.ListAccountsForGroup(groupID, systemAccountID, requestedModel, "")...)
	}
	catalog := []chat.ProviderModelCatalogItem{}
	providerCodes := []string{}
	seen := map[string]bool{}
	for _, account := range accounts {
		code := strings.ToLower(strings.TrimSpace(account.ProviderCode))
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		providerCodes = append(providerCodes, code)
	}
	for _, providerCode := range providerCodes {
		catalog = append(catalog, deps.ModelCatalog.ListProviderCatalog(providerCode, systemAccountID)...)
	}
	return accounts, catalog
}

// chatToolSupportedProtocols mirrors resolveChatSupportedProtocols
// (chat-transport.ts): per group, keep chat_completions/responses when any
// account supports the model over that protocol's endpoint family.
func chatToolSupportedProtocols(deps *chat.Deps, groupIDs []string, systemAccountID, model string) []chat.ChatTransportProtocol {
	protocolOrder := []chat.ChatTransportProtocol{chat.ProtocolChatCompletions, chat.ProtocolResponses}
	supported := map[chat.ChatTransportProtocol]bool{}
	for _, groupID := range chatToolUniqueStrings(groupIDs) {
		for _, protocol := range protocolOrder {
			if supported[protocol] || deps == nil || deps.ModelCatalog == nil {
				continue
			}
			accounts := deps.ModelCatalog.ListAccountsForGroup(groupID, systemAccountID, model, string(protocol))
			for _, account := range accounts {
				if chatToolAccountSupportsProtocol(account, model, protocol) {
					supported[protocol] = true
					break
				}
			}
		}
		if supported[chat.ProtocolChatCompletions] && supported[chat.ProtocolResponses] {
			break
		}
	}
	out := []chat.ChatTransportProtocol{}
	for _, protocol := range protocolOrder {
		if supported[protocol] {
			out = append(out, protocol)
		}
	}
	return out
}

// chatToolAccountSupportsProtocol mirrors chatTransportAccountSupportsProtocol
// (chat-transport.ts): the enabled source-model mapping (with endpoint family
// gate and upstream reroute), the canonical supported-models gate, then the
// upstream endpoint family's SSE mode requirement.
func chatToolAccountSupportsProtocol(account chat.ChatTransportAccount, model string, protocol chat.ChatTransportProtocol) bool {
	var mapping *chat.ChatTransportModelMapping
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
		upstreamProtocol = chat.ChatTransportProtocol(mapping.UpstreamEndpointFamily)
	}
	requiredMode := ""
	switch upstreamProtocol {
	case chat.ProtocolResponses:
		requiredMode = "responses_sse"
	case chat.ProtocolChatCompletions:
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

// chatToolSelectTransport mirrors selectChatTransport (chat-transport.ts).
func chatToolSelectTransport(supportedProtocols []chat.ChatTransportProtocol, preferResponses bool) chat.ChatTransportProtocol {
	has := func(protocol chat.ChatTransportProtocol) bool {
		for _, candidate := range supportedProtocols {
			if candidate == protocol {
				return true
			}
		}
		return false
	}
	if preferResponses && has(chat.ProtocolResponses) {
		return chat.ProtocolResponses
	}
	if has(chat.ProtocolChatCompletions) {
		return chat.ProtocolChatCompletions
	}
	if has(chat.ProtocolResponses) {
		return chat.ProtocolResponses
	}
	return chat.ProtocolChatCompletions
}

// chatToolHasImageGenerationRoute mirrors hasChatImageGenerationRoute
// (generation_deps.go): any api_key-type account routed for gpt-image-2.
func chatToolHasImageGenerationRoute(deps *chat.Deps, groupIDs []string, systemAccountID string) bool {
	if deps == nil || deps.ModelCatalog == nil {
		return false
	}
	for _, groupID := range chatToolUniqueStrings(groupIDs) {
		accounts := deps.ModelCatalog.ListAccountsForGroup(groupID, systemAccountID, "gpt-image-2", "")
		for _, account := range accounts {
			if account.Type == "api_key" {
				return true
			}
		}
	}
	return false
}

func chatToolUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func chatToolContains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func chatToolIntersectLists(lists [][]string) []string {
	if len(lists) == 0 {
		return []string{}
	}
	seen := map[string]bool{}
	orderedFirst := []string{}
	for _, value := range lists[0] {
		if !seen[value] {
			seen[value] = true
			orderedFirst = append(orderedFirst, value)
		}
	}
	out := []string{}
	for _, value := range orderedFirst {
		all := true
		for _, other := range lists[1:] {
			if !chatToolContains(other, value) {
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
