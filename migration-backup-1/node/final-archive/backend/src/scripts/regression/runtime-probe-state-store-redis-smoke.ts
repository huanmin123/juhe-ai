import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'

const redisUrl = process.env.JUHE_AI_TEST_REDIS_URL?.trim()
if (!redisUrl) {
  console.log('runtime probe state Redis smoke skipped: JUHE_AI_TEST_REDIS_URL 未配置')
  process.exit(0)
}

runtimeConfig.runtimeStateDriver = 'redis'
runtimeConfig.redis.stateUrl = redisUrl

const { createRuntimeProbeStateStore } = await import('../../shared/runtime-probe-state-store.js')

interface ProbeState {
  runtimeKey: string
  generation: number
  nextProbeAtMs: number
  configRevision?: number
  outcome?: string
  sourceFences?: string[]
  probeRunId?: string
  probeRunUntilMs?: number
  probeRunPreviousNextProbeAtMs?: number
}

const storeName = `runtime-probe-fence-${process.pid}-${Date.now()}`
const store = createRuntimeProbeStateStore<ProbeState>(storeName)
const runtimeKey = `account-${process.pid}-${Date.now()}`
const ttlMs = 60_000
const now = Date.now()

try {
  assert.equal(await store.set({ runtimeKey, generation: 1, nextProbeAtMs: now }, ttlMs), true)
  const claimed = await store.acquireGenerationRun(runtimeKey, 1, 'run-current', now + 10_000, ttlMs)
  assert.equal(claimed?.probeRunId, 'run-current')
  assert.equal(await store.renewGenerationRun(runtimeKey, 1, 'run-current', now + 20_000, ttlMs), true)
  assert.equal((await store.get(runtimeKey))?.probeRunUntilMs, now + 20_000)
  assert.equal(await store.renewGenerationRun(runtimeKey, 1, 'run-stale', now + 30_000, ttlMs), false)

  assert.equal(await store.deleteGenerationRun(runtimeKey, 1, 'run-current'), true)
  assert.equal(
    await store.renewGenerationRun(runtimeKey, 1, 'run-current', now + 30_000, ttlMs),
    false,
    '真实成功删除 Redis 运行态后，迟到 run 不得续租'
  )

  assert.equal(await store.set({ runtimeKey, generation: 2, nextProbeAtMs: now }, ttlMs), true)
  assert(await store.acquireGenerationRun(runtimeKey, 2, 'run-generation-2', now + 10_000, ttlMs))
  assert.equal(await store.set({ runtimeKey, generation: 3, nextProbeAtMs: now }, ttlMs), true)
  assert.equal(
    await store.renewGenerationRun(runtimeKey, 2, 'run-generation-2', now + 20_000, ttlMs),
    false,
    '新 generation 覆盖后，旧 generation + runId 不得续租'
  )

  const settled = {
    runtimeKey,
    generation: 4,
    nextProbeAtMs: now,
    configRevision: 7,
    outcome: 'success',
    sourceFences: ['["source-state","account-a",1,"00000000-0000-4000-8000-000000000001"]']
  } satisfies ProbeState
  assert.equal(await store.set(settled, ttlMs), true)
  const replacement = {
    runtimeKey,
    generation: 5,
    nextProbeAtMs: now + 1_000,
    configRevision: 7,
    probeRunId: 'replacement-owner',
    probeRunUntilMs: now + 10_000
  } satisfies ProbeState
  assert.deepEqual(
    await store.replaceSettledGeneration(replacement, settled.generation, ttlMs),
    settled,
    'Redis 原子替换必须返回完整旧 settled 快照，供调用方结算旧 fence'
  )
  assert.deepEqual(
    await store.get(runtimeKey),
    replacement,
    'Redis 原子替换后新 generation 不能继承旧 outcome 或 source fence'
  )

  console.log('runtime-probe-state-store-redis-smoke passed')
} finally {
  try {
    await store.delete(runtimeKey)
  } finally {
    await closeRedisClients()
  }
}
