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
const apiKeyListSource = readFileSync(
  fileURLToPath(new URL('../../views/api-keys/ApiKeyResponsiveList.vue', import.meta.url)),
  'utf8'
)
const apiKeyApiSource = readFileSync(
  fileURLToPath(new URL('../../api/domains/apiKeys.ts', import.meta.url)),
  'utf8'
)
const apiKeyTypeSource = readFileSync(
  fileURLToPath(new URL('../../types/domain/access.ts', import.meta.url)),
  'utf8'
)

const fetchPageSource = sourceBetween(
  apiKeysViewSource,
  'fetchPage: async',
  'requestSignature:'
)

assert.match(fetchPageSource, /await\s+apiKeysApi\.list\(/, 'API Key 首屏必须只等待列表接口')
assert.doesNotMatch(fetchPageSource, /loadApiKeyOptions|loadRouteStrategyOptions/, 'API Key 首屏不得预取策略路由候选')
assert.doesNotMatch(fetchPageSource, /Promise\.all/, 'API Key 列表请求不得与辅助 options 绑定成同一个首屏等待链')
assert.doesNotMatch(apiKeysViewSource, /function loadApiKeyOptions\(/, '页面不得保留由列表加载链调用的宽 options 聚合入口')
assert.doesNotMatch(apiKeysViewSource, /loadApiKeyUsage|apiKeyUsageState|apiKeyUsageErrors|retryApiKeyUsage/, 'API Key 页面不得保留用量补发状态机')
assert.doesNotMatch(apiKeyApiSource, /\/api-keys\/usage|\/my-api-keys\/usage|usage:/, '前端 API 不得保留独立用量接口')
assert.match(apiKeyTypeSource, /interface ApiKeyUsageListSummary[\s\S]*requestCount:[\s\S]*totalTokens:[\s\S]*totalCost:/, 'API Key 列表 usage 必须使用三字段展示摘要')
assert.match(apiKeyTypeSource, /usage:\s*ApiKeyUsageListSummary/, 'API Key 列表项不得复用完整 AccountUsageSummary')
assert.match(apiKeyListSource, /<UsageSummaryTags :usage="record\.usage" \/>/, '桌面列表应直接展示列表项用量')
assert.match(apiKeyListSource, /<strong>\{\{ formatUsageSummary\(record\.usage\) \}\}<\/strong>/, '移动列表应直接展示列表项用量')
assert.doesNotMatch(apiKeyListSource, /usageState|usageErrors|retry-usage|用量加载失败/, '列表组件不得保留用量加载占位或重试状态')
assert.match(apiKeysViewSource, /onDeactivated\(\(\) =>/, 'KeepAlive 失活时应标记页面需要重载')
assert.match(apiKeysViewSource, /onActivated\(\(\) =>/, 'KeepAlive 激活时必须重载列表')
assert.match(
  apiKeysViewSource,
  /loadUserReferenceData\(\{ viewScope: 'admin', systemAccountId \}\)\.catch\(\(\) => undefined\)/,
  '管理视图选定目标用户后应异步预热其默认路由引用'
)

const filterDropdownSource = sourceBetween(
  apiKeysViewSource,
  'function handleRouteStrategyOptionsDropdown',
  'function handleRouteStrategyOptionsSearch'
)
assert.match(filterDropdownSource, /open[^\n]*loadRouteStrategyOptions/, '筛选下拉必须在首次展开时加载策略路由候选')
assert.match(apiKeysViewSource, /routeStrategyOptionsLoadingKey === requestKey/, '筛选候选相同进行中请求必须复用')
assert.match(apiKeysViewSource, /routeStrategyOptionsRaw\.value = \[\]/, '切换列表作用域时必须清除上一作用域候选')

const openCreateSource = sourceBetween(apiKeyModalSource, 'function openCreate', 'function openEdit')
const openEditSource = sourceBetween(apiKeyModalSource, 'function openEdit', 'function apiKeyOperationScopeParams')
for (const [label, source] of [['新建', openCreateSource], ['编辑', openEditSource]] as const) {
  assert.doesNotMatch(source, /loadRouteStrategyOptions|resetRouteStrategyOptions|await\s/, `${label}弹窗打开不得加载策略路由候选或阻塞等待`)
  assert.match(source, /modalOpen\.value = true/, `${label}弹窗必须同步打开`)
}
assert.doesNotMatch(openEditSource, /loadUserReferenceData|prewarmCreateDefaultRouteStrategy/, '编辑弹窗不需要补发默认引用预热')
assert.match(openCreateSource, /cachedDefaultRouteStrategy\(\)/, '新建弹窗只允许同步读取共享默认策略缓存')
assert.match(openCreateSource, /if \(!defaultStrategy\) prewarmCreateDefaultRouteStrategy\(\)/, '缓存缺失时可在弹窗已打开后异步重试默认引用预热')
assert.doesNotMatch(openCreateSource, /await\s+prewarmCreateDefaultRouteStrategy/, '引用预热重试不得阻塞新建弹窗')
assert.match(openEditSource, /routeStrategy:\s*apiKeyRouteStrategySelection\(apiKey\)/, '编辑弹窗必须从列表行注入当前已选策略')

const modalDropdownSource = sourceBetween(
  apiKeyModalSource,
  'function handleRouteStrategyDropdown',
  'function handleRouteStrategySearch'
)
assert.match(modalDropdownSource, /open[^\n]*loadRouteStrategyOptions/, '编辑弹窗策略候选只能在用户展开下拉时首次加载')

console.log('API Key 单接口完整列表与策略路由候选按需加载回归通过')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
