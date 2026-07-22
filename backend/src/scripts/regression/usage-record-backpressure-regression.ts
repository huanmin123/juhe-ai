import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

runtimeConfig.processRole = 'server'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

class FakeIngestWorkerProcess extends EventEmitter {
  connected = true
  killed = false
  pid = 43001
  sentCount = 0

  send(_message: unknown, callback?: (error?: Error | null) => void): boolean {
    this.sentCount += 1
    setImmediate(() => callback?.(null))
    return true
  }

  kill(): boolean {
    this.killed = true
    this.connected = false
    return true
  }

  ready(): void {
    this.emit('message', { type: 'background_worker_ready', pid: this.pid })
  }
}

const backgroundIpc = await import('../../modules/background/background-ipc.js')
await verifyUsageIpcAdmissionIsBoundedAndRecoverable()

runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
const failureFinalization = await import('../../modules/gateway/usage/failure-finalization.service.js')
const usageRecordQueue = await import('../../modules/gateway/usage/record-queue.service.js')

await verifyFailureFinalizationAdmissionIsBoundedAndFifo()
await verifyLocalUsageAdmissionIsBoundedAndLossless()

console.log('使用记录背压回归通过：IPC、收尾和本地队列 admission 均有界，容量恢复后继续投递，等待区耗尽后明确拒绝')

async function verifyUsageIpcAdmissionIsBoundedAndRecoverable(): Promise<void> {
  for (let index = 0; index < 10_000; index += 1) {
    assert.equal(backgroundIpc.sendUsageRecordsToWorker([buildUsageRecord(index)]), true)
  }
  const waiters = Array.from({ length: 2048 }, (_, index) => (
    backgroundIpc.sendUsageRecordsToWorkerAsync([buildUsageRecord(10_000 + index)])
  ))
  const saturated = backgroundIpc.getBackgroundWorkerState()
  assert.equal(saturated.pendingQueues.usageRecords.queueLength, 12_048, 'usage IPC drain 状态应同时统计硬队列与 admission 等待者')
  await assert.rejects(
    backgroundIpc.sendUsageRecordsToWorkerAsync([buildUsageRecord(12_048)]),
    /admission 等待队列已满/,
    'usage IPC admission 等待区耗尽后必须明确拒绝'
  )

  const fakeWorker = new FakeIngestWorkerProcess()
  backgroundIpc.attachBackgroundWorkerProcess(fakeWorker as unknown as ChildProcess, { role: 'ingest-worker' })
  fakeWorker.ready()
  await Promise.all(waiters)
  await waitUntil(() => backgroundIpc.getBackgroundWorkerState().pendingQueues.usageRecords.queueLength === 0)
  assert.equal(fakeWorker.sentCount, 12_048, '容量恢复后 IPC 队列与等待区记录应全部投递')
}

async function verifyFailureFinalizationAdmissionIsBoundedAndFifo(): Promise<void> {
  let releaseTasks: (() => void) | undefined
  const taskGate = new Promise<void>((resolvePromise) => {
    releaseTasks = resolvePromise
  })
  const admitted: number[] = []
  const dispatches: Array<Promise<void>> = []
  for (let index = 0; index < 32 + 2048 + 2048; index += 1) {
    dispatches.push(failureFinalization.dispatchGatewayUsageFinalization({
      bytes: 1,
      taskFactory: async () => {
        admitted.push(index)
        await taskGate
      }
    }))
  }
  const saturated = failureFinalization.getGatewayUsageFinalizationRuntime()
  assert.equal(saturated.activeCount, 32, '收尾任务应受并发上限约束')
  assert.equal(saturated.queuedCount, 2048, '收尾执行队列应达到硬上限')
  assert.equal(saturated.pendingCount, 32 + 2048 + 2048, '收尾等待区应计入 pending 且保持有界')
  await assert.rejects(
    failureFinalization.dispatchGatewayUsageFinalization({ bytes: 1, taskFactory: async () => undefined }),
    /等待队列已满/,
    '收尾等待区耗尽后必须明确拒绝'
  )
  assert.equal(failureFinalization.getGatewayUsageFinalizationRuntime().droppedCount, 1, '拒绝 admission 应进入运行指标')

  releaseTasks?.()
  await Promise.all(dispatches)
  assert.equal(await failureFinalization.waitForGatewayFailureUsageFinalizationsIdle(10_000), true, '全部收尾任务应可排空')
  assert.deepEqual(admitted, Array.from({ length: admitted.length }, (_, index) => index), '收尾任务应按 FIFO admission 执行')
}

async function verifyLocalUsageAdmissionIsBoundedAndLossless(): Promise<void> {
  usageRecordQueue.clearUsageRecordQueueForTest()
  for (let index = 0; index < 10_000; index += 1) {
    void usageRecordQueue.enqueueUsageRecordsLocal([buildUsageRecord(index)])
  }
  const waiters = Array.from({ length: 2048 }, (_, index) => (
    usageRecordQueue.enqueueUsageRecordsLocal([buildUsageRecord(10_000 + index)])
  ))
  const saturated = usageRecordQueue.getUsageRecordQueueRuntime()
  assert.equal(saturated.queueLength, 10_000)
  assert.equal(saturated.admissionWaiterCount, 2048)
  assert.equal(saturated.droppedOverflowCount, 0, '本地队列满时等待中的记录不得计为丢弃')
  await assert.rejects(
    async () => await usageRecordQueue.enqueueUsageRecordsLocal([buildUsageRecord(12_048)]),
    /admission 等待已满/,
    '本地 admission 等待区耗尽后必须明确拒绝'
  )
  usageRecordQueue.releaseUsageRecordQueueCapacityForTest(2048)
  await Promise.all(waiters)
  const admitted = usageRecordQueue.getUsageRecordQueueRuntime()
  assert.equal(admitted.queueLength, 10_000, '释放的槽位应全部由等待记录补齐')
  assert.equal(admitted.admissionWaiterCount, 0)
  assert.equal(admitted.rejectedAdmissionCount, 1)
  usageRecordQueue.clearUsageRecordQueueForTest()
}

function buildUsageRecord(index: number) {
  return {
    traceId: `trace-usage-backpressure-${index}`,
    trafficSource: 'gateway' as const,
    systemAccountId: 'sys_usage_backpressure',
    endpoint: 'POST /v1/responses',
    providerCode: 'gpt',
    success: true,
    stream: false,
    statusCode: 200,
    durationMs: 1,
    inputTokens: 1,
    outputTokens: 1,
    costUsd: 0,
    createdAt: '2000-01-01T00:00:00.000Z'
  }
}

async function waitUntil(predicate: () => boolean, timeoutMs = 10_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('等待 usage IPC 队列排空超时')
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
}
