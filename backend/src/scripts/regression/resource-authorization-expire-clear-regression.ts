import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-resource-authorization-expire-clear-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'resource-authorization-expire-clear-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, authorizationHelpers, usageStatsHelpers] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/resource-authorization-helpers.js'),
  import('../../storage/usage-stats-helpers.js')
])

try {
  const owner = repositories.createSystemAccount({
    username: 'authorization_expire_clear_owner',
    displayName: '授权有效期清空所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'authorization_expire_clear_grantee',
    displayName: '授权有效期清空被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const teamMember = repositories.createSystemAccount({
    username: 'authorization_expire_clear_team_member',
    displayName: '授权有效期清空团队成员',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const futureExpiresAt = '2099-01-01T00:00:00.000Z'

  const userGroup = repositories.createGroup({
    name: '授权有效期清空个人分组',
    providerCode: 'openai',
    enabled: true
  }, ownerAccess)
  const userAuthorization = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: userGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    expiresAt: futureExpiresAt
  }, ownerAccess)

  const clearedUserAuthorization = repositories.updateResourceAuthorization(userAuthorization.id, { expiresAt: null }, ownerAccess)
  assert.equal(clearedUserAuthorization?.expiresAt, undefined, '个人授权列表视图应清空过期时间')
  assert.equal(grantExpiresAt(userAuthorization.id), null, '个人授权 grant 表应清空 expires_at')
  assert.equal(runtimeExpiresAt('group', userGroup.id, grantee.id), null, '个人授权 runtime 表应同步清空 expires_at')
  assert.equal(authorizationHelpers.activeGroupAuthorization(userGroup.id, grantee.id)?.id, runtimeAuthorizationId('group', userGroup.id, grantee.id), '清空有效期后个人授权应仍可用于运行态调度')

  const team = repositories.createSystemTeam({
    name: '授权有效期清空团队',
    description: '回归测试团队'
  })
  repositories.addSystemTeamMembers(team.id, { systemAccountIds: [teamMember.id] })
  const teamGroup = repositories.createGroup({
    name: '授权有效期清空团队分组',
    providerCode: 'openai',
    enabled: true
  }, ownerAccess)
  const teamAuthorization = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: teamGroup.id,
    granteeType: 'team',
    granteeId: team.id,
    expiresAt: futureExpiresAt
  }, ownerAccess)

  const clearedTeamAuthorization = repositories.updateResourceAuthorization(teamAuthorization.id, { expiresAt: null }, ownerAccess)
  assert.equal(clearedTeamAuthorization?.expiresAt, undefined, '团队授权列表视图应清空过期时间')
  assert.equal(grantExpiresAt(teamAuthorization.id), null, '团队授权 grant 表应清空 expires_at')
  assert.equal(runtimeExpiresAt('group', teamGroup.id, teamMember.id), null, '团队授权展开到成员的 runtime 表应同步清空 expires_at')
  assert.equal(authorizationHelpers.activeGroupAuthorization(teamGroup.id, teamMember.id)?.id, runtimeAuthorizationId('group', teamGroup.id, teamMember.id), '清空有效期后团队成员授权应仍可用于运行态调度')

  const accountExpiresAt = new Date(Date.now() + 2 * 24 * 60 * 60 * 1000).toISOString()
  const validAuthorizationExpiresAt = new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString()
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '授权有效期边界账户',
    type: 'api_key',
    credentials: { api_key: 'sk-resource-authorization-expire-boundary', base_url: 'https://api.openai.com/v1' },
    accountExpiresAt
  }, ownerAccess)
  assert.throws(() => repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    expiresAt: new Date(Date.now() - 60_000).toISOString()
  }, ownerAccess), /授权到期时间不能早于当前时间/, '新增授权不应允许过期时间早于当前时间')
  assert.throws(() => repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    expiresAt: new Date(Date.parse(accountExpiresAt) + 60_000).toISOString()
  }, ownerAccess), /授权到期时间不能晚于账户到期时间/, '新增账户授权不应允许晚于账户到期时间')
  const accountAuthorization = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    expiresAt: validAuthorizationExpiresAt,
    limits: {
      daily: { enabled: true, limit: 12 },
      total: { enabled: true, limit: 30 }
    }
  }, ownerAccess)
  assert.throws(() => repositories.updateResourceAuthorization(accountAuthorization.id, {
    expiresAt: new Date(Date.parse(accountExpiresAt) + 60_000).toISOString()
  }, ownerAccess), /授权到期时间不能晚于账户到期时间/, '修改账户授权不应允许晚于账户到期时间')
  const authorizedAccount = repositories.listAccounts(granteeAccess).find((item) => item.id === account.id)
  assert.equal(authorizedAccount?.authorizationExpiresAt, validAuthorizationExpiresAt, '被授权账户列表应返回授权到期时间')
  assert.equal(authorizedAccount?.authorizationLimits?.daily?.limit, 12, '被授权账户列表应返回日限额')
  assert.equal(authorizedAccount?.authorizationLimits?.total?.limit, 30, '被授权账户列表应返回总限额')
  assert.equal(authorizedAccount?.authorizationQuotaExceeded, false, '未超限时被授权账户列表不应标记额度用完')

  const recordDatabase = databaseModule.getRecordDatabase()
  const statDate = usageStatsHelpers.todayDateKey(usageStatsHelpers.usageStatsTimezone())
  const runtimeAccountAuthorizationId = runtimeAuthorizationId('account', account.id, grantee.id)
  assert(runtimeAccountAuthorizationId, '账号授权运行时记录不存在')
  insertUsageTotal(recordDatabase, owner.id, 'account_authorization', runtimeAccountAuthorizationId, 30)
  insertUsageDaily(recordDatabase, owner.id, 'account_authorization', runtimeAccountAuthorizationId, statDate, 12)
  const quotaExceededAccount = repositories.listAccounts(granteeAccess).find((item) => item.id === account.id)
  assert.equal(quotaExceededAccount?.authorizationQuotaExceeded, true, '授权额度用完时被授权账户列表应返回超限标记')

  const teamQuotaAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '授权有效期团队额度账户',
    type: 'api_key',
    credentials: { api_key: 'sk-resource-authorization-team-quota', base_url: 'https://api.openai.com/v1' }
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: teamQuotaAccount.id,
    granteeType: 'team',
    granteeId: team.id,
    limits: {
      daily: { enabled: true, limit: 5 }
    }
  }, ownerAccess)
  const teamMemberAccess = { systemAccountId: teamMember.id, role: 'user' as const }
  const teamAuthorizedAccount = repositories.listAccounts(teamMemberAccess).find((item) => item.id === teamQuotaAccount.id)
  assert.equal(teamAuthorizedAccount?.authorizationQuotaExceeded, false, '团队来源授权未超限时列表不应标记额度用完')
  insertUsageDaily(recordDatabase, owner.id, 'account_authorization_team', `${teamQuotaAccount.id}:${team.id}`, statDate, 5)
  const teamQuotaExceededAccount = repositories.listAccounts(teamMemberAccess).find((item) => item.id === teamQuotaAccount.id)
  assert.equal(teamQuotaExceededAccount?.authorizationQuotaExceeded, true, '团队来源授权额度用完时列表应返回超限标记')

  console.log('资源授权有效期清空回归通过：grant 和 runtime 过期时间保持一致')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function grantExpiresAt(id: string): string | null {
  const row = databaseModule.getDatabase()
    .prepare('SELECT expires_at FROM resource_authorization_grants WHERE id = ?')
    .get(id) as { expires_at?: string | null } | undefined
  return row?.expires_at ?? null
}

function runtimeExpiresAt(resourceType: string, resourceId: string, granteeSystemAccountId: string): string | null {
  const row = databaseModule.getDatabase()
    .prepare('SELECT expires_at FROM resource_authorizations WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ?')
    .get(resourceType, resourceId, granteeSystemAccountId) as { expires_at?: string | null } | undefined
  return row?.expires_at ?? null
}

function runtimeAuthorizationId(resourceType: string, resourceId: string, granteeSystemAccountId: string): string | undefined {
  const row = databaseModule.getDatabase()
    .prepare('SELECT id FROM resource_authorizations WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ?')
    .get(resourceType, resourceId, granteeSystemAccountId) as { id?: string } | undefined
  return row?.id
}

function insertUsageTotal(database: ReturnType<typeof databaseModule.getRecordDatabase>, systemAccountId: string, scopeType: string, scopeId: string, totalCost: number) {
  database.prepare(`
    INSERT INTO usage_stats_totals (
      system_account_id, scope_type, scope_id, total_cost_usd, updated_at
    ) VALUES (?, ?, ?, ?, ?)
  `).run(systemAccountId, scopeType, scopeId, totalCost, new Date().toISOString())
}

function insertUsageDaily(database: ReturnType<typeof databaseModule.getRecordDatabase>, systemAccountId: string, scopeType: string, scopeId: string, statDate: string, totalCost: number) {
  database.prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, total_cost_usd, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?)
  `).run(systemAccountId, scopeType, scopeId, statDate, totalCost, new Date().toISOString())
}
