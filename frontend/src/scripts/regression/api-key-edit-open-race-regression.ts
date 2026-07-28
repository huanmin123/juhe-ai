import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const modalSource = readFileSync(
  fileURLToPath(new URL('../../views/api-keys/ApiKeyEditModal.vue', import.meta.url)),
  'utf8'
)

assert.doesNotMatch(modalSource, /createApiKeyEditOpenOperationGuard|beginOpenOperation|isCurrentOpenOperation/, '同步打开后不得保留异步 open generation guard')

const openCreateSource = sourceBetween(modalSource, 'function openCreate', 'function openEdit')
assert.doesNotMatch(openCreateSource, /async|await|loadRouteStrategyOptions|loadUserReferenceData|resetRouteStrategyOptions/, '新建必须同步完成且不得发起候选请求')
assertOrdered(openCreateSource, [
  'const defaultStrategy = cachedDefaultRouteStrategy()',
  'Object.assign(form',
  'editingBaseline = undefined',
  'modalOpen.value = true',
  'if (!defaultStrategy) prewarmCreateDefaultRouteStrategy()'
], '新建必须先读取缓存快照并立即开窗，缓存缺失时才在开窗后异步重试常量')

const prewarmSource = sourceBetween(modalSource, 'function prewarmCreateDefaultRouteStrategy', 'async function loadRouteStrategyOptions')
assert.match(prewarmSource, /void loadUserReferenceData\(params\)\.then/, '缓存缺失时应复用用户常量接口异步重试')
assert.doesNotMatch(prewarmSource, /loadRouteStrategyOptions|routeStrategies\.options/, '常量重试不得加载策略路由候选列表')
assert.match(prewarmSource, /!modalOpen\.value[\s\S]+editingId\.value[\s\S]+routeStrategyTouched\.value[\s\S]+form\.routeStrategyId/, '异步常量回填必须避开已关闭、已切换编辑或用户已操作的表单')

const createPayloadSource = sourceBetween(modalSource, "const result = await props.apiKeysApi.create", "emit('created'")
assert.match(createPayloadSource, /routeStrategyTouched\.value && snapshot\.routeStrategyId/, '用户未操作策略下拉时不得提交缓存展示用的默认策略 ID')

const openEditSource = sourceBetween(modalSource, 'function openEdit', 'function apiKeyOperationScopeParams')
assert.doesNotMatch(openEditSource, /async|await|loadRouteStrategyOptions|resetRouteStrategyOptions/, '编辑必须同步完成且不得碰候选加载状态')
assertOrdered(openEditSource, [
  'editingRevision = apiKey.revision',
  'routeStrategy: apiKeyRouteStrategySelection(apiKey)',
  'editingBaseline = currentApiKeyEditableSnapshot',
  'modalOpen.value = true'
], '编辑必须从列表行写入已选策略和 revision/baseline 后立即开窗')

console.log('API Key 弹窗同步打开、默认常量异步重试与零候选预加载回归通过')

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
