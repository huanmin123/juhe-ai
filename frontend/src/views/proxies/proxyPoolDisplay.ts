import { formatDateTime, formatMillisecondsAsSeconds } from '@/shared/formatters'
import type { ProxyPoolMemberDetail, ProxyPoolStrategy } from '@/types/domain'

// 代理池管理页展示辅助（代理池设计 §9.1）。

export const POOL_MEMBER_HEALTH_STREAK_LIMIT = 3

export const proxyPoolStrategyOptions: Array<{ label: string; value: ProxyPoolStrategy }> = [
  { label: '轮询', value: 'round_robin' },
  { label: '权重', value: 'weighted' }
]

export function proxyPoolStrategyText(strategy: string): string {
  return proxyPoolStrategyOptions.find((option) => option.value === strategy)?.label ?? strategy
}

export function proxyPoolStrategyColor(strategy: string): string {
  return strategy === 'weighted' ? 'gold' : 'blue'
}

export function proxyPoolHealthColor(status: string): string {
  if (status === 'healthy') return 'green'
  if (status === 'degraded') return 'gold'
  if (status === 'dead') return 'red'
  return 'default'
}

export function proxyPoolHealthText(status: string): string {
  if (status === 'healthy') return '健康'
  if (status === 'degraded') return '部分可用'
  if (status === 'dead') return '已异常'
  return '未知'
}

export function proxyPoolHealthTooltip(member: Pick<ProxyPoolMemberDetail, 'poolHealthStatus' | 'poolHealthFailureStreak' | 'poolHealthCheckedAt'>): string {
  const parts = [
    `池健康：${proxyPoolHealthText(member.poolHealthStatus)}`,
    member.poolHealthStatus === 'dead' || member.poolHealthStatus === 'degraded'
      ? `连续失败 ${member.poolHealthFailureStreak}/${POOL_MEMBER_HEALTH_STREAK_LIMIT}`
      : '',
    member.poolHealthCheckedAt ? `最近池健康检测：${formatDateTime(member.poolHealthCheckedAt)}` : ''
  ].filter(Boolean)
  return parts.join('\n')
}

export function formatPoolMemberLatency(value?: number): string {
  return formatMillisecondsAsSeconds(value)
}

/** 池成员可分配判定（§6.2 可分配成员）：人工启用 + 代理启用 + 池健康未异常。 */
export function poolMemberAssignableText(member: Pick<ProxyPoolMemberDetail, 'assignable' | 'memberEnabled' | 'enabled'>): string {
  if (member.assignable) return '可分配'
  if (!member.memberEnabled) return '成员已停用'
  if (!member.enabled) return '代理已停用'
  return '不可分配'
}

export function poolMemberAssignableColor(member: Pick<ProxyPoolMemberDetail, 'assignable' | 'memberEnabled' | 'enabled'>): string {
  if (member.assignable) return 'green'
  if (!member.memberEnabled || !member.enabled) return 'default'
  return 'gold'
}

// 代理协议/检测状态/延迟沿用代理管理页的既有展示口径（同一字段来源），
// 避免池成员表复制一套颜色与文案。
export { formatLatency, proxyTypeColor, testStatusColor, testStatusText } from './proxyDisplay'
