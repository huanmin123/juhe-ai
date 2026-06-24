import type { AccountEffectiveAvailabilityStatus, AccountStatus, AccountSummary } from './types.js'

const accountStatusValues = new Set<AccountStatus>([
  'active',
  'pending_test',
  'disabled',
  'error',
  'rate_limited',
  'temporary_unavailable'
])

export function accountStatusFilterForEffectiveAvailabilityStatus(
  status: AccountEffectiveAvailabilityStatus
): AccountStatus | undefined {
  if (status === 'available' || status === 'runtime_degraded') return 'active'
  if (status === 'source_pending_test' || status === 'instance_pending_test') return 'pending_test'
  if (status === 'source_error' || status === 'instance_error') return 'error'
  if (
    status === 'authorization_quota_exceeded'
    || status === 'source_rate_limited'
    || status === 'instance_rate_limited'
  ) {
    return 'rate_limited'
  }
  if (
    status === 'source_temporary_unavailable'
    || status === 'source_cooldown'
    || status === 'instance_temporary_unavailable'
    || status === 'instance_cooldown'
    || status === 'api_key_pool_unavailable'
    || status === 'runtime_local_suppressed'
    || status === 'runtime_half_open'
    || status === 'runtime_precheck_pending'
    || status === 'runtime_precheck_failed'
  ) {
    return 'temporary_unavailable'
  }
  if (
    status === 'authorization_expired'
    || status === 'authorization_paused'
    || status === 'authorization_unavailable'
    || status === 'binding_missing'
    || status === 'permission_denied'
    || status === 'source_deleted'
    || status === 'source_expired'
    || status === 'source_disabled'
    || status === 'source_unschedulable'
    || status === 'instance_expired'
    || status === 'instance_disabled'
    || status === 'source_schedule_inactive'
    || status === 'instance_schedule_inactive'
    || status === 'instance_unschedulable'
  ) {
    return 'disabled'
  }
  return undefined
}

export function accountFilterStatuses(
  account: Pick<AccountSummary, 'status' | 'effectiveAvailability' | 'authorizationQuotaExceeded'>
): Set<AccountStatus> {
  const effectiveStatus = account.effectiveAvailability?.status
  const derivedStatus = effectiveStatus
    ? accountStatusFilterForEffectiveAvailabilityStatus(effectiveStatus)
    : undefined
  if (derivedStatus) {
    return new Set([derivedStatus])
  }
  if (account.authorizationQuotaExceeded) {
    return new Set(['rate_limited'])
  }
  if (account.status === 'active' && account.effectiveAvailability?.available === false) {
    return new Set()
  }
  return new Set([account.status])
}

export function accountMatchesStatusFilters(
  account: Pick<AccountSummary, 'status' | 'effectiveAvailability' | 'authorizationQuotaExceeded'>,
  statuses: string[]
): boolean {
  const normalizedStatuses = statuses.filter(isAccountStatus)
  if (!normalizedStatuses.length) return true
  const accountStatuses = accountFilterStatuses(account)
  return normalizedStatuses.some((status) => accountStatuses.has(status))
}

export function isAccountStatus(value: string): value is AccountStatus {
  return accountStatusValues.has(value as AccountStatus)
}
