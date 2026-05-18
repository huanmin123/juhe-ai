import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorization-quota-batch-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorization-quota-batch.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'authorization-quota-batch-records.sqlite3')
runtimeConfig.secret = 'authorization-quota-batch-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, quotaService] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/authorization-quota.service.js')
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
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({
    name: '额度批量分组',
    providerCode: 'openai'
  }, granteeAccess)

  const accountCount = 30
  const accountAuthorizationIds: string[] = []
  for (let index = 0; index < accountCount; index += 1) {
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: `额度批量账户 ${String(index).padStart(2, '0')}`,
      type: 'api_key',
      credentials: { api_key: `sk-authorization-quota-batch-${index}`, base_url: 'https://api.openai.com/v1' }
    }, ownerAccess)
    repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: account.id,
      granteeType: 'system_account',
      granteeId: grantee.id,
      remark: '额度批量回归',
      limits: {
        hourly: { enabled: true, hours: 3, limit: 1 },
        daily: { enabled: true, limit: 1 },
        weekly: { enabled: true, limit: 1 },
        monthly: { enabled: true, limit: 1 },
        total: { enabled: true, limit: 1 }
      }
    }, ownerAccess)
    const runtimeAuthorization = databaseModule.getDatabase()
      .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'account' AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1")
      .get(account.id, grantee.id) as unknown as { id?: string } | undefined
    assert(runtimeAuthorization?.id, `运行时授权不存在：${account.name}`)
    accountAuthorizationIds.push(runtimeAuthorization.id)
    const bound = repositories.setAccountGroup(account.id, granteeGroup.id, granteeAccess)
    assert(bound, `授权账户绑定分组失败：${account.name}`)
  }

  const exceededAuthorizationId = accountAuthorizationIds[accountAuthorizationIds.length - 1]
  const recordDatabase = databaseModule.getRecordDatabase()
  const now = new Date()
  const statDate = now.toISOString().slice(0, 10)
  insertUsageTotal(recordDatabase, owner.id, 'account_authorization', exceededAuthorizationId, 5)
  insertUsageDaily(recordDatabase, owner.id, 'account_authorization', exceededAuthorizationId, statDate, 5)
  insertUsageHourlyWindow(recordDatabase, owner.id, 'account_authorization', exceededAuthorizationId, 3, 5)

  quotaService.clearAuthorizationQuotaCache()
  const businessDatabase = databaseModule.getDatabase()
  const originalBusinessPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  const originalRecordPrepare = recordDatabase.prepare.bind(recordDatabase) as typeof recordDatabase.prepare
  let authorizationSelects = 0
  let grantSelects = 0
  let usageSelects = 0
  businessDatabase.prepare = ((sql: string) => {
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+resource_authorizations\b/i.test(sql)) {
      authorizationSelects += 1
    }
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+resource_authorization_grants\b/i.test(sql)) {
      grantSelects += 1
    }
    return originalBusinessPrepare(sql)
  }) as typeof businessDatabase.prepare
  recordDatabase.prepare = ((sql: string) => {
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+usage_(stats|quota)_/i.test(sql)) {
      usageSelects += 1
    }
    return originalRecordPrepare(sql)
  }) as typeof recordDatabase.prepare

  try {
    const decisions = quotaService.checkGatewayAuthorizationQuotaBatchByIds({
      accounts: accountAuthorizationIds.map((authorizationId, index) => ({
        accountId: `account-${index}`,
        accountAuthorizationId: authorizationId
      })),
      now
    })
    assert.equal(decisions.length, accountCount, '批量额度检查应返回每个候选账号的判定')
    assert(decisions.slice(0, -1).every((decision) => decision.allowed), '未超限授权账号应允许调度')
    assert.equal(decisions.at(-1)?.allowed, false, '超限授权账号应被拒绝调度')
    assert.equal(authorizationSelects, 1, '授权额度批量检查应批量读取授权主表')
    assert.equal(grantSelects, 0, '没有团队授权来源时不应读取团队授权表')
    assert(usageSelects <= 5, `用量窗口查询应保持常量，实际 ${usageSelects}`)
  } finally {
    businessDatabase.prepare = originalBusinessPrepare
    recordDatabase.prepare = originalRecordPrepare
  }

  console.log('授权额度批量检查回归通过')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
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

function insertUsageHourlyWindow(database: ReturnType<typeof databaseModule.getRecordDatabase>, systemAccountId: string, scopeType: string, scopeId: string, windowHours: number, totalCost: number) {
  database.prepare(`
    INSERT INTO usage_quota_hourly_windows (
      system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?)
  `).run(systemAccountId, scopeType, scopeId, windowHours, totalCost, new Date().toISOString())
}
