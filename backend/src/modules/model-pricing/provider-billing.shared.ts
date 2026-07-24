import type {
  ProviderBillingCostInput,
  ProviderBillingPricing,
  ProviderBillingPriceSet,
  ProviderCatalogDisplayItem,
  ProviderCatalogDisplaySection,
  ProviderCostBreakdown,
  ProviderCostLineItem,
  ProviderCostLineKind,
  ResolvedProviderBillingRates
} from './provider-billing.types.js'

const TOKEN_UNIT_SIZE = 1_000_000

export interface TokenBillingLabels {
  input: string
  output: string
  cacheRead: string
  cacheWrite: string
  cacheWrite1h: string
  imageInput: string
  imageOutput: string
  audioInput: string
  audioOutput: string
  imageOutputUnit: string
}

export interface TokenBillingOptions {
  cacheReadIncludedInInput: boolean
  cacheReadFallbackToInput: boolean
  labels: TokenBillingLabels
}

export const defaultTokenBillingLabels: TokenBillingLabels = {
  input: '输入 Token',
  output: '输出 Token',
  cacheRead: '缓存命中 Token',
  cacheWrite: '缓存写入 Token',
  cacheWrite1h: '1h 缓存写入 Token',
  imageInput: '图片输入 Token',
  imageOutput: '图片输出 Token',
  audioInput: '音频输入 Token',
  audioOutput: '音频输出 Token',
  imageOutputUnit: '输出图片'
}

export function buildTokenCostBreakdown(
  pricing: ProviderBillingPricing,
  input: ProviderBillingCostInput,
  rates: ResolvedProviderBillingRates,
  options: TokenBillingOptions
): ProviderCostBreakdown | undefined {
  if (!hasAnyRate(rates)) return undefined

  const cacheReadTokens = nonNegative(input.cacheReadTokens)
  const cacheWriteTokens = nonNegative(input.cacheWriteTokens)
  const cacheWrite1hTokens = Math.min(nonNegative(input.cacheWrite1hTokens), cacheWriteTokens || nonNegative(input.cacheWrite1hTokens))
  const cacheWriteStandardTokens = Math.max(cacheWriteTokens - cacheWrite1hTokens, 0)
  const inputImageTokens = rates.imageInputUsdPer1M === undefined ? 0 : nonNegative(input.inputImageTokens)
  const outputImageTokens = rates.imageOutputUsdPer1M === undefined ? 0 : nonNegative(input.outputImageTokens)
  const inputAudioTokens = rates.audioInputUsdPer1M === undefined ? 0 : nonNegative(input.inputAudioTokens)
  const outputAudioTokens = rates.audioOutputUsdPer1M === undefined ? 0 : nonNegative(input.outputAudioTokens)
  const outputImageCount = rates.outputUsdPerImage === undefined ? 0 : nonNegative(input.outputImageCount)
  if (nonNegative(input.outputImageCount) > 0 && rates.outputUsdPerImage === undefined) return undefined
  const cacheReadRate = rates.cachedInputUsdPer1M
    ?? (options.cacheReadFallbackToInput ? rates.inputUsdPer1M : undefined)
  const uncachedInputTokens = Math.max(
    nonNegative(input.inputTokens)
      - (options.cacheReadIncludedInInput ? cacheReadTokens : 0)
      - inputImageTokens
      - inputAudioTokens,
    0
  )
  const outputTokens = Math.max(nonNegative(input.outputTokens) - outputImageTokens - outputAudioTokens, 0)

  if (hasUnpricedUsage({
    uncachedInputTokens,
    inputRate: rates.inputUsdPer1M,
    outputTokens,
    outputRate: rates.outputUsdPer1M,
    cacheReadTokens,
    cacheReadRate,
    cacheWriteStandardTokens,
    cacheWriteRate: rates.cacheWriteUsdPer1M,
    cacheWrite1hTokens,
    cacheWrite1hRate: rates.cacheWrite1hUsdPer1M ?? rates.cacheWriteUsdPer1M
  })) return undefined

  const lines: ProviderCostLineItem[] = []
  addTokenLine(lines, 'input', 'input', options.labels.input, uncachedInputTokens, rates.inputUsdPer1M)
  addTokenLine(lines, 'output', 'output', options.labels.output, outputTokens, rates.outputUsdPer1M)
  addTokenLine(lines, 'cache_read', 'cache_read', options.labels.cacheRead, cacheReadTokens, cacheReadRate)
  addTokenLine(lines, 'cache_write', 'cache_write', options.labels.cacheWrite, cacheWriteStandardTokens, rates.cacheWriteUsdPer1M)
  addTokenLine(lines, 'cache_write_1h', 'cache_write_1h', options.labels.cacheWrite1h, cacheWrite1hTokens, rates.cacheWrite1hUsdPer1M ?? rates.cacheWriteUsdPer1M)
  addTokenLine(lines, 'image_input', 'image_input', options.labels.imageInput, inputImageTokens, rates.imageInputUsdPer1M)
  addTokenLine(lines, 'image_output', 'image_output', options.labels.imageOutput, outputImageTokens, rates.imageOutputUsdPer1M)
  addTokenLine(lines, 'audio_input', 'audio_input', options.labels.audioInput, inputAudioTokens, rates.audioInputUsdPer1M)
  addTokenLine(lines, 'audio_output', 'audio_output', options.labels.audioOutput, outputAudioTokens, rates.audioOutputUsdPer1M)
  addUnitLine(lines, 'image_output_unit', 'image_output_unit', options.labels.imageOutputUnit, outputImageCount, 'image', rates.outputUsdPerImage)

  return legacyBreakdownFromLines(lines, input, rates)
}

export function standardRates(pricing: ProviderBillingPricing): ResolvedProviderBillingRates {
  return {
    ...directRates(pricing),
    serviceTierPricingSource: 'default'
  }
}

export function hasUnsupportedServiceTier(pricing: ProviderBillingPricing, input: ProviderBillingCostInput): boolean {
  const tier = normalizedTier(input.serviceTier)
  return tier !== undefined && !pricing.supportedServiceTiers?.includes(tier)
}

export function serviceTierRates(
  pricing: ProviderBillingPricing,
  input: ProviderBillingCostInput
): ResolvedProviderBillingRates {
  const tier = normalizedTier(input.serviceTier)
  if (!tier) return standardRates(pricing)
  if (!pricing.supportedServiceTiers?.includes(tier)) {
    return { serviceTierPricingSource: 'unknown' }
  }
  const tierRates = pricing.serviceTierPrices?.[tier]
  if (!tierRates) return { serviceTierPricingSource: 'unknown' }
  const metadata = tierPricingMetadata(pricing, tierRates)
  const resolvedTierRates = directRates(tierRates)
  return {
    ...resolvedTierRates,
    imageInputUsdPer1M: resolvedTierRates.imageInputUsdPer1M ?? finite(pricing.imageInputUsdPer1M),
    imageOutputUsdPer1M: resolvedTierRates.imageOutputUsdPer1M ?? finite(pricing.imageOutputUsdPer1M),
    audioInputUsdPer1M: resolvedTierRates.audioInputUsdPer1M ?? finite(pricing.audioInputUsdPer1M),
    audioOutputUsdPer1M: resolvedTierRates.audioOutputUsdPer1M ?? finite(pricing.audioOutputUsdPer1M),
    outputUsdPerImage: resolvedTierRates.outputUsdPerImage ?? finite(pricing.outputUsdPerImage),
    ...metadata
  }
}

export function applyLongContextRates(
  pricing: ProviderBillingPricing,
  input: ProviderBillingCostInput,
  rates: ResolvedProviderBillingRates
): ResolvedProviderBillingRates {
  const threshold = pricing.longContextInputTokenThreshold
  if (threshold === undefined) return rates
  const inputTokens = nonNegative(input.inputTokens)
  const applies = pricing.longContextInputTokenThresholdInclusive
    ? inputTokens >= threshold
    : inputTokens > threshold
  if (!applies) return rates
  const inputMultiplier = validMultiplier(pricing.longContextInputCostMultiplier)
  const outputMultiplier = validMultiplier(pricing.longContextOutputCostMultiplier)
  return {
    ...rates,
    inputUsdPer1M: multiply(rates.inputUsdPer1M, inputMultiplier),
    cachedInputUsdPer1M: multiply(rates.cachedInputUsdPer1M, inputMultiplier),
    cacheWriteUsdPer1M: multiply(rates.cacheWriteUsdPer1M, inputMultiplier),
    cacheWrite1hUsdPer1M: multiply(rates.cacheWrite1hUsdPer1M, inputMultiplier),
    outputUsdPer1M: multiply(rates.outputUsdPer1M, outputMultiplier)
  }
}

export function priceSection(
  key: string,
  label: string,
  entries: Array<[string, string, number | undefined, ProviderCatalogDisplayItem['format']]>
): ProviderCatalogDisplaySection | undefined {
  const items = entries.flatMap(([itemKey, itemLabel, value, format]) => (
    value === undefined ? [] : [{ key: itemKey, label: itemLabel, value, format }]
  ))
  return items.length ? { key, label, items } : undefined
}

export function textSection(
  key: string,
  label: string,
  entries: Array<[string, string, string | number | undefined, ProviderCatalogDisplayItem['format']]>
): ProviderCatalogDisplaySection | undefined {
  const items = entries.flatMap(([itemKey, itemLabel, value, format]) => (
    value === undefined || value === '' ? [] : [{ key: itemKey, label: itemLabel, value, format }]
  ))
  return items.length ? { key, label, items } : undefined
}

export function capacitySection(pricing: ProviderBillingPricing): ProviderCatalogDisplaySection | undefined {
  return textSection('capacity', '容量', [
    ['context', '上下文', pricing.contextWindowTokens, 'tokens'],
    ['max_input', '最大输入', pricing.maxInputTokens, 'tokens'],
    ['max_output', '最大输出', pricing.maxOutputTokens, 'tokens']
  ])
}

export function reasoningSection(pricing: ProviderBillingPricing): ProviderCatalogDisplaySection | undefined {
  const efforts = pricing.supportedReasoningEfforts?.filter(Boolean) ?? []
  if (!efforts.length) return undefined
  const value = efforts.join(' / ')
  return textSection('reasoning', '思考能力', [['levels', '级别', value, 'text']])
}

export function longContextSection(pricing: ProviderBillingPricing): ProviderCatalogDisplaySection | undefined {
  if (pricing.longContextInputTokenThreshold === undefined) return undefined
  return textSection('long_context', '长上下文计费', [
    ['threshold', pricing.longContextInputTokenThresholdInclusive ? '触发阈值（含）' : '触发阈值（不含）', pricing.longContextInputTokenThreshold, 'tokens'],
    ['input_multiplier', '输入倍率', pricing.longContextInputCostMultiplier, 'multiplier'],
    ['output_multiplier', '输出倍率', pricing.longContextOutputCostMultiplier, 'multiplier']
  ])
}

export function serviceTierSections(pricing: ProviderBillingPricing): ProviderCatalogDisplaySection[] {
  return (pricing.supportedServiceTiers ?? []).flatMap((tier) => {
    const rates = pricing.serviceTierPrices?.[tier]
    if (!rates) return []
    const section = priceSection(`tier_${tier}`, tierLabel(tier), [
      ['input', '输入', rates.inputUsdPer1M, 'usd_per_1m_tokens'],
      ['output', '输出', rates.outputUsdPer1M, 'usd_per_1m_tokens'],
      ['cache_read', '缓存命中', rates.cachedInputUsdPer1M, 'usd_per_1m_tokens'],
      ['cache_write', '缓存写入', rates.cacheWriteUsdPer1M, 'usd_per_1m_tokens'],
      ['cache_write_1h', '1h 缓存写入', rates.cacheWrite1hUsdPer1M, 'usd_per_1m_tokens'],
      ['cache_storage', '缓存存储', rates.cacheStorageUsdPer1MPerHour, 'usd_per_1m_token_hour'],
      ['audio_input', '音频输入', rates.audioInputUsdPer1M, 'usd_per_1m_tokens']
    ])
    return section ? [section] : []
  })
}

export function sourceConversionSection(pricing: ProviderBillingPricing): ProviderCatalogDisplaySection | undefined {
  if (!pricing.sourcePricingCurrency || pricing.sourcePricingCurrency === 'USD') return undefined
  return textSection('currency_conversion', '美元换算', [
    ['source_currency', '官方币种', pricing.sourcePricingCurrency, 'text'],
    ['exchange_rate', `1 ${pricing.sourcePricingCurrency}`, pricing.sourceExchangeRateToUsd === undefined ? undefined : formatUsdRate(pricing.sourceExchangeRateToUsd), 'text'],
    ['exchange_rate_date', '汇率日期', pricing.sourceExchangeRateDate, 'text'],
    ['source_note', '官方源价', pricing.sourcePricingNote, 'text']
  ])
}

export function compactSections(sections: Array<ProviderCatalogDisplaySection | undefined>): ProviderCatalogDisplaySection[] {
  return sections.filter((section): section is ProviderCatalogDisplaySection => section !== undefined && section.items.length > 0)
}

function directRates(pricing: ProviderBillingPriceSet): ProviderBillingPriceSet {
  return {
    inputUsdPer1M: finite(pricing.inputUsdPer1M),
    outputUsdPer1M: finite(pricing.outputUsdPer1M),
    cachedInputUsdPer1M: finite(pricing.cachedInputUsdPer1M),
    cacheWriteUsdPer1M: finite(pricing.cacheWriteUsdPer1M),
    cacheWrite1hUsdPer1M: finite(pricing.cacheWrite1hUsdPer1M),
    cacheStorageUsdPer1MPerHour: finite(pricing.cacheStorageUsdPer1MPerHour),
    imageInputUsdPer1M: finite(pricing.imageInputUsdPer1M),
    imageOutputUsdPer1M: finite(pricing.imageOutputUsdPer1M),
    audioInputUsdPer1M: finite(pricing.audioInputUsdPer1M),
    audioOutputUsdPer1M: finite(pricing.audioOutputUsdPer1M),
    outputUsdPerImage: finite(pricing.outputUsdPerImage)
  }
}

function legacyBreakdownFromLines(
  lines: ProviderCostLineItem[],
  input: ProviderBillingCostInput,
  rates: ResolvedProviderBillingRates
): ProviderCostBreakdown {
  const line = (kind: ProviderCostLineKind) => lines.find((item) => item.kind === kind)
  const cost = (kind: ProviderCostLineKind) => line(kind)?.costUsd
  const rate = (kind: ProviderCostLineKind) => line(kind)?.unitPriceUsd
  const calculated = roundCost(lines.reduce((total, item) => total + item.costUsd, 0))
  return {
    lineItems: lines,
    inputCostUsd: cost('input'),
    outputCostUsd: cost('output'),
    inputUsdPer1M: rate('input') ?? rates.inputUsdPer1M,
    outputUsdPer1M: rate('output') ?? rates.outputUsdPer1M,
    cacheReadCostUsd: cost('cache_read'),
    cacheReadUsdPer1M: rate('cache_read') ?? rates.cachedInputUsdPer1M,
    cacheWriteCostUsd: cost('cache_write'),
    cacheWriteUsdPer1M: rate('cache_write') ?? rates.cacheWriteUsdPer1M,
    cacheWrite1hCostUsd: cost('cache_write_1h'),
    cacheWrite1hUsdPer1M: rate('cache_write_1h') ?? rates.cacheWrite1hUsdPer1M ?? rates.cacheWriteUsdPer1M,
    thinkingTokens: input.thinkingTokens,
    inputImageCostUsd: cost('image_input'),
    outputImageCostUsd: cost('image_output'),
    inputImageUsdPer1M: rate('image_input') ?? rates.imageInputUsdPer1M,
    outputImageUsdPer1M: rate('image_output') ?? rates.imageOutputUsdPer1M,
    inputAudioCostUsd: cost('audio_input'),
    outputAudioCostUsd: cost('audio_output'),
    inputAudioUsdPer1M: rate('audio_input') ?? rates.audioInputUsdPer1M,
    outputAudioUsdPer1M: rate('audio_output') ?? rates.audioOutputUsdPer1M,
    outputImageUnitCostUsd: cost('image_output_unit'),
    outputUsdPerImage: rate('image_output_unit') ?? rates.outputUsdPerImage,
    accountChargeUsd: finite(input.costUsd) ?? calculated,
    multiplier: 1,
    serviceTierPricingSource: rates.serviceTierPricingSource,
    serviceTierMultiplier: rates.serviceTierMultiplier
  }
}

function addTokenLine(
  lines: ProviderCostLineItem[],
  key: string,
  kind: ProviderCostLineKind,
  label: string,
  quantity: number,
  unitPriceUsd: number | undefined
): void {
  if (quantity <= 0 || unitPriceUsd === undefined) return
  lines.push({
    key,
    kind,
    label,
    quantity,
    unit: 'token',
    unitSize: TOKEN_UNIT_SIZE,
    unitPriceUsd,
    costUsd: roundCost(quantity / TOKEN_UNIT_SIZE * unitPriceUsd)
  })
}

function addUnitLine(
  lines: ProviderCostLineItem[],
  key: string,
  kind: ProviderCostLineKind,
  label: string,
  quantity: number,
  unit: ProviderCostLineItem['unit'],
  unitPriceUsd: number | undefined
): void {
  if (quantity <= 0 || unitPriceUsd === undefined) return
  lines.push({ key, kind, label, quantity, unit, unitSize: 1, unitPriceUsd, costUsd: roundCost(quantity * unitPriceUsd) })
}

function tierPricingMetadata(
  standard: ProviderBillingPriceSet,
  tier: ProviderBillingPriceSet
): Pick<ResolvedProviderBillingRates, 'serviceTierPricingSource' | 'serviceTierMultiplier'> {
  const pairs: Array<[number | undefined, number | undefined]> = [
    [standard.inputUsdPer1M, tier.inputUsdPer1M],
    [standard.outputUsdPer1M, tier.outputUsdPer1M],
    [standard.cachedInputUsdPer1M, tier.cachedInputUsdPer1M],
    [standard.cacheWriteUsdPer1M, tier.cacheWriteUsdPer1M],
    [standard.cacheWrite1hUsdPer1M, tier.cacheWrite1hUsdPer1M],
    [standard.audioInputUsdPer1M, tier.audioInputUsdPer1M],
    [standard.audioOutputUsdPer1M, tier.audioOutputUsdPer1M]
  ]
  let specific = 0
  let missing = 0
  for (const [standardRate, tierRate] of pairs) {
    if (tierRate !== undefined) specific += 1
    else if (standardRate !== undefined) missing += 1
  }
  if (specific > 0 && missing === 0) return { serviceTierPricingSource: 'tier_specific' }
  if (specific > 0) return { serviceTierPricingSource: 'mixed' }
  return { serviceTierPricingSource: 'unknown' }
}

function hasAnyRate(rates: ProviderBillingPriceSet): boolean {
  return Object.values(rates).some((value) => typeof value === 'number' && Number.isFinite(value))
}

function hasUnpricedUsage(input: {
  uncachedInputTokens: number
  inputRate?: number
  outputTokens: number
  outputRate?: number
  cacheReadTokens: number
  cacheReadRate?: number
  cacheWriteStandardTokens: number
  cacheWriteRate?: number
  cacheWrite1hTokens: number
  cacheWrite1hRate?: number
}): boolean {
  return (input.uncachedInputTokens > 0 && input.inputRate === undefined)
    || (input.outputTokens > 0 && input.outputRate === undefined)
    || (input.cacheReadTokens > 0 && input.cacheReadRate === undefined)
    || (input.cacheWriteStandardTokens > 0 && input.cacheWriteRate === undefined)
    || (input.cacheWrite1hTokens > 0 && input.cacheWrite1hRate === undefined)
}

function normalizedTier(value: string | undefined): string | undefined {
  const tier = value?.trim()
  return tier && tier !== 'default' && tier !== 'standard' ? tier : undefined
}

function tierLabel(value: string): string {
  if (value === 'priority') return 'Priority'
  if (value === 'flex') return 'Flex'
  if (value === 'batch') return 'Batch API'
  return value
}

function nonNegative(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(value, 0) : 0
}

function finite(value: number | undefined): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined
}

function validMultiplier(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 1
}

function multiply(value: number | undefined, multiplier: number): number | undefined {
  return value === undefined ? undefined : value * multiplier
}

function roundCost(value: number): number {
  return Number(value.toFixed(10))
}

function formatUsdRate(value: number): string {
  return `$${Number(value.toFixed(8))}`
}
