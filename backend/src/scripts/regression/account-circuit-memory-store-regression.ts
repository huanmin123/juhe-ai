import assert from 'node:assert/strict'

import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import {
  accountCircuitBackoffDelayMs,
  accountCircuitScopeKey,
  normalizeAccountCircuitEscalationDistinctScopeThreshold,
  normalizeAccountCircuitEscalationWindowMs,
  type AccountCircuitScope
} from '../../modules/gateway/runtime/account-circuit-store.js'
import { passiveScheduleJitterWindowMs } from '../../shared/passive-schedule-jitter.js'

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
const initialEvidence = 'a'.repeat(64)
const confirmationEvidenceA = 'b'.repeat(64)
const confirmationEvidenceB = 'c'.repeat(64)

assert.notEqual(accountCircuitScopeKey(accountScope), accountCircuitScopeKey(keyScope), '账户、Key 与协议模型作用域必须隔离')
assert.notEqual(accountCircuitScopeKey(keyScope), accountCircuitScopeKey(modelScope), 'Key 与协议模型作用域不得碰撞')
assert.equal(normalizeAccountCircuitEscalationDistinctScopeThreshold(undefined), 3)
assert.throws(() => normalizeAccountCircuitEscalationDistinctScopeThreshold(2), /3\.\.64/)
assert.equal(normalizeAccountCircuitEscalationWindowMs(undefined), 10 * 60_000)
assert.throws(() => normalizeAccountCircuitEscalationWindowMs(59_999), /60000\.\.86400000/)
for (const attempt of [1, 2, 3, 4]) {
  const base = [3_000, 5_000, 10_000, 30_000, 60_000, 120_000, 300_000, 600_000, 900_000, 900_000][attempt - 1]!
  const delay = accountCircuitBackoffDelayMs(attempt)
  assert.equal(delay, base, `第 ${attempt} 次短电路退避保持最小控制窗口`)
}
for (const attempt of [5, 6, 7, 8, 9, 10]) {
  const base = [3_000, 5_000, 10_000, 30_000, 60_000, 120_000, 300_000, 600_000, 900_000, 900_000][attempt - 1]!
  const delay = accountCircuitBackoffDelayMs(attempt)
  const window = passiveScheduleJitterWindowMs(base)
  assert.ok(delay >= base - window && delay <= base + window && delay !== base, `第 ${attempt} 次长电路退避必须使用全局偏移`)
}
assert.equal((await store.get(accountScope)).phase, 'CLOSED', '缺失状态必须按 CLOSED 读取')
await assert.rejects(
  store.restore({
    scope: accountScope,
    scopeKey: 'durable-key-does-not-match-scope',
    phase: 'OPEN',
    generation: 1,
    dispatchRevision: 'rev-corrupt',
    transitionId: 'corrupt-scope-key',
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    retryAtMs: now + 3_000,
    updatedAtMs: now
  }, now),
  /scopeKey 与作用域字段不一致/,
  'Memory restore 不得接受请求永远查不到的伪 scopeKey'
)

const suspected = await store.suspect({
  scope: accountScope,
  dispatchRevision: 'rev-1',
  transitionId: 'suspect-1',
  reason: 'connect timeout',
  failureEvidenceKey: initialEvidence,
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
assert.equal((await store.acquireConfirmationLease({
  ...confirmationIdentity,
  transitionId: 'confirmation-not-due',
  leaseId: 'confirmation-not-due',
  leaseUntilMs: now + 1_000,
  expectedFailureEvidenceKey: initialEvidence,
  confirmationEvidenceKey: confirmationEvidenceA,
  nowMs: now
})).status, 'not_due', 'SUSPECT retryAt 前不得取得 confirmation 租约')
assert.equal((await store.acquireConfirmationLease({
  ...confirmationIdentity,
  transitionId: 'confirmation-same-evidence',
  leaseId: 'confirmation-same-evidence',
  leaseUntilMs: now + 1_000,
  expectedFailureEvidenceKey: initialEvidence,
  confirmationEvidenceKey: initialEvidence,
  nowMs: now + 3_000
})).status, 'state_mismatch', '同一 evidence 即使已到期也不得取得 confirmation 租约')
now += 3_000
const [confirmationA, confirmationB] = await Promise.all([
  store.acquireConfirmationLease({
    ...confirmationIdentity,
    transitionId: 'confirmation-acquire-a',
    leaseId: 'confirmation-a',
    leaseUntilMs: now + 1_000,
    expectedFailureEvidenceKey: initialEvidence,
    confirmationEvidenceKey: confirmationEvidenceA,
    nowMs: now
  }),
  store.acquireConfirmationLease({
    ...confirmationIdentity,
    transitionId: 'confirmation-acquire-b',
    leaseId: 'confirmation-b',
    leaseUntilMs: now + 1_000,
    expectedFailureEvidenceKey: initialEvidence,
    confirmationEvidenceKey: confirmationEvidenceA,
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
  failureEvidenceKey: confirmationEvidenceA,
  nowMs: now
})
assert.equal(opened.state.phase, 'SUSPECT', '默认需要两次独立 confirmation，首次失败只能释放租约并保持 SUSPECT')
assert.equal(opened.state.confirmationFailuresRequired, 2)
assert.equal(opened.state.confirmationFailureCount, 1)
assert.deepEqual(opened.state.failureEvidenceKeys, [initialEvidence, confirmationEvidenceA])
assert.equal(opened.state.lease, undefined)
assert.deepEqual(await store.listDue(now, 10), [], '无租约 SUSPECT 不得占用恢复 due batch')

now += 3_000
const duplicateAcquire = await store.acquireConfirmationLease({
  ...confirmationIdentity,
  transitionId: 'confirmation-acquire-duplicate',
  leaseId: 'confirmation-duplicate',
  leaseUntilMs: now + 1_000,
  expectedFailureEvidenceKey: confirmationEvidenceA,
  confirmationEvidenceKey: confirmationEvidenceA,
  nowMs: now
})
assert.equal(duplicateAcquire.status, 'state_mismatch', '重复坏会话 evidence 必须在租约获取前被拒绝')

await store.acquireConfirmationLease({
  ...confirmationIdentity,
  transitionId: 'confirmation-acquire-second-independent',
  leaseId: 'confirmation-second-independent',
  leaseUntilMs: now + 1_000,
  expectedFailureEvidenceKey: confirmationEvidenceA,
  confirmationEvidenceKey: confirmationEvidenceB,
  nowMs: now
})
const openedAfterThreshold = await store.completeConfirmation({
  ...confirmationIdentity,
  transitionId: 'confirmation-failed-second-independent',
  leaseId: 'confirmation-second-independent',
  outcome: 'transport_failure',
  reason: 'second independent timeout confirmed',
  failureEvidenceKey: confirmationEvidenceB,
  nowMs: now
})
assert.equal(openedAfterThreshold.state.phase, 'OPEN')
assert.equal(openedAfterThreshold.state.confirmationFailureCount, 2)
assert.deepEqual(openedAfterThreshold.state.failureEvidenceKeys, [initialEvidence, confirmationEvidenceA, confirmationEvidenceB])
const openedState = openedAfterThreshold
assert.equal(openedState.state.retryAtMs, now + 3_000)
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

const revisionFenceStore = new MemoryAccountCircuitStore({ capacity: 4, now: () => now })
const revisionFenceScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'revision-fence' }
await revisionFenceStore.restore({
  scope: revisionFenceScope,
  scopeKey: accountCircuitScopeKey(revisionFenceScope),
  phase: 'OPEN',
  generation: 4,
  dispatchRevision: '10',
  transitionId: 'revision-fence-open-10',
  backoffAttempt: 2,
  recoverySuccessCount: 0,
  openedAtMs: now,
  retryAtMs: now + 30_000,
  updatedAtMs: now
}, now)
const lateReplace = await revisionFenceStore.replaceDispatchRevision({
  scope: revisionFenceScope,
  dispatchRevision: '9',
  transitionId: 'late-revision-9',
  nowMs: now + 1
})
assert.equal(lateReplace.status, 'stale_dispatch_revision', '迟到旧 revision 不得关闭较新 OPEN')
assert.equal(lateReplace.state.phase, 'OPEN')
assert.equal(lateReplace.state.dispatchRevision, '10')
const duplicateOwnerReplace = await revisionFenceStore.replaceDispatchRevision({
  scope: revisionFenceScope,
  dispatchRevision: '10',
  transitionId: 'duplicate-owner-revision-10',
  nowMs: now + 1
})
assert.equal(duplicateOwnerReplace.status, 'idempotent', '同 owner revision 的重复投影不得关闭 OPEN')
assert.equal(duplicateOwnerReplace.state.phase, 'OPEN')
const lateRestore = await revisionFenceStore.restore({
  ...lateReplace.state,
  phase: 'CLOSED',
  generation: 99,
  dispatchRevision: '8',
  transitionId: 'late-restore-8',
  openedAtMs: undefined,
  retryAtMs: undefined,
  updatedAtMs: now + 2
}, now + 2)
assert.equal(lateRestore.status, 'stale_dispatch_revision', '迟到 durable 旧 revision 即使 generation/时间更大也不得覆盖')
assert.equal(lateRestore.state.phase, 'OPEN')
const nextOwner = await revisionFenceStore.replaceDispatchRevision({
  scope: revisionFenceScope,
  dispatchRevision: '11',
  transitionId: 'revision-11',
  nowMs: now + 3
})
assert.equal(nextOwner.status, 'applied')
assert.equal(nextOwner.state.phase, 'CLOSED', '真正的新 owner revision 可以开启新代')
assert.equal(nextOwner.state.generation, 5)
assert.equal((await revisionFenceStore.suspect({
  scope: revisionFenceScope,
  dispatchRevision: '10',
  transitionId: 'late-suspect-10',
  reason: 'late transport observation',
  nowMs: now + 4
})).status, 'stale_dispatch_revision', '旧 attempt 的迟到失败不得污染新 owner CLOSED')

const expiringScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'expiring' }
await store.suspect({ scope: expiringScope, dispatchRevision: 'r1', transitionId: 'exp-suspect', reason: 'timeout', confirmationFailuresRequired: 1, nowMs: now })
now += 3_000
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
  { kind: 'protocol_model', accountRuntimeKey: 'hierarchy-account', protocolProfile: 'profile', requestLane: 'text', modelBucket: 'model-b' },
  { kind: 'protocol_model', accountRuntimeKey: 'hierarchy-account', protocolProfile: 'profile', requestLane: 'text', modelBucket: 'model-c' },
  { kind: 'protocol_model', accountRuntimeKey: 'hierarchy-account', protocolProfile: 'profile', requestLane: 'text', modelBucket: 'model-d' }
]
for (const [index, childScope] of hierarchyChildren.entries()) {
  await hierarchyStore.suspect({ scope: childScope, dispatchRevision: 'h1', transitionId: `hierarchy-suspect-${index}`, reason: 'transport', confirmationFailuresRequired: 1, nowMs: now })
  now += 3_000
  await hierarchyStore.acquireConfirmationLease({ scope: childScope, generation: 1, dispatchRevision: 'h1', transitionId: `hierarchy-acquire-${index}`, leaseId: `hierarchy-lease-${index}`, leaseUntilMs: now + 1_000, nowMs: now })
  await hierarchyStore.completeConfirmation({ scope: childScope, generation: 1, dispatchRevision: 'h1', transitionId: `hierarchy-open-${index}`, leaseId: `hierarchy-lease-${index}`, outcome: 'transport_failure', nowMs: now })
}
const hierarchyEvidenceA = await hierarchyStore.recordProtocolModelOpenEvidence({
  scope: hierarchyChildren[0]!, generation: 1, dispatchRevision: 'h1', evidenceId: 'hierarchy-evidence-a', accountTransitionId: 'hierarchy-account-open', reason: 'multiple protocol failures', confirmedFailureCount: 100, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
})
assert.equal(hierarchyEvidenceA.status, 'recorded')
const hierarchyEvidenceB = await hierarchyStore.recordProtocolModelOpenEvidence({
  scope: hierarchyChildren[1]!, generation: 1, dispatchRevision: 'h1', evidenceId: 'hierarchy-evidence-b', accountTransitionId: 'hierarchy-account-open', reason: 'multiple protocol failures', confirmedFailureCount: 100, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
})
assert.equal(hierarchyEvidenceB.status, 'recorded', '任意高失败累计都不能让两个 child scope 升级父级')
assert.equal((await hierarchyStore.get(hierarchyAccount)).phase, 'CLOSED')
const hierarchyRepeatedScope = await hierarchyStore.recordProtocolModelOpenEvidence({
  scope: hierarchyChildren[0]!, generation: 1, dispatchRevision: 'h1', evidenceId: 'hierarchy-evidence-a-repeated', accountTransitionId: 'hierarchy-account-open', reason: 'repeated protocol failure', confirmedFailureCount: 100, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
})
assert.equal(hierarchyRepeatedScope.status, 'recorded', '同一 child scope 的重复 evidence 只能刷新该 scope，不能增加独立 scope 数')
assert.equal(hierarchyRepeatedScope.protocolScopeCount, 2)
const hierarchyEvidenceC = await hierarchyStore.recordProtocolModelOpenEvidence({
  scope: hierarchyChildren[2]!, generation: 1, dispatchRevision: 'h1', evidenceId: 'hierarchy-evidence-c', accountTransitionId: 'hierarchy-account-open', reason: 'third distinct protocol failure', confirmedFailureCount: 1, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
})
assert.equal(hierarchyEvidenceC.status, 'escalated')
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
const postRecoveryEvidence = await hierarchyStore.recordProtocolModelOpenEvidence({
  scope: hierarchyChildren[0]!, generation: 1, dispatchRevision: 'h1', evidenceId: 'hierarchy-evidence-after-recovery', accountTransitionId: 'hierarchy-account-reopen', reason: 'single protocol failure after recovery', confirmedFailureCount: 100, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
})
assert.equal(postRecoveryEvidence.status, 'recorded', '父级恢复必须清除旧升级证据，单一 child 新失败不能借旧证据立即升级')
assert.equal(postRecoveryEvidence.protocolScopeCount, 1)
assert.equal((await hierarchyStore.get(hierarchyAccount)).phase, 'CLOSED')
for (const [index, childScope] of hierarchyChildren.slice(1, 3).entries()) {
  const evidence = await hierarchyStore.recordProtocolModelOpenEvidence({
    scope: childScope,
    generation: 1,
    dispatchRevision: 'h1',
    evidenceId: `hierarchy-configured-threshold-${index + 1}`,
    accountTransitionId: 'hierarchy-account-threshold-four',
    reason: 'configured threshold evidence',
    confirmedFailureCount: 100,
    distinctScopeThreshold: 4,
    windowMs: 60_000,
    maxProtocolScopes: 8,
    nowMs: now
  })
  assert.equal(evidence.status, 'recorded', '配置为 4 时前三个独立 scope 都不能升级父级')
}
assert.equal((await hierarchyStore.get(hierarchyAccount)).phase, 'CLOSED')
const configuredThresholdFourth = await hierarchyStore.recordProtocolModelOpenEvidence({
  scope: hierarchyChildren[3]!,
  generation: 1,
  dispatchRevision: 'h1',
  evidenceId: 'hierarchy-configured-threshold-4',
  accountTransitionId: 'hierarchy-account-threshold-four',
  reason: 'fourth configured threshold evidence',
  confirmedFailureCount: 1,
  distinctScopeThreshold: 4,
  windowMs: 60_000,
  maxProtocolScopes: 8,
  nowMs: now
})
assert.equal(configuredThresholdFourth.status, 'escalated', '配置为 4 时第四个独立 scope 才能升级父级')

const windowStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => now })
const windowAccount: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'window-account' }
const windowChildren: Array<Extract<AccountCircuitScope, { kind: 'protocol_model' }>> = ['a', 'b', 'c'].map((modelBucket) => ({
  kind: 'protocol_model',
  accountRuntimeKey: 'window-account',
  protocolProfile: 'profile',
  requestLane: 'text',
  modelBucket
}))
for (const [index, childScope] of windowChildren.entries()) {
  await windowStore.suspect({ scope: childScope, dispatchRevision: 'w1', transitionId: `window-suspect-${index}`, reason: 'transport', confirmationFailuresRequired: 1, nowMs: now })
  now += 3_000
  await windowStore.acquireConfirmationLease({ scope: childScope, generation: 1, dispatchRevision: 'w1', transitionId: `window-acquire-${index}`, leaseId: `window-lease-${index}`, leaseUntilMs: now + 1_000, nowMs: now })
  await windowStore.completeConfirmation({ scope: childScope, generation: 1, dispatchRevision: 'w1', transitionId: `window-open-${index}`, leaseId: `window-lease-${index}`, outcome: 'transport_failure', nowMs: now })
}
const windowEvidence = (scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>, evidenceId: string) => windowStore.recordProtocolModelOpenEvidence({
  scope,
  generation: 1,
  dispatchRevision: 'w1',
  evidenceId,
  accountTransitionId: 'window-parent-open',
  reason: 'windowed distinct failure',
  confirmedFailureCount: 1,
  distinctScopeThreshold: 3,
  windowMs: 60_000,
  maxProtocolScopes: 8,
  nowMs: now
})
assert.equal((await windowEvidence(windowChildren[0]!, 'window-evidence-a')).status, 'recorded')
now += 60_001
assert.equal((await windowEvidence(windowChildren[1]!, 'window-evidence-b')).status, 'recorded')
const outsideWindowThird = await windowEvidence(windowChildren[2]!, 'window-evidence-c')
assert.equal(outsideWindowThird.status, 'recorded', '窗口外的旧 scope 不得参与父级升级')
assert.equal(outsideWindowThird.protocolScopeCount, 2)
assert.equal((await windowStore.get(windowAccount)).phase, 'CLOSED')
assert.equal((await windowEvidence(windowChildren[0]!, 'window-evidence-a-refresh')).status, 'escalated', '旧 scope 在当前窗口重新 OPEN 取证后才可作为第三个独立 scope')

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

const legacyStore = new MemoryAccountCircuitStore({ capacity: 32, now: () => now })
const legacyScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'legacy-active-suspect' }
await legacyStore.restore({
  scope: legacyScope,
  scopeKey: accountCircuitScopeKey(legacyScope),
  phase: 'SUSPECT',
  generation: 1,
  dispatchRevision: 'legacy-1',
  transitionId: 'legacy-suspect',
  backoffAttempt: 0,
  recoverySuccessCount: 0,
  updatedAtMs: now
}, now)
const restoredLegacy = await legacyStore.get(legacyScope, now)
assert.equal(restoredLegacy.confirmationFailuresRequired, 1, '旧 active incident 缺阈值时必须兼容一次确认语义')
assert.equal(restoredLegacy.confirmationFailureCount, 0)
assert.deepEqual(restoredLegacy.failureEvidenceKeys, [])
await legacyStore.acquireConfirmationLease({
  scope: legacyScope,
  generation: 1,
  dispatchRevision: 'legacy-1',
  transitionId: 'legacy-acquire',
  leaseId: 'legacy-lease',
  leaseUntilMs: now + 1_000,
  nowMs: now
})
assert.equal((await legacyStore.completeConfirmation({
  scope: legacyScope,
  generation: 1,
  dispatchRevision: 'legacy-1',
  transitionId: 'legacy-confirmed',
  leaseId: 'legacy-lease',
  outcome: 'transport_failure',
  failureEvidenceKey: 'd'.repeat(64),
  nowMs: now
})).state.phase, 'OPEN', '旧 active incident 一次独立 confirmation 后仍按旧语义 OPEN')

const confirmationSuccessStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => now })
const confirmationSuccessScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'confirmation-success' }
await confirmationSuccessStore.suspect({
  scope: confirmationSuccessScope,
  dispatchRevision: 'success-1',
  transitionId: 'success-suspect',
  reason: 'transport:first',
  confirmationFailuresRequired: 2,
  failureEvidenceKey: 'e'.repeat(64),
  nowMs: now
})
now += 3_000
await confirmationSuccessStore.acquireConfirmationLease({
  scope: confirmationSuccessScope,
  generation: 1,
  dispatchRevision: 'success-1',
  transitionId: 'success-acquire-failure',
  leaseId: 'success-failure-lease',
  leaseUntilMs: now + 1_000,
  nowMs: now
})
await confirmationSuccessStore.completeConfirmation({
  scope: confirmationSuccessScope,
  generation: 1,
  dispatchRevision: 'success-1',
  transitionId: 'success-first-confirmation-failed',
  leaseId: 'success-failure-lease',
  outcome: 'transport_failure',
  failureEvidenceKey: 'f'.repeat(64),
  reason: 'transport:confirmation',
  nowMs: now
})
now += 3_000
await confirmationSuccessStore.acquireConfirmationLease({
  scope: confirmationSuccessScope,
  generation: 1,
  dispatchRevision: 'success-1',
  transitionId: 'success-acquire-unknown',
  leaseId: 'success-unknown-lease',
  leaseUntilMs: now + 1_000,
  nowMs: now
})
const unknownConfirmation = await confirmationSuccessStore.completeConfirmation({
  scope: confirmationSuccessScope,
  generation: 1,
  dispatchRevision: 'success-1',
  transitionId: 'success-unknown',
  leaseId: 'success-unknown-lease',
  outcome: 'unknown',
  nowMs: now
})
assert.equal(unknownConfirmation.state.confirmationFailureCount, 1, 'unknown/cancel 不得增加确认失败数')
assert.equal(unknownConfirmation.state.lease, undefined)
now = unknownConfirmation.state.retryAtMs ?? now
await confirmationSuccessStore.acquireConfirmationLease({
  scope: confirmationSuccessScope,
  generation: 1,
  dispatchRevision: 'success-1',
  transitionId: 'success-acquire-framing',
  leaseId: 'success-framing-lease',
  leaseUntilMs: now + 1_000,
  nowMs: now
})
const framingRecovered = await confirmationSuccessStore.completeConfirmation({
  scope: confirmationSuccessScope,
  generation: 1,
  dispatchRevision: 'success-1',
  transitionId: 'success-framing-complete',
  leaseId: 'success-framing-lease',
  outcome: 'framing_complete',
  nowMs: now
})
assert.equal(framingRecovered.state.phase, 'RECOVERING')
assert.equal(framingRecovered.state.confirmationFailureCount, 0)
assert.deepEqual(framingRecovered.state.failureEvidenceKeys, [])
assert.equal(framingRecovered.state.failureReason, undefined, 'framing complete 必须清除嵌入 reason 的旧 evidence')

const maximumThresholdStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => now })
const maximumThresholdScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'maximum-confirmation-threshold' }
await maximumThresholdStore.suspect({
  scope: maximumThresholdScope,
  dispatchRevision: 'max-1',
  transitionId: 'max-suspect',
  reason: 'transport',
  confirmationFailuresRequired: 5,
  failureEvidenceKey: '0'.repeat(64),
  nowMs: now
})
let maximumThresholdState = await maximumThresholdStore.get(maximumThresholdScope, now)
for (let index = 1; index <= 5; index++) {
  now += 3_000
  await maximumThresholdStore.acquireConfirmationLease({
    scope: maximumThresholdScope,
    generation: 1,
    dispatchRevision: 'max-1',
    transitionId: `max-acquire-${index}`,
    leaseId: `max-lease-${index}`,
    leaseUntilMs: now + 1_000,
    nowMs: now
  })
  maximumThresholdState = (await maximumThresholdStore.completeConfirmation({
    scope: maximumThresholdScope,
    generation: 1,
    dispatchRevision: 'max-1',
    transitionId: `max-complete-${index}`,
    leaseId: `max-lease-${index}`,
    outcome: 'transport_failure',
    failureEvidenceKey: index.toString(16).repeat(64),
    nowMs: now
  })).state
  assert.equal(maximumThresholdState.phase, index === 5 ? 'OPEN' : 'SUSPECT')
}
assert.equal(maximumThresholdState.confirmationFailureCount, 5)
assert.equal(maximumThresholdState.failureEvidenceKeys?.length, 6, '阈值 5 的 evidence 窗口上限必须是 N+1=6')
assert.equal(new Set(maximumThresholdState.failureEvidenceKeys).size, 6)
for (const [index, invalidThreshold] of [0, 6, 1.5].entries()) {
  await assert.rejects(maximumThresholdStore.suspect({
    scope: { kind: 'account', accountRuntimeKey: `invalid-confirmation-threshold-${index}` },
    dispatchRevision: 'invalid-1',
    transitionId: `invalid-threshold-${index}`,
    reason: 'transport',
    confirmationFailuresRequired: invalidThreshold,
    nowMs: now
  }), /confirmationFailuresRequired/)
}

const dueStore = new MemoryAccountCircuitStore({ capacity: 32, now: () => now })
for (let index = 0; index < 12; index++) {
  const scope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: `idle-suspect-${index}` }
  await dueStore.suspect({
    scope,
    dispatchRevision: 'due-1',
    transitionId: `idle-suspect-transition-${index}`,
    reason: 'transport',
    nowMs: now - 100 - index
  })
}
const dueOpenScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'due-open' }
await dueStore.restore({
  scope: dueOpenScope,
  scopeKey: accountCircuitScopeKey(dueOpenScope),
  phase: 'OPEN',
  generation: 1,
  dispatchRevision: 'due-1',
  transitionId: 'due-open-transition',
  backoffAttempt: 1,
  recoverySuccessCount: 0,
  retryAtMs: now,
  updatedAtMs: now - 1
}, now)
assert.deepEqual(
  (await dueStore.listDue(now, 10)).map((state) => state.scopeKey),
  [accountCircuitScopeKey(dueOpenScope)],
  '超过 batch 的无租约 SUSPECT 不能饿死真正到期的 OPEN'
)

console.log('account-circuit-memory-store-regression passed')
