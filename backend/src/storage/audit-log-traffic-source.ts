import type { AuditTrafficSource, PersistedAuditTrafficSource } from './audit-log-types.js'

export type AuditTrafficSourceContext = { id?: string; traceId?: string }

export const nonPersistedAuditTrafficSources = [
  'account_health_check',
  'runtime_recovery_probe',
  'cooldown_retest'
] as const

export function normalizeAuditTrafficSource(value: unknown, context?: AuditTrafficSourceContext): AuditTrafficSource {
  if (value === undefined) return 'gateway'
  if (
    value === 'gateway'
    || value === 'manual_account_test'
    || value === 'account_health_check'
    || value === 'runtime_recovery_probe'
    || value === 'cooldown_retest'
    || value === 'hybrid_scoring'
    || value === 'hybrid_quality_scoring'
  ) {
    return value
  }
  const contextText = [context?.id ? `id=${context.id}` : '', context?.traceId ? `traceId=${context.traceId}` : '']
    .filter(Boolean)
    .join(' ')
  throw new Error(`非法审计流量来源：${String(value)}${contextText ? ` (${contextText})` : ''}`)
}

export function isPersistedAuditTrafficSource(value: AuditTrafficSource): value is PersistedAuditTrafficSource {
  return value === 'gateway'
    || value === 'manual_account_test'
    || value === 'hybrid_scoring'
    || value === 'hybrid_quality_scoring'
}

export function normalizePersistedAuditTrafficSource(
  value: unknown,
  context?: AuditTrafficSourceContext
): PersistedAuditTrafficSource | undefined {
  const normalized = normalizeAuditTrafficSource(value, context)
  return isPersistedAuditTrafficSource(normalized) ? normalized : undefined
}
