import type {
  ExternalIntegrationSourceListItem,
  ExternalIntegrationSourceStatus
} from '@/types/domain'

export interface ExternalSourceListMutationContext {
  accumulated: boolean
  hasMore: boolean
  keyword: string
  page: number
  pageSize: number
  pageUpperBound: number
  status: ExternalIntegrationSourceStatus | 'all'
}

export interface ExternalSourceListMutationState {
  hasMore: boolean
  items: ExternalIntegrationSourceListItem[]
  page: number
  pageUpperBound: number
  requiresReload: boolean
}

export function reconcileCreatedExternalSource(
  items: ExternalIntegrationSourceListItem[],
  created: ExternalIntegrationSourceListItem,
  context: ExternalSourceListMutationContext
): ExternalSourceListMutationState {
  if (!matchesExternalSourceFilters(created, context)) return unchanged(items, context)

  const alreadyPresent = items.some((item) => item.id === created.id)
  if (context.page > 1 && !context.accumulated) {
    return { ...unchanged(items, context), requiresReload: true }
  }

  const limit = context.accumulated ? context.page * context.pageSize : context.pageSize
  const candidates = [created, ...items.filter((item) => item.id !== created.id)]
    .sort(compareExternalSourcesByListOrder)
  const overflowedLoadedWindow = !alreadyPresent && candidates.length > limit
  const nextItems = candidates.slice(0, limit)
  const hasMore = context.hasMore || overflowedLoadedWindow
  return {
    items: nextItems,
    page: context.page,
    pageUpperBound: visibleWindowUpperBound(nextItems.length, context, hasMore),
    hasMore,
    requiresReload: false
  }
}

export function reconcileDeletedExternalSource(
  items: ExternalIntegrationSourceListItem[],
  id: string,
  context: ExternalSourceListMutationContext
): ExternalSourceListMutationState {
  if (!items.some((item) => item.id === id)) {
    return { ...unchanged(items, context), requiresReload: true }
  }

  const nextItems = items.filter((item) => item.id !== id)
  if (context.page > 1 && !context.accumulated && nextItems.length === 0) {
    return {
      items: nextItems,
      page: context.page - 1,
      pageUpperBound: Math.max(0, context.pageUpperBound - 1),
      hasMore: context.hasMore,
      requiresReload: true
    }
  }

  const requiresReload = context.hasMore
  return {
    items: nextItems,
    page: context.page,
    pageUpperBound: requiresReload
      ? Math.max(0, context.pageUpperBound - 1)
      : visibleWindowUpperBound(nextItems.length, context, false),
    hasMore: context.hasMore,
    requiresReload
  }
}

export function reconcilePatchedExternalSource(
  items: ExternalIntegrationSourceListItem[],
  patched: ExternalIntegrationSourceListItem,
  context: ExternalSourceListMutationContext
): ExternalSourceListMutationState {
  if (!items.some((item) => item.id === patched.id)) {
    return { ...unchanged(items, context), requiresReload: true }
  }
  if (context.page > 1 && !context.accumulated) {
    return { ...unchanged(items, context), requiresReload: true }
  }

  const remaining = items.filter((item) => item.id !== patched.id)
  if (!matchesExternalSourceFilters(patched, context)) {
    const requiresReload = context.hasMore
    return {
      items: remaining,
      page: context.page,
      pageUpperBound: Math.max(0, context.pageUpperBound - 1),
      hasMore: context.hasMore,
      requiresReload
    }
  }

  const limit = context.accumulated ? context.page * context.pageSize : context.pageSize
  const nextItems = [patched, ...remaining]
    .sort(compareExternalSourcesByListOrder)
    .slice(0, limit)
  return {
    items: nextItems,
    page: context.page,
    pageUpperBound: visibleWindowUpperBound(nextItems.length, context, context.hasMore),
    hasMore: context.hasMore,
    requiresReload: false
  }
}

export function matchesExternalSourceFilters(
  item: Pick<ExternalIntegrationSourceListItem, 'name' | 'status'>,
  context: Pick<ExternalSourceListMutationContext, 'keyword' | 'status'>
): boolean {
  if (context.status !== 'all' && item.status !== context.status) return false
  const keyword = context.keyword.trim()
  return !keyword || item.name.toLowerCase().startsWith(keyword.toLowerCase())
}

function unchanged(
  items: ExternalIntegrationSourceListItem[],
  context: ExternalSourceListMutationContext
): ExternalSourceListMutationState {
  return {
    items,
    page: context.page,
    pageUpperBound: context.pageUpperBound,
    hasMore: context.hasMore,
    requiresReload: false
  }
}

function visibleWindowUpperBound(
  itemCount: number,
  context: ExternalSourceListMutationContext,
  hasMore: boolean
): number {
  const offset = context.accumulated ? 0 : (context.page - 1) * context.pageSize
  return offset + itemCount + (hasMore ? 1 : 0)
}

function compareExternalSourcesByListOrder(
  left: ExternalIntegrationSourceListItem,
  right: ExternalIntegrationSourceListItem
): number {
  const updatedAtOrder = right.updatedAt.localeCompare(left.updatedAt)
  return updatedAtOrder || right.id.localeCompare(left.id)
}
