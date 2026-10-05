// Package pricing holds the static model pricing catalog ported from
// backend/src/modules/model-pricing/ (the *.data.ts snapshots). The package
// currently contains only:
//
//   - the six provider pricing snapshots (openai/gpt, anthropic, deepseek,
//     glm, gemini, xai) as immutable Go data with per-million conversions
//     bit-compatible with the Node snapshots,
//   - the shared pricing/billing types (PriceSet, Pricing, CostInput,
//     CostLineItem, CostBreakdown) mirrored from provider-billing.types.ts,
//   - the unexported shared helpers the later billing slice builds on
//     (per-million conversion, rounding, nil/NaN collapsing, catalog model
//     aliasing in catalog.go).
//
// Ported (T3 billing slice):
//
//   - the lookup closure (lookup.go): ListProviderModelPricing /
//     FindProviderModelPricing with the per-provider candidate loop and the
//     canonical OpenAI alias double fallback (model-pricing.service.ts
//     findProviderModelPricing), shutdown filtering and catalog ordering,
//   - the billing engine (billing.go): BuildCostBreakdown +
//     EstimateProviderCostUsd / EstimateProviderCacheWriteCostUsd /
//     EstimateProviderCacheReadCostUsd over the six per-provider policies
//     (provider-billing.{shared,policies,registry,service}.ts): tier
//     exact prices (priority/flex/batch), cache split into
//     input/cache_write/cache_write_1h/cache_read, long-context
//     multipliers, image/audio line items and the costUsd override.
//
// Deferred: the catalog display rendering (buildCatalogDisplay,
// presentation-only) and the account-custom catalog merge
// (model-catalog.service.ts, providers slice).
//
// The module exposes no HTTP routes on the Node side; the model catalog
// management surface (model-catalog.service.ts custom catalog + codex models)
// belongs to the providers slice. Catalog display rendering
// (buildProviderCatalogDisplay) is presentation-only and stays deferred.
package pricing

import (
	"math"
	"strconv"
)

// PriceSet mirrors provider-billing.types ProviderBillingPriceSet. nil means
// the Node undefined price; a non-nil 0 is a real free price (glm-4.7-flash).
// TtsInputUsdPer1MChars / AudioInputUsdPerSecond 是 M1 同步音频计费维度
// （音频设计 §10）：TTS 按输入字符（USD/1M chars）、STT 按音频秒（USD/s）；
// VideoOutputUsdPerSecond 是 M2 视频计费维度（媒体设计 §10）：视频按输出
// 秒（USD/s）；VideoOutputUsdPerCall 是视频按次计费维度（2026-10-05 用户
// 裁决，契约 §2.8）：按次上游单槽位（一次任务 = 一次调用），与秒价槽位
// 互斥（同一目录行两字段只落其一）。计费引擎行项消费由 billing 扩展另行
// 交付，本结构只承载目录单价。
type PriceSet struct {
	InputUsdPer1M               *float64
	OutputUsdPer1M              *float64
	CachedInputUsdPer1M         *float64
	CacheWriteUsdPer1M          *float64
	CacheWrite1hUsdPer1M        *float64
	CacheStorageUsdPer1MPerHour *float64
	ImageInputUsdPer1M          *float64
	ImageOutputUsdPer1M         *float64
	AudioInputUsdPer1M          *float64
	AudioOutputUsdPer1M         *float64
	OutputUsdPerImage           *float64
	TtsInputUsdPer1MChars       *float64
	AudioInputUsdPerSecond      *float64
	VideoOutputUsdPerSecond     *float64
	VideoOutputUsdPerCall       *float64
}

// ServiceTierPrices mirrors Record<string, ModelPriceSet> (priority/flex/batch).
type ServiceTierPrices map[string]PriceSet

// Pricing mirrors the ProviderModelPricing / ProviderBillingPricing merge the
// Node billing path consumes (model-pricing.service.ts
// toProviderModelPricing).
type Pricing struct {
	ProviderCode string
	Model        string
	Mode         string
	CatalogOrder *int
	ReleaseDate  string
	ShutdownDate string
	PriceSet

	CachedImageInputUsdPer1M *float64
	ServiceTierPrices        ServiceTierPrices

	SupportedAPIProtocols []string
	InputModalities       []string
	OutputModalities      []string
	// ResponseFormats 是 TTS 模型的对外 response_format 支持清单（音频设计
	// §4.1/契约 §5.1：openai tts 官方全集、gemini tts 仅 ["pcm"]，零转码）。
	ResponseFormats []string
	// SupportedToolsByProtocol 是「协议 × 工具」矩阵（6.4）：键为该行协议枚举，
	// 值为该协议下可用的工具集。一维 SupportedTools 已随阶段 2 退场。
	SupportedToolsByProtocol  map[string][]string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    string

	CodexSupportedReasoningLevels []string
	CodexDefaultReasoningLevel    string
	CodexMultiAgentVersion        string

	ContextWindowTokens *int
	MaxInputTokens      *int
	MaxOutputTokens     *int
	MaxTokens           *int

	LongContextInputTokenThreshold          *int
	LongContextInputTokenThresholdInclusive bool
	LongContextInputCostMultiplier          *float64
	LongContextOutputCostMultiplier         *float64

	SupportsPromptCaching bool
	SupportsServiceTier   bool
	CatalogVisible        bool

	SourcePricingCurrency   string
	SourceExchangeRateToUsd *float64
	SourceExchangeRateDate  string
	SourcePricingNote       string
	Source                  string
}

// CostInput mirrors ProviderBillingCostInput plus the costUsd override the
// CostBreakdownInput extension carries. nil = Node undefined.
// TtsInputChars / AudioInputSeconds 是 M1 同步音频计量维度（音频设计 §10）：
// TTS 请求输入字符数、STT 音频秒数，行项由 billing.go 的
// tts_input_chars / audio_input_seconds 消费。
// OutputVideoSeconds 是 M2 视频任务输出秒数（媒体设计 §10：任务终态由轮询
// 响应驱动，失败任务不虚计），行项由 billing.go 的 video_output_seconds 消费。
// VideoCalls 是视频按次计量（2026-10-05 按次计费批，契约 §2.8）：按次上游
// 一次任务 = 一次调用（结算链按任务粒度填 1），行项由 billing.go 的
// video_output_calls 消费；与 OutputVideoSeconds 是互斥维度（同一目录行只
// 落秒价/按次价其一，按各自槽位计价）。
type CostInput struct {
	ProviderCode string
	Model        string
	ServiceTier  string

	InputTokens        *float64
	OutputTokens       *float64
	CacheReadTokens    *float64
	CacheWriteTokens   *float64
	CacheWrite1hTokens *float64
	ThinkingTokens     *float64
	InputImageTokens   *float64
	OutputImageTokens  *float64
	InputAudioTokens   *float64
	OutputAudioTokens  *float64
	OutputImageCount   *float64
	TtsInputChars      *float64
	AudioInputSeconds  *float64
	OutputVideoSeconds *float64
	VideoCalls         *float64
	CostUsd            *float64
}

// CostLineKind mirrors ProviderCostLineKind.
type CostLineKind string

const (
	LineInput        CostLineKind = "input"
	LineOutput       CostLineKind = "output"
	LineCacheRead    CostLineKind = "cache_read"
	LineCacheWrite   CostLineKind = "cache_write"
	LineCacheWrite1h CostLineKind = "cache_write_1h"
	LineImageInput   CostLineKind = "image_input"
	LineImageOutput  CostLineKind = "image_output"
	LineAudioInput   CostLineKind = "audio_input"
	LineAudioOutput  CostLineKind = "audio_output"
	LineImageOutUnit CostLineKind = "image_output_unit"
	LineOther        CostLineKind = "other"
	// M1 同步音频行项（音频设计 §10）：TTS 按输入字符、STT 按音频秒。
	// 行项生成（billing.go 消费）由计费扩展另行交付。
	CostLineKindTtsInputChars     CostLineKind = "tts_input_chars"
	CostLineKindAudioInputSeconds CostLineKind = "audio_input_seconds"
	// M2 视频行项（媒体设计 §10）：视频按输出秒计费（video_output_seconds）。
	// 行项生成由 billing.go 消费（终态计费任务交付），本枚举先落词表。
	CostLineKindVideoOutputSeconds CostLineKind = "video_output_seconds"
	// CostLineKindVideoOutputCalls 是视频按次行项（2026-10-05 按次计费批，
	// 契约 §2.8）：按次上游一次任务 = 一次调用，成本 = 次价 × 次数；与秒价
	// 行项互斥（同一目录行两槽位只落其一，最多产出其一）。
	CostLineKindVideoOutputCalls CostLineKind = "video_output_calls"
	LineUnitToken                             = "token"
	LineUnitImage                             = "image"
	// LineUnitChar / LineUnitSecond 是 M1 音频行项的计量单位（tts_input_chars
	// 按 1M 字符计价、audio_input_seconds 按秒计价）；video_output_seconds
	// 同按秒计价（M2），复用 LineUnitSecond；LineUnitCall 是视频按次行项的
	// 计量单位（video_output_calls，一次任务 = 一次调用）。
	LineUnitChar   = "char"
	LineUnitSecond = "second"
	LineUnitCall   = "call"
	tokenUnitSize  = 1_000_000.0
)

// CostLineItem mirrors ProviderCostLineItem.
type CostLineItem struct {
	Key          string
	Kind         CostLineKind
	Label        string
	Quantity     float64
	Unit         string
	UnitSize     float64
	UnitPriceUsd float64
	CostUsd      float64
}

// ServiceTierPricingSource mirrors the ProviderCostBreakdown discriminant.
type ServiceTierPricingSource string

const (
	TierSourceDefault      ServiceTierPricingSource = "default"
	TierSourceTierSpecific ServiceTierPricingSource = "tier_specific"
	TierSourceMultiplier   ServiceTierPricingSource = "multiplier"
	TierSourceMixed        ServiceTierPricingSource = "mixed"
	TierSourceUnknown      ServiceTierPricingSource = "unknown"
)

// CostBreakdown mirrors ProviderCostBreakdown. Missing optional cost/rate
// fields stay nil (Node undefined).
type CostBreakdown struct {
	Currency      string // "USD" in the Node ProviderCostBreakdown output
	BillingPolicy string // provider billing policy id in the Node output
	LineItems     []CostLineItem

	InputCostUsd        *float64
	OutputCostUsd       *float64
	InputUsdPer1M       *float64
	OutputUsdPer1M      *float64
	CacheReadCostUsd    *float64
	CacheReadUsdPer1M   *float64
	CacheWriteCostUsd   *float64
	CacheWriteUsdPer1M  *float64
	CacheWrite1hCostUsd *float64

	CacheWrite1hUsdPer1M *float64
	ThinkingTokens       *float64

	InputImageCostUsd      *float64
	OutputImageCostUsd     *float64
	InputImageUsdPer1M     *float64
	OutputImageUsdPer1M    *float64
	InputAudioCostUsd      *float64
	OutputAudioCostUsd     *float64
	InputAudioUsdPer1M     *float64
	OutputAudioUsdPer1M    *float64
	OutputImageUnitCostUsd *float64
	OutputUsdPerImage      *float64

	AccountChargeUsd *float64
	Multiplier       float64 // always 1; Node never produces another value

	ServiceTierPricingSource ServiceTierPricingSource
	ServiceTierMultiplier    *float64 // never set: no generic tier multiplier exists
}

// f64p allocates an optional float64 (test/data helper).
func f64p(v float64) *float64 { return &v }

// intp allocates an optional int (test/data helper).
func intp(v int) *int { return &v }

// nonNegative mirrors provider-billing.shared nonNegative: undefined/NaN/Inf
// collapse to 0, negatives clamp to 0.
func nonNegative(v *float64) float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return 0
	}
	return math.Max(*v, 0)
}

// finite mirrors provider-billing.shared finite: undefined/NaN/Inf/negative
// collapse to undefined.
func finite(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 {
		return nil
	}
	return v
}

// validMultiplier mirrors provider-billing.shared validMultiplier.
func validMultiplier(v *float64) float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v <= 0 {
		return 1
	}
	return *v
}

// multiplyRate mirrors multiply: undefined stays undefined.
func multiplyRate(v *float64, multiplier float64) *float64 {
	if v == nil {
		return nil
	}
	out := *v * multiplier
	return &out
}

// roundCost mirrors roundCost: Number(value.toFixed(10)).
func roundCost(v float64) float64 {
	return fixedNumber(v, 10)
}

// fixedNumber mirrors Number(value.toFixed(digits)).
func fixedNumber(v float64, digits int) float64 {
	out, err := strconv.ParseFloat(strconv.FormatFloat(v, 'f', digits, 64), 64)
	if err != nil {
		return v
	}
	return out
}

// perMillion mirrors model-pricing.service perMillion:
// Number((price * 1_000_000).toFixed(8)), undefined-safe.
func perMillion(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	out := fixedNumber(*v*1_000_000, 8)
	return &out
}

// sumOptionalCosts mirrors sumOptionalCosts: sum defined parts and round;
// no defined part stays undefined.
func sumOptionalCosts(parts ...*float64) *float64 {
	total := 0.0
	any := false
	for _, part := range parts {
		if part == nil {
			continue
		}
		any = true
		total += *part
	}
	if !any {
		return nil
	}
	out := roundCost(total)
	return &out
}

// hasAnyRate mirrors hasAnyRate: at least one finite rate must exist.
// M1 音频单价（TtsInputUsdPer1MChars / AudioInputUsdPerSecond）、M2 视频单价
// （VideoOutputUsdPerSecond）与按次视频单价（VideoOutputUsdPerCall，2026-10-05
// 按次计费批）计入：纯 TTS/STT/视频模型可能只携带这些单价
// （tts-1 / whisper-1 / cogvideox 系）。
func hasAnyRate(set PriceSet) bool {
	for _, v := range []*float64{
		set.InputUsdPer1M, set.OutputUsdPer1M, set.CachedInputUsdPer1M,
		set.CacheWriteUsdPer1M, set.CacheWrite1hUsdPer1M,
		set.CacheStorageUsdPer1MPerHour, set.ImageInputUsdPer1M,
		set.ImageOutputUsdPer1M, set.AudioInputUsdPer1M,
		set.AudioOutputUsdPer1M, set.OutputUsdPerImage,
		set.TtsInputUsdPer1MChars, set.AudioInputUsdPerSecond,
		set.VideoOutputUsdPerSecond, set.VideoOutputUsdPerCall,
	} {
		if v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) {
			return true
		}
	}
	return false
}

// hasAnyCostDimension mirrors hasAnyCostDimension. M1 音频计量维度、M2 视频
// 秒计量维度与视频按次计量维度（VideoCalls，2026-10-05 按次计费批）同样计入
// （TTS 请求只有字符计量、视频任务只有秒/次计量，无 token 计量）。
func hasAnyCostDimension(input CostInput) bool {
	return input.InputTokens != nil || input.OutputTokens != nil ||
		input.CacheReadTokens != nil || input.CacheWriteTokens != nil ||
		input.CacheWrite1hTokens != nil || input.InputImageTokens != nil ||
		input.OutputImageTokens != nil || input.InputAudioTokens != nil ||
		input.OutputAudioTokens != nil || input.OutputImageCount != nil ||
		input.TtsInputChars != nil || input.AudioInputSeconds != nil ||
		input.OutputVideoSeconds != nil || input.VideoCalls != nil
}
