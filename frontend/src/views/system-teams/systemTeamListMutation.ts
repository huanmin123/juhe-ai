import type { SystemTeamListItem, SystemTeamMutationResult } from '@/types/domain'
import { compareServerDateTime, serverDateTimeTimestamp } from '@/shared/formatters'

export interface SystemTeamListMutationContext {
  accumulated: boolean
  hasMore: boolean
  keyword: string
  page: number
  pageSize: number
  total: number
}

export interface SystemTeamListMutationState {
  items: SystemTeamListItem[]
  requiresReload: boolean
  total: number
}

export function reconcileCreatedSystemTeam(
  items: SystemTeamListItem[],
  created: SystemTeamListItem,
  context: SystemTeamListMutationContext
): SystemTeamListMutationState {
  if (!matchesSystemTeamKeyword(created, context.keyword)) {
    return { items, requiresReload: false, total: context.total }
  }
  if (context.accumulated || context.page > 1) {
    return { items, requiresReload: true, total: context.total + 1 }
  }

  const previousLength = items.length
  const allLoaded = !context.hasMore
  const disabledIsVisible = items.some((item) => item.status === 'disabled')
  if (created.status === 'disabled' && !disabledIsVisible && !allLoaded) {
    return { items, requiresReload: false, total: context.total + 1 }
  }

  const targetLength = allLoaded && previousLength < context.pageSize
    ? previousLength + 1
    : previousLength
  const nextItems = sortSystemTeamItems([created, ...items.filter((item) => item.id !== created.id)])
    .slice(0, targetLength)
  return { items: nextItems, requiresReload: false, total: context.total + 1 }
}

export function reconcileSystemTeamPatch(
  items: SystemTeamListItem[],
  mutation: SystemTeamMutationResult,
  context: SystemTeamListMutationContext
): SystemTeamListMutationState {
  const current = items.find((item) => item.id === mutation.id)
  if (!current || isOlderRevision(mutation.updatedAt, current.updatedAt)) {
    return { items, requiresReload: !current, total: context.total }
  }
  const changedFields = new Set(mutation.changedFields)
  const patched: SystemTeamListItem = {
    ...current,
    updatedAt: mutation.updatedAt,
    ...(changedFields.has('name') && mutation.rowPatch.name !== undefined ? { name: mutation.rowPatch.name } : {}),
    ...(changedFields.has('description') ? { description: mutation.rowPatch.description ?? undefined } : {}),
    ...(changedFields.has('status') && mutation.rowPatch.status !== undefined ? { status: mutation.rowPatch.status } : {})
  }
  if (!matchesSystemTeamKeyword(patched, context.keyword)) {
    return {
      items: context.accumulated ? items.filter((item) => item.id !== patched.id) : items,
      requiresReload: !context.accumulated,
      total: Math.max(0, context.total - 1)
    }
  }
  if (context.page > 1 && !context.accumulated) {
    return { items, requiresReload: true, total: context.total }
  }
  if (current.status === 'active' && patched.status === 'disabled' && context.hasMore) {
    const otherVisibleDisabled = items.some((item) => item.id !== current.id && item.status === 'disabled')
    if (!otherVisibleDisabled) {
      return { items, requiresReload: true, total: context.total }
    }
  }
  return {
    items: sortSystemTeamItems(items.map((item) => item.id === patched.id ? patched : item)),
    requiresReload: false,
    total: context.total
  }
}

export function reconcileSystemTeamMemberMutation(
  items: SystemTeamListItem[],
  mutation: { id: string; memberCount: number; updatedAt: string },
  context: SystemTeamListMutationContext
): SystemTeamListMutationState {
  const current = items.find((item) => item.id === mutation.id)
  if (!current || isOlderRevision(mutation.updatedAt, current.updatedAt)) {
    return { items, requiresReload: false, total: context.total }
  }
  const orderingChanged = compareServerDateTime(mutation.updatedAt, current.updatedAt) > 0
  if (orderingChanged && context.page > 1 && !context.accumulated) {
    return { items, requiresReload: true, total: context.total }
  }
  const nextItems = items.map((item) => item.id === mutation.id
    ? { ...item, memberCount: mutation.memberCount, updatedAt: mutation.updatedAt }
    : item)
  return {
    items: orderingChanged ? sortSystemTeamItems(nextItems) : nextItems,
    requiresReload: false,
    total: context.total
  }
}

export function isOlderRevision(candidate: string, current: string): boolean {
  if (serverDateTimeTimestamp(candidate) === undefined) return true
  return compareServerDateTime(candidate, current) < 0
}

function matchesSystemTeamKeyword(item: Pick<SystemTeamListItem, 'name'>, keyword: string): boolean {
  const normalizedKeyword = keyword.trim()
  return !normalizedKeyword || item.name.startsWith(normalizedKeyword)
}

function sortSystemTeamItems(items: SystemTeamListItem[]): SystemTeamListItem[] {
  return [...items].sort((left, right) => {
    const statusComparison = compareBinaryText(left.status, right.status)
    if (statusComparison !== 0) return statusComparison
    const revisionComparison = compareServerDateTime(right.updatedAt, left.updatedAt)
    if (revisionComparison !== 0) return revisionComparison
    const nameComparison = compareBinaryText(left.name, right.name)
    return nameComparison !== 0 ? nameComparison : compareBinaryText(left.id, right.id)
  })
}

function compareBinaryText(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0
}
