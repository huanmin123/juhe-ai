import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import type { SystemAccountListItem } from '../../src/types/domain'
import {
  buildSystemAccountEditablePatch,
  cloneSystemAccountEditableValues,
  hasSystemAccountEditableChanges,
  mergeSystemAccountMutation,
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
const firstPage = reconcileSystemAccountMutationPage([neighbor, row], { id: row.id, updatedAt: 'revision-4', displayName: '系统账户 A2' }, { keyword: '', page: 1, pageSize: 2 })
assert.equal(firstPage.disposition, 'moved_to_first')
assert.deepEqual(firstPage.items.map((item) => item.id), [row.id, neighbor.id], '首页更新行必须按 updated_at DESC 置顶')
const laterPage = reconcileSystemAccountMutationPage([neighbor, row], { id: row.id, updatedAt: 'revision-4' }, { keyword: '', page: 2, pageSize: 2 })
assert.equal(laterPage.disposition, 'relocated_to_first_page')
assert.deepEqual(laterPage.items.map((item) => item.id), [neighbor.id], '后续页更新行已迁入首页，不得留在旧页')
const accumulatedNeighbor: SystemAccountListItem = { ...row, id: 'sys_3', username: 'user_3', displayName: '系统账户 C', editVersion: 'revision-5' }
const accumulatedPage = reconcileSystemAccountMutationPage([neighbor, accumulatedNeighbor, row], { id: row.id, updatedAt: 'revision-6' }, { keyword: '', page: 2, pageSize: 2 })
assert.equal(accumulatedPage.disposition, 'moved_to_first')
assert.deepEqual(accumulatedPage.items.map((item) => item.id), [row.id, neighbor.id, accumulatedNeighbor.id], '移动端累计页不得将更新行误删，应在累计列表中置顶')
const filteredPage = reconcileSystemAccountMutationPage([neighbor, row], { id: row.id, updatedAt: 'revision-4', displayName: '新名称' }, { keyword: '系统账户 A', page: 1, pageSize: 2 })
assert.equal(filteredPage.disposition, 'filtered_out')
assert.deepEqual(filteredPage.items.map((item) => item.id), [neighbor.id], '更名后不再匹配关键词时必须移出当前结果')

const editBranch = sourceBetween(viewSource, 'if (editingId.value) {', '} else {')
assert.match(editBranch, /buildSystemAccountEditablePatch\(editingBaseline\.value, editableValues\)/, '编辑保存必须相对打开时基线构造 delta')
assert.match(editBranch, /hasSystemAccountEditableChanges\(patch\)/, '编辑保存必须拦截 no-op')
assert.match(editBranch, /expectedUpdatedAt:\s*editingVersion\.value/, '编辑 PATCH 必须携带列表版本')
assert.doesNotMatch(editBranch, /loadData\(/, '编辑成功后不得重新加载列表')
assert.match(viewSource, /async function applySystemAccountMutation[\s\S]*invalidatePendingLoads\(\)[\s\S]*reconcileSystemAccountMutationPage\(accounts\.value[\s\S]*disposition === 'filtered_out'[\s\S]*accumulatedMobilePages[\s\S]*pagination\.total = Math\.max\(0, pagination\.total - 1\)[\s\S]*mobileHasMore\.value[\s\S]*await loadData\(\{ quiet: true \}\)[\s\S]*disposition === 'relocated_to_first_page'[\s\S]*await loadData\(\{ quiet: true \}\)[\s\S]*accounts\.value = reconciliation\.items/, '本地合并必须作废旧列表请求；首页和移动端局部协调，桌面筛选补位或后续页排序窗口变化时才刷新')

const resetBranch = sourceBetween(viewSource, "const handleResetPassword = submitAction", '\n\nfunction searchAccounts')
assert.match(resetBranch, /expectedUpdatedAt:\s*resettingVersion\.value/, '密码重置必须携带列表版本')
assert.match(resetBranch, /applySystemAccountMutation/, '密码重置成功必须本地合并最小回执')
assert.doesNotMatch(resetBranch, /loadData\(/, '密码重置成功后不得重新加载列表')

assert.match(apiSource, /payload:\s*SystemAccountPatchPayload/, '系统账户 API 必须使用字段级 PATCH 类型')
assert.match(apiSource, /unwrap<SystemAccountMutationResult>\(http\.patch/, '系统账户 PATCH 必须使用最小 mutation result')

console.log('系统账户 delta PATCH 与最小回执前端回归通过')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
