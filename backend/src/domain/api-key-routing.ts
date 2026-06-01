import type { ApiKeyGroupRouteStrategy } from './types.js'

export const DEFAULT_API_KEY_GROUP_ROUTE_STRATEGY: ApiKeyGroupRouteStrategy = 'priority_failover'

const dynamicStrategies = new Set<ApiKeyGroupRouteStrategy>([
  'round_robin',
  'weighted_round_robin'
])

export function normalizeApiKeyGroupRouteStrategy(value: unknown): ApiKeyGroupRouteStrategy {
  if (value === undefined) return DEFAULT_API_KEY_GROUP_ROUTE_STRATEGY
  if (value === 'priority_failover' || value === 'round_robin' || value === 'weighted_round_robin') {
    return value
  }
  throw new Error('API Key 分组路由策略无效')
}

export function isDynamicApiKeyGroupRouteStrategy(value: unknown): boolean {
  return dynamicStrategies.has(normalizeApiKeyGroupRouteStrategy(value))
}

export function normalizeApiKeyGroupBindingWeight(value: unknown): number {
  if (value === undefined || value === null) {
    return 1
  }
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 100) {
    throw new Error('API Key 分组权重必须是 1-100 之间的整数')
  }
  return value
}
