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

  console.log('runtime-probe-state-store-redis-smoke passed')
} finally {
  try {
    await store.delete(runtimeKey)
  } finally {
    await closeRedisClients()
  }
}
