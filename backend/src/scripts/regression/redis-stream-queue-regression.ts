import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  RedisStreamQueue,
  RedisStreamQueueCapacityExceededError,
  type RedisStreamMessage
} from '../../shared/redis-stream-queue.js'
import type { RedisCommandClient } from '../../shared/redis-client.js'

interface TestPayload {
  id: string
  value: number
}

const queue = new RedisStreamQueue<TestPayload>({
  streamKey: 'juhe-ai:test:stream',
  groupName: 'juhe-ai:test:group',
  redisUrl: 'redis://:unused@127.0.0.1:6379/0'
})

const parser = queue as unknown as {
  parseStreamReadResult(result: unknown): Array<RedisStreamMessage<TestPayload>>
  parseAutoClaimResult(result: unknown): Array<RedisStreamMessage<TestPayload>>
  parsePendingMessageInspection(result: unknown): { ids: string[]; entries: Array<RedisStreamMessage<TestPayload>> }
}

const readMessages = parser.parseStreamReadResult([
  [
    'juhe-ai:test:stream',
    [
      ['1730000000000-0', ['payload', JSON.stringify({ id: 'a', value: 1 })]],
      ['1730000000000-1', ['payload', JSON.stringify({ id: 'b', value: 2 })]]
    ]
  ]
])
assert.deepEqual(readMessages, [
  { id: '1730000000000-0', payload: { id: 'a', value: 1 } },
  { id: '1730000000000-1', payload: { id: 'b', value: 2 } }
], 'XREADGROUP raw result should parse Redis stream payloads')

const objectReadMessages = parser.parseStreamReadResult({
  'juhe-ai:test:stream': [
    ['1730000000000-2', ['payload', JSON.stringify({ id: 'object-a', value: 10 })]]
  ]
})
assert.deepEqual(objectReadMessages, [
  { id: '1730000000000-2', payload: { id: 'object-a', value: 10 } }
], 'XREADGROUP object result should parse node-redis stream payloads')

const claimedMessages = parser.parseAutoClaimResult([
  '0-0',
  [
    ['1730000000001-0', ['payload', JSON.stringify({ id: 'c', value: 3 })]]
  ],
  []
])
assert.deepEqual(claimedMessages, [
  { id: '1730000000001-0', payload: { id: 'c', value: 3 } }
], 'XAUTOCLAIM raw result should parse pending Redis stream payloads')

const objectClaimedMessages = parser.parseAutoClaimResult({
  nextId: '0-0',
  messages: [
    ['1730000000001-1', ['payload', JSON.stringify({ id: 'object-c', value: 30 })]]
  ]
})
assert.deepEqual(objectClaimedMessages, [
  { id: '1730000000001-1', payload: { id: 'object-c', value: 30 } }
], 'XAUTOCLAIM object result should parse node-redis pending payloads')

const pendingInspection = parser.parsePendingMessageInspection([
  '1730000000002-0',
  [
    ['1730000000002-0', ['payload', JSON.stringify({ id: 'pending-a', value: 100 })]]
  ],
  '1730000000002-1',
  [
    ['1730000000002-1', ['payload', JSON.stringify({ id: 'pending-b', value: 101 })]]
  ]
])
assert.deepEqual(pendingInspection, {
  ids: ['1730000000002-0', '1730000000002-1'],
  entries: [
    { id: '1730000000002-0', payload: { id: 'pending-a', value: 100 } },
    { id: '1730000000002-1', payload: { id: 'pending-b', value: 101 } }
  ]
}, 'Lua XPENDING/XRANGE inspection result should parse pending ids and payloads')

await assertRedisStreamQueueCapacityBudget()

const runtimeSource = readFileSync(new URL('../../config/runtime.ts', import.meta.url), 'utf8')
assert.match(runtimeSource, /export type QueueDriver = 'memory' \| 'redis_stream'/, 'runtime config should expose queue driver')
assert.match(runtimeSource, /JUHE_AI_QUEUE_DRIVER/, 'runtime config should read JUHE_AI_QUEUE_DRIVER')
assert.match(runtimeSource, /JUHE_AI_REDIS_QUEUE_URL/, 'runtime config should support a dedicated Redis queue URL')

const redisStreamQueueSource = readFileSync(new URL('../../shared/redis-stream-queue.ts', import.meta.url), 'utf8')
assert.match(redisStreamQueueSource, /isRedisNoGroupError/, 'Redis Stream queue should recognize NOGROUP errors')
assert.match(redisStreamQueueSource, /recreateGroupAfterNoGroup/, 'Redis Stream queue should recreate deleted stream groups')
assert.match(redisStreamQueueSource, /readNewUnsafe[\s\S]*recreateGroupAfterNoGroup[\s\S]*readNewUnsafe/, 'XREADGROUP should retry once after recreating a missing group')
assert.match(redisStreamQueueSource, /claimPendingUnsafe[\s\S]*recreateGroupAfterNoGroup[\s\S]*claimPendingUnsafe/, 'XAUTOCLAIM should retry once after recreating a missing group')
assert.match(redisStreamQueueSource, /parseEntries[\s\S]*try[\s\S]*this\.decode\(payload\)[\s\S]*catch[\s\S]*recordPoisonMessage/, 'Redis Stream parser should retain poison messages pending and record decode failures')
assert.doesNotMatch(redisStreamQueueSource, /ackPoisonMessage|redis_stream_poison_message_ack_failed/, '坏消息不得自动 XACK/XDEL，否则 one-shot 会在未落库时丢失唯一现场')
assert.match(redisStreamQueueSource, /Redis Stream 消息解码失败，消息保留 pending 并阻断排空/, '坏消息日志必须明确其保留和门禁语义')
assert.match(redisStreamQueueSource, /this\.streamKey = redisNamespacedKey\(options\.streamKey\)/, 'Redis Stream queue should namespace stream keys')
assert.match(redisStreamQueueSource, /this\.groupName = redisNamespacedGroup\(options\.groupName\)/, 'Redis Stream queue should namespace consumer groups')
assert.match(redisStreamQueueSource, /const redisInspectPendingMessagesScript = `[\s\S]*XPENDING[\s\S]*XRANGE/, 'Redis Stream backlog inspection should fetch pending ids and payloads in one Lua call')
assert.match(redisStreamQueueSource, /async inspectBacklog\(limit = 256\)[\s\S]*pendingTruncated[\s\S]*undeliveredTruncated/, 'Redis Stream backlog inspection should expose truncation flags for stats safety windows')
assert.match(redisStreamQueueSource, /redisEnqueueWithFenceAndBacklogIndexScript[\s\S]*XADD[\s\S]*ZADD/, 'Redis Stream should support atomic backlog createdAt indexing for ordered watermarks')
assert.match(redisStreamQueueSource, /redisAckDeleteAndRemoveBacklogIndexScript[\s\S]*XACK[\s\S]*XDEL[\s\S]*ZREM/, 'Redis Stream indexed ack should atomically remove acknowledged backlog entries')
assert.match(redisStreamQueueSource, /async enqueue\(payload: T\)[\s\S]*this\.enqueueEncodedInternal\(this\.encode\(payload\), score\)/, 'Redis Stream enqueue should encode ordinary payloads and preserve optional backlog ordering metadata')
assert.match(redisStreamQueueSource, /async enqueueEncoded\(encodedPayload: string\)[\s\S]*this\.enqueueEncodedInternal\(encodedPayload\)[\s\S]*client\.eval\(redisEnqueueWithFenceScript[\s\S]*arguments: \['payload', encodedPayload\]/, 'Redis Stream should support pre-encoded worker payloads through the atomic fence/XADD script')
assert.match(redisStreamQueueSource, /const redisEnqueueWithFenceScript = `[\s\S]*GET[\s\S]*QUEUE_QUIESCED[\s\S]*XADD/, 'Redis Stream enqueue must check the queue fence and XADD atomically')
assert.match(redisStreamQueueSource, /async ack\(ids: string\[\]\): Promise<number>[\s\S]*redisAckAndDeleteMessagesScript/, 'Redis Stream ack should use a single Lua script for XACK and XDEL')
assert.match(redisStreamQueueSource, /const redisAckAndDeleteMessagesScript = `[\s\S]*XACK[\s\S]*if result > 0 then[\s\S]*XDEL/, 'Redis Stream ack should delete only messages successfully acknowledged by XACK')
assert.match(redisStreamQueueSource, /export class RedisStreamQueueCapacityExceededError/, 'Redis Stream capacity rejection should have a typed error for bounded producer fallbacks')
assert.match(redisStreamQueueSource, /maxItems\?: number[\s\S]*maxStreamMemoryBytes\?: number/, 'Redis Stream queue should expose optional item and Stream-memory capacity budgets')
assert.match(redisStreamQueueSource, /const redisEnqueueWithFenceAndCapacityScript = `[\s\S]*XLEN[\s\S]*MEMORY', 'USAGE'[\s\S]*string\.len\(ARGV\[2\]\)[\s\S]*QUEUE_CAPACITY_EXCEEDED[\s\S]*XADD/, 'capacity-enabled enqueue must atomically check stream length and actual Stream memory before XADD')
assert.match(redisStreamQueueSource, /const redisEnqueueWithFenceBacklogIndexAndCapacityScript = `[\s\S]*MEMORY', 'USAGE'[\s\S]*ZADD/, 'backlog-indexed capacity enqueue must use the same actual Stream memory budget')
assert.doesNotMatch(redisStreamQueueSource, /payload-bytes|payloadByteCounter|INCRBY|DECRBY|QUEUE_CAPACITY_COUNTER_UNINITIALIZED/, 'capacity budgets must not depend on a payload-byte sidecar that old XADD writers can desynchronize')
assert.match(redisStreamQueueSource, /async inspectRuntime\(\): Promise<RedisStreamQueueRuntime>[\s\S]*'XLEN', this\.streamKey[\s\S]*streamLength:/, 'Redis Stream runtime should expose XLEN for queue capacity monitoring')
assert.doesNotMatch(redisStreamQueueSource, /MAXLEN|redisStreamMaxLen|maxLen/, 'Redis Stream reliable queue must not expose trimming controls')

const usageQueueSource = readFileSync(new URL('../../modules/gateway/usage/record-queue.service.ts', import.meta.url), 'utf8')
assert.match(usageQueueSource, /shouldEnqueueUsageRecordToRedisStream/, 'usage record queue should route producers through Redis Stream in redis_stream mode')
assertProducerPrefersRedisStream(usageQueueSource, 'enqueueUsageRecord', 'shouldEnqueueUsageRecordToRedisStream()', [
  'sendUsageRecordsToWorker',
  'sendUsageRecordFromDbServiceToServer',
  'enqueueUsageRecordLocal'
], 'usage record producer must short-circuit to Redis Stream before IPC/local queues')
assert.match(usageQueueSource, /startUsageRecordRedisStreamConsumer/, 'usage record queue should expose an ingest-worker Redis Stream consumer')
assert.match(usageQueueSource, /queue\.ack\(messages\.map/, 'usage record Redis Stream consumer should ack only after flush')
assert.match(usageQueueSource, /Redis Stream 使用记录落库失败，消息保持 pending 等待重投/, 'usage record Redis Stream consumer should keep failed messages pending')
assert.doesNotMatch(usageQueueSource, /AfterRedisStreamFailure/, 'usage record Redis Stream producer must not fall back to IPC/local queues after enqueue failure')

const operationQueueSource = readFileSync(new URL('../../modules/operation-logs/operation-log-queue.service.ts', import.meta.url), 'utf8')
assert.match(operationQueueSource, /shouldEnqueueOperationLogToRedisStream/, 'operation log queue should route producers through Redis Stream in redis_stream mode')
assertProducerPrefersRedisStream(operationQueueSource, 'enqueueOperationLog', 'shouldEnqueueOperationLogToRedisStream()', [
  'sendOperationLogsToWorker',
  'process.send',
  'enqueueOperationLogLocal'
], 'operation log producer must short-circuit to Redis Stream before IPC/local queues')
assert.match(operationQueueSource, /startOperationLogRedisStreamConsumer/, 'operation log queue should expose an ingest-worker Redis Stream consumer')
assert.match(operationQueueSource, /queue\.ack\(messages\.map/, 'operation log Redis Stream consumer should ack only after flush')
assert.match(operationQueueSource, /Redis Stream 操作日志落库失败，消息保持 pending 等待重投/, 'operation log Redis Stream consumer should keep failed messages pending')
assert.match(operationQueueSource, /readCount: operationLogBatchSize/, 'operation log Redis Stream consumer should keep batches bounded')
assert.match(operationQueueSource, /runRedisEnqueueWithBoundedRetry\(\(\) => operationLogRedisStreamQueue\(\)\.enqueue\(input\)\)/, 'operation log producer should retry transient Redis enqueue failures with the stable ID')
assert.doesNotMatch(operationQueueSource, /AfterRedisStreamFailure/, 'operation log Redis Stream producer must not fall back to IPC/local queues after enqueue failure')

const publicApiQueueSource = readFileSync(new URL('../../modules/public-api-logs/public-api-log-queue.service.ts', import.meta.url), 'utf8')
assert.match(publicApiQueueSource, /const stableInput = ensurePublicApiLogQueueId\(input\)/, '公开接口日志必须在 Redis/IPС/本地队列分流前生成稳定 ID')
assert.match(publicApiQueueSource, /enqueuePublicApiLogToRedisStream\(stableInput\)/, 'Redis Stream 重放必须携带首次入队生成的稳定 ID')
assert.match(publicApiQueueSource, /enqueuePublicApiLogLocal\(ensurePublicApiLogQueueId\(input\)\)/, 'IPC/本地公开接口日志入队也必须在首次排队时固化稳定 ID')
assert.match(publicApiQueueSource, /shouldEnqueuePublicApiLogToRedisStream/, 'public API log queue should route producers through Redis Stream in redis_stream mode')
assertProducerPrefersRedisStream(publicApiQueueSource, 'enqueuePublicApiLog', 'shouldEnqueuePublicApiLogToRedisStream()', [
  'sendPublicApiLogsToWorker',
  'sendPublicApiLogToParent',
  'enqueuePublicApiLogsLocal'
], 'public API log producer must short-circuit to Redis Stream before IPC/local queues')
assert.match(publicApiQueueSource, /startPublicApiLogRedisStreamConsumer/, 'public API log queue should expose an ingest-worker Redis Stream consumer')
assert.match(publicApiQueueSource, /queue\.ack\(messages\.map/, 'public API log Redis Stream consumer should ack only after flush')
assert.match(publicApiQueueSource, /Redis Stream 公开接口日志落库失败，消息保持 pending 等待重投/, 'public API log Redis Stream consumer should keep failed messages pending')
assert.match(publicApiQueueSource, /readCount: publicApiLogFlushBatchSize/, 'public API log Redis Stream consumer should keep batches bounded')
assert.match(publicApiQueueSource, /runRedisEnqueueWithBoundedRetry\(\(\) => publicApiLogRedisStreamQueue\(\)\.enqueue\(input\)\)/, 'public API log producer should retry transient Redis enqueue failures with the stable ID')
assert.doesNotMatch(publicApiQueueSource, /AfterRedisStreamFailure/, 'public API log Redis Stream producer must not fall back to IPC/local queues after enqueue failure')

const recordMaintenanceQueueSource = readFileSync(new URL('../../modules/record-maintenance/record-maintenance-queue.service.ts', import.meta.url), 'utf8')
assert.match(recordMaintenanceQueueSource, /shouldEnqueueRecordMaintenanceJobToRedisStream/, 'record maintenance queue should route all producers through Redis Stream in redis_stream mode')
assertProducerPrefersRedisStream(recordMaintenanceQueueSource, 'enqueueRecordMaintenanceJobWithResult', 'shouldEnqueueRecordMaintenanceJobToRedisStream(job)', [
  'sendRecordMaintenanceJobsToWorker',
  'process.send',
  'enqueueRecordMaintenanceJobLocal'
], 'record maintenance producer must short-circuit to Redis Stream before IPC/local queues')
assert.match(recordMaintenanceQueueSource, /startRecordMaintenanceRedisStreamConsumer/, 'record maintenance queue should expose an ingest-worker Redis Stream consumer')
assert.match(recordMaintenanceQueueSource, /queue\.ack\(/, 'record maintenance Redis Stream consumer should ack only after each successful job or coalesced batch')
assert.match(recordMaintenanceQueueSource, /Redis Stream 数据维护任务执行失败，消息保持 pending 等待重投/, 'record maintenance Redis Stream consumer should keep failed messages pending')
assert.match(recordMaintenanceQueueSource, /readCount: recordMaintenanceBatchSize/, 'record maintenance Redis Stream consumer should keep batches bounded')
assert.doesNotMatch(recordMaintenanceQueueSource, /AfterRedisStreamFailure/, 'record maintenance Redis Stream producer must not fall back to IPC/local queues after enqueue failure')

const workerSource = readFileSync(new URL('../../worker.ts', import.meta.url), 'utf8')
assert.doesNotMatch(workerSource, /startAuditLogRedisStreamConsumer\(\)|stopAuditLogRedisStreamConsumer\(\)/, 'F3 owner exit must remove the audit Redis Stream consumer from Node')
assert.match(workerSource, /startOperationLogRedisStreamConsumer\(\)/, 'ingest worker should start operation log Redis Stream consumer')
assert.match(workerSource, /await stopOperationLogRedisStreamConsumer\(\)/, 'worker shutdown should stop operation log Redis Stream consumer')
assert.match(workerSource, /startPublicApiLogRedisStreamConsumer\(\)/, 'ingest worker should start public API log Redis Stream consumer')
assert.match(workerSource, /await stopPublicApiLogRedisStreamConsumer\(\)/, 'worker shutdown should stop public API log Redis Stream consumer')
assert.match(workerSource, /startRecordMaintenanceRedisStreamConsumer\(\)/, 'ingest worker should start record maintenance Redis Stream consumer')
assert.match(workerSource, /await stopRecordMaintenanceRedisStreamConsumer\(\)/, 'worker shutdown should stop record maintenance Redis Stream consumer')
assert.doesNotMatch(workerSource, /RuntimeLogRedisStreamConsumer/, 'ingest worker must not start a runtime log Redis Stream consumer')

const gatewayLoadSource = readFileSync(new URL('../performance/performance-gateway-load-test.ts', import.meta.url), 'utf8')
assert.match(gatewayLoadSource, /XINFO', 'GROUPS'/, 'gateway load test should sample Redis Stream group lag, not only pending')
assert.match(gatewayLoadSource, /backlogCount: usageRecords\.backlogCount \+ operationLogs\.backlogCount \+ publicApiLogs\.backlogCount \+ recordMaintenance\.backlogCount/, 'gateway load test should gate total active Redis Stream backlog')
assert.match(gatewayLoadSource, /redisStreamsDelta\(input\.redisBefore,\s*input\.redisAfter\)/, 'gateway load test should compare Redis Stream backlog against the pre-test baseline')
assert.match(gatewayLoadSource, /redisDelta\.positiveBacklogDelta > input\.input\.maxAllowedRedisPending/, 'gateway load test should fail only on new Redis Stream backlog produced by the current run')
assert.doesNotMatch(gatewayLoadSource, /input\.redisAfter\.backlogCount > input\.input\.maxAllowedRedisPending/, 'gateway load test should not fail on historical Redis Stream backlog alone')
assert.match(gatewayLoadSource, /operationLogRedisStreamKey = redisNamespacedKey\('juhe-ai:queue:operation-logs'\)/, 'gateway load test should sample operation log Redis Stream')
assert.match(gatewayLoadSource, /publicApiLogRedisStreamKey = redisNamespacedKey\('juhe-ai:queue:public-api-logs'\)/, 'gateway load test should sample public API log Redis Stream')
assert.match(gatewayLoadSource, /recordMaintenanceRedisStreamKey = redisNamespacedKey\('juhe-ai:queue:record-maintenance'\)/, 'gateway load test should sample record maintenance Redis Stream')
assert.doesNotMatch(gatewayLoadSource, /auditLogRedisStreamKey|auditLogRedisStreamGroup|auditLogs: RedisStream/, 'F3 audit input must not be sampled as a Redis Stream')
assert.doesNotMatch(gatewayLoadSource, /runtimeLogRedisStreamKey/, 'gateway load test must not sample a removed runtime log Redis Stream')

console.log('redis-stream-queue-regression passed')

function assertProducerPrefersRedisStream(
  source: string,
  functionName: string,
  redisMarker: string,
  forbiddenBeforeRedisMarkers: string[],
  message: string
): void {
  const body = sourceFunctionBlock(source, functionName)
  const redisIndex = body.indexOf(redisMarker)
  assert(redisIndex >= 0, `${message}: missing ${redisMarker}`)
  for (const marker of forbiddenBeforeRedisMarkers) {
    const markerIndex = body.indexOf(marker)
    assert(markerIndex < 0 || redisIndex < markerIndex, `${message}: ${marker} appears before Redis Stream branch`)
  }
}

function sourceFunctionBlock(source: string, functionName: string): string {
  const start = source.indexOf(`function ${functionName}`)
  assert(start >= 0, `missing function ${functionName}`)
  let openBrace = -1
  let parenDepth = 0
  for (let index = start; index < source.length; index += 1) {
    const char = source[index]
    if (char === '(') parenDepth += 1
    if (char === ')') parenDepth = Math.max(0, parenDepth - 1)
    if (char === '{' && parenDepth === 0) {
      openBrace = index
      break
    }
  }
  assert(openBrace >= 0, `missing body for ${functionName}`)
  let depth = 0
  for (let index = openBrace; index < source.length; index += 1) {
    const char = source[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return source.slice(openBrace, index + 1)
      }
    }
  }
  throw new Error(`unclosed function body for ${functionName}`)
}

async function assertRedisStreamQueueCapacityBudget(): Promise<void> {
  const entries = new Map<string, string>()
  const pending = new Set<string>()
  let nextId = 0
  const streamMemoryBytes = () => Array.from(entries.values())
    .reduce((total, payload) => total + Buffer.byteLength(payload, 'utf8'), 0)
  const client = {
    connect: async () => undefined,
    get: async () => null,
    set: async () => 'OK',
    del: async () => 0,
    sendCommand: async () => null,
    on: () => undefined,
    eval: async (script, options) => {
      if (script.includes('QUEUE_CAPACITY_EXCEEDED')) {
        assert.equal(options.keys.length, 2, 'capacity enqueue must keep only the fence and Stream in one Lua invocation')
        const [field, encodedPayload, maxItemsValue, maxStreamMemoryBytesValue] = options.arguments
        assert.equal(field, 'payload')
        const maxItems = Number(maxItemsValue)
        const maxStreamMemoryBytes = Number(maxStreamMemoryBytesValue)
        const bytes = Buffer.byteLength(encodedPayload ?? '', 'utf8')
        if (maxItems >= 0 && entries.size >= maxItems) {
          throw new Error('QUEUE_CAPACITY_EXCEEDED')
        }
        if (maxStreamMemoryBytes >= 0 && streamMemoryBytes() + bytes > maxStreamMemoryBytes) {
          throw new Error('QUEUE_CAPACITY_EXCEEDED')
        }
        nextId += 1
        const id = `${nextId}-0`
        entries.set(id, encodedPayload ?? '')
        pending.add(id)
        return id
      }
      if (script.includes("redis.call('XACK'")) {
        assert.equal(options.keys.length, 1, 'capacity ACK must not mutate a sidecar counter')
        const [, ...ids] = options.arguments
        let acked = 0
        for (const id of ids) {
          if (!pending.delete(id)) continue
          entries.delete(id)
          acked += 1
        }
        return acked
      }
      throw new Error('unexpected Redis Stream queue script')
    }
  } satisfies RedisCommandClient

  const queue = new RedisStreamQueue<string>({
    streamKey: 'juhe-ai:test:capacity-stream',
    groupName: 'juhe-ai:test:capacity-group',
    redisUrl: 'redis://127.0.0.1:6381/0',
    producerClient: async () => client,
    maxItems: 10,
    maxStreamMemoryBytes: 6
  })

  const firstId = await queue.enqueueEncoded('abc')
  entries.set('old-writer-0', 'def')
  await assert.rejects(
    queue.enqueueEncoded('g'),
    (error: unknown) => error instanceof RedisStreamQueueCapacityExceededError,
    'a new writer must reject against actual Stream memory after an old direct XADD writer adds data'
  )
  assert.equal(await queue.ack([firstId]), 1, 'successful ACK must use the normal XACK/XDEL path without a sidecar update')
  assert.equal(await queue.enqueueEncoded('a'), '2-0', 'after ACK, the actual remaining Stream memory must admit a bounded new write')

  entries.clear()
  pending.clear()
  const itemQueue = new RedisStreamQueue<string>({
    streamKey: 'juhe-ai:test:item-capacity-stream',
    groupName: 'juhe-ai:test:item-capacity-group',
    redisUrl: 'redis://127.0.0.1:6381/0',
    producerClient: async () => client,
    maxItems: 2,
    maxStreamMemoryBytes: 100
  })
  const secondId = await itemQueue.enqueueEncoded('a')
  const thirdId = await itemQueue.enqueueEncoded('b')
  await assert.rejects(
    itemQueue.enqueueEncoded('c'),
    (error: unknown) => error instanceof RedisStreamQueueCapacityExceededError,
    'item budget must reject the next producer without trimming pending messages'
  )
  assert.equal(await itemQueue.ack([secondId, thirdId]), 2)
}
