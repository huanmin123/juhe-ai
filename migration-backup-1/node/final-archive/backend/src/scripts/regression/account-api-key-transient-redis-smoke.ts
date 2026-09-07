import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import {
  RedisAccountApiKeyTransientStateStore,
  type AccountApiKeyTransientTarget
} from '../../modules/gateway/runtime/account-api-key-transient-redis-store.js'
import { closeRedisClients } from '../../shared/redis-client.js'

const redisUrl = runtimeConfig.redis.stateUrl
assert(redisUrl, 'API Key transient Redis smoke 需要 JUHE_AI_REDIS_STATE_URL')

const marker = `${Date.now()}-${Math.random().toString(16).slice(2)}`
const store = new RedisAccountApiKeyTransientStateStore({
  redisUrl,
  name: `api-key-transient-smoke-${marker}`,
  suppressionDelayMs: [3_000, 5_000, 10_000]
})
const target: AccountApiKeyTransientTarget = {
  accountId: `account-${marker}`,
  keyFingerprint: `fingerprint-${marker}`,
  keyIndex: 0
}
const abaStore = new RedisAccountApiKeyTransientStateStore({
  redisUrl,
  name: `api-key-transient-aba-smoke-${marker}`,
  stateTtlMs: 50,
  suppressionDelayMs: [10],
  allowUnsafeShortStateTtlForTest: true
})
const abaTarget: AccountApiKeyTransientTarget = {
  accountId: `account-aba-${marker}`,
  keyFingerprint: `fingerprint-aba-${marker}`,
  keyIndex: 0
}
const counterStore = new RedisAccountApiKeyTransientStateStore({
  redisUrl,
  name: `api-key-transient-counter-smoke-${marker}`,
  stateTtlMs: 1_000,
  suppressionDelayMs: [10, 20, 30],
  failureCounterWindowMs: 50,
  allowUnsafeShortStateTtlForTest: true
})
const counterTarget: AccountApiKeyTransientTarget = {
  accountId: `account-counter-${marker}`,
  keyFingerprint: `fingerprint-counter-${marker}`,
  keyIndex: 0
}
const malformedTarget: AccountApiKeyTransientTarget = {
  accountId: `account-malformed-${marker}`,
  keyFingerprint: `fingerprint-malformed-${marker}`,
  keyIndex: 0
}

try {
  const initialGeneration = (await store.loadMany(target.accountId, [target.keyFingerprint]))[0]?.state.generation
  assert(initialGeneration)
  const firstFailure = await store.recordFailure({
    target,
    status: 'temporary_unavailable',
    expectedGeneration: initialGeneration
  })
  assert.equal(firstFailure.applied, true)
  assert.equal(firstFailure.state?.generation, initialGeneration)
  assert.equal(firstFailure.state?.failureCount, 1)
  assert.equal((await store.loadMany(target.accountId, [target.keyFingerprint]))[0]?.suppressed, true)

  const success = await store.recordSuccess({
    target,
    expectedGeneration: initialGeneration
  })
  assert.equal(success.applied, true)
  const successGeneration = success.state?.generation
  assert(successGeneration && successGeneration !== initialGeneration)
  assert.equal(success.state?.observationKind, 'success')
  assert.equal((await store.loadMany(target.accountId, [target.keyFingerprint]))[0]?.suppressed, false)

  const staleFailure = await store.recordFailure({
    target,
    status: 'error',
    expectedGeneration: initialGeneration
  })
  assert.equal(staleFailure.applied, false, '迟到 failure 必须被 success tombstone fence')
  assert.equal(staleFailure.state?.observationKind, 'success')

  const sameGenerationFailure = await store.recordFailure({
    target,
    status: 'temporary_unavailable',
    expectedGeneration: successGeneration
  })
  assert.equal(sameGenerationFailure.applied, true)
  const sameGenerationSuccess = await store.recordSuccess({
    target,
    expectedGeneration: successGeneration
  })
  assert.equal(sameGenerationSuccess.applied, true, '同 generation success 必须覆盖 failure')
  const lateSameGenerationFailure = await store.recordFailure({
    target,
    status: 'rate_limited',
    expectedGeneration: successGeneration
  })
  assert.equal(lateSameGenerationFailure.applied, false)
  assert.equal(lateSameGenerationFailure.state?.observationKind, 'success')

  const nextGeneration = sameGenerationSuccess.state?.generation
  assert(nextGeneration && nextGeneration !== successGeneration)

  const freshFailure = await store.recordFailure({
    target,
    status: 'temporary_unavailable',
    expectedGeneration: nextGeneration
  })
  assert.equal(freshFailure.applied, true, 'success 后真正更新的 failure 仍必须生效')
  assert.equal(freshFailure.state?.failureCount, 1, 'success tombstone 必须重置 transient failure backoff generation')

  const expiredGeneration = (await abaStore.loadMany(abaTarget.accountId, [abaTarget.keyFingerprint]))[0]?.state.generation
  assert(expiredGeneration)
  await delay(75)
  const replacementGeneration = (await abaStore.loadMany(abaTarget.accountId, [abaTarget.keyFingerprint]))[0]?.state.generation
  assert(replacementGeneration && replacementGeneration !== expiredGeneration, 'TTL 后 generation 必须使用新 UUID，不能 ABA 重用')
  const expiredGenerationFailure = await abaStore.recordFailure({
    target: abaTarget,
    status: 'temporary_unavailable',
    expectedGeneration: expiredGeneration
  })
  assert.equal(expiredGenerationFailure.applied, false, '过期前旧请求不得跨过 TTL 后的新 generation')
  assert.equal(expiredGenerationFailure.reason, 'stale_generation')

  const counterGeneration = (await counterStore.loadMany(counterTarget.accountId, [counterTarget.keyFingerprint]))[0]?.state.generation
  assert(counterGeneration)
  assert.equal((await counterStore.recordFailure({
    target: counterTarget,
    status: 'temporary_unavailable',
    expectedGeneration: counterGeneration
  })).state?.failureCount, 1)
  await delay(75)
  assert.equal((await counterStore.recordFailure({
    target: counterTarget,
    status: 'temporary_unavailable',
    expectedGeneration: counterGeneration
  })).state?.failureCount, 1, 'failure counter 静默超过窗口后必须重置，不能把昨天的故障延续到今天')

  await store.setRawStateForTest(malformedTarget, JSON.stringify({
    schemaVersion: 1,
    accountId: malformedTarget.accountId,
    keyFingerprint: malformedTarget.keyFingerprint,
    generation: 'partial-generation',
    observationKind: 'failure'
  }))
  const repairedMalformed = (await store.loadMany(malformedTarget.accountId, [malformedTarget.keyFingerprint]))[0]?.state
  assert(repairedMalformed)
  assert.equal(repairedMalformed.observationKind, 'success', '结构化但字段不全的 state 必须在同一 load Lua 原子修复')
  assert.notEqual(repairedMalformed.generation, 'partial-generation')
  await store.setRawStateForTest(malformedTarget, JSON.stringify({
    schemaVersion: 1,
    accountId: malformedTarget.accountId,
    keyFingerprint: malformedTarget.keyFingerprint,
    generation: 'fractional-generation',
    lastObservedAtMs: Date.now(),
    observationKind: 'failure',
    failureCount: 1.5,
    status: 'temporary_unavailable',
    suppressUntilMs: Date.now() + 1_000
  }))
  const repairedNumericMismatch = (await store.loadMany(malformedTarget.accountId, [malformedTarget.keyFingerprint]))[0]?.state
  assert(repairedNumericMismatch)
  assert.equal(repairedNumericMismatch.observationKind, 'success', 'Lua 必须拒绝 TS parser 不接受的小数/越界数值并原子修复')
  assert.notEqual(repairedNumericMismatch.generation, 'fractional-generation')
  await store.setRawStateForTest(malformedTarget, JSON.stringify({
    schemaVersion: 1,
    accountId: malformedTarget.accountId,
    keyFingerprint: malformedTarget.keyFingerprint,
    generation: 'numeric-string-generation',
    lastObservedAtMs: String(Date.now()),
    observationKind: 'failure',
    failureCount: '1',
    status: 'temporary_unavailable',
    suppressUntilMs: String(Date.now() + 1_000)
  }))
  const repairedNumericStrings = (await store.loadMany(malformedTarget.accountId, [malformedTarget.keyFingerprint]))[0]?.state
  assert(repairedNumericStrings)
  assert.equal(repairedNumericStrings.observationKind, 'success', 'Lua 与 TS 必须一致拒绝 JSON numeric string 并原子修复')
  assert.notEqual(repairedNumericStrings.generation, 'numeric-string-generation')

  console.log(JSON.stringify({
    message: '账户内 API Key transient Redis 原子 fencing smoke 通过',
    finalGeneration: freshFailure.state?.generation,
    ttlAbaFenced: true,
    staleFailureCounterReset: true,
    malformedStateRepaired: true
  }))
} finally {
  await Promise.all([
    store.deleteManyForTest([target, malformedTarget]),
    abaStore.deleteManyForTest([abaTarget]),
    counterStore.deleteManyForTest([counterTarget])
  ]).catch(() => undefined)
  await closeRedisClients()
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
