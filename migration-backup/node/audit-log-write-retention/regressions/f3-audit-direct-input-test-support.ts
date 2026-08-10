/**
 * F3 test-only compatibility seam for gateway regressions that previously
 * cleared or flushed the retired Node audit queue as test isolation.
 *
 * This is deliberately not a queue, a writer, a retry mechanism, or a
 * fallback. F3 capture is now one-shot Node -> Go direct input; tests that
 * need to prove Go persistence must start the real Go input owner instead of
 * using these no-op cleanup hooks.
 */
export function setDbServiceAuditLogLocalWriteAllowedForTest(_allowed: boolean): void {
  // F3 has no Node-local audit writer to enable.
}

export function flushAllAuditLogQueue(): void {
  // F3 direct input has no Node queue to drain.
}

export async function flushAllAuditLogQueueAsync(): Promise<void> {
  // Keep only gateway-test teardown sequencing; do not emulate delivery.
}

export function clearAuditLogQueueForTest(): void {
  // F3 direct input retains no Node-local test state.
}
