import assert from 'node:assert/strict'

import { closeRedisClients, getRedisClient } from '../../shared/redis-client.js'
import { MemoryHotQualityStore } from '../../modules/gateway/runtime/hot-quality-memory-store.js'
import {
  RedisHotQualityStore,
  redisHotQualityStoreKeys
} from '../../modules/gateway/runtime/hot-quality-redis-store.js'
import {
  createHotQualityModelFamilyCatalog,
  type HotQualityScope,
  type HotQualityStore
} from '../../modules/gateway/runtime/hot-quality-store.js'

const terminalOutcomeClasses = [
  'completed_response',
  'explicit_policy_failure',
  'transport_failure',
  'timeout',
  'read_interruption',
  'incomplete_response',
  'unknown',
  'client_cancellation'
] as const

const firstByteSamples = [800, 1_500, 5_000, 10_001, 30_000, 60_001]

const redisUrl = process.env.JUHE_AI_TEST_REDIS_URL?.trim()
if (!redisUrl) {
  console.log('hot-quality-redis-smoke skipped: JUHE_AI_TEST_REDIS_URL 未配置')
  process.exit(0)
}

const name = `regression-${process.pid}-${Date.now()}`
const keys = redisHotQualityStoreKeys(name)
const redis = await getRedisClient(redisUrl)
await clearStore(redis, keys.prefix)

try {
  const now = 120_000
  const families = createHotQualityModelFamilyCatalog(['gpt-5'])
  const scope: HotQualityScope = {
    accountRuntimeKey: 'redis-parity-account',
    protocolProfile: 'openai-responses',
    requestLane: 'text',
    modelFamily: families.resolve('gpt-5')
  }
  const memory = new MemoryHotQualityStore({ keyCapacity: 4, attemptCapacity: 256, now: () => now })
  const first = new RedisHotQualityStore({ redisUrl, name, keyCapacity: 4, attemptCapacity: 256, now: () => now })
  const second = new RedisHotQualityStore({ redisUrl, name, keyCapacity: 4, attemptCapacity: 256, now: () => now })
  const attemptIds = Array.from({ length: 100 }, (_, index) => `redis-parity-${index}`)

  await Promise.all(attemptIds.flatMap((attemptId, index) => [
    memory.recordAttempt({ attemptId, scope, nowMs: now }),
    (index % 2 === 0 ? first : second).recordAttempt({ attemptId, scope, nowMs: now })
  ]))

  const memoryTerminalResults = await duplicateTerminalWave(memory, attemptIds, scope, now)
  const redisTerminalResults = await Promise.all(attemptIds.flatMap((attemptId, index) => {
    const input = terminalInput(attemptId, scope, now, index)
    return index % 2 === 0
      ? [first.recordTerminal(input), second.recordTerminal(input)]
      : [second.recordTerminal(input), first.recordTerminal(input)]
  }))
  assert.equal(memoryTerminalResults.filter((result) => result.status === 'applied').length, 100)
  assert.equal(memoryTerminalResults.filter((result) => result.status === 'idempotent').length, 100)
  assert.equal(redisTerminalResults.filter((result) => result.status === 'applied').length, 100, '双 adapter 并发终态每个 attempt 只能生效一次')
  assert.equal(redisTerminalResults.filter((result) => result.status === 'idempotent').length, 100)

  const [memorySnapshot, redisSnapshot] = await Promise.all([memory.get(scope, now), first.get(scope, now)])
  assert.deepEqual(normalized(redisSnapshot), normalized(memorySnapshot), 'Redis 与 memory 的 30 桶及 5/10/30 快照必须一致')
  assert.equal((await second.stats(now)).terminalIdentityCount, 100)

  console.log('hot-quality-redis-smoke passed')
} finally {
  try {
    await clearStore(redis, keys.prefix)
  } finally {
    await closeRedisClients()
  }
}

function terminalInput(attemptId: string, scope: HotQualityScope, nowMs: number, index: number) {
  const outcomeClass = terminalOutcomeClasses[index % terminalOutcomeClasses.length]!
  return {
    attemptId,
    scope,
    terminalOutcomeId: `terminal-${attemptId}`,
    outcomeClass,
    failureScope: outcomeClass === 'completed_response' ? 'none' as const : 'protocol_model' as const,
    source: outcomeClass === 'explicit_policy_failure'
      ? 'explicit_policy' as const
      : outcomeClass === 'unknown' || outcomeClass === 'client_cancellation'
        ? 'request_lifecycle' as const
        : 'gateway_transport' as const,
    firstByteMs: firstByteSamples[index % firstByteSamples.length],
    nowMs
  }
}

async function duplicateTerminalWave(store: HotQualityStore, attemptIds: string[], scope: HotQualityScope, nowMs: number) {
  return Promise.all(attemptIds.flatMap((attemptId, index) => {
    const input = terminalInput(attemptId, scope, nowMs, index)
    return [store.recordTerminal(input), store.recordTerminal(input)]
  }))
}

async function clearStore(client: Awaited<ReturnType<typeof getRedisClient>>, prefix: string): Promise<void> {
  let cursor = '0'
  do {
    const raw = await client.sendCommand(['SCAN', cursor, 'MATCH', `${prefix}:*`, 'COUNT', '200'])
    const [nextCursor, values] = raw as [string | number, unknown[]]
    cursor = String(nextCursor)
    const redisKeys = values.map((value) => Buffer.isBuffer(value) ? value.toString('utf8') : String(value))
    if (redisKeys.length > 0) await client.sendCommand(['DEL', ...redisKeys])
  } while (cursor !== '0')
}

function normalized(value: unknown): unknown {
  return JSON.parse(JSON.stringify(value)) as unknown
}
