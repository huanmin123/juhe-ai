import type { ApiKeyGroupRouteStrategy, ApiKeyRouteMode } from '@/types/domain'

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
    { title: '路由模式', key: 'routeMode', width: 160 },
    { title: '绑定分组', key: 'group', width: 220 },
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
  ...apiKeyStatusOptions
]

export const apiKeyBindingStatusOptions: Array<{ label: string; value: 'active' | 'disabled' }> = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

export const apiKeyGroupRouteStrategyOptions: Array<{ label: string; value: ApiKeyGroupRouteStrategy }> = [
  { label: '主备优先', value: 'priority_failover' },
  { label: '轮询分配', value: 'round_robin' },
  { label: '权重分配', value: 'weighted_round_robin' }
]

export const apiKeyRouteModeOptions: Array<{ label: string; value: ApiKeyRouteMode }> = [
  { label: '普通路由', value: 'normal' },
  { label: '混合智能路由', value: 'hybrid' }
]
