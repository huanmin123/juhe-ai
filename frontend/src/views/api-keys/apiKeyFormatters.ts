import { formatCompactUsageAmount, formatRequestCountTag, formatUsd } from '@/shared/formatters'
import type { ApiKeyAvailabilitySchedule, ApiKeySummary, ApiKeyUsageListSummary, RouteStrategyMode } from '@/types/domain'
import { systemAccountDisplayText } from '@/utils/systemAccountFilter'
import { timeScheduleSummary, timeScheduleTagColor } from '@/views/shared/timeSchedule'

export const apiKeyScheduleLabel = 'API Key 时间计划'

export function apiKeyRouteStrategyName(apiKey: ApiKeySummary): string {
  return apiKey.routeStrategyName || apiKey.routeStrategyId || '-'
}

export function apiKeySchedulingPreferenceText(apiKey: ApiKeySummary): string {
  return apiKey.schedulingPreferenceName?.trim() || '-'
}

export function apiKeyRouteStrategyModeText(mode?: RouteStrategyMode): string {
  if (mode === 'weighted') return '权重调度路由'
  if (mode === 'round_robin') return '轮询路由'
  if (mode === 'failover') return '故障回退路由'
  if (mode === 'normal') return '普通路由'
  return '未识别'
}

export function apiKeyRouteStrategyTagColor(apiKey: ApiKeySummary): string {
  if (apiKey.routeStrategyStatus === 'disabled') return 'default'
  if (apiKey.routeStrategyMode === 'weighted') return 'purple'
  if (apiKey.routeStrategyMode === 'round_robin') return 'blue'
  if (apiKey.routeStrategyMode === 'failover') return 'orange'
  return 'green'
}

export function apiKeyIsScheduleOutOfWindow(apiKey: Pick<ApiKeySummary, 'status' | 'availabilityScheduleGate'>): boolean {
  return apiKey.status === 'active' && apiKey.availabilityScheduleGate === 0
}

export interface ApiKeyStatusTagInfo {
  color: string
  label: string
}

/**
 * API Key 运行状态标签（时间计划闸门设计 §5.4/§6）：`active + gate=0` 时段外显示
 * 「非时间段」，不改写运行状态、不显示为停用；其余沿用现有状态文案。
 */
export function apiKeyStatusTag(apiKey: Pick<ApiKeySummary, 'status' | 'availabilityScheduleGate'>): ApiKeyStatusTagInfo {
  if (apiKeyIsScheduleOutOfWindow(apiKey)) return { color: 'blue', label: '非时间段' }
  return { color: apiKeyStatusTagColor(apiKey), label: apiKeyStatusTagLabel(apiKey) }
}

export function apiKeyStatusTagLabel(apiKey: Pick<ApiKeySummary, 'status'>): string {
  return apiKey.status === 'active' ? '启用' : '停用'
}

export function apiKeyStatusTagColor(apiKey: Pick<ApiKeySummary, 'status'>): string {
  return apiKey.status === 'active' ? 'green' : 'default'
}

export function apiKeyStatusTooltipLines(apiKey: ApiKeySummary): string[] {
  const lines: string[] = []
  if (apiKeyIsScheduleOutOfWindow(apiKey)) {
    lines.push('当前不在可用时段，到达下一个时间计划边界后自动恢复；运行状态仍是启用')
  }
  if (apiKey.availabilitySchedule?.enabled) {
    // 时间计划不再写 status：计划边界只切换可用时段（非时间段），人工停用不会被计划自动恢复。
    lines.push('已配置时间计划；计划边界只切换可用时段（非时间段），不改写运行状态')
  }
  return lines
}

export function apiKeyScheduleSummary(schedule?: ApiKeyAvailabilitySchedule): string {
  return timeScheduleSummary(schedule, {
    label: apiKeyScheduleLabel
  })
}

export function apiKeyScheduleTagColor(apiKey: ApiKeySummary): string {
  return timeScheduleTagColor(apiKey.availabilitySchedule, {
    label: apiKeyScheduleLabel
  })
}

export function formatKeyPreview(apiKey: Pick<ApiKeySummary, 'keyPrefix' | 'keySuffix'>): string {
  return maskSecretPreview(apiKey.keyPrefix, apiKey.keySuffix, '密钥未返回')
}

export function keyDisplayTitle(apiKey: Pick<ApiKeySummary, 'keyPrefix' | 'keySuffix'>): string {
  return apiKey.keyPrefix ? '列表仅显示密钥标识，点击复制按钮复制完整密钥' : '密钥未返回'
}

function maskSecretPreview(prefix: string | undefined, suffix: string | undefined, fallback: string): string {
  const head = prefix?.slice(0, 8) ?? ''
  const tail = suffix?.slice(-8) ?? ''
  if (head && tail) return `${head}...${tail}`
  if (head) return `${head}...`
  return fallback
}

export function apiKeySystemAccountText(apiKey: ApiKeySummary): string {
  return systemAccountDisplayText(apiKey)
}

export function formatUsageSummary(usage: ApiKeyUsageListSummary): string {
  return `${formatRequestCountTag(usage.requestCount)} / ${formatCompactUsageAmount(usage.totalTokens)} / ${formatUsd(usage.totalCost)}`
}
