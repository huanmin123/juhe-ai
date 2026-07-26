import { monitorEventLoopDelay } from 'node:perf_hooks'

import { runtimeConfig, type ProcessRole, type WorkerRuntimeRole } from '../config/runtime.js'

export type ProcessEventLoopRole =
  | ProcessRole
  | WorkerRuntimeRole
  | `gateway:${string}`
  | `control:${string}`
  | `db-service:${string}`
  | `${Exclude<WorkerRuntimeRole, 'worker'>}:${number}`

export interface ProcessEventLoopSample {
  processRole: ProcessEventLoopRole
  processPid: number
  sampledAt: string
  eventLoopLagMs?: number
  processRssBytes?: number
  processHeapUsedBytes?: number
  processHeapTotalBytes?: number
  processExternalBytes?: number
  processArrayBuffersBytes?: number
}

const eventLoopDelayHistogram = monitorEventLoopDelay({ resolution: 10 })
let enabled = false

export function startProcessEventLoopMonitor(): void {
  if (enabled) return
  eventLoopDelayHistogram.enable()
  enabled = true
}

export function currentProcessEventLoopLagMs(): number | undefined {
  startProcessEventLoopMonitor()
  const minNs = eventLoopDelayHistogram.min
  const maxNs = eventLoopDelayHistogram.max
  eventLoopDelayHistogram.reset()

  if (!Number.isFinite(minNs) || !Number.isFinite(maxNs) || maxNs <= 0 || minNs > maxNs) {
    return undefined
  }

  return roundMetricMs((maxNs - minNs) / 1_000_000)
}

export function buildProcessEventLoopSample(processRole: ProcessEventLoopRole = currentProcessEventLoopRole()): ProcessEventLoopSample {
  const memoryUsage = process.memoryUsage()
  return {
    processRole,
    processPid: process.pid,
    sampledAt: new Date().toISOString(),
    eventLoopLagMs: currentProcessEventLoopLagMs(),
    processRssBytes: memoryUsage.rss,
    processHeapUsedBytes: memoryUsage.heapUsed,
    processHeapTotalBytes: memoryUsage.heapTotal,
    processExternalBytes: memoryUsage.external,
    processArrayBuffersBytes: memoryUsage.arrayBuffers
  }
}

export function currentProcessEventLoopRole(): ProcessEventLoopRole {
  if (runtimeConfig.runtimeMode === 'performance') {
    if (runtimeConfig.processRole === 'server') {
      return `${runtimeConfig.performanceNodeRole === 'gateway' ? 'gateway' : 'control'}:${runtimeConfig.instanceId}`
    }
    if (runtimeConfig.processRole === 'db-service') {
      return `db-service:${runtimeConfig.instanceId}`
    }
    if (runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole !== 'worker') {
      return `${runtimeConfig.workerRole}:${runtimeConfig.workerReplicaIndex + 1}`
    }
  }
  if (runtimeConfig.processRole !== 'worker') {
    return runtimeConfig.processRole
  }
  if (
    runtimeConfig.workerRole === 'ingest-worker'
    || runtimeConfig.workerRole === 'stats-worker'
    || runtimeConfig.workerRole === 'ops-worker'
  ) {
    return runtimeConfig.workerRole
  }
  return 'worker'
}

export function processEventLoopRoleFromUnknown(value: unknown): ProcessEventLoopRole | undefined {
  if (typeof value !== 'string' || value.length === 0 || value.length > 96) return undefined
  if (
    value === 'server'
    || value === 'ingest-worker'
    || value === 'stats-worker'
    || value === 'ops-worker'
    || value === 'db-service'
  ) {
    return value
  }
  if (/^(?:gateway|control|db-service):[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(value)) {
    return value as ProcessEventLoopRole
  }
  if (/^(?:ingest-worker|usage-worker|log-worker|stats-worker|ops-worker):(?:[1-9]|[1-5][0-9]|6[0-4])$/.test(value)) {
    return value as ProcessEventLoopRole
  }
  return undefined
}

function roundMetricMs(value: number): number | undefined {
  if (!Number.isFinite(value)) return undefined
  return Math.round(Math.max(0, value) * 100) / 100
}
