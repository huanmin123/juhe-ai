import type { ApiKeyGroupRouteStrategy } from './types.js'

export const DEFAULT_API_KEY_GROUP_ROUTE_STRATEGY: ApiKeyGroupRouteStrategy = 'priority_failover'

const dynamicStrategies = new Set<ApiKeyGroupRouteStrategy>([
  'round_robin',
  'weighted_round_robin'
])

export function normalizeApiKeyGroupRouteStrategy(value: unknown): ApiKeyGroupRouteStrategy {
  return value === 'round_robin' || value === 'weighted_round_robin'
    ? value
    : DEFAULT_API_KEY_GROUP_ROUTE_STRATEGY
}

export function isDynamicApiKeyGroupRouteStrategy(value: unknown): boolean {
  return dynamicStrategies.has(normalizeApiKeyGroupRouteStrategy(value))
}

export function normalizeApiKeyGroupBindingWeight(value: unknown): number {
  const numeric = typeof value === 'number'
    ? value
    : typeof value === 'string'
      ? Number(value)
      : Number.NaN
  if (!Number.isFinite(numeric)) {
    return 1
  }
  return Math.min(100, Math.max(1, Math.trunc(numeric)))
}
