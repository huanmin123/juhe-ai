export function withoutChatLongSessionAcceptanceObservability<
  T extends { auditLogCount: number; upstreamAttemptCount: number }
>(snapshot: T): Omit<T, 'auditLogCount' | 'upstreamAttemptCount'> {
  const {
    auditLogCount: _auditLogCount,
    upstreamAttemptCount: _upstreamAttemptCount,
    ...businessSnapshot
  } = snapshot
  return businessSnapshot
}
