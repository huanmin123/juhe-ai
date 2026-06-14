import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, installProcessLogHandlers, logger } from '../../shared/logger.js'
import { buildProcessEventLoopSample, startProcessEventLoopMonitor } from '../../shared/process-event-loop-monitor.js'
import { getBusinessDatabase, getDatasetDatabase, getStatsDatabase } from '../../storage/database.js'
import {
  finishBackgroundTaskRun,
  getBackgroundTaskRun,
  heartbeatBackgroundTaskRun,
  tryStartBackgroundTaskRun
} from '../../storage/repositories.js'
import { insertProcessEventLoopSample } from '../../storage/system-metrics.repository.js'
import { isRecordMaintenanceJob, runRecordMaintenanceJobOnce, type RecordMaintenanceJob } from './record-maintenance-queue.service.js'

const temporaryMaintenanceLeaseMs = 5 * 60 * 1000
const temporaryMaintenanceHeartbeatMs = 30 * 1000
const temporaryMaintenanceEventLoopSampleMs = 10 * 1000
const temporaryMaintenanceEventLoopWarmupMs = 20

export async function runTemporaryMaintenanceWorker(runId: string): Promise<number> {
  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'temporary-maintenance-worker'
  installProcessLogHandlers()
  getBusinessDatabase()
  getDatasetDatabase()
  getStatsDatabase()
  startProcessEventLoopMonitor()

  const ownerId = `temporary-maintenance-worker:${process.pid}:${Date.now()}`
  const started = tryStartBackgroundTaskRun({
    runId,
    ownerId,
    leaseUntil: leaseUntilIso()
  })
  if (!started) {
    logger.warn({
      event: 'temporary_maintenance_worker_start_skipped',
      runId,
      ownerId
    }, '临时维护 worker 未获得任务运行权，已退出')
    finishBackgroundTaskRun({
      runId,
      status: 'skipped',
      result: { skippedReason: 'lease_or_status_unavailable' },
      exitCode: 0
    })
    return 0
  }
  await warmupTemporaryMaintenanceEventLoopMonitor()

  const heartbeatTimer = setInterval(() => {
    heartbeatBackgroundTaskRun(runId, ownerId, leaseUntilIso())
  }, temporaryMaintenanceHeartbeatMs)
  heartbeatTimer.unref()
  const eventLoopSampleTimer = setInterval(() => {
    recordTemporaryMaintenanceEventLoopSample(runId)
  }, temporaryMaintenanceEventLoopSampleMs)
  eventLoopSampleTimer.unref()

  try {
    const run = getBackgroundTaskRun(runId)
    const job = run?.params.job
    if (!isRecordMaintenanceJob(job) || !isTemporaryRecordMaintenanceJob(job)) {
      throw new Error('临时维护任务参数无效或不允许由临时 worker 执行')
    }
    const result = await runRecordMaintenanceJobOnce(job)
    finishBackgroundTaskRun({
      runId,
      status: 'completed',
      result: result as Record<string, unknown>,
      exitCode: 0
    })
    logger.info({
      event: 'temporary_maintenance_worker_completed',
      runId,
      ownerId,
      jobType: job.type,
      result
    }, '临时维护 worker 执行完成')
    return 0
  } catch (error) {
    finishBackgroundTaskRun({
      runId,
      status: 'failed',
      result: {},
      errorMessage: error instanceof Error ? error.message : String(error),
      exitCode: 1
    })
    logger.error(errorLogFields(error, {
      event: 'temporary_maintenance_worker_failed',
      runId,
      ownerId
    }), '临时维护 worker 执行失败')
    return 1
  } finally {
    recordTemporaryMaintenanceEventLoopSample(runId)
    clearInterval(heartbeatTimer)
    clearInterval(eventLoopSampleTimer)
  }
}

function isTemporaryRecordMaintenanceJob(job: RecordMaintenanceJob): job is Extract<RecordMaintenanceJob, { type: 'usage_records_cleanup' | 'non_business_data_cleanup' }> {
  return job.type === 'usage_records_cleanup' || job.type === 'non_business_data_cleanup'
}

function leaseUntilIso(): string {
  return new Date(Date.now() + temporaryMaintenanceLeaseMs).toISOString()
}

function warmupTemporaryMaintenanceEventLoopMonitor(): Promise<void> {
  return new Promise((resolvePromise) => {
    setTimeout(resolvePromise, temporaryMaintenanceEventLoopWarmupMs)
  })
}

function recordTemporaryMaintenanceEventLoopSample(runId: string): void {
  try {
    insertProcessEventLoopSample(buildProcessEventLoopSample('temporary-maintenance-worker'))
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'temporary_maintenance_worker_event_loop_sample_failed',
      runId
    }), '临时维护 worker 事件循环采样写入失败')
  }
}
