package playground

// 演练场账户与模型目录分类（设计 §3.2）：chat=协议含 chat_completions|
// messages|generate_content，functionCalling=任一协议声明 function_calling；
// image/video/audio 按既有 chat 面白名单口径（复制自 internal/chat/
// tool_bindings.go 的 chatSearchImageModelFamily / chatMediaProviderAllowed /
// accountSupportsImageModel / accountSupportsMediaModel 与
// generation_deps.go 的 normalizeProviderToken——internal/chat 有并行未提交
// 改动且将随设计 §8 整体退役，playground 不得 import，故按任务约束把这几段
// 纯判定函数复制进本包，口径保持一致）。

import (
	"context"
	"net/http"
	"strings"
)

// chat 协议词表（设计 §3.2：三协议之一即 chat 行）。
var chatKindProtocols = []string{"chat_completions", "messages", "generate_content"}

// functionCallingToolKey 是目录矩阵「协议 × 工具」里 function_calling 的键
// （chat transport.go supportsTool 的判定键，同一词表）。
const functionCallingToolKey = "function_calling"

// normalizeProviderToken 归一供应商码（来源：internal/chat/generation_deps.go
// normalizeProviderToken，退役复制；小写 + 去空白）。
func normalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// playgroundSearchImageModelFamily 返回供应商家族可路由的注册图像模型枚举
// （来源：internal/chat/tool_bindings.go chatSearchImageModelFamily，退役复
// 制；契约 §3.2 image 口径：gpt 系 × gpt-image-2、xai × grok-imagine 系，
// 目录声明 image_generation 的其他模型不进候选）。
func playgroundSearchImageModelFamily(providerCode string) []string {
	switch normalizeProviderToken(providerCode) {
	case "openai", "gpt":
		return []string{"gpt-image-2"}
	case "xai":
		return []string{"grok-imagine-image", "grok-imagine-quality"}
	default:
		return nil
	}
}

// playgroundMediaProviderAllowed 是视频/TTS 目录行的供应商家族白名单（来源：
// internal/chat/tool_bindings.go chatMediaProviderAllowed，退役复制；问答音
// 视频工具设计 §3：openai（含 gpt 子供应商）/gemini/minimax/volcengine/qwen）。
func playgroundMediaProviderAllowed(providerCode string) bool {
	switch normalizeProviderToken(providerCode) {
	case "openai", "gpt", "gemini", "minimax", "volcengine", "qwen":
		return true
	}
	return false
}

// accountSupportsImageModel 判定账户视图可路由注册图像模型（来源：
// internal/chat/tool_bindings.go accountSupportsImageModel，退役复制；图像
// 路由口径：api_key 类型账户 + 账户模型清单（非空时）含该模型或其映射上游
// 模型）。
func accountSupportsImageModel(account TransportAccount, model string) bool {
	if account.Type != "api_key" {
		return false
	}
	return transportAccountSupportsModel(account, model)
}

// accountSupportsMediaModel 判定账户视图可路由目录媒体模型（来源：
// internal/chat/tool_bindings.go accountSupportsMediaModel，退役复制；差异
// 口径：媒体路由判定按账户启用的端点模式——视频要求 video_create、语音要求
// audio_speech，与 /v1 媒体链的候选能力门口径一致，不限 api_key 账户类型）。
func accountSupportsMediaModel(account TransportAccount, model, requiredEndpointMode string) bool {
	if !containsString(account.SupportedEndpointModes, requiredEndpointMode) {
		return false
	}
	return transportAccountSupportsModel(account, model)
}

// accountSupportsChatModel 判定账户可派发 chat 模型：连接面含 chat lane 端点
// 模式之一（chat_json/chat_sse，/v1/chat/completions 派发的路由依据）× 账户
// 模型清单（含映射）可路由（与 accountSupportsMediaModel 同构，模式维度为
// 「任一 chat lane 模式」）。
func accountSupportsChatModel(account TransportAccount, model string) bool {
	if !containsString(account.SupportedEndpointModes, "chat_json") && !containsString(account.SupportedEndpointModes, "chat_sse") {
		return false
	}
	return transportAccountSupportsModel(account, model)
}

// transportAccountSupportsModel 是模型清单/映射路由的共用判定（来源：
// internal/chat/tool_bindings.go 两个 accountSupports*Model 的共用后半段，
// 退役复制；清单空 = 不限制；映射启用且源模型命中时按上游目标模型对清单）。
func transportAccountSupportsModel(account TransportAccount, model string) bool {
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

// containsString 是顺序包含判定（来源：chat 面同名词表辅助，退役复制）。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// ModelKind 是目录条目的演练场分类（设计 §3.2）。
type ModelKind string

const (
	KindChat  ModelKind = "chat"
	KindImage ModelKind = "image"
	KindVideo ModelKind = "video"
	KindAudio ModelKind = "audio"
)

// ModelOption 是 GET /accounts 响应里的模型条目（设计 §3.2 响应形状）。
type ModelOption struct {
	ModelID         string    `json:"modelId"`
	Kind            ModelKind `json:"kind"`
	FunctionCalling bool      `json:"functionCalling,omitempty"`
}

// PlaygroundModelOption 已并入 ModelOption（functionCalling 直接挂在 chat 行
// 上）；媒体三类行不带该字段（契约 §3.2 示例形状）。

// classifyCatalogModels 按设计 §3.2 对目录行分类，四类统一「目录声明 × 账户
// 实际可派发」复核（跑不通的组合不进候选，2026-10-31 E2E 裁决：纯目录 chat
// 分类会让视频账户列出不可能跑通的 chat 模型）：
//   - chat：目录协议含 chat_completions/messages/generate_content 之一 × 账户
//     连接面含 chat lane 端点模式（chat_json/chat_sse，/v1/chat/completions
//     派发的路由依据）× 账户模型清单（含映射）可路由；functionCalling = 任一
//     协议行声明 function_calling（chat supportsTool 同口径）。
//   - image：家族白名单（gpt-image-2 / grok-imagine 系）× api_key 账户可路由。
//   - video：供应商白名单 × 目录声明 video 协议 × 端点模式 video_create 可路由。
//   - audio：目录声明 audio_speech 协议 × 端点模式 audio_speech 可路由。
//
// transportView 为空（账户不在任何启用分组快照中）时无路由依据，四类均不产出。
func classifyCatalogModels(providerCode string, catalog []CatalogItem, transportView *TransportAccount) []ModelOption {
	options := []ModelOption{}
	// 去重键为 (modelId, kind)：同一模型可兼具多类能力（如 veo-3 同时声明
	// generate_content 与 video 协议），四页下拉按 kind 各取所需，双行合法。
	seen := map[string]bool{}
	add := func(option ModelOption) {
		key := option.ModelID + "@" + string(option.Kind)
		if seen[key] {
			return
		}
		seen[key] = true
		options = append(options, option)
	}
	for _, item := range catalog {
		model := strings.TrimSpace(item.Model)
		if model == "" {
			continue
		}
		if transportView == nil {
			continue
		}
		// chat：三协议之一 × 账户 chat lane 可路由（跨协议桥承载
		// messages/generate_content 行，派发入口仍为 /v1/chat/completions）。
		if hasAnyProtocol(item.SupportedAPIProtocols, chatKindProtocols) && accountSupportsChatModel(*transportView, model) {
			add(ModelOption{
				ModelID:         model,
				Kind:            KindChat,
				FunctionCalling: supportsFunctionCalling(item),
			})
		}
		// image：家族白名单内的模型才可能入列（目录行仅用于确认路由，白名单
		// 是候选面，与旧 chat 生图候选同口径）× 连接面 images_json 端点模式
		// （/v1/images/* 派发路由依据，2026-10-31 E2E 收口：chat 域 grok 账户
		// 无该模式时生图派发必 503，不进候选）。
		for _, imageModel := range playgroundSearchImageModelFamily(providerCode) {
			if imageModel != model {
				continue
			}
			if containsString(transportView.SupportedEndpointModes, "images_json") && accountSupportsImageModel(*transportView, model) {
				add(ModelOption{ModelID: model, Kind: KindImage})
			}
		}
		// video/audio：供应商白名单内 × 目录协议声明 × 端点模式可路由。
		if !playgroundMediaProviderAllowed(providerCode) {
			continue
		}
		if containsString(item.SupportedAPIProtocols, "video") && accountSupportsMediaModel(*transportView, model, "video_create") {
			add(ModelOption{ModelID: model, Kind: KindVideo})
		}
		if containsString(item.SupportedAPIProtocols, "audio_speech") && accountSupportsMediaModel(*transportView, model, "audio_speech") {
			add(ModelOption{ModelID: model, Kind: KindAudio})
		}
	}
	return options
}

// hasAnyProtocol 报告目录行协议列表是否含给定词表任一值。
func hasAnyProtocol(protocols []string, wanted []string) bool {
	for _, protocol := range protocols {
		if containsString(wanted, protocol) {
			return true
		}
	}
	return false
}

// supportsFunctionCalling 是 functionCalling 判定（chat transport.go
// supportsTool 口径：矩阵任一协议下声明 function_calling，避免误杀仅声明在
// 单一协议下的模型）。
func supportsFunctionCalling(item CatalogItem) bool {
	for _, tools := range item.SupportedToolsByProtocol {
		if containsString(tools, functionCallingToolKey) {
			return true
		}
	}
	return false
}

// resolveAccountPlaygroundView 解析单账户的演练场视图（GET /accounts 与各端
// 点的候选校验共用）：账户事实 → chat-pinned 运行时视图 → 目录（vendor 为空
// 的 generic_http 账户按账户显式绑定模型补齐，禁止按空 provider 查目录——
// chat loadChatModelCatalogForScope 同口径）。返回 (ref, 目录, 传输视图)；
// 账户不在数据范围时 ref 为 nil。
func (rt *playgroundRoutes) resolveAccountPlaygroundView(ctx context.Context, scope Scope, accountID string) (*AccountRef, []CatalogItem, *TransportAccount, *HTTPError) {
	if rt.deps.AccountLookup == nil {
		return nil, nil, nil, &HTTPError{Status: http.StatusServiceUnavailable, Code: codeEndpointDegraded, Message: "账户查询暂不可用，请稍后重试"}
	}
	ref, err := rt.deps.AccountLookup.FindAccount(ctx, scope, accountID)
	if err != nil {
		return nil, nil, nil, &HTTPError{Status: http.StatusInternalServerError, Code: codeEndpointDegraded, Message: "账户查询失败"}
	}
	if ref == nil {
		return nil, nil, nil, nil
	}
	var transportView *TransportAccount
	if rt.deps.TransportAccounts != nil {
		for _, groupID := range uniqueStrings(ref.EnabledGroupIDs) {
			if accounts := rt.deps.TransportAccounts.ListPinnedAccountsForGroup(groupID, scope.ViewerID, ref.ID); len(accounts) > 0 {
				view := accounts[0]
				transportView = &view
				break
			}
		}
	}
	catalog := rt.catalogForAccount(ref, scope.ViewerID)
	return ref, catalog, transportView, nil
}

// catalogForAccount 取账户目录（chat loadChatModelCatalogForScope 的单账户
// 变体：真实 vendor 按 provider_code 查供应商目录；generic_http（vendor 空）
// 按 account_supported_models 取真实 models 行）。
func (rt *playgroundRoutes) catalogForAccount(ref *AccountRef, systemAccountID string) []CatalogItem {
	code := normalizeProviderToken(ref.ProviderCode)
	if code == "" {
		if rt.deps.AccountCatalog == nil {
			return []CatalogItem{}
		}
		return rt.deps.AccountCatalog.ListAccountSupportedModels(ref.ID, systemAccountID)
	}
	if rt.deps.ProviderCatalog == nil {
		return []CatalogItem{}
	}
	return rt.deps.ProviderCatalog.ListProviderCatalog(code, systemAccountID)
}

// uniqueStrings 去重保序（chat convergeChatAccountScope 同款前置）。
func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
