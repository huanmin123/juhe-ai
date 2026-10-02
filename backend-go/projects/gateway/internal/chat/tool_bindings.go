package chat

// 会话工具绑定（AI问答工具体系与主子模型设计 §6.3/§7/§8 + AI问答会话账户唯一
// 绑定设计 §7）：模型工具（web_search/generate_image）的会话级「账户+模型」
// 绑定、候选过滤与绑定状态聚合。候选范围 = 用户授权范围内全部可派发账户 ×
// 已实现执行器的能力白名单 × 模型目录能力声明（跨账户合法，不随会话绑定收窄）。

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
)

// ChatToolBindingCandidate 是候选/绑定条目（契约 §6.3：绑定粒度为「账号+模型」
// 二元组，呈现为「账号名 · 模型名」）。
type ChatToolBindingCandidate struct {
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName"`
	ModelID     string `json:"modelId"`
	ModelName   string `json:"modelName"`
}

// ChatToolBindingStatus 是单个工具的绑定状态（GET tool-bindings / 会话详情
// toolCapabilities 的条目形状，契约 §8.1/§8.3）。
type ChatToolBindingStatus struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Bound：会话是否已为该工具写入绑定（web_search = 账户+模型两列；generate_
	// image = 账户列 + default_image_model）。
	Bound bool `json:"bound"`
	// Binding 为当前绑定快照（失效绑定的 AccountName 尽力解析，账户已删除时为
	// 空串）；未绑定为 null。
	Binding *ChatToolBindingCandidate `json:"binding"`
	// Valid：绑定仍可派发（账户 active 且模型仍在候选/账户可路由）；失效时
	// InvalidReason 说明原因。
	Valid         bool                       `json:"valid"`
	InvalidReason string                     `json:"invalidReason,omitempty"`
	Candidates    []ChatToolBindingCandidate `json:"candidates"`
}

// ChatToolBindingsPayload 是 GET /my-chat/conversations/{id}/tool-bindings 与
// 会话详情 toolCapabilities 的响应形状（契约 §8.1/§8.3）。
type ChatToolBindingsPayload struct {
	Tools []ChatToolBindingStatus `json:"tools"`
}

// chatToolBindingCandidates 是一次候选解析的产物：两类模型工具的候选列表与
// 候选账户视图（valid 判定复用）。
type chatToolBindingCandidates struct {
	search []ChatToolBindingCandidate
	image  []ChatToolBindingCandidate
}

// chatToolBindingRuntime 是发送链路的绑定运行时视图（stream_route 解析、
// generationExecuteInput 携带）：绑定目标 + 候选摘要 + 固定派发执行器。
type chatToolBindingRuntime struct {
	SearchAccountID  string
	SearchModelID    string
	SearchExecutor   GenerationExecutor
	SearchCandidates []ChatToolBindingCandidate
	ImageAccountID   string
	ImageExecutor    GenerationExecutor
	ImageCandidates  []ChatToolBindingCandidate
}

// chatToolBindingCandidatesOf 取指定工具的候选摘要（nil 视图返回 nil）。
func chatToolBindingCandidatesOf(runtime *chatToolBindingRuntime, toolID string) []ChatToolBindingCandidate {
	if runtime == nil {
		return nil
	}
	switch toolID {
	case "web_search":
		return runtime.SearchCandidates
	case "generate_image":
		return runtime.ImageCandidates
	}
	return nil
}

// chatSearchImageModelFamily 返回 provider 家族可路由的注册图像模型枚举
// （候选白名单：GPT 系供应商（gpt/openai vendor）× gpt-image-2；Grok 系账户 ×
// grok-imagine 系，契约 §6.3——目录声明 image_generation 的其他模型不进候选）。
func chatSearchImageModelFamily(providerCode string) []string {
	switch normalizeProviderToken(providerCode) {
	case "openai", "gpt":
		return []string{string(ImageModelGPTImage2)}
	case "xai":
		return []string{string(ImageModelGrokImagineImage), string(ImageModelGrokImagineQuality)}
	default:
		return nil
	}
}

// constrainChatImageModel 把主模型自选的生图模型收敛到绑定账户实际可路由的
// 集合内（BUG-0230）：model 已在集合内原样返回；不在时优先会话默认生图模型
// （其亦需在集合内），否则取该账户候选首项（候选解析顺序确定性）。候选为空
// 或绑定账户无候选时不约束（未绑定/解析失败由发送预检与绑定校验承接）。
func constrainChatImageModel(model, accountID, defaultModel string, candidates []ChatToolBindingCandidate) string {
	if model == "" || accountID == "" {
		return model
	}
	supported := map[string]bool{}
	first := ""
	for _, candidate := range candidates {
		if candidate.AccountID != accountID {
			continue
		}
		if !supported[candidate.ModelID] && first == "" {
			first = candidate.ModelID
		}
		supported[candidate.ModelID] = true
	}
	if len(supported) == 0 || supported[model] {
		return model
	}
	if supported[defaultModel] {
		return defaultModel
	}
	if first != "" {
		return first
	}
	return model
}

// accountSupportsImageModel 判定账户视图可路由注册图像模型：图像路由口径与
// scopeHasImageGenerationRoute 一致（api_key 类型账户）+ 账户模型清单（非空时）
// 含该模型或其映射上游模型。
func accountSupportsImageModel(account ChatTransportAccount, model string) bool {
	if account.Type != "api_key" {
		return false
	}
	if len(account.SupportedModels) == 0 {
		return true
	}
	for _, mapping := range account.ModelMappings {
		if mapping.Enabled != nil && !*mapping.Enabled {
			continue
		}
		if mapping.SourceModel != model {
			continue
		}
		target := mapping.UpstreamModel
		if target == "" {
			target = model
		}
		for _, candidate := range account.SupportedModels {
			if candidate == target {
				return true
			}
		}
	}
	for _, candidate := range account.SupportedModels {
		if candidate == model {
			return true
		}
	}
	return false
}

// resolveChatToolBindingCandidates 计算两类模型工具的绑定候选（契约 §6.3）：
// 搜索候选 = 账户可派发 responses 协议 × 目录矩阵 supportedToolsByProtocol
// ["responses"] 含 web_search 的模型；生图候选 = 注册图像枚举中账户可路由的
// （家族 × api_key 类型）。候选按账户列表顺序（/my-chat/accounts 口径）与
// 模型枚举/目录顺序确定性输出。
func (rt *chatRoutes) resolveChatToolBindingCandidates(bindScope ChatBindScope) (*chatToolBindingCandidates, error) {
	if rt.deps.AccountOptionsLookup == nil {
		return nil, &DomainError{Message: "工具绑定候选暂不可用，请稍后重试"}
	}
	if rt.deps.AccountLookup == nil {
		return nil, &DomainError{Message: "工具绑定候选暂不可用，请稍后重试"}
	}
	accounts, err := rt.deps.AccountOptionsLookup.ListChatAccountOptions(context.Background(), bindScope)
	if err != nil {
		return nil, err
	}
	out := &chatToolBindingCandidates{search: []ChatToolBindingCandidate{}, image: []ChatToolBindingCandidate{}}
	seenSearch := map[string]bool{}
	seenImage := map[string]bool{}
	for _, account := range accounts {
		if account.Status != "active" {
			continue
		}
		ref, err := rt.deps.AccountLookup.FindChatAccount(bindScope, account.ID)
		if err != nil || ref == nil || !ref.Enabled {
			continue
		}
		view := rt.convergeChatAccountScope(ref, bindScope.ViewerID)
		if len(view) == 0 {
			continue
		}
		accountView := view[0]
		// 搜索候选：目录矩阵（经 chat 面读取链，custom 继承已生效）中
		// responses 协议声明 web_search 的模型 × 账户可派发该协议。
		catalog := rt.providerCatalogForAccount(accountView, bindScope.ViewerID)
		for _, item := range catalog {
			if !containsString(item.SupportedToolsByProtocol[string(ProtocolResponses)], HostedToolSearchKey) {
				continue
			}
			if !chatTransportAccountSupportsProtocol(accountView, item.Model, ProtocolResponses) {
				continue
			}
			key := account.ID + "@" + item.Model
			if seenSearch[key] {
				continue
			}
			seenSearch[key] = true
			out.search = append(out.search, ChatToolBindingCandidate{
				AccountID: account.ID, AccountName: account.Name,
				ModelID: item.Model, ModelName: item.Model,
			})
		}
		// 生图候选：注册图像枚举（家族白名单）中账户可路由的条目。
		for _, model := range chatSearchImageModelFamily(accountView.ProviderCode) {
			if !accountSupportsImageModel(accountView, model) {
				continue
			}
			key := account.ID + "@" + model
			if seenImage[key] {
				continue
			}
			seenImage[key] = true
			out.image = append(out.image, ChatToolBindingCandidate{
				AccountID: account.ID, AccountName: account.Name,
				ModelID: model, ModelName: model,
			})
		}
	}
	return out, nil
}

// HostedToolSearchKey 是搜索候选过滤读取的目录矩阵工具键（Responses hosted
// web_search——当前唯一已实现的搜索执行方式，契约 §6.3）。
const HostedToolSearchKey = "web_search"

// providerCatalogForAccount 取账户视图的 provider 目录（loadChatModelCatalogForScope
// 的单账户变体）。
func (rt *chatRoutes) providerCatalogForAccount(account ChatTransportAccount, systemAccountID string) []ProviderModelCatalogItem {
	catalog := []ProviderModelCatalogItem{}
	if rt.deps.ModelCatalog == nil {
		return catalog
	}
	code := normalizeProviderToken(account.ProviderCode)
	if code == "" {
		return catalog
	}
	return rt.deps.ModelCatalog.ListProviderCatalog(code, systemAccountID)
}

// chatToolBindingSource 是绑定状态聚合的绑定来源（工具体系设计 §8.1/§8.5）：
// 会话版取会话三列 + default_image_model；用户偏好版取偏好行四列（生图生效
// 模型为空串时由调用方先落 gpt-image-2 兜底）。空串 = 未绑定。
type chatToolBindingSource struct {
	SearchAccountID     string
	SearchModelID       string
	ImageAccountID      string
	EffectiveImageModel string
}

// buildChatToolBindingStatuses 按候选列表聚合两类模型工具的绑定状态（bound/
// valid 判定：绑定 ∈ 候选 = 可派发）+ code 工具仅列出。会话版与用户偏好版
// （GET/PATCH /my-chat/tool-preferences）共用。
func (rt *chatRoutes) buildChatToolBindingStatuses(bindScope ChatBindScope, source chatToolBindingSource, candidates *chatToolBindingCandidates) []ChatToolBindingStatus {
	tools := []ChatToolBindingStatus{}

	// web_search：绑定 = search_account_id + search_model_id 两列（一体写入）。
	searchStatus := ChatToolBindingStatus{
		ID: "web_search", Kind: "model",
		Candidates: candidates.search,
	}
	if source.SearchAccountID != "" && source.SearchModelID != "" {
		searchStatus.Bound = true
		searchStatus.Binding = &ChatToolBindingCandidate{
			AccountID: source.SearchAccountID,
			ModelID:   source.SearchModelID,
		}
		if name, ok := rt.bindingAccountName(bindScope, source.SearchAccountID); ok {
			searchStatus.Binding.AccountName = name
		}
		searchStatus.Binding.ModelName = searchStatus.Binding.ModelID
		searchStatus.Valid = containsChatToolBindingCandidate(candidates.search, *searchStatus.Binding)
		if !searchStatus.Valid {
			searchStatus.InvalidReason = "搜索绑定已失效（账户停用/删除或模型不再支持），请重新设置"
		}
	}
	tools = append(tools, searchStatus)

	// generate_image：绑定 = image_account_id + default_image_model（模型列
	// 语义升级，列名不变）。
	imageStatus := ChatToolBindingStatus{
		ID: "generate_image", Kind: "model",
		Candidates: candidates.image,
	}
	if source.ImageAccountID != "" {
		imageStatus.Bound = true
		imageStatus.Binding = &ChatToolBindingCandidate{
			AccountID: source.ImageAccountID,
			ModelID:   source.EffectiveImageModel,
			ModelName: source.EffectiveImageModel,
		}
		if name, ok := rt.bindingAccountName(bindScope, source.ImageAccountID); ok {
			imageStatus.Binding.AccountName = name
		}
		imageStatus.Valid = containsChatToolBindingCandidate(candidates.image, *imageStatus.Binding)
		if !imageStatus.Valid {
			imageStatus.InvalidReason = "生图绑定已失效（账户停用/删除或图像模型不可路由），请重新设置"
		}
	}
	tools = append(tools, imageStatus)

	// code 工具仅列出（无绑定概念）：按注册器当前环境的实际注册情况。
	environment := rt.deps.ToolEnvironment
	if environment == "" {
		environment = "development"
	}
	registry := newChatInternalToolRegistry(environment, rt.deps.DiagnosticToolEnabled, true)
	for _, definition := range registry.resolveTools(true) {
		if definition.Kind != "code" {
			continue
		}
		tools = append(tools, ChatToolBindingStatus{ID: definition.ModelName, Kind: "code"})
	}
	return tools
}

// buildChatToolBindingsPayload 聚合会话的工具绑定状态（契约 §8.1）：
// bound/valid 判定按候选列表（绑定 ∈ 候选 = 可派发）；code 工具仅列出。
func (rt *chatRoutes) buildChatToolBindingsPayload(bindScope ChatBindScope, conversation *Conversation) (*ChatToolBindingsPayload, error) {
	candidates, err := rt.resolveChatToolBindingCandidates(bindScope)
	if err != nil {
		return nil, err
	}
	payload := &ChatToolBindingsPayload{Tools: rt.buildChatToolBindingStatuses(bindScope, chatToolBindingSource{
		SearchAccountID:     derefString(conversation.SearchAccountID),
		SearchModelID:       derefString(conversation.SearchModelID),
		ImageAccountID:      derefString(conversation.ImageAccountID),
		EffectiveImageModel: string(conversation.DefaultImageModel),
	}, candidates)}
	return payload, nil
}

// bindingAccountName 尽力解析绑定账户名称（候选/账户查询；账户已删除时 ok=false）。
func (rt *chatRoutes) bindingAccountName(bindScope ChatBindScope, accountID string) (string, bool) {
	if rt.deps.AccountLookup == nil {
		return "", false
	}
	ref, err := rt.deps.AccountLookup.FindChatAccount(bindScope, accountID)
	if err != nil || ref == nil {
		return "", false
	}
	return ref.Name, true
}

func containsChatToolBindingCandidate(candidates []ChatToolBindingCandidate, candidate ChatToolBindingCandidate) bool {
	for _, item := range candidates {
		if item.AccountID == candidate.AccountID && item.ModelID == candidate.ModelID {
			return true
		}
	}
	return false
}

// toolBindingsHandler mirrors GET /my-chat/conversations/{id}/tool-bindings.
// 归档（存量旧模式）会话只读可见绑定状态（候选照常返回，供参考）。
func (rt *chatRoutes) toolBindingsHandler(w http.ResponseWriter, r *http.Request) {
	bindScope, err := rt.requireChatBindScope(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.GetConversation(r.PathValue("conversationId"), bindScope.ViewerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		writeMessageCode(w, http.StatusNotFound, "会话不存在", "chat_conversation_not_found")
		return
	}
	payload, err := rt.buildChatToolBindingsPayload(bindScope, conversation)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	setNoStoreHeaders(w)
	writeOK(w, payload)
}

// chatToolBindingInvalidError 是 PATCH 绑定键候选校验失败（400 + 候选返回，
// 契约 §8.2）。
type chatToolBindingInvalidError struct {
	Message    string
	ToolID     string
	Candidates []ChatToolBindingCandidate
}

func (e *chatToolBindingInvalidError) Error() string { return e.Message }

// writeChatToolBindingInvalid 渲染 400 + 候选负载。
func writeChatToolBindingInvalid(w http.ResponseWriter, err *chatToolBindingInvalidError) {
	candidates := err.Candidates
	if candidates == nil {
		candidates = []ChatToolBindingCandidate{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message":    err.Message,
		"code":       "chat_tool_binding_invalid",
		"toolId":     err.ToolID,
		"candidates": candidates,
	})
}

// resolveChatToolBindingRuntime 解析发送链路的绑定运行时视图（stream_route）：
// 绑定目标来自会话三列；候选摘要供 binding_required 引导事件；执行器按绑定
// 账户解析固定派发视图（与绑定会话同款调度覆盖端口）。端口缺失（测试未接线）
// 时降级为无候选视图——绑定列照常生效，候选摘要为空。
func (rt *chatRoutes) resolveChatToolBindingRuntime(bindScope ChatBindScope, conversation *Conversation) *chatToolBindingRuntime {
	runtime := &chatToolBindingRuntime{}
	if conversation.SearchAccountID != nil && conversation.SearchModelID != nil {
		runtime.SearchAccountID = *conversation.SearchAccountID
		runtime.SearchModelID = *conversation.SearchModelID
	}
	if conversation.ImageAccountID != nil {
		runtime.ImageAccountID = *conversation.ImageAccountID
	}
	if candidates, err := rt.resolveChatToolBindingCandidates(bindScope); err == nil {
		runtime.SearchCandidates = candidates.search
		runtime.ImageCandidates = candidates.image
	}
	runtime.SearchExecutor = rt.toolDispatchExecutor(runtime.SearchAccountID)
	runtime.ImageExecutor = rt.toolDispatchExecutor(runtime.ImageAccountID)
	return runtime
}

// toolDispatchExecutor 解析工具子代理的固定派发执行器视图：绑定账户非空且
// 执行器实现调度覆盖端口时返回目标视图，否则原样返回。
func (rt *chatRoutes) toolDispatchExecutor(accountID string) GenerationExecutor {
	if accountID == "" || rt.deps.Executor == nil {
		return rt.deps.Executor
	}
	aware, ok := rt.deps.Executor.(chatDispatchTargetAware)
	if !ok {
		return rt.deps.Executor
	}
	return aware.WithChatDispatchAccount(accountID)
}

// chatToolPreferencesDefaultImageModelFallback 是偏好行 default_image_model 为
// 空时的生效模型兜底（与会话创建默认一致，契约 §8.5/§8.6）。
const chatToolPreferencesDefaultImageModelFallback = string(ImageModelGPTImage2)

// buildUserToolPreferencesPayload 聚合用户级默认绑定的状态负载（契约 §8.5，
// 2026-10-02）：与 tool-bindings 同形状（bound/binding/valid/invalidReason/
// candidates + code 工具仅列出）；binding 来自偏好行，生图生效模型取偏好行
// default_image_model（空则 gpt-image-2）；候选解析与 tool-bindings 同源
// （resolveChatToolBindingCandidates，跨账户合法口径不变）。偏好行不存在时
// 两类模型工具均 bound:false。
func (rt *chatRoutes) buildUserToolPreferencesPayload(bindScope ChatBindScope, pref *UserToolPreferences) (*ChatToolBindingsPayload, error) {
	candidates, err := rt.resolveChatToolBindingCandidates(bindScope)
	if err != nil {
		return nil, err
	}
	source := chatToolBindingSource{
		EffectiveImageModel: chatToolPreferencesDefaultImageModelFallback,
	}
	if pref != nil {
		source.SearchAccountID = pref.SearchAccountID
		source.SearchModelID = pref.SearchModelID
		source.ImageAccountID = pref.ImageAccountID
		if pref.DefaultImageModel != "" {
			source.EffectiveImageModel = pref.DefaultImageModel
		}
	}
	return &ChatToolBindingsPayload{Tools: rt.buildChatToolBindingStatuses(bindScope, source, candidates)}, nil
}

// writeBackUserToolPreferences 把会话绑定变更回写为用户级全局默认（契约
// §2.11/§10.6，best-effort）：只动请求键对应列——searchBinding 键动搜索两列
// （解绑置空），imageBinding 键动 image_account_id + 本请求生效后的
// default_image_model（含联动与同请求 defaultImageModel），不覆盖另一工具的
// 既有默认；读偏好失败按无偏好行继续合并。回写失败不阻断会话 PATCH 的成功
// 响应，仅记日志（chat 包无日志端口，用标准库 log 落 stderr，见交付说明）。
func (rt *chatRoutes) writeBackUserToolPreferences(ownerID string, fields updateConversationFields, effectiveImageModel ChatImageModel) {
	pref, err := rt.deps.Store.GetUserToolPreferences(ownerID)
	if err != nil {
		log.Printf("chat: 读取用户工具偏好失败（回写按无偏好行继续）owner=%s: %v", ownerID, err)
	}
	merged := UserToolPreferences{}
	if pref != nil {
		merged = *pref
	}
	merged.SystemAccountID = ownerID
	if fields.searchBinding != nil {
		if fields.searchBinding.unbound {
			merged.SearchAccountID = ""
			merged.SearchModelID = ""
		} else {
			merged.SearchAccountID = fields.searchBinding.accountID
			merged.SearchModelID = fields.searchBinding.modelID
		}
	}
	if fields.imageBinding != nil {
		if fields.imageBinding.unbound {
			merged.ImageAccountID = ""
		} else {
			merged.ImageAccountID = fields.imageBinding.accountID
		}
		merged.DefaultImageModel = string(effectiveImageModel)
	}
	if err := rt.deps.Store.UpsertUserToolPreferences(merged); err != nil {
		log.Printf("chat: 用户工具偏好回写失败 owner=%s: %v", ownerID, err)
	}
}
