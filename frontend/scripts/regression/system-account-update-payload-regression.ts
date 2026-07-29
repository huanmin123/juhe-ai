import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import type { SystemAccountListItem } from '../../src/types/domain'
import {
  buildSystemAccountEditablePatch,
  cloneSystemAccountEditableValues,
  hasSystemAccountEditableChanges,
  mergeSystemAccountMutation,
  reconcileCreatedSystemAccount,
  reconcileSystemAccountMutationPage,
  systemAccountMatchesListKeyword,
  type SystemAccountEditableValues
} from '../../src/views/system-accounts/systemAccountEditForm'

const frontendRoot = resolve(import.meta.dirname, '../..')
const viewSource = readFileSync(resolve(frontendRoot, 'src/views/system-accounts/SystemAccountsView.vue'), 'utf8')
const apiSource = readFileSync(resolve(frontendRoot, 'src/api/domains/systemAccounts.ts'), 'utf8')

const baseline: SystemAccountEditableValues = {
  displayName: '系统账户 A',
  description: '原说明',
  role: 'user',
  status: 'active',
  mustChangePassword: true,
  imageGenerationEnabled: false,
  requestLimits: { perMinute: 10, expiresOn: '2026-08-01' }
}

const cloned = cloneSystemAccountEditableValues(baseline)
assert.notEqual(cloned.requestLimits, baseline.requestLimits, '编辑基线必须深拷贝请求限制')
const unchanged = buildSystemAccountEditablePatch(baseline, cloned)
assert.deepEqual(unchanged, {}, '未修改表单必须生成空 PATCH')
assert.equal(hasSystemAccountEditableChanges(unchanged), false, '空 PATCH 必须在前端拦截')

const descriptionPatch = buildSystemAccountEditablePatch(baseline, { ...cloned, description: '新说明' })
assert.deepEqual(descriptionPatch, { description: '新说明' }, '只改说明时只能提交 description')

const limitsPatch = buildSystemAccountEditablePatch(baseline, {
  ...cloned,
  requestLimits: { perMinute: 11, expiresOn: '2026-08-01' }
})
assert.deepEqual(limitsPatch, { requestLimits: { perMinute: 11, expiresOn: '2026-08-01' } }, '请求限制变化只能替换请求限制聚合')

const row: SystemAccountListItem = {
  id: 'sys_1',
  username: 'user_1',
  displayName: baseline.displayName,
  description: baseline.description,
  role: baseline.role,
  status: baseline.status,
  mustChangePassword: baseline.mustChangePassword,
  imageGenerationEnabled: baseline.imageGenerationEnabled,
  requestLimits: baseline.requestLimits ?? undefined,
  editVersion: 'revision-1'
}
const merged = mergeSystemAccountMutation(row, {
  id: row.id,
  updatedAt: 'revision-2',
  description: null,
  requestLimits: null
})
assert.equal(merged.editVersion, 'revision-2', '最小回执必须推进列表编辑版本')
assert.equal(merged.description, undefined, '清空说明必须在列表行归一化为缺失值')
assert.equal(merged.requestLimits, undefined, '清空请求限制必须在列表行归一化为缺失值')
assert.equal(merged.username, row.username, '最小回执不得覆盖未返回的列表字段')
assert.equal(systemAccountMatchesListKeyword(merged, '系统'), true, '更新后显示名称前缀匹配时应保留当前行')
assert.equal(systemAccountMatchesListKeyword(merged, 'user_'), true, '用户名前缀匹配时应保留当前行')
assert.equal(systemAccountMatchesListKeyword(merged, '不匹配'), false, '显示名称与用户名都不匹配时应移出筛选列表')

const neighbor: SystemAccountListItem = { ...row, id: 'sys_2', username: 'user_2', displayName: '系统账户 B', editVersion: 'revision-3' }
const firstPage = reconcileSystemAccountMutationPage([neighbor, row], { id: row.id, updatedAt: 'revision-4', displayName: '系统账户 A2' }, {
  accumulated: false,
  hasMore: false,
  keyword: '',
  page: 1,
  pageSize: 2,
  total: 2
})
assert.equal(firstPage.requiresReload, false, '首页更新应由本地排序协调')
assert.equal(firstPage.requiresBackfill, false, '完整首页更新不应请求补位')
assert.deepEqual(firstPage.items.map((item) => item.id), [row.id, neighbor.id], '首页更新行必须按 updated_at DESC 置顶')
const laterPage = reconcileSystemAccountMutationPage([neighbor, row], { id: row.id, updatedAt: 'revision-4' }, {
  accumulated: false,
  hasMore: true,
  keyword: '',
  page: 2,
  pageSize: 2,
  total: 5
})
assert.equal(laterPage.requiresReload, true, '桌面后续页的排序窗口变化必须定点重载')
assert.deepEqual(laterPage.items.map((item) => item.id), [neighbor.id, row.id], '重载前不得构造不完整的后续页窗口')
const accumulatedNeighbor: SystemAccountListItem = { ...row, id: 'sys_3', username: 'user_3', displayName: '系统账户 C', editVersion: 'revision-5' }
const accumulatedPage = reconcileSystemAccountMutationPage([neighbor, accumulatedNeighbor, row], { id: row.id, updatedAt: 'revision-6' }, {
  accumulated: true,
  hasMore: false,
  keyword: '',
  page: 2,
  pageSize: 2,
  total: 3
})
assert.equal(accumulatedPage.requiresReload, false, '移动端累计页可以在内存中重排')
assert.deepEqual(accumulatedPage.items.map((item) => item.id), [row.id, accumulatedNeighbor.id, neighbor.id], '移动端累计页不得将更新行误删，应在累计列表中置顶')
const filteredPage = reconcileSystemAccountMutationPage([neighbor, row], { id: row.id, updatedAt: 'revision-4', displayName: '新名称' }, {
  accumulated: false,
  hasMore: false,
  keyword: '系统账户 A',
  page: 1,
  pageSize: 2,
  total: 2
})
assert.equal(filteredPage.requiresReload, false, '完整首页筛除行可在本地处理')
assert.equal(filteredPage.total, 1, '筛除匹配行必须收缩当前总数')
assert.deepEqual(filteredPage.items.map((item) => item.id), [neighbor.id], '更名后不再匹配关键词时必须移出当前结果')

const created: SystemAccountListItem = { ...row, id: 'sys_new', username: 'new_user', displayName: '新账户', editVersion: 'revision-7' }
const createdFirstPage = reconcileCreatedSystemAccount([neighbor, row], created, {
  accumulated: false,
  hasMore: false,
  keyword: '',
  page: 1,
  pageSize: 2,
  total: 2
})
assert.equal(createdFirstPage.requiresReload, false)
assert.equal(createdFirstPage.total, 3)
assert.deepEqual(createdFirstPage.items.map((item) => item.id), [created.id, neighbor.id], '首页创建必须本地置顶并维持分页窗口')
const createdFiltered = reconcileCreatedSystemAccount([neighbor], created, {
  accumulated: false,
  hasMore: false,
  keyword: '系统账户',
  page: 1,
  pageSize: 2,
  total: 1
})
assert.equal(createdFiltered.total, 1, '新行不匹配筛选时不得扩大当前筛选总数')
assert.deepEqual(createdFiltered.items.map((item) => item.id), [neighbor.id])
const createdLaterPage = reconcileCreatedSystemAccount([neighbor, row], created, {
  accumulated: false,
  hasMore: true,
  keyword: '',
  page: 2,
  pageSize: 2,
  total: 5
})
assert.equal(createdLaterPage.requiresReload, true, '桌面后续页创建后必须定点刷新当前分页窗口')

const editBranch = sourceBetween(viewSource, 'if (editingId.value) {', '} else {')
assert.match(editBranch, /buildSystemAccountEditablePatch\(editingBaseline\.value, editableValues\)/, '编辑保存必须相对打开时基线构造 delta')
assert.match(editBranch, /hasSystemAccountEditableChanges\(patch\)/, '编辑保存必须拦截 no-op')
assert.match(editBranch, /expectedUpdatedAt:\s*editingVersion\.value/, '编辑 PATCH 必须携带列表版本')
assert.doesNotMatch(editBranch, /loadData\(/, '编辑成功后不得重新加载列表')
const createBranch = sourceBetween(viewSource, '} else {\n      const payload', '\n    }\n    modalOpen.value = false')
assert.match(createBranch, /const created = await api\.systemAccounts\.create\(payload\)/, '创建必须消费列表行回执')
assert.match(createBranch, /applyCreatedSystemAccount\(created\)/, '创建成功必须优先本地协调列表')
assert.doesNotMatch(createBranch, /loadData\(/, '创建成功不得无条件重新加载列表')
assert.match(viewSource, /async function applyCreatedSystemAccount[\s\S]*cancelPendingSystemAccountLoads\(\)[\s\S]*reconcileCreatedSystemAccount\(accounts\.value[\s\S]*requiresReload[\s\S]*loadData\(\{ quiet: true \}\)[\s\S]*applySystemAccountPageResult/, '只有无法本地恢复分页窗口时才允许重新加载')
assert.match(viewSource, /async function applySystemAccountMutation[\s\S]*mutationApplies[\s\S]*cancelPendingSystemAccountLoads\(\)[\s\S]*reconcileSystemAccountMutationPage\(accounts\.value[\s\S]*requiresReload[\s\S]*loadData\(\{ quiet: true \}\)[\s\S]*applySystemAccountPageResult[\s\S]*requiresBackfill[\s\S]*loadData\(\{ append: true, quiet: true \}\)/, '本地合并必须作废旧列表请求；只在桌面排序窗口变化或移动端需要补位时加载数据')

const resetBranch = sourceBetween(viewSource, "const handleResetPassword = submitAction", '\n\nfunction searchAccounts')
assert.match(resetBranch, /expectedUpdatedAt:\s*resettingVersion\.value/, '密码重置必须携带列表版本')
assert.match(resetBranch, /applySystemAccountMutation/, '密码重置成功必须本地合并最小回执')
assert.doesNotMatch(resetBranch, /loadData\(/, '密码重置成功后不得重新加载列表')

assert.match(apiSource, /payload:\s*SystemAccountPatchPayload/, '系统账户 API 必须使用字段级 PATCH 类型')
assert.match(apiSource, /unwrap<SystemAccountMutationResult>\(http\.patch/, '系统账户 PATCH 必须使用最小 mutation result')
assert.match(apiSource, /unwrap<SystemAccountListItem>\(http\.post/, '系统账户创建必须返回可直接渲染的列表行')

console.log('系统账户 delta PATCH 与最小回执前端回归通过')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
