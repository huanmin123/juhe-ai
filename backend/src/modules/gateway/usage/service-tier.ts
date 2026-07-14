export type UsageServiceTier = string

export interface UsageServiceTierFacts {
  requestedServiceTier: UsageServiceTier
  effectiveServiceTier: UsageServiceTier
  reportedServiceTier?: UsageServiceTier
  billedServiceTier: UsageServiceTier
}

export function normalizeUsageServiceTier(value: unknown): UsageServiceTier {
  return normalizeOptionalUsageServiceTier(value) ?? 'default'
}

export function normalizeOptionalUsageServiceTier(value: unknown): UsageServiceTier | undefined {
  return normalizeUsageCapabilityToken(value)
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

export function normalizeUsageCapabilityToken(value: unknown): string | undefined {
  if (typeof value !== 'string' || value !== value.trim()) return undefined
  return /^[a-z0-9][a-z0-9._-]{0,63}$/i.test(value) ? value : undefined
}
