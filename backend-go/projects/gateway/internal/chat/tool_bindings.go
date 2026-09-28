package chat

// 会话工具绑定（AI问答工具体系与主子模型设计 §6.3/§7/§8 + AI问答会话账户唯一
// 绑定设计 §7）：模型工具（web_search/generate_image）的会话级「账户+模型」
// 绑定、候选过滤与绑定状态聚合。候选范围 = 用户授权范围内全部可派发账户 ×
// 已实现执行器的能力白名单 × 模型目录能力声明（跨账户合法，不随会话绑定收窄）。

import (
	"context"
	"encoding/json"
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
	ID  string `json:"id"`
	Kind string `json:"kind"`
	// Bound：会话是否已为该工具写入绑定（web_search = 账户+模型两列；generate_
	// image = 账户列 + default_image_model）。
	Bound bool `json:"bound"`
	// Binding 为当前绑定快照（失效绑定的 AccountName 尽力解析，账户已删除时为
	// 空串）；未绑定为 null。
	Binding *ChatToolBindingCandidate `json:"binding"`
	// Valid：绑定仍可派发（账户 active 且模型仍在候选/账户可路由）；失效时
	// InvalidReason 说明原因。
	Valid         bool   `json:"valid"`
	InvalidReason string `json:"invalidReason,omitempty"`
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
	SearchAccountID string
	SearchModelID   string
	SearchExecutor  GenerationExecutor
	SearchCandidates []ChatToolBindingCandidate
	ImageAccountID  string
	ImageExecutor   GenerationExecutor
	ImageCandidates []ChatToolBindingCandidate
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
//（候选白名单：GPT 系供应商（gpt/openai vendor）× gpt-image-2；Grok 系账户 ×
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
//（家族 × api_key 类型）。候选按账户列表顺序（/my-chat/accounts 口径）与
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

// buildChatToolBindingsPayload 聚合会话的工具绑定状态（契约 §8.1）：
// bound/valid 判定按候选列表（绑定 ∈ 候选 = 可派发）；code 工具仅列出。
func (rt *chatRoutes) buildChatToolBindingsPayload(bindScope ChatBindScope, conversation *Conversation) (*ChatToolBindingsPayload, error) {
	candidates, err := rt.resolveChatToolBindingCandidates(bindScope)
	if err != nil {
		return nil, err
	}
	payload := &ChatToolBindingsPayload{Tools: []ChatToolBindingStatus{}}

	// web_search：绑定 = search_account_id + search_model_id 两列（一体写入）。
	searchStatus := ChatToolBindingStatus{
		ID: "web_search", Kind: "model",
		Candidates: candidates.search,
	}
	if conversation.SearchAccountID != nil && conversation.SearchModelID != nil {
		searchStatus.Bound = true
		searchStatus.Binding = &ChatToolBindingCandidate{
			AccountID: *conversation.SearchAccountID,
			ModelID:   *conversation.SearchModelID,
		}
		if name, ok := rt.bindingAccountName(bindScope, *conversation.SearchAccountID); ok {
			searchStatus.Binding.AccountName = name
		}
		searchStatus.Binding.ModelName = searchStatus.Binding.ModelID
		searchStatus.Valid = containsChatToolBindingCandidate(candidates.search, *searchStatus.Binding)
		if !searchStatus.Valid {
			searchStatus.InvalidReason = "搜索绑定已失效（账户停用/删除或模型不再支持），请重新设置"
		}
	}
	payload.Tools = append(payload.Tools, searchStatus)

	// generate_image：绑定 = image_account_id + default_image_model（模型列
	// 语义升级，列名不变）。
	imageStatus := ChatToolBindingStatus{
		ID: "generate_image", Kind: "model",
		Candidates: candidates.image,
	}
	if conversation.ImageAccountID != nil {
		imageStatus.Bound = true
		imageStatus.Binding = &ChatToolBindingCandidate{
			AccountID: *conversation.ImageAccountID,
			ModelID:   string(conversation.DefaultImageModel),
			ModelName: string(conversation.DefaultImageModel),
		}
		if name, ok := rt.bindingAccountName(bindScope, *conversation.ImageAccountID); ok {
			imageStatus.Binding.AccountName = name
		}
		imageStatus.Valid = containsChatToolBindingCandidate(candidates.image, *imageStatus.Binding)
		if !imageStatus.Valid {
			imageStatus.InvalidReason = "生图绑定已失效（账户停用/删除或图像模型不可路由），请重新设置"
		}
	}
	payload.Tools = append(payload.Tools, imageStatus)

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
		payload.Tools = append(payload.Tools, ChatToolBindingStatus{ID: definition.ModelName, Kind: "code"})
	}
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
