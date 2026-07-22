import { errorLogFields, logger } from '../../../shared/logger.js'

const pendingGatewayFailureUsageFinalizations = new Set<Promise<void>>()
const queuedGatewayUsageFinalizations: Array<{
  taskFactory: () => Promise<void>
  bytes: number
}> = []
const gatewayUsageFinalizationMaxItems = 2048
const gatewayUsageFinalizationMaxBytes = 64 * 1024 * 1024
const gatewayUsageFinalizationMaxConcurrency = 32
let queuedGatewayUsageFinalizationBytes = 0
let activeGatewayUsageFinalizations = 0
let droppedGatewayUsageFinalizations = 0

export function dispatchGatewayUsageFinalization(input: {
  taskFactory: () => Promise<void>
  bytes?: number
}): boolean {
  const bytes = Math.max(0, Math.trunc(input.bytes ?? 0))
  if (
    bytes > gatewayUsageFinalizationMaxBytes
    || queuedGatewayUsageFinalizations.length >= gatewayUsageFinalizationMaxItems
    || queuedGatewayUsageFinalizationBytes + bytes > gatewayUsageFinalizationMaxBytes
  ) {
    droppedGatewayUsageFinalizations += 1
    logger.warn({
      event: 'gateway_usage_finalization_dropped',
      reason: bytes > gatewayUsageFinalizationMaxBytes ? 'oversize' : 'overflow',
      droppedCount: droppedGatewayUsageFinalizations,
      queuedCount: queuedGatewayUsageFinalizations.length,
      queuedBytes: queuedGatewayUsageFinalizationBytes
    }, '网关使用记录异步收尾达到容量上限，已丢弃本条投递')
    return false
  }
  queuedGatewayUsageFinalizations.push({ taskFactory: input.taskFactory, bytes })
  queuedGatewayUsageFinalizationBytes += bytes
  pumpGatewayUsageFinalizations()
  return true
}

export function trackGatewayUsageFinalization(
  task: Promise<void>,
  onError: (error: unknown) => void = (error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_usage_finalization_failed'
    }), '网关使用记录异步收尾失败')
  }
): void {
  pendingGatewayFailureUsageFinalizations.add(task)
  void task.then(
    () => pendingGatewayFailureUsageFinalizations.delete(task),
    (error) => {
      pendingGatewayFailureUsageFinalizations.delete(task)
      onError?.(error)
    }
  )
}

export function trackGatewayFailureUsageFinalization(task: Promise<void>): void {
  trackGatewayUsageFinalization(task)
}

export function getPendingGatewayFailureUsageFinalizationCount(): number {
  return pendingGatewayFailureUsageFinalizations.size + queuedGatewayUsageFinalizations.length
}

export interface GatewayUsageFinalizationRuntime {
  pendingCount: number
  queuedCount: number
  queuedBytes: number
  activeCount: number
  droppedCount: number
  maxItems: number
  maxBytes: number
  maxConcurrency: number
}

export function getGatewayUsageFinalizationRuntime(): GatewayUsageFinalizationRuntime {
  return {
    pendingCount: getPendingGatewayFailureUsageFinalizationCount(),
    queuedCount: queuedGatewayUsageFinalizations.length,
    queuedBytes: queuedGatewayUsageFinalizationBytes,
    activeCount: activeGatewayUsageFinalizations,
    droppedCount: droppedGatewayUsageFinalizations,
    maxItems: gatewayUsageFinalizationMaxItems,
    maxBytes: gatewayUsageFinalizationMaxBytes,
    maxConcurrency: gatewayUsageFinalizationMaxConcurrency
  }
}

export async function waitForGatewayFailureUsageFinalizationsIdle(timeoutMs = 10_000): Promise<boolean> {
  const deadline = Date.now() + Math.max(1, timeoutMs)
  while (pendingGatewayFailureUsageFinalizations.size > 0 || queuedGatewayUsageFinalizations.length > 0) {
    if (Date.now() >= deadline) return false
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
  await new Promise<void>((resolvePromise) => setImmediate(resolvePromise))
  return true
}

function pumpGatewayUsageFinalizations(): void {
  while (
    activeGatewayUsageFinalizations < gatewayUsageFinalizationMaxConcurrency
    && queuedGatewayUsageFinalizations.length > 0
  ) {
    const queued = queuedGatewayUsageFinalizations.shift()
    if (!queued) break
    queuedGatewayUsageFinalizationBytes = Math.max(0, queuedGatewayUsageFinalizationBytes - queued.bytes)
    activeGatewayUsageFinalizations += 1
    const task = Promise.resolve()
      .then(queued.taskFactory)
      .finally(() => {
        activeGatewayUsageFinalizations = Math.max(0, activeGatewayUsageFinalizations - 1)
        pumpGatewayUsageFinalizations()
      })
    trackGatewayUsageFinalization(task)
  }
}
