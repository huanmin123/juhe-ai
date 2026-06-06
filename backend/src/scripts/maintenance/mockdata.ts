import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

import type {
  AccountSummary,
  ApiKeySummary,
  GroupSummary,
  ResourceAuthorizationSummary,
  SystemAccountSummary,
  SystemTeamSummary
} from '../../domain/types.js'
import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { refreshAccountQualityFromUsage } from '../../storage/account-quality.repository.js'
import { datasetDatabasePath, getBusinessDatabase, getDatasetDatabase, getStatsDatabase, nowIso, statsDatabasePath } from '../../storage/database.js'
import * as repositories from '../../storage/repositories.js'
import { createRuntimeLogsBatch } from '../../storage/runtime-logs.repository.js'
import type { AuditLogInput } from '../../storage/audit-logs.repository.js'
import type { OperationLogInput } from '../../storage/operation-logs.repository.js'
import type { RuntimeLogIndexInput } from '../../storage/runtime-logs.repository.js'
import type { UsageRecordInput } from '../../storage/usage-records.repository.js'
import { createPublicApiLog } from '../../storage/public-api-logs.repository.js'
import { createStreamInterceptPolicy } from '../../storage/stream-intercept-policy.repository.js'
import {
  aggregateClientIpStatsBatch,
  rebuildClientIpUsageRangeWindows,
  recordClientIpPolicyHits
} from '../../storage/client-ip-stats.repository.js'
import {
  builtInExternalIntegrationTestSourceId,
  builtInExternalIntegrationTestTokenId,
  createExternalIntegrationSourceAuthorization,
  createExternalIntegrationSourceToken,
  externalIntegrationScopeOptions,
  findExternalIntegrationSource,
  type CreatedExternalIntegrationSourceAuthorization
} from '../../storage/external-integration-source.repository.js'
import { deleteUsageRecordShardEntries, getUsageRecordShardDatabase, listUsageRecordShardLocations } from '../../storage/usage-record-shards.js'
import {
  aggregateUsageStatsBatch,
  refreshGroupAccountStatsCache,
  refreshUsageQuotaHourlyWindowsCache,
  refreshUsageRankSnapshots
} from '../../storage/usage-stats.repository.js'
import { hourKey, usageStatsTimezone } from '../../storage/usage-stats-helpers.js'

type Database = ReturnType<typeof getBusinessDatabase>
type SqlValue = string | number | null
type MockModelCheckLevel = 'high_confidence' | 'likely' | 'uncertain' | 'suspicious' | 'unavailable'
type MockModelCheckRunStatus = 'running' | 'completed' | 'failed' | 'canceled'
type MockModelCheckItemStatus = 'passed' | 'warning' | 'failed' | 'skipped'

interface MockdataOptions {
  days: number
  dailyRequests: number
  help: boolean
}

interface MockSystemAccounts {
  admin: SystemAccountSummary
  ops: SystemAccountSummary
  dev: SystemAccountSummary
  tester: SystemAccountSummary
  finance: SystemAccountSummary
  viewer: SystemAccountSummary
  disabled: SystemAccountSummary
}

interface MockGroups {
  main: GroupSummary
  highConcurrency: GroupSummary
  backup: GroupSummary
  oauth: GroupSummary
  experiment: GroupSummary
  empty: GroupSummary
  devDefault: GroupSummary
  opsDefault: GroupSummary
  testerDefault: GroupSummary
  financeDefault: GroupSummary
  viewerDefault: GroupSummary
}

interface MockAccounts {
  primary: AccountSummary
  proxied: AccountSummary
  normal: AccountSummary
  burstFast: AccountSummary
  burstImage: AccountSummary
  burstFallback: AccountSummary
  fallback: AccountSummary
  oauth: AccountSummary
  oauthBackup: AccountSummary
  rateLimited: AccountSummary
  temporary: AccountSummary
  error: AccountSummary
  expired: AccountSummary
}

type ApiKeyWithSecret = ApiKeySummary & { key: string }

interface MockApiKeys {
  adminMain: ApiKeyWithSecret
  adminHighConcurrency: ApiKeyWithSecret
  adminHighFrequency: ApiKeyWithSecret
  adminBackup: ApiKeyWithSecret
  adminOAuth: ApiKeyWithSecret
  adminDisabled: ApiKeyWithSecret
  adminExpired: ApiKeyWithSecret
  devGroupAuthorized: ApiKeyWithSecret
  testerTeamAuthorized: ApiKeyWithSecret
  opsAccountAuthorized: ApiKeyWithSecret
  financeAuthorized: ApiKeyWithSecret
  viewerAuthorized: ApiKeyWithSecret
}

interface MockTeams {
  devTeam: SystemTeamSummary
  opsTeam: SystemTeamSummary
  disabledTeam: SystemTeamSummary
}

interface MockExternalSources {
  primary: CreatedExternalIntegrationSourceAuthorization
  readonly: CreatedExternalIntegrationSourceAuthorization
}

interface CreatedMockdata {
  users: MockSystemAccounts
  groups: MockGroups
  accounts: MockAccounts
  apiKeys: MockApiKeys
  teams: MockTeams
  authorizations: ResourceAuthorizationSummary[]
  externalSources: MockExternalSources
  streamInterceptPolicies: number
}

interface UsageRecordSeed extends UsageRecordInput {
  id: string
  createdAt: string
}

interface ModelCheckMockdataCounts {
  runs: number
  items: number
}

interface DerivedCacheCounts {
  usageRecords: number
  accountQualityAccounts: number
  clientIpRecords: number
}

interface RecordCleanupMockdataCounts {
  accountTargets: number
  apiKeyTargets: number
}

interface ClientIpPolicyMockdataCounts {
  policies: number
  policyHits: number
}

interface ExtraMockdataCounts {
  publicApiLogs: number
  accountCleanupTargets: number
  apiKeyCleanupTargets: number
  clientIpAggregatedRecords: number
  clientIpPolicies: number
  clientIpPolicyHits: number
}

interface KeyScenario {
  key: ApiKeyWithSecret
  owner: SystemAccountSummary
  group: GroupSummary
  accounts: AccountSummary[]
  label: string
  clientIpBase: string
}

interface ModelCheckTargetSeed {
  account: AccountSummary
  group: GroupSummary
  apiKey: ApiKeyWithSecret
  actor: SystemAccountSummary
  comparisonAccount?: AccountSummary
}

interface AccountMetricRow {
  sample_count: number
  cpu_percent_sum: number
  cpu_percent_max: number | null
  memory_used_percent_sum: number
  memory_used_percent_max: number | null
  process_rss_bytes_sum: number
  process_rss_bytes_max: number | null
  process_heap_used_bytes_sum: number
  process_heap_used_bytes_max: number | null
  event_loop_lag_ms_sum: number
  event_loop_lag_ms_max: number | null
  network_rx_bytes_per_sec_sum: number
  network_rx_bytes_per_sec_max: number | null
  network_rx_bytes_per_sec_count: number
  network_tx_bytes_per_sec_sum: number
  network_tx_bytes_per_sec_max: number | null
  network_tx_bytes_per_sec_count: number
  network_rx_total_bytes_max: number | null
  network_tx_total_bytes_max: number | null
  db_file_bytes_max: number | null
  stats_lag_seconds_max: number | null
}

interface ProcessMetricRow {
  sample_count: number
  event_loop_lag_ms_sum: number
  event_loop_lag_ms_max: number | null
}

const idPrefix = 'mockdata_'
const tracePrefix = 'mockdata-'
const namePrefix = '造数-'
const mockPassword = 'mockdata123456'
const apiKeyAuthorizedGroupBindingRule = '分组授权可在分组列表 / 我的授权中查看；API Key 绑定下拉只显示调用方自己的本地分组，不能直接绑定授权方分组。'
const dayMs = 24 * 60 * 60 * 1000
const minuteMs = 60 * 1000
const defaultDays = 31
const defaultDailyRequests = 120

const adminUsername = 'admin'
const providerCode = 'openai'

const modelPrices: Record<string, { input: number; output: number; cached: number }> = {
  'gpt-5.4-mini': { input: 0.15, output: 0.6, cached: 0.04 },
  'gpt-5.4': { input: 1.25, output: 10, cached: 0.3 },
  'gpt-4.1-mini': { input: 0.4, output: 1.6, cached: 0.1 },
  'gpt-4o-mini': { input: 0.15, output: 0.6, cached: 0.04 }
}

function main(): void {
  const options = parseOptions(process.argv.slice(2))
  if (options.help) {
    printHelp()
    return
  }

  const startedAt = Date.now()
  const businessDatabase = getBusinessDatabase()
  const datasetDatabase = getDatasetDatabase()
  const statsDatabase = getStatsDatabase()
  const admin = findAdminAccount()
  const adminAccess: AccessScope = { systemAccountId: admin.id, role: 'admin', systemAccountFilterId: admin.id }

  console.log(`开始生成 Mockdata：${options.days} 天，每天 ${options.dailyRequests} 条使用记录，资源归属 ${admin.username}`)
  cleanupMockdata(businessDatabase, datasetDatabase, statsDatabase, admin.id)

  const created = createBusinessMockdata(admin, adminAccess)
  const usageRecords = createUsageMockdata(created, options)
  createAuditMockdata(usageRecords)
  const publicApiLogs = createPublicApiLogMockdata(created, options)
  createOperationMockdata(created, usageRecords)
  createRuntimeLogMockdata(usageRecords)
  const modelCheckCounts = createModelCheckMockdata(created, options)
  const cleanupCounts = createRecordCleanupMockdata(created)
  createMonitoringMockdata(options)

  const derivedCounts = rebuildDerivedCaches(statsDatabase)
  const clientIpPolicyCounts = createClientIpPolicyMockdata(created)
  createStorageMockdata(created, options)
  updateApiKeyLastUsedAt(usageRecords)
  writeSummary(
    created,
    usageRecords,
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

  console.log(`Mockdata 已生成：使用记录 ${usageRecords.length} 条，公开接口日志 ${publicApiLogs} 条，审计 ${Math.ceil(usageRecords.length / 4)} 条，模型检测 ${modelCheckCounts.runs} 次，耗时 ${Date.now() - startedAt}ms`)
  console.log(`业务库：${runtimeConfig.databasePath}`)
  console.log(`数据集目录库：${datasetDatabasePath()}`)
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
  - 生成的本地网关 Key 会写入 backend/data/mockdata-summary.json。
`)
}

function createBusinessMockdata(admin: SystemAccountSummary, adminAccess: AccessScope): CreatedMockdata {
  const users = createMockUsers(admin)
  const policies = createErrorPolicies(admin.id)
  const proxies = createProxies(adminAccess)
  const groups = createGroups(adminAccess, users)
  const accounts = createAccounts(adminAccess, groups, policies, proxies)
  const teams = createTeams(adminAccess, users)
  const authorizations = createAuthorizations(adminAccess, groups, accounts, users, teams)
  assertNoMockSelfAuthorizations(admin.id)
  bindAuthorizedAccountToUserGroup(authorizationInstanceAccount(accounts.proxied, users.ops), groups.opsDefault, users.ops)
  const apiKeys = createApiKeys(adminAccess, groups, users)
  const externalSources = createExternalSources()
  const streamInterceptPolicies = createStreamInterceptPolicies()
  createAnnouncements(admin.id, users)
  seedOauthUsageSnapshots(accounts)
  tuneGroupAccountBindings(groups, accounts)

  return {
    users,
    groups,
    accounts,
    apiKeys,
    teams,
    authorizations,
    externalSources,
    streamInterceptPolicies
  }
}

function createMockUsers(admin: SystemAccountSummary): MockSystemAccounts {
  return {
    admin,
    ops: ensureSystemAccount({
      username: 'mockdata_ops',
      displayName: `${namePrefix}运维用户`,
      description: 'Mockdata 运维协作用户，用于账户授权、操作日志和调用方统计',
      status: 'active'
    }),
    dev: ensureSystemAccount({
      username: 'mockdata_dev',
      displayName: `${namePrefix}研发用户`,
      description: 'Mockdata 研发协作用户，用于分组授权和团队授权',
      status: 'active'
    }),
    tester: ensureSystemAccount({
      username: 'mockdata_tester',
      displayName: `${namePrefix}测试用户`,
      description: 'Mockdata 测试协作用户，用于团队授权和回归验证',
      status: 'active'
    }),
    finance: ensureSystemAccount({
      username: 'mockdata_finance',
      displayName: `${namePrefix}财务用户`,
      description: 'Mockdata 财务观察用户，用于额度和授权展示',
      status: 'active'
    }),
    viewer: ensureSystemAccount({
      username: 'mockdata_viewer',
      displayName: `${namePrefix}只读观察用户`,
      description: 'Mockdata 观察用户，用于公告已读和操作可见性',
      status: 'active'
    }),
    disabled: ensureSystemAccount({
      username: 'mockdata_disabled',
      displayName: `${namePrefix}停用用户`,
      description: 'Mockdata 停用用户，用于系统账号状态展示',
      status: 'disabled'
    })
  }
}

function ensureSystemAccount(input: {
  username: string
  displayName: string
  description: string
  status: 'active' | 'disabled'
}): SystemAccountSummary {
  const existing = repositories.findSystemAccountByUsername(input.username)
  if (existing) {
    const updated = repositories.updateSystemAccount(existing.id, {
      displayName: input.displayName,
      description: input.description,
      role: 'user',
      status: input.status,
      mustChangePassword: false,
      password: mockPassword
    })
    if (!updated) throw new Error(`更新 Mockdata 用户失败：${input.username}`)
    return updated
  }
  return repositories.createSystemAccount({
    username: input.username,
    displayName: input.displayName,
    description: input.description,
    password: mockPassword,
    role: 'user',
    status: input.status,
    mustChangePassword: false
  })
}

function createErrorPolicies(adminId: string): { quota: string; strict: string; temporary: string } {
  const now = nowIso()
  const policies = [
    {
      id: `${idPrefix}policy_quota`,
      name: `${namePrefix}额度与限流策略`,
      rules: [
        {
          enabled: true,
          name: '429 日额度耗尽',
          priority: 10,
          status_codes: [429],
          keywords: ['DAILY_LIMIT_EXCEEDED', 'daily quota', '每日额度'],
          action: 'rate_limited',
          reset_strategy: 'daily',
          daily_reset_hour: 0,
          description: '按天额度耗尽处理'
        },
        {
          enabled: true,
          name: '402 余额不足',
          priority: 20,
          status_codes: [402],
          action: 'rate_limited',
          reset_strategy: 'daily',
          daily_reset_hour: 0
        }
      ]
    },
    {
      id: `${idPrefix}policy_strict`,
      name: `${namePrefix}认证异常严格策略`,
      rules: [
        {
          enabled: true,
          name: '401 认证失败',
          priority: 10,
          status_codes: [401],
          action: 'error_disabled',
          description: '认证失败直接标记异常'
        },
        {
          enabled: true,
          name: '403 禁止访问',
          priority: 20,
          status_codes: [403],
          action: 'error_disabled'
        }
      ]
    },
    {
      id: `${idPrefix}policy_temporary`,
      name: `${namePrefix}临时故障避让策略`,
      rules: [
        {
          enabled: true,
          name: '503 服务不可用',
          priority: 30,
          status_codes: [503],
          action: 'temp_unschedulable'
        },
        {
          enabled: true,
          name: '500 上游错误',
          priority: 40,
          status_codes: [500],
          action: 'temp_unschedulable'
        }
      ]
    }
  ]
  const statement = getBusinessDatabase().prepare(`
    INSERT INTO error_policies (id, system_account_id, name, enabled, rules_json, created_at, updated_at)
    VALUES (?, ?, ?, 1, ?, ?, ?)
  `)
  for (const policy of policies) {
    statement.run(policy.id, adminId, policy.name, JSON.stringify(policy.rules), now, now)
  }
  return {
    quota: policies[0].id,
    strict: policies[1].id,
    temporary: policies[2].id
  }
}

function createProxies(adminAccess: AccessScope): { http: string; socks: string; disabled: string } {
  const http = repositories.createProxy({
    name: `${namePrefix}HTTP 代理`,
    description: 'Mockdata HTTP 代理，绑定到主力 API Key 账户',
    type: 'http',
    host: '127.0.0.1',
    port: 7890,
    username: 'mock_proxy',
    password: 'mock_proxy_password',
    enabled: true
  }, adminAccess)
  repositories.updateProxyTestState(http.id, {
    testStatus: 'passed',
    latencyMs: 82,
    outboundIp: '203.0.113.10',
    outboundRegion: '本地测试出口',
    lastTestMessage: 'Mockdata 代理连通正常'
  })

  const socks = repositories.createProxy({
    name: `${namePrefix}SOCKS 代理`,
    description: 'Mockdata SOCKS 代理，绑定到 OAuth 账户',
    type: 'socks5h',
    host: '127.0.0.1',
    port: 1080,
    enabled: true
  }, adminAccess)
  repositories.updateProxyTestState(socks.id, {
    testStatus: 'passed',
    latencyMs: 118,
    outboundIp: '203.0.113.11',
    outboundRegion: '本地备用出口',
    lastTestMessage: 'Mockdata SOCKS 代理连通正常'
  })

  const disabled = repositories.createProxy({
    name: `${namePrefix}停用代理`,
    description: 'Mockdata 停用代理，用于代理状态展示',
    type: 'http',
    host: '127.0.0.1',
    port: 18080,
    enabled: false
  }, adminAccess)

  return { http: http.id, socks: socks.id, disabled: disabled.id }
}

function createGroups(adminAccess: AccessScope, users: MockSystemAccounts): MockGroups {
  const main = repositories.createGroup({
    name: `${namePrefix}主力分组`,
    description: '主力业务分组，包含高优先级与常规账户',
    providerCode,
    enabled: true
  }, adminAccess)
  const highConcurrency = repositories.createGroup({
    name: `${namePrefix}高并发 AI 分组`,
    description: '高并发调度分组，用于分组管理页展示软并发、短队列和单 IP 并发限制',
    providerCode,
    enabled: true,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 12,
      maxQueueWaitMs: 45_000,
      clientIpConcurrencyLimit: 8,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: 6
    }
  }, adminAccess)
  const backup = repositories.createGroup({
    name: `${namePrefix}备用分组`,
    description: '备用调度分组，包含降级账户和冷却样例',
    providerCode,
    enabled: true
  }, adminAccess)
  const oauth = repositories.createGroup({
    name: `${namePrefix}OAuth 分组`,
    description: 'OAuth 账户分组，用于 Codex 额度快照展示',
    providerCode,
    enabled: true
  }, adminAccess)
  const experiment = repositories.createGroup({
    name: `${namePrefix}实验分组`,
    description: '实验分组，用于授权、额度和模型策略演示',
    providerCode,
    enabled: true
  }, adminAccess)
  const empty = repositories.createGroup({
    name: `${namePrefix}空分组`,
    description: '空分组，用于无账户状态展示',
    providerCode,
    enabled: false
  }, adminAccess)

  return {
    main,
    highConcurrency,
    backup,
    oauth,
    experiment,
    empty,
    devDefault: defaultOpenAIGroup(users.dev.id),
    opsDefault: defaultOpenAIGroup(users.ops.id),
    testerDefault: defaultOpenAIGroup(users.tester.id),
    financeDefault: defaultOpenAIGroup(users.finance.id),
    viewerDefault: defaultOpenAIGroup(users.viewer.id)
  }
}

function createAccounts(
  adminAccess: AccessScope,
  groups: MockGroups,
  policies: { quota: string; strict: string; temporary: string },
  proxies: { http: string; socks: string }
): MockAccounts {
  const primary = repositories.createAccount({
    providerCode,
    name: `${namePrefix}主力 API Key 账户`,
    type: 'api_key',
    groupId: groups.main.id,
    credentials: apiKeyCredentials('primary'),
    proxyProfileId: proxies.http,
    errorPolicyId: policies.quota,
    supportedModels: ['gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini', 'gpt-4.1-mini'],
    concurrencyLimit: 80,
    priority: 0,
    superPriorityEnabled: true,
    notes: 'Mockdata 主力账号，超级优先'
  }, adminAccess)

  const proxied = repositories.createAccount({
    providerCode,
    name: `${namePrefix}带代理 API Key 账户`,
    type: 'api_key',
    groupId: groups.main.id,
    credentials: apiKeyCredentials('proxied'),
    proxyProfileId: proxies.http,
    errorPolicyId: policies.temporary,
    supportedModels: ['gpt-5.4', 'gpt-5.4-mini', 'gpt-4o-mini'],
    concurrencyLimit: 45,
    priority: 10,
    notes: 'Mockdata 代理账号，用于账户授权给用户'
  }, adminAccess)

  const normal = repositories.createAccount({
    providerCode,
    name: `${namePrefix}普通 API Key 账户`,
    type: 'api_key',
    groupId: groups.main.id,
    credentials: apiKeyCredentials('normal'),
    errorPolicyId: policies.strict,
    supportedModels: ['gpt-5.4-mini', 'gpt-4.1-mini', 'gpt-4o-mini'],
    concurrencyLimit: 35,
    priority: 30,
    notes: 'Mockdata 普通账号'
  }, adminAccess)

  const burstFast = repositories.createAccount({
    providerCode,
    name: `${namePrefix}高并发快响账户`,
    type: 'api_key',
    groupId: groups.highConcurrency.id,
    credentials: apiKeyCredentials('burst-fast'),
    errorPolicyId: policies.temporary,
    supportedModels: ['gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini'],
    concurrencyLimit: 180,
    priority: 2,
    superPriorityEnabled: true,
    notes: 'Mockdata 高并发分组快响账号，用于高并发调度策略展示'
  }, adminAccess)

  const burstImage = repositories.createAccount({
    providerCode,
    name: `${namePrefix}高并发图像账户`,
    type: 'api_key',
    groupId: groups.highConcurrency.id,
    credentials: apiKeyCredentials('burst-image'),
    errorPolicyId: policies.quota,
    supportedModels: ['gpt-5.4', 'gpt-5.4-mini', 'gpt-4.1-mini'],
    concurrencyLimit: 120,
    priority: 12,
    notes: 'Mockdata 高并发分组图像 / 长请求账号，用于图像 lane 并发展示'
  }, adminAccess)

  const burstFallback = repositories.createAccount({
    providerCode,
    name: `${namePrefix}高并发备用账户`,
    type: 'api_key',
    groupId: groups.highConcurrency.id,
    credentials: apiKeyCredentials('burst-fallback'),
    errorPolicyId: policies.temporary,
    supportedModels: ['gpt-5.4', 'gpt-5.4-mini'],
    concurrencyLimit: 90,
    priority: 70,
    fallbackEnabled: true,
    notes: 'Mockdata 高并发分组备用账号，用于软并发触发后的 fallback 展示'
  }, adminAccess)

  const fallback = repositories.createAccount({
    providerCode,
    name: `${namePrefix}降级备用账户`,
    type: 'api_key',
    groupId: groups.backup.id,
    credentials: apiKeyCredentials('fallback'),
    errorPolicyId: policies.temporary,
    supportedModels: ['gpt-5.4', 'gpt-4.1-mini'],
    concurrencyLimit: 25,
    priority: 80,
    fallbackEnabled: true,
    notes: 'Mockdata 备用账号'
  }, adminAccess)

  const oauth = repositories.createAccount({
    providerCode,
    name: `${namePrefix}OAuth 主力账户`,
    type: 'oauth',
    groupId: groups.oauth.id,
    credentials: oauthCredentials('oauth-main', 2),
    proxyProfileId: proxies.socks,
    errorPolicyId: policies.quota,
    supportedModels: ['gpt-5.5', 'gpt-5.4'],
    concurrencyLimit: 50,
    priority: 5,
    notes: 'Mockdata OAuth 主力账号，带 Codex 额度快照'
  }, adminAccess)

  const oauthBackup = repositories.createAccount({
    providerCode,
    name: `${namePrefix}OAuth 备用账户`,
    type: 'oauth',
    groupId: groups.oauth.id,
    credentials: oauthCredentials('oauth-backup', 6),
    errorPolicyId: policies.temporary,
    supportedModels: ['gpt-5.4', 'gpt-5.4-mini'],
    concurrencyLimit: 20,
    priority: 60,
    fallbackEnabled: true,
    notes: 'Mockdata OAuth 备用账号'
  }, adminAccess)

  const rateLimited = repositories.createAccount({
    providerCode,
    name: `${namePrefix}限流中账户`,
    type: 'api_key',
    groupId: groups.backup.id,
    credentials: apiKeyCredentials('rate-limited'),
    errorPolicyId: policies.quota,
    supportedModels: ['gpt-5.4-mini'],
    concurrencyLimit: 15,
    priority: 120,
    notes: 'Mockdata 限流状态账号'
  }, adminAccess)
  repositories.markAccountCooldown(
    rateLimited.id,
    new Date(Date.now() + 2 * 60 * 60_000).toISOString(),
    'Mockdata 模拟上游日额度耗尽',
    'rate_limited'
  )

  const temporary = repositories.createAccount({
    providerCode,
    name: `${namePrefix}临时不可调用账户`,
    type: 'api_key',
    groupId: groups.experiment.id,
    credentials: apiKeyCredentials('temporary'),
    errorPolicyId: policies.temporary,
    supportedModels: ['gpt-5.4', 'gpt-4.1-mini'],
    concurrencyLimit: 15,
    priority: 130,
    notes: 'Mockdata 临时不可调用状态账号'
  }, adminAccess)
  repositories.markAccountTemporaryUnavailable(temporary.id, 'Mockdata 模拟上游 503 维护')

  const error = repositories.createAccount({
    providerCode,
    name: `${namePrefix}异常账户`,
    type: 'api_key',
    groupId: groups.experiment.id,
    credentials: apiKeyCredentials('error'),
    errorPolicyId: policies.strict,
    supportedModels: ['gpt-5.5'],
    concurrencyLimit: 10,
    priority: 160,
    notes: 'Mockdata 异常状态账号'
  }, adminAccess)
  repositories.markAccountDisabledByFailure(error.id, 'Mockdata 模拟 401 认证失败')

  const expired = repositories.createAccount({
    providerCode,
    name: `${namePrefix}已到期账户`,
    type: 'api_key',
    groupId: groups.experiment.id,
    credentials: apiKeyCredentials('expired'),
    errorPolicyId: policies.strict,
    supportedModels: ['gpt-4.1-mini'],
    concurrencyLimit: 5,
    accountExpiresAt: new Date(Date.now() - dayMs).toISOString(),
    notes: 'Mockdata 已到期停用账号'
  }, adminAccess)

  return {
    primary,
    proxied,
    normal,
    burstFast,
    burstImage,
    burstFallback,
    fallback,
    oauth,
    oauthBackup,
    rateLimited: refreshAccount(rateLimited.id),
    temporary: refreshAccount(temporary.id),
    error: refreshAccount(error.id),
    expired: refreshAccount(expired.id)
  }
}

function apiKeyCredentials(suffix: string): Record<string, unknown> {
  return {
    api_key: `sk-mockdata-admin-${suffix}-${'x'.repeat(24)}`,
    base_url: 'https://api.openai.com/v1',
    error_handling_rules: [
      {
        enabled: true,
        name: '429 临时限流',
        priority: 40,
        status_codes: [429],
        action: 'temp_unschedulable'
      },
      {
        enabled: true,
        name: '503 服务不可用',
        priority: 80,
        status_codes: [503],
        action: 'temp_unschedulable'
      }
    ]
  }
}

function oauthCredentials(suffix: string, expiresHours: number): Record<string, unknown> {
  return {
    access_token: `mockdata-oauth-access-${suffix}-${'a'.repeat(32)}`,
    refresh_token: `mockdata-oauth-refresh-${suffix}-${'r'.repeat(32)}`,
    client_id: 'mockdata-openai-oauth-client',
    account_id: `mockdata-openai-user-${suffix}`,
    expires_at: new Date(Date.now() + expiresHours * 60 * 60_000).toISOString(),
    base_url: 'https://api.openai.com/v1',
    error_handling_rules: [
      {
        enabled: true,
        name: 'OAuth 429 Codex 限流',
        priority: 10,
        status_codes: [429],
        keywords: ['rate limit', 'codex'],
        action: 'rate_limited',
        reset_strategy: 'duration',
        duration_hours: 5
      }
    ]
  }
}

function createTeams(adminAccess: AccessScope, users: MockSystemAccounts): MockTeams {
  const teamAccess: AccessScope = { systemAccountId: adminAccess.systemAccountId, role: 'admin' }
  const devTeam = repositories.createSystemTeam({
    name: `${namePrefix}研发协作团队`,
    description: 'Mockdata 研发协作团队，承接团队级分组授权',
    status: 'active'
  }, teamAccess)
  repositories.addSystemTeamMembers(devTeam.id, {
    systemAccountIds: [users.dev.id, users.tester.id, users.ops.id]
  }, teamAccess)

  const opsTeam = repositories.createSystemTeam({
    name: `${namePrefix}运维保障团队`,
    description: 'Mockdata 运维保障团队，承接备用分组授权',
    status: 'active'
  }, teamAccess)
  repositories.addSystemTeamMembers(opsTeam.id, {
    systemAccountIds: [users.ops.id, users.viewer.id]
  }, teamAccess)

  const disabledTeam = repositories.createSystemTeam({
    name: `${namePrefix}停用历史团队`,
    description: 'Mockdata 停用团队，用于状态展示',
    status: 'active'
  }, teamAccess)
  repositories.addSystemTeamMembers(disabledTeam.id, {
    systemAccountIds: [users.finance.id]
  }, teamAccess)
  repositories.updateSystemTeam(disabledTeam.id, { status: 'disabled' }, teamAccess)

  return {
    devTeam: refreshTeam(devTeam.id, teamAccess),
    opsTeam: refreshTeam(opsTeam.id, teamAccess),
    disabledTeam: refreshTeam(disabledTeam.id, teamAccess)
  }
}

function createAuthorizations(
  adminAccess: AccessScope,
  groups: MockGroups,
  accounts: MockAccounts,
  users: MockSystemAccounts,
  teams: MockTeams
): ResourceAuthorizationSummary[] {
  const result: ResourceAuthorizationSummary[] = []
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.main.id,
    granteeType: 'system_account',
    granteeId: users.dev.id,
    remark: `${namePrefix}研发用户可调用主力分组`,
    limits: quotaLimits(25, 200, 800)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.highConcurrency.id,
    granteeType: 'system_account',
    granteeId: users.ops.id,
    remark: `${namePrefix}运维用户可调用高并发分组`,
    limits: quotaLimits(20, 160, 620)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.backup.id,
    granteeType: 'system_account',
    granteeId: users.viewer.id,
    remark: `${namePrefix}观察用户可调用备用分组`,
    limits: quotaLimits(6, 36, 120)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.experiment.id,
    granteeType: 'system_account',
    granteeId: users.tester.id,
    remark: `${namePrefix}测试用户可调用实验分组`,
    expiresAt: new Date(Date.now() + 14 * dayMs).toISOString(),
    limits: quotaLimits(8, 48, 160)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.oauth.id,
    granteeType: 'system_account',
    granteeId: users.finance.id,
    remark: `${namePrefix}财务用户可调用 OAuth 分组`,
    limits: quotaLimits(7, 42, 140)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.backup.id,
    granteeType: 'team',
    granteeId: teams.devTeam.id,
    remark: `${namePrefix}研发团队可调用备用分组`,
    limits: quotaLimits(18, 120, 500)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.oauth.id,
    granteeType: 'team',
    granteeId: teams.opsTeam.id,
    remark: `${namePrefix}运维团队可调用 OAuth 分组`,
    limits: quotaLimits(12, 80, 300)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.highConcurrency.id,
    granteeType: 'team',
    granteeId: teams.devTeam.id,
    remark: `${namePrefix}研发团队可调用高并发分组`,
    limits: quotaLimits(22, 180, 720)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.proxied.id,
    granteeType: 'system_account',
    granteeId: users.ops.id,
    targetGroupId: defaultOpenAIGroup(users.ops.id).id,
    remark: `${namePrefix}运维用户可调用带代理账户`,
    limits: quotaLimits(8, 60, 200)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.burstFast.id,
    granteeType: 'system_account',
    granteeId: users.dev.id,
    targetGroupId: groups.devDefault.id,
    remark: `${namePrefix}研发用户可调用高并发快响账户`,
    limits: quotaLimits(14, 110, 420)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.oauth.id,
    granteeType: 'system_account',
    granteeId: users.finance.id,
    targetGroupId: groups.financeDefault.id,
    remark: `${namePrefix}财务用户可调用 OAuth 主力账户`,
    limits: quotaLimits(6, 40, 150)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.burstImage.id,
    granteeType: 'system_account',
    granteeId: users.viewer.id,
    targetGroupId: groups.viewerDefault.id,
    remark: `${namePrefix}观察用户可调用高并发图像账户`,
    limits: quotaLimits(5, 28, 90)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.primary.id,
    granteeType: 'team',
    granteeId: teams.devTeam.id,
    remark: `${namePrefix}研发团队可调用主力账户`,
    limits: quotaLimits(10, 80, 240)
  }, adminAccess))
  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.burstFallback.id,
    granteeType: 'team',
    granteeId: teams.opsTeam.id,
    remark: `${namePrefix}运维团队可调用高并发备用账户`,
    limits: quotaLimits(9, 66, 220)
  }, adminAccess))

  const pausedTeam = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.experiment.id,
    granteeType: 'team',
    granteeId: teams.opsTeam.id,
    remark: `${namePrefix}运维团队暂停实验分组授权`,
    limits: quotaLimits(6, 36, 120)
  }, adminAccess)
  repositories.updateResourceAuthorization(pausedTeam.id, { status: 'paused' }, adminAccess)
  result.push(refreshAuthorization(pausedTeam.id, adminAccess))

  const revokedTeam = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.normal.id,
    granteeType: 'team',
    granteeId: teams.opsTeam.id,
    remark: `${namePrefix}运维团队已回收普通账户授权`,
    limits: quotaLimits(5, 30, 100)
  }, adminAccess)
  repositories.revokeResourceAuthorization(revokedTeam.id, adminAccess)
  result.push(refreshAuthorization(revokedTeam.id, adminAccess))

  const returned = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.oauthBackup.id,
    granteeType: 'system_account',
    granteeId: users.viewer.id,
    targetGroupId: defaultOpenAIGroup(users.viewer.id).id,
    remark: `${namePrefix}观察用户已归还 OAuth 账户授权`,
    limits: quotaLimits(2, 10, 40)
  }, adminAccess)
  repositories.returnResourceAuthorizationForGrantee(returned.id, { systemAccountId: users.viewer.id, role: 'user' })
  result.push(refreshAuthorization(returned.id, adminAccess))

  const paused = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.experiment.id,
    granteeType: 'system_account',
    granteeId: users.finance.id,
    remark: `${namePrefix}财务用户暂停授权`,
    limits: quotaLimits(4, 20, 80)
  }, adminAccess)
  repositories.updateResourceAuthorization(paused.id, { status: 'paused' }, adminAccess)
  result.push(refreshAuthorization(paused.id, adminAccess))

  const expiredGroup = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.main.id,
    granteeType: 'system_account',
    granteeId: users.finance.id,
    remark: `${namePrefix}财务用户已过期主力分组授权`,
    expiresAt: new Date(Date.now() + 2 * dayMs).toISOString(),
    limits: quotaLimits(3, 18, 60)
  }, adminAccess)
  repositories.updateResourceAuthorization(expiredGroup.id, {
    status: 'expired',
    expiresAt: new Date(Date.now() - 2 * dayMs).toISOString()
  }, adminAccess)
  result.push(refreshAuthorization(expiredGroup.id, adminAccess))

  const expired = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.fallback.id,
    granteeType: 'system_account',
    granteeId: users.tester.id,
    targetGroupId: defaultOpenAIGroup(users.tester.id).id,
    remark: `${namePrefix}测试用户已过期账户授权`,
    expiresAt: new Date(Date.now() + dayMs).toISOString(),
    limits: quotaLimits(3, 16, 50)
  }, adminAccess)
  repositories.updateResourceAuthorization(expired.id, {
    status: 'expired',
    expiresAt: new Date(Date.now() - dayMs).toISOString()
  }, adminAccess)
  result.push(refreshAuthorization(expired.id, adminAccess))

  const revoked = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.normal.id,
    granteeType: 'system_account',
    granteeId: users.viewer.id,
    targetGroupId: defaultOpenAIGroup(users.viewer.id).id,
    remark: `${namePrefix}观察用户已回收账户授权`,
    limits: quotaLimits(2, 12, 40)
  }, adminAccess)
  repositories.revokeResourceAuthorization(revoked.id, adminAccess)
  result.push(refreshAuthorization(revoked.id, adminAccess))

  const revokedTemporary = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.temporary.id,
    granteeType: 'system_account',
    granteeId: users.viewer.id,
    targetGroupId: groups.viewerDefault.id,
    remark: `${namePrefix}观察用户已回收临时账户授权`,
    limits: quotaLimits(1, 8, 24)
  }, adminAccess)
  repositories.revokeResourceAuthorization(revokedTemporary.id, adminAccess)
  result.push(refreshAuthorization(revokedTemporary.id, adminAccess))
  return result
}

function quotaLimits(hourly: number, daily: number, monthly: number): Record<string, unknown> {
  return {
    hourly: { enabled: true, hours: 1, limit: hourly },
    daily: { enabled: true, limit: daily },
    monthly: { enabled: true, limit: monthly }
  }
}

function bindAuthorizedAccountToUserGroup(account: AccountSummary, group: GroupSummary, user: SystemAccountSummary): void {
  const access: AccessScope = { systemAccountId: user.id, role: 'user' }
  const updated = repositories.setAccountGroup(account.id, group.id, access)
  if (!updated) {
    throw new Error('绑定授权账户到用户默认分组失败')
  }
}

function createApiKeys(adminAccess: AccessScope, groups: MockGroups, users: MockSystemAccounts): MockApiKeys {
  const adminMain = repositories.createApiKeyRecord({
    name: `${namePrefix}主力网关 Key`,
    description: 'Mockdata 主力本地网关 Key，绑定主力分组',
    groupBindings: [{ groupId: groups.main.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(35, 260, 1000)
  }, adminAccess)
  const adminHighConcurrency = repositories.createApiKeyRecord({
    name: `${namePrefix}高并发 AI Key`,
    description: 'Mockdata 高并发本地网关 Key，绑定高并发 AI 分组，用于分组管理和调度验收',
    groupBindings: [{ groupId: groups.highConcurrency.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: {
      hourly: { enabled: true, hours: 1, limit: 160 },
      daily: { enabled: true, limit: 1200 },
      monthly: { enabled: true, limit: 5200 }
    }
  }, adminAccess)
  const adminHighFrequency = repositories.createApiKeyRecord({
    name: `${namePrefix}高频限额 Key`,
    description: 'Mockdata 高频限额 Key，用于额度窗口展示',
    groupBindings: [{ groupId: groups.main.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: {
      hourly: { enabled: true, hours: 3, limit: 90 },
      daily: { enabled: true, limit: 420 },
      monthly: { enabled: true, limit: 1600 }
    }
  }, adminAccess)
  const adminBackup = repositories.createApiKeyRecord({
    name: `${namePrefix}备用网关 Key`,
    description: 'Mockdata 备用 Key，绑定备用分组',
    groupBindings: [{ groupId: groups.backup.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(20, 120, 480)
  }, adminAccess)
  const adminOAuth = repositories.createApiKeyRecord({
    name: `${namePrefix}OAuth 网关 Key`,
    description: 'Mockdata OAuth Key，绑定 OAuth 分组',
    groupBindings: [{ groupId: groups.oauth.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(18, 140, 520)
  }, adminAccess)
  const adminDisabled = repositories.createApiKeyRecord({
    name: `${namePrefix}停用网关 Key`,
    description: 'Mockdata 停用 Key，用于状态展示',
    groupBindings: [{ groupId: groups.experiment.id, priority: 1, status: 'active' }],
    status: 'disabled',
    quotaLimits: quotaLimits(5, 30, 100)
  }, adminAccess)
  const adminExpired = repositories.createApiKeyRecord({
    name: `${namePrefix}已过期网关 Key`,
    description: 'Mockdata 已过期 Key，用于过期状态展示',
    groupBindings: [{ groupId: groups.experiment.id, priority: 1, status: 'active' }],
    status: 'active',
    expiresAt: new Date(Date.now() - 2 * dayMs).toISOString(),
    quotaLimits: quotaLimits(5, 30, 100)
  }, adminAccess)

  const devGroupAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}研发授权调用 Key`,
    description: 'Mockdata 研发用户使用授权账户的 Key，绑定研发用户自己的默认分组',
    groupBindings: [{ groupId: groups.devDefault.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(8, 50, 180)
  }, { systemAccountId: users.dev.id, role: 'user' })

  const testerTeamAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}团队授权调用 Key`,
    description: 'Mockdata 测试用户使用团队授权账户的 Key，绑定测试用户自己的默认分组',
    groupBindings: [{ groupId: groups.testerDefault.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(6, 40, 150)
  }, { systemAccountId: users.tester.id, role: 'user' })

  const opsAccountAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}账户授权调用 Key`,
    description: 'Mockdata 运维用户使用授权账户的 Key，绑定运维用户自己的默认分组',
    groupBindings: [{ groupId: groups.opsDefault.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(6, 36, 120)
  }, { systemAccountId: users.ops.id, role: 'user' })

  const financeAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}财务授权调用 Key`,
    description: 'Mockdata 财务用户使用授权账户的 Key，绑定财务用户自己的默认分组',
    groupBindings: [{ groupId: groups.financeDefault.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(5, 30, 100)
  }, { systemAccountId: users.finance.id, role: 'user' })

  const viewerAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}观察授权调用 Key`,
    description: 'Mockdata 观察用户使用授权账户的 Key，绑定观察用户自己的默认分组',
    groupBindings: [{ groupId: groups.viewerDefault.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(4, 24, 80)
  }, { systemAccountId: users.viewer.id, role: 'user' })

  return {
    adminMain,
    adminHighConcurrency,
    adminHighFrequency,
    adminBackup,
    adminOAuth,
    adminDisabled,
    adminExpired,
    devGroupAuthorized,
    testerTeamAuthorized,
    opsAccountAuthorized,
    financeAuthorized,
    viewerAuthorized
  }
}

function createExternalSources(): MockExternalSources {
  const allScopes = externalIntegrationScopeOptions.map((option) => option.value)
  const readScopes = allScopes.filter((scope) => scope.includes(':read'))
  const primary = createExternalIntegrationSourceAuthorization({
    name: `${namePrefix}公益站公开接口`,
    status: 'active',
    scopes: allScopes,
    rateLimits: [
      { windowSeconds: 60, maxRequests: 180 },
      { windowSeconds: 3600, maxRequests: 6000 }
    ],
    expiresAt: new Date(Date.now() + 90 * dayMs).toISOString(),
    notes: 'Mockdata 正式来源系统，用于公开接口日志、鉴权和写接口演示'
  })
  createExternalIntegrationSourceToken({
    sourceRefId: primary.source.id,
    name: `${namePrefix}公益站备用 Token`,
    status: 'disabled',
    scopes: readScopes,
    expiresAt: new Date(Date.now() + 45 * dayMs).toISOString()
  })

  const readonly = createExternalIntegrationSourceAuthorization({
    name: `${namePrefix}只读统计来源`,
    status: 'active',
    scopes: readScopes,
    rateLimits: [
      { windowSeconds: 60, maxRequests: 90 },
      { windowSeconds: 3600, maxRequests: 2400 }
    ],
    notes: 'Mockdata 只读来源系统，用于公开统计读取接口演示'
  })
  return {
    primary,
    readonly
  }
}

function createStreamInterceptPolicies(): number {
  const policies = [
    {
      name: `${namePrefix}流式错误切换账户`,
      enabled: true,
      priority: 20,
      providerCode,
      match: {
        eventTypes: ['response.failed', 'error'],
        errorCodes: ['rate_limit_exceeded', 'server_error'],
        textIncludes: ['Mockdata']
      },
      action: 'retry_next_account' as const,
      notes: 'Mockdata 管理端策略：命中流式错误后请求下一个账号'
    },
    {
      name: `${namePrefix}安全策略干跑观察`,
      enabled: true,
      priority: 35,
      providerCode,
      match: {
        errorCodes: ['cyber_policy'],
        jsonPathsExists: ['response.error'],
        textIncludes: ['policy']
      },
      action: 'observe' as const,
      notes: 'Mockdata 管理端策略：只观察安全策略命中，不改变流'
    },
    {
      name: `${namePrefix}图像流异常账号避让`,
      enabled: false,
      priority: 55,
      providerCode,
      match: {
        dataTypes: ['response.output_item.done'],
        textIncludes: ['image_generation'],
        textExcludes: ['completed']
      },
      action: 'avoid_account_ttl' as const,
      notes: 'Mockdata 停用策略，用于流式拦截策略页面状态展示'
    }
  ]
  for (const policy of policies) {
    createStreamInterceptPolicy(policy)
  }
  return policies.length
}

function createAnnouncements(adminId: string, users: MockSystemAccounts): void {
  const announcements = [
    repositories.createAnnouncement({
      title: `${namePrefix}系统维护公告`,
      content: '今晚 23:30 到 23:45 将进行 Mockdata 演示维护，期间可能出现短暂网关重试。',
      level: 'critical',
      status: 'published'
    }, adminId),
    repositories.createAnnouncement({
      title: `${namePrefix}额度观察提醒`,
      content: '主力分组本月额度接近 70%，请关注 API Key 额度窗口和授权用量。',
      level: 'warning',
      status: 'published'
    }, adminId),
    repositories.createAnnouncement({
      title: `${namePrefix}新模型接入说明`,
      content: 'Mockdata 已补充 gpt-5.4-mini、gpt-5.4 和 gpt-4.1-mini 的混合调用记录。',
      level: 'info',
      status: 'published'
    }, adminId),
    repositories.createAnnouncement({
      title: `${namePrefix}草稿公告`,
      content: '这是一条 Mockdata 草稿公告，用于公告管理页面状态展示。',
      level: 'normal',
      status: 'draft'
    }, adminId),
    repositories.createAnnouncement({
      title: `${namePrefix}归档公告`,
      content: '这是一条 Mockdata 归档公告，用于公告归档状态展示。',
      level: 'info',
      status: 'archived'
    }, adminId)
  ]
  const database = getBusinessDatabase()
  announcements.forEach((announcement, index) => {
    const createdAt = new Date(Date.now() - (20 - index * 3) * dayMs).toISOString()
    database.prepare(`
      UPDATE announcements
      SET created_at = ?, updated_at = ?, published_at = CASE WHEN status = 'published' THEN ? ELSE published_at END
      WHERE id = ?
    `).run(createdAt, createdAt, createdAt, announcement.id)
  })
  repositories.markPublicAnnouncementsRead(users.dev.id, [announcements[0].id, announcements[1].id])
  repositories.markPublicAnnouncementsRead(users.ops.id, [announcements[0].id])
  repositories.markPublicAnnouncementsRead(users.viewer.id, [announcements[0].id, announcements[1].id, announcements[2].id])
}

function seedOauthUsageSnapshots(accounts: MockAccounts): void {
  const now = nowIso()
  repositories.upsertAccountUsageSnapshots([
    {
      accountId: accounts.oauth.id,
      kind: 'openai_codex',
      source: 'mockdata',
      snapshot: {
        codex_5h_used_percent: 62,
        codex_5h_reset_at: new Date(Date.now() + 2 * 60 * 60_000).toISOString(),
        codex_5h_window_minutes: 300,
        codex_7d_used_percent: 41,
        codex_7d_reset_at: new Date(Date.now() + 4 * dayMs).toISOString(),
        codex_7d_window_minutes: 10080
      },
      updatedAt: now
    },
    {
      accountId: accounts.oauthBackup.id,
      kind: 'openai_codex',
      source: 'mockdata',
      snapshot: {
        codex_5h_used_percent: 18,
        codex_5h_reset_at: new Date(Date.now() + 4 * 60 * 60_000).toISOString(),
        codex_5h_window_minutes: 300,
        codex_7d_used_percent: 9,
        codex_7d_reset_at: new Date(Date.now() + 6 * dayMs).toISOString(),
        codex_7d_window_minutes: 10080
      },
      updatedAt: now
    }
  ])
}

function tuneGroupAccountBindings(groups: MockGroups, accounts: MockAccounts): void {
  const now = nowIso()
  const database = getBusinessDatabase()
  const updates: Array<[number, number, GroupSummary, AccountSummary]> = [
    [1, 0, groups.main, accounts.primary],
    [0, 0, groups.main, accounts.proxied],
    [0, 0, groups.main, accounts.normal],
    [1, 0, groups.highConcurrency, accounts.burstFast],
    [0, 0, groups.highConcurrency, accounts.burstImage],
    [0, 1, groups.highConcurrency, accounts.burstFallback],
    [0, 1, groups.backup, accounts.fallback],
    [0, 0, groups.oauth, accounts.oauth],
    [0, 1, groups.oauth, accounts.oauthBackup]
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

function createUsageMockdata(created: CreatedMockdata, options: MockdataOptions): UsageRecordSeed[] {
  const devPrimaryInstance = authorizationInstanceAccount(created.accounts.primary, created.users.dev)
  const devBurstFastInstance = authorizationInstanceAccount(created.accounts.burstFast, created.users.dev)
  const testerPrimaryInstance = authorizationInstanceAccount(created.accounts.primary, created.users.tester)
  const opsPrimaryInstance = authorizationInstanceAccount(created.accounts.primary, created.users.ops)
  const opsProxiedInstance = authorizationInstanceAccount(created.accounts.proxied, created.users.ops)
  const opsBurstFallbackInstance = authorizationInstanceAccount(created.accounts.burstFallback, created.users.ops)
  const financeOauthInstance = authorizationInstanceAccount(created.accounts.oauth, created.users.finance)
  const viewerBurstImageInstance = authorizationInstanceAccount(created.accounts.burstImage, created.users.viewer)
  const viewerBurstFallbackInstance = authorizationInstanceAccount(created.accounts.burstFallback, created.users.viewer)
  const scenarios: KeyScenario[] = [
    {
      key: created.apiKeys.adminMain,
      owner: created.users.admin,
      group: created.groups.main,
      accounts: [created.accounts.primary, created.accounts.proxied, created.accounts.normal],
      label: 'admin-main',
      clientIpBase: '10.10.1.'
    },
    {
      key: created.apiKeys.adminHighFrequency,
      owner: created.users.admin,
      group: created.groups.main,
      accounts: [created.accounts.primary, created.accounts.normal],
      label: 'admin-high',
      clientIpBase: '10.10.2.'
    },
    {
      key: created.apiKeys.adminHighConcurrency,
      owner: created.users.admin,
      group: created.groups.highConcurrency,
      accounts: [created.accounts.burstFast, created.accounts.burstImage, created.accounts.burstFallback],
      label: 'admin-high-concurrency',
      clientIpBase: '10.10.7.'
    },
    {
      key: created.apiKeys.adminBackup,
      owner: created.users.admin,
      group: created.groups.backup,
      accounts: [created.accounts.fallback, created.accounts.rateLimited],
      label: 'admin-backup',
      clientIpBase: '10.10.3.'
    },
    {
      key: created.apiKeys.adminOAuth,
      owner: created.users.admin,
      group: created.groups.oauth,
      accounts: [created.accounts.oauth, created.accounts.oauthBackup],
      label: 'admin-oauth',
      clientIpBase: '10.10.4.'
    },
    {
      key: created.apiKeys.devGroupAuthorized,
      owner: created.users.dev,
      group: created.groups.main,
      accounts: [created.accounts.primary, created.accounts.proxied, created.accounts.normal],
      label: 'dev-direct-group-auth',
      clientIpBase: '10.20.1.'
    },
    {
      key: created.apiKeys.devGroupAuthorized,
      owner: created.users.dev,
      group: created.groups.devDefault,
      accounts: [devPrimaryInstance],
      label: 'dev-team-account-auth',
      clientIpBase: '10.20.2.'
    },
    {
      key: created.apiKeys.devGroupAuthorized,
      owner: created.users.dev,
      group: created.groups.devDefault,
      accounts: [devBurstFastInstance],
      label: 'dev-direct-burst-account-auth',
      clientIpBase: '10.20.7.'
    },
    {
      key: created.apiKeys.testerTeamAuthorized,
      owner: created.users.tester,
      group: created.groups.backup,
      accounts: [created.accounts.fallback, created.accounts.rateLimited],
      label: 'tester-team-group-auth',
      clientIpBase: '10.20.3.'
    },
    {
      key: created.apiKeys.testerTeamAuthorized,
      owner: created.users.tester,
      group: created.groups.testerDefault,
      accounts: [testerPrimaryInstance],
      label: 'tester-team-account-auth',
      clientIpBase: '10.20.4.'
    },
    {
      key: created.apiKeys.testerTeamAuthorized,
      owner: created.users.tester,
      group: created.groups.experiment,
      accounts: [created.accounts.temporary, created.accounts.error],
      label: 'tester-direct-experiment-group-auth',
      clientIpBase: '10.20.8.'
    },
    {
      key: created.apiKeys.opsAccountAuthorized,
      owner: created.users.ops,
      group: created.groups.oauth,
      accounts: [created.accounts.oauth, created.accounts.oauthBackup],
      label: 'ops-team-group-auth',
      clientIpBase: '10.20.5.'
    },
    {
      key: created.apiKeys.opsAccountAuthorized,
      owner: created.users.ops,
      group: created.groups.opsDefault,
      accounts: [opsProxiedInstance, opsPrimaryInstance],
      label: 'ops-account-auth',
      clientIpBase: '10.20.6.'
    },
    {
      key: created.apiKeys.opsAccountAuthorized,
      owner: created.users.ops,
      group: created.groups.highConcurrency,
      accounts: [created.accounts.burstFast, created.accounts.burstImage, created.accounts.burstFallback],
      label: 'ops-direct-high-concurrency-group-auth',
      clientIpBase: '10.20.9.'
    },
    {
      key: created.apiKeys.opsAccountAuthorized,
      owner: created.users.ops,
      group: created.groups.opsDefault,
      accounts: [opsBurstFallbackInstance],
      label: 'ops-team-burst-account-auth',
      clientIpBase: '10.20.10.'
    },
    {
      key: created.apiKeys.financeAuthorized,
      owner: created.users.finance,
      group: created.groups.oauth,
      accounts: [created.accounts.oauth, created.accounts.oauthBackup],
      label: 'finance-direct-oauth-group-auth',
      clientIpBase: '10.21.1.'
    },
    {
      key: created.apiKeys.financeAuthorized,
      owner: created.users.finance,
      group: created.groups.financeDefault,
      accounts: [financeOauthInstance],
      label: 'finance-direct-oauth-account-auth',
      clientIpBase: '10.21.2.'
    },
    {
      key: created.apiKeys.viewerAuthorized,
      owner: created.users.viewer,
      group: created.groups.backup,
      accounts: [created.accounts.fallback, created.accounts.rateLimited],
      label: 'viewer-direct-backup-group-auth',
      clientIpBase: '10.22.1.'
    },
    {
      key: created.apiKeys.viewerAuthorized,
      owner: created.users.viewer,
      group: created.groups.oauth,
      accounts: [created.accounts.oauth, created.accounts.oauthBackup],
      label: 'viewer-team-oauth-group-auth',
      clientIpBase: '10.22.2.'
    },
    {
      key: created.apiKeys.viewerAuthorized,
      owner: created.users.viewer,
      group: created.groups.viewerDefault,
      accounts: [viewerBurstImageInstance],
      label: 'viewer-direct-burst-image-account-auth',
      clientIpBase: '10.22.3.'
    },
    {
      key: created.apiKeys.viewerAuthorized,
      owner: created.users.viewer,
      group: created.groups.viewerDefault,
      accounts: [viewerBurstFallbackInstance],
      label: 'viewer-team-burst-account-auth',
      clientIpBase: '10.22.4.'
    }
  ]

  const records: UsageRecordSeed[] = []
  const endAt = new Date(Date.now() - 10 * minuteMs)
  const startAt = new Date(endAt)
  startAt.setUTCDate(endAt.getUTCDate() - options.days + 1)
  startAt.setUTCHours(0, 0, 0, 0)

  for (let dayIndex = 0; dayIndex < options.days; dayIndex += 1) {
    const dayStart = new Date(startAt.getTime() + dayIndex * dayMs)
    const latestForDay = dayIndex === options.days - 1
      ? endAt
      : new Date(dayStart.getTime() + dayMs - minuteMs)
    const minuteRange = Math.max(1, Math.floor((latestForDay.getTime() - dayStart.getTime()) / minuteMs))
    for (let requestIndex = 0; requestIndex < options.dailyRequests; requestIndex += 1) {
      const ordinal = dayIndex * options.dailyRequests + requestIndex
      const scenario = scenarios[weightedIndex(ordinal, scenarios.length)]
      const account = scenario.accounts[weightedIndex(ordinal + 11, scenario.accounts.length)]
      const minuteOfDay = Math.min(
        minuteRange,
        Math.floor(((requestIndex + pseudoRandom(ordinal, 2)) / options.dailyRequests) * minuteRange)
      )
      const createdAt = new Date(dayStart.getTime() + minuteOfDay * minuteMs + Math.floor(pseudoRandom(ordinal, 3) * 45_000))
      const record = buildUsageRecord({
        ordinal,
        createdAt: createdAt > latestForDay ? latestForDay : createdAt,
        scenario,
        account
      })
      records.push(record)
    }
  }

  for (const chunk of chunks(records, 500)) {
    repositories.createUsageRecordsBatch(chunk)
  }
  return records
}

function buildUsageRecord(input: {
  ordinal: number
  createdAt: Date
  scenario: KeyScenario
  account: AccountSummary
}): UsageRecordSeed {
  const endpointInfo = endpointForOrdinal(input.ordinal)
  const model = modelForOrdinal(input.ordinal)
  const forcedFailure = input.account.status === 'error'
    || input.account.status === 'disabled'
    || input.account.status === 'rate_limited'
    || input.account.status === 'temporary_unavailable'
  const failureRoll = pseudoRandom(input.ordinal, 10)
  const success = !forcedFailure && failureRoll > 0.11
  const error = success ? undefined : errorForOrdinal(input.ordinal, forcedFailure)
  const inputTokens = success ? 120 + Math.floor(pseudoRandom(input.ordinal, 20) * 6800) : Math.floor(pseudoRandom(input.ordinal, 21) * 300)
  const outputTokens = success ? 40 + Math.floor(pseudoRandom(input.ordinal, 22) * 2200) : Math.floor(pseudoRandom(input.ordinal, 23) * 80)
  const cacheReadTokens = success && input.ordinal % 5 === 0 ? Math.floor(inputTokens * (0.15 + pseudoRandom(input.ordinal, 24) * 0.4)) : 0
  const firstTokenMs = success && endpointInfo.path !== '/v1/models'
    ? 80 + Math.floor(pseudoRandom(input.ordinal, 30) * 1450)
    : undefined
  const durationMs = success
    ? (firstTokenMs ?? 60) + 300 + Math.floor(pseudoRandom(input.ordinal, 31) * 4200)
    : 90 + Math.floor(pseudoRandom(input.ordinal, 32) * 1200)
  const cost = usageCost(model, inputTokens, outputTokens, cacheReadTokens, success)
  const traceId = `${tracePrefix}usage-${String(input.ordinal + 1).padStart(5, '0')}`
  return {
    id: `${idPrefix}usage_${String(input.ordinal + 1).padStart(5, '0')}`,
    systemAccountId: input.scenario.owner.id,
    traceId,
    trafficSource: 'gateway',
    clientIp: `${input.scenario.clientIpBase}${20 + (input.ordinal % 180)}`,
    apiKeyId: input.scenario.key.id,
    groupId: input.scenario.group.id,
    accountId: input.account.id,
    endpoint: `${endpointInfo.method} ${endpointInfo.path}`,
    providerCode,
    model,
    stream: endpointInfo.stream,
    statusCode: success ? 200 : error?.statusCode,
    success,
    firstTokenMs,
    durationMs,
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheReadCostUsd: cost.cacheReadCost,
    inputImageTokens: input.ordinal % 37 === 0 ? 85 + (input.ordinal % 400) : undefined,
    outputImageTokens: input.ordinal % 97 === 0 ? 120 + (input.ordinal % 240) : undefined,
    costUsd: cost.totalCost,
    errorCode: error?.code,
    errorMessage: error?.message,
    requestSnapshot: {
      method: endpointInfo.method,
      path: endpointInfo.path,
      scenario: input.scenario.label,
      body: endpointInfo.path === '/v1/models'
        ? undefined
        : {
            model,
            stream: endpointInfo.stream,
            input: `Mockdata 请求 ${input.ordinal + 1}`,
            max_output_tokens: 1024
          }
    },
    responseSnapshot: success
      ? {
          status: 200,
          model,
          usage: {
            input_tokens: inputTokens,
            output_tokens: outputTokens,
            cache_read_tokens: cacheReadTokens
          }
        }
      : {
          status: error?.statusCode,
          error: {
            code: error?.code,
            message: error?.message
          }
        },
    createdAt: input.createdAt.toISOString()
  }
}

function endpointForOrdinal(ordinal: number): { method: string; path: string; stream: boolean } {
  if (ordinal % 11 === 0) return { method: 'GET', path: '/v1/models', stream: false }
  if (ordinal % 3 === 0) return { method: 'POST', path: '/v1/chat/completions', stream: ordinal % 2 === 0 }
  return { method: 'POST', path: '/v1/responses', stream: ordinal % 4 === 0 }
}

function modelForOrdinal(ordinal: number): string {
  const models = ['gpt-5.4-mini', 'gpt-5.4-mini', 'gpt-5.4', 'gpt-4.1-mini', 'gpt-4o-mini']
  return models[ordinal % models.length]
}

function errorForOrdinal(ordinal: number, forcedFailure: boolean): { statusCode: number; code: string; message: string } {
  if (forcedFailure && ordinal % 2 === 0) {
    return { statusCode: 429, code: 'rate_limit_exceeded', message: 'Mockdata 模拟账户当前处于冷却或限流状态' }
  }
  const errors = [
    { statusCode: 429, code: 'rate_limit_exceeded', message: 'Mockdata 模拟上游限流' },
    { statusCode: 503, code: 'service_unavailable', message: 'Mockdata 模拟上游维护' },
    { statusCode: 500, code: 'upstream_error', message: 'Mockdata 模拟上游内部错误' },
    { statusCode: 402, code: 'insufficient_balance', message: 'Mockdata 模拟服务商余额不足' },
    { statusCode: 401, code: 'invalid_api_key', message: 'Mockdata 模拟认证失败' }
  ]
  return errors[ordinal % errors.length]
}

function usageCost(model: string, inputTokens: number, outputTokens: number, cacheReadTokens: number, success: boolean): { totalCost: number; cacheReadCost: number } {
  if (!success) return { totalCost: 0, cacheReadCost: 0 }
  const price = modelPrices[model] ?? modelPrices['gpt-5.4-mini']
  const billableInputTokens = Math.max(0, inputTokens - cacheReadTokens)
  const cacheReadCost = roundCost((cacheReadTokens / 1_000_000) * price.cached)
  const totalCost = roundCost((billableInputTokens / 1_000_000) * price.input + (outputTokens / 1_000_000) * price.output + cacheReadCost)
  return { totalCost, cacheReadCost }
}

function createAuditMockdata(records: UsageRecordSeed[]): void {
  const auditLogs: AuditLogInput[] = records
    .filter((_, index) => index % 4 === 0)
    .map((record, index) => {
      const startedAt = record.createdAt
      const endedAt = new Date(Date.parse(startedAt) + (record.durationMs ?? 200)).toISOString()
      const retrySuccess = record.success && index % 13 === 0
      const auditOutcome = record.success
        ? retrySuccess ? 'success_after_retry' : 'success'
        : record.stream ? 'stream_failed' : 'upstream_failed'
      const firstAttemptFailed = retrySuccess
      return {
        id: `${idPrefix}audit_${String(index + 1).padStart(5, '0')}`,
        traceId: record.traceId,
        systemAccountId: record.systemAccountId,
        apiKeyId: record.apiKeyId,
        groupId: record.groupId,
        accountId: record.accountId,
        providerCode: record.providerCode,
        method: record.endpoint?.split(' ')[0] ?? 'POST',
        path: record.endpoint?.split(' ')[1] ?? '/v1/responses',
        model: record.model,
        stream: record.stream,
        clientIp: record.clientIp,
        userAgent: 'mockdata-client/1.0',
        auditOutcome,
        success: record.success,
        finalStatusCode: record.statusCode,
        errorPhase: record.success ? undefined : 'upstream_response',
        errorCode: record.errorCode,
        errorMessage: record.errorMessage,
        sampleBucket: index % 100,
        sampleReason: record.success ? 'mockdata_success_sample' : 'mockdata_failure_sample',
        startedAt,
        endedAt,
        durationMs: record.durationMs,
        firstTokenMs: record.firstTokenMs,
        attempts: [
          ...(firstAttemptFailed ? [{
            id: `${idPrefix}audit_attempt_${String(index + 1).padStart(5, '0')}_1`,
            tempId: `attempt-${index}-1`,
            attemptIndex: 1,
            accountId: record.accountId,
            accountOwnerSystemAccountId: record.accountOwnerSystemAccountId,
            groupId: record.groupId,
            providerCode: record.providerCode,
            upstreamMethod: record.endpoint?.split(' ')[0] ?? 'POST',
            upstreamUrl: `https://api.openai.com${record.endpoint?.split(' ')[1] ?? '/v1/responses'}`,
            upstreamStatusCode: 503,
            success: false,
            errorPhase: 'upstream_response',
            errorCode: 'service_unavailable',
            errorMessage: 'Mockdata 首次上游尝试失败',
            startedAt,
            endedAt: new Date(Date.parse(startedAt) + 180).toISOString(),
            durationMs: 180
          }] : []),
          {
            id: `${idPrefix}audit_attempt_${String(index + 1).padStart(5, '0')}_${firstAttemptFailed ? '2' : '1'}`,
            tempId: `attempt-${index}-final`,
            attemptIndex: firstAttemptFailed ? 2 : 1,
            accountId: record.accountId,
            accountOwnerSystemAccountId: record.accountOwnerSystemAccountId,
            groupId: record.groupId,
            providerCode: record.providerCode,
            upstreamMethod: record.endpoint?.split(' ')[0] ?? 'POST',
            upstreamUrl: `https://api.openai.com${record.endpoint?.split(' ')[1] ?? '/v1/responses'}`,
            upstreamStatusCode: record.statusCode,
            success: record.success,
            errorPhase: record.success ? undefined : 'upstream_response',
            errorCode: record.errorCode,
            errorMessage: record.errorMessage,
            startedAt: firstAttemptFailed ? new Date(Date.parse(startedAt) + 220).toISOString() : startedAt,
            endedAt,
            durationMs: record.durationMs
          }
        ],
        payloads: [
          {
            id: `${idPrefix}audit_payload_${String(index + 1).padStart(5, '0')}_client`,
            attemptTempId: `attempt-${index}-final`,
            partType: 'client_request',
            sequenceIndex: 0,
            contentType: 'application/json',
            headers: {
              'content-type': 'application/json',
              'x-mockdata': 'true'
            },
            body: JSON.stringify(record.requestSnapshot ?? {})
          },
          {
            id: `${idPrefix}audit_payload_${String(index + 1).padStart(5, '0')}_response`,
            attemptTempId: `attempt-${index}-final`,
            partType: record.success ? 'upstream_response' : 'gateway_error',
            sequenceIndex: 1,
            contentType: 'application/json',
            body: JSON.stringify(record.responseSnapshot ?? {})
          }
        ],
        createdAt: record.createdAt
      }
    })

  for (const chunk of chunks(auditLogs, 200)) {
    repositories.createAuditLogsBatch(chunk)
  }
}

function createPublicApiLogMockdata(created: CreatedMockdata, options: MockdataOptions): number {
  const endpoints = [
    { method: 'GET', path: '/__aipublic__/ip/usage', query: 'range=last_7_days&page=1&pageSize=20', scope: 'ip_usage' },
    { method: 'GET', path: '/__aipublic__/account/usage', query: 'range=last_30_days&page=1&pageSize=20', scope: 'account_usage' },
    { method: 'GET', path: '/__aipublic__/consumption/ranking', query: 'range=last_7_days&metric=cost', scope: 'ranking' },
    { method: 'GET', path: '/__aipublic__/access/info', query: '', scope: 'access_info' },
    { method: 'GET', path: '/__aipublic__/group/list', query: `targetUsername=${created.users.admin.username}&providerCode=openai`, scope: 'group_list' },
    { method: 'GET', path: '/__aipublic__/api-key/list', query: `targetUsername=${created.users.admin.username}`, scope: 'api_key_list' },
    { method: 'GET', path: '/__aipublic__/account/list', query: `targetUsername=${created.users.admin.username}&providerCode=openai`, scope: 'account_list' },
    { method: 'POST', path: '/__aipublic__/group/add', query: '', scope: 'group_write' },
    { method: 'POST', path: '/__aipublic__/group/update', query: '', scope: 'group_write' },
    { method: 'POST', path: '/__aipublic__/group/del', query: '', scope: 'group_write' },
    { method: 'POST', path: '/__aipublic__/api-key/add', query: '', scope: 'api_key_write' },
    { method: 'POST', path: '/__aipublic__/api-key/update', query: '', scope: 'api_key_write' },
    { method: 'POST', path: '/__aipublic__/api-key/del', query: '', scope: 'api_key_write' },
    { method: 'POST', path: '/__aipublic__/account/add', query: '', scope: 'account_write' },
    { method: 'POST', path: '/__aipublic__/account/update', query: '', scope: 'account_write' },
    { method: 'POST', path: '/__aipublic__/account/del', query: '', scope: 'account_write' }
  ]
  const perDay = Math.min(60, Math.max(12, Math.ceil(options.dailyRequests / 20)))
  const total = options.days * perDay
  const endAt = Date.now() - 20 * minuteMs
  const startAt = endAt - (options.days - 1) * dayMs
  const builtInTestSource = findExternalIntegrationSource(builtInExternalIntegrationTestSourceId)
  const builtInTestToken = builtInTestSource?.tokens.find((token) => token.id === builtInExternalIntegrationTestTokenId)
  for (let index = 0; index < total; index += 1) {
    const dayIndex = Math.floor(index / perDay)
    const indexInDay = index % perDay
    const endpoint = endpoints[index % endpoints.length]
    const startedAtMs = startAt + dayIndex * dayMs + Math.floor((indexInDay / perDay) * (dayMs - minuteMs))
    const durationMs = 40 + Math.floor(pseudoRandom(index, 70) * 1200)
    const status = publicApiLogStatus(index, endpoint.method)
    const success = status >= 200 && status < 300
    const useTestToken = index % 6 === 0
    const source = index % 5 === 0 ? created.externalSources.readonly : created.externalSources.primary
    const token = source.token
    createPublicApiLog({
      id: `${idPrefix}public_api_log_${String(index + 1).padStart(5, '0')}`,
      traceId: `${tracePrefix}public-api-${String(index + 1).padStart(5, '0')}`,
      sourceRefId: useTestToken ? builtInExternalIntegrationTestSourceId : source.source.id,
      sourceName: useTestToken ? builtInTestSource?.name ?? '内置测试来源' : source.source.name,
      tokenId: useTestToken ? builtInExternalIntegrationTestTokenId : token.id,
      tokenName: useTestToken ? '内置测试 Token' : token.name,
      tokenPrefix: useTestToken ? builtInTestToken?.tokenPrefix ?? 'juis_...' : token.tokenPrefix,
      isTestToken: useTestToken,
      method: endpoint.method,
      path: endpoint.path,
      queryString: endpoint.query || undefined,
      clientIp: `172.20.${index % 16}.${20 + (index % 180)}`,
      userAgent: index % 7 === 0 ? 'mockdata-public-bot/1.0' : 'mockdata-public-client/1.0',
      statusCode: status,
      success,
      durationMs,
      requestSizeBytes: endpoint.method === 'POST' ? 320 + (index % 2048) : 80 + (index % 512),
      responseSizeBytes: success ? 1200 + (index % 12000) : 220 + (index % 1200),
      requestCaptureStatus: index % 19 === 0 ? 'truncated' : endpoint.method === 'POST' ? 'complete' : 'empty',
      responseCaptureStatus: success ? (index % 23 === 0 ? 'truncated' : 'complete') : 'complete',
      requestData: publicApiLogRequestData(endpoint, created, index),
      responseData: publicApiLogResponseData(endpoint, success, index),
      errorCode: success ? undefined : publicApiLogErrorCode(status),
      errorMessage: success ? undefined : publicApiLogErrorMessage(status),
      startedAt: new Date(startedAtMs).toISOString(),
      endedAt: new Date(startedAtMs + durationMs).toISOString(),
      createdAt: new Date(startedAtMs + durationMs).toISOString()
    })
  }
  return total
}

function publicApiLogStatus(index: number, method: string): number {
  if (index % 29 === 0) return 401
  if (index % 23 === 0) return 403
  if (index % 19 === 0) return 429
  if (index % 17 === 0) return 400
  if (index % 13 === 0) return 500
  return method === 'POST' && index % 4 === 0 ? 201 : 200
}

function publicApiLogErrorCode(status: number): string {
  if (status === 401) return 'external_source_token_invalid'
  if (status === 403) return 'external_source_scope_denied'
  if (status === 429) return 'external_source_rate_limited'
  if (status === 400) return 'bad_request'
  return 'public_api_internal_error'
}

function publicApiLogErrorMessage(status: number): string {
  if (status === 401) return 'Mockdata 模拟来源 Token 无效'
  if (status === 403) return 'Mockdata 模拟来源系统缺少接口权限'
  if (status === 429) return 'Mockdata 模拟公开接口触发限流'
  if (status === 400) return 'Mockdata 模拟公开接口参数无效'
  return 'Mockdata 模拟公开接口内部错误'
}

function publicApiLogRequestData(
  endpoint: { method: string; path: string; scope: string },
  created: CreatedMockdata,
  index: number
): Record<string, unknown> {
  if (endpoint.method === 'GET') {
    return {
      query: endpoint.scope,
      page: 1 + (index % 5),
      pageSize: 20
    }
  }
  if (endpoint.path.includes('/group/')) {
    return {
      targetUsername: created.users.admin.username,
      providerCode,
      groupId: created.groups.main.id,
      name: `${namePrefix}公开接口分组 ${index % 9}`
    }
  }
  if (endpoint.path.includes('/api-key/')) {
    return {
      targetUsername: created.users.admin.username,
      apiKeyId: created.apiKeys.adminMain.id,
      groupBindings: [{ groupId: created.groups.main.id, priority: 1, status: 'active' }]
    }
  }
  return {
    targetUsername: created.users.admin.username,
    providerCode,
    accountId: created.accounts.primary.id,
    targetGroupName: `${namePrefix}公开接口账号分组`
  }
}

function publicApiLogResponseData(endpoint: { path: string; scope: string }, success: boolean, index: number): Record<string, unknown> {
  if (!success) {
    return {
      message: publicApiLogErrorMessage(publicApiLogStatus(index, endpoint.path)),
      code: publicApiLogErrorCode(publicApiLogStatus(index, endpoint.path))
    }
  }
  if (endpoint.path.includes('/ranking')) {
    return {
      source: 'stats',
      items: [
        { rank: 1, clientIp: `172.20.1.${20 + (index % 20)}`, totalCost: roundCost(4.2 + index / 1000) }
      ]
    }
  }
  if (endpoint.path.includes('/list')) {
    return {
      source: 'stats',
      page: 1,
      pageSize: 20,
      items: [{ id: `mockdata_public_item_${index}`, name: `${namePrefix}公开接口返回项` }]
    }
  }
  return {
    source: 'stats',
    action: endpoint.scope.includes('write') ? 'created' : 'read',
    mock: false
  }
}

function createOperationMockdata(created: CreatedMockdata, usageRecords: UsageRecordSeed[]): void {
  const resources = [
    { module: 'accounts', resourceType: 'account', resourceId: created.accounts.primary.id, resourceName: created.accounts.primary.name, action: 'create', summary: '创建主力 API Key 账户' },
    { module: 'accounts', resourceType: 'account', resourceId: created.accounts.rateLimited.id, resourceName: created.accounts.rateLimited.name, action: 'cooldown', summary: '标记账户限流冷却' },
    { module: 'groups', resourceType: 'group', resourceId: created.groups.main.id, resourceName: created.groups.main.name, action: 'create', summary: '创建主力分组' },
    { module: 'api_keys', resourceType: 'api_key', resourceId: created.apiKeys.adminMain.id, resourceName: created.apiKeys.adminMain.name, action: 'create', summary: '创建主力本地网关 Key' },
    { module: 'authorizations', resourceType: 'authorization', resourceId: created.authorizations[0]?.id, resourceName: '研发用户分组授权', action: 'create', summary: '创建研发用户分组授权' },
    { module: 'system_teams', resourceType: 'system_team', resourceId: created.teams.devTeam.id, resourceName: created.teams.devTeam.name, action: 'update_members', summary: '维护研发团队成员' },
    { module: 'proxies', resourceType: 'proxy', resourceId: `${idPrefix}proxy`, resourceName: `${namePrefix}HTTP 代理`, action: 'test', summary: '完成代理连通性测试' },
    { module: 'announcements', resourceType: 'announcement', resourceId: `${idPrefix}announcement`, resourceName: `${namePrefix}系统维护公告`, action: 'publish', summary: '发布系统维护公告' },
    { module: 'settings', resourceType: 'system_settings', resourceId: 'sys_admin', resourceName: '系统设置', action: 'update', summary: '调整统计聚合和数据保留参数' },
    { module: 'usage_records', resourceType: 'usage_record', resourceId: usageRecords[0]?.id, resourceName: '使用记录', action: 'query', summary: '查询近 1 月使用记录' }
  ]
  const logs: OperationLogInput[] = []
  for (let index = 0; index < 90; index += 1) {
    const resource = resources[index % resources.length]
    const actor = index % 7 === 0 ? created.users.dev : index % 11 === 0 ? created.users.ops : created.users.admin
    const createdAt = new Date(Date.now() - Math.floor((index / 90) * 30 * dayMs)).toISOString()
    logs.push({
      id: `${idPrefix}operation_${String(index + 1).padStart(4, '0')}`,
      traceId: usageRecords[index * 5 % usageRecords.length]?.traceId,
      actorSystemAccountId: actor.id,
      actorUsername: actor.username,
      actorDisplayName: actor.displayName,
      actorRole: actor.role,
      operationScopeSystemAccountId: created.users.admin.id,
      mode: actor.id === created.users.admin.id ? 'self' : 'admin',
      module: resource.module,
      action: resource.action,
      operationKey: `mockdata.${resource.module}.${resource.action}`,
      resourceType: resource.resourceType,
      resourceId: resource.resourceId,
      resourceName: resource.resourceName,
      summary: `${namePrefix}${resource.summary}`,
      detailLevel: index % 5 === 0 ? 'summary' : 'full',
      visibilityScope: 'targeted',
      changes: [
        {
          field: 'status',
          label: '状态',
          before: index % 3 === 0 ? 'disabled' : 'draft',
          after: index % 3 === 0 ? 'active' : 'published'
        }
      ],
      metadata: {
        source: 'mockdata',
        batch: 'admin-full-business',
        index
      },
      method: index % 2 === 0 ? 'POST' : 'PATCH',
      path: `/__aisys__/api/mockdata/${resource.module}`,
      statusCode: index % 17 === 0 ? 409 : 200,
      clientIp: `10.30.0.${20 + index}`,
      userAgent: 'mockdata-admin/1.0',
      targets: [
        {
          targetType: resource.resourceType,
          targetId: resource.resourceId,
          targetName: resource.resourceName,
          targetOwnerSystemAccountId: created.users.admin.id,
          relation: 'primary'
        }
      ],
      viewers: [
        {
          systemAccountId: created.users.dev.id,
          visibilityReason: 'authorization_grantee',
          detailLevel: index % 3 === 0 ? 'summary' : 'full'
        },
        {
          systemAccountId: created.users.ops.id,
          visibilityReason: 'team_member',
          detailLevel: 'summary'
        }
      ],
      createdAt
    })
  }
  repositories.createOperationLogsBatch(logs)
}

function createRuntimeLogMockdata(usageRecords: UsageRecordSeed[]): void {
  const recentRecords = usageRecords.slice(-240)
  const events = [
    'gateway_upstream_request_started',
    'gateway_upstream_response_received',
    'gateway_stream_finished_success',
    'gateway_upstream_attempt_failed',
    'background_usage_stats_aggregation_failed',
    'background_account_quality_refresh_completed',
    'db_service_started',
    'http_request_completed'
  ]
  const logs: RuntimeLogIndexInput[] = recentRecords.map((record, index) => {
    const level = record.success ? (index % 9 === 0 ? 'debug' : 'info') : (index % 5 === 0 ? 'error' : 'warn')
    const event = record.success ? events[index % 3] : events[3 + (index % 2)]
    const message = record.success
      ? `Mockdata 网关请求完成：${record.model}`
      : `Mockdata 网关请求失败：${record.errorCode}`
    return {
      id: `${idPrefix}runtime_${String(index + 1).padStart(4, '0')}`,
      logFile: join(backendRoot, 'logs', 'mockdata.log'),
      logOffset: index * 512,
      lineNumber: index + 1,
      time: new Date(Date.now() - Math.floor(((recentRecords.length - index) / recentRecords.length) * 3 * dayMs)).toISOString(),
      level,
      traceId: record.traceId,
      event,
      message,
      errorMessage: record.success ? undefined : record.errorMessage,
      rawJson: JSON.stringify({
        time: record.createdAt,
        level,
        event,
        traceId: record.traceId,
        message,
        mockdata: true,
        accountId: record.accountId,
        groupId: record.groupId,
        apiKeyId: record.apiKeyId
      }),
      createdAt: new Date(Date.now() - Math.floor(((recentRecords.length - index) / recentRecords.length) * 3 * dayMs)).toISOString()
    }
  })
  createRuntimeLogsBatch(logs)
}

function createModelCheckMockdata(created: CreatedMockdata, options: MockdataOptions): ModelCheckMockdataCounts {
  const devPrimaryInstance = authorizationInstanceAccount(created.accounts.primary, created.users.dev)
  const testerPrimaryInstance = authorizationInstanceAccount(created.accounts.primary, created.users.tester)
  const opsProxiedInstance = authorizationInstanceAccount(created.accounts.proxied, created.users.ops)
  const financeOauthInstance = authorizationInstanceAccount(created.accounts.oauth, created.users.finance)
  const viewerBurstImageInstance = authorizationInstanceAccount(created.accounts.burstImage, created.users.viewer)
  const targets: ModelCheckTargetSeed[] = [
    { account: created.accounts.primary, group: created.groups.main, apiKey: created.apiKeys.adminMain, actor: created.users.admin, comparisonAccount: created.accounts.oauth },
    { account: created.accounts.burstFast, group: created.groups.highConcurrency, apiKey: created.apiKeys.adminHighConcurrency, actor: created.users.admin, comparisonAccount: created.accounts.burstImage },
    { account: devPrimaryInstance, group: created.groups.devDefault, apiKey: created.apiKeys.devGroupAuthorized, actor: created.users.dev },
    { account: created.accounts.normal, group: created.groups.main, apiKey: created.apiKeys.adminHighFrequency, actor: created.users.admin, comparisonAccount: created.accounts.burstFast },
    { account: testerPrimaryInstance, group: created.groups.testerDefault, apiKey: created.apiKeys.testerTeamAuthorized, actor: created.users.tester },
    { account: opsProxiedInstance, group: created.groups.opsDefault, apiKey: created.apiKeys.opsAccountAuthorized, actor: created.users.ops },
    { account: created.accounts.oauth, group: created.groups.oauth, apiKey: created.apiKeys.adminOAuth, actor: created.users.admin, comparisonAccount: created.accounts.oauthBackup },
    { account: created.accounts.oauthBackup, group: created.groups.oauth, apiKey: created.apiKeys.adminOAuth, actor: created.users.admin, comparisonAccount: created.accounts.oauth },
    { account: financeOauthInstance, group: created.groups.financeDefault, apiKey: created.apiKeys.financeAuthorized, actor: created.users.finance },
    { account: viewerBurstImageInstance, group: created.groups.viewerDefault, apiKey: created.apiKeys.viewerAuthorized, actor: created.users.viewer },
    { account: created.accounts.rateLimited, group: created.groups.backup, apiKey: created.apiKeys.adminBackup, actor: created.users.admin, comparisonAccount: created.accounts.primary },
    { account: created.accounts.temporary, group: created.groups.experiment, apiKey: created.apiKeys.adminExpired, actor: created.users.admin, comparisonAccount: created.accounts.normal }
  ]
  const runCount = Math.min(120, Math.max(36, options.days))
  let itemCount = 0

  for (let index = 0; index < runCount; index += 1) {
    const target = targets[index % targets.length]
    const model = index % 3 === 0 ? 'gpt-5.5' : 'gpt-5.4'
    const runStatus = modelCheckRunStatusForIndex(index)
    const trustedComparison = Boolean(target.comparisonAccount) && index % 3 === 0
    const startedAtMs = Date.now() - 20 * minuteMs - Math.floor((index / Math.max(1, runCount - 1)) * options.days * dayMs)
    const startedAt = new Date(startedAtMs).toISOString()
    const runId = `${idPrefix}model_check_run_${String(index + 1).padStart(4, '0')}`
    const traceId = `${tracePrefix}model-check-${String(index + 1).padStart(4, '0')}`
    const checks = buildModelCheckItems({
      runIndex: index,
      runId,
      model,
      startedAtMs,
      trustedComparison,
      runStatus
    })
    const level = modelCheckLevelForRun(index, runStatus, checks.score, checks.maxScore)
    const message = modelCheckRunMessage(runStatus, level, checks.score, checks.maxScore)

    repositories.createModelCheckRun({
      id: runId,
      systemAccountId: target.actor.id,
      actorSystemAccountId: target.actor.id,
      providerCode,
      targetType: 'account',
      targetId: target.account.id,
      targetName: target.account.name,
      targetOwnerSystemAccountId: target.account.ownerSystemAccountId ?? target.account.systemAccountId ?? created.users.admin.id,
      accountId: target.account.id,
      groupId: target.group.id,
      apiKeyId: target.apiKey.id,
      model,
      profile: 'full',
      trustedComparison,
      trustedComparisonAvailable: trustedComparison && index % 4 !== 0,
      traceId,
      probeSetVersion: 'openai-model-check-v1',
      startedAt,
      requestSummary: {
        targetType: 'account',
        targetId: target.account.id,
        targetName: target.account.name,
        model,
        profile: 'full',
        trustedComparison,
        trustedComparisonAccountId: trustedComparison ? target.comparisonAccount?.id : undefined,
        trustedComparisonAccountName: trustedComparison ? target.comparisonAccount?.name : undefined,
        groupId: target.group.id,
        groupName: target.group.name,
        apiKeyId: target.apiKey.id,
        actorSystemAccountId: target.actor.id,
        generatedBy: 'mockdata'
      }
    })
    repositories.createModelCheckItems(runId, checks.items)
    itemCount += checks.items.length

    if (runStatus !== 'running') {
      const durationMs = checks.items.reduce((sum, item) => sum + (item.durationMs ?? 0), 0)
      repositories.finishModelCheckRun(runId, {
        level,
        score: checks.score,
        maxScore: checks.maxScore,
        status: runStatus,
        message,
        finishedAt: new Date(startedAtMs + durationMs + 800).toISOString(),
        durationMs: durationMs + 800,
        resultSummary: {
          verdict: message,
          passedItems: checks.items.filter((item) => item.status === 'passed').length,
          warningItems: checks.items.filter((item) => item.status === 'warning').length,
          failedItems: checks.items.filter((item) => item.status === 'failed').length,
          skippedItems: checks.items.filter((item) => item.status === 'skipped').length,
          trustedComparison,
          generatedBy: 'mockdata'
        },
        errorCode: runStatus === 'failed' ? 'mockdata_model_check_failed' : undefined,
        errorMessage: runStatus === 'failed' ? 'Mockdata 模拟上游探针失败' : undefined
      })
    }
  }

  return {
    runs: runCount,
    items: itemCount
  }
}

function buildModelCheckItems(input: {
  runIndex: number
  runId: string
  model: 'gpt-5.5' | 'gpt-5.4'
  startedAtMs: number
  trustedComparison: boolean
  runStatus: MockModelCheckRunStatus
}): {
  items: Array<{
    id: string
    itemKey: string
    itemType: string
    status: MockModelCheckItemStatus
    score: number
    maxScore: number
    durationMs: number
    traceId: string
    evidenceSummary: Record<string, unknown>
    errorCode?: string
    errorMessage?: string
    createdAt: string
  }>
  score: number
  maxScore: number
} {
  const definitions = [
    ['target.model_catalog', 'model_catalog', 10],
    ['target.responses_basic', 'responses_basic', 15],
    ['target.responses_stream', 'responses_stream', 15],
    ['target.structured_output', 'structured_output', 15],
    ['target.tool_calling', 'tool_calling', 10],
    ['target.behavior_probe', 'behavior_probe', 15],
    ['target.long_context', 'long_context', 10],
    ['target.stability_a', 'stability', 10],
    ...(input.trustedComparison ? [['trusted_comparison.comparison', 'trusted_comparison', 10] as const] : [])
  ] as const
  const items = definitions.map(([itemKey, itemType, maxScore], itemIndex) => {
    let status: MockModelCheckItemStatus = 'passed'
    let score: number = maxScore
    let errorCode: string | undefined
    let errorMessage: string | undefined
    if (input.runStatus === 'running' && itemIndex > 1) {
      status = 'skipped'
      score = 0
    } else if (input.runStatus === 'canceled' && itemIndex > 3) {
      status = 'skipped'
      score = 0
    } else if (input.runStatus === 'failed' && itemIndex === 2) {
      status = 'failed'
      score = 0
      errorCode = 'mockdata_probe_failed'
      errorMessage = 'Mockdata 模拟流式探针响应中断'
    } else if ((input.runIndex + itemIndex) % 17 === 0) {
      status = 'failed'
      score = Math.max(0, Math.floor(maxScore * 0.35))
      errorCode = 'mockdata_low_similarity'
      errorMessage = 'Mockdata 模拟输出特征偏离可信基线'
    } else if ((input.runIndex + itemIndex) % 7 === 0) {
      status = 'warning'
      score = Math.max(0, maxScore - 4)
    }
    const durationMs = 420 + ((input.runIndex + 1) * (itemIndex + 3) * 137) % 2600
    const createdAt = new Date(input.startedAtMs + (itemIndex + 1) * 1200).toISOString()
    const traceId = `${tracePrefix}model-check-${String(input.runIndex + 1).padStart(4, '0')}-${String(itemIndex + 1).padStart(2, '0')}`
    return {
      id: `${idPrefix}model_check_item_${String(input.runIndex + 1).padStart(4, '0')}_${String(itemIndex + 1).padStart(2, '0')}`,
      itemKey,
      itemType,
      status,
      score,
      maxScore,
      durationMs,
      traceId,
      evidenceSummary: {
        message: modelCheckItemMessage(status, itemType),
        responseModel: status === 'failed' ? 'unknown' : input.model,
        statusCode: status === 'failed' ? 502 : 200,
        latencyMs: durationMs,
        sample: `mockdata-${itemType}-${input.runIndex + 1}`
      },
      errorCode,
      errorMessage,
      createdAt
    }
  })
  return {
    items,
    score: items.reduce((sum, item) => sum + item.score, 0),
    maxScore: items.reduce((sum, item) => sum + item.maxScore, 0)
  }
}

function modelCheckRunStatusForIndex(index: number): MockModelCheckRunStatus {
  if (index % 41 === 0) return 'running'
  if (index % 29 === 0) return 'canceled'
  if (index % 13 === 0) return 'failed'
  return 'completed'
}

function modelCheckLevelForScore(score: number, maxScore: number): MockModelCheckLevel {
  const ratio = maxScore > 0 ? score / maxScore : 0
  if (ratio >= 0.92) return 'high_confidence'
  if (ratio >= 0.78) return 'likely'
  if (ratio >= 0.58) return 'uncertain'
  if (ratio > 0) return 'suspicious'
  return 'unavailable'
}

function modelCheckLevelForRun(index: number, status: MockModelCheckRunStatus, score: number, maxScore: number): MockModelCheckLevel {
  if (status !== 'completed') return 'unavailable'
  const base = modelCheckLevelForScore(score, maxScore)
  if (index % 12 === 0 && base !== 'high_confidence') return 'likely'
  if (index % 10 === 0 && base === 'high_confidence') return 'uncertain'
  return base
}

function modelCheckRunMessage(status: MockModelCheckRunStatus, level: MockModelCheckLevel, score: number, maxScore: number): string {
  if (status === 'running') return 'Mockdata 模拟检测仍在运行，等待后续探针完成'
  if (status === 'failed') return 'Mockdata 模拟检测失败：流式探针响应中断'
  if (status === 'canceled') return 'Mockdata 模拟检测已手动停止'
  const labels: Record<MockModelCheckLevel, string> = {
    high_confidence: '高可信',
    likely: '较可信',
    uncertain: '需复核',
    suspicious: '疑似异常',
    unavailable: '不可用'
  }
  return `Mockdata 检测完成：${labels[level]}，得分 ${score}/${maxScore}`
}

function modelCheckItemMessage(status: MockModelCheckItemStatus, itemType: string): string {
  if (status === 'passed') return `Mockdata ${itemType} 探针通过`
  if (status === 'warning') return `Mockdata ${itemType} 探针存在轻微偏差`
  if (status === 'failed') return `Mockdata ${itemType} 探针失败`
  return `Mockdata ${itemType} 探针已跳过`
}

function createRecordCleanupMockdata(created: CreatedMockdata): RecordCleanupMockdataCounts {
  const database = getDatasetDatabase()
  const now = nowIso()
  const accountTargets = [
    {
      accountId: `${idPrefix}display_deleted_account_01`,
      relatedAccountIds: [`${idPrefix}display_related_account_01`],
      authorizationIds: [`${idPrefix}display_authorization_01`],
      teamScopeIds: [`${idPrefix}display_team_scope_01`],
      attemptCount: 2,
      lastBlockedReason: `${namePrefix}等待 usage shard 短事务空闲`,
      lastErrorMessage: 'Mockdata 模拟账号相关记录清理遇到 SQLite 写锁'
    },
    {
      accountId: `${idPrefix}display_deleted_account_02`,
      relatedAccountIds: [],
      authorizationIds: [`${idPrefix}display_authorization_02`, `${idPrefix}display_authorization_03`],
      teamScopeIds: [`${idPrefix}display_team_scope_02`],
      attemptCount: 1,
      lastBlockedReason: `${namePrefix}等待授权窗口扣减完成`,
      lastErrorMessage: null
    },
    {
      accountId: `${idPrefix}display_deleted_account_03`,
      relatedAccountIds: [`${idPrefix}display_related_account_03`],
      authorizationIds: [],
      teamScopeIds: [],
      attemptCount: 0,
      lastBlockedReason: `${namePrefix}待后台维护任务处理`,
      lastErrorMessage: null
    }
  ]
  const apiKeyTargets = [
    { apiKeyId: `${idPrefix}display_deleted_api_key_01`, attemptCount: 2, reason: `${namePrefix}等待统计扣减重试`, error: 'Mockdata 模拟 API Key 清理锁竞争' },
    { apiKeyId: `${idPrefix}display_deleted_api_key_02`, attemptCount: 1, reason: `${namePrefix}等待数据集索引清理`, error: null },
    { apiKeyId: `${idPrefix}display_deleted_api_key_03`, attemptCount: 0, reason: `${namePrefix}待后台维护任务处理`, error: null }
  ]
  const insertAccount = database.prepare(`
    INSERT INTO account_record_cleanup_targets (
      account_id, system_account_id, related_account_ids_json, authorization_ids_json, team_scope_ids_json,
      created_at, updated_at, attempt_count, last_attempt_at, last_blocked_reason, last_error_message
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const insertApiKey = database.prepare(`
    INSERT INTO api_key_record_cleanup_targets (
      api_key_id, system_account_id, created_at, updated_at, attempt_count,
      last_attempt_at, last_blocked_reason, last_error_message
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `)
  database.exec('BEGIN')
  try {
    accountTargets.forEach((target, index) => {
      const createdAt = new Date(Date.now() - (index + 1) * dayMs).toISOString()
      insertAccount.run(
        target.accountId,
        '',
        JSON.stringify(target.relatedAccountIds),
        JSON.stringify(target.authorizationIds),
        JSON.stringify(target.teamScopeIds),
        createdAt,
        now,
        target.attemptCount,
        target.attemptCount > 0 ? new Date(Date.now() - (index + 2) * 60 * minuteMs).toISOString() : null,
        target.lastBlockedReason,
        target.lastErrorMessage
      )
    })
    apiKeyTargets.forEach((target, index) => {
      const createdAt = new Date(Date.now() - (index + 1) * dayMs).toISOString()
      insertApiKey.run(
        target.apiKeyId,
        '',
        createdAt,
        now,
        target.attemptCount,
        target.attemptCount > 0 ? new Date(Date.now() - (index + 3) * 60 * minuteMs).toISOString() : null,
        target.reason,
        target.error
      )
    })
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  return {
    accountTargets: accountTargets.length,
    apiKeyTargets: apiKeyTargets.length
  }
}

function createClientIpPolicyMockdata(created: CreatedMockdata): ClientIpPolicyMockdataCounts {
  const database = getStatsDatabase()
  const rows = database.prepare(`
    SELECT ip_hash, client_ip, aggregate_ip_key, last_seen_at
    FROM client_ip_registry
    WHERE client_ip LIKE '10.10.%'
       OR client_ip LIKE '10.20.%'
    ORDER BY last_seen_at DESC, ip_hash ASC
    LIMIT 8
  `).all() as Array<{ ip_hash: string; client_ip: string; aggregate_ip_key: string; last_seen_at?: string | null }>
  if (!rows.length) {
    return { policies: 0, policyHits: 0 }
  }
  const now = nowIso()
  const insertPolicy = database.prepare(`
    INSERT INTO client_ip_policies (
      id, ip_hash, status, reason, expires_at, created_by_system_account_id,
      created_at, updated_at, disabled_at, disabled_by_system_account_id, disabled_reason
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  database.exec('BEGIN')
  try {
    rows.forEach((row, index) => {
      const status = index % 4 === 3 ? 'disabled' : 'active'
      const createdAt = new Date(Date.now() - (index + 1) * 6 * 60 * minuteMs).toISOString()
      insertPolicy.run(
        `${idPrefix}client_ip_policy_${String(index + 1).padStart(2, '0')}`,
        row.ip_hash,
        status,
        index % 3 === 0 ? `${namePrefix}高错误率自动封禁样例` : `${namePrefix}公益接口异常流量观察`,
        status === 'active' && index % 2 === 0 ? new Date(Date.now() + (index + 1) * dayMs).toISOString() : null,
        created.users.admin.id,
        createdAt,
        now,
        status === 'disabled' ? new Date(Date.now() - index * 60 * minuteMs).toISOString() : null,
        status === 'disabled' ? created.users.admin.id : null,
        status === 'disabled' ? `${namePrefix}人工解除封禁样例` : null
      )
    })
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }

  const hits = rows.flatMap((row, index) => {
    const policyId = `${idPrefix}client_ip_policy_${String(index + 1).padStart(2, '0')}`
    return Array.from({ length: Math.min(14, Math.max(4, Math.floor(index + 4))) }, (_, dayIndex) => ({
      ipHash: row.ip_hash,
      policyId,
      hitCount: 1 + ((index + dayIndex) % 9),
      hitAt: new Date(Date.now() - dayIndex * dayMs - index * 20 * minuteMs).toISOString()
    }))
  })
  const recorded = recordClientIpPolicyHits(hits).recorded
  return {
    policies: rows.length,
    policyHits: recorded
  }
}

function createMonitoringMockdata(options: MockdataOptions): void {
  const database = getStatsDatabase()
  const now = Date.now() - 10 * minuteMs
  const start = now - (options.days * dayMs)
  const insertMetric = database.prepare(`
    INSERT INTO system_metrics_samples (
      id, sampled_at, cpu_percent, memory_used_percent, memory_total_bytes, memory_free_bytes,
      process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes, event_loop_lag_ms,
      network_rx_bytes_per_sec, network_tx_bytes_per_sec, network_rx_total_bytes, network_tx_total_bytes,
      db_file_bytes, stats_lag_seconds, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const insertProcess = database.prepare(`
    INSERT INTO process_event_loop_samples (
      id, sampled_at, process_role, process_pid, event_loop_lag_ms, created_at
    ) VALUES (?, ?, ?, ?, ?, ?)
  `)
  const roles = ['server', 'worker', 'db-service'] as const
  let metricIndex = 0
  database.exec('BEGIN')
  try {
    for (let timestamp = start; timestamp <= now; timestamp += 60 * minuteMs) {
      const sampledAt = new Date(timestamp).toISOString()
      const wave = Math.sin(metricIndex / 9)
      const cpu = 18 + wave * 8 + (metricIndex % 11)
      const memoryPercent = 46 + Math.cos(metricIndex / 13) * 9
      insertMetric.run(
        `${idPrefix}metric_${String(metricIndex + 1).padStart(5, '0')}`,
        sampledAt,
        roundNumber(cpu, 2),
        roundNumber(memoryPercent, 2),
        16 * 1024 * 1024 * 1024,
        Math.floor((100 - memoryPercent) / 100 * 16 * 1024 * 1024 * 1024),
        420 * 1024 * 1024 + metricIndex * 2048,
        130 * 1024 * 1024 + metricIndex * 1024,
        256 * 1024 * 1024,
        roundNumber(5 + Math.abs(Math.sin(metricIndex / 5)) * 18, 2),
        roundNumber(1024 * (20 + (metricIndex % 30)), 2),
        roundNumber(1024 * (14 + (metricIndex % 24)), 2),
        2_000_000_000 + metricIndex * 1024 * 20,
        1_400_000_000 + metricIndex * 1024 * 14,
        180 * 1024 * 1024 + metricIndex * 2048,
        metricIndex % 19 === 0 ? 30 + metricIndex % 120 : 0,
        sampledAt
      )
      roles.forEach((role, roleIndex) => {
        insertProcess.run(
          `${idPrefix}process_metric_${String(metricIndex + 1).padStart(5, '0')}_${role}`,
          sampledAt,
          role,
          31000 + roleIndex,
          roundNumber(2 + Math.abs(Math.sin((metricIndex + roleIndex) / 4)) * (roleIndex + 3), 2),
          sampledAt
        )
      })
      metricIndex += 1
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  rebuildSystemMetricsHourly(database)
  rebuildProcessEventLoopHourly(database)
}

function createStorageMockdata(created: CreatedMockdata, options: MockdataOptions): void {
  const database = getStatsDatabase()
  const now = Date.now() - 10 * minuteMs
  const insertDatabase = database.prepare(`
    INSERT INTO database_storage_snapshots (
      id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes,
      page_size, page_count, freelist_count, used_bytes, free_bytes, table_count, index_count, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const insertTable = database.prepare(`
    INSERT INTO table_storage_snapshots (
      id, database_role, table_name, sampled_at, row_count, table_bytes, index_bytes, total_bytes,
      page_count, index_count, growth_bytes_1h, growth_rows_1h, growth_bytes_24h, growth_rows_24h, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const businessDatabase = getBusinessDatabase()
  const datasetDatabase = getDatasetDatabase()
  const businessTables = [
    'accounts',
    'account_supported_models',
    'groups',
    'group_accounts',
    'api_keys',
    'api_key_group_bindings',
    'proxy_profiles',
    'error_policies',
    'stream_intercept_policies',
    'external_integration_sources',
    'external_integration_source_tokens',
    'resource_authorization_grants',
    'resource_authorizations',
    'resource_authorization_sources',
    'system_accounts',
    'system_teams',
    'system_team_members',
    'announcements',
    'announcement_reads'
  ] as const
  const datasetTables = [
    'usage_record_shards',
    'usage_record_shard_entries',
    'usage_record_account_shards',
    'usage_record_api_key_shards',
    'audit_logs',
    'audit_log_attempts',
    'audit_payload_refs',
    'audit_payload_blobs',
    'audit_error_groups',
    'operation_logs',
    'operation_log_targets',
    'operation_log_viewers',
    'operation_log_summary_search_terms',
    'runtime_logs',
    'runtime_log_event_facets',
    'runtime_log_level_facets',
    'runtime_log_facet_summary',
    'public_api_logs',
    'model_check_runs',
    'model_check_items',
    'account_record_cleanup_targets',
    'api_key_record_cleanup_targets'
  ] as const
  const statsTables = [
    'usage_stats_daily',
    'usage_stats_hourly',
    'usage_stats_monthly',
    'usage_model_rank_windows',
    'usage_error_rank_windows',
    'usage_overview_summary_windows',
    'usage_scope_range_windows',
    'authorization_team_usage_range_windows',
    'authorization_user_usage_range_windows',
    'ai_performance_summary_windows',
    'usage_quota_hourly_windows',
    'account_quality_scores',
    'account_quality_minute_stats',
    'client_ip_registry',
    'client_ip_stats_daily',
    'client_ip_usage_range_windows',
    'client_ip_policies',
    'client_ip_policy_hits',
    'system_metrics_samples',
    'system_metrics_hourly',
    'process_event_loop_samples',
    'process_event_loop_hourly'
  ] as const
  const databaseTargets = [
    { role: 'business', path: runtimeConfig.databasePath, baseBytes: 80_000_000, growthBytes: 1_200_000, tableCount: 20, indexCount: 38 },
    { role: 'dataset', path: datasetDatabasePath(), baseBytes: 220_000_000, growthBytes: 7_000_000, tableCount: 24, indexCount: 48 },
    { role: 'stats', path: statsDatabasePath(), baseBytes: 90_000_000, growthBytes: 1_500_000, tableCount: 36, indexCount: 62 }
  ] as const

  database.exec('BEGIN')
  try {
    for (let dayIndex = 0; dayIndex < options.days; dayIndex += 1) {
      const sampledAt = new Date(now - (options.days - dayIndex - 1) * dayMs).toISOString()
      for (const target of databaseTargets) {
        const fileBytes = target.baseBytes + dayIndex * target.growthBytes
        insertDatabase.run(
          `${idPrefix}storage_db_${target.role}_${String(dayIndex + 1).padStart(2, '0')}`,
          target.role,
          target.path,
          sampledAt,
          fileBytes,
          Math.floor(fileBytes * 0.08),
          32768,
          4096,
          Math.ceil(fileBytes / 4096),
          128 + dayIndex,
          Math.floor(fileBytes * 0.82),
          Math.floor(fileBytes * 0.18),
          target.tableCount,
          target.indexCount,
          sampledAt
        )
      }
      for (const tableName of businessTables) {
        const baseRows = tableRowCount(businessDatabase, tableName)
        const rows = baseRows + dayIndex * 2
        insertTable.run(...tableStorageValues('business', tableName, dayIndex, sampledAt, rows, 24_000 + rows * 900))
      }
      for (const tableName of datasetTables) {
        const baseRows = tableRowCount(datasetDatabase, tableName)
        const rows = baseRows + dayIndex * 12
        insertTable.run(...tableStorageValues('dataset', tableName, dayIndex, sampledAt, rows, 80_000 + rows * 1100))
      }
      for (const tableName of statsTables) {
        const baseRows = tableRowCount(database, tableName)
        const rows = baseRows + dayIndex * 4
        insertTable.run(...tableStorageValues('stats', tableName, dayIndex, sampledAt, rows, 60_000 + rows * 700))
      }
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function tableStorageValues(
  role: string,
  tableName: string,
  dayIndex: number,
  sampledAt: string,
  rowCount: number,
  totalBytes: number
): SqlValue[] {
  const tableBytes = Math.floor(totalBytes * 0.72)
  const indexBytes = totalBytes - tableBytes
  return [
    `${idPrefix}storage_table_${role}_${tableName}_${String(dayIndex + 1).padStart(2, '0')}`,
    role,
    tableName,
    sampledAt,
    rowCount,
    tableBytes,
    indexBytes,
    totalBytes,
    Math.ceil(totalBytes / 4096),
    3 + (dayIndex % 6),
    30_000 + dayIndex * 120,
    1 + (dayIndex % 8),
    500_000 + dayIndex * 10_000,
    10 + (dayIndex % 40),
    sampledAt
  ]
}

function tableRowCount(database: Database, tableName: string): number {
  const row = database.prepare(`SELECT COUNT(*) AS total FROM ${tableName}`).get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function rebuildDerivedCaches(statsDatabase: Database): DerivedCacheCounts {
  resetUsageStatsCache(statsDatabase)
  let totalProcessed = 0
  while (true) {
    const processed = aggregateUsageStatsBatch(5000)
    totalProcessed += processed
    if (processed <= 0) break
  }
  refreshUsageRankSnapshots()
  refreshUsageQuotaHourlyWindowsCache()
  refreshGroupAccountStatsCache()
  const quality = refreshAccountQualityFromUsage(24 * 60)
  let clientIpProcessed = 0
  while (true) {
    const processed = aggregateClientIpStatsBatch(10000)
    clientIpProcessed += processed
    if (processed <= 0) break
  }
  rebuildClientIpUsageRangeWindows()
  console.log(`统计缓存已重建：聚合 ${totalProcessed} 条，用量质量刷新 ${quality.refreshed} 个账号，IP 统计聚合 ${clientIpProcessed} 条`)
  return {
    usageRecords: totalProcessed,
    accountQualityAccounts: quality.refreshed,
    clientIpRecords: clientIpProcessed
  }
}

function resetUsageStatsCache(database: Database): void {
  const updatedAt = nowIso()
  const usageStatsTables = [
    'usage_stats_totals',
    'usage_stats_minute',
    'usage_stats_hourly',
    'usage_stats_daily',
    'usage_stats_weekly',
    'usage_stats_monthly',
    'usage_model_minute',
    'usage_model_hourly',
    'usage_model_daily',
    'usage_model_weekly',
    'usage_model_monthly',
    'usage_error_minute',
    'usage_error_hourly',
    'usage_error_daily',
    'usage_error_weekly',
    'usage_error_monthly',
    'usage_latency_minute',
    'usage_latency_hourly',
    'usage_latency_daily',
    'usage_latency_weekly',
    'usage_latency_monthly',
    'authorization_team_usage_summary_daily',
    'authorization_team_usage_range_windows',
    'authorization_user_usage_summary_daily',
    'authorization_user_usage_range_windows',
    'usage_rank_snapshots',
    'usage_overview_summary_windows',
    'usage_overview_trend_windows',
    'usage_model_rank_windows',
    'usage_error_rank_windows',
    'ai_performance_summary_windows',
    'usage_quota_hourly_windows',
    'usage_scope_range_windows',
    'system_metrics_trend_windows',
    'process_event_loop_trend_windows',
    'account_quality_minute_stats',
    'account_quality_scores',
    'client_ip_registry',
    'client_ip_stats_daily',
    'client_ip_usage_range_windows',
    'client_ip_range_window_dirty_ips'
  ]
  database.exec('BEGIN')
  try {
    for (const tableName of usageStatsTables) {
      database.prepare(`DELETE FROM ${tableName}`).run()
    }
    database.prepare(`
      DELETE FROM stats_job_state
      WHERE job_name IN ('usage_stats_aggregation', 'client_ip_stats_aggregation', 'client_ip_range_window_refresh')
    `).run()
    database.prepare(`
      INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
      VALUES ('global', '', 'usage_stats_aggregation', '', '', NULL, NULL, 0, ?)
    `).run(updatedAt)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function cleanupMockdata(businessDatabase: Database, datasetDatabase: Database, statsDatabase: Database, adminId: string): void {
  const mockUserIds = selectIds(businessDatabase, "SELECT id FROM system_accounts WHERE username LIKE 'mockdata_%'")
  const mockAccountIds = selectIds(businessDatabase, 'SELECT id FROM accounts WHERE name LIKE ?', `${namePrefix}%`)
  const mockApiKeyIds = selectIds(businessDatabase, 'SELECT id FROM api_keys WHERE name LIKE ?', `${namePrefix}%`)
  cleanupDatasetMockdata(datasetDatabase, mockAccountIds, mockApiKeyIds)
  cleanupStatsMockdata(statsDatabase, mockAccountIds)
  cleanupBusinessMockdata(businessDatabase, adminId, mockUserIds)
}

function cleanupBusinessMockdata(database: Database, adminId: string, mockUserIds: string[]): void {
  const likeName = `${namePrefix}%`
  database.exec('BEGIN')
  try {
    const mockAnnouncementIds = selectIds(database, 'SELECT id FROM announcements WHERE title LIKE ?', likeName)
    deleteWhereIn(database, 'announcement_reads', 'announcement_id', mockAnnouncementIds)
    deleteWhereIn(database, 'announcements', 'id', mockAnnouncementIds)

    const mockExternalSourceIds = selectIds(database, 'SELECT id FROM external_integration_sources WHERE name LIKE ?', likeName)
    deleteWhereIn(database, 'external_integration_source_tokens', 'source_ref_id', mockExternalSourceIds)
    deleteWhereIn(database, 'external_integration_sources', 'id', mockExternalSourceIds)

    const mockStreamInterceptPolicyIds = selectIds(database, 'SELECT id FROM stream_intercept_policies WHERE name LIKE ?', likeName)
    deleteWhereIn(database, 'stream_intercept_policies', 'id', mockStreamInterceptPolicyIds)

    const mockRuntimeAuthorizationIds = selectIds(database, 'SELECT id FROM resource_authorizations WHERE created_by = ? AND remark LIKE ?', adminId, likeName)
    deleteWhereIn(database, 'resource_authorization_sources', 'authorization_id', mockRuntimeAuthorizationIds)
    const mockGrantIds = selectIds(database, 'SELECT id FROM resource_authorization_grants WHERE created_by = ? AND remark LIKE ?', adminId, likeName)
    deleteWhereIn(database, 'resource_authorization_grants', 'id', mockGrantIds)

    const mockApiKeyIds = selectIds(database, 'SELECT id FROM api_keys WHERE name LIKE ?', likeName)
    deleteWhereIn(database, 'api_key_group_bindings', 'api_key_id', mockApiKeyIds)
    deleteWhereIn(database, 'api_keys', 'id', mockApiKeyIds)

    const mockGroupIds = selectIds(database, 'SELECT id FROM groups WHERE name LIKE ?', likeName)
    const mockAccountIds = selectIds(database, 'SELECT id FROM accounts WHERE name LIKE ?', likeName)
    deleteWhereIn(database, 'group_accounts', 'group_id', mockGroupIds)
    deleteWhereIn(database, 'group_accounts', 'account_id', mockAccountIds)
    deleteWhereIn(database, 'accounts', 'id', mockAccountIds)
    deleteWhereIn(database, 'groups', 'id', mockGroupIds)

    const userGroupIds = selectIdsForChunks(database, mockUserIds, 'SELECT id FROM groups WHERE system_account_id IN ({placeholders})')
    const userApiKeyIds = selectIdsForChunks(database, mockUserIds, 'SELECT id FROM api_keys WHERE system_account_id IN ({placeholders})')
    deleteWhereIn(database, 'group_accounts', 'group_id', userGroupIds)
    deleteWhereIn(database, 'api_key_group_bindings', 'group_id', userGroupIds)
    deleteWhereIn(database, 'api_key_group_bindings', 'api_key_id', userApiKeyIds)
    deleteWhereIn(database, 'api_keys', 'id', userApiKeyIds)
    deleteWhereIn(database, 'groups', 'id', userGroupIds)

    deleteWhereIn(database, 'resource_authorizations', 'id', mockRuntimeAuthorizationIds)

    const mockTeamIds = selectIds(database, 'SELECT id FROM system_teams WHERE name LIKE ?', likeName)
    deleteWhereIn(database, 'system_team_members', 'team_id', mockTeamIds)
    deleteWhereIn(database, 'system_teams', 'id', mockTeamIds)

    const mockProxyIds = selectIds(database, 'SELECT id FROM proxy_profiles WHERE name LIKE ?', likeName)
    const mockPolicyIds = selectIds(database, 'SELECT id FROM error_policies WHERE name LIKE ? OR id LIKE ?', likeName, `${idPrefix}%`)
    deleteWhereIn(database, 'proxy_profiles', 'id', mockProxyIds)
    deleteWhereIn(database, 'error_policies', 'id', mockPolicyIds)

    deleteWhereIn(database, 'system_sessions', 'system_account_id', mockUserIds)
    deleteWhereIn(database, 'system_accounts', 'id', mockUserIds)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function cleanupDatasetMockdata(database: Database, mockAccountIds: string[], mockApiKeyIds: string[]): void {
  database.exec('BEGIN')
  try {
    cleanupUsageRecordShardMockdata()

    deleteWhereIn(database, 'account_record_cleanup_targets', 'account_id', mockAccountIds)
    deleteWhereIn(database, 'api_key_record_cleanup_targets', 'api_key_id', mockApiKeyIds)

    database.prepare(`
      DELETE FROM public_api_logs
      WHERE id LIKE ?
         OR trace_id LIKE ?
         OR source_name LIKE ?
    `).run(`${idPrefix}%`, `${tracePrefix}%`, `${namePrefix}%`)

    database.prepare(`
      DELETE FROM account_record_cleanup_targets
      WHERE last_blocked_reason LIKE ?
         OR last_error_message LIKE ?
    `).run(`${namePrefix}%`, 'Mockdata%')
    database.prepare(`
      DELETE FROM api_key_record_cleanup_targets
      WHERE last_blocked_reason LIKE ?
         OR last_error_message LIKE ?
    `).run(`${namePrefix}%`, 'Mockdata%')

    database.prepare(`
      DELETE FROM audit_error_groups
      WHERE first_event_id LIKE ?
        OR last_event_id LIKE ?
        OR sample_event_id LIKE ?
        OR last_message LIKE ?
    `).run(`${idPrefix}%`, `${idPrefix}%`, `${idPrefix}%`, 'Mockdata%')

    const auditIds = selectIds(database, 'SELECT id FROM audit_logs WHERE id LIKE ? OR trace_id LIKE ?', `${idPrefix}%`, `${tracePrefix}%`)
    deleteWhereIn(database, 'audit_logs', 'id', auditIds)

    const operationIds = selectIds(database, 'SELECT id FROM operation_logs WHERE id LIKE ? OR trace_id LIKE ?', `${idPrefix}%`, `${tracePrefix}%`)
    deleteWhereIn(database, 'operation_logs', 'id', operationIds)

    const runtimeIds = selectIds(database, 'SELECT id FROM runtime_logs WHERE id LIKE ? OR trace_id LIKE ?', `${idPrefix}%`, `${tracePrefix}%`)
    deleteWhereIn(database, 'runtime_logs', 'id', runtimeIds)

    const modelCheckRunIds = selectIds(database, 'SELECT id FROM model_check_runs WHERE id LIKE ? OR trace_id LIKE ?', `${idPrefix}%`, `${tracePrefix}%`)
    deleteWhereIn(database, 'model_check_items', 'run_id', modelCheckRunIds)
    deleteWhereIn(database, 'model_check_runs', 'id', modelCheckRunIds)
    database.prepare('DELETE FROM model_check_items WHERE id LIKE ? OR trace_id LIKE ?').run(`${idPrefix}%`, `${tracePrefix}%`)

    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  repositories.cleanupUnreferencedAuditPayloadBlobs(10000)
}

function cleanupStatsMockdata(database: Database, mockAccountIds: string[]): void {
  database.exec('BEGIN')
  try {
    const mockClientIpPolicyIds = selectIds(database, 'SELECT id FROM client_ip_policies WHERE id LIKE ? OR reason LIKE ? OR disabled_reason LIKE ?', `${idPrefix}%`, `${namePrefix}%`, `${namePrefix}%`)
    deleteWhereIn(database, 'client_ip_policy_hits', 'policy_id', mockClientIpPolicyIds)
    deleteWhereIn(database, 'client_ip_policies', 'id', mockClientIpPolicyIds)
    deleteWhereIn(database, 'account_usage_snapshots', 'account_id', mockAccountIds)
    database.prepare('DELETE FROM system_metrics_samples WHERE id LIKE ?').run(`${idPrefix}%`)
    database.prepare('DELETE FROM process_event_loop_samples WHERE id LIKE ?').run(`${idPrefix}%`)
    database.prepare('DELETE FROM database_storage_snapshots WHERE id LIKE ?').run(`${idPrefix}%`)
    database.prepare('DELETE FROM table_storage_snapshots WHERE id LIKE ?').run(`${idPrefix}%`)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function cleanupUsageRecordShardMockdata(): void {
  const mockUsageIds = selectIds(
    getDatasetDatabase(),
    'SELECT usage_id AS id FROM usage_record_shard_entries WHERE usage_id LIKE ?',
    `${idPrefix}%`
  )
  for (const location of listUsageRecordShardLocations()) {
    getUsageRecordShardDatabase(location)
      .prepare("DELETE FROM usage_records WHERE id LIKE ? OR trace_id LIKE ?")
      .run(`${idPrefix}%`, `${tracePrefix}%`)
  }
  deleteUsageRecordShardEntries(mockUsageIds)
}

function rebuildSystemMetricsHourly(database: Database): void {
  const timezone = usageStatsTimezone()
  const rows = database.prepare('SELECT * FROM system_metrics_samples ORDER BY sampled_at ASC, id ASC').all() as unknown as Array<Record<string, unknown>>
  const buckets = new Map<string, AccountMetricRow>()
  for (const row of rows) {
    const statHour = hourKey(new Date(String(row.sampled_at)), timezone)
    const bucket = buckets.get(statHour) ?? emptyMetricRow()
    bucket.sample_count += 1
    addMetric(bucket, 'cpu_percent', row.cpu_percent)
    addMetric(bucket, 'memory_used_percent', row.memory_used_percent)
    addMetric(bucket, 'process_rss_bytes', row.process_rss_bytes)
    addMetric(bucket, 'process_heap_used_bytes', row.process_heap_used_bytes)
    addMetric(bucket, 'event_loop_lag_ms', row.event_loop_lag_ms)
    addMetric(bucket, 'network_rx_bytes_per_sec', row.network_rx_bytes_per_sec, true)
    addMetric(bucket, 'network_tx_bytes_per_sec', row.network_tx_bytes_per_sec, true)
    maxMetric(bucket, 'network_rx_total_bytes', row.network_rx_total_bytes)
    maxMetric(bucket, 'network_tx_total_bytes', row.network_tx_total_bytes)
    maxMetric(bucket, 'db_file_bytes', row.db_file_bytes)
    maxMetric(bucket, 'stats_lag_seconds', row.stats_lag_seconds)
    buckets.set(statHour, bucket)
  }
  database.exec('BEGIN')
  try {
    database.prepare('DELETE FROM system_metrics_hourly').run()
    const insert = database.prepare(`
      INSERT INTO system_metrics_hourly (
        stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
        memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
        process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_max,
        network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
        network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
        network_rx_total_bytes_max, network_tx_total_bytes_max, db_file_bytes_max, stats_lag_seconds_max, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    const updatedAt = nowIso()
    for (const [statHour, bucket] of buckets) {
      insert.run(
        statHour,
        bucket.sample_count,
        bucket.cpu_percent_sum,
        bucket.cpu_percent_max,
        bucket.memory_used_percent_sum,
        bucket.memory_used_percent_max,
        bucket.process_rss_bytes_sum,
        bucket.process_rss_bytes_max,
        bucket.process_heap_used_bytes_sum,
        bucket.process_heap_used_bytes_max,
        bucket.event_loop_lag_ms_sum,
        bucket.event_loop_lag_ms_max,
        bucket.network_rx_bytes_per_sec_sum,
        bucket.network_rx_bytes_per_sec_max,
        bucket.network_rx_bytes_per_sec_count,
        bucket.network_tx_bytes_per_sec_sum,
        bucket.network_tx_bytes_per_sec_max,
        bucket.network_tx_bytes_per_sec_count,
        bucket.network_rx_total_bytes_max,
        bucket.network_tx_total_bytes_max,
        bucket.db_file_bytes_max,
        bucket.stats_lag_seconds_max,
        updatedAt
      )
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function rebuildProcessEventLoopHourly(database: Database): void {
  const timezone = usageStatsTimezone()
  const rows = database.prepare('SELECT * FROM process_event_loop_samples ORDER BY sampled_at ASC, id ASC').all() as unknown as Array<Record<string, unknown>>
  const buckets = new Map<string, ProcessMetricRow>()
  for (const row of rows) {
    const statHour = hourKey(new Date(String(row.sampled_at)), timezone)
    const processRole = String(row.process_role ?? '')
    if (!processRole) continue
    const key = `${statHour}:${processRole}`
    const bucket = buckets.get(key) ?? { sample_count: 0, event_loop_lag_ms_sum: 0, event_loop_lag_ms_max: null }
    const lag = numeric(row.event_loop_lag_ms)
    if (lag !== undefined) {
      bucket.sample_count += 1
      bucket.event_loop_lag_ms_sum += lag
      bucket.event_loop_lag_ms_max = bucket.event_loop_lag_ms_max === null ? lag : Math.max(bucket.event_loop_lag_ms_max, lag)
    }
    buckets.set(key, bucket)
  }
  database.exec('BEGIN')
  try {
    database.prepare('DELETE FROM process_event_loop_hourly').run()
    const insert = database.prepare(`
      INSERT INTO process_event_loop_hourly (
        stat_hour, process_role, sample_count, event_loop_lag_ms_sum, event_loop_lag_ms_max, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?)
    `)
    const updatedAt = nowIso()
    for (const [key, bucket] of buckets) {
      const [statHour, processRole] = key.split(':')
      insert.run(statHour, processRole, bucket.sample_count, bucket.event_loop_lag_ms_sum, bucket.event_loop_lag_ms_max, updatedAt)
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function emptyMetricRow(): AccountMetricRow {
  return {
    sample_count: 0,
    cpu_percent_sum: 0,
    cpu_percent_max: null,
    memory_used_percent_sum: 0,
    memory_used_percent_max: null,
    process_rss_bytes_sum: 0,
    process_rss_bytes_max: null,
    process_heap_used_bytes_sum: 0,
    process_heap_used_bytes_max: null,
    event_loop_lag_ms_sum: 0,
    event_loop_lag_ms_max: null,
    network_rx_bytes_per_sec_sum: 0,
    network_rx_bytes_per_sec_max: null,
    network_rx_bytes_per_sec_count: 0,
    network_tx_bytes_per_sec_sum: 0,
    network_tx_bytes_per_sec_max: null,
    network_tx_bytes_per_sec_count: 0,
    network_rx_total_bytes_max: null,
    network_tx_total_bytes_max: null,
    db_file_bytes_max: null,
    stats_lag_seconds_max: null
  }
}

function addMetric(row: AccountMetricRow, key: string, value: unknown, counted = false): void {
  const number = numeric(value)
  if (number === undefined) return
  const sumKey = `${key}_sum` as keyof AccountMetricRow
  const maxKey = `${key}_max` as keyof AccountMetricRow
  row[sumKey] = Number(row[sumKey] ?? 0) + number as never
  row[maxKey] = row[maxKey] === null ? number as never : Math.max(Number(row[maxKey]), number) as never
  if (counted) {
    const countKey = `${key}_count` as keyof AccountMetricRow
    row[countKey] = Number(row[countKey] ?? 0) + 1 as never
  }
}

function maxMetric(row: AccountMetricRow, key: string, value: unknown): void {
  const number = numeric(value)
  if (number === undefined) return
  const maxKey = `${key}_max` as keyof AccountMetricRow
  row[maxKey] = row[maxKey] === null ? number as never : Math.max(Number(row[maxKey]), number) as never
}

function updateApiKeyLastUsedAt(records: UsageRecordSeed[]): void {
  const lastUsedByKey = new Map<string, string>()
  for (const record of records) {
    if (!record.apiKeyId) continue
    const previous = lastUsedByKey.get(record.apiKeyId)
    if (!previous || record.createdAt > previous) {
      lastUsedByKey.set(record.apiKeyId, record.createdAt)
    }
  }
  const statement = getBusinessDatabase().prepare('UPDATE api_keys SET last_used_at = ?, updated_at = ? WHERE id = ?')
  for (const [apiKeyId, lastUsedAt] of lastUsedByKey) {
    statement.run(lastUsedAt, lastUsedAt, apiKeyId)
  }
}

function mockUserSummaries(users: MockSystemAccounts): Array<Record<string, unknown>> {
  return Object.entries(users)
    .filter(([name]) => name !== 'admin')
    .map(([name, user]) => ({
      name,
      id: user.id,
      username: user.username,
      displayName: user.displayName,
      role: user.role,
      status: user.status,
      password: mockPassword
    }))
}

function mockGroupById(groups: MockGroups): Map<string, GroupSummary> {
  return new Map<string, GroupSummary>(Object.values(groups).map((group) => [group.id, group]))
}

function mockGroupOwnerById(groups: MockGroups, users: MockSystemAccounts): Map<string, SystemAccountSummary> {
  return new Map<string, SystemAccountSummary>([
    [groups.main.id, users.admin],
    [groups.highConcurrency.id, users.admin],
    [groups.backup.id, users.admin],
    [groups.oauth.id, users.admin],
    [groups.experiment.id, users.admin],
    [groups.empty.id, users.admin],
    [groups.devDefault.id, users.dev],
    [groups.opsDefault.id, users.ops],
    [groups.testerDefault.id, users.tester],
    [groups.financeDefault.id, users.finance],
    [groups.viewerDefault.id, users.viewer]
  ])
}

function apiKeySummariesForMockdata(
  apiKeys: MockApiKeys,
  groupById: Map<string, GroupSummary>,
  groupOwnerById: Map<string, SystemAccountSummary>
): Array<Record<string, unknown>> {
  return (Object.entries(apiKeys) as Array<[string, ApiKeyWithSecret]>).map(([name, key]) => {
    const firstOwner = key.groupBindings
      .map((binding) => groupOwnerById.get(binding.groupId))
      .find((owner): owner is SystemAccountSummary => Boolean(owner))
    return {
      name,
      id: key.id,
      label: key.name,
      description: key.description,
      ownerSystemAccountId: key.systemAccountId ?? firstOwner?.id,
      ownerSystemAccountName: key.systemAccountName ?? firstOwner?.displayName,
      bindingScope: 'owner_local_group',
      bindingRule: apiKeyAuthorizedGroupBindingRule,
      groupBindings: key.groupBindings.map((binding) => {
        const group = groupById.get(binding.groupId)
        const owner = groupOwnerById.get(binding.groupId)
        return {
          groupId: binding.groupId,
          groupName: binding.groupName ?? group?.name,
          groupOwnerSystemAccountId: owner?.id ?? group?.systemAccountId,
          groupOwnerSystemAccountName: owner?.displayName ?? group?.systemAccountName,
          accessType: 'owner',
          bindableToApiKey: true,
          priority: binding.priority,
          weight: binding.weight,
          status: binding.status
        }
      }),
      status: key.status,
      key: key.key
    }
  })
}

function groupAuthorizationSamples(authorizations: ResourceAuthorizationSummary[]): Array<Record<string, unknown>> {
  return authorizations
    .filter((authorization) => authorization.resourceType === 'group')
    .map((authorization) => ({
      id: authorization.id,
      resourceType: authorization.resourceType,
      resourceId: authorization.resourceId,
      resourceName: authorization.resourceName,
      resourceOwnerSystemAccountId: authorization.resourceOwnerSystemAccountId,
      resourceOwnerSystemAccountName: authorization.resourceOwnerSystemAccountName,
      granteeType: authorization.granteeType,
      granteeSystemAccountId: authorization.granteeSystemAccountId,
      granteeSystemAccountName: authorization.granteeSystemAccountName,
      granteeUsername: authorization.granteeUsername,
      granteeTeamId: authorization.granteeTeamId,
      granteeTeamName: authorization.granteeTeamName,
      status: authorization.status,
      remark: authorization.remark,
      expiresAt: authorization.expiresAt,
      bindableToApiKey: false,
      bindingRule: apiKeyAuthorizedGroupBindingRule
    }))
}

function writeSummary(
  created: CreatedMockdata,
  records: UsageRecordSeed[],
  modelCheckCounts: ModelCheckMockdataCounts,
  extraCounts: ExtraMockdataCounts,
  options: MockdataOptions,
  durationMs: number
): void {
  const groupById = mockGroupById(created.groups)
  const groupOwnerById = mockGroupOwnerById(created.groups, created.users)
  const summary = {
    generatedAt: nowIso(),
    durationMs,
    options,
    owner: {
      id: created.users.admin.id,
      username: created.users.admin.username,
      displayName: created.users.admin.displayName
    },
    mockUserPassword: mockPassword,
    mockUsers: mockUserSummaries(created.users),
    apiKeyBindingRule: apiKeyAuthorizedGroupBindingRule,
    authorizedUsageRecordNote: 'usage_records 中的 group_authorized 样本用于授权用量统计，不表示 API Key 可直接绑定授权方分组。',
    apiKeys: apiKeySummariesForMockdata(created.apiKeys, groupById, groupOwnerById),
    authorizationSamples: groupAuthorizationSamples(created.authorizations),
    counts: {
      users: Object.keys(created.users).length - 1,
      groups: Object.keys(created.groups).length,
      accounts: Object.keys(created.accounts).length,
      apiKeys: Object.keys(created.apiKeys).length,
      teams: Object.keys(created.teams).length,
      authorizations: created.authorizations.length,
      externalSources: Object.keys(created.externalSources).length,
      streamInterceptPolicies: created.streamInterceptPolicies,
      usageRecords: records.length,
      publicApiLogs: extraCounts.publicApiLogs,
      auditLogs: Math.ceil(records.length / 4),
      operationLogs: 90,
      runtimeLogs: Math.min(240, records.length),
      modelCheckRuns: modelCheckCounts.runs,
      modelCheckItems: modelCheckCounts.items,
      accountCleanupTargets: extraCounts.accountCleanupTargets,
      apiKeyCleanupTargets: extraCounts.apiKeyCleanupTargets,
      clientIpAggregatedRecords: extraCounts.clientIpAggregatedRecords,
      clientIpPolicies: extraCounts.clientIpPolicies,
      clientIpPolicyHits: extraCounts.clientIpPolicyHits
    }
  }
  const path = mockdataSummaryPath()
  mkdirSync(dirname(path), { recursive: true })
  writeFileSync(path, `${JSON.stringify(summary, null, 2)}\n`, 'utf8')
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

function defaultOpenAIGroup(systemAccountId: string): GroupSummary {
  const row = getBusinessDatabase()
    .prepare("SELECT id FROM groups WHERE system_account_id = ? AND provider_code = 'openai' AND is_default = 1 LIMIT 1")
    .get(systemAccountId) as unknown as { id?: string } | undefined
  if (!row?.id) throw new Error(`未找到默认 OpenAI 分组：${systemAccountId}`)
  const group = repositories.findGroupSummary(row.id, { systemAccountId, role: 'user' })
  if (!group) throw new Error(`默认 OpenAI 分组不可读：${systemAccountId}`)
  return group
}

function refreshAccount(id: string): AccountSummary {
  const account = repositories.findAccountSummary(id, { systemAccountId: 'sys_admin', role: 'admin' })
  if (!account) throw new Error(`读取 Mockdata 账户失败：${id}`)
  return account
}

function authorizationInstanceAccount(sourceAccount: AccountSummary, grantee: SystemAccountSummary): AccountSummary {
  const row = getBusinessDatabase()
    .prepare(`
      SELECT id
      FROM accounts
      WHERE authorization_instance_source_account_id = ?
        AND system_account_id = ?
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
    .get(sourceAccount.id, grantee.id) as unknown as { id?: string } | undefined
  if (!row?.id) {
    throw new Error(`未找到 Mockdata 授权账户实例：${sourceAccount.name} -> ${grantee.displayName || grantee.username}`)
  }
  return refreshAccount(row.id)
}

function refreshTeam(id: string, access: AccessScope): SystemTeamSummary {
  const team = repositories.findSystemTeamSummary(id, access)
  if (!team) throw new Error(`读取 Mockdata 团队失败：${id}`)
  return team
}

function refreshAuthorization(id: string, access: AccessScope): ResourceAuthorizationSummary {
  const authorization = repositories.findResourceAuthorization(id, access)
  if (!authorization) throw new Error(`读取 Mockdata 授权失败：${id}`)
  return authorization
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
  const adminRuntime = database.prepare(`
    SELECT id
    FROM resource_authorizations
    WHERE created_by = ?
      AND remark LIKE ?
      AND grantee_system_account_id = ?
    LIMIT 1
  `).get(adminId, likeName, adminId) as unknown as { id?: string } | undefined
  if (adminRuntime?.id) {
    throw new Error(`Mockdata 不应把超级管理员作为被授权人：${adminRuntime.id}`)
  }
}

function selectIds(database: Database, sql: string, ...params: SqlValue[]): string[] {
  return (database.prepare(sql).all(...params) as unknown as Array<{ id?: string }>)
    .map((row) => row.id)
    .filter((id): id is string => Boolean(id))
}

function selectIdsForChunks(database: Database, ids: string[], sqlTemplate: string): string[] {
  const output = new Set<string>()
  for (const chunk of chunks(ids, 800)) {
    if (!chunk.length) continue
    const placeholders = chunk.map(() => '?').join(',')
    for (const id of selectIds(database, sqlTemplate.replace('{placeholders}', placeholders), ...chunk)) {
      output.add(id)
    }
  }
  return [...output]
}

function deleteWhereIn(database: Database, tableName: string, columnName: string, ids: string[]): void {
  for (const chunk of chunks([...new Set(ids.filter(Boolean))], 800)) {
    if (!chunk.length) continue
    const placeholders = chunk.map(() => '?').join(',')
    database.prepare(`DELETE FROM ${tableName} WHERE ${columnName} IN (${placeholders})`).run(...chunk)
  }
}

function chunks<T>(items: T[], size: number): T[][] {
  const output: T[][] = []
  for (let index = 0; index < items.length; index += size) {
    output.push(items.slice(index, index + size))
  }
  return output
}

function weightedIndex(seed: number, length: number): number {
  if (length <= 1) return 0
  return Math.floor(pseudoRandom(seed, 1) * length) % length
}

function pseudoRandom(seed: number, salt: number): number {
  const value = Math.sin(seed * 12.9898 + salt * 78.233) * 43758.5453
  return value - Math.floor(value)
}

function boundedInteger(value: string | undefined, fallback: number, min: number, max: number): number {
  const number = Number(value)
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(Math.trunc(number), min), max)
}

function roundCost(value: number): number {
  return Math.round(value * 1_000_000) / 1_000_000
}

function roundNumber(value: number, digits: number): number {
  const factor = 10 ** digits
  return Math.round(value * factor) / factor
}

function numeric(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}

function mockdataSummaryPath(): string {
  return join(dirname(runtimeConfig.databasePath), 'mockdata-summary.json')
}

main()
