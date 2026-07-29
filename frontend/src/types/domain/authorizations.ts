import type { AccountStatus, AccountType, AuthorizationGranteeType, AuthorizationResourceType, AuthorizationSourceStatus, AuthorizationSourceType, AuthorizationStatus, ProviderCode, ResourceAccessType, TeamMemberStatus, TeamStatus } from './base'
import type { RequestQuotaLimits } from './access'
import type { AccountUsageDailyPoint, AccountUsageStatsRange, AccountUsageSummary } from './usage-stats'

export interface SystemTeamMemberSummary {
  id: string
  teamId: string
  systemAccountId: string
  systemAccountName?: string
  systemAccountUsername?: string
  username?: string
  memberRole: 'member'
  status: TeamMemberStatus
  joinedAt: string
  removedAt?: string
  createdAt: string
  updatedAt: string
}

export interface SystemTeamSummary {
  id: string
  name: string
  description?: string
  status: TeamStatus
  createdBy: string
  createdAt: string
  updatedAt: string
  memberCount: number
  activeMemberCount: number
  members?: SystemTeamMemberSummary[]
}

export interface SystemTeamListResult {
  items: SystemTeamListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface SystemTeamListItem {
  id: string
  name: string
  description?: string
  status: TeamStatus
  memberCount: number
  createdAt: string
  updatedAt: string
}

export interface SystemTeamMutationResult {
  id: string
  changedFields: Array<'name' | 'description' | 'status'>
  rowPatch: Partial<{
    name: string
    description: string | null
    status: TeamStatus
  }>
  updatedAt: string
}

export interface SystemTeamMemberDetail {
  id: string
  systemAccountId: string
  systemAccountName?: string
  joinedAt: string
}

export interface SystemTeamDetail {
  id: string
  name: string
  description?: string
  status: TeamStatus
  memberCount: number
  members: SystemTeamMemberDetail[]
  createdAt: string
}

export type SystemTeamPrincipalSummary = Pick<SystemTeamSummary, 'id' | 'name' | 'status'>

export interface AuthorizationGranteeGroupOptionSummary {
  id: string
  name: string
}

export interface AuthorizationSourceSummary {
  id: string
  authorizationId?: string
  sourceType: AuthorizationSourceType
  sourceTeamId?: string
  sourceTeamName?: string
  status: AuthorizationSourceStatus
  activatedAt?: string
  endedAt?: string
  endedReason?: string
  createdBy?: string
  createdAt: string
  revokedBy?: string
  revokedAt?: string
  updatedAt?: string
}

export interface AuthorizationSourceListSummary {
  activeSourceCount: number
  hasManual: boolean
  hasTeam: boolean
  teamSources: Array<{
    sourceTeamId: string
    sourceTeamName?: string
  }>
}

export interface AuthorizationUserUsageDetail {
  systemAccountId: string
  systemAccountName?: string
  username?: string
  requestCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCost: number
  cacheWriteTokens: number
  cacheWrite1hTokens: number
  cacheWriteCost: number
  thinkingTokens: number
  inputImageTokens: number
  outputImageTokens: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
  rangeUsage?: AccountUsageSummary
}

export interface ResourceAuthorizationSummary {
  id: string
  resourceType: AuthorizationResourceType
  resourceId: string
  resourceName?: string
  resourceOwnerSystemAccountId: string
  resourceOwnerSystemAccountName?: string
  granteeType?: AuthorizationGranteeType
  granteeSystemAccountId?: string
  granteeSystemAccountName?: string
  granteeUsername?: string
  granteeTeamId?: string
  granteeTeamName?: string
  status: AuthorizationStatus
  scope: 'use'
  remark?: string
  expiresAt?: string
  limits?: RequestQuotaLimits
  resourceAccountExpiresAt?: string
  effectiveSourceType?: AuthorizationSourceType
  effectiveSourceTeamId?: string
  effectiveSourceTeamName?: string
  activatedAt?: string
  lastSourceChangedAt?: string
  createdAt: string
  updatedAt: string
  revokedAt?: string
  revokedReason?: string
  createdBy?: string
  revokedBy?: string
  authorizationSources?: AuthorizationSourceSummary[]
  sourceSummary?: AuthorizationSourceListSummary
  usage?: AccountUsageSummary
  lastUsedAt?: string
  usageBySystemAccount?: AuthorizationUserUsageDetail[]
  usageBySystemAccountTotal?: number
  usageBySystemAccountPage?: number
  usageBySystemAccountPageSize?: number
  usageBySystemAccountHasMore?: boolean
  usageRange?: AccountUsageStatsRange
  permissions?: {
    canEdit: boolean
    canAuthorize: boolean
  }
}

export interface ResourceAuthorizationListItem {
  id: string
  resourceType: AuthorizationResourceType
  resourceId: string
  resourceName?: string
  resourceOwnerSystemAccountId: string
  resourceOwnerSystemAccountName?: string
  granteeType?: AuthorizationGranteeType
  granteeSystemAccountId?: string
  granteeSystemAccountName?: string
  granteeUsername?: string
  granteeTeamId?: string
  granteeTeamName?: string
  status: AuthorizationStatus
  remark?: string
  expiresAt?: string
  limits?: RequestQuotaLimits
  resourceAccountExpiresAt?: string
  effectiveSourceType?: AuthorizationSourceType
  effectiveSourceTeamId?: string
  effectiveSourceTeamName?: string
  createdAt: string
  updatedAt: string
  sourceSummary?: AuthorizationSourceListSummary
  permissions?: {
    canEdit: boolean
    canAuthorize: boolean
  }
}

export interface ResourceAuthorizationMutationResult {
  id: string
  status: AuthorizationStatus
  expiresAt?: string
  limits?: RequestQuotaLimits
  updatedAt: string
}

export interface ResourceAuthorizationListResult {
  items: ResourceAuthorizationListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface AuthorizationUsageRowSummary {
  requestCount: number
  totalTokens: number
  totalCost: number
}

export interface AuthorizationUsageAggregateSummary extends AuthorizationUsageRowSummary {
  inputTokens: number
  cacheWriteTokens: number
  lastUsedAt?: string
}

export interface AuthorizationTeamUsageRow {
  id: string
  teamId: string
  teamName: string
  resourceType?: AuthorizationResourceType
  resourceId?: string
  resourceName?: string
  accountOwnerSystemAccountId?: string
  accountOwnerSystemAccountName?: string
  usage: AuthorizationUsageRowSummary
  lastUsedAt?: string
}

export interface AuthorizationTeamUsageRowsResult {
  range: AccountUsageStatsRange
  rows: AuthorizationTeamUsageRow[]
  total: number
  page: number
  pageSize: number
  hasMore: boolean
}

export interface AuthorizationTeamUsageSummary {
  range: AccountUsageStatsRange
  summary: AuthorizationUsageAggregateSummary
}

export interface AuthorizationUserUsageRow {
  id: string
  userName: string
  username?: string
  teamNames?: string[]
  resourceType?: AuthorizationResourceType
  resourceName?: string
  accountOwnerSystemAccountName?: string
  usage: AuthorizationUsageRowSummary
  lastUsedAt?: string
}

export interface AuthorizationUserUsageRowsResult {
  range: AccountUsageStatsRange
  rows: AuthorizationUserUsageRow[]
  total: number
  page: number
  pageSize: number
  hasMore: boolean
}

export interface AuthorizationUserUsageSummary {
  range: AccountUsageStatsRange
  summary: AuthorizationUsageAggregateSummary
}

export interface AccountUsageStatsRow {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  ownerSystemAccountId: string
  ownerSystemAccountName?: string
  providerCode: ProviderCode
  name: string
  type: AccountType
  status: AccountStatus
  accessType?: ResourceAccessType
  rangeUsage: AccountUsageSummary
  dailyUsage: AccountUsageDailyPoint[]
  authorizationUsageAvailable: boolean
  authorizationCount: number
  authorizationTeamCount: number
}

export type AccountUsageStatsOption = Pick<AccountUsageStatsRow,
  | 'id'
  | 'systemAccountId'
  | 'systemAccountName'
  | 'ownerSystemAccountId'
  | 'ownerSystemAccountName'
  | 'providerCode'
  | 'name'
  | 'type'
  | 'status'
  | 'accessType'
> & { providerName: string }

export interface AccountUsageStatsOverview {
  range: AccountUsageStatsRange
  summary: AccountUsageSummary
  rows: AccountUsageStatsRow[]
  defaultTrendAccountIds: string[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export type AccountUsageStatsListResult = Omit<AccountUsageStatsOverview, 'summary'>
