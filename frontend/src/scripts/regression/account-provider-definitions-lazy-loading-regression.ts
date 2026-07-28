import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import type { ProviderDefinition } from '@/types/domain'
import { applyProviderDefinitionDefaultsAfterLoad } from '../../views/accounts/useAccountEditForm'

const accountListDataSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/useAccountListData.ts', import.meta.url)),
  'utf8'
)
const accountsViewSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/AccountsView.vue', import.meta.url)),
  'utf8'
)
const accountFilterToolbarSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/AccountFilterToolbar.vue', import.meta.url)),
  'utf8'
)
const accountListSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/AccountList.vue', import.meta.url)),
  'utf8'
)
const accountTableCellSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/AccountTableCell.vue', import.meta.url)),
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

assert.doesNotMatch(fetchPageSource, /loadAccountOptions\(/, '账户列表首屏不得为名称映射或筛选器预取供应商 options')
assert.doesNotMatch(fetchPageSource, /ensureProviderDefinition|includeDefinitions/, '账户列表首屏不得触发供应商详情')
assert.match(accountsViewSource, /function handleProviderFilterDropdown\(open: boolean\)[\s\S]*if \(!open\) return[\s\S]*loadAccountOptions\(/, '供应商 options 只能在用户展开供应商筛选时加载')
assert.equal((accountFilterToolbarSource.match(/@dropdown-visible-change="emit\('provider-dropdown', \$event\)"/g) ?? []).length, 2, '桌面和移动供应商筛选都必须透传真实展开事件')
assert.match(accountTableCellSource, /account\.providerName \|\| providerName\(account\.providerCode\)/, '桌面列表必须优先直接渲染 Node 返回的供应商名称')
assert.match(accountListSource, /record\.providerName \|\| providerName\(record\.providerCode\)/, '移动列表必须优先直接渲染 Node 返回的供应商名称')
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
const defaultFormSource = sourceBetween(accountEditFormSource, 'function defaultForm(', 'function resetForm(')
const mergedDefinitionsSource = sourceBetween(accountEditFormSource, 'function mergeAccountProviderDefinitions(', 'function authorizedAccountBasicDetail(')
assert.doesNotMatch(openCreateSource, /ensureProviderDefinition|providers\.detail|providers\.definitions/, '打开新增弹窗不得加载供应商详情')
assert.match(providerSelectSource, /ensureProviderDefinition\(providerCode, systemAccountId\)/, '只有用户选择供应商后才加载该供应商详情')
assert.match(providerSelectSource, /applyProviderDefinitionDefaultsAfterLoad\(/, '供应商选择流程必须在 detail 返回后安全回填权威默认值')
assert.match(accountEditFormSource, /mergeAccountProviderDefinitions\([\s\S]*FALLBACK_PROVIDERS/, '内置供应商在详情未加载时必须使用本地常量保持表单可用')
assert.match(accountEditFormSource, /const availableProviders = computed\(mergedProviderDefinitions\)/, '表单展示与默认值必须共用同一套供应商定义合并逻辑')
assert.match(defaultFormSource, /defaultAccountForm\(providerCode, type, mergedProviderDefinitions\(\), providerProtocolProfileId\)/, '表单默认值必须使用已合并的完整供应商定义')
assert.doesNotMatch(defaultFormSource, /defaultAccountForm\([^\n]*options\.providers\.value/, '表单默认值不得直接使用缺少协议档案的轻量供应商 options')
assert.match(mergedDefinitionsSource, /\[\.\.\.FALLBACK_PROVIDERS, \.\.\.loadedDefinitions\]/, '已加载 definition 必须覆盖内置回退定义')
assert.match(mergedDefinitionsSource, /return \{\s*\.\.\.definition,/, '合并结果必须保留完整 definition 的 Base URL、协议档案和账户类型能力')
assert.match(accountsViewSource, /providerDefinitions,[\s\S]*ensureProviderDefinition,[\s\S]*useAccountEditForm\([\s\S]*ensureProviderDefinition,[\s\S]*providerDefinitions,/, '账户页必须把按需详情状态和加载入口接入编辑表单')

assert.match(providerResourceSource, /options\.includeDefinitions[\s\S]*api\.providers\.definitions/, '完整 definitions 标志必须路由到 definitions API')
assert.match(providerResourceSource, /api\.providers\.options/, '轻量路径必须路由到 provider options API')

await verifyAsyncProviderDefinitionDefaults()

console.log('账户供应商定义按需加载回归通过：列表首屏零供应商 options，筛选展开才加载候选，用户选择后只读取当前供应商详情')

async function verifyAsyncProviderDefinitionDefaults(): Promise<void> {
  const initialDefaults = providerDefaults({
    providerProtocolProfileId: 'fallback-profile',
    baseUrl: 'https://fallback.example/v1'
  })
  const resolvedDefaults = providerDefaults({
    providerProtocolProfileId: 'server-profile',
    baseUrl: 'https://server.example/v1'
  })
  const form = providerDefaults(initialDefaults)
  const definitionDeferred = deferred<ProviderDefinition | undefined>()
  const applyPromise = applyProviderDefinitionDefaultsAfterLoad({
    ensureDefinition: () => definitionDeferred.promise,
    form,
    initialDefaults,
    isCurrent: () => true,
    resolvedDefaults: () => resolvedDefaults
  })

  assert.equal(form.baseUrl, initialDefaults.baseUrl, '详情返回前不得提前应用未加载的默认值')
  definitionDeferred.resolve({ code: 'gpt' } as ProviderDefinition)
  await applyPromise
  assert.deepEqual(form, resolvedDefaults, '详情返回后必须用权威 definition 的 profile 和 Base URL 替换 fallback 默认值')

  const editedForm = providerDefaults(initialDefaults)
  const editedDeferred = deferred<ProviderDefinition | undefined>()
  const editedApplyPromise = applyProviderDefinitionDefaultsAfterLoad({
    ensureDefinition: () => editedDeferred.promise,
    form: editedForm,
    initialDefaults,
    isCurrent: () => true,
    resolvedDefaults: () => resolvedDefaults
  })
  editedForm.baseUrl = 'https://user.example/v1'
  editedDeferred.resolve({ code: 'gpt' } as ProviderDefinition)
  await editedApplyPromise
  assert.equal(editedForm.baseUrl, 'https://user.example/v1', '详情请求期间的用户输入不得被异步回填覆盖')
  assert.equal(editedForm.providerProtocolProfileId, 'fallback-profile', '用户已修改默认字段时不得部分回填造成混合状态')
}

function providerDefaults(overrides: Partial<{
  providerProtocolProfileId: string
  type: 'api_key'
  baseUrl: string
  clientCompatibility: 'openai_standard'
  supportedEndpointModes: ['chat_json']
  healthCheckEndpointMode: 'chat_json'
  oauthMode: 'manual'
}> = {}) {
  return {
    providerProtocolProfileId: 'fallback-profile',
    type: 'api_key' as const,
    baseUrl: 'https://fallback.example/v1',
    clientCompatibility: 'openai_standard' as const,
    supportedEndpointModes: ['chat_json'] as ['chat_json'],
    healthCheckEndpointMode: 'chat_json' as const,
    oauthMode: 'manual' as const,
    ...overrides
  }
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolvePromise!: (value: T) => void
  const promise = new Promise<T>((resolve) => {
    resolvePromise = resolve
  })
  return { promise, resolve: resolvePromise }
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `未找到源码起点：${start}`)
  assert.notEqual(endIndex, -1, `未找到源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
