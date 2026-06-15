import type { AccountGroupOptionSummary, GroupListResult, GroupOptionSummary, GroupSummary } from '@/types/domain'
import type { GroupListParams, GroupOptionParams, ListParams } from '../contracts'
import { http, unwrap } from '../http'
import { groupListParams, groupOptionParams } from '../params'

type MyGroupOptionParams = Pick<GroupOptionParams, 'ids' | 'keyword' | 'providerCode' | 'limit' | 'manageableOnly' | 'preferDefault'>

export const groupsApi = {
  list: async (params?: GroupListParams) => (await unwrap<GroupListResult>(http.get('/groups', { params: groupListParams({ page: 1, pageSize: 500, ...params }) }))).items,
  listPage: (params?: GroupListParams) => unwrap<GroupListResult>(http.get('/groups', { params: groupListParams(params) })),
  options: (params?: GroupOptionParams) => unwrap<GroupOptionSummary[]>(http.get('/groups/options', { params: groupOptionParams(params) })),
  accountOptions: (params?: GroupOptionParams) => unwrap<AccountGroupOptionSummary[]>(http.get('/groups/account-options', { params: groupOptionParams(params) })),
  create: (payload: Record<string, unknown>, params?: ListParams) => unwrap<GroupSummary>(http.post('/groups', payload, { params })),
  update: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<GroupSummary>(http.patch(`/groups/${id}`, payload, { params })),
  returnAuthorization: (id: string, params?: ListParams) => http.post(`/groups/${id}/return-authorization`, {}, { params }),
  delete: (id: string, params?: ListParams) => http.delete(`/groups/${id}`, { params })
}

export const myGroupsApi = {
  list: async (params?: Omit<GroupListParams, 'systemAccountId'>) => (await unwrap<GroupListResult>(http.get('/my-groups', { params: groupListParams({ page: 1, pageSize: 500, ...params }, false) }))).items,
  listPage: (params?: GroupListParams) => unwrap<GroupListResult>(http.get('/my-groups', { params: groupListParams(params, false) })),
  options: (params?: MyGroupOptionParams) => unwrap<GroupOptionSummary[]>(http.get('/my-groups/options', { params: groupOptionParams(params, false) })),
  accountOptions: (params?: MyGroupOptionParams) => unwrap<AccountGroupOptionSummary[]>(http.get('/my-groups/account-options', { params: groupOptionParams(params, false) })),
  create: (payload: Record<string, unknown>) => unwrap<GroupSummary>(http.post('/my-groups', payload)),
  update: (id: string, payload: Record<string, unknown>) => unwrap<GroupSummary>(http.patch(`/my-groups/${id}`, payload)),
  returnAuthorization: (id: string) => http.post(`/my-groups/${id}/return-authorization`, {}),
  delete: (id: string) => http.delete(`/my-groups/${id}`)
}
