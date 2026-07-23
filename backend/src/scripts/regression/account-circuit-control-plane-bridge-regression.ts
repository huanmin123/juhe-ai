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

for (const forbidden of ['scopeKey', 'lease', 'generation', 'revision', 'count', 'ip']) {
  assert.equal(JSON.stringify(publicAccountCircuitSummary([baseIncident])).toLowerCase().includes(forbidden.toLowerCase()), false)
}

console.log(JSON.stringify({ message: '账户 circuit control-plane bridge 回归通过' }))
