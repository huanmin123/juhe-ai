import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import type { AccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-store.js'
import {
  GatewayAccountCircuitService,
  accountCircuitDispatchRevision,
  gatewayAccountProtocolModelScope,
  getGatewayAccountCircuitStore,
  resetGatewayAccountCircuitStoreForTest
} from '../../modules/gateway/runtime/account-circuit.service.js'

const account = accountFixture()
let now = 1_000
let id = 0
const store = new MemoryAccountCircuitStore({ capacity: 32, now: () => now })
const service = new GatewayAccountCircuitService(store, {
  now: () => now,
  createId: () => `id-${++id}`
})

const scope = gatewayAccountProtocolModelScope(account, 'text', 'gpt-5.4')
assert.equal(scope.accountRuntimeKey, account.id)
assert.equal(scope.protocolProfile, account.providerProtocolProfileId)
assert.equal(scope.requestLane, 'text')
assert.equal(scope.modelBucket, 'gpt-5.4')
assert.equal(gatewayAccountProtocolModelScope(account, 'text', 'arbitrary-user-model-a').modelBucket, 'unknown')
assert.equal(gatewayAccountProtocolModelScope(account, 'text', 'arbitrary-user-model-b').modelBucket, 'unknown')

const dispatchRevision = accountCircuitDispatchRevision(account)
assert.ok(dispatchRevision.startsWith('v1:'))
assert.doesNotMatch(dispatchRevision, /secret-api-key|refresh-secret/)
assert.notEqual(dispatchRevision, accountCircuitDispatchRevision({ ...account, apiKey: 'changed-secret' }))

const initialAttempts = await Promise.all(Array.from({ length: 20 }, () => service.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})))
assert.equal(initialAttempts.filter((result) => result.outcome === 'dispatchable').length, 20)
const firstInitialAttempt = initialAttempts[0]
assert.equal(firstInitialAttempt?.outcome, 'dispatchable')
if (firstInitialAttempt?.outcome !== 'dispatchable') throw new Error('缺少普通完整响应 attempt')
await firstInitialAttempt.attempt.reportFramingComplete()
assert.equal((await store.get(scope)).phase, 'CLOSED', '普通完整 HTTP framing 不得触发账户电路')

const transportDecisions = await Promise.all(initialAttempts.map(async (result, index) => {
  assert.equal(result.outcome, 'dispatchable')
  return result.attempt.reportTransportFailure({
    kind: 'transport',
    reason: `并发传输失败 ${index}`
  })
}))
const confirmations = transportDecisions.filter((decision) => decision.outcome === 'confirmation_acquired')
assert.equal(confirmations.length, 1, '同 generation 只能有一个 confirmation 租约赢家')

const blockedWhileSuspect = await Promise.all(Array.from({ length: 12 }, () => service.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})))
assert.equal(blockedWhileSuspect.filter((result) => result.outcome === 'dispatchable').length, 0)
assert.ok(blockedWhileSuspect.every((result) => result.outcome === 'blocked' && result.state.phase === 'SUSPECT'))

const confirmation = confirmations[0]
assert.equal(confirmation?.outcome, 'confirmation_acquired')
if (confirmation?.outcome !== 'confirmation_acquired') throw new Error('缺少 confirmation 租约')
const confirmationAttempt = await service.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmation: confirmation.confirmation
})
assert.equal(confirmationAttempt.outcome, 'dispatchable')
if (confirmationAttempt.outcome !== 'dispatchable') throw new Error('confirmation 租约无法派发')
assert.equal(confirmationAttempt.attempt.isConfirmation, true)
await confirmationAttempt.attempt.reportTransportFailure({ kind: 'timeout', reason: 'confirmation 超时' })
assert.equal((await store.get(scope)).phase, 'OPEN')

const otherLane = await service.prepareAttempt({
  account,
  requestLane: 'image',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})
assert.equal(otherLane.outcome, 'dispatchable', 'protocol-model 电路必须按 lane 隔离')

const recoveryAccount = accountFixture({ id: 'account-framing-complete' })
const recoveryAttempt = await service.prepareAttempt({
  account: recoveryAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})
assert.equal(recoveryAttempt.outcome, 'dispatchable')
if (recoveryAttempt.outcome !== 'dispatchable') throw new Error('初始 attempt 不可派发')
const recoveryDecision = await recoveryAttempt.attempt.reportTransportFailure({ kind: 'read_incomplete', reason: '正文读取中断' })
assert.equal(recoveryDecision.outcome, 'confirmation_acquired')
if (recoveryDecision.outcome !== 'confirmation_acquired') throw new Error('未取得 recovery confirmation')
const recoveryConfirmation = await service.prepareAttempt({
  account: recoveryAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmation: recoveryDecision.confirmation
})
assert.equal(recoveryConfirmation.outcome, 'dispatchable')
if (recoveryConfirmation.outcome !== 'dispatchable') throw new Error('recovery confirmation 不可派发')
await recoveryConfirmation.attempt.reportFramingComplete()
assert.equal((await store.get(gatewayAccountProtocolModelScope(recoveryAccount, 'text', 'gpt-5.4'))).phase, 'CLOSED')

const failingStore = {
  ...store,
  get: async () => { throw new Error('redis unavailable') }
}
const failingService = new GatewayAccountCircuitService(failingStore as unknown as AccountCircuitStore)
await assert.rejects(() => failingService.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
}), /redis unavailable/, '运行态失败不得伪装成 CLOSED')

const previousMode = runtimeConfig.runtimeMode
const previousDriver = runtimeConfig.runtimeStateDriver
const previousStateUrl = runtimeConfig.redis.stateUrl
try {
  resetGatewayAccountCircuitStoreForTest()
  runtimeConfig.runtimeMode = 'standalone'
  runtimeConfig.runtimeStateDriver = 'memory'
  const firstMemoryStore = getGatewayAccountCircuitStore()
  const secondMemoryStore = getGatewayAccountCircuitStore()
  assert.equal(firstMemoryStore, secondMemoryStore, 'standalone 必须复用进程单例 memory store')

  resetGatewayAccountCircuitStoreForTest()
  runtimeConfig.runtimeMode = 'performance'
  runtimeConfig.runtimeStateDriver = 'redis'
  runtimeConfig.redis.stateUrl = undefined
  assert.throws(() => getGatewayAccountCircuitStore(), /JUHE_AI_REDIS_STATE_URL/, 'performance 缺少 Redis state URL 必须显式失败')
} finally {
  runtimeConfig.runtimeMode = previousMode
  runtimeConfig.runtimeStateDriver = previousDriver
  runtimeConfig.redis.stateUrl = previousStateUrl
  resetGatewayAccountCircuitStoreForTest()
}

const upstreamDispatchSource = readFileSync(fileURLToPath(new URL('../../modules/gateway/dispatch/upstream-dispatch.ts', import.meta.url)), 'utf8')
const routesSource = readFileSync(fileURLToPath(new URL('../../modules/gateway/routes.ts', import.meta.url)), 'utf8')
assert.match(upstreamDispatchSource, /getGatewayAccountCircuitService/, '上游候选派发必须接入账户短电路 service')
assert.match(upstreamDispatchSource, /retrySameAccount:\s*false/, '普通 same-account retry 不得参与短电路路径')
assert.doesNotMatch(upstreamDispatchSource, /function shouldRetrySameAccountAfterFailure/, '旧普通同号重试函数必须移除')
assert.doesNotMatch(upstreamDispatchSource, /SameAccountRetryBudget|createSameAccountRetryBudget|sameAccountRetryBudget/, '非租约同号重试预算必须移除')
assert.doesNotMatch(upstreamDispatchSource, /OpaqueFailoverBudget|maxOpaqueFailoverAccountsPerRequest|opaqueFailoverBudget/, '普通候选不得保留固定四账户预算')
assert.match(routesSource, /transportFailure[\s\S]*reportTransportFailure/, '响应读取未完成必须回报账户短电路')

console.log('gateway account circuit dispatch regression passed')

function accountFixture(overrides: Partial<UpstreamAccount> = {}): UpstreamAccount {
  return {
    id: 'account-circuit-a',
    providerCode: 'openai',
    providerProtocolProfileId: 'profile_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    systemAccountId: 'system-a',
    accountOwnerSystemAccountId: 'system-a',
    groupOwnerSystemAccountId: 'system-a',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: 'Circuit A',
    type: 'openai_api_key',
    status: 'active',
    concurrencyLimit: 4,
    priority: 1,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedModels: ['gpt-5.4'],
    modelMappings: [],
    healthCheckModel: 'gpt-5.4',
    healthCheckEndpointMode: 'responses_json',
    baseUrl: 'https://api.example.com/v1',
    apiKey: 'secret-api-key',
    apiKeys: ['secret-api-key'],
    refreshToken: 'refresh-secret',
    streamFailureCount: 0,
    credentials: { api_key: 'secret-api-key', refresh_token: 'refresh-secret' },
    ...overrides
  }
}
