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
  assert.deepEqual(Object.keys(item).sort(), ['createdAt', 'description', 'id', 'memberCount', 'name', 'status'], '列表只返回页面需要字段')
  assert.equal(item.memberCount, 1, '列表成员数应统计有效成员')

  const detail = repositories.findSystemTeamDetail(team.id, access)
  assert(detail, '详情应返回新建团队')
  assert.deepEqual(Object.keys(detail).sort(), ['createdAt', 'description', 'id', 'memberCount', 'members', 'name', 'status'], '详情只返回弹窗需要字段')
  assert.equal(detail.members.length, 1, '详情应返回有效成员')
  assert.deepEqual(Object.keys(detail.members[0] ?? {}).sort(), ['id', 'joinedAt', 'systemAccountId', 'systemAccountName'], '成员 DTO 只返回四个字段')

  console.log('系统团队 SQLite 轻量读取回归通过')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
