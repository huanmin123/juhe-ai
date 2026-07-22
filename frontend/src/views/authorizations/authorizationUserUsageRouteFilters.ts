import type { LocationQuery } from 'vue-router'

export interface AuthorizationUserUsageRouteFilters {
  granteeSystemAccountId?: string
  resourceId?: string
  resourceOwnerSystemAccountId?: string
  resourceType?: 'account' | 'group'
  startDate?: string
  endDate?: string
  teamId?: string
}

const authorizationUserUsageRoutePaths = new Set([
  '/authorization-user-usage',
  '/my-authorization-user-usage'
])

export function isAuthorizationUserUsageRoutePath(path: string): boolean {
  return authorizationUserUsageRoutePaths.has(path)
}

export function authorizationUserUsageRouteFilterValues(query: LocationQuery): readonly unknown[] {
  return [
    query.teamId,
    query.granteeSystemAccountId,
    query.resourceOwnerSystemAccountId,
    query.resourceId,
    query.startDate,
    query.endDate,
    query.resourceType
  ] as const
}

export function hasAuthorizationUserUsageRouteFilters(query: LocationQuery): boolean {
  return Boolean(
    singleAuthorizationUserUsageQueryValue(query.teamId)
    || singleAuthorizationUserUsageQueryValue(query.granteeSystemAccountId)
    || singleAuthorizationUserUsageQueryValue(query.resourceOwnerSystemAccountId)
    || singleAuthorizationUserUsageQueryValue(query.resourceId)
    || singleAuthorizationUserUsageQueryValue(query.startDate)
    || singleAuthorizationUserUsageQueryValue(query.endDate)
    || query.resourceType === 'account'
    || query.resourceType === 'group'
  )
}

export function authorizationUserUsageRouteFiltersFromQuery(query: LocationQuery): AuthorizationUserUsageRouteFilters {
  return {
    teamId: singleAuthorizationUserUsageQueryValue(query.teamId),
    granteeSystemAccountId: singleAuthorizationUserUsageQueryValue(query.granteeSystemAccountId),
    resourceOwnerSystemAccountId: singleAuthorizationUserUsageQueryValue(query.resourceOwnerSystemAccountId),
    resourceId: singleAuthorizationUserUsageQueryValue(query.resourceId),
    resourceType: query.resourceType === 'account' || query.resourceType === 'group' ? query.resourceType : undefined,
    startDate: singleAuthorizationUserUsageQueryValue(query.startDate),
    endDate: singleAuthorizationUserUsageQueryValue(query.endDate)
  }
}

function singleAuthorizationUserUsageQueryValue(value: unknown): string | undefined {
  if (Array.isArray(value)) return typeof value[0] === 'string' ? value[0] : undefined
  return typeof value === 'string' ? value : undefined
}
