import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { GatewaySettings } from '../../modules/gateway/account-error-policy.service.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-account-runtime-side-effects-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorized-account-runtime-side-effects.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorized-account-runtime-side-effects-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  { applyAccountErrorHandling },
  { requestDbService }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/account-error-policy.service.js'),
  import('../../modules/db-service/db-service-ipc.js')
])

const gatewaySettings: GatewaySettings = {
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  streamCircuitBreakerEnabled: true,
  streamRequestTimeoutSeconds: 180,
  streamIdleTimeoutSeconds: 60,
  streamFailureThresholdCount: 1,
  streamFailureThresholdWindowMinutes: 10
}

try {
  const owner = repositories.createSystemAccount({
    username: 'runtime_side_effect_owner',
    displayName: '授权副作用所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'runtime_side_effect_grantee',
    displayName: '授权副作用被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({
    name: '授权副作用被授权分组',
    providerCode: 'openai'
  }, granteeAccess)

  const cooldownAccount = createAuthorizedAccount('授权副作用临时不可调用账户', 'sk-runtime-side-effect-cooldown', [{
    name: '授权副本 500 临时不可调用',
    enabled: true,
    status_codes: '500',
    action: 'temp_unschedulable',
    duration_minutes: 5
  }], ownerAccess, grantee.id, granteeGroup.id, granteeAccess)
  const disableAccount = createAuthorizedAccount('授权副作用异常账户', 'sk-runtime-side-effect-disable', [{
    name: '授权副本 503 标记异常',
    enabled: true,
    status_codes: '503',
    action: 'error_disabled'
  }], ownerAccess, grantee.id, granteeGroup.id, granteeAccess)
  const streamAccount = createAuthorizedAccount('授权副作用流式失败账户', 'sk-runtime-side-effect-stream', [], ownerAccess, grantee.id, granteeGroup.id, granteeAccess)

  const cooldownGatewayAccount = authorizedGatewayAccount(cooldownAccount.instanceId, granteeGroup.id, grantee.id)
  const cooldownResult = applyAccountErrorHandling(cooldownGatewayAccount, {
    success: false,
    statusCode: 500,
    bodyText: JSON.stringify({ error: { code: 'insufficient_quota', message: 'runtime side effect cooldown' } }),
    settings: gatewaySettings
  })
  assert.equal(cooldownResult.action, 'cooldown', '授权副本命中错误策略后应进入本地临时不可调用')
  assert.equal(cooldownResult.changed, true, '授权副本错误策略应写入本地绑定状态')
  assert.match(cooldownResult.reason ?? '', /insufficient_quota；runtime side effect cooldown/, '错误策略返回原因应带上真实上游错误摘要')
  assertOwnerStillActive(cooldownAccount.sourceId, ownerAccess, '错误策略临时不可调用不应修改归属人原账户')
  assertAuthorizedInstanceStatus(cooldownAccount.instanceId, granteeAccess, 'temporary_unavailable', '错误策略临时不可调用应只写入被授权实例状态')
  assertAuthorizedInstanceError(cooldownAccount.instanceId, granteeAccess, /insufficient_quota；runtime side effect cooldown/, '错误策略临时不可调用应保留真实上游错误摘要')

  const disableGatewayAccount = authorizedGatewayAccount(disableAccount.instanceId, granteeGroup.id, grantee.id)
  const disableResult = applyAccountErrorHandling(disableGatewayAccount, {
    success: false,
    statusCode: 503,
    bodyText: JSON.stringify({ error: { code: 'server_is_overloaded', message: 'runtime side effect disable' } }),
    settings: gatewaySettings
  })
  assert.equal(disableResult.action, 'disable', '授权副本命中禁用策略后应进入本地异常')
  assert.equal(disableResult.changed, true, '授权副本禁用策略应写入本地绑定状态')
  assert.match(disableResult.reason ?? '', /server_is_overloaded；runtime side effect disable/, '禁用策略返回原因应带上真实上游错误摘要')
  assertOwnerStillActive(disableAccount.sourceId, ownerAccess, '错误策略异常不应修改归属人原账户')
  assertAuthorizedInstanceStatus(disableAccount.instanceId, granteeAccess, 'error', '错误策略异常应只写入被授权实例状态')
  assertAuthorizedInstanceError(disableAccount.instanceId, granteeAccess, /server_is_overloaded；runtime side effect disable/, '错误策略异常应保留真实上游错误摘要')

  const streamGatewayAccount = authorizedGatewayAccount(streamAccount.instanceId, granteeGroup.id, grantee.id)
  const streamResult = await requestDbService({
    type: 'record_account_stream_failure',
    input: {
      accountId: streamGatewayAccount.id,
      account: streamGatewayAccount,
      thresholdCount: 1,
      thresholdWindowMinutes: 10,
      action: 'cooldown',
      cooldownMinutes: 5,
      reason: '模拟授权副本流式失败'
    }
  })
  assert.equal(streamResult.count, 1, '授权副本流式失败应累计到本地绑定窗口')
  assert.equal(streamResult.triggered, true, '授权副本流式失败达到阈值后应触发本地冷却')
  assertOwnerStillActive(streamAccount.sourceId, ownerAccess, '流式失败阈值不应修改归属人原账户')
  assertAuthorizedInstanceStatus(streamAccount.instanceId, granteeAccess, 'temporary_unavailable', '流式失败阈值应只写入被授权实例状态')

  console.log('授权账户运行时副作用隔离回归通过')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function createAuthorizedAccount(
  name: string,
  apiKey: string,
  errorHandlingRules: Array<Record<string, unknown>>,
  ownerAccess: { systemAccountId: string; role: 'user' },
  granteeId: string,
  granteeGroupId: string,
  granteeAccess: { systemAccountId: string; role: 'user' }
) {
  const account = repositories.createAccount({
    providerCode: 'openai',
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: 'https://example.invalid/v1',
      error_handling_rules: errorHandlingRules
    }
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId,
    remark: `${name} 授权副作用隔离回归`
  }, ownerAccess)
  const instance = authorizedInstanceForSource(account.id, granteeAccess)
  assert(repositories.setAccountGroup(instance.id, granteeGroupId, granteeAccess), `${name} 授权实例绑定到被授权用户分组失败`)
  return { sourceId: account.id, instanceId: instance.id }
}

function authorizedGatewayAccount(accountId: string, granteeGroupId: string, granteeId: string) {
  const account = repositories.listOpenAIAccountsForGroup(granteeGroupId, granteeId)
    .find((item) => item.id === accountId)
  assert(account, '被授权用户分组应能读取授权账户网关对象')
  assert.equal(account.accountAccessType, 'account_authorized', '网关对象应保留账户授权访问类型')
  assert.equal(account.bindingSystemAccountId, granteeId, '网关对象应保留本地绑定所属系统账户')
  assert.equal(account.boundGroupId, granteeGroupId, '网关对象应保留本地绑定分组')
  assert(account.accountAuthorizationId, '网关对象应保留最终用户授权 ID')
  return account
}

function assertOwnerStillActive(accountId: string, ownerAccess: { systemAccountId: string; role: 'user' }, message: string): void {
  const ownerView = repositories.findAccountSummary(accountId, ownerAccess)
  assert.equal(ownerView?.status, 'active', message)
  assert.equal(ownerView?.schedulable, true, `${message}：主账户调度开关也不应被关闭`)
  assert.equal(ownerView?.cooldownUntil, undefined, `${message}：主账户冷却时间不应被写入`)
  assert.equal(ownerView?.streamFailureCount ?? 0, 0, `${message}：主账户流式失败计数不应被写入`)
}

function assertAuthorizedInstanceStatus(accountId: string, granteeAccess: { systemAccountId: string; role: 'user' }, expectedStatus: string, message: string): void {
  const granteeView = repositories.findAccountSummary(accountId, granteeAccess)
  assert.equal(granteeView?.status, expectedStatus, message)
  assert.equal(granteeView?.localStatus, expectedStatus, `${message}：兼容字段应同步实例状态`)
  assert.equal(granteeView?.sourceStatus, undefined, `${message}：列表不应再暴露父账户来源状态`)
}

function assertAuthorizedInstanceError(accountId: string, granteeAccess: { systemAccountId: string; role: 'user' }, pattern: RegExp, message: string): void {
  const granteeView = repositories.findAccountSummary(accountId, granteeAccess)
  assert.match(granteeView?.lastErrorMessage ?? '', pattern, message)
}

function authorizedInstanceForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }) {
  const account = repositories.listAccounts(access)
    .find((item) => item.authorizationInstanceSourceAccountId === sourceAccountId)
  assert(account, `被授权用户视角应能读取来源账户 ${sourceAccountId} 的授权实例`)
  return account
}
