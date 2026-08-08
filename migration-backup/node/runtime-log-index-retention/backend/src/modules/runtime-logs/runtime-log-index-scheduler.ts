import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { ensureRuntimeLogFacetSnapshots } from '../../storage/runtime-log-index.repository.js'
import { backgroundScheduledJobName } from '../background/background-job-registry.js'
import { WorkerScheduler } from '../background/worker-scheduler.js'

const minuteMs = 60_000

// This is the complete Node-side scheduler boundary for F1. Its registration
// can be removed as one unit when the Go owner takes over the feature.
export function scheduleRuntimeLogIndexMaintenance(scheduler: WorkerScheduler): void {
  if (!runtimeConfig.log.indexEnabled) return
  scheduler.schedule({
    name: backgroundScheduledJobName('runtime-log-index-maintenance'),
    intervalMs: 60 * minuteMs,
    initialDelayMs: 9 * minuteMs,
    stablePhaseWindowMs: minuteMs,
    scheduleMode: 'fixedDelay',
    resourceLane: 'storage-maintenance',
    timeoutMs: 10 * minuteMs,
    failureBackoff: { baseMs: minuteMs, maxMs: 30 * minuteMs },
    task: runRuntimeLogIndexMaintenance
  })
}

async function runRuntimeLogIndexMaintenance(): Promise<void> {
  try {
    if (!runtimeConfig.log.indexEnabled || runtimeConfig.log.indexOwner !== 'node') return
    if (runtimeConfig.databaseDriver !== 'postgres') ensureRuntimeLogFacetSnapshots()
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_runtime_log_index_maintenance_failed' }), '运行日志索引维护失败')
    throw error
  }
}
