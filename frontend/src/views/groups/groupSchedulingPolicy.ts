import type { GroupSchedulingPolicy, GroupSummary, GroupType } from '@/types/domain'

export const defaultHighConcurrencySchedulingPolicy: Required<GroupSchedulingPolicy> = {
  mode: 'balanced_fast',
  defaultSoftConcurrency: 5,
  fastFirstEnabled: true,
  fallbackOnQueueEnabled: true,
  breakAffinityOnSoftLimit: true,
  breakAffinityOnQueueWaitMs: 0,
  slowRequestThresholdMs: 30000,
  firstOutputSlowThresholdMs: 15000,
  recentTimeoutWindowSeconds: 120,
  recentTimeoutPenaltyThreshold: 2,
  maxQueueWaitMs: 60000,
  maxQueueSize: 1000,
  perApiKeyQueueLimit: 1000,
  clientIpConcurrencyLimit: 0,
  clientIpConcurrencyOverflowMode: 'reject',
  imageLaneMaxConcurrency: 0
}

export const defaultClientIpConcurrencyLimit = 5

export const clientIpOverflowModeOptions = [
  { label: '立即拒绝', value: 'reject' },
  { label: '排队等待', value: 'queue' }
]

const groupSchedulingPolicyKeys = [
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

export function cloneHighConcurrencySchedulingPolicy(
  source?: GroupSchedulingPolicy,
  options: { requireComplete?: boolean } = {}
): Required<GroupSchedulingPolicy> {
  const input = groupSchedulingPolicyRecord(source, options)
  assertGroupSchedulingPolicyKeys(input)
  if (options.requireComplete) {
    assertCompleteGroupSchedulingPolicy(input)
  }
  return {
    mode: normalizeGroupSchedulingMode(input.mode),
    defaultSoftConcurrency: normalizeGroupSchedulingInteger(input.defaultSoftConcurrency, defaultHighConcurrencySchedulingPolicy.defaultSoftConcurrency, 1, 1_000_000, 'defaultSoftConcurrency'),
    fastFirstEnabled: normalizeGroupSchedulingBoolean(input.fastFirstEnabled, defaultHighConcurrencySchedulingPolicy.fastFirstEnabled, 'fastFirstEnabled'),
    fallbackOnQueueEnabled: normalizeGroupSchedulingBoolean(input.fallbackOnQueueEnabled, defaultHighConcurrencySchedulingPolicy.fallbackOnQueueEnabled, 'fallbackOnQueueEnabled'),
    breakAffinityOnSoftLimit: normalizeGroupSchedulingBoolean(input.breakAffinityOnSoftLimit, defaultHighConcurrencySchedulingPolicy.breakAffinityOnSoftLimit, 'breakAffinityOnSoftLimit'),
    breakAffinityOnQueueWaitMs: normalizeGroupSchedulingInteger(input.breakAffinityOnQueueWaitMs, defaultHighConcurrencySchedulingPolicy.breakAffinityOnQueueWaitMs, 0, 1_000_000, 'breakAffinityOnQueueWaitMs'),
    slowRequestThresholdMs: normalizeGroupSchedulingInteger(input.slowRequestThresholdMs, defaultHighConcurrencySchedulingPolicy.slowRequestThresholdMs, 1, 1_000_000, 'slowRequestThresholdMs'),
    firstOutputSlowThresholdMs: normalizeGroupSchedulingInteger(input.firstOutputSlowThresholdMs, defaultHighConcurrencySchedulingPolicy.firstOutputSlowThresholdMs, 1, 1_000_000, 'firstOutputSlowThresholdMs'),
    recentTimeoutWindowSeconds: normalizeGroupSchedulingInteger(input.recentTimeoutWindowSeconds, defaultHighConcurrencySchedulingPolicy.recentTimeoutWindowSeconds, 1, 1_000_000, 'recentTimeoutWindowSeconds'),
    recentTimeoutPenaltyThreshold: normalizeGroupSchedulingInteger(input.recentTimeoutPenaltyThreshold, defaultHighConcurrencySchedulingPolicy.recentTimeoutPenaltyThreshold, 1, 1_000_000, 'recentTimeoutPenaltyThreshold'),
    maxQueueWaitMs: normalizeGroupSchedulingInteger(input.maxQueueWaitMs, defaultHighConcurrencySchedulingPolicy.maxQueueWaitMs, 1, 3_600_000, 'maxQueueWaitMs'),
    maxQueueSize: normalizeGroupSchedulingInteger(input.maxQueueSize, defaultHighConcurrencySchedulingPolicy.maxQueueSize, 1, 1_000_000, 'maxQueueSize'),
    perApiKeyQueueLimit: normalizeGroupSchedulingInteger(input.perApiKeyQueueLimit, defaultHighConcurrencySchedulingPolicy.perApiKeyQueueLimit, 1, 1_000_000, 'perApiKeyQueueLimit'),
    clientIpConcurrencyLimit: normalizeGroupSchedulingInteger(input.clientIpConcurrencyLimit, defaultHighConcurrencySchedulingPolicy.clientIpConcurrencyLimit, 0, 1_000_000, 'clientIpConcurrencyLimit'),
    clientIpConcurrencyOverflowMode: normalizeGroupSchedulingOverflowMode(input.clientIpConcurrencyOverflowMode),
    imageLaneMaxConcurrency: normalizeGroupSchedulingInteger(input.imageLaneMaxConcurrency, defaultHighConcurrencySchedulingPolicy.imageLaneMaxConcurrency, 0, 1_000_000, 'imageLaneMaxConcurrency')
  }
}

export function normalizeClientIpConcurrencyLimit(value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0 || value > 1_000_000) {
    return 0
  }
  return value
}

export function groupTypeText(groupType?: GroupType): string {
  return groupType === 'high_concurrency' ? '高并发' : '个人'
}

export function groupTypeColor(groupType?: GroupType): string {
  return groupType === 'high_concurrency' ? 'purple' : 'blue'
}

export function groupPolicySummary(group: Pick<GroupSummary, 'groupType'> & Partial<Pick<GroupSummary, 'schedulingPolicy'>>): string {
  if (group.groupType !== 'high_concurrency') {
    return '个人分组保持稳定调度'
  }
  if (!group.schedulingPolicy) return '高并发调度'
  let policy: Required<GroupSchedulingPolicy>
  try {
    policy = cloneHighConcurrencySchedulingPolicy(group.schedulingPolicy, { requireComplete: true })
  } catch {
    return '高并发调度策略数据异常，请清理后再编辑'
  }
  const clientIpSummary = policy.clientIpConcurrencyLimit > 0
    ? `单 IP ${policy.clientIpConcurrencyLimit} 并发，超过后${policy.clientIpConcurrencyOverflowMode === 'queue' ? '排队等待' : '立即拒绝'}`
    : '单 IP 不限制'
  return `最大单账户排队 ${policy.defaultSoftConcurrency}，最大等待 ${Math.round(policy.maxQueueWaitMs / 1000)} 秒，${clientIpSummary}，队列上限 ${policy.maxQueueSize}`
}

function groupSchedulingPolicyRecord(source?: GroupSchedulingPolicy, options: { requireComplete?: boolean } = {}): Record<string, unknown> {
  if (source === undefined || source === null) {
    if (options.requireComplete) {
      throw new Error('分组调度策略缺失，请清理后再编辑')
    }
    return {}
  }
  if (typeof source !== 'object' || Array.isArray(source)) {
    throw new Error('分组调度策略无效')
  }
  return source as Record<string, unknown>
}

function assertGroupSchedulingPolicyKeys(input: Record<string, unknown>): void {
  const allowed = new Set<string>(groupSchedulingPolicyKeys)
  const unknownKeys = Object.keys(input).filter((key) => !allowed.has(key))
  if (unknownKeys.length) {
    throw new Error(`分组调度策略包含未知字段：${unknownKeys.join('、')}`)
  }
}

function assertCompleteGroupSchedulingPolicy(input: Record<string, unknown>): void {
  const missingKeys = groupSchedulingPolicyKeys.filter((key) => input[key] === undefined || input[key] === null)
  if (missingKeys.length) {
    throw new Error(`分组调度策略缺少字段：${missingKeys.join('、')}`)
  }
}

function normalizeGroupSchedulingInteger(value: unknown, fallback: number, min: number, max: number, key: string): number {
  if (value === undefined || value === null) return fallback
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    throw new Error(`分组调度策略 ${key} 必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`分组调度策略 ${key} 必须在 ${min}-${max} 之间`)
  }
  return value
}

function normalizeGroupSchedulingBoolean(value: unknown, fallback: boolean, key: string): boolean {
  if (value === undefined || value === null) return fallback
  if (typeof value !== 'boolean') {
    throw new Error(`分组调度策略 ${key} 必须是布尔值`)
  }
  return value
}

function normalizeGroupSchedulingMode(value: unknown): 'balanced_fast' {
  if (value === undefined || value === null || value === 'balanced_fast') return 'balanced_fast'
  throw new Error('分组调度策略 mode 无效')
}

function normalizeGroupSchedulingOverflowMode(value: unknown): 'reject' | 'queue' {
  if (value === undefined || value === null) return defaultHighConcurrencySchedulingPolicy.clientIpConcurrencyOverflowMode
  if (value === 'reject' || value === 'queue') return value
  throw new Error('分组调度策略 clientIpConcurrencyOverflowMode 无效')
}
