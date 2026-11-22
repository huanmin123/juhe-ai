package playground

// 演练场 Key 模型候选分类（docs/functions/演练场设计.md §3.2 真实 Key 化修订）：
// 所选 API Key 的分组模型并集经目录行逐模型分类为六类 kind（目录侧口径）：
//   - chat：目录协议含 chat_completions|messages|generate_content 之一；
//   - image：家族白名单（gpt 系 × gpt-image-2、xai × grok-imagine 系；目录
//     声明 image_generation 的其他模型不进候选）；
//   - video：供应商白名单 × 目录声明 video 协议；
//   - audio：供应商白名单 × 目录声明 audio_speech 协议；
//   - embedding：目录声明 embeddings 协议；
//   - rerank：目录声明 rerank 协议。
//
// 原账户侧复核（能力域 × 模型清单可路由）随固定派发退役：候选由 Key 的分组
// 并集承载，可派发性由 /v1 链按候选路由真实裁决。functionCalling 目录投影已
// 随 fetch_url 工具退役（§3.3 归一 SSE 修订）。

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

// classifyKeyCatalogModels 对单供应商目录行做六类分类（目录侧口径；入口行
// Model 为空时跳过）。去重键为 (modelId, kind)：同一模型可兼具多类能力
// （如 veo-3 同时声明 generate_content 与 video 协议），双行合法。
func classifyKeyCatalogModels(providerCode string, catalog []CatalogItem) []ModelOption {
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
