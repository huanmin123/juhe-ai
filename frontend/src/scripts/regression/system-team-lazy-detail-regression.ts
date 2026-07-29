import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const source = readFileSync(fileURLToPath(new URL('../../views/system-teams/SystemTeamsView.vue', import.meta.url)), 'utf8')
const openMemberSource = sourceBetween(source, 'function openMemberModal', 'function handleTeamAction')
assert.match(openMemberSource, /memberModalOpen\.value\s*=\s*true/, '成员弹窗应先打开壳再异步加载详情')
assert.doesNotMatch(openMemberSource, /void loadMemberOptions\(\)/, '成员弹窗打开时不得预取候选账户')
assert.match(openMemberSource, /loadSelectedTeamMembers\(team\.id, generation\)/, '成员弹窗只能在点击后加载当前团队成员')
assert.match(source, /@dropdown-visible-change="handleMemberOptionsDropdown"/, '成员候选必须保留下拉展开触发')
assert.doesNotMatch(source, /record\.activeMemberCount/, '团队列表不得依赖 activeMemberCount')
assert.doesNotMatch(source, /systemTeamsApi\.detail\(teamId/, '成员弹窗不得通过基础详情端点夹带成员')
assert.match(source, /systemTeamsApi\.members\(teamId, teamScopeParams\.value\)/, '成员弹窗必须使用独立成员端点')
assert.match(source, /memberDetailGeneration === generation/, '成员详情必须使用 generation 防止旧响应覆盖当前团队')
assert.match(source, /watch\(memberModalOpen,[\s\S]*memberDetailGeneration \+= 1/, '关闭成员弹窗必须作废在途详情请求')

const addMembersSource = sourceBetween(source, "const addMembers = submitAction('system_teams.add_members'", "const removeMember = submitAction('system_teams.remove_member'")
assert.match(addMembersSource, /expectedUpdatedAt/, '添加成员必须携带成员快照版本')
assert.match(addMembersSource, /result\.addedMembers/, '添加成员成功必须消费最小增量响应')
assert.doesNotMatch(addMembersSource, /loadData\(|loadSelectedTeamMembers\(/, '添加成员成功不得重载列表或成员详情')

const removeMemberSource = sourceBetween(source, "const removeMember = submitAction('system_teams.remove_member'", 'async function loadSelectedTeamMembers')
assert.match(removeMemberSource, /expectedUpdatedAt/, '移除成员必须携带成员快照版本')
assert.match(removeMemberSource, /result\.removedMemberId/, '移除成员成功必须消费最小增量响应')
assert.doesNotMatch(removeMemberSource, /loadData\(|loadSelectedTeamMembers\(/, '移除成员成功不得重载列表或成员详情')

const conflictSource = sourceBetween(source, 'async function refreshTeamAfterMemberConflict', 'async function refreshListAfterConflict')
assert.match(conflictSource, /loadData\(\{ quiet: true \}\)/, '成员版本冲突才允许校准团队列表')
assert.match(conflictSource, /loadSelectedTeamMembers\(teamId/, '成员版本冲突才允许校准成员详情')

console.log('系统团队成员详情与候选按需加载回归通过：独立 members 端点、generation fencing 和 mutation 零重载均已覆盖')

function sourceBetween(value: string, start: string, end: string): string {
  const startIndex = value.indexOf(start)
  const endIndex = value.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return value.slice(startIndex, endIndex)
}
