import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import type { GroupListItem } from '@/types/domain'
import { groupPolicySummary } from '@/views/groups/groupSchedulingPolicy'
import { groupRowActions } from '@/views/groups/groupRowActions'

const groupsViewSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsView.vue', import.meta.url)), 'utf8')
const groupsListSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsList.vue', import.meta.url)), 'utf8')
const groupsApiSource = readFileSync(fileURLToPath(new URL('../../api/domains/groups.ts', import.meta.url)), 'utf8')

assert.doesNotMatch(groupsViewSource, /statusSnapshot|loadGroupStatusSnapshot|groupStatusRequestSeq/, '分组列表不得在列表响应后补发状态快照')
assert.doesNotMatch(groupsApiSource, /status-snapshot|statusSnapshot/, '分组前端 API 不得保留独立状态快照入口')
assert.match(groupsViewSource, /const page = await groupsApi\.listPage/, '分组列表必须通过列表接口获取当前页完整数据')
assert.match(groupsListSource, /groupConcurrencyText\(record\)/, '分组列表必须消费列表响应中的当前并发')
assert.match(groupsListSource, /groupKindText\(record\)/, '分组列表类型标签必须按 groupKind 区分超级分组')
assert.doesNotMatch(groupsListSource, /groupTypeText\(record\.groupType\)/, '超级分组不得被渲染成个人类型标签')
assert.match(groupsListSource, /groupPolicySummary\(record\)/, '类型列摘要必须直接消费列表项 schedulingPolicy，不再逐行拉详情')
assert.doesNotMatch(groupsListSource, /more-actions/, '分组行操作必须全部平铺展示，不得收敛到更多菜单')

const rowGroupBase = {
  id: 'group-1',
  name: 'group',
  groupKind: 'standard',
  isDefault: false,
  accessType: 'owner',
  canEdit: true,
  canDelete: true,
  canReturn: false
} as unknown as GroupListItem

// 摘要文案按契约（普通分组可选调度策略设计 §2.3）：未启用 / 阈值 N / 单 IP N（超限拒绝）。
const enabledPolicyRow = {
  ...rowGroupBase,
  schedulingPolicy: { defaultSoftConcurrency: 3, clientIpConcurrencyLimit: 5 }
} as unknown as GroupListItem
assert.equal(
  groupPolicySummary(enabledPolicyRow),
  '单账户排队阈值 3，单 IP 并发 5（超限拒绝）',
  '两项策略同时启用时摘要必须同时展示阈值与单 IP 上限'
)
assert.equal(
  groupPolicySummary({ ...enabledPolicyRow, schedulingPolicy: null }),
  '未启用可选调度策略',
  'NULL 策略摘要必须显示未启用可选调度策略'
)
assert.equal(
  groupPolicySummary({ ...enabledPolicyRow, schedulingPolicy: { defaultSoftConcurrency: 3, clientIpConcurrencyLimit: 0 } }),
  '单账户排队阈值 3',
  '仅阈值启用时摘要只显示排队阈值'
)
assert.equal(
  groupPolicySummary({ ...enabledPolicyRow, schedulingPolicy: { defaultSoftConcurrency: -1, clientIpConcurrencyLimit: 5 } }),
  '单 IP 并发 5（超限拒绝）',
  '仅 IP 限制启用时摘要必须标注超限拒绝'
)
assert.equal(
  groupPolicySummary({ ...enabledPolicyRow, groupKind: 'super' }),
  '超级分组按成员顺序展开成员分组的账号候选',
  '超级分组摘要保持成员展开文案，不受策略键影响'
)

const superGroupKeys = groupRowActions({ ...rowGroupBase, groupKind: 'super' }).map((action) => action.key)
assert.deepEqual(superGroupKeys, ['edit', 'members', 'delete'], '超级分组行操作必须平铺编辑、成员、删除')
assert.equal(new Set(superGroupKeys).size, superGroupKeys.length, '超级分组行操作不得出现重复动作')
assert.deepEqual(groupRowActions(rowGroupBase).map((action) => action.key), ['edit', 'delete'], '普通分组行操作必须平铺编辑、删除')

console.log('分组列表单接口完整响应回归通过：不再补发状态快照，当前并发和今日用量随列表返回')
