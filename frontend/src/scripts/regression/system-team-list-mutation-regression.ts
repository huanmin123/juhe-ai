import assert from 'node:assert/strict'

import type { SystemTeamListItem } from '@/types/domain'
import {
  reconcileCreatedSystemTeam,
  reconcileSystemTeamMemberMutation,
  reconcileSystemTeamPatch
} from '../../views/system-teams/systemTeamListMutation'

const activeA = team('team_a', 'Alpha', 'active', '2026-07-29T00:00:03.000Z', 2)
const activeB = team('team_b', 'Beta', 'active', '2026-07-29T00:00:02.000Z', 1)
const disabled = team('team_c', 'Charlie', 'disabled', '2026-07-29T00:00:01.000Z', 0)
const context = { accumulated: false, hasMore: false, keyword: '', page: 1, pageSize: 20, total: 3 }

const created = reconcileCreatedSystemTeam([activeA, activeB, disabled], team('team_new', 'Newest', 'active', '2026-07-29T00:00:04.000Z', 0), context)
assert.equal(created.requiresReload, false)
assert.equal(created.total, 4)
assert.deepEqual(created.items.map((item) => item.id), ['team_new', 'team_a', 'team_b', 'team_c'], '首屏已完整加载时创建应直接插入且保持服务端排序')

const createdOutsideFilter = reconcileCreatedSystemTeam([activeA], team('team_other', 'Other', 'active', '2026-07-29T00:00:04.000Z', 0), { ...context, keyword: 'Al', total: 1 })
assert.deepEqual(createdOutsideFilter, { items: [activeA], requiresReload: false, total: 1 }, '新团队不命中当前关键词时不得污染结果或 total')

const accumulatedCreate = reconcileCreatedSystemTeam(
  [activeA, activeB, disabled],
  team('team_mobile', 'Mobile', 'active', '2026-07-29T00:00:04.000Z', 0),
  { ...context, accumulated: true, page: 2, pageSize: 2, total: 4 }
)
assert.equal(accumulatedCreate.items.length, 3, '校准前不得先修改移动端累计窗口')
assert.equal(accumulatedCreate.requiresReload, true, '移动端累计列表创建会改变后续 offset，必须回到第一页校准')

const patched = reconcileSystemTeamPatch([activeA, activeB, disabled], {
  id: 'team_b',
  changedFields: ['name'],
  rowPatch: { name: 'Beta 2' },
  updatedAt: '2026-07-29T00:00:05.000Z'
}, context)
assert.equal(patched.requiresReload, false)
assert.equal(patched.items[0]?.id, 'team_b', '编辑后 updatedAt 变化应按服务端顺序局部重排')
assert.equal(patched.items[0]?.name, 'Beta 2')

const stalePatch = reconcileSystemTeamPatch([activeA], {
  id: 'team_a',
  changedFields: ['name'],
  rowPatch: { name: 'Stale' },
  updatedAt: '2026-07-28T23:59:59.000Z'
}, { ...context, total: 1 })
assert.equal(stalePatch.items[0]?.name, 'Alpha', '迟到 mutation 不得覆盖较新的列表 revision')

const filteredOut = reconcileSystemTeamPatch([activeA], {
  id: 'team_a',
  changedFields: ['name'],
  rowPatch: { name: 'Zulu' },
  updatedAt: '2026-07-29T00:00:06.000Z'
}, { ...context, keyword: 'Al', total: 1 })
assert.equal(filteredOut.requiresReload, true, '编辑后离开当前筛选窗口必须校准列表')
assert.equal(filteredOut.total, 0)

const accumulatedFilteredOut = reconcileSystemTeamPatch([activeA, activeB], {
  id: 'team_a',
  changedFields: ['name'],
  rowPatch: { name: 'Zulu' },
  updatedAt: '2026-07-29T00:00:06.000Z'
}, { ...context, accumulated: true, page: 2, pageSize: 1, keyword: 'Al', total: 2 })
assert.equal(accumulatedFilteredOut.requiresReload, false, '移动端累计列表可直接移除离开筛选的行')
assert.deepEqual(accumulatedFilteredOut.items.map((item) => item.id), ['team_b'])

const accumulatedDisable = reconcileSystemTeamPatch([activeA, activeB], {
  id: 'team_a',
  changedFields: ['status'],
  rowPatch: { status: 'disabled' },
  updatedAt: '2026-07-29T00:00:08.000Z'
}, { ...context, accumulated: true, page: 2, pageSize: 1, total: 21, hasMore: true })
assert.equal(accumulatedDisable.requiresReload, true, '累计列表 active -> disabled 会改变未加载边界，必须回第一页校准')

const memberMerged = reconcileSystemTeamMemberMutation([activeA, activeB], { id: 'team_b', memberCount: 3, updatedAt: '2026-07-29T00:00:07.000Z' }, { ...context, total: 2 })
assert.equal(memberMerged.items[0]?.id, 'team_b', '成员 mutation 推进 updatedAt 后必须局部重排')
assert.equal(memberMerged.items[0]?.memberCount, 3)
const staleMemberMerged = reconcileSystemTeamMemberMutation(memberMerged.items, { id: 'team_b', memberCount: 1, updatedAt: '2026-07-29T00:00:05.000Z' }, { ...context, total: 2 })
assert.equal(staleMemberMerged.items[0]?.memberCount, 3, '迟到成员快照不得回退列表成员数')

const laterPageMemberMutation = reconcileSystemTeamMemberMutation([activeB], { id: 'team_b', memberCount: 3, updatedAt: '2026-07-29T00:00:07.000Z' }, { ...context, page: 2, total: 21, hasMore: true })
assert.equal(laterPageMemberMutation.requiresReload, true, '桌面后续页成员 mutation 会把行移到第一页，必须校准当前窗口')

console.log('系统团队列表最小 mutation 回归通过：创建/编辑/成员数均局部合并，迟到 revision 被拒绝')

function team(id: string, name: string, status: 'active' | 'disabled', updatedAt: string, memberCount: number): SystemTeamListItem {
  return {
    id,
    name,
    status,
    memberCount,
    createdAt: '2026-07-29T00:00:00.000Z',
    updatedAt
  }
}
