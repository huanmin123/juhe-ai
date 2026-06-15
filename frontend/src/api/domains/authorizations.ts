import type {
  AuthorizationResourceType,
  AuthorizationTeamUsageOverview,
  AuthorizationUserUsageOverview,
  RequestQuotaLimits,
  ResourceAuthorizationListResult,
  ResourceAuthorizationSummary
} from '@/types/domain'
import type {
  AuthorizationListParams,
  AuthorizationScopeParams,
  AuthorizationUsageOverviewParams,
  AuthorizationUsageParams
} from '../contracts'
import { http, unwrap } from '../http'
import { boundedAuthorizationListParams, stripSystemAccountParam } from '../params'

type AuthorizationCreatePayload = {
  resourceType: AuthorizationResourceType
  resourceId: string
  granteeType: 'system_account' | 'team'
  granteeId: string
  targetGroupId?: string
  remark?: string
  expiresAt?: string
  limits?: RequestQuotaLimits
}

type AuthorizationUpdatePayload = {
  status?: 'active' | 'paused'
  expiresAt?: string | null
  limits?: RequestQuotaLimits | null
}

type AuthorizationExpirePayload = {
  expiresAt: string | null
  limits?: RequestQuotaLimits | null
}

export const authorizationsApi = {
  list: async (params?: AuthorizationListParams) => (await unwrap<ResourceAuthorizationListResult>(http.get('/authorizations', { params: boundedAuthorizationListParams(params) }))).items,
  listPage: (params?: AuthorizationListParams) => unwrap<ResourceAuthorizationListResult>(http.get('/authorizations', { params })),
  create: (payload: AuthorizationCreatePayload, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationSummary>(http.post('/authorizations', payload, { params })),
  update: (id: string, payload: AuthorizationUpdatePayload, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationSummary>(http.patch(`/authorizations/${id}`, payload, { params })),
  updateExpire: (id: string, payload: AuthorizationExpirePayload, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationSummary>(http.patch(`/authorizations/${id}/expire`, payload, { params })),
  revoke: (id: string, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationSummary>(http.delete(`/authorizations/${id}`, { params })),
  returnAuthorization: (id: string, params?: AuthorizationScopeParams) => http.delete(`/authorizations/${id}/return`, { params }),
  usage: (id: string, params?: AuthorizationUsageParams) => unwrap<ResourceAuthorizationSummary>(http.get(`/authorizations/${id}/usage`, { params })),
  teamUsage: (params?: AuthorizationUsageOverviewParams) => unwrap<AuthorizationTeamUsageOverview>(http.get('/authorizations/usage/team-details', { params })),
  userUsage: (params?: AuthorizationUsageOverviewParams) => unwrap<AuthorizationUserUsageOverview>(http.get('/authorizations/usage/user-details', { params }))
}

export const myAuthorizationsApi = {
  list: async (params?: AuthorizationListParams) => (await unwrap<ResourceAuthorizationListResult>(http.get('/my-authorizations', { params: boundedAuthorizationListParams(stripSystemAccountParam(params)) }))).items,
  listPage: (params?: AuthorizationListParams) => unwrap<ResourceAuthorizationListResult>(http.get('/my-authorizations', { params: stripSystemAccountParam(params) })),
  create: (payload: AuthorizationCreatePayload) => unwrap<ResourceAuthorizationSummary>(http.post('/my-authorizations', payload)),
  update: (id: string, payload: AuthorizationUpdatePayload) => unwrap<ResourceAuthorizationSummary>(http.patch(`/my-authorizations/${id}`, payload)),
  updateExpire: (id: string, payload: AuthorizationExpirePayload) => unwrap<ResourceAuthorizationSummary>(http.patch(`/my-authorizations/${id}/expire`, payload)),
  revoke: (id: string) => unwrap<ResourceAuthorizationSummary>(http.delete(`/my-authorizations/${id}`)),
  returnAuthorization: (id: string) => http.delete(`/my-authorizations/${id}/return`),
  usage: (id: string, params?: AuthorizationUsageParams) => unwrap<ResourceAuthorizationSummary>(http.get(`/my-authorizations/${id}/usage`, { params: stripSystemAccountParam(params) })),
  teamUsage: (params?: AuthorizationUsageOverviewParams) => unwrap<AuthorizationTeamUsageOverview>(http.get('/my-authorizations/usage/team-details', { params: stripSystemAccountParam(params) })),
  userUsage: (params?: AuthorizationUsageOverviewParams) => unwrap<AuthorizationUserUsageOverview>(http.get('/my-authorizations/usage/user-details', { params: stripSystemAccountParam(params) }))
}
