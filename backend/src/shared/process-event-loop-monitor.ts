import { monitorEventLoopDelay } from 'node:perf_hooks'

import { runtimeConfig, type ProcessRole } from '../config/runtime.js'

export type ProcessEventLoopRole =
  | ProcessRole
  | 'metrics-worker'
  | 'ingest-worker'
  | 'stats-worker'
  | 'snapshot-worker'
  | 'probe-worker'
  | 'maintenance-worker'
  | 'temporary-maintenance-worker'

export interface ProcessEventLoopSample {
  processRole: ProcessEventLoopRole
  processPid: number
  sampledAt: string
  eventLoopLagMs?: number
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
  return {
    processRole,
    processPid: process.pid,
    sampledAt: new Date().toISOString(),
    eventLoopLagMs: currentProcessEventLoopLagMs()
  }
}

function currentProcessEventLoopRole(): ProcessEventLoopRole {
  if (runtimeConfig.processRole !== 'worker') {
    return runtimeConfig.processRole
  }
  if (
    runtimeConfig.workerRole === 'metrics-worker'
    || runtimeConfig.workerRole === 'ingest-worker'
    || runtimeConfig.workerRole === 'stats-worker'
    || runtimeConfig.workerRole === 'snapshot-worker'
    || runtimeConfig.workerRole === 'probe-worker'
    || runtimeConfig.workerRole === 'maintenance-worker'
    || runtimeConfig.workerRole === 'temporary-maintenance-worker'
  ) {
    return runtimeConfig.workerRole
  }
  return 'worker'
}

function roundMetricMs(value: number): number | undefined {
  if (!Number.isFinite(value)) return undefined
  return Math.round(Math.max(0, value) * 100) / 100
}
