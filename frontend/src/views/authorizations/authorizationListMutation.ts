import type {
  AuthorizationStatus,
  ResourceAuthorizationCreateMutationResult,
  ResourceAuthorizationListItem,
  ResourceAuthorizationMutationResult,
  ResourceAuthorizationTerminalMutationResult
} from '@/types/domain'

import type { AuthorizationListFilterContext } from './authorizationListFilters'

export interface AuthorizationListMutationState {
  items: ResourceAuthorizationListItem[]
  totalDelta: number
}

interface AuthorizationMutationFilterContext extends AuthorizationListFilterContext {
  currentSystemAccountId?: string
}

export function applyAuthorizationCreateMutation(
  items: ResourceAuthorizationListItem[],
  result: ResourceAuthorizationCreateMutationResult,
  context: AuthorizationMutationFilterContext,
  currentPage: number,
  pageSize: number
): AuthorizationListMutationState {
  const existing = items.find((item) => item.id === result.item.id)
  const previous = existing ?? authorizationWithStatus(result.item, result.previousStatus)
  const matchesNow = authorizationMatchesFilters(result.item, context)
  const matchedBefore = result.created
    ? false
    : previous
      ? authorizationMatchesFilters(previous, context)
      : matchesNow
  const totalDelta = Number(matchesNow) - Number(matchedBefore)

  const withoutTarget = items.filter((item) => item.id !== result.item.id)
  const accumulated = currentPage > 1 && items.length > pageSize
  if (!matchesNow) {
    return { items: withoutTarget, totalDelta }
  }
  if (currentPage !== 1 && !accumulated && !existing) {
    return { items, totalDelta }
  }
  const windowLimit = currentPage === 1
    ? pageSize
    : accumulated
      ? currentPage * pageSize
      : undefined
  return {
    items: [...withoutTarget, result.item].sort(compareAuthorizationListItems).slice(0, windowLimit),
    totalDelta
  }
}

export function applyAuthorizationPatchMutation(
  items: ResourceAuthorizationListItem[],
  result: ResourceAuthorizationMutationResult,
  context: AuthorizationMutationFilterContext
): AuthorizationListMutationState {
  const existing = items.find((item) => item.id === result.id)
  if (!existing) return { items, totalDelta: 0 }
  const updated: ResourceAuthorizationListItem = {
    ...existing,
    status: result.status,
    expiresAt: result.expiresAt ?? undefined,
    limits: result.limits ?? undefined,
    updatedAt: result.updatedAt
  }
  if (!authorizationMatchesFilters(updated, context)) {
    return { items: items.filter((item) => item.id !== result.id), totalDelta: -1 }
  }
  return {
    items: items.map((item) => item.id === result.id ? updated : item),
    totalDelta: 0
  }
}

export function applyAuthorizationTerminalMutation(
  items: ResourceAuthorizationListItem[],
  result: ResourceAuthorizationTerminalMutationResult,
  context: AuthorizationMutationFilterContext
): AuthorizationListMutationState {
  const existing = items.find((item) => item.id === result.id)
  if (!existing) return { items, totalDelta: 0 }
  const updated = { ...existing, status: result.status, updatedAt: result.updatedAt }
  if (!authorizationMatchesFilters(updated, context)) {
    return { items: items.filter((item) => item.id !== result.id), totalDelta: -1 }
  }
  return {
    items: items.map((item) => item.id === result.id ? updated : item),
    totalDelta: 0
  }
}

export function applyAuthorizationReturnMutation(
  items: ResourceAuthorizationListItem[],
  authorizationId: string
): AuthorizationListMutationState {
  const existing = items.find((item) => item.id === authorizationId)
  if (!existing) return { items, totalDelta: 0 }
  return { items: items.filter((item) => item.id !== authorizationId), totalDelta: -1 }
}

export function authorizationMatchesFilters(
  item: ResourceAuthorizationListItem,
  context: AuthorizationMutationFilterContext
): boolean {
  const { filters, keyword, isManagementView, filterResourceDisabled, allSystemAccountsValue, currentSystemAccountId } = context
  if (filters.status !== 'all' && item.status !== filters.status) return false
  if (filters.resourceType !== 'all' && item.resourceType !== filters.resourceType) return false
  if (filters.resourceType !== 'all' && !filterResourceDisabled && filters.resourceId && item.resourceId !== filters.resourceId) return false
  if (isManagementView && filters.resourceOwnerSystemAccountId !== allSystemAccountsValue
    && item.resourceOwnerSystemAccountId !== filters.resourceOwnerSystemAccountId) return false
  if (isManagementView && filters.teamId && item.granteeTeamId !== filters.teamId) return false
  if (isManagementView && filters.granteeSystemAccountId
    && item.granteeSystemAccountId !== filters.granteeSystemAccountId) return false
  if (!isManagementView) {
    if (filters.direction === 'outbound' && item.resourceOwnerSystemAccountId !== currentSystemAccountId) return false
    if (filters.direction === 'inbound' && item.resourceOwnerSystemAccountId === currentSystemAccountId) return false
    if (filters.sourceType === 'manual' && item.granteeType !== 'system_account') return false
    if (filters.sourceType === 'team' && item.granteeType !== 'team') return false
  }
  const prefix = keyword.trim()
  if (!prefix) return true
  return [
    item.id,
    item.resourceId,
    item.remark,
    item.resourceName,
    item.resourceOwnerSystemAccountName,
    item.granteeSystemAccountName,
    item.granteeUsername,
    item.granteeTeamName
  ].some((value) => value?.startsWith(prefix))
}

function authorizationWithStatus(
  item: ResourceAuthorizationListItem,
  status?: AuthorizationStatus
): ResourceAuthorizationListItem | undefined {
  return status ? { ...item, status } : undefined
}

function compareAuthorizationListItems(left: ResourceAuthorizationListItem, right: ResourceAuthorizationListItem): number {
  const createdAtOrder = right.createdAt.localeCompare(left.createdAt)
  return createdAtOrder || right.id.localeCompare(left.id)
}
