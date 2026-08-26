import dayjs from 'dayjs'
import type { Dayjs } from 'dayjs'

import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

import type { AuthorizationListFilterContext, AuthorizationListFilterValues } from './authorizationListFilters'
import type { AuthorizationFilters } from './authorizationPageState'
import { authorizationColumns, type AuthorizationDirectionFilter } from './authorizationTableColumns'

export const authorizationsPageSize = 50

type AuthorizationListTotalRange = [number, number]
type AuthorizationListTotalContext = {
  hasMore?: boolean
}

export function authorizationListTotalText(
  total: number,
  range?: AuthorizationListTotalRange,
  context?: AuthorizationListTotalContext
): string {
  return context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条授权，还有更多`
    : `共 ${total} 条授权`
}

export function authorizationListFilterValues(filters: AuthorizationFilters): AuthorizationListFilterValues {
  return {
    direction: filters.direction,
    sourceType: filters.sourceType,
    status: filters.status,
    resourceType: filters.resourceType,
    resourceOwnerSystemAccountId: filters.resourceOwnerSystemAccountId,
    resourceId: filters.resourceId,
    teamId: filters.teamId,
    granteeSystemAccountId: filters.granteeSystemAccountId
  }
}

export function authorizationListFilterContext(context: {
  filters: AuthorizationFilters
  keyword: string
  isManagementView: boolean
  filterResourceDisabled: boolean
}): AuthorizationListFilterContext {
  return {
    filters: authorizationListFilterValues(context.filters),
    keyword: context.keyword,
    isManagementView: context.isManagementView,
    filterResourceDisabled: context.filterResourceDisabled,
    allSystemAccountsValue
  }
}

export function authorizationVisibleColumns(context: {
  isManagementView: boolean
  direction: AuthorizationDirectionFilter
  hasReturnableInboundAuthorization: boolean
}): typeof authorizationColumns {
  const showActions = context.isManagementView || context.direction === 'outbound' || context.hasReturnableInboundAuthorization
  return authorizationColumns.filter((column) => {
    if (context.isManagementView && column.key === 'direction') return false
    if (['usageTotal', 'lastUsedAt', 'limits'].includes(String(column.key))) return false
    if (!showActions && column.key === 'actions') return false
    return true
  })
}

export function authorizationColumnStorageKey(isManagementView: boolean, direction: AuthorizationDirectionFilter): string {
  return `authorizations:${isManagementView ? 'management' : 'self'}:${direction}`
}

export function disabledAuthorizationExpireDate(date: Dayjs): boolean {
  return date.isBefore(dayjs().startOf('day'))
}
