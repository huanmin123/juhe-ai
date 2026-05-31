import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { maxSystemTeamActiveGrantCount, maxSystemTeamMemberBatchSize, maxSystemTeamMembersPerTeam } from '../../storage/system-team-limits.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-team-fanout-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-team-fanout-boundary-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const systemAccountIds = seedSystemAccounts(maxSystemTeamMembersPerTeam + 1, 'fanout_member')

  const batchTeam = repositories.createSystemTeam({ name: '团队成员批量上限' }, access)
  assert.throws(
    () => repositories.addSystemTeamMembers(batchTeam.id, { systemAccountIds: systemAccountIds.slice(0, maxSystemTeamMemberBatchSize + 1) }, access),
    new RegExp(`单次最多添加 ${maxSystemTeamMemberBatchSize} 个团队成员`),
    '团队成员单次添加数量必须有固定上限'
  )

  const capacityTeam = repositories.createSystemTeam({ name: '团队成员总量上限' }, access)
  repositories.addSystemTeamMembers(capacityTeam.id, { systemAccountIds: systemAccountIds.slice(0, maxSystemTeamMembersPerTeam) }, access)
  assert.throws(
    () => repositories.addSystemTeamMembers(capacityTeam.id, { systemAccountIds: [systemAccountIds[maxSystemTeamMembersPerTeam]] }, access),
    new RegExp(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员`),
    '单个团队成员总数必须有固定上限'
  )

  const grantTeam = repositories.createSystemTeam({ name: '团队授权展开上限' }, access)
  repositories.addSystemTeamMembers(grantTeam.id, { systemAccountIds: [systemAccountIds[0]] }, access)
  for (let index = 0; index < maxSystemTeamActiveGrantCount; index += 1) {
    const group = repositories.createGroup({
      name: `团队授权展开分组 ${String(index).padStart(2, '0')}`,
      providerCode: 'openai',
      enabled: true
    }, access)
    repositories.createResourceAuthorization({
      resourceType: 'group',
      resourceId: group.id,
      granteeType: 'team',
      granteeId: grantTeam.id
    }, access)
  }
  const overflowGroup = repositories.createGroup({
    name: '团队授权展开溢出分组',
    providerCode: 'openai',
    enabled: true
  }, access)
  assert.throws(
    () => repositories.createResourceAuthorization({
      resourceType: 'group',
      resourceId: overflowGroup.id,
      granteeType: 'team',
      granteeId: grantTeam.id
    }, access),
    new RegExp(`单个授权团队最多支持 ${maxSystemTeamActiveGrantCount} 条有效授权`),
    '单个团队有效授权数必须有固定上限，避免成员变更时线性展开无边界增长'
  )

  console.log('系统团队展开边界回归通过：成员批量、成员总量和团队有效授权展开都有固定上限')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedSystemAccounts(count: number, prefix: string): string[] {
  const now = '2026-02-04T00:00:00.000Z'
  const database = databaseModule.getBusinessDatabase()
  const statement = database.prepare(`
    INSERT INTO system_accounts (
      id, username, display_name, role, status, password_hash,
      must_change_password, image_generation_enabled, created_at, updated_at
    ) VALUES (?, ?, ?, 'user', 'active', 'fanout-boundary-password-hash', 0, 0, ?, ?)
  `)
  const ids: string[] = []
  for (let index = 0; index < count; index += 1) {
    const id = `${prefix}_${String(index).padStart(2, '0')}`
    ids.push(id)
    statement.run(id, id, `展开边界成员 ${index}`, now, now)
  }
  return ids
}
