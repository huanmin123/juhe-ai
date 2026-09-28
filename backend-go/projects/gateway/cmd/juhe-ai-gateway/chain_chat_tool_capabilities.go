package main

// BUG-0175 D-201: the chat ToolCapabilitiesResolver port wired at the
// composition root (chat.Deps.ToolCapabilit, GET
// ${systemApiPrefix}/my-chat/conversations/{id} toolCapabilities payload).
//
// Node authority: chat.routes.ts:1548-1626 loadChatConversationToolCapabilities
// — the web_search / generate_image availability matrix with the per-tool
// unavailable reasons and the catch-branch fallback shape. The resolver rides
// the same generation-wave ports the mount already assembles (ChatKeys +
// GatewayKeys + ModelCatalog，group/account 绑定模式另用 GroupLookup /
// AccountLookup 与发送侧 resolveChatBindingScope 对齐聚合口径); the small
// transport-selection helpers below mirror the archived chat-transport.ts /
// chat-model-options.ts logic because the chat package keeps its ports
// unexported and this file owns the wiring side. Catalog snapshot assembly
// mirrors the chat package's loadChatModelCatalogSnapshot (account fan-out per
// group + provider catalog lists); the archived constrainCatalogItemForAccountTypes
// only narrows service tiers, which the tool matrix never reads.

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
	// 绑定账户聚合口径（与发送侧 resolveChatBindingScope 对齐）：收敛为绑定
	// 账户的运行时传输视图（不在任何启用分组快照中时为空作用域）。绑定对象
	// 校验失败沿发送侧 400 文案作 unavailable reason，服务侧不可用保持 catch
	// 文案；归档/未选账户会话给只读/引导文案。
	scope, unavailableReason := chatToolBindScopeFor(deps, conversation, ownerID)
	if unavailableReason != "" {
		return unavailable(unavailableReason)
	}
	_, catalogItems := chatToolCatalogForScope(deps, scope, ownerID, model)
	// chatToolModelOption 恒返回非 nil（无目录行时返回空能力视图，w2 登记）。
	option := chatToolModelOption(model, catalogItems)
	supportedProtocols := chatToolProtocolsForScope(deps, scope, ownerID, model)
	supportsWebSearch := chatToolContains(option.supportedTools, "web_search")
	protocol := ""
	if len(supportedProtocols) > 0 {
		protocol = string(chatToolSelectTransport(supportedProtocols, supportsWebSearch))
	}
	webSearchAvailable := supportsWebSearch && protocol == string(chat.ProtocolResponses)
	imagePermissionEnabled := gatewayKey.ImageGenerationEnabled
	functionCallingAvailable := chatToolContains(option.supportedTools, "function_calling")
	imageRouteAvailable := chatToolImageRouteForScope(deps, scope, ownerID)
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
			imageReason = "当前 API Key 路由没有可用的图像生成 API Key 账户"
		}
	}
	return chatToolCapabilitiesPayload(modelValue,
		chatToolEntry("web_search", "网页搜索", webSearchAvailable, webSearchReason),
		chatToolEntry("generate_image", "图片生成", imageGenerationAvailable, imageReason))
}

// chatToolCapabilitiesCatchReason mirrors the Node catch branch copy.
const chatToolCapabilitiesCatchReason = "工具能力状态暂时无法读取"

// chatToolBindScope 是能力读取的绑定作用域：收敛为绑定账户的单元素传输
// 视图。镜像 chat 包的 chatBindingScope（包内类型未导出，镜像契约同
// generation_deps.go / stream_route.go）。
type chatToolBindScope struct {
	accounts []chat.ChatTransportAccount
}

// chatToolBindScopeFor 按会话绑定账户解析聚合口径，镜像发送侧
// resolveChatBindingScope（generation_deps.go）：归档（存量旧模式）会话只读
// 提示；未选账户提示先选账户；绑定账户须在数据范围内存在且启用（经启用分组
// 快照收敛传输视图）。
func chatToolBindScopeFor(deps *chat.Deps, conversation *chat.Conversation, ownerID string) (*chatToolBindScope, string) {
	if conversation.Archived {
		return nil, "该会话绑定方式已升级，请新建会话"
	}
	if conversation.BindAccountID == nil {
		return nil, "当前会话尚未选择 AI 账户"
	}
	if deps.AccountLookup == nil {
		return nil, chatToolCapabilitiesCatchReason
	}
	ref, err := deps.AccountLookup.FindChatAccount(chat.ChatBindScope{ViewerID: ownerID}, derefString(conversation.BindAccountID))
	if err != nil {
		return nil, chatToolCapabilitiesCatchReason
	}
	if ref == nil {
		return nil, "会话绑定的账户不存在或已删除"
	}
	if !ref.Enabled {
		return nil, "会话绑定的账户已停用"
	}
	return &chatToolBindScope{accounts: chatToolConvergeAccount(deps, ref, ownerID)}, ""
}

// chatToolConvergeAccount 镜像 chat 包 convergeChatAccountScope：按绑定账户
// 的启用分组快照收敛为单元素传输视图；不在任何启用分组快照中时为空作用域。
func chatToolConvergeAccount(deps *chat.Deps, ref *chat.ChatAccountRef, systemAccountID string) []chat.ChatTransportAccount {
	if deps == nil || deps.ModelCatalog == nil {
		return []chat.ChatTransportAccount{}
	}
	for _, groupID := range chatToolUniqueStrings(ref.EnabledGroupIDs) {
		for _, account := range deps.ModelCatalog.ListAccountsForGroup(groupID, systemAccountID, "", "") {
			if account.ID == ref.ID {
				return []chat.ChatTransportAccount{account}
			}
		}
	}
	return []chat.ChatTransportAccount{}
}

// chatToolCatalogForScope 按绑定作用域取账户快照与目录：单元素视图 +
// provider 目录单值（镜像 loadChatModelCatalogForScope）。
func chatToolCatalogForScope(deps *chat.Deps, scope *chatToolBindScope, systemAccountID, requestedModel string) ([]chat.ChatTransportAccount, []chat.ProviderModelCatalogItem) {
	catalog := []chat.ProviderModelCatalogItem{}
	if deps == nil || deps.ModelCatalog == nil {
		return scope.accounts, catalog
	}
	for _, account := range scope.accounts {
		code := strings.ToLower(strings.TrimSpace(account.ProviderCode))
		if code == "" {
			continue
		}
		catalog = append(catalog, deps.ModelCatalog.ListProviderCatalog(code, systemAccountID)...)
	}
	return scope.accounts, catalog
}

// chatToolProtocolsForScope 在收敛后的单账户视图上按固定顺序判定双协议
// （镜像 scopeSupportedProtocols）。
func chatToolProtocolsForScope(deps *chat.Deps, scope *chatToolBindScope, systemAccountID, model string) []chat.ChatTransportProtocol {
	supported := []chat.ChatTransportProtocol{}
	for _, protocol := range []chat.ChatTransportProtocol{chat.ProtocolChatCompletions, chat.ProtocolResponses} {
		for _, account := range scope.accounts {
			if chatToolAccountSupportsProtocol(account, model, protocol) {
				supported = append(supported, protocol)
				break
			}
		}
	}
	return supported
}

// chatToolImageRouteForScope 在收敛后的单账户视图上判断 api_key 类型账户
// （镜像 scopeHasImageGenerationRoute）。
func chatToolImageRouteForScope(deps *chat.Deps, scope *chatToolBindScope, systemAccountID string) bool {
	for _, account := range scope.accounts {
		if account.Type == "api_key" {
			return true
		}
	}
	return false
}

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
