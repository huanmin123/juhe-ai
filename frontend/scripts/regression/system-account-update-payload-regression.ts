import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import type { SystemAccountListItem } from '../../src/types/domain'
import {
  buildSystemAccountEditablePatch,
  cloneSystemAccountEditableValues,
  hasSystemAccountEditableChanges,
  mergeSystemAccountMutation,
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

const editBranch = sourceBetween(viewSource, 'if (editingId.value) {', '} else {')
assert.match(editBranch, /buildSystemAccountEditablePatch\(editingBaseline\.value, editableValues\)/, '编辑保存必须相对打开时基线构造 delta')
assert.match(editBranch, /hasSystemAccountEditableChanges\(patch\)/, '编辑保存必须拦截 no-op')
assert.match(editBranch, /expectedUpdatedAt:\s*editingVersion\.value/, '编辑 PATCH 必须携带列表版本')
assert.doesNotMatch(editBranch, /loadData\(/, '编辑成功后不得重新加载列表')

const resetBranch = sourceBetween(viewSource, "const handleResetPassword = submitAction", '\n\nfunction searchAccounts')
assert.match(resetBranch, /expectedUpdatedAt:\s*resettingVersion\.value/, '密码重置必须携带列表版本')
assert.match(resetBranch, /mergeSystemAccountMutation/, '密码重置成功必须本地合并最小回执')
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
