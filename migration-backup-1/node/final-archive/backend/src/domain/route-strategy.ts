import {
  hybridRoutingConfigJson,
  normalizeHybridRoutingConfig
} from './api-key-hybrid-routing.js'
import type {
  ApiKeyHybridRoutingConfig,
  RouteStrategyNormalRoutingConfig,
  RouteStrategyNormalSchedulingPreference,
  RouteStrategySpeedFirstConfig,
  RouteStrategyMode
} from './types.js'

export const DEFAULT_ROUTE_STRATEGY_MODE: RouteStrategyMode = 'normal'
export const DEFAULT_NORMAL_SCHEDULING_PREFERENCE: RouteStrategyNormalSchedulingPreference = 'cost_first'
export const DEFAULT_SPEED_FIRST_FIRST_BYTE_DEADLINE_MS = 30_000
export const DEFAULT_SPEED_FIRST_SLOW_TRIGGER_COUNT = 3
export const DEFAULT_SPEED_FIRST_SLOW_WINDOW_SECONDS = 120
export const DEFAULT_SPEED_FIRST_RECOVERY_SUCCESS_COUNT = 3
export const DEFAULT_SPEED_FIRST_PROBE_INTERVAL_SECONDS = 30
export const DEFAULT_SPEED_FIRST_DEGRADED_TTL_SECONDS = 300
export const DEFAULT_SPEED_FIRST_MAX_FIRST_BYTE_RETRIES_PER_REQUEST = 2

export interface RouteStrategyRuntimeConfig {
  normalRoutingConfig?: RouteStrategyNormalRoutingConfig
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
  if (config.normalRoutingConfig) {
    const normalRoutingConfig = normalizeNormalRoutingConfig(config.normalRoutingConfig)
    if (normalRoutingConfig.schedulingPreference !== DEFAULT_NORMAL_SCHEDULING_PREFERENCE) {
      output.normalRoutingConfig = normalRoutingConfig
    }
  }
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
    const rawNormalConfig = record.normalRoutingConfig
    const rawHybridConfig = record.hybridRoutingConfig
    return {
      normalRoutingConfig: rawNormalConfig ? normalizeNormalRoutingConfig(rawNormalConfig) : undefined,
      hybridRoutingConfig: rawHybridConfig ? normalizeHybridRoutingConfig(rawHybridConfig) : undefined
    }
  } catch (error) {
    if (error instanceof Error) throw error
    throw new Error('策略路由配置无效')
  }
}

export function defaultNormalRoutingConfig(): RouteStrategyNormalRoutingConfig {
  return {
    schedulingPreference: 'cost_first'
  }
}

export function defaultSpeedFirstConfig(): RouteStrategySpeedFirstConfig {
  return {
    slowTriggerCount: DEFAULT_SPEED_FIRST_SLOW_TRIGGER_COUNT,
    slowWindowSeconds: DEFAULT_SPEED_FIRST_SLOW_WINDOW_SECONDS,
    recoverySuccessCount: DEFAULT_SPEED_FIRST_RECOVERY_SUCCESS_COUNT,
    probeIntervalSeconds: DEFAULT_SPEED_FIRST_PROBE_INTERVAL_SECONDS,
    degradedTtlSeconds: DEFAULT_SPEED_FIRST_DEGRADED_TTL_SECONDS,
    maxFirstByteRetriesPerRequest: DEFAULT_SPEED_FIRST_MAX_FIRST_BYTE_RETRIES_PER_REQUEST
  }
}

export function normalizeNormalRoutingConfig(value: unknown): RouteStrategyNormalRoutingConfig {
  if (value === undefined || value === null || value === '') {
    return defaultNormalRoutingConfig()
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('普通路由调度配置无效')
  }
  const record = value as Record<string, unknown>
  const schedulingPreference = normalizeNormalSchedulingPreference(record.schedulingPreference)
  if (schedulingPreference === 'cost_first') {
    return { schedulingPreference }
  }
  const speedFirstRecord = normalizeOptionalRecord(record.speedFirstConfig, '速度优先配置无效')
  const hasCommonDeadline = hasConfiguredValue(record.firstByteDeadlineMs)
  const hasLegacyDeadline = hasConfiguredValue(speedFirstRecord?.firstByteThresholdMs)
  if (hasCommonDeadline && hasLegacyDeadline) {
    throw new Error('首字截止时间不能同时配置 firstByteDeadlineMs 和旧 firstByteThresholdMs')
  }
  const firstByteDeadlineMs = normalizeIntegerRange(
    hasCommonDeadline ? record.firstByteDeadlineMs : speedFirstRecord?.firstByteThresholdMs,
    DEFAULT_SPEED_FIRST_FIRST_BYTE_DEADLINE_MS,
    10_000,
    60_000,
    '首字截止时间必须是 10000-60000 毫秒'
  )
  return {
    schedulingPreference,
    firstByteDeadlineMs,
    speedFirstConfig: normalizeSpeedFirstConfig(speedFirstRecord)
  }
}

function normalizeNormalSchedulingPreference(value: unknown): RouteStrategyNormalSchedulingPreference {
  if (value === undefined || value === null || value === '') return DEFAULT_NORMAL_SCHEDULING_PREFERENCE
  if (value === 'cost_first' || value === 'speed_first') return value
  throw new Error('普通路由调度偏好无效')
}

function normalizeSpeedFirstConfig(value: unknown): RouteStrategySpeedFirstConfig {
  const fallback = defaultSpeedFirstConfig()
  if (value === undefined || value === null || value === '') return fallback
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('速度优先配置无效')
  }
  const record = value as Record<string, unknown>
  return {
    slowTriggerCount: normalizeIntegerRange(record.slowTriggerCount, fallback.slowTriggerCount, 2, 10, '速度优先触发次数必须是 2-10'),
    slowWindowSeconds: normalizeIntegerRange(record.slowWindowSeconds, fallback.slowWindowSeconds, 60, 600, '速度优先窗口期必须是 60-600 秒'),
    recoverySuccessCount: normalizeIntegerRange(record.recoverySuccessCount, fallback.recoverySuccessCount, 3, 10, '速度优先恢复次数必须是 3-10'),
    probeIntervalSeconds: normalizeIntegerRange(record.probeIntervalSeconds, fallback.probeIntervalSeconds, 10, 300, '速度优先探针间隔必须是 10-300 秒'),
    degradedTtlSeconds: normalizeIntegerRange(record.degradedTtlSeconds, fallback.degradedTtlSeconds, 60, 3600, '速度优先降级保留时间必须是 60-3600 秒'),
    maxFirstByteRetriesPerRequest: normalizeIntegerRange(record.maxFirstByteRetriesPerRequest, fallback.maxFirstByteRetriesPerRequest, 1, 3, '速度优先单请求切号次数必须是 1-3')
  }
}

function normalizeOptionalRecord(value: unknown, message: string): Record<string, unknown> | undefined {
  if (value === undefined || value === null || value === '') return undefined
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(message)
  }
  return value as Record<string, unknown>
}

function hasConfiguredValue(value: unknown): boolean {
  return value !== undefined && value !== null && value !== ''
}

function normalizeIntegerRange(
  value: unknown,
  fallback: number,
  min: number,
  max: number,
  message: string
): number {
  if (value === undefined || value === null || value === '') return fallback
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isInteger(numeric) || numeric < min || numeric > max) {
    throw new Error(message)
  }
  return numeric
}
