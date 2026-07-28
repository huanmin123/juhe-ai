import type { AccountGroupOptionSummary, GroupEditDetail, GroupListResult, GroupMutationResult, GroupOptionSummary, GroupSummary } from '@/types/domain'
import type { GroupListParams, GroupOptionParams, ListParams } from '../contracts'
import { http, unwrap } from '../http'
import { groupListParams, groupOptionParams } from '../params'

type MyGroupOptionParams = Pick<GroupOptionParams, 'ids' | 'keyword' | 'providerCode' | 'limit' | 'manageableOnly' | 'preferDefault' | 'purpose'>

export const groupsApi = {
  list: async (params?: GroupListParams) => (await unwrap<GroupListResult>(http.get('/groups', { params: groupListParams({ page: 1, pageSize: 500, ...params }) }))).items,
  listPage: (params?: GroupListParams) => unwrap<GroupListResult>(http.get('/groups', { params: groupListParams(params) })),
  detail: (id: string, params?: ListParams) => unwrap<GroupSummary>(http.get(`/groups/${id}`, { params })),
  editBasicDetail: (id: string, params?: ListParams) => unwrap<GroupEditDetail>(http.get(`/groups/${id}/edit-basic`, { params })),
  options: (params?: GroupOptionParams) => unwrap<GroupOptionSummary[]>(http.get('/groups/options', { params: groupOptionParams(params) })),
  authorizationOptions: async (params?: GroupOptionParams): Promise<GroupOptionSummary[]> => (await unwrap<Array<{ id: string; name: string; canAuthorize: boolean }>>(http.get('/groups/authorization-options', { params: groupOptionParams(params) })))
    .map(({ canAuthorize, ...option }) => ({ ...option, permissions: { canAuthorize } })),
  accountOptions: (params?: GroupOptionParams) => unwrap<AccountGroupOptionSummary[]>(http.get('/groups/account-options', { params: groupOptionParams(params) })),
  create: (payload: Record<string, unknown>, params?: ListParams) => unwrap<GroupSummary>(http.post('/groups', payload, { params })),
  update: (id: string, payload: Record<string, unknown>, params?: ListParams) => unwrap<GroupMutationResult>(http.patch(`/groups/${id}`, payload, { params })),
  returnAuthorization: (id: string, params?: ListParams) => http.post(`/groups/${id}/return-authorization`, {}, { params }),
  delete: (id: string, params?: ListParams) => http.delete(`/groups/${id}`, { params })
}

export const myGroupsApi = {
  list: async (params?: Omit<GroupListParams, 'systemAccountId'>) => (await unwrap<GroupListResult>(http.get('/my-groups', { params: groupListParams({ page: 1, pageSize: 500, ...params }, false) }))).items,
  listPage: (params?: GroupListParams) => unwrap<GroupListResult>(http.get('/my-groups', { params: groupListParams(params, false) })),
  detail: (id: string) => unwrap<GroupSummary>(http.get(`/my-groups/${id}`)),
  editBasicDetail: (id: string) => unwrap<GroupEditDetail>(http.get(`/my-groups/${id}/edit-basic`)),
  options: (params?: MyGroupOptionParams) => unwrap<GroupOptionSummary[]>(http.get('/my-groups/options', { params: groupOptionParams(params, false) })),
  authorizationOptions: async (params?: MyGroupOptionParams): Promise<GroupOptionSummary[]> => (await unwrap<Array<{ id: string; name: string; canAuthorize: boolean }>>(http.get('/my-groups/authorization-options', { params: groupOptionParams(params, false) })))
    .map(({ canAuthorize, ...option }) => ({ ...option, permissions: { canAuthorize } })),
  accountOptions: (params?: MyGroupOptionParams) => unwrap<AccountGroupOptionSummary[]>(http.get('/my-groups/account-options', { params: groupOptionParams(params, false) })),
  create: (payload: Record<string, unknown>) => unwrap<GroupSummary>(http.post('/my-groups', payload)),
  update: (id: string, payload: Record<string, unknown>) => unwrap<GroupMutationResult>(http.patch(`/my-groups/${id}`, payload)),
  returnAuthorization: (id: string) => http.post(`/my-groups/${id}/return-authorization`, {}),
  delete: (id: string) => http.delete(`/my-groups/${id}`)
}
