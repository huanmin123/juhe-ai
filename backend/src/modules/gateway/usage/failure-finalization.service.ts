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
let admissionWaitCount = 0
const capacityWaiters = new Set<() => void>()

export async function dispatchGatewayUsageFinalization(input: {
  taskFactory: () => Promise<void>
  bytes?: number
}): Promise<void> {
  const bytes = Math.max(0, Math.trunc(input.bytes ?? 0))
  if (bytes > gatewayUsageFinalizationMaxBytes) {
    throw new Error('网关使用记录异步收尾任务超过单条容量上限')
  }
  while (!hasGatewayUsageFinalizationCapacity(bytes)) {
    admissionWaitCount += 1
    await waitForGatewayUsageFinalizationCapacity()
  }
  queuedGatewayUsageFinalizations.push({ taskFactory: input.taskFactory, bytes })
  queuedGatewayUsageFinalizationBytes += bytes
  pumpGatewayUsageFinalizations()
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
  return pendingGatewayFailureUsageFinalizations.size + queuedGatewayUsageFinalizations.length + capacityWaiters.size
}

export interface GatewayUsageFinalizationRuntime {
  pendingCount: number
  queuedCount: number
  queuedBytes: number
  activeCount: number
  droppedCount: number
  admissionWaitCount: number
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
    droppedCount: 0,
    admissionWaitCount,
    maxItems: gatewayUsageFinalizationMaxItems,
    maxBytes: gatewayUsageFinalizationMaxBytes,
    maxConcurrency: gatewayUsageFinalizationMaxConcurrency
  }
}

export async function waitForGatewayFailureUsageFinalizationsIdle(timeoutMs = 10_000): Promise<boolean> {
  const deadline = Date.now() + Math.max(1, timeoutMs)
  while (true) {
    if (getPendingGatewayFailureUsageFinalizationCount() === 0) {
      await new Promise<void>((resolvePromise) => setImmediate(resolvePromise))
      if (getPendingGatewayFailureUsageFinalizationCount() === 0) return true
    }
    if (Date.now() >= deadline) return false
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
}

function pumpGatewayUsageFinalizations(): void {
  while (
    activeGatewayUsageFinalizations < gatewayUsageFinalizationMaxConcurrency
    && queuedGatewayUsageFinalizations.length > 0
  ) {
    const queued = queuedGatewayUsageFinalizations.shift()
    if (!queued) break
    queuedGatewayUsageFinalizationBytes = Math.max(0, queuedGatewayUsageFinalizationBytes - queued.bytes)
    notifyGatewayUsageFinalizationCapacity()
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

function hasGatewayUsageFinalizationCapacity(bytes: number): boolean {
  return queuedGatewayUsageFinalizations.length < gatewayUsageFinalizationMaxItems
    && queuedGatewayUsageFinalizationBytes + bytes <= gatewayUsageFinalizationMaxBytes
}

function waitForGatewayUsageFinalizationCapacity(): Promise<void> {
  return new Promise((resolvePromise) => {
    capacityWaiters.add(resolvePromise)
  })
}

function notifyGatewayUsageFinalizationCapacity(): void {
  for (const resolvePromise of capacityWaiters) {
    resolvePromise()
  }
  capacityWaiters.clear()
}
