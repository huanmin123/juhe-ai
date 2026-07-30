import type { GatewaySettings } from '../gateway/policy/account-error-policy.service.js'

export const accountDiagnosticRetryTimeoutMs = [10_000, 20_000, 30_000] as const
export const accountDiagnosticRetryMaxTotalTimeoutMs = accountDiagnosticRetryTimeoutMs.reduce((sum, timeoutMs) => sum + timeoutMs, 0)
export const accountImageDiagnosticRetryTimeoutMs = [120_000] as const

export function accountDiagnosticRetryTimeouts(probeKind: 'generation' | 'image_generation' | 'models_catalog'): readonly number[] {
  return probeKind === 'image_generation'
    ? accountImageDiagnosticRetryTimeoutMs
    : accountDiagnosticRetryTimeoutMs
}

export interface AccountDiagnosticAttemptProgress {
  attemptIndex: number
  attemptNumber: number
  totalAttempts: number
  timeoutMs: number
  maxTotalTimeoutMs: number
  elapsedMs: number
}

export type AccountDiagnosticAttemptProgressHandler = (progress: AccountDiagnosticAttemptProgress) => void

export interface AccountDiagnosticAttemptSignal {
  signal: AbortSignal
  timeoutSignal: AbortSignal
}

export function diagnosticAccountTestGatewaySettingsOverride(
  override: Partial<GatewaySettings> | undefined,
  timeoutMs: number
): Partial<GatewaySettings> {
  const timeoutSeconds = Math.max(1, Math.ceil(timeoutMs / 1000))
  return {
    ...override,
    temporaryUnschedulableRetryAttempts: 0,
    temporaryUnschedulableRetryIntervalSeconds: 0,
    textFirstResponseTimeoutSeconds: timeoutSeconds,
    textStreamIdleTimeoutSeconds: timeoutSeconds,
    noAvailableAccountWaitTimeoutSeconds: timeoutSeconds,
    textUncommittedAttemptMaxLifetimeSeconds: Math.max(60, timeoutSeconds)
  }
}

export function diagnosticAttemptSignal(signal: AbortSignal | undefined, timeoutMs: number): AbortSignal {
  return diagnosticAttemptSignals(signal, timeoutMs).signal
}

export function diagnosticAttemptSignals(signal: AbortSignal | undefined, timeoutMs: number): AccountDiagnosticAttemptSignal {
  const timeoutSignal = AbortSignal.timeout(Math.max(1, Math.trunc(timeoutMs)))
  if (!signal) {
    return { signal: timeoutSignal, timeoutSignal }
  }
  if (signal.aborted) {
    return { signal, timeoutSignal }
  }
  return { signal: AbortSignal.any([signal, timeoutSignal]), timeoutSignal }
}

export function isDiagnosticTimeoutSignal(signal: AbortSignal): boolean {
  const reason = signal.reason
  if (typeof reason === 'string') return /timeout|deadline/i.test(reason)
  if (!reason || typeof reason !== 'object') return false
  const candidate = reason as { name?: unknown; message?: unknown }
  return candidate.name === 'TimeoutError'
    || (typeof candidate.message === 'string' && /timeout|deadline/i.test(candidate.message))
}

export function accountDiagnosticAttemptProgress(
  attemptIndex: number,
  timeoutMs: number,
  startedAt: number,
  timeoutSchedule: readonly number[] = accountDiagnosticRetryTimeoutMs
): AccountDiagnosticAttemptProgress {
  return {
    attemptIndex,
    attemptNumber: attemptIndex + 1,
    totalAttempts: timeoutSchedule.length,
    timeoutMs,
    maxTotalTimeoutMs: timeoutSchedule.reduce((sum, attemptTimeoutMs) => sum + attemptTimeoutMs, 0),
    elapsedMs: Math.max(0, Date.now() - startedAt)
  }
}
