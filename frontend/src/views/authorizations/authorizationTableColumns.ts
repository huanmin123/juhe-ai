export type AuthorizationFilterResourceType = 'all' | 'account' | 'group'
export type AuthorizationDirectionFilter = 'outbound' | 'inbound'
export type AuthorizationSourceFilter = 'all' | 'manual' | 'team'

export const authorizationColumns = [
  { title: '资源名称', key: 'resource', width: 260 },
  { title: '方向', key: 'direction', width: 120 },
  { title: '归属人', key: 'owner', width: 180 },
  { title: '被授权的目标', key: 'grantee', width: 180 },
  { title: '今日', key: 'usageTotal', width: 180 },
  { title: '最后使用', key: 'lastUsedAt', width: 170 },
  { title: '额度限制', key: 'limits', width: 220 },
  { title: '状态', key: 'status', width: 90 },
  { title: '授权时间', key: 'createdAt', width: 170 },
  { title: '说明', key: 'remark', width: 200 },
  { title: '操作', key: 'actions', width: 120, fixed: 'right' }
]

export const authorizationResourceTypeOptions: Array<{ label: string; value: AuthorizationFilterResourceType }> = [
  { label: '全部内容', value: 'all' },
  { label: '单个 AI 账户', value: 'account' },
  { label: '整个分组账号池', value: 'group' }
]

export const authorizationDirectionOptions: Array<{ label: string; value: AuthorizationDirectionFilter }> = [
  { label: '我授权出去', value: 'outbound' },
  { label: '授权给我', value: 'inbound' }
]

export const authorizationSourceOptions: Array<{ label: string; value: AuthorizationSourceFilter }> = [
  { label: '全部方式', value: 'all' },
  { label: '直接授权', value: 'manual' },
  { label: '团队授权', value: 'team' }
]

export const createAuthorizationResourceTypeOptions: Array<{ label: string; value: 'account' | 'group' }> = [
  { label: '单个 AI 账户', value: 'account' },
  { label: '整个分组账号池', value: 'group' }
]
