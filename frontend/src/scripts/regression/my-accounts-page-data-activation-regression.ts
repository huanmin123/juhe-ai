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
import type {
  PageDataActivationHandle,
  PageDataActivationParticipant
} from '../../shared/pageDataActivationCoordinator'
import { myAccountsPageDataActivationManifest } from '../../shared/pageDataActivationManifests'
import {
  advancePageDataSessionGeneration,
  currentPageDataSecurityGeneration
} from '../../shared/pageDataGenerationFences'
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
  history: domWindow.history,
  location: domWindow.location,
  getComputedStyle: domWindow.getComputedStyle.bind(domWindow)
})

const { usePageDataActivation } = await import('../../composables/usePageDataActivation')
const { KeepAlive, computed, createApp, defineComponent, h, nextTick, reactive, ref } = await import('vue')

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
const revalidationTimeoutMs = 10_000
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
      revalidationTimeoutMs,
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
assert.deepEqual(
  clock.pendingTaskTimes(),
  [intervalAt + revalidationTimeoutMs],
  'interval callback 必须先清除周期 handle，confirm 完成前只保留 revalidation deadline'
)
await clock.advanceTo(intervalAt + 5_000)
const intervalRequest = requests.at(-1)
assert(intervalRequest)
gatedConfirmation.resolve(confirmationFor(intervalRequest, clock.nowMs))
gatedConfirmation = undefined
await waitFor(
  () => clock.pendingTaskTimes().length === 1 && clock.pendingTaskTimes()[0] === intervalAt + 35_000,
  '周期 confirm 完成后未从完成时间重排 timeout'
)
assert.deepEqual(clock.pendingTaskTimes(), [intervalAt + 35_000], '下一周期必须从 confirm 完成后重新计时 30 秒')

const neverSettles = new Promise<void>(() => undefined)
const deadlineRegistrationGate = deferred<void>()
let deadlineRevalidatorCalls = 0
let deadlineLateDecision: Promise<ReturnType<PageDataActivation['register']> extends Promise<infer T> ? T : never> | undefined
const deadlineParticipant: PageDataActivationParticipant = {
  resourceKey: 'deadline-account-list',
  domain: 'accounts.static',
  token: token('accounts.static'),
  generation: 98,
  writeEpoch: 0
}
const unregisterDeadlineRevalidator = activation.registerRevalidator('accounts.static', async (scopedActivation) => {
  deadlineRevalidatorCalls += 1
  if (deadlineRevalidatorCalls !== 1) return
  await deadlineRegistrationGate.promise
  deadlineLateDecision = scopedActivation.register(deadlineParticipant)
  await neverSettles
})
const deadlineStartedAt = clock.nowMs
activation.trigger('focus')
await clock.advanceBy(50)
assert.equal(deadlineRevalidatorCalls, 1, 'deadline 竞态准备必须启动永久 pending revalidator')
assert.equal(deadlineLateDecision, undefined, '旧 revalidator 必须在 deadline 前尚未登记 participant')
await clock.advanceTo(deadlineStartedAt + revalidationTimeoutMs - 1)
activation.trigger('focus')
await flushMicrotasks()
assert.equal(deadlineRevalidatorCalls, 1, 'deadline 前新 trigger 不得并发启动 full revalidation')
await clock.advanceTo(deadlineStartedAt + revalidationTimeoutMs)
activation.trigger('focus')
await flushMicrotasks()
assert.equal(deadlineRevalidatorCalls, 2, 'deadline 释放 full 锁后下一次 trigger 必须可运行')
deadlineRegistrationGate.resolve(undefined)
await flushMicrotasks()
assert(deadlineLateDecision, '旧 revalidator 释放后必须尝试登记 participant')
assert.equal((await deadlineLateDecision).state, 'superseded', '旧 scoped handle 不得加入 deadline 后的新 activation')
await clock.advanceBy(100)
unregisterDeadlineRevalidator()

const providerNetworkBeforeTargeted = networkCalls.get('providers.catalog') ?? 0
const tagNetworkBeforeTargeted = networkCalls.get('accounts.options') ?? 0
const providerRunsBeforeTargeted = revalidatorCalls.get('providers.catalog') ?? 0
const tagRunsBeforeTargeted = revalidatorCalls.get('accounts.options') ?? 0
const accountRunsBeforeTargeted = revalidatorCalls.get('accounts.static') ?? 0
const fullRevalidatorGate = deferred<void>()
let fullRevalidatorCalls = 0
const unregisterFullRevalidator = activation.registerRevalidator('accounts.static', () => {
  fullRevalidatorCalls += 1
  if (fullRevalidatorCalls === 1) return fullRevalidatorGate.promise
})
activation.trigger('focus')
await clock.advanceBy(100)
assert.equal(fullRevalidatorCalls, 1, 'targeted 竞态准备必须保持一个 full revalidator 挂起')
const providerRunsAfterFullStarted = revalidatorCalls.get('providers.catalog') ?? 0
const tagRunsAfterFullStarted = revalidatorCalls.get('accounts.options') ?? 0
const accountRunsAfterFullStarted = revalidatorCalls.get('accounts.static') ?? 0
let targetedBusinessCalls = 0
const targetedRunGate = deferred<void>()
const targetedRefresh = activation.runTargeted(['accounts.static'], async () => {
  targetedBusinessCalls += 1
  await targetedRunGate.promise
  const controller = controllers.get('accounts.static')
  assert(controller)
  return controller.refresh()
})
await flushMicrotasks()
assert.equal(targetedBusinessCalls, 0, 'targeted 必须等待当前 full revalidation 结束')
focusListener?.()
activation.trigger('interval')
await clock.advanceBy(100)
assert.equal(fullRevalidatorCalls, 1, 'targeted pending 期间 focus/interval 不得启动新的 full revalidation')
fullRevalidatorGate.resolve(undefined)
await waitFor(() => targetedBusinessCalls === 1, 'full revalidation 结束后 targeted 业务请求未启动')
focusListener?.()
activation.trigger('focus')
await clock.advanceBy(100)
assert.equal(revalidatorCalls.get('providers.catalog'), providerRunsAfterFullStarted, 'targeted 期间 focus 不得启动 providers full revalidator')
assert.equal(revalidatorCalls.get('accounts.options'), tagRunsAfterFullStarted, 'targeted 期间 focus 不得启动 tags full revalidator')
assert.equal(revalidatorCalls.get('accounts.static'), accountRunsAfterFullStarted, 'targeted 期间 focus 不得启动 accounts full revalidator')
targetedRunGate.resolve(undefined)
await clock.advanceBy(100)
assert.equal((await targetedRefresh).superseded, false)
assert.equal(networkCalls.get('accounts.static'), 2)
assert.equal(networkCalls.get('providers.catalog'), providerNetworkBeforeTargeted)
assert.equal(networkCalls.get('accounts.options'), tagNetworkBeforeTargeted)
assert.equal(revalidatorCalls.get('providers.catalog'), providerRunsBeforeTargeted + 2, 'targeted 期间 focus 必须合并并在结束后补跑一次 full')
assert.equal(revalidatorCalls.get('accounts.options'), tagRunsBeforeTargeted + 2, 'targeted 期间 focus 必须补跑 providers/tags')
assert.equal(revalidatorCalls.get('accounts.static'), accountRunsBeforeTargeted + 2, 'runTargeted 结束后必须恰好补跑一次 pending full')
assert.deepEqual(Object.keys(requests.at(-1)?.domains ?? {}), ['accounts.static'])
unregisterFullRevalidator()

const targetedOrder: string[] = []
const firstTargetedGate = deferred<void>()
const firstTargeted = activation.runTargeted(['accounts.static'], async () => {
  targetedOrder.push('first:start')
  await firstTargetedGate.promise
  targetedOrder.push('first:end')
  return 'first'
})
const secondTargeted = activation.runTargeted(['accounts.static'], async () => {
  targetedOrder.push('second:start')
  targetedOrder.push('second:end')
  return 'second'
})
await clock.advanceBy(100)
assert.deepEqual(targetedOrder, ['first:start'], '多个 targeted 调用必须 FIFO 串行，不得互相 supersede')
firstTargetedGate.resolve(undefined)
assert.equal(await firstTargeted, 'first')
assert.equal(await secondTargeted, 'second')
assert.deepEqual(targetedOrder, ['first:start', 'first:end', 'second:start', 'second:end'])

const stuckTargetedStartedAt = clock.nowMs
const stuckTargeted = activation.runTargeted(['accounts.static'], async () => neverSettles)
await flushMicrotasks()
const callsBeforeStuckTargetedFocus = new Map(revalidatorCalls)
focusListener?.()
activation.trigger('interval')
await clock.advanceTo(stuckTargetedStartedAt + revalidationTimeoutMs)
await assert.rejects(stuckTargeted, /targeted.*deadline/i, 'targeted 永久 pending 必须在 deadline 后释放调用方和 FIFO')
await clock.advanceBy(100)
for (const domain of myAccountsPageDataActivationManifest.domains) {
  assert.equal(
    revalidatorCalls.get(domain),
    (callsBeforeStuckTargetedFocus.get(domain) ?? 0) + 1,
    'targeted deadline 后必须补跑期间合并的 full revalidation'
  )
}

const cancelledTargeted = activation.runTargeted(['accounts.static'], async () => neverSettles)
await flushMicrotasks()
activation.deactivate()
await assert.rejects(cancelledTargeted, /activation.*不可用/i, 'deactivate 必须立即释放 targeted 锁')
showActivationPage.value = false
await nextTick()
showActivationPage.value = true
await nextTick()
await clock.advanceBy(100)

const zombieFullGate = deferred<void>()
let zombieFullCalls = 0
let zombieTargetedCalls = 0
const unregisterZombieFull = activation.registerRevalidator('accounts.static', () => {
  zombieFullCalls += 1
  if (zombieFullCalls === 1) return zombieFullGate.promise
})
activation.trigger('focus')
await clock.advanceBy(100)
assert.equal(zombieFullCalls, 1, 'zombie 竞态准备必须保持一个 full revalidator 挂起')
const zombieTargeted = activation.runTargeted(['accounts.static'], async () => {
  zombieTargetedCalls += 1
  return 'zombie:first'
})
const secondZombieTargeted = activation.runTargeted(['accounts.static'], async () => {
  zombieTargetedCalls += 1
  return 'zombie:second'
})
await flushMicrotasks()
const zombieTargetedRejected = assert.rejects(zombieTargeted, /activation.*不可用/i, '等待 full 的 targeted 必须可由 deactivate 释放')
const secondZombieTargetedRejected = assert.rejects(
  secondZombieTargeted,
  /activation.*不可用/i,
  '尚未进入 FIFO 执行体的 targeted 也必须由 deactivate 立即释放'
)
activation.deactivate()
await Promise.all([zombieTargetedRejected, secondZombieTargetedRejected])
showActivationPage.value = false
await nextTick()
showActivationPage.value = true
await nextTick()
zombieFullGate.resolve(undefined)
await clock.advanceBy(100)
assert.equal(zombieTargetedCalls, 0, '所有已取消 targeted 的旧 continuation 都不得在页面恢复后执行')
unregisterZombieFull()

gatedConfirmation = deferred<PageDataConfirmResult>()
const lateParticipant: PageDataActivationParticipant = {
  resourceKey: 'late-account-list',
  domain: 'accounts.static',
  token: token('accounts.static'),
  generation: 99,
  writeEpoch: 0
}
const lateDecision = activation.runTargeted(
  ['accounts.static'],
  (scopedActivation) => scopedActivation.register(lateParticipant)
)
await clock.advanceBy(50)
activation.deactivate()
assert.equal((await lateDecision).state, 'superseded', 'deactivate 必须 supersede 迟到结果')
gatedConfirmation.resolve(confirmationFor(requests.at(-1)!, clock.nowMs))
gatedConfirmation = undefined
await flushMicrotasks()

app.unmount()
for (const controller of controllers.values()) controller.close()

const { api } = await import('../../api/client')
const { authState } = await import('../../composables/useAuth')
const { useAccountListData } = await import('../../views/accounts/useAccountListData')
const { loadProviderOptionsResource } = await import('../../composables/useProviderOptionsResource')
const { useAccountEditTagOptions } = await import('../../views/accounts/useAccountEditTagOptions')
const { createMemoryHistory, createRouter } = await import('vue-router')
const {
  loadAccountTagOptionsCached,
  readAccountTagOptionsCache,
  writeAccountTagOptionsCache
} = await import('../../views/accounts/accountTagOptionsCache')
const accountRevalidators = new Map<PageDataDomain, (activation: PageDataActivationHandle) => void | Promise<void>>()
const wiringActivation = {
  register: async (participant: PageDataActivationParticipant) => unavailableActivationDecision('pre', participant),
  stabilize: async (participant: PageDataActivationParticipant & { baseline: PageDataRevisionToken }) => unavailableActivationDecision('post', participant),
  trigger: () => undefined,
  deactivate: () => undefined,
  dispose: () => undefined,
  registerRevalidator(domain: PageDataDomain, revalidate: (activation: PageDataActivationHandle) => void | Promise<void>) {
    accountRevalidators.set(domain, revalidate)
    return () => accountRevalidators.delete(domain)
  },
  runTargeted<T>(_domains: readonly PageDataDomain[], run: (activation: PageDataActivationHandle) => Promise<T>) {
    return run(scopedAccountActivation(100))
  }
} satisfies PageDataActivation
const previousCurrentUser = authState.currentUser.value
const originalMyAccountsList = api.myAccounts.list
const firstAccountNetworkStarted = deferred<void>()
const firstAccountNetwork = deferred<ReturnType<typeof accountListResult>>()
let accountNetworkCalls = 0
api.myAccounts.list = async () => {
  accountNetworkCalls += 1
  if (accountNetworkCalls === 1) {
    firstAccountNetworkStarted.resolve(undefined)
    return firstAccountNetwork.promise
  }
  return accountListResult('new-generation')
}
authState.currentUser.value = {
  id: 'task3-wiring-user',
  username: 'task3-wiring-user',
  displayName: 'Task3 Wiring User',
  role: 'user',
  mustChangePassword: false
}
let wiringAccounts: ReturnType<typeof useAccountListData> | undefined
const WiringPage = defineComponent({
  setup() {
    wiringAccounts = useAccountListData({
      isManagementView: computed(() => false),
      pageDataActivation: wiringActivation,
      scopedSystemAccountId: () => undefined
    })
    return () => h('div', 'account wiring page')
  }
})
const wiringContainer = document.createElement('div')
document.body.append(wiringContainer)
const wiringApp = createApp(WiringPage)
const wiringRouter = createRouter({
  history: createMemoryHistory(),
  routes: [{ path: '/my-accounts', component: WiringPage }]
})
await wiringRouter.push('/my-accounts')
await wiringRouter.isReady()
wiringApp.use(wiringRouter)
wiringApp.mount(wiringContainer)
await nextTick()
const accountRevalidator = accountRevalidators.get('accounts.static')
assert(accountRevalidator, '账户列表必须注册 accounts.static revalidator')
const oldGenerationLoad = Promise.resolve(accountRevalidator(scopedAccountActivation(1)))
await firstAccountNetworkStarted.promise
const newGenerationLoad = Promise.resolve(accountRevalidator(scopedAccountActivation(2)))
await waitFor(() => accountNetworkCalls === 2, '新 revalidation generation 不得复用旧账户 GET singleflight')
await newGenerationLoad
assert.equal(wiringAccounts?.accounts.value[0]?.id, 'new-generation', '新 generation 必须先应用新账户列表')
firstAccountNetwork.resolve(accountListResult('old-generation'))
await oldGenerationLoad
await flushMicrotasks()
assert.equal(wiringAccounts?.accounts.value[0]?.id, 'new-generation', '旧 GET 晚返回不得覆盖新 generation 数据')
wiringApp.unmount()
api.myAccounts.list = originalMyAccountsList
authState.currentUser.value = previousCurrentUser

const resourceTestUserBefore = authState.currentUser.value
const originalProviderDefinitions = api.providers.definitions
const originalMyAccountTags = api.myAccounts.tags
const oldProviderStarted = deferred<void>()
const oldProviderNetwork = deferred<Awaited<ReturnType<typeof originalProviderDefinitions>>>()
const oldTagsStarted = deferred<void>()
const oldTagsNetwork = deferred<Awaited<ReturnType<typeof originalMyAccountTags>>>()
let providerNetworkCalls = 0
let tagNetworkCalls = 0
const appliedProviderIds: string[] = []
try {
  authState.currentUser.value = {
    id: 'task3-resource-user',
    username: 'task3-resource-user',
    displayName: 'Task3 Resource User',
    role: 'user',
    mustChangePassword: false
  }
  api.providers.definitions = async () => {
    providerNetworkCalls += 1
    if (providerNetworkCalls === 1) {
      oldProviderStarted.resolve(undefined)
      return oldProviderNetwork.promise
    }
    return [providerDefinition('provider-new')]
  }
  api.myAccounts.tags = async () => {
    tagNetworkCalls += 1
    if (tagNetworkCalls === 1) {
      oldTagsStarted.resolve(undefined)
      return oldTagsNetwork.promise
    }
    return [{ id: 'tag-new', name: '新标签', accountCount: 0 }]
  }

  const oldProviderLoad = loadProviderOptionsResource({
    activation: scopedDomainActivation('providers.catalog', 1),
    apply: (items) => { if (items[0]) appliedProviderIds.push(items[0].id) },
    includeDefinitions: true,
    isManagementView: false
  })
  await oldProviderStarted.promise
  const currentProviderResult = await loadProviderOptionsResource({
    activation: scopedDomainActivation('providers.catalog', 2),
    apply: (items) => { if (items[0]) appliedProviderIds.push(items[0].id) },
    includeDefinitions: true,
    isManagementView: false
  })
  assert.deepEqual(currentProviderResult, {
    state: 'ready',
    data: [providerDefinition('provider-new')]
  }, '当前 provider resource 必须返回显式 ready 结果')
  oldProviderNetwork.resolve([providerDefinition('provider-old')])
  const oldProviderResult = await oldProviderLoad
  assert.deepEqual(oldProviderResult, { state: 'superseded' }, '旧 provider Promise 不得携带可被调用方误应用的数据')
  assert.deepEqual(appliedProviderIds, ['provider-new'], '旧 provider resource 晚返回不得 apply 覆盖新数据')

  const oldTagsLoad = loadAccountTagOptionsCached({
    activation: scopedDomainActivation('accounts.options', 1),
    isManagementView: false,
    revalidate: true
  })
  await oldTagsStarted.promise
  const newTagsResult = await loadAccountTagOptionsCached({
    activation: scopedDomainActivation('accounts.options', 2),
    isManagementView: false,
    revalidate: true
  })
  assert.equal(newTagsResult.superseded, false)
  assert.equal(readAccountTagOptionsCache('self')?.[0]?.id, 'tag-new')
  oldTagsNetwork.resolve([{ id: 'tag-old', name: '旧标签', accountCount: 0 }])
  const oldTagsResult = await oldTagsLoad
  assert.equal(oldTagsResult.superseded, true, '旧 tags resource 必须显式返回 superseded')
  assert.equal(readAccountTagOptionsCache('self')?.[0]?.id, 'tag-new', '旧 tags resource 晚返回不得覆盖独立内存 cache')
} finally {
  api.providers.definitions = originalProviderDefinitions
  api.myAccounts.tags = originalMyAccountTags
  authState.currentUser.value = resourceTestUserBefore
}

const tagMemoryGenerationBefore = currentPageDataSecurityGeneration()
writeAccountTagOptionsCache('self', [{ id: 'old-security-tag', name: '旧安全上下文标签', accountCount: 0 }])
assert.equal(readAccountTagOptionsCache('self')?.[0]?.id, 'old-security-tag', '同一 security generation 内标签内存缓存必须可复用')
advancePageDataSessionGeneration()
assert.equal(currentPageDataSecurityGeneration(), tagMemoryGenerationBefore + 1)
assert.equal(readAccountTagOptionsCache('self'), undefined, 'security generation 变化后不得命中旧标签内存缓存')

const originalDeleteMyAccountTag = api.myAccounts.deleteTag
const originalMyAccountTagsForMutation = api.myAccounts.tags
const delayedTagDelete = deferred<void>()
const editTagForm = reactive({ tags: ['旧标签'] }) as Parameters<typeof useAccountEditTagOptions>[0]['form']
let editTagOptions: ReturnType<typeof useAccountEditTagOptions> | undefined
try {
  api.myAccounts.tags = async () => [{ id: 'old-tag', name: '旧标签', accountCount: 0 }]
  api.myAccounts.deleteTag = async () => delayedTagDelete.promise
  const EditTagHarness = defineComponent({
    setup() {
      editTagOptions = useAccountEditTagOptions({
        accountTagOperationScopeParams: () => undefined,
        extractApiErrorMessage: (_error, fallback) => fallback,
        form: editTagForm,
        isManagementView: computed(() => false)
      })
      return () => h('div')
    }
  })
  const editTagContainer = document.createElement('div')
  document.body.append(editTagContainer)
  const editTagApp = createApp(EditTagHarness)
  editTagApp.mount(editTagContainer)
  await nextTick()
  assert(editTagOptions)
  await editTagOptions.loadAccountTagOptions(undefined, true)
  assert.equal(readAccountTagOptionsCache('self')?.[0]?.id, 'old-tag', '删除前必须建立真实 self 标签 cache')
  const deleteFlight = editTagOptions.deleteAccountTag('old-tag')
  await flushMicrotasks()
  advancePageDataSessionGeneration()
  delayedTagDelete.resolve(undefined)
  await deleteFlight
  assert.deepEqual(editTagForm.tags, ['旧标签'], '旧安全上下文 mutation 迟到成功不得修改新上下文表单')
  assert.equal(editTagOptions.accountTagOptions.value[0]?.id, 'old-tag', '旧 mutation 不得覆盖新上下文页面标签状态')
  assert.equal(readAccountTagOptionsCache('self'), undefined, '旧 mutation 不得以当前 security generation 重新写 self 内存缓存')
  editTagApp.unmount()
  editTagContainer.remove()
} finally {
  api.myAccounts.deleteTag = originalDeleteMyAccountTag
  api.myAccounts.tags = originalMyAccountTagsForMutation
}

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
assert.match(accountListDataSource, /pageDataActivation\.runTargeted\(\s*\['accounts\.static'\],[\s\S]{0,180}\(activation\)\s*=>\s*execute\(activation,\s*true\)/)
assert.match(accountListDataSource, /return refresh \? accountPageCache\.forceRefresh\(\) : accountPageCache\.load\(\)/)
assert.doesNotMatch(accountListDataSource, /beginTargeted/)
assert.doesNotMatch(accountListDataSource, /currentPageDataActivation/, '账户 revalidator 不得通过共享可变 activation 跨代传递')
assert.match(accountListDataSource, /requestIdentity:\s*revalidationGeneration/, '账户 full revalidation 必须带显式请求代次，禁止复用旧 singleflight')
assert.match(accountListDataSource, /pageDataActivation:\s*activation/, 'scoped activation 必须随单次 load options 传入 fetch/controller')
assert.match(accountListDataSource, /superseded:\s*accountListResult\.superseded/, '账户旧 controller 返回 superseded 时不得应用列表数据')
assert.match(accountListDataSource, /skipOptions:\s*true/, 'accounts.static revalidator 不得重复发起 providers/options 资源加载')
assert.match(tagCacheSource, /activation:\s*input\.activation/)
assert.match(tagCacheSource, /currentPageDataSecurityGeneration/, 'tags 独立内存缓存必须绑定安全上下文代次')
assert.match(tagCacheSource, /if\s*\(!superseded\)[\s\S]{0,120}writeAccountTagOptionsCache/, '旧 tags resource 不得写独立内存 cache')
assert.match(tagCacheSource, /outcome\.state\s*!==\s*'superseded'/, '旧 tags confirmation 不得写独立内存 cache')
assert.match(providerSource, /activation:\s*options\.activation/)
assert.match(providerSource, /if\s*\(result\.superseded\)\s*return\s*\{\s*state:\s*'superseded'\s*\}/, '旧 provider resource 必须返回无 data 的显式 superseded 结果')
assert.match(providerSource, /return\s*\{\s*state:\s*'ready',\s*data:\s*result\.data\s*\}/, '当前 provider resource 必须返回显式 ready data')
assert.match(providerSource, /outcome\.state\s*!==\s*'superseded'/, '旧 provider confirmation 不得 apply')
assert.doesNotMatch(activationSource, /setInterval|clearInterval/, '页面 activation 周期必须使用一次性 timeout')
assert.doesNotMatch(activationSource, /createCoordinatorTimer|setTimeout\(\(\) => undefined/, '生产代码不得使用空 timeout 辅助测试 flush')

console.log('我的账户页面数据 activation 回归通过')

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

function scopedAccountActivation(sequence: number): PageDataActivationHandle {
  return scopedDomainActivation('accounts.static', sequence)
}

function scopedDomainActivation(domain: PageDataDomain, sequence: number): PageDataActivationHandle {
  const revision = token(domain)
  revision.sequence = sequence
  return {
    register: async (participant) => ({
      state: 'confirmed',
      phase: 'pre',
      participant,
      result: { action: 'reload', token: revision, serverTime: new Date().toISOString() }
    }),
    stabilize: async (participant) => ({
      state: 'confirmed',
      phase: 'post',
      participant,
      result: { action: 'unchanged', token: revision, serverTime: new Date().toISOString() }
    }),
    trigger: () => undefined,
    deactivate: () => undefined,
    dispose: () => undefined
  }
}

function providerDefinition(id: string) {
  return {
    id,
    code: id,
    name: id,
    enabled: true,
    defaultProtocolProfileId: `${id}-default`,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: '',
    defaultHealthCheckModel: '',
    defaultSupportedModels: [],
    accountTypes: [],
    capabilities: [],
    protocolProfiles: []
  }
}

function unavailableActivationDecision(
  phase: 'pre' | 'post',
  participant: PageDataActivationParticipant
) {
  return { state: 'unavailable' as const, phase, participant }
}

function accountListResult(id: string) {
  return {
    items: [{
      id,
      name: id,
      providerCode: 'openai',
      providerProtocolProfileId: 'openai-default',
      protocolCode: 'openai',
      protocolVersion: 'v1',
      type: 'api_key',
      status: 'active',
      schedulable: true,
      priority: 0,
      concurrency: 1,
      currentConcurrency: 0,
      tags: [],
      supportedModels: [],
      createdAt: '2026-07-22T00:00:00.000Z',
      updatedAt: '2026-07-22T00:00:00.000Z'
    }],
    total: 1,
    hasMore: false,
    page: 1,
    pageSize: 20
  }
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
