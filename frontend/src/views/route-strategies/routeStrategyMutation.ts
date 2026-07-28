import type { RouteStrategyMutationPayload } from '@/api/domains/routeStrategies'

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

function stableValue(value: unknown): string {
  if (value === undefined) return 'undefined'
  if (value === null || typeof value !== 'object') return JSON.stringify(value) ?? String(value)
  if (Array.isArray(value)) return `[${value.map(stableValue).join(',')}]`
  const record = value as Record<string, unknown>
  return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${stableValue(record[key])}`).join(',')}}`
}
