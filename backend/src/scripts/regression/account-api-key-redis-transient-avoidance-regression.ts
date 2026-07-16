import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import type { RuntimeStateStore } from '../../shared/runtime-state-store.js'
import type { OpenAIAccountSecret } from '../../storage/openai-account-selector.types.js'

class RegressionRuntimeStateStore implements RuntimeStateStore {
  private readonly values = new Map<string, unknown>()
  getJsonCalls = 0

  async getJson<T>(key: string): Promise<T | undefined> {
    this.getJsonCalls += 1
    return this.values.get(key) as T | undefined
  }
  async getJsonMany<T>(keys: string[]): Promise<Array<T | undefined>> {
    this.getJsonCalls += 1
    return keys.map((key) => this.values.get(key) as T | undefined)
  }
  async getDeleteJson<T>(key: string): Promise<T | undefined> {
    const value = this.values.get(key) as T | undefined
    this.values.delete(key)
    return value
  }
  async setJson<T>(key: string, value: T): Promise<void> { this.values.set(key, value) }
  async compareSetJson<T>(): Promise<boolean> { return false }
  async compareDeleteJson<T>(): Promise<boolean> { return false }
  async delete(key: string): Promise<void> { this.values.delete(key) }
  async incr(key: string, options: { max?: number }): Promise<number> {
    const current = Number(this.values.get(key) ?? 0)
    const next = current + 1
    if (options.max === undefined || next <= options.max) this.values.set(key, next)
    return next
  }
  async acquireLock(): Promise<boolean> { return false }
  async releaseLock(): Promise<void> {}
}

runtimeConfig.runtimeStateDriver = 'redis'

const guard = await import('../../modules/gateway/runtime/account-api-key-failure-guard.service.js')
const store = new RegressionRuntimeStateStore()
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

  assert.equal(await guard.recordGatewayAccountApiKeyTransientFailure(account, {
    status: 'temporary_unavailable'
  }), true)
  assert.deepEqual(
    (await guard.loadGatewayAccountApiKeyTransientStatesForDispatch(
      account.id,
      ['fingerprint_a', 'fingerprint_b']
    )).map((state) => state.keyFingerprint),
    ['fingerprint_a'],
    'Redis transient avoidance 必须按完整 fingerprint 隔离'
  )
  assert.equal(store.getJsonCalls, 1, '单账户 Key 池 transient avoidance 必须批量读取，不能按 Key 产生 Redis N+1')

  assert.equal(await guard.clearGatewayAccountApiKeyTransientFailure(account), true)
  assert.equal(
    (await guard.loadGatewayAccountApiKeyTransientStatesForDispatch(account.id, ['fingerprint_a'])).length,
    0,
    '成功观察应清理 fingerprint transient avoidance'
  )

  console.log('账户内 API Key Redis transient avoidance 回归通过：网关失败不持久化 DB，短态按 fingerprint 写入、读取和清理')
} finally {
  guard.setGatewayAccountApiKeyTransientStateStoreForTest(undefined)
  runtimeConfig.runtimeStateDriver = 'memory'
}
