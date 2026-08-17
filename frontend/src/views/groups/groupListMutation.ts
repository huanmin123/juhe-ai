import type { GroupListItem } from '@/types/domain'
import { compareServerDateTime } from '@/shared/formatters'

export interface GroupListMutationContext {
  accumulated: boolean
  hasMore: boolean
  page: number
  pageSize: number
  total: number
}

export interface GroupListMutationState {
  currentPageCount: number
  hasMore: boolean
  items: GroupListItem[]
  requiresReload: boolean
  total: number
}

export function reconcileCreatedGroup(
  items: GroupListItem[],
  created: GroupListItem,
  context: GroupListMutationContext
): GroupListMutationState {
  const alreadyPresent = items.some((item) => item.id === created.id)
  const total = context.total + (!alreadyPresent && !context.hasMore ? 1 : 0)
  if (context.page > 1 && !context.accumulated) {
    return {
      currentPageCount: items.length,
      hasMore: context.hasMore,
      items,
      requiresReload: true,
      total
    }
  }

  const limit = context.accumulated ? context.page * context.pageSize : context.pageSize
  const reorderedItems = [created, ...items.filter((item) => item.id !== created.id)]
    .sort(compareGroupsByListOrder)
  const nextItems = reorderedItems.slice(0, limit)
  return {
    currentPageCount: Math.min(context.pageSize, Math.max(0, nextItems.length - ((context.page - 1) * context.pageSize))),
    hasMore: context.hasMore || reorderedItems.length > limit,
    items: nextItems,
    requiresReload: false,
    total
  }
}

function compareGroupsByListOrder(left: GroupListItem, right: GroupListItem): number {
  const updatedAtOrder = compareServerDateTime(right.updatedAt, left.updatedAt)
  return updatedAtOrder || right.id.localeCompare(left.id)
}
