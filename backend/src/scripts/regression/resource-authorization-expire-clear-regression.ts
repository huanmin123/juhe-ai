import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-resource-authorization-expire-clear-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
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

  const expiredRevokeGroup = repositories.createGroup({
    name: '授权到期后回收分组',
    providerCode: 'openai',
    enabled: true
  }, ownerAccess)
  const expiredRevokeAuthorization = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: expiredRevokeGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    expiresAt: futureExpiresAt
  }, ownerAccess)
  const alreadyExpiredAt = new Date(Date.now() - 60_000).toISOString()
  databaseModule.getDatabase()
    .prepare('UPDATE resource_authorization_grants SET expires_at = ?, updated_at = ? WHERE id = ?')
    .run(alreadyExpiredAt, alreadyExpiredAt, expiredRevokeAuthorization.id)
  const expiredListItem = repositories.listResourceAuthorizations({ status: 'expired' }, ownerAccess).find((authorization) => authorization.id === expiredRevokeAuthorization.id)
  assert.equal(expiredListItem?.status, 'expired', '到期扫描后授权应进入过期状态')
  const revokedExpiredAuthorization = repositories.revokeResourceAuthorization(expiredRevokeAuthorization.id, {}, ownerAccess)
  assert.equal(revokedExpiredAuthorization?.status, 'revoked', '到期授权仍应允许显式回收')
  assert.equal(runtimeStatus('group', expiredRevokeGroup.id, grantee.id), 'revoked', '到期授权显式回收后运行态也应标记已回收')
  assert.equal(repositories.listResourceAuthorizations({}, ownerAccess).some((authorization) => authorization.id === expiredRevokeAuthorization.id), true, '到期授权回收后默认列表仍应保留授权记录')

  const accountExpiresAt = new Date(Date.now() + 2 * 24 * 60 * 60 * 1000).toISOString()
  const validAuthorizationExpiresAt = new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString()
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '授权有效期边界账户',
    type: 'api_key',
    credentials: { api_key: 'sk-resource-authorization-expire-boundary', base_url: 'https://api.openai.com/v1' },
    accountExpiresAt
  }, ownerAccess)
  const granteeQuotaGroup = repositories.createGroup({
    name: '授权额度拦截分组',
    providerCode: 'openai',
    enabled: true
  }, granteeAccess)
  assert.throws(() => repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeQuotaGroup.id,
    expiresAt: new Date(Date.now() - 60_000).toISOString()
  }, ownerAccess), /授权到期时间不能早于当前时间/, '新增授权不应允许过期时间早于当前时间')
  assert.throws(() => repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeQuotaGroup.id,
    expiresAt: new Date(Date.parse(accountExpiresAt) + 60_000).toISOString()
  }, ownerAccess), /授权到期时间不能晚于账户到期时间/, '新增账户授权不应允许晚于账户到期时间')
  const accountAuthorization = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeQuotaGroup.id,
    expiresAt: validAuthorizationExpiresAt,
    limits: {
      daily: { enabled: true, limit: 12 },
      total: { enabled: true, limit: 30 }
    }
  }, ownerAccess)
  const quotaAuthorizedAccount = authorizedInstanceForSource(account.id, granteeAccess)
  const quotaBinding = repositories.setAccountGroup(quotaAuthorizedAccount.id, granteeQuotaGroup.id, granteeAccess)
  assert.equal(quotaBinding?.boundGroupId, granteeQuotaGroup.id, '额度账户应能先绑定到被授权人的分组')
  const migrationSourceAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '授权额度迁移源账户',
    type: 'api_key',
    credentials: { api_key: 'sk-resource-authorization-quota-source', base_url: 'https://api.openai.com/v1' }
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: migrationSourceAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeQuotaGroup.id,
    expiresAt: validAuthorizationExpiresAt
  }, ownerAccess)
  const migrationSourceAuthorizedAccount = authorizedInstanceForSource(migrationSourceAccount.id, granteeAccess)
  const sourceBinding = repositories.setAccountGroup(migrationSourceAuthorizedAccount.id, granteeQuotaGroup.id, granteeAccess)
  assert.equal(sourceBinding?.boundGroupId, granteeQuotaGroup.id, '迁移源授权账户应能绑定到同一个分组')
  assert.throws(() => repositories.updateResourceAuthorization(accountAuthorization.id, {
    expiresAt: new Date(Date.parse(accountExpiresAt) + 60_000).toISOString()
  }, ownerAccess), /授权到期时间不能晚于账户到期时间/, '修改账户授权不应允许晚于账户到期时间')
  const authorizedAccount = repositories.listAccounts(granteeAccess).find((item) => item.id === quotaAuthorizedAccount.id)
  assert.equal(authorizedAccount?.authorizationExpiresAt, validAuthorizationExpiresAt, '被授权账户列表应返回授权到期时间')
  assert.equal(authorizedAccount?.authorizationLimits?.daily?.limit, 12, '被授权账户列表应返回日限额')
  assert.equal(authorizedAccount?.authorizationLimits?.total?.limit, 30, '被授权账户列表应返回总限额')
  assert.equal(authorizedAccount?.authorizationQuotaExceeded, false, '未超限时被授权账户列表不应标记额度用完')

  const recordDatabase = databaseModule.getStatsDatabase()
  const statDate = usageStatsHelpers.todayDateKey(usageStatsHelpers.usageStatsTimezone())
  const runtimeAccountAuthorizationId = runtimeAuthorizationId('account', account.id, grantee.id)
  assert(runtimeAccountAuthorizationId, '账号授权运行时记录不存在')
  insertUsageTotal(recordDatabase, grantee.id, 'account_authorization', runtimeAccountAuthorizationId, 30)
  insertUsageDaily(recordDatabase, grantee.id, 'account_authorization', runtimeAccountAuthorizationId, statDate, 12)
  const quotaExceededAccount = repositories.listAccounts(granteeAccess).find((item) => item.id === quotaAuthorizedAccount.id)
  assert.equal(quotaExceededAccount?.authorizationQuotaExceeded, true, '授权额度用完时被授权账户列表应返回超限标记')
  assert.throws(() => repositories.updateAuthorizedAccountBindingDispatch(quotaAuthorizedAccount.id, {
    superPriorityEnabled: true
  }, granteeAccess), /授权额度已用完/, '授权额度用完后不应允许开启本地调度标记')
  assert.throws(() => repositories.migrateAccountTraffic({
    sourceAccountId: migrationSourceAuthorizedAccount.id,
    targetAccountId: quotaAuthorizedAccount.id,
    sourceStatus: 'temporary_unavailable'
  }, granteeAccess), /授权额度已用完/, '授权额度用完账户不应作为迁移目标')
  const quotaExceededTestAccount = repositories.findAccountForTest(quotaAuthorizedAccount.id, granteeAccess)
  assert(quotaExceededTestAccount, '额度用完账户仍应能被解析出来用于测试前置校验')
  assert.equal(repositories.accountTestUnavailableMessage(quotaExceededTestAccount), '授权额度已用完，当前账户不能调用', '测试接口应在实际调用前拦截授权额度用完账户')

  const ownerPausedAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '授权所有者停调账户',
    type: 'api_key',
    credentials: { api_key: 'sk-resource-authorization-owner-paused', base_url: 'https://api.openai.com/v1' },
    schedulable: false
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerPausedAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeQuotaGroup.id,
    expiresAt: validAuthorizationExpiresAt
  }, ownerAccess)
  const ownerPausedAuthorizedInstance = authorizedInstanceForSource(ownerPausedAccount.id, granteeAccess)
  const ownerPausedBinding = repositories.setAccountGroup(ownerPausedAuthorizedInstance.id, granteeQuotaGroup.id, granteeAccess)
  assert.equal(ownerPausedBinding?.boundGroupId, granteeQuotaGroup.id, '所有者停调账户的授权实例应能绑定到被授权人的分组')
  const ownerPausedAuthorizedAccount = repositories.listAccounts(granteeAccess).find((item) => item.id === ownerPausedAuthorizedInstance.id)
  assert.equal(ownerPausedAuthorizedAccount?.status, 'active', '所有者停调不应影响授权实例状态')
  assert.equal(ownerPausedAuthorizedAccount?.schedulable, true, '所有者停调不应阻断被授权实例调度')
  const ownerPausedDispatch = repositories.updateAuthorizedAccountBindingDispatch(ownerPausedAuthorizedInstance.id, {
    fallbackEnabled: true
  }, granteeAccess)
  assert.equal(ownerPausedDispatch?.fallbackEnabled, true, '所有者停调后仍应允许被授权用户管理自己的授权实例调度标记')
  const ownerPausedTestAccount = repositories.findAccountForTest(ownerPausedAuthorizedInstance.id, granteeAccess)
  assert(ownerPausedTestAccount, '所有者停调账户仍应能被解析出来用于测试前置校验')
  assert.equal(repositories.accountTestUnavailableMessage(ownerPausedTestAccount), undefined, '测试接口不应因所有者停调拦截被授权账户')

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
  const teamAuthorizedAccount = repositories.listAccounts(teamMemberAccess).find((item) => item.authorizationInstanceSourceAccountId === teamQuotaAccount.id)
  assert.equal(teamAuthorizedAccount?.authorizationQuotaExceeded, false, '团队来源授权未超限时列表不应标记额度用完')
  assert(teamAuthorizedAccount?.id, '团队来源授权应创建独立授权实例账户')
  insertUsageDaily(recordDatabase, teamMember.id, 'account_authorization_team', `${teamAuthorizedAccount.id}:${team.id}`, statDate, 5)
  const teamQuotaExceededAccount = repositories.listAccounts(teamMemberAccess).find((item) => item.id === teamAuthorizedAccount?.id)
  assert.equal(teamQuotaExceededAccount?.authorizationQuotaExceeded, true, '团队来源授权额度用完时列表应返回超限标记')

  console.log('资源授权有效期清空回归通过：grant 和 runtime 过期时间保持一致')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
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

function runtimeStatus(resourceType: string, resourceId: string, granteeSystemAccountId: string): string | undefined {
  const row = databaseModule.getDatabase()
    .prepare('SELECT status FROM resource_authorizations WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ?')
    .get(resourceType, resourceId, granteeSystemAccountId) as { status?: string } | undefined
  return row?.status
}

function authorizedInstanceForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }) {
  const account = repositories.listAccounts(access)
    .find((item) => item.authorizationInstanceSourceAccountId === sourceAccountId)
  assert(account, `被授权用户视角应能读取来源账户 ${sourceAccountId} 的授权实例`)
  return account
}

function insertUsageTotal(database: ReturnType<typeof databaseModule.getStatsDatabase>, systemAccountId: string, scopeType: string, scopeId: string, totalCost: number) {
  database.prepare(`
    INSERT INTO usage_stats_totals (
      system_account_id, scope_type, scope_id, total_cost_usd, updated_at
    ) VALUES (?, ?, ?, ?, ?)
  `).run(systemAccountId, scopeType, scopeId, totalCost, new Date().toISOString())
}

function insertUsageDaily(database: ReturnType<typeof databaseModule.getStatsDatabase>, systemAccountId: string, scopeType: string, scopeId: string, statDate: string, totalCost: number) {
  database.prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, total_cost_usd, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?)
  `).run(systemAccountId, scopeType, scopeId, statDate, totalCost, new Date().toISOString())
}
