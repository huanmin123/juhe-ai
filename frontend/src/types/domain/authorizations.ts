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
  items: SystemTeamSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export type SystemTeamPrincipalSummary = Pick<SystemTeamSummary, 'id' | 'name' | 'status'>

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
  authorizationSources: AuthorizationSourceSummary[]
  usage: AccountUsageSummary
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

export interface ResourceAuthorizationListResult {
  items: ResourceAuthorizationSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface AuthorizationTeamUsageRow {
  id: string
  teamId: string
  teamName: string
  status: TeamStatus
  resourceType?: AuthorizationResourceType
  resourceId?: string
  resourceName?: string
  accountId?: string
  accountName?: string
  accountOwnerSystemAccountId?: string
  accountOwnerSystemAccountName?: string
  usage: AccountUsageSummary
  lastUsedAt?: string
}

export interface AuthorizationTeamUsageOverview {
  range: AccountUsageStatsRange
  summary: AccountUsageSummary
  rows: AuthorizationTeamUsageRow[]
  teamCount: number
  total: number
  page: number
  pageSize: number
  hasMore: boolean
}

export interface AuthorizationUserUsageRow {
  id: string
  systemAccountId: string
  userName: string
  username?: string
  teamNames?: string[]
  resourceType?: AuthorizationResourceType
  resourceId?: string
  resourceName?: string
  accountId?: string
  accountName?: string
  accountOwnerSystemAccountId?: string
  accountOwnerSystemAccountName?: string
  sourceLabels: string[]
  usage: AccountUsageSummary
  lastUsedAt?: string
}

export interface AuthorizationUserUsageOverview {
  range: AccountUsageStatsRange
  summary: AccountUsageSummary
  rows: AuthorizationUserUsageRow[]
  userCount: number
  total: number
  page: number
  pageSize: number
  hasMore: boolean
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
