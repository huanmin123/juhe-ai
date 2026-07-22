import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, installProcessLogHandlers, logger } from '../../shared/logger.js'
import { startProcessEventLoopMonitor } from '../../shared/process-event-loop-monitor.js'
import {
  finishBackgroundTaskRun,
  finishBackgroundTaskRunAsync,
  getBackgroundTaskRun,
  getBackgroundTaskRunAsync,
  heartbeatBackgroundTaskRun,
  heartbeatBackgroundTaskRunAsync,
  tryStartBackgroundTaskRun,
  tryStartBackgroundTaskRunAsync
} from '../../storage/repositories.js'
import { isRecordMaintenanceJob, runRecordMaintenanceJobOnce, type RecordMaintenanceJob } from './record-maintenance-queue.service.js'

const temporaryMaintenanceLeaseMs = 5 * 60 * 1000
const temporaryMaintenanceHeartbeatMs = 30 * 1000
const temporaryMaintenanceEventLoopWarmupMs = 20
const temporaryMaintenanceLeaseUnavailableExitCode = 75

export async function runTemporaryMaintenanceWorker(runId: string): Promise<number> {
  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'temporary-maintenance-worker'
  installProcessLogHandlers()
  startProcessEventLoopMonitor()

  const ownerId = `temporary-maintenance-worker:${process.pid}:${Date.now()}`
  const started = await tryStartTemporaryBackgroundTaskRun({
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
    return temporaryMaintenanceLeaseUnavailableExitCode
  }
  await warmupTemporaryMaintenanceEventLoopMonitor()

  let heartbeatFailure: unknown
  let heartbeatInFlight = Promise.resolve()
  const heartbeatTimer = setInterval(() => {
    heartbeatInFlight = heartbeatInFlight.then(async () => {
      const renewed = await heartbeatTemporaryBackgroundTaskRun(runId, ownerId, leaseUntilIso())
      if (!renewed) throw new Error('临时维护 worker 已失去任务租约')
    }).catch((error) => {
      heartbeatFailure ??= error
      logger.error(errorLogFields(error, {
        event: 'temporary_maintenance_worker_heartbeat_failed',
        runId,
        ownerId
      }), '临时维护 worker 心跳续租失败')
    })
  }, temporaryMaintenanceHeartbeatMs)
  heartbeatTimer.unref()

  try {
    const run = await getTemporaryBackgroundTaskRun(runId)
    const job = run?.params.job
    if (!isRecordMaintenanceJob(job) || !isTemporaryRecordMaintenanceJob(job)) {
      throw new Error('临时维护任务参数无效或不允许由临时 worker 执行')
    }
    const result = await runRecordMaintenanceJobOnce(job)
    await heartbeatInFlight
    if (heartbeatFailure) throw heartbeatFailure
    const completed = await finishTemporaryBackgroundTaskRun({
      runId,
      ownerId,
      status: 'completed',
      result: result as Record<string, unknown>,
      exitCode: 0
    })
    if (!completed) throw new Error('临时维护 worker 完成时已失去任务运行权')
    logger.info({
      event: 'temporary_maintenance_worker_completed',
      runId,
      ownerId,
      jobType: job.type,
      result
    }, '临时维护 worker 执行完成')
    return 0
  } catch (error) {
    await finishTemporaryBackgroundTaskRun({
      runId,
      ownerId,
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
    await heartbeatInFlight
  }
}

async function tryStartTemporaryBackgroundTaskRun(input: Parameters<typeof tryStartBackgroundTaskRun>[0]): Promise<boolean> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? await tryStartBackgroundTaskRunAsync(input)
    : tryStartBackgroundTaskRun(input)
}

async function heartbeatTemporaryBackgroundTaskRun(runId: string, ownerId: string, leaseUntil: string): Promise<boolean> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? await heartbeatBackgroundTaskRunAsync(runId, ownerId, leaseUntil)
    : heartbeatBackgroundTaskRun(runId, ownerId, leaseUntil)
}

async function finishTemporaryBackgroundTaskRun(input: Parameters<typeof finishBackgroundTaskRun>[0]): Promise<boolean> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? await finishBackgroundTaskRunAsync(input)
    : finishBackgroundTaskRun(input)
}

async function getTemporaryBackgroundTaskRun(runId: string): Promise<ReturnType<typeof getBackgroundTaskRun>> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? await getBackgroundTaskRunAsync(runId)
    : getBackgroundTaskRun(runId)
}

function isTemporaryRecordMaintenanceJob(job: RecordMaintenanceJob): job is Extract<RecordMaintenanceJob, { type: 'usage_records_cleanup' | 'non_business_data_cleanup' | 'audit_retained_data_cleanup' }> {
  return job.type === 'usage_records_cleanup' || job.type === 'non_business_data_cleanup' || job.type === 'audit_retained_data_cleanup'
}

function leaseUntilIso(): string {
  return new Date(Date.now() + temporaryMaintenanceLeaseMs).toISOString()
}

function warmupTemporaryMaintenanceEventLoopMonitor(): Promise<void> {
  return new Promise((resolvePromise) => {
    setTimeout(resolvePromise, temporaryMaintenanceEventLoopWarmupMs)
  })
}
