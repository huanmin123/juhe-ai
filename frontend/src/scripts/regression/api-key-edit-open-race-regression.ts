import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const modalSource = readFileSync(
  fileURLToPath(new URL('../../views/api-keys/ApiKeyEditModal.vue', import.meta.url)),
  'utf8'
)

assert.match(modalSource, /createApiKeyEditOpenOperationGuard/, 'API Key 弹窗打开流程必须使用独立 generation guard')
assert.match(modalSource, /beginOpenOperation\(/, '每次新建或编辑必须先取得新的打开操作 token')
assert.match(modalSource, /isCurrentOpenOperation\(/, '异步 options 返回后必须校验 token 与作用域')

const openCreateSource = sourceBetween(modalSource, 'async function openCreate', 'async function openEdit')
assertOrdered(openCreateSource, [
  'await loadRouteStrategyOptions()',
  'isCurrentOpenOperation(openOperation, apiKeyEditOpenScopeKey())',
  'form.routeStrategyId = defaultStrategy.id',
  'modalOpen.value = true'
], '新建流程必须先校验最新操作，再写默认策略和打开弹窗')

const openEditSource = sourceBetween(modalSource, 'async function openEdit', 'function apiKeyEditOpenScopeKey')
assertOrdered(openEditSource, [
  "await loadRouteStrategyOptions('', [apiKey.routeStrategyId])",
  'isCurrentOpenOperation(openOperation, apiKeyEditOpenScopeKey(apiKey))',
  'form.routeStrategy = selectedRouteStrategySelection(apiKey.routeStrategyId)',
  'modalOpen.value = true'
], '编辑流程必须先校验最新操作，再回填已选策略和打开弹窗')

const { createApiKeyEditOpenOperationGuard } = await import('../../views/api-keys/apiKeyEditOpenOperation')

await verifyLatestOperationWins()
await verifyScopeChangeInvalidatesOperation()

console.log('API Key 弹窗异步打开竞态回归通过')

async function verifyLatestOperationWins(): Promise<void> {
  const guard = createApiKeyEditOpenOperationGuard()
  const firstOptions = deferred<void>()
  const secondOptions = deferred<void>()
  const writes: string[] = []

  const first = simulateOpen('management:owner-a:create', 'first', firstOptions.promise, writes, guard)
  const second = simulateOpen('management:owner-a:edit:key-b', 'second', secondOptions.promise, writes, guard)

  secondOptions.resolve()
  await second
  firstOptions.resolve()
  await first

  assert.deepEqual(writes, ['second'], '后发编辑完成后，先发新建不得再写默认策略或打开弹窗')
}

async function verifyScopeChangeInvalidatesOperation(): Promise<void> {
  const guard = createApiKeyEditOpenOperationGuard()
  const options = deferred<void>()
  const writes: string[] = []
  let currentScopeKey = 'management:owner-a:create'
  const operation = guard.beginOpenOperation(currentScopeKey)
  const pending = (async () => {
    await options.promise
    if (!guard.isCurrentOpenOperation(operation, currentScopeKey)) return
    writes.push('opened')
  })()

  currentScopeKey = 'management:owner-b:create'
  options.resolve()
  await pending

  assert.deepEqual(writes, [], '等待 options 期间切换系统账户后，旧作用域不得写表单或打开弹窗')
}

async function simulateOpen(
  scopeKey: string,
  label: string,
  optionsPromise: Promise<void>,
  writes: string[],
  guard: ReturnType<typeof createApiKeyEditOpenOperationGuard>
): Promise<void> {
  const operation = guard.beginOpenOperation(scopeKey)
  await optionsPromise
  if (!guard.isCurrentOpenOperation(operation, scopeKey)) return
  writes.push(label)
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve
  })
  return { promise, resolve }
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}

function assertOrdered(source: string, fragments: string[], message: string): void {
  let previousIndex = -1
  for (const fragment of fragments) {
    const currentIndex = source.indexOf(fragment)
    assert(currentIndex > previousIndex, `${message}；顺序片段缺失或错位：${fragment}`)
    previousIndex = currentIndex
  }
}
