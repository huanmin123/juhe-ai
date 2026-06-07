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

  const selfGrantGroup = repositories.createGroup({
    name: '禁止超级管理员自授权分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  assert.throws(
    () => repositories.createResourceAuthorization({
      resourceType: 'group',
      resourceId: selfGrantGroup.id,
      granteeType: 'system_account',
      granteeId: access.systemAccountId
    }, access),
    /不能授权给资源所有者自己/,
    '超级管理员不能把自己的分组授权给自己'
  )
  const selfGrantAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '禁止超级管理员自授权账户',
    type: 'api_key',
    groupId: selfGrantGroup.id,
    credentials: {
      api_key: 'sk-system-team-fanout-self-grant',
      base_url: 'https://api.openai.com/v1'
    }
  }, access)
  assert.throws(
    () => repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: selfGrantAccount.id,
      granteeType: 'system_account',
      granteeId: access.systemAccountId,
      targetGroupId: selfGrantGroup.id
    }, access),
    /不能授权给资源所有者自己/,
    '超级管理员不能把自己的 AI 账户授权给自己'
  )

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
      providerCode: 'gpt',
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
    providerCode: 'gpt',
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

  const missingDefaultGroupMemberId = seedSystemAccounts(1, 'fanout_missing_default_group')[0]
  const missingDefaultGroupTeam = repositories.createSystemTeam({ name: '缺默认分组团队授权' }, access)
  repositories.addSystemTeamMembers(missingDefaultGroupTeam.id, { systemAccountIds: [missingDefaultGroupMemberId] }, access)
  const accountGroup = repositories.createGroup({
    name: '缺默认分组团队授权来源分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const sourceAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '缺默认分组团队授权来源账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-team-fanout-missing-default-group',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: accountGroup.id,
    status: 'active'
  }, access)
  assert.throws(
    () => repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: sourceAccount.id,
      granteeType: 'team',
      granteeId: missingDefaultGroupTeam.id
    }, access),
    /目标用户缺少启用的默认分组/,
    '团队账号授权展开不能在成员缺默认分组时运行时补建分组'
  )
  assert.equal(openAIGroupCountForSystemAccount(missingDefaultGroupMemberId), 0, '缺默认分组属于当前数据异常，团队授权写路径不能写 groups 修复')

  console.log('系统团队展开边界回归通过：成员批量、成员总量和团队有效授权展开都有固定上限，团队账号授权不补建缺失默认分组')
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
    statement.run(id, id, `${prefix} 展开边界成员 ${index}`, now, now)
  }
  return ids
}

function openAIGroupCountForSystemAccount(systemAccountId: string): number {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT COUNT(*) AS total FROM groups WHERE system_account_id = ? AND provider_code = 'gpt'")
    .get(systemAccountId) as unknown as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
