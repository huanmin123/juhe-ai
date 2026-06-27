import { formatCompactUsageAmount, formatNumber, formatUsd } from '@/shared/formatters'
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
  return apiKey.status === 'active' && !isApiKeyScheduleInactive(apiKey) ? '启用' : '停用'
}

export function apiKeyStatusTagColor(apiKey: ApiKeySummary): string {
  return apiKey.status === 'active' && !isApiKeyScheduleInactive(apiKey) ? 'green' : 'default'
}

export function apiKeyStatusTooltipLines(apiKey: ApiKeySummary): string[] {
  if (!isApiKeyScheduleInactive(apiKey)) return []
  return [
    apiKey.status === 'disabled'
      ? 'API Key 已停用；时间计划派生状态当前为停用'
      : '时间计划派生状态当前为停用，可提前启用到下一次计划边界'
  ]
}

export function isApiKeyScheduleInactive(apiKey: ApiKeySummary): boolean {
  return Boolean(apiKey.availabilitySchedule?.enabled && apiKey.availabilityScheduleActive === false)
}

export function isApiKeyScheduleActive(apiKey: ApiKeySummary): boolean {
  return Boolean(apiKey.availabilitySchedule?.enabled && apiKey.availabilityScheduleActive === true)
}

export function apiKeyScheduleSummary(schedule?: ApiKeyAvailabilitySchedule, active?: boolean): string {
  return timeScheduleSummary(schedule, {
    active,
    label: apiKeyScheduleLabel,
    showActiveState: true
  })
}

export function apiKeyScheduleTagColor(apiKey: ApiKeySummary): string {
  return timeScheduleTagColor(apiKey.availabilitySchedule, {
    active: apiKey.availabilityScheduleActive,
    label: apiKeyScheduleLabel,
    showActiveState: true
  })
}

export function formatKeyPreview(apiKey: Pick<ApiKeySummary, 'key' | 'keyPrefix' | 'keySuffix'>): string {
  return maskSecretPreview(apiKey.key, apiKey.keyPrefix, apiKey.keySuffix, '密钥未返回')
}

export function keyDisplayTitle(apiKey: Pick<ApiKeySummary, 'key' | 'keyPrefix' | 'keySuffix'>): string {
  if (apiKey.key) return apiKey.key
  return apiKey.keyPrefix ? '列表仅显示密钥标识，点击复制按钮复制完整密钥' : '密钥未返回'
}

function maskSecretPreview(value: string | undefined, prefix: string | undefined, suffix: string | undefined, fallback: string): string {
  if (value) {
    return value.length > 16 ? `${value.slice(0, 8)}...${value.slice(-8)}` : value
  }
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
  return `${formatNumber(usage.requestCount)}req / ${formatCompactUsageAmount(usage.totalTokens)} / ${formatUsd(usage.totalCost)}`
}
