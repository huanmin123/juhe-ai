import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { computed } from 'vue'

import { useGroupFormModel } from '../../views/groups/groupFormModel'
import { cloneGroupSchedulingPolicy, defaultGroupSchedulingPolicy } from '../../views/groups/groupSchedulingPolicy'

// 普通分组可选调度策略（普通分组可选调度策略设计 §2.2；单键化见 API Key 单 IP
// 并发限制设计 §2.4）：策略只含 defaultSoftConcurrency 单键，NULL 等价于默认 -1；
// 阈值 -1 合法、0 非法；单 IP 并发限制键已迁出分组策略。
const enabledPolicy = { defaultSoftConcurrency: 7 }
const policyGroup = {
  name: '阈值分组',
  description: '原说明',
  enabled: true,
  groupType: 'personal' as const,
  groupKind: 'standard' as const,
  schedulingPolicy: enabledPolicy,
  accessType: 'owner' as const
}
const model = useGroupFormModel(computed(() => []))

assert.throws(
  () => cloneGroupSchedulingPolicy({ defaultSoftConcurrency: 0 }),
  /defaultSoftConcurrency 不能为 0/,
  '排队阈值 0 不是合法值，关闭只能用 -1'
)
assert.throws(
  () => cloneGroupSchedulingPolicy({}),
  /缺少字段/,
  '非空策略必须包含唯一键 defaultSoftConcurrency，缺键一律无效'
)
assert.throws(
  () => cloneGroupSchedulingPolicy({ ...enabledPolicy, maxQueueWaitMs: 60000 }),
  /未知字段/,
  '旧高并发策略键已退役，不得再进入前端模型'
)
assert.throws(
  () => cloneGroupSchedulingPolicy({ defaultSoftConcurrency: 5, clientIpConcurrencyLimit: 2 }),
  /未知字段.*clientIpConcurrencyLimit/,
  '单 IP 并发限制已迁至 API Key，旧两键分组策略必须被严格单键模型拒绝'
)
assert.deepEqual(cloneGroupSchedulingPolicy(null), defaultGroupSchedulingPolicy, 'NULL 策略等价于默认 -1')

model.applyGroupToForm(policyGroup)
assert.deepEqual(model.groupEditPatch(), {}, '未修改分组不得生成 PATCH 字段')

model.form.description = ' 新说明 '
assert.deepEqual(model.groupEditPatch(), { description: '新说明' }, '说明修改只能提交 description')

model.applyGroupToForm(policyGroup)
model.form.schedulingPolicy.defaultSoftConcurrency = 8
assert.deepEqual(model.groupEditPatch(), {
  schedulingPolicy: { defaultSoftConcurrency: 8 }
}, '策略修改必须整对象提交单键，不得附带名称、说明、状态或分组类型')

model.applyGroupToForm(policyGroup)
model.form.schedulingPolicy = { defaultSoftConcurrency: -1 }
assert.deepEqual(model.groupEditPatch(), { schedulingPolicy: null }, '阈值恢复默认时 PATCH 显式发 null 清空为 NULL')

model.applyGroupToForm({
  name: '普通分组',
  description: '',
  enabled: true,
  groupType: 'personal',
  accessType: 'owner'
})
model.form.schedulingPolicy.defaultSoftConcurrency = 3
assert.deepEqual(model.groupEditPatch(), {
  schedulingPolicy: { defaultSoftConcurrency: 3 }
}, '从 NULL 启用阈值须提交单键对象')

model.applyGroupToForm({ ...policyGroup, accessType: 'authorized' })
model.form.name = '不允许提交的授权名称'
model.form.enabled = false
model.form.schedulingPolicy.defaultSoftConcurrency = 9
assert.deepEqual(
  model.groupEditPatch(),
  { enabled: false },
  '授权分组只能依据打开时保存的 accessType 提交本地使用配置，策略与名称说明不提交'
)

model.resetGroupFormForCreate()
const createPayload = model.groupCreatePayload()
assert.deepEqual(
  Object.keys(createPayload).sort(),
  ['description', 'enabled', 'groupKind', 'groupType', 'name'],
  '默认策略的普通分组创建缺省不发 schedulingPolicy（后端存 NULL）'
)
assert.equal(createPayload.enabled, true, '创建表单默认启用')
assert.equal(createPayload.groupType, 'personal', '分组类型只剩 personal')
assert.equal('providerCode' in createPayload, false, '分组创建载荷不得携带已退役的 providerCode')

model.form.schedulingPolicy.defaultSoftConcurrency = 5
const enabledCreatePayload = model.groupCreatePayload()
assert.deepEqual(
  enabledCreatePayload.schedulingPolicy,
  { defaultSoftConcurrency: 5 },
  '非默认策略创建必须单键整对象提交'
)

const groupEditModalSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupEditModal.vue', import.meta.url)), 'utf8')
assert.doesNotMatch(groupEditModalSource, /分组类型|high_concurrency/, '分组编辑弹窗不得再保留分组类型选项（普通分组可选调度策略设计 §2.3）')
assert.match(groupEditModalSource, /最大单账户排队阈值/, '普通分组编辑必须提供最大单账户排队阈值输入')
assert.doesNotMatch(groupEditModalSource, /单 IP 并发限制|clientIpConcurrencyLimit/, '分组编辑不得再提供单 IP 并发限制输入（已迁至 API Key）')
assert.match(groupEditModalSource, /-1 表示不启用；设置后达到阈值的账户在新请求排序中让位，让账户使用更平均/, '阈值帮助文案必须按契约表述')
assert.match(groupEditModalSource, /form\.groupKind !== 'super'/, '超级分组必须隐藏调度策略区块')
assert.doesNotMatch(groupEditModalSource, /scheduling-policy-grid/, '单字段策略不得保留两列 grid 半宽布局')
assert.doesNotMatch(groupEditModalSource, /form\.enabled\s*=\s*false/, '切换为超级分组不得强制把启用状态改为停用')
const superGroupMembersModalSource = readFileSync(fileURLToPath(new URL('../../views/groups/SuperGroupMembersModal.vue', import.meta.url)), 'utf8')
assert.doesNotMatch(superGroupMembersModalSource, /至少需要两个启用成员/, '超级分组成员弹窗不得提示启用需要两个以上成员')

const schedulingPolicySource = readFileSync(fileURLToPath(new URL('../../views/groups/groupSchedulingPolicy.ts', import.meta.url)), 'utf8')
assert.doesNotMatch(schedulingPolicySource, /high_concurrency|fastFirstEnabled|fallbackOnQueueEnabled|maxQueueWaitMs|clientIpConcurrencyOverflowMode|imageLaneMaxConcurrency|clientIpConcurrencyLimit/, '旧高并发策略键与已迁移的单 IP 限制键不得再出现在前端分组策略模型')
assert.doesNotMatch(schedulingPolicySource, /purple/, '高并发紫色类型标签必须删除')
assert.match(schedulingPolicySource, /未启用可选调度策略/, '摘要必须提供「未启用可选调度策略」文案')

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
assert.match(saveSource, /changedFields\.has\('schedulingPolicy'\)[\s\S]*savedSchedulingPolicy\(updated, patch\)/, '保存成功必须按 changedFields 同步列表行的策略摘要')
assert.doesNotMatch(saveSource, /changedFields\.has\('groupType'\)/, '分组类型不再可切换，列表合并不得再消费 groupType 变更')
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

console.log('分组前端差异 PATCH 回归通过：单键策略 PATCH/Create 语义、列表摘要同步与编辑、删除、归还本地合并均符合普通分组可选调度策略契约')
