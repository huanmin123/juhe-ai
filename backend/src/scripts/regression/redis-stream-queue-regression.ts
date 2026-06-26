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

const usageQueueSource = readFileSync(new URL('../../modules/gateway/usage/record-queue.service.ts', import.meta.url), 'utf8')
assert.match(usageQueueSource, /shouldEnqueueUsageRecordToRedisStream/, 'usage record queue should route producers through Redis Stream in redis_stream mode')
assert.match(usageQueueSource, /startUsageRecordRedisStreamConsumer/, 'usage record queue should expose an ingest-worker Redis Stream consumer')
assert.match(usageQueueSource, /queue\.ack\(messages\.map/, 'usage record Redis Stream consumer should ack only after flush')
assert.match(usageQueueSource, /Redis Stream 使用记录落库失败，消息保持 pending 等待重投/, 'usage record Redis Stream consumer should keep failed messages pending')

console.log('redis-stream-queue-regression passed')
