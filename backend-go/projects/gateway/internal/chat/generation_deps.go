package chat

import (
	"encoding/json"
	"net/http"
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

func (d *Deps) traceID(r *http.Request) string {
	if d.TraceID == nil {
		return strings.TrimSpace(r.Header.Get("x-trace-id"))
	}
	return d.TraceID(r)
}

// maxConversationsPerUser mirrors runtimeConfig.chat.maxConversationsPerUser.
func (d *Deps) maxConversationsPerUser() int {
	if d.MaxConversationsPerUserInt != nil {
		return d.MaxConversationsPerUserInt()
	}
	return 30
}

// requireOwnedApiKey mirrors requireOwnedApiKey.
func (rt *chatRoutes) requireOwnedApiKey(apiKeyID, ownerID string) (*ChatAPIKeyRecord, error) {
	if apiKeyID == "" {
		return nil, &DomainError{Message: "会话绑定的 API Key 已删除"}
	}
	if rt.deps.ChatKeys == nil {
		return nil, &DomainError{Message: "AI 对话专用 API Key 不存在、已停用或已过期"}
	}
	key, err := rt.deps.ChatKeys.FindChatAPIKey(apiKeyID, ownerID)
	if err != nil {
		return nil, err
	}
	if key == nil || key.Secret == "" || key.Status != "active" {
		return nil, &DomainError{Message: "API Key 不存在或不可用"}
	}
	return key, nil
}

// loadChatModelAccess mirrors loadChatModelAccessAsync.
func (rt *chatRoutes) loadChatModelAccess(apiKey *ChatAPIKeyRecord) (*modelAccess, error) {
	if rt.deps.GatewayKeys == nil {
		return nil, &DomainError{Message: "API Key 不存在或不可用"}
	}
	gatewayKey, err := rt.deps.GatewayKeys.ValidateGatewayKey(apiKey.Secret)
	if err != nil {
		return nil, err
	}
	if gatewayKey == nil {
		return nil, &DomainError{Message: "API Key 不存在或不可用"}
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

// constrainChatModelOptionForAccounts mirrors constrainChatModelOptionForAccounts
// (protocol filtering; generation parameter capabilities are validated per-route).
func constrainChatModelOptionForAccounts(option *ChatModelOption, model string, accounts []ChatTransportAccount, requestedProtocols []ChatTransportProtocol) *ChatModelOption {
	protocols := option.SupportedAPIProtocols
	if requestedProtocols != nil {
		protocols = make([]string, 0, len(requestedProtocols))
		for _, protocol := range requestedProtocols {
			protocols = append(protocols, string(protocol))
		}
	}
	supported := []string{}
	for _, protocol := range protocols {
		if protocol != "chat_completions" && protocol != "responses" {
			continue
		}
		for _, account := range accounts {
			if chatTransportAccountSupportsProtocol(account, model, ChatTransportProtocol(protocol)) {
				supported = append(supported, protocol)
				break
			}
		}
	}
	constrained := *option
	constrained.SupportedAPIProtocols = supported
	return &constrained
}

// hasChatImageGenerationRoute mirrors hasChatImageGenerationRoute.
func (rt *chatRoutes) hasChatImageGenerationRoute(groupIDs []string, systemAccountID string) bool {
	accounts := rt.accountsForGroups(groupIDs, systemAccountID, "gpt-image-2", "")
	for _, account := range accounts {
		if account.Type == "api_key" {
			return true
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
func (rt *chatRoutes) loadChatModelListsFromAccountSnapshot(groupIDs []string, systemAccountID string) ([]ChatModelListOption, *ChatModelListOption, error) {
	accounts, catalog := rt.loadChatModelCatalogSnapshot(groupIDs, systemAccountID, "")
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
	var apiKeyID *string
	for key, value := range body {
		if key != "apiKeyId" {
			writeChatRouteError(w, &invalidRequestError{Message: "Unrecognized key: \"" + key + "\""})
			return
		}
		text, textErr := boundedTrimmedString(value, defaultStringLimit)
		if textErr != nil {
			writeChatRouteError(w, &invalidRequestError{Message: textErr.Error()})
			return
		}
		apiKeyID = text
	}
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	var apiKey *ChatAPIKeyRecord
	if apiKeyID != nil {
		apiKey, err = rt.requireOwnedApiKey(*apiKeyID, ownerID)
	} else {
		apiKey, err = rt.requireChatAPIKeyForOwner(ownerID)
	}
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	access, err := rt.loadChatModelAccess(apiKey)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	_, defaultModel, err := rt.loadChatModelListsFromAccountSnapshot(access.GroupIDs, ownerID)
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

// requireOwnedChatModelAccess mirrors requireOwnedChatModelAccessAsync.
func (rt *chatRoutes) requireOwnedChatModelAccess(conversationID, ownerID string) (*Conversation, *modelAccess, error) {
	conversation, err := rt.deps.Store.GetConversation(conversationID, ownerID)
	if err != nil {
		return nil, nil, err
	}
	if conversation == nil {
		return nil, nil, &ConversationNotFoundError{}
	}
	apiKey, err := rt.requireOwnedApiKey(derefString(conversation.APIKeyID), ownerID)
	if err != nil {
		return nil, nil, err
	}
	access, err := rt.loadChatModelAccess(apiKey)
	if err != nil {
		return nil, nil, err
	}
	return conversation, access, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// listConversationModels mirrors GET /conversations/{id}/models.
func (rt *chatRoutes) listConversationModels(w http.ResponseWriter, r *http.Request) {
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	_, access, err := rt.requireOwnedChatModelAccess(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	models, _, err := rt.loadChatModelListsFromAccountSnapshot(access.GroupIDs, ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	writeOK(w, models)
}

// getConversationModel mirrors GET /conversations/{id}/models/{modelId}.
func (rt *chatRoutes) getConversationModel(w http.ResponseWriter, r *http.Request) {
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	modelID := r.PathValue("modelId")
	_, access, err := rt.requireOwnedChatModelAccess(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	accounts, catalog := rt.loadChatModelCatalogSnapshot(access.GroupIDs, ownerID, modelID)
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
	input, err := rt.resolveChatCompactionInput(conversation, ownerID, *model)
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

// resolveChatCompactionInput mirrors resolveChatCompactionInput.
func (rt *chatRoutes) resolveChatCompactionInput(conversation *Conversation, ownerID, model string) (CompactionInput, error) {
	input := CompactionInput{ConversationID: conversation.ID, SystemAccountID: ownerID, Model: model}
	apiKey, err := rt.requireOwnedApiKey(derefString(conversation.APIKeyID), ownerID)
	if err != nil {
		return input, err
	}
	access, err := rt.loadChatModelAccess(apiKey)
	if err != nil {
		return input, err
	}
	input.APIKeySecret = apiKey.Secret
	_, catalog := rt.loadChatModelCatalogSnapshot(access.GroupIDs, ownerID, model)
	options := buildChatModelOptions([]string{model}, catalog)
	var option *ChatModelOption
	if len(options) > 0 {
		option = options[0]
	}
	if option == nil || containsString(option.SupportedAPIProtocols, "images") {
		return input, &ModelCapabilityError{Message: "当前模型不支持上下文压缩，请切换对话模型"}
	}
	supportedProtocols := resolveChatSupportedProtocols(access.GroupIDs, model, func(groupID, requestedModel, endpointFamily string) []ChatTransportAccount {
		return rt.accountsForGroups([]string{groupID}, ownerID, requestedModel, endpointFamily)
	})
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
