import type { StatsSummaryCardItem } from '@/views/stats/StatsSummaryCards.vue'
import type {
  AuthorizationUsageAggregateSummary,
  AuthorizationResourceType,
  AuthorizationTeamUsageRow,
  AuthorizationUserUsageRow
} from '@/types/domain'

import {
  emptyUsageSummary,
  formatCost,
  formatDateTime,
  formatNumber,
  formatUsageAmount
} from './authorizationFormatters'

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

export function buildAuthorizationUserUsageSummaryCards(options: {
  hasMore?: boolean
  loadedCount: number
  rangeLabel: string
  summary?: AuthorizationUsageAggregateSummary
}): StatsSummaryCardItem[] {
  const summary = options.summary ?? emptyUsageSummary()
  return [
    { key: 'rows', label: '已加载明细', value: formatNumber(options.loadedCount), extra: options.hasMore ? '还有更多用户消耗' : `范围 ${options.rangeLabel}` },
    { key: 'requests', label: '范围请求', value: formatNumber(summary.requestCount), extra: `最后使用 ${formatDateTime(summary.lastUsedAt)}` },
    { key: 'tokens', label: 'Token 消耗', value: formatUsageAmount(summary.totalTokens), extra: `输入 ${formatUsageAmount(summary.inputTokens)} / 缓存写 ${formatUsageAmount(summary.cacheWriteTokens)}` },
    { key: 'cost', label: '成本', value: formatCost(summary.totalCost), extra: `最后使用 ${formatDateTime(summary.lastUsedAt)}` }
  ]
}

export function buildAuthorizationTeamUsageSummaryCards(options: {
  hasMore?: boolean
  loadedCount: number
  rangeLabel: string
  summary?: AuthorizationUsageAggregateSummary
}): StatsSummaryCardItem[] {
  const summary = options.summary ?? emptyUsageSummary()
  return [
    { key: 'rows', label: '已加载明细', value: formatNumber(options.loadedCount), extra: options.hasMore ? '还有更多团队消耗' : `范围 ${options.rangeLabel}` },
    { key: 'requests', label: '范围请求', value: formatNumber(summary.requestCount), extra: `最后使用 ${formatDateTime(summary.lastUsedAt)}` },
    { key: 'tokens', label: 'Token 消耗', value: formatUsageAmount(summary.totalTokens), extra: `输入 ${formatUsageAmount(summary.inputTokens)} / 缓存写 ${formatUsageAmount(summary.cacheWriteTokens)}` },
    { key: 'cost', label: '成本', value: formatCost(summary.totalCost), extra: `最后使用 ${formatDateTime(summary.lastUsedAt)}` }
  ]
}
