import type {
  AuthorizationResourceType,
  AuthorizationTeamUsageRowsResult,
  AuthorizationTeamUsageSummary,
  AuthorizationUserUsageRowsResult,
  AuthorizationUserUsageSummary,
  RequestQuotaLimits,
  ResourceAuthorizationCreateMutationResult,
  ResourceAuthorizationListResult,
  ResourceAuthorizationMutationResult,
  ResourceAuthorizationSummary,
  ResourceAuthorizationTerminalMutationResult
} from '@/types/domain'
import type {
  AuthorizationListParams,
  AuthorizationScopeParams,
  AuthorizationTeamUsageSummaryParams,
  AuthorizationUsageRowsParams,
  AuthorizationUsageParams,
  AuthorizationUserUsageSummaryParams
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
  expectedUpdatedAt: string
  status?: 'active' | 'paused'
  expiresAt?: string | null
  limits?: RequestQuotaLimits | null
}

type AuthorizationExpirePayload = {
  expectedUpdatedAt: string
  expiresAt?: string | null
  limits?: RequestQuotaLimits | null
}

type AuthorizationTerminalPayload = {
  expectedUpdatedAt: string
}

export const authorizationsApi = {
  list: async (params?: AuthorizationListParams) => (await unwrap<ResourceAuthorizationListResult>(http.get('/authorizations', { params: boundedAuthorizationListParams(params) }))).items,
  listPage: (params?: AuthorizationListParams) => unwrap<ResourceAuthorizationListResult>(http.get('/authorizations', { params })),
  detail: (id: string, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationSummary>(http.get(`/authorizations/${id}`, { params })),
  create: (payload: AuthorizationCreatePayload, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationCreateMutationResult>(http.post('/authorizations', payload, { params })),
  update: (id: string, payload: AuthorizationUpdatePayload, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationMutationResult>(http.patch(`/authorizations/${id}`, payload, { params })),
  updateExpire: (id: string, payload: AuthorizationExpirePayload, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationMutationResult>(http.patch(`/authorizations/${id}/expire`, payload, { params })),
  revoke: (id: string, payload: AuthorizationTerminalPayload, params?: AuthorizationScopeParams) => unwrap<ResourceAuthorizationTerminalMutationResult>(http.delete(`/authorizations/${id}`, { data: payload, params })),
  returnAuthorization: (id: string, payload: AuthorizationTerminalPayload, params?: AuthorizationScopeParams) => http.delete(`/authorizations/${id}/return`, { data: payload, params }),
  usage: (id: string, params?: AuthorizationUsageParams) => unwrap<ResourceAuthorizationSummary>(http.get(`/authorizations/${id}/usage`, { params })),
  teamUsage: (params?: AuthorizationUsageRowsParams) => unwrap<AuthorizationTeamUsageRowsResult>(http.get('/authorizations/usage/team-details', { params })),
  userUsage: (params?: AuthorizationUsageRowsParams) => unwrap<AuthorizationUserUsageRowsResult>(http.get('/authorizations/usage/user-details', { params })),
  teamUsageSummary: (params?: AuthorizationTeamUsageSummaryParams) => unwrap<AuthorizationTeamUsageSummary>(http.get('/authorizations/usage/team-summary', { params })),
  userUsageSummary: (params?: AuthorizationUserUsageSummaryParams) => unwrap<AuthorizationUserUsageSummary>(http.get('/authorizations/usage/user-summary', { params }))
}

export const myAuthorizationsApi = {
  list: async (params?: AuthorizationListParams) => (await unwrap<ResourceAuthorizationListResult>(http.get('/my-authorizations', { params: boundedAuthorizationListParams(stripSystemAccountParam(params)) }))).items,
  listPage: (params?: AuthorizationListParams) => unwrap<ResourceAuthorizationListResult>(http.get('/my-authorizations', { params: stripSystemAccountParam(params) })),
  detail: (id: string) => unwrap<ResourceAuthorizationSummary>(http.get(`/my-authorizations/${id}`)),
  create: (payload: AuthorizationCreatePayload) => unwrap<ResourceAuthorizationCreateMutationResult>(http.post('/my-authorizations', payload)),
  update: (id: string, payload: AuthorizationUpdatePayload) => unwrap<ResourceAuthorizationMutationResult>(http.patch(`/my-authorizations/${id}`, payload)),
  updateExpire: (id: string, payload: AuthorizationExpirePayload) => unwrap<ResourceAuthorizationMutationResult>(http.patch(`/my-authorizations/${id}/expire`, payload)),
  revoke: (id: string, payload: AuthorizationTerminalPayload) => unwrap<ResourceAuthorizationTerminalMutationResult>(http.delete(`/my-authorizations/${id}`, { data: payload })),
  returnAuthorization: (id: string, payload: AuthorizationTerminalPayload) => http.delete(`/my-authorizations/${id}/return`, { data: payload }),
  usage: (id: string, params?: AuthorizationUsageParams) => unwrap<ResourceAuthorizationSummary>(http.get(`/my-authorizations/${id}/usage`, { params: stripSystemAccountParam(params) })),
  teamUsage: (params?: AuthorizationUsageRowsParams) => unwrap<AuthorizationTeamUsageRowsResult>(http.get('/my-authorizations/usage/team-details', { params: stripSystemAccountParam(params) })),
  userUsage: (params?: AuthorizationUsageRowsParams) => unwrap<AuthorizationUserUsageRowsResult>(http.get('/my-authorizations/usage/user-details', { params: stripSystemAccountParam(params) })),
  teamUsageSummary: (params?: AuthorizationTeamUsageSummaryParams) => unwrap<AuthorizationTeamUsageSummary>(http.get('/my-authorizations/usage/team-summary', { params: stripSystemAccountParam(params) })),
  userUsageSummary: (params?: AuthorizationUserUsageSummaryParams) => unwrap<AuthorizationUserUsageSummary>(http.get('/my-authorizations/usage/user-summary', { params: stripSystemAccountParam(params) }))
}
