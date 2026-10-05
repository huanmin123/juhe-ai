// derived.go owns the catalog derived fields ported from the Node write-side
// collaborators: generationParameterCapabilitiesForModel /
// limitGenerationParameterMaxOutputTokens
// (backend/src/modules/chat/chat-generation-parameters.ts) and
// buildProviderCatalogDisplay (provider-billing.service.ts +
// provider-billing.policies.ts + provider-billing.shared.ts).
//
// Static in-code pricing sources (inputModalities / outputModalities /
// supportedToolsByProtocol / cachedImageInputUsdPer1M / sourcePricing*) live in
// internal/pricing's snapshot tables; staticPricingFor resolves them through
// pricing.FindProviderModelPricing (getProviderModelPricing). The static
// generationParameterCapabilities override stays unused: the Node static rows
// derive the same capabilities via generationParameterCapabilitiesForModel, so
// the generated table below is already the static content.
package providers

import (
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pricing"
)

// staticPricingSnapshot mirrors the fields toBuiltInCatalogItem pulls from
// getProviderModelPricing(item.providerCode, item.model).
type staticPricingSnapshot struct {
	InputModalities                 []string
	OutputModalities                []string
	SupportedToolsByProtocol        map[string][]string
	GenerationParameterCapabilities map[string][]generationParameterCapability
	CachedImageInputUsdPer1M        *float64
	SourcePricingCurrency           string
	SourceExchangeRateToUsd         *float64
	SourceExchangeRateDate          string
	SourcePricingNote               string
}

// staticPricingFor is the internal/pricing seam. nil result = the static
// table has no entry; every caller then falls back to the generated/empty
// shapes.
func staticPricingFor(providerCode, model string) *staticPricingSnapshot {
	found := pricing.FindProviderModelPricing(providerCode, model)
	if found == nil {
		return nil
	}
	snapshot := &staticPricingSnapshot{
		InputModalities:          found.InputModalities,
		OutputModalities:         found.OutputModalities,
		SupportedToolsByProtocol: pricing.CopyToolsByProtocol(found.SupportedToolsByProtocol),
		CachedImageInputUsdPer1M: found.CachedImageInputUsdPer1M,
		SourcePricingCurrency:    found.SourcePricingCurrency,
		SourceExchangeRateToUsd:  found.SourceExchangeRateToUsd,
		SourceExchangeRateDate:   found.SourceExchangeRateDate,
		SourcePricingNote:        found.SourcePricingNote,
	}
	if snapshot.SupportedToolsByProtocol == nil {
		snapshot.SupportedToolsByProtocol = map[string][]string{}
	}
	return snapshot
}

// BuiltInStaticDerivedCapabilities 是内置目录行的静态派生能力全集：数据库目录表
// 从 Node 时代起就没有 supported_tools / input_modalities / output_modalities
// 列，Node toBuiltInCatalogItem 在读取链用代码内静态定价表兜底。管理面
// （ApplyBuiltInStaticDerivedFields）与 chat 面
// （cmd/juhe-ai-gateway/chain_catalog.go decorateBuiltinStaticDerivedCapabilities）
// 共同调用 ResolveBuiltInStaticDerivedCapabilities，保证两侧解析同源不漂移；
// 静态数据源仍是 internal/pricing 的快照表（staticPricingFor）。
//
// SupportedToolsByProtocol 是「协议 × 工具」矩阵（AI问答工具体系与主子模型设计
// 6.4）：键为该行协议枚举，值为该协议下可用的工具集；目录投影的一维
// 一维 supportedTools 过渡字段已随阶段 2 全链删除（消费方按二维矩阵读取）。
// （原注释行：
// 随 chat 面切换删除）。
type BuiltInStaticDerivedCapabilities struct {
	InputModalities                 []string
	OutputModalities                []string
	SupportedToolsByProtocol        map[string][]string
	CachedImageInputUsdPer1M        *float64
	SourcePricingCurrency           string
	SourceExchangeRateToUsd         *float64
	SourceExchangeRateDate          string
	SourcePricingNote               string
	GenerationParameterCapabilities map[string]any
}

// ResolveBuiltInStaticDerivedCapabilities ports the toBuiltInCatalogItem
// capability derivations (the body of ApplyBuiltInStaticDerivedFields) into a
// row-shape-agnostic result so the admin face and the chat face resolve the
// static fallback through one implementation. keepStaticPricingSource keeps
// the manual-override provenance rule: a manual-override row retains its own
// pricing source instead of the static table's.
func ResolveBuiltInStaticDerivedCapabilities(providerCode, model string, maxOutputTokens *int64, source string) BuiltInStaticDerivedCapabilities {
	resolved := BuiltInStaticDerivedCapabilities{
		InputModalities:                 []string{},
		OutputModalities:                []string{},
		SupportedToolsByProtocol:        map[string][]string{},
		GenerationParameterCapabilities: map[string]any{},
	}
	static := staticPricingFor(providerCode, model)
	keepStaticPricingSource := source != "manual-override"
	if static != nil {
		if len(static.InputModalities) > 0 {
			resolved.InputModalities = append([]string{}, static.InputModalities...)
		}
		if len(static.OutputModalities) > 0 {
			resolved.OutputModalities = append([]string{}, static.OutputModalities...)
		}
		if len(static.SupportedToolsByProtocol) > 0 {
			resolved.SupportedToolsByProtocol = pricing.CopyToolsByProtocol(static.SupportedToolsByProtocol)
		}
		if keepStaticPricingSource {
			resolved.CachedImageInputUsdPer1M = static.CachedImageInputUsdPer1M
			resolved.SourcePricingCurrency = static.SourcePricingCurrency
			resolved.SourceExchangeRateToUsd = static.SourceExchangeRateToUsd
			resolved.SourceExchangeRateDate = static.SourceExchangeRateDate
			resolved.SourcePricingNote = static.SourcePricingNote
		}
	}
	capabilities := generationParameterCapabilitiesForModel(providerCode, model, maxOutputTokens)
	if static != nil && len(static.GenerationParameterCapabilities) > 0 {
		capabilities = static.GenerationParameterCapabilities
	}
	resolved.GenerationParameterCapabilities = generationParameterCapabilitiesToAny(
		limitGenerationParameterMaxOutputTokens(capabilities, maxOutputTokens))
	return resolved
}

// generationParameterCapability mirrors ChatGenerationParameterCapability.
type generationParameterCapability struct {
	Parameter    string  `json:"parameter"`
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
	Step         float64 `json:"step"`
	DefaultValue float64 `json:"defaultValue"`
}

// generationParameterCapabilitiesForModel ports the same-named helper over
// chat-generation-parameters.ts.
func generationParameterCapabilitiesForModel(providerCode, model string, maxOutputTokens *int64) map[string][]generationParameterCapability {
	normalized := normalizeProviderToken(providerCode)
	modelName := strings.ToLower(strings.TrimSpace(model))
	limit := positiveInt64(maxOutputTokens)
	capability := func(parameter string) generationParameterCapability {
		definition, ok := generationParameterDefinitions[parameter]
		if !ok {
			return generationParameterCapability{Parameter: parameter}
		}
		if parameter == "maxOutputTokens" && limit != nil {
			definition.Max = float64(*limit)
			definition.DefaultValue = minFloat64(definition.DefaultValue, float64(*limit))
		}
		return definition
	}
	selectParameters := func(parameters ...string) []generationParameterCapability {
		output := make([]generationParameterCapability, 0, len(parameters))
		for _, parameter := range parameters {
			output = append(output, capability(parameter))
		}
		return output
	}
	allParameters := []string{"temperature", "topP", "frequencyPenalty", "presencePenalty", "maxOutputTokens", "seed"}

	switch normalized {
	case "gpt":
		chat := selectParameters(allParameters...)
		responses := selectParameters("temperature", "topP", "maxOutputTokens")
		if strings.HasPrefix(modelName, "gpt-5") {
			chat = selectParameters("frequencyPenalty", "presencePenalty", "maxOutputTokens", "seed")
			responses = selectParameters("maxOutputTokens")
		}
		return map[string][]generationParameterCapability{"chat_completions": chat, "responses": responses}
	case "xai":
		reasoning := strings.Contains(modelName, "reasoning") || strings.Contains(modelName, "think")
		chat := selectParameters(allParameters...)
		if reasoning {
			chat = selectParameters("temperature", "topP", "maxOutputTokens", "seed")
		}
		return map[string][]generationParameterCapability{
			"chat_completions": chat,
			"responses":        selectParameters("temperature", "topP", "maxOutputTokens"),
		}
	case "deepseek":
		if modelName == "deepseek-chat" {
			return map[string][]generationParameterCapability{
				"chat_completions": selectParameters("temperature", "topP", "maxOutputTokens"),
			}
		}
		return map[string][]generationParameterCapability{
			"chat_completions": selectParameters("maxOutputTokens"),
		}
	case "anthropic":
		if anthropicRecentSamplingPattern.MatchString(modelName) {
			return map[string][]generationParameterCapability{
				"chat_completions": selectParameters("maxOutputTokens"),
			}
		}
		return map[string][]generationParameterCapability{
			"chat_completions": selectParameters("temperature", "topP", "maxOutputTokens"),
		}
	case "gemini":
		if gemini3SamplingPattern.MatchString(modelName) {
			return map[string][]generationParameterCapability{
				"chat_completions": selectParameters("maxOutputTokens"),
				"responses":        selectParameters("maxOutputTokens"),
			}
		}
		sampling := selectParameters("temperature", "topP", "maxOutputTokens")
		return map[string][]generationParameterCapability{
			"chat_completions": append([]generationParameterCapability{}, sampling...),
			"responses":        sampling,
		}
	case "glm":
		output := make([]generationParameterCapability, 0, 3)
		for _, parameter := range []string{"temperature", "topP", "maxOutputTokens"} {
			entry := capability(parameter)
			if parameter == "temperature" {
				entry.Max = 1
			}
			if parameter == "topP" {
				entry.Min = 0.01
			}
			output = append(output, entry)
		}
		return map[string][]generationParameterCapability{"chat_completions": output}
	}
	return map[string][]generationParameterCapability{}
}

var (
	// /(?:claude-(?:opus|sonnet)-4\.(?:7|8)|(?:fable|mythos|opus|sonnet)-5)/
	anthropicRecentSamplingPattern = regexp.MustCompile(`(?:claude-(?:opus|sonnet)-4\.(?:7|8)|(?:fable|mythos|opus|sonnet)-5)`)
	// /(?:^|[-_.])gemini-3(?:[-_.]|$)/
	gemini3SamplingPattern = regexp.MustCompile(`(?:^|[-_.])gemini-3(?:[-_.]|$)`)
)

// generationParameterDefinitions mirrors the definitions table.
var generationParameterDefinitions = map[string]generationParameterCapability{
	"temperature":      {Parameter: "temperature", Min: 0, Max: 2, Step: 0.1, DefaultValue: 1},
	"topP":             {Parameter: "topP", Min: 0, Max: 1, Step: 0.05, DefaultValue: 1},
	"frequencyPenalty": {Parameter: "frequencyPenalty", Min: -2, Max: 2, Step: 0.1, DefaultValue: 0},
	"presencePenalty":  {Parameter: "presencePenalty", Min: -2, Max: 2, Step: 0.1, DefaultValue: 0},
	"maxOutputTokens":  {Parameter: "maxOutputTokens", Min: 1, Max: 128_000, Step: 1, DefaultValue: 4_096},
	"seed":             {Parameter: "seed", Min: 0, Max: 2_147_483_647, Step: 1, DefaultValue: 0},
}

// limitGenerationParameterMaxOutputTokens ports the same-named helper: the
// maxOutputTokens entry is clamped (or dropped when the clamp violates min).
func limitGenerationParameterMaxOutputTokens(capabilities map[string][]generationParameterCapability, maxOutputTokens *int64) map[string][]generationParameterCapability {
	limit := positiveInt64(maxOutputTokens)
	if limit == nil {
		return capabilities
	}
	output := make(map[string][]generationParameterCapability, len(capabilities))
	for protocol, items := range capabilities {
		limited := make([]generationParameterCapability, 0, len(items))
		for _, item := range items {
			if item.Parameter != "maxOutputTokens" {
				limited = append(limited, item)
				continue
			}
			max := item.Max
			if float64(*limit) < max {
				max = float64(*limit)
			}
			if max < item.Min {
				continue
			}
			item.Max = max
			item.DefaultValue = minFloat64(item.DefaultValue, max)
			limited = append(limited, item)
		}
		output[protocol] = limited
	}
	return output
}

// generationParameterCapabilitiesToAny widens the typed map into the DTO map.
func generationParameterCapabilitiesToAny(capabilities map[string][]generationParameterCapability) map[string]any {
	output := make(map[string]any, len(capabilities))
	for protocol, items := range capabilities {
		output[protocol] = items
	}
	return output
}

func positiveInt64(value *int64) *int64 {
	if value == nil || *value <= 0 {
		return nil
	}
	return value
}

func minFloat64(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

// --- catalog display -----------------------------------------------------

// catalogDisplayItem mirrors ProviderCatalogDisplayItem.
type catalogDisplayItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Value  any    `json:"value"`
	Format string `json:"format"`
}

// catalogDisplaySection mirrors ProviderCatalogDisplaySection.
type catalogDisplaySection struct {
	Key   string               `json:"key"`
	Label string               `json:"label"`
	Items []catalogDisplayItem `json:"items"`
}

// buildProviderCatalogDisplay ports buildProviderCatalogDisplay + the six
// provider billing policies' buildCatalogDisplay bodies; unknown provider
// codes render the Node [] default.
func buildProviderCatalogDisplay(item *ModelCatalogItem) []catalogDisplaySection {
	switch normalizeProviderToken(item.ProviderCode) {
	case "openai", "gpt":
		return openAICatalogDisplay(item)
	case "anthropic":
		return anthropicCatalogDisplay(item)
	case "deepseek":
		return deepSeekCatalogDisplay(item)
	case "glm":
		return glmCatalogDisplay(item)
	case "gemini":
		return geminiCatalogDisplay(item)
	case "xai":
		return xaiCatalogDisplay(item)
	case "minimax":
		return minimaxCatalogDisplay(item)
	case "volcengine":
		return volcengineCatalogDisplay(item)
	case "qwen":
		return qwenCatalogDisplay(item)
	}
	return []catalogDisplaySection{}
}

func openAICatalogDisplay(item *ModelCatalogItem) []catalogDisplaySection {
	hasModalityPrices := item.ImageInputUsdPer1M != nil || item.CachedImageInputUsdPer1M != nil ||
		item.ImageOutputUsdPer1M != nil || item.AudioInputUsdPer1M != nil || item.AudioOutputUsdPer1M != nil
	prefix := ""
	if hasModalityPrices {
		prefix = "文本"
	}
	sections := appendSection(nil,
		priceSection("token_pricing", "Token 计费", []catalogDisplayItem{
			priceEntry("input", prefix+"输入", item.InputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_read", prefix+"缓存读", item.CachedInputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_write", prefix+"缓存写入", item.CacheWriteUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("output", prefix+"输出", item.OutputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("image_input", "图片输入", item.ImageInputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("image_cache_read", "图片缓存读", item.CachedImageInputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("image_output", "图片输出", item.ImageOutputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("audio_input", "音频输入", item.AudioInputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("audio_output", "音频输出", item.AudioOutputUsdPer1M, "usd_per_1m_tokens"),
		}))
	sections = appendSection(sections, imageUnitSection(item))
	sections = appendSections(sections, serviceTierSections(item))
	sections = appendSection(sections, longContextSection(item))
	sections = appendSection(sections, reasoningSection(item))
	sections = appendSection(sections, capacitySection(item))
	sections = appendSection(sections, sourceConversionSection(item))
	return sections
}

func anthropicCatalogDisplay(item *ModelCatalogItem) []catalogDisplaySection {
	sections := appendSection(nil,
		priceSection("token_pricing", "Token 计费", []catalogDisplayItem{
			priceEntry("input", "输入", item.InputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("output", "输出", item.OutputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_read", "缓存读", item.CachedInputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_write_5m", "5m 缓存写入", item.CacheWriteUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_write_1h", "1h 缓存写入", item.CacheWrite1hUsdPer1M, "usd_per_1m_tokens"),
		}))
	sections = appendSections(sections, serviceTierSections(item))
	sections = appendSection(sections, reasoningSection(item))
	sections = appendSection(sections, capacitySection(item))
	sections = appendSection(sections, sourceConversionSection(item))
	return sections
}

func geminiCatalogDisplay(item *ModelCatalogItem) []catalogDisplaySection {
	entries := []catalogDisplayItem{
		priceEntry("input", "输入", item.InputUsdPer1M, "usd_per_1m_tokens"),
		priceEntry("output", "输出", item.OutputUsdPer1M, "usd_per_1m_tokens"),
		priceEntry("cache_read", "缓存读", item.CachedInputUsdPer1M, "usd_per_1m_tokens"),
		priceEntry("cache_storage", "缓存存储", item.CacheStorageUsdPer1MPerHour, "usd_per_1m_token_hour"),
		priceEntry("image_input", "图片输入", item.ImageInputUsdPer1M, "usd_per_1m_tokens"),
		priceEntry("image_output", "图片输出", item.ImageOutputUsdPer1M, "usd_per_1m_tokens"),
		priceEntry("audio_input", "音频输入", item.AudioInputUsdPer1M, "usd_per_1m_tokens"),
		priceEntry("audio_output", "音频输出", item.AudioOutputUsdPer1M, "usd_per_1m_tokens"),
	}
	entries = append(entries, longContextTokenEntries(item, "缓存读")...)
	sections := appendSection(nil, priceSection("token_pricing", "Token 计费", entries))
	sections = appendSections(sections, serviceTierSections(item))
	sections = appendSection(sections, reasoningSection(item))
	sections = appendSection(sections, capacitySection(item))
	sections = appendSection(sections, sourceConversionSection(item))
	return sections
}

func xaiCatalogDisplay(item *ModelCatalogItem) []catalogDisplaySection {
	label := "Token 计费"
	if item.Mode != nil && *item.Mode == "image" {
		label = "图像输入"
	}
	entries := []catalogDisplayItem{
		priceEntry("input", "输入", item.InputUsdPer1M, "usd_per_1m_tokens"),
		priceEntry("cache_read", "缓存读", item.CachedInputUsdPer1M, "usd_per_1m_tokens"),
		priceEntry("output", "输出", item.OutputUsdPer1M, "usd_per_1m_tokens"),
	}
	entries = append(entries, longContextTokenEntries(item, "缓存读")...)
	sections := appendSection(nil, priceSection("token_pricing", label, entries))
	sections = appendSection(sections, imageUnitSection(item))
	sections = appendSections(sections, serviceTierSections(item))
	sections = appendSection(sections, reasoningSection(item))
	sections = appendSection(sections, capacitySection(item))
	sections = appendSection(sections, sourceConversionSection(item))
	return sections
}

func deepSeekCatalogDisplay(item *ModelCatalogItem) []catalogDisplaySection {
	sections := appendSection(nil,
		priceSection("token_pricing", "Token 计费", []catalogDisplayItem{
			priceEntry("cache_miss", "输入", item.InputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_hit", "缓存读", item.CachedInputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("output", "输出", item.OutputUsdPer1M, "usd_per_1m_tokens"),
		}))
	sections = appendSection(sections, reasoningSection(item))
	sections = appendSection(sections, capacitySection(item))
	sections = appendSection(sections, sourceConversionSection(item))
	return sections
}

func glmCatalogDisplay(item *ModelCatalogItem) []catalogDisplaySection {
	usesSingleTokenPrice := item.InputUsdPer1M != nil && item.OutputUsdPer1M != nil &&
		*item.InputUsdPer1M == *item.OutputUsdPer1M && item.CachedInputUsdPer1M == nil
	entries := []catalogDisplayItem{
		priceEntry("input", "输入", item.InputUsdPer1M, "usd_per_1m_tokens"),
		priceEntry("cache_hit", "缓存读", item.CachedInputUsdPer1M, "usd_per_1m_tokens"),
		priceEntry("output", "输出", item.OutputUsdPer1M, "usd_per_1m_tokens"),
	}
	label := "Token 计费"
	if usesSingleTokenPrice {
		entries = []catalogDisplayItem{priceEntry("token", "Token", item.InputUsdPer1M, "usd_per_1m_tokens")}
	}
	sections := appendSection(nil, priceSection("token_pricing", label, entries))
	sections = appendSection(sections, reasoningSection(item))
	sections = appendSection(sections, capacitySection(item))
	return sections
}

// minimaxCatalogDisplay / volcengineCatalogDisplay / qwenCatalogDisplay 是
// 三家媒体供应商的目录投影薄壳，共享 genericMediaCatalogDisplay 实现；未来
// 单家分化时在此分叉。
func minimaxCatalogDisplay(item *ModelCatalogItem) []catalogDisplaySection {
	return genericMediaCatalogDisplay(item)
}

func volcengineCatalogDisplay(item *ModelCatalogItem) []catalogDisplaySection {
	return genericMediaCatalogDisplay(item)
}

func qwenCatalogDisplay(item *ModelCatalogItem) []catalogDisplaySection {
	return genericMediaCatalogDisplay(item)
}

// genericMediaCatalogDisplay 按 item.Mode 场景化输出（列表按场景不同），
// 价格只在 ModelCatalogItem 有真实通道时落 section（宁缺勿假）：
//   - chat（含 mode 未标注的 custom 行兜底）：Token 计费（输入/缓存读/
//     缓存写入/输出/音频输入）+ 思考能力 + 容量 + 美元换算；
//   - image / image_generation：Token 计费 + 图片生成（每张，走
//     OutputUsdPerImage 通道）+ 思考能力 + 容量 + 美元换算；
//   - audio（audio_speech / audio_transcription 协议行）、video、embedding：
//     TTS 字符价 / ASR 秒价 / 视频秒价只在 internal/pricing 静态层计费面
//     （PriceSet 的 TtsInputUsdPer1MChars / AudioInputUsdPerSecond /
//     VideoOutputUsdPerSecond），目录表无对应列、ModelCatalogItem 无对应
//     字段 → 不落价格 section；行恰带 token 价（如 chat 通道回填）时照实
//     显示 Token 计费。容量与美元换算照常。
//
// 人民币来源行的披露位是 sourceConversionSection（官方币种 CNY + 约定汇率
// 7.0 + 官方源价 note），经 ApplyBuiltInStaticDerivedFields 从静态表透传，
// 媒体行同样携带。无值 section 由 priceSection/textSection 自动丢弃。
func genericMediaCatalogDisplay(item *ModelCatalogItem) []catalogDisplaySection {
	mode := ""
	if item.Mode != nil {
		mode = strings.ToLower(strings.TrimSpace(*item.Mode))
	}
	sections := appendSection(nil,
		priceSection("token_pricing", "Token 计费", []catalogDisplayItem{
			priceEntry("input", "输入", item.InputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_read", "缓存读", item.CachedInputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_write", "缓存写入", item.CacheWriteUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("output", "输出", item.OutputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("audio_input", "音频输入", item.AudioInputUsdPer1M, "usd_per_1m_tokens"),
		}))
	if mode == "image" || mode == "image_generation" {
		sections = appendSection(sections, imageUnitSection(item))
	}
	if mode == "" || mode == "chat" || mode == "image" || mode == "image_generation" {
		sections = appendSection(sections, reasoningSection(item))
	}
	sections = appendSection(sections, capacitySection(item))
	sections = appendSection(sections, sourceConversionSection(item))
	return sections
}

func priceEntry(key, label string, value *float64, format string) catalogDisplayItem {
	entry := catalogDisplayItem{Key: key, Label: label, Format: format}
	if value != nil {
		entry.Value = *value
	}
	return entry
}

// priceSection drops the undefined entries (Node flatMap over
// value === undefined) and returns nil for empty sections.
func priceSection(key, label string, entries []catalogDisplayItem) *catalogDisplaySection {
	items := make([]catalogDisplayItem, 0, len(entries))
	for _, entry := range entries {
		if entry.Value == nil {
			continue
		}
		items = append(items, entry)
	}
	if len(items) == 0 {
		return nil
	}
	return &catalogDisplaySection{Key: key, Label: label, Items: items}
}

// textSection drops undefined and empty-string values.
func textSection(key, label string, entries []catalogDisplayItem) *catalogDisplaySection {
	items := make([]catalogDisplayItem, 0, len(entries))
	for _, entry := range entries {
		if entry.Value == nil {
			continue
		}
		if text, ok := entry.Value.(string); ok && text == "" {
			continue
		}
		items = append(items, entry)
	}
	if len(items) == 0 {
		return nil
	}
	return &catalogDisplaySection{Key: key, Label: label, Items: items}
}

func imageUnitSection(item *ModelCatalogItem) *catalogDisplaySection {
	return priceSection("image_generation", "图片生成", []catalogDisplayItem{
		priceEntry("output_image", "每张", item.OutputUsdPerImage, "usd_per_image"),
	})
}

func capacitySection(item *ModelCatalogItem) *catalogDisplaySection {
	return textSection("capacity", "容量", []catalogDisplayItem{
		textEntry("context", "上下文", item.ContextWindowTokens),
		textEntry("max_input", "最大输入", item.MaxInputTokens),
		textEntry("max_output", "最大输出", item.MaxOutputTokens),
	})
}

func textEntry(key, label string, value *int64) catalogDisplayItem {
	entry := catalogDisplayItem{Key: key, Label: label, Format: "tokens"}
	if value != nil {
		entry.Value = *value
	}
	return entry
}

func reasoningSection(item *ModelCatalogItem) *catalogDisplaySection {
	efforts := make([]string, 0, len(item.SupportedReasoningEfforts))
	for _, effort := range item.SupportedReasoningEfforts {
		if effort == "" || effort == "none" {
			continue
		}
		efforts = append(efforts, effort)
	}
	if len(efforts) == 0 {
		return nil
	}
	return textSection("reasoning", "思考能力", []catalogDisplayItem{{
		Key: "levels", Label: "级别", Value: strings.Join(efforts, " / "), Format: "text",
	}})
}

func longContextSection(item *ModelCatalogItem) *catalogDisplaySection {
	if item.LongContextInputTokenThreshold == nil {
		return nil
	}
	thresholdLabel := "触发阈值（不含）"
	if item.LongContextInputTokenThresholdInclusive != nil && *item.LongContextInputTokenThresholdInclusive {
		thresholdLabel = "触发阈值（含）"
	}
	return textSection("long_context", "长上下文计费", []catalogDisplayItem{
		{Key: "threshold", Label: thresholdLabel, Value: *item.LongContextInputTokenThreshold, Format: "tokens"},
		multiplierEntry("input_multiplier", "输入倍率", item.LongContextInputCostMultiplier),
		multiplierEntry("output_multiplier", "输出倍率", item.LongContextOutputCostMultiplier),
	})
}

func multiplierEntry(key, label string, value *float64) catalogDisplayItem {
	entry := catalogDisplayItem{Key: key, Label: label, Format: "multiplier"}
	if value != nil {
		entry.Value = *value
	}
	return entry
}

func serviceTierSections(item *ModelCatalogItem) []catalogDisplaySection {
	sections := []catalogDisplaySection{}
	for _, tier := range item.SupportedServiceTiers {
		rates, ok := item.ServiceTierPrices[tier]
		if !ok {
			continue
		}
		section := priceSection("tier_"+tier, tierLabel(tier), []catalogDisplayItem{
			priceEntry("input", "输入", rates.InputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_read", "缓存读", rates.CachedInputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_write", "缓存写入", rates.CacheWriteUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_write_1h", "1h 缓存写入", rates.CacheWrite1hUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("cache_storage", "缓存存储", rates.CacheStorageUsdPer1MPerHour, "usd_per_1m_token_hour"),
			priceEntry("output", "输出", rates.OutputUsdPer1M, "usd_per_1m_tokens"),
			priceEntry("audio_input", "音频输入", rates.AudioInputUsdPer1M, "usd_per_1m_tokens"),
		})
		if section != nil {
			sections = append(sections, *section)
		}
	}
	return sections
}

func sourceConversionSection(item *ModelCatalogItem) *catalogDisplaySection {
	if item.SourcePricingCurrency == "" || item.SourcePricingCurrency == "USD" {
		return nil
	}
	rate := ""
	if item.SourceExchangeRateToUsd != nil && *item.SourceExchangeRateToUsd > 0 {
		// SourceExchangeRateToUsd 语义是「1 USD = N 源币种」；展示同口径，
		// 不做倒数换算（1 CNY ≈ $0.1429 这类倒数值易被误读为报价精度）。
		rate = trimTrailingZeros(fixedNumber(*item.SourceExchangeRateToUsd, 4))
	}
	return textSection("currency_conversion", "美元换算", []catalogDisplayItem{
		{Key: "source_currency", Label: "官方币种", Value: item.SourcePricingCurrency, Format: "text"},
		{Key: "exchange_rate", Label: "1 USD 兑", Value: rate + " " + item.SourcePricingCurrency, Format: "text"},
		{Key: "exchange_rate_date", Label: "汇率日期", Value: item.SourceExchangeRateDate, Format: "text"},
		{Key: "source_note", Label: "官方源价", Value: item.SourcePricingNote, Format: "text"},
	})
}

func longContextTokenEntries(item *ModelCatalogItem, cacheReadLabel string) []catalogDisplayItem {
	if item.LongContextInputTokenThreshold == nil {
		return nil
	}
	threshold := *item.LongContextInputTokenThreshold
	comparison := ">"
	if item.LongContextInputTokenThresholdInclusive != nil && *item.LongContextInputTokenThresholdInclusive {
		comparison = ">="
	}
	prefix := "长上下文（" + comparison + " " + formatTokenThreshold(threshold) + "）"
	return []catalogDisplayItem{
		priceEntry("long_context_input", prefix+"输入", multipliedPrice(item.InputUsdPer1M, item.LongContextInputCostMultiplier), "usd_per_1m_tokens"),
		priceEntry("long_context_cache_read", prefix+cacheReadLabel, multipliedPrice(item.CachedInputUsdPer1M, item.LongContextInputCostMultiplier), "usd_per_1m_tokens"),
		priceEntry("long_context_output", prefix+"输出", multipliedPrice(item.OutputUsdPer1M, item.LongContextOutputCostMultiplier), "usd_per_1m_tokens"),
	}
}

func multipliedPrice(price, multiplier *float64) *float64 {
	if price == nil {
		return nil
	}
	factor := 1.0
	if multiplier != nil && *multiplier > 0 {
		factor = *multiplier
	}
	output := *price * factor
	return &output
}

// formatTokenThreshold mirrors formatTokenThreshold (1_000 → "1K").
func formatTokenThreshold(value int64) string {
	if value >= 1_000 && value%1_000 == 0 {
		return itoaInt64(value/1_000) + "K"
	}
	return itoaInt64(value)
}

func itoaInt64(value int64) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	negative := value < 0
	if negative {
		value = -value
	}
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	if negative {
		return "-" + digits
	}
	return digits
}

// tierLabel mirrors tierLabel.
func tierLabel(value string) string {
	switch value {
	case "priority":
		return "Priority"
	case "flex":
		return "Flex"
	case "batch":
		return "Batch API"
	}
	return value
}

// formatUsdRate mirrors formatUsdRate ($ + 8-digit fixed).
func formatUsdRate(value float64) string {
	return "$" + trimTrailingZeros(fixedNumber(value, 8))
}

func fixedNumber(value float64, digits int) string {
	shift := 1.0
	for index := 0; index < digits; index++ {
		shift *= 10
	}
	rounded := value * shift
	if rounded < 0 {
		rounded -= 0.5
	} else {
		rounded += 0.5
	}
	integer := int64(rounded)
	text := itoaInt64(integer)
	for len(text) <= digits {
		text = "0" + text
	}
	if digits == 0 {
		return text
	}
	return text[:len(text)-digits] + "." + text[len(text)-digits:]
}

func trimTrailingZeros(value string) string {
	if !strings.Contains(value, ".") {
		return value
	}
	value = strings.TrimRight(value, "0")
	return strings.TrimSuffix(value, ".")
}

// appendSection/appendSections drop nil/empty sections (Node compactSections).
func appendSection(dst []catalogDisplaySection, section *catalogDisplaySection) []catalogDisplaySection {
	if section != nil && len(section.Items) > 0 {
		return append(dst, *section)
	}
	return dst
}

func appendSections(dst []catalogDisplaySection, sections []catalogDisplaySection) []catalogDisplaySection {
	for index := range sections {
		dst = appendSection(dst, &sections[index])
	}
	return dst
}
