import {
  compareServerDateTime,
  formatCompactUsageAmount,
  formatDateTime,
  formatNumber,
  formatUsd,
  serverDateTimeTimestamp
} from '@/shared/formatters'
import type { AccountUsageSummary } from '@/types/domain'
export { extractApiErrorMessage } from '@/shared/apiError'

export function formatUsageAmount(value?: number): string {
  return formatCompactUsageAmount(value)
}

export function formatCost(value?: number): string {
  return formatUsd(value)
}

export function emptyUsageSummary(): AccountUsageSummary {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
    inputAudioTokens: 0,
    outputAudioTokens: 0,
    ttsInputChars: 0,
    audioInputSeconds: 0,
    outputVideoSeconds: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

export function normalizeUsageSummary(usage?: Partial<AccountUsageSummary>): AccountUsageSummary {
  return {
    requestCount: numberValue(usage?.requestCount),
    inputTokens: numberValue(usage?.inputTokens),
    outputTokens: numberValue(usage?.outputTokens),
    cacheReadTokens: numberValue(usage?.cacheReadTokens),
    cacheReadCost: numberValue(usage?.cacheReadCost),
    cacheWriteTokens: numberValue(usage?.cacheWriteTokens),
    cacheWrite1hTokens: numberValue(usage?.cacheWrite1hTokens),
    cacheWriteCost: numberValue(usage?.cacheWriteCost),
    thinkingTokens: numberValue(usage?.thinkingTokens),
    inputImageTokens: numberValue(usage?.inputImageTokens),
    outputImageTokens: numberValue(usage?.outputImageTokens),
    inputAudioTokens: numberValue(usage?.inputAudioTokens),
    outputAudioTokens: numberValue(usage?.outputAudioTokens),
    ttsInputChars: numberValue(usage?.ttsInputChars),
    audioInputSeconds: numberValue(usage?.audioInputSeconds),
    outputVideoSeconds: numberValue(usage?.outputVideoSeconds),
    totalTokens: numberValue(usage?.totalTokens),
    totalCost: numberValue(usage?.totalCost),
    lastUsedAt: usage?.lastUsedAt
  }
}

export function sumUsageSummaries(items: Array<Partial<AccountUsageSummary> | undefined>): AccountUsageSummary {
  return items.reduce<AccountUsageSummary>((summary, usage) => {
    const current = normalizeUsageSummary(usage)
    const lastUsedAt = [summary.lastUsedAt, current.lastUsedAt]
      .filter((value): value is string => Boolean(value) && serverDateTimeTimestamp(value) !== undefined)
      .sort((left, right) => compareServerDateTime(right, left))[0]
    return {
      requestCount: summary.requestCount + current.requestCount,
      inputTokens: summary.inputTokens + current.inputTokens,
      outputTokens: summary.outputTokens + current.outputTokens,
      cacheReadTokens: summary.cacheReadTokens + current.cacheReadTokens,
      cacheReadCost: summary.cacheReadCost + current.cacheReadCost,
      cacheWriteTokens: summary.cacheWriteTokens + current.cacheWriteTokens,
      cacheWrite1hTokens: summary.cacheWrite1hTokens + current.cacheWrite1hTokens,
      cacheWriteCost: summary.cacheWriteCost + current.cacheWriteCost,
      thinkingTokens: summary.thinkingTokens + current.thinkingTokens,
      inputImageTokens: summary.inputImageTokens + current.inputImageTokens,
      outputImageTokens: summary.outputImageTokens + current.outputImageTokens,
      inputAudioTokens: summary.inputAudioTokens + current.inputAudioTokens,
      outputAudioTokens: summary.outputAudioTokens + current.outputAudioTokens,
      ttsInputChars: summary.ttsInputChars + current.ttsInputChars,
      audioInputSeconds: summary.audioInputSeconds + current.audioInputSeconds,
      outputVideoSeconds: summary.outputVideoSeconds + current.outputVideoSeconds,
      totalTokens: summary.totalTokens + current.totalTokens,
      totalCost: summary.totalCost + current.totalCost,
      lastUsedAt
    }
  }, emptyUsageSummary())
}

export { formatDateTime, formatNumber }

function numberValue(value: unknown): number {
  const numericValue = typeof value === 'string' ? Number(value.trim()) : value
  return typeof numericValue === 'number' && Number.isFinite(numericValue) ? numericValue : 0
}
