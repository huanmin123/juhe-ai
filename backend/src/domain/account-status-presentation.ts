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
  cooldownNextProbeAt?: string
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
  const fingerprint = [input.kind, input.identity, input.attemptedAt, input.traceId ?? '', input.errorCode ?? '', input.reason ?? ''].join('|')
  return createHash('sha256').update(fingerprint).digest('hex').slice(0, 24)
}

function schedule(nextAttemptAt: string | undefined, now: Date, running = false): AccountProbeSchedule {
  if (running) return { state: 'running' }
  if (!nextAttemptAt) return { state: 'none' }
  const timestamp = new Date(nextAttemptAt).getTime()
  if (!Number.isFinite(timestamp)) return { state: 'none' }
  return { state: timestamp <= now.getTime() ? 'due_waiting' : 'scheduled', nextAttemptAt }
}

function hasValidDateTime(value: string | undefined): value is string {
  return Boolean(value && Number.isFinite(new Date(value).getTime()))
}

function hasProbeFact(probe: AccountProbeSummary | undefined): probe is AccountProbeSummary {
  return Boolean(probe && (probe.lastObservation || probe.schedule.state !== 'none'))
}

function healthObservation(account: AccountStatusPresentationInput, kind: AccountProbeKind): AccountProbeObservation | undefined {
  const attemptedAt = account.lastHealthCheckAt
  if (!attemptedAt) return undefined
  const failed = Boolean(account.lastHealthCheckErrorCode || account.lastHealthCheckErrorMessage || (account.lastHealthCheckStatusCode && account.lastHealthCheckStatusCode >= 400))
  const result: AccountProbeResult = failed ? 'failed' : 'success'
  const reason = failed ? account.lastHealthCheckErrorMessage : undefined
  const errorCode = failed ? account.lastHealthCheckErrorCode : undefined
  const traceId = failed ? account.lastHealthCheckTraceId : undefined
  return {
    observationId: accountProbeObservationId({ kind, identity: account.id, attemptedAt, traceId, errorCode, reason }),
    attemptedAt,
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
  source_cooldown: { status: 'temporarily_unavailable', action: 'contact_authorizer' },
  source_unschedulable: { status: 'source_blocked', action: 'contact_authorizer' },
  instance_expired: { status: 'expired', action: 'renew_authorization' },
  instance_disabled: { status: 'disabled', action: 'enable_account' },
  instance_pending_test: { status: 'pending_check', action: 'retry_check' },
  instance_error: { status: 'error', action: 'fix_configuration' },
  instance_rate_limited: { status: 'rate_limited', action: 'restore_account' },
  instance_temporary_unavailable: { status: 'temporarily_unavailable', action: 'restore_account' },
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
  const effective = account.effectiveAvailability
  const mapping = mappings[effective.status]
  const presentation: AccountAvailabilityPresentation = {
    status: mapping.status,
    label: effective.label,
    reason: effective.reason,
    action: mapping.action
  }
  const cooldownProbeSchedule = schedule(account.cooldownNextProbeAt, now)
  const apiKeyProbe = account.apiKeyProbe ?? accountApiKeyProbe(account, now)

  if (effective.status === 'instance_error' && account.lastErrorCode === 'account_activation_check_timeout') presentation.action = 'retry_check'
  else if (effective.status === 'instance_error' && account.lastErrorCode?.startsWith('cooldown_retest_')) presentation.action = 'restore_account'

  if (effective.status === 'authorization_expired' && hasValidDateTime(account.authorizationExpiresAt)) presentation.statusBoundary = { at: account.authorizationExpiresAt, kind: 'authorization_expired' }
  else if (effective.status === 'source_expired' && hasValidDateTime(account.authorizationInstanceSourceAccountExpiresAt)) presentation.statusBoundary = { at: account.authorizationInstanceSourceAccountExpiresAt, kind: 'source_expired' }
  else if (effective.status === 'instance_expired' && hasValidDateTime(account.accountExpiresAt)) presentation.statusBoundary = { at: account.accountExpiresAt, kind: 'account_expired' }
  else if (effective.status === 'authorization_quota_exceeded' && hasValidDateTime(account.quotaResetAt)) presentation.statusBoundary = { at: account.quotaResetAt, kind: 'quota_reset' }
  else if (effective.status === 'source_cooldown' && !hasProbeFact(account.sourceAccountProbe ?? sourceAccountProbe(account, now)) && hasValidDateTime(account.authorizationInstanceSourceAccountCooldownUntil)) presentation.statusBoundary = { at: account.authorizationInstanceSourceAccountCooldownUntil, kind: 'cooldown_expiry' }
  else if (effective.status === 'instance_cooldown' && cooldownProbeSchedule.state === 'none' && hasValidDateTime(account.cooldownUntil)) presentation.statusBoundary = { at: account.cooldownUntil, kind: 'cooldown_expiry' }
  else if (effective.status === 'runtime_local_suppressed' && hasValidDateTime(effective.retryAt)) presentation.statusBoundary = { at: effective.retryAt, kind: 'policy_ttl_expiry' }

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
  else if (effective.status === 'instance_pending_test') probe = { kind: 'activation_check', lastObservation: healthObservation(account, 'activation_check'), schedule: schedule(account.nextHealthCheckAt, now) }
  else if (effective.status === 'instance_temporary_unavailable' || effective.status === 'instance_rate_limited' || effective.status === 'instance_cooldown') probe = { kind: 'cooldown_retest', lastObservation: account.cooldownRetestLastAt ? {
    observationId: accountProbeObservationId({ kind: 'cooldown_retest', identity: account.id, attemptedAt: account.cooldownRetestLastAt, traceId: account.lastErrorTraceId, errorCode: account.lastErrorCode, reason: account.lastErrorMessage }),
    attemptedAt: account.cooldownRetestLastAt, result: account.lastErrorCode || account.lastErrorMessage || (account.cooldownRetestLastStatusCode && account.cooldownRetestLastStatusCode >= 400) ? 'failed' : 'success', httpStatus: account.cooldownRetestLastStatusCode, errorCode: account.lastErrorCode, reason: account.lastErrorMessage, traceId: account.lastErrorTraceId
  } : undefined, schedule: cooldownProbeSchedule }
  else if (effective.status === 'instance_error' && account.lastErrorCode === 'account_activation_check_timeout') probe = { kind: 'activation_check', lastObservation: healthObservation(account, 'activation_check'), schedule: schedule(account.nextHealthCheckAt, now) }
  else if (effective.status === 'instance_error' && account.lastErrorCode?.startsWith('cooldown_retest_')) probe = { kind: 'cooldown_retest', lastObservation: account.cooldownRetestLastAt ? {
    observationId: accountProbeObservationId({ kind: 'cooldown_retest', identity: account.id, attemptedAt: account.cooldownRetestLastAt, traceId: account.lastErrorTraceId, errorCode: account.lastErrorCode, reason: account.lastErrorMessage }),
    attemptedAt: account.cooldownRetestLastAt, result: 'failed', httpStatus: account.cooldownRetestLastStatusCode, errorCode: account.lastErrorCode, reason: account.lastErrorMessage, traceId: account.lastErrorTraceId
  } : undefined, schedule: cooldownProbeSchedule }
  else if (effective.status === 'available') probe = { kind: 'health_check', lastObservation: healthObservation(account, 'health_check'), schedule: schedule(account.nextHealthCheckAt, now) }
  if (hasProbeFact(probe)) presentation.probe = probe
  return presentation
}

function sourceAccountProbe(account: AccountStatusPresentationInput, now: Date): AccountProbeSummary | undefined {
  const effectiveStatus = account.effectiveAvailability.status
  const cooldownDriven = effectiveStatus === 'source_rate_limited'
    || effectiveStatus === 'source_temporary_unavailable'
    || effectiveStatus === 'source_cooldown'
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
  const nextAttemptAt = cooldownDriven ? undefined : account.authorizationInstanceSourceAccountNextHealthCheckAt
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
