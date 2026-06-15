import type { RowActionItem } from '@/components/rowActions'
import type { ClientIpStatsRow, ClientIpStatus, ClientIpUsageSummary } from '@/types/domain'

export type IpStatsPolicyAction = 'blacklist' | 'unblock'

export const ipStatsColumns = [
  { title: 'IP', key: 'ip', width: 180, fixed: 'left', align: 'left' },
  { title: '状态', key: 'status', width: 110, align: 'left' },
  { title: '请求', key: 'requestCount', width: 120, align: 'left', sorter: true },
  { title: 'Token', key: 'totalTokens', width: 120, align: 'left', sorter: true },
  { title: '输入 Token', key: 'inputTokens', width: 120, align: 'left' },
  { title: '输出 Token', key: 'outputTokens', width: 120, align: 'left' },
  { title: '缓存 Token', key: 'cacheReadTokens', width: 120, align: 'left' },
  { title: '缓存率', key: 'cacheRate', width: 100, align: 'left' },
  { title: '缓存成本', key: 'cacheCost', width: 120, align: 'left' },
  { title: '成本', key: 'cost', width: 130, align: 'left', sorter: true },
  { title: '失败率', key: 'errorRate', width: 110, align: 'left', sorter: true },
  { title: '活跃天数', key: 'activeDays', width: 120, align: 'left', sorter: true },
  { title: '平均首 Token', key: 'averageFirstTokenMs', width: 130, align: 'left' },
  { title: '平均总耗时', key: 'averageDurationMs', width: 130, align: 'left' },
  { title: '最大总耗时', key: 'maxDurationMs', width: 130, align: 'left' },
  { title: '最后使用', key: 'lastUsedAt', width: 180, align: 'left', sorter: true },
  { title: '操作', key: 'actions', fixed: 'right', align: 'left' }
]

export function ipRowActions(record: ClientIpStatsRow): RowActionItem[] {
  if (record.status === 'blacklisted') {
    return [{ key: 'unblock', label: '解封', icon: 'restore', tone: 'success' }]
  }
  return [{ key: 'blacklist', label: '封禁', icon: 'disable', tone: 'danger' }]
}

export function cacheReadRate(usage?: ClientIpUsageSummary): number {
  const inputTokens = usage?.inputTokens ?? 0
  if (inputTokens <= 0) return 0
  return ((usage?.cacheReadTokens ?? 0) / inputTokens) * 100
}

export function clientIpLastUsedAt(record: ClientIpStatsRow): string | undefined {
  return record.lastSeenAt ?? record.rangeUsage.lastUsedAt
}

export function statusText(status: ClientIpStatus): string {
  if (status === 'blacklisted') return '已封禁'
  if (status === 'normal') return '正常'
  return '全部'
}

export function statusColor(status: ClientIpStatus): string {
  if (status === 'blacklisted') return 'red'
  if (status === 'normal') return 'green'
  return 'default'
}
