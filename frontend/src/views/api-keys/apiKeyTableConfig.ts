import type { ApiKeySummary } from '@/types/domain'

export type ApiKeyStatusFilter = 'all' | 'active' | 'disabled'

export function buildApiKeyTableColumns(isManagementView: boolean): Array<Record<string, unknown>> {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '名称', dataIndex: 'name', key: 'name', width: 180 },
    { title: '密钥', key: 'key', width: 220 }
  ]
  if (isManagementView) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '策略路由', key: 'routeStrategy', width: 240 },
    { title: '调度偏好', key: 'schedulingPreference', width: 160 },
    { title: '安全策略', key: 'securityPolicy', width: 150 },
    { title: '运行状态', key: 'status', width: 120 },
    { title: '时间计划', key: 'availabilitySchedule', width: 260 },
    { title: '累计用量', key: 'usage', width: 180 },
    { title: '美元额度', key: 'quotaLimits', width: 220 },
    { title: '过期时间', dataIndex: 'expiresAt', key: 'expiresAt', width: 180 },
    { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
    { title: '操作', key: 'actions', fixed: 'right' }
  )
  return baseColumns
}

export function apiKeyColumnStorageKey(isManagementView: boolean): string {
  return isManagementView ? 'api-keys:management' : 'api-keys:self'
}

export const apiKeyStatusOptions: Array<{ label: string; value: 'active' | 'disabled' }> = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

export const apiKeyListStatusOptions: Array<{ label: string; value: ApiKeyStatusFilter }> = [
  { label: '全部状态', value: 'all' },
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

export const apiKeyBindingStatusOptions: Array<{ label: string; value: 'active' | 'disabled' }> = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

/**
 * 列表接口只接受 active/disabled/all；「启用」天然包含时段外（gate=0），
 * out_of_window 服务端虚拟筛选值已退役（时间计划闸门设计 §5.4，2026-11-07 裁决）。
 */
export function apiKeyListStatusQueryValue(
  filter: ApiKeyStatusFilter
): 'all' | 'active' | 'disabled' {
  return filter
}

export function apiKeyMatchesListStatusFilter(
  apiKey: Pick<ApiKeySummary, 'status'>,
  filter: ApiKeyStatusFilter
): boolean {
  if (filter === 'all') return true
  // 「启用」天然包含时段外（gate=0）：运行状态仍为 active（时间计划闸门设计 §5.4，2026-11-07 裁决）。
  return apiKey.status === filter
}
