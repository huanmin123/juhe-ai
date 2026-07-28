import type {
  ApiKeyAvailabilitySchedule,
  ApiKeyMutationResult,
  ApiKeyQuotaLimits,
  ApiKeySummary
} from '@/types/domain'
import type { ApiKeyMutationPayload } from '@/api/domains/apiKeys'

export interface ApiKeyEditableSnapshot {
  name: string
  routeStrategyId?: string
  status: 'active' | 'disabled'
  expiresAt: string | null
  description: string
  quotaLimits: ApiKeyQuotaLimits
  availabilitySchedule: ApiKeyAvailabilitySchedule | null
}

export function buildApiKeyCreatePayload(
  snapshot: ApiKeyEditableSnapshot,
  options: { routeStrategyTouched: boolean }
): ApiKeyMutationPayload {
  const description = snapshot.description.trim()
  return {
    name: snapshot.name,
    ...(options.routeStrategyTouched && snapshot.routeStrategyId
      ? { routeStrategyId: snapshot.routeStrategyId }
      : {}),
    ...(snapshot.status === 'disabled' ? { status: snapshot.status } : {}),
    ...(snapshot.expiresAt ? { expiresAt: snapshot.expiresAt } : {}),
    ...(description ? { description } : {}),
    ...(Object.keys(snapshot.quotaLimits).length ? { quotaLimits: snapshot.quotaLimits } : {}),
    ...(snapshot.availabilitySchedule ? { availabilitySchedule: snapshot.availabilitySchedule } : {})
  }
}

const editableFields = [
  'name',
  'routeStrategyId',
  'status',
  'expiresAt',
  'description',
  'quotaLimits',
  'availabilitySchedule'
] as const

const rowPatchFields = [
  'revision',
  'name',
  'description',
  'keyPrefix',
  'keySuffix',
  'status',
  'routeStrategyId',
  'routeStrategyName',
  'routeStrategyMode',
  'routeStrategyStatus',
  'expiresAt',
  'quotaLimits',
  'availabilitySchedule'
] as const

export function buildApiKeyMutationPatch(
  baseline: ApiKeyEditableSnapshot,
  current: ApiKeyEditableSnapshot
): ApiKeyMutationPayload {
  const patch: ApiKeyMutationPayload = {}
  const writablePatch = patch as Record<string, unknown>
  for (const field of editableFields) {
    if (stableValue(baseline[field]) === stableValue(current[field])) continue
    writablePatch[field] = current[field]
  }
  return patch
}

export function hasApiKeyMutationChanges(patch: ApiKeyMutationPayload): boolean {
  return editableFields.some((field) => Object.prototype.hasOwnProperty.call(patch, field))
}

export function mergeApiKeyMutationResult(
  current: ApiKeySummary,
  result: ApiKeyMutationResult
): ApiKeySummary {
  if (current.id !== result.id) return current
  const next = { ...current, revision: result.revision }
  const writableNext = next as unknown as Record<string, unknown>
  const rowPatch = result.rowPatch as unknown as Record<string, unknown>
  for (const field of rowPatchFields) {
    if (!Object.prototype.hasOwnProperty.call(rowPatch, field)) continue
    const value = rowPatch[field]
    if (value === undefined) continue
    if (value === null && (field === 'description' || field === 'expiresAt' || field === 'availabilitySchedule')) {
      delete writableNext[field]
      continue
    }
    writableNext[field] = value
  }
  next.revision = result.revision
  return next
}

function stableValue(value: unknown): string {
  if (value === undefined) return 'undefined'
  if (value === null || typeof value !== 'object') return JSON.stringify(value) ?? String(value)
  if (Array.isArray(value)) return `[${value.map(stableValue).join(',')}]`
  const record = value as Record<string, unknown>
  return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${stableValue(record[key])}`).join(',')}}`
}
