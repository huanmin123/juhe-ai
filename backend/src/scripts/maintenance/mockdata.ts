import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

import type {
  AccountSummary,
  GroupSummary,
  ResourceAuthorizationSummary,
  SystemAccountRole,
  SystemAccountSummary,
  SystemTeamSummary
} from '../../domain/types.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_VENDOR_CODE, OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { refreshAccountQualityFromUsage } from '../../storage/account-quality.repository.js'
import { datasetDatabasePath, getBusinessDatabase, getDatasetDatabase, getStatsDatabase, nowIso, statsDatabasePath } from '../../storage/database.js'
import * as repositories from '../../storage/repositories.js'
import { createResponseInspectionPolicy } from '../../storage/response-inspection-policy.repository.js'
import {
  aggregateClientIpStatsBatch,
  rebuildClientIpUsageRangeWindows,
  recordClientIpPolicyHits
} from '../../storage/client-ip-stats.repository.js'
import {
  createExternalIntegrationSourceAuthorization,
  createExternalIntegrationSourceToken,
  externalIntegrationScopeOptions
} from '../../storage/external-integration-source.repository.js'
import {
  aggregateUsageStatsBatch,
  refreshGroupAccountStatsCache,
  refreshUsageQuotaHourlyWindowsCache,
  refreshUsageRankSnapshots
} from '../../storage/usage-stats.repository.js'
import {
  adminUsername,
  apiKeyAuthorizedGroupBindingRule,
  boundedInteger,
  buildModelCheckItems,
  dayMs,
  defaultDailyRequests,
  defaultDays,
  idPrefix,
  minuteMs,
  mockPassword,
  modelCheckLevelForRun,
  modelCheckRunMessage,
  modelCheckRunStatusForIndex,
  namePrefix,
  providerCode,
  tracePrefix,
  type ApiKeyWithSecret,
  type ClientIpPolicyMockdataCounts,
  type CreatedMockdata,
  type DerivedCacheCounts,
  type ExtraMockdataCounts,
  type MockAccounts,
  type MockApiKeys,
  type MockdataOptions,
  type MockExternalSources,
  type MockGroups,
  type MockSystemAccounts,
  type MockTeams,
  type ModelCheckMockdataCounts,
  type ModelCheckTargetSeed,
  type RecordCleanupMockdataCounts,
  type UsageRecordSeed
} from './mockdata-shared.js'
import { authorizationInstanceAccount, refreshAccount } from './mockdata-account-helpers.js'
import { cleanupMockdata } from './mockdata-cleanup.js'
import {
  createAuditMockdata,
  createOperationMockdata,
  createPublicApiLogMockdata,
  createRuntimeLogMockdata
} from './mockdata-logs.js'
import { createMonitoringMockdata } from './mockdata-monitoring.js'
import { createStorageMockdata } from './mockdata-storage.js'
import { createUsageMockdata } from './mockdata-usage.js'

type Database = ReturnType<typeof getBusinessDatabase>

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
  const unscopedAdminAccess: AccessScope = { systemAccountId: admin.id, role: adminAccess.role }
  const users = createMockUsers(admin)
  const proxies = createProxies(adminAccess)
  const groups = createGroups(adminAccess, users)
  const accounts = createAccounts(adminAccess, groups, users, proxies)
  const teams = createTeams(adminAccess, users)
  const authorizations = createAuthorizations(adminAccess, unscopedAdminAccess, groups, accounts, users, teams)
  assertNoMockSelfAuthorizations(admin.id)
  bindAuthorizedAccountToUserGroup(authorizationInstanceAccount(accounts.proxied, users.ops), groups.opsDefault, users.ops)
  const apiKeys = createApiKeys(adminAccess, groups, users)
  const externalSources = createExternalSources()
  const responseInspectionPolicies = createResponseInspectionPolicies()
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
    responseInspectionPolicies
  }
}

function createMockUsers(admin: SystemAccountSummary): MockSystemAccounts {
  return {
    admin,
    manager: ensureSystemAccount({
      username: 'mockdata_admin',
      displayName: `${namePrefix}管理员用户`,
      description: 'Mockdata 普通管理员账号，用于管理员模式下验证管理员自有资源、筛选和创建目标',
      role: 'admin',
      status: 'active'
    }),
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
  role?: SystemAccountRole
  status: 'active' | 'disabled'
}): SystemAccountSummary {
  const role = input.role ?? 'user'
  const existing = repositories.findSystemAccountByUsername(input.username)
  if (existing) {
    const updated = repositories.updateSystemAccount(existing.id, {
      displayName: input.displayName,
      description: input.description,
      role,
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
    role,
    status: input.status,
    mustChangePassword: false
  })
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

function mockUserAccess(user: SystemAccountSummary): AccessScope {
  return { systemAccountId: user.id, role: user.role }
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
    providerCode,
    enabled: false
  }, adminAccess)
  const managerMain = repositories.createGroup({
    name: `${namePrefix}管理员自有分组`,
    description: '普通管理员自有分组，用于管理员模式按管理员角色筛选和创建目标验收',
    providerCode,
    enabled: true
  }, mockUserAccess(users.manager))
  const managerHighConcurrency = repositories.createGroup({
    name: `${namePrefix}管理员高并发分组`,
    description: '普通管理员自有高并发分组，用于管理员角色下的 AI 分组管理验收',
    providerCode,
    enabled: true,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 10,
      maxQueueWaitMs: 40_000,
      clientIpConcurrencyLimit: 6,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: 4
    }
  }, mockUserAccess(users.manager))
  const adminGrantedDev = repositories.createGroup({
    name: `${namePrefix}研发授权给超级管理员分组`,
    description: '研发用户自有分组，主动授权给超级管理员，用于 AI 分组管理查看授权分组',
    providerCode,
    enabled: true
  }, mockUserAccess(users.dev))
  const adminGrantedOps = repositories.createGroup({
    name: `${namePrefix}运维授权给超级管理员高并发分组`,
    description: '运维用户自有高并发分组，授权给超级管理员后暂停，用于授权状态展示',
    providerCode,
    enabled: true,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 6,
      maxQueueWaitMs: 30_000,
      clientIpConcurrencyLimit: 4,
      clientIpConcurrencyOverflowMode: 'reject',
      imageLaneMaxConcurrency: 2
    }
  }, mockUserAccess(users.ops))
  const adminGrantedTester = repositories.createGroup({
    name: `${namePrefix}测试授权给超级管理员过期分组`,
    description: '测试用户自有分组，授权给超级管理员后过期，用于 AI 分组管理过期状态展示',
    providerCode,
    enabled: true
  }, mockUserAccess(users.tester))

  return {
    main,
    highConcurrency,
    backup,
    oauth,
    experiment,
    empty,
    managerMain,
    managerHighConcurrency,
    adminGrantedDev,
    adminGrantedOps,
    adminGrantedTester,
    managerDefault: defaultGptGroup(users.manager.id),
    devDefault: defaultGptGroup(users.dev.id),
    opsDefault: defaultGptGroup(users.ops.id),
    testerDefault: defaultGptGroup(users.tester.id),
    financeDefault: defaultGptGroup(users.finance.id),
    viewerDefault: defaultGptGroup(users.viewer.id)
  }
}

function createAccounts(
  adminAccess: AccessScope,
  groups: MockGroups,
  users: MockSystemAccounts,
  proxies: { http: string; socks: string }
): MockAccounts {
  const primary = repositories.createAccount({
    providerCode,
    name: `${namePrefix}主力 API Key 账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.main.id,
    credentials: apiKeyCredentials('primary'),
    proxyProfileId: proxies.http,
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
    status: 'active',
    groupId: groups.main.id,
    credentials: apiKeyCredentials('proxied'),
    proxyProfileId: proxies.http,
    supportedModels: ['gpt-5.4', 'gpt-5.4-mini', 'gpt-4o-mini'],
    concurrencyLimit: 45,
    priority: 10,
    notes: 'Mockdata 代理账号，用于账户授权给用户'
  }, adminAccess)

  const normal = repositories.createAccount({
    providerCode,
    name: `${namePrefix}普通 API Key 账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.main.id,
    credentials: apiKeyCredentials('normal'),
    supportedModels: ['gpt-5.4-mini', 'gpt-4.1-mini', 'gpt-4o-mini'],
    concurrencyLimit: 35,
    priority: 30,
    notes: 'Mockdata 普通账号'
  }, adminAccess)

  const burstFast = repositories.createAccount({
    providerCode,
    name: `${namePrefix}高并发快响账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.highConcurrency.id,
    credentials: apiKeyCredentials('burst-fast'),
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
    status: 'active',
    groupId: groups.highConcurrency.id,
    credentials: apiKeyCredentials('burst-image'),
    supportedModels: ['gpt-5.4', 'gpt-5.4-mini', 'gpt-4.1-mini'],
    concurrencyLimit: 120,
    priority: 12,
    notes: 'Mockdata 高并发分组图像 / 长请求账号，用于图像 lane 并发展示'
  }, adminAccess)

  const burstFallback = repositories.createAccount({
    providerCode,
    name: `${namePrefix}高并发备用账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.highConcurrency.id,
    credentials: apiKeyCredentials('burst-fallback'),
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
    status: 'active',
    groupId: groups.backup.id,
    credentials: apiKeyCredentials('fallback'),
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
    status: 'active',
    groupId: groups.oauth.id,
    credentials: oauthCredentials('oauth-main', 2),
    proxyProfileId: proxies.socks,
    supportedModels: ['gpt-5.5', 'gpt-5.4'],
    concurrencyLimit: 50,
    priority: 5,
    notes: 'Mockdata OAuth 主力账号，带 Codex 额度快照'
  }, adminAccess)

  const oauthBackup = repositories.createAccount({
    providerCode,
    name: `${namePrefix}OAuth 备用账户`,
    type: 'oauth',
    status: 'active',
    groupId: groups.oauth.id,
    credentials: oauthCredentials('oauth-backup', 6),
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
    status: 'active',
    groupId: groups.backup.id,
    credentials: apiKeyCredentials('rate-limited'),
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
    status: 'active',
    groupId: groups.experiment.id,
    credentials: apiKeyCredentials('temporary'),
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
    status: 'active',
    groupId: groups.experiment.id,
    credentials: apiKeyCredentials('error'),
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
    status: 'active',
    groupId: groups.experiment.id,
    credentials: apiKeyCredentials('expired'),
    supportedModels: ['gpt-4.1-mini'],
    concurrencyLimit: 5,
    accountExpiresAt: new Date(Date.now() - dayMs).toISOString(),
    notes: 'Mockdata 已到期停用账号'
  }, adminAccess)

  const managerPrimary = repositories.createAccount({
    providerCode,
    name: `${namePrefix}管理员 API Key 账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.managerMain.id,
    credentials: apiKeyCredentials('manager-primary'),
    supportedModels: ['gpt-5.4', 'gpt-5.4-mini', 'gpt-4.1-mini'],
    concurrencyLimit: 48,
    priority: 8,
    notes: 'Mockdata 普通管理员自有账号，用于管理员模式资源归属验收'
  }, mockUserAccess(users.manager))

  const managerBurst = repositories.createAccount({
    providerCode,
    name: `${namePrefix}管理员高并发账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.managerHighConcurrency.id,
    credentials: apiKeyCredentials('manager-burst'),
    supportedModels: ['gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini'],
    concurrencyLimit: 120,
    priority: 4,
    superPriorityEnabled: true,
    notes: 'Mockdata 普通管理员高并发账号，用于管理员角色高并发分组验收'
  }, mockUserAccess(users.manager))

  const devShared = repositories.createAccount({
    providerCode,
    name: `${namePrefix}研发共享给超级管理员账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.adminGrantedDev.id,
    credentials: apiKeyCredentials('dev-admin-grant'),
    supportedModels: ['gpt-5.4-mini', 'gpt-4.1-mini'],
    concurrencyLimit: 24,
    priority: 20,
    notes: 'Mockdata 研发用户自有账号，用于授权分组给超级管理员'
  }, mockUserAccess(users.dev))

  const opsShared = repositories.createAccount({
    providerCode,
    name: `${namePrefix}运维共享给超级管理员账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.adminGrantedOps.id,
    credentials: apiKeyCredentials('ops-admin-grant'),
    supportedModels: ['gpt-5.4', 'gpt-5.4-mini'],
    concurrencyLimit: 40,
    priority: 15,
    notes: 'Mockdata 运维用户自有账号，用于暂停授权分组展示'
  }, mockUserAccess(users.ops))

  const testerShared = repositories.createAccount({
    providerCode,
    name: `${namePrefix}测试共享给超级管理员账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.adminGrantedTester.id,
    credentials: apiKeyCredentials('tester-admin-grant'),
    supportedModels: ['gpt-4.1-mini', 'gpt-4o-mini'],
    concurrencyLimit: 12,
    priority: 40,
    notes: 'Mockdata 测试用户自有账号，用于过期授权分组展示'
  }, mockUserAccess(users.tester))

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
    expired: refreshAccount(expired.id),
    managerPrimary,
    managerBurst,
    devShared,
    opsShared,
    testerShared
  }
}

function apiKeyCredentials(suffix: string): Record<string, unknown> {
  return {
    api_key: `sk-mockdata-admin-${suffix}-${'x'.repeat(24)}`,
    base_url: 'https://api.openai.com/v1'
  }
}

function oauthCredentials(suffix: string, expiresHours: number): Record<string, unknown> {
  return {
    access_token: `mockdata-oauth-access-${suffix}-${'a'.repeat(32)}`,
    refresh_token: `mockdata-oauth-refresh-${suffix}-${'r'.repeat(32)}`,
    client_id: 'mockdata-openai-oauth-client',
    account_id: `mockdata-openai-user-${suffix}`,
    expires_at: new Date(Date.now() + expiresHours * 60 * 60_000).toISOString(),
    base_url: 'https://api.openai.com/v1'
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
  unscopedAdminAccess: AccessScope,
  groups: MockGroups,
  accounts: MockAccounts,
  users: MockSystemAccounts,
  teams: MockTeams
): ResourceAuthorizationSummary[] {
  const result: ResourceAuthorizationSummary[] = []
  result.push(repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.adminGrantedDev.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    remark: `${namePrefix}研发分组授权给超级管理员`,
    limits: quotaLimits(12, 96, 360)
  }, unscopedAdminAccess))

  const adminPausedGroup = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.adminGrantedOps.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    remark: `${namePrefix}运维分组授权给超级管理员后暂停`,
    limits: quotaLimits(9, 72, 260)
  }, unscopedAdminAccess)
  repositories.updateResourceAuthorization(adminPausedGroup.id, { status: 'paused' }, unscopedAdminAccess)
  result.push(refreshAuthorization(adminPausedGroup.id, unscopedAdminAccess))

  const adminExpiredGroup = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groups.adminGrantedTester.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    remark: `${namePrefix}测试分组授权给超级管理员后过期`,
    expiresAt: new Date(Date.now() + 3 * dayMs).toISOString(),
    limits: quotaLimits(5, 30, 100)
  }, unscopedAdminAccess)
  repositories.updateResourceAuthorization(adminExpiredGroup.id, {
    status: 'expired',
    expiresAt: new Date(Date.now() - 3 * dayMs).toISOString()
  }, unscopedAdminAccess)
  result.push(refreshAuthorization(adminExpiredGroup.id, unscopedAdminAccess))

  result.push(repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.devShared.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    targetGroupId: defaultGptGroup(users.admin.id).id,
    remark: `${namePrefix}研发账户授权给超级管理员`,
    limits: quotaLimits(11, 88, 320)
  }, unscopedAdminAccess))

  const adminPausedAccount = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.opsShared.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    targetGroupId: defaultGptGroup(users.admin.id).id,
    remark: `${namePrefix}运维账户授权给超级管理员后暂停`,
    limits: quotaLimits(7, 56, 210)
  }, unscopedAdminAccess)
  repositories.updateResourceAuthorization(adminPausedAccount.id, { status: 'paused' }, unscopedAdminAccess)
  result.push(refreshAuthorization(adminPausedAccount.id, unscopedAdminAccess))

  const adminExpiredAccount = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accounts.testerShared.id,
    granteeType: 'system_account',
    granteeId: users.admin.id,
    targetGroupId: defaultGptGroup(users.admin.id).id,
    remark: `${namePrefix}测试账户授权给超级管理员后过期`,
    expiresAt: new Date(Date.now() + 4 * dayMs).toISOString(),
    limits: quotaLimits(4, 24, 96)
  }, unscopedAdminAccess)
  repositories.updateResourceAuthorization(adminExpiredAccount.id, {
    status: 'expired',
    expiresAt: new Date(Date.now() - 4 * dayMs).toISOString()
  }, unscopedAdminAccess)
  result.push(refreshAuthorization(adminExpiredAccount.id, unscopedAdminAccess))

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
    targetGroupId: defaultGptGroup(users.ops.id).id,
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
    targetGroupId: defaultGptGroup(users.viewer.id).id,
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
    targetGroupId: defaultGptGroup(users.tester.id).id,
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
    targetGroupId: defaultGptGroup(users.viewer.id).id,
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
  const adminAuthorizedGroups = repositories.createApiKeyRecord({
    name: `${namePrefix}超级管理员授权分组 Key`,
    description: 'Mockdata 超级管理员使用别人授权给自己的分组，验证 AI 分组管理和授权分组路由',
    groupBindings: [{ groupId: groups.adminGrantedDev.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(10, 80, 300)
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

  const managerMain = repositories.createApiKeyRecord({
    name: `${namePrefix}管理员网关 Key`,
    description: 'Mockdata 普通管理员本地网关 Key，绑定管理员自有分组',
    groupBindings: [{ groupId: groups.managerMain.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: quotaLimits(16, 120, 420)
  }, mockUserAccess(users.manager))

  const managerHighConcurrency = repositories.createApiKeyRecord({
    name: `${namePrefix}管理员高并发 Key`,
    description: 'Mockdata 普通管理员高并发 Key，绑定管理员高并发分组',
    groupBindings: [{ groupId: groups.managerHighConcurrency.id, priority: 1, status: 'active' }],
    status: 'active',
    quotaLimits: {
      hourly: { enabled: true, hours: 1, limit: 90 },
      daily: { enabled: true, limit: 720 },
      monthly: { enabled: true, limit: 2600 }
    }
  }, mockUserAccess(users.manager))

  const devGroupAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}研发授权调用 Key`,
    description: 'Mockdata 研发用户使用授权分组和授权账户的 Key',
    groupBindings: [
      { groupId: groups.main.id, priority: 1, status: 'active' },
      { groupId: groups.devDefault.id, priority: 2, status: 'active' }
    ],
    status: 'active',
    quotaLimits: quotaLimits(8, 50, 180)
  }, { systemAccountId: users.dev.id, role: 'user' })

  const testerTeamAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}团队授权调用 Key`,
    description: 'Mockdata 测试用户使用团队授权分组和团队授权账户的 Key',
    groupBindings: [
      { groupId: groups.backup.id, priority: 1, status: 'active' },
      { groupId: groups.testerDefault.id, priority: 2, status: 'active' },
      { groupId: groups.experiment.id, priority: 3, status: 'active' }
    ],
    status: 'active',
    quotaLimits: quotaLimits(6, 40, 150)
  }, { systemAccountId: users.tester.id, role: 'user' })

  const opsAccountAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}账户授权调用 Key`,
    description: 'Mockdata 运维用户使用授权分组和授权账户的 Key',
    groupBindings: [
      { groupId: groups.highConcurrency.id, priority: 1, status: 'active' },
      { groupId: groups.oauth.id, priority: 2, status: 'active' },
      { groupId: groups.opsDefault.id, priority: 3, status: 'active' }
    ],
    status: 'active',
    quotaLimits: quotaLimits(6, 36, 120)
  }, { systemAccountId: users.ops.id, role: 'user' })

  const financeAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}财务授权调用 Key`,
    description: 'Mockdata 财务用户使用授权分组和授权账户的 Key',
    groupBindings: [
      { groupId: groups.oauth.id, priority: 1, status: 'active' },
      { groupId: groups.financeDefault.id, priority: 2, status: 'active' }
    ],
    status: 'active',
    quotaLimits: quotaLimits(5, 30, 100)
  }, { systemAccountId: users.finance.id, role: 'user' })

  const viewerAuthorized = repositories.createApiKeyRecord({
    name: `${namePrefix}观察授权调用 Key`,
    description: 'Mockdata 观察用户使用授权分组和授权账户的 Key',
    groupBindings: [
      { groupId: groups.backup.id, priority: 1, status: 'active' },
      { groupId: groups.oauth.id, priority: 2, status: 'active' },
      { groupId: groups.viewerDefault.id, priority: 3, status: 'active' }
    ],
    status: 'active',
    quotaLimits: quotaLimits(4, 24, 80)
  }, { systemAccountId: users.viewer.id, role: 'user' })

  return {
    adminMain,
    adminHighConcurrency,
    adminHighFrequency,
    adminBackup,
    adminOAuth,
    adminAuthorizedGroups,
    adminDisabled,
    adminExpired,
    managerMain,
    managerHighConcurrency,
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

function createResponseInspectionPolicies(): number {
  const policies = [
    {
      name: `${namePrefix}响应错误切换账户`,
      enabled: true,
      priority: 20,
      scopeType: 'provider' as const,
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      match: {
        errorCodes: ['rate_limit_exceeded', 'server_error'],
        outputTextIncludes: ['Mockdata']
      },
      action: 'retry_next_account' as const,
      notes: 'Mockdata 管理端策略：命中响应错误后请求下一个账号'
    },
    {
      name: `${namePrefix}安全策略干跑观察`,
      enabled: true,
      priority: 35,
      scopeType: 'provider' as const,
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      match: {
        errorCodes: ['cyber_policy'],
        jsonPathsExists: ['response.error'],
        outputTextIncludes: ['policy']
      },
      action: 'observe' as const,
      notes: 'Mockdata GPT 供应商层策略：只观察安全策略命中，不改变响应'
    },
    {
      name: `${namePrefix}图像响应异常账号避让`,
      enabled: false,
      priority: 55,
      scopeType: 'provider' as const,
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      match: {
        finishReasons: ['failed'],
        outputTextIncludes: ['image_generation'],
        outputTextExcludes: ['completed']
      },
      action: 'avoid_account_ttl' as const,
      notes: 'Mockdata 停用策略，用于响应检查策略页面状态展示'
    }
  ]
  for (const policy of policies) {
    createResponseInspectionPolicy(policy)
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
    [0, 1, groups.oauth, accounts.oauthBackup],
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
    [groups.managerMain.id, users.manager],
    [groups.managerHighConcurrency.id, users.manager],
    [groups.adminGrantedDev.id, users.dev],
    [groups.adminGrantedOps.id, users.ops],
    [groups.adminGrantedTester.id, users.tester],
    [groups.managerDefault.id, users.manager],
    [groups.devDefault.id, users.dev],
    [groups.opsDefault.id, users.ops],
    [groups.testerDefault.id, users.tester],
    [groups.financeDefault.id, users.finance],
    [groups.viewerDefault.id, users.viewer]
  ])
}

function mockApiKeyOwnerByName(name: string, users: MockSystemAccounts): SystemAccountSummary | undefined {
  if (name.startsWith('admin')) return users.admin
  if (name.startsWith('manager')) return users.manager
  if (name.startsWith('dev')) return users.dev
  if (name.startsWith('tester')) return users.tester
  if (name.startsWith('ops')) return users.ops
  if (name.startsWith('finance')) return users.finance
  if (name.startsWith('viewer')) return users.viewer
  return undefined
}

function apiKeySummariesForMockdata(
  apiKeys: MockApiKeys,
  groupById: Map<string, GroupSummary>,
  groupOwnerById: Map<string, SystemAccountSummary>,
  users: MockSystemAccounts
): Array<Record<string, unknown>> {
  return (Object.entries(apiKeys) as Array<[string, ApiKeyWithSecret]>).map(([name, key]) => {
    const keyOwner = mockApiKeyOwnerByName(name, users)
    return {
      name,
      id: key.id,
      label: key.name,
      description: key.description,
      ownerSystemAccountId: key.systemAccountId ?? keyOwner?.id,
      ownerSystemAccountName: key.systemAccountName ?? keyOwner?.displayName,
      bindingScope: 'visible_group',
      bindingRule: apiKeyAuthorizedGroupBindingRule,
      groupBindings: key.groupBindings.map((binding) => {
        const group = groupById.get(binding.groupId)
        const owner = groupOwnerById.get(binding.groupId)
        const accessType = keyOwner && owner?.id === keyOwner.id ? 'owner' : 'authorized'
        return {
          groupId: binding.groupId,
          groupName: binding.groupName ?? group?.name,
          groupOwnerSystemAccountId: owner?.id ?? group?.systemAccountId,
          groupOwnerSystemAccountName: owner?.displayName ?? group?.systemAccountName,
          accessType,
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
      bindableToApiKey: authorization.status === 'active',
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
    authorizedUsageRecordNote: 'usage_records 中的 group_authorized 样本用于授权分组直接作为 API Key 号池时的调度、审计和授权用量统计。',
    apiKeys: apiKeySummariesForMockdata(created.apiKeys, groupById, groupOwnerById, created.users),
    authorizationSamples: groupAuthorizationSamples(created.authorizations),
    counts: {
      users: Object.keys(created.users).length - 1,
      groups: Object.keys(created.groups).length,
      accounts: Object.keys(created.accounts).length,
      apiKeys: Object.keys(created.apiKeys).length,
      teams: Object.keys(created.teams).length,
      authorizations: created.authorizations.length,
      externalSources: Object.keys(created.externalSources).length,
      responseInspectionPolicies: created.responseInspectionPolicies,
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

function defaultGptGroup(systemAccountId: string): GroupSummary {
  const row = getBusinessDatabase()
    .prepare("SELECT id FROM groups WHERE system_account_id = ? AND provider_code = 'gpt' AND is_default = 1 LIMIT 1")
    .get(systemAccountId) as unknown as { id?: string } | undefined
  if (!row?.id) throw new Error(`未找到默认 GPT 分组：${systemAccountId}`)
  const group = repositories.findGroupSummary(row.id, { systemAccountId, role: 'user' })
  if (!group) throw new Error(`默认 GPT 分组不可读：${systemAccountId}`)
  return group
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
}

function mockdataSummaryPath(): string {
  return join(dirname(runtimeConfig.databasePath), 'mockdata-summary.json')
}

main()
