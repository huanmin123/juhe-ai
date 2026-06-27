import type {
  GroupSummary,
  SystemAccountSummary
} from '../../../../domain/types.js'
import type { AccessScope } from '../../../../storage/access-scope.js'
import { getBusinessDatabase, nowIso } from '../../../../storage/database.js'
import * as repositories from '../../../../storage/repositories.js'
import { accountApiKeyEntries } from '../../../../storage/account-api-key-rotation.js'
import {
  activeAccountAvailabilitySchedule,
  inactiveAccountAvailabilitySchedule
} from './availability-schedules.js'
import { refreshAccount } from './account-helpers.js'
import {
  dayMs,
  idPrefix,
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

  const standardClient = repositories.createAccount({
    providerCode,
    name: `${namePrefix}OpenAI 标准兼容账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.main.id,
    credentials: apiKeyCredentials('standard-client'),
    supportedModels: ['gpt-5.4-mini', 'gpt-4.1-mini'],
    modelMappings: [
      {
        sourceModel: 'mockdata-global-long-context',
        sourceEndpointFamily: 'chat_completions',
        upstreamModel: 'gpt-5.4-mini',
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      },
      {
        sourceModel: 'gpt-5.5',
        sourceEndpointFamily: 'chat_completions',
        upstreamModel: 'gpt-5.4',
        upstreamEndpointFamily: 'chat_completions',
        enabled: false
      }
    ],
    tags: ['标准兼容', '模型映射', 'Mockdata'],
    concurrencyLimit: 30,
    priority: 35,
    notes: 'Mockdata OpenAI 标准兼容账号，用于客户端兼容、模型映射和标签展示'
  }, adminAccess)

  const multiKeyPool = repositories.createAccount({
    providerCode,
    name: `${namePrefix}多 API Key 轮换账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.main.id,
    credentials: multiApiKeyCredentials('multi-key-pool', 'weighted_round_robin'),
    supportedModels: ['gpt-5.4-mini', 'gpt-4.1-mini'],
    tags: ['多Key', '故障隔离'],
    concurrencyLimit: 70,
    priority: 18,
    notes: 'Mockdata 账户内多 API Key，用于 key 级故障隔离、轮换策略和运行态汇总展示'
  }, adminAccess)
  seedAccountApiKeyRuntimeStates(multiKeyPool)

  const image = repositories.createAccount({
    providerCode,
    name: `${namePrefix}图像生成账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.experiment.id,
    credentials: apiKeyCredentials('image'),
    supportedModels: ['mockdata-global-image', 'gpt-image-1'],
    tags: ['图像生成'],
    concurrencyLimit: 18,
    priority: 50,
    notes: 'Mockdata 图像生成账号，用于 Images API、图片 token 和系统账户图像权限展示'
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

  const pendingTest = repositories.createAccount({
    providerCode,
    name: `${namePrefix}待测试账户`,
    type: 'api_key',
    status: 'pending_test',
    groupId: groups.experiment.id,
    credentials: apiKeyCredentials('pending-test'),
    supportedModels: ['gpt-5.4-mini'],
    tags: ['待测试'],
    concurrencyLimit: 10,
    priority: 90,
    notes: 'Mockdata 待测试账号，用于待测试状态、手动测试入口和不可调度展示'
  }, adminAccess)

  const disabled = repositories.createAccount({
    providerCode,
    name: `${namePrefix}手动停用账户`,
    type: 'api_key',
    status: 'disabled',
    groupId: groups.experiment.id,
    credentials: apiKeyCredentials('disabled'),
    supportedModels: ['gpt-5.4-mini'],
    tags: ['停用'],
    concurrencyLimit: 8,
    priority: 95,
    notes: 'Mockdata 手动停用账号，用于停用状态和恢复入口展示'
  }, adminAccess)

  const unschedulable = repositories.createAccount({
    providerCode,
    name: `${namePrefix}停调账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.experiment.id,
    credentials: apiKeyCredentials('unschedulable'),
    supportedModels: ['gpt-5.4-mini'],
    schedulable: false,
    tags: ['停调'],
    concurrencyLimit: 8,
    priority: 98,
    notes: 'Mockdata 正常但手动关闭调度账号，用于参与调度筛选和有效可用性展示'
  }, adminAccess)

  const scheduledInactive = repositories.createAccount({
    providerCode,
    name: `${namePrefix}时间计划停调账户`,
    type: 'api_key',
    status: 'active',
    groupId: groups.experiment.id,
    credentials: apiKeyCredentials('scheduled-inactive'),
    supportedModels: ['gpt-5.4-mini'],
    availabilitySchedule: inactiveAccountAvailabilitySchedule(),
    tags: ['时间计划'],
    concurrencyLimit: 8,
    priority: 100,
    notes: 'Mockdata 当前不在允许时段内的账号，用于账户时间计划和调度过滤展示'
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
    standardClient,
    multiKeyPool: refreshAccount(multiKeyPool.id),
    image,
    burstFast,
    burstImage,
    burstFallback,
    fallback,
    oauth,
    oauthBackup,
    pendingTest,
    disabled,
    unschedulable,
    scheduledInactive,
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

function multiApiKeyCredentials(suffix: string, strategy: 'round_robin' | 'weighted_round_robin'): Record<string, unknown> {
  const apiKeys = [1, 2, 3, 4].map((index) => `sk-mockdata-admin-${suffix}-${index}-${'m'.repeat(20)}`)
  return {
    api_key: apiKeys[0],
    api_keys: apiKeys,
    api_key_strategy: strategy,
    api_key_weights: strategy === 'weighted_round_robin' ? [6, 3, 1, 1] : undefined,
    base_url: 'https://api.openai.com/v1',
    error_handling_rules: [
      {
        enabled: true,
        name: 'Mockdata 429 限流冷却',
        priority: 1,
        status_codes: [429],
        action: 'rate_limited',
        reset_strategy: 'duration',
        duration_hours: 2,
        description: 'Mockdata key 级限流规则'
      },
      {
        enabled: true,
        name: 'Mockdata 5xx 临时停调',
        priority: 2,
        status_codes: [500, 502, 503],
        action: 'temp_unschedulable',
        description: 'Mockdata key 级临时停调规则'
      }
    ],
    response_inspection_rules: [
      {
        enabled: true,
        name: 'Mockdata 输出污染切号',
        priority: 10,
        match: {
          outputTextIncludes: ['Mockdata 广告污染'],
          accountClientCompatibilities: ['codex_responses']
        },
        action: 'retry_next_account',
        notes: 'Mockdata 账户级响应检查规则'
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
    base_url: 'https://api.openai.com/v1'
  }
}

function seedAccountApiKeyRuntimeStates(account: { id: string; systemAccountId?: string; credentials: Record<string, unknown> }): void {
  const entries = accountApiKeyEntries(account.credentials)
  if (entries.length < 4) return
  const now = nowIso()
  const database = getBusinessDatabase()
  const states = [
    { entryIndex: 1, status: 'temporary_unavailable', failures: 2, successes: 8, message: 'Mockdata 模拟 key 级 503 冷却', nextProbeMinutes: 5 },
    { entryIndex: 2, status: 'rate_limited', failures: 5, successes: 21, message: 'Mockdata 模拟 key 级限流', nextProbeMinutes: 45 },
    { entryIndex: 3, status: 'error', failures: 7, successes: 3, message: 'Mockdata 模拟 key 级认证异常', nextProbeMinutes: 15 }
  ]
  const insert = database.prepare(`
    INSERT INTO account_api_key_runtime_states (
      id, system_account_id, account_id, key_fingerprint, key_index, credential_revision,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
      last_attempt_at, last_success_at, last_failure_at, last_error_code, last_error_message,
      last_probe_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(account_id, key_fingerprint) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      key_index = excluded.key_index,
      credential_revision = excluded.credential_revision,
      status = excluded.status,
      failure_count = excluded.failure_count,
      consecutive_failures = excluded.consecutive_failures,
      success_count = excluded.success_count,
      cooldown_until = excluded.cooldown_until,
      next_probe_at = excluded.next_probe_at,
      probe_backoff_seconds = excluded.probe_backoff_seconds,
      recovery_started_at = excluded.recovery_started_at,
      last_attempt_at = excluded.last_attempt_at,
      last_success_at = excluded.last_success_at,
      last_failure_at = excluded.last_failure_at,
      last_error_code = excluded.last_error_code,
      last_error_message = excluded.last_error_message,
      last_probe_at = excluded.last_probe_at,
      updated_at = excluded.updated_at
  `)
  for (const state of states) {
    const entry = entries[state.entryIndex]
    if (!entry) continue
    const nextProbeAt = new Date(Date.now() + state.nextProbeMinutes * 60_000).toISOString()
    insert.run(
      `${idPrefix}account_api_key_runtime_${state.entryIndex}`,
      account.systemAccountId ?? null,
      account.id,
      entry.fingerprint,
      entry.index,
      'mockdata',
      state.status,
      state.failures,
      state.failures,
      state.successes,
      nextProbeAt,
      nextProbeAt,
      state.nextProbeMinutes * 60,
      now,
      now,
      new Date(Date.now() - 2 * 60_000).toISOString(),
      now,
      state.status === 'rate_limited' ? 'rate_limit_exceeded' : state.status === 'error' ? 'invalid_api_key' : 'service_unavailable',
      state.message,
      new Date(Date.now() - 60_000).toISOString(),
      now,
      now
    )
  }
}
