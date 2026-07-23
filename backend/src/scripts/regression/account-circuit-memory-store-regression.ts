import assert from 'node:assert/strict'

import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import {
  accountCircuitBackoffDelayMs,
  accountCircuitScopeKey,
  type AccountCircuitScope
} from '../../modules/gateway/runtime/account-circuit-store.js'

let now = 10_000
const store = new MemoryAccountCircuitStore({ capacity: 4, closedRetentionMs: 100, now: () => now })
const accountScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'acct:authorized:user:group:grant' }
const keyScope: AccountCircuitScope = { kind: 'key', accountRuntimeKey: 'acct', keyFingerprint: 'key-fp' }
const modelScope: AccountCircuitScope = {
  kind: 'protocol_model',
  accountRuntimeKey: 'acct',
  protocolProfile: 'profile_openai_v1',
  requestLane: 'text',
  modelBucket: 'gpt-5'
}

assert.notEqual(accountCircuitScopeKey(accountScope), accountCircuitScopeKey(keyScope), '账户、Key 与协议模型作用域必须隔离')
assert.notEqual(accountCircuitScopeKey(keyScope), accountCircuitScopeKey(modelScope), 'Key 与协议模型作用域不得碰撞')
assert.deepEqual([1, 2, 3, 4, 5, 6].map(accountCircuitBackoffDelayMs), [3_000, 5_000, 10_000, 30_000, 60_000, 60_000])
assert.equal((await store.get(accountScope)).phase, 'CLOSED', '缺失状态必须按 CLOSED 读取')

const suspected = await store.suspect({
  scope: accountScope,
  dispatchRevision: 'rev-1',
  transitionId: 'suspect-1',
  reason: 'connect timeout',
  nowMs: now
})
assert.equal(suspected.status, 'applied')
assert.equal(suspected.state.phase, 'SUSPECT')
assert.equal(suspected.state.generation, 1)
assert.equal((await store.suspect({
  scope: accountScope,
  dispatchRevision: 'rev-1',
  transitionId: 'suspect-1',
  reason: 'duplicate',
  nowMs: now
})).status, 'idempotent', '重复 transitionId 必须幂等')

const confirmationIdentity = {
  scope: accountScope,
  generation: 1,
  dispatchRevision: 'rev-1'
}
const [confirmationA, confirmationB] = await Promise.all([
  store.acquireConfirmationLease({
    ...confirmationIdentity,
    transitionId: 'confirmation-acquire-a',
    leaseId: 'confirmation-a',
    leaseUntilMs: now + 1_000,
    nowMs: now
  }),
  store.acquireConfirmationLease({
    ...confirmationIdentity,
    transitionId: 'confirmation-acquire-b',
    leaseId: 'confirmation-b',
    leaseUntilMs: now + 1_000,
    nowMs: now
  })
])
assert.equal([confirmationA, confirmationB].filter((item) => item.status === 'applied').length, 1, '同 generation confirmation 只能单飞')
const confirmationLeaseId = confirmationA.status === 'applied' ? 'confirmation-a' : 'confirmation-b'
assert.equal((await store.completeConfirmation({
  ...confirmationIdentity,
  transitionId: 'confirmation-stale-revision',
  leaseId: confirmationLeaseId,
  outcome: 'transport_failure',
  dispatchRevision: 'rev-old',
  nowMs: now
})).status, 'stale_dispatch_revision')
assert.equal((await store.completeConfirmation({
  ...confirmationIdentity,
  transitionId: 'confirmation-stale-generation',
  leaseId: confirmationLeaseId,
  outcome: 'transport_failure',
  generation: 0,
  nowMs: now
})).status, 'stale_generation')
assert.equal((await store.completeConfirmation({
  ...confirmationIdentity,
  transitionId: 'confirmation-wrong-lease',
  leaseId: 'wrong',
  outcome: 'transport_failure',
  nowMs: now
})).status, 'lease_mismatch')

const opened = await store.completeConfirmation({
  ...confirmationIdentity,
  transitionId: 'confirmation-failed',
  leaseId: confirmationLeaseId,
  outcome: 'transport_failure',
  reason: 'timeout confirmed',
  nowMs: now
})
assert.equal(opened.state.phase, 'OPEN')
assert.equal(opened.state.retryAtMs, now + 3_000)
assert.equal((await store.acquireCanaryLease({
  ...confirmationIdentity,
  transitionId: 'canary-too-early',
  leaseId: 'canary-early',
  leaseUntilMs: now + 4_000,
  nowMs: now
})).status, 'not_due')

now += 3_000
const canary1 = await store.acquireCanaryLease({
  ...confirmationIdentity,
  transitionId: 'half-open-acquire',
  leaseId: 'half-open-lease',
  leaseUntilMs: now + 1_000,
  nowMs: now
})
assert.equal(canary1.state.phase, 'HALF_OPEN')
const recovery1 = await store.completeCanary({
  ...confirmationIdentity,
  transitionId: 'half-open-success',
  leaseId: 'half-open-lease',
  outcome: 'framing_complete',
  nowMs: now
})
assert.equal(recovery1.state.phase, 'RECOVERING')
assert.equal(recovery1.state.recoverySuccessCount, 0, '首次 half-open 成功只进入 RECOVERING，不计入三次 canary')
assert.equal((await store.completeCanary({
  ...confirmationIdentity,
  transitionId: 'half-open-success',
  leaseId: 'half-open-lease',
  outcome: 'framing_complete',
  nowMs: now
})).status, 'idempotent', '重复 canary 结果不得重复增加恢复计数')

for (const index of [1, 2, 3]) {
  now += 3_000
  const acquired = await store.acquireCanaryLease({
    ...confirmationIdentity,
    transitionId: `canary-acquire-${index}`,
    leaseId: `canary-${index}`,
    leaseUntilMs: now + 1_000,
    nowMs: now
  })
  assert.equal(acquired.status, 'applied')
  const completed = await store.completeCanary({
    ...confirmationIdentity,
    transitionId: `canary-success-${index}`,
    leaseId: `canary-${index}`,
    outcome: 'framing_complete',
    nowMs: now
  })
  assert.equal(completed.state.phase, index === 3 ? 'CLOSED' : 'RECOVERING')
}

const revised = await store.replaceDispatchRevision({
  scope: accountScope,
  dispatchRevision: 'rev-2',
  transitionId: 'revision-2',
  nowMs: now
})
assert.equal(revised.state.phase, 'CLOSED')
assert.equal(revised.state.generation, 2, '配置 revision 切换必须推进 generation fencing')
assert.equal((await store.suspect({
  scope: accountScope,
  dispatchRevision: 'rev-2',
  transitionId: 'suspect-2',
  reason: 'new failure',
  nowMs: now
})).state.generation, 3)

const expiringScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'expiring' }
await store.suspect({ scope: expiringScope, dispatchRevision: 'r1', transitionId: 'exp-suspect', reason: 'timeout', nowMs: now })
await store.acquireConfirmationLease({
  scope: expiringScope,
  generation: 1,
  dispatchRevision: 'r1',
  transitionId: 'exp-confirm-acquire',
  leaseId: 'exp-confirm',
  leaseUntilMs: now + 10,
  nowMs: now
})
now += 11
assert.equal((await store.get(expiringScope, now)).phase, 'SUSPECT', 'confirmation 租约过期必须保守回到 SUSPECT')
assert.equal((await store.get(expiringScope, now)).lease, undefined)

await store.acquireConfirmationLease({
  scope: expiringScope,
  generation: 1,
  dispatchRevision: 'r1',
  transitionId: 'exp-confirm-acquire-2',
  leaseId: 'exp-confirm-2',
  leaseUntilMs: now + 10,
  nowMs: now
})
await store.completeConfirmation({
  scope: expiringScope,
  generation: 1,
  dispatchRevision: 'r1',
  transitionId: 'exp-open',
  leaseId: 'exp-confirm-2',
  outcome: 'transport_failure',
  nowMs: now
})
now += 3_000
await store.acquireCanaryLease({
  scope: expiringScope,
  generation: 1,
  dispatchRevision: 'r1',
  transitionId: 'exp-canary-acquire',
  leaseId: 'exp-canary',
  leaseUntilMs: now + 10,
  nowMs: now
})
now += 11
assert.equal((await store.get(expiringScope, now)).phase, 'OPEN', 'HALF_OPEN 租约过期不得静默关闭电路')

const capacityStore = new MemoryAccountCircuitStore({ capacity: 2, closedRetentionMs: 100, now: () => now })
const capacityA: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'capacity-a' }
const capacityB: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'capacity-b' }
const capacityC: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'capacity-c' }
await capacityStore.suspect({ scope: capacityA, dispatchRevision: 'r', transitionId: 'a', reason: 'a', nowMs: now })
await capacityStore.suspect({ scope: capacityB, dispatchRevision: 'r', transitionId: 'b', reason: 'b', nowMs: now })
assert.equal((await capacityStore.suspect({
  scope: capacityC,
  dispatchRevision: 'r',
  transitionId: 'c',
  reason: 'c',
  nowMs: now
})).status, 'capacity_exhausted', '容量满时不得淘汰活动电路')
assert.equal((await capacityStore.get(capacityA)).phase, 'SUSPECT')
assert.equal((await capacityStore.get(capacityB)).phase, 'SUSPECT')

const hierarchyStore = new MemoryAccountCircuitStore({ capacity: 10, now: () => now })
const hierarchyAccount: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'hierarchy-account' }
const hierarchyChildren: Array<Extract<AccountCircuitScope, { kind: 'protocol_model' }>> = [
  { kind: 'protocol_model', accountRuntimeKey: 'hierarchy-account', protocolProfile: 'profile', requestLane: 'text', modelBucket: 'model-a' },
  { kind: 'protocol_model', accountRuntimeKey: 'hierarchy-account', protocolProfile: 'profile', requestLane: 'text', modelBucket: 'model-b' }
]
for (const [index, childScope] of hierarchyChildren.entries()) {
  await hierarchyStore.suspect({ scope: childScope, dispatchRevision: 'h1', transitionId: `hierarchy-suspect-${index}`, reason: 'transport', nowMs: now })
  await hierarchyStore.acquireConfirmationLease({ scope: childScope, generation: 1, dispatchRevision: 'h1', transitionId: `hierarchy-acquire-${index}`, leaseId: `hierarchy-lease-${index}`, leaseUntilMs: now + 1_000, nowMs: now })
  await hierarchyStore.completeConfirmation({ scope: childScope, generation: 1, dispatchRevision: 'h1', transitionId: `hierarchy-open-${index}`, leaseId: `hierarchy-lease-${index}`, outcome: 'transport_failure', nowMs: now })
}
const hierarchyEvidenceA = await hierarchyStore.recordProtocolModelOpenEvidence({
  scope: hierarchyChildren[0]!, generation: 1, dispatchRevision: 'h1', evidenceId: 'hierarchy-evidence-a', accountTransitionId: 'hierarchy-account-open', reason: 'multiple protocol failures', confirmedFailureCount: 2, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
})
assert.equal(hierarchyEvidenceA.status, 'recorded')
const hierarchyEvidenceB = await hierarchyStore.recordProtocolModelOpenEvidence({
  scope: hierarchyChildren[1]!, generation: 1, dispatchRevision: 'h1', evidenceId: 'hierarchy-evidence-b', accountTransitionId: 'hierarchy-account-open', reason: 'multiple protocol failures', confirmedFailureCount: 2, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
})
assert.equal(hierarchyEvidenceB.status, 'escalated')
assert.equal((await hierarchyStore.get(hierarchyAccount)).phase, 'OPEN')
assert.equal((await hierarchyStore.get(hierarchyChildren[0]!)).shadowedByIncidentId, 'hierarchy-account-open')
assert.equal((await hierarchyStore.get(hierarchyChildren[1]!)).shadowedByIncidentId, 'hierarchy-account-open')
now += 3_000
const hierarchyAccountLease = await hierarchyStore.acquireCanaryLease({ scope: hierarchyAccount, generation: 1, dispatchRevision: 'h1', transitionId: 'hierarchy-half-open', leaseId: 'hierarchy-half-open-lease', leaseUntilMs: now + 1_000, nowMs: now })
assert.equal(hierarchyAccountLease.status, 'applied')
const hierarchyFirst = await hierarchyStore.completeCanary({ scope: hierarchyAccount, generation: 1, dispatchRevision: 'h1', transitionId: 'hierarchy-half-open-complete', leaseId: 'hierarchy-half-open-lease', outcome: 'framing_complete', nowMs: now })
assert.equal(hierarchyFirst.state.recoverySuccessCount, 0)
for (const [index, evidenceScopeKey] of [accountCircuitScopeKey(hierarchyChildren[0]!), accountCircuitScopeKey(hierarchyChildren[1]!), accountCircuitScopeKey(hierarchyChildren[0]!)].entries()) {
  now += 3_000
  const lease = await hierarchyStore.acquireCanaryLease({ scope: hierarchyAccount, generation: 1, dispatchRevision: 'h1', transitionId: `hierarchy-canary-acquire-${index}`, leaseId: `hierarchy-canary-${index}`, leaseUntilMs: now + 1_000, nowMs: now })
  assert.equal(lease.status, 'applied')
  const completed = await hierarchyStore.completeCanary({ scope: hierarchyAccount, generation: 1, dispatchRevision: 'h1', transitionId: `hierarchy-canary-complete-${index}`, leaseId: `hierarchy-canary-${index}`, outcome: 'framing_complete', evidenceScopeKey, nowMs: now })
  assert.equal(completed.state.phase, index === 2 ? 'CLOSED' : 'RECOVERING')
}
assert.equal((await hierarchyStore.get(hierarchyChildren[0]!)).shadowedByIncidentId, undefined, '父级关闭只能解除自身 shadow，不删除子级状态')
assert.equal((await hierarchyStore.get(hierarchyChildren[0]!)).phase, 'OPEN')

const authorizedFamilyStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => now })
const authorizedInstanceId = 'authorized-instance'
const authorizedScopes: AccountCircuitScope[] = [
  { kind: 'account', accountRuntimeKey: `${authorizedInstanceId}:authorized:grantee-a:group-a:grant-a` },
  { kind: 'account', accountRuntimeKey: `${authorizedInstanceId}:authorized:grantee-a:group-b:grant-a` }
]
const unrelatedScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'authorized-instance-similar' }
for (const [index, authorizedScope] of authorizedScopes.entries()) {
  await authorizedFamilyStore.suspect({
    scope: authorizedScope,
    dispatchRevision: '7',
    transitionId: `authorized-suspect-${index}`,
    reason: 'old authorized configuration',
    nowMs: now
  })
}
await authorizedFamilyStore.suspect({
  scope: unrelatedScope,
  dispatchRevision: '7',
  transitionId: 'unrelated-suspect',
  reason: 'unrelated',
  nowMs: now
})
assert.equal(await authorizedFamilyStore.replaceAccountDispatchRevision({
  accountRuntimeKey: authorizedInstanceId,
  dispatchRevision: '8',
  transitionId: 'authorized-revision-8',
  nowMs: now
}), 2, '裸授权实例 ID 必须立即 fence 该实例的全部 runtime key 上下文')
for (const authorizedScope of authorizedScopes) {
  const state = await authorizedFamilyStore.get(authorizedScope, now)
  assert.equal(state.phase, 'CLOSED')
  assert.equal(state.dispatchRevision, '8')
}
assert.equal((await authorizedFamilyStore.get(unrelatedScope, now)).phase, 'SUSPECT', 'family 匹配不能误伤相似账户 ID')
assert.equal(await authorizedFamilyStore.replaceAccountDispatchRevision({
  accountRuntimeKey: authorizedInstanceId,
  dispatchRevision: '7',
  transitionId: 'authorized-late-revision-7',
  nowMs: now + 1
}), 0, '迟到旧 revision 不得覆盖已投影的新 revision')
await Promise.all([
  authorizedFamilyStore.replaceAccountDispatchRevision({ accountRuntimeKey: authorizedInstanceId, dispatchRevision: '10', transitionId: 'authorized-revision-10', nowMs: now + 2 }),
  authorizedFamilyStore.replaceAccountDispatchRevision({ accountRuntimeKey: authorizedInstanceId, dispatchRevision: '9', transitionId: 'authorized-revision-9', nowMs: now + 2 })
])
assert.equal((await authorizedFamilyStore.get(authorizedScopes[0]!, now + 2)).dispatchRevision, '10', '并发乱序投影必须保留最大 numeric revision')

console.log('account-circuit-memory-store-regression passed')
