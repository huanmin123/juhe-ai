import type { AccountStatus } from '../domain/types.js'

const accountStatusValues: readonly AccountStatus[] = ['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated']
const coolingAccountStatusValues: readonly AccountStatus[] = ['rate_limited', 'temporary_unavailable']

export function normalizeAccountStatus(value: unknown): AccountStatus {
  if (typeof value === 'string' && accountStatusValues.includes(value as AccountStatus)) {
    return value as AccountStatus
  }
  throw new Error('账户状态无效')
}

export function normalizedAccountStatusInput(value: unknown, fallback: AccountStatus): AccountStatus {
  if (value === undefined) return fallback
  if (typeof value === 'string' && accountStatusValues.includes(value as AccountStatus)) {
    return value as AccountStatus
  }
  throw new Error('账户状态无效')
}

export function isCoolingAccountStatus(status: AccountStatus): boolean {
  return coolingAccountStatusValues.includes(status)
}

export function isAccountStatusEligibleForRecoveryProbe(status: AccountStatus): boolean {
  return status === 'active' || isCoolingAccountStatus(status)
}

export function isHardUnavailableAccountStatus(status: AccountStatus): boolean {
  return status === 'disabled' || status === 'pending_test' || status === 'error' || status === 'quality_isolated'
}
