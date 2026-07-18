export const gatewayRequestAbsoluteDeadlineCapMs = 270_000

export function gatewayRequestAbsoluteDeadlineAtMs(
  startedAtMs: number,
  configuredTotalWaitSeconds: number
): number {
  const normalizedStartedAtMs = finiteInteger(startedAtMs, Date.now())
  const configuredWaitSeconds = Math.max(10, finiteInteger(configuredTotalWaitSeconds, 10))
  const configuredWaitMs = configuredWaitSeconds * 1000
  return normalizedStartedAtMs + Math.min(configuredWaitMs, gatewayRequestAbsoluteDeadlineCapMs)
}

export function gatewayRequestDeadlineRemainingMs(deadlineAtMs: number, nowMs = Date.now()): number {
  const normalizedNowMs = finiteInteger(nowMs, Date.now())
  const normalizedDeadlineAtMs = finiteInteger(deadlineAtMs, normalizedNowMs)
  return Math.max(0, normalizedDeadlineAtMs - normalizedNowMs)
}

export function gatewayRequestDeadlineExpired(deadlineAtMs: number, nowMs = Date.now()): boolean {
  return gatewayRequestDeadlineRemainingMs(deadlineAtMs, nowMs) === 0
}

export function gatewayRequestWaitWithinDeadlineMs(
  requestedWaitMs: number,
  deadlineAtMs: number,
  nowMs = Date.now()
): number {
  const normalizedWaitMs = Math.max(0, finiteInteger(requestedWaitMs, 0))
  return Math.min(normalizedWaitMs, gatewayRequestDeadlineRemainingMs(deadlineAtMs, nowMs))
}

function finiteInteger(value: number, fallback: number): number {
  return Number.isFinite(value) ? Math.trunc(value) : Math.trunc(fallback)
}
