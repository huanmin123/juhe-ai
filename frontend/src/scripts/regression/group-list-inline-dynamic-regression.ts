import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import type { GroupListItem } from '@/types/domain'
import { groupRowActions } from '@/views/groups/groupRowActions'

const groupsViewSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsView.vue', import.meta.url)), 'utf8')
const groupsListSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsList.vue', import.meta.url)), 'utf8')
const groupsApiSource = readFileSync(fileURLToPath(new URL('../../api/domains/groups.ts', import.meta.url)), 'utf8')

assert.doesNotMatch(groupsViewSource, /statusSnapshot|loadGroupStatusSnapshot|groupStatusRequestSeq/, '分组列表不得在列表响应后补发状态快照')
assert.doesNotMatch(groupsApiSource, /status-snapshot|statusSnapshot/, '分组前端 API 不得保留独立状态快照入口')
assert.match(groupsViewSource, /const page = await groupsApi\.listPage/, '分组列表必须通过列表接口获取当前页完整数据')
assert.match(groupsListSource, /groupConcurrencyText\(record\)/, '分组列表必须消费列表响应中的当前并发')
assert.match(groupsListSource, /groupKindText\(record\)/, '分组列表类型标签必须按 groupKind 区分超级分组')
assert.doesNotMatch(groupsListSource, /groupTypeText\(record\.groupType\)/, '超级分组不得被渲染成个人/高并发类型标签')
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
const superGroupKeys = groupRowActions({ ...rowGroupBase, groupKind: 'super' }).map((action) => action.key)
assert.deepEqual(superGroupKeys, ['edit', 'members', 'delete'], '超级分组行操作必须平铺编辑、成员、删除')
assert.equal(new Set(superGroupKeys).size, superGroupKeys.length, '超级分组行操作不得出现重复动作')
assert.deepEqual(groupRowActions(rowGroupBase).map((action) => action.key), ['edit', 'delete'], '普通分组行操作必须平铺编辑、删除')

console.log('分组列表单接口完整响应回归通过：不再补发状态快照，当前并发和今日用量随列表返回')
