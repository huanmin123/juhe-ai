import { normalizeUsageCapabilityToken } from './service-tier.js'

export type UsageReasoningEffort = string

export function normalizeUsageReasoningEffort(value: unknown): UsageReasoningEffort | undefined {
  return normalizeUsageCapabilityToken(value)
}
