import type { ApiKeySummary } from '@/types/domain'

export type ApiKeyStatusFilter = 'all' | 'active' | 'disabled' | 'out_of_window'

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
  ...apiKeyStatusOptions,
  // 时间计划闸门（设计 §5.4）：时段外是启用状态的子集，独立筛选值。
  { label: '非时间段', value: 'out_of_window' }
]

export const apiKeyBindingStatusOptions: Array<{ label: string; value: 'active' | 'disabled' }> = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

/**
 * 列表接口接受 active/disabled/out_of_window/all；「非时间段」自 2026-11-03 起
 * 下沉为服务端虚拟筛选值（等价 status='active' AND availability_schedule_gate=0，
 * 时间计划闸门设计 §5.4），避免只过滤当前页造成跨页假阴性。
 */
export function apiKeyListStatusQueryValue(
  filter: ApiKeyStatusFilter
): 'all' | 'active' | 'disabled' | 'out_of_window' {
  return filter
}

export function apiKeyMatchesListStatusFilter(
  apiKey: Pick<ApiKeySummary, 'status' | 'availabilityScheduleGate'>,
  filter: ApiKeyStatusFilter
): boolean {
  if (filter === 'all') return true
  if (filter === 'out_of_window') return apiKey.status === 'active' && apiKey.availabilityScheduleGate === 0
  return apiKey.status === filter
}
