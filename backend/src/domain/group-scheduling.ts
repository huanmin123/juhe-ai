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

type BooleanPolicyKey =
  | 'fastFirstEnabled'
  | 'fallbackOnQueueEnabled'
  | 'breakAffinityOnSoftLimit'

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
  clientIpConcurrencyOverflowMode: 'reject'
}

export function normalizeGroupType(value: unknown): GroupType {
  return value === 'high_concurrency' ? 'high_concurrency' : 'personal'
}

export function resolveGroupSchedulingPolicy(groupType: GroupType, value: unknown): GroupSchedulingPolicy | undefined {
  if (groupType !== 'high_concurrency') {
    return undefined
  }
  const input = objectValue(value)
  const maxQueueSize = numericPolicy(input.maxQueueSize, 'maxQueueSize')
  return {
    mode: 'balanced_fast',
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
    clientIpConcurrencyOverflowMode: clientIpConcurrencyOverflowMode(input.clientIpConcurrencyOverflowMode)
  }
}

export function parseGroupSchedulingPolicyJson(value: string | null | undefined, groupType: GroupType): GroupSchedulingPolicy | undefined {
  if (typeof value !== 'string' || !value.trim()) {
    return resolvePersistedGroupSchedulingPolicy(groupType, undefined)
  }
  try {
    return resolvePersistedGroupSchedulingPolicy(groupType, JSON.parse(value) as unknown)
  } catch {
    return resolvePersistedGroupSchedulingPolicy(groupType, undefined)
  }
}

export function groupSchedulingPolicyJson(value: unknown, groupType: GroupType): string | null {
  const policy = resolvePersistedGroupSchedulingPolicy(groupType, value)
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

function objectValue(value: unknown): Record<string, unknown> {
  if (typeof value === 'string' && value.trim()) {
    try {
      const parsed = JSON.parse(value) as unknown
      return objectValue(parsed)
    } catch {
      return {}
    }
  }
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function numericPolicy(value: unknown, key: NumericPolicyKey): number {
  const max = key === 'maxQueueWaitMs' ? 3_600_000 : 1_000_000
  const min = key === 'breakAffinityOnQueueWaitMs' || key === 'clientIpConcurrencyLimit' ? 0 : 1
  return boundedInteger(value, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY[key], min, max)
}

function booleanPolicy(value: unknown, key: BooleanPolicyKey): boolean {
  return typeof value === 'boolean' ? value : DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY[key]
}

function clientIpConcurrencyOverflowMode(value: unknown): 'reject' | 'queue' {
  return value === 'queue' ? 'queue' : 'reject'
}

function resolvePersistedGroupSchedulingPolicy(groupType: GroupType, value: unknown): GroupSchedulingPolicy | undefined {
  const input = objectValue(value)
  return resolveGroupSchedulingPolicy(groupType, {
    defaultSoftConcurrency: input.defaultSoftConcurrency,
    maxQueueWaitMs: input.maxQueueWaitMs,
    clientIpConcurrencyLimit: input.clientIpConcurrencyLimit,
    clientIpConcurrencyOverflowMode: input.clientIpConcurrencyOverflowMode
  })
}

function resolvePerApiKeyQueueLimit(value: unknown, maxQueueSize: number): number {
  if (value === undefined || value === null) {
    return maxQueueSize
  }
  return boundedInteger(value, maxQueueSize, 1, maxQueueSize)
}

function positiveInteger(value: unknown, fallback: number, max = 1_000_000): number {
  return boundedInteger(value, fallback, 1, max)
}

function boundedInteger(value: unknown, fallback: number, min: number, max: number): number {
  const numeric = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : Number.NaN
  if (!Number.isFinite(numeric)) {
    return fallback
  }
  return Math.min(max, Math.max(min, Math.trunc(numeric)))
}
