import assert from 'node:assert/strict'

import type { TransportProbeOutcome } from '../../modules/accounts/automatic-account-probe-outcome.js'
import {
  AccountCircuitRecoveryService,
  createScheduledAccountCircuitRecoveryResolver,
  type AccountCircuitRecoveryTargetResolver
} from '../../modules/background/account-circuit-recovery.service.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import type { AccountCircuitScope } from '../../modules/gateway/runtime/account-circuit-store.js'

let now = 10_000
let idSequence = 0
const createId = () => `recovery-${++idSequence}`
const store = new MemoryAccountCircuitStore({ capacity: 20, now: () => now })

const recoveringScope = scope('recovering')
await openCircuit(store, recoveringScope, 'r1', now)
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
await openCircuit(store, singleFlightScope, 'r1', now)
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
await openCircuit(store, failureScope, 'r1', now)
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
await openCircuit(store, unknownScope, 'r1', now)
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
assert.equal(afterUnknown.backoffAttempt, beforeUnknown.backoffAttempt, 'unknown 不得推进退避')

const revisionScope = scope('revision')
await openCircuit(store, revisionScope, 'old-revision', now)
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
await openCircuit(store, staleScope, 'r1', now)
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
await openCircuit(store, missingScope, 'r1', now)
now += 3_000
const missingSweep = await service(store, async (state) => state.scope.accountRuntimeKey === 'missing-target'
  ? undefined
  : {
      dispatchRevision: 'r1',
      probe: async () => ({ kind: 'framing_complete', statusCode: 200 })
    }).sweep()
assert(missingSweep.unknownCount >= 1)
assert.equal((await store.get(missingScope, now)).phase, 'OPEN', 'resolver 缺失目标时必须保守释放到 OPEN')

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
): Promise<void> {
  const suspected = await targetStore.suspect({
    scope: targetScope,
    dispatchRevision,
    transitionId: createId(),
    reason: 'timeout',
    nowMs: atMs
  })
  const leaseId = createId()
  await targetStore.acquireConfirmationLease({
    scope: targetScope,
    generation: suspected.state.generation,
    dispatchRevision,
    transitionId: createId(),
    leaseId,
    leaseUntilMs: atMs + 1_000,
    nowMs: atMs
  })
  const opened = await targetStore.completeConfirmation({
    scope: targetScope,
    generation: suspected.state.generation,
    dispatchRevision,
    transitionId: createId(),
    leaseId,
    outcome: 'transport_failure',
    nowMs: atMs
  })
  assert.equal(opened.state.phase, 'OPEN')
}
