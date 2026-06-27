import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { RedisStreamQueue, type RedisStreamMessage } from '../../shared/redis-stream-queue.js'

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

const runtimeSource = readFileSync(new URL('../../config/runtime.ts', import.meta.url), 'utf8')
assert.match(runtimeSource, /export type QueueDriver = 'memory' \| 'redis_stream'/, 'runtime config should expose queue driver')
assert.match(runtimeSource, /JUHE_AI_QUEUE_DRIVER/, 'runtime config should read JUHE_AI_QUEUE_DRIVER')
assert.match(runtimeSource, /JUHE_AI_REDIS_QUEUE_URL/, 'runtime config should support a dedicated Redis queue URL')

const redisStreamQueueSource = readFileSync(new URL('../../shared/redis-stream-queue.ts', import.meta.url), 'utf8')
assert.match(redisStreamQueueSource, /isRedisNoGroupError/, 'Redis Stream queue should recognize NOGROUP errors')
assert.match(redisStreamQueueSource, /recreateGroupAfterNoGroup/, 'Redis Stream queue should recreate deleted stream groups')
assert.match(redisStreamQueueSource, /readNewUnsafe[\s\S]*recreateGroupAfterNoGroup[\s\S]*readNewUnsafe/, 'XREADGROUP should retry once after recreating a missing group')
assert.match(redisStreamQueueSource, /claimPendingUnsafe[\s\S]*recreateGroupAfterNoGroup[\s\S]*claimPendingUnsafe/, 'XAUTOCLAIM should retry once after recreating a missing group')

const usageQueueSource = readFileSync(new URL('../../modules/gateway/usage/record-queue.service.ts', import.meta.url), 'utf8')
assert.match(usageQueueSource, /shouldEnqueueUsageRecordToRedisStream/, 'usage record queue should route producers through Redis Stream in redis_stream mode')
assert.match(usageQueueSource, /startUsageRecordRedisStreamConsumer/, 'usage record queue should expose an ingest-worker Redis Stream consumer')
assert.match(usageQueueSource, /queue\.ack\(messages\.map/, 'usage record Redis Stream consumer should ack only after flush')
assert.match(usageQueueSource, /Redis Stream 使用记录落库失败，消息保持 pending 等待重投/, 'usage record Redis Stream consumer should keep failed messages pending')

const auditQueueSource = readFileSync(new URL('../../modules/audit-logs/audit-log-queue.service.ts', import.meta.url), 'utf8')
assert.match(auditQueueSource, /shouldEnqueueAuditLogToRedisStream/, 'audit log queue should route producers through Redis Stream in redis_stream mode')
assert.match(auditQueueSource, /startAuditLogRedisStreamConsumer/, 'audit log queue should expose an ingest-worker Redis Stream consumer')
assert.match(auditQueueSource, /queue\.ack\(messages\.map/, 'audit log Redis Stream consumer should ack only after flush')
assert.match(auditQueueSource, /Redis Stream 审计日志落库失败，消息保持 pending 等待重投/, 'audit log Redis Stream consumer should keep failed messages pending')
assert.match(auditQueueSource, /encode: encodeAuditLogStreamPayload/, 'audit log Redis Stream queue should use custom payload encoding')
assert.match(auditQueueSource, /decode: decodeAuditLogStreamPayload/, 'audit log Redis Stream queue should use custom payload decoding')
assert.match(auditQueueSource, /readCount: runtimeConfig\.databaseDriver === 'postgres' \? auditLogPostgresFlushBatchSize : undefined/, 'audit log Redis Stream consumer should keep PG audit batches bounded')
assert.match(auditQueueSource, /auditLogPostgresFlushBatchSize = 25/, 'audit log Redis Stream consumer should keep PG audit batches short to avoid long transactions')
assert.match(auditQueueSource, /auditLogPostgresRedisConsumerConcurrency = 1/, 'audit log Redis Stream consumer should stay single-lane to avoid PG audit write lock pressure')
assert.match(auditQueueSource, /Array\.from\(\{ length: concurrency \}/, 'audit log Redis Stream consumer should start all bounded consumer loops')
assert.match(auditQueueSource, /__juheAuditBuffer/, 'audit log Redis Stream payload encoding should preserve Buffer bodies')
assert.match(auditQueueSource, /Buffer\.from\(body\.base64, 'base64'\)/, 'audit log Redis Stream payload decoding should restore Buffer bodies')

const operationQueueSource = readFileSync(new URL('../../modules/operation-logs/operation-log-queue.service.ts', import.meta.url), 'utf8')
assert.match(operationQueueSource, /shouldEnqueueOperationLogToRedisStream/, 'operation log queue should route producers through Redis Stream in redis_stream mode')
assert.match(operationQueueSource, /startOperationLogRedisStreamConsumer/, 'operation log queue should expose an ingest-worker Redis Stream consumer')
assert.match(operationQueueSource, /queue\.ack\(messages\.map/, 'operation log Redis Stream consumer should ack only after flush')
assert.match(operationQueueSource, /Redis Stream 操作日志落库失败，消息保持 pending 等待重投/, 'operation log Redis Stream consumer should keep failed messages pending')
assert.match(operationQueueSource, /readCount: operationLogBatchSize/, 'operation log Redis Stream consumer should keep batches bounded')

const publicApiQueueSource = readFileSync(new URL('../../modules/public-api-logs/public-api-log-queue.service.ts', import.meta.url), 'utf8')
assert.match(publicApiQueueSource, /shouldEnqueuePublicApiLogToRedisStream/, 'public API log queue should route producers through Redis Stream in redis_stream mode')
assert.match(publicApiQueueSource, /startPublicApiLogRedisStreamConsumer/, 'public API log queue should expose an ingest-worker Redis Stream consumer')
assert.match(publicApiQueueSource, /queue\.ack\(messages\.map/, 'public API log Redis Stream consumer should ack only after flush')
assert.match(publicApiQueueSource, /Redis Stream 公开接口日志落库失败，消息保持 pending 等待重投/, 'public API log Redis Stream consumer should keep failed messages pending')
assert.match(publicApiQueueSource, /readCount: publicApiLogFlushBatchSize/, 'public API log Redis Stream consumer should keep batches bounded')

const recordMaintenanceQueueSource = readFileSync(new URL('../../modules/record-maintenance/record-maintenance-queue.service.ts', import.meta.url), 'utf8')
assert.match(recordMaintenanceQueueSource, /shouldEnqueueRecordMaintenanceJobToRedisStream/, 'record maintenance queue should route non-local producers through Redis Stream in redis_stream mode')
assert.match(recordMaintenanceQueueSource, /startRecordMaintenanceRedisStreamConsumer/, 'record maintenance queue should expose an ingest-worker Redis Stream consumer')
assert.match(recordMaintenanceQueueSource, /queue\.ack\(/, 'record maintenance Redis Stream consumer should ack only after each successful job or coalesced batch')
assert.match(recordMaintenanceQueueSource, /Redis Stream 数据维护任务执行失败，消息保持 pending 等待重投/, 'record maintenance Redis Stream consumer should keep failed messages pending')
assert.match(recordMaintenanceQueueSource, /readCount: recordMaintenanceBatchSize/, 'record maintenance Redis Stream consumer should keep batches bounded')

const runtimeLogQueueSource = readFileSync(new URL('../../modules/runtime-logs/runtime-log-index-queue.service.ts', import.meta.url), 'utf8')
assert.match(runtimeLogQueueSource, /shouldEnqueueRuntimeLogToRedisStream/, 'runtime log index queue should route non-ingest producers through Redis Stream in redis_stream mode')
assert.match(runtimeLogQueueSource, /startRuntimeLogRedisStreamConsumer/, 'runtime log index queue should expose an ingest-worker Redis Stream consumer')
assert.match(runtimeLogQueueSource, /createRuntimeLogsBatchAsync/, 'runtime log Redis Stream consumer should use the async PG-capable writer')
assert.match(runtimeLogQueueSource, /queue\.ack\(messages\.map/, 'runtime log Redis Stream consumer should ack only after flush')
assert.match(runtimeLogQueueSource, /Redis Stream 运行日志索引落库失败，消息保持 pending 等待重投/, 'runtime log Redis Stream consumer should keep failed messages pending')
assert.match(runtimeLogQueueSource, /readCount: runtimeLogBatchSize/, 'runtime log Redis Stream consumer should keep batches bounded')

const runtimeLogsRepositorySource = readFileSync(new URL('../../storage/runtime-logs.repository.ts', import.meta.url), 'utf8')
assert.match(runtimeLogsRepositorySource, /createRuntimeLogsBatchAsync/, 'runtime logs repository should expose async PG batch writes')
assert.match(runtimeLogsRepositorySource, /juhe_dataset\.runtime_logs/, 'runtime logs async writer should target PG dataset schema')
assert.match(runtimeLogsRepositorySource, /ON CONFLICT\(id\) DO NOTHING/, 'runtime logs async writer should keep stable source IDs idempotent')

const workerSource = readFileSync(new URL('../../worker.ts', import.meta.url), 'utf8')
assert.match(workerSource, /startAuditLogRedisStreamConsumer\(\)/, 'ingest worker should start audit log Redis Stream consumer')
assert.match(workerSource, /await stopAuditLogRedisStreamConsumer\(\)/, 'worker shutdown should stop audit log Redis Stream consumer')
assert.match(workerSource, /startOperationLogRedisStreamConsumer\(\)/, 'ingest worker should start operation log Redis Stream consumer')
assert.match(workerSource, /await stopOperationLogRedisStreamConsumer\(\)/, 'worker shutdown should stop operation log Redis Stream consumer')
assert.match(workerSource, /startPublicApiLogRedisStreamConsumer\(\)/, 'ingest worker should start public API log Redis Stream consumer')
assert.match(workerSource, /await stopPublicApiLogRedisStreamConsumer\(\)/, 'worker shutdown should stop public API log Redis Stream consumer')
assert.match(workerSource, /startRecordMaintenanceRedisStreamConsumer\(\)/, 'ingest worker should start record maintenance Redis Stream consumer')
assert.match(workerSource, /await stopRecordMaintenanceRedisStreamConsumer\(\)/, 'worker shutdown should stop record maintenance Redis Stream consumer')
assert.match(workerSource, /startRuntimeLogRedisStreamConsumer\(\)/, 'ingest worker should start runtime log Redis Stream consumer')
assert.match(workerSource, /await stopRuntimeLogRedisStreamConsumer\(\)/, 'worker shutdown should stop runtime log Redis Stream consumer')

const gatewayLoadSource = readFileSync(new URL('../performance/performance-gateway-load-test.ts', import.meta.url), 'utf8')
assert.match(gatewayLoadSource, /XINFO', 'GROUPS'/, 'gateway load test should sample Redis Stream group lag, not only pending')
assert.match(gatewayLoadSource, /backlogCount: usageRecords\.backlogCount \+ auditLogs\.backlogCount \+ operationLogs\.backlogCount \+ publicApiLogs\.backlogCount \+ recordMaintenance\.backlogCount \+ runtimeLogs\.backlogCount/, 'gateway load test should gate total Redis Stream backlog')
assert.match(gatewayLoadSource, /operationLogRedisStreamKey = 'juhe-ai:queue:operation-logs'/, 'gateway load test should sample operation log Redis Stream')
assert.match(gatewayLoadSource, /publicApiLogRedisStreamKey = 'juhe-ai:queue:public-api-logs'/, 'gateway load test should sample public API log Redis Stream')
assert.match(gatewayLoadSource, /recordMaintenanceRedisStreamKey = 'juhe-ai:queue:record-maintenance'/, 'gateway load test should sample record maintenance Redis Stream')
assert.match(gatewayLoadSource, /runtimeLogRedisStreamKey = 'juhe-ai:queue:runtime-log-index'/, 'gateway load test should sample runtime log Redis Stream')

console.log('redis-stream-queue-regression passed')
