import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const queueSource = readFileSync(new URL('../../shared/redis-stream-queue.ts', import.meta.url), 'utf8')
const usageQueueSource = readFileSync(new URL('../../modules/gateway/usage/record-queue.service.ts', import.meta.url), 'utf8')

assert.match(queueSource, /backlogCreatedAt\?: \(payload: T\) => string \| undefined/)
assert.match(
  queueSource,
  /redisEnqueueWithFenceAndBacklogIndexScript[\s\S]*XADD[\s\S]*ZADD/,
  'usage backlog createdAt 必须和 Stream XADD 原子写入有序索引'
)
assert.match(
  queueSource,
  /redisAckDeleteAndRemoveBacklogIndexScript[\s\S]*XACK[\s\S]*XDEL[\s\S]*ZREM/,
  'usage backlog createdAt 索引必须和成功 ACK/XDEL 原子清理'
)
assert.match(
  queueSource,
  /inspectOldestBacklogCreatedAt\(backfillLimit = 512\)[\s\S]*backlogCreatedAtCursorKey[\s\S]*XRANGE[\s\S]*String\(normalizedLimit\)[\s\S]*redisBackfillBacklogCreatedAtIndexScript/,
  '历史 backlog 索引补建必须使用持久 cursor 和固定单轮上限'
)
assert.match(
  queueSource,
  /redisReadBacklogCreatedAtIndexScript[\s\S]*XLEN[\s\S]*ZCARD[\s\S]*ZRANGE/,
  '水位只允许从覆盖完整 backlog 的有序索引读取，不能取截断前缀最小值'
)
assert.match(
  usageQueueSource,
  /backlogCreatedAt: \(input\) => normalizeUsageRecordCreatedAtForBacklog\(input\.createdAt\)/,
  'usage Redis Stream 必须配置业务 createdAt 索引'
)

const watermarkFunction = functionBody(usageQueueSource, 'getUsageRecordRedisStreamOldestCreatedAt')
assert.match(watermarkFunction, /inspectOldestBacklogCreatedAt\(512\)/)
assert.match(watermarkFunction, /if \(!watermark\.ready\)[\s\S]*usageRecordRedisStreamBacklogConservativeCreatedAt/)
assert.match(watermarkFunction, /return watermark\.oldestCreatedAt/)
assert.doesNotMatch(watermarkFunction, /inspectBacklog\(/)

const createdAtValues = Array.from({ length: 900 }, (_, index) => Date.parse('2026-07-26T01:00:00.000Z') + index)
createdAtValues[700] = Date.parse('2026-07-25T00:00:00.000Z')
assert.notEqual(
  Math.min(...createdAtValues.slice(0, 512)),
  Math.min(...createdAtValues),
  '回归夹具必须证明截断前缀无法代表全 backlog 的最老业务时间'
)

console.log('usage-record-backlog-watermark-regression passed')

function functionBody(source: string, functionName: string): string {
  const start = source.indexOf(`function ${functionName}`)
  assert(start >= 0, `missing function ${functionName}`)
  const openBrace = source.indexOf('{', start)
  assert(openBrace >= 0, `missing body for ${functionName}`)
  let depth = 0
  for (let index = openBrace; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1
    if (source[index] === '}') {
      depth -= 1
      if (depth === 0) return source.slice(openBrace, index + 1)
    }
  }
  throw new Error(`unclosed function body for ${functionName}`)
}
