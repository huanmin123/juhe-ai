package pricing

// Provider billing engine, ported from
// backend/src/modules/model-pricing/provider-billing.{types,shared,policies,
// registry,service}.ts:
//
//   - BuildCostBreakdown        = buildProviderBillingCostBreakdown
//   - EstimateProviderCostUsd   = estimateProviderCostUsd
//   - EstimateProviderCacheWriteCostUsd / EstimateProviderCacheReadCostUsd
//   - per-provider policies (openai/anthropic/gemini/xai/deepseek/glm)
//     with tier-specific rates (priority/flex/batch), cache split into
//     input/cache_write/cache_write_1h/cache_read, long-context
//     multipliers, image/audio line items and the costUsd override.
//
// The catalog display surface (buildCatalogDisplay /
// buildProviderCatalogDisplay) is presentation-only and stays deferred, so
// ProviderBillingPolicy.buildCatalogDisplay has no counterpart here.

import (
	"math"
	"sort"
	"strings"
)

// tokenBillingLabels mirrors provider-billing.shared TokenBillingLabels.
type tokenBillingLabels struct {
	input           string
	output          string
	cacheRead       string
	cacheWrite      string
	cacheWrite1h    string
	imageInput      string
	imageOutput     string
	audioInput      string
	audioOutput     string
	imageOutputUnit string
	imageInputUnit  string
	// ttsInputChars / audioInputSeconds 是 M1 音频行项标签（音频设计 §10）；
	// videoOutputSeconds 是 M2 视频行项标签（媒体设计 §10）；
	// videoOutputCalls 是视频按次行项标签（2026-10-05 按次计费批，契约 §2.8）。
	ttsInputChars string
	// ttsOutputCalls 是 token 口径 TTS 无 usage 时的按次固定价行项标签
	//（2026-11-12 用户裁决：官方按 token、上游不回报 → 按次固定价，不空着）。
	ttsOutputCalls string
	// audioInputSeconds 是音频秒行项标签（音频设计 §10）：承载 STT 输入秒
	// 与 volcengine TTS 生成秒（2026-10-17 维度修正批）——同一 usage 行只
	// 会是其一，费率按模型目录无混算。
	audioInputSeconds  string
	videoOutputSeconds string
	videoOutputCalls   string
}

// defaultTokenBillingLabels mirrors defaultTokenBillingLabels.
var defaultTokenBillingLabels = tokenBillingLabels{
	input:              "输入 Token",
	output:             "输出 Token",
	cacheRead:          "缓存读 Token",
	cacheWrite:         "缓存写入 Token",
	cacheWrite1h:       "1h 缓存写入 Token",
	imageInput:         "图片输入 Token",
	imageOutput:        "图片输出 Token",
	audioInput:         "音频输入 Token",
	audioOutput:        "音频输出 Token",
	imageOutputUnit:    "输出图片",
	ttsInputChars:      "TTS 输入字符",
	audioInputSeconds:  "音频秒",
	videoOutputSeconds: "视频输出秒",
	videoOutputCalls:   "视频输出次数",
}

// tokenBillingOptions mirrors provider-billing.shared TokenBillingOptions.
//
// cacheWriteIncludedInInput：上游把 cache_write_tokens 计入 input_tokens 总额
// （OpenAI 兼容族语义）时，普通 input 行项必须先扣除缓存写入部分再计价，否则
// 同一批 token 被「输入价 + 缓存写入价」各计一次（BUG-0347）。Anthropic 的
// cache_creation_input_tokens 独立于 input_tokens，保持 false。
type tokenBillingOptions struct {
	cacheReadIncludedInInput  bool
	cacheWriteIncludedInInput bool
	cacheReadFallbackToInput  bool
	labels                    tokenBillingLabels
}

// resolvedRates mirrors provider-billing.types ResolvedProviderBillingRates.
type resolvedRates struct {
	PriceSet
	serviceTierPricingSource ServiceTierPricingSource
	serviceTierMultiplier    *float64
}

// billingPolicy mirrors the per-provider buildCostBreakdown differences.
// The display half of the Node ProviderBillingPolicy interface is not
// ported (presentation-only).
type billingPolicy struct {
	id                        string
	applyLongContext          bool
	cacheReadIncludedInInput  bool
	cacheWriteIncludedInInput bool
	cacheReadFallbackToInput  bool
	labels                    tokenBillingLabels
}

// billingPolicies mirrors the six provider-billing.policies.ts policies.
var billingPolicies = []*billingPolicy{
	{
		id:                        "openai",
		applyLongContext:          true,
		cacheReadIncludedInInput:  true,
		cacheWriteIncludedInInput: true,
		cacheReadFallbackToInput:  true,
		labels:                    defaultTokenBillingLabels,
	},
	{
		// anthropic 的 cache_creation_input_tokens 独立于 input_tokens，
		// 缓存读取/写入都不从 input 中扣除（cacheReadIncludedInInput 与
		// cacheWriteIncludedInInput 同为 false）。
		id: "anthropic",
		labels: tokenBillingLabels{
			input:           "输入 Token",
			output:          "输出 Token",
			cacheRead:       "缓存读 Token",
			cacheWrite:      "5m 缓存写入 Token",
			cacheWrite1h:    "1h 缓存写入 Token",
			imageInput:      "图片输入 Token",
			imageOutput:     "图片输出 Token",
			audioInput:      "音频输入 Token",
			audioOutput:     "音频输出 Token",
			imageOutputUnit: "输出图片",
			// M1 音频行项标签随默认集（音频设计 §10）；各 policy 不单独定制。
			ttsInputChars:      defaultTokenBillingLabels.ttsInputChars,
			ttsOutputCalls:     defaultTokenBillingLabels.ttsOutputCalls,
			audioInputSeconds:  defaultTokenBillingLabels.audioInputSeconds,
			videoOutputSeconds: defaultTokenBillingLabels.videoOutputSeconds,
			videoOutputCalls:   defaultTokenBillingLabels.videoOutputCalls,
		},
	},
	{
		id:                        "gemini",
		applyLongContext:          true,
		cacheReadIncludedInInput:  true,
		cacheWriteIncludedInInput: true,
		labels: tokenBillingLabels{
			input:           "输入 Token",
			output:          "输出 Token",
			cacheRead:       "缓存读 Token",
			cacheWrite:      "缓存写入 Token",
			cacheWrite1h:    "1h 缓存写入 Token",
			imageInput:      "图片输入 Token",
			imageOutput:     "图片输出 Token",
			audioInput:      "音频输入 Token",
			audioOutput:     "音频输出 Token",
			imageOutputUnit: "输出图片",
			// M1 音频行项标签随默认集（音频设计 §10）；各 policy 不单独定制。
			ttsInputChars:      defaultTokenBillingLabels.ttsInputChars,
			ttsOutputCalls:     defaultTokenBillingLabels.ttsOutputCalls,
			audioInputSeconds:  defaultTokenBillingLabels.audioInputSeconds,
			videoOutputSeconds: defaultTokenBillingLabels.videoOutputSeconds,
			videoOutputCalls:   defaultTokenBillingLabels.videoOutputCalls,
		},
	},
	{
		id:                        "xai",
		applyLongContext:          true,
		cacheReadIncludedInInput:  true,
		cacheWriteIncludedInInput: true,
		labels:                    defaultTokenBillingLabels,
	},
	{
		id:                        "deepseek",
		cacheReadIncludedInInput:  true,
		cacheWriteIncludedInInput: true,
		labels: tokenBillingLabels{
			input:           "输入 Token",
			output:          "输出 Token",
			cacheRead:       "缓存读 Token",
			cacheWrite:      "缓存写入 Token",
			cacheWrite1h:    "1h 缓存写入 Token",
			imageInput:      "图片输入 Token",
			imageOutput:     "图片输出 Token",
			audioInput:      "音频输入 Token",
			audioOutput:     "音频输出 Token",
			imageOutputUnit: "输出图片",
			// M1 音频行项标签随默认集（音频设计 §10）；各 policy 不单独定制。
			ttsInputChars:      defaultTokenBillingLabels.ttsInputChars,
			ttsOutputCalls:     defaultTokenBillingLabels.ttsOutputCalls,
			audioInputSeconds:  defaultTokenBillingLabels.audioInputSeconds,
			videoOutputSeconds: defaultTokenBillingLabels.videoOutputSeconds,
			videoOutputCalls:   defaultTokenBillingLabels.videoOutputCalls,
		},
	},
	{
		id:                        "glm",
		cacheReadIncludedInInput:  true,
		cacheWriteIncludedInInput: true,
		labels: tokenBillingLabels{
			input:           "输入 Token",
			output:          "输出 Token",
			cacheRead:       "缓存读 Token",
			cacheWrite:      "缓存写入 Token",
			cacheWrite1h:    "1h 缓存写入 Token",
			imageInput:      "图片输入 Token",
			imageOutput:     "图片输出 Token",
			audioInput:      "音频输入 Token",
			audioOutput:     "音频输出 Token",
			imageOutputUnit: "输出图片",
			// M1 音频行项标签随默认集（音频设计 §10）；各 policy 不单独定制。
			ttsInputChars:      defaultTokenBillingLabels.ttsInputChars,
			ttsOutputCalls:     defaultTokenBillingLabels.ttsOutputCalls,
			audioInputSeconds:  defaultTokenBillingLabels.audioInputSeconds,
			videoOutputSeconds: defaultTokenBillingLabels.videoOutputSeconds,
			videoOutputCalls:   defaultTokenBillingLabels.videoOutputCalls,
		},
	},
	{
		// M3 媒体供应商新增（媒体设计 §9/契约 §8）：MiniMax 只有媒体行项
		//（tts_input_chars / video_output_seconds，目录未落价——见
		// data_minimax.go 计价落法注释），token 行项标签沿用默认集。
		id:                        "minimax",
		cacheReadIncludedInInput:  true,
		cacheWriteIncludedInInput: true,
		labels:                    defaultTokenBillingLabels,
	},
	{
		// M3 媒体供应商新增（媒体设计 §9/契约 §9）：火山方舟只有媒体行项
		//（video_output_seconds——2026-10-17 国内价口径批起 720p 主档秒价
		// 已落值，1-0-pro 除外；seed-audio-1.0 音频秒行同批落值），token
		// 行项标签沿用默认集。
		id:                        "volcengine",
		cacheReadIncludedInInput:  true,
		cacheWriteIncludedInInput: true,
		labels:                    defaultTokenBillingLabels,
	},
	{
		// M3 媒体供应商新增（媒体设计 §9/契约 §10）：通义百炼只有媒体行项
		//（video_output_seconds，目录未落价——见 data_qwen.go 计价落法注释，
		// usage 秒计量照抽、成本不虚计），token 行项标签沿用默认集。
		id:                        "qwen",
		cacheReadIncludedInInput:  true,
		cacheWriteIncludedInInput: true,
		labels:                    defaultTokenBillingLabels,
	},
}

// billingPolicyForProvider mirrors providerBillingPolicyForProvider
// (provider-billing.registry.ts): the driver registry resolves the
// provider token, the policy rides on the driver.
func billingPolicyForProvider(providerCode string) *billingPolicy {
	entry := providerEntryFor(providerCode)
	if entry == nil {
		return nil
	}
	for _, policy := range billingPolicies {
		if policy.id == entry.billingPolicy {
			return policy
		}
	}
	return nil
}

// BuildCostBreakdown mirrors buildProviderBillingCostBreakdown: the policy
// is selected by pricing.ProviderCode (never the input's), and the Node
// output always stamps currency 'USD' plus the policy id. nil mirrors the
// Node undefined breakdown (unknown provider, unsupported tier or unpriced
// usage).
func BuildCostBreakdown(pricing *Pricing, input CostInput) *CostBreakdown {
	if pricing == nil {
		return nil
	}
	policy := billingPolicyForProvider(pricing.ProviderCode)
	if policy == nil {
		return nil
	}
	breakdown := policy.buildCostBreakdown(pricing, input)
	if breakdown == nil {
		return nil
	}
	breakdown.Currency = "USD"
	breakdown.BillingPolicy = policy.id
	return breakdown
}

// buildCostBreakdown mirrors the per-provider policy bodies in
// provider-billing.policies.ts.
//
// T93-3（C3-H4）：原 `rejectUnsupportedTier` 前置门（anthropic/deepseek/glm/
// minimax/volcengine/qwen）在客户端声明任意档位时直接返回 nil，使整单 cost
// 落 NULL。该语义已废止——未知档位统一由 serviceTierRates 回退标准档单价，
// 档位来源保留 unknown 标记；provider 策略不再因档位拒绝计价。
func (p *billingPolicy) buildCostBreakdown(pricing *Pricing, input CostInput) *CostBreakdown {
	rates := serviceTierRates(pricing, input)
	if p.applyLongContext {
		rates = applyLongContextRates(pricing, input, rates)
	}
	billable := input
	if pricing.Mode == "image_generation" &&
		rates.ImageOutputUsdPer1M != nil && input.OutputImageTokens == nil {
		// OpenAI image policy: the text output stream is billed as image
		// output tokens unless the caller separates them.
		billable.OutputImageTokens = input.OutputTokens
	}
	if pricing.Mode == "image_generation" &&
		rates.OutputUsdPerImage != nil &&
		rates.OutputUsdPer1M == nil && rates.ImageOutputUsdPer1M == nil &&
		input.OutputTokens != nil {
		// 按张计价策略（seedream/image-01/cogview/grok-imagine-image 族，目录
		// 槽位 OutputUsdPerImage）：上游 usage.output_tokens 是图像内部 token
		// 核算（生成尺寸折算），行上没有独立的文本输出 token 价——置零避免
		// hasUnpricedUsage 以"未定价输出 token"拒绝整单计费。2026-10-17 实弹
		// 取证：volcengine seedream flash output_tokens=4096 使按张计费落 0 成本。
		billable.OutputTokens = nil
	}
	return buildTokenCostBreakdown(pricing, billable, rates, tokenBillingOptions{
		cacheReadIncludedInInput:  p.cacheReadIncludedInInput,
		cacheWriteIncludedInInput: p.cacheWriteIncludedInInput,
		cacheReadFallbackToInput:  p.cacheReadFallbackToInput,
		labels:                    p.labels,
	})
}

// EstimateProviderCostUsd mirrors estimateProviderCostUsd: resolve the
// model over the built-in snapshot and return accountChargeUsd.
func EstimateProviderCostUsd(input CostInput) *float64 {
	breakdown := estimateProviderBreakdown(input)
	if breakdown == nil || breakdown.AccountChargeUsd == nil {
		return nil
	}
	return breakdown.AccountChargeUsd
}

// estimateProviderBreakdown mirrors the staticPricingBreakdown helper: the
// model must exist in the built-in snapshot for the input's provider.
func estimateProviderBreakdown(input CostInput) *CostBreakdown {
	if input.Model == "" || !hasAnyCostDimension(input) {
		return nil
	}
	pricing := FindProviderModelPricing(input.ProviderCode, input.Model)
	if pricing == nil {
		return nil
	}
	return BuildCostBreakdown(pricing, input)
}

// EstimateProviderCacheWriteCostUsd mirrors estimateProviderCacheWriteCostUsd.
func EstimateProviderCacheWriteCostUsd(input CostInput) *float64 {
	if input.Model == "" || (input.CacheWriteTokens == nil && input.CacheWrite1hTokens == nil) {
		return nil
	}
	breakdown := estimateProviderBreakdown(input)
	if breakdown == nil {
		return nil
	}
	if total := sumOptionalCosts(breakdown.CacheWriteCostUsd, breakdown.CacheWrite1hCostUsd); total != nil {
		return total
	}
	if (input.CacheWriteTokens == nil || *input.CacheWriteTokens == 0) &&
		(input.CacheWrite1hTokens == nil || *input.CacheWrite1hTokens == 0) &&
		(breakdown.CacheWriteUsdPer1M != nil || breakdown.CacheWrite1hUsdPer1M != nil) {
		zero := 0.0
		return &zero
	}
	return nil
}

// EstimateProviderCacheReadCostUsd mirrors estimateProviderCacheReadCostUsd.
func EstimateProviderCacheReadCostUsd(input CostInput) *float64 {
	if input.Model == "" || input.CacheReadTokens == nil {
		return nil
	}
	breakdown := estimateProviderBreakdown(input)
	if breakdown == nil || breakdown.CacheReadUsdPer1M == nil {
		return nil
	}
	if breakdown.CacheReadCostUsd != nil {
		return breakdown.CacheReadCostUsd
	}
	zero := 0.0
	return &zero
}

// standardRates mirrors provider-billing.shared standardRates.
func standardRates(pricing *Pricing) resolvedRates {
	return resolvedRates{
		PriceSet:                 directRates(pricing.PriceSet),
		serviceTierPricingSource: TierSourceDefault,
	}
}

// standardTierFallback 是 T93-3（C3-H4）裁决的落点：未知档位（不在
// SupportedServiceTiers）或声明了但目录无该档位价时，回退标准档单价继续按
// 正常维度结算，档位来源保留 unknown 标记。返回空价集会让整单 cost 落
// NULL（免费用量），客户端任意 service_tier token 不得触发该后果。
func standardTierFallback(pricing *Pricing) resolvedRates {
	rates := standardRates(pricing)
	rates.serviceTierPricingSource = TierSourceUnknown
	return rates
}

// serviceTierRates mirrors provider-billing.shared serviceTierRates: the
// tier table owns the token rates (no standard-rate fallback for
// input/output/cache), while image/audio/per-image rates fall back to the
// standard modal rates.
//
// 未知/无价档位走 standardTierFallback（T93-3）；支持档位仍用档位价。
func serviceTierRates(pricing *Pricing, input CostInput) resolvedRates {
	tier := normalizedTier(input.ServiceTier)
	if tier == "" {
		return standardRates(pricing)
	}
	supported := false
	for _, supportedTier := range pricing.SupportedServiceTiers {
		if supportedTier == tier {
			supported = true
			break
		}
	}
	if !supported {
		return standardTierFallback(pricing)
	}
	tierRates, ok := pricing.ServiceTierPrices[tier]
	if !ok {
		return standardTierFallback(pricing)
	}
	resolved := directRates(tierRates)
	if resolved.ImageInputUsdPer1M == nil {
		resolved.ImageInputUsdPer1M = finite(pricing.ImageInputUsdPer1M)
	}
	if resolved.ImageOutputUsdPer1M == nil {
		resolved.ImageOutputUsdPer1M = finite(pricing.ImageOutputUsdPer1M)
	}
	if resolved.AudioInputUsdPer1M == nil {
		resolved.AudioInputUsdPer1M = finite(pricing.AudioInputUsdPer1M)
	}
	if resolved.AudioOutputUsdPer1M == nil {
		resolved.AudioOutputUsdPer1M = finite(pricing.AudioOutputUsdPer1M)
	}
	if resolved.OutputUsdPerImage == nil {
		resolved.OutputUsdPerImage = finite(pricing.OutputUsdPerImage)
	}
	if len(resolved.OutputUsdPerImageTiers) == 0 {
		resolved.OutputUsdPerImageTiers = pricing.OutputUsdPerImageTiers
	}
	if resolved.InputUsdPerImage == nil {
		resolved.InputUsdPerImage = finite(pricing.InputUsdPerImage)
	}
	if resolved.InputImageFreeCount == 0 {
		resolved.InputImageFreeCount = pricing.InputImageFreeCount
	}
	if resolved.TtsInputUsdPer1MChars == nil {
		resolved.TtsInputUsdPer1MChars = finite(pricing.TtsInputUsdPer1MChars)
	}
	if resolved.AudioInputUsdPerSecond == nil {
		resolved.AudioInputUsdPerSecond = finite(pricing.AudioInputUsdPerSecond)
	}
	if resolved.VideoOutputUsdPerSecond == nil {
		resolved.VideoOutputUsdPerSecond = finite(pricing.VideoOutputUsdPerSecond)
	}
	if len(resolved.VideoOutputUsdPerSecondByResolution) == 0 {
		resolved.VideoOutputUsdPerSecondByResolution = pricing.VideoOutputUsdPerSecondByResolution
	}
	if resolved.VideoOutputUsdPerCall == nil {
		resolved.VideoOutputUsdPerCall = finite(pricing.VideoOutputUsdPerCall)
	}
	if len(resolved.VideoOutputUsdPerCallBySeconds) == 0 {
		resolved.VideoOutputUsdPerCallBySeconds = pricing.VideoOutputUsdPerCallBySeconds
	}
	if len(resolved.VideoOutputUsdPerCallByResolutionSeconds) == 0 {
		resolved.VideoOutputUsdPerCallByResolutionSeconds = pricing.VideoOutputUsdPerCallByResolutionSeconds
	}
	source, multiplier := tierPricingMetadata(pricing.PriceSet, tierRates)
	return resolvedRates{
		PriceSet:                 resolved,
		serviceTierPricingSource: source,
		serviceTierMultiplier:    multiplier,
	}
}

// applyLongContextRates mirrors provider-billing.shared applyLongContextRates.
func applyLongContextRates(pricing *Pricing, input CostInput, rates resolvedRates) resolvedRates {
	threshold := pricing.LongContextInputTokenThreshold
	if threshold == nil {
		return rates
	}
	inputTokens := nonNegative(input.InputTokens)
	applies := inputTokens >= float64(*threshold)
	if !pricing.LongContextInputTokenThresholdInclusive {
		applies = inputTokens > float64(*threshold)
	}
	if !applies {
		return rates
	}
	inputMultiplier := validMultiplier(pricing.LongContextInputCostMultiplier)
	outputMultiplier := validMultiplier(pricing.LongContextOutputCostMultiplier)
	// Node spreads {...rates} and only overrides the five token rates; the
	// tier pricing source and multiplier carry through unchanged.
	rates.InputUsdPer1M = multiplyRate(rates.InputUsdPer1M, inputMultiplier)
	rates.CachedInputUsdPer1M = multiplyRate(rates.CachedInputUsdPer1M, inputMultiplier)
	rates.CacheWriteUsdPer1M = multiplyRate(rates.CacheWriteUsdPer1M, inputMultiplier)
	rates.CacheWrite1hUsdPer1M = multiplyRate(rates.CacheWrite1hUsdPer1M, inputMultiplier)
	rates.OutputUsdPer1M = multiplyRate(rates.OutputUsdPer1M, outputMultiplier)
	return rates
}

// buildTokenCostBreakdown mirrors provider-billing.shared
// buildTokenCostBreakdown.
func buildTokenCostBreakdown(pricing *Pricing, input CostInput, rates resolvedRates, options tokenBillingOptions) *CostBreakdown {
	if !hasAnyRate(rates.PriceSet) {
		return nil
	}

	cacheReadTokens := nonNegative(input.CacheReadTokens)
	cacheWriteTokens := nonNegative(input.CacheWriteTokens)
	cacheWrite1hTotal := cacheWriteTokens
	if cacheWrite1hTotal == 0 {
		cacheWrite1hTotal = nonNegative(input.CacheWrite1hTokens)
	}
	cacheWrite1hTokens := math.Min(nonNegative(input.CacheWrite1hTokens), cacheWrite1hTotal)
	cacheWriteStandardTokens := math.Max(cacheWriteTokens-cacheWrite1hTokens, 0)
	inputImageTokens := 0.0
	if rates.ImageInputUsdPer1M != nil {
		inputImageTokens = nonNegative(input.InputImageTokens)
	}
	outputImageTokens := 0.0
	if rates.ImageOutputUsdPer1M != nil {
		outputImageTokens = nonNegative(input.OutputImageTokens)
	}
	inputAudioTokens := 0.0
	if rates.AudioInputUsdPer1M != nil {
		inputAudioTokens = nonNegative(input.InputAudioTokens)
	}
	outputAudioTokens := 0.0
	if rates.AudioOutputUsdPer1M != nil {
		outputAudioTokens = nonNegative(input.OutputAudioTokens)
	}
	// 按张价选档（2026-11-12 档位批）：目录带像素档表且请求给出输出像素时
	// 按档位价；档位不命中 → 无按张价（不回退主档）；像素未知 → 主档价。
	outputImageRate := rates.OutputUsdPerImage
	if len(rates.OutputUsdPerImageTiers) > 0 && input.OutputImagePixels != nil {
		outputImageRate = selectImageTierRate(rates.OutputUsdPerImageTiers, *input.OutputImagePixels)
	}
	outputImageCount := 0.0
	if outputImageRate != nil {
		outputImageCount = nonNegative(input.OutputImageCount)
	}
	// 张数计量在图像模型缺按张价时的裁决：token 计价的图像行（gpt-image 族，
	// Mode=image_generation 且 ImageOutputUsdPer1M 有价）由 image token 行项
	// 承接，data[] 张数只是可选计量，不得因缺按张价把上游 200 的成功出图判成
	// 零计费（2026-11-06 dev 复现：gpt-image-2 返回 1 张 + 343 image tokens，
	// costUsd 为 NULL）。非图像模型或完全没有图像 token 价的配置仍按
	// “未定价张数”拒绝整单（既有契约测试 gpt-5.5 合成张数场景）。
	if nonNegative(input.OutputImageCount) > 0 && outputImageRate == nil {
		if !(pricing.Mode == "image_generation" && rates.ImageOutputUsdPer1M != nil) {
			return nil
		}
	}
	cacheReadRate := rates.CachedInputUsdPer1M
	if cacheReadRate == nil && options.cacheReadFallbackToInput {
		cacheReadRate = rates.InputUsdPer1M
	}
	// 未缓存输入 = 输入总额 −（计入 input 的）缓存读取 −（计入 input 的）
	// 缓存写入 − 已单列的图片/音频输入。cacheWrite 扣的是写入总量
	// （cacheWriteStandard + cacheWrite1h 两行之和，含只在 1h 槽位出现的
	// 上游形态），不是只扣标准档：BUG-0347，OpenAI 兼容族把
	// cache_write_tokens 计入 input_tokens 总额，不扣就会让同一批 token 按
	// 「输入价 + 缓存写入价」各计一次（生产 gpt-6.1-sol 样本 input 13196 /
	// cache_write 13193，虚增约 76%）。anthropic 的
	// cache_creation_input_tokens 独立于 input_tokens，
	// cacheWriteIncludedInInput=false 保持全量输入计价。
	cacheWriteDeductedTokens := cacheWriteStandardTokens + cacheWrite1hTokens
	uncachedInputTokens := math.Max(
		nonNegative(input.InputTokens)-
			boolFloat(options.cacheReadIncludedInInput)*cacheReadTokens-
			boolFloat(options.cacheWriteIncludedInInput)*cacheWriteDeductedTokens-
			inputImageTokens-
			inputAudioTokens,
		0)
	outputTokens := math.Max(nonNegative(input.OutputTokens)-outputImageTokens-outputAudioTokens, 0)

	if hasUnpricedUsage(hasUnpricedUsageInput{
		uncachedInputTokens: uncachedInputTokens,
		inputRate:           rates.InputUsdPer1M,
		outputTokens:        outputTokens,
		outputRate:          rates.OutputUsdPer1M,
		cacheReadTokens:     cacheReadTokens,
		cacheReadRate:       cacheReadRate,
		cacheWriteStandard:  cacheWriteStandardTokens,
		cacheWriteRate:      rates.CacheWriteUsdPer1M,
		cacheWrite1hTokens:  cacheWrite1hTokens,
		cacheWrite1hRate:    orRate(rates.CacheWrite1hUsdPer1M, rates.CacheWriteUsdPer1M),
	}) {
		return nil
	}

	var lines []CostLineItem
	lines = addTokenLine(lines, "input", LineInput, options.labels.input, uncachedInputTokens, rates.InputUsdPer1M)
	lines = addTokenLine(lines, "output", LineOutput, options.labels.output, outputTokens, rates.OutputUsdPer1M)
	lines = addTokenLine(lines, "cache_read", LineCacheRead, options.labels.cacheRead, cacheReadTokens, cacheReadRate)
	lines = addTokenLine(lines, "cache_write", LineCacheWrite, options.labels.cacheWrite, cacheWriteStandardTokens, rates.CacheWriteUsdPer1M)
	lines = addTokenLine(lines, "cache_write_1h", LineCacheWrite1h, options.labels.cacheWrite1h, cacheWrite1hTokens, orRate(rates.CacheWrite1hUsdPer1M, rates.CacheWriteUsdPer1M))
	lines = addTokenLine(lines, "image_input", LineImageInput, options.labels.imageInput, inputImageTokens, rates.ImageInputUsdPer1M)
	lines = addTokenLine(lines, "image_output", LineImageOutput, options.labels.imageOutput, outputImageTokens, rates.ImageOutputUsdPer1M)
	lines = addTokenLine(lines, "audio_input", LineAudioInput, options.labels.audioInput, inputAudioTokens, rates.AudioInputUsdPer1M)
	lines = addTokenLine(lines, "audio_output", LineAudioOutput, options.labels.audioOutput, outputAudioTokens, rates.AudioOutputUsdPer1M)
	lines = addUnitLine(lines, "image_output_unit", LineImageOutUnit, options.labels.imageOutputUnit, outputImageCount, LineUnitImage, outputImageRate)
	// 输入图按张价（2026-11-12 输入素材批）：厂商首张/前 N 张免费时先扣免费
	// 额度，再按张计费；无价或扣完为 0 不产行项（输入图费用是可选项）。
	billableInputImages := math.Max(nonNegative(input.InputImageCount)-float64(rates.InputImageFreeCount), 0)
	lines = addUnitLine(lines, "image_input_unit", CostLineKindImageInputUnit, options.labels.imageInputUnit, billableInputImages, LineUnitImage, rates.InputUsdPerImage)
	// M1 同步音频行项（音频设计 §10）：TTS 按输入字符（USD/1M chars，与
	// token 行同构的 UnitSize=1M 折算）、STT 按音频秒（USD/s 直乘）。无价格
	// 或计量为 0 不产行项（0 计费不产生行，与 token 行一致）。
	lines = addPerMillionUnitLine(lines, "tts_input_chars", CostLineKindTtsInputChars, options.labels.ttsInputChars, nonNegative(input.TtsInputChars), LineUnitChar, rates.TtsInputUsdPer1MChars)
	// token 口径 TTS 无 usage 的按次固定价（2026-11-12 用户裁决）：官方按
	// 输出音频 token，上游二进制响应不回报 usage → 一次请求 = 一次调用，
	// 成本 = 次价 × 1；不解析音频、不估算 token。无次价不产行项。
	lines = addUnitLine(lines, "tts_output_calls", CostLineKindTtsOutputCalls, options.labels.ttsOutputCalls, nonNegative(input.TtsCalls), LineUnitCall, rates.TtsOutputUsdPerCall)
	lines = addUnitLine(lines, "audio_input_seconds", CostLineKindAudioInputSeconds, options.labels.audioInputSeconds, nonNegative(input.AudioInputSeconds), LineUnitSecond, rates.AudioInputUsdPerSecond)
	// M2 视频行项（媒体设计 §10）：视频按输出秒计费（USD/s 直乘，与
	// audio_input_seconds 同构）。无价格或计量为 0 不产行项（失败任务不虚计）。
	// 2026-11-12 分辨率档批：目录行带 VideoOutputUsdPerSecondByResolution 且
	// 创建时冻结了计费分辨率时按档位价；分辨率已知但档位不命中 → 不产行项
	//（不回退主档）；分辨率为空（创建时无法确定）→ 用主档价。
	videoSecondRate := rates.VideoOutputUsdPerSecond
	if len(rates.VideoOutputUsdPerSecondByResolution) > 0 && input.OutputVideoResolution != "" {
		if tierRate, ok := rates.VideoOutputUsdPerSecondByResolution[normalizeResolutionKey(input.OutputVideoResolution)]; ok {
			videoSecondRate = &tierRate
		} else {
			videoSecondRate = nil
		}
	}
	lines = addUnitLine(lines, "video_output_seconds", CostLineKindVideoOutputSeconds, options.labels.videoOutputSeconds, nonNegative(input.OutputVideoSeconds), LineUnitSecond, videoSecondRate)
	// 视频按次行项（2026-10-05 按次计费批，契约 §2.8）：按次上游一次任务 =
	// 一次调用（VideoCalls 由结算链按任务粒度填 1），USD/次直乘。秒价与次
	// 价互斥（目录门禁：同一行两槽位只落其一），同一行最多产出两行项之一；
	// 无价格或计量为 0 不产行项。2026-11-12 时长档批 + 2026-11-13 分辨率×
	// 时长组合档：先按 "分辨率-秒数" 选档，未命中再按秒数档（主档分辨率），
	// 已知分辨率有专门档但组合不命中 → 不产行项（不回退主档）。
	lines = addUnitLine(lines, "video_output_calls", CostLineKindVideoOutputCalls, options.labels.videoOutputCalls, nonNegative(input.VideoCalls), LineUnitCall, selectVideoCallRate(rates.PriceSet, input))

	return legacyBreakdownFromLines(lines, input, rates)
}

// selectVideoCallRate 选择按次价（2026-11-12 时长档批 + 2026-11-13 分辨率
// ×时长组合档）：优先 composite 表 "<resolution>-<seconds>"；该分辨率没有
// 任何组合档时回落按秒数档表（主档分辨率价）；该分辨率有专门档但当前时长
// 组合不命中 → nil（不回退主档）。秒数档表存在但秒数不命中同样 nil。
func selectVideoCallRate(rates PriceSet, input CostInput) *float64 {
	if input.VideoCallSeconds == nil {
		return rates.VideoOutputUsdPerCall
	}
	secondsKey := formatSecondsKey(*input.VideoCallSeconds)
	if secondsKey == "" {
		return rates.VideoOutputUsdPerCall
	}
	if len(rates.VideoOutputUsdPerCallByResolutionSeconds) > 0 && input.OutputVideoResolution != "" {
		resolution := normalizeResolutionKey(input.OutputVideoResolution)
		if tierRate, ok := rates.VideoOutputUsdPerCallByResolutionSeconds[resolution+"-"+secondsKey]; ok {
			return &tierRate
		}
		prefix := resolution + "-"
		for key := range rates.VideoOutputUsdPerCallByResolutionSeconds {
			if strings.HasPrefix(key, prefix) {
				return nil
			}
		}
	}
	if len(rates.VideoOutputUsdPerCallBySeconds) > 0 {
		if tierRate, ok := rates.VideoOutputUsdPerCallBySeconds[secondsKey]; ok {
			return &tierRate
		}
		return nil
	}
	return rates.VideoOutputUsdPerCall
}

// selectImageTierRate 按输出像素选择按张档位价（2026-11-12 档位批）：档位按
// MaxPixels 升序，返回第一个 pixels ≤ MaxPixels 的档；MaxPixels ≤ 0 视为
// 无上限。全部不命中返回 nil（不静默回退主档）。
func selectImageTierRate(tiers []ImagePriceTier, pixels int64) *float64 {
	ordered := make([]ImagePriceTier, 0, len(tiers))
	ordered = append(ordered, tiers...)
	sort.SliceStable(ordered, func(left, right int) bool {
		leftLimit := ordered[left].MaxPixels
		rightLimit := ordered[right].MaxPixels
		if leftLimit <= 0 {
			leftLimit = math.MaxInt64
		}
		if rightLimit <= 0 {
			rightLimit = math.MaxInt64
		}
		return leftLimit < rightLimit
	})
	for _, tier := range ordered {
		if tier.MaxPixels <= 0 || pixels <= tier.MaxPixels {
			rate := tier.UsdPerImage
			return &rate
		}
	}
	return nil
}

// orRate mirrors the `a ?? b` rate fallbacks.
func orRate(primary, fallback *float64) *float64 {
	if primary != nil {
		return primary
	}
	return fallback
}

// boolFloat renders a boolean as 0/1 for arithmetic.
func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// legacyBreakdownFromLines mirrors provider-billing.shared
// legacyBreakdownFromLines: the legacy flat fields are read back off the
// line items, the account charge is the costUsd override or the line sum.
func legacyBreakdownFromLines(lines []CostLineItem, input CostInput, rates resolvedRates) *CostBreakdown {
	line := func(kind CostLineKind) *CostLineItem {
		for index := range lines {
			if lines[index].Kind == kind {
				return &lines[index]
			}
		}
		return nil
	}
	cost := func(kind CostLineKind) *float64 {
		if item := line(kind); item != nil {
			value := item.CostUsd
			return &value
		}
		return nil
	}
	rate := func(kind CostLineKind) *float64 {
		if item := line(kind); item != nil {
			value := item.UnitPriceUsd
			return &value
		}
		return nil
	}
	calculated := 0.0
	for index := range lines {
		calculated += lines[index].CostUsd
	}
	calculated = roundCost(calculated)

	return &CostBreakdown{
		LineItems:                lines,
		InputCostUsd:             cost(LineInput),
		OutputCostUsd:            cost(LineOutput),
		InputUsdPer1M:            orRate(rate(LineInput), rates.InputUsdPer1M),
		OutputUsdPer1M:           orRate(rate(LineOutput), rates.OutputUsdPer1M),
		CacheReadCostUsd:         cost(LineCacheRead),
		CacheReadUsdPer1M:        orRate(rate(LineCacheRead), rates.CachedInputUsdPer1M),
		CacheWriteCostUsd:        cost(LineCacheWrite),
		CacheWriteUsdPer1M:       orRate(rate(LineCacheWrite), rates.CacheWriteUsdPer1M),
		CacheWrite1hCostUsd:      cost(LineCacheWrite1h),
		CacheWrite1hUsdPer1M:     orRate(orRate(rate(LineCacheWrite1h), rates.CacheWrite1hUsdPer1M), rates.CacheWriteUsdPer1M),
		ThinkingTokens:           input.ThinkingTokens,
		InputImageCostUsd:        cost(LineImageInput),
		OutputImageCostUsd:       cost(LineImageOutput),
		InputImageUsdPer1M:       orRate(rate(LineImageInput), rates.ImageInputUsdPer1M),
		OutputImageUsdPer1M:      orRate(rate(LineImageOutput), rates.ImageOutputUsdPer1M),
		InputAudioCostUsd:        cost(LineAudioInput),
		OutputAudioCostUsd:       cost(LineAudioOutput),
		InputAudioUsdPer1M:       orRate(rate(LineAudioInput), rates.AudioInputUsdPer1M),
		OutputAudioUsdPer1M:      orRate(rate(LineAudioOutput), rates.AudioOutputUsdPer1M),
		OutputImageUnitCostUsd:   cost(LineImageOutUnit),
		OutputUsdPerImage:        orRate(rate(LineImageOutUnit), rates.OutputUsdPerImage),
		AccountChargeUsd:         orRate(finite(input.CostUsd), &calculated),
		Multiplier:               1,
		ServiceTierPricingSource: rates.serviceTierPricingSource,
		ServiceTierMultiplier:    rates.serviceTierMultiplier,
	}
}

// directRates mirrors provider-billing.shared directRates.
// M1 音频单价与 M2 视频单价同样经 finite 收敛透传（tier 表不携带时由
// serviceTierRates 回落标准价，模式同 image/audio token 价）。
func directRates(set PriceSet) PriceSet {
	return PriceSet{
		InputUsdPer1M:               finite(set.InputUsdPer1M),
		OutputUsdPer1M:              finite(set.OutputUsdPer1M),
		CachedInputUsdPer1M:         finite(set.CachedInputUsdPer1M),
		CacheWriteUsdPer1M:          finite(set.CacheWriteUsdPer1M),
		CacheWrite1hUsdPer1M:        finite(set.CacheWrite1hUsdPer1M),
		CacheStorageUsdPer1MPerHour: finite(set.CacheStorageUsdPer1MPerHour),
		ImageInputUsdPer1M:          finite(set.ImageInputUsdPer1M),
		ImageOutputUsdPer1M:         finite(set.ImageOutputUsdPer1M),
		AudioInputUsdPer1M:          finite(set.AudioInputUsdPer1M),
		AudioOutputUsdPer1M:         finite(set.AudioOutputUsdPer1M),
		OutputUsdPerImage:           finite(set.OutputUsdPerImage),
		OutputUsdPerImageTiers:      set.OutputUsdPerImageTiers,
		InputUsdPerImage:            finite(set.InputUsdPerImage),
		InputImageFreeCount:         set.InputImageFreeCount,
		TtsInputUsdPer1MChars:       finite(set.TtsInputUsdPer1MChars),
		TtsOutputUsdPerCall:         finite(set.TtsOutputUsdPerCall),
		AudioInputUsdPerSecond:      finite(set.AudioInputUsdPerSecond),
		VideoOutputUsdPerSecond:     finite(set.VideoOutputUsdPerSecond),
		// 分辨率秒价档表是价格事实而非费率，直接继承（2026-11-12 档位批）。
		VideoOutputUsdPerSecondByResolution: set.VideoOutputUsdPerSecondByResolution,
		VideoOutputUsdPerCall:               finite(set.VideoOutputUsdPerCall),
		// 档位表是价格事实而非费率，直接继承（2026-11-12 档位批）。
		VideoOutputUsdPerCallBySeconds: set.VideoOutputUsdPerCallBySeconds,
		// 分辨率×时长组合档同属价格事实（2026-11-13 Hailuo 1080P 批）。
		VideoOutputUsdPerCallByResolutionSeconds: set.VideoOutputUsdPerCallByResolutionSeconds,
	}
}

// tierPricingMetadata mirrors provider-billing.shared tierPricingMetadata:
// every token rate present in the tier table marks tier specificity; a
// standard rate missing from the tier table marks the mix.
func tierPricingMetadata(standard, tier PriceSet) (ServiceTierPricingSource, *float64) {
	pairs := [][2]*float64{
		{standard.InputUsdPer1M, tier.InputUsdPer1M},
		{standard.OutputUsdPer1M, tier.OutputUsdPer1M},
		{standard.CachedInputUsdPer1M, tier.CachedInputUsdPer1M},
		{standard.CacheWriteUsdPer1M, tier.CacheWriteUsdPer1M},
		{standard.CacheWrite1hUsdPer1M, tier.CacheWrite1hUsdPer1M},
		{standard.AudioInputUsdPer1M, tier.AudioInputUsdPer1M},
		{standard.AudioOutputUsdPer1M, tier.AudioOutputUsdPer1M},
	}
	specific := 0
	missing := 0
	for _, pair := range pairs {
		switch {
		case pair[1] != nil:
			specific++
		case pair[0] != nil:
			missing++
		}
	}
	switch {
	case specific > 0 && missing == 0:
		return TierSourceTierSpecific, nil
	case specific > 0:
		return TierSourceMixed, nil
	default:
		return TierSourceUnknown, nil
	}
}

// hasUnpricedUsageInput mirrors the named argument object of
// provider-billing.shared hasUnpricedUsage.
type hasUnpricedUsageInput struct {
	uncachedInputTokens float64
	inputRate           *float64
	outputTokens        float64
	outputRate          *float64
	cacheReadTokens     float64
	cacheReadRate       *float64
	cacheWriteStandard  float64
	cacheWriteRate      *float64
	cacheWrite1hTokens  float64
	cacheWrite1hRate    *float64
}

// hasUnpricedUsage mirrors provider-billing.shared hasUnpricedUsage: any
// positive quantity without a rate collapses the breakdown.
func hasUnpricedUsage(input hasUnpricedUsageInput) bool {
	return (input.uncachedInputTokens > 0 && input.inputRate == nil) ||
		(input.outputTokens > 0 && input.outputRate == nil) ||
		(input.cacheReadTokens > 0 && input.cacheReadRate == nil) ||
		(input.cacheWriteStandard > 0 && input.cacheWriteRate == nil) ||
		(input.cacheWrite1hTokens > 0 && input.cacheWrite1hRate == nil)
}

// addTokenLine mirrors provider-billing.shared addTokenLine.
func addTokenLine(lines []CostLineItem, key string, kind CostLineKind, label string, quantity float64, unitPriceUsd *float64) []CostLineItem {
	if quantity <= 0 || unitPriceUsd == nil {
		return lines
	}
	cost := roundCost(quantity / tokenUnitSize * *unitPriceUsd)
	return append(lines, CostLineItem{
		Key:          key,
		Kind:         kind,
		Label:        label,
		Quantity:     quantity,
		Unit:         LineUnitToken,
		UnitSize:     tokenUnitSize,
		UnitPriceUsd: *unitPriceUsd,
		CostUsd:      cost,
	})
}

// addUnitLine mirrors provider-billing.shared addUnitLine.
func addUnitLine(lines []CostLineItem, key string, kind CostLineKind, label string, quantity float64, unit string, unitPriceUsd *float64) []CostLineItem {
	if quantity <= 0 || unitPriceUsd == nil {
		return lines
	}
	cost := roundCost(quantity * *unitPriceUsd)
	return append(lines, CostLineItem{
		Key:          key,
		Kind:         kind,
		Label:        label,
		Quantity:     quantity,
		Unit:         unit,
		UnitSize:     1,
		UnitPriceUsd: *unitPriceUsd,
		CostUsd:      cost,
	})
}

// addPerMillionUnitLine 是 M1 音频字符行项的折算 helper：单价以「每 1M 计量
// 单位」标价（USD/1M chars），折算与 addTokenLine 同构（quantity/1M×单价），
// 仅 Unit 标签不同（char）。
func addPerMillionUnitLine(lines []CostLineItem, key string, kind CostLineKind, label string, quantity float64, unit string, unitPriceUsd *float64) []CostLineItem {
	if quantity <= 0 || unitPriceUsd == nil {
		return lines
	}
	cost := roundCost(quantity / tokenUnitSize * *unitPriceUsd)
	return append(lines, CostLineItem{
		Key:          key,
		Kind:         kind,
		Label:        label,
		Quantity:     quantity,
		Unit:         unit,
		UnitSize:     tokenUnitSize,
		UnitPriceUsd: *unitPriceUsd,
		CostUsd:      cost,
	})
}

// normalizedTier mirrors provider-billing.shared normalizedTier: blank,
// 'default' and 'standard' mean the standard rate set.
func normalizedTier(value string) string {
	tier := strings.TrimSpace(value)
	if tier == "" || tier == "default" || tier == "standard" {
		return ""
	}
	return tier
}
