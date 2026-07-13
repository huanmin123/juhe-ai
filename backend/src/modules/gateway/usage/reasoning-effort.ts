export type UsageReasoningEffort =
  | 'none'
  | 'minimal'
  | 'low'
  | 'medium'
  | 'high'
  | 'xhigh'
  | 'max'

const usageReasoningEfforts = new Set<UsageReasoningEffort>([
  'none',
  'minimal',
  'low',
  'medium',
  'high',
  'xhigh',
  'max'
])

export function normalizeUsageReasoningEffort(value: unknown): UsageReasoningEffort | undefined {
  return typeof value === 'string' && usageReasoningEfforts.has(value as UsageReasoningEffort)
    ? value as UsageReasoningEffort
    : undefined
}
