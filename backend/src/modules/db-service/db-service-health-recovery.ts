export const dbServiceHealthRecoveryDefaults = {
  startupGraceMs: 180_000,
  failureThreshold: 3,
  recoveryCooldownMs: 60_000,
  recoveryBudgetWindowMs: 15 * 60_000,
  recoveryBudgetMaxAttempts: 3
} as const

export interface DbServiceHealthRecoveryState {
  childStartedAtMs: number
  consecutiveFailures: number
  lastRecoveryAtMs?: number
  recoveryAttemptsMs: number[]
}

export type DbServiceHealthRecoveryAction =
  | 'none'
  | 'ignored_grace'
  | 'recover'
  | 'suppressed_cooldown'
  | 'suppressed_budget'

export interface DbServiceHealthRecoveryResult {
  state: DbServiceHealthRecoveryState
  action: DbServiceHealthRecoveryAction
}

export function createDbServiceHealthRecoveryState(childStartedAtMs: number): DbServiceHealthRecoveryState {
  return {
    childStartedAtMs,
    consecutiveFailures: 0,
    recoveryAttemptsMs: []
  }
}

export function resetDbServiceHealthRecoveryState(
  state: DbServiceHealthRecoveryState,
  childStartedAtMs: number
): DbServiceHealthRecoveryState {
  return {
    childStartedAtMs,
    consecutiveFailures: 0,
    lastRecoveryAtMs: state.lastRecoveryAtMs,
    recoveryAttemptsMs: [...state.recoveryAttemptsMs]
  }
}

export function recordDbServiceHealthProbe(
  state: DbServiceHealthRecoveryState,
  input: { nowMs: number; healthy: boolean }
): DbServiceHealthRecoveryResult {
  const recoveryAttemptsMs = state.recoveryAttemptsMs.filter(
    (attemptedAtMs) => input.nowMs - attemptedAtMs < dbServiceHealthRecoveryDefaults.recoveryBudgetWindowMs
  )
  const currentState = { ...state, recoveryAttemptsMs }

  if (input.nowMs - state.childStartedAtMs < dbServiceHealthRecoveryDefaults.startupGraceMs) {
    return {
      state: { ...currentState, consecutiveFailures: 0 },
      action: 'ignored_grace'
    }
  }
  if (input.healthy) {
    return {
      state: { ...currentState, consecutiveFailures: 0 },
      action: 'none'
    }
  }

  const consecutiveFailures = currentState.consecutiveFailures + 1
  if (consecutiveFailures < dbServiceHealthRecoveryDefaults.failureThreshold) {
    return {
      state: { ...currentState, consecutiveFailures },
      action: 'none'
    }
  }
  if (currentState.lastRecoveryAtMs !== undefined
    && input.nowMs - currentState.lastRecoveryAtMs < dbServiceHealthRecoveryDefaults.recoveryCooldownMs) {
    return {
      state: { ...currentState, consecutiveFailures },
      action: 'suppressed_cooldown'
    }
  }
  if (recoveryAttemptsMs.length >= dbServiceHealthRecoveryDefaults.recoveryBudgetMaxAttempts) {
    return {
      state: { ...currentState, consecutiveFailures },
      action: 'suppressed_budget'
    }
  }

  return {
    state: {
      ...currentState,
      consecutiveFailures: 0,
      lastRecoveryAtMs: input.nowMs,
      recoveryAttemptsMs: [...recoveryAttemptsMs, input.nowMs]
    },
    action: 'recover'
  }
}
