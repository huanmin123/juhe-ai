import type { GroupSchedulingPolicy, GroupType } from './types.js'

type NumericPolicyKey =
  | 'defaultSoftConcurrency'
  | 'breakAffinityOnQueueWaitMs'
  | 'slowRequestThresholdMs'
  | 'firstOutputSlowThresholdMs'
  | 'recentTimeoutWindowSeconds'
  | 'recentTimeoutPenaltyThreshold'
  | 'maxQueueWaitMs'
  | 'maxQueueSize'
  | 'perApiKeyQueueLimit'
  | 'clientIpConcurrencyLimit'
  | 'imageLaneMaxConcurrency'

type BooleanPolicyKey =
  | 'fastFirstEnabled'
  | 'fallbackOnQueueEnabled'
  | 'breakAffinityOnSoftLimit'

const writableGroupSchedulingPolicyKeys = [
  'defaultSoftConcurrency',
  'maxQueueWaitMs',
  'clientIpConcurrencyLimit',
  'clientIpConcurrencyOverflowMode',
  'imageLaneMaxConcurrency'
] as const

const storedGroupSchedulingPolicyKeys = [
  'mode',
  'defaultSoftConcurrency',
  'fastFirstEnabled',
  'fallbackOnQueueEnabled',
  'breakAffinityOnSoftLimit',
  'breakAffinityOnQueueWaitMs',
  'slowRequestThresholdMs',
  'firstOutputSlowThresholdMs',
  'recentTimeoutWindowSeconds',
  'recentTimeoutPenaltyThreshold',
  'maxQueueWaitMs',
  'maxQueueSize',
  'perApiKeyQueueLimit',
  'clientIpConcurrencyLimit',
  'clientIpConcurrencyOverflowMode',
  'imageLaneMaxConcurrency'
] as const

export const DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY: Required<GroupSchedulingPolicy> = {
  mode: 'balanced_fast',
  defaultSoftConcurrency: 5,
  fastFirstEnabled: true,
  fallbackOnQueueEnabled: true,
  breakAffinityOnSoftLimit: true,
  breakAffinityOnQueueWaitMs: 0,
  slowRequestThresholdMs: 30_000,
  firstOutputSlowThresholdMs: 15_000,
  recentTimeoutWindowSeconds: 120,
  recentTimeoutPenaltyThreshold: 2,
  maxQueueWaitMs: 60_000,
  maxQueueSize: 1_000,
  perApiKeyQueueLimit: 1_000,
  clientIpConcurrencyLimit: 0,
  clientIpConcurrencyOverflowMode: 'reject',
  imageLaneMaxConcurrency: 0
}

export function normalizeGroupType(value: unknown): GroupType {
  if (value === undefined || value === null) {
    return 'personal'
  }
  if (value === 'personal' || value === 'high_concurrency') {
    return value
  }
  throw new Error('分组类型无效')
}

export function resolveGroupSchedulingPolicy(groupType: GroupType, value: unknown): GroupSchedulingPolicy | undefined {
  if (groupType !== 'high_concurrency') {
    return undefined
  }
  const input = objectValue(value)
  assertOnlyKeys(input, storedGroupSchedulingPolicyKeys, '分组调度策略')
  const maxQueueSize = numericPolicy(input.maxQueueSize, 'maxQueueSize')
  return {
    mode: modePolicy(input.mode),
    defaultSoftConcurrency: numericPolicy(input.defaultSoftConcurrency, 'defaultSoftConcurrency'),
    fastFirstEnabled: booleanPolicy(input.fastFirstEnabled, 'fastFirstEnabled'),
    fallbackOnQueueEnabled: booleanPolicy(input.fallbackOnQueueEnabled, 'fallbackOnQueueEnabled'),
    breakAffinityOnSoftLimit: booleanPolicy(input.breakAffinityOnSoftLimit, 'breakAffinityOnSoftLimit'),
    breakAffinityOnQueueWaitMs: numericPolicy(input.breakAffinityOnQueueWaitMs, 'breakAffinityOnQueueWaitMs'),
    slowRequestThresholdMs: numericPolicy(input.slowRequestThresholdMs, 'slowRequestThresholdMs'),
    firstOutputSlowThresholdMs: numericPolicy(input.firstOutputSlowThresholdMs, 'firstOutputSlowThresholdMs'),
    recentTimeoutWindowSeconds: numericPolicy(input.recentTimeoutWindowSeconds, 'recentTimeoutWindowSeconds'),
    recentTimeoutPenaltyThreshold: numericPolicy(input.recentTimeoutPenaltyThreshold, 'recentTimeoutPenaltyThreshold'),
    maxQueueWaitMs: numericPolicy(input.maxQueueWaitMs, 'maxQueueWaitMs'),
    maxQueueSize,
    perApiKeyQueueLimit: resolvePerApiKeyQueueLimit(input.perApiKeyQueueLimit, maxQueueSize),
    clientIpConcurrencyLimit: numericPolicy(input.clientIpConcurrencyLimit, 'clientIpConcurrencyLimit'),
    clientIpConcurrencyOverflowMode: clientIpConcurrencyOverflowMode(input.clientIpConcurrencyOverflowMode),
    imageLaneMaxConcurrency: numericPolicy(input.imageLaneMaxConcurrency, 'imageLaneMaxConcurrency')
  }
}

export function parseGroupSchedulingPolicyJson(value: string | null | undefined, groupType: GroupType): GroupSchedulingPolicy | undefined {
  if (groupType !== 'high_concurrency') {
    return undefined
  }
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error('高并发分组调度策略缺失')
  }
  return resolveStoredGroupSchedulingPolicy(JSON.parse(value) as unknown)
}

export function groupSchedulingPolicyJson(value: unknown, groupType: GroupType): string | null {
  const policy = resolveWritableGroupSchedulingPolicy(groupType, value)
  return policy ? JSON.stringify(policy) : null
}

export function effectiveSoftConcurrencyLimit(input: {
  accountConcurrencyLimit: number
  policy?: GroupSchedulingPolicy
}): number {
  const hardLimit = positiveInteger(input.accountConcurrencyLimit, 1, 1_000_000)
  const policy = resolveGroupSchedulingPolicy('high_concurrency', input.policy) ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY
  const base = policy.defaultSoftConcurrency ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.defaultSoftConcurrency
  return Math.min(hardLimit, Math.max(1, Math.trunc(base)))
}

export function effectiveImageLaneConcurrencyLimit(input: {
  accountConcurrencyLimit: number
  policy?: GroupSchedulingPolicy
}): number {
  const hardLimit = positiveInteger(input.accountConcurrencyLimit, 1, 1_000_000)
  const policy = resolveGroupSchedulingPolicy('high_concurrency', input.policy) ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY
  const configured = policy.imageLaneMaxConcurrency ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.imageLaneMaxConcurrency
  if (configured > 0) {
    return Math.min(hardLimit, Math.max(1, Math.trunc(configured)))
  }
  return hardLimit > 1 ? hardLimit - 1 : hardLimit
}

function objectValue(value: unknown): Record<string, unknown> {
  if (value === undefined || value === null) {
    return {}
  }
  if (typeof value === 'object' && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  throw new Error('分组调度策略无效')
}

function numericPolicy(value: unknown, key: NumericPolicyKey): number {
  const max = key === 'maxQueueWaitMs' ? 3_600_000 : 1_000_000
  const min = key === 'breakAffinityOnQueueWaitMs' || key === 'clientIpConcurrencyLimit' || key === 'imageLaneMaxConcurrency' ? 0 : 1
  return boundedInteger(value, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY[key], min, max, key)
}

function booleanPolicy(value: unknown, key: BooleanPolicyKey): boolean {
  if (value === undefined || value === null) {
    return DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY[key]
  }
  if (typeof value !== 'boolean') {
    throw new Error(`分组调度策略 ${key} 必须是布尔值`)
  }
  return value
}

function clientIpConcurrencyOverflowMode(value: unknown): 'reject' | 'queue' {
  if (value === undefined || value === null) {
    return DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.clientIpConcurrencyOverflowMode
  }
  if (value === 'reject' || value === 'queue') {
    return value
  }
  throw new Error('分组调度策略 clientIpConcurrencyOverflowMode 无效')
}

function resolveStoredGroupSchedulingPolicy(value: unknown): GroupSchedulingPolicy {
  const input = requiredObjectValue(value)
  assertOnlyKeys(input, storedGroupSchedulingPolicyKeys, '分组调度策略')
  assertRequiredKeys(input, storedGroupSchedulingPolicyKeys, '分组调度策略')
  const policy = resolveGroupSchedulingPolicy('high_concurrency', input)
  if (!policy) {
    throw new Error('高并发分组调度策略无效')
  }
  return policy
}

function resolveWritableGroupSchedulingPolicy(groupType: GroupType, value: unknown): GroupSchedulingPolicy | undefined {
  if (groupType !== 'high_concurrency') {
    return undefined
  }
  const input = objectValue(value)
  assertOnlyKeys(input, writableGroupSchedulingPolicyKeys, '分组调度策略')
  return resolveGroupSchedulingPolicy(groupType, input)
}

function resolvePerApiKeyQueueLimit(value: unknown, maxQueueSize: number): number {
  if (value === undefined || value === null) {
    return maxQueueSize
  }
  return boundedInteger(value, maxQueueSize, 1, maxQueueSize, 'perApiKeyQueueLimit')
}

function positiveInteger(value: unknown, fallback: number, max = 1_000_000): number {
  return boundedInteger(value, fallback, 1, max, 'positiveInteger')
}

function boundedInteger(value: unknown, fallback: number, min: number, max: number, key: string): number {
  if (value === undefined || value === null) {
    return fallback
  }
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    throw new Error(`分组调度策略 ${key} 必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`分组调度策略 ${key} 必须在 ${min}-${max} 之间`)
  }
  return value
}

function modePolicy(value: unknown): 'balanced_fast' {
  if (value === undefined || value === null) {
    return DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.mode
  }
  if (value === 'balanced_fast') {
    return value
  }
  throw new Error('分组调度策略 mode 无效')
}

function assertOnlyKeys(value: Record<string, unknown>, allowedKeys: readonly string[], label: string): void {
  const allowed = new Set(allowedKeys)
  const unknownKeys = Object.keys(value).filter((key) => !allowed.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

function assertRequiredKeys(value: Record<string, unknown>, requiredKeys: readonly string[], label: string): void {
  const missingKeys = requiredKeys.filter((key) => value[key] === undefined || value[key] === null)
  if (missingKeys.length) {
    throw new Error(`${label}缺少字段：${missingKeys.join('、')}`)
  }
}

function requiredObjectValue(value: unknown): Record<string, unknown> {
  if (typeof value === 'object' && value !== null && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  throw new Error('分组调度策略无效')
}
