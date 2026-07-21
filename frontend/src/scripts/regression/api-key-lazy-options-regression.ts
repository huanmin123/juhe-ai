import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const apiKeysViewSource = readFileSync(
  fileURLToPath(new URL('../../views/api-keys/ApiKeysView.vue', import.meta.url)),
  'utf8'
)
const apiKeyModalSource = readFileSync(
  fileURLToPath(new URL('../../views/api-keys/ApiKeyEditModal.vue', import.meta.url)),
  'utf8'
)

const fetchPageSource = sourceBetween(
  apiKeysViewSource,
  'fetchPage: async',
  'requestSignature:'
)

assert.match(fetchPageSource, /return\s+apiKeysApi\.list\(/, 'API Key 首屏必须只等待列表接口')
assert.doesNotMatch(fetchPageSource, /loadApiKeyOptions|loadRouteStrategyOptions/, 'API Key 首屏不得预取策略路由候选')
assert.doesNotMatch(fetchPageSource, /Promise\.all/, 'API Key 列表请求不得与辅助 options 绑定成同一个首屏等待链')
assert.doesNotMatch(apiKeysViewSource, /function loadApiKeyOptions\(/, '页面不得保留由列表加载链调用的宽 options 聚合入口')

const filterDropdownSource = sourceBetween(
  apiKeysViewSource,
  'function handleRouteStrategyOptionsDropdown',
  'function handleRouteStrategyOptionsSearch'
)
assert.match(filterDropdownSource, /open[^\n]*loadRouteStrategyOptions/, '筛选下拉必须在首次展开时加载策略路由候选')
assert.match(apiKeysViewSource, /routeStrategyOptionsLoadingKey === requestKey/, '筛选候选相同进行中请求必须复用')
assert.match(apiKeysViewSource, /routeStrategyOptionsRaw\.value = \[\]/, '切换列表作用域时必须清除上一作用域候选')

const openCreateSource = sourceBetween(apiKeyModalSource, 'async function openCreate', 'async function openEdit')
const openEditSource = sourceBetween(apiKeyModalSource, 'async function openEdit', 'function apiKeyOperationScopeParams')
assert.match(openCreateSource, /await loadRouteStrategyOptions\(\)/, '新建弹窗应在打开操作中按需加载策略路由候选')
assert.match(openEditSource, /await loadRouteStrategyOptions\('', \[apiKey\.routeStrategyId\]\)/, '编辑弹窗应按需加载候选并补齐已选策略')

console.log('API Key 策略路由候选按需加载回归通过')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
