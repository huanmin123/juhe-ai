import { passiveScheduleDelayMs, passiveScheduleNotBeforeDelayMs } from '../../../shared/passive-schedule-jitter.js'

export const accountPrecheckProbeIntervalMs = 2 * 60_000
export const accountPrecheckMinimumObservationMs = 5 * 60_000

export function nextAccountPrecheckProbeAtMs(input: {
  attemptCount: number
  maxAttempts: number
  startedAtMs: number
  nowMs: number
}): number | undefined {
  if (input.attemptCount < input.maxAttempts) {
    return input.nowMs + passiveScheduleDelayMs(accountPrecheckProbeIntervalMs)
  }
  const confirmationAtMs = input.startedAtMs + accountPrecheckMinimumObservationMs
  return input.nowMs < confirmationAtMs
    ? input.nowMs + passiveScheduleNotBeforeDelayMs(confirmationAtMs - input.nowMs)
    : undefined
}
