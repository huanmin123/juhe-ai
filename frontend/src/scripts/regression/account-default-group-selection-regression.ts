import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolveAccountDefaultGroupSelection } from '@/views/accounts/accountDerivedState'
import type { GroupOptionSummary } from '@/types/domain'

const anthropicCachedDefault = { id: 'anthropic-default', name: '默认 Anthropic 分组' }

assert.deepEqual(
  resolveAccountDefaultGroupSelection({
    groups: [],
    providerCode: 'anthropic',
    selectedGroupId: 'gpt-default',
    cachedDefaultGroup: anthropicCachedDefault
  }),
  anthropicCachedDefault,
  '切换到 Anthropic 且旧 GPT 选项已清空时，必须回填 Anthropic 缓存默认分组'
)

const anthropicManualGroup = group({ id: 'anthropic-manual', name: 'Anthropic 手选分组', providerCode: 'anthropic' })
assert.deepEqual(
  resolveAccountDefaultGroupSelection({
    groups: [anthropicManualGroup, group({ id: 'anthropic-default', name: '默认 Anthropic 分组', providerCode: 'anthropic', isDefault: true })],
    providerCode: 'anthropic',
    selectedGroupId: anthropicManualGroup.id,
    cachedDefaultGroup: anthropicCachedDefault
  }),
  { id: anthropicManualGroup.id, name: anthropicManualGroup.name },
  '当前供应商已加载且可管理的手选分组必须优先，不得被默认分组覆盖'
)

const anthropicLoadedDefault = group({ id: 'anthropic-loaded-default', name: '已加载 Anthropic 默认分组', providerCode: 'anthropic', isDefault: true })
assert.deepEqual(
  resolveAccountDefaultGroupSelection({
    groups: [anthropicLoadedDefault],
    providerCode: 'anthropic',
    selectedGroupId: 'gpt-default',
    cachedDefaultGroup: anthropicCachedDefault
  }),
  { id: anthropicLoadedDefault.id, name: anthropicLoadedDefault.name },
  '当前供应商已加载默认组必须优先于缓存默认组'
)

assert.equal(
  resolveAccountDefaultGroupSelection({
    groups: [],
    providerCode: 'anthropic',
    selectedGroupId: 'gpt-default'
  }),
  undefined,
  '缓存缺失且没有可用候选时必须返回 undefined'
)

const formSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/useAccountEditForm.ts', import.meta.url)),
  'utf8'
)
const groupOptionsSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/useAccountGroupOptions.ts', import.meta.url)),
  'utf8'
)

const ensureDefaultGroupSource = sourceBetween(formSource, 'function ensureDefaultGroupSelected(', 'async function openCreate()')
const cachedDefaultGroupSource = sourceBetween(formSource, 'function cachedDefaultGroupForProvider(', 'async function openEdit(')

assert.match(
  cachedDefaultGroupSource,
  /viewScope:\s*options\.isManagementView\.value \? 'admin' : 'self'[\s\S]*systemAccountId:\s*createScopeParams\.value\?\.systemAccountId[\s\S]*getCachedUserReferenceData\(referenceParams\)/,
  '缓存默认分组必须按当前 self/admin 视图与创建账户 owner 作用域读取'
)
assert.match(
  cachedDefaultGroupSource,
  /providerDefaults[\s\S]*item\.providerCode === providerCode[\s\S]*defaultGroup/,
  '缓存默认分组必须按当前供应商匹配'
)
assert.match(
  ensureDefaultGroupSource,
  /resolveAccountDefaultGroupSelection\(\{[\s\S]*groups:\s*options\.groups\.value[\s\S]*providerCode,[\s\S]*selectedGroupId:\s*form\.groupId,[\s\S]*cachedDefaultGroup:\s*cachedDefaultGroupForProvider\(providerCode\)/,
  '表单默认选择必须调用可执行的候选优先级解析函数'
)
assert.match(
  cachedDefaultGroupSource,
  /getCachedUserReferenceData\(referenceParams\)/,
  '默认选择只能读取当前作用域内存缓存，不能单独发起网络请求'
)
assert.doesNotMatch(
  cachedDefaultGroupSource,
  /\bapi\.|\bawait\b|\.load\(/,
  '缓存默认分组读取不得触发网络请求'
)
assert.match(
  groupOptionsSource,
  /watch\([\s\S]*currentCatalogScopeKey[\s\S]*resetOptions\(\)[\s\S]*flush:\s*'sync'/,
  '切换供应商仍必须同步清空旧分组选项，避免旧作用域选项泄漏'
)
assert.match(
  groupOptionsSource,
  /function resetOptions\(\)[\s\S]*groups\.value = \[\]/,
  '作用域切换仍必须清空旧分组选项内容'
)

console.log('账户默认分组选择回归通过：Anthropic 切换后的空选项窗口会回填当前作用域缓存默认组，已加载手选组与默认组保持既定优先级，旧作用域选项仍同步清空')

function group(overrides: Partial<GroupOptionSummary> & Pick<GroupOptionSummary, 'id' | 'name'>): GroupOptionSummary {
  return {
    accessType: 'owned',
    permissions: { canManageAccounts: true },
    ...overrides
  }
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `未找到源码起点：${start}`)
  assert.notEqual(endIndex, -1, `未找到源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
