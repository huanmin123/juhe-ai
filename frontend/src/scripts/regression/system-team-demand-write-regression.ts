import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { api } from '@/api/client'
import { http } from '@/api/http'
import { buildSystemTeamEditPatch } from '../../views/system-teams/systemTeamEditPatch'

const baseline = {
  name: '系统团队',
  description: '原说明',
  status: 'active' as const
}

assert.deepEqual(buildSystemTeamEditPatch(baseline, {
  name: ' 系统团队 ',
  description: ' 原说明 ',
  statusActive: true
}), {}, '未修改编辑表单不得生成 PATCH 字段')

assert.deepEqual(buildSystemTeamEditPatch(baseline, {
  name: '系统团队',
  description: ' 新说明 ',
  statusActive: true
}), { description: '新说明' }, '修改说明只能提交 description')

assert.deepEqual(buildSystemTeamEditPatch(baseline, {
  name: '系统团队',
  description: '   ',
  statusActive: true
}), { description: null }, '清空说明必须显式提交 null，不能被 JSON 丢弃')

assert.deepEqual(buildSystemTeamEditPatch(baseline, {
  name: '系统团队',
  description: '原说明',
  statusActive: false
}), { status: 'disabled' }, '修改状态只能提交 status')

const calls: Array<{ url: string; payload: unknown; config: unknown }> = []
const httpPatch = http.patch
;(http as unknown as { patch: typeof http.patch }).patch = (async (url: string, payload: unknown, config: unknown) => {
  calls.push({ url, payload, config })
  return {
    data: {
      data: {
        id: 'team_demand',
        changedFields: ['description'],
        rowPatch: { description: '新说明' },
        updatedAt: '2026-07-29T01:00:01.000Z'
      }
    }
  }
}) as typeof http.patch
try {
  await api.systemTeams.update('team_demand', {
    description: '新说明',
    expectedUpdatedAt: '2026-07-29T01:00:00.000Z'
  }, { systemAccountId: 'sys_owner' })
} finally {
  ;(http as unknown as { patch: typeof http.patch }).patch = httpPatch
}
assert.deepEqual(calls, [{
  url: '/system-teams/team_demand',
  payload: {
    description: '新说明',
    expectedUpdatedAt: '2026-07-29T01:00:00.000Z'
  },
  config: { params: { systemAccountId: 'sys_owner' } }
}], '团队 API 必须只发送差异字段、打开弹窗时版本和可选作用域')

const viewSource = readFileSync(fileURLToPath(new URL('../../views/system-teams/SystemTeamsView.vue', import.meta.url)), 'utf8')
const editSource = sourceBetween(viewSource, 'function openEditTeam', "const saveTeam = submitAction('system_teams.save'")
assert.doesNotMatch(editSource, /await|\.detail\(|loadSelectedTeamDetail/, '打开编辑弹窗必须直接使用列表行，不得请求详情')
assert.match(editSource, /updatedAt:\s*team\.updatedAt/, '编辑基线必须保存列表返回的版本')

const saveSource = sourceBetween(viewSource, "const saveTeam = submitAction('system_teams.save'", 'async function openMemberModal')
assert.match(saveSource, /buildSystemTeamEditPatch\(baseline, teamForm\)/, '保存必须按打开弹窗时基线生成差异 PATCH')
assert.match(saveSource, /if \(!Object\.keys\(patch\)\.length\)[\s\S]*teamModalOpen\.value = false[\s\S]*return/, '空差异必须在请求前结束')
assert.match(saveSource, /expectedUpdatedAt:\s*baseline\.updatedAt/, 'PATCH 必须携带打开弹窗时版本')
assert.match(saveSource, /new Set\(updated\.changedFields\)/, '本地合并只能采用后端确认的字段')
assert.match(saveSource, /updatedAt:\s*updated\.updatedAt/, '本地列表必须推进后端确认的版本')
const createBranch = saveSource.indexOf('    } else {')
assert.notEqual(createBranch, -1)
assert.doesNotMatch(saveSource.slice(0, createBranch), /\bloadData\s*\(/, '编辑成功不得重新加载整页列表')
assert.match(saveSource.slice(createBranch), /await api\.systemTeams\.create[\s\S]*await loadData\(\)/, '创建成功仍应加载服务端生成的新行')

const apiSource = readFileSync(fileURLToPath(new URL('../../api/domains/systemTeams.ts', import.meta.url)), 'utf8')
assert.match(apiSource, /unwrap<SystemTeamMutationResult>\(http\.patch/, '更新 API 必须采用最小 mutation response 类型')

console.log('系统团队前端按需写回归通过：编辑零预取、字段级 PATCH、版本 CAS 与本地合并均已验证')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
