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
	// 绑定模式聚合口径（与发送侧 resolveChatBindingScope 对齐）：api_key/
	// legacy 沿 Key 视图分组绑定全量（Node 原口径，空/重复 id 在 fan-out 收
	// 敛）；group 按会话绑定分组的账户候选聚合；account 收敛为绑定账户的运
	// 行时传输视图（不在任何启用分组快照中时为空作用域）。绑定对象校验失败
	// 沿发送侧 400 文案作 unavailable reason，服务侧不可用保持 catch 文案。
	scope, unavailableReason := chatToolBindScopeFor(deps, conversation, gatewayKey, ownerID)
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

// chatToolBindScope 是能力读取的绑定作用域：api_key/group 按分组聚合，
// account 收敛为绑定账户的单元素传输视图。镜像 chat 包的 chatBindingScope
// （包内类型未导出，镜像契约同 generation_deps.go / stream_route.go）。
type chatToolBindScope struct {
	bindMode string
	groupIDs []string
	accounts []chat.ChatTransportAccount
}

// chatToolBindScopeFor 按会话 bind_mode 解析聚合口径，镜像发送侧
// resolveChatBindingScope（generation_deps.go）：api_key/legacy = Key 视图
// 分组绑定全量；group = 会话绑定分组（数据范围内存在且启用）；account =
// 绑定账户（数据范围内存在且启用，经启用分组快照收敛传输视图）。
func chatToolBindScopeFor(deps *chat.Deps, conversation *chat.Conversation, gatewayKey *chat.GatewayKeyView, ownerID string) (*chatToolBindScope, string) {
	switch conversation.BindMode {
	case chat.BindModeGroup:
		if deps.GroupLookup == nil {
			return nil, chatToolCapabilitiesCatchReason
		}
		group, err := deps.GroupLookup.FindChatGroup(chat.ChatBindScope{ViewerID: ownerID}, derefString(conversation.BindGroupID))
		if err != nil {
			return nil, chatToolCapabilitiesCatchReason
		}
		if group == nil {
			return nil, "会话绑定的分组不存在或已删除"
		}
		if !group.Enabled {
			return nil, "会话绑定的分组已停用"
		}
		return &chatToolBindScope{bindMode: chat.BindModeGroup, groupIDs: []string{group.ID}}, ""
	case chat.BindModeAccount:
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
		return &chatToolBindScope{bindMode: chat.BindModeAccount, accounts: chatToolConvergeAccount(deps, ref, ownerID)}, ""
	default:
		// api_key/legacy：Node 原口径——Key 视图分组绑定全量（unfiltered，
		// 空/重复 id 在 fan-out 收敛）。
		groupIDs := make([]string, 0, len(gatewayKey.GroupBindings))
		for _, binding := range gatewayKey.GroupBindings {
			groupIDs = append(groupIDs, binding.GroupID)
		}
		return &chatToolBindScope{bindMode: chat.BindModeAPIKey, groupIDs: groupIDs}, ""
	}
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

// chatToolCatalogForScope 按绑定作用域取账户快照与目录：api_key/group 沿
// 分组 fan-out；account 取单元素视图 + provider 目录单值（镜像
// loadChatModelCatalogForScope 的 account 分支）。
func chatToolCatalogForScope(deps *chat.Deps, scope *chatToolBindScope, systemAccountID, requestedModel string) ([]chat.ChatTransportAccount, []chat.ProviderModelCatalogItem) {
	if scope.bindMode != chat.BindModeAccount {
		return chatToolCatalogSnapshot(deps, scope.groupIDs, systemAccountID, requestedModel)
	}
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

// chatToolProtocolsForScope 按绑定作用域判定双协议：api_key/group 沿分组
// 快照路径；account 在收敛后的单账户视图上按固定顺序判定（镜像
// scopeSupportedProtocols 的 account 分支）。
func chatToolProtocolsForScope(deps *chat.Deps, scope *chatToolBindScope, systemAccountID, model string) []chat.ChatTransportProtocol {
	if scope.bindMode != chat.BindModeAccount {
		return chatToolSupportedProtocols(deps, scope.groupIDs, systemAccountID, model)
	}
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

// chatToolImageRouteForScope 按绑定作用域判断生图路由：api_key/group 沿分组
// 路径；account 在收敛后的单账户视图上判断 api_key 类型账户（镜像
// scopeHasImageGenerationRoute 的 account 分支）。
func chatToolImageRouteForScope(deps *chat.Deps, scope *chatToolBindScope, systemAccountID string) bool {
	if scope.bindMode != chat.BindModeAccount {
		return chatToolHasImageGenerationRoute(deps, scope.groupIDs, systemAccountID)
	}
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
// (generation_deps.go): 任一注册图像模型存在 api_key 类型账户即视为有生图路由；
// 与 chat 包实现保持镜像（chat 包 ports 未导出，镜像契约见 generation_deps.go）。
func chatToolHasImageGenerationRoute(deps *chat.Deps, groupIDs []string, systemAccountID string) bool {
	if deps == nil || deps.ModelCatalog == nil {
		return false
	}
	for _, groupID := range chatToolUniqueStrings(groupIDs) {
		for _, model := range chat.SupportedChatImageModels() {
			accounts := deps.ModelCatalog.ListAccountsForGroup(groupID, systemAccountID, string(model), "")
			for _, account := range accounts {
				if account.Type == "api_key" {
					return true
				}
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
