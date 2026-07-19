import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import type { PageDataConfirmRequest, PageDataConfirmResult, PageDataRevisionToken } from '../../api/domains/pageData'
import {
  BrowserPageDataTabCoordinator,
  PageDataCacheController,
  PageDataRequestCacheManager,
  PageDataVisibleConfirmScheduler,
  createPageDataActivationController,
  type PageDataCacheRecord,
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

await testMicrotaskConfirmBatching()

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
assert.match(composableSource, /onActivate:\s*\(\)\s*=>\s*\{\s*void confirmCurrent\(\)\.catch\(\(\)\s*=>\s*undefined\)\s*\}/, 'KeepAlive 页面恢复必须立即确认且吞掉已处理的后台拒绝')
assert.match(composableSource, /onMounted[\s\S]*activationController\.mount\(\)/, '首次 mount 必须只启动调度，不能触发恢复确认')
assert.match(composableSource, /onUnmounted[\s\S]*activationController\.dispose\(\)[\s\S]*controller\.close\(\)/, 'Vue composable 卸载时必须清理调度器、订阅和 controller')
const requestComposableSource = readFileSync(fileURLToPath(new URL('../../composables/usePageDataRequestCache.ts', import.meta.url)), 'utf8')
assert.match(requestComposableSource, /resolveRequest\(\)/, '动态页面缓存每次 load 必须解析当前 scope、route、query 与 version')
assert.match(requestComposableSource, /manager\.subscribe/, '动态页面缓存必须只订阅当前 manager 的更新')
assert.match(requestComposableSource, /manager\.confirmCurrent\(\)/, '30 秒和 focus 只能确认当前 query')
assert.match(requestComposableSource, /applyConfirmOutcome/, '周期 confirm 必须把过期不可用结果同步给页面，不能继续展示旧快照')
assert.match(requestComposableSource, /getDefaultPageDataTabCoordinator\(\)/, '页面消费者未显式传入协调器时必须共享默认多标签 leader')
assert.match(requestComposableSource, /forceRefresh/, '动态页面缓存必须提供当前 query 强制刷新')
assert.match(requestComposableSource, /onActivated[\s\S]*activationController\.activate\(\)/, '动态 KeepAlive 页面激活时必须恢复确认调度')
assert.match(requestComposableSource, /onDeactivated[\s\S]*activationController\.deactivate\(\)/, '动态 KeepAlive 页面隐藏时必须停止确认调度')
assert.match(requestComposableSource, /onActivate:\s*\(\)\s*=>\s*\{\s*void confirmCurrent\(\)\.catch\(\(\)\s*=>\s*undefined\)\s*\}/, '动态 KeepAlive 页面恢复必须立即确认且吞掉已处理的后台拒绝')
assert.match(requestComposableSource, /onMounted[\s\S]*activationController\.mount\(\)/, '动态页面首次 mount 必须只启动调度，不能触发恢复确认')
assert.match(requestComposableSource, /onUnmounted[\s\S]*activationController\.dispose\(\)[\s\S]*manager\.close\(\)/, '动态页面缓存卸载必须清理当前 query controller 与订阅')

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

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  return { promise: new Promise<T>((next) => { resolve = next }), resolve }
}

async function microtask(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
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
