import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

const domWindow = new Window({ url: 'http://127.0.0.1/my-models' })
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

const { useKeepAliveSupersededRecovery } = await import('../../composables/useKeepAliveSupersededRecovery')
const { KeepAlive, createApp, defineComponent, h, nextTick, onMounted, ref } = await import('vue')

type ResourceState = 'ready' | 'superseded'

const showProviderPage = ref(true)
const pendingInitial = deferred<ResourceState>()
const queuedResults: ResourceState[] = []
let loadCalls = 0
let recordResult: ((state: ResourceState) => void) | undefined
let startRequest: (() => number) | undefined
let recordRequest: ((request: number, state: ResourceState) => void) | undefined

const ProviderPage = defineComponent({
  name: 'ProviderPage',
  setup() {
    const recovery = useKeepAliveSupersededRecovery(load)
    startRequest = recovery.start
    recordRequest = recovery.record
    recordResult = (state) => {
      const request = recovery.start()
      recovery.record(request, state)
    }

    async function load(): Promise<void> {
      const request = recovery.start()
      loadCalls += 1
      const result = loadCalls === 1
        ? await pendingInitial.promise
        : queuedResults.shift() ?? 'ready'
      recovery.record(request, result)
    }

    onMounted(() => { void load() })
    return () => h('div', 'providers')
  }
})
const IdlePage = defineComponent({ name: 'AccountsPage', setup: () => () => h('div', 'accounts') })
const Root = defineComponent({
  setup: () => () => h(KeepAlive, null, {
    default: () => showProviderPage.value
      ? h(ProviderPage, { key: '/my-models' })
      : h(IdlePage, { key: '/my-accounts' })
  })
})

const container = document.createElement('div')
document.body.append(container)
const app = createApp(Root)
app.mount(container)
await nextTick()
await flushMicrotasks()
assert.equal(loadCalls, 1, '首次 onMounted/onActivated 不得双请求')

showProviderPage.value = false
await nextTick()
showProviderPage.value = true
await nextTick()
assert.equal(loadCalls, 1, '旧请求尚未返回时快速切回应等待 superseded 结果，不得抢跑重复请求')

queuedResults.push('ready')
pendingInitial.resolve('superseded')
await flushMicrotasks()
assert.equal(loadCalls, 2, '快速 my-models -> my-accounts -> my-models 后，旧请求 superseded 必须在当前 activation 自动恢复')

const staleRequest = startRequest?.()
const currentRequest = startRequest?.()
assert.notEqual(staleRequest, undefined)
assert.notEqual(currentRequest, undefined)
recordRequest?.(currentRequest!, 'ready')
recordRequest?.(staleRequest!, 'superseded')
await flushMicrotasks()
assert.equal(loadCalls, 2, '较新 ready 后迟到的旧 superseded 不得触发额外恢复')

recordResult?.('superseded')
recordResult?.('superseded')
await flushMicrotasks()
assert.equal(loadCalls, 2, '同一 activation 内 superseded 恢复必须有界，不能形成重试循环')

showProviderPage.value = false
await nextTick()
recordResult?.('superseded')
await flushMicrotasks()
assert.equal(loadCalls, 2, 'deactivated 期间不得发起恢复请求')

queuedResults.push('ready')
showProviderPage.value = true
await nextTick()
await flushMicrotasks()
assert.equal(loadCalls, 3, 'deactivated 期间记录的 superseded 应在下一次 onActivated 恢复一次')

app.unmount()
container.remove()

for (const relativePath of [
  '../../views/providers/ProvidersView.vue',
  '../../views/response-inspection-policies/ResponseInspectionPoliciesView.vue',
  '../../views/groups/GroupsView.vue',
  '../../views/usage-stats/UsageStatsView.vue'
]) {
  const source = readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), 'utf8')
  assert.match(source, /useKeepAliveSupersededRecovery/, `${relativePath} 必须接入 KeepAlive superseded 恢复 guard`)
  assert.match(source, /\.record\(providerRecoveryRequest, provider(?:List|Result)?\.state\)/, `${relativePath} 必须按请求序号记录 provider resource 结果`)
}

const groupsSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsView.vue', import.meta.url)), 'utf8')
assert.match(
  groupsSource,
  /useKeepAliveSupersededRecovery\(\(\) => loadGroupOptions\(false, true\)\)/,
  '分组页恢复必须绕过本地 loaded 快路径，但不得 force invalidate 共享 provider resource'
)

const usageStatsSource = readFileSync(fileURLToPath(new URL('../../views/usage-stats/UsageStatsView.vue', import.meta.url)), 'utf8')
assert.match(
  usageStatsSource,
  /useKeepAliveSupersededRecovery\(\(\) => loadUsageStatsOptions\(false, true\)\)/,
  '用量页恢复必须绕过本地 loaded 快路径，但不得 force invalidate 共享 provider resource'
)

const accountsSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountListData.ts', import.meta.url)), 'utf8')
assert.doesNotMatch(accountsSource, /useKeepAliveSupersededRecovery/, '账户页已有页面 activation，不得叠加私有 KeepAlive 恢复')

const policiesSource = readFileSync(fileURLToPath(new URL('../../views/response-inspection-policies/ResponseInspectionPoliciesView.vue', import.meta.url)), 'utf8')
assert.match(
  policiesSource,
  /useKeepAliveSupersededRecovery\(\(\) => loadPolicyProviders\(\)\)/,
  '策略页 superseded 恢复只能补 provider 子资源，不得重复加载策略列表'
)

console.log('KeepAlive provider superseded 有界恢复回归通过')

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((nextResolve) => { resolve = nextResolve })
  return { promise, resolve }
}

async function flushMicrotasks(rounds = 8): Promise<void> {
  for (let index = 0; index < rounds; index += 1) await Promise.resolve()
}
