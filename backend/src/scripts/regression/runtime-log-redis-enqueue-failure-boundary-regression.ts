import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  clearRuntimeLogIndexQueueForTest,
  getRuntimeLogIndexRuntime,
  recordRuntimeLogRedisStreamEnqueueFailureForTest
} from '../../modules/runtime-logs/runtime-log-index-queue.service.js'
import { BoundedRuntimeLogRedisProducer } from '../../modules/runtime-logs/runtime-log-redis-producer.js'

clearRuntimeLogIndexQueueForTest()

try {
  recordRuntimeLogRedisStreamEnqueueFailureForTest(Object.assign(new Error(''), { code: 'ECONNRESET' }))
  const runtime = getRuntimeLogIndexRuntime()

  assert.equal(runtime.redisEnqueueFailureCount, 1, 'Redis Stream 入队失败应单独计数')
  assert.equal(runtime.droppedCount, 1, '入队失败的派生运行日志应计入丢弃总数')
  assert(runtime.redisEnqueueLastErrorAt, 'Redis Stream 入队失败应记录发生时间')
  assert.match(runtime.flushLastError ?? '', /Error code=ECONNRESET/, '空错误消息也必须保留错误类型和 code')

  const source = readFileSync(new URL('../../modules/runtime-logs/runtime-log-index-queue.service.ts', import.meta.url), 'utf8')
  const redisClientSource = readFileSync(new URL('../../shared/redis-client.ts', import.meta.url), 'utf8')
  assert.match(source, /disableOfflineQueue: true/, '运行日志生产者必须使用禁用离线队列的专用 Redis 连接')
  assert.match(source, /commandsQueueMaxLength: runtimeLogRedisProducerCommandQueueMaxLength/, '运行日志生产者必须限制 node-redis 命令队列')
  assert.match(source, /runtimeLogRedisProducerMaxInFlightCount/, '运行日志生产者必须限制应用层并发命令数')
  assert.match(source, /runtimeLogRedisProducerMaxInFlightBytes/, '运行日志生产者必须限制应用层在途字节数')
  assert.match(source, /readinessTimeoutMs: runtimeLogRedisProducerReadinessTimeoutMs/, '首次 Redis 连接必须使用独立于 XADD 的就绪窗口')
  assert.match(redisClientSource, /disableOfflineQueue\?: boolean/, '专用 Redis client 必须支持关闭离线队列')
  assert.match(redisClientSource, /commandsQueueMaxLength\?: number/, '专用 Redis client 必须支持限制命令队列长度')
  assert.doesNotMatch(source, /scheduleProcessFatalError/, '单次运行日志索引入队失败不得触发进程级 fatal')

  let releasePending: (() => void) | undefined
  const pendingCommand = new Promise<void>((resolve) => { releasePending = resolve })
  const saturatedDrops: string[] = []
  const pendingProducer = new BoundedRuntimeLogRedisProducer<string>({
    maxInFlightCount: 2,
    maxInFlightBytes: 100,
    commandTimeoutMs: 1000,
    isReady: () => true,
    send: async () => await pendingCommand,
    onDrop: (event) => saturatedDrops.push(event.reason)
  })
  assert.equal(pendingProducer.enqueue('pending-a', 40), true)
  assert.equal(pendingProducer.enqueue('pending-b', 40), true)
  assert.equal(pendingProducer.enqueue('overflow', 40), false, 'pending Promise 达到数量上限后必须立即丢弃')
  assert.deepEqual(saturatedDrops, ['saturated'])
  assert.deepEqual(pendingProducer.snapshot(), {
    inFlightCount: 2,
    inFlightBytes: 80,
    maxInFlightCount: 2,
    maxInFlightBytes: 100,
    acceptedCount: 2,
    successCount: 0,
    droppedCount: 1,
    saturatedDropCount: 1,
    disconnectedDropCount: 0,
    timeoutDropCount: 0,
    commandFailureDropCount: 0
  })
  releasePending?.()
  await waitFor(() => pendingProducer.snapshot().inFlightCount === 0)

  let disconnectedSendCount = 0
  const disconnectedDrops: string[] = []
  const disconnectedProducer = new BoundedRuntimeLogRedisProducer<string>({
    maxInFlightCount: 1,
    maxInFlightBytes: 100,
    commandTimeoutMs: 100,
    isReady: () => false,
    send: async () => { disconnectedSendCount += 1 },
    onDrop: (event) => disconnectedDrops.push(event.reason)
  })
  assert.equal(disconnectedProducer.enqueue('disconnected', 10), true)
  await waitFor(() => disconnectedProducer.snapshot().inFlightCount === 0)
  assert.equal(disconnectedSendCount, 0, '连接已断开时不得把 XADD 交给 node-redis offline queue')
  assert.deepEqual(disconnectedDrops, ['disconnected'])
  assert.equal(disconnectedProducer.snapshot().disconnectedDropCount, 1)

  let readinessSendCount = 0
  const delayedReadinessProducer = new BoundedRuntimeLogRedisProducer<string>({
    maxInFlightCount: 1,
    maxInFlightBytes: 100,
    readinessTimeoutMs: 100,
    commandTimeoutMs: 20,
    isReady: async () => {
      await new Promise((resolve) => setTimeout(resolve, 40))
      return true
    },
    send: async () => { readinessSendCount += 1 }
  })
  assert.equal(delayedReadinessProducer.enqueue('cold-start', 10), true)
  await waitFor(() => delayedReadinessProducer.snapshot().successCount === 1, 500)
  assert.equal(readinessSendCount, 1, '首次连接慢于 XADD 截止时，仍应在独立 readiness 窗口内完成入队')
  assert.equal(delayedReadinessProducer.snapshot().timeoutDropCount, 0)

  let readinessTimeoutCallbackCount = 0
  const readinessDrops: Array<{ reason: string; error?: unknown }> = []
  const readinessTimeoutProducer = new BoundedRuntimeLogRedisProducer<string>({
    maxInFlightCount: 1,
    maxInFlightBytes: 100,
    readinessTimeoutMs: 20,
    commandTimeoutMs: 100,
    isReady: async () => await new Promise(() => undefined),
    send: async () => undefined,
    onDrop: (event) => readinessDrops.push({ reason: event.reason, error: event.error }),
    onTimeout: () => { readinessTimeoutCallbackCount += 1 }
  })
  assert.equal(readinessTimeoutProducer.enqueue('readiness-timeout', 10), true)
  await waitFor(() => readinessTimeoutProducer.snapshot().disconnectedDropCount === 1, 500)
  assert.equal(readinessTimeoutCallbackCount, 0, '连接就绪超时不能冒充 XADD 超时并销毁已连接客户端')
  assert.equal(readinessDrops[0]?.reason, 'disconnected')
  assert.match(readinessDrops[0]?.error instanceof Error ? readinessDrops[0].error.message : '', /readiness timed out/)

  let timeoutCallbackCount = 0
  const timeoutProducer = new BoundedRuntimeLogRedisProducer<string>({
    maxInFlightCount: 1,
    maxInFlightBytes: 100,
    commandTimeoutMs: 20,
    isReady: () => true,
    send: async () => await new Promise(() => undefined),
    onTimeout: () => { timeoutCallbackCount += 1 }
  })
  assert.equal(timeoutProducer.enqueue('timeout', 10), true)
  await waitFor(() => timeoutProducer.snapshot().timeoutDropCount === 1, 500)
  assert.equal(timeoutCallbackCount, 1, 'XADD 命令超时必须触发专用连接销毁回调')
  assert.equal(timeoutProducer.snapshot().inFlightCount, 0)

  console.log('运行日志 Redis 入队失败边界回归通过：offline queue 已禁用，pending/断线/超时均有界降级且不终止业务进程')
} finally {
  clearRuntimeLogIndexQueueForTest()
}

async function waitFor(predicate: () => boolean, timeoutMs = 1000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('等待运行日志 Redis 生产者状态超时')
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}
