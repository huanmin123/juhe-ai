import { formatCompactUsageAmount, formatNumber, formatUsd } from '@/shared/formatters'
import { displayGroupName } from '@/shared/groupLabelCache'
import type { AccountUsageSummary, ApiKeyAvailabilitySchedule, ApiKeyGroupBindingSummary, ApiKeyGroupRouteStrategy, ApiKeySummary } from '@/types/domain'
import { systemAccountDisplayText } from '@/utils/systemAccountFilter'
import { timeScheduleSummary, timeScheduleTagColor } from '@/views/shared/timeSchedule'

export const apiKeyScheduleLabel = 'API Key 时间计划'

export function apiKeyGroupBindings(apiKey: ApiKeySummary): ApiKeyGroupBindingSummary[] {
  return apiKey.groupBindings
}

export function apiKeyGroupBindingTagColor(binding: ApiKeyGroupBindingSummary): string {
  if (binding.status === 'disabled') return 'default'
  if (!binding.groupEnabled) return 'orange'
  return binding.priority === 1 ? 'purple' : 'blue'
}

export function apiKeyGroupBindingTagText(apiKey: ApiKeySummary, binding: ApiKeyGroupBindingSummary, index = 0): string {
  const name = displayGroupName(binding.groupName, binding.groupId)
  const suffix = binding.status === 'disabled'
    ? '停用'
    : binding.groupEnabled ? undefined : '分组不可用'
  const routeLabel = groupBindingLabelByStrategy(apiKey.groupRouteStrategy, binding, index)
  const text = suffix ? `${routeLabel}：${name}（${suffix}）` : `${routeLabel}：${name}`
  if (apiKey.groupRouteStrategy !== 'weighted_round_robin' || binding.status !== 'active') {
    return text
  }
  return typeof binding.weight === 'number' && Number.isInteger(binding.weight) && binding.weight > 0
    ? `${text} 权重 ${binding.weight}`
    : `${text}（权重数据异常）`
}

export function apiKeyGroupRouteText(apiKey: ApiKeySummary): string {
  return apiKeyGroupBindings(apiKey)
    .map((binding, index) => apiKeyGroupBindingTagText(apiKey, binding, index))
    .join(' / ')
}

function groupBindingLabelByStrategy(strategy: ApiKeyGroupRouteStrategy | undefined, binding: ApiKeyGroupBindingSummary, index: number): string {
  if (strategy === 'round_robin') return `轮询 ${index + 1}`
  if (strategy === 'weighted_round_robin') return `权重 ${index + 1}`
  return groupBindingPriorityTextByPriority(binding.priority)
}

function groupBindingPriorityTextByPriority(priority: number | undefined): string {
  const normalizedPriority = typeof priority === 'number' && Number.isFinite(priority)
    ? Math.max(1, Math.trunc(priority))
    : 1
  return normalizedPriority === 1 ? '主号池' : `备 ${normalizedPriority - 1}`
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
      ? '时间计划当前不在允许时段，API Key 已停用'
      : '时间计划当前不在允许时段，等待后台同步停用'
  ]
}

function isApiKeyScheduleInactive(apiKey: ApiKeySummary): boolean {
  return Boolean(apiKey.availabilitySchedule?.enabled && apiKey.availabilityScheduleActive === false)
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
