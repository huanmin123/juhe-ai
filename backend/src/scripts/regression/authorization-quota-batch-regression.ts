import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { GroupUsageAccessMetadata, OpenAIAccountSecret } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorization-quota-batch-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorization-quota-batch.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorization-quota-batch-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, quotaService] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/quota/authorization-quota.service.js')
])

try {
  const owner = repositories.createSystemAccount({
    username: 'quota_batch_owner',
    displayName: '额度批量所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'quota_batch_grantee',
    displayName: '额度批量被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const teamGrantee = repositories.createSystemAccount({
    username: 'quota_batch_team_grantee',
    displayName: '额度批量团队成员',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({
    name: '额度批量分组',
    providerCode: 'gpt'
  }, granteeAccess)
  const ownerGroup = repositories.createGroup({
    name: '额度单次分组授权资源',
    providerCode: 'gpt'
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: ownerGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '额度单次回归',
    limits: {
      hourly: { enabled: true, hours: 3, limit: 100 },
      daily: { enabled: true, limit: 100 },
      weekly: { enabled: true, limit: 100 },
      monthly: { enabled: true, limit: 100 },
      total: { enabled: true, limit: 100 }
    }
  }, ownerAccess)
  const groupAuthorization = databaseModule.getBusinessDatabase()
    .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'group' AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1")
    .get(ownerGroup.id, grantee.id) as unknown as { id?: string } | undefined
  assert(groupAuthorization?.id, '分组授权运行时记录不存在')

  const accountCount = 925
  const accountAuthorizationIds: string[] = []
  const accountInstanceIds: string[] = []
  for (let index = 0; index < accountCount; index += 1) {
    const account = repositories.createAccount({
      providerCode: 'gpt',
      name: `额度批量账户 ${String(index).padStart(2, '0')}`,
      type: 'api_key',
      credentials: { api_key: `sk-authorization-quota-batch-${index}`, base_url: 'https://api.openai.com/v1' },
      groupId: ownerGroup.id
    }, ownerAccess)
    repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: account.id,
      granteeType: 'system_account',
      granteeId: grantee.id,
      targetGroupId: granteeGroup.id,
      remark: '额度批量回归',
      limits: {
        hourly: { enabled: true, hours: 3, limit: 1 },
        daily: { enabled: true, limit: 1 },
        weekly: { enabled: true, limit: 1 },
        monthly: { enabled: true, limit: 1 },
        total: { enabled: true, limit: 1 }
      }
    }, ownerAccess)
    const runtimeAuthorization = databaseModule.getBusinessDatabase()
      .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'account' AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1")
      .get(account.id, grantee.id) as unknown as { id?: string } | undefined
    assert(runtimeAuthorization?.id, `运行时授权不存在：${account.name}`)
    const authorizedInstance = authorizedInstanceForSource(account.id, granteeAccess)
    assert.equal(authorizedInstance.accountAuthorizationId, runtimeAuthorization.id, `授权实例应绑定运行时授权：${account.name}`)
    accountAuthorizationIds.push(runtimeAuthorization.id)
    accountInstanceIds.push(authorizedInstance.id)
    const bound = repositories.setAccountGroup(authorizedInstance.id, granteeGroup.id, granteeAccess)
    assert(bound, `授权实例绑定分组失败：${account.name}`)
  }

  const exceededAuthorizationId = accountAuthorizationIds[accountAuthorizationIds.length - 1]
  const statsDatabase = databaseModule.getStatsDatabase()
  const now = new Date()
  const statDate = now.toISOString().slice(0, 10)
  insertUsageTotal(statsDatabase, grantee.id, 'account_authorization', exceededAuthorizationId, 5)
  insertUsageDaily(statsDatabase, grantee.id, 'account_authorization', exceededAuthorizationId, statDate, 5)
  insertUsageHourlyWindow(statsDatabase, grantee.id, 'account_authorization', exceededAuthorizationId, 3, 5)

  quotaService.clearAuthorizationQuotaCache()
  const businessDatabase = databaseModule.getBusinessDatabase()
  const originalBusinessPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  const originalStatsPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
  let authorizationSelects = 0
  let grantSelects = 0
  let usageSelects = 0
  businessDatabase.prepare = ((sql: string) => {
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+resource_authorizations\b/i.test(sql)) {
      authorizationSelects += 1
    }
    if (/^\s*SELECT\b/i.test(sql) && /\bresource_authorization_grants\b/i.test(sql)) {
      grantSelects += 1
    }
    return originalBusinessPrepare(sql)
  }) as typeof businessDatabase.prepare
  statsDatabase.prepare = ((sql: string) => {
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+usage_(stats|quota)_/i.test(sql)) {
      usageSelects += 1
    }
    return originalStatsPrepare(sql)
  }) as typeof statsDatabase.prepare

  try {
    const decisions = quotaService.checkGatewayAuthorizationQuotaBatchByIds({
      accounts: accountAuthorizationIds.map((authorizationId, index) => ({
        accountId: accountInstanceIds[index],
        accountAuthorizationId: authorizationId
      })),
      now
    })
    assert.equal(decisions.length, accountCount, '批量额度检查应返回每个候选账号的判定')
    assert(decisions.slice(0, -1).every((decision) => decision.allowed), '未超限授权账号应允许调度')
    assert.equal(decisions.at(-1)?.allowed, false, '超限授权账号应被拒绝调度')
    assert.equal(authorizationSelects, 2, '授权额度批量检查应按批读取授权主表，避免大候选列表撞 SQLite 参数上限')
    assert.equal(grantSelects, 0, '没有团队授权来源时不应读取团队授权表')
    assert(usageSelects <= 30, `用量窗口查询应按窗口分块批量读取，实际 ${usageSelects}`)

    quotaService.clearAuthorizationQuotaCache()
    authorizationSelects = 0
    grantSelects = 0
    usageSelects = 0
    const singleDecision = quotaService.checkGatewayAuthorizationQuotaByIds({
      groupAuthorizationId: groupAuthorization.id,
      accountAuthorizationId: accountAuthorizationIds[0],
      now
    })
    assert.equal(singleDecision.allowed, true, '单次授权额度检查应同时合并分组与账号授权额度')
    assert.equal(authorizationSelects, 1, '单次授权额度检查应复用批量读取授权主表')
    assert.equal(grantSelects, 0, '没有团队授权来源时单次检查不应读取团队授权表')
    assert(usageSelects <= 5, `单次检查用量窗口查询应保持常量，实际 ${usageSelects}`)

    quotaService.clearAuthorizationQuotaCache()
    authorizationSelects = 0
    grantSelects = 0
    usageSelects = 0
    const asyncFirstDecisions = await withDbServiceRole(() => quotaService.checkGatewayAuthorizationQuotaBatchAsync({
      groupAccess: quotaGroupAccess(groupAuthorization.id),
      accounts: [
        quotaAccount('async-manual-ok', accountAuthorizationIds[0]),
        quotaAccount('async-manual-over-limit', exceededAuthorizationId)
      ]
    }))
    assert.deepEqual(
      [asyncFirstDecisions.get('async-manual-ok')?.allowed, asyncFirstDecisions.get('async-manual-over-limit')?.allowed],
      [true, false],
      '异步授权额度检查首次应返回正确判定'
    )
    assert(authorizationSelects > 0, '异步授权额度检查首次 miss 时允许读取授权主表')
    assert(usageSelects > 0, '异步授权额度检查首次 miss 时允许读取额度窗口')
    authorizationSelects = 0
    grantSelects = 0
    usageSelects = 0
    const asyncSecondDecisions = await withDbServiceRole(() => quotaService.checkGatewayAuthorizationQuotaBatchAsync({
      groupAccess: quotaGroupAccess(groupAuthorization.id),
      accounts: [
        quotaAccount('async-manual-ok-second', accountAuthorizationIds[0]),
        quotaAccount('async-manual-over-limit-second', exceededAuthorizationId)
      ]
    }))
    assert.deepEqual(
      [asyncSecondDecisions.get('async-manual-ok-second')?.allowed, asyncSecondDecisions.get('async-manual-over-limit-second')?.allowed],
      [true, false],
      '异步授权额度检查缓存命中时仍应返回正确判定'
    )
    assert.equal(authorizationSelects, 0, '异步授权额度检查缓存命中时不应重复读取授权主表')
    assert.equal(grantSelects, 0, '异步授权额度检查缓存命中时不应重复读取团队授权表')
    assert.equal(usageSelects, 0, '异步授权额度检查缓存命中时不应重复读取额度窗口')

    const team = repositories.createSystemTeam({
      name: '额度批量团队',
      status: 'active'
    }, adminAccess)
    assert(repositories.addSystemTeamMembers(team.id, { systemAccountIds: [teamGrantee.id] }, adminAccess), '额度批量团队成员添加失败')
    const teamAccount = repositories.createAccount({
      providerCode: 'gpt',
      name: '额度批量团队授权账户',
      type: 'api_key',
      credentials: { api_key: 'sk-authorization-quota-team', base_url: 'https://api.openai.com/v1' },
      groupId: ownerGroup.id
    }, ownerAccess)
    const teamAuthorizationGrant = repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: teamAccount.id,
      granteeType: 'team',
      granteeId: team.id,
      remark: '额度批量团队授权回归',
      limits: {
        hourly: { enabled: true, hours: 3, limit: 100 },
        daily: { enabled: true, limit: 100 },
        weekly: { enabled: true, limit: 100 },
        monthly: { enabled: true, limit: 100 },
        total: { enabled: true, limit: 100 }
      }
    }, ownerAccess)
    const teamMemberAuthorization = businessDatabase
      .prepare("SELECT id, effective_source_team_id FROM resource_authorizations WHERE resource_type = 'account' AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1")
      .get(teamAccount.id, teamGrantee.id) as unknown as { id?: string; effective_source_team_id?: string | null } | undefined
    assert(teamMemberAuthorization?.id, '团队来源账号授权运行时记录不存在')
    assert.equal(teamMemberAuthorization.effective_source_team_id, team.id, '团队来源账号授权应记录来源团队')
    const teamAuthorizedInstance = authorizedInstanceForSource(teamAccount.id, { systemAccountId: teamGrantee.id, role: 'user' as const })
    assert.equal(teamAuthorizedInstance.accountAuthorizationId, teamMemberAuthorization.id, '团队来源授权实例应绑定成员运行时授权')
    insertUsageTotal(statsDatabase, teamGrantee.id, 'account_authorization_team', `${teamAuthorizedInstance.id}:${team.id}`, 150)
    insertUsageDaily(statsDatabase, teamGrantee.id, 'account_authorization_team', `${teamAuthorizedInstance.id}:${team.id}`, statDate, 150)
    insertUsageHourlyWindow(statsDatabase, teamGrantee.id, 'account_authorization_team', `${teamAuthorizedInstance.id}:${team.id}`, 3, 150)

    quotaService.clearAuthorizationQuotaCache()
    authorizationSelects = 0
    grantSelects = 0
    usageSelects = 0
    const mixedDecisions = quotaService.checkGatewayAuthorizationQuotaBatchByIds({
      groupAuthorizationId: groupAuthorization.id,
      accounts: [
        { accountId: teamAuthorizedInstance.id, accountAuthorizationId: teamMemberAuthorization.id },
        { accountId: accountInstanceIds[0], accountAuthorizationId: accountAuthorizationIds[0] },
        { accountId: accountInstanceIds[accountInstanceIds.length - 1], accountAuthorizationId: exceededAuthorizationId },
        { accountId: `${teamAuthorizedInstance.id}:duplicate`, accountAuthorizationId: teamMemberAuthorization.id },
        { accountId: 'owner-account-without-authorization' }
      ],
      now
    })
    assert.deepEqual(mixedDecisions.map((decision) => decision.allowed), [false, true, false, false, true], '混合团队/账号授权批量判定应保持输入顺序并命中团队来源额度')
    assert.equal(authorizationSelects, 2, '混合授权额度检查应批量读取授权主表和团队授权来源')
    assert.equal(grantSelects, 1, '团队来源授权应批量读取团队授权表')
    assert(usageSelects <= 5, `混合授权额度检查用量窗口查询应保持常量，实际 ${usageSelects}`)
    assert(teamAuthorizationGrant.id, '团队授权 grant 应保留可追踪 ID')
  } finally {
    businessDatabase.prepare = originalBusinessPrepare
    statsDatabase.prepare = originalStatsPrepare
  }

  console.log('授权额度批量检查回归通过')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
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

function insertUsageHourlyWindow(database: ReturnType<typeof databaseModule.getStatsDatabase>, systemAccountId: string, scopeType: string, scopeId: string, windowHours: number, totalCost: number) {
  database.prepare(`
    INSERT INTO usage_quota_hourly_windows (
      system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?)
  `).run(systemAccountId, scopeType, scopeId, windowHours, totalCost, new Date().toISOString())
}

function quotaGroupAccess(groupAuthorizationId?: string): GroupUsageAccessMetadata {
  return {
    groupOwnerSystemAccountId: 'quota_batch_owner',
    groupAccessType: 'authorized',
    groupAuthorizationId
  } as GroupUsageAccessMetadata
}

function quotaAccount(accountId: string, accountAuthorizationId?: string): OpenAIAccountSecret {
  return {
    id: accountId,
    accountAuthorizationId
  } as OpenAIAccountSecret
}

async function withDbServiceRole<T>(action: () => Promise<T>): Promise<T> {
  const previousProcessRole = runtimeConfig.processRole
  try {
    runtimeConfig.processRole = 'db-service'
    return await action()
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
}

function authorizedInstanceForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }) {
  const row = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT id, authorization_instance_authorization_id
      FROM accounts
      WHERE authorization_instance_source_account_id = ?
        AND system_account_id = ?
      LIMIT 1
    `)
    .get(sourceAccountId, access.systemAccountId) as { id?: string; authorization_instance_authorization_id?: string | null } | undefined
  assert(row?.id, `被授权用户视角应能读取来源账户 ${sourceAccountId} 的授权实例`)
  return {
    id: row.id,
    accountAuthorizationId: row.authorization_instance_authorization_id ?? undefined
  }
}
