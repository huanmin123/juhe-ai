import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, installProcessLogHandlers, logger } from '../../shared/logger.js'
import { getBusinessDatabase, getDatasetDatabase, getStatsDatabase } from '../../storage/database.js'
import {
  finishBackgroundTaskRun,
  getBackgroundTaskRun,
  heartbeatBackgroundTaskRun,
  tryStartBackgroundTaskRun
} from '../../storage/repositories.js'
import { isRecordMaintenanceJob, runRecordMaintenanceJobOnce, type RecordMaintenanceJob } from './record-maintenance-queue.service.js'

const temporaryMaintenanceLeaseMs = 5 * 60 * 1000
const temporaryMaintenanceHeartbeatMs = 30 * 1000

export async function runTemporaryMaintenanceWorker(runId: string): Promise<number> {
  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'temporary-maintenance-worker'
  installProcessLogHandlers()
  getBusinessDatabase()
  getDatasetDatabase()
  getStatsDatabase()

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

  const heartbeatTimer = setInterval(() => {
    heartbeatBackgroundTaskRun(runId, ownerId, leaseUntilIso())
  }, temporaryMaintenanceHeartbeatMs)
  heartbeatTimer.unref()

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
    clearInterval(heartbeatTimer)
  }
}

function isTemporaryRecordMaintenanceJob(job: RecordMaintenanceJob): job is Extract<RecordMaintenanceJob, { type: 'usage_records_cleanup' | 'non_business_data_cleanup' }> {
  return job.type === 'usage_records_cleanup' || job.type === 'non_business_data_cleanup'
}

function leaseUntilIso(): string {
  return new Date(Date.now() + temporaryMaintenanceLeaseMs).toISOString()
}
