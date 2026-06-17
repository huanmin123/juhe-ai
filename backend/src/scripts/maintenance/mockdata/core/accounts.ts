import type {
  GroupSummary,
  SystemAccountSummary
} from '../../../../domain/types.js'
import type { AccessScope } from '../../../../storage/access-scope.js'
import * as repositories from '../../../../storage/repositories.js'
import {
  activeAccountAvailabilitySchedule,
  inactiveAccountAvailabilitySchedule
} from './availability-schedules.js'
import { refreshAccount } from './account-helpers.js'
import {
  dayMs,
  namePrefix,
  providerCode,
  type MockAccounts,
  type MockGroups,
  type MockSystemAccounts
} from '../shared.js'

type DefaultGptGroupResolver = (systemAccountId: string) => GroupSummary

function mockUserAccess(user: SystemAccountSummary): AccessScope {
  return { systemAccountId: user.id, role: user.role }
}

export function createGroups(
  adminAccess: AccessScope,
  users: MockSystemAccounts,
  defaultGptGroup: DefaultGptGroupResolver
): MockGroups {
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

export function createAccounts(
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
    availabilitySchedule: activeAccountAvailabilitySchedule(),
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
    availabilitySchedule: inactiveAccountAvailabilitySchedule(),
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
    availabilitySchedule: activeAccountAvailabilitySchedule(),
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
