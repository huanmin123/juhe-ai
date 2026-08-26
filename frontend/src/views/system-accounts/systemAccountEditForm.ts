import type { SystemAccountListItem, SystemAccountMutationResult, SystemAccountPatchPayload, SystemAccountRole, SystemAccountStatus, UserRequestLimits } from '@/types/domain'

export interface SystemAccountEditableValues {
  displayName: string
  description: string
  role: SystemAccountRole
  status: SystemAccountStatus
  mustChangePassword: boolean
  imageGenerationEnabled: boolean
  aiAccountLimit: number | null
  requestLimits: UserRequestLimits | null
}

type SystemAccountEditablePatch = Omit<SystemAccountPatchPayload, 'expectedUpdatedAt' | 'password'>

const editableFields = [
  'displayName',
  'description',
  'role',
  'status',
  'mustChangePassword',
  'imageGenerationEnabled',
  'aiAccountLimit',
  'requestLimits'
] as const satisfies readonly (keyof SystemAccountEditableValues)[]

export function buildSystemAccountEditablePatch(
  baseline: SystemAccountEditableValues,
  current: SystemAccountEditableValues
): SystemAccountEditablePatch {
  const patch: SystemAccountEditablePatch = {}
  for (const field of editableFields) {
    if (sameSystemAccountEditableValue(baseline[field], current[field])) continue
    Object.assign(patch, { [field]: current[field] })
  }
  return patch
}

export function hasSystemAccountEditableChanges(patch: SystemAccountEditablePatch): boolean {
  return Object.keys(patch).length > 0
}

export function cloneSystemAccountEditableValues(values: SystemAccountEditableValues): SystemAccountEditableValues {
  return {
    ...values,
    requestLimits: values.requestLimits ? { ...values.requestLimits } : null
  }
}

export function mergeSystemAccountMutation(
  current: SystemAccountListItem,
  mutation: SystemAccountMutationResult
): SystemAccountListItem {
  if (compareSystemAccountRevision(mutation.updatedAt, current.editVersion) < 0) return current
  const next: SystemAccountListItem = { ...current, editVersion: mutation.updatedAt }
  if (mutation.displayName !== undefined) next.displayName = mutation.displayName
  if (Object.hasOwn(mutation, 'description')) next.description = mutation.description ?? undefined
  if (mutation.role !== undefined) next.role = mutation.role
  if (mutation.status !== undefined) next.status = mutation.status
  if (mutation.mustChangePassword !== undefined) next.mustChangePassword = mutation.mustChangePassword
  if (mutation.imageGenerationEnabled !== undefined) next.imageGenerationEnabled = mutation.imageGenerationEnabled
  if (Object.hasOwn(mutation, 'aiAccountLimit')) next.aiAccountLimit = mutation.aiAccountLimit ?? undefined
  if (Object.hasOwn(mutation, 'requestLimits')) next.requestLimits = mutation.requestLimits ?? undefined
  return next
}

export function systemAccountMatchesListKeyword(item: Pick<SystemAccountListItem, 'username' | 'displayName'>, keyword: string): boolean {
  const normalizedKeyword = keyword.trim().toLowerCase()
  if (!normalizedKeyword) return true
  return item.username.toLowerCase().startsWith(normalizedKeyword)
    || item.displayName.toLowerCase().startsWith(normalizedKeyword)
}

export function mergeSystemAccountPageItems(
  currentItems: SystemAccountListItem[],
  nextItems: SystemAccountListItem[]
): SystemAccountListItem[] {
  const existingIds = new Set(currentItems.map((item) => item.id))
  return [...currentItems, ...nextItems.filter((item) => !existingIds.has(item.id))]
}

export interface SystemAccountListMutationContext {
  accumulated: boolean
  hasMore: boolean
  keyword: string
  page: number
  pageSize: number
  total: number
}

export interface SystemAccountListMutationState {
  currentPageCount: number
  hasMore: boolean
  items: SystemAccountListItem[]
  requiresBackfill: boolean
  requiresReload: boolean
  total: number
}

export function reconcileCreatedSystemAccount(
  items: SystemAccountListItem[],
  created: SystemAccountListItem,
  context: SystemAccountListMutationContext
): SystemAccountListMutationState {
  const alreadyPresent = items.some((item) => item.id === created.id)
  if (!systemAccountMatchesListKeyword(created, context.keyword)) {
    return mutationState(items, context, {
      total: context.total,
      requiresReload: false
    })
  }
  const total = context.total + (!alreadyPresent && !context.hasMore ? 1 : 0)
  if (context.page > 1 && !context.accumulated) {
    return mutationState(items, context, { total, requiresReload: true })
  }
  const limit = context.accumulated ? context.page * context.pageSize : context.pageSize
  const reorderedItems = sortSystemAccountItems([created, ...items.filter((item) => item.id !== created.id)])
  const nextItems = reorderedItems.slice(0, limit)
  return mutationState(nextItems, context, {
    total,
    hasMore: context.hasMore || reorderedItems.length > limit,
    requiresReload: false
  })
}

export function reconcileSystemAccountMutationPage(
  items: SystemAccountListItem[],
  mutation: SystemAccountMutationResult,
  context: SystemAccountListMutationContext
): SystemAccountListMutationState {
  const current = items.find((item) => item.id === mutation.id)
  if (!current || compareSystemAccountRevision(mutation.updatedAt, current.editVersion) < 0) {
    return mutationState(items, context, { requiresReload: false })
  }
  const merged = mergeSystemAccountMutation(current, mutation)
  const remaining = items.filter((item) => item.id !== mutation.id)
  if (!systemAccountMatchesListKeyword(merged, context.keyword)) {
    return mutationState(remaining, context, {
      total: context.hasMore ? context.total : Math.max(0, context.total - 1),
      requiresBackfill: context.accumulated && context.hasMore,
      requiresReload: !context.accumulated && context.hasMore
    })
  }
  const orderingChanged = compareSystemAccountRevision(merged.editVersion, current.editVersion) > 0
  if (orderingChanged && context.page > 1 && !context.accumulated) {
    return mutationState(items, context, { requiresReload: true })
  }
  return mutationState(orderingChanged ? sortSystemAccountItems([merged, ...remaining]) : items.map((item) => item.id === merged.id ? merged : item), context, {
    requiresReload: false
  })
}

function mutationState(
  items: SystemAccountListItem[],
  context: SystemAccountListMutationContext,
  overrides: Partial<Omit<SystemAccountListMutationState, 'items' | 'currentPageCount'>>
): SystemAccountListMutationState {
  const currentPageCount = context.accumulated
    ? Math.min(context.pageSize, Math.max(0, items.length - ((context.page - 1) * context.pageSize)))
    : items.length
  return {
    items,
    currentPageCount,
    total: context.total,
    hasMore: context.hasMore,
    requiresBackfill: false,
    requiresReload: false,
    ...overrides
  }
}

function sortSystemAccountItems(items: SystemAccountListItem[]): SystemAccountListItem[] {
  return [...items].sort((left, right) => {
    const revisionOrder = compareSystemAccountRevision(right.editVersion, left.editVersion)
    return revisionOrder || compareSystemAccountRevision(right.id, left.id)
  })
}

function compareSystemAccountRevision(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0
}

function sameSystemAccountEditableValue(left: unknown, right: unknown): boolean {
  if (left === right) return true
  if (!left || !right || typeof left !== 'object' || typeof right !== 'object') return false
  return JSON.stringify(left) === JSON.stringify(right)
}
