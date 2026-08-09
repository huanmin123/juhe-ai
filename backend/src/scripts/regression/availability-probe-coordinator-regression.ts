import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'

runtimeConfig.runtimeStateDriver = 'memory'

const coordinator = await import('../../modules/gateway/runtime/availability-probe-coordinator.js')

const scope = 'acct_a:authorized:sys_a:group_a:grant_a'
const acquisitions = await Promise.all(Array.from({ length: 32 }, async () => await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: scope,
  probeKind: 'codex_source_avoidance',
  configRevision: 7,
  nowMs: 1_000,
  leaseMs: 500
})))
const owners = acquisitions.filter((result) => result.disposition === 'owner')
assert.equal(owners.length, 1, '同一 scope/kind/revision 的并发触发只能有一个探活 owner')
assert.equal(acquisitions.filter((result) => result.disposition === 'joined').length, 31, '其余触发必须 joined，不能追加探活')
const owner = owners[0]!

const oldOwnerSettle = await coordinator.settleAvailabilityProbe({
  runtimeKey: owner.runtimeKey,
  generation: owner.generation,
  ownerToken: owner.ownerToken,
  outcome: 'unknown',
  nowMs: 1_100
})
assert.equal(oldOwnerSettle, true, 'owner 应能按自己的 generation fence 结算未知结果')
const completed = await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: scope,
  probeKind: 'codex_source_avoidance',
  configRevision: 7,
  nowMs: 1_101
})
assert.equal(completed.disposition, 'joined', '有界保留的结果应被 joined 消费，不能重复入队')

const sourceFence = (stateKey: string, sourceGeneration: number) => ({
  stateKey,
  accountId: 'acct_a',
  sourceGeneration,
  sourceFenceId: `00000000-0000-4000-8000-${sourceGeneration.toString(16).padStart(12, '0')}`
})
const oldSourceFence = sourceFence('source_old', 1)
const oldSource = await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: 'acct_source', probeKind: 'account_health_check', configRevision: 7, sourceFence: oldSourceFence, executionRole: 'source_dispatch'
})
assert.equal(oldSource.disposition, 'owner')
if (oldSource.disposition !== 'owner') throw new Error('expected old source owner')
assert.equal(await coordinator.settleAvailabilityProbe({
  runtimeKey: oldSource.runtimeKey, generation: oldSource.generation, ownerToken: oldSource.ownerToken, outcome: 'success'
}), true)
const newSource = await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: 'acct_source', probeKind: 'account_health_check', configRevision: 7,
  sourceFence: sourceFence('source_new', 2), executionRole: 'source_dispatch'
})
assert.equal(newSource.disposition, 'owner', 'settled success 后的新 activation 必须创建 owner generation，不能消费旧 success')
if (newSource.disposition !== 'owner') throw new Error('expected new source owner')
assert(newSource.generation > oldSource.generation, '新 activation 必须使用更高 coordinator generation')
assert.deepEqual(await coordinator.availabilityProbeSourceFences(newSource.runtimeKey, newSource.generation), [sourceFence('source_new', 2)], '新 generation 不得继承或清除旧 settled fence')

const forceReplaceOldFence = sourceFence('source_force_replace_old', 3)
const forceReplaceOld = await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: 'acct_force_replace', probeKind: 'account_health_check', configRevision: 7,
  sourceFence: forceReplaceOldFence, executionRole: 'source_dispatch'
})
assert.equal(forceReplaceOld.disposition, 'owner')
if (forceReplaceOld.disposition !== 'owner') throw new Error('expected force replacement old owner')
assert.equal(await coordinator.settleAvailabilityProbe({
  runtimeKey: forceReplaceOld.runtimeKey,
  generation: forceReplaceOld.generation,
  ownerToken: forceReplaceOld.ownerToken,
  outcome: 'success'
}), true)
const forceReplacement = await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: 'acct_force_replace', probeKind: 'account_health_check', configRevision: 7,
  executionRole: 'health_probe', forceNewGeneration: true
})
assert.equal(forceReplacement.disposition, 'owner', '普通 request_failure 必须为已结算结果创建新 generation')
if (forceReplacement.disposition !== 'owner') throw new Error('expected force replacement owner')
assert.deepEqual(
  forceReplacement.replacedFenceSettlement,
  {
    generation: forceReplaceOld.generation,
    configRevision: 7,
    outcome: 'success',
    sourceFences: [forceReplaceOldFence]
  },
  '原子替换必须把旧 settled generation 的完整 fence 快照交给新 owner 结算'
)
assert.deepEqual(
  await coordinator.availabilityProbeSourceFences(forceReplacement.runtimeKey, forceReplacement.generation),
  [],
  '新 generation 不得继承旧 fence；旧 fence 只能按旧 outcome 结算'
)

const replacementRaceOwner = await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: 'acct_source_replacement_race', probeKind: 'account_health_check', configRevision: 7,
  sourceFence: sourceFence('source_race_old', 1), executionRole: 'source_dispatch'
})
assert.equal(replacementRaceOwner.disposition, 'owner')
if (replacementRaceOwner.disposition !== 'owner') throw new Error('expected replacement race owner')
assert.equal(await coordinator.settleAvailabilityProbe({
  runtimeKey: replacementRaceOwner.runtimeKey,
  generation: replacementRaceOwner.generation,
  ownerToken: replacementRaceOwner.ownerToken,
  outcome: 'success'
}), true)
const replacementRace = await Promise.all(Array.from({ length: 32 }, async (_, index) => await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: 'acct_source_replacement_race',
  probeKind: 'account_health_check',
  configRevision: 7,
  sourceFence: sourceFence(`source_race_${index}`, index + 2),
  executionRole: 'source_dispatch'
})))
const replacementRaceOwners = replacementRace.filter((result) => result.disposition === 'owner')
assert.equal(replacementRaceOwners.length, 1, '旧 success 后并发 activation 只能原子替换为一个新 owner')
const replacementGeneration = replacementRaceOwners[0]!.generation
assert(replacementGeneration > replacementRaceOwner.generation, 'replacement 不得返回旧 settled generation')
assert(replacementRace.every((result) => result.generation === replacementGeneration), '并发 activation 只能加入同一 replacement generation，不能丢失 fence 或返回旧 generation')
assert.equal((await coordinator.availabilityProbeSourceFences(replacementRaceOwner.runtimeKey, replacementGeneration)).length, 32, 'replacement generation 必须保留所有并发加入的 source fence')

const handoffLeaseOwner = await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: 'acct_source_handoff_lease', probeKind: 'account_health_check', configRevision: 7,
  sourceFence: sourceFence('source_handoff_1', 1), executionRole: 'source_dispatch', nowMs: 3_000, leaseMs: 100
})
assert.equal(handoffLeaseOwner.disposition, 'owner')
if (handoffLeaseOwner.disposition !== 'owner') throw new Error('expected handoff lease owner')
assert.equal(await coordinator.releaseAvailabilityProbeForExecution({
  runtimeKey: handoffLeaseOwner.runtimeKey, generation: handoffLeaseOwner.generation, ownerToken: handoffLeaseOwner.ownerToken, nowMs: 3_000, leaseMs: 100
}), true)
const handoffLeaseTakeover = await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: 'acct_source_handoff_lease', probeKind: 'account_health_check', configRevision: 7,
  sourceFence: sourceFence('source_handoff_2', 2), executionRole: 'source_dispatch', nowMs: 3_101, leaseMs: 100
})
assert.equal(handoffLeaseTakeover.disposition, 'owner', '未回传的 dispatch handoff lease 过期后必须允许新 source owner 接管')

const leaseOwner = await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: scope,
  probeKind: 'account_health_check',
  configRevision: 7,
  nowMs: 2_000,
  leaseMs: 100
})
assert.equal(leaseOwner.disposition, 'owner')
if (leaseOwner.disposition !== 'owner') throw new Error('expected lease owner')
const takeover = await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: scope,
  probeKind: 'account_health_check',
  configRevision: 7,
  nowMs: 2_101,
  leaseMs: 100
})
assert.equal(takeover.disposition, 'owner', 'lease 过期后必须允许新 owner 接管')
if (takeover.disposition !== 'owner') throw new Error('expected lease takeover')
assert.notEqual(takeover.ownerToken, leaseOwner.ownerToken)
assert.equal(await coordinator.settleAvailabilityProbe({
  runtimeKey: leaseOwner.runtimeKey,
  generation: leaseOwner.generation,
  ownerToken: leaseOwner.ownerToken,
  outcome: 'success',
  nowMs: 2_110
}), false, '过期 owner 的结果不得结算接管后的 lease')
assert.equal(await coordinator.settleAvailabilityProbe({
  runtimeKey: takeover.runtimeKey,
  generation: takeover.generation,
  ownerToken: takeover.ownerToken,
  outcome: 'health_failure',
  nowMs: 2_120
}), true, '当前 owner 必须能结算自己的探活结果')

const revisionChanged = await coordinator.acquireAvailabilityProbe({
  accountRuntimeScope: scope,
  probeKind: 'account_health_check',
  configRevision: 8,
  nowMs: 2_130
})
assert.equal(revisionChanged.disposition, 'owner', '配置 revision 改变必须创建新 generation scope')
assert.notEqual(revisionChanged.runtimeKey, takeover.runtimeKey)

console.log('可用性探活协调器回归通过：并发单飞、settled 原子替换、fence 合并、lease 接管、owner fencing、结果保留和配置 revision 隔离符合预期')
