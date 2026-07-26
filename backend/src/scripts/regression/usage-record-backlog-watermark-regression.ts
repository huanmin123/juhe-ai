import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { RedisStreamQueue } from '../../shared/redis-stream-queue.js'
import type { RedisCommandClient } from '../../shared/redis-client.js'

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

interface TestPayload {
  createdAt: string
}

async function runBehaviorRegression(): Promise<void> {
  const baseTime = Date.parse('2026-07-26T01:00:00.000Z')
  const outOfOrderTime = '2026-07-25T00:00:00.000Z'
  const legacyEntries = Array.from({ length: 900 }, (_, index) => ({
    id: `${index + 1}-0`,
    payload: {
      createdAt: index === 700 ? outOfOrderTime : new Date(baseTime + index).toISOString()
    }
  }))
  const fakeRedis = new FakeBacklogRedis(legacyEntries)
  const queue = new RedisStreamQueue<TestPayload>({
    streamKey: 'usage-watermark-regression',
    groupName: 'usage-watermark-regression',
    redisUrl: 'redis://unit.invalid',
    producerClient: async () => fakeRedis.client,
    backlogCreatedAt: (payload) => payload.createdAt
  })
  await assert.rejects(
    queue.enqueueEncoded(JSON.stringify({ createdAt: outOfOrderTime })),
    /禁止绕过结构化入队/,
    '启用 createdAt 索引的队列不能通过预编码入口制造未索引消息'
  )

  const firstBackfill = await queue.inspectOldestBacklogCreatedAt(512)
  assert.deepEqual(firstBackfill, {
    ready: false,
    backfilledCount: 512,
    failureReason: 'backfill_incomplete'
  })
  const completedBackfill = await queue.inspectOldestBacklogCreatedAt(512)
  assert.equal(completedBackfill.ready, true)
  assert.equal(completedBackfill.backfilledCount, 388)
  assert.equal(completedBackfill.oldestCreatedAt, outOfOrderTime)
  assert.equal(fakeRedis.maxRangeCount, 512)
  assert.equal(fakeRedis.rangeCalls, 2)

  assert.equal(await queue.ack(['701-0']), 1)
  const advanced = await queue.inspectOldestBacklogCreatedAt(512)
  assert.equal(advanced.ready, true)
  assert.equal(advanced.oldestCreatedAt, new Date(baseTime).toISOString())

  const newlyEnqueuedOldest = '2026-07-24T00:00:00.000Z'
  await queue.enqueue({ createdAt: newlyEnqueuedOldest })
  assert.equal((await queue.inspectOldestBacklogCreatedAt(512)).oldestCreatedAt, newlyEnqueuedOldest)

  const invalidRedis = new FakeBacklogRedis([{ id: '1-0', payload: { createdAt: 'invalid' } }])
  const invalidQueue = new RedisStreamQueue<TestPayload>({
    streamKey: 'usage-watermark-invalid-regression',
    groupName: 'usage-watermark-invalid-regression',
    redisUrl: 'redis://unit.invalid',
    producerClient: async () => invalidRedis.client,
    backlogCreatedAt: (payload) => payload.createdAt
  })
  assert.deepEqual(await invalidQueue.inspectOldestBacklogCreatedAt(512), {
    ready: false,
    backfilledCount: 0,
    failureReason: 'invalid_created_at'
  })

  const failedClient = {
    eval: async () => { throw new Error('redis unavailable') },
    sendCommand: async () => { throw new Error('redis unavailable') }
  } as unknown as RedisCommandClient
  const failedQueue = new RedisStreamQueue<TestPayload>({
    streamKey: 'usage-watermark-failure-regression',
    groupName: 'usage-watermark-failure-regression',
    redisUrl: 'redis://unit.invalid',
    producerClient: async () => failedClient,
    backlogCreatedAt: (payload) => payload.createdAt
  })
  await assert.rejects(
    failedQueue.inspectOldestBacklogCreatedAt(512),
    /redis unavailable/,
    'Redis 查询失败必须中止本轮水位计算，不能返回可能越界的时间'
  )
}

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

class FakeBacklogRedis {
  readonly client: RedisCommandClient
  readonly stream = new Map<string, string>()
  readonly index = new Map<string, number>()
  readonly strings = new Map<string, string>()
  rangeCalls = 0
  maxRangeCount = 0
  private nextId = 1

  constructor(entries: Array<{ id: string; payload: TestPayload }>) {
    for (const entry of entries) {
      this.stream.set(entry.id, JSON.stringify(entry.payload))
      this.nextId = Math.max(this.nextId, Number(entry.id.split('-')[0]) + 1)
    }
    this.client = {
      eval: async (script: string, options: { keys: string[]; arguments: string[] }) => this.eval(String(script), options.keys, options.arguments),
      sendCommand: async (command: string[]) => this.sendCommand(command)
    } as unknown as RedisCommandClient
  }

  private eval(script: string, keys: string[], args: string[]): unknown {
    if (script.includes("redis.call('GET', KEYS[4])")) {
      if (this.strings.get(keys[3]) !== '1') return [0]
      if (this.stream.size !== this.index.size) {
        this.index.clear()
        this.strings.delete(keys[2])
        this.strings.delete(keys[3])
        return [0]
      }
      const oldest = [...this.index.values()].sort((left, right) => left - right)[0]
      return [1, oldest === undefined ? '' : String(oldest)]
    }
    if (script.includes('for index = 3, #ARGV, 2')) {
      for (let index = 2; index < args.length; index += 2) {
        const id = args[index]
        const score = Number(args[index + 1])
        if (id && this.stream.has(id) && Number.isFinite(score)) this.index.set(id, score)
      }
      this.strings.set(keys[2], args[0] ?? '0-0')
      if (args[1] === '1') this.strings.set(keys[3], '1')
      return 1
    }
    if (script.includes("redis.call('ZREM', KEYS[2], id)")) {
      let acked = 0
      for (const id of args.slice(1)) {
        if (!this.stream.delete(id)) continue
        this.index.delete(id)
        acked += 1
      }
      return acked
    }
    if (script.includes("redis.call('ZADD', KEYS[3], ARGV[3], id)")) {
      const id = `${this.nextId}-0`
      this.nextId += 1
      this.stream.set(id, args[1] ?? '')
      this.index.set(id, Number(args[2]))
      return id
    }
    throw new Error('unexpected Redis Lua script in watermark regression')
  }

  private sendCommand(command: string[]): unknown {
    if (command[0] === 'GET') return this.strings.get(command[1] ?? '') ?? null
    if (command[0] !== 'XRANGE') throw new Error(`unexpected Redis command ${command[0]}`)
    this.rangeCalls += 1
    const count = Number(command[5])
    this.maxRangeCount = Math.max(this.maxRangeCount, count)
    const rawStart = command[2] ?? '-'
    const exclusiveStart = rawStart.startsWith('(') ? rawStart.slice(1) : undefined
    return [...this.stream.entries()]
      .sort(([left], [right]) => streamIdNumber(left) - streamIdNumber(right))
      .filter(([id]) => !exclusiveStart || streamIdNumber(id) > streamIdNumber(exclusiveStart))
      .slice(0, count)
      .map(([id, payload]) => [id, ['payload', payload]])
  }
}

function streamIdNumber(id: string): number {
  return Number(id.split('-')[0])
}

await runBehaviorRegression()
console.log('usage-record-backlog-watermark-regression passed')
