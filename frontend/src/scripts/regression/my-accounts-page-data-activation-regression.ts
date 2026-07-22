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
import type { PageDataActivationParticipant, PageDataActivationTimer } from '../../shared/pageDataActivationCoordinator'
import { myAccountsPageDataActivationManifest } from '../../shared/pageDataActivationManifests'
import {
  usePageDataActivation,
  type PageDataActivation,
  type PageDataActivationLifecycleTimer
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

const { KeepAlive, createApp, defineComponent, h, nextTick, ref } = await import('vue')

class FakeClock {
  private nextId = 1
  private readonly tasks = new Map<number, { at: number; callback: () => void; intervalMs?: number }>()
  nowMs = 0

  readonly timer: PageDataActivationTimer & PageDataActivationLifecycleTimer = {
    setTimeout: (callback, delayMs) => this.schedule(callback, delayMs),
    clearTimeout: (handle) => this.tasks.delete(handle as number),
    setInterval: (callback, intervalMs) => this.schedule(callback, intervalMs, Math.max(1, intervalMs)),
    clearInterval: (handle) => this.tasks.delete(handle as number)
  }

  delay(delayMs: number): Promise<void> {
    return new Promise((resolve) => { this.schedule(resolve, delayMs) })
  }

  async advanceTo(targetMs: number): Promise<void> {
    while (true) {
      const due = [...this.tasks.entries()]
        .filter(([, task]) => task.at <= targetMs)
        .sort((left, right) => left[1].at - right[1].at || left[0] - right[0])[0]
      if (!due) break
      const [id, task] = due
      this.nowMs = task.at
      if (task.intervalMs) task.at += task.intervalMs
      else this.tasks.delete(id)
      task.callback()
      await flushMicrotasks()
    }
    this.nowMs = targetMs
    await flushMicrotasks()
  }

  private schedule(callback: () => void, delayMs: number, intervalMs?: number): number {
    const id = this.nextId++
    this.tasks.set(id, {
      at: this.nowMs + Math.max(0, delayMs),
      callback,
      ...(intervalMs ? { intervalMs } : {})
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

unregisterFailure?.()
pageVisible = false
visibilityListener?.()
await clock.advanceTo(20_000)
const callsBeforeResume = new Map(revalidatorCalls)
pageVisible = true
visibilityListener?.()
focusListener?.()
await flushMicrotasks()
await clock.advanceTo(20_100)
assert.equal(requests.length, 2, '30 秒内恢复必须直接使用热缓存')
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), (callsBeforeResume.get(domain) ?? 0) + 1, 'focus 与 visibility 必须去重')
}
assert.equal(isolatedFailureCalls, 1, '已注销 revalidator 不得再次执行')

await clock.advanceTo(80_100)
assert.equal(requests.length, 3, '周期点最多一个 aggregate confirm')
assert.deepEqual(Object.keys(requests[2]?.domains ?? {}).sort(), [
  'accounts.options',
  'accounts.static',
  'providers.catalog'
])
for (const domain of myAccountsPageDataActivationManifest.domains) assert.equal(networkCalls.get(domain), 1)

showActivationPage.value = false
await nextTick()
const requestsBeforeDeactivateWait = requests.length
await clock.advanceTo(120_100)
assert.equal(requests.length, requestsBeforeDeactivateWait, 'KeepAlive deactivated 不得周期 confirm')

const callsBeforeActivate = new Map(revalidatorCalls)
showActivationPage.value = true
await nextTick()
focusListener?.()
await flushMicrotasks()
await clock.advanceTo(120_200)
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(revalidatorCalls.get(domain), (callsBeforeActivate.get(domain) ?? 0) + 1, 'activate 与紧邻 focus 必须 singleflight')
}

const providerNetworkBeforeTargeted = networkCalls.get('providers.catalog') ?? 0
const tagNetworkBeforeTargeted = networkCalls.get('accounts.options') ?? 0
const providerRunsBeforeTargeted = revalidatorCalls.get('providers.catalog') ?? 0
const tagRunsBeforeTargeted = revalidatorCalls.get('accounts.options') ?? 0
activation.beginTargeted(['accounts.static'])
const targetedRefresh = controllers.get('accounts.static')?.refresh()
assert(targetedRefresh)
await clock.advanceTo(120_300)
assert.equal((await targetedRefresh).superseded, false)
assert.equal(networkCalls.get('accounts.static'), 2)
assert.equal(networkCalls.get('providers.catalog'), providerNetworkBeforeTargeted)
assert.equal(networkCalls.get('accounts.options'), tagNetworkBeforeTargeted)
assert.equal(revalidatorCalls.get('providers.catalog'), providerRunsBeforeTargeted)
assert.equal(revalidatorCalls.get('accounts.options'), tagRunsBeforeTargeted)
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
await clock.advanceTo(120_350)
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
assert.match(accountsViewSource, /usePageDataActivation/)
assert.match(accountsViewSource, /accounts\.static[\s\S]*accounts\.options[\s\S]*providers\.catalog/)
assert.match(accountListDataSource, /activationManaged:\s*Boolean\(/)
assert.match(accountListDataSource, /activation:\s*options\.pageDataActivation/)
assert.match(tagCacheSource, /activation:\s*input\.activation/)
assert.match(providerSource, /activation:\s*options\.activation/)

console.log('我的账户页面数据 activation 回归通过')

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

async function flushMicrotasks(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
}

async function waitFor(predicate: () => boolean, message: string): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (predicate()) return
    await flushMicrotasks()
  }
  throw new Error(message)
}
