import { http, unwrap } from '../http'

export type AuthorizationResourceType = 'account' | 'group'

export type AuthorizationStatus =
  | 'pending_owner_approval'
  | 'pending'
  | 'pending_team_approval'
  | 'pending_member_acceptance'
  | 'active'
  | 'paused'
  | 'expired'
  | 'revoked'
  | 'returned'
  | 'declined'

export interface AuthorizationInboxSummary {
  pendingSources: number
  needsReviewSources: number
  pendingBindings: number
  teamApprovals: number
  ownerApprovals: number
  unreadNotifications: number
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
  status: 'pending' | 'active' | 'dormant' | 'needs_review' | 'revoked' | 'returned' | 'declined' | 'expired'
  runtimeStatus: string
  vendorCode?: string
  connectionHosts: string[]
  supportedModelCount: number
  proxySummary?: string
  limits?: Record<string, unknown>
  expiresAt?: string
  pendingExpiresAt?: string
  acceptedAt?: string
  needsReviewReason?: string
  needsReviewDetail?: unknown
  observedSourceRevision: string
  revisionVersion: number
  instanceId?: string
  bound: boolean
  groupEnabled: boolean
  snapshotRevision?: string
  createdAt: string
  canAccept: boolean
  canDecline: boolean
  canReturn: boolean
  canBind: boolean
  canUnbind: boolean
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

export interface AuthorizationScopeAccount {
  accountId: string
  accountName?: string
  sourceRevision: string
  revisionVersion: number
  acceptedAt?: string
}

export interface AuthorizationScopeDiff {
  sourceId: string
  groupId: string
  snapshotRevision?: string
  snapshotMissing: boolean
  snapshotMode?: string
  enabled: boolean
  currentCount: number
  snapshotCount: number
  added: AuthorizationScopeAccount[]
  removed: AuthorizationScopeAccount[]
  revisionChanged: AuthorizationScopeAccount[]
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
  pendingExpiresAt?: string
  createdBy: string
  createdByName?: string
  ownerApprovalPending: boolean
  sourceCount: number
  pendingCount: number
  activeCount: number
  dormantCount: number
  needsReviewCount: number
  returnedCount: number
  declinedCount: number
  teamApprovedAt?: string
  ownerApprovedAt?: string
  acceptedAt?: string
  createdAt: string
  endedAt?: string
  endedReason?: string
  legacyExpiryUnknown?: boolean
}

export interface AuthorizationTeamOfferSummary extends AuthorizationOfferSummary {
  resourceOwnerName: string
  memberCount: number
  acceptedMembers: number
  declinedMembers: number
}

export interface AuthorizationBlock {
  id: string
  blockerSystemAccountId: string
  blockedSystemAccountId?: string
  blockedResourceType?: string
  blockedResourceId?: string
  reason?: string
  createdAt: string
}

export interface AuthorizationNotification {
  id: string
  type: string
  grantId?: string
  sourceId?: string
  payload?: unknown
  readAt?: string
  createdAt: string
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
  limits?: Record<string, unknown>
}

export interface AuthorizationOfferCreateResult {
  grantId: string
  sourceId?: string
  status: AuthorizationStatus
  ownerApprovalRequired: boolean
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
  ownerPending: async () => (await unwrap<{ items: AuthorizationOfferSummary[] }>(http.get('/my-authorizations', { params: { direction: 'owner_pending' } }))).items,
  sourceDetail: (sourceId: string) => unwrap<AuthorizationSourceDetail>(http.get(`/my-authorization-sources/${sourceId}`)),
  accept: (sourceId: string, body: { expectedRevision: string; expectedRevisionVersion: number; retainFallback?: boolean }) =>
    unwrap<unknown>(http.post(`/my-authorization-sources/${sourceId}/accept`, body)),
  decline: (sourceId: string) => unwrap<unknown>(http.post(`/my-authorization-sources/${sourceId}/decline`)),
  return: (sourceId: string) => unwrap<unknown>(http.post(`/my-authorization-sources/${sourceId}/return`)),
  block: (sourceId: string, body: { scope: 'grantor' | 'resource'; returnExisting?: boolean; reason?: string }) =>
    unwrap<unknown>(http.post(`/my-authorization-sources/${sourceId}/block`, body)),
  bind: (sourceId: string) => unwrap<unknown>(http.post(`/my-authorization-sources/${sourceId}/bind`)),
  unbind: (sourceId: string) => unwrap<unknown>(http.post(`/my-authorization-sources/${sourceId}/unbind`)),
  scopeDiff: (sourceId: string) => unwrap<AuthorizationScopeDiff>(http.get(`/my-authorization-sources/${sourceId}/scope-diff`)),
  refreshScope: (sourceId: string, body: {
    expectedSnapshotRevision: string
    reviewedAdded: Array<{ accountId: string; sourceRevision: string; revisionVersion: number }>
    rebuildMissingSnapshot?: boolean
  }) => unwrap<AuthorizationScopeDiff>(http.post(`/my-authorization-sources/${sourceId}/refresh-scope`, body)),
  reconfirmScopeAccount: (sourceId: string, accountId: string, body: { expectedRevision: string; expectedRevisionVersion: number }) =>
    unwrap<AuthorizationScopeDiff>(http.post(`/my-authorization-sources/${sourceId}/accounts/${accountId}/reconfirm`, body)),
  pause: (grantId: string) => unwrap<unknown>(http.post(`/my-authorizations/${grantId}/pause`)),
  resume: (grantId: string) => unwrap<unknown>(http.post(`/my-authorizations/${grantId}/resume`)),
  extend: (grantId: string, body: { expiresAt: string; confirmExpiredFromStatus?: string }) =>
    unwrap<unknown>(http.post(`/my-authorizations/${grantId}/extend`, body)),
  revoke: (grantId: string) => unwrap<unknown>(http.delete(`/my-authorizations/${grantId}`)),
  ownerApprove: (grantId: string) => unwrap<unknown>(http.post(`/my-authorizations/${grantId}/owner-approve`)),
  ownerDecline: (grantId: string) => unwrap<unknown>(http.post(`/my-authorizations/${grantId}/owner-decline`)),
  reinvite: (grantId: string, memberId: string) => unwrap<{ sourceId: string }>(http.post(`/my-authorizations/${grantId}/reinvite`, { memberId })),
  blocks: async () => (await unwrap<{ items: AuthorizationBlock[] }>(http.get('/my-authorization-blocks'))).items,
  deleteBlock: (blockId: string) => unwrap<unknown>(http.delete(`/my-authorization-blocks/${blockId}`)),
  notifications: async (page = 1, pageSize = 20) =>
    unwrap<{ items: AuthorizationNotification[]; total: number }>(http.get(`/my-authorizations/notifications`, { params: { page, pageSize } })),
  markNotificationRead: (id: string) => unwrap<unknown>(http.post(`/my-authorizations/notifications/${id}/read`)),
  teamOffers: async (teamId: string) =>
    (await unwrap<{ items: AuthorizationTeamOfferSummary[] }>(http.get(`/my-teams/${teamId}/authorization-offers`))).items,
  approveTeamOffer: (teamId: string, grantId: string) =>
    unwrap<unknown>(http.post(`/my-teams/${teamId}/authorization-offers/${grantId}/approve`)),
  declineTeamOffer: (teamId: string, grantId: string) =>
    unwrap<unknown>(http.post(`/my-teams/${teamId}/authorization-offers/${grantId}/decline`))
}

export const adminAuthorizationCenterApi = {
  create: (body: AuthorizationOfferCreatePayload, ownerSystemAccountId: string) =>
    unwrap<AuthorizationOfferCreateResult>(http.post('/authorizations', body, { params: { systemAccountId: ownerSystemAccountId } })),
  list: async (status?: string) =>
    (await unwrap<{ items: AuthorizationOfferSummary[] }>(http.get('/authorizations', { params: status ? { status } : undefined }))).items,
  pause: (grantId: string) => unwrap<unknown>(http.post(`/authorizations/${grantId}/pause`)),
  resume: (grantId: string) => unwrap<unknown>(http.post(`/authorizations/${grantId}/resume`)),
  extend: (grantId: string, body: { expiresAt: string; confirmExpiredFromStatus?: string }) =>
    unwrap<unknown>(http.post(`/authorizations/${grantId}/extend`, body)),
  revoke: (grantId: string) => unwrap<unknown>(http.delete(`/authorizations/${grantId}`)),
  blocks: async () => (await unwrap<{ items: AuthorizationBlock[] }>(http.get(`/authorizations/blocks`))).items,
  teamOffers: async (teamId: string) =>
    (await unwrap<{ items: AuthorizationTeamOfferSummary[] }>(http.get(`/system-teams/${teamId}/authorization-offers`))).items,
  approveTeamOffer: (teamId: string, grantId: string) =>
    unwrap<unknown>(http.post(`/system-teams/${teamId}/authorization-offers/${grantId}/approve`)),
  declineTeamOffer: (teamId: string, grantId: string) =>
    unwrap<unknown>(http.post(`/system-teams/${teamId}/authorization-offers/${grantId}/decline`))
}
