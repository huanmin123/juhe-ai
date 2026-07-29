import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-team-lightweight-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-team-lightweight-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const account = repositories.createSystemAccount({
    username: 'system_team_lightweight_member',
    displayName: '轻量成员',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const team = repositories.createSystemTeam({ name: '轻量团队', description: '列表说明' }, access)
  repositories.addSystemTeamMembers(team.id, { systemAccountIds: [account.id] }, access)

  const list = repositories.listSystemTeamsPage(access, { page: 1, pageSize: 20 })
  const item = list.items.find((candidate) => candidate.id === team.id)
  assert(item, '列表应返回新建团队')
  assert.deepEqual(Object.keys(item).sort(), ['createdAt', 'description', 'id', 'memberCount', 'name', 'status', 'updatedAt'], '列表只返回页面需要字段与编辑 CAS 版本')
  assert.equal(item.memberCount, 1, '列表成员数应统计有效成员')

  const detail = repositories.findSystemTeamDetail(team.id, access)
  assert(detail, '详情应返回新建团队')
  assert.deepEqual(Object.keys(detail).sort(), ['createdAt', 'description', 'id', 'memberCount', 'name', 'status', 'updatedAt'], '基础详情不得提前返回成员集合')
  const members = repositories.listSystemTeamMembers(team.id, access)
  assert(members, '成员接口应返回新建团队成员')
  assert.deepEqual(Object.keys(members).sort(), ['id', 'items', 'memberCount', 'updatedAt'], '成员集合必须走独立按需响应')
  assert.equal(members.items.length, 1, '成员接口应返回有效成员')
  assert.deepEqual(Object.keys(members.items[0] ?? {}).sort(), ['id', 'joinedAt', 'systemAccountId', 'systemAccountName'], '成员 DTO 只返回四个字段')
  const activeMemberId = members.items[0]?.id
  assert(activeMemberId)
  repositories.removeSystemTeamMember(team.id, activeMemberId, access)
  const history = repositories.listSystemTeamMemberHistory(team.id, { page: 1, pageSize: 20 }, access)
  assert(history, '历史成员接口应返回团队历史')
  assert.deepEqual(Object.keys(history).sort(), ['hasMore', 'id', 'items', 'page', 'pageSize', 'total'], '历史成员必须使用独立分页响应')
  assert(history.items.every((candidate) => candidate.status === 'removed'), '历史成员接口不得混入当前有效成员')
  const removed = history.items.find((candidate) => candidate.id === activeMemberId)
  assert.deepEqual(Object.keys(removed ?? {}).sort(), ['id', 'joinedAt', 'removedAt', 'status', 'systemAccountId', 'systemAccountName'], '历史成员 DTO 只返回展示字段')
  assert.equal(removed?.status, 'removed')

  console.log('系统团队 SQLite 轻量读取回归通过')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
