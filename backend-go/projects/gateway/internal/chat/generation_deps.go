package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Generation-wave route handlers: provision (POST /conversations), model
// directory (GET .../models[/{id}]) and the compaction trigger
// (POST .../context/compactions). Route order, status codes and Chinese error
// strings mirror chat.routes.ts.

// modelAccess mirrors loadChatModelAccessAsync.
type modelAccess struct {
	APIKey   *ChatAPIKeyRecord
	GroupIDs []string
}

// --- 会话绑定模式（AI 问答三种绑定模式）端口与作用域 ---

// ChatBindScope 是会话绑定对象的数据范围，普通用户=自有+被授权的启用对象，
// admin/super_admin=全量号池；由 requireChatBindScope 从登录态解析，沿创建
// 校验、下拉端点与发送/模型作用域复核三条链路统一传递。
type ChatBindScope struct {
	ViewerID string
	IsAdmin  bool
}

// ChatGroupLookup resolves a group binding object (group 模式创建与模型作用
// 域校验：数据范围内存在且 enabled + 名称快照). Port satisfied at the
// composition root by the groups store; nil disables the group bind mode with
// an explicit error.
type ChatGroupLookup interface {
	FindChatGroup(scope ChatBindScope, groupID string) (*ChatGroupRef, error)
}

// ChatAccountLookup resolves an account binding object (account 模式创建与
// 模型作用域校验：数据范围内存在且启用 + 名称/provider 事实 + 启用分组绑定).
// Port satisfied at the composition root by the accounts store; nil disables
// the account bind mode with an explicit error.
type ChatAccountLookup interface {
	FindChatAccount(scope ChatBindScope, accountID string) (*ChatAccountRef, error)
}

// ChatBindOption 是新建会话绑定下拉的最小 id/name 投影：不含归属、供应商、
// 协议、授权状态等任何管理面字段，避免为登录用户放开管理面 options 端点。
type ChatBindOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ChatGroupOptionsLookup 列出新建会话绑定下拉的启用分组最小摘要（仅有效
// enabled 行；数据范围与 FindChatGroup 同口径）。ctx 供实现复用带 context 的
// Options 查询。Port satisfied at the composition root by the groups store;
// nil 让绑定下拉端点返回显式错误。
type ChatGroupOptionsLookup interface {
	ListChatGroupOptions(ctx context.Context, scope ChatBindScope) ([]ChatBindOption, error)
}

// ChatAccountOptionsLookup 列出新建会话绑定下拉的可绑定账户最小摘要（口径
// 与 FindChatAccount 完全一致：数据范围内未删、非授权实例戳行、生效状态
// active）。ctx 供实现复用带 context 的查询。Port satisfied at the
// composition root by the accounts store; nil 让绑定下拉端点返回显式错误。
type ChatAccountOptionsLookup interface {
	ListChatAccountOptions(ctx context.Context, scope ChatBindScope) ([]ChatBindOption, error)
}

// ChatGroupRef is the read-only group view the bind modes rely on. Enabled
// mirrors groups.enabled.
type ChatGroupRef struct {
	ID      string
	Name    string
	Enabled bool
}

// ChatAccountRef is the read-only account view the bind modes rely on.
// Enabled follows the /accounts/options 启用口径（effective status active，
// 与管理面账户下拉一致）；EnabledGroupIDs 是该账户 enabled=1 的分组绑定，
// 供模型作用域复用运行时账户快照。
type ChatAccountRef struct {
	ID              string
	Name            string
	ProviderCode    string
	Enabled         bool
	EnabledGroupIDs []string
}

// chatBindingScope is the resolved per-conversation model scope: api_key 与
// group 模式经分组账户快照聚合（groupIDs），account 模式收敛为该账户的
// 运行时传输视图（accounts 单元素，可为空切片 = 空作用域）。
type chatBindingScope struct {
	bindMode string
	groupIDs []string
	accounts []ChatTransportAccount
}

func (d *Deps) traceID(r *http.Request) string {
	if d.TraceID == nil {
		return strings.TrimSpace(r.Header.Get("x-trace-id"))
	}
	return d.TraceID(r)
}

// chatEnvIntOrDefault resolves an integer env override with the Node
// integerConfig semantics (runtime.ts): unset/empty keeps the fallback; a
// non-integer or out-of-range value fails fast instead of being clamped away.
// Node throws at startup; here the package-level callers evaluate the value
// once at init, so the panic keeps the same "bad config never serves traffic"
// contract without routing the env through the composition root.
func chatEnvIntOrDefault(name string, fallback, min, max int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		panic(fmt.Sprintf("%s 必须配置为整数: %q", name, raw))
	}
	if value < min || value > max {
		panic(fmt.Sprintf("%s 必须在 %d-%d 范围内: %d", name, min, max, value))
	}
	return value
}

// defaultMaxConversationsPerUser mirrors runtimeConfig.chat.maxConversationsPerUser
// (JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER, default 50, range 1..1000;
// runtime.ts:687). The original Go fallback of 30 was a migration artifact
// that silently dropped the Node env override.
var defaultMaxConversationsPerUser = chatEnvIntOrDefault("JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER", 50, 1, 1000)

// maxConversationsPerUser mirrors runtimeConfig.chat.maxConversationsPerUser.
func (d *Deps) maxConversationsPerUser() int {
	if d.MaxConversationsPerUserInt != nil {
		return d.MaxConversationsPerUserInt()
	}
	return defaultMaxConversationsPerUser
}

// requireOwnedApiKey mirrors requireOwnedApiKey. 用户引用的 Key 缺失/停用/
// 已删除属于可恢复输入错误（400 chat_invalid_request）；ChatKeys 端口未接线
// 属服务端问题，保持 DomainError（500）。
func (rt *chatRoutes) requireOwnedApiKey(apiKeyID, ownerID string) (*ChatAPIKeyRecord, error) {
	if apiKeyID == "" {
		return nil, &invalidRequestError{Message: "会话绑定的 API Key 已删除"}
	}
	if rt.deps.ChatKeys == nil {
		return nil, &DomainError{Message: "AI 对话专用 API Key 不存在、已停用或已过期"}
	}
	key, err := rt.deps.ChatKeys.FindChatAPIKey(apiKeyID, ownerID)
	if err != nil {
		return nil, err
	}
	if key == nil || key.Secret == "" || key.Status != "active" {
		return nil, &invalidRequestError{Message: "API Key 不存在或不可用"}
	}
	return key, nil
}

// loadChatModelAccess mirrors loadChatModelAccessAsync. GatewayKeys 未接线是
// 服务端装配缺失（与 ChatKeys/GroupLookup/AccountLookup nil 同口径，500）；
// 视图解析不出表达该 Key 在网关运行时不可用（用户引用错误，400）。
func (rt *chatRoutes) loadChatModelAccess(apiKey *ChatAPIKeyRecord) (*modelAccess, error) {
	if rt.deps.GatewayKeys == nil {
		return nil, &DomainError{Message: "API Key 不存在或不可用"}
	}
	gatewayKey, err := rt.deps.GatewayKeys.ValidateGatewayKey(apiKey.Secret)
	if err != nil {
		return nil, err
	}
	if gatewayKey == nil {
		return nil, &invalidRequestError{Message: "API Key 不存在或不可用"}
	}
	groupIDs := []string{}
	seen := map[string]bool{}
	for _, binding := range gatewayKey.GroupBindings {
		if binding.Status != "active" || !binding.GroupEnabled {
			continue
		}
		if binding.GroupID == "" || seen[binding.GroupID] {
			continue
		}
		seen[binding.GroupID] = true
		groupIDs = append(groupIDs, binding.GroupID)
	}
	return &modelAccess{APIKey: apiKey, GroupIDs: groupIDs}, nil
}

// accountsForGroups mirrors the account snapshot fan-out.
func (rt *chatRoutes) accountsForGroups(groupIDs []string, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	if rt.deps.ModelCatalog == nil {
		return []ChatTransportAccount{}
	}
	accounts := []ChatTransportAccount{}
	for _, groupID := range uniqueStrings(groupIDs) {
		accounts = append(accounts, rt.deps.ModelCatalog.ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily)...)
	}
	return accounts
}

// loadChatModelCatalogSnapshot mirrors loadChatModelCatalogSnapshot.
func (rt *chatRoutes) loadChatModelCatalogSnapshot(groupIDs []string, systemAccountID, requestedModel string) ([]ChatTransportAccount, []ProviderModelCatalogItem) {
	accounts := rt.accountsForGroups(groupIDs, systemAccountID, requestedModel, "")
	catalog := []ProviderModelCatalogItem{}
	if rt.deps.ModelCatalog == nil {
		return accounts, catalog
	}
	providerCodes := []string{}
	seen := map[string]bool{}
	for _, account := range accounts {
		code := normalizeProviderToken(account.ProviderCode)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		providerCodes = append(providerCodes, code)
	}
	for _, providerCode := range providerCodes {
		catalog = append(catalog, rt.deps.ModelCatalog.ListProviderCatalog(providerCode, systemAccountID)...)
	}
	return accounts, catalog
}

func normalizeProviderToken(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ""
	}
	return normalized
}

// resolveChatBindingScope 校验会话归属下的绑定对象可用性并解析模型作用域：
// api_key（含存量默认）保持 Key 校验与 Key 视图分组；group/account 校验绑定
// 对象在请求者数据范围（ChatBindScope）内存在且启用——范围外与不存在同型
// (nil, nil)，沿用既有 400 文案。
func (rt *chatRoutes) resolveChatBindingScope(conversation *Conversation, bindScope ChatBindScope) (*chatBindingScope, error) {
	ownerID := bindScope.ViewerID
	switch conversation.BindMode {
	case "", BindModeAPIKey:
		apiKey, err := rt.requireOwnedApiKey(derefString(conversation.APIKeyID), ownerID)
		if err != nil {
			return nil, err
		}
		access, err := rt.loadChatModelAccess(apiKey)
		if err != nil {
			return nil, err
		}
		return &chatBindingScope{bindMode: BindModeAPIKey, groupIDs: access.GroupIDs}, nil
	case BindModeGroup:
		groupID := derefString(conversation.BindGroupID)
		if rt.deps.GroupLookup == nil {
			return nil, &DomainError{Message: "会话绑定分组校验暂不可用，请稍后重试"}
		}
		group, err := rt.deps.GroupLookup.FindChatGroup(bindScope, groupID)
		if err != nil {
			return nil, err
		}
		if group == nil {
			return nil, &invalidRequestError{Message: "会话绑定的分组不存在或已删除"}
		}
		if !group.Enabled {
			return nil, &invalidRequestError{Message: "会话绑定的分组已停用"}
		}
		return &chatBindingScope{bindMode: BindModeGroup, groupIDs: []string{groupID}}, nil
	case BindModeAccount:
		accountID := derefString(conversation.BindAccountID)
		if rt.deps.AccountLookup == nil {
			return nil, &DomainError{Message: "会话绑定账户校验暂不可用，请稍后重试"}
		}
		ref, err := rt.deps.AccountLookup.FindChatAccount(bindScope, accountID)
		if err != nil {
			return nil, err
		}
		if ref == nil {
			return nil, &invalidRequestError{Message: "会话绑定的账户不存在或已删除"}
		}
		if !ref.Enabled {
			return nil, &invalidRequestError{Message: "会话绑定的账户已停用"}
		}
		return &chatBindingScope{bindMode: BindModeAccount, accounts: rt.convergeChatAccountScope(ref, ownerID)}, nil
	default:
		return nil, &DomainError{Message: "会话绑定方式无效"}
	}
}

// convergeChatAccountScope 从运行时账户快照收敛绑定账户的传输视图：遍历该
// 账户 enabled 分组绑定的快照列表并按 ID 收敛为单元素；账户不在任何启用
// 分组快照中时为空作用域（模型列表返回空列表）。
func (rt *chatRoutes) convergeChatAccountScope(ref *ChatAccountRef, systemAccountID string) []ChatTransportAccount {
	for _, groupID := range uniqueStrings(ref.EnabledGroupIDs) {
		for _, account := range rt.accountsForGroups([]string{groupID}, systemAccountID, "", "") {
			if account.ID == ref.ID {
				return []ChatTransportAccount{account}
			}
		}
	}
	return []ChatTransportAccount{}
}

// loadChatModelCatalogForScope mirrors loadChatModelCatalogSnapshot for the
// three bind modes: api_key/group 按分组聚合 provider_codes；account 模式取
// 该账户 provider_code 单值目录，与 account_supported_models 的交集由账户
// 视图的模型/协议过滤自然收窄（空集合表示不限制）。
func (rt *chatRoutes) loadChatModelCatalogForScope(scope *chatBindingScope, systemAccountID, requestedModel string) ([]ChatTransportAccount, []ProviderModelCatalogItem) {
	if scope.bindMode != BindModeAccount {
		return rt.loadChatModelCatalogSnapshot(scope.groupIDs, systemAccountID, requestedModel)
	}
	accounts := scope.accounts
	catalog := []ProviderModelCatalogItem{}
	if rt.deps.ModelCatalog == nil {
		return accounts, catalog
	}
	for _, account := range accounts {
		code := normalizeProviderToken(account.ProviderCode)
		if code == "" {
			continue
		}
		catalog = append(catalog, rt.deps.ModelCatalog.ListProviderCatalog(code, systemAccountID)...)
	}
	return accounts, catalog
}

// loadChatModelListsForScope mirrors loadChatModelListsFromAccountSnapshot
// over the bind-mode scope; 排序与 defaultModel=排序第一项 规则不变。
func (rt *chatRoutes) loadChatModelListsForScope(scope *chatBindingScope, systemAccountID string) ([]ChatModelListOption, *ChatModelListOption, error) {
	accounts, catalog := rt.loadChatModelCatalogForScope(scope, systemAccountID, "")
	modelIDs := make([]string, 0, len(catalog))
	seen := map[string]bool{}
	for _, item := range catalog {
		if seen[item.Model] {
			continue
		}
		seen[item.Model] = true
		modelIDs = append(modelIDs, item.Model)
	}
	modelIDs = sortCatalogModels(modelIDs)
	options := buildChatModelOptions(modelIDs, catalog)
	models := resolveChatModelOptionsFromAccountSnapshot(accounts, options)
	list := make([]ChatModelListOption, 0, len(models))
	for _, model := range models {
		list = append(list, ChatModelListOption{ID: model, Name: model})
	}
	var defaultModel *ChatModelListOption
	if len(list) > 0 {
		defaultModel = &list[0]
	}
	return list, defaultModel, nil
}

// scopeSupportedProtocols mirrors resolveChatSupportedProtocols over the bind
// scope; account 模式直接在收敛后的单账户视图上按固定顺序判定双协议。
func (rt *chatRoutes) scopeSupportedProtocols(scope *chatBindingScope, systemAccountID, model string) []ChatTransportProtocol {
	if scope.bindMode != BindModeAccount {
		return resolveChatSupportedProtocols(scope.groupIDs, model, func(groupID, requestedModel, endpointFamily string) []ChatTransportAccount {
			return rt.accountsForGroups([]string{groupID}, systemAccountID, requestedModel, endpointFamily)
		})
	}
	supported := []ChatTransportProtocol{}
	for _, protocol := range []ChatTransportProtocol{ProtocolChatCompletions, ProtocolResponses} {
		for _, account := range scope.accounts {
			if chatTransportAccountSupportsProtocol(account, model, protocol) {
				supported = append(supported, protocol)
				break
			}
		}
	}
	return supported
}

// constrainChatModelOptionForAccounts mirrors constrainChatModelOptionForAccounts
// (chat.routes.ts:1669-1689): the protocol filter narrows to routes with at
// least one supporting account, and the generation parameter capabilities are
// the per-protocol route constraint intersection (BUG-0175 D-185).
func constrainChatModelOptionForAccounts(option *ChatModelOption, model string, accounts []ChatTransportAccount, requestedProtocols []ChatTransportProtocol) *ChatModelOption {
	protocols := option.SupportedAPIProtocols
	if requestedProtocols != nil {
		protocols = make([]string, 0, len(requestedProtocols))
		for _, protocol := range requestedProtocols {
			protocols = append(protocols, string(protocol))
		}
	}
	supported := []string{}
	capabilityLists := [][]ChatGenerationParameterCapability{}
	for _, protocol := range protocols {
		if protocol != "chat_completions" && protocol != "responses" {
			continue
		}
		routeAccounts := []ChatTransportAccount{}
		for _, account := range accounts {
			if chatTransportAccountSupportsProtocol(account, model, ChatTransportProtocol(protocol)) {
				routeAccounts = append(routeAccounts, account)
			}
		}
		if len(routeAccounts) == 0 {
			continue
		}
		supported = append(supported, protocol)
		capabilityLists = append(capabilityLists, constrainChatGenerationParametersForRoute(
			option.GenerationParameters, model, ChatTransportProtocol(protocol), routeAccounts))
	}
	constrained := *option
	constrained.SupportedAPIProtocols = supported
	constrained.GenerationParameters = intersectGenerationParameterCapabilityLists(capabilityLists)
	return &constrained
}

// hasChatImageGenerationRoute mirrors hasChatImageGenerationRoute: 任一注册
// 图像模型存在 api_key 类型账户即视为有生图路由。镜像实现见
// cmd/juhe-ai-gateway/chain_chat_tool_capabilities.go chatToolHasImageGenerationRoute。
func (rt *chatRoutes) hasChatImageGenerationRoute(groupIDs []string, systemAccountID string) bool {
	for _, model := range SupportedChatImageModels() {
		accounts := rt.accountsForGroups(groupIDs, systemAccountID, string(model), "")
		for _, account := range accounts {
			if account.Type == "api_key" {
				return true
			}
		}
	}
	return false
}

// ChatModelListOption mirrors ChatModelListOption.
type ChatModelListOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// loadChatModelListsFromAccountSnapshot mirrors loadChatModelListsFromAccountSnapshot.
// 保留分组入参形态（api_key 作用域等价），实现统一走绑定作用域。
func (rt *chatRoutes) loadChatModelListsFromAccountSnapshot(groupIDs []string, systemAccountID string) ([]ChatModelListOption, *ChatModelListOption, error) {
	return rt.loadChatModelListsForScope(&chatBindingScope{bindMode: BindModeAPIKey, groupIDs: groupIDs}, systemAccountID)
}

// resolveChatModelOptionsFromAccountSnapshot mirrors
// resolveChatModelOptionsFromAccountSnapshot (chat-model-availability.ts).
func resolveChatModelOptionsFromAccountSnapshot(accounts []ChatTransportAccount, options []*ChatModelOption) []string {
	out := []string{}
	for _, option := range options {
		if len(option.SupportedAPIProtocols) == 0 {
			continue
		}
		reachable := false
		for _, protocol := range option.SupportedAPIProtocols {
			for _, account := range accounts {
				if chatTransportAccountSupportsProtocol(account, option.ID, ChatTransportProtocol(protocol)) {
					reachable = true
					break
				}
			}
			if reachable {
				break
			}
		}
		if reachable {
			out = append(out, option.ID)
		}
	}
	return out
}

// --- handlers ---

// createConversationHandler mirrors POST /conversations (provision side).
// 创建必填 bindMode（api_key|group|account）+ 对应对象；api_key 模式必须
// 显式选择用户自己的 Key；group/account 模式鉴权主体自动用专用 chat Key
// （EnsureChatAPIKey 幂等不变）。旧"省略 apiKeyId 自动绑定"路径删除。
func (rt *chatRoutes) createConversationHandler(w http.ResponseWriter, r *http.Request) {
	raw, err := readJSONBody(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	body, err := decodeObjectBody(raw)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	values := map[string]*string{}
	for key, value := range body {
		switch key {
		case "bindMode", "apiKeyId", "groupId", "accountId":
			text, textErr := boundedTrimmedString(value, defaultStringLimit)
			if textErr != nil {
				// bindMode 空白按缺失处理，与"缺失 bindMode"同一中文错误
				//（compactionTrigger 对"请选择模型"的同款特判模式）。
				if key == "bindMode" && textErr.Error() == "String must contain at least 1 character(s)" {
					writeChatRouteError(w, &invalidRequestError{Message: "请选择会话绑定方式"})
					return
				}
				writeChatRouteError(w, &invalidRequestError{Message: textErr.Error()})
				return
			}
			values[key] = text
		default:
			writeChatRouteError(w, &invalidRequestError{Message: "Unrecognized key: \"" + key + "\""})
			return
		}
	}
	bindModeValue := values["bindMode"]
	if bindModeValue == nil || *bindModeValue == "" {
		writeChatRouteError(w, &invalidRequestError{Message: "请选择会话绑定方式"})
		return
	}
	bindMode := *bindModeValue
	switch bindMode {
	case BindModeAPIKey, BindModeGroup, BindModeAccount:
	default:
		writeChatRouteError(w, &invalidRequestError{Message: "会话绑定方式无效"})
		return
	}
	// 携带与模式不符的键 → 400（严格键校验，与未知键同一错误契约）。
	mismatched := ""
	switch bindMode {
	case BindModeAPIKey:
		if values["groupId"] != nil {
			mismatched = "groupId"
		} else if values["accountId"] != nil {
			mismatched = "accountId"
		}
	case BindModeGroup:
		if values["apiKeyId"] != nil {
			mismatched = "apiKeyId"
		} else if values["accountId"] != nil {
			mismatched = "accountId"
		}
	case BindModeAccount:
		if values["apiKeyId"] != nil {
			mismatched = "apiKeyId"
		} else if values["groupId"] != nil {
			mismatched = "groupId"
		}
	}
	if mismatched != "" {
		writeChatRouteError(w, &invalidRequestError{Message: "Unrecognized key: \"" + mismatched + "\""})
		return
	}
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	bindScope, err := rt.requireChatBindScope(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	var apiKey *ChatAPIKeyRecord
	scope := &chatBindingScope{bindMode: bindMode}
	bindGroupID, bindGroupName := "", ""
	bindAccountID, bindAccountName := "", ""
	switch bindMode {
	case BindModeAPIKey:
		apiKeyID := values["apiKeyId"]
		if apiKeyID == nil {
			writeChatRouteError(w, &invalidRequestError{Message: "请选择会话绑定的 API Key"})
			return
		}
		apiKey, err = rt.requireOwnedApiKey(*apiKeyID, ownerID)
		if err != nil {
			writeChatRouteError(w, err)
			return
		}
		access, accessErr := rt.loadChatModelAccess(apiKey)
		if accessErr != nil {
			writeChatRouteError(w, accessErr)
			return
		}
		scope.groupIDs = access.GroupIDs
	case BindModeGroup:
		groupID := values["groupId"]
		if groupID == nil {
			writeChatRouteError(w, &invalidRequestError{Message: "请选择会话绑定的分组"})
			return
		}
		if rt.deps.GroupLookup == nil {
			writeChatRouteError(w, &DomainError{Message: "会话绑定分组校验暂不可用，请稍后重试"})
			return
		}
		group, groupErr := rt.deps.GroupLookup.FindChatGroup(bindScope, *groupID)
		if groupErr != nil {
			writeChatRouteError(w, groupErr)
			return
		}
		if group == nil {
			writeChatRouteError(w, &invalidRequestError{Message: "绑定的分组不存在"})
			return
		}
		if !group.Enabled {
			writeChatRouteError(w, &invalidRequestError{Message: "绑定的分组已停用"})
			return
		}
		bindGroupID, bindGroupName = group.ID, group.Name
		scope.groupIDs = []string{group.ID}
		apiKey, err = rt.requireChatAPIKeyForOwner(ownerID)
		if err != nil {
			writeChatRouteError(w, err)
			return
		}
	case BindModeAccount:
		accountID := values["accountId"]
		if accountID == nil {
			writeChatRouteError(w, &invalidRequestError{Message: "请选择会话绑定的账户"})
			return
		}
		if rt.deps.AccountLookup == nil {
			writeChatRouteError(w, &DomainError{Message: "会话绑定账户校验暂不可用，请稍后重试"})
			return
		}
		account, accountErr := rt.deps.AccountLookup.FindChatAccount(bindScope, *accountID)
		if accountErr != nil {
			writeChatRouteError(w, accountErr)
			return
		}
		if account == nil {
			writeChatRouteError(w, &invalidRequestError{Message: "绑定的账户不存在"})
			return
		}
		if !account.Enabled {
			writeChatRouteError(w, &invalidRequestError{Message: "绑定的账户已停用"})
			return
		}
		bindAccountID, bindAccountName = account.ID, account.Name
		scope.accounts = rt.convergeChatAccountScope(account, ownerID)
		apiKey, err = rt.requireChatAPIKeyForOwner(ownerID)
		if err != nil {
			writeChatRouteError(w, err)
			return
		}
	}
	_, defaultModel, err := rt.loadChatModelListsForScope(scope, ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	defaultModelID := ""
	if defaultModel != nil {
		defaultModelID = defaultModel.ID
	}
	conversation, err := rt.deps.Store.CreateConversation(CreateConversationInput{
		SystemAccountID:         ownerID,
		APIKeyID:                apiKey.ID,
		APIKeyNameSnapshot:      apiKey.Name,
		BindMode:                bindMode,
		BindGroupID:             bindGroupID,
		BindGroupNameSnapshot:   bindGroupName,
		BindAccountID:           bindAccountID,
		BindAccountNameSnapshot: bindAccountName,
		DefaultModel:            defaultModelID,
		Now:                     rt.now(),
		MaxConversationsPerUser: rt.deps.maxConversationsPerUser(),
	})
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	var payload map[string]any
	rawPayload, marshalErr := json.Marshal(rt.conversationPayload(conversation))
	if marshalErr != nil {
		writeChatRouteError(w, marshalErr)
		return
	}
	_ = json.Unmarshal(rawPayload, &payload)
	if defaultModel != nil {
		payload["defaultModel"] = map[string]any{"id": defaultModel.ID, "name": defaultModel.Name}
	}
	writeOKStatus(w, http.StatusCreated, payload)
}

// defaultStringLimit mirrors the apiKeyId string bound (Node has no explicit
// max; the route uses 120 like other ids).
const defaultStringLimit = 120

// requireChatAPIKeyForOwner mirrors requireChatApiKeyForOwnerAsync.
func (rt *chatRoutes) requireChatAPIKeyForOwner(ownerID string) (*ChatAPIKeyRecord, error) {
	if rt.deps.ChatKeys == nil {
		return nil, &DomainError{Message: "AI 对话专用 API Key 不存在、已停用或已过期"}
	}
	keyID, err := rt.deps.ChatKeys.EnsureChatAPIKey(ownerID)
	if err != nil {
		return nil, err
	}
	key, err := rt.deps.ChatKeys.FindChatAPIKey(keyID, ownerID)
	if err != nil {
		return nil, err
	}
	if key == nil || key.Secret == "" || key.Status != "active" {
		return nil, &DomainError{Message: "AI 对话专用 API Key 不存在、已停用或已过期"}
	}
	return key, nil
}

// requireOwnedChatModelScope mirrors requireOwnedChatModelAccessAsync with
// the bind-mode generalization: 会话归属 + 绑定对象可用性校验（api_key 模式
// 保持 Key 校验；group/account 校验对象在请求者数据范围内存在且启用）。
func (rt *chatRoutes) requireOwnedChatModelScope(conversationID string, bindScope ChatBindScope) (*Conversation, *chatBindingScope, error) {
	conversation, err := rt.deps.Store.GetConversation(conversationID, bindScope.ViewerID)
	if err != nil {
		return nil, nil, err
	}
	if conversation == nil {
		return nil, nil, &ConversationNotFoundError{}
	}
	scope, err := rt.resolveChatBindingScope(conversation, bindScope)
	if err != nil {
		return nil, nil, err
	}
	return conversation, scope, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// listConversationModels mirrors GET /conversations/{id}/models.
func (rt *chatRoutes) listConversationModels(w http.ResponseWriter, r *http.Request) {
	bindScope, err := rt.requireChatBindScope(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	ownerID := bindScope.ViewerID
	_, scope, err := rt.requireOwnedChatModelScope(r.PathValue("conversationId"), bindScope)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	models, _, err := rt.loadChatModelListsForScope(scope, ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	writeOK(w, models)
}

// getConversationModel mirrors GET /conversations/{id}/models/{modelId}.
func (rt *chatRoutes) getConversationModel(w http.ResponseWriter, r *http.Request) {
	bindScope, err := rt.requireChatBindScope(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	ownerID := bindScope.ViewerID
	modelID := r.PathValue("modelId")
	_, scope, err := rt.requireOwnedChatModelScope(r.PathValue("conversationId"), bindScope)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	accounts, catalog := rt.loadChatModelCatalogForScope(scope, ownerID, modelID)
	catalogItems := []ProviderModelCatalogItem{}
	for _, item := range catalog {
		if item.Model != modelID {
			continue
		}
		if !containsAny(item.SupportedAPIProtocols, []string{"chat_completions", "responses"}) {
			continue
		}
		catalogItems = append(catalogItems, item)
	}
	var option *ChatModelOption
	if len(catalogItems) > 0 {
		options := buildChatModelOptions([]string{modelID}, catalogItems)
		if len(options) > 0 {
			option = constrainChatModelOptionForAccounts(options[0], modelID, accounts, nil)
		}
	}
	if option == nil || len(option.SupportedAPIProtocols) == 0 {
		writeMessageCode(w, http.StatusNotFound, "当前会话没有可用的该模型", "chat_model_not_found")
		return
	}
	writeOK(w, chatModelCapabilitiesPayload(option))
}

func containsAny(values []string, candidates []string) bool {
	for _, value := range values {
		if containsString(candidates, value) {
			return true
		}
	}
	return false
}

// chatModelCapabilitiesPayload mirrors { ...modelOption, name: modelOption.id }.
func chatModelCapabilitiesPayload(option *ChatModelOption) map[string]any {
	payload := map[string]any{
		"id":                        option.ID,
		"name":                      option.ID,
		"supportsPromptCaching":     option.SupportsPromptCaching,
		"supportedReasoningEfforts": nilToEmpty(option.SupportedReasoningEfforts),
		"supportedServiceTiers":     nilToEmpty(option.SupportedServiceTiers),
		"supportedApiProtocols":     nilToEmpty(option.SupportedAPIProtocols),
		"inputModalities":           nilToEmpty(option.InputModalities),
		"outputModalities":          nilToEmpty(option.OutputModalities),
		"supportedTools":            nilToEmpty(option.SupportedTools),
		"generationParameters":      generationParametersPayload(option.GenerationParameters),
	}
	if option.DefaultReasoningEffort != "" {
		payload["defaultReasoningEffort"] = option.DefaultReasoningEffort
	}
	if option.ContextWindowTokens != nil {
		payload["contextWindowTokens"] = *option.ContextWindowTokens
	}
	if option.MaxInputTokens != nil {
		payload["maxInputTokens"] = *option.MaxInputTokens
	}
	if option.MaxOutputTokens != nil {
		payload["maxOutputTokens"] = *option.MaxOutputTokens
	}
	return payload
}

func generationParametersPayload(capabilities []ChatGenerationParameterCapability) []any {
	out := make([]any, 0, len(capabilities))
	for _, capability := range capabilities {
		out = append(out, map[string]any{
			"parameter":    capability.Parameter,
			"min":          capability.Min,
			"max":          capability.Max,
			"defaultValue": capability.DefaultValue,
		})
	}
	return out
}

// compactionTrigger mirrors POST /conversations/{id}/context/compactions.
func (rt *chatRoutes) compactionTrigger(w http.ResponseWriter, r *http.Request) {
	raw, err := readJSONBody(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	body, err := decodeObjectBody(raw)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	var model *string
	for key, value := range body {
		if key != "model" {
			writeChatRouteError(w, &invalidRequestError{Message: "Unrecognized key: \"" + key + "\""})
			return
		}
		text, textErr := boundedTrimmedString(value, 200)
		if textErr != nil {
			if textErr.Error() == "String must contain at least 1 character(s)" {
				writeChatRouteError(w, &invalidRequestError{Message: "请选择模型"})
				return
			}
			writeChatRouteError(w, &invalidRequestError{Message: textErr.Error()})
			return
		}
		model = text
	}
	if model == nil {
		writeChatRouteError(w, &invalidRequestError{Message: "请选择模型"})
		return
	}
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	bindScope, err := rt.requireChatBindScope(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.GetConversation(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		writeChatRouteError(w, &ConversationNotFoundError{})
		return
	}
	if action := rt.getAction(conversation.ID, ownerID); action != nil && action.kind == "compacting" {
		writeOKStatus(w, http.StatusAccepted, map[string]any{"state": "already_running", "serverTime": rt.now()})
		return
	}
	if action := rt.getAction(conversation.ID, ownerID); action != nil && action.kind == "clearing" {
		writeChatRouteError(w, &ConflictError{Code: ConflictConversationClearing})
		return
	}
	if conversation.ActiveTurnID != nil || rt.getPreparationForConversation(conversation.ID, ownerID) != nil {
		writeChatRouteError(w, &ConflictError{Code: ConflictMessageInProgress})
		return
	}
	claim := rt.claimAction(conversation.ID, ownerID, "compacting")
	if claim == nil {
		writeChatRouteError(w, &ConflictError{Code: ConflictMessageInProgress})
		return
	}
	defer rt.deleteActionIfMatches(conversation.ID, claim.token)
	if rt.deps.Compactions == nil {
		writeChatRouteError(w, &DomainError{Message: "上下文压缩启动失败，请稍后重试"})
		return
	}
	input, err := rt.resolveChatCompactionInput(conversation, bindScope, *model)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	result := rt.deps.Compactions.Start(r.Context(), input)
	serverTime := rt.now()
	if result.Status == "accepted" || result.Status == "already_running" {
		writeOKStatus(w, http.StatusAccepted, map[string]any{"state": result.Status, "serverTime": serverTime})
		return
	}
	if result.Status == "skipped" {
		code := "chat_context_compaction_skipped"
		if result.Reason == "no_compactable_turn" {
			code = "no_compactable_turn"
		}
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "当前会话没有可压缩的内容", "code": code, "serverTime": serverTime})
		return
	}
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{"message": "上下文压缩启动失败，请稍后重试", "code": "chat_context_compaction_failed", "serverTime": serverTime})
}

// resolveChatCompactionInput mirrors resolveChatCompactionInput. 鉴权主体恒
// 为会话 api_key_id（三种模式同一语义）；绑定对象按请求者数据范围复核，模型
// 能力与协议按绑定作用域收敛。
func (rt *chatRoutes) resolveChatCompactionInput(conversation *Conversation, bindScope ChatBindScope, model string) (CompactionInput, error) {
	ownerID := bindScope.ViewerID
	input := CompactionInput{ConversationID: conversation.ID, SystemAccountID: ownerID, Model: model}
	apiKey, err := rt.requireOwnedApiKey(derefString(conversation.APIKeyID), ownerID)
	if err != nil {
		return input, err
	}
	input.APIKeySecret = apiKey.Secret
	scope, err := rt.resolveChatBindingScope(conversation, bindScope)
	if err != nil {
		return input, err
	}
	_, catalog := rt.loadChatModelCatalogForScope(scope, ownerID, model)
	options := buildChatModelOptions([]string{model}, catalog)
	var option *ChatModelOption
	if len(options) > 0 {
		option = options[0]
	}
	if option == nil || containsString(option.SupportedAPIProtocols, "images") {
		return input, &ModelCapabilityError{Message: "当前模型不支持上下文压缩，请切换对话模型"}
	}
	supportedProtocols := rt.scopeSupportedProtocols(scope, ownerID, model)
	if len(supportedProtocols) == 0 {
		return input, &ModelCapabilityError{Message: "当前 API Key 没有可用于该模型的对话路由"}
	}
	supportsWebSearch := containsString(option.SupportedTools, "web_search")
	input.Protocol = selectChatTransport(supportedProtocols, supportsWebSearch)
	if option.MaxInputTokens != nil {
		limit := *option.MaxInputTokens
		input.EffectiveContextLimitTokens = &limit
	}
	return input, nil
}
