import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { selectAccountApiKeyForDispatch } from '../../modules/gateway/dispatch/account-preparation.js'
import {
  redisAccountApiKeyTransientLoadScript,
  redisAccountApiKeyTransientMutationScript,
  type AccountApiKeyTransientMutationResult,
  type AccountApiKeyTransientDispatchState,
  type AccountApiKeyTransientState,
  type AccountApiKeyTransientStateStore,
  type AccountApiKeyTransientTarget
} from '../../modules/gateway/runtime/account-api-key-transient-redis-store.js'
import type { OpenAIAccountSecret } from '../../storage/openai-account-selector.types.js'
import { accountApiKeyEntries } from '../../storage/account-api-key-rotation.js'

class RegressionAccountApiKeyTransientStore implements AccountApiKeyTransientStateStore {
  private readonly values = new Map<string, AccountApiKeyTransientState>()
  private generationSequence = 0
  getJsonCalls = 0

  async recordFailure(input: {
    target: AccountApiKeyTransientTarget
    status: 'temporary_unavailable' | 'rate_limited' | 'error'
    expectedGeneration: string
  }): Promise<AccountApiKeyTransientMutationResult> {
    const key = stateKey(input.target)
    const current = this.values.get(key)
    if (!current) return { applied: false, reason: 'missing_state' }
    const currentGeneration = current.generation
    if (input.expectedGeneration !== currentGeneration) return { applied: false, reason: 'stale_generation', state: current }
    const failureCount = current?.observationKind === 'failure'
      ? Math.min(3, current.failureCount + 1)
      : 1
    const delayMs = [3_000, 5_000, 10_000][failureCount - 1] ?? 10_000
    const state: AccountApiKeyTransientState = {
      schemaVersion: 1,
      ...input.target,
      generation: currentGeneration,
      lastObservedAtMs: Date.now(),
      observationKind: 'failure',
      failureCount,
      status: input.status,
      suppressUntilMs: Date.now() + delayMs
    }
    this.values.set(key, state)
    return { applied: true, reason: 'applied', state }
  }

  async recordSuccess(input: {
    target: AccountApiKeyTransientTarget
    expectedGeneration: string
  }): Promise<AccountApiKeyTransientMutationResult> {
    const key = stateKey(input.target)
    const current = this.values.get(key)
    if (!current) return { applied: false, reason: 'missing_state' }
    const currentGeneration = current.generation
    if (input.expectedGeneration !== currentGeneration) return { applied: false, reason: 'stale_generation', state: current }
    const state: AccountApiKeyTransientState = {
      schemaVersion: 1,
      ...input.target,
      generation: this.nextGeneration(),
      lastObservedAtMs: Date.now(),
      observationKind: 'success',
      failureCount: 0
    }
    this.values.set(key, state)
    return { applied: true, reason: 'applied', state }
  }

  async loadMany(accountId: string, keyFingerprints: string[]): Promise<AccountApiKeyTransientDispatchState[]> {
    this.getJsonCalls += 1
    return keyFingerprints
      .map((keyFingerprint) => {
        const key = stateKey({ accountId, keyFingerprint })
        const current = this.values.get(key)
        if (current) return current
        const state: AccountApiKeyTransientState = {
          schemaVersion: 1,
          accountId,
          keyFingerprint,
          generation: this.nextGeneration(),
          lastObservedAtMs: Date.now(),
          observationKind: 'success',
          failureCount: 0
        }
        this.values.set(key, state)
        return state
      })
      .map((state) => ({
        state,
        suppressed: state.observationKind === 'failure'
          && state.suppressUntilMs !== undefined
          && state.suppressUntilMs > Date.now()
      }))
  }

  state(target: AccountApiKeyTransientTarget): AccountApiKeyTransientState | undefined {
    return this.values.get(stateKey(target))
  }

  private nextGeneration(): string {
    this.generationSequence += 1
    return `generation-${this.generationSequence}`
  }
}

runtimeConfig.runtimeStateDriver = 'redis'

const guard = await import('../../modules/gateway/runtime/account-api-key-failure-guard.service.js')
const store = new RegressionAccountApiKeyTransientStore()
guard.setGatewayAccountApiKeyTransientStateStoreForTest(store)

const account = {
  id: 'account_redis_transient',
  credentialSourceAccountId: 'account_redis_transient',
  selectedApiKeyFingerprint: 'fingerprint_a',
  selectedApiKeyIndex: 0
} as OpenAIAccountSecret

try {
  const decision = guard.recordGatewayAccountApiKeyFailureGuard(account, {
    status: 'temporary_unavailable',
    trafficSource: 'gateway',
    source: 'redis_transient_regression'
  })
  assert.equal(decision.persist, false, 'Redis 模式真实网关失败不得请求持久化 DB Key runtime row')
  assert.equal(decision.reason, 'redis_transient_only')

  const initialDispatchState = (await guard.loadGatewayAccountApiKeyTransientStatesForDispatch(
    account.id,
    ['fingerprint_a']
  ))[0]
  assert(initialDispatchState?.transientGeneration)
  const initialDispatchAccount = {
    ...account,
    selectedApiKeyTransientGeneration: initialDispatchState.transientGeneration
  }
  assert.equal(await guard.recordGatewayAccountApiKeyTransientFailure(initialDispatchAccount, {
    status: 'temporary_unavailable'
  }), true)
  const firstGeneration = store.state({ accountId: account.id, keyFingerprint: 'fingerprint_a' })?.generation
  assert(firstGeneration)
  assert.deepEqual(
    (await guard.loadGatewayAccountApiKeyTransientStatesForDispatch(
      account.id,
      ['fingerprint_a', 'fingerprint_b']
    )).filter((state) => state.status !== 'active').map((state) => state.keyFingerprint),
    ['fingerprint_a'],
    'Redis transient avoidance 必须按完整 fingerprint 隔离'
  )
  assert.equal(store.getJsonCalls, 2, '无调度 generation 的直接写先原子初始化一次，显式 dispatch 读取仍必须按账户批量完成')

  assert.equal(await guard.clearGatewayAccountApiKeyTransientFailure(initialDispatchAccount), true)
  assert.equal(store.state({ accountId: account.id, keyFingerprint: 'fingerprint_a' })?.observationKind, 'success', '成功必须写 tombstone，不能用 DELETE 丢失 fencing 证据')
  const successGeneration = store.state({ accountId: account.id, keyFingerprint: 'fingerprint_a' })?.generation
  assert(successGeneration && successGeneration !== firstGeneration, 'success tombstone 必须换成不可复用 generation')
  assert.deepEqual(
    await guard.loadGatewayAccountApiKeyTransientStatesForDispatch(account.id, ['fingerprint_a']),
    [{
      keyFingerprint: 'fingerprint_a',
      keyIndex: 0,
      status: 'active',
      transientGeneration: successGeneration
    }],
    '成功观察应保留 active tombstone generation，同时清理 fingerprint transient avoidance'
  )

  const staleAccount = { ...account, selectedApiKeyTransientGeneration: firstGeneration }
  assert.equal(await guard.recordGatewayAccountApiKeyTransientFailure(staleAccount, {
    status: 'error'
  }), false, '迟到的旧 failure 不得跨过 success tombstone 复活避让')
  assert.equal(
    (await guard.loadGatewayAccountApiKeyTransientStatesForDispatch(account.id, ['fingerprint_a']))[0]?.status,
    'active',
    '迟到 failure 后成功 tombstone 仍应保持可调度'
  )

  const currentGenerationAccount = { ...account, selectedApiKeyTransientGeneration: successGeneration }
  assert.equal(await guard.recordGatewayAccountApiKeyTransientFailure(currentGenerationAccount, {
    status: 'temporary_unavailable'
  }), true, 'success 之后真正更新的 failure 仍应生效')
  assert.equal(await guard.clearGatewayAccountApiKeyTransientFailure(currentGenerationAccount), true, '同 generation success 必须覆盖 failure')
  assert.equal(await guard.recordGatewayAccountApiKeyTransientFailure(currentGenerationAccount, {
    status: 'temporary_unavailable'
  }), false, 'success tombstone 之后的旧 generation failure 必须被 fence')
  assert.equal(store.state({ accountId: account.id, keyFingerprint: 'fingerprint_a' })?.observationKind, 'success')

  assert.match(redisAccountApiKeyTransientMutationScript, /expected_generation ~= current_generation/, 'Lua failure/success 必须使用调度快照 generation fencing')
  assert.match(redisAccountApiKeyTransientMutationScript, /local redis_time = redis\.call\('TIME'\)/, 'Lua 抑制截止时间必须使用 Redis 服务端时钟')
  assert.match(redisAccountApiKeyTransientMutationScript, /observationKind = operation[\s\S]*redis\.call\('SET'/, 'Lua success 必须原子写 tombstone，不能双 DELETE')
  assert.match(redisAccountApiKeyTransientLoadScript, /local redis_time = redis\.call\('TIME'\)[\s\S]*redis\.call\('GET', key\)[\s\S]*if not state then[\s\S]*redis\.call\('SET'/, 'Redis 读取、generation 初始化与坏值修复必须在同一 Lua 中完成，不能迟到删除新状态')
  assert.equal((redisAccountApiKeyTransientLoadScript.match(/redis\.call\('SET'/g) ?? []).length, 1, '已有 generation 的 dispatch load 必须只读，不能按 QPS x Key 数续写 Redis')

  const dispatchCandidate = {
    ...account,
    providerCode: 'gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    type: 'api_key',
    apiKey: 'sk-generation-dispatch-a',
    apiKeys: ['sk-generation-dispatch-a', 'sk-generation-dispatch-b'],
    selectedApiKeyFingerprint: undefined,
    selectedApiKeyIndex: undefined,
    selectedApiKeyTransientGeneration: undefined,
    credentials: {
      api_key: 'sk-generation-dispatch-a',
      api_keys: ['sk-generation-dispatch-a', 'sk-generation-dispatch-b'],
      api_key_strategy: 'round_robin'
    }
  } as OpenAIAccountSecret
  const dispatchEntries = accountApiKeyEntries(dispatchCandidate.credentials)
  const dispatchStates = await store.loadMany(dispatchCandidate.id, dispatchEntries.map((entry) => entry.fingerprint))
  const blockedEntry = dispatchEntries[1]
  const blockedState = dispatchStates.find((item) => item.state.keyFingerprint === blockedEntry?.fingerprint)
  assert(blockedEntry && blockedState)
  await store.recordFailure({
    target: {
      accountId: dispatchCandidate.id,
      keyFingerprint: blockedEntry.fingerprint,
      keyIndex: blockedEntry.index
    },
    status: 'temporary_unavailable',
    expectedGeneration: blockedState.state.generation
  })
  const preparedDispatch = await selectAccountApiKeyForDispatch(dispatchCandidate)
  assert(preparedDispatch?.selectedApiKeyFingerprint)
  assert(preparedDispatch.selectedApiKeyTransientGeneration, 'Redis dispatch 必须把所选 Key 的 generation 附着到上游 attempt')

  console.log('账户内 API Key Redis transient avoidance 回归通过：单键 generation/tombstone 原子 fencing 阻止迟到失败复活')
} finally {
  guard.setGatewayAccountApiKeyTransientStateStoreForTest(undefined)
  runtimeConfig.runtimeStateDriver = 'memory'
}

function stateKey(target: Pick<AccountApiKeyTransientTarget, 'accountId' | 'keyFingerprint'>): string {
  return `${target.accountId}\u0000${target.keyFingerprint}`
}
