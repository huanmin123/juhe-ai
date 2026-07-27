import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GPT_OPENAI_V1_PROFILE_ID
} from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { accountTestFailureEligibleForAccount } from '../../modules/accounts/account-test-failure-eligibility.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-health-check-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-health-check.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-health-check-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, healthCheckRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-health-check.repository.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const healthSettings = {
  intervalHours: 12,
  jitterMinutes: 0,
  failureThreshold: 3
}

try {
  const defaultHealthSettings = healthCheckRepository.normalizedHealthCheckSettings()
  assert.equal(defaultHealthSettings.intervalHours, 1, '默认健康检查基础间隔应为 1 小时')
  assert.equal(defaultHealthSettings.jitterMinutes, 10, '默认健康检查应按账户稳定错峰 0 到 10 分钟')
  const scheduleBaseAt = '2026-07-26T00:00:00.000Z'
  const staggeredSchedules = ['account-alpha', 'account-beta', 'account-gamma', 'account-delta'].map((accountId) => (
    healthCheckRepository.accountHealthSuccessSignalSchedule(accountId, scheduleBaseAt, defaultHealthSettings).nextHealthCheckAt
  ))
  const staggeredOffsets = staggeredSchedules.map((nextCheckAt) => Date.parse(nextCheckAt) - Date.parse(scheduleBaseAt))
  assert.ok(staggeredOffsets.every((offset) => offset >= 60 * 60_000 && offset < 70 * 60_000), '默认下次检查必须落在 1 小时后的 10 分钟错峰窗口内')
  assert.ok(new Set(staggeredOffsets).size > 1, '不同账户不能全部集中在同一个检查时间点')
  assert.equal(
    healthCheckRepository.accountHealthSuccessSignalSchedule('account-alpha', scheduleBaseAt, defaultHealthSettings).nextHealthCheckAt,
    staggeredSchedules[0],
    '同一账户的错峰偏移必须稳定，不能因重复计算改变'
  )

  const postgresFailureStartedAt = new Date('2026-07-16T20:13:55.032Z')
  assert.equal(
    healthCheckRepository.accountHealthCheckDatabaseDateTimeIso(postgresFailureStartedAt),
    postgresFailureStartedAt.toISOString(),
    'PostgreSQL timestamptz 返回 Date 时，健康检查首次失败时间必须归一化为 ISO 字符串'
  )
  assert.equal(
    healthCheckRepository.accountHealthCheckHasNewerSuccess(
      new Date('2026-07-16T20:13:56.000Z'),
      '2026-07-16T20:13:55.000Z'
    ),
    true,
    'PostgreSQL 健康成功时间返回 Date 时，旧探测保护必须按时间语义生效'
  )

  const repositorySource = readFileSync(resolve('src/storage/account-health-check.repository.ts'), 'utf8')
  const usageRepositorySource = readFileSync(resolve('src/storage/usage-records.repository.ts'), 'utf8')
  const serviceSource = readFileSync(resolve('src/modules/background/account-health-check.service.ts'), 'utf8')
  const postgresSuccessSource = sourceBetween(
    repositorySource,
    'export async function recordAccountHealthCheckSuccessAsync',
    'function healthCheckActivationStatus'
  )
  const postgresFailureSource = sourceBetween(
    repositorySource,
    'export async function recordAccountHealthCheckFailureAsync',
    'export function recordAccountHealthSuccessSignals'
  )
  assert.match(repositorySource, /SELECT status, config_revision, health_check_failure_count, health_check_failure_started_at, last_health_success_at[\s\S]+FOR UPDATE/, 'PostgreSQL 健康失败计数和首次失败窗口必须锁定账户行后更新')
  assert.match(repositorySource, /expectedConfigRevision[\s\S]+config_revision = \?/, '健康检查结果写入必须绑定账户配置版本')
  assert.doesNotMatch(postgresSuccessSource, /\(\? IS NULL OR/, 'PostgreSQL 健康成功写回不能使用无法推断参数类型的 NULL 守卫')
  assert.doesNotMatch(postgresFailureSource, /\(\? IS NULL OR/, 'PostgreSQL 健康失败写回不能使用无法推断参数类型的 NULL 守卫')
  assert.match(repositorySource, /CASE WHEN accounts\.status = 'pending_test' THEN 0 ELSE 1 END/, '周期兜底应优先处理待检查账户')
  assert.equal(
    repositorySource.match(/accounts\.status = 'pending_test' AND accounts\.last_health_check_at IS NULL/g)?.length,
    2,
    'SQLite 和 PostgreSQL 候选查询都应让从未检查的待检查账户忽略遗留复检时间'
  )
  assert.match(serviceSource, /queuedConfigRevision[\s\S]+currentConfigRevision/, '健康检查队列执行前必须丢弃旧配置版本任务')
  assert.match(serviceSource, /healthCheckGuard/, '达到阈值后的保护状态写入必须携带健康失败快照')
  assert.match(serviceSource, /errorLogFields\(event\.error/, '健康检查队列耗尽日志必须保留真实异常')
  assert.match(repositorySource, /pendingHealthCheckRetryIntervalMs = 60 \* 60_000/, '待检查账户失败后必须固定每 1 小时复检')
  assert.match(repositorySource, /pendingHealthCheckFailureTimeoutMs = 24 \* 60 \* 60_000/, '待检查账户必须从首次失败起 24 小时收敛为异常')
  assert.match(repositorySource, /account_activation_check_timeout/, '待检查超时必须写入明确异常码')
  assert.match(repositorySource, /function availabilityScheduleJsonValue/, '健康检查必须兼容 PostgreSQL JSONB 返回对象')
  assert.match(repositorySource, /JSON\.stringify\(value\)/, 'PostgreSQL JSONB 时间计划必须规范化为现有解析器使用的 JSON 文本')
  assert.match(usageRepositorySource, /accountHealthSuccessSignalSchedule\(accountId, successAt, healthCheckSettings \?\? \{\}\)/, 'PostgreSQL 真实成功请求必须复用健康检测间隔与 jitter 计划')
  assert.match(usageRepositorySource, /SET last_health_success_at = \?,[\s\S]+next_health_check_at = \?/, 'PostgreSQL 真实成功请求必须同时顺延下次健康复核')
  assert.match(usageRepositorySource, /next_health_check_at < \?[\s\S]+next_health_check_at > \?/, 'PostgreSQL 真实成功请求应与 SQLite 一致节流，避免每请求重写账户行')
  assert.equal((repositorySource.match(/AND status = 'active'/g) ?? []).length >= 2, true, 'SQLite 和通用 PostgreSQL 成功信号写回应只更新 active 账户')
  assert.match(usageRepositorySource, /WHERE id = \?[\s\S]+AND status = 'active'/, 'PostgreSQL usage 成功信号不得覆盖待检查状态')
  assert.equal(
    (repositorySource.match(/row\.status !== 'pending_test' && recentSuccessAt/g) ?? []).length,
    2,
    'SQLite 和 PostgreSQL 都必须让修改配置后的待检查账户忽略旧成功信号并立即进入检查候选'
  )

  const database = databaseModule.getBusinessDatabase()
  const accountColumns = database.prepare('PRAGMA table_info(accounts)').all() as unknown as Array<{ name: string }>
  for (const column of [
    'last_health_check_at',
    'next_health_check_at',
    'last_health_success_at',
    'health_check_failure_count',
    'health_check_failure_started_at',
    'last_health_check_status_code',
    'last_health_check_error_code',
    'last_health_check_error_message',
    'last_error_trace_id',
    'last_health_check_trace_id'
  ]) {
    assert.ok(accountColumns.some((row) => row.name === column), `accounts 应包含 ${column} 字段`)
  }
  for (const [statusCode, errorCode, message] of [
    [400, 'invalid_request_error', 'Unsupported model'],
    [401, 'invalid_api_key', 'Invalid API key'],
    [404, 'model_not_found', 'The model does not exist'],
    [429, 'rate_limit', 'Provider-defined throttling'],
    [500, 'server_error', 'Provider-defined failure'],
    [503, 'upstream_body_interrupted', 'Provider-controlled diagnostic']
  ] as const) {
    assert.equal(accountTestFailureEligibleForAccount({ statusCode, errorCode, message }), true, `HTTP ${statusCode} 的供应商语义不得改变诊断重试资格`)
  }

  const group = repositories.createGroup({
    name: '账号健康检测回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const anthropicGroup = repositories.createGroup({
    name: 'Anthropic 健康检测回归分组',
    providerCode: 'anthropic',
    enabled: true
  }, access)
  const geminiGroup = repositories.createGroup({
    name: 'Gemini 健康检测回归分组',
    providerCode: 'gemini',
    enabled: true
  }, access)
  const dueAccount = createActiveAccount(repositories, group.id, '健康检测到期账号', 'sk-health-due')
  const recentAccount = createActiveAccount(repositories, group.id, '健康检测近期成功账号', 'sk-health-recent')
  const disabledAccount = createActiveAccount(repositories, group.id, '健康检测停用账号', 'sk-health-disabled')
  const runtimeFailureTraceId = 'trace-runtime-failure-regression'
  assert(repositories.markAccountTemporaryUnavailable(dueAccount.id, '上游运行态失败', undefined, runtimeFailureTraceId), '运行态失败应写入冷却状态')
  assert.equal(repositories.findAccountSummary(dueAccount.id, access)?.lastErrorTraceId, runtimeFailureTraceId, '运行态错误提示必须保留对应 traceId')
  repositories.clearAccountFailureStateResult(dueAccount.id, access, { allowErrorRestore: false })
  assert.equal(repositories.findAccountSummary(dueAccount.id, access)?.lastErrorTraceId, undefined, '恢复账户时必须同步清理旧错误 traceId')
  repositories.updateAccount(disabledAccount.id, { status: 'disabled' }, access)
  const futureScheduledAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '健康检测未来时间计划账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-health-future-schedule',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-5.5'],
    availabilitySchedule: {
      enabled: true,
      timezone: 'UTC',
      mode: 'allow_windows',
      dateRange: { startDate: '2999-01-01' },
      windows: [{ daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:00', end: '23:59' }]
    }
  }, access)
  assert.equal(repositories.recordAccountHealthCheckSuccess(futureScheduledAccount.id, {
    ...healthSettings,
    statusCode: 200
  }), true, '未来时间计划账户的后台健康检查应能写入成功事实')
  assert.equal(
    repositories.findAccountSummary(futureScheduledAccount.id, access)?.status,
    'disabled',
    '后台健康检查成功不得绕过账户时间计划激活账户'
  )

  const oldCheckAt = new Date(Date.now() - 13 * 60 * 60_000).toISOString()
  const recentSuccessAt = new Date(Date.now() - 30 * 60_000).toISOString()
  database.prepare(`
    UPDATE accounts
    SET last_health_check_at = ?,
        next_health_check_at = ?,
        last_health_success_at = NULL,
        health_check_failure_count = 0
    WHERE id = ?
  `).run(oldCheckAt, oldCheckAt, dueAccount.id)
  database.prepare(`
    UPDATE accounts
    SET next_health_check_at = ?,
        last_health_success_at = ?,
        health_check_failure_count = 2,
        last_health_check_error_code = 'account_health_check_failed',
        last_health_check_error_message = '旧失败'
    WHERE id = ?
  `).run(oldCheckAt, recentSuccessAt, recentAccount.id)

  const pendingFirstCheckAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '待检查账户不应继承未来复检时间',
    type: 'api_key',
    credentials: {
      api_key: 'sk-health-pending-first-check',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-5.5']
  }, access)
  database.prepare(`
    UPDATE accounts
    SET next_health_check_at = ?,
        last_health_check_at = NULL,
        last_health_success_at = ?
    WHERE id = ?
  `).run(
    new Date(Date.now() + 12 * 60 * 60_000).toISOString(),
    new Date().toISOString(),
    pendingFirstCheckAccount.id
  )

  const anthropicPendingAccount = repositories.createAccount({
    providerCode: 'anthropic',
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    name: 'Anthropic 待检查候选账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-health-anthropic-pending',
      base_url: 'https://api.anthropic.com/v1'
    },
    groupId: anthropicGroup.id,
    supportedModels: ['claude-sonnet-4-5'],
    healthCheckModel: 'claude-sonnet-4-5',
    healthCheckEndpointMode: 'messages_sse'
  }, access)
  const geminiPendingAccount = repositories.createAccount({
    providerCode: 'gemini',
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    name: 'Gemini 待检查候选账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-health-gemini-pending',
      base_url: 'https://generativelanguage.googleapis.com/v1beta'
    },
    groupId: geminiGroup.id,
    supportedModels: ['gemini-2.5-flash'],
    healthCheckModel: 'gemini-2.5-flash',
    healthCheckEndpointMode: 'generate_content_sse'
  }, access)

  const due = repositories.listAccountsDueForHealthCheck({ limit: 10, ...healthSettings })
  const dueIds = due.map((account) => account.id)
  for (const pendingId of [pendingFirstCheckAccount.id, anthropicPendingAccount.id, geminiPendingAccount.id]) {
    assert.ok(dueIds.includes(pendingId), '八种生成协议的待检查账户都必须进入常规健康检查候选')
    assert.ok(dueIds.indexOf(pendingId) < dueIds.indexOf(dueAccount.id), '待检查账户必须优先于到期 active 账户')
  }
  assert.equal(repositories.findAccountForHealthCheck(anthropicPendingAccount.id)?.id, anthropicPendingAccount.id, 'Anthropic Messages 待检查账户不得被 OpenAI profile 限制')
  assert.equal(repositories.findAccountForHealthCheck(geminiPendingAccount.id)?.id, geminiPendingAccount.id, 'Gemini GenerateContent 待检查账户不得被 OpenAI profile 限制')

  const recentAfterSkip = repositories.findAccountSummary(recentAccount.id, access)
  assert.equal(recentAfterSkip?.healthCheckFailureCount, 0, '近期成功信号应清理健康检测失败计数')
  assert.equal(recentAfterSkip?.lastHealthCheckErrorCode, undefined, '近期成功信号应清理健康检测错误码')
  assert.ok(recentAfterSkip?.nextHealthCheckAt, '近期成功信号应顺延下次检测时间')
  assert.ok(Date.parse(recentAfterSkip?.nextHealthCheckAt ?? '') > Date.now() + 11 * 60 * 60_000, '顺延后的下次检测应接近 12 小时后')

  const successAt = new Date().toISOString()
  repositories.recordAccountHealthCheckSuccess(dueAccount.id, {
    ...healthSettings,
    checkedAt: successAt,
    statusCode: 200,
    traceId: 'trace-health-success-regression'
  })
  const dueAfterSuccess = repositories.findAccountSummary(dueAccount.id, access)
  assert.equal(dueAfterSuccess?.lastHealthCheckAt, successAt, '健康检测成功应写入检测时间')
  assert.equal(dueAfterSuccess?.lastHealthSuccessAt, successAt, '健康检测成功应写入成功时间')
  assert.equal(dueAfterSuccess?.healthCheckFailureCount, 0, '健康检测成功应清零失败计数')
  assert.equal(dueAfterSuccess?.lastHealthCheckStatusCode, 200, '健康检测成功应记录 HTTP 状态码')
  assert.equal(dueAfterSuccess?.lastHealthCheckTraceId, 'trace-health-success-regression', '健康检测成功应记录结构化 traceId')
  assert.equal(
    repositories.findAccountForHealthCheck(dueAccount.id),
    undefined,
    '下次检查时间未到时，周期健康检查仍应跳过账户'
  )
  assert.equal(
    repositories.findAccountForHealthCheck(dueAccount.id, { ignoreSchedule: true })?.id,
    dueAccount.id,
    '真实请求失败触发的独立检查必须绕过周期到期门槛'
  )

  const staleProbeAccount = createActiveAccount(repositories, group.id, '健康检测配置版本账号', 'sk-health-revision')
  const staleProbeBefore = repositories.findAccountSummary(staleProbeAccount.id, access)
  const staleProbeRevision = staleProbeBefore?.configRevision
  assert.ok(staleProbeRevision, '健康检测候选应包含配置版本')
  repositories.updateAccount(staleProbeAccount.id, { notes: '探测期间配置已变化' }, access)
  const staleSuccessChanged = repositories.recordAccountHealthCheckSuccess(staleProbeAccount.id, {
    ...healthSettings,
    statusCode: 200,
    expectedConfigRevision: staleProbeRevision
  })
  assert.equal(staleSuccessChanged, false, '旧配置版本的健康检查成功不得写入新配置')
  assert.equal(
    repositories.findAccountSummary(staleProbeAccount.id, access)?.lastHealthCheckAt,
    staleProbeBefore?.lastHealthCheckAt,
    '旧配置版本的健康检查不得留下成功诊断'
  )

  const newerTrafficSuccessAt = new Date().toISOString()
  database.prepare(`
    UPDATE accounts
    SET last_health_success_at = ?,
        health_check_failure_count = 0
    WHERE id = ?
  `).run(newerTrafficSuccessAt, staleProbeAccount.id)
  const staleFailure = repositories.recordAccountHealthCheckFailure(staleProbeAccount.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '旧探测失败',
    expectedConfigRevision: repositories.findAccountSummary(staleProbeAccount.id, access)?.configRevision,
    observedAt: new Date(Date.parse(newerTrafficSuccessAt) - 1_000).toISOString()
  })
  assert.equal(staleFailure.changed, false, '较新的真实请求成功后，旧健康检查失败不得覆盖成功信号')
  assert.equal(staleFailure.failureCount, 0, '旧健康检查失败不得重新累加已被真实成功清零的计数')

  const configurationFailure = repositories.recordAccountHealthCheckFailure(dueAccount.id, {
    ...healthSettings,
    statusCode: 404,
    errorCode: 'model_not_found',
    errorMessage: '检查模型不存在',
    countTowardsThreshold: false
  })
  assert.equal(configurationFailure.failureCount, 0, '检查模型或请求配置错误不应累计账户连续失败次数')
  assert.equal(configurationFailure.reachedThreshold, false, '配置错误不得触发账户临时不可用阈值')
  const afterConfigurationFailure = repositories.findAccountSummary(dueAccount.id, access)
  assert.equal(afterConfigurationFailure?.lastHealthCheckErrorCode, 'model_not_found', '配置错误仍应保留健康检查诊断')
  assert.equal(afterConfigurationFailure?.lastHealthCheckTraceId, undefined, '最新检查没有 traceId 时必须清理旧探针 trace，不能与新错误摘要错配')
  assert.ok(afterConfigurationFailure?.nextHealthCheckAt, '配置错误仍应安排后台复检')

  const firstFailure = repositories.recordAccountHealthCheckFailure(dueAccount.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '模拟失败',
    traceId: 'trace-health-failure-regression'
  })
  assert.equal(firstFailure.failureCount, 1, '第一次失败应记录连续失败 1 次')
  assert.equal(firstFailure.reachedThreshold, false, '第一次失败不应达到阈值')
  const secondFailure = repositories.recordAccountHealthCheckFailure(dueAccount.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '模拟失败'
  })
  assert.equal(repositories.findAccountSummary(dueAccount.id, access)?.lastHealthCheckTraceId, undefined, '后续无 trace 探针应覆盖清理前一次失败 trace')
  assert.equal(secondFailure.failureCount, 2, '第二次失败应递增连续失败次数')
  const thirdFailure = repositories.recordAccountHealthCheckFailure(dueAccount.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '模拟失败',
    traceId: 'trace-health-failure-latest'
  })
  assert.equal(thirdFailure.failureCount, 3, '第三次失败应递增到阈值')
  assert.equal(thirdFailure.reachedThreshold, true, '第三次失败应达到阈值')
  const dueAfterFailure = repositories.findAccountSummary(dueAccount.id, access)
  assert.equal(dueAfterFailure?.lastHealthCheckStatusCode, 401, '失败应记录最近 HTTP 状态码')
  assert.equal(dueAfterFailure?.lastHealthCheckErrorCode, 'invalid_api_key', '失败应记录错误码')
  assert.match(dueAfterFailure?.lastHealthCheckErrorMessage ?? '', /模拟失败/, '失败应记录错误摘要')
  assert.equal(dueAfterFailure?.lastHealthCheckTraceId, 'trace-health-failure-latest', '健康检测失败应记录最新探针的结构化 traceId')
  assert.ok(dueAfterFailure?.nextHealthCheckAt, '失败应写入短退避复检时间')
  const guardedAccount = repositories.findAccountSummary(dueAccount.id, access)
  assert.ok(guardedAccount?.configRevision, '达到阈值的账户应包含配置版本')
  const staleGuardUpdate = repositories.markAccountTestTemporaryUnavailable(
    guardedAccount,
    '错误版本不应改变状态',
    access,
    {
      configRevision: (guardedAccount.configRevision ?? 1) + 1,
      checkedAt: thirdFailure.checkedAt,
      failureCount: thirdFailure.failureCount,
      observedAt: thirdFailure.checkedAt
    }
  )
  assert.equal(staleGuardUpdate, undefined, '健康检查临时不可用写入必须校验配置版本和失败快照')
  const guardedUpdate = repositories.markAccountTestTemporaryUnavailable(
    guardedAccount,
    '达到阈值后标记临时不可调用',
    access,
    {
      configRevision: guardedAccount.configRevision ?? 1,
      checkedAt: thirdFailure.checkedAt,
      failureCount: thirdFailure.failureCount,
      observedAt: thirdFailure.checkedAt
    }
  )
  assert.equal(guardedUpdate?.status, 'temporary_unavailable', '匹配健康检查快照时才允许标记临时不可调用')

  const trafficAccount = createActiveAccount(repositories, group.id, '健康检测真实流量账号', 'sk-health-traffic')
  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin',
    traceId: 'trace-health-check-success',
    trafficSource: 'gateway',
    accountId: trafficAccount.id,
    groupId: group.id,
    providerCode: 'gpt',
    endpoint: '/v1/responses',
    model: 'gpt-test',
    stream: false,
    success: true,
    statusCode: 200,
    durationMs: 10
  }])
  const trafficAfterSuccess = repositories.findAccountSummary(trafficAccount.id, access)
  assert.ok(trafficAfterSuccess?.lastHealthSuccessAt, '真实成功请求应刷新健康成功信号')
  assert.ok(trafficAfterSuccess?.nextHealthCheckAt, '真实成功请求应顺延下次健康检测')
  const trafficFirstHealthSuccessAt = trafficAfterSuccess?.lastHealthSuccessAt
  const trafficFirstNextHealthCheckAt = trafficAfterSuccess?.nextHealthCheckAt
  assert.ok(trafficFirstHealthSuccessAt, '真实成功请求应写入健康成功时间')
  assert.ok(trafficFirstNextHealthCheckAt, '真实成功请求应写入下次健康检测时间')

  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin',
    traceId: 'trace-health-check-success-throttled',
    trafficSource: 'gateway',
    accountId: trafficAccount.id,
    groupId: group.id,
    providerCode: 'gpt',
    endpoint: '/v1/responses',
    model: 'gpt-test',
    stream: false,
    success: true,
    statusCode: 200,
    durationMs: 10,
    createdAt: new Date(Date.parse(trafficFirstHealthSuccessAt) + 60_000).toISOString()
  }])
  const trafficAfterEarlySuccess = repositories.findAccountSummary(trafficAccount.id, access)
  assert.equal(trafficAfterEarlySuccess?.lastHealthSuccessAt, trafficFirstHealthSuccessAt, '检测窗口未过半时不应为每次成功请求重写健康成功信号')
  assert.equal(trafficAfterEarlySuccess?.nextHealthCheckAt, trafficFirstNextHealthCheckAt, '检测窗口未过半时不应为每次成功请求重写下次健康检测')

  const credentialChangedAccount = createActiveAccount(repositories, group.id, '健康检测修改 Key 竞态账号', 'sk-health-key-before')
  repositories.updateAccount(credentialChangedAccount.id, {
    credentials: { api_key: 'sk-health-key-after', base_url: 'https://api.openai.com/v1' }
  }, access)
  const pendingAfterCredentialChange = repositories.findAccountSummary(credentialChangedAccount.id, access)
  assert.equal(pendingAfterCredentialChange?.status, 'pending_test', '修改 Key 后账户必须进入待检查')
  assert.equal(pendingAfterCredentialChange?.nextHealthCheckAt, undefined, '修改 Key 后应立即等待后台检查')
  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin', traceId: 'trace-old-inflight-success-after-key-change', trafficSource: 'gateway',
    accountId: credentialChangedAccount.id, groupId: group.id, providerCode: 'gpt', endpoint: '/v1/responses', model: 'gpt-test',
    stream: false, success: true, statusCode: 200, durationMs: 10,
    createdAt: new Date(Date.now() + 60_000).toISOString()
  }])
  const pendingAfterInflightSuccess = repositories.findAccountSummary(credentialChangedAccount.id, access)
  assert.equal(pendingAfterInflightSuccess?.status, 'pending_test', '旧在途成功请求不得激活新配置')
  assert.equal(pendingAfterInflightSuccess?.nextHealthCheckAt, undefined, '旧在途成功请求不得把待检查推迟到正常周期')
  assert(repositories.listAccountsDueForHealthCheck({ limit: 100, ...healthSettings }).some((item) => item.id === credentialChangedAccount.id), '修改 Key 的待检查账户应立即进入健康检查候选')
  repositories.recordAccountHealthCheckFailure(credentialChangedAccount.id, {
    ...healthSettings,
    errorCode: 'probe_task_failure',
    errorMessage: '未收到上游响应头',
    countTowardsThreshold: false
  })
  const pendingAfterInconclusiveProbe = repositories.findAccountSummary(credentialChangedAccount.id, access)
  assert.equal(pendingAfterInconclusiveProbe?.status, 'pending_test', '无结论探针不得改变待检查账户状态')
  assert.equal(pendingAfterInconclusiveProbe?.healthCheckFailureStartedAt, undefined, '无结论探针不得启动 24 小时失败窗口')
  const oldPendingFailureStartedAt = new Date(Date.now() - 25 * 60 * 60_000).toISOString()
  database.prepare('UPDATE accounts SET health_check_failure_started_at = ? WHERE id = ?').run(oldPendingFailureStartedAt, credentialChangedAccount.id)
  repositories.recordAccountHealthCheckFailure(credentialChangedAccount.id, {
    ...healthSettings,
    errorCode: 'probe_task_failure',
    errorMessage: '仍未收到上游响应头',
    countTowardsThreshold: false
  })
  assert.equal(repositories.findAccountSummary(credentialChangedAccount.id, access)?.status, 'pending_test', '既有失败窗口超过 24 小时时，无结论探针也不得把账户转为异常')

  const cooldownProbeAt = trafficAfterSuccess?.lastHealthSuccessAt
  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin',
    traceId: 'trace-health-check-cooldown',
    trafficSource: 'cooldown_retest',
    accountId: trafficAccount.id,
    groupId: group.id,
    providerCode: 'gpt',
    endpoint: '/v1/responses',
    model: 'gpt-test',
    stream: false,
    success: true,
    statusCode: 200,
    durationMs: 10
  }])
  const trafficAfterCooldownProbe = repositories.findAccountSummary(trafficAccount.id, access)
  assert.equal(trafficAfterCooldownProbe?.lastHealthSuccessAt, cooldownProbeAt, '后台探活流量不应刷新真实健康成功信号')

  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin',
    traceId: 'trace-account-health-check',
    trafficSource: 'account_health_check',
    accountId: trafficAccount.id,
    groupId: group.id,
    providerCode: 'gpt',
    endpoint: '/v1/responses',
    model: 'gpt-test',
    stream: false,
    success: true,
    statusCode: 200,
    durationMs: 10
  }])
  const trafficAfterHealthCheck = repositories.findAccountSummary(trafficAccount.id, access)
  assert.equal(trafficAfterHealthCheck?.lastHealthSuccessAt, cooldownProbeAt, '定时健康检查使用记录不得重复刷新真实流量成功信号')

  const failedTrafficAccount = createActiveAccount(repositories, group.id, '健康检测真实失败账号', 'sk-health-failed-traffic')
  database.prepare(`
    UPDATE accounts
    SET last_health_success_at = ?,
        next_health_check_at = ?
    WHERE id = ?
  `).run(oldCheckAt, oldCheckAt, failedTrafficAccount.id)
  const failedTrafficBeforeUsage = repositories.findAccountSummary(failedTrafficAccount.id, access)
  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin',
    traceId: 'trace-health-check-failed-traffic',
    trafficSource: 'gateway',
    accountId: failedTrafficAccount.id,
    groupId: group.id,
    providerCode: 'gpt',
    endpoint: '/v1/responses',
    model: 'gpt-test',
    stream: false,
    success: false,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '模拟真实失败请求',
    durationMs: 10
  }])
  const failedTrafficAfterUsage = repositories.findAccountSummary(failedTrafficAccount.id, access)
  assert.equal(
    failedTrafficAfterUsage?.lastHealthSuccessAt,
    failedTrafficBeforeUsage?.lastHealthSuccessAt,
    '真实失败请求不应刷新健康成功信号'
  )
  const dueAfterFailedTraffic = repositories.listAccountsDueForHealthCheck({ limit: 10, ...healthSettings })
  assert.ok(dueAfterFailedTraffic.some((account) => account.id === failedTrafficAccount.id), '最近只有失败请求的账号仍应进入健康检测候选')

  console.log('account-health-check-regression passed')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}

function createActiveAccount(
  repositories: typeof import('../../storage/repositories.js'),
  groupId: string,
  name: string,
  apiKey: string
) {
  const created = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: 'https://api.openai.com/v1'
    },
    groupId,
    supportedModels: ['gpt-5.5'],
    status: 'active'
  }, access)
  assert.equal(created.status, 'pending_test', '新账户应先进入待检查状态')
  assert.equal(repositories.recordAccountHealthCheckSuccess(created.id, {
    ...healthSettings,
    statusCode: 200
  }), true, '后台激活检查成功应更新账户')
  const activated = repositories.findAccountSummary(created.id, access)
  assert.equal(activated?.status, 'active', '后台激活检查成功应把待检查账户恢复为正常')
  return activated!
}
