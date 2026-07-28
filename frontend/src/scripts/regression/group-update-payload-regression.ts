import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { computed } from 'vue'

import { FALLBACK_PROVIDERS } from '../../views/accounts/accountOptions'
import { useGroupFormModel } from '../../views/groups/groupFormModel'
import { defaultHighConcurrencySchedulingPolicy } from '../../views/groups/groupSchedulingPolicy'

const highConcurrencyPolicy = {
  defaultSoftConcurrency: 7,
  maxQueueWaitMs: 90_000,
  clientIpConcurrencyLimit: 2,
  clientIpConcurrencyOverflowMode: 'queue' as const,
  imageLaneMaxConcurrency: 1
}
const completeHighConcurrencyPolicy = { ...defaultHighConcurrencySchedulingPolicy, ...highConcurrencyPolicy }
const highConcurrencyGroup = {
  name: '高并发分组',
  providerCode: 'gpt',
  description: '原说明',
  enabled: true,
  groupType: 'high_concurrency' as const,
  schedulingPolicy: completeHighConcurrencyPolicy,
  accessType: 'owner' as const
}
const model = useGroupFormModel(computed(() => FALLBACK_PROVIDERS))

model.applyGroupToForm(highConcurrencyGroup)
assert.deepEqual(model.groupEditPatch(), {}, '未修改分组不得生成 PATCH 字段')

model.form.description = ' 新说明 '
assert.deepEqual(model.groupEditPatch(), { description: '新说明' }, '说明修改只能提交 description')

model.applyGroupToForm(highConcurrencyGroup)
model.form.schedulingPolicy.defaultSoftConcurrency = 8
assert.deepEqual(model.groupEditPatch(), {
  schedulingPolicy: { ...highConcurrencyPolicy, defaultSoftConcurrency: 8 }
}, '高并发策略修改不得附带名称、供应商、说明、状态或分组类型')

model.applyGroupToForm({ ...highConcurrencyGroup, accessType: 'authorized' })
model.form.name = '不允许提交的授权名称'
model.form.enabled = false
assert.deepEqual(model.groupEditPatch(), { enabled: false }, '授权分组只能依据打开时保存的 accessType 提交本地使用配置')

model.applyGroupToForm({
  name: '普通分组',
  providerCode: 'gpt',
  description: '',
  enabled: true,
  groupType: 'personal',
  accessType: 'owner'
})
model.form.groupType = 'high_concurrency'
const promotePatch = model.groupEditPatch()
assert.equal(promotePatch.groupType, 'high_concurrency')
assert.deepEqual(promotePatch.schedulingPolicy, writableSchedulingPolicy(model.form.schedulingPolicy), '切换到高并发必须同时提交当前可写策略')
assert.deepEqual(Object.keys(promotePatch).sort(), ['groupType', 'schedulingPolicy'])

model.applyGroupToForm(highConcurrencyGroup)
model.form.groupType = 'personal'
assert.deepEqual(model.groupEditPatch(), { groupType: 'personal' }, '切换到普通分组不应回传旧高并发策略')

model.resetGroupFormForCreate()
const createPayload = model.groupCreatePayload()
assert.deepEqual(Object.keys(createPayload).sort(), ['description', 'enabled', 'groupType', 'name', 'providerCode'], '普通分组创建只提交必需表单字段')

const groupsViewSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsView.vue', import.meta.url)), 'utf8')
const saveStart = groupsViewSource.indexOf("const saveGroup = submitAction('groups.save'")
const saveEnd = groupsViewSource.indexOf('async function removeGroup', saveStart)
assert.notEqual(saveStart, -1)
assert.notEqual(saveEnd, -1)
const saveSource = groupsViewSource.slice(saveStart, saveEnd)
assert.doesNotMatch(saveSource, /groups\.value\.find/, '编辑保存不得重新依赖可变列表定位目标')
assert.match(saveSource, /const target = editingTarget[\s\S]*const patch = groupEditPatch\(\)/, '编辑保存必须使用打开弹窗时保存的目标与差异基线')
assert.match(saveSource, /if \(!Object\.keys\(patch\)\.length\)[\s\S]*modalOpen\.value = false[\s\S]*return/, '空差异必须在请求前直接结束')
assert.match(saveSource, /const payload = \{ \.\.\.patch, expectedUpdatedAt: target\.updatedAt \}/, '编辑 PATCH 必须携带打开弹窗时保存的版本')
assert.match(saveSource, /new Set\(updated\.changedFields\)/, '列表局部合并只能采用后端确认的变化字段')
assert.match(saveSource, /updatedAt: updated\.updatedAt/, '保存成功必须用最小响应在本地推进列表版本')
const createBranchStart = saveSource.indexOf("    } else {\n      const payload = groupCreatePayload()")
assert.notEqual(createBranchStart, -1)
assert.doesNotMatch(saveSource.slice(0, createBranchStart), /\bloadData\s*\(/, '编辑成功已局部合并列表，不得再无条件刷新整页')

const removeStart = groupsViewSource.indexOf('async function removeGroup')
const removeEnd = groupsViewSource.indexOf('function snapshotPageState', removeStart)
assert.notEqual(removeStart, -1)
assert.notEqual(removeEnd, -1)
const removeSource = groupsViewSource.slice(removeStart, removeEnd)
assert.match(removeSource, /await groupsApi\.delete[\s\S]*removeGroupItems/, '删除成功必须从本地列表移除目标行')
assert.match(removeSource, /await groupsApi\.returnAuthorization[\s\S]*removeGroupItems/, '归还授权分组成功必须从本地列表移除目标行')
assert.doesNotMatch(removeSource, /\bloadData\s*\(/, '删除或归还成功后不得重复刷新整页')

console.log('分组前端差异 PATCH 回归通过：编辑、删除和归还成功后均按本地结果更新且不发额外列表请求')

function writableSchedulingPolicy(policy: typeof model.form.schedulingPolicy) {
  return {
    defaultSoftConcurrency: policy.defaultSoftConcurrency,
    maxQueueWaitMs: policy.maxQueueWaitMs,
    clientIpConcurrencyLimit: policy.clientIpConcurrencyLimit,
    clientIpConcurrencyOverflowMode: policy.clientIpConcurrencyLimit > 0 ? policy.clientIpConcurrencyOverflowMode : 'reject',
    imageLaneMaxConcurrency: policy.imageLaneMaxConcurrency
  }
}
