import type { LocationQuery } from 'vue-router'

import type { AccountSelection } from '@/shared/accountLabelCache'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import type {
  AuthorizationDirectionFilter,
  AuthorizationFilterResourceType,
  AuthorizationSourceFilter,
  AuthorizationStatusFilter
} from './authorizationTableColumns'

export interface AuthorizationFilters {
  direction: AuthorizationDirectionFilter
  sourceType: AuthorizationSourceFilter
  status: AuthorizationStatusFilter
  resourceType: AuthorizationFilterResourceType
  resourceOwnerSystemAccountId: string
  resourceOwnerSystemAccount?: PrincipalSelection
  resourceId?: string
  resourceAccount?: AccountSelection
  resourceGroup?: GroupSelection
  teamId?: string
  team?: PrincipalSelection
  granteeSystemAccountId?: string
  granteeSystemAccount?: PrincipalSelection
}

export interface AuthorizationsPageState {
  filters: AuthorizationFilters
  keywordFilter: string
  pagination?: { current: number; pageSize: number }
}

export interface AuthorizationRouteFilters {
  resourceType?: 'account' | 'group'
  resourceId?: string
  status: AuthorizationStatusFilter
  resourceOwnerSystemAccountId?: string
  teamId?: string
  granteeSystemAccountId?: string
}

export function createDefaultAuthorizationsPageState(pageSize: number): AuthorizationsPageState {
  return {
    filters: {
      direction: 'outbound',
      sourceType: 'all',
      status: 'active',
      resourceType: 'all',
      resourceOwnerSystemAccountId: allSystemAccountsValue,
      resourceOwnerSystemAccount: undefined,
      resourceId: undefined,
      resourceAccount: undefined,
      resourceGroup: undefined,
      teamId: undefined,
      team: undefined,
      granteeSystemAccountId: undefined,
      granteeSystemAccount: undefined
    },
    keywordFilter: '',
    pagination: { current: 1, pageSize }
  }
}

export function sanitizeAuthorizationsPageState(value: unknown, fallback: AuthorizationsPageState, pageSize: number): AuthorizationsPageState {
  const state = value as Partial<AuthorizationsPageState>
  const filters = state.filters && typeof state.filters === 'object'
    ? state.filters as Partial<AuthorizationFilters> & { direction?: unknown; sourceType?: unknown }
    : {}
  const pagination = state.pagination && typeof state.pagination === 'object'
    ? state.pagination as Partial<{ current: number; pageSize: number }>
    : {}
  const keywordFilter = typeof state.keywordFilter === 'string' ? state.keywordFilter : fallback.keywordFilter
  return {
    filters: {
      ...fallback.filters,
      ...filters,
      direction: filters.direction === 'inbound' ? 'inbound' : 'outbound',
      sourceType: filters.sourceType === 'manual' || filters.sourceType === 'team' ? filters.sourceType : 'all',
      status: normalizeAuthorizationStatusFilter(filters.status)
    },
    keywordFilter,
    pagination: {
      current: positiveIntegerOrFallback(pagination.current, fallback.pagination?.current ?? 1),
      pageSize: positiveIntegerOrFallback(pagination.pageSize, fallback.pagination?.pageSize ?? pageSize)
    }
  }
}

export function authorizationRouteFilterValues(query: LocationQuery): readonly unknown[] {
  return [
    query.resourceType,
    query.resourceId,
    query.status,
    query.resourceOwnerSystemAccountId,
    query.teamId,
    query.granteeSystemAccountId
  ] as const
}

export function hasAuthorizationRouteFilters(query: LocationQuery): boolean {
  return authorizationRouteFilterValues(query).some((value) => value !== undefined)
}

export function authorizationFiltersFromRouteQuery(query: LocationQuery): AuthorizationRouteFilters {
  return {
    resourceType: query.resourceType === 'group' ? 'group' : query.resourceType === 'account' ? 'account' : undefined,
    resourceId: typeof query.resourceId === 'string' ? query.resourceId : undefined,
    status: normalizeAuthorizationStatusFilter(query.status),
    resourceOwnerSystemAccountId: typeof query.resourceOwnerSystemAccountId === 'string' ? query.resourceOwnerSystemAccountId : undefined,
    teamId: typeof query.teamId === 'string' ? query.teamId : undefined,
    granteeSystemAccountId: typeof query.granteeSystemAccountId === 'string' ? query.granteeSystemAccountId : undefined
  }
}

export function normalizeAuthorizationStatusFilter(value: unknown): AuthorizationStatusFilter {
  return value === 'active' || value === 'paused' || value === 'expired' || value === 'revoked' || value === 'returned' || value === 'all'
    ? value
    : 'all'
}

function positiveIntegerOrFallback(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.trunc(value) : fallback
}
