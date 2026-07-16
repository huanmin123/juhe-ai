const pendingGatewayFailureUsageFinalizations = new Set<Promise<void>>()

export function trackGatewayFailureUsageFinalization(task: Promise<void>): void {
  pendingGatewayFailureUsageFinalizations.add(task)
  void task.then(
    () => pendingGatewayFailureUsageFinalizations.delete(task),
    () => pendingGatewayFailureUsageFinalizations.delete(task)
  )
}

export function getPendingGatewayFailureUsageFinalizationCount(): number {
  return pendingGatewayFailureUsageFinalizations.size
}

export async function waitForGatewayFailureUsageFinalizationsIdle(timeoutMs = 10_000): Promise<boolean> {
  const deadline = Date.now() + Math.max(1, timeoutMs)
  while (pendingGatewayFailureUsageFinalizations.size > 0) {
    if (Date.now() >= deadline) return false
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
  await new Promise<void>((resolvePromise) => setImmediate(resolvePromise))
  return true
}
