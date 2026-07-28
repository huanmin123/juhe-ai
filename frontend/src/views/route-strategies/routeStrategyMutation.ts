import type { RouteStrategyMutationPayload } from '@/api/domains/routeStrategies'
import type { RouteStrategyListItem, RouteStrategyMutationResult } from '@/types/domain'

const editableFields = [
  'name',
  'description',
  'mode',
  'status',
  'groupBindings',
  'normalRoutingConfig',
  'hybridRoutingConfig'
] as const

export function buildRouteStrategyMutationPatch(
  baseline: RouteStrategyMutationPayload,
  current: RouteStrategyMutationPayload
): RouteStrategyMutationPayload {
  const patch: RouteStrategyMutationPayload = {}
  const writablePatch = patch as Record<string, unknown>
  for (const field of editableFields) {
    if (stableValue(baseline[field]) === stableValue(current[field])) continue
    writablePatch[field] = current[field]
  }
  return patch
}

export function hasRouteStrategyMutationChanges(payload: RouteStrategyMutationPayload): boolean {
  return editableFields.some((field) => Object.prototype.hasOwnProperty.call(payload, field))
}

export function mergeRouteStrategyMutationResult(
  current: RouteStrategyListItem,
  mutation: RouteStrategyMutationResult
): RouteStrategyListItem {
  if (current.id !== mutation.id) return current
  const patch = mutation.rowPatch
  const next = { ...current }
  if (patch.name !== undefined) next.name = patch.name
  if (Object.prototype.hasOwnProperty.call(patch, 'description')) {
    if (patch.description === null || patch.description === undefined) delete next.description
    else next.description = patch.description
  }
  if (patch.mode !== undefined) next.mode = patch.mode
  if (patch.status !== undefined) next.status = patch.status
  if (Object.prototype.hasOwnProperty.call(patch, 'normalRoutingConfig')) {
    if (patch.normalRoutingConfig === null || patch.normalRoutingConfig === undefined) delete next.normalRoutingConfig
    else next.normalRoutingConfig = patch.normalRoutingConfig
  }
  if (patch.bindingCount !== undefined) next.bindingCount = patch.bindingCount
  if (patch.groupBindingPreview !== undefined) next.groupBindingPreview = [...patch.groupBindingPreview]
  if (patch.updatedAt !== undefined) next.updatedAt = patch.updatedAt
  return next
}

function stableValue(value: unknown): string {
  if (value === undefined) return 'undefined'
  if (value === null || typeof value !== 'object') return JSON.stringify(value) ?? String(value)
  if (Array.isArray(value)) return `[${value.map(stableValue).join(',')}]`
  const record = value as Record<string, unknown>
  return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${stableValue(record[key])}`).join(',')}}`
}
