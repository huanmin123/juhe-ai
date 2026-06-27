import {
  hybridRoutingConfigJson,
  normalizeHybridRoutingConfig
} from './api-key-hybrid-routing.js'
import type {
  ApiKeyHybridRoutingConfig,
  RouteStrategyMode
} from './types.js'

export const DEFAULT_ROUTE_STRATEGY_MODE: RouteStrategyMode = 'normal'

export interface RouteStrategyRuntimeConfig {
  hybridRoutingConfig?: ApiKeyHybridRoutingConfig
}

export function normalizeRouteStrategyMode(value: unknown): RouteStrategyMode {
  if (value === undefined || value === null || value === '') return DEFAULT_ROUTE_STRATEGY_MODE
  if (
    value === 'normal'
    || value === 'hybrid_smart'
    || value === 'weighted'
    || value === 'failover'
    || value === 'round_robin'
  ) {
    return value
  }
  throw new Error('路由策略模式无效')
}

export function isDynamicRouteStrategyMode(value: unknown): boolean {
  const mode = normalizeRouteStrategyMode(value)
  return mode === 'round_robin' || mode === 'weighted'
}

export function routeStrategyConfigJson(config: RouteStrategyRuntimeConfig): string | null {
  const output: Record<string, unknown> = {}
  if (config.hybridRoutingConfig) {
    output.hybridRoutingConfig = JSON.parse(hybridRoutingConfigJson(config.hybridRoutingConfig) ?? 'null')
  }
  return Object.keys(output).length ? JSON.stringify(output) : null
}

export function parseRouteStrategyRuntimeConfigJson(value: string | null | undefined): RouteStrategyRuntimeConfig {
  if (!value) return {}
  try {
    const parsed = JSON.parse(value)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return {}
    }
    const record = parsed as Record<string, unknown>
    const rawHybridConfig = record.hybridRoutingConfig
    return {
      hybridRoutingConfig: rawHybridConfig ? normalizeHybridRoutingConfig(rawHybridConfig) : undefined
    }
  } catch (error) {
    if (error instanceof Error) throw error
    throw new Error('策略路由配置无效')
  }
}
