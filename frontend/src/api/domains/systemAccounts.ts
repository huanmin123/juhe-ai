import type { SystemAccountListResult, SystemAccountPrincipalSummary, SystemAccountSummary } from '@/types/domain'
import type { SystemAccountListParams, SystemAccountOptionsParams } from '../contracts'
import { http, unwrap } from '../http'
import { systemAccountListParams, systemAccountOptionsParams } from '../params'

export const systemAccountsApi = {
  list: async () => (await unwrap<SystemAccountListResult>(http.get('/system-accounts', { params: systemAccountListParams({ page: 1, pageSize: 100 }) }))).items,
  listPage: (params?: SystemAccountListParams) => unwrap<SystemAccountListResult>(http.get('/system-accounts', { params: systemAccountListParams(params) })),
  options: (params?: SystemAccountOptionsParams) => unwrap<SystemAccountPrincipalSummary[]>(http.get('/system-accounts/options', { params: systemAccountOptionsParams(params) })),
  create: (payload: Record<string, unknown>) => unwrap<SystemAccountSummary>(http.post('/system-accounts', payload)),
  update: (id: string, payload: Record<string, unknown>) => unwrap<SystemAccountSummary>(http.patch(`/system-accounts/${id}`, payload))
}
