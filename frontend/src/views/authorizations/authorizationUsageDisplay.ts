import type { StatsSummaryCardItem } from '@/views/stats/StatsSummaryCards.vue'
import type {
  AuthorizationUsageAggregateSummary,
  AuthorizationResourceType,
  AuthorizationTeamUsageRow,
  AuthorizationUserUsageRow
} from '@/types/domain'

import { formatCost, formatNumber } from './authorizationFormatters'
import {
  formatCompactInteger,
  formatDurationSeconds,
  formatInteger,
  formatPercent
} from '@/views/stats/statsFormatters'

export function resourceTypeTag(resourceType: AuthorizationResourceType): { text: string; color: string } {
  return resourceType === 'group'
    ? { text: '分组', color: 'purple' }
    : { text: 'AI账户', color: 'blue' }
}

export function resourceDisplayName(row: Pick<AuthorizationTeamUsageRow | AuthorizationUserUsageRow, 'resourceName'>): string {
  return row.resourceName || '-'
}

export function teamDisplayName(row: Pick<AuthorizationUserUsageRow, 'teamNames'>): string {
  return row.teamNames?.filter(Boolean).join('、') ?? ''
}

export function createAuthorizationUsageShowTotal(itemLabel: string) {
  return (
    total: number,
    _range?: [number, number],
    context?: {
      current: number
      currentPageCount: number
      hasMore: boolean
      pageSize: number
    }
  ): string => {
    const loaded = context ? (context.current - 1) * context.pageSize + context.currentPageCount : total
    return context?.hasMore ? `已加载到第 ${formatNumber(loaded)} 条${itemLabel}，还有更多` : `共 ${formatNumber(total)} 条${itemLabel}`
  }
}

// 摘要卡片口径与「统计与监控 → 用量分析」4 卡一致（StatsView.summaryCards）：
// 范围请求（成功/失败/失败率）、平均首 Token（平均总耗时）、Token 消耗
// （输入/输出/缓存读）、成本（均次/每 1M Token）。
const emptyAggregateSummary: AuthorizationUsageAggregateSummary = {
  requestCount: 0,
  totalTokens: 0,
  totalCost: 0,
  successCount: 0,
  errorCount: 0,
  inputTokens: 0,
  outputTokens: 0,
  cacheReadTokens: 0,
  cacheWriteTokens: 0,
  averageFirstTokenMs: 0,
  averageDurationMs: 0
}

function usageSummaryCards(summary: AuthorizationUsageAggregateSummary): StatsSummaryCardItem[] {
  const requestCount = summary.requestCount
  const errorRatePercent = requestCount > 0 ? (summary.errorCount / requestCount) * 100 : 0
  const averageRequestCost = requestCount > 0 ? summary.totalCost / requestCount : undefined
  const costPerMillionTokens = summary.totalTokens > 0 ? (summary.totalCost / summary.totalTokens) * 1_000_000 : undefined
  const optionalCost = (value?: number) => (typeof value === 'number' && Number.isFinite(value) ? formatCost(value) : '-')
  return [
    { key: 'requests', label: '范围请求', value: formatInteger(summary.requestCount), extra: `成功 ${formatInteger(summary.successCount)} / 失败 ${formatInteger(summary.errorCount)} / 失败率 ${formatPercent(errorRatePercent)}` },
    { key: 'firstToken', label: '平均首 Token', value: formatDurationSeconds(summary.averageFirstTokenMs), extra: `平均总耗时 ${formatDurationSeconds(summary.averageDurationMs)}` },
    { key: 'tokens', label: 'Token 消耗', value: formatCompactInteger(summary.totalTokens), extra: `输入 ${formatCompactInteger(summary.inputTokens)} / 输出 ${formatCompactInteger(summary.outputTokens)} / 缓存读 ${formatCompactInteger(summary.cacheReadTokens)}` },
    { key: 'cost', label: '成本', value: formatCost(summary.totalCost), extra: `均次 ${optionalCost(averageRequestCost)} / 每 1M Token ${optionalCost(costPerMillionTokens)}` }
  ]
}

export function buildAuthorizationUserUsageSummaryCards(options: {
  summary?: AuthorizationUsageAggregateSummary
}): StatsSummaryCardItem[] {
  return usageSummaryCards(options.summary ?? emptyAggregateSummary)
}

export function buildAuthorizationTeamUsageSummaryCards(options: {
  summary?: AuthorizationUsageAggregateSummary
}): StatsSummaryCardItem[] {
  return usageSummaryCards(options.summary ?? emptyAggregateSummary)
}
