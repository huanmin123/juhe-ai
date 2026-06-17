import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

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
  const database = databaseModule.getBusinessDatabase()
  const accountColumns = database.prepare('PRAGMA table_info(accounts)').all() as unknown as Array<{ name: string }>
  for (const column of [
    'health_check_enabled',
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

  const group = repositories.createGroup({
    name: '账号健康检测回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const dueAccount = createActiveAccount(repositories, group.id, '健康检测到期账号', 'sk-health-due')
  const recentAccount = createActiveAccount(repositories, group.id, '健康检测近期成功账号', 'sk-health-recent')
  const disabledAccount = createActiveAccount(repositories, group.id, '健康检测停用账号', 'sk-health-disabled')
  repositories.updateAccount(disabledAccount.id, { status: 'disabled' }, access)

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

  const failedTrafficAccount = createActiveAccount(repositories, group.id, '健康检测真实失败账号', 'sk-health-failed-traffic')
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
  assert.equal(failedTrafficAfterUsage?.lastHealthSuccessAt, undefined, '真实失败请求不应刷新健康成功信号')
  const dueAfterFailedTraffic = repositories.listAccountsDueForHealthCheck({ limit: 10, ...healthSettings })
  assert.ok(dueAfterFailedTraffic.some((account) => account.id === failedTrafficAccount.id), '最近只有失败请求的账号仍应进入健康检测候选')

  console.log('account-health-check-regression passed')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function createActiveAccount(
  repositories: typeof import('../../storage/repositories.js'),
  groupId: string,
  name: string,
  apiKey: string
) {
  return repositories.createAccount({
    providerCode: 'gpt',
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: 'https://api.openai.com/v1'
    },
    groupId,
    status: 'active'
  }, access)
}
