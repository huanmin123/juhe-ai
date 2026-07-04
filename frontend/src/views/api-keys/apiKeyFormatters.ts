import { formatCompactUsageAmount, formatRequestCountTag, formatUsd } from '@/shared/formatters'
import type { AccountUsageSummary, ApiKeyAvailabilitySchedule, ApiKeySummary, RouteStrategyMode } from '@/types/domain'
import { systemAccountDisplayText } from '@/utils/systemAccountFilter'
import { timeScheduleSummary, timeScheduleTagColor } from '@/views/shared/timeSchedule'

export const apiKeyScheduleLabel = 'API Key 时间计划'

export function apiKeyRouteStrategyName(apiKey: ApiKeySummary): string {
  return apiKey.routeStrategyName || apiKey.routeStrategyId || '-'
}

export function apiKeyRouteStrategyModeText(mode?: RouteStrategyMode): string {
  if (mode === 'hybrid_smart') return '混合智能路由'
  if (mode === 'weighted') return '权重调度路由'
  if (mode === 'round_robin') return '轮询路由'
  if (mode === 'failover') return '故障回退路由'
  if (mode === 'normal') return '普通路由'
  return '未识别'
}

export function apiKeyRouteStrategyTagColor(apiKey: ApiKeySummary): string {
  if (apiKey.routeStrategyStatus === 'disabled') return 'default'
  if (apiKey.routeStrategyMode === 'hybrid_smart') return 'cyan'
  if (apiKey.routeStrategyMode === 'weighted') return 'purple'
  if (apiKey.routeStrategyMode === 'round_robin') return 'blue'
  if (apiKey.routeStrategyMode === 'failover') return 'orange'
  return 'green'
}

export function apiKeyStatusTagLabel(apiKey: ApiKeySummary): string {
  return apiKey.status === 'active' ? '启用' : '停用'
}

export function apiKeyStatusTagColor(apiKey: ApiKeySummary): string {
  return apiKey.status === 'active' ? 'green' : 'default'
}

export function apiKeyStatusTooltipLines(apiKey: ApiKeySummary): string[] {
  return apiKey.availabilitySchedule?.enabled
    ? ['已配置时间计划；计划边界会自动更新当前运行状态']
    : []
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

export function formatUsageSummary(usage: AccountUsageSummary): string {
  return `${formatRequestCountTag(usage.requestCount)} / ${formatCompactUsageAmount(usage.totalTokens)} / ${formatUsd(usage.totalCost)}`
}
