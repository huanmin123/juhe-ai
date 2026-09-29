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

// --- 会话绑定（AI 问答会话账户唯一绑定）端口与作用域 ---

// ChatBindScope 是会话绑定对象的数据范围，普通用户=自有+被授权的启用对象，
// admin/super_admin=全量号池；由 requireChatBindScope 从登录态解析，沿账户
// 选择/切换校验、账户列表端点与发送/模型作用域复核三条链路统一传递。
type ChatBindScope struct {
	ViewerID string
	IsAdmin  bool
}

// ChatAccountLookup resolves the conversation's bound account object（数据范围
// 内存在且启用 + 名称/provider 事实 + 启用分组绑定). Port satisfied at the
// composition root by the accounts store; nil disables binding validation with
// an explicit error.
type ChatAccountLookup interface {
	FindChatAccount(scope ChatBindScope, accountID string) (*ChatAccountRef, error)
}

// ChatAccountOption 是 GET /my-chat/accounts 的最小投影：用户授权范围内全部
// 可派发账户的 id/名称/供应商/生效状态，供会话绑定与（后续阶段的）工具绑定
// 统一使用；不含归属、授权状态等管理面字段。
type ChatAccountOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderCode string `json:"providerCode"`
	Status       string `json:"status"`
}

// ChatAccountOptionsLookup 列出用户授权范围内全部可派发账户（口径与
// FindChatAccount 一致：数据范围内未删、非授权实例戳行、生效状态 active）。
// ctx 供实现复用带 context 的查询。Port satisfied at the composition root by
// the accounts store; nil 让 /my-chat/accounts 返回显式错误。
type ChatAccountOptionsLookup interface {
	ListChatAccountOptions(ctx context.Context, scope ChatBindScope) ([]ChatAccountOption, error)
}

// ChatAccountRef is the read-only account view the binding relies on.
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

// chatBindingScope is the resolved per-conversation model scope：会话绑定账户
// 的运行时传输视图（单元素；账户不在任何启用分组快照中时为空切片 = 空作用域）。
type chatBindingScope struct {
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

func normalizeProviderToken(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ""
	}
	return normalized
}

// resolveChatBindingScope 校验会话归属下的绑定账户可用性并解析模型作用域
// （仅 account 单一路径）：绑定对象须在请求者数据范围（ChatBindScope）内存
// 在且启用——范围外与不存在同型 (nil, nil)，沿用既有 400 文案。未选账户由
// 调用方预检（发送 400 chat_account_required、模型列表空列表），此处按可恢
// 复输入错误返回同一引导文案兜底。
func (rt *chatRoutes) resolveChatBindingScope(conversation *Conversation, bindScope ChatBindScope) (*chatBindingScope, error) {
	ownerID := bindScope.ViewerID
	accountID := derefString(conversation.BindAccountID)
	if accountID == "" {
		return nil, &invalidRequestError{Message: chatAccountRequiredMessage}
	}
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
	return &chatBindingScope{accounts: rt.convergeChatAccountScope(ref, ownerID)}, nil
}

// chatAccountRequiredMessage 是未选账户会话的发送预检引导文案（400
// chat_account_required）。
const chatAccountRequiredMessage = "请先选择会话绑定的 AI 账户，再发送消息"

// chatConversationArchivedMessage 是归档（存量旧模式）会话的只读提示文案
// （设计 §8）。
const chatConversationArchivedMessage = "该会话绑定方式已升级，请新建会话"

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

// loadChatModelCatalogForScope 取绑定账户作用域的账户快照与目录：provider
// 目录按账户 provider_code 单值取列表，与 account_supported_models 的交集由
// 账户视图的模型/协议过滤自然收窄（空集合表示不限制）。
func (rt *chatRoutes) loadChatModelCatalogForScope(scope *chatBindingScope, systemAccountID, requestedModel string) ([]ChatTransportAccount, []ProviderModelCatalogItem) {
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
// over the binding scope; 排序与 defaultModel=排序第一项 规则不变。
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

// scopeSupportedProtocols 在收敛后的单账户视图上按固定顺序判定双协议。
func (rt *chatRoutes) scopeSupportedProtocols(scope *chatBindingScope, systemAccountID, model string) []ChatTransportProtocol {
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

// ChatModelListOption mirrors ChatModelListOption.
type ChatModelListOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
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

// createConversationHandler mirrors POST /conversations（免弹窗直进，设计 §5.1）：
// 创建即空会话（bind_account_id NULL），任何请求体内容（含历史 bindMode/
// apiKeyId/groupId/accountId 字段）按兼容口径忽略；鉴权主体仍是自动创建/复用
// 的 chat 专用 Key（EnsureChatAPIKey 幂等不变）。账户选定经 PATCH accountId
// 写入，模型默认推举由前端在账户选定后从模型列表取首项。
func (rt *chatRoutes) createConversationHandler(w http.ResponseWriter, r *http.Request) {
	// 兼容忽略历史字段：仅做 JSON 合法性与大小校验（Express json() 同款），
	// 内容不参与创建。
	if _, err := readJSONBody(r); err != nil {
		writeChatRouteError(w, err)
		return
	}
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	apiKey, err := rt.requireChatAPIKeyForOwner(ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.CreateConversation(CreateConversationInput{
		SystemAccountID:         ownerID,
		APIKeyID:                apiKey.ID,
		APIKeyNameSnapshot:      apiKey.Name,
		Now:                     rt.now(),
		MaxConversationsPerUser: rt.deps.maxConversationsPerUser(),
	})
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	writeOKStatus(w, http.StatusCreated, rt.conversationPayload(conversation))
}

// defaultStringLimit mirrors the apiKeyId string bound (Node has no explicit
// max; the route uses 120 like other ids).
const defaultStringLimit = 120

// requireChatAPIKeyForOwner mirrors requireChatApiKeyForOwnerAsync.
// 可靠性批次2（缺陷4）：EnsureChatAPIKey 幂等重建后 Key 仍缺失/停用/过期属于
// 用户可恢复状态（API Key 页面可恢复），与 api_key 模式 requireOwnedApiKey 的
// 停用口径对齐返回 400（invalidRequestError）；ChatKeys 端口未接线与查询失败
// 仍是服务端问题，保持 DomainError（500）。
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
		return nil, &invalidRequestError{Message: "专用对话 Key 已停用或过期，请在 API Key 页面恢复后重试"}
	}
	return key, nil
}

// requireOwnedChatModelScope mirrors requireOwnedChatModelAccessAsync with
// the account-only binding: 会话归属 + 绑定账户可用性校验（请求者数据范围内
// 存在且启用）。未选账户的会话返回 (conversation, nil, nil)，由调用方决定
// 空作用域语义（模型列表空列表 / 单模型 404）。
func (rt *chatRoutes) requireOwnedChatModelScope(conversationID string, bindScope ChatBindScope) (*Conversation, *chatBindingScope, error) {
	conversation, err := rt.deps.Store.GetConversation(conversationID, bindScope.ViewerID)
	if err != nil {
		return nil, nil, err
	}
	if conversation == nil {
		return nil, nil, &ConversationNotFoundError{}
	}
	if conversation.BindAccountID == nil {
		return conversation, nil, nil
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

// listConversationModels mirrors GET /conversations/{id}/models: 候选=会话
// 绑定账户可路由模型；未选账户返回空列表（设计 §5.3）。
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
	if scope == nil {
		writeOK(w, []ChatModelListOption{})
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
// 未选账户的会话无候选模型，一律 404 chat_model_not_found。
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
	if scope == nil {
		writeMessageCode(w, http.StatusNotFound, "当前会话没有可用的该模型", "chat_model_not_found")
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
// 工具能力按「协议 × 工具」矩阵返回（supportedToolsByProtocol）；一维
// supportedTools 已退场（工具体系设计 6.4）。
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
		"supportedToolsByProtocol":  option.SupportedToolsByProtocol,
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
			"step":         capability.Step,
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
	// 发送面预检同款（设计 §8/§5.5）：归档会话只读；未选账户无压缩路由。
	if conversation.Archived {
		writeMessageCode(w, http.StatusForbidden, chatConversationArchivedMessage, "chat_conversation_archived")
		return
	}
	if conversation.BindAccountID == nil {
		writeMessageCode(w, http.StatusBadRequest, chatAccountRequiredMessage, "chat_account_required")
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
	// 可靠性批次2（缺陷2）：claim 成功后 Start 立即返回 202，压缩主体继续在
	// 后台执行；执行 context 与请求 context 脱钩（WithoutCancel 保留取值、
	// 去掉请求生命周期取消），避免 handler 返回后 r.Context() 取消把后台压缩
	// 打断。服务端停止压缩的能力保持在服务层（failClaim 重试水位与停机排空）。
	result := rt.deps.Compactions.Start(context.WithoutCancel(r.Context()), input)
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
// 为会话 api_key_id；绑定对象按请求者数据范围复核，模型能力按绑定作用域收敛。
// 压缩调用恒 chat_completions（工具体系设计 §11.3：上下文压缩的协议偏好
// 随主对话协议偏好一并删除）。
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
	routeAccounts := rt.scopeRouteAccounts(scope, ownerID, model, string(ProtocolChatCompletions))
	chatReachable := false
	for _, account := range routeAccounts {
		if chatTransportAccountSupportsProtocol(account, model, ProtocolChatCompletions) {
			chatReachable = true
			break
		}
	}
	if !chatReachable {
		return input, &ModelCapabilityError{Message: "当前 API Key 没有可用于该模型的对话路由"}
	}
	if option.MaxInputTokens != nil {
		limit := *option.MaxInputTokens
		input.EffectiveContextLimitTokens = &limit
	}
	return input, nil
}
