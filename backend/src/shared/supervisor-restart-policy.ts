export interface SupervisorRestartState {
  restartAttempts: number
  readyAtMs?: number
}

const restartDelaySequenceMs = [
  1_000,
  5_000,
  15_000,
  30_000,
  60_000,
  120_000,
  300_000,
  600_000
] as const

export const supervisorStableResetMs = 10 * 60_000

export function createSupervisorRestartState(): SupervisorRestartState {
  return { restartAttempts: 0 }
}

export function recordSupervisorChildReady(
  state: SupervisorRestartState,
  nowMs: number
): SupervisorRestartState {
  return { ...state, readyAtMs: nowMs }
}

export function recordSupervisorChildStopped(
  state: SupervisorRestartState,
  nowMs: number
): SupervisorRestartState {
  const stable = state.readyAtMs !== undefined
    && nowMs - state.readyAtMs >= supervisorStableResetMs
  return {
    restartAttempts: stable ? 1 : state.restartAttempts + 1
  }
}

export function supervisorRestartDelayMs(restartAttempts: number): number {
  const normalizedAttempts = Math.max(1, Math.floor(restartAttempts))
  return restartDelaySequenceMs[Math.min(normalizedAttempts - 1, restartDelaySequenceMs.length - 1)]
}
