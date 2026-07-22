import type { AuditTrafficSource } from './audit-log-types.js'

export function normalizeAuditTrafficSource(value: unknown): AuditTrafficSource {
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
  throw new Error(`非法审计流量来源：${String(value)}`)
}
