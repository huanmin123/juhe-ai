import assert from 'node:assert/strict'

import type { AxiosAdapter } from 'axios'
import { ref } from 'vue'

import { myRouteStrategiesApi, routeStrategiesApi } from '../../api/domains/routeStrategies.js'
import { http } from '../../api/http.js'
import { useScopedRouteStrategiesApi } from '../../composables/useScopedDomainApi.js'
import type {
  RouteStrategyListItem,
  RouteStrategyListResult,
  RouteStrategyListSnapshotResult
} from '../../types/domain/index.js'
import {
  createRouteStrategyListProgressiveState,
  routeStrategyListFallbackPage,
  type RouteStrategyListSnapshotState
} from '../../views/route-strategies/routeStrategyListProgressiveState.js'

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (error: unknown) => void
}

let scopeKey = 'self:user-a'
let visibleIds: string[] = []
let loading = false
let listItems: RouteStrategyListItem[] = []
let snapshotStates = new Map<string, RouteStrategyListSnapshotState>()
const listErrors: unknown[] = []

const state = createRouteStrategyListProgressiveState({
  currentScopeKey: () => scopeKey,
  currentVisibleIds: () => visibleIds,
  setListLoading: (value) => {
    loading = value
  },
  applyList: (result) => {
    listItems = result.items
    visibleIds = result.items.map((item) => item.id)
  },
  applySnapshotStates: (states) => {
    snapshotStates = states
  },
  onListError: (error) => {
    listErrors.push(error)
  }
})

const initialSnapshot = deferred<RouteStrategyListSnapshotResult>()
let initialSnapshotCalls = 0
const initialLoad = state.load({
  scopeKey,
  list: async () => listResult([listItem('route-a'), listItem('route-b')]),
  snapshot: async (ids) => {
    initialSnapshotCalls += 1
    assert.deepEqual(ids, ['route-a', 'route-b'], 'snapshot 必须只请求当前基础列表的去重 ID')
    return initialSnapshot.promise
  }
})

assert.equal(await initialLoad, true)
assert.equal(loading, false, '基础列表完成后必须立即结束整表 loading，不等待 snapshot')
assert.deepEqual(listItems.map((item) => item.id), ['route-a', 'route-b'], 'snapshot pending 时基础行必须已经可见')
assert.equal(initialSnapshotCalls, 1, '非空当前页只能发起一次 snapshot 请求')
assert.equal(snapshotStates.get('route-a')?.status, 'pending')
assert.equal(snapshotStates.get('route-b')?.status, 'pending')

initialSnapshot.resolve(snapshotResult([
  snapshotItem('route-a', 0, 0)
]))
await flushPromises()
const zeroState = snapshotStates.get('route-a')
assert.equal(zeroState?.status, 'ready', '真实零值必须进入 ready，不能与 pending/error 混淆')
assert.equal(zeroState?.status === 'ready' ? zeroState.item.bindingCount : undefined, 0)
assert.equal(zeroState?.status === 'ready' ? zeroState.item.apiKeyCount : undefined, 0)
assert.equal(snapshotStates.get('route-b')?.status, 'error', 'snapshot 缺少当前可见 ID 时不能伪装成零值')

let emptySnapshotCalls = 0
assert.equal(await state.load({
  scopeKey,
  list: async () => listResult([]),
  snapshot: async () => {
    emptySnapshotCalls += 1
    return snapshotResult([])
  }
}), true)
assert.equal(emptySnapshotCalls, 0, '空页不得发起 snapshot 请求')
assert.equal(snapshotStates.size, 0, '空页必须清除上一页动态状态')

const failedSnapshot = deferred<RouteStrategyListSnapshotResult>()
await state.load({
  scopeKey,
  list: async () => listResult([listItem('route-error')]),
  snapshot: async () => failedSnapshot.promise
})
failedSnapshot.reject(new Error('snapshot unavailable'))
await flushPromises()
assert.equal(snapshotStates.get('route-error')?.status, 'error', 'snapshot 失败应只标记动态列错误')
assert.equal(listItems[0]?.id, 'route-error', 'snapshot 失败不能清空基础列表')
assert.equal(listErrors.length, 0, 'snapshot 失败不能走阻断性的基础列表错误通道')

const staleList = deferred<RouteStrategyListResult>()
let staleListSnapshotCalls = 0
const staleListLoad = state.load({
  scopeKey,
  list: async () => staleList.promise,
  snapshot: async () => {
    staleListSnapshotCalls += 1
    return snapshotResult([])
  }
})
const currentListLoad = state.load({
  scopeKey,
  list: async () => listResult([listItem('route-current-list')]),
  snapshot: async () => snapshotResult([snapshotItem('route-current-list', 2, 3)])
})
assert.equal(await currentListLoad, true)
await flushPromises()
staleList.resolve(listResult([listItem('route-stale-list')]))
assert.equal(await staleListLoad, false, '迟到的旧列表请求必须被 generation 拒绝')
assert.equal(staleListSnapshotCalls, 0, '已过期基础列表不得继续派生 snapshot 请求')
assert.equal(listItems[0]?.id, 'route-current-list')

const oldPageSnapshot = deferred<RouteStrategyListSnapshotResult>()
await state.load({
  scopeKey,
  list: async () => listResult([listItem('route-old-page')]),
  snapshot: async () => oldPageSnapshot.promise
})
await state.load({
  scopeKey,
  list: async () => listResult([listItem('route-new-page')]),
  snapshot: async () => snapshotResult([snapshotItem('route-new-page', 4, 5)])
})
await flushPromises()
oldPageSnapshot.resolve(snapshotResult([snapshotItem('route-old-page', 99, 99)]))
await flushPromises()
assert.equal(snapshotStates.get('route-new-page')?.status, 'ready')
assert.equal(snapshotStates.has('route-old-page'), false, '旧页 snapshot 迟到不能污染新页')

const staleFailureList = deferred<RouteStrategyListResult>()
const currentPendingList = deferred<RouteStrategyListResult>()
const listErrorCountBeforeStaleFailure = listErrors.length
const staleFailureLoad = state.load({
  scopeKey,
  list: async () => staleFailureList.promise,
  snapshot: async () => snapshotResult([])
})
const currentPendingLoad = state.load({
  scopeKey,
  list: async () => currentPendingList.promise,
  snapshot: async () => snapshotResult([snapshotItem('route-loading-owner', 1, 1)])
})
staleFailureList.reject(new Error('stale list failure'))
assert.equal(await staleFailureLoad, false)
assert.equal(loading, true, '旧 generation 失败不得结束当前 generation 的 loading')
assert.equal(listErrors.length, listErrorCountBeforeStaleFailure, '旧列表失败必须静默，不得弹当前页错误')
currentPendingList.resolve(listResult([listItem('route-loading-owner')]))
assert.equal(await currentPendingLoad, true)
await flushPromises()
assert.equal(loading, false, '只有当前 generation 完成后才能结束 loading')

const ownerASnapshot = deferred<RouteStrategyListSnapshotResult>()
scopeKey = 'management:owner-a'
await state.load({
  scopeKey,
  list: async () => listResult([listItem('same-route-id')]),
  snapshot: async () => ownerASnapshot.promise
})
scopeKey = 'management:owner-b'
await state.load({
  scopeKey,
  list: async () => listResult([listItem('same-route-id')]),
  snapshot: async () => snapshotResult([snapshotItem('same-route-id', 6, 7)])
})
await flushPromises()
ownerASnapshot.resolve(snapshotResult([snapshotItem('same-route-id', 66, 77)]))
await flushPromises()
assertReadyCounts('same-route-id', 6, 7, '旧 owner snapshot 即使 ID 相同也不得覆盖当前 owner')

const oldRefreshSnapshot = deferred<RouteStrategyListSnapshotResult>()
await state.load({
  scopeKey,
  list: async () => listResult([listItem('route-refresh')]),
  snapshot: async () => oldRefreshSnapshot.promise
})
await state.load({
  scopeKey,
  list: async () => listResult([listItem('route-refresh')]),
  snapshot: async () => snapshotResult([snapshotItem('route-refresh', 8, 9)])
})
await flushPromises()
oldRefreshSnapshot.reject(new Error('old refresh failed'))
await flushPromises()
assertReadyCounts('route-refresh', 8, 9, '同页旧刷新失败不得覆盖较新的成功 snapshot')

const firstASnapshot = deferred<RouteStrategyListSnapshotResult>()
await state.load({
  scopeKey,
  list: async () => listResult([listItem('route-a-return')]),
  snapshot: async () => firstASnapshot.promise
})
await state.load({
  scopeKey,
  list: async () => listResult([listItem('route-b-between')]),
  snapshot: async () => snapshotResult([snapshotItem('route-b-between', 12, 13)])
})
await state.load({
  scopeKey,
  list: async () => listResult([listItem('route-a-return')]),
  snapshot: async () => snapshotResult([snapshotItem('route-a-return', 14, 15)])
})
await flushPromises()
firstASnapshot.resolve(snapshotResult([snapshotItem('route-a-return', 140, 150)]))
await flushPromises()
assertReadyCounts('route-a-return', 14, 15, 'A→B→A 时第一轮 A 的迟到结果不得覆盖新一轮 A')

assert.equal(routeStrategyListFallbackPage({ ...listResult([]), page: 3 }), 2, '删除导致非首页空页时应回退一页')
assert.equal(routeStrategyListFallbackPage({ ...listResult([]), page: 1 }), undefined, '首页空页不得继续回退')
assert.equal(routeStrategyListFallbackPage({ ...listResult([listItem('route-present')]), page: 3 }), undefined, '非空页不得回退')

await assertSnapshotApiScopeContract()

const disposedSnapshot = deferred<RouteStrategyListSnapshotResult>()
await state.load({
  scopeKey,
  list: async () => listResult([listItem('route-disposed')]),
  snapshot: async () => disposedSnapshot.promise
})
state.dispose()
const statesAtDispose = snapshotStates
disposedSnapshot.resolve(snapshotResult([snapshotItem('route-disposed', 10, 11)]))
await flushPromises()
assert.equal(snapshotStates, statesAtDispose, '组件卸载后迟到 snapshot 不得再写状态')
assert.equal(loading, false, '组件卸载必须结束基础 loading')

console.log('策略路由渐进加载前端回归通过：当前页单次 snapshot、动态状态和迟到响应隔离均正确')

function listItem(id: string): RouteStrategyListItem {
  return {
    id,
    name: id,
    mode: 'normal',
    status: 'active',
    isDefault: false,
    normalRoutingConfig: { schedulingPreference: 'cost_first' },
    createdAt: '2026-07-23T00:00:00.000Z',
    updatedAt: '2026-07-23T00:00:00.000Z'
  }
}

function listResult(items: RouteStrategyListItem[]): RouteStrategyListResult {
  return {
    items,
    page: 1,
    pageSize: 20,
    total: items.length,
    hasMore: false
  }
}

function snapshotItem(id: string, bindingCount: number, apiKeyCount: number) {
  return {
    id,
    bindingCount,
    apiKeyCount,
    groupBindingPreview: []
  }
}

function snapshotResult(items: ReturnType<typeof snapshotItem>[]): RouteStrategyListSnapshotResult {
  return {
    generatedAt: '2026-07-23T00:00:00.000Z',
    items
  }
}

function deferred<T>(): Deferred<T> {
  let resolvePromise!: (value: T) => void
  let rejectPromise!: (error: unknown) => void
  const promise = new Promise<T>((resolve, reject) => {
    resolvePromise = resolve
    rejectPromise = reject
  })
  return { promise, resolve: resolvePromise, reject: rejectPromise }
}

function assertReadyCounts(id: string, bindingCount: number, apiKeyCount: number, message: string): void {
  const itemState = snapshotStates.get(id)
  assert.equal(itemState?.status, 'ready', message)
  if (itemState?.status !== 'ready') return
  assert.equal(itemState.item.bindingCount, bindingCount, message)
  assert.equal(itemState.item.apiKeyCount, apiKeyCount, message)
}

async function flushPromises(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

async function assertSnapshotApiScopeContract(): Promise<void> {
  const requests: Array<{ url: string; params: unknown }> = []
  const originalAdapter = http.defaults.adapter
  const adapter: AxiosAdapter = async (config) => {
    requests.push({ url: String(config.url ?? ''), params: config.params })
    return {
      data: { data: snapshotResult([]) },
      status: 200,
      statusText: 'OK',
      headers: {},
      config
    }
  }
  try {
    http.defaults.adapter = adapter
    await routeStrategiesApi.listSnapshot([' route-a ', 'route-b', 'route-a'], { systemAccountId: ' owner-a ' })
    await myRouteStrategiesApi.listSnapshot(['route-self'])
    await useScopedRouteStrategiesApi(ref(false)).listSnapshot(['route-scoped-self'], { systemAccountId: 'must-not-leak' })
  } finally {
    http.defaults.adapter = originalAdapter
  }
  assert.equal(requests.length, 3)
  assert.equal(requests[0]?.url, '/route-strategies/list-snapshot')
  assert(requests[0]?.params instanceof URLSearchParams)
  assert.deepEqual(requests[0].params.getAll('ids'), ['route-a', 'route-b'], '管理 API 必须用重复裸键编码去重 IDs')
  assert.equal(requests[0].params.get('systemAccountId'), 'owner-a', '管理 API 必须保留规范化 owner scope')
  for (const [index, expectedId] of [[1, 'route-self'], [2, 'route-scoped-self']] as const) {
    const request = requests[index]
    assert.equal(request?.url, '/my-route-strategies/list-snapshot')
    assert(request?.params instanceof URLSearchParams)
    assert.deepEqual(request.params.getAll('ids'), [expectedId])
    assert.equal(request.params.has('systemAccountId'), false, '个人 API 不得泄漏调用方传入的 owner scope')
  }
}
