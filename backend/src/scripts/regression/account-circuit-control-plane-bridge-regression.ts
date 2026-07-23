import assert from 'node:assert/strict'

import {
  AccountCircuitControlPlaneBridge,
  publicAccountCircuitSummary
} from '../../modules/gateway/runtime/account-circuit-control-plane-bridge.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import { accountCircuitScopeKey, type AccountCircuitState } from '../../modules/gateway/runtime/account-circuit-store.js'
import type { AccountCircuitIncidentRecord } from '../../storage/account-circuit-control-plane.repository.js'

const scope = {
  kind: 'protocol_model' as const,
  accountRuntimeKey: 'account-1',
  protocolProfile: 'openai-v1',
  requestLane: 'text' as const,
  modelBucket: 'unknown'
}
const store = new MemoryAccountCircuitStore({ capacity: 8, now: () => 10_000 })
const open: AccountCircuitState = {
  scope,
  scopeKey: accountCircuitScopeKey(scope),
  phase: 'OPEN',
  generation: 2,
  dispatchRevision: '7',
  transitionId: 'transition-open',
  backoffAttempt: 1,
  recoverySuccessCount: 0,
  openedAtMs: 9_000,
  retryAtMs: 15_000,
  updatedAtMs: 9_000
}

assert.equal((await store.restore(open, 10_000)).status, 'applied')
assert.equal((await store.get(scope, 10_001)).phase, 'OPEN', '重启恢复后活动电路不能按缺失 key 解释成 CLOSED')
assert.equal((await store.restore({ ...open, generation: 1, updatedAtMs: 8_000 }, 10_002)).status, 'idempotent')
assert.equal((await store.get(scope, 10_003)).generation, 2, '延迟 projector 不得覆盖更新 generation')

const baseIncident: AccountCircuitIncidentRecord = {
  circuitScopeKey: open.scopeKey,
  accountId: 'account-1',
  accountRuntimeKey: 'account-1',
  scopeKind: 'protocol_model',
  protocolCode: 'openai-v1',
  requestLane: 'text',
  modelFamily: 'unknown',
  incidentId: 'incident-1',
  childIncidentIds: [],
  state: 'OPEN',
  generation: 2,
  dispatchRevision: 7,
  ledgerRevision: 2,
  projectedLedgerRevision: 1,
  transitionId: 'transition-open',
  cooldownObservationGeneration: 0,
  nextTransitionAtMs: 15_000,
  upstreamAttemptObserved: true,
  backoffLevel: 1,
  consecutiveFailures: 1,
  recoveringSuccesses: 0,
  lastFailureClass: 'timeout_before_complete',
  createdAtMs: 8_000,
  updatedAtMs: 9_000
}

assert.deepEqual(publicAccountCircuitSummary([]), { status: 'normal' })
assert.deepEqual(publicAccountCircuitSummary([baseIncident]), {
  status: 'avoided',
  reason: 'timeout_before_complete',
  since: new Date(9_000).toISOString(),
  nextCheckAt: new Date(15_000).toISOString()
})
assert.equal(publicAccountCircuitSummary([
  { ...baseIncident, state: 'RECOVERING', updatedAtMs: 12_000 },
  { ...baseIncident, state: 'HALF_OPEN', updatedAtMs: 13_000 }
]).status, 'verifying')

let rebuildLoads = 0
const coldStartBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8, now: () => 10_000 }),
  loadRebuildPage: async () => {
    rebuildLoads++
    await new Promise((resolve) => setTimeout(resolve, 1))
    return { items: [], nextCursor: undefined }
  }
})
const coldStartResults = await Promise.all([
  coldStartBridge.rebuild(),
  coldStartBridge.rebuild(),
  coldStartBridge.rebuild()
])
assert.equal(rebuildLoads, 1, '冷启动并发请求必须共享同一个 rebuild 单飞 promise')
assert.equal(coldStartResults.every((result) => result.blocked === false), true)
assert.equal(coldStartBridge.isReady(), true)

const failedBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  loadRebuildPage: async () => { throw new Error('durable state unavailable') }
})
const failedRebuild = await failedBridge.rebuild()
assert.equal(failedRebuild.blocked, true)
assert.equal(failedBridge.isReady(), false, '持久状态不可用时必须保持 fail-closed gate')
assert.equal(await failedBridge.reconcileActive(), 0)

let durableAvailable = false
const persistedTransitions: string[] = []
const boundedBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  retryDelayMs: 5,
  maxPersistAttempts: 2,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    persistedTransitions.push(input.transitionId)
    if (!durableAvailable) throw new Error('durable state unavailable')
    return { status: 'applied', currentDispatchRevision: input.dispatchRevision }
  }
})
assert.equal((await boundedBridge.rebuild()).blocked, false)
boundedBridge.observe({ scope, state: open })
await waitUntil(() => !boundedBridge.isReady(), '持久化重试耗尽后必须关闭就绪门')

for (let generation = 3; generation <= 102; generation++) {
  boundedBridge.observe({
    scope,
    state: {
      ...open,
      generation,
      transitionId: `transition-${generation}`,
      updatedAtMs: open.updatedAtMs + generation
    }
  })
}
durableAvailable = true
await waitUntil(() => boundedBridge.isReady(), 'DB 恢复后必须自动持久化并重新打开就绪门')
assert.equal(persistedTransitions.length, 3, '持续 DB 故障时每 scope 只能保留一个有限重试批次和最新待写状态')
assert.equal(persistedTransitions.at(-1), 'transition-102', '恢复时必须持久化故障期间的最新状态')

let conflictActive = true
const conflictExpectedRevisions: Array<number | null> = []
const conflictBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  retryDelayMs: 5,
  maxPersistAttempts: 2,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    conflictExpectedRevisions.push(input.expectedLedgerRevision)
    if (conflictActive) {
      return {
        status: 'cas_conflict',
        currentDispatchRevision: input.dispatchRevision,
        incident: { ...baseIncident, generation: 1, ledgerRevision: 11 }
      }
    }
    return { status: 'applied', currentDispatchRevision: input.dispatchRevision }
  }
})
assert.equal((await conflictBridge.rebuild()).blocked, false)
conflictBridge.observe({ scope, state: open })
await waitUntil(() => !conflictBridge.isReady(), '持续 CAS 冲突耗尽批次后必须关闭就绪门')
assert.deepEqual(conflictExpectedRevisions, [null, 11], 'CAS 冲突重试必须使用 ledger 返回的最新 revision')
conflictActive = false
await waitUntil(() => conflictBridge.isReady(), 'CAS 冲突解除后必须自动恢复')
assert.equal(conflictExpectedRevisions.length, 3, 'CAS 冲突期间不得形成无界同步重试')

let staleGenerationCalls = 0
const staleGenerationBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  retryDelayMs: 5,
  maxPersistAttempts: 2,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    staleGenerationCalls++
    return {
      status: 'cas_conflict',
      currentDispatchRevision: input.dispatchRevision,
      incident: { ...baseIncident, generation: input.generation + 1, ledgerRevision: 12 }
    }
  }
})
assert.equal((await staleGenerationBridge.rebuild()).blocked, false)
staleGenerationBridge.observe({ scope, state: open })
await waitUntil(() => staleGenerationCalls === 1, '较新持久 generation 应吸收晚到旧状态')
await new Promise((resolve) => setTimeout(resolve, 20))
assert.equal(staleGenerationCalls, 1, '晚到旧 generation 不得用新 ledger revision 反向覆盖持久状态')
assert.equal(staleGenerationBridge.isReady(), true)

let staleCalls = 0
const staleBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  retryDelayMs: 5,
  maxPersistAttempts: 2,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    staleCalls++
    return { status: 'stale_dispatch_revision', currentDispatchRevision: input.dispatchRevision + 1 }
  }
})
assert.equal((await staleBridge.rebuild()).blocked, false)
staleBridge.observe({ scope, state: open })
await waitUntil(() => staleCalls === 1, 'stale dispatch revision 应立即完成旧状态收口')
await new Promise((resolve) => setTimeout(resolve, 20))
assert.equal(staleCalls, 1, 'stale dispatch revision 不得进入永久重试')
assert.equal(staleBridge.isReady(), true)

for (const forbidden of ['scopeKey', 'lease', 'generation', 'revision', 'count', 'ip']) {
  assert.equal(JSON.stringify(publicAccountCircuitSummary([baseIncident])).toLowerCase().includes(forbidden.toLowerCase()), false)
}

console.log(JSON.stringify({ message: '账户 circuit control-plane bridge 回归通过' }))

async function waitUntil(predicate: () => boolean, message: string): Promise<void> {
  const deadline = Date.now() + 2_000
  while (!predicate()) {
    if (Date.now() >= deadline) assert.fail(message)
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}
