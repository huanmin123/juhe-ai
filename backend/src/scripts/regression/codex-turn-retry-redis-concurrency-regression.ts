import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import type { RuntimeStateStore } from '../../shared/runtime-state-store.js'
import {
  orderOpenAIAccountsByCodexTurnAvoidanceAsync,
  clearCodexTurnAccountAvoidanceByFenceAsync,
  rememberCodexTurnStreamFailureAsync,
  setCodexTurnRetryStateStoreForTest
} from '../../modules/gateway/client-profiles/codex-turn-retry.service.js'
import type { OpenAIGatewayClientStrategyContext } from '../../modules/gateway/client-profiles/strategy.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'

async function main(): Promise<void> {
  const originalDriver = runtimeConfig.runtimeStateDriver
  const store = new ContendedRuntimeStateStore(3)
  runtimeConfig.runtimeStateDriver = 'redis'
  setCodexTurnRetryStateStoreForTest(store)
  try {
    const strategy = codexStrategy('concurrent-strong')
    const accountIds = Array.from({ length: 64 }, (_, index) => `acct_${index}`)
    const results = await Promise.all(accountIds.map((accountId, index) => (
      rememberCodexTurnStreamFailureAsync(strategy, accountId, {
        evidence: 'committed_retry_signal',
        observationId: `attempt_${index}`
      })
    )))
    assert(results.every(Boolean), '并发强证据在有界 CAS 内应全部合并')
    const orderedStrong = await orderOpenAIAccountsByCodexTurnAvoidanceAsync(accountIds.map(account), strategy)
    assert.equal(orderedStrong.avoidedAccountIds.length, 64, '每个账户必须保存在独立 persistent scope 后仍能合并排序')
    assert(store.compareSetCalls > 64, '夹具必须制造 CAS 冲突并验证重试')

    const duplicate = await rememberCodexTurnStreamFailureAsync(strategy, 'acct_0', {
      evidence: 'committed_retry_signal',
      observationId: 'attempt_0'
    })
    assert.equal(duplicate?.duplicateObservation, true)
    assert.equal((await orderOpenAIAccountsByCodexTurnAvoidanceAsync(accountIds.map(account), strategy)).avoidedAccountIds.length, 64)

    setCodexTurnRetryStateStoreForTest(store)
    const weakStrategy = codexStrategy('concurrent-weak')
    await Promise.all([
      rememberCodexTurnStreamFailureAsync(weakStrategy, 'acct_a', {
        evidence: 'incomplete_downstream_abort',
        observationId: 'weak_1'
      }),
      rememberCodexTurnStreamFailureAsync(weakStrategy, 'acct_a', {
        evidence: 'incomplete_downstream_abort',
        observationId: 'weak_2'
      })
    ])
    const ordered = await orderOpenAIAccountsByCodexTurnAvoidanceAsync(
      [account('acct_a'), account('acct_b')],
      weakStrategy
    )
    assert.deepEqual(ordered.accounts.map((item) => item.id), ['acct_b', 'acct_a'])

    const fenceStrategy = codexStrategy('fence-monotonic')
    const first = await rememberCodexTurnStreamFailureAsync(fenceStrategy, 'acct_fence', {
      evidence: 'committed_retry_signal', observationId: 'fence_first'
    })
    assert(first?.activation)
    assert.equal(await clearCodexTurnAccountAvoidanceByFenceAsync({
      stateKey: first.stateKey, accountId: 'acct_fence', sourceGeneration: first.activation.sourceGeneration, sourceFenceId: first.activation.sourceFenceId
    }), true, '匹配 fence 必须能清除对应激活')
    const second = await rememberCodexTurnStreamFailureAsync(fenceStrategy, 'acct_fence', {
      evidence: 'committed_retry_signal', observationId: 'fence_second'
    })
    assert(second?.activation)
    assert(second.activation.sourceGeneration > first.activation.sourceGeneration, '清除后再次激活必须使用单调递增 generation')
    assert.equal(await clearCodexTurnAccountAvoidanceByFenceAsync({
      stateKey: first.stateKey, accountId: 'acct_fence', sourceGeneration: first.activation.sourceGeneration, sourceFenceId: first.activation.sourceFenceId
    }), false, '迟到 fence 绝不可清除新激活')

    const exhaustedStore = new ContendedRuntimeStateStore(Number.POSITIVE_INFINITY)
    setCodexTurnRetryStateStoreForTest(exhaustedStore)
    const exhausted = await rememberCodexTurnStreamFailureAsync(codexStrategy('cas-exhausted'), 'acct_a', {
      evidence: 'committed_retry_signal',
      observationId: 'exhausted_1'
    })
    assert.equal(exhausted, undefined, 'CAS 耗尽必须 fail-open')

    console.log('Codex turn Redis 并发回归通过：账户分片合并、observation 幂等、单调 fence、迟到清理隔离、弱阈值和 CAS 耗尽 fail-open 符合预期')
  } finally {
    setCodexTurnRetryStateStoreForTest(undefined)
    runtimeConfig.runtimeStateDriver = originalDriver
  }
}

class ContendedRuntimeStateStore implements RuntimeStateStore {
  private readonly values = new Map<string, unknown>()
  private forcedConflicts: number
  private readonly counters = new Map<string, number>()
  compareSetCalls = 0

  constructor(
    forcedConflicts: number,
    private readonly firstConflictReplacement?: unknown
  ) {
    this.forcedConflicts = forcedConflicts
  }

  value<T>(key: string): T | undefined {
    return clone(this.values.get(key)) as T | undefined
  }

  async getJson<T>(key: string): Promise<T | undefined> {
    await yieldEventLoop()
    return this.value<T>(key)
  }

  async getJsonMany<T>(keys: string[]): Promise<Array<T | undefined>> {
    return Promise.all(keys.map((key) => this.getJson<T>(key)))
  }

  async compareSetJson<T>(key: string, expectedValue: T | undefined, nextValue: T): Promise<boolean> {
    this.compareSetCalls += 1
    await yieldEventLoop()
    if (this.forcedConflicts > 0) {
      this.forcedConflicts -= 1
      if (this.firstConflictReplacement !== undefined && !this.values.has(key)) {
        this.values.set(key, clone(this.firstConflictReplacement))
      }
      return false
    }
    const current = this.values.get(key)
    if (JSON.stringify(current) !== JSON.stringify(expectedValue)) {
      return false
    }
    this.values.set(key, clone(nextValue))
    return true
  }

  async setJson<T>(key: string, value: T): Promise<void> {
    this.values.set(key, clone(value))
  }

  async getDeleteJson<T>(): Promise<T | undefined> { return undefined }
  async compareDeleteJson<T>(): Promise<boolean> { return false }
  async delete(): Promise<void> {}
  async incr(key: string): Promise<number> {
    const next = (this.counters.get(key) ?? 0) + 1
    this.counters.set(key, next)
    return next
  }
  async acquireLock(): Promise<boolean> { return false }
  async renewLock(): Promise<boolean> { return false }
  async releaseLock(): Promise<void> {}
}

function codexStrategy(stateKey: string): OpenAIGatewayClientStrategyContext {
  return {
    clientProfile: 'codex',
    requestClientCompatibility: 'codex_responses',
    downstreamProtocol: 'responses_sse',
    upstreamAdapter: 'openai_mixed',
    codexCompactionExpected: false,
    codexTurn: {
      turnId: stateKey,
      stateKey
    },
    clientSourceAvoidanceStateKey: stateKey,
    retryCoordination: {
      preCommitFailureSignal: 'protocol_error_event',
      committedFailureSignal: 'protocol_error_event'
    },
    allowClientSourceAccountAvoidance: true,
    allowCodexTurnAccountAvoidance: true
  }
}

function account(id: string): UpstreamAccount {
  return { id, name: id, priority: 0 } as UpstreamAccount
}

function clone<T>(value: T): T {
  return value === undefined ? value : structuredClone(value)
}

async function yieldEventLoop(): Promise<void> {
  await new Promise<void>((resolve) => setImmediate(resolve))
}

await main()
