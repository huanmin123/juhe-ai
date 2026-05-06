export type AuthorizationFilterResourceType = 'all' | 'account' | 'group'

export const authorizationColumns = [
  { title: 'AI账户名称', key: 'resource', width: 260 },
  { title: '归属人', key: 'owner', width: 180 },
  { title: '被授权用户', key: 'grantee', width: 180 },
  { title: '用量(日)', key: 'usageTotal', width: 260 },
  { title: '额度限制', key: 'limits', width: 220 },
  { title: '状态', key: 'status', width: 90 },
  { title: '授权时间', key: 'createdAt', width: 170 },
  { title: '说明', key: 'remark', width: 200 },
  { title: '操作', key: 'actions', width: 140, fixed: 'right' }
]

export const authorizationUsageDetailColumns = [
  { title: '系统账户', key: 'name', width: 220 },
  { title: '今日用量', key: 'usage', width: 280 },
  { title: '最后使用', key: 'lastUsedAt', width: 180 }
]

export const authorizationTeamUsageColumns = [
  { title: '团队', key: 'teamName', width: 180 },
  { title: '成员', key: 'memberName', width: 180 },
  { title: '今日用量', key: 'usage', width: 260 }
]

export const authorizationResourceTypeOptions: Array<{ label: string; value: AuthorizationFilterResourceType }> = [
  { label: '全部资源', value: 'all' },
  { label: 'AI账户', value: 'account' },
  { label: '分组', value: 'group' }
]

export const createAuthorizationResourceTypeOptions: Array<{ label: string; value: 'account' | 'group' }> = [
  { label: 'AI账户', value: 'account' },
  { label: '分组', value: 'group' }
]
