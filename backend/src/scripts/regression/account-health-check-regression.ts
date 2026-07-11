import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
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

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const healthSettings = {
  intervalHours: 12,
  jitterMinutes: 0,
  failureThreshold: 3
}

try {
  const repositorySource = readFileSync(resolve('src/storage/account-health-check.repository.ts'), 'utf8')
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
  assert.match(repositorySource, /SELECT config_revision, health_check_failure_count, last_health_success_at[\s\S]+FOR UPDATE/, 'PostgreSQL 健康失败计数必须锁定账户行后递增')
  assert.match(repositorySource, /expectedConfigRevision[\s\S]+config_revision = \?/, '健康检查结果写入必须绑定账户配置版本')
  assert.doesNotMatch(postgresSuccessSource, /\(\? IS NULL OR/, 'PostgreSQL 健康成功写回不能使用无法推断参数类型的 NULL 守卫')
  assert.doesNotMatch(postgresFailureSource, /\(\? IS NULL OR/, 'PostgreSQL 健康失败写回不能使用无法推断参数类型的 NULL 守卫')
  assert.match(repositorySource, /CASE WHEN accounts\.status = 'pending_test' THEN 0 ELSE 1 END/, '周期兜底应优先处理待检查账户')
  assert.match(serviceSource, /queuedConfigRevision[\s\S]+currentConfigRevision/, '健康检查队列执行前必须丢弃旧配置版本任务')
  assert.match(serviceSource, /healthCheckGuard/, '达到阈值后的保护状态写入必须携带健康失败快照')
  assert.match(serviceSource, /errorLogFields\(event\.error/, '健康检查队列耗尽日志必须保留真实异常')

  const database = databaseModule.getBusinessDatabase()
  const accountColumns = database.prepare('PRAGMA table_info(accounts)').all() as unknown as Array<{ name: string }>
  for (const column of [
    'last_health_check_at',
    'next_health_check_at',
    'last_health_success_at',
    'health_check_failure_count',
    'last_health_check_status_code',
    'last_health_check_error_code',
    'last_health_check_error_message'
  ]) {
    assert.ok(accountColumns.some((row) => row.name === column), `accounts 应包含 ${column} 字段`)
  }
  assert.equal(accountTestFailureEligibleForAccount({
    statusCode: 404,
    errorCode: 'model_not_found',
    message: 'The model does not exist'
  }), false, '检查模型不存在不应判定为整个账户故障')
  assert.equal(accountTestFailureEligibleForAccount({
    statusCode: 400,
    errorCode: 'invalid_request_error',
    message: 'Unsupported model'
  }), false, '模型或请求配置错误不应判定为整个账户故障')
  assert.equal(accountTestFailureEligibleForAccount({
    statusCode: 401,
    errorCode: 'invalid_api_key',
    message: 'Invalid API key'
  }), true, '凭据失败应判定为账户故障')

  const group = repositories.createGroup({
    name: '账号健康检测回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const dueAccount = createActiveAccount(repositories, group.id, '健康检测到期账号', 'sk-health-due')
  const recentAccount = createActiveAccount(repositories, group.id, '健康检测近期成功账号', 'sk-health-recent')
  const disabledAccount = createActiveAccount(repositories, group.id, '健康检测停用账号', 'sk-health-disabled')
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

  const due = repositories.listAccountsDueForHealthCheck({ limit: 10, ...healthSettings })
  assert.deepEqual(due.map((account) => account.id), [dueAccount.id], '候选查询只应返回到期且缺少近期成功信号的正常账号')

  const recentAfterSkip = repositories.findAccountSummary(recentAccount.id, access)
  assert.equal(recentAfterSkip?.healthCheckFailureCount, 0, '近期成功信号应清理健康检测失败计数')
  assert.equal(recentAfterSkip?.lastHealthCheckErrorCode, undefined, '近期成功信号应清理健康检测错误码')
  assert.ok(recentAfterSkip?.nextHealthCheckAt, '近期成功信号应顺延下次检测时间')
  assert.ok(Date.parse(recentAfterSkip?.nextHealthCheckAt ?? '') > Date.now() + 11 * 60 * 60_000, '顺延后的下次检测应接近 12 小时后')

  const successAt = new Date().toISOString()
  repositories.recordAccountHealthCheckSuccess(dueAccount.id, {
    ...healthSettings,
    checkedAt: successAt,
    statusCode: 200
  })
  const dueAfterSuccess = repositories.findAccountSummary(dueAccount.id, access)
  assert.equal(dueAfterSuccess?.lastHealthCheckAt, successAt, '健康检测成功应写入检测时间')
  assert.equal(dueAfterSuccess?.lastHealthSuccessAt, successAt, '健康检测成功应写入成功时间')
  assert.equal(dueAfterSuccess?.healthCheckFailureCount, 0, '健康检测成功应清零失败计数')
  assert.equal(dueAfterSuccess?.lastHealthCheckStatusCode, 200, '健康检测成功应记录 HTTP 状态码')

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
  assert.ok(afterConfigurationFailure?.nextHealthCheckAt, '配置错误仍应安排后台复检')

  const firstFailure = repositories.recordAccountHealthCheckFailure(dueAccount.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '模拟失败'
  })
  assert.equal(firstFailure.failureCount, 1, '第一次失败应记录连续失败 1 次')
  assert.equal(firstFailure.reachedThreshold, false, '第一次失败不应达到阈值')
  const secondFailure = repositories.recordAccountHealthCheckFailure(dueAccount.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '模拟失败'
  })
  assert.equal(secondFailure.failureCount, 2, '第二次失败应递增连续失败次数')
  const thirdFailure = repositories.recordAccountHealthCheckFailure(dueAccount.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '模拟失败'
  })
  assert.equal(thirdFailure.failureCount, 3, '第三次失败应递增到阈值')
  assert.equal(thirdFailure.reachedThreshold, true, '第三次失败应达到阈值')
  const dueAfterFailure = repositories.findAccountSummary(dueAccount.id, access)
  assert.equal(dueAfterFailure?.lastHealthCheckStatusCode, 401, '失败应记录最近 HTTP 状态码')
  assert.equal(dueAfterFailure?.lastHealthCheckErrorCode, 'invalid_api_key', '失败应记录错误码')
  assert.match(dueAfterFailure?.lastHealthCheckErrorMessage ?? '', /模拟失败/, '失败应记录错误摘要')
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
