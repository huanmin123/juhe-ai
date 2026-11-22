package playground

// 演练场 Key 模型候选分类（docs/functions/演练场设计.md §3.2）：所选 API Key
// 的分组模型并集经目录行逐模型分类为六类 kind，再经提供账户能力域复核：
//   - chat：目录协议含 chat_completions|messages|generate_content 之一；
//   - image：家族白名单（gpt 系 × gpt-image-2、xai × grok-imagine 系；目录
//     声明 image_generation 的其他模型不进候选）；
//   - video：供应商白名单 × 目录声明 video 协议；
//   - audio：供应商白名单 × 目录声明 audio_speech 协议；
//   - embedding：目录声明 embeddings 协议；
//   - rerank：目录声明 rerank 协议。
//
// 每条候选行要求「∃ 提供账户：能力域覆盖该 kind 且模型清单（含映射）含该
// 模型」（§3.2 目录声明 × 提供账户能力域复核，延续「目录声明 × 实际可派发」
// 收口：纯目录分类会列出跑不通的模型）。账户投影与 preflight 能力域闸门账户
// 侧同口径（域归一空值落 chat、清单空 = 不限制、启用映射按上游目标对清单）。
// functionCalling 目录投影已随 fetch_url 工具退役（§3.3 归一 SSE 修订）。

import (
	"strings"
)

// chat 协议词表（设计 §3.2：三协议之一即 chat 行）。
var chatKindProtocols = []string{"chat_completions", "messages", "generate_content"}

// normalizeProviderToken 归一供应商码（小写 + 去空白）。
func normalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// playgroundSearchImageModelFamily 返回供应商家族可路由的注册图像模型枚举
// （契约 §3.2 image 口径：gpt 系 × gpt-image-2、xai × grok-imagine 系）。
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

// playgroundMediaProviderAllowed 是视频/语音目录行的供应商家族白名单（问答音
// 视频工具设计 §3：openai（含 gpt 子供应商）/gemini/minimax/volcengine/qwen）。
func playgroundMediaProviderAllowed(providerCode string) bool {
	switch normalizeProviderToken(providerCode) {
	case "openai", "gpt", "gemini", "minimax", "volcengine", "qwen":
		return true
	}
	return false
}

// containsString 是顺序包含判定。
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
	KindChat      ModelKind = "chat"
	KindImage     ModelKind = "image"
	KindVideo     ModelKind = "video"
	KindAudio     ModelKind = "audio"
	KindEmbedding ModelKind = "embedding"
	KindRerank    ModelKind = "rerank"
)

// ModelOption 是目录分类产出的模型条目（key-models 响应行与六域候选校验
// 共用形状）。
type ModelOption struct {
	ModelID string    `json:"modelId"`
	Kind    ModelKind `json:"kind"`
}

// ClassifyKeyCatalogModels 对单供应商目录行做六类分类（目录侧口径；入口行
// Model 为空时跳过）。去重键为 (modelId, kind)：同一模型可兼具多类能力
// （如 veo-3 同时声明 generate_content 与 video 协议），双行合法。cmd 层
// KeyModels 适配器对并集模型逐行调用。
func ClassifyKeyCatalogModels(providerCode string, catalog []CatalogItem) []ModelOption {
	options := []ModelOption{}
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
		// chat：三协议之一（跨协议映射已退役，目录行的协议声明即派发入口族）。
		if hasAnyProtocol(item.SupportedAPIProtocols, chatKindProtocols) {
			add(ModelOption{ModelID: model, Kind: KindChat})
		}
		// image：家族白名单内的模型才可能入列（目录行仅用于确认路由，白名单
		// 是候选面，与旧 chat 生图候选同口径）。
		for _, imageModel := range playgroundSearchImageModelFamily(providerCode) {
			if imageModel == model {
				add(ModelOption{ModelID: model, Kind: KindImage})
			}
		}
		// embedding/rerank：目录协议声明（供应商不限媒体白名单，故置于
		// video/audio 白名单闸门之前）。
		if containsString(item.SupportedAPIProtocols, "embeddings") {
			add(ModelOption{ModelID: model, Kind: KindEmbedding})
		}
		if containsString(item.SupportedAPIProtocols, "rerank") {
			add(ModelOption{ModelID: model, Kind: KindRerank})
		}
		// video/audio：供应商白名单内 × 目录协议声明。
		if !playgroundMediaProviderAllowed(providerCode) {
			continue
		}
		if containsString(item.SupportedAPIProtocols, "video") {
			add(ModelOption{ModelID: model, Kind: KindVideo})
		}
		if containsString(item.SupportedAPIProtocols, "audio_speech") {
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

// KeyAccountModelMapping 是提供账户模型映射的最小投影（runtime cache
// AccountModelMapping 的演练场子集）。
type KeyAccountModelMapping struct {
	SourceModel   string
	UpstreamModel string
	Enabled       bool
}

// KeyCandidateAccount 是参与能力域复核的提供账户最小投影（cmd 层适配器从
// runtime cache 分组账户快照投影；CapabilityDomain/SupportedModels/
// ModelMappings 与 preflight 能力域闸门账户侧同口径）。
type KeyCandidateAccount struct {
	CapabilityDomain string
	SupportedModels  []string
	ModelMappings    []KeyAccountModelMapping
}

// keyAccountCapabilityDomain 归一提供账户能力域（空值 / 词表外值落 chat，
// accounts.capability_domain NOT NULL DEFAULT 'chat' 的列默认语义；与
// preflight 域闸门账户侧同口径，playground 侧独立投影避免 dispatch 依赖）。
func keyAccountCapabilityDomain(account KeyCandidateAccount) string {
	switch strings.ToLower(strings.TrimSpace(account.CapabilityDomain)) {
	case "video", "audio", "image", "embedding", "rerank":
		return strings.ToLower(strings.TrimSpace(account.CapabilityDomain))
	default:
		return "chat"
	}
}

// keyAccountSupportsModel 是模型清单/映射路由的共用判定（清单空 = 不限制；
// 启用映射且源模型命中时按上游目标模型对清单）。
func keyAccountSupportsModel(account KeyCandidateAccount, model string) bool {
	if len(account.SupportedModels) == 0 {
		return true
	}
	for _, mapping := range account.ModelMappings {
		if !mapping.Enabled || mapping.SourceModel != model {
			continue
		}
		target := mapping.UpstreamModel
		if target == "" {
			target = model
		}
		if containsString(account.SupportedModels, target) {
			return true
		}
	}
	return containsString(account.SupportedModels, model)
}

// KeyModelProvidedByAccounts 是 §3.2 提供账户复核：∃ 账户能力域覆盖该 kind
// （六值一一对应）且模型清单（含映射）含该模型；未过复核的模型不进候选。
func KeyModelProvidedByAccounts(kind ModelKind, model string, accounts []KeyCandidateAccount) bool {
	domain := string(kind)
	for _, account := range accounts {
		if keyAccountCapabilityDomain(account) != domain {
			continue
		}
		if keyAccountSupportsModel(account, model) {
			return true
		}
	}
	return false
}

// KeyRowProviderEntry 是同一 (modelId, kind) 在多供应商目录下的候选行来源
// （§3.2 单行口径消歧输入）。
type KeyRowProviderEntry struct {
	ProviderCode string
	Scope        string
}

// keyCatalogScopeRank 是目录行作用域 rank（personal 账户作用域自定义行 > builtin
// 平台内置行 > global 全局行；词表外 0）。
func keyCatalogScopeRank(scope string) int {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "personal":
		return 3
	case "builtin":
		return 2
	case "global":
		return 1
	default:
		return 0
	}
}

// SelectKeyRowProvider 在 (modelId, kind) 去重取单行时确定 providerCode：
// scope rank 高者优先，同 rank 按 providerCode 归一字典序升序（确定性消歧，
// §3.2「providerCode 取目录归属供应商行」）。entries 空返回 false。
func SelectKeyRowProvider(entries []KeyRowProviderEntry) (string, bool) {
	best := ""
	bestRank := -1
	found := false
	for _, entry := range entries {
		if strings.TrimSpace(entry.ProviderCode) == "" {
			continue
		}
		rank := keyCatalogScopeRank(entry.Scope)
		normalized := normalizeProviderToken(entry.ProviderCode)
		if !found || rank > bestRank || (rank == bestRank && normalized < normalizeProviderToken(best)) {
			best = entry.ProviderCode
			bestRank = rank
			found = true
		}
	}
	return best, found
}
