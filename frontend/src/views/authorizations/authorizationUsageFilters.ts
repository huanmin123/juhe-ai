import type { AccountSelection } from '@/shared/accountLabelCache'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import type { AuthorizationFilterResourceType } from './authorizationTableColumns'

export type AuthorizationUsageResourceFilters = {
  resourceOwnerSystemAccountId: string
  resourceType: AuthorizationFilterResourceType
  resourceId?: string
  resourceAccount?: AccountSelection
  resourceGroup?: GroupSelection
}

export type AuthorizationUserUsageFilters = {
  teamId?: string
  team?: PrincipalSelection
  granteeSystemAccountId?: string
  granteeSystemAccount?: PrincipalSelection
  resourceOwnerSystemAccount?: PrincipalSelection
} & AuthorizationUsageResourceFilters

export type AuthorizationTeamUsageFilters = {
  teamId?: string
  team?: PrincipalSelection
  resourceOwnerSystemAccount?: PrincipalSelection
} & AuthorizationUsageResourceFilters

type UsageFilterCountContext = {
  dateRangeExplicit: boolean
  resourceGroupDisabled: boolean
  selectedResourceOwnerSystemAccountId?: string
}

type AuthorizationUsageAdvancedFilters = Pick<
  AuthorizationUserUsageFilters | AuthorizationTeamUsageFilters,
  'resourceId' | 'resourceType'
>

export function defaultAuthorizationUserUsageFilters(): AuthorizationUserUsageFilters {
  return {
    resourceOwnerSystemAccountId: allSystemAccountsValue,
    resourceOwnerSystemAccount: undefined,
    resourceType: 'all',
    resourceId: undefined,
    resourceAccount: undefined,
    team: undefined,
    teamId: undefined,
    granteeSystemAccount: undefined,
    granteeSystemAccountId: undefined
  }
}

export function defaultAuthorizationTeamUsageFilters(): AuthorizationTeamUsageFilters {
  return {
    resourceOwnerSystemAccountId: allSystemAccountsValue,
    resourceOwnerSystemAccount: undefined,
    resourceType: 'all',
    resourceId: undefined,
    resourceAccount: undefined,
    team: undefined,
    teamId: undefined
  }
}

export function countAuthorizationUserUsageActiveFilters(
  filters: AuthorizationUserUsageFilters,
  context: UsageFilterCountContext
): number {
  return countBaseAuthorizationUsageActiveFilters(filters, context)
    + (filters.granteeSystemAccountId ? 1 : 0)
}

export function countAuthorizationTeamUsageActiveFilters(
  filters: AuthorizationTeamUsageFilters,
  context: UsageFilterCountContext
): number {
  return countBaseAuthorizationUsageActiveFilters(filters, context)
}

export function countAuthorizationUsageAdvancedFilters(
  filters: AuthorizationUsageAdvancedFilters,
  resourceGroupDisabled: boolean
): number {
  let count = 0
  if (filters.resourceType !== 'all') count += 1
  if (!resourceGroupDisabled && filters.resourceId) count += 1
  return count
}

function countBaseAuthorizationUsageActiveFilters(
  filters: Pick<
    AuthorizationUserUsageFilters | AuthorizationTeamUsageFilters,
    'resourceId' | 'resourceType' | 'teamId'
  >,
  context: UsageFilterCountContext
): number {
  let count = 0
  if (filters.teamId) count += 1
  if (context.selectedResourceOwnerSystemAccountId) count += 1
  if (filters.resourceType !== 'all') count += 1
  if (!context.resourceGroupDisabled && filters.resourceId) count += 1
  if (context.dateRangeExplicit) count += 1
  return count
}
