import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { computed, createSSRApp, h, nextTick, ref, type ComputedRef, type Ref } from 'vue'
import { renderToString } from 'vue/server-renderer'

import { api, type ListParams } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { clearUsageStatsWindowCache, useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import type { ProviderModelOption, UsageStatsWindow } from '@/types/domain'
import { useUsageRecordModelOptions } from '@/views/usage-records/useUsageRecordModelOptions'

type UsageRecordModelOptionsHarness = {
  handleDropdown: (open: boolean) => Promise<void> | void
  handleSearch: (value: string) => Promise<void> | void
  modelOptions: Ref<ProviderModelOption[]>
  modelOptionsLoading: Ref<boolean>
}

type UsageRecordModelOptionsFactory = (options: {
  scopeParams: ComputedRef<ListParams | undefined>
  selectedModel: ComputedRef<string>
}) => UsageRecordModelOptionsHarness

type UsageStatsWindowHarness = {
  loadUsageStatsWindow: (options?: { force?: boolean; viewScope?: 'admin' | 'self' }) => Promise<UsageStatsWindow>
}

const createModelOptions = useUsageRecordModelOptions as unknown as UsageRecordModelOptionsFactory
const originalModelOptions = api.providers.modelOptions
const originalAdminUsageWindow = api.stats.usageWindow
const originalSelfUsageWindow = api.myStats.usageWindow
const usageRecordsViewSource = readFileSync(new URL('../../views/usage-records/UsageRecordsView.vue', import.meta.url), 'utf8')

try {
  await verifyDesktopManagementScopeAndCache()
  await verifyMobileSearchDeduplication()
  await verifyScopeSwitchAndLateResponseIsolation()
  await verifyRevisitedQueryRequestsCurrentData()
  await verifyUsageWindowAdminTtlAndInflightCache()
  await verifyUsageWindowScopeIsolationAndForceRace()
  assert.match(
    usageRecordsViewSource,
    /loadUsageStatsWindow\(\{ viewScope: isManagementView\.value \? 'admin' : 'self' \}\)/,
    '使用记录管理页和个人页都必须复用对应作用域的 usage-window 时区'
  )
  assert.doesNotMatch(
    usageRecordsViewSource,
    /if \(!isManagementView\.value\) return[\s\S]{0,160}loadUsageStatsWindow/,
    '个人使用记录页不得跳过 usage-window 而退回浏览器时区'
  )
  console.log('使用记录模型候选按需加载行为回归通过：桌面、移动、作用域、并发、竞态和时区缓存均符合契约')
} finally {
  api.providers.modelOptions = originalModelOptions
  api.stats.usageWindow = originalAdminUsageWindow
  api.myStats.usageWindow = originalSelfUsageWindow
  authState.currentUser.value = undefined
  clearUsageStatsWindowCache()
}

async function verifyDesktopManagementScopeAndCache(): Promise<void> {
  authState.currentUser.value = currentUser('usage-desktop-admin')
  const scopeParams = ref<ListParams | undefined>({ systemAccountId: 'owner-desktop-a' })
  const calls: Array<Record<string, unknown> | undefined> = []
  api.providers.modelOptions = async (params) => {
    calls.push(params as Record<string, unknown> | undefined)
    return [modelOption('desktop-model', 'openai'), modelOption('desktop-model', 'anthropic')]
  }

  const harness = await createModelHarness(computed(() => scopeParams.value))
  assert.equal(typeof harness.handleDropdown, 'function', '页面模型候选 composable 必须提供下拉按需加载处理器')
  assert.equal(typeof harness.handleSearch, 'function', '页面模型候选 composable 必须提供搜索按需加载处理器')

  await harness.handleDropdown(false)
  assert.equal(calls.length, 0, '桌面下拉关闭不得加载模型候选')
  await harness.handleDropdown(true)
  assert.equal(calls.length, 1, '桌面首次打开模型下拉必须只请求一次')
  assert.equal(harness.modelOptions.value.length, 1, '跨供应商同一模型在 usage 下拉中必须聚合为一个 id/name 选项')
  assert.equal(calls[0]?.systemAccountId, 'owner-desktop-a', '管理端模型候选必须携带所选系统账户作用域')
  assert.deepEqual(calls[0]?.selectedIds, ['selected-model'], '模型候选必须补齐当前已选模型')

  const firstSearch = harness.handleSearch('d')
  const secondSearch = harness.handleSearch('de')
  const thirdSearch = harness.handleSearch('desktop')
  await Promise.all([firstSearch, secondSearch, thirdSearch])
  assert.equal(calls.length, 2, '连续搜索按键必须防抖为最后一次关键词请求')
  assert.equal(calls[1]?.keyword, 'desktop', '防抖搜索必须使用最后一次关键词')
  await harness.handleSearch('desktop')
  assert.equal(calls.length, 3, '已完成关键词的重复搜索必须重新读取当前业务数据')
}

async function verifyMobileSearchDeduplication(): Promise<void> {
  authState.currentUser.value = currentUser('usage-mobile-admin')
  const scopeParams = ref<ListParams | undefined>({ systemAccountId: 'owner-mobile-a' })
  const pending = deferred<ProviderModelOption[]>()
  let calls = 0
  api.providers.modelOptions = async (params) => {
    calls += 1
    assert.equal(params?.systemAccountId, 'owner-mobile-a')
    return await pending.promise
  }
  const harness = await createModelHarness(computed(() => scopeParams.value))

  const first = harness.handleSearch('m')
  const second = harness.handleSearch('mo')
  const third = harness.handleSearch('mobile')
  await waitFor(() => calls > 0, '移动端模型候选请求未启动')
  assert.equal(calls, 1, '移动端首次搜索期间的连续按键必须复用同一个进行中请求')
  pending.resolve([modelOption('mobile-model')])
  await Promise.all([first, second, third])
  await harness.handleDropdown(true)
  assert.equal(calls, 2, '移动端搜索完成后打开下拉应仅补一次空关键词基础窗口')
  await harness.handleDropdown(true)
  assert.equal(calls, 3, '移动端基础窗口重复打开必须重新读取当前业务数据')
}

async function verifyScopeSwitchAndLateResponseIsolation(): Promise<void> {
  authState.currentUser.value = currentUser('usage-race-admin')
  const scopeParams = ref<ListParams | undefined>({ systemAccountId: 'owner-race-a' })
  const firstPending = deferred<ProviderModelOption[]>()
  const calls: string[] = []
  api.providers.modelOptions = async (params) => {
    const owner = params?.systemAccountId ?? 'self'
    calls.push(owner)
    if (owner === 'owner-race-a') return await firstPending.promise
    return [modelOption('race-model-b')]
  }
  const harness = await createModelHarness(computed(() => scopeParams.value))

  const firstLoad = harness.handleDropdown(true)
  await waitFor(() => calls.length === 1, '旧作用域模型请求未启动')
  scopeParams.value = { systemAccountId: 'owner-race-b' }
  await nextTick()
  assert.deepEqual(harness.modelOptions.value, [], '切换系统账户作用域必须立即清空旧候选')
  await harness.handleDropdown(true)
  assert.deepEqual(calls, ['owner-race-a', 'owner-race-b'], '切换作用域后必须以新系统账户重新加载')
  assert.deepEqual(harness.modelOptions.value.map((item) => item.id), ['race-model-b'])

  firstPending.resolve([modelOption('race-model-a-late')])
  await firstLoad
  await Promise.resolve()
  assert.deepEqual(
    harness.modelOptions.value.map((item) => item.id),
    ['race-model-b'],
    '旧作用域迟到响应不得覆盖新作用域候选'
  )
}

async function verifyRevisitedQueryRequestsCurrentData(): Promise<void> {
  authState.currentUser.value = currentUser('usage-direct-admin')
  const scopeParams = ref<ListParams | undefined>({ systemAccountId: 'owner-direct-a' })
  let networkCalls = 0
  api.providers.modelOptions = async (params) => {
    networkCalls += 1
    const keyword = typeof params?.keyword === 'string' ? params.keyword : 'base'
    return [modelOption(`direct-${keyword}-${networkCalls}`)]
  }
  const harness = await createModelHarness(computed(() => scopeParams.value))

  await harness.handleSearch('alpha')
  await harness.handleSearch('beta')
  await harness.handleSearch('alpha')
  assert.equal(networkCalls, 3, '重新访问旧关键词必须直接读取当前业务数据，不能复用已退场的页面快照')
  assert.deepEqual(harness.modelOptions.value.map((item) => item.id), ['direct-alpha-3'])
}

async function verifyUsageWindowAdminTtlAndInflightCache(): Promise<void> {
  clearUsageStatsWindowCache()
  const pending = deferred<UsageStatsWindow>()
  let adminCalls = 0
  let selfCalls = 0
  api.stats.usageWindow = async () => {
    adminCalls += 1
    return await pending.promise
  }
  api.myStats.usageWindow = async () => {
    selfCalls += 1
    return usageWindow('UTC')
  }
  const harness = useUsageStatsWindow() as UsageStatsWindowHarness

  const first = harness.loadUsageStatsWindow({ viewScope: 'admin' })
  const second = harness.loadUsageStatsWindow({ viewScope: 'admin' })
  await Promise.resolve()
  assert.equal(adminCalls, 1, '管理端时区并发请求必须复用同一个 usage-window in-flight promise')
  assert.equal(selfCalls, 0, '管理端时区不得调用 my-stats usage-window')
  pending.resolve(usageWindow('Asia/Shanghai'))
  const [firstResult, secondResult] = await Promise.all([first, second])
  assert.equal(firstResult.timezone, 'Asia/Shanghai')
  assert.equal(secondResult.timezone, 'Asia/Shanghai')

  const cached = await harness.loadUsageStatsWindow({ viewScope: 'admin' })
  assert.equal(cached.timezone, 'Asia/Shanghai')
  assert.equal(adminCalls, 1, 'TTL 内重复读取管理端时区不得再次请求')
}

async function verifyUsageWindowScopeIsolationAndForceRace(): Promise<void> {
  clearUsageStatsWindowCache()
  let selfCalls = 0
  let adminCalls = 0
  api.myStats.usageWindow = async () => {
    selfCalls += 1
    return usageWindow('UTC')
  }
  api.stats.usageWindow = async () => {
    adminCalls += 1
    return usageWindow('Asia/Shanghai')
  }
  const scopedHarness = useUsageStatsWindow() as UsageStatsWindowHarness & { usageStatsWindow: Ref<UsageStatsWindow | undefined> }
  await scopedHarness.loadUsageStatsWindow({ viewScope: 'self' })
  const adminWindow = await scopedHarness.loadUsageStatsWindow({ viewScope: 'admin' })
  assert.equal(selfCalls, 1)
  assert.equal(adminCalls, 1, 'self 的 TTL 缓存不得阻止管理端读取 admin usage-window')
  assert.equal(adminWindow.timezone, 'Asia/Shanghai')

  clearUsageStatsWindowCache()
  const oldPending = deferred<UsageStatsWindow>()
  const forcedPending = deferred<UsageStatsWindow>()
  adminCalls = 0
  api.stats.usageWindow = async () => {
    adminCalls += 1
    return await (adminCalls === 1 ? oldPending.promise : forcedPending.promise)
  }
  const raceHarness = useUsageStatsWindow() as UsageStatsWindowHarness & { usageStatsWindow: Ref<UsageStatsWindow | undefined> }
  const oldRequest = raceHarness.loadUsageStatsWindow({ viewScope: 'admin' })
  const forcedRequest = raceHarness.loadUsageStatsWindow({ viewScope: 'admin', force: true })
  forcedPending.resolve(usageWindow('Asia/Shanghai'))
  await forcedRequest
  oldPending.resolve(usageWindow('UTC'))
  await oldRequest
  assert.equal(raceHarness.usageStatsWindow.value?.timezone, 'Asia/Shanghai', '旧 usage-window 请求晚到不得覆盖较新的 force 结果')

  clearUsageStatsWindowCache()
  adminCalls = 0
  const refreshPending = deferred<UsageStatsWindow>()
  api.stats.usageWindow = async () => {
    adminCalls += 1
    if (adminCalls === 1) return usageWindow('UTC')
    return await refreshPending.promise
  }
  await raceHarness.loadUsageStatsWindow({ viewScope: 'admin' })
  const refreshRequest = raceHarness.loadUsageStatsWindow({ viewScope: 'admin', force: true })
  const joinedRequest = raceHarness.loadUsageStatsWindow({ viewScope: 'admin' })
  refreshPending.resolve(usageWindow('Asia/Shanghai'))
  const joinedWindow = await joinedRequest
  await refreshRequest
  assert.equal(adminCalls, 2, 'force 进行中时普通读取必须复用刷新请求，不能命中旧 TTL 缓存')
  assert.equal(joinedWindow.timezone, 'Asia/Shanghai')
  assert.equal(raceHarness.usageStatsWindow.value?.timezone, 'Asia/Shanghai', 'force 刷新期间的普通读取不得让共享窗口停留在旧值')
}

async function createModelHarness(scopeParams: ComputedRef<ListParams | undefined>): Promise<UsageRecordModelOptionsHarness> {
  let harness: UsageRecordModelOptionsHarness | undefined
  const app = createSSRApp({
    setup() {
      harness = createModelOptions({
        scopeParams,
        selectedModel: computed(() => 'selected-model')
      })
      return () => h('div')
    }
  })
  await renderToString(app)
  assert(harness, '使用记录模型候选 composable 未初始化')
  return harness
}

function modelOption(model: string, providerCode = 'openai'): ProviderModelOption {
  return {
    id: model,
    name: model,
    providerCode
  }
}

function usageWindow(timezone: string): UsageStatsWindow {
  return {
    timezone,
    startDate: '2026-06-20',
    endDate: '2026-07-20',
    days: 31,
    maxDays: 31
  }
}

function currentUser(id: string) {
  return {
    id,
    username: id,
    displayName: id,
    role: 'admin' as const,
    mustChangePassword: false
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

async function waitFor(predicate: () => boolean, message: string): Promise<void> {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    if (predicate()) return
    await new Promise((resolve) => setTimeout(resolve, 10))
  }
  throw new Error(message)
}
