import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath, pathToFileURL } from 'node:url'

import type {
  PageDataConfirmRequest,
  PageDataConfirmResult,
  PageDataRevisionToken
} from '../../api/domains/pageData'
import type {
  PageDataActivationHandle,
  PageDataActivationParticipant
} from '../../shared/pageDataActivationCoordinator'
import {
  BrowserPageDataTabCoordinator,
  createPageDataCacheKey,
  createMemoryPageDataCacheStorage,
  type PageDataLoadResult,
  type PageDataTabCoordinator
} from '../../shared/pageDataCache'

const resourceModulePath = fileURLToPath(new URL('../../shared/pageDataResourceCache.ts', import.meta.url))
assert.equal(existsSync(resourceModulePath), true, '必须实现统一 IndexedDB page-data resource cache')

type ResourceRequest<T> = {
  cacheKey: { scope: string; route: string; query: unknown; version: string | number }
  domain: 'providers.catalog'
  viewScope: 'self'
  loadNetwork: () => Promise<T>
  maxStaleMs?: number
  activation?: PageDataActivationHandle
  writeEpoch?: (domain: 'providers.catalog') => number
}
type ResourceCache = {
  load<T>(request: ResourceRequest<T>): Promise<PageDataLoadResult<T>>
  invalidate(domain: 'providers.catalog', scope?: string, route?: string): Promise<void>
  close(): void
}
type ResourceCacheConstructor = new (options: {
  storage: ReturnType<typeof createMemoryPageDataCacheStorage>
  confirm: (request: PageDataConfirmRequest) => Promise<PageDataConfirmResult>
  tabCoordinator: PageDataTabCoordinator
  now?: () => Date
  activation?: PageDataActivationHandle
  writeEpoch?: (domain: 'providers.catalog') => number
}) => ResourceCache

const resourceModule = await import(pathToFileURL(resourceModulePath).href) as {
  PageDataResourceCache: ResourceCacheConstructor
}
const tabCoordinator = new BrowserPageDataTabCoordinator({ heartbeatIntervalMs: 60_000 })

const token = (sequence: number, scope = 'scope-self'): PageDataRevisionToken => ({
  protocolVersion: 2,
  epoch: 'resource-epoch',
  scope,
  domain: 'providers.catalog',
  sequence,
  resetSequence: 0
})

let sequence = 1
let networkLoads = 0
let confirmCalls = 0
const storage = createMemoryPageDataCacheStorage()
const cache = new resourceModule.PageDataResourceCache({
  storage,
  tabCoordinator,
  now: () => new Date('2026-07-18T12:00:01.000Z'),
  confirm: async (request) => {
    confirmCalls += 1
    const known = request.domains['providers.catalog']
    const current = token(sequence)
    return {
      serverTime: '2026-07-18T12:00:00.000Z',
      domains: {
        'providers.catalog': {
          action: known?.sequence === current.sequence ? 'unchanged' : 'reload',
          token: current
        }
      }
    }
  }
})

const request = (): ResourceRequest<string[]> => ({
  cacheKey: {
    scope: 'self:user-a',
    route: '/providers/gpt/models',
    query: { providerCode: 'gpt', systemAccountId: 'user-a', includeUnpriced: true },
    version: 1
  },
  domain: 'providers.catalog',
  viewScope: 'self',
  loadNetwork: async () => {
    networkLoads += 1
    return [`models-${networkLoads}`]
  }
})

const first = await cache.load(request())
assert.equal(first.source, 'network')
assert.deepEqual(first.data, ['models-1'])
assert.equal(networkLoads, 1)
assert.equal(confirmCalls, 2, '首次 miss 必须用前后两次 confirm 封闭竞态')

const second = await cache.load(request())
assert.equal(second.source, 'cache')
assert.deepEqual(second.data, ['models-1'])
assert.equal(networkLoads, 1, 'IndexedDB / storage 命中不得读取完整模型接口')
assert.equal((await second.confirmation)?.state, 'unchanged')

sequence = 2
const changed = await cache.load(request())
assert.equal(changed.source, 'cache', 'revision 变化前仍应先返回本地快照')
const changedConfirmation = await changed.confirmation
assert.equal(changedConfirmation?.state, 'updated')
assert.deepEqual(changedConfirmation?.data, ['models-2'])
assert.equal(networkLoads, 2, 'revision 变化后后台确认只回源一次')

await cache.invalidate('providers.catalog', 'self:user-a')
const afterInvalidation = await cache.load(request())
assert.equal(afterInvalidation.source, 'network', '按 domain + scope 失效后下一次必须回源')
assert.deepEqual(afterInvalidation.data, ['models-3'])

await cache.invalidate('providers.catalog', 'self:user-a')
const concurrent = await Promise.all([cache.load(request()), cache.load(request())])
assert.deepEqual(concurrent.map((item) => item.data), [['models-4'], ['models-4']])
assert.equal(networkLoads, 4, '相同资源 key 并发 miss 必须共享一次 loader')

let otherScopeLoads = 0
const otherScope = await cache.load({
  ...request(),
  cacheKey: { ...request().cacheKey, scope: 'self:user-b' },
  loadNetwork: async () => {
    otherScopeLoads += 1
    return ['user-b-models']
  }
})
assert.deepEqual(otherScope.data, ['user-b-models'])
assert.equal(otherScopeLoads, 1, '不同用户 scope 不能复用资源缓存')

let gptRouteLoads = 0
let openAiRouteLoads = 0
const routeCache = new resourceModule.PageDataResourceCache({
  storage: createMemoryPageDataCacheStorage(),
  tabCoordinator,
  now: () => new Date('2026-07-18T12:00:01.000Z'),
  confirm: async (input) => {
    const current = token(1)
    const known = input.domains['providers.catalog']
    return {
      serverTime: '2026-07-18T12:00:00.000Z',
      domains: {
        'providers.catalog': {
          action: known?.sequence === current.sequence ? 'unchanged' : 'reload',
          token: current
        }
      }
    }
  }
})
const routeRequest = (providerCode: 'gpt' | 'openai'): ResourceRequest<string[]> => ({
  cacheKey: {
    scope: 'self:route-user',
    route: `/providers/${providerCode}/models`,
    query: { providerCode },
    version: 1
  },
  domain: 'providers.catalog',
  viewScope: 'self',
  loadNetwork: async () => {
    if (providerCode === 'gpt') gptRouteLoads += 1
    else openAiRouteLoads += 1
    return [`${providerCode}-models`]
  }
})
await routeCache.load(routeRequest('gpt'))
await routeCache.load(routeRequest('openai'))
await routeCache.invalidate('providers.catalog', 'self:route-user', '/providers/gpt/models')
await routeCache.load(routeRequest('gpt'))
await routeCache.load(routeRequest('openai'))
assert.equal(gptRouteLoads, 2, '目标供应商 route 失效后必须重新读取')
assert.equal(openAiRouteLoads, 1, '目标供应商 route 失效不得清理同域其他供应商缓存')

let registryNowMs = Date.parse('2026-07-18T12:00:00.000Z')
const registryStorage = createMemoryPageDataCacheStorage({ now: () => new Date(registryNowMs) })
let registryNetworkLoads = 0
const registryCache = new resourceModule.PageDataResourceCache({
  storage: registryStorage,
  tabCoordinator,
  now: () => new Date(registryNowMs),
  confirm: async () => { throw new Error('confirm unavailable') }
})
const registryRequest = (): ResourceRequest<string[]> => ({
  ...request(),
  loadNetwork: async () => {
    registryNetworkLoads += 1
    return [`registry-${registryNetworkLoads}`]
  }
})
assert.deepEqual((await registryCache.load(registryRequest())).data, ['registry-1'])
registryNowMs += 300_001
assert.deepEqual((await registryCache.load(registryRequest())).data, ['registry-2'], '调用方未显式配置时必须采用 providers.catalog 注册表的 maxStaleMs，超时且确认失败后回源')
assert.equal(registryNetworkLoads, 2)

let managedRegisters = 0
let managedStabilizes = 0
let managedLegacyConfirms = 0
const managedActivation: PageDataActivationHandle = {
  register: async (participant) => {
    managedRegisters += 1
    assertManagedParticipant(participant)
    return {
      state: 'confirmed',
      phase: 'pre',
      participant,
      result: { action: 'reload', token: token(8), serverTime: '2026-07-18T12:00:00.000Z' }
    }
  },
  stabilize: async (participant) => {
    managedStabilizes += 1
    assertManagedParticipant(participant)
    assert.equal(participant.baseline.sequence, 8)
    return {
      state: 'confirmed',
      phase: 'post',
      participant,
      result: { action: 'unchanged', token: token(8), serverTime: '2026-07-18T12:00:00.000Z' }
    }
  },
  trigger: () => undefined,
  deactivate: () => undefined,
  dispose: () => undefined
}
const managedResourceCache = new resourceModule.PageDataResourceCache({
  storage: createMemoryPageDataCacheStorage(),
  tabCoordinator,
  activation: managedActivation,
  writeEpoch: () => 12,
  confirm: async () => {
    managedLegacyConfirms += 1
    throw new Error('受管 resource cache 不得调用私有 confirm')
  }
})
const managedResource = await managedResourceCache.load({
  ...request(),
  cacheKey: { ...request().cacheKey, scope: 'self:managed-resource' },
  loadNetwork: async () => ['managed-resource']
})
assert.deepEqual(managedResource.data, ['managed-resource'])
assert.equal(managedResource.confirmed, true)
assert.deepEqual([managedRegisters, managedStabilizes, managedLegacyConfirms], [1, 1, 0], 'resource cache 必须透传 activation/writeEpoch 并复用 pre/post barrier')

let rebindingNowMs = Date.parse('2026-07-18T12:00:00.000Z')
let rebindingLegacyConfirms = 0
let rebindingManagedRegisters = 0
let rebindingManagedStabilizes = 0
let rebindingManagedLoads = 0
const rebindingStorage = createMemoryPageDataCacheStorage({ now: () => new Date(rebindingNowMs) })
const rebindingRequest = (): ResourceRequest<string[]> => ({
  ...request(),
  cacheKey: { ...request().cacheKey, scope: 'self:rebinding' },
  loadNetwork: async () => ['unmanaged-network']
})
const rebindingKey = createPageDataCacheKey({ ...rebindingRequest().cacheKey, domain: 'providers.catalog' })
await rebindingStorage.writeIfCurrent({
  key: rebindingKey,
  domain: 'providers.catalog',
  scope: rebindingRequest().cacheKey.scope,
  route: rebindingRequest().cacheKey.route,
  value: ['unmanaged-cache'],
  token: token(7),
  confirmedAt: new Date(rebindingNowMs).toISOString(),
  writtenAt: new Date(rebindingNowMs).toISOString(),
  expiresAt: new Date(rebindingNowMs + 60_000).toISOString(),
  lastAccessedAt: new Date(rebindingNowMs).toISOString()
})
const rebindingCache = new resourceModule.PageDataResourceCache({
  storage: rebindingStorage,
  tabCoordinator,
  now: () => new Date(rebindingNowMs),
  confirm: async (input) => {
    rebindingLegacyConfirms += 1
    const current = token(7)
    return {
      serverTime: new Date(rebindingNowMs).toISOString(),
      domains: {
        'providers.catalog': {
          action: input.domains['providers.catalog']?.sequence === current.sequence ? 'unchanged' : 'reload',
          token: current
        }
      }
    }
  }
})
const unmanagedPending = rebindingCache.load(rebindingRequest())
rebindingNowMs += 31_000
const reboundActivation: PageDataActivationHandle = {
  register: async (participant) => {
    rebindingManagedRegisters += 1
    assert.equal(participant.writeEpoch, 21)
    return {
      state: 'confirmed', phase: 'pre', participant,
      result: { action: 'reload', token: token(8), serverTime: new Date(rebindingNowMs).toISOString() }
    }
  },
  stabilize: async (participant) => {
    rebindingManagedStabilizes += 1
    assert.equal(participant.writeEpoch, 21)
    return {
      state: 'confirmed', phase: 'post', participant,
      result: { action: 'unchanged', token: token(8), serverTime: new Date(rebindingNowMs).toISOString() }
    }
  },
  trigger: () => undefined,
  deactivate: () => undefined,
  dispose: () => undefined
}
const reboundWriteEpoch = () => 21
const reboundNetwork = deferred<string[]>()
const reboundRequest = (): ResourceRequest<string[]> => ({
  ...rebindingRequest(),
  activation: reboundActivation,
  writeEpoch: reboundWriteEpoch,
  loadNetwork: async () => {
    rebindingManagedLoads += 1
    return reboundNetwork.promise
  }
})
const firstRebound = rebindingCache.load(reboundRequest())
await unmanagedPending
const secondRebound = rebindingCache.load(reboundRequest())
reboundNetwork.resolve(['managed-rebound'])
const rebound = await Promise.all([firstRebound, secondRebound])
assert.deepEqual(rebound.map((item) => item.data), [['managed-rebound'], ['managed-rebound']], 'managed 请求不得复用同 key 的 unmanaged pending/controller')
assert.deepEqual(
  [rebindingLegacyConfirms, rebindingManagedRegisters, rebindingManagedStabilizes, rebindingManagedLoads],
  [0, 1, 1, 1],
  '重绑定必须关闭旧 controller，并让相同新绑定继续合并并发'
)

const resourceSource = readFileSync(resourceModulePath, 'utf8')
assert.match(resourceSource, /invalidateDefaultPageDataResourceCache[\s\S]{0,700}createPageDataCacheStorage\(\)/, '默认 resource cache 尚未实例化时也必须直接清理 IndexedDB domain')
assert.match(resourceSource, /onDomainInvalidated/, 'resource cache 必须接收其他标签页发出的按域失效广播')

cache.close()
routeCache.close()
registryCache.close()
managedResourceCache.close()
rebindingCache.close()
tabCoordinator.close()
console.log('页面 resource cache 回归通过：cache-first、轻量确认、并发合并、scope 隔离和 domain 失效生效')

function assertManagedParticipant(participant: PageDataActivationParticipant): void {
  assert.equal(participant.domain, 'providers.catalog')
  assert.equal(participant.writeEpoch, 12)
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  return { promise: new Promise<T>((next) => { resolve = next }), resolve }
}
