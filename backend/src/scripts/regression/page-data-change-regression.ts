import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  createMemoryPageDataChangeStore,
  createRedisPageDataChangeStore,
  pageDataDomains,
  pageDataScope,
  type PageDataChangeEvent,
  type PageDataRedisClient
} from '../../modules/page-data/page-data-change.service.js'
import {
  createPageDataChangePublisher,
  resolveAccountPageDataOwners
} from '../../modules/page-data/page-data-change.publisher.js'
import {
  createPageDataDirtyDomainState,
  createPageDataPublishRetryQueue,
  createRecoveringPageDataChangeStore,
  acceptPageDataChangeFromIpc,
  acceptPageDataDirtyDomainsParentAck,
  dispatchPageDataDirtyDomainsForProcess,
  dispatchPageDataChangeForProcess,
  sendPageDataDirtyDomainsToParent
} from '../../modules/page-data/page-data-change.runtime.js'

const now = new Date('2026-07-17T00:00:00.000Z')
const firstBatchDomains = [
  'providers.catalog',
  'groups.static',
  'accounts.options',
  'systemAccounts.options',
  'teams.options',
  'routeStrategies.options',
  'stats.overview',
  'stats.accountUsage',
  'stats.aiPerformance'
] as const
for (const domain of firstBatchDomains) {
  assert.ok(pageDataDomains.includes(domain), `Node page-data store 必须支持第一批数据域 ${domain}`)
}
let recoveringConfirmCalls = 0
const recoveringStore = createRecoveringPageDataChangeStore({
  confirm: async () => {
    recoveringConfirmCalls += 1
    if (recoveringConfirmCalls === 1) throw new Error('runtime store unavailable')
    return {
      serverTime: now.toISOString(),
      domains: {
        'accounts.static': {
          action: 'unchanged',
          token: { protocolVersion: 2, epoch: 'recover', scope: 'scope', domain: 'accounts.static', sequence: 1, resetSequence: 0 }
        }
      }
    }
  },
  publish: async () => undefined
})
await assert.rejects(() => recoveringStore.confirm({ streamKey: 'owner:a', fingerprint: 'scope' }, { 'accounts.static': undefined }))
const recoveredConfirm = await recoveringStore.confirm({ streamKey: 'owner:a', fingerprint: 'scope' }, { 'accounts.static': undefined })
assert.equal(recoveredConfirm.domains['accounts.static']?.action, 'unchanged', '只读 confirm 失败不得把数据域写成 dirty')

const persistedDirty = new Map<string, number>()
let persistenceLoadCount = 0
const dirtyPersistence = {
  async mark(domain: string) {
    const generation = (persistedDirty.get(domain) ?? 0) + 1
    persistedDirty.set(domain, generation)
    return generation
  },
  async clear(domain: string, generation: number) {
    if (persistedDirty.get(domain) !== generation) return false
    return persistedDirty.delete(domain)
  }
}
persistedDirty.set('accounts.static', 1)
persistedDirty.set('accounts.runtime', 3)
const loadPersistedDirty = () => {
  persistenceLoadCount += 1
  return [...persistedDirty].map(([domain, generation]) => ({ domain, generation }))
}
const dirtyState = createPageDataDirtyDomainState({ initialRows: loadPersistedDirty(), persistence: dirtyPersistence })
const dirtyConfirmStore = createRecoveringPageDataChangeStore({
  confirm: async (_scope, domains) => ({
    serverTime: now.toISOString(),
    domains: Object.fromEntries(Object.keys(domains).map((domain) => [domain, {
      action: 'unchanged',
      token: { protocolVersion: 2, epoch: 'dirty', scope: 'scope', domain, sequence: 1, resetSequence: 0 }
    }]))
  }),
  publish: async () => undefined
}, { dirtyState })
const staticOnly = await dirtyConfirmStore.confirm({ streamKey: 'owner:a', fingerprint: 'scope' }, {
  'accounts.static': undefined,
  'accounts.runtime': undefined
})
assert.equal(staticOnly.domains['accounts.static']?.action, 'reset')
assert.equal(staticOnly.domains['accounts.runtime']?.action, 'reset')
assert.equal(persistenceLoadCount, 1, 'confirm 热路径只能读取启动时加载的内存 Map')

const recoveredDomains: string[] = []
await dirtyState.recover(async (dirtyEvent) => {
  recoveredDomains.push(dirtyEvent.domain)
  if (dirtyEvent.domain === 'accounts.static') await dirtyState.markDirty('accounts.runtime')
})
assert.deepEqual(recoveredDomains, ['accounts.static', 'accounts.runtime'])
assert.equal(persistedDirty.has('accounts.static'), false, '对应域 reset 成功后应逐域清理')
assert.equal(persistedDirty.get('accounts.runtime'), 4, '旧代际 reset 成功不得清理飞行中产生的新代际')
const afterStaticRecovery = await dirtyConfirmStore.confirm({ streamKey: 'owner:a', fingerprint: 'scope' }, {
  'accounts.static': undefined,
  'accounts.runtime': undefined
})
assert.equal(afterStaticRecovery.domains['accounts.static']?.action, 'unchanged', 'static 清理不能影响其他域')
assert.equal(afterStaticRecovery.domains['accounts.runtime']?.action, 'reset', 'runtime 新代际仍须 reset')

let persistedAbaGeneration: number | undefined = 1
let clearAbaEnteredResolve: (() => void) | undefined
const clearAbaEntered = new Promise<void>((resolve) => { clearAbaEnteredResolve = resolve })
let releaseAbaClearResolve: (() => void) | undefined
const releaseAbaClear = new Promise<void>((resolve) => { releaseAbaClearResolve = resolve })
let abaMarkEntered = false
let abaClearFinished = false
let abaMarkEnteredBeforeClearFinished = false
const abaState = createPageDataDirtyDomainState({
  initialRows: [{ domain: 'accounts.static', generation: 1 }],
  persistence: {
    async mark() {
      abaMarkEntered = true
      if (!abaClearFinished) abaMarkEnteredBeforeClearFinished = true
      persistedAbaGeneration = persistedAbaGeneration === undefined ? 1 : persistedAbaGeneration + 1
      return persistedAbaGeneration
    },
    async clear(_domain, generation) {
      if (persistedAbaGeneration !== generation) return false
      persistedAbaGeneration = undefined
      clearAbaEnteredResolve?.()
      await releaseAbaClear
      abaClearFinished = true
      return true
    }
  }
})
const abaRecovery = abaState.recover(async () => undefined)
await clearAbaEntered
const abaMark = abaState.markDirty('accounts.static')
await Promise.resolve()
releaseAbaClearResolve?.()
await Promise.all([abaRecovery, abaMark])
assert.equal(abaMarkEntered, true)
assert.equal(abaMarkEnteredBeforeClearFinished, false, '同一 domain 的 mark 不得插入到 DELETE CAS 与内存清理之间形成 generation ABA')
assert.equal(abaState.isDirty('accounts.static'), true, '恢复飞行中产生的新 dirty 必须保留在内存，不能被旧 generation 清理')

let singleflightPublishCount = 0
let releaseSingleflightPublishResolve: (() => void) | undefined
const releaseSingleflightPublish = new Promise<void>((resolve) => { releaseSingleflightPublishResolve = resolve })
let singleflightPublishEnteredResolve: (() => void) | undefined
const singleflightPublishEntered = new Promise<void>((resolve) => { singleflightPublishEnteredResolve = resolve })
const singleflightState = createPageDataDirtyDomainState({
  initialRows: [{ domain: 'accounts.runtime', generation: 1 }],
  persistence: {
    async mark() { return 2 },
    async clear() { return false }
  }
})
const singleflightStore = createRecoveringPageDataChangeStore({
  confirm: async () => ({ serverTime: now.toISOString(), domains: {} }),
  publish: async () => {
    singleflightPublishCount += 1
    singleflightPublishEnteredResolve?.()
    await releaseSingleflightPublish
  }
}, { dirtyState: singleflightState })
const concurrentRecoveries = [
  singleflightStore.recoverDirtyDomains(),
  singleflightStore.recoverDirtyDomains(),
  singleflightStore.recoverDirtyDomains()
]
await singleflightPublishEntered
releaseSingleflightPublishResolve?.()
await Promise.all(concurrentRecoveries)
assert.equal(singleflightPublishCount, 1, '并发 recovery 调用必须共享同一次飞行任务，不能递归串行重放 reset')

const inFlightRecoveryCallbacks: Array<() => Promise<void>> = []
let inFlightPublishCount = 0
let releaseInFlightPublishResolve: (() => void) | undefined
const releaseInFlightPublish = new Promise<void>((resolve) => { releaseInFlightPublishResolve = resolve })
let inFlightPublishEnteredResolve: (() => void) | undefined
const inFlightPublishEntered = new Promise<void>((resolve) => { inFlightPublishEnteredResolve = resolve })
const inFlightState = createPageDataDirtyDomainState({
  initialRows: [{ domain: 'accounts.static', generation: 1 }],
  persistence: {
    async mark() { return 2 },
    async clear() { return false }
  }
})
const inFlightStore = createRecoveringPageDataChangeStore({
  confirm: async () => ({ serverTime: now.toISOString(), domains: {} }),
  publish: async () => {
    inFlightPublishCount += 1
    inFlightPublishEnteredResolve?.()
    await releaseInFlightPublish
  }
}, {
  dirtyState: inFlightState,
  autoRecover: true,
  scheduleRecovery: (callback) => { inFlightRecoveryCallbacks.push(callback); return inFlightRecoveryCallbacks.length },
  cancelRecovery: () => undefined
})
const firstInFlightRecovery = inFlightStore.recoverDirtyDomains()
await inFlightPublishEntered
await inFlightStore.markDirty('accounts.static')
assert.equal(inFlightRecoveryCallbacks.length, 1, 'recovery 飞行中产生新 dirty 必须安排补偿 timer')
const earlyTimerRecovery = inFlightRecoveryCallbacks[0]?.()
assert(earlyTimerRecovery)
releaseInFlightPublishResolve?.()
await Promise.all([firstInFlightRecovery, earlyTimerRecovery])
assert.equal(inFlightPublishCount, 2, '补偿 timer 早于旧 recovery 完成时，必须追加恰好一轮 recovery')

const restartedState = createPageDataDirtyDomainState({ initialRows: loadPersistedDirty(), persistence: dirtyPersistence })
const restartedRecovered: string[] = []
await restartedState.recover(async (dirtyEvent) => { restartedRecovered.push(dirtyEvent.domain) })
assert.deepEqual(restartedRecovered, ['accounts.runtime'], 'DB service 重启必须从持久 dirty 行恢复逐域 reset')
assert.equal(persistedDirty.size, 0)

const recoveryCallbacks: Array<() => Promise<void>> = []
const retryingState = createPageDataDirtyDomainState({ initialRows: [{ domain: 'announcements.public', generation: 1 }] })
let recoveryPublishAttempts = 0
const retryingStore = createRecoveringPageDataChangeStore({
  confirm: async (_scope, domains) => ({
    serverTime: now.toISOString(),
    domains: Object.fromEntries(Object.keys(domains).map((domain) => [domain, {
      action: 'unchanged',
      token: { protocolVersion: 2, epoch: 'retry', scope: 'scope', domain, sequence: 1, resetSequence: 1 }
    }]))
  }),
  publish: async () => {
    recoveryPublishAttempts += 1
    if (recoveryPublishAttempts === 1) throw new Error('redis unavailable')
  }
}, {
  dirtyState: retryingState,
  autoRecover: true,
  scheduleRecovery: (callback) => { recoveryCallbacks.push(callback); return recoveryCallbacks.length },
  cancelRecovery: () => undefined
})
await assert.rejects(() => retryingStore.recoverDirtyDomains(), /redis unavailable/)
assert.equal(recoveryCallbacks.length, 1, '启动恢复失败必须保留 dirty 并安排后台重试')
await recoveryCallbacks[0]?.()
assert.equal(recoveryPublishAttempts, 2, '后台恢复重试必须重新发布 allScopes reset')
const afterRetryRecovery = await retryingStore.confirm({ streamKey: 'global', fingerprint: 'scope' }, {
  'announcements.public': undefined
})
assert.equal(afterRetryRecovery.domains['announcements.public']?.action, 'unchanged', 'Redis 恢复后无需新 dirty 或重启，后续 confirm 应恢复 unchanged')

const retryCallbacks: Array<() => Promise<void>> = []
let retryAttempts = 0
const retryQueue = createPageDataPublishRetryQueue({
  deliver: async () => {
    retryAttempts += 1
    if (retryAttempts === 1) throw new Error('publish failed')
  },
  schedule: (callback) => { retryCallbacks.push(callback); return retryCallbacks.length },
  cancel: () => undefined
})
await assert.rejects(() => retryQueue.publish({
  eventId: 'retry-event', domain: 'accounts.static', operation: 'range_reset', fieldMask: [], ownerSystemAccountIds: ['owner-a'],
  membershipChanged: true, orderChanged: true, filterChanged: true, pageChanged: true, occurredAt: now.toISOString()
}))
assert.equal(retryQueue.pendingCount, 1)
await retryCallbacks[0]?.()
assert.equal(retryQueue.pendingCount, 0, 'publish 失败事件必须在进程生命周期内重试成功')

let dirtyFailure = true
const dirtyDelivered: PageDataChangeEvent[] = []
const dirtyQueue = createPageDataPublishRetryQueue({
  maxPending: 4,
  deliver: async (queuedEvent) => {
    if (dirtyFailure) throw new Error('persistent publish failure')
    dirtyDelivered.push(queuedEvent)
  },
  schedule: () => 1,
  cancel: () => undefined,
  now: () => now
})
for (let index = 0; index < 5; index += 1) {
  await assert.rejects(() => dirtyQueue.publish({
    eventId: `overflow-${index}`, domain: 'accounts.static', operation: 'upsert', fieldMask: ['name'], ownerSystemAccountIds: ['owner-a'],
    membershipChanged: false, orderChanged: false, filterChanged: false, pageChanged: false, occurredAt: now.toISOString()
  }))
}
assert.equal(dirtyQueue.pendingCount, 1, '重试队列溢出必须折叠为有界 domain dirty reset')
dirtyFailure = false
await dirtyQueue.flush()
assert.equal(dirtyDelivered[0]?.allScopes, true)
assert.equal(dirtyDelivered[0]?.operation, 'range_reset')
const store = createMemoryPageDataChangeStore({ epoch: 'test-epoch', now: () => now })
const selfScope = pageDataScope({ viewerSystemAccountId: 'user-a', viewScope: 'self' })
const otherScope = pageDataScope({ viewerSystemAccountId: 'user-b', viewScope: 'self' })
const adminTargetScope = pageDataScope({
  viewerSystemAccountId: 'admin-a',
  viewScope: 'admin',
  targetSystemAccountId: 'user-a'
})

const initial = await store.confirm(selfScope, {
  'accounts.static': undefined,
  'accounts.runtime': undefined,
  'usage.records': undefined
})
assert.equal(initial.domains['accounts.static']?.action, 'reload')
assert.equal(initial.domains['accounts.runtime']?.action, 'reload')
assert.equal(initial.domains['accounts.runtime']?.token.epoch, 'test-epoch')

const baseToken = initial.domains['accounts.runtime']?.token
assert(baseToken)
const adminBaseToken = (await store.confirm(adminTargetScope, {
  'accounts.runtime': undefined
})).domains['accounts.runtime']?.token
assert(adminBaseToken)
const event: PageDataChangeEvent = {
  eventId: 'evt-1',
  domain: 'accounts.runtime',
  entityId: 'account-a',
  operation: 'upsert',
  fieldMask: ['status'],
  ownerSystemAccountIds: ['user-a'],
  membershipChanged: false,
  orderChanged: false,
  filterChanged: false,
  pageChanged: false,
  occurredAt: now.toISOString()
}
await store.publish(event)

const changed = await store.confirm(selfScope, { 'accounts.runtime': baseToken })
assert.equal(changed.domains['accounts.runtime']?.action, 'delta')
assert.deepEqual(changed.domains['accounts.runtime']?.changes, [{
  entityId: 'account-a',
  operation: 'upsert',
  fieldMask: ['status'],
  membershipChanged: false,
  orderChanged: false,
  filterChanged: false,
  pageChanged: false
}])

const unchanged = await store.confirm(selfScope, {
  'accounts.runtime': changed.domains['accounts.runtime']?.token
})
assert.equal(unchanged.domains['accounts.runtime']?.action, 'unchanged')

const other = await store.confirm(otherScope, {
  'accounts.runtime': (await store.confirm(otherScope, { 'accounts.runtime': undefined })).domains['accounts.runtime']?.token
})
assert.equal(other.domains['accounts.runtime']?.action, 'unchanged', '其他用户不能看到 user-a 的变更')

const usageSelfBase = (await store.confirm(selfScope, { 'usage.records': undefined })).domains['usage.records']?.token
assert(usageSelfBase)
await store.publish({
  ...event,
  eventId: 'global-usage-reset',
  domain: 'usage.records',
  operation: 'range_reset',
  ownerSystemAccountIds: [],
  allScopes: true
})
const usageSelfReset = await store.confirm(selfScope, { 'usage.records': usageSelfBase })
assert.equal(usageSelfReset.domains['usage.records']?.action, 'reset', 'global usage reset 必须对 self owner scope 生效')
assert.equal(usageSelfReset.domains['usage.records']?.token.resetSequence, 1)

const publicBase = (await store.confirm(otherScope, {
  'announcements.public': undefined
})).domains['announcements.public']?.token
assert(publicBase)
await store.publish({
  ...event,
  eventId: 'evt-public',
  domain: 'announcements.public',
  entityId: 'announcement-a',
  ownerSystemAccountIds: []
})
const publicChanged = await store.confirm(otherScope, { 'announcements.public': publicBase })
assert.equal(publicChanged.domains['announcements.public']?.action, 'delta', '公开全局域必须从 global stream 向普通用户确认变更')

const adminTarget = await store.confirm(adminTargetScope, { 'accounts.runtime': adminBaseToken })
assert.equal(adminTarget.domains['accounts.runtime']?.action, 'delta', '管理员目标用户作用域应看到该用户变更')

const resetEvent: PageDataChangeEvent = {
  ...event,
  eventId: 'evt-reset',
  operation: 'range_reset',
  fieldMask: []
}
await store.publish(resetEvent)
const reset = await store.confirm(selfScope, {
  'accounts.runtime': changed.domains['accounts.runtime']?.token
})
assert.equal(reset.domains['accounts.runtime']?.action, 'reset')

await assert.rejects(
  () => store.confirm(selfScope, Object.fromEntries(Array.from({ length: 33 }, (_, index) => [`unknown.${index}`, undefined]))),
  /最多确认 32 个数据域/
)
await assert.rejects(
  () => store.confirm(selfScope, { 'unknown.domain': undefined }),
  /不支持的数据域/
)

const redisClient = createFakePageDataRedisClient()
const redisStoreA = createRedisPageDataChangeStore({ client: redisClient, keyPrefix: 'test:page-data', epoch: 'redis-epoch' })
const redisStoreB = createRedisPageDataChangeStore({ client: redisClient, keyPrefix: 'test:page-data', epoch: 'ignored-second-epoch' })
const redisBase = (await redisStoreA.confirm(selfScope, { 'usage.records': undefined })).domains['usage.records']?.token
assert(redisBase)
assert.deepEqual(redisClient.commandCounts(), { eval: 1, get: 0, set: 0, sendCommand: 0 }, '单域首次 confirm 必须恰好一次 EVAL')
redisClient.resetCommandCounts()
const redisInitialUnchanged = await redisStoreA.confirm(selfScope, { 'usage.records': redisBase })
assert.equal(redisInitialUnchanged.domains['usage.records']?.action, 'unchanged')
assert.deepEqual(redisClient.commandCounts(), { eval: 1, get: 0, set: 0, sendCommand: 0 }, '单域 unchanged confirm 必须恰好一次 EVAL 且无旁路命令')

for (const domains of [
  ['accounts.static', 'accounts.runtime', 'usage.records'],
  ['accounts.static', 'accounts.runtime', 'usage.records', 'announcements.public']
] as const) {
  redisClient.resetCommandCounts()
  const base = await redisStoreA.confirm(selfScope, Object.fromEntries(domains.map((domain) => [domain, undefined])))
  assert.deepEqual(redisClient.commandCounts(), { eval: 1, get: 0, set: 0, sendCommand: 0 }, `${domains.length} 域首次 confirm 必须恰好一次 EVAL`)
  redisClient.resetCommandCounts()
  const unchangedResult = await redisStoreA.confirm(selfScope, Object.fromEntries(domains.map((domain) => [domain, base.domains[domain]?.token])))
  assert(domains.every((domain) => unchangedResult.domains[domain]?.action === 'unchanged'))
  assert.deepEqual(redisClient.commandCounts(), { eval: 1, get: 0, set: 0, sendCommand: 0 }, `${domains.length} 域 unchanged confirm 必须恰好一次 EVAL 且无旁路命令`)
}
redisClient.resetCommandCounts()
await redisStoreA.publish({
  ...event,
  eventId: 'redis-event-1',
  domain: 'usage.records',
  operation: 'append',
  fieldMask: ['createdAt']
})
const redisChanged = await redisStoreB.confirm(selfScope, { 'usage.records': redisBase })
assert.equal(redisChanged.domains['usage.records']?.action, 'delta', '不同进程实例必须共享 Redis epoch 和变更序列')
assert.equal(redisChanged.domains['usage.records']?.token.epoch, 'redis-epoch')
assert.equal(redisClient.commandCounts().eval >= 2, true, 'publish 与后续 confirm 分别使用 Lua；publish 命令不计入 confirm 门禁')
await redisStoreA.publish({
  ...event,
  eventId: 'redis-event-1',
  domain: 'usage.records',
  operation: 'append',
  fieldMask: ['createdAt']
})
const redisDeduplicated = await redisStoreB.confirm(selfScope, {
  'usage.records': redisChanged.domains['usage.records']?.token
})
assert.equal(redisDeduplicated.domains['usage.records']?.action, 'unchanged', '重复 eventId 必须幂等')

const redisOldEpoch = await redisStoreB.confirm(selfScope, {
  'usage.records': { ...redisChanged.domains['usage.records']!.token, epoch: 'old-epoch' }
})
assert.equal(redisOldEpoch.domains['usage.records']?.action, 'reload', '旧 epoch token 必须 reload')

const redisResetBase = (await redisStoreA.confirm(selfScope, { 'accounts.static': undefined })).domains['accounts.static']?.token
assert(redisResetBase)
await redisStoreA.publish({ ...event, eventId: 'redis-global-reset', domain: 'accounts.static', operation: 'range_reset', allScopes: true })
const redisGlobalReset = await redisStoreB.confirm(selfScope, { 'accounts.static': redisResetBase })
assert.equal(redisGlobalReset.domains['accounts.static']?.action, 'reset', 'Redis 全域 reset sequence 必须对 owner scope 生效')

const gapClient = createFakePageDataRedisClient()
const gapStore = createRedisPageDataChangeStore({ client: gapClient, keyPrefix: 'test:page-data-gap', epoch: 'gap-epoch', maxEventsPerStream: 1 })
const gapBase = (await gapStore.confirm(selfScope, { 'accounts.runtime': undefined })).domains['accounts.runtime']?.token
assert(gapBase)
await gapStore.publish({ ...event, eventId: 'gap-1' })
await gapStore.publish({ ...event, eventId: 'gap-2' })
const gapResult = await gapStore.confirm(selfScope, { 'accounts.runtime': gapBase })
assert.equal(gapResult.domains['accounts.runtime']?.action, 'reset', 'Redis 日志缺口必须 reset')

const rangeBase = gapResult.domains['accounts.runtime']?.token
assert(rangeBase)
await gapStore.publish({ ...event, eventId: 'range-reset', operation: 'range_reset', fieldMask: [] })
const rangeResult = await gapStore.confirm(selfScope, { 'accounts.runtime': rangeBase })
assert.equal(rangeResult.domains['accounts.runtime']?.action, 'reset', 'Redis range_reset 事件必须 reset')

const malformedMiddle = await createRedisLogIntegrityScenario('malformed-middle')
malformedMiddle.client.mutateLogEntry(2, () => '2\t{malformed')
const malformedMiddleResult = await malformedMiddle.store.confirm(selfScope, { 'accounts.runtime': malformedMiddle.baseToken })
assert.equal(malformedMiddleResult.domains['accounts.runtime']?.action, 'reset', 'Redis 日志中间坏项不能被过滤后误判为 delta')

const jumpedSequence = await createRedisLogIntegrityScenario('jumped-sequence')
jumpedSequence.client.mutateLogEntry(2, (entry) => entry.replace(/^2\t/, '4\t'))
const jumpedSequenceResult = await jumpedSequence.store.confirm(selfScope, { 'accounts.runtime': jumpedSequence.baseToken })
assert.equal(jumpedSequenceResult.domains['accounts.runtime']?.action, 'reset', 'Redis 日志序号跳跃必须 reset')

const malformedTail = await createRedisLogIntegrityScenario('malformed-tail')
malformedTail.client.mutateLogEntry(3, () => '3\t{malformed')
const malformedTailResult = await malformedTail.store.confirm(selfScope, { 'accounts.runtime': malformedTail.baseToken })
assert.equal(malformedTailResult.domains['accounts.runtime']?.action, 'reset', 'Redis 日志尾部坏项导致尾序号缺失时必须 reset')

const serviceSource = readFileSync(resolve('src/modules/page-data/page-data-change.service.ts'), 'utf8')
assert.doesNotMatch(serviceSource, /EXPIRE', sequenceKey/, '共享 sequence 不得过期回绕后复用同一 epoch')
const fakeBenchmarkSource = readFileSync(resolve('src/scripts/performance/page-data-confirm-benchmark.ts'), 'utf8')
assert.match(fakeBenchmarkSource, /4-domain-sequential/, 'fake benchmark 必须覆盖 4 域顺序 confirm')
assert.match(fakeBenchmarkSource, /4-domain-concurrent-20/, 'fake benchmark 必须覆盖 4 域 20 并发 confirm')
const backendPackage = JSON.parse(readFileSync(resolve('package.json'), 'utf8')) as { scripts?: Record<string, string> }
assert.match(backendPackage.scripts?.['benchmark:page-data-confirm'] ?? '', /page-data-confirm-redis-benchmark/, '默认 page-data confirm benchmark 必须指向真实 Redis 实现')
assert.match(backendPackage.scripts?.['benchmark:page-data-confirm-fake'] ?? '', /page-data-confirm-benchmark/, 'fake benchmark 必须使用明确的独立命令')
const redisBenchmarkPath = resolve('src/scripts/performance/page-data-confirm-redis-benchmark.ts')
assert.equal(existsSync(redisBenchmarkPath), true, '必须提供独立的真实 Redis page-data confirm benchmark')
const redisBenchmarkSource = readFileSync(redisBenchmarkPath, 'utf8')
assert.match(redisBenchmarkSource, /createDedicatedRedisClient/, '真实 Redis benchmark 必须建立专用 Redis 连接')
assert.match(redisBenchmarkSource, /JUHE_AI_PAGE_DATA_CONFIRM_BENCHMARK_REDIS_URL/, '真实 Redis benchmark 必须要求专用环境变量')
assert.doesNotMatch(redisBenchmarkSource, /createBenchmarkRedisClient|createFakePageDataRedisClient/, '真实 Redis benchmark 禁止 fallback 到 fake client')
const systemAppSource = readFileSync(resolve('src/modules/system-api/system-api-app.ts'), 'utf8')
assert.match(systemAppSource, /createPageDataChangesRouter/, '有真实写端后必须挂载页面变更确认路由')
assert.match(systemAppSource, /\/data-changes/, '页面变更确认路由必须使用稳定路径')

const published: PageDataChangeEvent[] = []
const resolvedOwners = await resolveAccountPageDataOwners({
  accountId: 'source-account',
  ownerSystemAccountIds: ['owner-a'],
  loadGranteeIds: async () => ['direct-a', 'team-member-a', 'direct-a']
})
assert.deepEqual(resolvedOwners, { ownerSystemAccountIds: ['direct-a', 'owner-a', 'team-member-a'], allScopes: false })
const fallbackOwners = await resolveAccountPageDataOwners({
  accountId: 'source-account',
  ownerSystemAccountIds: ['owner-a'],
  loadGranteeIds: async () => { throw new Error('authorization query failed') }
})
assert.equal(fallbackOwners.allScopes, true, '授权 fanout 查询失败必须降级全域 reset')
const timers: Array<() => Promise<void>> = []
const publisher = createPageDataChangePublisher({
  publish: async (publishedEvent) => {
    published.push(publishedEvent)
  },
  now: () => now,
  usageThrottleMs: 250,
  schedule: (callback) => {
    timers.push(callback)
    return timers.length
  },
  cancel: () => undefined
})
await publisher.publishAccountStatic({
  accountId: 'account-a',
  ownerSystemAccountIds: ['user-a'],
  operation: 'upsert',
  fieldMask: ['name']
})
await publisher.publishAccountRuntime({
  accountId: 'account-a',
  ownerSystemAccountIds: ['user-a'],
  fieldMask: ['status']
})
publisher.publishUsageRecords([{ id: 'usage-a', systemAccountId: 'user-a' }])
publisher.publishUsageRecords([{ id: 'usage-b', systemAccountId: 'user-b' }])
assert.equal(timers.length, 1, '高频使用记录写入只能安排一次节流发布')
await timers[0]?.()
assert.deepEqual(published.map((item) => item.domain), [
  'accounts.static',
  'accounts.runtime',
  'usage.records'
])
assert.deepEqual(published[2]?.ownerSystemAccountIds, ['user-a', 'user-b'])
assert.equal(published[2]?.pageChanged, true, '新使用记录会改变首页窗口，必须要求 reload')
publisher.publishUsageRecords([{ id: 'usage-global' }])
assert.equal(timers.length, 2)
await timers[1]?.()
assert.equal(published[3]?.domain, 'usage.records', '缺少 owner 的管理员可见记录也必须发布 global 变更')
assert.deepEqual(published[3]?.ownerSystemAccountIds, [])

publisher.publishUsageRecords(Array.from({ length: 300 }, (_, index) => ({
  id: `usage-many-${index}`,
  systemAccountId: `user-many-${index}`
})))
assert.equal(timers.length, 3)
await timers[2]?.()
const manyOwnerEvents = published.slice(4)
assert.equal(manyOwnerEvents.length, 2, '大批次 owner 必须有界分片发布')
assert.equal(manyOwnerEvents.flatMap((item) => item.ownerSystemAccountIds).length, 300)

const routed: string[] = []
await dispatchPageDataChangeForProcess(event, {
  runtimeStateDriver: 'memory',
  processRole: 'db-service',
  publishLocal: async () => { routed.push('db-local') },
  sendToParent: async () => { routed.push('parent') },
  sendToDbService: async () => { routed.push('db-child') }
})
await dispatchPageDataChangeForProcess(event, {
  runtimeStateDriver: 'memory',
  processRole: 'worker',
  publishLocal: async () => { routed.push('worker-local') },
  sendToParent: async () => { routed.push('parent') },
  sendToDbService: async () => { routed.push('db-child') }
})
await dispatchPageDataChangeForProcess(event, {
  runtimeStateDriver: 'memory',
  processRole: 'server',
  publishLocal: async () => { routed.push('server-local') },
  sendToParent: async () => { routed.push('parent') },
  sendToDbService: async () => { routed.push('db-child') }
})
await dispatchPageDataChangeForProcess(event, {
  runtimeStateDriver: 'redis',
  processRole: 'worker',
  publishLocal: async () => { routed.push('redis') },
  sendToParent: async () => { routed.push('parent') },
  sendToDbService: async () => { routed.push('db-child') }
})
assert.deepEqual(routed, ['db-local', 'parent', 'server-local', 'db-child', 'redis'], 'standalone server 必须本地提交并镜像 DB service，performance 直接走 Redis')

const redisFallback: string[] = []
await dispatchPageDataChangeForProcess(event, {
  runtimeStateDriver: 'redis',
  processRole: 'worker',
  publishLocal: async () => { throw new Error('redis publish failed') },
  sendToParent: async () => { redisFallback.push('server-ipc') },
  sendToDbService: async () => undefined
})
assert.deepEqual(redisFallback, ['server-ipc'], 'performance worker Redis publish 失败必须 fallback 到 server IPC')

const acceptedIpcSteps: string[] = []
await acceptPageDataChangeFromIpc(event, {
  invalidateDomain: async (domain) => { acceptedIpcSteps.push(`invalidate:${domain}`) },
  publishLocal: async (acceptedEvent) => { acceptedIpcSteps.push(`publish:${acceptedEvent.domain}`) }
})
assert.deepEqual(
  acceptedIpcSteps,
  [`invalidate:${event.domain}`, `publish:${event.domain}`],
  'DB service 接收 worker page-data IPC 时必须先清理后端 read-through 缓存，再推进 revision'
)

const dirtyHandoffs: string[] = []
await dispatchPageDataDirtyDomainsForProcess(['accounts.runtime'], {
  processRole: 'worker',
  recoverLocal: async () => { dirtyHandoffs.push('recover') },
  sendToParent: async (domains) => { dirtyHandoffs.push(`parent:${domains.join(',')}`) },
  sendToDbService: async (domains) => { dirtyHandoffs.push(`db:${domains.join(',')}`) }
})
dirtyHandoffs.push('worker-exit')
await dispatchPageDataDirtyDomainsForProcess(['accounts.static', 'accounts.runtime'], {
  processRole: 'server',
  recoverLocal: async () => { dirtyHandoffs.push('recover') },
  sendToParent: async (domains) => { dirtyHandoffs.push(`parent:${domains.join(',')}`) },
  sendToDbService: async (domains) => { dirtyHandoffs.push(`db:${domains.join(',')}`) }
})
assert.deepEqual(dirtyHandoffs, [
  'parent:accounts.runtime',
  'worker-exit',
  'db:accounts.static,accounts.runtime'
], 'worker 失败必须等待父进程接收 dirty domain 后才能退出，server 再批量转交 DB service')

const originalProcessSend = process.send
let parentDirtyRequest: { requestId?: string } | undefined
Object.defineProperty(process, 'send', {
  configurable: true,
  writable: true,
  value: (message: { requestId?: string }, callback?: (error: Error | null) => void) => {
    parentDirtyRequest = message
    callback?.(null)
    return true
  }
})
try {
  const dirtyAckFailure = sendPageDataDirtyDomainsToParent(['accounts.runtime'], 1_000)
  await Promise.resolve()
  assert.equal(typeof parentDirtyRequest?.requestId, 'string', 'worker dirty IPC 必须携带 requestId')
  let dirtyAckSettled = false
  void dirtyAckFailure.then(() => { dirtyAckSettled = true }, () => { dirtyAckSettled = true })
  await Promise.resolve()
  assert.equal(dirtyAckSettled, false, 'worker dirty IPC 不能把 process.send 传输回调误当作持久化完成')
  acceptPageDataDirtyDomainsParentAck(parentDirtyRequest?.requestId ?? '', false, 'dirty persistence failed')
  await assert.rejects(dirtyAckFailure, /dirty persistence failed/, '传输成功但持久化 ACK 失败时 worker 必须保留失败结果供上层重试')
} finally {
  Object.defineProperty(process, 'send', { configurable: true, writable: true, value: originalProcessSend })
}

console.log('页面数据变更确认回归通过：全局/owner 作用域、Redis 代际和未公开边界生效')

interface FakePageDataRedisClient extends PageDataRedisClient {
  commandCounts(): { eval: number; get: number; set: number; sendCommand: number }
  resetCommandCounts(): void
  mutateLogEntry(sequence: number, mutate: (entry: string) => string): void
}

function createFakePageDataRedisClient(): FakePageDataRedisClient {
  const strings = new Map<string, string>()
  const lists = new Map<string, string[]>()
  const sets = new Map<string, Set<string>>()
  const counts = { eval: 0, get: 0, set: 0, sendCommand: 0 }
  return {
    commandCounts: () => ({ ...counts }),
    resetCommandCounts: () => { counts.eval = 0; counts.get = 0; counts.set = 0; counts.sendCommand = 0 },
    mutateLogEntry: (sequence, mutate) => {
      for (const [key, entries] of lists) {
        lists.set(key, entries.map((entry) => entry.startsWith(`${sequence}\t`) ? mutate(entry) : entry))
      }
    },
    async get(key) {
      counts.get += 1
      return strings.get(key) ?? null
    },
    async set(key, value, options) {
      counts.set += 1
      if (options?.NX === true && strings.has(key)) return null
      strings.set(key, value)
      return 'OK'
    },
    async eval(script, options) {
      counts.eval += 1
      if (script.includes('page_data_confirm_v1')) {
        const [epochKey, ...streamKeys] = options.keys
        const [proposedEpoch, domainCountText, ...knownTokens] = options.arguments
        assert(epochKey && proposedEpoch && domainCountText)
        if (!strings.has(epochKey)) strings.set(epochKey, proposedEpoch)
        const epoch = strings.get(epochKey)!
        const domainCount = Number(domainCountText)
        const result: unknown[] = [epoch]
        for (let index = 0; index < domainCount; index += 1) {
          const sequenceKey = streamKeys[index * 3]
          const logKey = streamKeys[index * 3 + 1]
          const resetSequenceKey = streamKeys[index * 3 + 2]
          assert(sequenceKey && logKey && resetSequenceKey)
          const sequence = Number(strings.get(sequenceKey) ?? 0)
          const resetSequence = Number(strings.get(resetSequenceKey) ?? 0)
          const knownEpoch = knownTokens[index * 3]
          const knownSequence = Number(knownTokens[index * 3 + 1])
          const knownResetSequence = Number(knownTokens[index * 3 + 2])
          const rawLog = knownEpoch === epoch
            && knownSequence >= 0
            && knownSequence < sequence
            && knownResetSequence === resetSequence
            ? [...(lists.get(logKey) ?? [])]
            : []
          result.push([sequence, resetSequence, rawLog])
        }
        return result
      }
      if (!script.includes('page_data_publish_v1')) throw new Error('Fake Redis 收到未知 Lua 脚本')
      const [sequenceKey, logKey, dedupeKey] = options.keys
      const [eventId, rawEvent, maxEventsText] = options.arguments
      assert(sequenceKey && logKey && dedupeKey && eventId && rawEvent && maxEventsText)
      const dedupe = sets.get(dedupeKey) ?? new Set<string>()
      if (dedupe.has(eventId)) return Number(strings.get(sequenceKey) ?? 0)
      dedupe.add(eventId)
      sets.set(dedupeKey, dedupe)
      const sequence = Number(strings.get(sequenceKey) ?? 0) + 1
      strings.set(sequenceKey, String(sequence))
      const list = lists.get(logKey) ?? []
      list.push(`${sequence}\t${rawEvent}`)
      const maxEvents = Number(maxEventsText)
      lists.set(logKey, list.slice(Math.max(0, list.length - maxEvents)))
      return sequence
    },
    async sendCommand(command) {
      counts.sendCommand += 1
      const [name, key, startText, endText] = command
      if (name !== 'LRANGE' || !key || startText === undefined || endText === undefined) {
        throw new Error(`Fake Redis 不支持命令：${command.join(' ')}`)
      }
      const list = lists.get(key) ?? []
      const start = Number(startText)
      const end = Number(endText)
      return list.slice(start, end < 0 ? undefined : end + 1)
    }
  }
}

async function createRedisLogIntegrityScenario(keySuffix: string): Promise<{
  client: FakePageDataRedisClient
  store: ReturnType<typeof createRedisPageDataChangeStore>
  baseToken: NonNullable<Awaited<ReturnType<ReturnType<typeof createRedisPageDataChangeStore>['confirm']>>['domains']['accounts.runtime']>['token']
}> {
  const client = createFakePageDataRedisClient()
  const store = createRedisPageDataChangeStore({ client, keyPrefix: `test:page-data-${keySuffix}`, epoch: `${keySuffix}-epoch` })
  const baseToken = (await store.confirm(selfScope, { 'accounts.runtime': undefined })).domains['accounts.runtime']?.token
  assert(baseToken)
  for (let sequence = 1; sequence <= 3; sequence += 1) {
    await store.publish({ ...event, eventId: `${keySuffix}-${sequence}` })
  }
  return { client, store, baseToken }
}
