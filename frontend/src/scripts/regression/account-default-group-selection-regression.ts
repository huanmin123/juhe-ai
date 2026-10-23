import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { groupOptionsFrom, resolveAccountDefaultGroupSelection } from '@/views/accounts/accountDerivedState'
import type { GroupOptionSummary } from '@/types/domain'

// 新契约：分组实体的供应商维度已退役（GroupOptionSummary 无 providerCode），
// 候选不做供应商过滤；预选优先级 = 已选可管理分组 > isDefault 系统默认分组 >
// 用户引用数据 providerDefaults 默认分组 > 首个可管理分组；供应商切换不清空候选。

const referenceDataDefaultGroup = { id: 'ref-default', name: '用户引用数据默认分组' }

// 跨供应商历史分组在候选列表中并存且全部可见。
const crossProviderGroups = [
  group({ id: 'gpt-group', name: 'GPT 分组' }),
  group({ id: 'anthropic-group', name: 'Anthropic 分组' }),
  group({ id: 'deepseek-group', name: 'DeepSeek 分组' })
]
assert.deepEqual(
  groupOptionsFrom(crossProviderGroups).map((option) => option.value),
  ['gpt-group', 'anthropic-group', 'deepseek-group'],
  '分组实体供应商维度退役后，跨供应商候选必须全部可见'
)

// 供应商切换不清空候选：已选可管理分组不受供应商维度限制，保持预选。
assert.deepEqual(
  resolveAccountDefaultGroupSelection({
    groups: crossProviderGroups,
    providerCode: 'anthropic',
    selectedGroupId: 'gpt-group'
  }),
  { id: 'gpt-group', name: 'GPT 分组' },
  '切换供应商后已选可管理分组必须保持预选，不得因供应商切换清空候选'
)

// 优先级 1：已选可管理分组 > isDefault 系统默认分组 > 用户引用数据默认分组。
const manualGroup = group({ id: 'manual-group', name: '手选分组' })
assert.deepEqual(
  resolveAccountDefaultGroupSelection({
    groups: [manualGroup, group({ id: 'system-default', name: '系统默认分组', isDefault: true })],
    providerCode: 'anthropic',
    selectedGroupId: manualGroup.id,
    cachedDefaultGroup: referenceDataDefaultGroup
  }),
  { id: manualGroup.id, name: manualGroup.name },
  '已选可管理分组必须优先，不得被系统默认分组或引用数据默认分组覆盖'
)

// 优先级 2：isDefault 系统默认分组 > 用户引用数据默认分组。
const loadedDefault = group({ id: 'system-default', name: '系统默认分组', isDefault: true })
assert.deepEqual(
  resolveAccountDefaultGroupSelection({
    groups: [loadedDefault],
    providerCode: 'anthropic',
    selectedGroupId: 'gone-group',
    cachedDefaultGroup: referenceDataDefaultGroup
  }),
  { id: loadedDefault.id, name: loadedDefault.name },
  '系统默认分组必须优先于用户引用数据默认分组'
)

// 优先级 3：候选缺失时回填用户引用数据 providerDefaults 默认分组（按供应商存的用户偏好）。
assert.deepEqual(
  resolveAccountDefaultGroupSelection({
    groups: [],
    providerCode: 'anthropic',
    selectedGroupId: 'gone-group',
    cachedDefaultGroup: referenceDataDefaultGroup
  }),
  referenceDataDefaultGroup,
  '候选缺失时必须回填用户引用数据默认分组'
)

// 优先级 4：首个可管理分组兜底；引用数据默认分组仍优先于它。
const firstGroup = group({ id: 'first-group', name: '首个可管理分组' })
assert.deepEqual(
  resolveAccountDefaultGroupSelection({ groups: [firstGroup], providerCode: 'deepseek' }),
  { id: firstGroup.id, name: firstGroup.name },
  '无默认组且无引用数据默认分组时必须预选首个可管理分组（创建向导默认选中）'
)
const cachedFirstGroup = { id: 'cached-first', name: '引用数据默认分组' }
assert.deepEqual(
  resolveAccountDefaultGroupSelection({
    groups: [firstGroup],
    providerCode: 'deepseek',
    cachedDefaultGroup: cachedFirstGroup
  }),
  cachedFirstGroup,
  '引用数据默认分组必须仍优先于首个可管理分组兜底'
)

// 空串守卫：未选供应商不预选（保留原守卫行为）；候选与缓存皆缺失时返回 undefined。
assert.equal(
  resolveAccountDefaultGroupSelection({ groups: crossProviderGroups, providerCode: '' }),
  undefined,
  '未选供应商时不得预选分组'
)
assert.equal(
  resolveAccountDefaultGroupSelection({ groups: [], providerCode: 'anthropic' }),
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
  '用户引用数据默认分组必须按当前供应商匹配（per-provider 用户偏好，不是分组实体属性）'
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

console.log('账户默认分组选择回归通过：分组候选无供应商过滤（跨供应商候选全部可见、供应商切换不清空已选），预选优先级为已选可管理分组 > 系统默认分组 > 引用数据 per-provider 默认分组 > 首个可管理分组，旧作用域选项仍同步清空')

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
