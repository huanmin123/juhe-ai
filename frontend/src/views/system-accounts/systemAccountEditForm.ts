import type { SystemAccountListItem, SystemAccountMutationResult, SystemAccountPatchPayload, SystemAccountRole, SystemAccountStatus, UserRequestLimits } from '@/types/domain'

export interface SystemAccountEditableValues {
  displayName: string
  description: string
  role: SystemAccountRole
  status: SystemAccountStatus
  mustChangePassword: boolean
  imageGenerationEnabled: boolean
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
  const next: SystemAccountListItem = { ...current, editVersion: mutation.updatedAt }
  if (mutation.displayName !== undefined) next.displayName = mutation.displayName
  if (Object.hasOwn(mutation, 'description')) next.description = mutation.description ?? undefined
  if (mutation.role !== undefined) next.role = mutation.role
  if (mutation.status !== undefined) next.status = mutation.status
  if (mutation.mustChangePassword !== undefined) next.mustChangePassword = mutation.mustChangePassword
  if (mutation.imageGenerationEnabled !== undefined) next.imageGenerationEnabled = mutation.imageGenerationEnabled
  if (Object.hasOwn(mutation, 'requestLimits')) next.requestLimits = mutation.requestLimits ?? undefined
  return next
}

export function systemAccountMatchesListKeyword(item: Pick<SystemAccountListItem, 'username' | 'displayName'>, keyword: string): boolean {
  const normalizedKeyword = keyword.trim().toLowerCase()
  if (!normalizedKeyword) return true
  return item.username.toLowerCase().startsWith(normalizedKeyword)
    || item.displayName.toLowerCase().startsWith(normalizedKeyword)
}

export type SystemAccountMutationPageDisposition = 'not_found' | 'moved_to_first' | 'relocated_to_first_page' | 'filtered_out'

export function reconcileSystemAccountMutationPage(
  items: SystemAccountListItem[],
  mutation: SystemAccountMutationResult,
  options: { keyword: string; page: number; pageSize: number }
): { items: SystemAccountListItem[]; disposition: SystemAccountMutationPageDisposition } {
  const current = items.find((item) => item.id === mutation.id)
  if (!current) return { items, disposition: 'not_found' }
  const merged = mergeSystemAccountMutation(current, mutation)
  const remaining = items.filter((item) => item.id !== mutation.id)
  if (!systemAccountMatchesListKeyword(merged, options.keyword)) {
    return { items: remaining, disposition: 'filtered_out' }
  }
  const accumulatedPages = items.length > options.pageSize
  if (options.page > 1 && !accumulatedPages) {
    return { items: remaining, disposition: 'relocated_to_first_page' }
  }
  return { items: [merged, ...remaining], disposition: 'moved_to_first' }
}

function sameSystemAccountEditableValue(left: unknown, right: unknown): boolean {
  if (left === right) return true
  if (!left || !right || typeof left !== 'object' || typeof right !== 'object') return false
  return JSON.stringify(left) === JSON.stringify(right)
}
