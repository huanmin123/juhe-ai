import type { SystemAccountListItem, SystemAccountListResult, SystemAccountMutationResult, SystemAccountOptionSummary, SystemAccountPatchPayload, SystemAccountPrincipalSummary } from '@/types/domain'
import type { SystemAccountListParams, SystemAccountOptionsParams } from '../contracts'
import { http, unwrap } from '../http'
import { systemAccountListParams, systemAccountOptionsParams } from '../params'

export interface AuthorizationSuspensionStatus {
  suspended: boolean
  suspendedBy?: string
  reason?: string
  suspendedAt?: string
}

export const systemAccountsApi = {
  authorizationSuspension: (id: string) => unwrap<AuthorizationSuspensionStatus>(http.get(`/system-accounts/${id}/authorization-suspension`)),
  suspendAuthorization: (id: string, reason?: string) => unwrap<unknown>(http.post(`/system-accounts/${id}/authorization-suspension`, { reason: reason ?? '' })),
  unsuspendAuthorization: (id: string) => unwrap<unknown>(http.delete(`/system-accounts/${id}/authorization-suspension`)),
  list: async () => (await unwrap<SystemAccountListResult>(http.get('/system-accounts', { params: systemAccountListParams({ page: 1, pageSize: 100 }) }))).items,
  listPage: (params?: SystemAccountListParams) => unwrap<SystemAccountListResult>(http.get('/system-accounts', { params: systemAccountListParams(params) })),
  options: async (params?: SystemAccountOptionsParams): Promise<SystemAccountPrincipalSummary[]> => {
    const options = await unwrap<SystemAccountOptionSummary[]>(http.get('/system-accounts/options', { params: systemAccountOptionsParams(params) }))
    return options.map((option) => ({
      id: option.id,
      username: option.name,
      displayName: option.name,
      status: option.disabledReason ? 'disabled' : 'active'
    }))
  },
  create: (payload: Record<string, unknown>) => unwrap<SystemAccountListItem>(http.post('/system-accounts', payload)),
  update: (id: string, payload: SystemAccountPatchPayload) => unwrap<SystemAccountMutationResult>(http.patch(`/system-accounts/${id}`, payload))
}
