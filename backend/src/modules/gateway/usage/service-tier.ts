export type UsageServiceTier = 'default' | 'priority' | 'flex'

export interface UsageServiceTierFacts {
  requestedServiceTier: UsageServiceTier
  effectiveServiceTier: UsageServiceTier
  reportedServiceTier?: UsageServiceTier
  billedServiceTier: UsageServiceTier
}

export function normalizeUsageServiceTier(value: unknown): UsageServiceTier {
  if (value === 'priority' || value === 'flex') return value
  return 'default'
}

export function resolveUsageServiceTiers(input: {
  requestedServiceTier?: UsageServiceTier
  effectiveServiceTier?: UsageServiceTier
  reportedServiceTier?: UsageServiceTier
}): UsageServiceTierFacts {
  const requestedServiceTier = input.requestedServiceTier ?? 'default'
  const effectiveServiceTier = input.effectiveServiceTier ?? requestedServiceTier
  const reportedServiceTier = input.reportedServiceTier
  return {
    requestedServiceTier,
    effectiveServiceTier,
    reportedServiceTier,
    billedServiceTier: reportedServiceTier ?? effectiveServiceTier
  }
}
