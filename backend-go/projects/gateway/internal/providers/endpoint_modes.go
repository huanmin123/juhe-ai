// endpoint_modes.go 承载 models 单表（供应商与上游协议面重构设计 §3）的
// supported_endpoint_modes_json 与旧目录行 (mode, supportedApiProtocols) 投影
// 之间的双向映射。旧 `mode` 与 `supported_api_protocols_json` 无物理列，管理面
// 目录行的这两个 DTO 键由本文件的逆映射从 modes json 派生（对前端保留键名）；
// 写路径（自定义模型保存、内置行配置补丁、settings 目录重插）经正向映射落
// supported_endpoint_modes_json。
//
// 正向映射 modelSeedProtocolEndpointProtocols 与 maintenance 种子投影
// （schema/model_catalog_data.go modelSeedProtocolEndpointModes）逐键一致；
// 逆映射 catalogEndpointModeLegacyProjection 与 chat 面
// （cmd/juhe-ai-gateway/chain_catalog.go 同名变量）逐键一致——三处任一改动
// 必须同步，防止管理面/chat 面目录投影漂移。
package providers

import "sort"

// catalogProtocolEndpointModes 把目录行声明的上游 API 协议名映射到设计 §2.2
// 的 25 值 endpoint mode 词表。"completions" 是旧 /v1/completions 遗留声明，
// 目标协议面无对应 mode，显式映射为空集（与 maintenance 种子同口径）。
var catalogProtocolEndpointModes = map[string][]string{
	"chat_completions":        {"chat_json", "chat_sse"},
	"responses":               {"responses_json", "responses_sse"},
	"messages":                {"messages_json", "messages_sse"},
	"generate_content":        {"generate_content_json", "generate_content_sse"},
	"stream_generate_content": {"generate_content_sse"},
	"interactions":            {"interactions_json", "interactions_sse"},
	"message_token_counting":  {"message_token_counting"},
	"count_tokens":            {"count_tokens"},
	"embed_content":           {"embed_content"},
	"images":                  {"images_json"},
	"video":                   {"video_create", "video_get", "video_content", "video_cancel"},
	"realtime":                {"realtime_session"},
	"audio_speech":            {"audio_speech"},
	"audio_transcription":     {"audio_transcription_json"},
	"completions":             {},
}

// catalogEndpointModeLegacyProjection 是 supported_endpoint_modes_json 元素到
// 旧目录行 (mode, supportedApiProtocols) 投影的读侧逆映射（与 chain_catalog
// 同名变量同源）。generate_content_sse 正向来自 generate_content 与
// stream_generate_content 两个旧协议声明，反推取两 token 并集。
var catalogEndpointModeLegacyProjection = map[string][][2]string{
	"chat_json":                {{"chat", "chat_completions"}},
	"chat_sse":                 {{"chat", "chat_completions"}},
	"messages_json":            {{"chat", "messages"}},
	"messages_sse":             {{"chat", "messages"}},
	"responses_json":           {{"responses", "responses"}},
	"responses_sse":            {{"responses", "responses"}},
	"generate_content_json":    {{"chat", "generate_content"}},
	"generate_content_sse":     {{"chat", "generate_content"}, {"chat", "stream_generate_content"}},
	"interactions_json":        {{"chat", "interactions"}},
	"interactions_sse":         {{"chat", "interactions"}},
	"images_json":              {{"image_generation", "images"}},
	"video_create":             {{"video", "video"}},
	"video_get":                {{"video", "video"}},
	"video_content":            {{"video", "video"}},
	"video_cancel":             {{"video", "video"}},
	"audio_speech":             {{"audio", "audio_speech"}},
	"audio_transcription_json": {{"audio", "audio_transcription"}},
	"realtime_session":         {{"audio", "realtime"}},
	"message_token_counting":   {{"embedding", "message_token_counting"}},
	"count_tokens":             {{"embedding", "count_tokens"}},
	"embed_content":            {{"embedding", "embed_content"}},
}

// catalogLegacyModeRank 是逆映射 mode 的词表优先序（video > audio >
// image_generation > embedding > responses > chat，与旧 seed 行的 mode 值域
// 一致）；链路面 chain_catalog 使用同一 rank 表。
var catalogLegacyModeRank = map[string]int{
	"chat":             0,
	"responses":        1,
	"embedding":        2,
	"image_generation": 3,
	"audio":            4,
	"video":            5,
}

// DeriveLegacyModeAndProtocols 从 supported_endpoint_modes_json 元素反推旧目
// 录行的 mode 与 supportedApiProtocols 投影：mode 取词表最高优先命中，协议
// token 去重升序；空 mode 集（draft/disabled 行）返回空 mode 与空协议（调用方
// 按旧行为渲染 NULL→absent / 空数组）。
func DeriveLegacyModeAndProtocols(endpointModes []string) (mode string, protocols []string) {
	seenProtocols := map[string]bool{}
	protocols = []string{}
	bestMode := ""
	bestRank := -1
	for _, endpointMode := range endpointModes {
		for _, pair := range catalogEndpointModeLegacyProjection[endpointMode] {
			if rank, known := catalogLegacyModeRank[pair[0]]; known && rank > bestRank {
				bestRank = rank
				bestMode = pair[0]
			}
			if !seenProtocols[pair[1]] {
				seenProtocols[pair[1]] = true
				protocols = append(protocols, pair[1])
			}
		}
	}
	sort.Strings(protocols)
	return bestMode, protocols
}

// ForwardProtocolsToEndpointModes 把目录行声明的协议列表正向映射为设计 §2.2
// 的 endpoint mode 集合（去重、ASCII 升序）。deny-by-default：未知协议返回
// error（与 maintenance 种子投影同语义，禁止对未知协议静默产空 mode 集）。
// 空协议列表返回空集合（draft/disabled 行允许空；active 空集由调用方按各自
// 契约处理——自定义模型写路径回退 mode 缺省集）。
func ForwardProtocolsToEndpointModes(protocols []string) ([]string, error) {
	seen := map[string]bool{}
	modes := []string{}
	for _, protocol := range protocols {
		declared, known := catalogProtocolEndpointModes[protocol]
		if !known {
			return nil, &UnknownCatalogProtocolError{Protocol: protocol}
		}
		for _, mode := range declared {
			if !seen[mode] {
				seen[mode] = true
				modes = append(modes, mode)
			}
		}
	}
	sort.Strings(modes)
	return modes, nil
}

// UnknownCatalogProtocolError 是 ForwardProtocolsToEndpointModes 的拒绝错误
// （deny-by-default 契约的显式形状）。
type UnknownCatalogProtocolError struct{ Protocol string }

func (e *UnknownCatalogProtocolError) Error() string {
	return "不支持的接口协议：" + e.Protocol
}

// catalogModeDefaultEndpointModes 是自定义模型写路径的 mode 缺省集：提交未
// 携带任何可映射协议时按 UI 分类（text/image/audio/video）补默认 mode 集，
// 对齐旧「mode 有值、协议为空」行的目录语义。
var catalogModeDefaultEndpointModes = map[string][]string{
	"text":  {"chat_json", "chat_sse"},
	"image": {"images_json"},
	"audio": {"audio_speech"},
	"video": {"video_create", "video_get", "video_content", "video_cancel"},
}

// ForwardCustomModelEndpointModes 把自定义模型写路径的 (UI mode, protocols)
// 合成 supported_endpoint_modes_json 元素集：协议非空走协议映射（未知协议
// 拒绝），协议为空回退 mode 缺省集。结果去重、ASCII 升序。
func ForwardCustomModelEndpointModes(mode string, protocols []string) ([]string, error) {
	modes, err := ForwardProtocolsToEndpointModes(protocols)
	if err != nil {
		return nil, err
	}
	if len(modes) > 0 {
		return modes, nil
	}
	defaults, known := catalogModeDefaultEndpointModes[mode]
	if !known {
		return []string{}, nil
	}
	copied := make([]string, len(defaults))
	copy(copied, defaults)
	sort.Strings(copied)
	return copied, nil
}
