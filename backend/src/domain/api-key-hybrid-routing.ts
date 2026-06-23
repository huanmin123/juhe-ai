import type {
  ApiKeyHybridLevelRoute,
  ApiKeyHybridQualityInspectionConfig,
  ApiKeyHybridRoutingConfig,
  ApiKeyRouteMode
} from './types.js'

export const DEFAULT_API_KEY_ROUTE_MODE: ApiKeyRouteMode = 'normal'
export const DEFAULT_HYBRID_SCORING_CONTEXT_MODE: ApiKeyHybridRoutingConfig['scoringContextMode'] = 'full_request'
export const DEFAULT_HYBRID_QUALITY_PREFERENCE: ApiKeyHybridRoutingConfig['qualityPreference'] = 'balanced'
export const DEFAULT_HYBRID_SCORING_TIMEOUT_MS = 15000
export const DEFAULT_HYBRID_FAILURE_DEFAULT_LEVEL = 7
export const DEFAULT_HYBRID_SCORING_CACHE_ENABLED = true
export const DEFAULT_HYBRID_SCORING_CACHE_TTL_SECONDS = 300
export const DEFAULT_HYBRID_AFFINITY_TTL_SECONDS = 900
export const DEFAULT_HYBRID_SWITCH_MIN_LEVEL_DELTA = 2
export const DEFAULT_HYBRID_DOWNGRADE_CONSECUTIVE_LOW_COUNT = 2
export const DEFAULT_HYBRID_QUALITY_INSPECTION_ENABLED = true
export const DEFAULT_HYBRID_QUALITY_INSPECTION_TRIGGER_MODE: ApiKeyHybridQualityInspectionConfig['triggerMode'] = 'risk_based'
export const DEFAULT_HYBRID_QUALITY_INSPECTION_MIN_TRIGGER_LEVEL = 7
export const DEFAULT_HYBRID_QUALITY_INSPECTION_MAX_RETRIES = 1
export const DEFAULT_HYBRID_QUALITY_INSPECTION_FAILURE_ACTION: ApiKeyHybridQualityInspectionConfig['failureAction'] = 'upgrade_next_level'

export function normalizeApiKeyRouteMode(value: unknown): ApiKeyRouteMode {
  if (value === undefined || value === null || value === '') return DEFAULT_API_KEY_ROUTE_MODE
  if (value === 'normal' || value === 'hybrid') return value
  throw new Error('API Key 路由模式无效')
}

export function parseHybridRoutingConfigJson(value: string | null | undefined): ApiKeyHybridRoutingConfig | undefined {
  if (!value) return undefined
  try {
    return normalizeHybridRoutingConfig(JSON.parse(value))
  } catch (error) {
    if (error instanceof Error) throw error
    throw new Error('混合路由配置无效')
  }
}

export function hybridRoutingConfigJson(value: ApiKeyHybridRoutingConfig | undefined): string | null {
  return value ? JSON.stringify(normalizeHybridRoutingConfig(value)) : null
}

export function normalizeHybridRoutingConfig(value: unknown): ApiKeyHybridRoutingConfig {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('混合路由配置不能为空')
  }
  const record = value as Record<string, unknown>
  const scoringGroupId = normalizeOptionalString(record.scoringGroupId)
  const scoringModel = normalizedNonEmptyString(record.scoringModel, '混合路由评分模型不能为空')
  const scoringContextMode = normalizeScoringContextMode(record.scoringContextMode)
  const qualityPreference = normalizeQualityPreference(record.qualityPreference)
  const scoringTimeoutMs = normalizeIntegerRange(
    record.scoringTimeoutMs,
    DEFAULT_HYBRID_SCORING_TIMEOUT_MS,
    1000,
    60000,
    '混合路由评分超时时间必须是 1000-60000 毫秒'
  )
  const failureDefaultLevel = normalizeIntegerRange(
    record.failureDefaultLevel,
    DEFAULT_HYBRID_FAILURE_DEFAULT_LEVEL,
    1,
    10,
    '混合路由评分失败默认等级必须是 1-10'
  )
  const scoringCacheEnabled = DEFAULT_HYBRID_SCORING_CACHE_ENABLED
  const scoringCacheTtlSeconds = normalizeIntegerRange(
    record.scoringCacheTtlSeconds,
    DEFAULT_HYBRID_SCORING_CACHE_TTL_SECONDS,
    1,
    3600,
    '混合路由评分缓存 TTL 必须是 1-3600 秒'
  )
  const cacheAffinityEnabled = true
  const affinityTtlSeconds = normalizeIntegerRange(
    record.affinityTtlSeconds,
    DEFAULT_HYBRID_AFFINITY_TTL_SECONDS,
    1,
    86400,
    '混合路由缓存亲和 TTL 必须是 1-86400 秒'
  )
  const switchMinLevelDelta = normalizeIntegerRange(
    record.switchMinLevelDelta,
    DEFAULT_HYBRID_SWITCH_MIN_LEVEL_DELTA,
    0,
    9,
    '混合路由切换等级差必须是 0-9'
  )
  const downgradeConsecutiveLowCount = normalizeIntegerRange(
    record.downgradeConsecutiveLowCount,
    DEFAULT_HYBRID_DOWNGRADE_CONSECUTIVE_LOW_COUNT,
    1,
    20,
    '混合路由降级确认次数必须是 1-20'
  )
  const levelRoutes = normalizeLevelRoutes(record.levelRoutes)
  const qualityInspection = normalizeQualityInspectionConfig(record.qualityInspection, {
    scoringModel
  })
  return {
    ...(scoringGroupId ? { scoringGroupId } : {}),
    scoringModel,
    scoringContextMode,
    qualityPreference,
    scoringTimeoutMs,
    failureDefaultLevel,
    scoringCacheEnabled,
    scoringCacheTtlSeconds,
    cacheAffinityEnabled,
    affinityTtlSeconds,
    switchMinLevelDelta,
    downgradeConsecutiveLowCount,
    levelRoutes,
    ...(qualityInspection ? { qualityInspection } : {})
  }
}

export function targetHybridLevelRouteForLevel(
  config: ApiKeyHybridRoutingConfig,
  level: number
): ApiKeyHybridLevelRoute | undefined {
  const normalizedLevel = clampHybridLevel(level)
  return config.levelRoutes.find((route) =>
    route.enabled
    && route.minLevel <= normalizedLevel
    && route.maxLevel >= normalizedLevel
  )
}

export function higherHybridLevelRoutes(
  config: ApiKeyHybridRoutingConfig,
  route: ApiKeyHybridLevelRoute
): ApiKeyHybridLevelRoute[] {
  return config.levelRoutes
    .filter((item) => item.enabled && item.minLevel > route.maxLevel)
    .sort((left, right) => left.minLevel - right.minLevel || left.maxLevel - right.maxLevel)
}

export function clampHybridLevel(value: unknown): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(numeric)) return DEFAULT_HYBRID_FAILURE_DEFAULT_LEVEL
  return Math.min(10, Math.max(1, Math.round(numeric)))
}

function normalizeScoringContextMode(value: unknown): ApiKeyHybridRoutingConfig['scoringContextMode'] {
  if (value === undefined || value === null || value === '') return DEFAULT_HYBRID_SCORING_CONTEXT_MODE
  if (value === 'full_request') return value
  throw new Error('混合路由评分上下文模式无效')
}

function normalizeQualityPreference(value: unknown): ApiKeyHybridRoutingConfig['qualityPreference'] {
  if (value === undefined || value === null || value === '') return DEFAULT_HYBRID_QUALITY_PREFERENCE
  if (value === 'cost_first' || value === 'balanced' || value === 'quality_first') return value
  throw new Error('混合路由质量偏好无效')
}

function normalizeQualityInspectionConfig(
  value: unknown,
  defaults: { scoringModel: string }
): ApiKeyHybridQualityInspectionConfig | undefined {
  if (value === undefined || value === null || value === '') {
    return {
      enabled: DEFAULT_HYBRID_QUALITY_INSPECTION_ENABLED,
      scoringModel: defaults.scoringModel,
      triggerMode: DEFAULT_HYBRID_QUALITY_INSPECTION_TRIGGER_MODE,
      minTriggerLevel: DEFAULT_HYBRID_QUALITY_INSPECTION_MIN_TRIGGER_LEVEL,
      maxRetries: DEFAULT_HYBRID_QUALITY_INSPECTION_MAX_RETRIES,
      failureAction: DEFAULT_HYBRID_QUALITY_INSPECTION_FAILURE_ACTION
    }
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('混合路由质量评分配置无效')
  }
  const record = value as Record<string, unknown>
  const enabled = record.enabled === undefined
    ? DEFAULT_HYBRID_QUALITY_INSPECTION_ENABLED
    : normalizeBoolean(record.enabled, '混合路由质量评分开关必须是布尔值')
  const scoringGroupId = normalizeOptionalString(record.scoringGroupId)
  const scoringModel = normalizeOptionalString(record.scoringModel) ?? defaults.scoringModel
  if (enabled && !scoringModel) {
    throw new Error('混合路由质量评分模型不能为空')
  }
  return {
    enabled,
    ...(scoringGroupId ? { scoringGroupId } : {}),
    scoringModel: scoringModel ?? '',
    triggerMode: normalizeQualityInspectionTriggerMode(record.triggerMode),
    minTriggerLevel: normalizeIntegerRange(
      record.minTriggerLevel,
      DEFAULT_HYBRID_QUALITY_INSPECTION_MIN_TRIGGER_LEVEL,
      1,
      10,
      '混合路由质量评分最低触发等级必须是 1-10'
    ),
    maxRetries: normalizeIntegerRange(
      record.maxRetries,
      DEFAULT_HYBRID_QUALITY_INSPECTION_MAX_RETRIES,
      0,
      2,
      '混合路由质量评分重试次数必须是 0-2'
    ),
    failureAction: normalizeQualityInspectionFailureAction(record.failureAction)
  }
}

function normalizeQualityInspectionTriggerMode(value: unknown): ApiKeyHybridQualityInspectionConfig['triggerMode'] {
  if (value === undefined || value === null || value === '') return DEFAULT_HYBRID_QUALITY_INSPECTION_TRIGGER_MODE
  if (value === 'quality_first_only' || value === 'risk_based' || value === 'always_for_hybrid') return value
  throw new Error('混合路由质量评分触发模式无效')
}

function normalizeQualityInspectionFailureAction(value: unknown): ApiKeyHybridQualityInspectionConfig['failureAction'] {
  if (value === undefined || value === null || value === '') return DEFAULT_HYBRID_QUALITY_INSPECTION_FAILURE_ACTION
  if (value === 'upgrade_next_level' || value === 'retry_same_model' || value === 'return_error') return value
  throw new Error('混合路由质量评分失败动作无效')
}

function normalizeLevelRoutes(value: unknown): ApiKeyHybridLevelRoute[] {
  if (!Array.isArray(value) || value.length === 0) {
    throw new Error('混合路由等级范围不能为空')
  }
  const routes = value.map(normalizeLevelRoute)
    .filter((route) => route.enabled)
    .sort((left, right) => left.minLevel - right.minLevel || left.maxLevel - right.maxLevel)
  if (!routes.length) {
    throw new Error('混合路由至少需要一个启用的等级范围')
  }
  const coverage = new Set<number>()
  for (const route of routes) {
    for (let level = route.minLevel; level <= route.maxLevel; level += 1) {
      if (coverage.has(level)) {
        throw new Error(`混合路由等级范围重叠：${level}`)
      }
      coverage.add(level)
    }
  }
  for (let level = 1; level <= 10; level += 1) {
    if (!coverage.has(level)) {
      throw new Error(`混合路由等级范围必须完整覆盖 1-10，缺少等级 ${level}`)
    }
  }
  return routes
}

function normalizeLevelRoute(value: unknown): ApiKeyHybridLevelRoute {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('混合路由等级范围无效')
  }
  const record = value as Record<string, unknown>
  const minLevel = normalizeIntegerRange(record.minLevel, undefined, 1, 10, '混合路由最小等级必须是 1-10')
  const maxLevel = normalizeIntegerRange(record.maxLevel, undefined, 1, 10, '混合路由最大等级必须是 1-10')
  if (minLevel > maxLevel) {
    throw new Error('混合路由等级范围最小值不能大于最大值')
  }
  return {
    minLevel,
    maxLevel,
    targetModel: normalizedNonEmptyString(record.targetModel, '混合路由目标模型不能为空'),
    enabled: record.enabled === undefined ? true : normalizeBoolean(record.enabled, '混合路由等级范围启用状态必须是布尔值')
  }
}

function normalizedNonEmptyString(value: unknown, message: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(message)
  }
  return value.trim()
}

function normalizeOptionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function normalizeBoolean(value: unknown, message: string): boolean {
  if (typeof value !== 'boolean') {
    throw new Error(message)
  }
  return value
}

function normalizeIntegerRange(
  value: unknown,
  fallback: number | undefined,
  min: number,
  max: number,
  message: string
): number {
  if (value === undefined || value === null || value === '') {
    if (fallback !== undefined) return fallback
    throw new Error(message)
  }
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isInteger(numeric) || numeric < min || numeric > max) {
    throw new Error(message)
  }
  return numeric
}
