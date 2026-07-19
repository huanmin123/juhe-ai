import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  ModelCatalogSnapshotReconcileService,
  type ModelCatalogSnapshotReconcileDependencies
} from '../../modules/model-pricing/model-catalog-snapshot-reconcile.service.js'
import type { ModelCatalogSnapshotRebuildRequest } from '../../storage/model-catalog-snapshot-rebuild.repository.js'

const repositorySource = readFileSync(new URL('../../storage/model-catalog-snapshot-rebuild.repository.ts', import.meta.url), 'utf8')
assert(repositorySource.includes("client.dialect.qualifyTable('juhe_business', 'model_catalog_snapshot_rebuild_requests')"))
assert.match(repositorySource, /DELETE FROM[\s\S]+WHERE scope = \? AND system_account_id = \? AND generation = \?/)
assert.match(repositorySource, /if \(scope === 'all'\)[\s\S]+return ''/)

const allRequest: ModelCatalogSnapshotRebuildRequest = {
  scope: 'all',
  generation: 1,
  updatedAt: '2026-07-20T00:00:00.000Z'
}
const personalRequests = Array.from({ length: 9 }, (_, index) => ({
  scope: 'personal' as const,
  systemAccountId: `account-${index + 1}`,
  generation: index + 1,
  updatedAt: '2026-07-20T00:00:00.000Z'
}))
const events: string[] = []
let activePersonal = 0
let maxPersonal = 0
let personalRebuildCount = 0
const acknowledged: Array<{ scope: string; systemAccountId?: string; generation: number }> = []

const dependencies: ModelCatalogSnapshotReconcileDependencies = {
  listPendingRequests: async () => [allRequest, ...personalRequests],
  findPendingRequest: async (input) => input.scope === 'all' ? allRequest : personalRequests.find((request) => request.systemAccountId === input.systemAccountId),
  ackRequest: async (request) => {
    acknowledged.push({
      scope: request.scope,
      ...(request.systemAccountId ? { systemAccountId: request.systemAccountId } : {}),
      generation: request.generation
    })
    return true
  },
  rebuildAll: async () => {
    events.push('all:start')
    await delay(10)
    events.push('all:end')
  },
  rebuildPersonal: async (systemAccountId) => {
    personalRebuildCount += 1
    activePersonal += 1
    maxPersonal = Math.max(maxPersonal, activePersonal)
    events.push(`${systemAccountId}:start`)
    await delay(5)
    events.push(`${systemAccountId}:end`)
    activePersonal -= 1
  }
}

const service = new ModelCatalogSnapshotReconcileService(dependencies)
const scan = await service.reconcileDirtyOnceAsync()
assert.equal(scan.capturedCount, 10)
assert.equal(scan.rebuildCount, 1)
assert.equal(scan.acknowledgedCount, 10)
assert.equal(scan.retainedCount, 0)
assert.equal(scan.failedCount, 0)
assert.equal(events[0], 'all:start')
assert.equal(events[1], 'all:end')
assert.equal(personalRebuildCount, 0, 'all 成功后不得重复重建本轮捕获的 personal scope')
assert.deepEqual(acknowledged[0], { scope: 'all', generation: 1 })
assert.deepEqual(
  acknowledged.slice(1).map((request) => `${request.systemAccountId}:${request.generation}`).sort(),
  personalRequests.map((request) => `${request.systemAccountId}:${request.generation}`).sort()
)

const personalOnlyService = new ModelCatalogSnapshotReconcileService({
  ...dependencies,
  listPendingRequests: async () => personalRequests
})
activePersonal = 0
maxPersonal = 0
personalRebuildCount = 0
const personalOnlyScan = await personalOnlyService.reconcileDirtyOnceAsync()
assert.equal(personalOnlyScan.rebuildCount, 9)
assert.equal(personalOnlyScan.failedCount, 0)
assert.equal(personalRebuildCount, 9)
assert.equal(maxPersonal, 4, 'personal 重建必须使用恰好 4 个并发 worker')

let rebuildAfterAllAckFailure = 0
const allAckFailureService = new ModelCatalogSnapshotReconcileService({
  ...dependencies,
  ackRequest: async (request) => {
    if (request.scope === 'all') throw new Error('ack failed')
    return true
  },
  rebuildPersonal: async () => { rebuildAfterAllAckFailure += 1 }
})
const allAckFailureScan = await allAckFailureService.reconcileDirtyOnceAsync()
assert.equal(allAckFailureScan.rebuildCount, 1)
assert.equal(allAckFailureScan.acknowledgedCount, 9)
assert.equal(allAckFailureScan.retainedCount, 1)
assert.equal(allAckFailureScan.failedCount, 1)
assert.equal(rebuildAfterAllAckFailure, 0, 'all rebuild 成功后即使其 ack 异常，也不得重复重建 personal')

const retainedService = new ModelCatalogSnapshotReconcileService({
  ...dependencies,
  listPendingRequests: async () => [personalRequests[0] as ModelCatalogSnapshotRebuildRequest],
  ackRequest: async () => false
})
assert.deepEqual(await retainedService.reconcileDirtyOnceAsync(), {
  capturedCount: 1,
  rebuildCount: 1,
  acknowledgedCount: 0,
  retainedCount: 1,
  failedCount: 0
})

let coveredPersonalAckCount = 0
let releaseCoveredPersonal: (() => void) | undefined
const coveredPersonalService = new ModelCatalogSnapshotReconcileService({
  ...dependencies,
  listPendingRequests: async () => [allRequest, personalRequests[0] as ModelCatalogSnapshotRebuildRequest],
  ackRequest: async (request) => {
    if (request.scope === 'personal') coveredPersonalAckCount += 1
    return true
  },
  rebuildPersonal: async () => {
    await new Promise<void>((resolve) => {
      releaseCoveredPersonal = resolve
    })
  }
})
const directPersonal = coveredPersonalService.reconcileScopeAsync({ scope: 'personal', systemAccountId: 'account-1' })
await delay(0)
const coveredScan = coveredPersonalService.reconcileDirtyOnceAsync()
await delay(20)
releaseCoveredPersonal?.()
await Promise.all([directPersonal, coveredScan])
assert.equal(coveredPersonalAckCount, 1, 'all covered ack 必须复用同 generation 的 personal singleflight，禁止重复确认')

let rebuildCount = 0
let releaseRebuild: (() => void) | undefined
const singleflightRequest = personalRequests[0]
const singleflightService = new ModelCatalogSnapshotReconcileService({
  ...dependencies,
  findPendingRequest: async () => singleflightRequest,
  rebuildPersonal: async () => {
    rebuildCount += 1
    await new Promise<void>((resolve) => {
      releaseRebuild = resolve
    })
  }
})
const first = singleflightService.reconcileScopeAsync({ scope: 'personal', systemAccountId: singleflightRequest.systemAccountId })
const second = singleflightService.reconcileScopeAsync({ scope: 'personal', systemAccountId: singleflightRequest.systemAccountId })
await delay(0)
assert.equal(rebuildCount, 1)
releaseRebuild?.()
assert.deepEqual(await Promise.all([first, second]), [
  { scope: 'personal', systemAccountId: 'account-1', generation: 1, acknowledged: true },
  { scope: 'personal', systemAccountId: 'account-1', generation: 1, acknowledged: true }
])

let generationFindCount = 0
let generationRebuildCount = 0
let releaseFirstGeneration: (() => void) | undefined
const nextGenerationRequest = { ...singleflightRequest, generation: 2 }
const generationService = new ModelCatalogSnapshotReconcileService({
  ...dependencies,
  findPendingRequest: async () => generationFindCount++ === 0 ? singleflightRequest : nextGenerationRequest,
  rebuildPersonal: async () => {
    generationRebuildCount += 1
    if (generationRebuildCount === 1) {
      await new Promise<void>((resolve) => {
        releaseFirstGeneration = resolve
      })
    }
  }
})
const firstGeneration = generationService.reconcileScopeAsync({ scope: 'personal', systemAccountId: 'account-1' })
const secondGeneration = generationService.reconcileScopeAsync({ scope: 'personal', systemAccountId: 'account-1' })
await delay(0)
assert.equal(generationRebuildCount, 1)
releaseFirstGeneration?.()
assert.deepEqual((await Promise.all([firstGeneration, secondGeneration])).map((result) => result?.generation), [1, 2])
assert.equal(generationRebuildCount, 2, '不同 generation 必须在同一 scope 上串行执行，不能错误复用旧 promise')

let failureAcked = false
const failureService = new ModelCatalogSnapshotReconcileService({
  ...dependencies,
  findPendingRequest: async () => allRequest,
  rebuildAll: async () => { throw new Error('rebuild failed') },
  ackRequest: async () => { failureAcked = true; return true }
})
await assert.rejects(() => failureService.reconcileScopeAsync({ scope: 'all' }), /rebuild failed/)
assert.equal(failureAcked, false)

console.log('模型目录 snapshot dirty/reconcile 协调回归通过')

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}
