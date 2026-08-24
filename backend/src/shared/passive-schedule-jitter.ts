/**
 * Global policy for passive, periodic business work.
 *
 * Lease renewal, request timeouts, heartbeats, and event-driven recovery do
 * not use it because their freshness/ownership semantics require an exact
 * deadline. Passive polling and periodic scans, including queue/projector
 * polling, must use it to prevent same-phase fleets from converging.
 */
export const passiveScheduleJitterPolicy = {
  subMinuteWindowMs: 30_000,
  minuteWindowMs: 30_000,
  hourWindowMs: 30 * 60_000,
  dayWindowMs: 60 * 60_000,
  weekWindowMs: 8 * 60 * 60_000
} as const

/** Returns the symmetric jitter window for one passive interval. */
export function passiveScheduleJitterWindowMs(intervalMs: number): number {
  const interval = Math.max(1, Math.trunc(Number(intervalMs) || 1))
  let windowMs: number
  if (interval < 60_000) {
    // Never let a short interval become negative or run back-to-back.
    windowMs = Math.min(passiveScheduleJitterPolicy.subMinuteWindowMs, Math.floor(interval / 2))
  } else if (interval < 60 * 60_000) {
    windowMs = passiveScheduleJitterPolicy.minuteWindowMs
  } else if (interval < 24 * 60 * 60_000) {
    windowMs = passiveScheduleJitterPolicy.hourWindowMs
  } else if (interval < 7 * 24 * 60 * 60_000) {
    windowMs = passiveScheduleJitterPolicy.dayWindowMs
  } else {
    windowMs = passiveScheduleJitterPolicy.weekWindowMs
  }
  // A symmetric window must leave a positive delay even for very small values.
  return Math.min(windowMs, Math.max(0, Math.floor(interval / 2)))
}

/**
 * Produces a fresh, bounded, symmetric offset. A zero result is changed to
 * one millisecond so a passive run is never intentionally placed at the exact
 * configured timestamp.
 */
export function passiveScheduleOffsetMs(intervalMs: number, random = Math.random): number {
  return passiveScheduleOffsetWithinWindowMs(passiveScheduleJitterWindowMs(intervalMs), random)
}

/**
 * Smears a delayed startup without allowing a configured short startup delay
 * to be pulled forward to zero. Immediate, event-driven startup remains the
 * caller's explicit choice.
 */
export function passiveScheduleInitialDelayMs(initialDelayMs: number, intervalMs: number, random = Math.random): number {
  const delayMs = Math.max(1, Math.trunc(Number(initialDelayMs) || 1))
  const windowMs = Math.min(passiveScheduleJitterWindowMs(intervalMs), Math.floor(delayMs / 2))
  return Math.max(1, delayMs + passiveScheduleOffsetWithinWindowMs(windowMs, random))
}

function passiveScheduleOffsetWithinWindowMs(windowMs: number, random: () => number): number {
  if (windowMs <= 0) return 0
  const sampled = Number(random())
  const unit = Number.isFinite(sampled) ? Math.min(1, Math.max(0, sampled)) : 0
  const offset = Math.min(windowMs, Math.floor(unit * (windowMs * 2 + 1)) - windowMs)
  return offset === 0 ? 1 : offset
}

/** Adds a fresh offset while keeping a strictly positive delay. */
export function passiveScheduleDelayMs(intervalMs: number, random = Math.random): number {
  return Math.max(1, Math.trunc(Number(intervalMs) || 1) + passiveScheduleOffsetMs(intervalMs, random))
}

/**
 * Returns a fresh delay at or after a hard external deadline. This is used
 * for passive probes that must not run before an upstream cooldown expires.
 */
export function passiveScheduleNotBeforeDelayMs(intervalMs: number, random = Math.random): number {
  const interval = Math.max(1, Math.trunc(Number(intervalMs) || 1))
  const offset = passiveScheduleOffsetMs(interval, random)
  return interval + (offset === 0 ? 1 : Math.abs(offset))
}
