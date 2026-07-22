import assert from 'node:assert/strict'

import { getRedisClient } from '../../shared/redis-client.js'
import {
  RedisAccountCircuitStore,
  redisAccountCircuitStoreKeys
} from '../../modules/gateway/runtime/account-circuit-redis-store.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import type { AccountCircuitScope } from '../../modules/gateway/runtime/account-circuit-store.js'

const redisUrl = process.env.JUHE_AI_TEST_REDIS_URL?.trim()
if (!redisUrl) {
  console.log('account-circuit-redis-smoke skipped: JUHE_AI_TEST_REDIS_URL 未配置')
  process.exit(0)
}

const name = `regression-${process.pid}-${Date.now()}`
const keys = redisAccountCircuitStoreKeys(name)
const parityName = `${name}-parity`
const parityKeys = redisAccountCircuitStoreKeys(parityName)
const redis = await getRedisClient(redisUrl)
await redis.sendCommand(['DEL', keys.states, keys.due, keys.closed, parityKeys.states, parityKeys.due, parityKeys.closed])

try {
  let now = 100_000
  const memoryParity = new MemoryAccountCircuitStore({ capacity: 4, closedRetentionMs: 100, now: () => now })
  const redisParity = new RedisAccountCircuitStore({ redisUrl, name: parityName, capacity: 4, closedRetentionMs: 100, now: () => now })
  const parityScope: AccountCircuitScope = {
    kind: 'protocol_model',
    accountRuntimeKey: 'parity',
    protocolProfile: 'profile_openai_v1',
    requestLane: 'text',
    modelBucket: 'gpt-5'
  }
  const paritySteps = [
    () => Promise.all([
      memoryParity.suspect({ scope: parityScope, dispatchRevision: 'p1', transitionId: 'p-suspect', reason: 'timeout', nowMs: now }),
      redisParity.suspect({ scope: parityScope, dispatchRevision: 'p1', transitionId: 'p-suspect', reason: 'timeout', nowMs: now })
    ]),
    () => Promise.all([
      memoryParity.acquireConfirmationLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-acquire', leaseId: 'p-confirm', leaseUntilMs: now + 1_000, nowMs: now }),
      redisParity.acquireConfirmationLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-acquire', leaseId: 'p-confirm', leaseUntilMs: now + 1_000, nowMs: now })
    ]),
    () => Promise.all([
      memoryParity.completeConfirmation({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-fail', leaseId: 'p-confirm', outcome: 'transport_failure', nowMs: now }),
      redisParity.completeConfirmation({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-fail', leaseId: 'p-confirm', outcome: 'transport_failure', nowMs: now })
    ])
  ]
  for (const step of paritySteps) {
    const [memoryResult, redisResult] = await step()
    assert.deepEqual(normalizedResult(redisResult), normalizedResult(memoryResult), 'Redis 与 memory 状态转换必须一致')
  }
  now += 3_000
  const [memoryCanary, redisCanary] = await Promise.all([
    memoryParity.acquireCanaryLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-canary-acquire', leaseId: 'p-canary', leaseUntilMs: now + 1_000, nowMs: now }),
    redisParity.acquireCanaryLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-canary-acquire', leaseId: 'p-canary', leaseUntilMs: now + 1_000, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisCanary), normalizedResult(memoryCanary), 'Redis 与 memory canary acquire 必须一致')
  const [memoryRecovery, redisRecovery] = await Promise.all([
    memoryParity.completeCanary({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-canary-success', leaseId: 'p-canary', outcome: 'framing_complete', nowMs: now }),
    redisParity.completeCanary({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-canary-success', leaseId: 'p-canary', outcome: 'framing_complete', nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisRecovery), normalizedResult(memoryRecovery), 'Redis 与 memory recovery 推进必须一致')

  const first = new RedisAccountCircuitStore({ redisUrl, name, capacity: 4, closedRetentionMs: 100, now: () => now })
  const second = new RedisAccountCircuitStore({ redisUrl, name, capacity: 4, closedRetentionMs: 100, now: () => now })
  const scope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: `redis-account-${process.pid}` }

  assert.equal((await first.get(scope)).phase, 'CLOSED')
  assert.equal((await first.suspect({
    scope,
    dispatchRevision: 'rev-1',
    transitionId: 'suspect',
    reason: 'timeout',
    nowMs: now
  })).status, 'applied')
  assert.equal((await second.get(scope)).phase, 'SUSPECT', '第二个 adapter 必须读取共享 Redis 状态')

  const identity = { scope, generation: 1, dispatchRevision: 'rev-1' }
  const [leaseA, leaseB] = await Promise.all([
    first.acquireConfirmationLease({
      ...identity,
      transitionId: 'confirm-acquire-a',
      leaseId: 'confirm-a',
      leaseUntilMs: now + 1_000,
      nowMs: now
    }),
    second.acquireConfirmationLease({
      ...identity,
      transitionId: 'confirm-acquire-b',
      leaseId: 'confirm-b',
      leaseUntilMs: now + 1_000,
      nowMs: now
    })
  ])
  assert.equal([leaseA, leaseB].filter((item) => item.status === 'applied').length, 1, '双节点 confirmation 只能单飞')
  const leaseId = leaseA.status === 'applied' ? 'confirm-a' : 'confirm-b'
  const opened = await second.completeConfirmation({
    ...identity,
    transitionId: 'confirm-failure',
    leaseId,
    outcome: 'transport_failure',
    nowMs: now
  })
  assert.equal(opened.state.phase, 'OPEN')
  assert.equal(opened.state.retryAtMs, now + 3_000)
  assert.equal((await first.listDue(now, 10)).length, 0)

  now += 3_000
  assert.equal((await second.listDue(now, 10))[0]?.scopeKey, opened.state.scopeKey)
  for (const index of [1, 2, 3]) {
    const adapter = index % 2 === 0 ? second : first
    assert.equal((await adapter.acquireCanaryLease({
      ...identity,
      transitionId: `canary-acquire-${index}`,
      leaseId: `canary-${index}`,
      leaseUntilMs: now + 1_000,
      nowMs: now
    })).status, 'applied')
    const completed = await adapter.completeCanary({
      ...identity,
      transitionId: `canary-complete-${index}`,
      leaseId: `canary-${index}`,
      outcome: 'framing_complete',
      nowMs: now
    })
    assert.equal(completed.state.phase, index === 3 ? 'CLOSED' : 'RECOVERING')
    if (index === 1) {
      const replay = await second.completeCanary({
        ...identity,
        transitionId: 'canary-complete-1',
        leaseId: 'canary-1',
        outcome: 'framing_complete',
        nowMs: now
      })
      assert.equal(replay.status, 'idempotent')
      assert.equal(replay.state.recoverySuccessCount, 1, '重复结果不得推进恢复计数')
    }
  }

  const revised = await second.replaceDispatchRevision({
    scope,
    dispatchRevision: 'rev-2',
    transitionId: 'revision-2',
    nowMs: now
  })
  assert.equal(revised.state.generation, 2)
  assert.equal((await first.acquireCanaryLease({
    ...identity,
    transitionId: 'stale-after-revision',
    leaseId: 'stale',
    leaseUntilMs: now + 1_000,
    nowMs: now
  })).status, 'stale_generation')

  now += 101
  assert.equal(await first.size(), 0, 'CLOSED tombstone 到期后必须从共享容量索引清理')
  console.log('account-circuit-redis-smoke passed')
} finally {
  await redis.sendCommand(['DEL', keys.states, keys.due, keys.closed, parityKeys.states, parityKeys.due, parityKeys.closed])
}

function normalizedResult(value: unknown): unknown {
  return JSON.parse(JSON.stringify(value)) as unknown
}
