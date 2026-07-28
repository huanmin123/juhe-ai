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

function sameSystemAccountEditableValue(left: unknown, right: unknown): boolean {
  if (left === right) return true
  if (!left || !right || typeof left !== 'object' || typeof right !== 'object') return false
  return JSON.stringify(left) === JSON.stringify(right)
}
