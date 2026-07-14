import type { UsageRecordSummary } from '@/types/domain'
import { formatCost, formatTokens, formatUnitPrice } from './usageRecordFormatters'

export type UsageRecordCostProviderFamily = 'openai' | 'anthropic' | 'gemini' | 'deepseek' | 'glm' | 'generic'

export interface UsageRecordCostDetailRow {
  key: string
  label: string
  value: string
}

export function usageRecordCostDetailTitle(record: UsageRecordSummary): string {
  void record
  return '成本明细'
}

export function usageRecordHasCostDetails(record: UsageRecordSummary): boolean {
  return usageRecordCostMetadataRows(record).length > 0
    || usageRecordCostAmountRows(record).length > 0
    || usageRecordCostPriceRows(record).length > 0
}

export function usageRecordCostMetadataRows(record: UsageRecordSummary): UsageRecordCostDetailRow[] {
  const rows: UsageRecordCostDetailRow[] = []
  if (record.pricingModel) {
    rows.push({ key: 'pricingModel', label: '计价模型', value: record.pricingModel })
  }
  pushServiceTierRow(rows, 'billedServiceTier', '实际服务档位', record.billedServiceTier)
  const pricingSource = serviceTierPricingSourceText(record)
  if (pricingSource) {
    rows.push({ key: 'serviceTierPricingSource', label: '计价来源', value: pricingSource })
  }
  return rows
}

function pushServiceTierRow(
  rows: UsageRecordCostDetailRow[],
  key: string,
  label: string,
  tier: UsageRecordSummary['billedServiceTier']
): void {
  if (tier !== 'priority' && tier !== 'flex') return
  rows.push({
    key,
    label,
    value: tier === 'priority' ? 'Priority' : tier === 'flex' ? 'Flex' : 'Default'
  })
}

export function usageRecordCostTokenRows(record: UsageRecordSummary): UsageRecordCostDetailRow[] {
  const family = usageRecordCostProviderFamily(record)
  const rows: UsageRecordCostDetailRow[] = []
  const cacheWrite1hTokens = positiveNumber(record.cacheWrite1hTokens)
  const cacheWriteStandardTokens = standardCacheWriteTokens(record)
  const thinkingTokens = positiveNumber(record.costBreakdown?.thinkingTokens ?? record.thinkingTokens)
  const inputImageTokens = positiveNumber(record.inputImageTokens)
  const outputImageTokens = positiveNumber(record.outputImageTokens)
  const inputAudioTokens = positiveNumber(record.inputAudioTokens)
  const outputAudioTokens = positiveNumber(record.outputAudioTokens)
  const outputImageCount = positiveNumber(record.outputImageCount)

  pushTokenRow(rows, 'cacheWriteTokens', cacheWriteTokenLabel(family), cacheWriteStandardTokens)
  pushTokenRow(rows, 'cacheWrite1hTokens', '1h 缓存写入 Tokens', cacheWrite1hTokens)
  pushTokenRow(rows, 'thinkingTokens', thinkingTokenLabel(family), thinkingTokens)
  pushTokenRow(rows, 'inputImageTokens', '图片输入 Tokens', inputImageTokens)
  pushTokenRow(rows, 'outputImageTokens', '图片输出 Tokens', outputImageTokens)
  pushTokenRow(rows, 'inputAudioTokens', '音频输入 Tokens', inputAudioTokens)
  pushTokenRow(rows, 'outputAudioTokens', '音频输出 Tokens', outputAudioTokens)
  if (outputImageCount > 0) {
    rows.push({ key: 'outputImageCount', label: '输出图片张数', value: `${formatTokens(outputImageCount)} 张` })
  }
  return rows
}

export function usageRecordCostAmountRows(record: UsageRecordSummary): UsageRecordCostDetailRow[] {
  const costBreakdown = record.costBreakdown
  if (!costBreakdown) return []

  const family = usageRecordCostProviderFamily(record)
  const rows: UsageRecordCostDetailRow[] = []
  const cacheWriteStandardTokens = standardCacheWriteTokens(record)
  const cacheWrite1hTokens = positiveNumber(record.cacheWrite1hTokens)

  pushCostRow(rows, 'inputCostUsd', '输入成本', costBreakdown.inputCostUsd, activeDimension(record.inputTokens, costBreakdown.inputCostUsd))
  pushCostRow(rows, 'outputCostUsd', '输出成本', costBreakdown.outputCostUsd, activeDimension(record.outputTokens, costBreakdown.outputCostUsd))
  pushCostRow(rows, 'cacheReadCostUsd', '缓存读成本', costBreakdown.cacheReadCostUsd, activeDimension(record.cacheReadTokens, costBreakdown.cacheReadCostUsd))
  pushCostRow(rows, 'cacheWriteCostUsd', cacheWriteCostLabel(family), costBreakdown.cacheWriteCostUsd, activeDimension(cacheWriteStandardTokens, costBreakdown.cacheWriteCostUsd))
  pushCostRow(rows, 'cacheWrite1hCostUsd', '1h 缓存写入成本', costBreakdown.cacheWrite1hCostUsd, activeDimension(cacheWrite1hTokens, costBreakdown.cacheWrite1hCostUsd))
  pushCostRow(rows, 'inputImageCostUsd', '图片输入成本', costBreakdown.inputImageCostUsd, activeDimension(record.inputImageTokens, costBreakdown.inputImageCostUsd))
  pushCostRow(rows, 'outputImageCostUsd', '图片输出成本', costBreakdown.outputImageCostUsd, activeDimension(record.outputImageTokens, costBreakdown.outputImageCostUsd))
  pushCostRow(rows, 'inputAudioCostUsd', '音频输入成本', costBreakdown.inputAudioCostUsd, activeDimension(record.inputAudioTokens, costBreakdown.inputAudioCostUsd))
  pushCostRow(rows, 'outputAudioCostUsd', '音频输出成本', costBreakdown.outputAudioCostUsd, activeDimension(record.outputAudioTokens, costBreakdown.outputAudioCostUsd))
  pushCostRow(rows, 'outputImageUnitCostUsd', '图片张数成本', costBreakdown.outputImageUnitCostUsd, activeDimension(record.outputImageCount, costBreakdown.outputImageUnitCostUsd))

  if (isFiniteNumber(costBreakdown.accountChargeUsd)) {
    rows.push({ key: 'accountChargeUsd', label: '合计成本', value: formatCost(costBreakdown.accountChargeUsd) })
  }
  return rows
}

export function usageRecordCostPriceRows(record: UsageRecordSummary): UsageRecordCostDetailRow[] {
  const costBreakdown = record.costBreakdown
  if (!costBreakdown) return []

  const family = usageRecordCostProviderFamily(record)
  const rows: UsageRecordCostDetailRow[] = []
  const cacheWriteStandardTokens = standardCacheWriteTokens(record)
  const cacheWrite1hTokens = positiveNumber(record.cacheWrite1hTokens)

  pushUnitPriceRow(rows, 'inputUsdPer1M', '输入单价', costBreakdown.inputUsdPer1M, activeDimension(record.inputTokens, costBreakdown.inputCostUsd))
  pushUnitPriceRow(rows, 'outputUsdPer1M', '输出单价', costBreakdown.outputUsdPer1M, activeDimension(record.outputTokens, costBreakdown.outputCostUsd))
  pushUnitPriceRow(rows, 'cacheReadUsdPer1M', '缓存读单价', costBreakdown.cacheReadUsdPer1M, activeDimension(record.cacheReadTokens, costBreakdown.cacheReadCostUsd))
  pushUnitPriceRow(rows, 'cacheWriteUsdPer1M', cacheWritePriceLabel(family), costBreakdown.cacheWriteUsdPer1M, activeDimension(cacheWriteStandardTokens, costBreakdown.cacheWriteCostUsd))
  pushUnitPriceRow(
    rows,
    'cacheWrite1hUsdPer1M',
    '1h 缓存写入单价',
    costBreakdown.cacheWrite1hUsdPer1M,
    activeDimension(cacheWrite1hTokens, costBreakdown.cacheWrite1hCostUsd)
      && costBreakdown.cacheWrite1hUsdPer1M !== costBreakdown.cacheWriteUsdPer1M
  )
  pushUnitPriceRow(rows, 'inputImageUsdPer1M', '图片输入单价', costBreakdown.inputImageUsdPer1M, activeDimension(record.inputImageTokens, costBreakdown.inputImageCostUsd))
  pushUnitPriceRow(rows, 'outputImageUsdPer1M', '图片输出单价', costBreakdown.outputImageUsdPer1M, activeDimension(record.outputImageTokens, costBreakdown.outputImageCostUsd))
  pushUnitPriceRow(rows, 'inputAudioUsdPer1M', '音频输入单价', costBreakdown.inputAudioUsdPer1M, activeDimension(record.inputAudioTokens, costBreakdown.inputAudioCostUsd))
  pushUnitPriceRow(rows, 'outputAudioUsdPer1M', '音频输出单价', costBreakdown.outputAudioUsdPer1M, activeDimension(record.outputAudioTokens, costBreakdown.outputAudioCostUsd))
  if (isFiniteNumber(costBreakdown.outputUsdPerImage) && activeDimension(record.outputImageCount, costBreakdown.outputImageUnitCostUsd)) {
    rows.push({ key: 'outputUsdPerImage', label: '每张图片单价', value: formatCost(costBreakdown.outputUsdPerImage) })
  }
  return rows
}

export function usageRecordCostProviderFamily(record: UsageRecordSummary): UsageRecordCostProviderFamily {
  const usageSemantic = normalizeToken(record.usageSemantic)
  const providerCode = normalizeToken(record.providerCode)
  if (usageSemantic === 'anthropic' || providerCode === 'anthropic') return 'anthropic'
  if (usageSemantic === 'gemini' || providerCode === 'gemini') return 'gemini'
  if (providerCode === 'deepseek') return 'deepseek'
  if (providerCode === 'glm') return 'glm'
  if (isOpenAICompatibleFamily(usageSemantic) || isOpenAICompatibleFamily(providerCode)) return 'openai'
  return 'generic'
}

function isOpenAICompatibleFamily(value: string): boolean {
  return value === 'openai' || value === 'openai-compatible' || value === 'gpt'
}

function cacheWriteTokenLabel(family: UsageRecordCostProviderFamily): string {
  return family === 'anthropic' ? '5m 缓存写入 Tokens' : '缓存写入 Tokens'
}

function cacheWriteCostLabel(family: UsageRecordCostProviderFamily): string {
  return family === 'anthropic' ? '5m 缓存写入成本' : '缓存写入成本'
}

function cacheWritePriceLabel(family: UsageRecordCostProviderFamily): string {
  return family === 'anthropic' ? '5m 缓存写入单价' : '缓存写入单价'
}

function thinkingTokenLabel(family: UsageRecordCostProviderFamily): string {
  return family === 'openai' || family === 'deepseek' || family === 'glm'
    ? '推理 Tokens'
    : '思考 Tokens'
}

function serviceTierPricingSourceText(record: UsageRecordSummary): string | undefined {
  if (record.billedServiceTier !== 'priority' && record.billedServiceTier !== 'flex') return undefined
  const breakdown = record.costBreakdown
  if (!breakdown) return undefined
  if (breakdown.serviceTierPricingSource === 'tier_specific') return '档位专用价'
  const multiplier = breakdown.serviceTierMultiplier
  const multiplierText = isFiniteNumber(multiplier) ? `${multiplier}x` : '配置倍率'
  if (breakdown.serviceTierPricingSource === 'mixed') return `档位专用价 + 基础价 ${multiplierText}`
  if (breakdown.serviceTierPricingSource === 'multiplier') return `基础价 ${multiplierText}`
  if (breakdown.serviceTierPricingSource === 'unknown') return undefined
  return '标准价'
}

function standardCacheWriteTokens(record: UsageRecordSummary): number {
  return Math.max(positiveNumber(record.cacheWriteTokens) - positiveNumber(record.cacheWrite1hTokens), 0)
}

function activeDimension(tokens: number | undefined, cost: number | undefined): boolean {
  return positiveNumber(tokens) > 0 || positiveNumber(cost) > 0
}

function pushTokenRow(rows: UsageRecordCostDetailRow[], key: string, label: string, tokens: number): void {
  if (tokens > 0) {
    rows.push({ key, label, value: formatTokens(tokens) })
  }
}

function pushCostRow(rows: UsageRecordCostDetailRow[], key: string, label: string, cost: number | undefined, active: boolean): void {
  if (active && isFiniteNumber(cost)) {
    rows.push({ key, label, value: formatCost(cost) })
  }
}

function pushUnitPriceRow(rows: UsageRecordCostDetailRow[], key: string, label: string, price: number | undefined, active: boolean): void {
  if (active && isFiniteNumber(price)) {
    rows.push({ key, label, value: formatUnitPrice(price) })
  }
}

function positiveNumber(value: unknown): number {
  const numericValue = numberValue(value)
  return numericValue !== undefined && numericValue > 0 ? numericValue : 0
}

function isFiniteNumber(value: unknown): boolean {
  return numberValue(value) !== undefined
}

function numberValue(value: unknown): number | undefined {
  const numericValue = typeof value === 'string' ? Number(value.trim()) : value
  return typeof numericValue === 'number' && Number.isFinite(numericValue) ? numericValue : undefined
}

function normalizeToken(value: unknown): string {
  return typeof value === 'string' ? value.trim().toLowerCase() : ''
}
