import { createHash } from 'node:crypto'

import type {
  AccountAvailabilityPresentation,
  AccountEffectiveAvailabilityStatus,
  AccountProbeKind,
  AccountProbeObservation,
  AccountProbeResult,
  AccountProbeSchedule,
  AccountProbeSummary,
  AccountRuntimeProbePresentation,
  AccountApiKeyRuntimeSummary
} from './types.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'

export interface AccountStatusPresentationInput {
  id: string
  status: string
  effectiveAvailability: {
    available: boolean
    status: AccountEffectiveAvailabilityStatus
    label: string
    color?: string
    reason?: string
    retryAt?: string
  }
  accountExpiresAt?: string
  authorizationExpiresAt?: string
  quotaResetAt?: string
  authorizationInstanceSourceAccountId?: string
  authorizationInstanceSourceAccountExpiresAt?: string
  authorizationInstanceSourceAccountCooldownUntil?: string
  authorizationInstanceSourceAccountLastErrorCode?: string
  authorizationInstanceSourceAccountLastErrorMessage?: string
  authorizationInstanceSourceAccountLastErrorTraceId?: string
  authorizationInstanceSourceAccountCooldownRetestLastAt?: string
  authorizationInstanceSourceAccountCooldownRetestLastStatusCode?: number
  authorizationInstanceSourceAccountLastHealthCheckAt?: string
  authorizationInstanceSourceAccountNextHealthCheckAt?: string
  authorizationInstanceSourceAccountLastHealthCheckStatusCode?: number
  authorizationInstanceSourceAccountLastHealthCheckErrorCode?: string
  authorizationInstanceSourceAccountLastHealthCheckErrorMessage?: string
  authorizationInstanceSourceAccountLastHealthCheckTraceId?: string
  nextHealthCheckAt?: string
  lastHealthCheckAt?: string
  lastHealthCheckStatusCode?: number
  lastHealthCheckErrorCode?: string
  lastHealthCheckErrorMessage?: string
  lastHealthCheckTraceId?: string
  lastHealthSuccessAt?: string
  cooldownRetestLastAt?: string
  cooldownRetestLastStatusCode?: number
  lastErrorCode?: string
  lastErrorMessage?: string
  lastErrorTraceId?: string
  cooldownUntil?: string
  runtimeProbe?: AccountRuntimeProbePresentation
  apiKeyProbe?: AccountProbeSummary
  apiKeyRuntime?: AccountApiKeyRuntimeSummary
  sourceAccountProbe?: AccountProbeSummary
}

export function accountProbeObservationId(input: {
  kind: AccountProbeKind
  identity: string
  attemptedAt: string
  traceId?: string
  errorCode?: string
  reason?: string
}): string {
  const attemptedAt = requiredRfc3339Instant(input.attemptedAt, '账户探针 attemptedAt')
  const fingerprint = [input.kind, input.identity, attemptedAt, input.traceId ?? '', input.errorCode ?? '', input.reason ?? ''].join('|')
  return createHash('sha256').update(fingerprint).digest('hex').slice(0, 24)
}

function requiredTimestamp(value: string, label: string): string {
  return requiredRfc3339Instant(value, label)
}

function optionalTimestamp(value: string | null | undefined, label: string): string | undefined {
  return value == null ? undefined : requiredTimestamp(value, label)
}

function timestampMilliseconds(value: string, label: string): number {
  const milliseconds = rfc3339InstantMilliseconds(value)
  if (milliseconds === undefined) throw new Error(`${label}必须是带 Z 或数值 offset 的 RFC3339 时间`)
  return milliseconds
}

function normalizedObservation(observation: AccountProbeObservation, label: string): AccountProbeObservation {
  return {
    ...observation,
    attemptedAt: requiredTimestamp(observation.attemptedAt, `${label} attemptedAt`)
  }
}

function normalizedSchedule(scheduleValue: AccountProbeSchedule, label: string): AccountProbeSchedule {
  const nextAttemptAt = optionalTimestamp(scheduleValue.nextAttemptAt, `${label} nextAttemptAt`)
  return nextAttemptAt === undefined ? { ...scheduleValue, nextAttemptAt: undefined } : { ...scheduleValue, nextAttemptAt }
}

function normalizedProbeSummary(probe: AccountProbeSummary, label: string): AccountProbeSummary {
  return {
    ...probe,
    ...(probe.lastObservation
      ? { lastObservation: normalizedObservation(probe.lastObservation, `${label} observation`) }
      : {}),
    schedule: normalizedSchedule(probe.schedule, `${label} schedule`)
  }
}

function normalizedRuntimeProbe(probe: AccountRuntimeProbePresentation, label: string): AccountRuntimeProbePresentation {
  const recoveryAt = optionalTimestamp(probe.recoveryAt, `${label} recoveryAt`)
  return {
    ...probe,
    ...(probe.lastObservation
      ? { lastObservation: normalizedObservation(probe.lastObservation, `${label} observation`) }
      : {}),
    schedule: normalizedSchedule(probe.schedule, `${label} schedule`),
    ...(recoveryAt === undefined ? { recoveryAt: undefined } : { recoveryAt })
  }
}

function normalizedAccount(account: AccountStatusPresentationInput): AccountStatusPresentationInput {
  const effectiveRetryAt = optionalTimestamp(account.effectiveAvailability.retryAt, '账户 effectiveAvailability retryAt')
  const apiKeyRuntime = account.apiKeyRuntime
  return {
    ...account,
    effectiveAvailability: {
      ...account.effectiveAvailability,
      ...(effectiveRetryAt === undefined ? { retryAt: undefined } : { retryAt: effectiveRetryAt })
    },
    accountExpiresAt: optionalTimestamp(account.accountExpiresAt, '账户 accountExpiresAt'),
    authorizationExpiresAt: optionalTimestamp(account.authorizationExpiresAt, '账户 authorizationExpiresAt'),
    quotaResetAt: optionalTimestamp(account.quotaResetAt, '账户 quotaResetAt'),
    authorizationInstanceSourceAccountExpiresAt: optionalTimestamp(account.authorizationInstanceSourceAccountExpiresAt, '授权来源 accountExpiresAt'),
    authorizationInstanceSourceAccountCooldownUntil: optionalTimestamp(account.authorizationInstanceSourceAccountCooldownUntil, '授权来源 cooldownUntil'),
    authorizationInstanceSourceAccountCooldownRetestLastAt: optionalTimestamp(account.authorizationInstanceSourceAccountCooldownRetestLastAt, '授权来源 cooldownRetestLastAt'),
    authorizationInstanceSourceAccountLastHealthCheckAt: optionalTimestamp(account.authorizationInstanceSourceAccountLastHealthCheckAt, '授权来源 lastHealthCheckAt'),
    authorizationInstanceSourceAccountNextHealthCheckAt: optionalTimestamp(account.authorizationInstanceSourceAccountNextHealthCheckAt, '授权来源 nextHealthCheckAt'),
    nextHealthCheckAt: optionalTimestamp(account.nextHealthCheckAt, '账户 nextHealthCheckAt'),
    lastHealthCheckAt: optionalTimestamp(account.lastHealthCheckAt, '账户 lastHealthCheckAt'),
    lastHealthSuccessAt: optionalTimestamp(account.lastHealthSuccessAt, '账户 lastHealthSuccessAt'),
    cooldownRetestLastAt: optionalTimestamp(account.cooldownRetestLastAt, '账户 cooldownRetestLastAt'),
    cooldownUntil: optionalTimestamp(account.cooldownUntil, '账户 cooldownUntil'),
    ...(account.runtimeProbe
      ? { runtimeProbe: normalizedRuntimeProbe(account.runtimeProbe, '账户 runtime probe') }
      : { runtimeProbe: undefined }),
    ...(account.apiKeyProbe
      ? { apiKeyProbe: normalizedProbeSummary(account.apiKeyProbe, '账户 API Key probe') }
      : { apiKeyProbe: undefined }),
    ...(account.sourceAccountProbe
      ? { sourceAccountProbe: normalizedProbeSummary(account.sourceAccountProbe, '账户来源 probe') }
      : { sourceAccountProbe: undefined }),
    ...(apiKeyRuntime
      ? {
          apiKeyRuntime: {
            ...apiKeyRuntime,
            nextProbeAt: optionalTimestamp(apiKeyRuntime.nextProbeAt, '账户 API Key nextProbeAt'),
            lastFailureAt: optionalTimestamp(apiKeyRuntime.lastFailureAt, '账户 API Key lastFailureAt')
          }
        }
      : { apiKeyRuntime: undefined })
  }
}

function schedule(nextAttemptAt: string | undefined, now: Date, running = false, label = '账户 probe schedule'): AccountProbeSchedule {
  const normalizedNextAttemptAt = optionalTimestamp(nextAttemptAt, `${label} nextAttemptAt`)
  if (running) return { state: 'running' }
  if (normalizedNextAttemptAt === undefined) return { state: 'none' }
  const timestamp = timestampMilliseconds(normalizedNextAttemptAt, `${label} nextAttemptAt`)
  return { state: timestamp <= now.getTime() ? 'due_waiting' : 'scheduled', nextAttemptAt: normalizedNextAttemptAt }
}

function hasProbeFact(probe: AccountProbeSummary | undefined): probe is AccountProbeSummary {
  return Boolean(probe && (probe.lastObservation || probe.schedule.state !== 'none'))
}

function healthObservation(account: AccountStatusPresentationInput, kind: AccountProbeKind): AccountProbeObservation | undefined {
  const attemptedAt = account.lastHealthCheckAt
  if (!attemptedAt) return undefined
  const normalizedAttemptedAt = requiredTimestamp(attemptedAt, `账户 ${kind} attemptedAt`)
  const failed = Boolean(account.lastHealthCheckErrorCode || account.lastHealthCheckErrorMessage || (account.lastHealthCheckStatusCode && account.lastHealthCheckStatusCode >= 400))
  const result: AccountProbeResult = failed ? 'failed' : 'success'
  const reason = failed ? account.lastHealthCheckErrorMessage : undefined
  const errorCode = failed ? account.lastHealthCheckErrorCode : undefined
  const traceId = failed ? account.lastHealthCheckTraceId : undefined
  return {
    observationId: accountProbeObservationId({ kind, identity: account.id, attemptedAt: normalizedAttemptedAt, traceId, errorCode, reason }),
    attemptedAt: normalizedAttemptedAt,
    result,
    httpStatus: account.lastHealthCheckStatusCode,
    errorCode,
    reason,
    traceId
  }
}

const mappings: Record<AccountEffectiveAvailabilityStatus, { status: AccountAvailabilityPresentation['status']; action: AccountAvailabilityPresentation['action'] }> = {
  available: { status: 'available', action: 'none' },
  permission_denied: { status: 'permission_denied', action: 'contact_admin' },
  authorization_expired: { status: 'expired', action: 'renew_authorization' },
  authorization_paused: { status: 'authorization_blocked', action: 'contact_authorizer' },
  authorization_unavailable: { status: 'authorization_blocked', action: 'contact_authorizer' },
  authorization_quota_exceeded: { status: 'authorization_blocked', action: 'contact_authorizer' },
  source_deleted: { status: 'source_blocked', action: 'contact_authorizer' },
  source_expired: { status: 'expired', action: 'contact_authorizer' },
  source_disabled: { status: 'source_blocked', action: 'contact_authorizer' },
  source_pending_test: { status: 'pending_check', action: 'contact_authorizer' },
  source_error: { status: 'error', action: 'contact_authorizer' },
  source_rate_limited: { status: 'rate_limited', action: 'contact_authorizer' },
  source_temporary_unavailable: { status: 'temporarily_unavailable', action: 'contact_authorizer' },
  source_quality_isolated: { status: 'source_blocked', action: 'contact_authorizer' },
  source_cooldown: { status: 'temporarily_unavailable', action: 'contact_authorizer' },
  source_unschedulable: { status: 'source_blocked', action: 'contact_authorizer' },
  instance_expired: { status: 'expired', action: 'renew_authorization' },
  instance_disabled: { status: 'disabled', action: 'enable_account' },
  instance_pending_test: { status: 'pending_check', action: 'retry_check' },
  instance_error: { status: 'error', action: 'fix_configuration' },
  instance_rate_limited: { status: 'rate_limited', action: 'restore_account' },
  instance_temporary_unavailable: { status: 'temporarily_unavailable', action: 'restore_account' },
  instance_quality_isolated: { status: 'error', action: 'retry_check' },
  instance_cooldown: { status: 'temporarily_unavailable', action: 'restore_account' },
  instance_unschedulable: { status: 'disabled', action: 'enable_account' },
  binding_missing: { status: 'binding_missing', action: 'bind_group' },
  api_key_pool_unavailable: { status: 'key_pool_unavailable', action: 'retry_check' },
  runtime_degraded: { status: 'degraded', action: 'restore_account' },
  runtime_local_suppressed: { status: 'avoided', action: 'restore_account' },
  runtime_half_open: { status: 'verifying', action: 'none' },
  runtime_precheck_pending: { status: 'verifying', action: 'restore_account' },
  runtime_precheck_failed: { status: 'verification_failed', action: 'retry_check' }
}

export function accountAvailabilityPresentation(account: AccountStatusPresentationInput, now = new Date()): AccountAvailabilityPresentation {
  account = normalizedAccount(account)
  const effective = account.effectiveAvailability
  const mapping = mappings[effective.status]
  const presentation: AccountAvailabilityPresentation = {
    status: mapping.status,
    label: effective.label,
    reason: effective.reason,
    action: mapping.action
  }
  // cooldown_until is the durable due time consumed by the cooldown retest
  // worker for persisted temporary-unavailable and rate-limited states.
  // It is only a status boundary for an otherwise active instance/source
  // account that is waiting for its own cooldown to expire.
  const cooldownProbeSchedule = schedule(account.cooldownUntil, now, false, '账户 cooldown')
  const apiKeyProbe = account.apiKeyProbe ?? accountApiKeyProbe(account, now)

  if (effective.status === 'instance_error' && account.lastErrorCode === 'account_activation_check_timeout') presentation.action = 'retry_check'
  else if (effective.status === 'instance_error' && account.lastErrorCode?.startsWith('cooldown_retest_')) presentation.action = 'restore_account'

  if (effective.status === 'authorization_expired' && account.authorizationExpiresAt !== undefined) presentation.statusBoundary = { at: account.authorizationExpiresAt, kind: 'authorization_expired' }
  else if (effective.status === 'source_expired' && account.authorizationInstanceSourceAccountExpiresAt !== undefined) presentation.statusBoundary = { at: account.authorizationInstanceSourceAccountExpiresAt, kind: 'source_expired' }
  else if (effective.status === 'instance_expired' && account.accountExpiresAt !== undefined) presentation.statusBoundary = { at: account.accountExpiresAt, kind: 'account_expired' }
  else if (effective.status === 'authorization_quota_exceeded' && account.quotaResetAt !== undefined) presentation.statusBoundary = { at: account.quotaResetAt, kind: 'quota_reset' }
  else if (effective.status === 'source_cooldown' && account.authorizationInstanceSourceAccountCooldownUntil !== undefined) presentation.statusBoundary = { at: account.authorizationInstanceSourceAccountCooldownUntil, kind: 'cooldown_expiry' }
  else if (effective.status === 'instance_cooldown' && account.cooldownUntil !== undefined) presentation.statusBoundary = { at: account.cooldownUntil, kind: 'cooldown_expiry' }
  else if (
    effective.status === 'runtime_local_suppressed'
    && account.runtimeProbe?.recoveryAtKind === 'policy_ttl_expiry'
    && account.runtimeProbe.recoveryAt !== undefined
  ) presentation.statusBoundary = { at: account.runtimeProbe.recoveryAt, kind: 'policy_ttl_expiry' }

  if (presentation.statusBoundary) return presentation
  if (effective.status === 'instance_disabled' || effective.status === 'source_disabled' || effective.status === 'binding_missing' || effective.status === 'permission_denied' || effective.status === 'authorization_paused' || effective.status === 'authorization_unavailable' || effective.status === 'source_deleted' || effective.status === 'source_unschedulable' || effective.status === 'instance_unschedulable') return presentation

  let probe: AccountProbeSummary | undefined
  if (effective.status === 'api_key_pool_unavailable') probe = apiKeyProbe
  else if (effective.status.startsWith('runtime_')) {
    if (account.runtimeProbe) probe = {
      kind: 'runtime_probe',
      lastObservation: account.runtimeProbe.lastObservation,
      schedule: account.runtimeProbe.schedule
    }
  } else if (effective.status.startsWith('source_')) probe = account.sourceAccountProbe ?? sourceAccountProbe(account, now)
  else if (effective.status === 'instance_pending_test') probe = { kind: 'activation_check', lastObservation: healthObservation(account, 'activation_check'), schedule: schedule(account.nextHealthCheckAt, now, false, '账户健康检查') }
  else if (effective.status === 'instance_temporary_unavailable' || effective.status === 'instance_rate_limited') probe = { kind: 'cooldown_retest', lastObservation: account.cooldownRetestLastAt ? {
    observationId: accountProbeObservationId({ kind: 'cooldown_retest', identity: account.id, attemptedAt: account.cooldownRetestLastAt, traceId: account.lastErrorTraceId, errorCode: account.lastErrorCode, reason: account.lastErrorMessage }),
    attemptedAt: account.cooldownRetestLastAt, result: account.lastErrorCode || account.lastErrorMessage || (account.cooldownRetestLastStatusCode && account.cooldownRetestLastStatusCode >= 400) ? 'failed' : 'success', httpStatus: account.cooldownRetestLastStatusCode, errorCode: account.lastErrorCode, reason: account.lastErrorMessage, traceId: account.lastErrorTraceId
  } : undefined, schedule: cooldownProbeSchedule }
  else if (effective.status === 'instance_error' && account.lastErrorCode === 'account_activation_check_timeout') probe = { kind: 'activation_check', lastObservation: healthObservation(account, 'activation_check'), schedule: schedule(account.nextHealthCheckAt, now, false, '账户健康检查') }
  else if (effective.status === 'instance_error' && account.lastErrorCode?.startsWith('cooldown_retest_')) probe = { kind: 'cooldown_retest', lastObservation: account.cooldownRetestLastAt ? {
    observationId: accountProbeObservationId({ kind: 'cooldown_retest', identity: account.id, attemptedAt: account.cooldownRetestLastAt, traceId: account.lastErrorTraceId, errorCode: account.lastErrorCode, reason: account.lastErrorMessage }),
    attemptedAt: account.cooldownRetestLastAt, result: 'failed', httpStatus: account.cooldownRetestLastStatusCode, errorCode: account.lastErrorCode, reason: account.lastErrorMessage, traceId: account.lastErrorTraceId
  } : undefined, schedule: cooldownProbeSchedule }
  else if (effective.status === 'instance_error') probe = {
    kind: 'health_check',
    lastObservation: healthObservation(account, 'health_check'),
    // Generic account errors have no automatic recovery policy. Keep the
    // observed probe fact visible without inventing a future check time.
    schedule: { state: 'none' }
  }
  else if (effective.status === 'available') probe = { kind: 'health_check', lastObservation: healthObservation(account, 'health_check'), schedule: schedule(account.nextHealthCheckAt, now, false, '账户健康检查') }
  if (hasProbeFact(probe) || effective.status === 'instance_error') presentation.probe = probe
  return presentation
}

function sourceAccountProbe(account: AccountStatusPresentationInput, now: Date): AccountProbeSummary | undefined {
  const effectiveStatus = account.effectiveAvailability.status
  const cooldownDriven = effectiveStatus === 'source_rate_limited'
    || effectiveStatus === 'source_temporary_unavailable'
    || (effectiveStatus === 'source_error' && account.authorizationInstanceSourceAccountLastErrorCode?.startsWith('cooldown_retest_'))
  const attemptedAt = cooldownDriven
    ? account.authorizationInstanceSourceAccountCooldownRetestLastAt
    : account.authorizationInstanceSourceAccountLastHealthCheckAt
  const errorCode = cooldownDriven
    ? account.authorizationInstanceSourceAccountLastErrorCode
    : account.authorizationInstanceSourceAccountLastHealthCheckErrorCode
  const reason = cooldownDriven
    ? account.authorizationInstanceSourceAccountLastErrorMessage
    : account.authorizationInstanceSourceAccountLastHealthCheckErrorMessage
  const traceId = cooldownDriven
    ? account.authorizationInstanceSourceAccountLastErrorTraceId
    : account.authorizationInstanceSourceAccountLastHealthCheckTraceId
  const httpStatus = cooldownDriven
    ? account.authorizationInstanceSourceAccountCooldownRetestLastStatusCode
    : account.authorizationInstanceSourceAccountLastHealthCheckStatusCode
  const lastObservation = attemptedAt
    ? {
        observationId: accountProbeObservationId({ kind: 'source_account_probe', identity: account.authorizationInstanceSourceAccountId ?? account.id, attemptedAt, traceId, errorCode, reason }),
        attemptedAt,
        result: errorCode || reason || (httpStatus && httpStatus >= 400) ? 'failed' as const : 'success' as const,
        httpStatus,
        errorCode,
        reason,
        traceId
      }
    : undefined
  const nextAttemptAt = cooldownDriven
    ? account.authorizationInstanceSourceAccountCooldownUntil
    : account.authorizationInstanceSourceAccountNextHealthCheckAt
  return { kind: 'source_account_probe', lastObservation, schedule: schedule(nextAttemptAt, now) }
}

function accountApiKeyProbe(account: AccountStatusPresentationInput, now: Date): AccountProbeSummary | undefined {
  const runtime = account.apiKeyRuntime
  if (!runtime) return undefined
  const lastObservation = runtime.lastFailureAt
    ? {
        observationId: accountProbeObservationId({
          kind: 'api_key_retest',
          identity: account.id,
          attemptedAt: runtime.lastFailureAt,
          traceId: runtime.lastTraceId,
          errorCode: runtime.lastErrorCode,
          reason: runtime.lastErrorMessage
        }),
        attemptedAt: runtime.lastFailureAt,
        result: 'failed' as const,
        errorCode: runtime.lastErrorCode,
        reason: runtime.lastErrorMessage,
        traceId: runtime.lastTraceId
      }
    : undefined
  return {
    kind: 'api_key_retest',
    lastObservation,
    schedule: schedule(runtime.nextProbeAt, now)
  }
}
