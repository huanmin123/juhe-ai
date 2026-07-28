import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const accountListDataSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/useAccountListData.ts', import.meta.url)),
  'utf8'
)
const accountsViewSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/AccountsView.vue', import.meta.url)),
  'utf8'
)
const accountEditFormSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/useAccountEditForm.ts', import.meta.url)),
  'utf8'
)
const providerResourceSource = readFileSync(
  fileURLToPath(new URL('../../composables/useProviderOptionsResource.ts', import.meta.url)),
  'utf8'
)

const fetchPageSource = sourceBetween(accountListDataSource, 'fetchPage: async', 'requestSignature:')
const lightOptionsSource = sourceBetween(accountListDataSource, 'async function loadAccountOptions(', 'async function ensureProviderDefinition(')
const definitionsSource = sourceBetween(accountListDataSource, 'async function ensureProviderDefinition(', 'function isCurrentProviderDefinitionsRequest(')
const definitionsFenceSource = sourceBetween(accountListDataSource, 'function isCurrentAccountOptionsRequest(', 'function currentAccountProviderScopeKey(')
const scopeKeySource = sourceBetween(accountListDataSource, 'function accountProviderScopeKey(', 'function accountListParams(')

assert.match(fetchPageSource, /void loadAccountOptions\(/, '账户列表首屏只允许触发轻量供应商 options')
assert.doesNotMatch(fetchPageSource, /ensureProviderDefinition|includeDefinitions/, '账户列表首屏不得触发供应商详情')
assert.match(lightOptionsSource, /includeDefinitions:\s*false/, '列表辅助数据必须显式请求轻量 provider options')
assert.doesNotMatch(lightOptionsSource, /includeDefinitions:\s*true|providerDefinitions\.value/, '轻量 loader 不得读取或写入完整供应商定义')

assert.match(accountListDataSource, /const providers = ref<ProviderDefinition\[]>\(\[\]\)/, '列表轻量 providers 必须保持独立状态')
assert.match(accountListDataSource, /const providerDefinitions = ref<ProviderDefinition\[]>\(\[\]\)/, '表单完整 providerDefinitions 必须保持独立状态')
assert.equal(
  (accountListDataSource.match(/ensureProviderDefinition\(/g) ?? []).length,
  1,
  '供应商详情 loader 只能暴露为显式交互入口，列表内部不得自行调用'
)
assert.match(definitionsSource, /api\.providers\.detail\(\s*code,/, '显式 ensure 入口必须只请求当前供应商详情')
assert.doesNotMatch(definitionsSource, /api\.providers\.definitions|includeDefinitions:\s*true/, '选择一个供应商不得读取全部 definitions')
assert.match(definitionsSource, /requestKey = JSON\.stringify\(\[scopeKey, code\]\)/, '供应商详情缓存键必须同时包含 owner scope 与 provider code')
assert.match(definitionsSource, /providerDefinitionsInFlight\.get\(requestKey\)/, '同一 owner 和供应商详情请求必须 singleflight')
assert.match(definitionsSource, /return existingRequest\.promise/, '同一供应商在途请求必须复用同一个 Promise')
assert.match(definitionsSource, /providerDefinitionsScopeKey\.value !== scopeKey[\s\S]*providerDefinitions\.value = \[\]/, '切换作用域必须先清空旧完整定义')
assert.match(definitionsFenceSource, /requestId === providerDefinitionsRequestId[\s\S]*scopeKey === accountProviderScopeKey\(systemAccountId\)/, '完整定义写回必须同时校验请求代次、身份与显式 owner 作用域')
assert.match(definitionsFenceSource, /listScopeAnchorKey[\s\S]*currentAccountProviderScopeKey\(\)/, '未显式指定 owner 时，完整定义请求必须绑定当前列表目标作用域')
assert.match(scopeKeySource, /authState\.revision\.value/, 'definitions 作用域必须包含认证 revision')
assert.match(scopeKeySource, /viewer\?\.id/, 'definitions 作用域必须包含当前登录用户')
assert.match(scopeKeySource, /viewer\?\.role/, 'definitions 作用域必须包含当前登录角色')
assert.match(scopeKeySource, /options\.isManagementView\.value/, 'definitions 作用域必须区分用户视图与管理视图')
assert.match(scopeKeySource, /systemAccountId \?\? 'all'/, '管理视图 definitions 作用域必须区分目标系统账户')

const openCreateSource = sourceBetween(accountEditFormSource, 'async function openCreate()', 'watch(')
const providerSelectSource = sourceBetween(accountEditFormSource, 'function selectProvider(', 'function selectAccountType(')
assert.doesNotMatch(openCreateSource, /ensureProviderDefinition|providers\.detail|providers\.definitions/, '打开新增弹窗不得加载供应商详情')
assert.match(providerSelectSource, /ensureProviderDefinition\(providerCode, systemAccountId\)/, '只有用户选择供应商后才加载该供应商详情')
assert.match(accountEditFormSource, /mergeAccountProviderDefinitions\([\s\S]*FALLBACK_PROVIDERS/, '内置供应商在详情未加载时必须使用本地常量保持表单可用')
assert.match(accountsViewSource, /providerDefinitions,[\s\S]*ensureProviderDefinition,[\s\S]*useAccountEditForm\([\s\S]*ensureProviderDefinition,[\s\S]*providerDefinitions,/, '账户页必须把按需详情状态和加载入口接入编辑表单')

assert.match(providerResourceSource, /options\.includeDefinitions[\s\S]*api\.providers\.definitions/, '完整 definitions 标志必须路由到 definitions API')
assert.match(providerResourceSource, /api\.providers\.options/, '轻量路径必须路由到 provider options API')

console.log('账户供应商定义按需加载回归通过：列表首屏仅请求轻量 options，用户选择后只读取当前供应商详情并按身份作用域隔离')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `未找到源码起点：${start}`)
  assert.notEqual(endIndex, -1, `未找到源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
