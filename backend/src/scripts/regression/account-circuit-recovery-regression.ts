import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'

import type { TransportProbeOutcome } from '../../modules/accounts/automatic-account-probe-outcome.js'
import {
  AccountCircuitRecoveryService,
  createScheduledAccountCircuitRecoveryResolver,
  type AccountCircuitRecoveryTargetResolver
} from '../../modules/background/account-circuit-recovery.service.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import {
  accountCircuitScopeKey,
  type AccountCircuitScope,
  type AccountCircuitState
} from '../../modules/gateway/runtime/account-circuit-store.js'

let now = 10_000
let idSequence = 0
const createId = () => `recovery-${++idSequence}`
const store = new MemoryAccountCircuitStore({ capacity: 20, now: () => now })

const recoveringScope = scope('recovering')
now = await openCircuit(store, recoveringScope, 'r1', now)
now += 3_000
const framingOutcomes: TransportProbeOutcome[] = [
  { kind: 'framing_complete', statusCode: 401 },
  { kind: 'framing_complete', statusCode: 401 },
  { kind: 'framing_complete', statusCode: 429 },
  { kind: 'framing_complete', statusCode: 503 }
]
const recoveryService = service(store, async () => ({
  dispatchRevision: 'r1',
  probe: async () => framingOutcomes.shift()!
}))

for (const expectedCount of [0, 1, 2, 3]) {
  const sweep = await recoveryService.sweep()
  assert.equal(sweep.framingCompleteCount, 1, '任意 framing 完整 HTTP 响应都应推进恢复')
  const state = await store.get(recoveringScope, now)
  assert.equal(state.phase, expectedCount === 3 ? 'CLOSED' : 'RECOVERING')
  assert.equal(state.recoverySuccessCount, expectedCount === 3 ? 0 : expectedCount)
  if (expectedCount < 3) now += 3_000
}

const singleFlightScope = scope('single-flight')
now = await openCircuit(store, singleFlightScope, 'r1', now)
now += 3_000
let probeCount = 0
const singleFlightResolver: AccountCircuitRecoveryTargetResolver = async () => ({
  dispatchRevision: 'r1',
  probe: async () => {
    probeCount += 1
    return { kind: 'framing_complete', statusCode: 200 }
  }
})
const [singleFlightA, singleFlightB] = await Promise.all([
  service(store, singleFlightResolver).sweep(),
  service(store, singleFlightResolver).sweep()
])
assert.equal(probeCount, 1, '两个 worker 同时扫描同一 generation 时只能一个取得半开 lease')
assert.equal(singleFlightA.leasedCount + singleFlightB.leasedCount, 1)

const failureScope = scope('reopen')
now = await openCircuit(store, failureScope, 'r1', now)
now += 3_000
const failureOutcomes: TransportProbeOutcome[] = [
  { kind: 'framing_complete', statusCode: 500 },
  { kind: 'transport_incomplete', failureKind: 'read', statusCode: 200 }
]
const failureService = service(store, async (state) => ({
  dispatchRevision: 'r1',
  probe: async () => state.scope.accountRuntimeKey === 'reopen'
    ? failureOutcomes.shift()!
    : { kind: 'framing_complete', statusCode: 200 }
}))
await failureService.sweep()
now += 3_000
const failureSweep = await failureService.sweep()
assert.equal(failureSweep.transportIncompleteCount, 1)
const reopened = await store.get(failureScope, now)
assert.equal(reopened.phase, 'OPEN')
assert.equal(reopened.backoffAttempt, 2)
assert.equal(reopened.retryAtMs, now + 5_000, '恢复探针 transport 不完整必须推进下一档退避')

const unknownScope = scope('unknown')
now = await openCircuit(store, unknownScope, 'r1', now)
now += 3_000
const unknownOutcomes: TransportProbeOutcome[] = [
  { kind: 'framing_complete', statusCode: 200 },
  { kind: 'unknown', failureKind: 'canceled' }
]
const unknownService = service(store, async (state) => ({
  dispatchRevision: 'r1',
  probe: async () => state.scope.accountRuntimeKey === 'unknown'
    ? unknownOutcomes.shift()!
    : { kind: 'framing_complete', statusCode: 200 }
}))
await unknownService.sweep()
const beforeUnknown = await store.get(unknownScope, now)
now += 3_000
await unknownService.sweep()
const afterUnknown = await store.get(unknownScope, now)
assert.equal(afterUnknown.phase, 'RECOVERING')
assert.equal(afterUnknown.recoverySuccessCount, beforeUnknown.recoverySuccessCount, 'unknown 不得增加成功或失败计数')
assert.equal(afterUnknown.backoffAttempt, beforeUnknown.backoffAttempt + 1, 'unknown 必须推进退避，避免无结论探针形成紧密风暴')
assert((afterUnknown.retryAtMs ?? 0) > now, 'unknown 必须安排未来重试时间，不能立即重新争抢恢复租约')

const revisionScope = scope('revision')
now = await openCircuit(store, revisionScope, 'old-revision', now)
now += 3_000
let revisionProbeCount = 0
const revisionSweep = await service(store, async (state) => ({
  dispatchRevision: state.scope.accountRuntimeKey === 'revision' ? 'new-revision' : 'r1',
  probe: async () => {
    if (state.scope.accountRuntimeKey === 'revision') revisionProbeCount += 1
    return { kind: 'framing_complete', statusCode: 200 }
  }
})).sweep()
assert(revisionSweep.fencedCount >= 1)
assert.equal(revisionProbeCount, 0, 'dispatchRevision 变化必须在发探针前 fencing')
const revised = await store.get(revisionScope, now)
assert.equal(revised.phase, 'CLOSED')
assert.equal(revised.dispatchRevision, 'new-revision')

const staleScope = scope('stale-result')
now = await openCircuit(store, staleScope, 'r1', now)
now += 3_000
let probeStarted!: () => void
let completeProbe!: (outcome: TransportProbeOutcome) => void
const probeStartedPromise = new Promise<void>((resolve) => { probeStarted = resolve })
const probePromise = new Promise<TransportProbeOutcome>((resolve) => { completeProbe = resolve })
const staleSweepPromise = service(store, async () => ({
  dispatchRevision: 'r1',
  probe: async () => {
    probeStarted()
    return await probePromise
  }
})).sweep()
await probeStartedPromise
await store.replaceDispatchRevision({
  scope: staleScope,
  dispatchRevision: 'r2',
  transitionId: createId(),
  nowMs: now
})
completeProbe({ kind: 'framing_complete', statusCode: 200 })
const staleSweep = await staleSweepPromise
assert.equal(staleSweep.fencedCount, 1, '旧 generation 的迟到探针结果必须被拒绝')
assert.equal((await store.get(staleScope, now)).dispatchRevision, 'r2')

const missingScope = scope('missing-target')
now = await openCircuit(store, missingScope, 'r1', now)
now += 3_000
const missingSweep = await service(store, async (state) => state.scope.accountRuntimeKey === 'missing-target'
  ? undefined
  : {
      dispatchRevision: 'r1',
      probe: async () => ({ kind: 'framing_complete', statusCode: 200 })
    }).sweep()
assert(missingSweep.unknownCount >= 1)
assert.equal((await store.get(missingScope, now)).phase, 'OPEN', 'resolver 缺失目标时必须保守释放到 OPEN')

const stableSessionStore = new MemoryAccountCircuitStore({ capacity: 20, now: () => now })
const stableSessionScope = scope('stable-session-storm')
const stableSessionEvidence = 'a'.repeat(64)
const stableSessionSuspect = await stableSessionStore.suspect({
  scope: stableSessionScope,
  dispatchRevision: 'r1',
  transitionId: createId(),
  reason: 'timeout',
  confirmationFailuresRequired: 2,
  failureEvidenceKey: stableSessionEvidence,
  nowMs: now
})
let stableSessionRetryAt = requiredRetryAt(stableSessionSuspect.state, '坏会话 SUSPECT 必须安排确认重试')
for (let index = 0; index < 20; index += 1) {
  now = stableSessionRetryAt
  const leaseId = createId()
  assert.equal((await stableSessionStore.acquireConfirmationLease({
    scope: stableSessionScope,
    generation: stableSessionSuspect.state.generation,
    dispatchRevision: 'r1',
    transitionId: createId(),
    leaseId,
    leaseUntilMs: now + 1_000,
    nowMs: now
  })).status, 'applied')
  const duplicate = await stableSessionStore.completeConfirmation({
    scope: stableSessionScope,
    generation: stableSessionSuspect.state.generation,
    dispatchRevision: 'r1',
    transitionId: createId(),
    leaseId,
    outcome: 'transport_failure',
    failureEvidenceKey: stableSessionEvidence,
    nowMs: now
  })
  assert.equal(duplicate.state.phase, 'SUSPECT')
  assert.equal(duplicate.state.confirmationFailureCount, 0, '同一稳定会话重复失败不得形成独立 confirmation evidence')
  stableSessionRetryAt = requiredRetryAt(duplicate.state, '重复 evidence 后必须有界延后下一次确认')
}
assert.equal((await stableSessionStore.listDue(now, 10)).length, 0, 'SUSPECT 应等待有界后台确认间隔而非热循环')
now = stableSessionRetryAt
assert.equal((await stableSessionStore.listDue(now, 10))[0]?.scopeKey, stableSessionSuspect.state.scopeKey, '低流量 SUSPECT 必须自动进入后台确认队列')

let stableSessionProbeCount = 0
const stableSessionProbe = service(stableSessionStore, async () => ({
  dispatchRevision: 'r1',
  probe: async () => {
    stableSessionProbeCount += 1
    return { kind: 'transport_incomplete', failureKind: 'connection' }
  }
}))
const [stableSessionSweepA, stableSessionSweepB] = await Promise.all([
  stableSessionProbe.sweep(),
  stableSessionProbe.sweep()
])
assert.equal(stableSessionProbeCount, 1, '并发 worker 对同一 SUSPECT generation 只能发出一个后台确认探针')
assert.equal(stableSessionSweepA.leasedCount + stableSessionSweepB.leasedCount, 1)
let stableSessionState = await stableSessionStore.get(stableSessionScope, now)
assert.equal(stableSessionState.phase, 'SUSPECT')
assert.equal(stableSessionState.confirmationFailureCount, 1, '后台独立传输探针应贡献第一份 confirmation evidence')
assert.equal(stableSessionState.failureEvidenceKeys?.length, 2)
now += 3_000
await stableSessionProbe.sweep()
stableSessionState = await stableSessionStore.get(stableSessionScope, now)
assert.equal(stableSessionState.phase, 'OPEN', '真正死亡账户即使没有新客户端会话，也必须由后台独立确认最终 OPEN')
assert.equal(stableSessionState.confirmationFailureCount, 2)
assert.equal(stableSessionState.failureEvidenceKeys?.length, 3)

const framingStore = new MemoryAccountCircuitStore({ capacity: 10, now: () => now })
const framingScope = scope('suspect-framing-recovery')
await framingStore.suspect({
  scope: framingScope,
  dispatchRevision: 'r1',
  transitionId: createId(),
  reason: 'timeout',
  confirmationFailuresRequired: 2,
  nowMs: now
})
now += 3_000
const framingMutations: AccountCircuitState[] = []
const framingSweep = await new AccountCircuitRecoveryService(
  framingStore,
  async () => ({
    dispatchRevision: 'r1',
    probe: async () => ({ kind: 'framing_complete', statusCode: 503 })
  }),
  {
    batchSize: 10,
    leaseDurationMs: 10_000,
    now: () => now,
    createId,
    onMutation: ({ status, state }) => {
      if (status === 'applied') framingMutations.push(state)
    }
  }
).sweep()
assert.equal(framingSweep.framingCompleteCount, 1)
assert.equal((await framingStore.get(framingScope, now)).phase, 'CLOSED', '完整 framing 即使状态码不可信，也应证明 transport circuit 可立即清除 SUSPECT')
assert.deepEqual(framingMutations.map((state) => state.phase), ['SUSPECT', 'CLOSED'], '后台 confirmation 的 lease 与终态都必须交给 control-plane mutation observer')

const taskFailureStore = new MemoryAccountCircuitStore({ capacity: 10, now: () => now })
const taskFailureScope = scope('suspect-task-failure')
await taskFailureStore.suspect({
  scope: taskFailureScope,
  dispatchRevision: 'r1',
  transitionId: createId(),
  reason: 'timeout',
  confirmationFailuresRequired: 2,
  nowMs: now
})
now += 3_000
const taskFailureSweep = await service(taskFailureStore, async () => ({
  dispatchRevision: 'r1',
  probe: async () => ({ kind: 'unknown', failureKind: 'task_failure' })
})).sweep()
assert.equal(taskFailureSweep.unknownCount, 1)
const taskFailureState = await taskFailureStore.get(taskFailureScope, now)
assert.equal(taskFailureState.phase, 'SUSPECT')
assert.equal(taskFailureState.confirmationFailureCount, 0, 'probe task failure 不得伪造上游传输失败证据')
assert.equal(taskFailureState.backoffAttempt, 1)
assert.equal(taskFailureState.retryAtMs, now + 3_000, '首次 unknown 只延后 3 秒，不改变账户失败计数')
assert.equal(taskFailureState.lease, undefined)
assert.equal((await taskFailureStore.listDue(now, 10)).length, 0, 'unknown 结果必须延后下一轮而非同一 sweep 热循环')
now += 3_000
const secondTaskFailureSweep = await service(taskFailureStore, async () => ({
  dispatchRevision: 'r1',
  probe: async () => ({ kind: 'unknown', failureKind: 'task_failure' })
})).sweep()
assert.equal(secondTaskFailureSweep.unknownCount, 1)
const secondTaskFailureState = await taskFailureStore.get(taskFailureScope, now)
assert.equal(secondTaskFailureState.confirmationFailureCount, 0)
assert.equal(secondTaskFailureState.backoffAttempt, 2)
assert.equal(secondTaskFailureState.retryAtMs, now + 5_000, '持续 unknown 必须进入渐进退避，不能每 3 秒探测一次')
assert.equal((await taskFailureStore.listDue(now + 4_999, 10)).length, 0)
assert.equal((await taskFailureStore.listDue(now + 5_000, 10)).length, 1)

now += 5_000
const taskFailureLeaseId = createId()
assert.equal((await taskFailureStore.acquireConfirmationLease({
  scope: taskFailureScope,
  generation: secondTaskFailureState.generation,
  dispatchRevision: 'r1',
  transitionId: createId(),
  leaseId: taskFailureLeaseId,
  leaseUntilMs: now + 1_000,
  nowMs: now
})).status, 'applied')
const taskFailureConfirmed = await taskFailureStore.completeConfirmation({
  scope: taskFailureScope,
  generation: secondTaskFailureState.generation,
  dispatchRevision: 'r1',
  transitionId: createId(),
  leaseId: taskFailureLeaseId,
  outcome: 'transport_failure',
  failureEvidenceKey: 'f'.repeat(64),
  nowMs: now
})
assert.equal(taskFailureConfirmed.state.confirmationFailureCount, 1)
assert.equal(taskFailureConfirmed.state.backoffAttempt, 0, '真实独立传输证据必须清除内部 unknown 退避轮次')
assert.equal(taskFailureConfirmed.state.retryAtMs, now + 3_000)

const expiredLeaseStore = new MemoryAccountCircuitStore({ capacity: 10, now: () => now })
const expiredLeaseScope = scope('expired-foreground-lease')
const expiredLeaseSuspect = await expiredLeaseStore.suspect({
  scope: expiredLeaseScope,
  dispatchRevision: 'r1',
  transitionId: createId(),
  reason: 'timeout',
  confirmationFailuresRequired: 2,
  nowMs: now
})
now = requiredRetryAt(expiredLeaseSuspect.state, '过期 lease 场景必须先推进到 confirmation retryAt')
assert.equal((await expiredLeaseStore.acquireConfirmationLease({
  scope: expiredLeaseScope,
  generation: expiredLeaseSuspect.state.generation,
  dispatchRevision: 'r1',
  transitionId: createId(),
  leaseId: createId(),
  leaseUntilMs: now + 1_000,
  nowMs: now
})).status, 'applied')
now += 1_000
let expiredLeaseProbeCount = 0
const expiredLeaseResolver: AccountCircuitRecoveryTargetResolver = async () => ({
  dispatchRevision: 'r1',
  probe: async () => {
    expiredLeaseProbeCount += 1
    return { kind: 'transport_incomplete', failureKind: 'timeout' }
  }
})
const [expiredLeaseSweepA, expiredLeaseSweepB] = await Promise.all([
  service(expiredLeaseStore, expiredLeaseResolver).sweep(),
  service(expiredLeaseStore, expiredLeaseResolver).sweep()
])
assert.equal(expiredLeaseProbeCount, 1, '过期前台 confirmation lease 必须可被一个后台 worker 原子接管')
assert.equal(expiredLeaseSweepA.leasedCount + expiredLeaseSweepB.leasedCount, 1)

const suspectRevisionStore = new MemoryAccountCircuitStore({ capacity: 10, now: () => now })
const suspectRevisionScope = scope('suspect-stale-result')
await suspectRevisionStore.suspect({
  scope: suspectRevisionScope,
  dispatchRevision: '1',
  transitionId: createId(),
  reason: 'timeout',
  confirmationFailuresRequired: 2,
  nowMs: now
})
now += 3_000
let suspectProbeStarted!: () => void
let completeSuspectProbe!: (outcome: TransportProbeOutcome) => void
const suspectProbeStartedPromise = new Promise<void>((resolve) => { suspectProbeStarted = resolve })
const suspectProbePromise = new Promise<TransportProbeOutcome>((resolve) => { completeSuspectProbe = resolve })
const suspectRevisionSweepPromise = service(suspectRevisionStore, async () => ({
  dispatchRevision: '1',
  probe: async () => {
    suspectProbeStarted()
    return await suspectProbePromise
  }
})).sweep()
await suspectProbeStartedPromise
await suspectRevisionStore.replaceDispatchRevision({
  scope: suspectRevisionScope,
  dispatchRevision: '2',
  transitionId: createId(),
  nowMs: now
})
completeSuspectProbe({ kind: 'transport_incomplete', failureKind: 'connection' })
const suspectRevisionSweep = await suspectRevisionSweepPromise
assert.equal(suspectRevisionSweep.fencedCount, 1, '旧 generation/revision 的后台 confirmation 结果必须被拒绝')
const suspectRevisionState = await suspectRevisionStore.get(suspectRevisionScope, now)
assert.equal(suspectRevisionState.phase, 'CLOSED')
assert.equal(suspectRevisionState.dispatchRevision, '2')
assert.equal(suspectRevisionState.confirmationFailureCount, undefined)

// An account parent must be recoverable from its generic transport canary even
// when one of the child protocol/model scopes has no traffic. Child circuits
// remain shadowed and are restored independently after the parent closes.
const parentRecoveryStore = new MemoryAccountCircuitStore({ capacity: 20, now: () => now })
const parentScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'parent-recovery' }
const parentChildScopes = [scope('parent-recovery'), { ...scope('parent-recovery'), modelBucket: 'gpt-5-mini' }]
const parentRequiredKeys = parentChildScopes.map((child) => accountCircuitScopeKey(child))
await parentRecoveryStore.restore({
  scopeKey: accountCircuitScopeKey(parentScope),
  scope: parentScope,
  phase: 'OPEN',
  generation: 1,
  dispatchRevision: 'r1',
  transitionId: 'parent-open',
  backoffAttempt: 1,
  recoverySuccessCount: 0,
  retryAtMs: now,
  requiredRecoveryScopeKeys: parentRequiredKeys,
  recoveryEvidenceScopeKeys: [],
  childScopeKeys: parentRequiredKeys,
  updatedAtMs: now
}, now)
const parentRecoveryService = service(parentRecoveryStore, async () => ({
  dispatchRevision: 'r1',
  probe: async () => ({ kind: 'framing_complete', statusCode: 503 } as TransportProbeOutcome)
}))
for (let index = 0; index < 4; index += 1) {
  await parentRecoveryService.sweep()
  now += 3_000
}
assert.equal((await parentRecoveryStore.get(parentScope, now)).phase, 'CLOSED', '父账户恢复不得等待没有流量的子 scope 证据')

const resolverCalls: string[] = []
const resolverAccount = { id: 'owner-1', systemAccountId: 'owner-system', boundGroupId: 'group-1' } as never
const resolverCandidate = {
  id: 'owner-1',
  dispatchRevision: 7,
  accountAccessType: 'owner',
  bindingSystemAccountId: undefined,
  boundGroupId: 'group-1'
} as never
const resolver = createScheduledAccountCircuitRecoveryResolver({
  findAccountForTest: async (accountId, access) => {
    resolverCalls.push(`account:${accountId}:${access?.systemAccountId ?? 'owner'}`)
    return accountId === 'owner-1' || accountId === 'authorized-1' ? resolverAccount : undefined
  },
  findOpenAIAccountForGroup: async (groupId, accountId, systemAccountId) => {
    resolverCalls.push(`candidate:${groupId}:${accountId}:${systemAccountId}`)
    if (accountId === 'authorized-1') {
      return {
        id: accountId,
        dispatchRevision: 8,
        accountAccessType: 'account_authorized',
        bindingSystemAccountId: systemAccountId,
        boundGroupId: groupId,
        accountAuthorizationId: 'grant-1'
      } as never
    }
    return resolverCandidate
  },
  probe: async ({ signal }) => signal.aborted
    ? { kind: 'unknown', failureKind: 'canceled' }
    : { kind: 'framing_complete', statusCode: 503 }
})
const ownerTarget = await resolver(recoveryState(scope('owner-1')), new AbortController().signal)
assert.equal(ownerTarget?.dispatchRevision, '7', 'owner resolver 必须使用当前账户 dispatchRevision')
const authorizedTarget = await resolver(recoveryState(scope('authorized-1:authorized:grantee-1:group-2:grant-1')), new AbortController().signal)
assert.equal(authorizedTarget?.dispatchRevision, '8', 'authorized resolver 必须使用当前授权实例 revision')
assert(resolverCalls.includes('account:authorized-1:grantee-1'), 'authorized resolver 必须带授权使用方访问上下文')
const canceledController = new AbortController()
canceledController.abort()
assert.equal(await resolver(recoveryState(scope('owner-1')), canceledController.signal), undefined, '取消的 resolver 不应触发数据库解析')
const mismatchResolver = createScheduledAccountCircuitRecoveryResolver({
  findAccountForTest: async () => resolverAccount,
  findOpenAIAccountForGroup: async () => ({
    id: 'different-account',
    dispatchRevision: 7,
    accountAccessType: 'owner',
    boundGroupId: 'group-1'
  } as never),
  probe: async () => ({ kind: 'framing_complete', statusCode: 200 })
})
assert.equal(await mismatchResolver(recoveryState(scope('owner-1')), new AbortController().signal), undefined, 'runtime key 绑定变化必须在探针前拒绝目标')

console.log('account-circuit-recovery-regression passed')

function service(
  targetStore: MemoryAccountCircuitStore,
  resolver: AccountCircuitRecoveryTargetResolver
): AccountCircuitRecoveryService {
  return new AccountCircuitRecoveryService(targetStore, resolver, {
    batchSize: 20,
    leaseDurationMs: 10_000,
    now: () => now,
    createId
  })
}

function recoveryState(targetScope: AccountCircuitScope): never {
  return { scope: targetScope, phase: 'OPEN', generation: 1, dispatchRevision: '1', retryAtMs: 0 } as never
}

function scope(accountRuntimeKey: string): Extract<AccountCircuitScope, { kind: 'protocol_model' }> {
  return {
    kind: 'protocol_model',
    accountRuntimeKey,
    protocolProfile: 'openai_v1',
    requestLane: 'text',
    modelBucket: 'gpt-5'
  }
}

async function openCircuit(
  targetStore: MemoryAccountCircuitStore,
  targetScope: AccountCircuitScope,
  dispatchRevision: string,
  atMs: number
): Promise<number> {
  const initialFailureEvidenceKey = evidenceKey(`${accountCircuitScopeKey(targetScope)}:initial`)
  const confirmationEvidenceKeys = [
    evidenceKey(`${accountCircuitScopeKey(targetScope)}:confirmation-a`),
    evidenceKey(`${accountCircuitScopeKey(targetScope)}:confirmation-b`)
  ]
  const suspected = await targetStore.suspect({
    scope: targetScope,
    dispatchRevision,
    transitionId: createId(),
    reason: 'timeout',
    confirmationFailuresRequired: 2,
    failureEvidenceKey: initialFailureEvidenceKey,
    nowMs: atMs
  })
  assert.equal(suspected.status, 'applied')
  assert.equal(suspected.state.phase, 'SUSPECT')
  assert.equal(suspected.state.confirmationFailuresRequired, 2, '新 incident 不得回退到旧的一次确认阈值')
  assert.deepEqual(suspected.state.failureEvidenceKeys, [initialFailureEvidenceKey])

  let confirmationAtMs = requiredRetryAt(suspected.state, 'SUSPECT 必须安排独立 confirmation 的 retryAt')
  let expectedFailureEvidenceKey = initialFailureEvidenceKey
  for (const [index, failureEvidenceKey] of confirmationEvidenceKeys.entries()) {
    const leaseId = createId()
    const acquired = await targetStore.acquireConfirmationLease({
      scope: targetScope,
      generation: suspected.state.generation,
      dispatchRevision,
      transitionId: createId(),
      leaseId,
      leaseUntilMs: confirmationAtMs + 1_000,
      expectedFailureEvidenceKey,
      confirmationEvidenceKey: failureEvidenceKey,
      nowMs: confirmationAtMs
    })
    assert.equal(acquired.status, 'applied', '到期且带独立 evidence 的 confirmation 必须取得 lease')

    const completed = await targetStore.completeConfirmation({
      scope: targetScope,
      generation: suspected.state.generation,
      dispatchRevision,
      transitionId: createId(),
      leaseId,
      outcome: 'transport_failure',
      failureEvidenceKey,
      nowMs: confirmationAtMs
    })
    assert.equal(completed.status, 'applied')
    assert.equal(completed.state.phase, index === 0 ? 'SUSPECT' : 'OPEN')
    assert.equal(completed.state.confirmationFailureCount, index + 1)
    assert.deepEqual(
      completed.state.failureEvidenceKeys,
      [initialFailureEvidenceKey, ...confirmationEvidenceKeys.slice(0, index + 1)]
    )
    expectedFailureEvidenceKey = failureEvidenceKey
    if (index === 0) {
      confirmationAtMs = requiredRetryAt(completed.state, '首次 confirmation 后必须安排第二份独立 confirmation')
    }
  }
  return confirmationAtMs
}

function evidenceKey(seed: string): string {
  return createHash('sha256').update(seed).digest('hex')
}

function requiredRetryAt(state: AccountCircuitState, message: string): number {
  assert.equal(typeof state.retryAtMs, 'number', message)
  if (typeof state.retryAtMs !== 'number') throw new Error(message)
  return state.retryAtMs
}
