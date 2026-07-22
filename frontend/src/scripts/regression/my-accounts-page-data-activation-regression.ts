import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

import type {
  PageDataConfirmRequest,
  PageDataConfirmResult,
  PageDataDomain,
  PageDataRevisionToken
} from '../../api/domains/pageData'
import {
  PageDataCacheController,
  createMemoryPageDataCacheStorage,
  type PageDataLoadResult
} from '../../shared/pageDataCache'
import type { PageDataActivationParticipant } from '../../shared/pageDataActivationCoordinator'
import { myAccountsPageDataActivationManifest } from '../../shared/pageDataActivationManifests'
import type {
  PageDataActivation,
  PageDataActivationLifecycleTimer
} from '../../composables/usePageDataActivation'

const domWindow = new Window({ url: 'http://127.0.0.1/my-accounts' })
Object.assign(globalThis, {
  window: domWindow,
  document: domWindow.document,
  Node: domWindow.Node,
  Element: domWindow.Element,
  HTMLElement: domWindow.HTMLElement,
  SVGElement: domWindow.SVGElement,
  MutationObserver: domWindow.MutationObserver,
  getComputedStyle: domWindow.getComputedStyle.bind(domWindow)
})

const { usePageDataActivation } = await import('../../composables/usePageDataActivation')
const { KeepAlive, createApp, defineComponent, h, nextTick, ref } = await import('vue')

class FakeClock {
  private nextId = 1
  private readonly tasks = new Map<number, { at: number; callback: () => void }>()
  nowMs = 0

  readonly timer: PageDataActivationLifecycleTimer = {
    setTimeout: (callback, delayMs) => this.schedule(callback, delayMs),
    clearTimeout: (handle) => this.tasks.delete(handle as number)
  }

  delay(delayMs: number): Promise<void> {
    return new Promise((resolve) => { this.schedule(resolve, delayMs) })
  }

  pendingTaskTimes(): number[] {
    return [...this.tasks.values()].map((task) => task.at).sort((left, right) => left - right)
  }

  advanceBy(delayMs: number): Promise<void> {
    return this.advanceTo(this.nowMs + delayMs)
  }

  async advanceTo(targetMs: number): Promise<void> {
    while (true) {
      await flushMicrotasks()
      const due = [...this.tasks.entries()]
        .filter(([, task]) => task.at <= targetMs)
        .sort((left, right) => left[1].at - right[1].at || left[0] - right[0])[0]
      if (!due) break
      const [id, task] = due
      this.nowMs = task.at
      this.tasks.delete(id)
      task.callback()
      await flushMicrotasks()
    }
    this.nowMs = targetMs
    await flushMicrotasks()
  }

  private schedule(callback: () => void, delayMs: number): number {
    const id = this.nextId++
    this.tasks.set(id, {
      at: this.nowMs + Math.max(0, delayMs),
      callback
    })
    return id
  }
}

const token = (domain: PageDataDomain): PageDataRevisionToken => ({
  protocolVersion: 2,
  epoch: 'page-activation-epoch',
  scope: 'page-activation-self',
  domain,
  sequence: 1,
  resetSequence: 0
})

const confirmationFor = (request: PageDataConfirmRequest, nowMs: number): PageDataConfirmResult => ({
  serverTime: new Date(Date.UTC(2026, 6, 22) + nowMs).toISOString(),
  domains: Object.fromEntries(Object.entries(request.domains).map(([domain, known]) => [
    domain,
    { action: known ? 'unchanged' : 'reload', token: known ?? token(domain as PageDataDomain) }
  ]))
})

const clock = new FakeClock()
const requests: PageDataConfirmRequest[] = []
let gatedConfirmation: ReturnType<typeof deferred<PageDataConfirmResult>> | undefined
let focusListener: (() => void) | undefined
let visibilityListener: (() => void) | undefined
let pageVisible = true
let activation: PageDataActivation | undefined
const showActivationPage = ref(true)
const loadResults = new Map<PageDataDomain, PageDataLoadResult<string[]>[]>()
const networkCalls = new Map<PageDataDomain, number>()
const revalidatorCalls = new Map<PageDataDomain, number>()
let isolatedFailureCalls = 0
let unregisterFailure: (() => void) | undefined
const storage = createMemoryPageDataCacheStorage({
  now: () => new Date(Date.UTC(2026, 6, 22) + clock.nowMs)
})
const controllers = new Map<PageDataDomain, PageDataCacheController<string[]>>()

const ActivationPage = defineComponent({
  name: 'ActivationPage',
  setup() {
    activation = usePageDataActivation({
      enabled: true,
      manifest: myAccountsPageDataActivationManifest,
      viewScope: 'self',
      confirm: async (request) => {
        requests.push(request)
        return gatedConfirmation ? gatedConfirmation.promise : confirmationFor(request, clock.nowMs)
      },
      batchWindowMs: 50,
      intervalMs: 30_000,
      timer: clock.timer,
      now: () => clock.nowMs,
      isVisible: () => pageVisible,
      addFocusListener: (listener) => {
        focusListener = listener
        return () => { if (focusListener === listener) focusListener = undefined }
      },
      addVisibilityListener: (listener) => {
        visibilityListener = listener
        return () => { if (visibilityListener === listener) visibilityListener = undefined }
      }
    })
    assert(activation, 'self 视图必须创建页面 activation')

    for (const resource of [
      { domain: 'providers.catalog', route: '/providers/definitions', startDelayMs: 0, networkDelayMs: 5 },
      { domain: 'accounts.options', route: '/my-accounts/tags', startDelayMs: 25, networkDelayMs: 15 },
      { domain: 'accounts.static', route: '/my-accounts', startDelayMs: 45, networkDelayMs: 25 }
    ] as const) {
      const controller = new PageDataCacheController<string[]>({
        cacheKey: { scope: 'self:test-viewer', route: resource.route, query: {}, version: 1 },
        domain: resource.domain,
        viewScope: 'self',
        storage,
        confirm: async (request) => confirmationFor(request, clock.nowMs),
        activation,
        now: () => new Date(Date.UTC(2026, 6, 22) + clock.nowMs),
        maxStaleMs: 30_000,
        loadNetwork: async () => {
          await clock.delay(resource.networkDelayMs)
          const count = (networkCalls.get(resource.domain) ?? 0) + 1
          networkCalls.set(resource.domain, count)
          return [`${resource.domain}:${count}`]
        }
      })
      controllers.set(resource.domain, controller)
      loadResults.set(resource.domain, [])
      activation.registerRevalidator(resource.domain, async () => {
        revalidatorCalls.set(resource.domain, (revalidatorCalls.get(resource.domain) ?? 0) + 1)
        await clock.delay(resource.startDelayMs)
        loadResults.get(resource.domain)?.push(await controller.load())
      })
    }
    unregisterFailure = activation.registerRevalidator('accounts.static', async () => {
      isolatedFailureCalls += 1
      throw new Error('isolated revalidator failure')
    })
    return () => h('div', 'activation page')
  }
})

const IdlePage = defineComponent({ name: 'IdlePage', setup: () => () => h('div', 'idle') })
const Root = defineComponent({
  setup: () => () => h(KeepAlive, null, {
    default: () => showActivationPage.value
      ? h(ActivationPage, { key: 'activation' })
      : h(IdlePage, { key: 'idle' })
  })
})

const container = document.createElement('div')
document.body.append(container)
const app = createApp(Root)
app.mount(container)
await nextTick()
await flushMicrotasks()
await clock.advanceTo(105)
await waitFor(() => (loadResults.get('accounts.static')?.length ?? 0) === 1, '初始账户列表 revalidator 未完成')

assert.equal(isolatedFailureCalls, 1, '单个 revalidator 失败必须隔离')
assert.equal(requests.length, 2, '冷启动必须只有一个 pre 和一个 post confirm')
for (const request of requests) {
  assert.deepEqual(Object.keys(request.domains).sort(), [
    'accounts.options',
    'accounts.static',
    'providers.catalog'
  ])
}
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), 1, '首次 mount 与初始 onActivated 必须去重')
  assert.equal(networkCalls.get(domain), 1, 'changed 资源只能执行一次 GET')
}

assert.deepEqual(clock.pendingTaskTimes(), [30_105], '初始 post-confirm 完成后才能开始 30 秒周期')

unregisterFailure?.()
const callsBeforePublicTrigger = new Map(revalidatorCalls)
const requestsBeforePublicTrigger = requests.length
activation.trigger('focus')
activation.trigger('interval')
await clock.advanceBy(100)
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), (callsBeforePublicTrigger.get(domain) ?? 0) + 1, '公开 trigger 必须走全页 revalidate')
}
assert.equal(requests.length, requestsBeforePublicTrigger, '热缓存 trigger 不应额外 confirm')
assert.deepEqual(clock.pendingTaskTimes(), [30_105], '普通 focus revalidation 不得推迟已有周期')

showActivationPage.value = false
await nextTick()
const callsWhileDeactivated = new Map(revalidatorCalls)
const requestsWhileDeactivated = requests.length
pageVisible = false
visibilityListener?.()
focusListener?.()
pageVisible = true
visibilityListener?.()
focusListener?.()
activation.trigger('focus')
await clock.advanceBy(100)
assert.equal(requests.length, requestsWhileDeactivated, 'KeepAlive deactivated 期间 focus/visibility/trigger 不得 confirm')
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), callsWhileDeactivated.get(domain), 'KeepAlive deactivated 期间 focus/visibility/trigger 不得 revalidate')
}
assert.deepEqual(clock.pendingTaskTimes(), [], 'KeepAlive deactivated 必须清理周期 timeout')

const callsBeforeActivate = new Map(revalidatorCalls)
showActivationPage.value = true
await nextTick()
focusListener?.()
await clock.advanceBy(100)
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), (callsBeforeActivate.get(domain) ?? 0) + 1, 'activate 与紧邻 focus 必须 singleflight')
}
assert.equal(isolatedFailureCalls, 1, '已注销 revalidator 不得再次执行')

const activeIntervalAt = clock.pendingTaskTimes()[0]
assert(activeIntervalAt, '重新激活后必须安排周期 timeout')
const callsBeforeDocumentPause = new Map(revalidatorCalls)
const requestsBeforeDocumentPause = requests.length
pageVisible = false
visibilityListener?.()
focusListener?.()
activation.trigger('focus')
await clock.advanceBy(100)
assert.equal(requests.length, requestsBeforeDocumentPause, 'document hidden 期间 focus/trigger 不得 confirm')
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), callsBeforeDocumentPause.get(domain), 'document hidden 期间 focus/trigger 不得 revalidate')
}
assert.deepEqual(clock.pendingTaskTimes(), [], 'document hidden 必须暂停周期 timeout')
pageVisible = true
visibilityListener?.()
focusListener?.()
await clock.advanceBy(100)
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), (callsBeforeDocumentPause.get(domain) ?? 0) + 1, 'document visible 且 componentActive 时必须恢复 revalidate')
}
assert(clock.pendingTaskTimes()[0] > activeIntervalAt, 'document visible 恢复后必须重新安排周期 timeout')

const callsBeforeFocus = new Map(revalidatorCalls)
const intervalBeforeFocus = clock.pendingTaskTimes()[0]
focusListener?.()
await clock.advanceBy(100)
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), (callsBeforeFocus.get(domain) ?? 0) + 1, 'active 期间 focus 必须执行一次 revalidate')
}
assert.deepEqual(clock.pendingTaskTimes(), [intervalBeforeFocus], 'active 期间 focus 不得重排已有周期 timeout')

const resumedRevalidatorGate = deferred<void>()
let resumedRevalidatorCalls = 0
const unregisterResumedRevalidator = activation.registerRevalidator('accounts.static', () => {
  resumedRevalidatorCalls += 1
  if (resumedRevalidatorCalls === 1) return resumedRevalidatorGate.promise
})
const callsBeforeResumedRevalidation = new Map(revalidatorCalls)
activation.trigger('focus')
await clock.advanceBy(100)
assert.equal(resumedRevalidatorCalls, 1, '竞态准备必须保持一个旧 revalidator 挂起')
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), (callsBeforeResumedRevalidation.get(domain) ?? 0) + 1, '旧 activation 必须只启动一次 revalidate')
}

showActivationPage.value = false
await nextTick()
showActivationPage.value = true
await nextTick()
visibilityListener?.()
focusListener?.()
await flushMicrotasks()
assert.equal(resumedRevalidatorCalls, 1, '旧任务完成前 activate/visibility/focus 必须合并为一个 pending resume')

const resumedRevalidationStartedAt = clock.nowMs
resumedRevalidatorGate.resolve(undefined)
await flushMicrotasks()
assert(
  clock.pendingTaskTimes().every((taskAt) => taskAt < resumedRevalidationStartedAt + 30_000),
  '旧 operation finally 必须先补跑 pending resume，不得先安排周期 timeout'
)
await clock.advanceBy(100)
assert.equal(resumedRevalidatorCalls, 2, '旧任务完成后必须恰好补跑一次 resumed revalidation')
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), (callsBeforeResumedRevalidation.get(domain) ?? 0) + 2, '恢复后的新 activation 必须补跑且只补跑一次')
}
assert.equal(clock.pendingTaskTimes().length, 1, 'pending resume 完成后才能安排一个周期 timeout')
assert(clock.pendingTaskTimes()[0] > resumedRevalidationStartedAt + 30_000, '周期必须从 resumed revalidation 完成后重新计时')
unregisterResumedRevalidator()

const pausedRevalidatorGate = deferred<void>()
let pausedRevalidatorCalls = 0
const unregisterPausedRevalidator = activation.registerRevalidator('accounts.static', () => {
  pausedRevalidatorCalls += 1
  if (pausedRevalidatorCalls === 1) return pausedRevalidatorGate.promise
})
const callsBeforePauseOnly = new Map(revalidatorCalls)
activation.trigger('focus')
await clock.advanceBy(100)
showActivationPage.value = false
await nextTick()
pausedRevalidatorGate.resolve(undefined)
await flushMicrotasks()
await clock.advanceBy(100)
assert.equal(pausedRevalidatorCalls, 1, '旧任务结束前未 resume 时不得补跑 revalidation')
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), (callsBeforePauseOnly.get(domain) ?? 0) + 1, '暂停且未恢复时必须只保留旧 activation 的一次执行')
}
assert.deepEqual(clock.pendingTaskTimes(), [], '暂停且未恢复时旧任务 finally 不得安排周期 timeout')
unregisterPausedRevalidator()
showActivationPage.value = true
await nextTick()
await clock.advanceBy(100)

const callsBeforePublicDeactivate = new Map(revalidatorCalls)
const requestsBeforePublicDeactivate = requests.length
activation.deactivate()
pageVisible = false
visibilityListener?.()
focusListener?.()
pageVisible = true
visibilityListener?.()
focusListener?.()
activation.trigger('focus')
await clock.advanceBy(100)
assert.equal(requests.length, requestsBeforePublicDeactivate, '公开 deactivate 后 focus/visibility/trigger 不得 confirm')
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), callsBeforePublicDeactivate.get(domain), '公开 deactivate 后 focus/visibility/trigger 不得 revalidate')
}
assert.deepEqual(clock.pendingTaskTimes(), [], '公开 deactivate 必须清理周期 timeout')

showActivationPage.value = false
await nextTick()
const callsBeforeReactivated = new Map(revalidatorCalls)
showActivationPage.value = true
await nextTick()
await clock.advanceBy(100)
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), (callsBeforeReactivated.get(domain) ?? 0) + 1, '公开 deactivate 后只有 onActivated 可恢复 revalidate')
}

const intervalAt = clock.pendingTaskTimes()[0]
assert(intervalAt, '再次激活后必须安排周期 timeout')
const requestsBeforeInterval = requests.length
gatedConfirmation = deferred<PageDataConfirmResult>()
await clock.advanceTo(intervalAt + 50)
assert.equal(requests.length, requestsBeforeInterval + 1, '周期点必须只有一个 aggregate confirm')
assert.deepEqual(Object.keys(requests.at(-1)?.domains ?? {}).sort(), [
  'accounts.options',
  'accounts.static',
  'providers.catalog'
])
assert.deepEqual(clock.pendingTaskTimes(), [], 'interval callback 必须先清除 handle，confirm 完成前不重排')
await clock.advanceTo(intervalAt + 5_000)
const intervalRequest = requests.at(-1)
assert(intervalRequest)
gatedConfirmation.resolve(confirmationFor(intervalRequest, clock.nowMs))
gatedConfirmation = undefined
await waitFor(() => clock.pendingTaskTimes().length === 1, '周期 confirm 完成后未重排 timeout')
assert.deepEqual(clock.pendingTaskTimes(), [intervalAt + 35_000], '下一周期必须从 confirm 完成后重新计时 30 秒')

const providerNetworkBeforeTargeted = networkCalls.get('providers.catalog') ?? 0
const tagNetworkBeforeTargeted = networkCalls.get('accounts.options') ?? 0
const providerRunsBeforeTargeted = revalidatorCalls.get('providers.catalog') ?? 0
const tagRunsBeforeTargeted = revalidatorCalls.get('accounts.options') ?? 0
const accountRunsBeforeTargeted = revalidatorCalls.get('accounts.static') ?? 0
activation.beginTargeted(['accounts.static'])
const targetedRefresh = controllers.get('accounts.static')?.refresh()
assert(targetedRefresh)
await clock.advanceBy(100)
assert.equal((await targetedRefresh).superseded, false)
assert.equal(networkCalls.get('accounts.static'), 2)
assert.equal(networkCalls.get('providers.catalog'), providerNetworkBeforeTargeted)
assert.equal(networkCalls.get('accounts.options'), tagNetworkBeforeTargeted)
assert.equal(revalidatorCalls.get('providers.catalog'), providerRunsBeforeTargeted)
assert.equal(revalidatorCalls.get('accounts.options'), tagRunsBeforeTargeted)
assert.equal(revalidatorCalls.get('accounts.static'), accountRunsBeforeTargeted, 'beginTargeted 不得运行全页 revalidator')
assert.deepEqual(Object.keys(requests.at(-1)?.domains ?? {}), ['accounts.static'])

gatedConfirmation = deferred<PageDataConfirmResult>()
activation.beginTargeted(['accounts.static'])
const lateParticipant: PageDataActivationParticipant = {
  resourceKey: 'late-account-list',
  domain: 'accounts.static',
  token: token('accounts.static'),
  generation: 99,
  writeEpoch: 0
}
const lateDecision = activation.register(lateParticipant)
await clock.advanceBy(50)
activation.deactivate()
assert.equal((await lateDecision).state, 'superseded', 'deactivate 必须 supersede 迟到结果')
gatedConfirmation.resolve(confirmationFor(requests.at(-1)!, clock.nowMs))
gatedConfirmation = undefined
await flushMicrotasks()

app.unmount()
for (const controller of controllers.values()) controller.close()

let disabledActivation: PageDataActivation | undefined
const disabledApp = createApp(defineComponent({
  setup() {
    disabledActivation = usePageDataActivation({
      enabled: false,
      manifest: myAccountsPageDataActivationManifest,
      viewScope: 'self',
      confirm: async (request) => confirmationFor(request, clock.nowMs)
    })
    return () => h('div')
  }
}))
const disabledContainer = document.createElement('div')
document.body.append(disabledContainer)
disabledApp.mount(disabledContainer)
await nextTick()
assert.equal(disabledActivation, undefined, '管理视图不得创建页面 activation')
disabledApp.unmount()

const accountsViewSource = readFileSync(fileURLToPath(new URL('../../views/accounts/AccountsView.vue', import.meta.url)), 'utf8')
const accountListDataSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountListData.ts', import.meta.url)), 'utf8')
const tagCacheSource = readFileSync(fileURLToPath(new URL('../../views/accounts/accountTagOptionsCache.ts', import.meta.url)), 'utf8')
const providerSource = readFileSync(fileURLToPath(new URL('../../composables/useProviderOptionsResource.ts', import.meta.url)), 'utf8')
const activationSource = readFileSync(fileURLToPath(new URL('../../composables/usePageDataActivation.ts', import.meta.url)), 'utf8')
assert.match(accountsViewSource, /usePageDataActivation/)
assert.match(accountsViewSource, /accounts\.static[\s\S]*accounts\.options[\s\S]*providers\.catalog/)
assert.match(accountListDataSource, /activationManaged:\s*Boolean\(/)
assert.match(accountListDataSource, /activation:\s*options\.pageDataActivation/)
assert.match(tagCacheSource, /activation:\s*input\.activation/)
assert.match(providerSource, /activation:\s*options\.activation/)
assert.doesNotMatch(activationSource, /setInterval|clearInterval/, '页面 activation 周期必须使用一次性 timeout')
assert.doesNotMatch(activationSource, /createCoordinatorTimer|setTimeout\(\(\) => undefined/, '生产代码不得使用空 timeout 辅助测试 flush')

console.log('我的账户页面数据 activation 回归通过')

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

async function flushMicrotasks(): Promise<void> {
  for (let turn = 0; turn < 10; turn += 1) await Promise.resolve()
}

async function waitFor(predicate: () => boolean, message: string): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (predicate()) return
    await flushMicrotasks()
  }
  throw new Error(message)
}
