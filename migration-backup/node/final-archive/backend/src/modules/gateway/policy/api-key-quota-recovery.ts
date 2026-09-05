import { canonicalizeRfc3339Instant, rfc3339InstantMilliseconds } from '../../../shared/rfc3339.js'
import {
  quotaRecoveryCooldownUntil,
  type QuotaRecoveryPolicy
} from '../../accounts/quota-recovery-policy.js'

export const API_KEY_QUOTA_GENERIC_ERROR_CODE = 'api_key_quota_insufficient'
export const API_KEY_QUOTA_EXPLICIT_RESET_ERROR_CODE = 'api_key_quota_insufficient_reset'
export const API_KEY_QUOTA_RECOVERY_TIMEOUT_ERROR_CODE = 'api_key_quota_recovery_timeout'

/**
 * The generic fallback uses the configured quota reset boundary plus a stable
 * positive offset derived from the account/key recovery seed. The offset is
 * bounded by the shared passive interval window (not the legacy fixed
 * jitter_minutes/15-minute metadata), so repeated observations keep one
 * auditable probe deadline.
 */
export const API_KEY_GENERIC_QUOTA_PROBE_INTERVAL_MS = 60 * 60 * 1000
export const API_KEY_QUOTA_OBSERVATION_TIMEOUT_MS = 30 * 24 * 60 * 60 * 1000

export type ApiKeyQuotaRecoveryMode = 'generic' | 'explicit_reset'
export type ApiKeyQuotaRecoveryHintSource = 'reset_at' | 'retry_after' | 'provider_header'

export interface ApiKeyQuotaRecoveryHint {
  mode: ApiKeyQuotaRecoveryMode
  cooldownUntil: string
  source?: ApiKeyQuotaRecoveryHintSource
}

export function genericApiKeyQuotaCooldownUntil(input?: {
  now?: Date
  seed?: string
  policy?: QuotaRecoveryPolicy
} | Date): string {
  const options = input instanceof Date ? { now: input } : (input ?? {})
  return quotaRecoveryCooldownUntil({
    accountType: 'api_key',
    now: options.now,
    seed: options.seed ?? 'system:api_key:generic',
    policy: options.policy
  })
}

export function apiKeyQuotaObservationExceeded(
  recoveryStartedAt: string | undefined,
  observedAt = new Date()
): boolean {
  const startedAtMs = rfc3339InstantMilliseconds(recoveryStartedAt)
  return startedAtMs !== undefined
    && observedAt.getTime() - startedAtMs >= API_KEY_QUOTA_OBSERVATION_TIMEOUT_MS
}

export function apiKeyQuotaRecoveryModeFromErrorCode(value: string | undefined): ApiKeyQuotaRecoveryMode | undefined {
  if (value === API_KEY_QUOTA_GENERIC_ERROR_CODE) return 'generic'
  if (value === API_KEY_QUOTA_EXPLICIT_RESET_ERROR_CODE) return 'explicit_reset'
  return undefined
}

export function quotaRecoveryErrorCode(mode: ApiKeyQuotaRecoveryMode): string {
  return mode === 'explicit_reset'
    ? API_KEY_QUOTA_EXPLICIT_RESET_ERROR_CODE
    : API_KEY_QUOTA_GENERIC_ERROR_CODE
}

export function extractApiKeyQuotaRecoveryHint(input: {
  bodyText?: string
  headers?: Headers
  now?: Date
}): ApiKeyQuotaRecoveryHint | undefined {
  const now = input.now ?? new Date()
  const bodyValue = parseJsonValue(input.bodyText)
  const absolute = findFirstField(bodyValue, ['reset_at', 'resetAt', 'quota_reset_at', 'quotaResetAt'])
  const absoluteAt = parseAbsoluteRecoveryTime(absolute)
  if (absoluteAt && absoluteAt.getTime() > now.getTime()) {
    return {
      mode: 'explicit_reset',
      cooldownUntil: absoluteAt.toISOString(),
      source: 'reset_at'
    }
  }

  const delaySeconds = parsePositiveSeconds(findFirstField(bodyValue, [
    'reset_after_seconds',
    'resetAfterSeconds',
    'retry_after_seconds',
    'retryAfterSeconds'
  ]))
  if (delaySeconds !== undefined) {
    const cooldownUntil = dateAfterSeconds(now, delaySeconds)
    if (!cooldownUntil) return undefined
    return {
      mode: 'explicit_reset',
      cooldownUntil: cooldownUntil.toISOString(),
      source: 'reset_at'
    }
  }

  const headers = input.headers
  if (headers) {
    const retryAfter = parseRetryAfter(headers.get('retry-after'), now)
    if (retryAfter) {
      return {
        mode: 'explicit_reset',
        cooldownUntil: retryAfter.toISOString(),
        source: 'retry_after'
      }
    }
    const providerReset = parseProviderResetHeader(
      headers.get('x-quota-reset-at')
        ?? headers.get('x-ratelimit-reset')
        ?? headers.get('x-rate-limit-reset'),
      now
    )
    if (providerReset) {
      return {
        mode: 'explicit_reset',
        cooldownUntil: providerReset.toISOString(),
        source: 'provider_header'
      }
    }
  }

  return undefined
}

function parseJsonValue(text: string | undefined): unknown {
  if (!text?.trim()) return undefined
  try {
    return JSON.parse(text)
  } catch {
    return undefined
  }
}

function findFirstField(value: unknown, names: string[]): unknown {
  if (!value || typeof value !== 'object') return undefined
  const object = value as Record<string, unknown>
  for (const name of names) {
    if (Object.prototype.hasOwnProperty.call(object, name)) return object[name]
  }
  for (const child of Object.values(object)) {
    const found = findFirstField(child, names)
    if (found !== undefined) return found
  }
  return undefined
}

function parseAbsoluteRecoveryTime(value: unknown): Date | undefined {
  const numericValue = typeof value === 'number'
    ? value
    : typeof value === 'string' && /^\d+(?:\.\d+)?$/.test(value.trim())
      ? Number(value)
      : undefined
  if (numericValue !== undefined && Number.isFinite(numericValue) && numericValue > 0) {
    const milliseconds = numericValue > 10_000_000_000 ? numericValue : numericValue * 1000
    const date = new Date(milliseconds)
    return Number.isFinite(date.getTime()) ? date : undefined
  }
  const normalized = canonicalizeRfc3339Instant(value)
  if (!normalized) return undefined
  const date = new Date(normalized)
  return Number.isFinite(date.getTime()) ? date : undefined
}

function parsePositiveSeconds(value: unknown): number | undefined {
  const seconds = typeof value === 'number'
    ? value
    : typeof value === 'string' && value.trim() && Number.isFinite(Number(value))
      ? Number(value)
      : undefined
  if (seconds === undefined || !Number.isFinite(seconds) || seconds <= 0) return undefined
  return Math.ceil(seconds)
}

function parseRetryAfter(value: string | null, now: Date): Date | undefined {
  if (!value?.trim()) return undefined
  const seconds = parsePositiveSeconds(value)
  if (seconds !== undefined) return dateAfterSeconds(now, seconds)
  const absolute = parseAbsoluteRecoveryTime(value)
  if (absolute && absolute.getTime() > now.getTime()) return absolute
  const httpDateMs = Date.parse(value)
  return Number.isFinite(httpDateMs) && httpDateMs > now.getTime() ? new Date(httpDateMs) : undefined
}

function dateAfterSeconds(now: Date, seconds: number): Date | undefined {
  const date = new Date(now.getTime() + seconds * 1000)
  return Number.isFinite(date.getTime()) ? date : undefined
}

function parseProviderResetHeader(value: string | null, now: Date): Date | undefined {
  if (!value?.trim()) return undefined
  const absolute = parseAbsoluteRecoveryTime(value)
  return absolute && absolute.getTime() > now.getTime() ? absolute : undefined
}
