import type { RowActionItem } from '@/components/rowActions'

type AuthorizationUsageTableColumn = Record<string, unknown>

export const authorizationUserUsageColumns: AuthorizationUsageTableColumn[] = [
  { title: '资源名称', key: 'account', width: 220 },
  { title: '资源归属人', key: 'accountOwner', width: 180 },
  { title: '被授权用户', key: 'user', width: 230 },
  { title: '所属团队', key: 'teams', width: 180 },
  { title: '范围消耗', key: 'usage', width: 220 },
  { title: '最后使用', key: 'lastUsedAt', width: 180 }
]

export const authorizationTeamUsageDetailActions: RowActionItem[] = [
  { key: 'users', label: '查询用户明细', icon: 'detail', tone: 'info' }
]

export const authorizationTeamUsageColumns: AuthorizationUsageTableColumn[] = [
  { title: '资源名称', key: 'account', width: 220 },
  { title: '资源归属人', key: 'accountOwner', width: 180 },
  { title: '被授权团队', key: 'team', width: 240 },
  { title: '范围消耗', key: 'usage', width: 220 },
  { title: '最后使用', key: 'lastUsedAt', width: 180 },
  { title: '操作', key: 'actions', fixed: 'right' }
]
