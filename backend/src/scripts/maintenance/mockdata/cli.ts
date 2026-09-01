import type {
  AccountSummary,
  GroupSummary,
  SystemAccountSummary
} from '../../../domain/types.js'
import { runtimeConfig } from '../../../config/runtime.js'
import type { AccessScope } from '../../../storage/access-scope.js'
import { datasetDatabasePath, getBusinessDatabase, getDatasetDatabase, getStatsDatabase, getUsageCatalogDatabase, nowIso, statsDatabasePath, usageCatalogDatabasePath } from '../../../storage/database.js'
import * as repositories from '../../../storage/repositories.js'
import { mockdataSummaryPath, writeSummary } from './summary.js'
import {
  adminUsername,
  boundedInteger,
  defaultDailyRequests,
  defaultDays,
  idPrefix,
  namePrefix,
  tracePrefix,
  type CreatedMockdata,
  type MockAccounts,
  type MockdataOptions,
  type MockGroups,
  type MockSystemAccounts,
  type MockTeams
} from './shared.js'
import { authorizationInstanceAccount } from './core/account-helpers.js'
import { updateApiKeyLastUsedAt } from './maintenance/api-key-last-used.js'
import { createMockUsers, createProxies } from './business/foundation.js'
import { createOidcProviderMockdata } from './business/oidc-provider.js'
import {
  createAnnouncements,
  createCustomProviderModels,
  createExternalSources,
  createResponseInspectionPolicies,
  seedOauthUsageSnapshots
} from './business/extras.js'
import { createAccounts, createGroups } from './core/accounts.js'
import { createApiKeys } from './core/api-keys.js'
import {
  bindAuthorizedAccountToUserGroup,
  createAuthorizations
} from './core/authorizations.js'
import { createTeams } from './core/teams.js'
import { assertMockdataCoverage } from '../mockdata-coverage.js'
import { createClientIpPolicyMockdata } from './observability/client-ip-policy.js'
import { cleanupMockdata } from './maintenance/cleanup.js'
import { rebuildDerivedCaches } from './maintenance/derived-cache.js'
import {
  createBusinessTableCoverageMockdata,
  createStatsTableCoverageMockdata
} from './maintenance/table-coverage.js'
import { createAuditMockdata, createPublicApiLogMockdata } from './observability/logs.js'
import { createMonitoringMockdata } from './observability/monitoring.js'
import { createRecordCleanupMockdata } from './records/record-cleanup.js'
import { createStorageMockdata } from './observability/storage.js'
import { createUsageMockdata } from './records/usage.js'

async function main(): Promise<void> {
  const options = parseOptions(process.argv.slice(2))
  if (options.help) {
    printHelp()
    return
  }
  assertSqliteMockdataCli()

  const startedAt = Date.now()
  const businessDatabase = getBusinessDatabase()
  const datasetDatabase = getDatasetDatabase()
  getUsageCatalogDatabase()
  const statsDatabase = getStatsDatabase()
  const admin = findAdminAccount()
  const adminAccess: AccessScope = { systemAccountId: admin.id, role: 'admin', systemAccountFilterId: admin.id }

  console.log(`开始生成 Mockdata：${options.days} 天，每天 ${options.dailyRequests} 条使用记录，资源归属 ${admin.username}`)
  cleanupMockdata(businessDatabase, datasetDatabase, statsDatabase, admin.id)

  const created = await createBusinessMockdata(admin, adminAccess)
  syncAvailabilityScheduleStatuses()
  const usageRecords = createUsageMockdata(created, options)
  const auditLogs = createAuditMockdata(usageRecords)
  const publicApiLogs = createPublicApiLogMockdata(created, options)
  // J3b model-check data is now owned by Gateway and is not fabricated in the
  // Node mockdata writer. Keep the summary shape stable for existing tooling.
  const modelCheckCounts = { runs: 0, items: 0 }
  const cleanupCounts = createRecordCleanupMockdata()
  createMonitoringMockdata(options)

  const derivedCounts = rebuildDerivedCaches(statsDatabase)
  const clientIpPolicyCounts = createClientIpPolicyMockdata(created)
  createStatsTableCoverageMockdata(created, usageRecords)
  createStorageMockdata(created, options)
  updateApiKeyLastUsedAt(usageRecords)
  assertMockdataCoverage(created, options)
  writeSummary(
    created,
    usageRecords,
    auditLogs,
    modelCheckCounts,
    {
      publicApiLogs,
      accountCleanupTargets: cleanupCounts.accountTargets,
      apiKeyCleanupTargets: cleanupCounts.apiKeyTargets,
      clientIpAggregatedRecords: derivedCounts.clientIpRecords,
      clientIpPolicies: clientIpPolicyCounts.policies,
      clientIpPolicyHits: clientIpPolicyCounts.policyHits
    },
    options,
    Date.now() - startedAt
  )

  console.log(`Mockdata 已生成：使用记录 ${usageRecords.length} 条，公开接口日志 ${publicApiLogs} 条，审计 ${auditLogs} 条，模型检测 ${modelCheckCounts.runs} 次，耗时 ${Date.now() - startedAt}ms`)
  console.log(`业务库：${runtimeConfig.databasePath}`)
  console.log(`数据集目录库：${datasetDatabasePath()}`)
  console.log(`使用记录目录库：${usageCatalogDatabasePath()}`)
  console.log(`统计结果库：${statsDatabasePath()}`)
  console.log(`摘要文件：${mockdataSummaryPath()}`)
}

function parseOptions(args: string[]): MockdataOptions {
  const options: MockdataOptions = {
    days: defaultDays,
    dailyRequests: defaultDailyRequests,
    help: false
  }
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index]
    const [name, inlineValue] = arg.includes('=') ? arg.split('=', 2) : [arg, undefined]
    const value = inlineValue ?? args[index + 1]
    if (name === '--help' || name === '-h') {
      options.help = true
      continue
    }
    if (name === '--days') {
      options.days = boundedInteger(value, defaultDays, 1, 90)
      if (inlineValue === undefined) index += 1
      continue
    }
    if (name === '--daily-requests' || name === '--requests-per-day') {
      options.dailyRequests = boundedInteger(value, defaultDailyRequests, 1, 500)
      if (inlineValue === undefined) index += 1
    }
  }
  return options
}

function printHelp(): void {
  console.log(`
用法：
  pnpm mockdata
  pnpm mockdata -- --days 31 --daily-requests 120

说明：
  - 默认清理上一批 Mockdata，仅重建 ${namePrefix} 前缀和 ${idPrefix} ID 前缀的数据。
  - 默认给 admin 用户生成近 31 天业务数据，并重建用量统计、排行、账号质量、运行日志分面和监控窗口。
  - 仅用于本地 SQLite standalone 模式；PostgreSQL 高性能模式请使用 PG smoke/readiness 脚本或后台接口准备临时数据。
  - 生成的本地网关 Key 会写入 backend/data/mockdata-summary.json。
`)
}

function assertSqliteMockdataCli(): void {
  if (runtimeConfig.databaseDriver === 'postgres' || runtimeConfig.runtimeMode === 'performance') {
    throw new Error('Mockdata 脚本仅支持本地 SQLite standalone 模式；PostgreSQL 高性能模式请使用 PG smoke/readiness 脚本或后台接口准备临时数据')
  }
}

async function createBusinessMockdata(admin: SystemAccountSummary, adminAccess: AccessScope): Promise<CreatedMockdata> {
  const unscopedAdminAccess: AccessScope = { systemAccountId: admin.id, role: adminAccess.role }
  const users = createMockUsers(admin)
  const customProviderModels = createCustomProviderModels(admin.id, users)
  const proxies = createProxies(adminAccess)
  const groups = createGroups(adminAccess, users, defaultGptGroup)
  const accounts = createAccounts(adminAccess, groups, users, proxies)
  const teams = createTeams(adminAccess, users)
  const authorizations = createAuthorizations(adminAccess, unscopedAdminAccess, groups, accounts, users, teams, defaultGptGroup)
  assertNoMockSelfAuthorizations(admin.id)
  bindAuthorizedAccountToUserGroup(authorizationInstanceAccount(accounts.proxied, users.ops), groups.opsDefault, users.ops)
  const apiKeys = createApiKeys(adminAccess, groups, users)
  const externalSources = createExternalSources()
  const responseInspectionPolicies = createResponseInspectionPolicies()
  const oidc = runtimeConfig.oidc.enabled
    ? await createOidcProviderMockdata(admin)
    : undefined
  createAnnouncements(admin.id, users)
  seedOauthUsageSnapshots(accounts)
  tuneGroupAccountBindings(groups, accounts)
  await createBusinessTableCoverageMockdata({
    users,
    groups,
    accounts,
    apiKeys,
    teams,
    authorizations,
    externalSources,
    oidc,
    responseInspectionPolicies,
    customProviderModels
  })

  return {
    users,
    groups,
    accounts,
    apiKeys,
    teams,
    authorizations,
    externalSources,
    oidc,
    responseInspectionPolicies,
    customProviderModels
  }
}

function tuneGroupAccountBindings(groups: MockGroups, accounts: MockAccounts): void {
  const now = nowIso()
  const database = getBusinessDatabase()
  const updates: Array<[number, number, GroupSummary, AccountSummary]> = [
    [1, 0, groups.main, accounts.primary],
    [0, 0, groups.main, accounts.proxied],
    [0, 0, groups.main, accounts.normal],
    [0, 0, groups.main, accounts.standardClient],
    [0, 0, groups.main, accounts.multiKeyPool],
    [1, 0, groups.highConcurrency, accounts.burstFast],
    [0, 0, groups.highConcurrency, accounts.burstImage],
    [0, 1, groups.highConcurrency, accounts.burstFallback],
    [0, 1, groups.backup, accounts.fallback],
    [0, 0, groups.oauth, accounts.oauth],
    [0, 1, groups.oauth, accounts.oauthBackup],
    [0, 0, groups.experiment, accounts.image],
    [0, 0, groups.experiment, accounts.pendingTest],
    [0, 0, groups.experiment, accounts.disabled],
    [0, 0, groups.experiment, accounts.unschedulable],
    [0, 0, groups.experiment, accounts.scheduledInactive],
    [0, 0, groups.managerMain, accounts.managerPrimary],
    [1, 0, groups.managerHighConcurrency, accounts.managerBurst]
  ]
  for (const [localSuper, localFallback, group, account] of updates) {
    database.prepare(`
      UPDATE group_accounts
      SET local_super_priority_enabled = ?,
          local_fallback_enabled = ?,
          updated_at = ?
      WHERE group_id = ? AND account_id = ?
    `).run(localSuper, localFallback, now, group.id, account.id)
  }
}

function findAdminAccount(): SystemAccountSummary {
  const admin = repositories.findSystemAccountByUsername(adminUsername)
  if (!admin) {
    throw new Error('未找到 admin 用户，请先启动或初始化后端默认数据')
  }
  if (admin.status !== 'active') {
    throw new Error('admin 用户已停用，无法生成 Mockdata')
  }
  return admin
}

function syncAvailabilityScheduleStatuses(): void {
  const accountResult = repositories.syncAccountAvailabilityScheduleStatuses()
  const apiKeyResult = repositories.syncApiKeyAvailabilityScheduleStatuses()
  console.log(
    `时间计划状态已同步：账号 ${accountResult.scanned} 个，API Key ${apiKeyResult.scanned} 个，禁用 ${accountResult.disabled + apiKeyResult.disabled} 个，启用 ${accountResult.activated + apiKeyResult.activated} 个`
  )
}

function defaultGptGroup(systemAccountId: string): GroupSummary {
  const row = getBusinessDatabase()
    .prepare("SELECT id FROM groups WHERE system_account_id = ? AND provider_code = 'gpt' AND is_default = 1 LIMIT 1")
    .get(systemAccountId) as unknown as { id?: string } | undefined
  if (!row?.id) throw new Error(`未找到默认 GPT 分组：${systemAccountId}`)
  const group = repositories.findGroupSummary(row.id, { systemAccountId, role: 'user' })
  if (!group) throw new Error(`默认 GPT 分组不可读：${systemAccountId}`)
  return group
}

function assertNoMockSelfAuthorizations(adminId: string): void {
  const database = getBusinessDatabase()
  const likeName = `${namePrefix}%`
  const selfRuntime = database.prepare(`
    SELECT id
    FROM resource_authorizations
    WHERE created_by = ?
      AND remark LIKE ?
      AND resource_owner_system_account_id = grantee_system_account_id
    LIMIT 1
  `).get(adminId, likeName) as unknown as { id?: string } | undefined
  if (selfRuntime?.id) {
    throw new Error(`Mockdata 不能生成自授权运行时记录：${selfRuntime.id}`)
  }
  const selfGrant = database.prepare(`
    SELECT id
    FROM resource_authorization_grants
    WHERE created_by = ?
      AND remark LIKE ?
      AND grantee_type = 'system_account'
      AND resource_owner_system_account_id = grantee_system_account_id
    LIMIT 1
  `).get(adminId, likeName) as unknown as { id?: string } | undefined
  if (selfGrant?.id) {
    throw new Error(`Mockdata 不能生成自授权业务记录：${selfGrant.id}`)
  }
}

await main()
