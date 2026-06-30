import type { UsageStatsAccumulator } from './usage-stats-types.js'

export function statsParamsTail(stats: UsageStatsAccumulator, updatedAt: string): Array<number | string | null> {
  return [
    stats.requestCount,
    stats.successCount,
    stats.errorCount,
    stats.inputTokens,
    stats.outputTokens,
    stats.cacheReadTokens,
    stats.cacheReadCostUsd,
    stats.cacheWriteTokens,
    stats.cacheWrite1hTokens,
    stats.cacheWriteCostUsd,
    stats.thinkingTokens,
    stats.inputImageTokens,
    stats.outputImageTokens,
    stats.totalCostUsd,
    stats.durationMsSum,
    stats.durationMsCount,
    stats.durationMsMax,
    stats.firstTokenMsSum,
    stats.firstTokenMsCount,
    stats.firstTokenMsMax,
    stats.lastUsedAt ?? null,
    stats.lastErrorAt ?? null,
    updatedAt
  ]
}

export function statsSubtractParams(stats: UsageStatsAccumulator): number[] {
  return [
    stats.requestCount,
    stats.successCount,
    stats.errorCount,
    stats.inputTokens,
    stats.outputTokens,
    stats.cacheReadTokens,
    stats.cacheReadCostUsd,
    stats.cacheWriteTokens,
    stats.cacheWrite1hTokens,
    stats.cacheWriteCostUsd,
    stats.thinkingTokens,
    stats.inputImageTokens,
    stats.outputImageTokens,
    stats.totalCostUsd,
    stats.durationMsSum,
    stats.durationMsCount,
    stats.durationMsCount,
    stats.firstTokenMsSum,
    stats.firstTokenMsCount,
    stats.firstTokenMsCount,
    stats.requestCount,
    stats.errorCount
  ]
}
