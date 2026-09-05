import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

const {
  cleanupDataRetentionBatchesForTest,
  runDataRetentionCleanupStagesForTest
} = await import('../../modules/background/data-retention-cleanup.service.js')

await verifyPreAbortedSignalRejectsBeforeFirstStage()
await verifyRunningStageCompletesWithoutStartingNextStage()
await verifyRunningBatchCompletesAfterAbort()
await verifyAbortBetweenBatchesDoesNotStartNextBatch()
await verifyBatchCleanupStillStopsOnShortBatch()

console.log('数据保留 AbortSignal 回归通过：取消后不准入新阶段/新批次，已开始工作按既定语义完成')

async function verifyPreAbortedSignalRejectsBeforeFirstStage(): Promise<void> {
  const controller = new AbortController()
  const abortReason = new Error('pre-aborted-retention-stage')
  let stageStarts = 0
  controller.abort(abortReason)

  await assertRejectsWithReason(
    runDataRetentionCleanupStagesForTest([
      () => {
        stageStarts += 1
      }
    ], controller.signal),
    abortReason
  )
  assert.equal(stageStarts, 0, '预先取消时不得启动第一个清理阶段')
}

async function verifyRunningStageCompletesWithoutStartingNextStage(): Promise<void> {
  const controller = new AbortController()
  const abortReason = new Error('abort-running-retention-stage')
  const stageGate = deferred<void>()
  const events: string[] = []

  const cleanup = runDataRetentionCleanupStagesForTest([
    async () => {
      events.push('stage-1-started')
      await stageGate.promise
      events.push('stage-1-completed')
    },
    () => {
      events.push('stage-2-started')
    }
  ], controller.signal)

  assert.deepEqual(events, ['stage-1-started'], '第一阶段应先开始并等待已准入的工作')
  controller.abort(abortReason)
  stageGate.resolve()

  await assertRejectsWithReason(cleanup, abortReason)
  assert.deepEqual(
    events,
    ['stage-1-started', 'stage-1-completed'],
    '运行中的阶段收到取消后应完成已开始工作，但不得再启动下一阶段'
  )
}

async function verifyRunningBatchCompletesAfterAbort(): Promise<void> {
  const controller = new AbortController()
  const abortReason = new Error('abort-running-retention-batch')
  const batchGate = deferred<void>()
  const events: string[] = []
  let batchStarts = 0

  const cleanup = cleanupDataRetentionBatchesForTest(async () => {
    batchStarts += 1
    events.push(`batch-${batchStarts}-started`)
    await batchGate.promise
    events.push(`batch-${batchStarts}-completed`)
    return 2
  }, 2, 3, controller.signal)

  assert.deepEqual(events, ['batch-1-started'], '第一批应先开始并等待已准入的工作')
  controller.abort(abortReason)
  batchGate.resolve()

  await assertRejectsWithReason(cleanup, abortReason)
  assert.equal(batchStarts, 1, '运行中的第一批收到取消后，不得再启动第二批')
  assert.deepEqual(
    events,
    ['batch-1-started', 'batch-1-completed'],
    '已准入的当前批次应完整结束后再响应取消'
  )
}

async function verifyAbortBetweenBatchesDoesNotStartNextBatch(): Promise<void> {
  const controller = new AbortController()
  const abortReason = new Error('abort-between-retention-batches')
  let batchStarts = 0

  const cleanup = cleanupDataRetentionBatchesForTest(async () => {
    batchStarts += 1
    queueMicrotask(() => controller.abort(abortReason))
    return 2
  }, 2, 3, controller.signal)

  await assertRejectsWithReason(cleanup, abortReason)
  assert.equal(batchStarts, 1, '第一批完成后收到取消，不得再启动第二批')
}

async function verifyBatchCleanupStillStopsOnShortBatch(): Promise<void> {
  const controller = new AbortController()
  const deletedRows = [2, 1]
  let batchStarts = 0

  const deleted = await cleanupDataRetentionBatchesForTest(() => {
    const batchDeleted = deletedRows[batchStarts] ?? 0
    batchStarts += 1
    return batchDeleted
  }, 2, 5, controller.signal)

  assert.equal(deleted, 3, '正常执行时应累加已完成批次的删除数')
  assert.equal(batchStarts, 2, '短批次仍应结束当轮清理')
}

async function assertRejectsWithReason(promise: Promise<unknown>, reason: unknown): Promise<void> {
  try {
    await promise
  } catch (error) {
    assert.equal(error, reason, '清理应保留 AbortSignal.reason，便于 scheduler 识别取消原因')
    return
  }
  assert.fail('预期 AbortSignal 使清理拒绝')
}

function deferred<T>(): {
  promise: Promise<T>
  resolve: (value: T | PromiseLike<T>) => void
} {
  let resolvePromise!: (value: T | PromiseLike<T>) => void
  return {
    promise: new Promise<T>((resolve) => {
      resolvePromise = resolve
    }),
    resolve: resolvePromise
  }
}
