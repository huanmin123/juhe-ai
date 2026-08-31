export const modelCheckProbeMaxAttempts = 3

export function isTerminalModelCheckProbeFailure(result: {
  statusCode: number
  attemptCount?: number
  retryMaxAttempts?: number
}): boolean {
  if (result.statusCode === 200) return false
  const maxAttempts = result.retryMaxAttempts ?? modelCheckProbeMaxAttempts
  return (result.attemptCount ?? 1) >= maxAttempts
}
