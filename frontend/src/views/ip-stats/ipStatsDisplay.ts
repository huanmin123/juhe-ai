import type { RowActionItem } from '@/components/rowActions'
import type { ClientIpStatsRow, ClientIpStatus, ClientIpUsageSummary } from '@/types/domain'

export type IpStatsPolicyAction = 'blacklist' | 'unblock' | 'allowlist' | 'unallowlist'
export type IpStatsRowAction = 'detail' | IpStatsPolicyAction

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
  const detailAction: RowActionItem = { key: 'detail', label: '详情', icon: 'detail' }
  if (record.status === 'blacklisted') {
    return [detailAction, { key: 'unblock', label: '解封', icon: 'restore', tone: 'success', confirmTitle: '确认解除这个 IP 的封禁？', confirmOkText: '解封' }]
  }
  if (record.status === 'allowlisted') {
    return [detailAction, { key: 'unallowlist', label: '移出白名单', icon: 'disable', tone: 'warning', confirmTitle: '确认将这个 IP 移出白名单？', confirmOkText: '移出' }]
  }
  return [
    detailAction,
    { key: 'allowlist', label: '加白', icon: 'restore', tone: 'success', confirmTitle: '确认将这个 IP 加入白名单？', confirmOkText: '加白' },
    { key: 'blacklist', label: '封禁', icon: 'disable', tone: 'danger', confirmTitle: '确认封禁这个 IP？封禁后该 IP 的公开请求会被拒绝。', confirmOkText: '封禁' }
  ]
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
  if (status === 'allowlisted') return '白名单'
  if (status === 'normal') return '正常'
  return '全部'
}

export function statusColor(status: ClientIpStatus): string {
  if (status === 'blacklisted') return 'red'
  if (status === 'allowlisted') return 'blue'
  if (status === 'normal') return 'green'
  return 'default'
}
