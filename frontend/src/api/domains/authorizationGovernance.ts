import type {
  AuthorizationTeamUsageRowsResult,
  AuthorizationTeamUsageSummary,
  AuthorizationUserUsageRowsResult,
  AuthorizationUserUsageSummary
} from '@/types/domain'
import type {
  AuthorizationTeamUsageSummaryParams,
  AuthorizationUsageRowsParams,
  AuthorizationUserUsageSummaryParams
} from '../contracts'
import { http, unwrap } from '../http'
import { stripSystemAccountParam } from '../params'

export type AuthorizationResourceType = 'account' | 'group'

// 状态词表：直接发送 → 接收方领取 → 生效；无审批、无范围快照、无待领取过期。
export type AuthorizationStatus =
  | 'pending'
  | 'active'
  | 'paused'
  | 'expired'
  | 'revoked'
  | 'returned'
  | 'declined'

export interface AuthorizationInboxSummary {
  pendingSources: number
  activeSources?: number
}

export interface AuthorizationSourceSummary {
  sourceId: string
  grantId: string
  authorizationId: string
  resourceType: AuthorizationResourceType
  resourceId: string
  resourceName: string
  resourceOwnerSystemAccountId: string
  resourceOwnerName: string
  granteeType: 'system_account' | 'team'
  teamId?: string
  teamName?: string
  sourceType: 'manual' | 'team'
  status: AuthorizationStatus
  runtimeStatus: string
  vendorCode?: string
  proxySummary?: string
  expiresAt?: string
  acceptedAt?: string
  instanceId?: string
  bound: boolean
  groupEnabled: boolean
  createdAt: string
  canAccept: boolean
  canDecline: boolean
  canReturn: boolean
}

export interface AuthorizationConnection {
  connectionPresetCode: string
  protocolSurfaceCode: string
  baseUrl: string
  endpointModes: string[]
}

export interface AuthorizationSourceDetail extends AuthorizationSourceSummary {
  connections: AuthorizationConnection[]
  supportedModels: string[]
}

export interface AuthorizationOfferSummary {
  grantId: string
  resourceType: AuthorizationResourceType
  resourceId: string
  resourceName: string
  resourceOwnerSystemAccountId?: string
  granteeType: 'system_account' | 'team'
  granteeSystemAccountId?: string
  granteeName?: string
  teamId?: string
  teamName?: string
  status: AuthorizationStatus
  statusReason?: string
  remark?: string
  expiresAt?: string
  createdBy: string
  createdByName?: string
  pendingCount: number
  activeCount: number
  acceptedAt?: string
  createdAt: string
  endedAt?: string
  endedReason?: string
}

export interface AuthorizationOfferCreatePayload {
  resourceType: AuthorizationResourceType
  resourceId: string
  granteeType: 'system_account' | 'team'
  granteeUsername?: string
  granteeSystemAccountId?: string
  teamName?: string
  teamId?: string
  remark?: string
  expiresAt?: string
}

export interface AuthorizationOfferCreateResult {
  grantId: string
  sourceId?: string
  status: AuthorizationStatus
  created: boolean
  previousStatus?: string
}

export const myAuthorizationCenterApi = {
  create: (body: AuthorizationOfferCreatePayload) =>
    unwrap<AuthorizationOfferCreateResult>(http.post('/my-authorizations', body)),
  inboxSummary: () => unwrap<AuthorizationInboxSummary>(http.get(`/my-authorizations/inbox-summary`)),
  inbound: async () => (await unwrap<{ items: AuthorizationSourceSummary[] }>(http.get('/my-authorizations', { params: { direction: 'inbound' } }))).items,
  outbound: async () => (await unwrap<{ items: AuthorizationOfferSummary[] }>(http.get('/my-authorizations', { params: { direction: 'outbound' } }))).items,
  owned: async () => (await unwrap<{ items: AuthorizationOfferSummary[] }>(http.get('/my-authorizations', { params: { direction: 'owned' } }))).items,
  sourceDetail: (sourceId: string) => unwrap<AuthorizationSourceDetail>(http.get(`/my-authorization-sources/${sourceId}`)),
  // 领取即生效：账户授权携带 targetGroupId 一步进组；分组授权直接生效。
  accept: (sourceId: string, body: { targetGroupId?: string }) =>
    unwrap<unknown>(http.post(`/my-authorization-sources/${sourceId}/accept`, body)),
  decline: (sourceId: string) => unwrap<unknown>(http.post(`/my-authorization-sources/${sourceId}/decline`)),
  return: (sourceId: string) => unwrap<unknown>(http.post(`/my-authorization-sources/${sourceId}/return`)),
  pause: (grantId: string) => unwrap<unknown>(http.post(`/my-authorizations/${grantId}/pause`)),
  resume: (grantId: string) => unwrap<unknown>(http.post(`/my-authorizations/${grantId}/resume`)),
  extend: (grantId: string, body: { expiresAt: string }) =>
    unwrap<unknown>(http.post(`/my-authorizations/${grantId}/extend`, body)),
  revoke: (grantId: string) => unwrap<unknown>(http.delete(`/my-authorizations/${grantId}`)),
  teamUsage: (params?: AuthorizationUsageRowsParams) =>
    unwrap<AuthorizationTeamUsageRowsResult>(http.get('/my-authorizations/usage/team-details', { params: stripSystemAccountParam(params) })),
  userUsage: (params?: AuthorizationUsageRowsParams) =>
    unwrap<AuthorizationUserUsageRowsResult>(http.get('/my-authorizations/usage/user-details', { params: stripSystemAccountParam(params) })),
  teamUsageSummary: (params?: AuthorizationTeamUsageSummaryParams) =>
    unwrap<AuthorizationTeamUsageSummary>(http.get('/my-authorizations/usage/team-summary', { params: stripSystemAccountParam(params) })),
  userUsageSummary: (params?: AuthorizationUserUsageSummaryParams) =>
    unwrap<AuthorizationUserUsageSummary>(http.get('/my-authorizations/usage/user-summary', { params: stripSystemAccountParam(params) }))
}

export const adminAuthorizationCenterApi = {
  create: (body: AuthorizationOfferCreatePayload, ownerSystemAccountId: string) =>
    unwrap<AuthorizationOfferCreateResult>(http.post('/authorizations', body, { params: { systemAccountId: ownerSystemAccountId } })),
  list: async (status?: string) =>
    (await unwrap<{ items: AuthorizationOfferSummary[] }>(http.get('/authorizations', { params: status ? { status } : undefined }))).items,
  pause: (grantId: string) => unwrap<unknown>(http.post(`/authorizations/${grantId}/pause`)),
  resume: (grantId: string) => unwrap<unknown>(http.post(`/authorizations/${grantId}/resume`)),
  extend: (grantId: string, body: { expiresAt: string }) =>
    unwrap<unknown>(http.post(`/authorizations/${grantId}/extend`, body)),
  revoke: (grantId: string) => unwrap<unknown>(http.delete(`/authorizations/${grantId}`)),
  teamUsage: (params?: AuthorizationUsageRowsParams) =>
    unwrap<AuthorizationTeamUsageRowsResult>(http.get('/authorizations/usage/team-details', { params })),
  userUsage: (params?: AuthorizationUsageRowsParams) =>
    unwrap<AuthorizationUserUsageRowsResult>(http.get('/authorizations/usage/user-details', { params })),
  teamUsageSummary: (params?: AuthorizationTeamUsageSummaryParams) =>
    unwrap<AuthorizationTeamUsageSummary>(http.get('/authorizations/usage/team-summary', { params })),
  userUsageSummary: (params?: AuthorizationUserUsageSummaryParams) =>
    unwrap<AuthorizationUserUsageSummary>(http.get('/authorizations/usage/user-summary', { params }))
}
