import type { GatewaySettings } from '../gateway/account-error-policy.service.js'

export const accountDiagnosticRetryTimeoutMs = [10_000, 20_000, 30_000] as const
export const accountDiagnosticRetryMaxTotalTimeoutMs = accountDiagnosticRetryTimeoutMs.reduce((sum, timeoutMs) => sum + timeoutMs, 0)

export interface AccountDiagnosticAttemptProgress {
  attemptIndex: number
  attemptNumber: number
  totalAttempts: number
  timeoutMs: number
  maxTotalTimeoutMs: number
  elapsedMs: number
}

export type AccountDiagnosticAttemptProgressHandler = (progress: AccountDiagnosticAttemptProgress) => void

export function diagnosticAccountTestGatewaySettingsOverride(
  override: Partial<GatewaySettings> | undefined,
  timeoutMs: number
): Partial<GatewaySettings> {
  const timeoutSeconds = Math.max(1, Math.ceil(timeoutMs / 1000))
  return {
    ...override,
    temporaryUnschedulableRetryAttempts: 0,
    temporaryUnschedulableRetryIntervalSeconds: 0,
    streamRequestTimeoutSeconds: timeoutSeconds,
    streamIdleTimeoutSeconds: timeoutSeconds
  }
}

export function diagnosticAttemptSignal(signal: AbortSignal | undefined, timeoutMs: number): AbortSignal {
  const timeoutSignal = AbortSignal.timeout(Math.max(1, Math.trunc(timeoutMs)))
  if (!signal) {
    return timeoutSignal
  }
  if (signal.aborted) {
    return signal
  }
  return AbortSignal.any([signal, timeoutSignal])
}

export function isDiagnosticTimeoutSignal(signal: AbortSignal): boolean {
  const reason = signal.reason
  return Boolean(reason && typeof reason === 'object' && 'name' in reason && reason.name === 'TimeoutError')
}

export function accountDiagnosticAttemptProgress(
  attemptIndex: number,
  timeoutMs: number,
  startedAt: number
): AccountDiagnosticAttemptProgress {
  return {
    attemptIndex,
    attemptNumber: attemptIndex + 1,
    totalAttempts: accountDiagnosticRetryTimeoutMs.length,
    timeoutMs,
    maxTotalTimeoutMs: accountDiagnosticRetryMaxTotalTimeoutMs,
    elapsedMs: Math.max(0, Date.now() - startedAt)
  }
}
