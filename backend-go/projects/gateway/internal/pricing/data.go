package pricing

import (
	"strings"
)

// rawModel mirrors provider-driver.types RawModelPricing — the LiteLLM/
// model-price-repo style snapshot rows the Node *.data.ts files carry. Token
// prices are USD per token; per-million values are derived by the same
// runtime arithmetic the Node factories use so the float64 bits match.
type rawModel struct {
	Model        string
	Mode         string
	CatalogOrder *int
	ReleaseDate  string
	ShutdownDate string

	InputCostPerToken          *float64
	InputCostPerTokenPriority  *float64
	InputCostPerTokenFlex      *float64
	InputCostPerTokenBatch     *float64
	OutputCostPerToken         *float64
	OutputCostPerTokenPriority *float64
	OutputCostPerTokenFlex     *float64
	OutputCostPerTokenBatch    *float64

	CacheCreationInputTokenCost                 *float64
	CacheCreationInputTokenCostPriority         *float64
	CacheCreationInputTokenCostFlex             *float64
	CacheCreationInputTokenCostAbove1hr         *float64
	CacheCreationInputTokenCostAbove1hrPriority *float64
	CacheCreationInputTokenCostAbove1hrFlex     *float64
	CacheStorageInputTokenCostPerHour           *float64
	CacheStorageInputTokenCostPerHourPriority   *float64
	CacheStorageInputTokenCostPerHourFlex       *float64
	CacheReadInputTokenCost                     *float64
	CacheReadInputTokenCostPriority             *float64
	CacheReadInputTokenCostFlex                 *float64
	CacheReadInputImageTokenCost                *float64

	InputCostPerImageToken         *float64
	OutputCostPerImage             *float64
	OutputCostPerImageToken        *float64
	InputCostPerAudioToken         *float64
	InputCostPerAudioTokenPriority *float64
	InputCostPerAudioTokenFlex     *float64
	OutputCostPerAudioToken        *float64
	// M1 同步音频维度（音频设计 §10）：TTS 输入按 USD/字符字面量
	// （perMillion 折算 USD/1M chars）；STT 输入按 USD/秒字面量。
	// M2 视频维度（媒体设计 §10）：视频输出按 USD/秒字面量。
	TtsInputCostPerChar      *float64
	AudioInputCostPerSecond  *float64
	VideoOutputCostPerSecond *float64

	ContextWindowTokens *int
	MaxInputTokens      *int
	MaxOutputTokens     *int
	MaxTokens           *int

	LongContextInputTokenThreshold          *int
	LongContextInputTokenThresholdInclusive bool
	LongContextInputCostMultiplier          *float64
	LongContextOutputCostMultiplier         *float64

	SupportedAPIProtocols []string
	InputModalities       []string
	OutputModalities      []string
	// ResponseFormats 是 TTS 行的对外 response_format 清单（音频设计 §4.1：
	// openai tts 官方全集 ["mp3","opus","aac","flac","wav","pcm"]、gemini tts
	// ["pcm"]，零转码）。
	ResponseFormats []string
	// SupportedToolsByProtocol 是「协议 × 工具」矩阵（AI问答工具体系与主子模型
	// 设计 6.4）：键为该行 SupportedAPIProtocols 的现有枚举值，值为该协议下
	// 可用的工具集。hosted 工具只在能执行它的协议下声明；一维 SupportedTools
	// 快照字段已随阶段 2 全链退场（消费方一律按二维矩阵读取）。
	SupportedToolsByProtocol map[string][]string

	SupportsPromptCaching     bool
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    string

	CodexSupportedReasoningLevels []string
	CodexDefaultReasoningLevel    string
	CodexMultiAgentVersion        string

	CatalogVisible *bool

	SourcePricingCurrency   string
	SourceExchangeRateToUsd *float64
	SourceExchangeRateDate  string
	SourcePricingNote       string
}

// perToken divides a USD-per-1M literal at runtime float64 precision, matching
// the Node `<usd> / 1_000_000` data rows bit for bit.
func perToken(usdPer1M float64) *float64 {
	out := usdPer1M / 1_000_000
	return &out
}

// usdPerMinuteToPerSecond 把官方按分钟标价的音频输入单价折算为每秒单价
// （M1 STT 计量落秒，音频设计 §10/契约 §2.8；运行时除法保持来源可追溯）。
func usdPerMinuteToPerSecond(usdPerMinute float64) *float64 {
	out := usdPerMinute / 60
	return &out
}

// perChar 把 USD/1M characters 官方字符价折算为每字符字面量（TTS 行
// TtsInputCostPerChar 使用；算术与 perToken 相同，perMillion 回转无损）。
func perChar(usdPer1MChars float64) *float64 {
	return perToken(usdPer1MChars)
}

// toolsByProtocol 按静态快照的统一拆分口径，把一行的一维工具集分配到其
// SupportedAPIProtocols 枚举下（6.4「协议 × 工具」矩阵，不新增协议枚举）：
//   - chat_completions：恒只携带 function_calling（该行声明了它才出现）；
//   - responses / messages / generate_content / stream_generate_content /
//     interactions：hosted/原生工具的归属协议，携带完整声明工具集；
//   - 其余协议（count_tokens / message_token_counting / embed_content /
//     completions / images 等计数、嵌入、遗留与媒体协议）：不执行对话工具，
//     不出现键。
//
// 空 tools 返回 nil（行未声明任何工具）。
func toolsByProtocol(protocols, tools []string) map[string][]string {
	if len(tools) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, protocol := range protocols {
		switch protocol {
		case "chat_completions":
			for _, tool := range tools {
				if tool == "function_calling" {
					out[protocol] = []string{"function_calling"}
					break
				}
			}
		case "responses", "messages", "generate_content", "stream_generate_content", "interactions":
			out[protocol] = tools
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CopyToolsByProtocol 深拷贝矩阵，隔离快照共享的列表底层数组；nil 保持 nil。
func CopyToolsByProtocol(toolsByProtocol map[string][]string) map[string][]string {
	if toolsByProtocol == nil {
		return nil
	}
	out := make(map[string][]string, len(toolsByProtocol))
	for protocol, tools := range toolsByProtocol {
		out[protocol] = append([]string(nil), tools...)
	}
	return out
}

// providerEntry mirrors one ModelPricingProviderDriver registration.
type providerEntry struct {
	providerID    string
	pricingSource string
	billingPolicy string
	supports      func(providerCode string) bool
	rawModels     []rawModel
}

// providerCatalog mirrors modelPricingProviderDrivers (registry order kept:
// openai, deepseek, glm, anthropic, gemini, xai).
var providerCatalog = []*providerEntry{
	{
		providerID:    "openai-compatible",
		pricingSource: "openai-pricing-snapshot",
		billingPolicy: "openai",
		supports:      isOpenAICompatibleProviderCode,
		rawModels:     openAIModelPricingData,
	},
	{
		providerID:    "deepseek",
		pricingSource: "deepseek-pricing-snapshot",
		billingPolicy: "deepseek",
		supports:      func(code string) bool { return normalizeProviderToken(code) == "deepseek" },
		rawModels:     deepSeekModelPricingData,
	},
	{
		providerID:    "glm",
		pricingSource: "glm-pricing-snapshot",
		billingPolicy: "glm",
		supports:      func(code string) bool { return normalizeProviderToken(code) == "glm" },
		rawModels:     glmModelPricingData,
	},
	{
		providerID:    "anthropic",
		pricingSource: "anthropic-pricing-snapshot",
		billingPolicy: "anthropic",
		supports:      func(code string) bool { return normalizeProviderToken(code) == "anthropic" },
		rawModels:     anthropicModelPricingData,
	},
	{
		providerID:    "gemini",
		pricingSource: "gemini-pricing-snapshot",
		billingPolicy: "gemini",
		supports:      func(code string) bool { return normalizeProviderToken(code) == "gemini" },
		rawModels:     geminiModelPricingData,
	},
	{
		providerID:    "xai",
		pricingSource: "xai-official-pricing-2026-07-18",
		billingPolicy: "xai",
		supports:      func(code string) bool { return normalizeProviderToken(code) == "xai" },
		rawModels:     xAIModelPricingData,
	},
}

// providerEntryFor mirrors modelPricingProviderDriverForProvider: the first
// driver whose supportsProvider matches the normalized provider token.
func providerEntryFor(providerCode string) *providerEntry {
	normalized := normalizeProviderToken(providerCode)
	if normalized == "" {
		return nil
	}
	for _, entry := range providerCatalog {
		if entry.supports(normalized) {
			return entry
		}
	}
	return nil
}

// normalizeProviderToken mirrors domain/provider-protocol normalizeProviderToken.
func normalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
