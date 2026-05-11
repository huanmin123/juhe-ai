import type { AccountStatus, AccountType, AuthorizationResourceType, AuthorizationSourceStatus, AuthorizationSourceType, AuthorizationStatus, ProviderCode, ResourceAccessType, TeamMemberStatus, TeamStatus } from './base'
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
  joinedAt?: string
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
  memberCount?: number
  members?: SystemTeamMemberSummary[]
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

export interface AuthorizationUserUsageDetail {
  systemAccountId: string
  systemAccountName?: string
  username?: string
  requestCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
  rangeUsage?: AccountUsageSummary
  dailyUsage?: AccountUsageDailyPoint[]
  usageBuckets?: AuthorizationUsageBucket[]
}

export type AuthorizationUsageGroupBy = 'day' | 'week'

export interface AuthorizationUsageBucket extends AccountUsageSummary {
  bucketKey: string
  startDate: string
  endDate: string
}

export interface ResourceAuthorizationSummary {
  id: string
  resourceType: AuthorizationResourceType
  resourceId: string
  resourceName?: string
  resourceOwnerSystemAccountId: string
  resourceOwnerSystemAccountName?: string
  granteeSystemAccountId: string
  granteeSystemAccountName?: string
  granteeUsername?: string
  status: AuthorizationStatus
  scope: 'use'
  remark?: string
  expiresAt?: string
  limits?: RequestQuotaLimits
  modelPolicy?: Record<string, unknown>
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
  sources?: AuthorizationSourceSummary[]
  authorizationSources: AuthorizationSourceSummary[]
  usage: AccountUsageSummary
  usageBySystemAccount?: AuthorizationUserUsageDetail[]
  usageRange?: AccountUsageStatsRange
  usageGroupBy?: AuthorizationUsageGroupBy
  usageBuckets?: AuthorizationUsageBucket[]
  permissions?: {
    canEdit: boolean
    canAuthorize: boolean
  }
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
  total: number
  page: number
  pageSize: number
  statsLagSeconds: number
}
