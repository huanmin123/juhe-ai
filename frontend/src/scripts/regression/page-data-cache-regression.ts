import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import type { PageDataConfirmRequest, PageDataConfirmResult, PageDataRevisionToken } from '../../api/domains/pageData'
import type {
  PageDataActivationDecision,
  PageDataActivationHandle,
  PageDataActivationParticipant,
  PageDataActivationPhase
} from '../../shared/pageDataActivationCoordinator'
import {
  BrowserPageDataTabCoordinator,
  PageDataCacheController,
  PageDataRequestCacheManager,
  PageDataVisibleConfirmScheduler,
  createPageDataActivationController,
  type PageDataCacheRecord,
  type PageDataCacheStorage,
  createMemoryPageDataCacheStorage,
  createPageDataCacheKey,
  createPageDataCacheStorage
} from '../../shared/pageDataCache'

const token = (sequence: number, epoch = 'epoch-1'): PageDataRevisionToken => ({
  protocolVersion: 2,
  epoch,
  scope: 'scope-1',
  domain: 'accounts.runtime',
  sequence,
  resetSequence: 0
})

const domainToken = (domain: PageDataRevisionToken['domain'], sequence: number): PageDataRevisionToken => ({
  ...token(sequence),
  domain
})

const confirmResult = (known: PageDataRevisionToken | null | undefined, current = token(1)): PageDataConfirmResult => ({
  serverTime: '2026-07-17T12:00:00.000Z',
  domains: {
    'accounts.runtime': {
      action: known && known.sequence === current.sequence && known.epoch === current.epoch ? 'unchanged' : 'reload',
      token: current
    }
  }
})

const cacheRecord = <T>(key: string, value: T, options: Partial<PageDataCacheRecord<T>> = {}): PageDataCacheRecord<T> => ({
  key,
  domain: 'accounts.runtime',
  scope: 'self:fixture',
  route: '/fixture',
  value,
  writtenAt: '2026-07-17T12:00:00.000Z',
  lastAccessedAt: '2026-07-17T12:00:00.000Z',
  expiresAt: '2030-07-17T12:00:00.000Z',
  ...options
})

class FakeChannelHub {
  private readonly channels = new Set<FakeChannel>()
  readonly factory = (_name: string): Pick<BroadcastChannel, 'postMessage' | 'close' | 'onmessage'> => {
    const channel = new FakeChannel(this)
    this.channels.add(channel)
    return channel
  }

  publish(sender: FakeChannel, value: unknown): void {
    for (const channel of this.channels) {
      if (channel !== sender) channel.onmessage?.({ data: value } as MessageEvent)
    }
  }

  remove(channel: FakeChannel): void {
    this.channels.delete(channel)
  }
}

class FakeChannel {
  onmessage: ((this: BroadcastChannel, ev: MessageEvent) => unknown) | null = null
  constructor(private readonly hub: FakeChannelHub) {}
  postMessage(value: unknown): void { this.hub.publish(this, value) }
  close(): void { this.hub.remove(this) }
}

const canonicalKey = createPageDataCacheKey({ scope: 'self:sys-1', route: '/accounts', query: { page: 1, filters: { status: 'active', tag: ['a'] } }, version: 3, domain: 'accounts.runtime' })
assert.equal(canonicalKey, createPageDataCacheKey({ scope: 'self:sys-1', route: '/accounts', query: { filters: { tag: ['a'], status: 'active' }, page: 1 }, version: 3, domain: 'accounts.runtime' }), 'query 字段顺序不能改变缓存 key')
assert.notEqual(canonicalKey, createPageDataCacheKey({ scope: 'admin:sys-1', route: '/accounts', query: { page: 1, filters: { status: 'active', tag: ['a'] } }, version: 3, domain: 'accounts.runtime' }), '权限 scope 必须隔离缓存')
assert.notEqual(canonicalKey, createPageDataCacheKey({ scope: 'self:sys-1', route: '/usage-records', query: { page: 1, filters: { status: 'active', tag: ['a'] } }, version: 3, domain: 'accounts.runtime' }), 'route 必须隔离缓存')
assert.notEqual(canonicalKey, createPageDataCacheKey({ scope: 'self:sys-1', route: '/accounts', query: { page: 1, filters: { status: 'active', tag: ['a'] } }, version: 4, domain: 'accounts.runtime' }), 'schema version 必须隔离缓存')
assert.notEqual(canonicalKey, createPageDataCacheKey({ scope: 'self:sys-1', route: '/accounts', query: { page: 1, filters: { status: 'active', tag: ['a'] } }, version: 3, domain: 'accounts.static' }), 'domain 必须隔离缓存')

const fallbackStorage = createPageDataCacheStorage({ indexedDB: undefined })
assert.equal(await fallbackStorage.writeIfCurrent(cacheRecord('fallback', 1)), true, 'IndexedDB 不可用时必须降级内存缓存')
assert.equal((await fallbackStorage.read<number>('fallback'))?.value, 1)
const monotonicStorage = createMemoryPageDataCacheStorage()
assert.equal(await monotonicStorage.writeIfCurrent(cacheRecord('monotonic', 'new-epoch', { token: token(0, 'epoch-2'), confirmedAt: '2026-07-17T12:00:00.000Z' })), true)
assert.equal(await monotonicStorage.writeIfCurrent(cacheRecord('monotonic', 'late-old-epoch', { token: token(99, 'epoch-1'), confirmedAt: '2026-07-17T11:59:59.000Z', writtenAt: '2026-07-17T12:01:00.000Z' })), false, '旧 epoch 的迟到响应不得按本地完成时间覆盖新 epoch')
assert.equal((await monotonicStorage.read<string>('monotonic'))?.value, 'new-epoch')

let boundedNowMs = Date.parse('2026-07-17T12:00:00.000Z')
const boundedStorage = createMemoryPageDataCacheStorage({ maxEntries: 2, now: () => new Date(boundedNowMs) })
await boundedStorage.writeIfCurrent(cacheRecord('expired', 0, { expiresAt: '2026-07-17T11:59:59.999Z' }))
assert.equal(await boundedStorage.read('expired'), undefined, '超过 7 天硬过期边界的缓存不得命中')
await boundedStorage.writeIfCurrent(cacheRecord('lru-a', 1, { lastAccessedAt: new Date(boundedNowMs).toISOString() }))
boundedNowMs += 1_000
await boundedStorage.writeIfCurrent(cacheRecord('lru-b', 2, { writtenAt: new Date(boundedNowMs).toISOString(), lastAccessedAt: new Date(boundedNowMs).toISOString() }))
boundedNowMs += 1_000
await boundedStorage.read('lru-a')
boundedNowMs += 1_000
await boundedStorage.writeIfCurrent(cacheRecord('lru-c', 3, { writtenAt: new Date(boundedNowMs).toISOString(), lastAccessedAt: new Date(boundedNowMs).toISOString() }))
assert.equal(await boundedStorage.read('lru-b'), undefined, '超过 maxEntries 时必须淘汰最久未访问缓存')
assert.equal((await boundedStorage.read<number>('lru-a'))?.value, 1)
assert.equal((await boundedStorage.read<number>('lru-c'))?.value, 3)

const cacheFirstStorage = createMemoryPageDataCacheStorage()
await cacheFirstStorage.writeIfCurrent(cacheRecord(canonicalKey, ['cached'], { token: token(1), confirmedAt: '2026-07-17T11:59:00.000Z', writtenAt: '2026-07-17T11:59:00.000Z' }))
let cacheFirstNetworkLoads = 0
let cacheFirstConfirms = 0
const cacheFirstController = new PageDataCacheController<string[]>({
  cacheKey: { scope: 'self:sys-1', route: '/accounts', query: { filters: { tag: ['a'], status: 'active' }, page: 1 }, version: 3 },
  domain: 'accounts.runtime',
  viewScope: 'self',
  storage: cacheFirstStorage,
  confirm: async (request) => {
    cacheFirstConfirms += 1
    return confirmResult(request.domains['accounts.runtime'])
  },
  loadNetwork: async () => {
    cacheFirstNetworkLoads += 1
    return ['network']
  }
})
const cacheFirst = await cacheFirstController.load()
assert.equal(cacheFirst.source, 'cache')
assert.deepEqual(cacheFirst.data, ['cached'], 'cache-first 必须立即返回本地快照')
assert.equal(cacheFirstNetworkLoads, 0, 'revision 未变化时不得读取完整业务接口')
assert.equal((await cacheFirst.confirmation)?.state, 'unchanged')
assert.equal(cacheFirstConfirms, 1)

const forceRefreshStorage = createMemoryPageDataCacheStorage()
const forceRefreshKey = createPageDataCacheKey({ scope: 'self:force-refresh', route: '/accounts', query: {}, version: 1, domain: 'accounts.runtime' })
await forceRefreshStorage.writeIfCurrent(cacheRecord(forceRefreshKey, ['cached'], { token: token(1), confirmedAt: '2026-07-17T12:00:00.000Z' }))
const forceRefreshSteps: string[] = []
const forceRefreshController = new PageDataCacheController<string[]>({
  cacheKey: { scope: 'self:force-refresh', route: '/accounts', query: {}, version: 1 },
  domain: 'accounts.runtime', viewScope: 'self', storage: forceRefreshStorage,
  confirm: async (request) => {
    forceRefreshSteps.push('confirm')
    return confirmResult(request.domains['accounts.runtime'])
  },
  loadNetwork: async () => {
    forceRefreshSteps.push('network')
    return ['force-network']
  }
})
const forceRefresh = await forceRefreshController.refresh()
assert.deepEqual(forceRefreshSteps, ['network', 'confirm'], '已有有效 token 的强刷必须先取完整网络快照，再只做一次轻量确认')
assert.equal(forceRefresh.confirmed, true)
assert.deepEqual((await forceRefreshStorage.read<string[]>(forceRefreshKey))?.value, ['force-network'], 'unchanged 确认必须写入刚取得的网络快照')

const delayedWriteBaseStorage = createMemoryPageDataCacheStorage()
const delayedWriteKeyInput = { scope: 'self:delayed-write', route: '/accounts', query: {}, version: 1, domain: 'accounts.runtime' as const }
const delayedWriteKey = createPageDataCacheKey(delayedWriteKeyInput)
await delayedWriteBaseStorage.writeIfCurrent(cacheRecord(delayedWriteKey, ['cached'], { token: token(1), confirmedAt: '2026-07-17T12:00:00.000Z' }))
const delayedWriteGate = deferred<void>()
let delayedWriteEntered = false
let delayedWriteUpdates = 0
let delayedWriteNotifications = 0
const delayedWriteStorage = {
  read: delayedWriteBaseStorage.read,
  touch: delayedWriteBaseStorage.touch,
  remove: delayedWriteBaseStorage.remove,
  async writeIfCurrent<T>(record: PageDataCacheRecord<T>) {
    delayedWriteEntered = true
    await delayedWriteGate.promise
    return delayedWriteBaseStorage.writeIfCurrent(record)
  }
}
const delayedWriteController = new PageDataCacheController<string[]>({
  cacheKey: delayedWriteKeyInput,
  domain: 'accounts.runtime', viewScope: 'self', storage: delayedWriteStorage,
  tabCoordinator: {
    isLeader: () => true,
    requestConfirm: async () => false,
    notifyUpdated: () => { delayedWriteNotifications += 1 },
    notifyInvalidated: () => undefined,
    onConfirmRequested: () => () => undefined,
    onCacheUpdated: () => () => undefined,
    onCacheInvalidated: () => () => undefined
  },
  confirm: async (request) => confirmResult(request.domains['accounts.runtime']),
  loadNetwork: async () => ['delayed-network']
})
delayedWriteController.subscribe(() => { delayedWriteUpdates += 1 })
const delayedWriteRefresh = delayedWriteController.refresh()
while (!delayedWriteEntered) await microtask()
delayedWriteController.close()
delayedWriteGate.resolve()
const delayedWriteResult = await delayedWriteRefresh
assert.equal(delayedWriteResult.superseded, true, 'writeIfCurrent 等待期间关闭 controller 后必须返回 superseded')
assert.equal(delayedWriteUpdates, 0, '关闭后的迟到写入不得 publish 到本地订阅者')
assert.equal(delayedWriteNotifications, 0, '关闭后的迟到写入不得广播 cache-updated')

const sharedChainStorage = createMemoryPageDataCacheStorage()
const sharedChainKey = createPageDataCacheKey({ scope: 'self:shared-chain', route: '/accounts', query: {}, version: 1, domain: 'accounts.runtime' })
await sharedChainStorage.writeIfCurrent(cacheRecord(sharedChainKey, ['cached'], { token: token(1), confirmedAt: '2026-07-17T12:00:00.000Z' }))
const sharedChainNetwork = deferred<string[]>()
let sharedChainConfirmCalls = 0
const sharedChainController = new PageDataCacheController<string[]>({
  cacheKey: { scope: 'self:shared-chain', route: '/accounts', query: {}, version: 1 },
  domain: 'accounts.runtime', viewScope: 'self', storage: sharedChainStorage,
  confirm: async (request) => {
    sharedChainConfirmCalls += 1
    return confirmResult(request.domains['accounts.runtime'])
  },
  loadNetwork: () => sharedChainNetwork.promise
})
const sharedRefresh = sharedChainController.refresh()
await microtask()
const sharedConfirm = sharedChainController.confirmNow()
sharedChainNetwork.resolve(['shared-network'])
const [sharedRefreshResult, sharedConfirmResult] = await Promise.all([sharedRefresh, sharedConfirm])
assert.equal(sharedRefreshResult.superseded, false, '并发 confirmNow 不得把正在执行的 refresh 标记为 superseded')
assert.equal(sharedConfirmResult.state, 'updated', '并发 confirmNow 必须共享 refresh 的确认链结果')
assert.equal(sharedChainConfirmCalls, 1, 'refresh 与 confirmNow 共享确认链时不得重复确认')

const lifecycleRefreshStorage = createMemoryPageDataCacheStorage()
const lifecycleRefreshKey = createPageDataCacheKey({ scope: 'self:lifecycle-refresh', route: '/accounts', query: {}, version: 1, domain: 'accounts.runtime' })
await lifecycleRefreshStorage.writeIfCurrent(cacheRecord(lifecycleRefreshKey, ['cached'], { token: token(1), confirmedAt: '2026-07-17T12:00:00.000Z' }))
const lifecycleConfirmGate = deferred<PageDataConfirmResult>()
let lifecycleConfirmCalls = 0
let lifecycleNetworkCalls = 0
const lifecycleController = new PageDataCacheController<string[]>({
  cacheKey: { scope: 'self:lifecycle-refresh', route: '/accounts', query: {}, version: 1 },
  domain: 'accounts.runtime', viewScope: 'self', storage: lifecycleRefreshStorage,
  confirm: async (request) => {
    lifecycleConfirmCalls += 1
    return lifecycleConfirmGate.promise
  },
  loadNetwork: async () => {
    lifecycleNetworkCalls += 1
    return ['manual-refresh-network']
  }
})
const lifecycleConfirm = lifecycleController.confirmNow()
await microtask()
const lifecycleRefresh = lifecycleController.refresh()
lifecycleConfirmGate.resolve(confirmResult(token(1)))
const lifecycleResult = await lifecycleRefresh
await lifecycleConfirm
assert.equal(lifecycleConfirmCalls, 1, '生命周期确认在途时手动刷新不得再发第二次确认')
assert.equal(lifecycleNetworkCalls, 1, '生命周期确认在途时手动刷新仍应只读取一次业务列表')
assert.deepEqual(lifecycleResult.data, ['manual-refresh-network'])
assert.deepEqual((await lifecycleRefreshStorage.read<string[]>(lifecycleRefreshKey))?.value, ['manual-refresh-network'], '手动刷新拿到的新首屏不得在下一次 confirm 时被旧缓存覆盖')
lifecycleController.close()

await testMicrotaskConfirmBatching()
await testActivationManagedCacheFlow()
await testPendingConfirmFallbackMetadata()

const followerSharedStorage = createMemoryPageDataCacheStorage()
const followerSharedKey = createPageDataCacheKey({ scope: 'self:follower-shared-refresh', route: '/accounts', query: {}, version: 1, domain: 'accounts.runtime' })
await followerSharedStorage.writeIfCurrent(cacheRecord(followerSharedKey, ['cached'], { token: token(1), confirmedAt: '2026-07-17T12:00:00.000Z' }))
const followerSharedNetwork = deferred<string[]>()
let followerLeaderRequests = 0
let followerSharedConfirms = 0
const followerSharedController = new PageDataCacheController<string[]>({
  cacheKey: { scope: 'self:follower-shared-refresh', route: '/accounts', query: {}, version: 1 },
  domain: 'accounts.runtime', viewScope: 'self', storage: followerSharedStorage,
  tabCoordinator: {
    isLeader: () => false,
    requestConfirm: async () => { followerLeaderRequests += 1; return true },
    notifyUpdated: () => undefined,
    notifyInvalidated: () => undefined,
    onConfirmRequested: () => () => undefined,
    onCacheUpdated: () => () => undefined,
    onCacheInvalidated: () => () => undefined
  },
  confirm: async (request) => {
    followerSharedConfirms += 1
    return confirmResult(request.domains['accounts.runtime'])
  },
  loadNetwork: () => followerSharedNetwork.promise
})
const followerSharedRefresh = followerSharedController.refresh()
await microtask()
const followerSharedConfirm = followerSharedController.requestConfirm()
await microtask()
assert.equal(followerLeaderRequests, 0, '本地 refresh 在途时 requestConfirm 必须先共享本地确认链，不得委托 leader')
followerSharedNetwork.resolve(['follower-shared-network'])
const [followerSharedRefreshResult, followerSharedConfirmResult] = await Promise.all([followerSharedRefresh, followerSharedConfirm])
assert.equal(followerSharedRefreshResult.superseded, false)
assert.equal(followerSharedConfirmResult.state, 'updated')
assert.equal(followerSharedConfirms, 1, 'follower 本地 refresh 与 requestConfirm 不得重复 confirm')

const missStorage = createMemoryPageDataCacheStorage()
let missNetworkLoads = 0
let missConfirmCalls = 0
const missController = new PageDataCacheController<string[]>({
  cacheKey: { scope: 'self:sys-2', route: '/accounts', query: { page: 1 }, version: 1 },
  domain: 'accounts.runtime', viewScope: 'self', storage: missStorage,
  confirm: async (request) => {
    missConfirmCalls += 1
    return confirmResult(request.domains['accounts.runtime'])
  },
  loadNetwork: async () => {
    missNetworkLoads += 1
    return ['fresh']
  }
})
const miss = await missController.load()
assert.deepEqual(miss.data, ['fresh'])
assert.equal(miss.confirmed, true, '首次网络快照必须在前后 revision 稳定后才落缓存')
assert.equal(missNetworkLoads, 1)
assert.equal(missConfirmCalls, 2, '首次加载必须执行前后两次轻量确认以封闭取数竞态')
const missCached = await missStorage.read<string[]>(missController.key)
assert(missCached)
assert.equal(Date.parse(missCached.expiresAt) - Date.parse(missCached.writtenAt), 7 * 24 * 60 * 60_000, '页面缓存默认硬过期时间必须是 7 天')

const unavailableStorage = createMemoryPageDataCacheStorage()
let unavailableLoads = 0
const unavailableController = new PageDataCacheController<string[]>({
  cacheKey: { scope: 'self:sys-3', route: '/accounts', query: {}, version: 1 },
  domain: 'accounts.runtime', viewScope: 'self', storage: unavailableStorage,
  confirm: async () => { throw new Error('confirm unavailable') },
  loadNetwork: async () => { unavailableLoads += 1; return ['fallback-network'] }
})
const unavailable = await unavailableController.load()
assert.deepEqual(unavailable.data, ['fallback-network'], '无缓存且 confirm 不可用时页面必须回退网络')
assert.equal(unavailableLoads, 1)
assert.equal(unavailable.confirmed, false)

let staleNowMs = Date.parse('2026-07-17T12:00:31.000Z')
const staleStorage = createMemoryPageDataCacheStorage({ now: () => new Date(staleNowMs) })
const staleKey = createPageDataCacheKey({ scope: 'self:stale', route: '/usage-records', query: {}, version: 1, domain: 'accounts.runtime' })
await staleStorage.writeIfCurrent(cacheRecord(staleKey, ['too-old'], {
  token: token(1),
  confirmedAt: '2026-07-17T12:00:00.000Z',
  expiresAt: '2026-07-24T12:00:00.000Z'
}))
let staleNetworkLoads = 0
const staleController = new PageDataCacheController<string[]>({
  cacheKey: { scope: 'self:stale', route: '/usage-records', query: {}, version: 1 },
  domain: 'accounts.runtime', viewScope: 'self', storage: staleStorage,
  maxStaleMs: 30_000,
  now: () => new Date(staleNowMs),
  confirm: async () => { throw new Error('Redis unavailable') },
  loadNetwork: async () => {
    staleNetworkLoads += 1
    if (staleNetworkLoads > 1) throw new Error('business endpoint unavailable')
    return ['fresh-fallback']
  }
})
const staleLoad = await staleController.load()
assert.deepEqual(staleLoad.data, ['fresh-fallback'], '超过 maxStaleMs 的缓存不得先展示，confirm 不可用时必须回退业务接口')
assert.equal(staleNetworkLoads, 1)
staleNowMs += 31_000
const staleConfirm = await staleController.requestConfirm()
assert.equal(staleConfirm.state, 'unavailable')
assert.equal(staleConfirm.data, undefined, '已展示数据超过 maxStaleMs 且 confirm 不可用时必须撤下旧快照')
assert.equal(staleNetworkLoads, 2, '周期 confirm 超过 maxStaleMs 时必须先尝试业务接口刷新，再决定撤下')

const confirmedStaleStorage = createMemoryPageDataCacheStorage({ now: () => new Date(staleNowMs) })
const confirmedStaleKey = createPageDataCacheKey({ scope: 'self:confirmed-stale', route: '/providers/gpt/models', query: {}, version: 1, domain: 'providers.catalog' })
await confirmedStaleStorage.writeIfCurrent(cacheRecord(confirmedStaleKey, ['persisted'], {
  token: { ...token(7), domain: 'providers.catalog' },
  confirmedAt: new Date(staleNowMs - 300_001).toISOString(),
  expiresAt: new Date(staleNowMs + 7 * 24 * 60 * 60_000).toISOString()
}))
let confirmedStaleNetworkLoads = 0
const confirmedStaleController = new PageDataCacheController<string[]>({
  cacheKey: { scope: 'self:confirmed-stale', route: '/providers/gpt/models', query: {}, version: 1 },
  domain: 'providers.catalog',
  viewScope: 'self',
  storage: confirmedStaleStorage,
  maxStaleMs: 300_000,
  now: () => new Date(staleNowMs),
  confirm: async (request) => {
    const current = { ...token(7), domain: 'providers.catalog' as const }
    const known = request.domains['providers.catalog']
    return {
      serverTime: new Date(staleNowMs).toISOString(),
      domains: {
        'providers.catalog': {
          action: known?.sequence === current.sequence ? 'unchanged' as const : 'reload' as const,
          token: current
        }
      }
    }
  },
  loadNetwork: async () => {
    confirmedStaleNetworkLoads += 1
    return ['unnecessary-network']
  }
})
const confirmedStaleLoad = await confirmedStaleController.load()
assert.equal(confirmedStaleLoad.source, 'cache', '超过直接展示时间的持久快照经轻量 confirm unchanged 后仍应从 IndexedDB 返回')
assert.deepEqual(confirmedStaleLoad.data, ['persisted'])
assert.equal(confirmedStaleNetworkLoads, 0, 'revision unchanged 时不得因为 maxStaleMs 到期重查完整业务接口')

const raceStorage = createMemoryPageDataCacheStorage()
const firstNetwork = deferred<string[]>()
const secondNetwork = deferred<string[]>()
let raceLoadIndex = 0
const raceController = new PageDataCacheController<string[]>({
  cacheKey: { scope: 'self:race', route: '/accounts', query: {}, version: 1 },
  domain: 'accounts.runtime', viewScope: 'self', storage: raceStorage,
  confirm: async (request) => confirmResult(request.domains['accounts.runtime']),
  loadNetwork: async () => (++raceLoadIndex === 1 ? firstNetwork.promise : secondNetwork.promise)
})
const olderRefresh = raceController.refresh()
await microtask()
const newerRefresh = raceController.refresh()
await microtask()
secondNetwork.resolve(['newer'])
assert.deepEqual((await newerRefresh).data, ['newer'])
firstNetwork.resolve(['older'])
const supersededRefresh = await olderRefresh
assert.equal(supersededRefresh.superseded, true, '迟到的页面刷新结果必须显式标记为已被取代')
assert.deepEqual(supersededRefresh.data, ['newer'], '迟到刷新应返回已提交的新快照，避免调用方误应用旧数据')
assert.deepEqual((await raceStorage.read<string[]>(raceController.key))?.value, ['newer'], '迟到旧代次不得覆盖较新的缓存快照')

const dynamicStorage = createMemoryPageDataCacheStorage()
const dynamicManager = new PageDataRequestCacheManager<string[]>({
  storage: dynamicStorage,
  confirm: async (request) => confirmResult(request.domains['accounts.runtime'])
})
const queryANetwork = deferred<string[]>()
const queryBNetwork = deferred<string[]>()
const dynamicRequest = (query: string, loadNetwork: () => Promise<string[]>) => ({
  cacheKey: { scope: 'self:dynamic', route: '/accounts', query: { keyword: query }, version: 1 },
  domain: 'accounts.runtime' as const,
  viewScope: 'self' as const,
  loadNetwork
})
const dynamicUpdates: string[][] = []
dynamicManager.subscribe((record) => { if (record) dynamicUpdates.push(record.value) })
const queryALoad = dynamicManager.load(dynamicRequest('a', () => queryANetwork.promise))
await microtask()
const queryBLoad = dynamicManager.load(dynamicRequest('b', () => queryBNetwork.promise))
await microtask()
queryBNetwork.resolve(['query-b'])
assert.deepEqual((await queryBLoad).data, ['query-b'])
queryANetwork.resolve(['query-a-late'])
const staleQueryResult = await queryALoad
assert.equal(staleQueryResult.superseded, true, '切换 query 后旧 controller 的结果必须标记为已取代')
assert.deepEqual(dynamicUpdates, [['query-b']], '只有当前 query 的缓存通知可以应用到页面')
assert.equal(dynamicManager.currentKey, createPageDataCacheKey({ scope: 'self:dynamic', route: '/accounts', query: { keyword: 'b' }, version: 1, domain: 'accounts.runtime' }))

let visible = false
let scheduledConfirms = 0
let focusListener: (() => void) | undefined
const scheduler = new PageDataVisibleConfirmScheduler({
  confirm: () => { scheduledConfirms += 1 },
  isVisible: () => visible,
  addFocusListener: (listener) => { focusListener = listener; return () => { focusListener = undefined } }
})
scheduler.start()
scheduler.tick()
assert.equal(scheduledConfirms, 0, '隐藏页不得执行 30 秒确认')
visible = true
scheduler.tick()
focusListener?.()
assert.equal(scheduledConfirms, 2, '可见周期与窗口聚焦都必须触发轻量确认')
scheduler.stop()

let lifecycleStarts = 0
let lifecycleStops = 0
let lifecycleImmediateRefreshes = 0
const activationController = createPageDataActivationController({
  start: () => { lifecycleStarts += 1 },
  stop: () => { lifecycleStops += 1 },
  onActivate: () => { lifecycleImmediateRefreshes += 1 }
})
activationController.mount()
assert.deepEqual([lifecycleStarts, lifecycleImmediateRefreshes], [1, 0], '首次 mount 只启动调度，不得触发恢复专用即时确认')
activationController.activate()
activationController.activate()
assert.deepEqual([lifecycleStarts, lifecycleImmediateRefreshes], [1, 0], '首次 mount 紧邻的 activated 不得增加确认请求')
activationController.deactivate()
activationController.deactivate()
assert.equal(lifecycleStops, 1, '重复隐藏只能停止一次')
activationController.activate()
assert.deepEqual([lifecycleStarts, lifecycleImmediateRefreshes], [2, 1], '隐藏后恢复必须重新启动并立即同步')
activationController.dispose()
activationController.activate()
assert.deepEqual([lifecycleStarts, lifecycleStops, lifecycleImmediateRefreshes], [2, 2, 1], '销毁后必须停止且不可再次激活')

const channels = new FakeChannelHub()
let now = 1_000
const leader = new BrowserPageDataTabCoordinator({ tabId: 'a', channelFactory: channels.factory, now: () => now, heartbeatIntervalMs: 60_000 })
const follower = new BrowserPageDataTabCoordinator({ tabId: 'b', channelFactory: channels.factory, now: () => now, heartbeatIntervalMs: 60_000 })
assert.equal(leader.isLeader(), true)
assert.equal(follower.isLeader(), false, '多标签必须确定唯一 leader')
let requestedKey = ''
let updatedKey = ''
let invalidatedDomain = ''
leader.onConfirmRequested((key) => { requestedKey = key })
follower.onCacheUpdated((key) => { updatedKey = key })
follower.onDomainInvalidated((domain) => { invalidatedDomain = domain.domain })
assert.equal(await follower.requestConfirm('key-1'), true)
leader.notifyUpdated('key-1')
leader.notifyDomainInvalidated('providers.catalog', 'self:user-a', '/providers/gpt/models')
assert.equal(requestedKey, 'key-1', 'follower 必须把确认请求交给 leader')
assert.equal(updatedKey, 'key-1', 'leader 写入后必须通知其他标签页重读 IndexedDB')
assert.equal(invalidatedDomain, 'providers.catalog', '写入标签即使未实例化目标 key，也必须按 domain 通知其他标签清理缓存')
now += 20_000
assert.equal(follower.isLeader(), true, 'leader 心跳过期后 follower 必须接管确认')
leader.close()
follower.close()

await testFollowerTakesOverWhenLeaderDoesNotOwnKey()
await testFollowerMaxStaleFallsBackToNetwork()
await testLeaderInvalidationWithdrawsFollowerData()

const composableSource = readFileSync(fileURLToPath(new URL('../../composables/usePageDataCache.ts', import.meta.url)), 'utf8')
assert.match(composableSource, /controller\.subscribe/, 'Vue composable 必须订阅跨标签与后台更新')
assert.match(composableSource, /scheduler\.start\(\)/, 'Vue composable 必须启动 30 秒可见页与 focus 确认')
assert.match(composableSource, /forceRefresh/, 'Vue composable 必须提供强制刷新入口')
assert.match(composableSource, /onActivated[\s\S]*activationController\.activate\(\)/, 'KeepAlive 页面激活时必须恢复确认调度')
assert.match(composableSource, /onDeactivated[\s\S]*activationController\.deactivate\(\)/, 'KeepAlive 页面隐藏时必须停止确认调度')
assert.match(composableSource, /onActivate:[\s\S]{0,160}!options\.activationManaged[\s\S]{0,100}confirmCurrent\(\)\.catch/, '未受管 KeepAlive 页面恢复必须立即确认，受管页面不得启动私有确认')
assert.match(composableSource, /onMounted[\s\S]*activationController\.mount\(\)/, '首次 mount 必须只启动调度，不能触发恢复确认')
assert.match(composableSource, /onUnmounted[\s\S]*activationController\.dispose\(\)[\s\S]*controller\.close\(\)/, 'Vue composable 卸载时必须清理调度器、订阅和 controller')
assert.match(composableSource, /activationManaged\?:\s*boolean/, '静态页面缓存 composable 必须显式声明 activationManaged')
assert.match(composableSource, /if\s*\(!options\.activationManaged\)[\s\S]{0,300}new PageDataVisibleConfirmScheduler/, '受管静态页面缓存不得创建私有可见页 scheduler')
const requestComposableSource = readFileSync(fileURLToPath(new URL('../../composables/usePageDataRequestCache.ts', import.meta.url)), 'utf8')
assert.match(requestComposableSource, /resolveRequest\(\)/, '动态页面缓存每次 load 必须解析当前 scope、route、query 与 version')
assert.match(requestComposableSource, /manager\.subscribe/, '动态页面缓存必须只订阅当前 manager 的更新')
assert.match(requestComposableSource, /manager\.confirmCurrent\(\)/, '30 秒和 focus 只能确认当前 query')
assert.match(requestComposableSource, /applyConfirmOutcome/, '周期 confirm 必须把过期不可用结果同步给页面，不能继续展示旧快照')
assert.match(requestComposableSource, /getDefaultPageDataTabCoordinator\(\)/, '页面消费者未显式传入协调器时必须共享默认多标签 leader')
assert.match(requestComposableSource, /forceRefresh/, '动态页面缓存必须提供当前 query 强制刷新')
assert.match(requestComposableSource, /onActivated[\s\S]*activationController\.activate\(\)/, '动态 KeepAlive 页面激活时必须恢复确认调度')
assert.match(requestComposableSource, /onDeactivated[\s\S]*activationController\.deactivate\(\)/, '动态 KeepAlive 页面隐藏时必须停止确认调度')
assert.match(requestComposableSource, /onActivate:[\s\S]{0,160}!options\.activationManaged[\s\S]{0,100}confirmCurrent\(\)\.catch/, '未受管动态 KeepAlive 页面恢复必须立即确认，受管页面不得启动私有确认')
assert.match(requestComposableSource, /onMounted[\s\S]*activationController\.mount\(\)/, '动态页面首次 mount 必须只启动调度，不能触发恢复确认')
assert.match(requestComposableSource, /onUnmounted[\s\S]*activationController\.dispose\(\)[\s\S]*manager\.close\(\)/, '动态页面缓存卸载必须清理当前 query controller 与订阅')
assert.match(requestComposableSource, /activationManaged\?:\s*boolean/, '动态页面缓存 composable 必须显式声明 activationManaged')
assert.match(requestComposableSource, /if\s*\(!options\.activationManaged\)[\s\S]{0,300}new PageDataVisibleConfirmScheduler/, '受管动态页面缓存不得创建私有可见页 scheduler')

const pageDataApiSource = readFileSync(fileURLToPath(new URL('../../api/domains/pageData.ts', import.meta.url)), 'utf8')
assert.match(pageDataApiSource, /'accounts\.static'/, '前端数据域契约必须包含账户静态列表域')
const accountListSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountListData.ts', import.meta.url)), 'utf8')
assert.match(accountListSource, /usePageDataRequestCache/, '账户列表必须接入动态页面缓存 manager')
assert.match(accountListSource, /domain:\s*'accounts\.static'/, '账户列表完整快照必须由 accounts.static revision 确认')
assert.match(accountListSource, /pageDataApi\.confirm/, '账户列表必须调用轻量 revision confirm 接口')
assert.match(accountListSource, /maxStaleMs:\s*30_000/, '账户静态列表缓存最多允许 30 秒未确认')
assert.match(accountListSource, /confirmIntervalMs:\s*30_000/, '账户静态列表必须按 maxStaleMs 周期确认')
assert.match(accountListSource, /watch\(accountPageCache\.data/, '账户页面必须接收周期确认更新或过期撤下结果')
assert.match(accountListSource, /applyAccountPageCacheResult/, '账户缓存后台更新必须走分页列表统一应用入口')
const usageRecordsSource = readFileSync(fileURLToPath(new URL('../../views/usage-records/UsageRecordsView.vue', import.meta.url)), 'utf8')
assert.doesNotMatch(usageRecordsSource, /usePageDataRequestCache/, '使用记录列表不得接入动态页面缓存 manager')
assert.doesNotMatch(usageRecordsSource, /pageDataApi\.confirm/, '使用记录列表不得调用页面 revision confirm 接口')
assert.doesNotMatch(usageRecordsSource, /usageRecordPageCache/, '使用记录列表不得保留 IndexedDB 页面结果缓存')
assert.doesNotMatch(usageRecordsSource, /confirmIntervalMs:\s*15_000/, '使用记录列表不得启动 15 秒自动确认')
assert.match(usageRecordsSource, /return\s+await\s+usageRecordsApi\.list\(usageRecordRequestParams\(pageState\)\)/, '使用记录所有显式加载入口必须直接请求 scoped 列表 API')
const pageDataCacheSource = readFileSync(fileURLToPath(new URL('../../shared/pageDataCache.ts', import.meta.url)), 'utf8')
assert.doesNotMatch(pageDataCacheSource, /if \(!primaryAvailable\) return fallbackCall\(\)/, 'IndexedDB 一次失败后不得永久降级到内存，后续操作必须重试持久存储')
assert.match(pageDataCacheSource, /removeBoth/, '删除和按域失效必须同时尝试 IndexedDB 与内存，避免任一层旧值复活')
const indexedDbStorageStart = pageDataCacheSource.indexOf('function createIndexedDbPageDataCacheStorage')
assert.notEqual(indexedDbStorageStart, -1, '必须保留 IndexedDB 页面缓存实现')
const indexedDbStorageSource = pageDataCacheSource.slice(indexedDbStorageStart)
const indexedDbWriteStart = indexedDbStorageSource.indexOf('async writeIfCurrent<T>')
const indexedDbTouchStart = indexedDbStorageSource.indexOf('async touch(', indexedDbWriteStart)
const indexedDbRemoveStart = indexedDbStorageSource.indexOf('async remove(', indexedDbTouchStart)
assert.notEqual(indexedDbWriteStart, -1, 'IndexedDB storage 必须实现 writeIfCurrent')
assert.notEqual(indexedDbTouchStart, -1, 'IndexedDB storage 必须实现 touch')
assert.notEqual(indexedDbRemoveStart, -1, 'IndexedDB storage 必须实现 remove')
const indexedDbWriteSource = indexedDbStorageSource.slice(indexedDbWriteStart, indexedDbTouchStart)
const indexedDbTouchSource = indexedDbStorageSource.slice(indexedDbTouchStart, indexedDbRemoveStart)
assertSourceOrder(indexedDbWriteSource, [
  'commitGuard && !commitGuard()',
  'const putRequest = store.put(record)',
  'putRequest.onsuccess = () => {',
  'commitGuard && !commitGuard()',
  'written = false',
  'transaction.abort()',
  'written = true'
], 'writeIfCurrent 必须在 put 完成边界再次检查 guard，并回滚失效写入')
assertSourceOrder(indexedDbTouchSource, [
  'commitGuard && !commitGuard()',
  'const putRequest = store.put(',
  'putRequest.onsuccess = () => {',
  'commitGuard && !commitGuard()',
  'touched = false',
  'transaction.abort()',
  'touched = true'
], 'touch 必须在 put 完成边界再次检查 guard，并回滚失效更新')
assert.match(indexedDbWriteSource, /transaction\.onabort\s*=\s*\(\)\s*=>\s*\{[\s\S]*guardAborted[\s\S]*resolve\(false\)[\s\S]*reject\(/, 'writeIfCurrent 的 guard abort 必须正常返回 false，真实 abort 仍 reject')
assert.match(indexedDbTouchSource, /transaction\.onabort\s*=\s*\(\)\s*=>\s*\{[\s\S]*guardAborted[\s\S]*resolve\(false\)[\s\S]*reject\(/, 'touch 的 guard abort 必须正常返回 false，真实 abort 仍 reject')
assert.match(indexedDbWriteSource, /transaction\.onerror\s*=\s*\(\)\s*=>\s*\{[\s\S]*reject\(/, 'writeIfCurrent 的真实事务错误必须 reject')
assert.match(indexedDbTouchSource, /transaction\.onerror\s*=\s*\(\)\s*=>\s*\{[\s\S]*reject\(/, 'touch 的真实事务错误必须 reject')
assert.match(indexedDbWriteSource, /transaction\.oncomplete[\s\S]*if \(written\)[\s\S]*pruneIndexedDbRecords/, 'writeIfCurrent 只能在事务真实提交后按 written 结果触发 prune')
const resilientStorageStart = pageDataCacheSource.indexOf('function createResilientStorage')
assert.notEqual(resilientStorageStart, -1, '必须保留 IndexedDB resilient wrapper')
const resilientStorageSource = pageDataCacheSource.slice(resilientStorageStart)
assert.match(resilientStorageSource, /primary\.writeIfCurrent\(record, commitGuard\)[\s\S]*fallback\.writeIfCurrent\(record, commitGuard\)/, 'resilient writeIfCurrent 必须向 primary 与 fallback 透传同一 guard')
assert.match(resilientStorageSource, /primary\.touch\(key, token, confirmedAt, commitGuard\)[\s\S]*fallback\.touch\(key, token, confirmedAt, commitGuard\)/, 'resilient touch 必须向 primary 与 fallback 透传同一 guard')

cacheFirstController.close()
forceRefreshController.close()
delayedWriteController.close()
sharedChainController.close()
followerSharedController.close()
missController.close()
unavailableController.close()
staleController.close()
confirmedStaleController.close()
raceController.close()
dynamicManager.close()
console.log('通用页面 IndexedDB cache-first、revision 与多标签协调回归通过')

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void; reject: (reason?: unknown) => void } {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  return {
    promise: new Promise<T>((next, fail) => {
      resolve = next
      reject = fail
    }),
    resolve,
    reject
  }
}

function assertSourceOrder(source: string, markers: string[], message: string): void {
  let cursor = -1
  for (const marker of markers) {
    cursor = source.indexOf(marker, cursor + 1)
    assert.notEqual(cursor, -1, `${message}: 缺少或顺序错误 ${marker}`)
  }
}

async function microtask(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

async function testPendingConfirmFallbackMetadata(): Promise<void> {
  const now = () => new Date('2026-07-17T12:00:31.000Z')
  const summaries: unknown[] = []

  const unmanagedStorage = createMemoryPageDataCacheStorage({ now })
  const unmanagedConfirm = deferred<PageDataConfirmResult>()
  const unmanagedNetwork = deferred<string[]>()
  let unmanagedNetworkLoads = 0
  const unmanagedController = new PageDataCacheController<string[]>({
    cacheKey: { scope: 'self:pending-fallback-unmanaged', route: '/accounts', query: {}, version: 1 },
    domain: 'accounts.runtime',
    viewScope: 'self',
    storage: unmanagedStorage,
    maxStaleMs: 30_000,
    now,
    confirm: () => unmanagedConfirm.promise,
    loadNetwork: () => {
      unmanagedNetworkLoads += 1
      return unmanagedNetwork.promise
    }
  })
  await unmanagedStorage.writeIfCurrent(cacheRecord(unmanagedController.key, ['unmanaged-stale'], {
    token: token(1),
    confirmedAt: '2026-07-17T12:00:00.000Z'
  }))
  const unmanagedPendingConfirm = unmanagedController.requestConfirm()
  await microtask()
  const unmanagedRefresh = unmanagedController.refresh()
  unmanagedConfirm.reject(new Error('unmanaged confirm unavailable'))
  await microtask()
  unmanagedNetwork.resolve(['unmanaged-fallback'])
  const [unmanagedOutcome, unmanagedResult] = await Promise.all([unmanagedPendingConfirm, unmanagedRefresh])
  summaries.push({
    mode: 'unmanaged',
    networkLoads: unmanagedNetworkLoads,
    outcome: unmanagedOutcome,
    refresh: unmanagedResult
  })
  unmanagedController.close()

  const managedStorage = createMemoryPageDataCacheStorage({ now })
  const managedPre = deferred<PageDataActivationDecision>()
  let managedNetworkLoads = 0
  const managedController = new PageDataCacheController<string[]>({
    cacheKey: { scope: 'self:pending-fallback-managed', route: '/accounts', query: {}, version: 1 },
    domain: 'accounts.runtime',
    viewScope: 'self',
    storage: managedStorage,
    maxStaleMs: 30_000,
    now,
    activation: activationHandle({ register: () => managedPre.promise }),
    writeEpoch: () => 0,
    confirm: async () => { throw new Error('managed request must not use legacy confirm') },
    loadNetwork: async () => {
      managedNetworkLoads += 1
      return ['managed-fallback']
    }
  })
  await managedStorage.writeIfCurrent(cacheRecord(managedController.key, ['managed-stale'], {
    token: token(1),
    confirmedAt: '2026-07-17T12:00:00.000Z'
  }))
  const managedPendingConfirm = managedController.requestConfirm()
  await microtask()
  const managedRefresh = managedController.refresh()
  managedPre.resolve(activationFailure('unavailable', 'pre', {
    resourceKey: managedController.key,
    domain: 'accounts.runtime',
    token: token(1),
    generation: 1,
    writeEpoch: 0
  }))
  const [managedOutcome, managedResult] = await Promise.all([managedPendingConfirm, managedRefresh])
  summaries.push({
    mode: 'managed',
    networkLoads: managedNetworkLoads,
    outcome: managedOutcome,
    refresh: managedResult
  })
  managedController.close()

  assert.deepEqual(summaries, [
    {
      mode: 'unmanaged',
      networkLoads: 1,
      outcome: {
        state: 'updated',
        data: ['unmanaged-fallback'],
        source: 'network',
        confirmed: false,
        cached: true
      },
      refresh: {
        source: 'network',
        data: ['unmanaged-fallback'],
        confirmed: false,
        cached: true,
        superseded: false
      }
    },
    {
      mode: 'managed',
      networkLoads: 1,
      outcome: {
        state: 'updated',
        data: ['managed-fallback'],
        source: 'network',
        confirmed: false,
        cached: false
      },
      refresh: {
        source: 'network',
        data: ['managed-fallback'],
        confirmed: false,
        cached: false,
        superseded: false
      }
    }
  ], 'confirm 失败后的 pending refresh 必须复用唯一一次 fallback GET，并保留真实确认与缓存元数据')
}

async function testActivationManagedCacheFlow(): Promise<void> {
  const keyInput = {
    scope: 'self:managed',
    route: '/accounts',
    query: {},
    version: 1,
    domain: 'accounts.runtime' as const
  }

  const hotStorage = createMemoryPageDataCacheStorage()
  const hotKey = createPageDataCacheKey(keyInput)
  await hotStorage.writeIfCurrent(cacheRecord(hotKey, ['hot'], {
    token: token(1),
    confirmedAt: '2026-07-17T12:00:00.000Z'
  }))
  let hotRegisters = 0
  let hotLegacyConfirms = 0
  const hotController = new PageDataCacheController<string[]>({
    cacheKey: keyInput,
    domain: 'accounts.runtime',
    viewScope: 'self',
    storage: hotStorage,
    maxStaleMs: 300_000,
    now: () => new Date('2026-07-17T12:00:10.000Z'),
    activation: activationHandle({
      register: (participant) => {
        hotRegisters += 1
        return confirmedActivation('pre', participant, 'unchanged', token(1))
      }
    }),
    writeEpoch: () => 0,
    confirm: async (request) => {
      hotLegacyConfirms += 1
      return confirmResult(request.domains['accounts.runtime'])
    },
    loadNetwork: async () => ['network']
  })
  const hot = await hotController.load()
  await microtask()
  assert.equal(hot.source, 'cache')
  assert.equal(hot.confirmation, undefined, '受管模式 30 秒内热缓存必须立即返回且不启动后台确认')
  assert.deepEqual([hotRegisters, hotLegacyConfirms], [0, 0], '受管热缓存不得调用 activation.register 或私有 confirm')

  const unmanagedStorage = createMemoryPageDataCacheStorage()
  let unmanagedConfirms = 0
  const unmanagedController = new PageDataCacheController<string[]>({
    cacheKey: { ...keyInput, scope: 'self:unmanaged' },
    domain: 'accounts.runtime',
    viewScope: 'self',
    storage: unmanagedStorage,
    confirm: async (request) => {
      unmanagedConfirms += 1
      return confirmResult(request.domains['accounts.runtime'])
    },
    loadNetwork: async () => ['network']
  })
  await unmanagedStorage.writeIfCurrent(cacheRecord(unmanagedController.key, ['hot'], {
    token: token(1),
    confirmedAt: '2026-07-17T12:00:00.000Z'
  }))
  const unmanaged = await unmanagedController.load()
  assert.equal((await unmanaged.confirmation)?.state, 'unchanged', '未受管模式必须保留现有后台 confirm')
  assert.equal(unmanagedConfirms, 1)

  let currentWriteEpoch = 4
  let managedLegacyConfirms = 0
  const managedStorage = createMemoryPageDataCacheStorage()
  await managedStorage.writeIfCurrent(cacheRecord(hotKey, ['stale'], {
    token: token(1),
    confirmedAt: '2026-07-17T12:00:00.000Z'
  }))
  const preParticipants: PageDataActivationParticipant[] = []
  const postParticipants: PageDataActivationParticipant[] = []
  const managedController = new PageDataCacheController<string[]>({
    cacheKey: keyInput,
    domain: 'accounts.runtime',
    viewScope: 'self',
    storage: managedStorage,
    maxStaleMs: 300_000,
    now: () => new Date('2026-07-17T12:00:31.000Z'),
    activation: activationHandle({
      register: (participant) => {
        preParticipants.push(participant)
        return confirmedActivation('pre', participant, 'reload', token(2))
      },
      stabilize: (participant) => {
        postParticipants.push(participant)
        return confirmedActivation('post', participant, 'unchanged', token(2))
      }
    }),
    writeEpoch: () => currentWriteEpoch,
    confirm: async (request) => {
      managedLegacyConfirms += 1
      return confirmResult(request.domains['accounts.runtime'], token(2))
    },
    loadNetwork: async () => ['managed-network']
  })
  const managed = await managedController.load()
  assert.deepEqual([managed.confirmed, managed.cached, managed.data], [true, true, ['managed-network']])
  assert.equal(managedLegacyConfirms, 0, '受管模式不得调用私有 requestBatchedConfirm')
  assert.equal(preParticipants.length, 1, '受管缓存超过固定 30 秒后必须参加 aggregate pre-confirm，不能被更宽的 domain maxStaleMs 跳过')
  assert.equal(postParticipants.length, 1, 'changed GET 完成后必须参加一次 aggregate post barrier')
  assert.equal(preParticipants[0]?.token?.sequence, 1)
  assert.equal(preParticipants[0]?.writeEpoch, 4)
  assert.equal(postParticipants[0]?.writeEpoch, 4)
  assert.equal((await managedStorage.read<string[]>(hotKey))?.token?.sequence, 2)

  for (const postOutcome of ['stable', 'token-drift', 'late', 'unavailable'] as const) {
    const storage = createMemoryPageDataCacheStorage()
    const controller = new PageDataCacheController<string[]>({
      cacheKey: { ...keyInput, scope: `self:pending-confirm-${postOutcome}` },
      domain: 'accounts.runtime',
      viewScope: 'self',
      storage,
      activation: activationHandle({
        register: (participant) => confirmedActivation('pre', participant, 'unchanged', token(7)),
        stabilize: (participant) => {
          postCalls += 1
          return postOutcome === 'stable'
            ? confirmedActivation('post', participant, 'unchanged', token(7))
            : postOutcome === 'token-drift'
              ? confirmedActivation('post', participant, 'unchanged', token(8))
              : activationFailure(postOutcome, 'post', participant)
        }
      }),
      writeEpoch: () => 0,
      confirm: async (request) => {
        legacyConfirms += 1
        return confirmResult(request.domains['accounts.runtime'], token(7))
      },
      loadNetwork: async () => {
        networkLoads += 1
        return [`pending-confirm-${postOutcome}`]
      }
    })
    await storage.writeIfCurrent(cacheRecord(controller.key, ['pending-confirm-before'], {
      token: token(7),
      confirmedAt: '2026-07-17T12:00:00.000Z'
    }))
    let postCalls = 0
    let legacyConfirms = 0
    let networkLoads = 0
    const pendingConfirm = controller.requestConfirm()
    const refresh = controller.refresh()
    assert.equal((await pendingConfirm).state, 'unchanged')
    const result = await refresh
    const record = await storage.read<string[]>(controller.key)
    assert.equal(postCalls, 1, `${postOutcome} 时 pending confirm 后的 GET 必须参加 post barrier`)
    assert.equal(networkLoads, 1, `${postOutcome} 时 force refresh 只能 GET 一次`)
    assert.equal(legacyConfirms, 0, `${postOutcome} 时 managed force refresh 不得调用私有 confirm`)
    if (postOutcome === 'stable') {
      assert.deepEqual([result.confirmed, result.cached], [true, true], '稳定 unchanged 同 token 才能写入 force refresh 结果')
      assert.deepEqual(record?.value, ['pending-confirm-stable'])
    } else {
      assert.deepEqual([result.confirmed, result.cached], [false, false], `${postOutcome} 时不得确认或缓存 force refresh 结果`)
      assert.deepEqual(record?.value, ['pending-confirm-before'], `${postOutcome} 时不得覆盖 pending confirm 前的缓存`)
    }
    controller.close()
  }

  const pendingBaselineStorage = createMemoryPageDataCacheStorage()
  const pendingBaselineNetworkStarted = deferred<void>()
  const pendingBaselineNetwork = deferred<string[]>()
  let pendingBaselinePostToken: PageDataRevisionToken | undefined
  let pendingBaselineCacheUpdated: ((key: string) => void) | undefined
  const pendingBaselineController = new PageDataCacheController<string[]>({
    cacheKey: { ...keyInput, scope: 'self:pending-confirm-frozen-baseline' },
    domain: 'accounts.runtime', viewScope: 'self', storage: pendingBaselineStorage,
    activation: activationHandle({
      register: (participant) => confirmedActivation('pre', participant, 'unchanged', token(1)),
      stabilize: (participant) => {
        pendingBaselinePostToken = participant.baseline
        return confirmedActivation('post', participant, 'unchanged', participant.baseline)
      }
    }),
    writeEpoch: () => 0,
    tabCoordinator: {
      isLeader: () => true,
      requestConfirm: async () => false,
      notifyUpdated: () => undefined,
      notifyInvalidated: () => undefined,
      notifyDomainInvalidated: () => undefined,
      onConfirmRequested: () => () => undefined,
      onCacheUpdated: (listener) => {
        pendingBaselineCacheUpdated = listener
        return () => { pendingBaselineCacheUpdated = undefined }
      },
      onCacheInvalidated: () => () => undefined,
      onDomainInvalidated: () => () => undefined
    },
    confirm: async (request) => confirmResult(request.domains['accounts.runtime'], token(1)),
    loadNetwork: async () => {
      pendingBaselineNetworkStarted.resolve()
      return pendingBaselineNetwork.promise
    }
  })
  await pendingBaselineStorage.writeIfCurrent(cacheRecord(pendingBaselineController.key, ['token-1-value'], {
    token: token(1)
  }))
  const pendingBaselineConfirm = pendingBaselineController.requestConfirm()
  const pendingBaselineRefresh = pendingBaselineController.refresh()
  assert.equal((await pendingBaselineConfirm).state, 'unchanged')
  await pendingBaselineNetworkStarted.promise
  await pendingBaselineStorage.writeIfCurrent(cacheRecord(pendingBaselineController.key, ['token-2-newer-value'], {
    token: token(2),
    confirmedAt: '2026-07-17T12:00:32.000Z',
    writtenAt: '2026-07-17T12:00:32.000Z'
  }))
  pendingBaselineCacheUpdated?.(pendingBaselineController.key)
  await microtask()
  pendingBaselineNetwork.resolve(['token-1-old-get'])
  const pendingBaselineResult = await pendingBaselineRefresh
  const pendingBaselineRecord = await pendingBaselineStorage.read<string[]>(pendingBaselineController.key)
  assert.equal(pendingBaselinePostToken?.sequence, 1, 'pending confirm 后的 GET 必须冻结 pre-confirm 已确认的 token1 baseline')
  assert.equal(pendingBaselineResult.superseded, true, '共享缓存推进到 token2 后旧 GET 必须返回 superseded')
  assert.equal(pendingBaselineResult.confirmed, false, '旧 GET 不得借用 token2 标记 confirmed')
  assert.deepEqual(pendingBaselineResult.data, ['token-2-newer-value'], 'superseded 结果应返回共享缓存中的新快照')
  assert.equal(pendingBaselineRecord?.token?.sequence, 2, '旧 GET 不得回退共享缓存 token')
  assert.deepEqual(pendingBaselineRecord?.value, ['token-2-newer-value'], '旧 GET 不得覆盖其他标签页的新快照')
  pendingBaselineController.close()

  const pendingGenerationStorage = createMemoryPageDataCacheStorage()
  const pendingGenerationPre = deferred<PageDataActivationDecision>()
  const pendingGenerationFirstNetwork = deferred<string[]>()
  const pendingGenerationSecondNetwork = deferred<string[]>()
  let pendingGenerationNetworkIndex = 0
  let pendingGenerationPostCalls = 0
  const pendingGenerationController = new PageDataCacheController<string[]>({
    cacheKey: { ...keyInput, scope: 'self:pending-confirm-generation' },
    domain: 'accounts.runtime', viewScope: 'self', storage: pendingGenerationStorage,
    activation: activationHandle({
      register: () => pendingGenerationPre.promise,
      stabilize: (participant) => {
        pendingGenerationPostCalls += 1
        return confirmedActivation('post', participant, 'unchanged', token(9))
      }
    }),
    writeEpoch: () => 0,
    confirm: async (request) => confirmResult(request.domains['accounts.runtime'], token(9)),
    loadNetwork: () => (++pendingGenerationNetworkIndex === 1
      ? pendingGenerationFirstNetwork.promise
      : pendingGenerationSecondNetwork.promise)
  })
  await pendingGenerationStorage.writeIfCurrent(cacheRecord(pendingGenerationController.key, ['pending-generation-before'], {
    token: token(9)
  }))
  const pendingGenerationConfirm = pendingGenerationController.requestConfirm()
  const pendingGenerationOlder = pendingGenerationController.refresh()
  pendingGenerationPre.resolve(confirmedActivation('pre', {
    resourceKey: pendingGenerationController.key,
    domain: 'accounts.runtime', token: token(9), generation: 1, writeEpoch: 0
  }, 'unchanged', token(9)))
  await pendingGenerationConfirm
  for (let attempt = 0; attempt < 10 && pendingGenerationNetworkIndex < 1; attempt += 1) await microtask()
  const pendingGenerationNewer = pendingGenerationController.refresh()
  for (let attempt = 0; attempt < 10 && pendingGenerationNetworkIndex < 2; attempt += 1) await microtask()
  pendingGenerationSecondNetwork.resolve(['pending-generation-newer'])
  assert.equal((await pendingGenerationNewer).confirmed, true)
  pendingGenerationFirstNetwork.resolve(['pending-generation-older'])
  assert.equal((await pendingGenerationOlder).superseded, true, 'pending confirm 后 generation 变化必须 supersede 旧 GET')
  assert.equal(pendingGenerationPostCalls, 1, 'pending confirm 后旧 generation 不得参加 post barrier')
  assert.deepEqual((await pendingGenerationStorage.read<string[]>(pendingGenerationController.key))?.value, ['pending-generation-newer'])

  let pendingEpoch = 60
  const pendingEpochStorage = createMemoryPageDataCacheStorage()
  const pendingEpochPre = deferred<PageDataActivationDecision>()
  const pendingEpochPost = deferred<PageDataActivationDecision>()
  let pendingEpochPostParticipant: (PageDataActivationParticipant & { baseline: PageDataRevisionToken }) | undefined
  const pendingEpochController = new PageDataCacheController<string[]>({
    cacheKey: { ...keyInput, scope: 'self:pending-confirm-epoch' },
    domain: 'accounts.runtime', viewScope: 'self', storage: pendingEpochStorage,
    activation: activationHandle({
      register: () => pendingEpochPre.promise,
      stabilize: (participant) => {
        pendingEpochPostParticipant = participant
        return pendingEpochPost.promise
      }
    }),
    writeEpoch: () => pendingEpoch,
    confirm: async (request) => confirmResult(request.domains['accounts.runtime'], token(10)),
    loadNetwork: async () => ['pending-epoch-after']
  })
  await pendingEpochStorage.writeIfCurrent(cacheRecord(pendingEpochController.key, ['pending-epoch-before'], {
    token: token(10)
  }))
  const pendingEpochConfirm = pendingEpochController.requestConfirm()
  const pendingEpochRefresh = pendingEpochController.refresh()
  pendingEpochPre.resolve(confirmedActivation('pre', {
    resourceKey: pendingEpochController.key,
    domain: 'accounts.runtime', token: token(10), generation: 1, writeEpoch: 60
  }, 'unchanged', token(10)))
  await pendingEpochConfirm
  for (let attempt = 0; attempt < 10 && !pendingEpochPostParticipant; attempt += 1) await microtask()
  assert(pendingEpochPostParticipant, 'pending confirm 后 GET 必须进入 post barrier')
  pendingEpoch += 1
  pendingEpochPost.resolve(confirmedActivation('post', pendingEpochPostParticipant, 'unchanged', token(10)))
  assert.equal((await pendingEpochRefresh).superseded, true, 'pending confirm 的 post barrier 在途期间 writeEpoch 变化必须 supersede 结果')
  assert.deepEqual((await pendingEpochStorage.read<string[]>(pendingEpochController.key))?.value, ['pending-epoch-before'])

  for (const failure of ['token-drift', 'late', 'unavailable'] as const) {
    const storage = createMemoryPageDataCacheStorage()
    const controller = new PageDataCacheController<string[]>({
      cacheKey: { ...keyInput, scope: `self:${failure}` },
      domain: 'accounts.runtime',
      viewScope: 'self',
      storage,
      activation: activationHandle({
        register: (participant) => confirmedActivation('pre', participant, 'reload', token(5)),
        stabilize: (participant) => failure === 'token-drift'
          ? confirmedActivation('post', participant, 'unchanged', token(6))
          : activationFailure(failure === 'late' ? 'late' : 'unavailable', 'post', participant)
      }),
      writeEpoch: () => 0,
      confirm: async (request) => confirmResult(request.domains['accounts.runtime'], token(5)),
      loadNetwork: async () => [`visible-${failure}`]
    })
    const result = await controller.load()
    assert.deepEqual(result.data, [`visible-${failure}`], `${failure} 时网络结果仍可展示`)
    assert.equal(result.confirmed, false, `${failure} 时不得写 confirmed`)
    assert.equal(result.cached, false, `${failure} 时不得声称结果已缓存稳定`)
    assert.equal(await storage.read(controller.key), undefined, `${failure} 时不得写入稳定缓存`)
    controller.close()
  }

  const failedPreStorage = createMemoryPageDataCacheStorage()
  const failedPreController = new PageDataCacheController<string[]>({
    cacheKey: { ...keyInput, scope: 'self:pre-unavailable' },
    domain: 'accounts.runtime',
    viewScope: 'self',
    storage: failedPreStorage,
    activation: activationHandle({
      register: (participant) => activationFailure('unavailable', 'pre', participant)
    }),
    writeEpoch: () => 0,
    confirm: async (request) => confirmResult(request.domains['accounts.runtime']),
    loadNetwork: async () => ['pre-unavailable']
  })
  const failedPre = await failedPreController.load()
  assert.equal(failedPre.confirmed, false)
  assert.equal(failedPre.cached, false, '受管 pre-confirm 失败时结果只能展示，不能写入未确认缓存')
  assert.equal(await failedPreStorage.read(failedPreController.key), undefined)

  let epoch = 10
  const epochStorage = createMemoryPageDataCacheStorage()
  const epochKey = createPageDataCacheKey({ ...keyInput, scope: 'self:epoch-touch' })
  await epochStorage.writeIfCurrent(cacheRecord(epochKey, ['before-mutation'], {
    token: token(1),
    confirmedAt: '2026-07-17T12:00:00.000Z'
  }))
  const epochPre = deferred<PageDataActivationDecision>()
  let epochNetworkLoads = 0
  const epochController = new PageDataCacheController<string[]>({
    cacheKey: { ...keyInput, scope: 'self:epoch-touch' },
    domain: 'accounts.runtime',
    viewScope: 'self',
    storage: epochStorage,
    maxStaleMs: 30_000,
    now: () => new Date('2026-07-17T12:00:31.000Z'),
    activation: activationHandle({ register: () => epochPre.promise }),
    writeEpoch: () => epoch,
    confirm: async (request) => confirmResult(request.domains['accounts.runtime']),
    loadNetwork: async () => { epochNetworkLoads += 1; return ['after-mutation'] }
  })
  const epochLoad = epochController.load()
  await microtask()
  epoch += 1
  epochPre.resolve(confirmedActivation('pre', {
    resourceKey: epochController.key,
    domain: 'accounts.runtime',
    token: token(1),
    generation: 1,
    writeEpoch: 10
  }, 'unchanged', token(1)))
  const epochResult = await epochLoad
  assert.equal(epochResult.superseded, true, 'writeEpoch 变化必须 supersede 在途 pre-confirm')
  assert.equal(epochNetworkLoads, 0, '过时 pre-confirm 不得继续 GET')
  assert.equal((await epochStorage.read<string[]>(epochKey))?.confirmedAt, '2026-07-17T12:00:00.000Z', 'writeEpoch 变化后不得 touch')

  let touchCommitEpoch = 40
  const touchCommitBaseStorage = createMemoryPageDataCacheStorage()
  const touchCommitKey = createPageDataCacheKey({ ...keyInput, scope: 'self:touch-commit-guard' })
  await touchCommitBaseStorage.writeIfCurrent(cacheRecord(touchCommitKey, ['touch-before'], {
    token: token(1),
    confirmedAt: '2026-07-17T12:00:00.000Z'
  }))
  const touchCommitEntered = deferred<void>()
  const touchCommitGate = deferred<void>()
  const touchCommitStorage: PageDataCacheStorage = {
    read: touchCommitBaseStorage.read,
    writeIfCurrent: touchCommitBaseStorage.writeIfCurrent,
    async touch(key, currentToken, confirmedAt, commitGuard?: () => boolean) {
      touchCommitEntered.resolve()
      await touchCommitGate.promise
      return touchCommitBaseStorage.touch(key, currentToken, confirmedAt, commitGuard)
    },
    remove: touchCommitBaseStorage.remove,
    removeDomain: touchCommitBaseStorage.removeDomain
  }
  const touchCommitController = new PageDataCacheController<string[]>({
    cacheKey: { ...keyInput, scope: 'self:touch-commit-guard' },
    domain: 'accounts.runtime', viewScope: 'self', storage: touchCommitStorage,
    now: () => new Date('2026-07-17T12:00:31.000Z'),
    activation: activationHandle({
      register: (participant) => confirmedActivation('pre', participant, 'unchanged', token(1))
    }),
    writeEpoch: () => touchCommitEpoch,
    confirm: async (request) => confirmResult(request.domains['accounts.runtime']),
    loadNetwork: async () => ['touch-after']
  })
  const touchCommitLoad = touchCommitController.load()
  await touchCommitEntered.promise
  touchCommitEpoch += 1
  touchCommitGate.resolve()
  const touchCommitResult = await touchCommitLoad
  const touchCommitRecord = await touchCommitBaseStorage.read<string[]>(touchCommitKey)
  assert.equal(touchCommitResult.superseded, true, 'touch 实际提交前 writeEpoch 变化必须返回 superseded')
  assert.deepEqual(touchCommitRecord?.value, ['touch-before'], '失效 touch commit guard 不得改 value')
  assert.equal(touchCommitRecord?.token?.sequence, 1, '失效 touch commit guard 不得改 token')
  assert.equal(touchCommitRecord?.confirmedAt, '2026-07-17T12:00:00.000Z', '失效 touch commit guard 不得改 confirmedAt')

  let writeCommitEpoch = 50
  const writeCommitBaseStorage = createMemoryPageDataCacheStorage()
  const writeCommitKey = createPageDataCacheKey({ ...keyInput, scope: 'self:write-commit-guard' })
  await writeCommitBaseStorage.writeIfCurrent(cacheRecord(writeCommitKey, ['write-before'], {
    token: token(1),
    confirmedAt: '2026-07-17T12:00:00.000Z'
  }))
  const writeCommitEntered = deferred<void>()
  const writeCommitGate = deferred<void>()
  const writeCommitStorage: PageDataCacheStorage = {
    read: writeCommitBaseStorage.read,
    async writeIfCurrent<T>(record: PageDataCacheRecord<T>, commitGuard?: () => boolean) {
      writeCommitEntered.resolve()
      await writeCommitGate.promise
      return writeCommitBaseStorage.writeIfCurrent(record, commitGuard)
    },
    touch: writeCommitBaseStorage.touch,
    remove: writeCommitBaseStorage.remove,
    removeDomain: writeCommitBaseStorage.removeDomain
  }
  const writeCommitController = new PageDataCacheController<string[]>({
    cacheKey: { ...keyInput, scope: 'self:write-commit-guard' },
    domain: 'accounts.runtime', viewScope: 'self', storage: writeCommitStorage,
    now: () => new Date('2026-07-17T12:00:31.000Z'),
    activation: activationHandle({
      register: (participant) => confirmedActivation('pre', participant, 'reload', token(2)),
      stabilize: (participant) => confirmedActivation('post', participant, 'unchanged', token(2))
    }),
    writeEpoch: () => writeCommitEpoch,
    confirm: async (request) => confirmResult(request.domains['accounts.runtime'], token(2)),
    loadNetwork: async () => ['write-after']
  })
  const writeCommitLoad = writeCommitController.load()
  await writeCommitEntered.promise
  writeCommitEpoch += 1
  writeCommitGate.resolve()
  const writeCommitResult = await writeCommitLoad
  const writeCommitRecord = await writeCommitBaseStorage.read<string[]>(writeCommitKey)
  assert.equal(writeCommitResult.superseded, true, 'writeIfCurrent 实际提交前 writeEpoch 变化必须返回 superseded')
  assert.deepEqual(writeCommitRecord?.value, ['write-before'], '失效 write commit guard 不得改 value')
  assert.equal(writeCommitRecord?.token?.sequence, 1, '失效 write commit guard 不得改 token')
  assert.equal(writeCommitRecord?.confirmedAt, '2026-07-17T12:00:00.000Z', '失效 write commit guard 不得改 confirmedAt')

  let deltaEpoch = 20
  let deltaApplies = 0
  const deltaStorage = createMemoryPageDataCacheStorage()
  const deltaKey = createPageDataCacheKey({ ...keyInput, scope: 'self:epoch-delta' })
  await deltaStorage.writeIfCurrent(cacheRecord(deltaKey, ['delta-base'], { token: token(1) }))
  const deltaPre = deferred<PageDataActivationDecision>()
  const deltaController = new PageDataCacheController<string[]>({
    cacheKey: { ...keyInput, scope: 'self:epoch-delta' },
    domain: 'accounts.runtime', viewScope: 'self', storage: deltaStorage,
    activation: activationHandle({ register: () => deltaPre.promise }),
    writeEpoch: () => deltaEpoch,
    confirm: async (request) => confirmResult(request.domains['accounts.runtime']),
    applyDelta: (data) => { deltaApplies += 1; return [...data, 'delta'] },
    loadNetwork: async () => ['network']
  })
  const deltaConfirm = deltaController.confirmNow()
  await microtask()
  deltaEpoch += 1
  deltaPre.resolve(confirmedActivation('pre', {
    resourceKey: deltaController.key,
    domain: 'accounts.runtime', token: token(1), generation: 1, writeEpoch: 20
  }, 'delta', token(2)))
  assert.equal((await deltaConfirm).state, 'superseded')
  assert.equal(deltaApplies, 0, 'writeEpoch 变化后不得 apply delta')
  assert.deepEqual((await deltaStorage.read<string[]>(deltaKey))?.value, ['delta-base'])

  let postEpoch = 30
  let postParticipant: (PageDataActivationParticipant & { baseline: PageDataRevisionToken }) | undefined
  const postGate = deferred<PageDataActivationDecision>()
  const postStorage = createMemoryPageDataCacheStorage()
  const postEpochController = new PageDataCacheController<string[]>({
    cacheKey: { ...keyInput, scope: 'self:post-epoch' },
    domain: 'accounts.runtime', viewScope: 'self', storage: postStorage,
    activation: activationHandle({
      register: (participant) => confirmedActivation('pre', participant, 'reload', token(4)),
      stabilize: (participant) => {
        postParticipant = participant
        return postGate.promise
      }
    }),
    writeEpoch: () => postEpoch,
    confirm: async (request) => confirmResult(request.domains['accounts.runtime'], token(4)),
    loadNetwork: async () => ['post-epoch-network']
  })
  const postEpochLoad = postEpochController.load()
  for (let attempt = 0; attempt < 10 && !postParticipant; attempt += 1) await microtask()
  assert(postParticipant, 'changed GET 完成后必须已进入 post barrier')
  postEpoch += 1
  postGate.resolve(confirmedActivation('post', postParticipant, 'unchanged', token(4)))
  const postEpochResult = await postEpochLoad
  assert.equal(postEpochResult.superseded, true, 'post barrier 在途期间 writeEpoch 变化必须 supersede 结果')
  assert.equal(await postStorage.read(postEpochController.key), undefined, '旧 writeEpoch 的 post 结果不得写缓存')

  const generationStorage = createMemoryPageDataCacheStorage()
  const firstNetwork = deferred<string[]>()
  const secondNetwork = deferred<string[]>()
  let generationNetworkIndex = 0
  let generationPostCalls = 0
  const generationController = new PageDataCacheController<string[]>({
    cacheKey: { ...keyInput, scope: 'self:generation' },
    domain: 'accounts.runtime', viewScope: 'self', storage: generationStorage,
    activation: activationHandle({
      register: (participant) => confirmedActivation('pre', participant, 'reload', token(3)),
      stabilize: (participant) => {
        generationPostCalls += 1
        return confirmedActivation('post', participant, 'unchanged', token(3))
      }
    }),
    writeEpoch: () => 0,
    confirm: async (request) => confirmResult(request.domains['accounts.runtime'], token(3)),
    loadNetwork: () => (++generationNetworkIndex === 1 ? firstNetwork.promise : secondNetwork.promise)
  })
  const older = generationController.load()
  await microtask()
  const newer = generationController.refresh()
  await microtask()
  secondNetwork.resolve(['newer-generation'])
  assert.equal((await newer).confirmed, true)
  firstNetwork.resolve(['older-generation'])
  assert.equal((await older).superseded, true, 'generation 变化必须 supersede 旧 GET')
  assert.equal(generationPostCalls, 1, '旧 generation 不得参加 post barrier')
  assert.deepEqual((await generationStorage.read<string[]>(generationController.key))?.value, ['newer-generation'])

  hotController.close()
  unmanagedController.close()
  managedController.close()
  failedPreController.close()
  epochController.close()
  touchCommitController.close()
  writeCommitController.close()
  deltaController.close()
  postEpochController.close()
  generationController.close()
  pendingGenerationController.close()
  pendingEpochController.close()
}

function activationHandle(options: {
  register?: (participant: PageDataActivationParticipant) => PageDataActivationDecision | Promise<PageDataActivationDecision>
  stabilize?: (participant: PageDataActivationParticipant & { baseline: PageDataRevisionToken }) => PageDataActivationDecision | Promise<PageDataActivationDecision>
}): PageDataActivationHandle {
  return {
    register: async (participant) => options.register?.(participant)
      ?? activationFailure('unavailable', 'pre', participant),
    stabilize: async (participant) => options.stabilize?.(participant)
      ?? activationFailure('unavailable', 'post', participant),
    trigger: () => undefined,
    deactivate: () => undefined,
    dispose: () => undefined
  }
}

function confirmedActivation(
  phase: PageDataActivationPhase,
  participant: PageDataActivationParticipant,
  action: 'unchanged' | 'delta' | 'reload' | 'reset',
  currentToken: PageDataRevisionToken
): PageDataActivationDecision {
  return {
    state: 'confirmed',
    phase,
    participant: { ...participant },
    result: {
      action,
      token: { ...currentToken },
      ...(action === 'delta' ? { changes: [{ kind: 'upsert', id: 'delta', fieldMask: ['name'] }] } : {}),
      serverTime: '2026-07-17T12:00:31.000Z'
    }
  }
}

function activationFailure(
  state: 'token_conflict' | 'late' | 'superseded' | 'unavailable',
  phase: PageDataActivationPhase,
  participant: PageDataActivationParticipant
): PageDataActivationDecision {
  return { state, phase, participant: { ...participant } }
}

async function testMicrotaskConfirmBatching(): Promise<void> {
  const storage = createMemoryPageDataCacheStorage()
  const requests: PageDataConfirmRequest[] = []
  const confirm = async (request: PageDataConfirmRequest): Promise<PageDataConfirmResult> => {
    requests.push(request)
    return {
      serverTime: '2026-07-17T12:00:00.000Z',
      domains: Object.fromEntries(Object.entries(request.domains).map(([domain, known]) => [domain, {
        action: 'unchanged',
        token: known
      }])) as PageDataConfirmResult['domains']
    }
  }
  const createController = async (domain: PageDataRevisionToken['domain'], route: string, revision: PageDataRevisionToken) => {
    const input = { scope: 'self:batched', route, query: {}, version: 1, domain }
    await storage.writeIfCurrent(cacheRecord(createPageDataCacheKey(input), [route], { token: revision, confirmedAt: '2026-07-17T12:00:00.000Z' }))
    return new PageDataCacheController<string[]>({
      cacheKey: input, domain, viewScope: 'self', storage, confirm, loadNetwork: async () => [route]
    })
  }
  const accounts = await createController('accounts.runtime', '/accounts', domainToken('accounts.runtime', 1))
  const usage = await createController('usage.records', '/usage', domainToken('usage.records', 2))
  await Promise.all([accounts.confirmNow(), usage.confirmNow()])
  assert.equal(requests.length, 1, '同 scope 同一微任务的不同 domain 必须合并为一次 confirm')
  assert.deepEqual(Object.keys(requests[0]!.domains).sort(), ['accounts.runtime', 'usage.records'])
  accounts.close()
  usage.close()

  requests.length = 0
  const sameA = await createController('accounts.runtime', '/same-a', domainToken('accounts.runtime', 3))
  const sameB = await createController('accounts.runtime', '/same-b', domainToken('accounts.runtime', 3))
  await Promise.all([sameA.confirmNow(), sameB.confirmNow()])
  assert.equal(requests.length, 1, '同 domain 同 token 必须共享一次 confirm')
  sameA.close()
  sameB.close()

  const distinctTransportCalls: string[] = []
  const createTransportConfirm = (name: string) => async (request: PageDataConfirmRequest): Promise<PageDataConfirmResult> => {
    distinctTransportCalls.push(name)
    return {
      serverTime: '2026-07-17T12:00:00.000Z',
      domains: Object.fromEntries(Object.entries(request.domains).map(([domain, known]) => [domain, {
        action: 'unchanged',
        token: known
      }])) as PageDataConfirmResult['domains']
    }
  }
  const createTransportController = async (
    route: string,
    domain: PageDataRevisionToken['domain'],
    sequence: number,
    confirmTransport: (request: PageDataConfirmRequest) => Promise<PageDataConfirmResult>,
    confirmBatchKey?: string
  ) => {
    const input = { scope: 'self:transport-isolation', route, query: {}, version: 1, domain }
    await storage.writeIfCurrent(cacheRecord(createPageDataCacheKey(input), [route], {
      token: domainToken(domain, sequence),
      confirmedAt: '2026-07-17T12:00:00.000Z'
    }))
    return new PageDataCacheController<string[]>({
      cacheKey: input,
      domain,
      viewScope: 'self',
      storage,
      confirm: confirmTransport,
      confirmBatchKey,
      loadNetwork: async () => [route]
    })
  }
  const isolatedTransportA = await createTransportController(
    '/transport-a',
    'accounts.runtime',
    4,
    createTransportConfirm('transport-a')
  )
  const isolatedTransportB = await createTransportController(
    '/transport-b',
    'usage.records',
    5,
    createTransportConfirm('transport-b')
  )
  await Promise.all([isolatedTransportA.confirmNow(), isolatedTransportB.confirmNow()])
  assert.deepEqual(distinctTransportCalls.sort(), ['transport-a', 'transport-b'], '没有显式 batch key 时不同 confirm transport 不得误合批')
  isolatedTransportA.close()
  isolatedTransportB.close()

  distinctTransportCalls.length = 0
  const sharedTransportA = await createTransportController(
    '/shared-transport-a',
    'accounts.runtime',
    4,
    createTransportConfirm('shared-transport-a'),
    'shared-page-data-http-client'
  )
  const sharedTransportB = await createTransportController(
    '/shared-transport-b',
    'usage.records',
    5,
    createTransportConfirm('shared-transport-b'),
    'shared-page-data-http-client'
  )
  await Promise.all([sharedTransportA.confirmNow(), sharedTransportB.confirmNow()])
  assert.equal(distinctTransportCalls.length, 1, '显式相同 batch key 时不同组件包装函数必须合并为一次 confirm')
  sharedTransportA.close()
  sharedTransportB.close()

  requests.length = 0
  const inFlightConfirm = deferred<PageDataConfirmResult>()
  let inFlightCalls = 0
  const delayedConfirm = async (request: PageDataConfirmRequest): Promise<PageDataConfirmResult> => {
    inFlightCalls += 1
    assert.equal(request.domains['accounts.runtime']?.sequence, 6)
    return inFlightConfirm.promise
  }
  const delayedController = async (route: string) => {
    const input = { scope: 'self:in-flight-batch', route, query: {}, version: 1, domain: 'accounts.runtime' as const }
    await storage.writeIfCurrent(cacheRecord(createPageDataCacheKey(input), [route], { token: domainToken('accounts.runtime', 6), confirmedAt: '2026-07-17T12:00:00.000Z' }))
    return new PageDataCacheController<string[]>({
      cacheKey: input, domain: 'accounts.runtime', viewScope: 'self', storage, confirm: delayedConfirm, loadNetwork: async () => [route]
    })
  }
  const delayedA = await delayedController('/delayed-a')
  const delayedB = await delayedController('/delayed-b')
  const firstDelayed = delayedA.confirmNow()
  await microtask()
  assert.equal(inFlightCalls, 1, '首次 confirm 应已进入 HTTP 在途状态')
  const secondDelayed = delayedB.confirmNow()
  await microtask()
  assert.equal(inFlightCalls, 1, 'HTTP 在途期间同 scope/domain/token 的 confirm 必须继续共享')
  inFlightConfirm.resolve({
    serverTime: '2026-07-17T12:00:00.000Z',
    domains: { 'accounts.runtime': { action: 'unchanged', token: domainToken('accounts.runtime', 6) } }
  })
  assert.deepEqual((await Promise.all([firstDelayed, secondDelayed])).map((outcome) => outcome.state), ['unchanged', 'unchanged'])
  delayedA.close()
  delayedB.close()

  const isolatedCalls: number[] = []
  const isolatedResults = new Map<number, ReturnType<typeof deferred<PageDataConfirmResult>>>()
  const isolatedConfirm = async (request: PageDataConfirmRequest): Promise<PageDataConfirmResult> => {
    const sequence = request.domains['accounts.runtime']!.sequence
    isolatedCalls.push(sequence)
    const result = deferred<PageDataConfirmResult>()
    isolatedResults.set(sequence, result)
    return result.promise
  }
  const isolatedController = async (route: string, sequence: number) => {
    const input = { scope: 'self:in-flight-isolated', route, query: {}, version: 1, domain: 'accounts.runtime' as const }
    await storage.writeIfCurrent(cacheRecord(createPageDataCacheKey(input), [route], { token: domainToken('accounts.runtime', sequence), confirmedAt: '2026-07-17T12:00:00.000Z' }))
    return new PageDataCacheController<string[]>({
      cacheKey: input, domain: 'accounts.runtime', viewScope: 'self', storage, confirm: isolatedConfirm, loadNetwork: async () => [route]
    })
  }
  const isolatedA = await isolatedController('/isolated-a', 10)
  const isolatedB = await isolatedController('/isolated-b', 11)
  const firstIsolated = isolatedA.confirmNow()
  await microtask()
  const secondIsolated = isolatedB.confirmNow()
  await microtask()
  assert.deepEqual(isolatedCalls, [10, 11], 'HTTP 在途期间同 domain 不同 token 必须保持独立 confirm')
  for (const sequence of isolatedCalls) {
    isolatedResults.get(sequence)!.resolve({
      serverTime: '2026-07-17T12:00:00.000Z',
      domains: { 'accounts.runtime': { action: 'unchanged', token: domainToken('accounts.runtime', sequence) } }
    })
  }
  await Promise.all([firstIsolated, secondIsolated])
  isolatedA.close()
  isolatedB.close()

  let synchronousThrowCalls = 0
  const synchronousThrowInput = { scope: 'self:sync-throw', route: '/sync-throw', query: {}, version: 1, domain: 'accounts.runtime' as const }
  await storage.writeIfCurrent(cacheRecord(createPageDataCacheKey(synchronousThrowInput), ['sync-throw'], { token: domainToken('accounts.runtime', 12), confirmedAt: '2026-07-17T12:00:00.000Z' }))
  const synchronousThrowController = new PageDataCacheController<string[]>({
    cacheKey: synchronousThrowInput,
    domain: 'accounts.runtime', viewScope: 'self', storage,
    confirm: (() => {
    synchronousThrowCalls += 1
    throw new Error('synchronous confirm failure')
    }) as (request: PageDataConfirmRequest) => Promise<PageDataConfirmResult>,
    loadNetwork: async () => ['sync-throw']
  })
  assert.equal((await synchronousThrowController.confirmNow()).state, 'unavailable', 'confirm 同步抛错必须转换为正常 unavailable 结果')
  assert.equal((await synchronousThrowController.confirmNow()).state, 'unavailable', '同步抛错后的 batch 必须清理并允许再次请求')
  assert.equal(synchronousThrowCalls, 2)
  synchronousThrowController.close()

  requests.length = 0
  const deltas: number[] = []
  const deltaConfirm = async (request: PageDataConfirmRequest): Promise<PageDataConfirmResult> => {
    requests.push(request)
    const known = request.domains['accounts.runtime']!
    return {
      serverTime: '2026-07-17T12:00:00.000Z',
      domains: {
        'accounts.runtime': {
          action: 'delta',
          token: domainToken('accounts.runtime', known.sequence + 1),
          changes: [{ kind: 'upsert', id: String(known.sequence) }]
        }
      }
    }
  }
  const different = async (route: string, sequence: number) => {
    const input = { scope: 'self:different-token', route, query: {}, version: 1, domain: 'accounts.runtime' as const }
    await storage.writeIfCurrent(cacheRecord(createPageDataCacheKey(input), [sequence], { token: domainToken('accounts.runtime', sequence), confirmedAt: '2026-07-17T12:00:00.000Z' }))
    return new PageDataCacheController<number[]>({
      cacheKey: input,
      domain: 'accounts.runtime', viewScope: 'self', storage, confirm: deltaConfirm,
      loadNetwork: async () => [sequence],
      applyDelta: (current, changes) => {
        const value = Number(changes[0]?.id)
        deltas.push(value)
        return [...current, value]
      }
    })
  }
  const differentA = await different('/different-a', 4)
  const differentB = await different('/different-b', 7)
  const outcomes = await Promise.all([differentA.confirmNow(), differentB.confirmNow()])
  assert.equal(requests.length, 2, '同 domain 不同 token 不能错误共享一次 delta confirm')
  assert.deepEqual(deltas.sort((left, right) => left - right), [4, 7])
  assert.deepEqual(outcomes.map((outcome) => outcome.data), [[4, 4], [7, 7]], '每个 delta 必须应用到对应 token 的缓存')
  differentA.close()
  differentB.close()
}

async function testFollowerTakesOverWhenLeaderDoesNotOwnKey(): Promise<void> {
  const hub = new FakeChannelHub()
  const idleLeader = new BrowserPageDataTabCoordinator({ tabId: 'a', channelFactory: hub.factory, heartbeatIntervalMs: 60_000 })
  const followerCoordinator = new BrowserPageDataTabCoordinator({ tabId: 'b', channelFactory: hub.factory, heartbeatIntervalMs: 60_000 })
  const storage = createMemoryPageDataCacheStorage()
  const keyInput = { scope: 'self:follower-takeover', route: '/accounts', query: {}, version: 1 }
  const key = createPageDataCacheKey({ ...keyInput, domain: 'accounts.runtime' })
  await storage.writeIfCurrent(cacheRecord(key, ['cached'], { token: token(1), confirmedAt: '2026-07-17T12:00:00.000Z' }))
  let confirmCalls = 0
  const controller = new PageDataCacheController<string[]>({
    cacheKey: keyInput,
    domain: 'accounts.runtime',
    viewScope: 'self',
    storage,
    tabCoordinator: followerCoordinator,
    confirm: async (request) => {
      confirmCalls += 1
      return confirmResult(request.domains['accounts.runtime'])
    },
    loadNetwork: async () => ['network']
  })
  try {
    const outcome = await controller.requestConfirm()
    assert.equal(confirmCalls, 1, 'leader 没有该 key controller 时 follower 必须短等待后本地接管 confirm')
    assert.equal(outcome.state, 'unchanged')
  } finally {
    controller.close()
    idleLeader.close()
    followerCoordinator.close()
  }
}

async function testFollowerMaxStaleFallsBackToNetwork(): Promise<void> {
  const hub = new FakeChannelHub()
  const idleLeader = new BrowserPageDataTabCoordinator({ tabId: 'a', channelFactory: hub.factory, heartbeatIntervalMs: 60_000 })
  const followerCoordinator = new BrowserPageDataTabCoordinator({ tabId: 'b', channelFactory: hub.factory, heartbeatIntervalMs: 60_000 })
  const storage = createMemoryPageDataCacheStorage()
  const keyInput = { scope: 'self:follower-stale', route: '/usage-records', query: {}, version: 1 }
  const key = createPageDataCacheKey({ ...keyInput, domain: 'accounts.runtime' })
  await storage.writeIfCurrent(cacheRecord(key, ['stale'], { token: token(1), confirmedAt: '2026-07-17T12:00:00.000Z' }))
  let networkLoads = 0
  const controller = new PageDataCacheController<string[]>({
    cacheKey: keyInput,
    domain: 'accounts.runtime',
    viewScope: 'self',
    storage,
    tabCoordinator: followerCoordinator,
    maxStaleMs: 15_000,
    now: () => new Date('2026-07-17T12:00:16.000Z'),
    confirm: async () => { throw new Error('confirm unavailable') },
    loadNetwork: async () => {
      networkLoads += 1
      return ['fresh-network']
    }
  })
  try {
    const outcome = await controller.requestConfirm()
    assert.equal(networkLoads, 1, 'follower 缓存超过 maxStale 后必须本地 confirm 并回退业务接口')
    assert.equal(outcome.state, 'updated')
    assert.deepEqual(outcome.data, ['fresh-network'])
  } finally {
    controller.close()
    idleLeader.close()
    followerCoordinator.close()
  }
}

async function testLeaderInvalidationWithdrawsFollowerData(): Promise<void> {
  const hub = new FakeChannelHub()
  const leaderCoordinator = new BrowserPageDataTabCoordinator({ tabId: 'a', channelFactory: hub.factory, heartbeatIntervalMs: 60_000 })
  const followerCoordinator = new BrowserPageDataTabCoordinator({ tabId: 'b', channelFactory: hub.factory, heartbeatIntervalMs: 60_000 })
  const leaderStorage = createMemoryPageDataCacheStorage()
  const followerStorage = createMemoryPageDataCacheStorage()
  const keyInput = { scope: 'self:invalidation', route: '/accounts', query: {}, version: 1 }
  const key = createPageDataCacheKey({ ...keyInput, domain: 'accounts.runtime' })
  const stale = cacheRecord(key, ['stale'], { token: token(1), confirmedAt: '2026-07-17T12:00:00.000Z' })
  await leaderStorage.writeIfCurrent(stale)
  await followerStorage.writeIfCurrent(stale)
  const common = {
    cacheKey: keyInput,
    domain: 'accounts.runtime' as const,
    viewScope: 'self' as const,
    maxStaleMs: 15_000,
    now: () => new Date('2026-07-17T12:00:16.000Z'),
    confirm: async () => { throw new Error('confirm unavailable') },
    loadNetwork: async (): Promise<string[]> => { throw new Error('business unavailable') }
  }
  const leaderController = new PageDataCacheController<string[]>({ ...common, storage: leaderStorage, tabCoordinator: leaderCoordinator })
  const followerController = new PageDataCacheController<string[]>({ ...common, storage: followerStorage, tabCoordinator: followerCoordinator })
  const followerUpdates: Array<string[] | undefined> = []
  followerController.subscribe((record) => followerUpdates.push(record?.value))
  try {
    const outcome = await followerController.requestConfirm()
    assert.equal(outcome.state, 'unavailable')
    await microtask()
    assert.equal(await followerStorage.read(key), undefined, 'leader 删除缓存后 follower 本地缓存也必须失效')
    assert.deepEqual(followerUpdates, [undefined], 'invalidated 广播必须通知页面撤下旧数据')
  } finally {
    leaderController.close()
    followerController.close()
    leaderCoordinator.close()
    followerCoordinator.close()
  }
}
