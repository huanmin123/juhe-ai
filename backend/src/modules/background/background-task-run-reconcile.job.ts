import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import {
  reconcileStaleBackgroundTaskRuns,
  reconcileStaleBackgroundTaskRunsAsync
} from '../../storage/background-task-runs.repository.js'

export const backgroundTaskRunReconcileInitialDelayMs = 2_000
export const backgroundTaskRunReconcileIntervalMs = 5 * 60_000

const backgroundTaskRunStaleAfterMs = 10 * 60_000
const backgroundTaskRunReconcileBatchSize = 500

export async function runBackgroundTaskRunReconcile(): Promise<void> {
  const nowMs = Date.now()
  const now = new Date(nowMs).toISOString()
  const staleBefore = new Date(nowMs - backgroundTaskRunStaleAfterMs).toISOString()
  const input = {
    queuedBefore: staleBefore,
    runningHeartbeatBefore: staleBefore,
    now,
    limit: backgroundTaskRunReconcileBatchSize
  }
  const result = runtimeConfig.databaseDriver === 'postgres'
    ? await reconcileStaleBackgroundTaskRunsAsync(input)
    : reconcileStaleBackgroundTaskRuns(input)
  const reconciledCount = result.failedQueuedCount + result.failedRunningCount
  const fields = {
    event: 'background_task_run_reconcile_completed',
    ...result,
    reconciledCount,
    staleBefore
  }
  if (reconciledCount > 0 || result.deletedExpiredLeaseCount > 0) {
    logger.warn(fields, '已回收无有效租约的陈旧后台临时任务状态')
  } else {
    logger.debug(fields, '后台临时任务状态无需回收')
  }
}
