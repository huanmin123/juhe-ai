import assert from 'node:assert/strict'

import { closeRedisClients, getRedisClient } from '../../shared/redis-client.js'
import {
  RedisAccountCircuitStore,
  redisAccountCircuitStoreKeys
} from '../../modules/gateway/runtime/account-circuit-redis-store.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import { AccountCircuitRecoveryService } from '../../modules/background/account-circuit-recovery.service.js'
import {
  accountCircuitScopeKey,
  type AccountCircuitScope,
  type AccountCircuitState
} from '../../modules/gateway/runtime/account-circuit-store.js'

const redisUrl = process.env.JUHE_AI_TEST_REDIS_URL?.trim()
if (!redisUrl) {
  console.log('account-circuit-redis-smoke skipped: JUHE_AI_TEST_REDIS_URL 未配置')
  process.exit(0)
}

let idSequence = 0
const name = `regression-${process.pid}-${Date.now()}`
const keys = redisAccountCircuitStoreKeys(name)
const parityName = `${name}-parity`
const parityKeys = redisAccountCircuitStoreKeys(parityName)
const observerParityName = `${name}-observer-parity`
const observerParityKeys = redisAccountCircuitStoreKeys(observerParityName)
const evidenceName = `${name}-evidence`
const evidenceKeys = redisAccountCircuitStoreKeys(evidenceName)
const dueName = `${name}-due`
const dueKeys = redisAccountCircuitStoreKeys(dueName)
const revisionName = `${name}-revision`
const revisionKeys = redisAccountCircuitStoreKeys(revisionName)
const backgroundName = `${name}-background-confirmation`
const backgroundKeys = redisAccountCircuitStoreKeys(backgroundName)
const capacityName = `${name}-capacity`
const capacityKeys = redisAccountCircuitStoreKeys(capacityName)
const jitterName = `${name}-jitter`
const jitterKeys = redisAccountCircuitStoreKeys(jitterName)
const tombstoneBoundaryName = `${name}-tombstone-boundary`
const tombstoneBoundaryKeys = redisAccountCircuitStoreKeys(tombstoneBoundaryName)
const revisionScanName = `${name}-revision-scan`
const revisionScanKeys = redisAccountCircuitStoreKeys(revisionScanName)
const relationshipRestoreName = `${name}-relationship-restore`
const relationshipRestoreKeys = redisAccountCircuitStoreKeys(relationshipRestoreName)
const redis = await getRedisClient(redisUrl)
await redis.sendCommand([
  'DEL',
  keys.states,
  keys.due,
  keys.closed,
  keys.escalation,
  parityKeys.states,
  parityKeys.due,
  parityKeys.closed,
  parityKeys.escalation,
  observerParityKeys.states,
  observerParityKeys.due,
  observerParityKeys.closed,
  observerParityKeys.escalation,
  observerParityKeys.capacitySaturated,
  evidenceKeys.states,
  evidenceKeys.due,
  evidenceKeys.closed,
  evidenceKeys.escalation,
  dueKeys.states,
  dueKeys.due,
  dueKeys.closed,
  dueKeys.escalation,
  revisionKeys.states,
  revisionKeys.due,
  revisionKeys.closed,
  revisionKeys.escalation,
  backgroundKeys.states,
  backgroundKeys.due,
  backgroundKeys.closed,
  backgroundKeys.escalation,
  backgroundKeys.capacitySaturated,
  capacityKeys.states,
  capacityKeys.due,
  capacityKeys.closed,
  capacityKeys.escalation,
  capacityKeys.capacitySaturated,
  jitterKeys.states,
  jitterKeys.due,
  jitterKeys.closed,
  jitterKeys.escalation,
  jitterKeys.capacitySaturated,
  tombstoneBoundaryKeys.states,
  tombstoneBoundaryKeys.due,
  tombstoneBoundaryKeys.closed,
  tombstoneBoundaryKeys.escalation,
  tombstoneBoundaryKeys.capacitySaturated,
  revisionScanKeys.states,
  revisionScanKeys.due,
  revisionScanKeys.closed,
  revisionScanKeys.escalation,
  revisionScanKeys.capacitySaturated,
  relationshipRestoreKeys.states,
  relationshipRestoreKeys.due,
  relationshipRestoreKeys.closed,
  relationshipRestoreKeys.escalation,
  relationshipRestoreKeys.capacitySaturated
])

try {
  let now = 100_000
  const memoryParity = new MemoryAccountCircuitStore({ capacity: 4, closedRetentionMs: 100, now: () => now })
  const redisParity = new RedisAccountCircuitStore({ redisUrl, name: parityName, capacity: 4, closedRetentionMs: 100, now: () => now })
  const parityScope: AccountCircuitScope = {
    kind: 'protocol_model',
    accountRuntimeKey: 'parity',
    protocolProfile: 'profile_openai_v1',
    requestLane: 'text',
    modelBucket: 'gpt-5'
  }
  const paritySteps = [
    () => Promise.all([
      memoryParity.suspect({ scope: parityScope, dispatchRevision: 'p1', transitionId: 'p-suspect', reason: 'timeout', failureEvidenceKey: 'a'.repeat(64), nowMs: now }),
      redisParity.suspect({ scope: parityScope, dispatchRevision: 'p1', transitionId: 'p-suspect', reason: 'timeout', failureEvidenceKey: 'a'.repeat(64), nowMs: now })
    ]),
    () => Promise.all([
      memoryParity.acquireConfirmationLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-stale-evidence-acquire', leaseId: 'p-stale-evidence', leaseUntilMs: now + 1_000, expectedFailureEvidenceKey: 'f'.repeat(64), nowMs: now }),
      redisParity.acquireConfirmationLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-stale-evidence-acquire', leaseId: 'p-stale-evidence', leaseUntilMs: now + 1_000, expectedFailureEvidenceKey: 'f'.repeat(64), nowMs: now })
    ]),
    () => {
      now += 3_000
      return Promise.all([
      memoryParity.acquireConfirmationLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-acquire', leaseId: 'p-confirm', leaseUntilMs: now + 1_000, expectedFailureEvidenceKey: 'a'.repeat(64), nowMs: now }),
      redisParity.acquireConfirmationLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-acquire', leaseId: 'p-confirm', leaseUntilMs: now + 1_000, expectedFailureEvidenceKey: 'a'.repeat(64), nowMs: now })
      ])
    },
    () => Promise.all([
      memoryParity.completeConfirmation({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-fail', leaseId: 'p-confirm', outcome: 'transport_failure', failureEvidenceKey: 'b'.repeat(64), nowMs: now }),
      redisParity.completeConfirmation({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-fail', leaseId: 'p-confirm', outcome: 'transport_failure', failureEvidenceKey: 'b'.repeat(64), nowMs: now })
    ]),
    () => {
      now += 3_000
      return Promise.all([
      memoryParity.acquireConfirmationLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-acquire-2', leaseId: 'p-confirm-2', leaseUntilMs: now + 1_000, nowMs: now }),
      redisParity.acquireConfirmationLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-acquire-2', leaseId: 'p-confirm-2', leaseUntilMs: now + 1_000, nowMs: now })
      ])
    },
    () => Promise.all([
      memoryParity.completeConfirmation({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-fail-2', leaseId: 'p-confirm-2', outcome: 'transport_failure', failureEvidenceKey: 'c'.repeat(64), nowMs: now }),
      redisParity.completeConfirmation({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-confirm-fail-2', leaseId: 'p-confirm-2', outcome: 'transport_failure', failureEvidenceKey: 'c'.repeat(64), nowMs: now })
    ])
  ]
  for (const step of paritySteps) {
    const [memoryResult, redisResult] = await step()
    assert.deepEqual(normalizedResult(redisResult), normalizedResult(memoryResult), 'Redis 与 memory 状态转换必须一致')
  }
  now += 3_000
  const [memoryCanary, redisCanary] = await Promise.all([
    memoryParity.acquireCanaryLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-canary-acquire', leaseId: 'p-canary', leaseUntilMs: now + 1_000, nowMs: now }),
    redisParity.acquireCanaryLease({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-canary-acquire', leaseId: 'p-canary', leaseUntilMs: now + 1_000, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisCanary), normalizedResult(memoryCanary), 'Redis 与 memory canary acquire 必须一致')
  const [memoryRecovery, redisRecovery] = await Promise.all([
    memoryParity.completeCanary({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-canary-success', leaseId: 'p-canary', outcome: 'framing_complete', nowMs: now }),
    redisParity.completeCanary({ scope: parityScope, generation: 1, dispatchRevision: 'p1', transitionId: 'p-canary-success', leaseId: 'p-canary', outcome: 'framing_complete', nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisRecovery), normalizedResult(memoryRecovery), 'Redis 与 memory recovery 推进必须一致')
  assert.equal(memoryRecovery.state.recoverySuccessCount, 0, '首次 half-open 成功只进入 RECOVERING，不计入三次 canary')

  const memoryObserverParity = new MemoryAccountCircuitStore({ capacity: 8, closedRetentionMs: 100, now: () => now })
  const redisObserverParity = new RedisAccountCircuitStore({ redisUrl, name: observerParityName, capacity: 8, closedRetentionMs: 100, now: () => now })
  const observerParityScope: Extract<AccountCircuitScope, { kind: 'protocol_model' }> = {
    kind: 'protocol_model',
    accountRuntimeKey: 'observer-parity',
    protocolProfile: 'profile_openai_v1',
    requestLane: 'text',
    modelBucket: 'gpt-5'
  }
  const observerInitialEvidence = 'a'.repeat(64)
  const observerConfirmationEvidence = 'b'.repeat(64)
  const observerSuccessEvidence = 'c'.repeat(64)
  const [memoryObserverSuspect, redisObserverSuspect] = await Promise.all([
    memoryObserverParity.suspect({ scope: observerParityScope, dispatchRevision: 'o1', transitionId: 'observer-suspect-1', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now }),
    redisObserverParity.suspect({ scope: observerParityScope, dispatchRevision: 'o1', transitionId: 'observer-suspect-1', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisObserverSuspect), normalizedResult(memoryObserverSuspect))
  const observerIdentity = { scope: observerParityScope, generation: 1, dispatchRevision: 'o1' }
  const [memoryNotDue, redisNotDue] = await Promise.all([
    memoryObserverParity.acquireConfirmationLease({ ...observerIdentity, transitionId: 'observer-not-due', leaseId: 'observer-not-due', leaseUntilMs: now + 1_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerConfirmationEvidence, nowMs: now }),
    redisObserverParity.acquireConfirmationLease({ ...observerIdentity, transitionId: 'observer-not-due', leaseId: 'observer-not-due', leaseUntilMs: now + 1_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerConfirmationEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisNotDue), normalizedResult(memoryNotDue))
  assert.equal(memoryNotDue.status, 'not_due')
  const [memorySameEvidence, redisSameEvidence] = await Promise.all([
    memoryObserverParity.acquireConfirmationLease({ ...observerIdentity, transitionId: 'observer-same-evidence', leaseId: 'observer-same-evidence', leaseUntilMs: now + 4_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerInitialEvidence, nowMs: now + 3_000 }),
    redisObserverParity.acquireConfirmationLease({ ...observerIdentity, transitionId: 'observer-same-evidence', leaseId: 'observer-same-evidence', leaseUntilMs: now + 4_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerInitialEvidence, nowMs: now + 3_000 })
  ])
  assert.deepEqual(normalizedResult(redisSameEvidence), normalizedResult(memorySameEvidence))
  assert.equal(memorySameEvidence.status, 'state_mismatch')
  const [memoryNotDueObserverClose, redisNotDueObserverClose] = await Promise.all([
    memoryObserverParity.closeSuspectFromObserver({ ...observerIdentity, transitionId: 'observer-not-due-close', expectedFailureEvidenceKey: observerInitialEvidence, observerEvidenceKey: observerSuccessEvidence, nowMs: now }),
    redisObserverParity.closeSuspectFromObserver({ ...observerIdentity, transitionId: 'observer-not-due-close', expectedFailureEvidenceKey: observerInitialEvidence, observerEvidenceKey: observerSuccessEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisNotDueObserverClose), normalizedResult(memoryNotDueObserverClose))
  assert.equal(memoryNotDueObserverClose.state.phase, 'CLOSED')

  const [memoryLeaseRaceSuspect, redisLeaseRaceSuspect] = await Promise.all([
    memoryObserverParity.suspect({ scope: observerParityScope, dispatchRevision: 'o1', transitionId: 'observer-suspect-2', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now }),
    redisObserverParity.suspect({ scope: observerParityScope, dispatchRevision: 'o1', transitionId: 'observer-suspect-2', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisLeaseRaceSuspect), normalizedResult(memoryLeaseRaceSuspect))
  now += 3_000
  const leaseRaceIdentity = { scope: observerParityScope, generation: 2, dispatchRevision: 'o1' }
  const memoryLeaseRace = await Promise.all(['a', 'b'].map((suffix) => memoryObserverParity.acquireConfirmationLease({
    ...leaseRaceIdentity,
    transitionId: `observer-lease-race-${suffix}`,
    leaseId: `observer-lease-${suffix}`,
    leaseUntilMs: now + 1_000,
    expectedFailureEvidenceKey: observerInitialEvidence,
    confirmationEvidenceKey: observerConfirmationEvidence,
    nowMs: now
  })))
  const redisLeaseRace = await Promise.all(['a', 'b'].map((suffix) => redisObserverParity.acquireConfirmationLease({
    ...leaseRaceIdentity,
    transitionId: `observer-lease-race-${suffix}`,
    leaseId: `observer-lease-${suffix}`,
    leaseUntilMs: now + 1_000,
    expectedFailureEvidenceKey: observerInitialEvidence,
    confirmationEvidenceKey: observerConfirmationEvidence,
    nowMs: now
  })))
  assert.equal(memoryLeaseRace.filter((result) => result.status === 'applied').length, 1)
  assert.equal(redisLeaseRace.filter((result) => result.status === 'applied').length, 1)
  const memoryLeaseRaceId = memoryLeaseRace[0]!.status === 'applied' ? 'observer-lease-a' : 'observer-lease-b'
  const redisLeaseRaceId = redisLeaseRace[0]!.status === 'applied' ? 'observer-lease-a' : 'observer-lease-b'
  const [memoryObserverWon, redisObserverWon] = await Promise.all([
    memoryObserverParity.closeSuspectFromObserver({ ...leaseRaceIdentity, transitionId: 'observer-wins-race', expectedFailureEvidenceKey: observerInitialEvidence, observerEvidenceKey: observerSuccessEvidence, nowMs: now }),
    redisObserverParity.closeSuspectFromObserver({ ...leaseRaceIdentity, transitionId: 'observer-wins-race', expectedFailureEvidenceKey: observerInitialEvidence, observerEvidenceKey: observerSuccessEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisObserverWon), normalizedResult(memoryObserverWon))
  assert.equal(memoryObserverWon.state.phase, 'CLOSED')
  const [memoryLateConfirmation, redisLateConfirmation] = await Promise.all([
    memoryObserverParity.completeConfirmation({ ...leaseRaceIdentity, transitionId: 'observer-late-confirmation', leaseId: memoryLeaseRaceId, outcome: 'transport_failure', failureEvidenceKey: observerConfirmationEvidence, nowMs: now }),
    redisObserverParity.completeConfirmation({ ...leaseRaceIdentity, transitionId: 'observer-late-confirmation', leaseId: redisLeaseRaceId, outcome: 'transport_failure', failureEvidenceKey: observerConfirmationEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisLateConfirmation), normalizedResult(memoryLateConfirmation))
  assert.equal(memoryLateConfirmation.status, 'state_mismatch')

  const [memoryRotationSuspect, redisRotationSuspect] = await Promise.all([
    memoryObserverParity.suspect({ scope: observerParityScope, dispatchRevision: 'o1', transitionId: 'observer-suspect-3', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now }),
    redisObserverParity.suspect({ scope: observerParityScope, dispatchRevision: 'o1', transitionId: 'observer-suspect-3', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisRotationSuspect), normalizedResult(memoryRotationSuspect))
  const rotationIdentity = { scope: observerParityScope, generation: 3, dispatchRevision: 'o1' }
  const [memoryRotationClosed, redisRotationClosed] = await Promise.all([
    memoryObserverParity.closeSuspectFromKeyRotation({ ...rotationIdentity, transitionId: 'observer-key-rotation-close', expectedFailureEvidenceKey: observerInitialEvidence, nowMs: now }),
    redisObserverParity.closeSuspectFromKeyRotation({ ...rotationIdentity, transitionId: 'observer-key-rotation-close', expectedFailureEvidenceKey: observerInitialEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisRotationClosed), normalizedResult(memoryRotationClosed))
  assert.equal(memoryRotationClosed.state.phase, 'CLOSED')

  const confirmationCloseScope: Extract<AccountCircuitScope, { kind: 'protocol_model' }> = {
    ...observerParityScope,
    accountRuntimeKey: 'confirmation-key-rotation-close-parity'
  }
  const [memoryConfirmationCloseSuspect, redisConfirmationCloseSuspect] = await Promise.all([
    memoryObserverParity.suspect({ scope: confirmationCloseScope, dispatchRevision: 'o1', transitionId: 'confirmation-close-suspect', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now - 3_000 }),
    redisObserverParity.suspect({ scope: confirmationCloseScope, dispatchRevision: 'o1', transitionId: 'confirmation-close-suspect', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now - 3_000 })
  ])
  assert.deepEqual(normalizedResult(redisConfirmationCloseSuspect), normalizedResult(memoryConfirmationCloseSuspect))
  const confirmationCloseIdentity = { scope: confirmationCloseScope, generation: 1, dispatchRevision: 'o1' }
  const [memoryConfirmationCloseLease, redisConfirmationCloseLease] = await Promise.all([
    memoryObserverParity.acquireConfirmationLease({ ...confirmationCloseIdentity, transitionId: 'confirmation-close-acquire', leaseId: 'confirmation-close-lease', leaseUntilMs: now + 1_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerConfirmationEvidence, nowMs: now }),
    redisObserverParity.acquireConfirmationLease({ ...confirmationCloseIdentity, transitionId: 'confirmation-close-acquire', leaseId: 'confirmation-close-lease', leaseUntilMs: now + 1_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerConfirmationEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisConfirmationCloseLease), normalizedResult(memoryConfirmationCloseLease))
  const [memoryConfirmationClosed, redisConfirmationClosed] = await Promise.all([
    memoryObserverParity.completeConfirmation({ ...confirmationCloseIdentity, transitionId: 'confirmation-close-complete', leaseId: 'confirmation-close-lease', outcome: 'framing_complete', framingCompleteDisposition: 'closed', nowMs: now }),
    redisObserverParity.completeConfirmation({ ...confirmationCloseIdentity, transitionId: 'confirmation-close-complete', leaseId: 'confirmation-close-lease', outcome: 'framing_complete', framingCompleteDisposition: 'closed', nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisConfirmationClosed), normalizedResult(memoryConfirmationClosed))
  assert.equal(memoryConfirmationClosed.state.phase, 'CLOSED', '跨 Key confirmation 完整 framing 必须在 Memory 与 Redis 都直接关闭')
  assert.equal(memoryConfirmationClosed.state.lease, undefined)

  const [memoryUnknownSuspect, redisUnknownSuspect] = await Promise.all([
    memoryObserverParity.suspect({ scope: observerParityScope, dispatchRevision: 'o1', transitionId: 'observer-suspect-4', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now }),
    redisObserverParity.suspect({ scope: observerParityScope, dispatchRevision: 'o1', transitionId: 'observer-suspect-4', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisUnknownSuspect), normalizedResult(memoryUnknownSuspect))
  now += 3_000
  const unknownIdentity = { scope: observerParityScope, generation: 4, dispatchRevision: 'o1' }
  const [memoryUnknownLease, redisUnknownLease] = await Promise.all([
    memoryObserverParity.acquireConfirmationLease({ ...unknownIdentity, transitionId: 'observer-unknown-acquire', leaseId: 'observer-unknown-lease', leaseUntilMs: now + 1_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerConfirmationEvidence, nowMs: now }),
    redisObserverParity.acquireConfirmationLease({ ...unknownIdentity, transitionId: 'observer-unknown-acquire', leaseId: 'observer-unknown-lease', leaseUntilMs: now + 1_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerConfirmationEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisUnknownLease), normalizedResult(memoryUnknownLease))
  const [memoryUnknown, redisUnknown] = await Promise.all([
    memoryObserverParity.completeConfirmation({ ...unknownIdentity, transitionId: 'observer-unknown-complete', leaseId: 'observer-unknown-lease', outcome: 'unknown', nowMs: now }),
    redisObserverParity.completeConfirmation({ ...unknownIdentity, transitionId: 'observer-unknown-complete', leaseId: 'observer-unknown-lease', outcome: 'unknown', nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisUnknown), normalizedResult(memoryUnknown))
  assert.equal(memoryUnknown.state.retryAtMs, now + 3_000)
  const [memoryUnknownEarly, redisUnknownEarly] = await Promise.all([
    memoryObserverParity.acquireConfirmationLease({ ...unknownIdentity, transitionId: 'observer-unknown-early', leaseId: 'observer-unknown-early', leaseUntilMs: now + 3_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerSuccessEvidence, nowMs: now + 2_999 }),
    redisObserverParity.acquireConfirmationLease({ ...unknownIdentity, transitionId: 'observer-unknown-early', leaseId: 'observer-unknown-early', leaseUntilMs: now + 3_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerSuccessEvidence, nowMs: now + 2_999 })
  ])
  assert.deepEqual(normalizedResult(redisUnknownEarly), normalizedResult(memoryUnknownEarly))
  assert.equal(memoryUnknownEarly.status, 'not_due')

  const [memoryShadowSuspect, redisShadowSuspect] = await Promise.all([
    memoryObserverParity.closeSuspectFromKeyRotation({ ...unknownIdentity, transitionId: 'observer-unknown-cleanup', expectedFailureEvidenceKey: observerInitialEvidence, nowMs: now }),
    redisObserverParity.closeSuspectFromKeyRotation({ ...unknownIdentity, transitionId: 'observer-unknown-cleanup', expectedFailureEvidenceKey: observerInitialEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisShadowSuspect), normalizedResult(memoryShadowSuspect))
  const [memoryShadowChild, redisShadowChild] = await Promise.all([
    memoryObserverParity.suspect({ scope: observerParityScope, dispatchRevision: 'o1', transitionId: 'observer-suspect-5', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now }),
    redisObserverParity.suspect({ scope: observerParityScope, dispatchRevision: 'o1', transitionId: 'observer-suspect-5', reason: 'transport', failureEvidenceKey: observerInitialEvidence, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisShadowChild), normalizedResult(memoryShadowChild))
  const observerParentScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: observerParityScope.accountRuntimeKey }
  const observerParentState: AccountCircuitState = {
    scope: observerParentScope,
    scopeKey: accountCircuitScopeKey(observerParentScope),
    phase: 'OPEN',
    generation: 1,
    dispatchRevision: 'o1',
    transitionId: 'observer-parent-open',
    incidentId: 'observer-parent-incident',
    childScopeKeys: [memoryShadowChild.state.scopeKey],
    childIncidentIds: [memoryShadowChild.state.incidentId!],
    requiredRecoveryScopeKeys: [memoryShadowChild.state.scopeKey],
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    retryAtMs: now + 30_000,
    updatedAtMs: now
  }
  const [memoryParentRestored, redisParentRestored] = await Promise.all([
    memoryObserverParity.restore(observerParentState, now),
    redisObserverParity.restore(observerParentState, now)
  ])
  assert.deepEqual(normalizedResult(redisParentRestored), normalizedResult(memoryParentRestored))
  const shadowIdentity = { scope: observerParityScope, generation: 5, dispatchRevision: 'o1' }
  for (const [memoryResult, redisResult] of await Promise.all([
    Promise.all([
      memoryObserverParity.acquireConfirmationLease({ ...shadowIdentity, transitionId: 'observer-shadow-acquire', leaseId: 'observer-shadow-lease', leaseUntilMs: now + 4_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerConfirmationEvidence, nowMs: now + 3_000 }),
      redisObserverParity.acquireConfirmationLease({ ...shadowIdentity, transitionId: 'observer-shadow-acquire', leaseId: 'observer-shadow-lease', leaseUntilMs: now + 4_000, expectedFailureEvidenceKey: observerInitialEvidence, confirmationEvidenceKey: observerConfirmationEvidence, nowMs: now + 3_000 })
    ]),
    Promise.all([
      memoryObserverParity.closeSuspectFromObserver({ ...shadowIdentity, transitionId: 'observer-shadow-close', expectedFailureEvidenceKey: observerInitialEvidence, observerEvidenceKey: observerSuccessEvidence, nowMs: now }),
      redisObserverParity.closeSuspectFromObserver({ ...shadowIdentity, transitionId: 'observer-shadow-close', expectedFailureEvidenceKey: observerInitialEvidence, observerEvidenceKey: observerSuccessEvidence, nowMs: now })
    ]),
    Promise.all([
      memoryObserverParity.closeSuspectFromKeyRotation({ ...shadowIdentity, transitionId: 'observer-shadow-rotation', expectedFailureEvidenceKey: observerInitialEvidence, nowMs: now }),
      redisObserverParity.closeSuspectFromKeyRotation({ ...shadowIdentity, transitionId: 'observer-shadow-rotation', expectedFailureEvidenceKey: observerInitialEvidence, nowMs: now })
    ])
  ])) {
    assert.deepEqual(normalizedResult(redisResult), normalizedResult(memoryResult))
    assert.equal(memoryResult.status, 'state_mismatch', '父 incident shadow 后 child acquire/close CAS 必须全部拒绝')
  }

  const evidenceFirst = new RedisAccountCircuitStore({ redisUrl, name: evidenceName, capacity: 8, now: () => now })
  const evidenceSecond = new RedisAccountCircuitStore({ redisUrl, name: evidenceName, capacity: 8, now: () => now })
  const evidenceRuntimeKey = `redis-evidence-${process.pid}`
  const evidenceAccount: AccountCircuitScope = { kind: 'account', accountRuntimeKey: evidenceRuntimeKey }
  const evidenceChildren: Array<Extract<AccountCircuitScope, { kind: 'protocol_model' }>> = [
    { kind: 'protocol_model', accountRuntimeKey: evidenceRuntimeKey, protocolProfile: 'profile_openai_v1', requestLane: 'text', modelBucket: 'model-a' },
    { kind: 'protocol_model', accountRuntimeKey: evidenceRuntimeKey, protocolProfile: 'profile_openai_v1', requestLane: 'text', modelBucket: 'model-b' },
    { kind: 'protocol_model', accountRuntimeKey: evidenceRuntimeKey, protocolProfile: 'profile_openai_v1', requestLane: 'text', modelBucket: 'model-c' },
    { kind: 'protocol_model', accountRuntimeKey: evidenceRuntimeKey, protocolProfile: 'profile_openai_v1', requestLane: 'text', modelBucket: 'model-d' }
  ]
  for (const [index, childScope] of evidenceChildren.entries()) {
    await evidenceFirst.suspect({ scope: childScope, dispatchRevision: 'e1', transitionId: `evidence-suspect-${index}`, reason: 'transport', confirmationFailuresRequired: 1, nowMs: now - 3_000 })
    await evidenceFirst.acquireConfirmationLease({ scope: childScope, generation: 1, dispatchRevision: 'e1', transitionId: `evidence-acquire-${index}`, leaseId: `evidence-lease-${index}`, leaseUntilMs: now + 1_000, nowMs: now })
    await evidenceFirst.completeConfirmation({ scope: childScope, generation: 1, dispatchRevision: 'e1', transitionId: `evidence-open-${index}`, leaseId: `evidence-lease-${index}`, outcome: 'transport_failure', nowMs: now })
  }
  assert.equal((await evidenceFirst.recordProtocolModelOpenEvidence({
    scope: evidenceChildren[0]!, generation: 1, dispatchRevision: 'e1', evidenceId: 'redis-evidence-a', accountTransitionId: 'redis-account-open', reason: 'multiple protocol failures', confirmedFailureCount: 100, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
  })).status, 'recorded')
  assert.equal((await evidenceSecond.recordProtocolModelOpenEvidence({
    scope: evidenceChildren[1]!, generation: 1, dispatchRevision: 'e1', evidenceId: 'redis-evidence-b', accountTransitionId: 'redis-account-open', reason: 'multiple protocol failures', confirmedFailureCount: 100, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
  })).status, 'recorded', 'Redis 不得用两个 scope 的累计失败数升级父级')
  assert.equal((await evidenceSecond.recordProtocolModelOpenEvidence({
    scope: evidenceChildren[0]!, generation: 1, dispatchRevision: 'e1', evidenceId: 'redis-evidence-a-repeated', accountTransitionId: 'redis-account-open', reason: 'repeated protocol failure', confirmedFailureCount: 100, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
  })).status, 'recorded', 'Redis 同一 scope 重复 evidence 不得增加独立 scope 数')
  const redisEscalation = await evidenceFirst.recordProtocolModelOpenEvidence({
    scope: evidenceChildren[2]!, generation: 1, dispatchRevision: 'e1', evidenceId: 'redis-evidence-c', accountTransitionId: 'redis-account-open', reason: 'third distinct protocol failure', confirmedFailureCount: 1, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
  })
  assert.equal(redisEscalation.status, 'escalated')
  assert.equal(redisEscalation.relatedStates?.length, 3)
  for (const childState of redisEscalation.relatedStates ?? []) {
    assert.equal(childState.shadowedByIncidentId, redisEscalation.accountState.incidentId)
    assert.match(childState.transitionId, /^hierarchy:shadow:[a-f0-9]{40}$/)
  }
  const redisIncrementalEscalation = await evidenceSecond.recordProtocolModelOpenEvidence({
    scope: evidenceChildren[3]!, generation: 1, dispatchRevision: 'e1', evidenceId: 'redis-evidence-d', accountTransitionId: 'redis-account-extend', reason: 'fourth distinct protocol failure', confirmedFailureCount: 1, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
  })
  assert.equal(redisIncrementalEscalation.status, 'already_active')
  assert.equal(redisIncrementalEscalation.accountState.transitionId, 'redis-account-extend')
  assert.equal(redisIncrementalEscalation.accountState.childIncidentIds?.length, 4)
  assert.equal(redisIncrementalEscalation.relatedStates?.length, 1)
  assert.equal(redisIncrementalEscalation.relatedStates?.[0]?.scopeKey, accountCircuitScopeKey(evidenceChildren[3]!))
  assert.notEqual(await redis.sendCommand(['HGET', evidenceKeys.escalation, evidenceRuntimeKey]), null)
  const evidenceIdentity = { scope: evidenceAccount, generation: 1, dispatchRevision: 'e1' }
  now += 3_000
  assert.equal((await evidenceFirst.acquireCanaryLease({ ...evidenceIdentity, transitionId: 'evidence-half-open-acquire', leaseId: 'evidence-half-open', leaseUntilMs: now + 1_000, nowMs: now })).status, 'applied')
  assert.equal((await evidenceFirst.completeCanary({ ...evidenceIdentity, transitionId: 'evidence-half-open-complete', leaseId: 'evidence-half-open', outcome: 'framing_complete', nowMs: now })).state.phase, 'RECOVERING')
  let redisClosedParent: Awaited<ReturnType<typeof evidenceFirst.completeCanary>> | undefined
  for (const index of [1, 2, 3]) {
    now += 3_000
    const adapter = index % 2 === 0 ? evidenceSecond : evidenceFirst
    assert.equal((await adapter.acquireCanaryLease({ ...evidenceIdentity, transitionId: `evidence-canary-acquire-${index}`, leaseId: `evidence-canary-${index}`, leaseUntilMs: now + 1_000, nowMs: now })).status, 'applied')
    const completed = await adapter.completeCanary({ ...evidenceIdentity, transitionId: `evidence-canary-complete-${index}`, leaseId: `evidence-canary-${index}`, outcome: 'framing_complete', nowMs: now })
    assert.equal(completed.state.phase, index === 3 ? 'CLOSED' : 'RECOVERING')
    if (index === 3) redisClosedParent = completed
  }
  assert.equal(redisClosedParent?.relatedStates?.length, 4)
  for (const childScope of evidenceChildren) {
    const childState = await evidenceFirst.get(childScope, now)
    assert.equal(childState.shadowedByIncidentId, undefined)
    assert.match(childState.transitionId, /^hierarchy:unshadow:[a-f0-9]{40}$/)
  }
  assert.equal(await redis.sendCommand(['HGET', evidenceKeys.escalation, evidenceRuntimeKey]), null, '父级 CLOSED 与旧升级证据删除必须在同一 Redis 转换中完成')
  const redisPostRecoveryEvidence = await evidenceSecond.recordProtocolModelOpenEvidence({
    scope: evidenceChildren[0]!, generation: 1, dispatchRevision: 'e1', evidenceId: 'redis-evidence-after-recovery', accountTransitionId: 'redis-account-reopen', reason: 'single protocol failure after recovery', confirmedFailureCount: 100, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
  })
  assert.equal(redisPostRecoveryEvidence.status, 'recorded', '父级恢复后单一 child 新失败不能借旧 Redis 证据立即升级')
  assert.equal(redisPostRecoveryEvidence.protocolScopeCount, 1)
  assert.equal((await evidenceFirst.get(evidenceAccount, now)).phase, 'CLOSED')
  assert.equal(await evidenceFirst.clearAccountEscalationEvidence({
    accountRuntimeKey: evidenceRuntimeKey,
    dispatchRevision: 'stale-e0',
    evidenceId: 'stale-framing-complete',
    nowMs: now
  }), false, '旧 revision 的 framing 不得清理当前升级证据')
  assert.notEqual(await redis.sendCommand(['HGET', evidenceKeys.escalation, evidenceRuntimeKey]), null)
  assert.equal(await evidenceSecond.clearAccountEscalationEvidence({
    accountRuntimeKey: evidenceRuntimeKey,
    dispatchRevision: 'e1',
    evidenceId: 'current-framing-complete',
    nowMs: now
  }), true, '当前 revision 的完整 framing 必须原子清理 Redis 升级证据')
  assert.equal(await redis.sendCommand(['HGET', evidenceKeys.escalation, evidenceRuntimeKey]), null)
  const redisEvidenceAfterFraming = await evidenceFirst.recordProtocolModelOpenEvidence({
    scope: evidenceChildren[1]!, generation: 1, dispatchRevision: 'e1', evidenceId: 'redis-evidence-after-framing', accountTransitionId: 'redis-account-reopen-after-framing', reason: 'new failure after framing', confirmedFailureCount: 1, distinctScopeThreshold: 3, windowMs: 60_000, maxProtocolScopes: 8, nowMs: now
  })
  assert.equal(redisEvidenceAfterFraming.status, 'recorded', 'Redis framing 清理后单一新 scope 不得借旧证据升级父级')
  assert.equal(redisEvidenceAfterFraming.protocolScopeCount, 1)
  assert.equal((await evidenceSecond.get(evidenceAccount, now)).phase, 'CLOSED')

  const relationshipRestoreStore = new RedisAccountCircuitStore({
    redisUrl,
    name: relationshipRestoreName,
    capacity: 8,
    now: () => now
  })
  const relationshipRestoreChildScope: Extract<AccountCircuitScope, { kind: 'protocol_model' }> = {
    kind: 'protocol_model',
    accountRuntimeKey: 'redis-relationship-restore',
    protocolProfile: 'profile_openai_v1',
    requestLane: 'text',
    modelBucket: 'model-a'
  }
  const relationshipRestoreParentScope: AccountCircuitScope = {
    kind: 'account',
    accountRuntimeKey: relationshipRestoreChildScope.accountRuntimeKey
  }
  const relationshipRestoreChild: AccountCircuitState = {
    scope: relationshipRestoreChildScope,
    scopeKey: accountCircuitScopeKey(relationshipRestoreChildScope),
    phase: 'OPEN',
    generation: 2,
    dispatchRevision: '1',
    transitionId: 'redis-relationship-child-open',
    incidentId: 'redis-relationship-child-incident',
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    retryAtMs: now + 10_000,
    updatedAtMs: now - 2
  }
  const relationshipRestoreParent: AccountCircuitState = {
    scope: relationshipRestoreParentScope,
    scopeKey: accountCircuitScopeKey(relationshipRestoreParentScope),
    phase: 'OPEN',
    generation: 1,
    dispatchRevision: '1',
    transitionId: 'redis-relationship-parent-open',
    incidentId: 'redis-relationship-parent-incident',
    childScopeKeys: [relationshipRestoreChild.scopeKey],
    childIncidentIds: [relationshipRestoreChild.incidentId!],
    requiredRecoveryScopeKeys: [relationshipRestoreChild.scopeKey],
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    retryAtMs: now + 10_000,
    updatedAtMs: now - 1
  }
  assert.equal((await relationshipRestoreStore.restore(relationshipRestoreChild, now)).status, 'applied')
  const redisRelationshipRestore = await relationshipRestoreStore.restore(relationshipRestoreParent, now)
  assert.equal(redisRelationshipRestore.status, 'applied')
  assert.equal(redisRelationshipRestore.relatedStates?.length, 1)
  const redisRelationshipRepaired = await relationshipRestoreStore.get(relationshipRestoreChildScope, now)
  assert.equal(redisRelationshipRepaired.shadowedByIncidentId, relationshipRestoreParent.incidentId)
  assert.match(redisRelationshipRepaired.transitionId, /^hierarchy:shadow:[a-f0-9]{40}$/)
  const redisRelationshipReplay = await relationshipRestoreStore.restore(relationshipRestoreParent, now)
  assert.equal(redisRelationshipReplay.status, 'idempotent')
  assert.equal(redisRelationshipReplay.relatedStates, undefined)
  assert.equal(
    (await relationshipRestoreStore.get(relationshipRestoreChildScope, now)).transitionId,
    redisRelationshipRepaired.transitionId,
    'Redis 父关系重放必须保持同一子 transition 并且不重复返回 related state'
  )
  const relationshipRestoreClosedParent: AccountCircuitState = {
    ...relationshipRestoreParent,
    phase: 'CLOSED',
    transitionId: 'redis-relationship-parent-closed',
    backoffAttempt: 0,
    retryAtMs: undefined,
    updatedAtMs: now
  }
  const redisRelationshipClosed = await relationshipRestoreStore.restore(relationshipRestoreClosedParent, now)
  assert.equal(redisRelationshipClosed.status, 'applied')
  assert.equal(redisRelationshipClosed.relatedStates?.length, 1)
  assert.equal(
    (await relationshipRestoreStore.get(relationshipRestoreChildScope, now)).shadowedByIncidentId,
    undefined,
    'Redis 冷重建必须从父 CLOSED tombstone 修复 unshadow 崩溃窗口'
  )
  assert.match(
    (await relationshipRestoreStore.get(relationshipRestoreChildScope, now)).transitionId,
    /^hierarchy:unshadow:[a-f0-9]{40}$/
  )

  const first = new RedisAccountCircuitStore({ redisUrl, name, capacity: 4, closedRetentionMs: 100, now: () => now })
  const second = new RedisAccountCircuitStore({ redisUrl, name, capacity: 4, closedRetentionMs: 100, now: () => now })
  const scope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: `redis-account-${process.pid}` }

  assert.equal((await first.get(scope)).phase, 'CLOSED')
  assert.equal((await first.suspect({
    scope,
    dispatchRevision: 'rev-1',
    transitionId: 'suspect',
    reason: 'timeout',
    nowMs: now
  })).status, 'applied')
  assert.equal((await second.get(scope)).phase, 'SUSPECT', '第二个 adapter 必须读取共享 Redis 状态')

  const identity = { scope, generation: 1, dispatchRevision: 'rev-1' }
  now += 3_000
  const [leaseA, leaseB] = await Promise.all([
    first.acquireConfirmationLease({
      ...identity,
      transitionId: 'confirm-acquire-a',
      leaseId: 'confirm-a',
      leaseUntilMs: now + 1_000,
      nowMs: now
    }),
    second.acquireConfirmationLease({
      ...identity,
      transitionId: 'confirm-acquire-b',
      leaseId: 'confirm-b',
      leaseUntilMs: now + 1_000,
      nowMs: now
    })
  ])
  assert.equal([leaseA, leaseB].filter((item) => item.status === 'applied').length, 1, '双节点 confirmation 只能单飞')
  const leaseId = leaseA.status === 'applied' ? 'confirm-a' : 'confirm-b'
  const opened = await second.completeConfirmation({
    ...identity,
    transitionId: 'confirm-failure',
    leaseId,
    outcome: 'transport_failure',
    failureEvidenceKey: 'd'.repeat(64),
    nowMs: now
  })
  assert.equal(opened.state.phase, 'SUSPECT')
  assert.equal(opened.state.confirmationFailureCount, 1)
  assert.equal((await first.listDue(now, 10)).length, 0)

  now += 3_000
  assert.equal((await first.acquireConfirmationLease({
    ...identity,
    transitionId: 'confirm-acquire-second-independent',
    leaseId: 'confirm-second-independent',
    leaseUntilMs: now + 1_000,
    nowMs: now
  })).status, 'applied')
  const openedAfterThreshold = await second.completeConfirmation({
    ...identity,
    transitionId: 'confirm-failure-second-independent',
    leaseId: 'confirm-second-independent',
    outcome: 'transport_failure',
    failureEvidenceKey: 'e'.repeat(64),
    nowMs: now
  })
  assert.equal(openedAfterThreshold.state.phase, 'OPEN')
  assert.equal(openedAfterThreshold.state.confirmationFailureCount, 2)
  assert.equal(openedAfterThreshold.state.retryAtMs, now + 3_000)

  now += 3_000
  assert.equal((await second.listDue(now, 10))[0]?.scopeKey, openedAfterThreshold.state.scopeKey)
  for (const index of [1, 2, 3, 4]) {
    now += 3_000
    const adapter = index % 2 === 0 ? second : first
    assert.equal((await adapter.acquireCanaryLease({
      ...identity,
      transitionId: `canary-acquire-${index}`,
      leaseId: `canary-${index}`,
      leaseUntilMs: now + 1_000,
      nowMs: now
    })).status, 'applied')
    const completed = await adapter.completeCanary({
      ...identity,
      transitionId: `canary-complete-${index}`,
      leaseId: `canary-${index}`,
      outcome: 'framing_complete',
      nowMs: now
    })
    assert.equal(completed.state.phase, index === 4 ? 'CLOSED' : 'RECOVERING')
    if (index === 1) {
      const replay = await second.completeCanary({
        ...identity,
        transitionId: 'canary-complete-1',
        leaseId: 'canary-1',
        outcome: 'framing_complete',
        nowMs: now
      })
      assert.equal(replay.status, 'idempotent')
      assert.equal(replay.state.recoverySuccessCount, 0, '首次 half-open 成功只进入 RECOVERING，重复结果不得推进恢复计数')
    }
  }

  const revised = await second.replaceDispatchRevision({
    scope,
    dispatchRevision: 'rev-2',
    transitionId: 'revision-2',
    nowMs: now
  })
  assert.equal(revised.state.generation, 2)
  assert.equal((await first.acquireCanaryLease({
    ...identity,
    transitionId: 'stale-after-revision',
    leaseId: 'stale',
    leaseUntilMs: now + 1_000,
    nowMs: now
  })).status, 'stale_generation')

  const revisionFirst = new RedisAccountCircuitStore({ redisUrl, name: revisionName, capacity: 4, closedRetentionMs: 100, now: () => now })
  const revisionSecond = new RedisAccountCircuitStore({ redisUrl, name: revisionName, capacity: 4, closedRetentionMs: 100, now: () => now })
  const revisionFenceScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: `redis-revision-fence-${process.pid}` }
  await revisionFirst.restore({
    scope: revisionFenceScope,
    scopeKey: accountCircuitScopeKey(revisionFenceScope),
    phase: 'OPEN',
    generation: 4,
    dispatchRevision: '10',
    transitionId: 'redis-revision-open-10',
    backoffAttempt: 2,
    recoverySuccessCount: 0,
    openedAtMs: now,
    retryAtMs: now + 30_000,
    updatedAtMs: now
  }, now)
  const staleReplace = await revisionSecond.replaceDispatchRevision({
    scope: revisionFenceScope,
    dispatchRevision: '9',
    transitionId: 'redis-late-revision-9',
    nowMs: now + 1
  })
  assert.equal(staleReplace.status, 'stale_dispatch_revision')
  assert.equal(staleReplace.state.phase, 'OPEN', 'Redis 迟到旧 revision 不得关闭较新 OPEN')
  const duplicateOwnerReplace = await revisionSecond.replaceDispatchRevision({
    scope: revisionFenceScope,
    dispatchRevision: '10',
    transitionId: 'redis-duplicate-revision-10',
    nowMs: now + 1
  })
  assert.equal(duplicateOwnerReplace.status, 'idempotent')
  assert.equal(duplicateOwnerReplace.state.phase, 'OPEN', 'Redis 同 owner revision 重复投影不得关闭 OPEN')
  assert.equal((await revisionFirst.restore({
    ...staleReplace.state,
    phase: 'CLOSED',
    generation: 99,
    dispatchRevision: '8',
    transitionId: 'redis-late-restore-8',
    openedAtMs: undefined,
    retryAtMs: undefined,
    updatedAtMs: now + 2
  }, now + 2)).status, 'stale_dispatch_revision', 'Redis reconcile 旧 revision 即使 generation 更大也必须被拒绝')
  const nextOwner = await revisionSecond.replaceDispatchRevision({
    scope: revisionFenceScope,
    dispatchRevision: '11',
    transitionId: 'redis-revision-11',
    nowMs: now + 3
  })
  assert.equal(nextOwner.status, 'applied')
  assert.equal(nextOwner.state.phase, 'CLOSED')
  assert.equal((await revisionFirst.suspect({
    scope: revisionFenceScope,
    dispatchRevision: '10',
    transitionId: 'redis-late-suspect-10',
    reason: 'late transport observation',
    nowMs: now + 4
  })).status, 'stale_dispatch_revision', 'Redis 旧 attempt 迟到失败不得污染新 owner CLOSED')

  now += 101
  assert.equal(await first.size(), 0, 'CLOSED tombstone 到期后必须从共享容量索引清理')

  const authorizedInstanceId = `redis-authorized-${process.pid}`
  const authorizedScopes: AccountCircuitScope[] = [
    { kind: 'account', accountRuntimeKey: `${authorizedInstanceId}:authorized:grantee:group-a:grant` },
    { kind: 'account', accountRuntimeKey: `${authorizedInstanceId}:authorized:grantee:group-b:grant` }
  ]
  for (const [index, authorizedScope] of authorizedScopes.entries()) {
    await first.suspect({ scope: authorizedScope, dispatchRevision: '7', transitionId: `authorized-suspect-${index}`, reason: 'old config', nowMs: now })
  }
  assert.equal(await second.replaceAccountDispatchRevision({
    accountRuntimeKey: authorizedInstanceId,
    dispatchRevision: '8',
    transitionId: 'authorized-revision-8',
    nowMs: now
  }), 2, 'Redis 裸授权实例 ID 必须原子 fence 全部 runtime-key family')
  await Promise.all([
    first.replaceAccountDispatchRevision({ accountRuntimeKey: authorizedInstanceId, dispatchRevision: '10', transitionId: 'authorized-revision-10', nowMs: now + 1 }),
    second.replaceAccountDispatchRevision({ accountRuntimeKey: authorizedInstanceId, dispatchRevision: '9', transitionId: 'authorized-revision-9', nowMs: now + 1 })
  ])
  for (const authorizedScope of authorizedScopes) {
    assert.equal((await first.get(authorizedScope, now + 1)).dispatchRevision, '10', 'Redis 并发乱序投影必须保留最大 numeric revision')
  }

  const dueStore = new RedisAccountCircuitStore({ redisUrl, name: dueName, capacity: 32, now: () => now })
  const staleDueScopeKeys: string[] = []
  for (let index = 0; index < 12; index++) {
    const suspected = await dueStore.suspect({
      scope: { kind: 'account', accountRuntimeKey: `redis-idle-suspect-${index}` },
      dispatchRevision: 'due-1',
      transitionId: `redis-idle-suspect-transition-${index}`,
      reason: 'transport',
      nowMs: now - 100 - index
    })
    staleDueScopeKeys.push(suspected.state.scopeKey)
  }
  for (const scopeKey of staleDueScopeKeys) {
    await redis.sendCommand(['ZADD', dueKeys.due, String(now - 100), scopeKey])
  }
  const dueOpenScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'redis-due-open' }
  await dueStore.restore({
    scope: dueOpenScope,
    scopeKey: (await dueStore.get(dueOpenScope, now)).scopeKey,
    phase: 'OPEN',
    generation: 1,
    dispatchRevision: 'due-1',
    transitionId: 'redis-due-open-transition',
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    retryAtMs: now,
    updatedAtMs: now - 1
  }, now)
  const leasedHalfOpenScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'redis-due-leased-half-open' }
  const leasedHalfOpenState = (await dueStore.restore({
    scope: leasedHalfOpenScope,
    scopeKey: (await dueStore.get(leasedHalfOpenScope, now)).scopeKey,
    phase: 'OPEN',
    generation: 1,
    dispatchRevision: 'due-1',
    transitionId: 'redis-due-leased-open-transition',
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    retryAtMs: now,
    updatedAtMs: now - 1
  }, now)).state
  const leasedUntilMs = now + 10_000
  assert.equal((await dueStore.acquireCanaryLease({
    scope: leasedHalfOpenScope,
    generation: leasedHalfOpenState.generation,
    dispatchRevision: leasedHalfOpenState.dispatchRevision,
    transitionId: 'redis-due-leased-half-open-transition',
    leaseId: 'redis-due-leased-half-open-lease',
    leaseUntilMs: leasedUntilMs,
    nowMs: now
  })).status, 'applied')
  const leasedHalfOpenScopeKey = (await dueStore.get(leasedHalfOpenScope, now)).scopeKey
  await redis.sendCommand(['ZADD', dueKeys.due, String(now - 100), leasedHalfOpenScopeKey])
  assert.deepEqual(
    (await dueStore.listDue(now, 10)).map((state) => state.scopeKey),
    [(await dueStore.get(dueOpenScope, now)).scopeKey],
    'Redis 未到期 SUSPECT 和带活动租约 HALF_OPEN 不得占满 batch 饿死到期 OPEN'
  )
  await dueStore.replaceDispatchRevision({
    scope: dueOpenScope,
    dispatchRevision: 'due-2',
    transitionId: 'redis-due-open-close',
    nowMs: now
  })
  assert.equal(Number(await redis.sendCommand(['ZSCORE', dueKeys.due, leasedHalfOpenScopeKey])), leasedUntilMs, 'Redis listDue 必须按当前 HALF_OPEN lease 原子修正旧 due score')
  for (const scopeKey of staleDueScopeKeys) {
    const stateRaw = await redis.sendCommand(['HGET', dueKeys.states, scopeKey])
    assert.equal(typeof stateRaw, 'string')
    const state = JSON.parse(String(stateRaw)) as { state: { retryAtMs: number } }
    assert.equal(Number(await redis.sendCommand(['ZSCORE', dueKeys.due, scopeKey])), state.state.retryAtMs, 'SUSPECT 的旧 due score 必须原子修正为后台确认时间')
  }

  now += 3_000
  const dueSuspects = await dueStore.listDue(now, 10)
  assert.equal(dueSuspects.length, 10)
  assert.equal(dueSuspects.every((state) => state.phase === 'SUSPECT'), true, '低流量 SUSPECT 到期后必须由 Redis 共享队列分批返回')

  const backgroundStore = new RedisAccountCircuitStore({ redisUrl, name: backgroundName, capacity: 32, now: () => now })
  const redisBackgroundScope: Extract<AccountCircuitScope, { kind: 'protocol_model' }> = {
    kind: 'protocol_model',
    accountRuntimeKey: 'redis-background-confirmation',
    protocolProfile: 'openai_v1',
    requestLane: 'text',
    modelBucket: 'gpt-5'
  }
  await backgroundStore.suspect({
    scope: redisBackgroundScope,
    dispatchRevision: '1',
    transitionId: 'redis-background-suspect',
    reason: 'timeout',
    confirmationFailuresRequired: 2,
    failureEvidenceKey: 'a'.repeat(64),
    nowMs: now
  })
  now += 3_000
  let redisBackgroundProbeCount = 0
  const redisBackgroundService = () => new AccountCircuitRecoveryService(
    backgroundStore,
    async () => ({
      dispatchRevision: '1',
      probe: async () => {
        redisBackgroundProbeCount += 1
        return { kind: 'transport_incomplete', failureKind: 'connection' }
      }
    }),
    {
      batchSize: 32,
      leaseDurationMs: 10_000,
      now: () => now,
      createId: () => `redis-background-${++idSequence}`
    }
  )
  const [redisBackgroundSweepA, redisBackgroundSweepB] = await Promise.all([
    redisBackgroundService().sweep(),
    redisBackgroundService().sweep()
  ])
  assert.equal(redisBackgroundProbeCount, 1, '两个 Redis worker 对同一 SUSPECT generation 只能有一个后台确认探针')
  assert.equal(redisBackgroundSweepA.leasedCount + redisBackgroundSweepB.leasedCount, 1)
  assert.equal((await backgroundStore.get(redisBackgroundScope, now)).confirmationFailureCount, 1)
  now += 3_000
  await redisBackgroundService().sweep()
  assert.equal((await backgroundStore.get(redisBackgroundScope, now)).phase, 'OPEN', '真实死亡账户必须由 Redis 后台独立 evidence 最终确认 OPEN')
  await backgroundStore.replaceDispatchRevision({
    scope: redisBackgroundScope,
    dispatchRevision: '2',
    transitionId: 'redis-background-death-case-cleanup',
    nowMs: now
  })

  const redisBackgroundHealthyScope: Extract<AccountCircuitScope, { kind: 'protocol_model' }> = {
    ...redisBackgroundScope,
    accountRuntimeKey: 'redis-background-framing'
  }
  await backgroundStore.suspect({
    scope: redisBackgroundHealthyScope,
    dispatchRevision: '1',
    transitionId: 'redis-background-framing-suspect',
    reason: 'timeout',
    confirmationFailuresRequired: 2,
    nowMs: now
  })
  now += 3_000
  await new AccountCircuitRecoveryService(
    backgroundStore,
    async () => ({
      dispatchRevision: '1',
      probe: async () => ({ kind: 'framing_complete', statusCode: 429 })
    }),
    {
      batchSize: 32,
      leaseDurationMs: 10_000,
      now: () => now,
      createId: () => `redis-background-${++idSequence}`
    }
  ).sweep()
  assert.equal((await backgroundStore.get(redisBackgroundHealthyScope, now)).phase, 'CLOSED', 'Redis 后台完整 framing 不得按不可信状态码误判，应直接清除 transport SUSPECT')

  const redisBackgroundUnknownScope: Extract<AccountCircuitScope, { kind: 'protocol_model' }> = {
    ...redisBackgroundScope,
    accountRuntimeKey: 'redis-background-unknown-backoff'
  }
  await backgroundStore.suspect({
    scope: redisBackgroundUnknownScope,
    dispatchRevision: '1',
    transitionId: 'redis-background-unknown-suspect',
    reason: 'timeout',
    confirmationFailuresRequired: 2,
    nowMs: now
  })
  for (const [index, expectedDelayMs] of [3_000, 5_000].entries()) {
    now += 3_000
    const leaseId = `redis-background-unknown-lease-${index}`
    assert.equal((await backgroundStore.acquireConfirmationLease({
      scope: redisBackgroundUnknownScope,
      generation: 1,
      dispatchRevision: '1',
      transitionId: `redis-background-unknown-acquire-${index}`,
      leaseId,
      leaseUntilMs: now + 1_000,
      nowMs: now
    })).status, 'applied')
    const unknown = await backgroundStore.completeConfirmation({
      scope: redisBackgroundUnknownScope,
      generation: 1,
      dispatchRevision: '1',
      transitionId: `redis-background-unknown-complete-${index}`,
      leaseId,
      outcome: 'unknown',
      nowMs: now
    })
    assert.equal(unknown.state.confirmationFailureCount, 0)
    assert.equal(unknown.state.backoffAttempt, index + 1)
    assert.equal(unknown.state.retryAtMs, now + expectedDelayMs, 'Redis SUSPECT unknown 必须按共享退避阶梯推进')
  }
  assert.equal(
    (await backgroundStore.listDue(now + 4_999, 32)).some((state) => state.scopeKey === accountCircuitScopeKey(redisBackgroundUnknownScope)),
    false
  )
  assert.equal(
    (await backgroundStore.listDue(now + 5_000, 32)).some((state) => state.scopeKey === accountCircuitScopeKey(redisBackgroundUnknownScope)),
    true
  )
  await backgroundStore.replaceDispatchRevision({
    scope: redisBackgroundUnknownScope,
    dispatchRevision: '2',
    transitionId: 'redis-background-unknown-cleanup',
    nowMs: now
  })

  const redisRebuiltSuspectScope: Extract<AccountCircuitScope, { kind: 'protocol_model' }> = {
    ...redisBackgroundScope,
    accountRuntimeKey: 'redis-rebuilt-suspect'
  }
  await backgroundStore.restore({
    scope: redisRebuiltSuspectScope,
    scopeKey: accountCircuitScopeKey(redisRebuiltSuspectScope),
    phase: 'SUSPECT',
    generation: 4,
    dispatchRevision: '4',
    transitionId: 'redis-rebuilt-suspect-transition',
    backoffAttempt: 0,
    recoverySuccessCount: 0,
    confirmationFailuresRequired: 2,
    confirmationFailureCount: 1,
    failureEvidenceKeys: ['b'.repeat(64), 'c'.repeat(64)],
    updatedAtMs: now
  }, now)
  assert.equal(
    (await backgroundStore.listDue(now, 10)).some((state) => state.scopeKey === accountCircuitScopeKey(redisRebuiltSuspectScope)),
    true,
    'control-plane 重建的旧 SUSPECT 即使缺少 nextTransitionAtMs，也必须恢复 Redis due 索引'
  )

  const capacityFirst = new RedisAccountCircuitStore({ redisUrl, name: capacityName, capacity: 1, now: () => now })
  const capacitySecond = new RedisAccountCircuitStore({ redisUrl, name: capacityName, capacity: 1, now: () => now })
  const occupiedScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'redis-capacity-occupied' }
  const rejectedScope: AccountCircuitScope = {
    kind: 'protocol_model',
    accountRuntimeKey: 'redis-capacity-rejected',
    protocolProfile: 'openai_v1',
    requestLane: 'text',
    modelBucket: 'gpt-5'
  }
  assert.equal((await capacityFirst.restore({
    scope: occupiedScope,
    scopeKey: accountCircuitScopeKey(occupiedScope),
    phase: 'OPEN',
    generation: 1,
    dispatchRevision: '1',
    transitionId: 'redis-capacity-occupied',
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    retryAtMs: now + 60_000,
    updatedAtMs: now
  }, now)).status, 'applied')
  const capacityRejected = await capacitySecond.suspect({
    scope: rejectedScope,
    dispatchRevision: '1',
    transitionId: 'redis-capacity-rejected',
    reason: 'transport',
    nowMs: now
  })
  assert.equal(capacityRejected.status, 'capacity_exhausted')
  assert.equal(capacityRejected.state.phase, 'SUSPECT')
  assert.equal(capacityRejected.state.failureReason, 'runtime_state_capacity_exhausted')
  assert.equal((await capacityFirst.get(rejectedScope, now)).failureReason, 'runtime_state_capacity_exhausted', '容量哨兵必须跨 Redis store 实例共享')
  assert.equal((await capacityFirst.restore({
    ...capacityRejected.state,
    generation: 2,
    transitionId: 'redis-capacity-restore-rejected',
    updatedAtMs: now + 1
  }, now + 1)).status, 'capacity_exhausted', 'control-plane restore 不得越过容量上限')
  await capacityFirst.replaceAccountDispatchRevision({
    accountRuntimeKey: occupiedScope.accountRuntimeKey,
    dispatchRevision: '2',
    transitionId: 'redis-capacity-release',
    nowMs: now + 2
  })
  assert.equal((await capacitySecond.get(rejectedScope, now + 2)).phase, 'CLOSED', '出现可回收 CLOSED 后共享容量哨兵必须自动解除')

  const tombstoneBoundaryCapacity = 300
  const tombstoneBoundaryStore = new RedisAccountCircuitStore({
    redisUrl,
    name: tombstoneBoundaryName,
    capacity: tombstoneBoundaryCapacity,
    closedRetentionMs: 100,
    now: () => now
  })
  const preservedActiveScope: AccountCircuitScope = {
    kind: 'account',
    accountRuntimeKey: 'redis-tombstone-active-scope'
  }
  await tombstoneBoundaryStore.restore({
    scope: preservedActiveScope,
    scopeKey: accountCircuitScopeKey(preservedActiveScope),
    phase: 'OPEN',
    generation: 1,
    dispatchRevision: '1',
    transitionId: 'redis-tombstone-active-open',
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    retryAtMs: now + 60_000,
    updatedAtMs: now
  }, now)
  const preservedActiveScopeKey = accountCircuitScopeKey(preservedActiveScope)
  await redis.sendCommand(['ZADD', tombstoneBoundaryKeys.closed, String(now - 2_000), preservedActiveScopeKey])
  const tombstoneStateArgs = ['HSET', tombstoneBoundaryKeys.states]
  const tombstoneIndexArgs = ['ZADD', tombstoneBoundaryKeys.closed]
  for (let index = 0; index < 600; index++) {
    const scope: AccountCircuitScope = {
      kind: 'account',
      accountRuntimeKey: `redis-expired-tombstone-${String(index).padStart(4, '0')}`
    }
    const scopeKey = accountCircuitScopeKey(scope)
    tombstoneStateArgs.push(scopeKey, encodedClosedEntry(scope, now - 1, `redis-expired-${index}`))
    tombstoneIndexArgs.push(String(now - 1_000), scopeKey)
  }
  await redis.sendCommand(tombstoneStateArgs)
  await redis.sendCommand(tombstoneIndexArgs)
  assert.equal(Number(await redis.sendCommand(['HLEN', tombstoneBoundaryKeys.states])), 601)

  const boundaryCandidateScope: AccountCircuitScope = {
    kind: 'account',
    accountRuntimeKey: 'redis-tombstone-boundary-candidate'
  }
  const firstBoundaryAttempt = await tombstoneBoundaryStore.suspect({
    scope: boundaryCandidateScope,
    dispatchRevision: '1',
    transitionId: 'redis-tombstone-boundary-first',
    reason: 'transport',
    nowMs: now
  })
  assert.equal(firstBoundaryAttempt.status, 'capacity_exhausted', '单次清满 256 条后不得在仍超容量时强行插入新 scope')
  assert.equal(Number(await redis.sendCommand(['HLEN', tombstoneBoundaryKeys.states])), 346, '单次 Lua 只处理 256 个 CLOSED 索引成员，其中陈旧 active 索引只修复不删状态')
  const activeAfterFirstCleanup = JSON.parse(String(await redis.sendCommand(['HGET', tombstoneBoundaryKeys.states, preservedActiveScopeKey]))) as { state: { phase: string } }
  assert.equal(activeAfterFirstCleanup.state.phase, 'OPEN', '陈旧 CLOSED 索引不得删除 active scope')
  assert.equal(await redis.sendCommand(['ZSCORE', tombstoneBoundaryKeys.closed, preservedActiveScopeKey]), null, '陈旧 active CLOSED 索引必须被惰性修复')
  assert.equal(await tombstoneBoundaryStore.size(), 1, 'size 必须通过多个 256 条以内的小 Lua 清完 tombstone，并返回精确 active 数量')
  assert.equal(Number(await redis.sendCommand(['HLEN', tombstoneBoundaryKeys.states])), 1)

  const secondBoundaryAttempt = await tombstoneBoundaryStore.suspect({
    scope: boundaryCandidateScope,
    dispatchRevision: '1',
    transitionId: 'redis-tombstone-boundary-second',
    reason: 'transport',
    nowMs: now
  })
  assert.equal(secondBoundaryAttempt.status, 'applied', '后续调用必须继续清理剩余 tombstone 并在容量真正释放后接纳新 scope')
  assert.equal(Number(await redis.sendCommand(['HLEN', tombstoneBoundaryKeys.states])), 2)
  assert.equal(await tombstoneBoundaryStore.size(), 2, '全部 tombstone 清理后只保留两个 active scope')
  assert.equal((await tombstoneBoundaryStore.get(preservedActiveScope, now)).phase, 'OPEN')

  const revisionScanStore = new RedisAccountCircuitStore({
    redisUrl,
    name: revisionScanName,
    capacity: 64,
    closedRetentionMs: 100,
    now: () => now
  })
  const revisionScanRuntimeKey = 'redis-revision-scan-target'
  const revisionScanScopes: AccountCircuitScope[] = [
    { kind: 'account', accountRuntimeKey: revisionScanRuntimeKey },
    { kind: 'key', accountRuntimeKey: `${revisionScanRuntimeKey}:authorized:tenant-a`, keyFingerprint: 'fingerprint-a' },
    {
      kind: 'protocol_model',
      accountRuntimeKey: `${revisionScanRuntimeKey}:authorized:tenant-b`,
      protocolProfile: 'openai_v1',
      requestLane: 'text',
      modelBucket: 'gpt-5'
    }
  ]
  for (const [index, scope] of revisionScanScopes.entries()) {
    await revisionScanStore.restore({
      scope,
      scopeKey: accountCircuitScopeKey(scope),
      phase: 'OPEN',
      generation: 1,
      dispatchRevision: '1',
      transitionId: `redis-revision-scan-active-${index}`,
      backoffAttempt: 1,
      recoverySuccessCount: 0,
      retryAtMs: now + 60_000,
      updatedAtMs: now
    }, now)
  }
  const revisionStateArgs = ['HSET', revisionScanKeys.states]
  for (let index = 0; index < 1_024; index++) {
    const scope: AccountCircuitScope = {
      kind: 'account',
      accountRuntimeKey: `redis-revision-scan-unrelated-${String(index).padStart(4, '0')}`
    }
    revisionStateArgs.push(
      accountCircuitScopeKey(scope),
      encodedClosedEntry(scope, now + 60_000, `redis-revision-scan-unrelated-${index}`)
    )
  }
  await redis.sendCommand(revisionStateArgs)
  const revisionEvidenceArgs = ['HSET', revisionScanKeys.escalation]
  for (let index = 0; index < 2_048; index++) {
    revisionEvidenceArgs.push(
      `redis-revision-evidence-unrelated-${String(index).padStart(4, '0')}`,
      JSON.stringify({ dispatchRevision: '1', scopes: [] })
    )
  }
  revisionEvidenceArgs.push(
    revisionScanRuntimeKey,
    JSON.stringify({ dispatchRevision: '1', scopes: [] }),
    `${revisionScanRuntimeKey}:authorized:tenant-a`,
    JSON.stringify({ dispatchRevision: '1', scopes: [] })
  )
  await redis.sendCommand(revisionEvidenceArgs)
  assert.equal(await revisionScanStore.replaceAccountDispatchRevision({
    accountRuntimeKey: revisionScanRuntimeKey,
    dispatchRevision: '2',
    transitionId: 'redis-revision-scan-replace',
    nowMs: now
  }), 3, '超过 capacity 的遗留 hash 也必须通过有界 HSCAN 找全目标 active scope')
  for (const scope of revisionScanScopes) {
    const state = await revisionScanStore.get(scope, now)
    assert.equal(state.phase, 'CLOSED')
    assert.equal(state.dispatchRevision, '2')
  }
  assert.equal(await redis.sendCommand(['HGET', revisionScanKeys.escalation, revisionScanRuntimeKey]), null)
  assert.equal(await redis.sendCommand(['HGET', revisionScanKeys.escalation, `${revisionScanRuntimeKey}:authorized:tenant-a`]), null)
  assert.notEqual(await redis.sendCommand(['HGET', revisionScanKeys.escalation, 'redis-revision-evidence-unrelated-0000']), null, 'revision HSCAN 不得误删其他账户证据')

  const jitterScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'redis-jitter-parity' }
  const jitterState = {
    scope: jitterScope,
    scopeKey: accountCircuitScopeKey(jitterScope),
    phase: 'OPEN' as const,
    generation: 1,
    dispatchRevision: '1',
    transitionId: 'redis-jitter-open',
    backoffAttempt: 5,
    recoverySuccessCount: 0,
    retryAtMs: now,
    updatedAtMs: now
  }
  const memoryJitter = new MemoryAccountCircuitStore({ capacity: 2, now: () => now })
  const redisJitter = new RedisAccountCircuitStore({ redisUrl, name: jitterName, capacity: 2, now: () => now })
  await Promise.all([memoryJitter.restore(jitterState, now), redisJitter.restore(jitterState, now)])
  const jitterLeaseId = 'redis-jitter-lease'
  const [memoryJitterLease, redisJitterLease] = await Promise.all([
    memoryJitter.acquireCanaryLease({ scope: jitterScope, generation: 1, dispatchRevision: '1', transitionId: 'memory-jitter-acquire', leaseId: jitterLeaseId, leaseUntilMs: now + 1_000, nowMs: now }),
    redisJitter.acquireCanaryLease({ scope: jitterScope, generation: 1, dispatchRevision: '1', transitionId: 'memory-jitter-acquire', leaseId: jitterLeaseId, leaseUntilMs: now + 1_000, nowMs: now })
  ])
  assert.deepEqual(normalizedResult(redisJitterLease), normalizedResult(memoryJitterLease))
  const [memoryJitterFailure, redisJitterFailure] = await Promise.all([
    memoryJitter.completeCanary({ scope: jitterScope, generation: 1, dispatchRevision: '1', transitionId: 'memory-jitter-fail', leaseId: jitterLeaseId, outcome: 'transport_failure', nowMs: now }),
    redisJitter.completeCanary({ scope: jitterScope, generation: 1, dispatchRevision: '1', transitionId: 'memory-jitter-fail', leaseId: jitterLeaseId, outcome: 'transport_failure', nowMs: now })
  ])
  assert.equal(redisJitterFailure.state.backoffAttempt, memoryJitterFailure.state.backoffAttempt)
  assert.equal(redisJitterFailure.state.retryAtMs, memoryJitterFailure.state.retryAtMs, 'Redis 与 memory 的长期 backoff jitter 必须逐毫秒一致')
  assert.equal(redisJitterFailure.state.incidentId, memoryJitterFailure.state.incidentId)

  const confirmationUnknownState = {
    ...jitterState,
    phase: 'SUSPECT' as const,
    lease: { kind: 'confirmation' as const, leaseId: 'redis-jitter-confirmation', leaseUntilMs: now + 1_000 }
  }
  await Promise.all([memoryJitter.restore(confirmationUnknownState, now), redisJitter.restore(confirmationUnknownState, now)])
  const [memoryConfirmationUnknown, redisConfirmationUnknown] = await Promise.all([
    memoryJitter.completeConfirmation({ scope: jitterScope, generation: 1, dispatchRevision: '1', transitionId: 'redis-jitter-confirmation-unknown', leaseId: 'redis-jitter-confirmation', outcome: 'unknown', nowMs: now }),
    redisJitter.completeConfirmation({ scope: jitterScope, generation: 1, dispatchRevision: '1', transitionId: 'redis-jitter-confirmation-unknown', leaseId: 'redis-jitter-confirmation', outcome: 'unknown', nowMs: now })
  ])
  assert.equal(redisConfirmationUnknown.state.retryAtMs, memoryConfirmationUnknown.state.retryAtMs, 'Redis 与 memory 的 confirmation unknown 长退避必须逐毫秒一致')

  const canaryUnknownState = {
    ...jitterState,
    phase: 'HALF_OPEN' as const,
    halfOpenOrigin: 'OPEN' as const,
    lease: { kind: 'half_open' as const, leaseId: 'redis-jitter-canary', leaseUntilMs: now + 1_000 }
  }
  await Promise.all([memoryJitter.restore(canaryUnknownState, now), redisJitter.restore(canaryUnknownState, now)])
  const [memoryCanaryUnknown, redisCanaryUnknown] = await Promise.all([
    memoryJitter.completeCanary({ scope: jitterScope, generation: 1, dispatchRevision: '1', transitionId: 'redis-jitter-canary-unknown', leaseId: 'redis-jitter-canary', outcome: 'unknown', nowMs: now }),
    redisJitter.completeCanary({ scope: jitterScope, generation: 1, dispatchRevision: '1', transitionId: 'redis-jitter-canary-unknown', leaseId: 'redis-jitter-canary', outcome: 'unknown', nowMs: now })
  ])
  assert.equal(redisCanaryUnknown.state.retryAtMs, memoryCanaryUnknown.state.retryAtMs, 'Redis 与 memory 的 canary unknown 长退避必须逐毫秒一致')
  console.log('account-circuit-redis-smoke passed')
} finally {
  try {
    await redis.sendCommand([
      'DEL',
      keys.states,
      keys.due,
      keys.closed,
      keys.escalation,
      parityKeys.states,
      parityKeys.due,
      parityKeys.closed,
      parityKeys.escalation,
      observerParityKeys.states,
      observerParityKeys.due,
      observerParityKeys.closed,
      observerParityKeys.escalation,
      observerParityKeys.capacitySaturated,
      evidenceKeys.states,
      evidenceKeys.due,
      evidenceKeys.closed,
      evidenceKeys.escalation,
      dueKeys.states,
      dueKeys.due,
      dueKeys.closed,
      dueKeys.escalation,
      revisionKeys.states,
      revisionKeys.due,
      revisionKeys.closed,
      revisionKeys.escalation,
      backgroundKeys.states,
      backgroundKeys.due,
      backgroundKeys.closed,
      backgroundKeys.escalation,
      backgroundKeys.capacitySaturated,
      capacityKeys.states,
      capacityKeys.due,
      capacityKeys.closed,
      capacityKeys.escalation,
      capacityKeys.capacitySaturated,
      jitterKeys.states,
      jitterKeys.due,
      jitterKeys.closed,
      jitterKeys.escalation,
      jitterKeys.capacitySaturated,
      tombstoneBoundaryKeys.states,
      tombstoneBoundaryKeys.due,
      tombstoneBoundaryKeys.closed,
      tombstoneBoundaryKeys.escalation,
      tombstoneBoundaryKeys.capacitySaturated,
      revisionScanKeys.states,
      revisionScanKeys.due,
      revisionScanKeys.closed,
      revisionScanKeys.escalation,
      revisionScanKeys.capacitySaturated,
      relationshipRestoreKeys.states,
      relationshipRestoreKeys.due,
      relationshipRestoreKeys.closed,
      relationshipRestoreKeys.escalation,
      relationshipRestoreKeys.capacitySaturated
    ])
  } finally {
    await closeRedisClients()
  }
}

function normalizedResult(value: unknown): unknown {
  return JSON.parse(JSON.stringify(value)) as unknown
}

function encodedClosedEntry(
  scope: AccountCircuitScope,
  closedExpiresAtMs: number,
  transitionId: string
): string {
  return JSON.stringify({
    state: {
      scope,
      scopeKey: accountCircuitScopeKey(scope),
      phase: 'CLOSED',
      generation: 1,
      dispatchRevision: '1',
      transitionId,
      backoffAttempt: 0,
      recoverySuccessCount: 0,
      updatedAtMs: closedExpiresAtMs - 1
    },
    closedExpiresAtMs,
    replayIds: [transitionId],
    replayOrder: [transitionId]
  })
}
