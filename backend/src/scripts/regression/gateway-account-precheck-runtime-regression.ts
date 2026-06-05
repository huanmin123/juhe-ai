import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AccountSummary } from '../../domain/types.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-precheck-runtime-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'precheck-runtime-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  gatewaySideEffects,
  accountConcurrency,
  databaseModule,
  repositories,
  { handleDbServiceOperation }
] = await Promise.all([
  import('../../modules/gateway/gateway-account-side-effects.service.js'),
  import('../../shared/account-concurrency.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js')
])

const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  testRuntimePrecheckPendingAndSuccessRecovery()
  await testPersistedAccountErrorClearsRuntimeAvailability()
  await testStalePrecheckAfterManualRestoreIsSkipped()
  await testFailedUsageDoesNotMakePrecheckStale()
  await testFreshPrecheckStillMarksTemporaryUnavailable()
  await testPrecheckWaitsForInFlightConcurrencyBeforeMarking()

  console.log('网关账号事前确认运行态与过期写回保护回归通过')
} finally {
  accountConcurrency.clearAccountConcurrency()
  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function testRuntimePrecheckPendingAndSuccessRecovery(): void {
  const account = createRuntimeAccount('precheck-runtime-account')
  for (let index = 0; index < 4; index += 1) {
    gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest(account, undefined, {
      systemAccountId: 'sys_admin',
      groupId: 'group-a',
      apiKeyId: 'key-a',
      clientIp: '10.0.0.1',
      endpoint: '/v1/responses',
      reason: '模拟网关短窗口失败'
    })
  }
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id], undefined, '未达到阈值前不应产生账号运行态避让')

  gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest(account, undefined, {
    systemAccountId: 'sys_admin',
    groupId: 'group-a',
    apiKeyId: 'key-b',
    clientIp: '10.0.0.2',
    endpoint: '/v1/responses',
    reason: '模拟网关短窗口失败'
  })

  const runtime = gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]
  assert(runtime, '达到短窗口多来源阈值后应写入账号运行态缓存')
  assert.equal(runtime.status, 'precheck_pending')
  assert.equal(runtime.failureCount, 5)
  assert.equal(runtime.distinctClientIpCount, 2)
  assert.equal(runtime.distinctApiKeyCount, 2)
  assert.equal(runtime.precheckAttemptCount, 0)
  assert(runtime.reason?.includes('等待事前确认'), '运行态原因应说明这是事前确认前的短暂状态')

  gatewaySideEffects.enqueueGatewayAccountErrorHandlingSideEffect({
    type: 'apply_account_error_handling',
    account,
    input: { success: true }
  })
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id],
    undefined,
    '真实成功回写应清理账号本地 suppression、failure storm 和待执行预检查'
  )

  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 60_000)
  assert(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id], '测试本地屏蔽应存在')
  const clearSuppressedResult = gatewaySideEffects.clearGatewayAccountRuntimeAvailability(account)
  assert.equal(clearSuppressedResult.cleared, true, '手动恢复入口应报告已清理本地运行态屏蔽')
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id],
    undefined,
    '手动恢复入口应能清理账号本地运行态屏蔽'
  )

  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 60_000, '模拟探针失败避让', 'precheck_failed')
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]?.status, 'precheck_failed', '应能构造探针失败运行态')
  const clearFailedResult = gatewaySideEffects.clearGatewayAccountRuntimeAvailability({ accountId: account.id })
  assert.equal(clearFailedResult.cleared, true, '手动恢复入口应能清理探针失败运行态')
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id], undefined, '探针失败运行态清理后不应残留')

  const authorizedAccount = createRuntimeAuthorizedAccount('precheck-runtime-authorized-account')
  for (let index = 0; index < 5; index += 1) {
    gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest(authorizedAccount, undefined, {
      systemAccountId: 'sys_grantee',
      groupId: 'group-authorized',
      apiKeyId: index < 4 ? 'key-authorized-a' : 'key-authorized-b',
      clientIp: index < 4 ? '10.0.1.1' : '10.0.1.2',
      endpoint: '/v1/responses',
      reason: '模拟授权账号短窗口失败'
    })
  }
  const authorizedRuntimeKey = `${authorizedAccount.id}:authorized:sys_grantee:group-authorized:auth-account-a`
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[authorizedRuntimeKey]?.status, 'precheck_pending', '授权账号运行态应按本地绑定维度隔离')
  const clearAuthorizedResult = gatewaySideEffects.clearGatewayAccountRuntimeAvailability({
    accountId: authorizedAccount.id,
    authorizedBinding: {
      systemAccountId: 'sys_grantee',
      groupId: 'group-authorized',
      accountAuthorizationId: 'auth-account-a'
    }
  })
  assert.equal(clearAuthorizedResult.cleared, true, '手动恢复入口应能清理授权账号绑定维度运行态')
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[authorizedRuntimeKey], undefined, '授权账号绑定维度运行态清理后不应残留')
}

async function testPersistedAccountErrorClearsRuntimeAvailability(): Promise<void> {
  const { account, gatewayAccount } = createGatewayAccount('落库错误清理运行态', {
    error_handling_rules: [{
      enabled: true,
      name: '测试 529 冷却',
      priority: 1,
      status_codes: [529],
      action: 'temp_unschedulable',
      durationMinutes: 1
    }]
  })
  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 60_000, '写库前临时避让')
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]?.status, 'local_suppressed', '写库前应允许运行态短避让')

  gatewaySideEffects.enqueueGatewayAccountErrorHandlingSideEffect({
    type: 'apply_account_error_handling',
    account: gatewayAccount,
    input: {
      success: false,
      statusCode: 529,
      bodyText: '{"error":{"code":"overloaded","message":"模拟 529 失败"}}',
      trafficSource: 'manual_account_test'
    }
  })
  await withDbServiceRole(() => gatewaySideEffects.flushGatewayAccountSideEffectsForTest())

  const latest = repositories.findAccountSummary(account.id, adminAccess)
  assert.equal(latest?.status, 'temporary_unavailable', '错误策略落库后账号应进入临时不可调用')
  assert.match(latest?.lastErrorMessage ?? '', /模拟 529 失败/, '落库后的最后错误应保留策略命中的真实摘要')
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id],
    undefined,
    '错误已经落库后不应再保留同一账号运行态错误，避免前端出现双来源原因'
  )
}

async function testStalePrecheckAfterManualRestoreIsSkipped(): Promise<void> {
  const { account, group, gatewayAccount } = createGatewayAccount('预检查过期写回手动恢复')
  await delay(5)
  const precheckStartedAt = new Date().toISOString()
  await delay(5)
  const cooled = repositories.markAccountCooldown(account.id, new Date(Date.now() + 60_000).toISOString(), '模拟较早预检查先写入冷却')
  assert.equal(cooled?.status, 'temporary_unavailable', '测试账号应先被写入临时不可调用')
  const restored = repositories.clearAccountFailureState(account.id, adminAccess)
  assert.equal(restored?.status, 'active', '测试账号应已手动恢复正常')

  const result = await handleDbServiceOperation({
    type: 'mark_account_precheck_temporary_unavailable',
    account: gatewayAccount,
    reason: '较早预检查失败不应覆盖手动恢复',
    precheckStartedAt
  })
  assert.equal(result.updated, false, '手动恢复后的过期预检查写回不应再次改状态')
  assert.equal(result.skippedReason, 'stale_account_updated', '过期预检查应被识别为账号状态已更新')
  assertActiveAccount(account.id, '手动恢复后的过期预检查不应把账号改回临时不可调用')
  assert.equal(group.providerCode, 'openai', '测试分组应为 OpenAI 分组')
}

async function testFailedUsageDoesNotMakePrecheckStale(): Promise<void> {
  const { account, group, gatewayAccount } = createGatewayAccount('预检查失败用量不算恢复')
  await delay(5)
  const precheckStartedAt = new Date().toISOString()
  await delay(5)
  repositories.createUsageRecordsBatch([{
    traceId: 'precheck-failed-usage',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    groupId: group.id,
    accountId: account.id,
    endpoint: '/v1/responses',
    providerCode: 'openai',
    model: 'gpt-5.5',
    stream: false,
    statusCode: 502,
    success: false,
    durationMs: 10,
    createdAt: new Date().toISOString()
  }])
  const afterFailedUsage = repositories.findAccountSummary(account.id, adminAccess)
  assert(afterFailedUsage?.lastUsedAt && afterFailedUsage.lastUsedAt > precheckStartedAt, '失败使用记录应刷新 lastUsedAt 以覆盖误判风险')

  const result = await handleDbServiceOperation({
    type: 'mark_account_precheck_temporary_unavailable',
    account: gatewayAccount,
    reason: '失败使用记录不能伪装成恢复；HTTP 403；insufficient_quota；余额和订阅额度均不足',
    precheckStartedAt
  })
  assert.equal(result.updated, true, '仅有失败使用记录时，预检查仍应能写入临时不可调用')
  const afterPrecheck = repositories.findAccountSummary(account.id, adminAccess)
  assert.equal(afterPrecheck?.status, 'temporary_unavailable', '失败使用记录不应阻止预检查降级')
  assert.match(afterPrecheck?.lastErrorMessage ?? '', /HTTP 403；insufficient_quota；余额和订阅额度均不足/, '预检查写库应保留探针传入的真实上游错误摘要')
}

async function testFreshPrecheckStillMarksTemporaryUnavailable(): Promise<void> {
  const { account, gatewayAccount } = createGatewayAccount('预检查正常写回')
  await delay(5)
  const precheckStartedAt = new Date().toISOString()

  const result = await handleDbServiceOperation({
    type: 'mark_account_precheck_temporary_unavailable',
    account: gatewayAccount,
    reason: '模拟当前预检查失败；HTTP 403；insufficient_quota；余额和订阅额度均不足',
    precheckStartedAt
  })
  assert.equal(result.updated, true, '没有更新状态介入时，预检查仍应写入临时不可调用')
  const afterPrecheck = repositories.findAccountSummary(account.id, adminAccess)
  assert.equal(afterPrecheck?.status, 'temporary_unavailable', '当前预检查失败应能降级账号')
  assert.match(afterPrecheck?.lastErrorMessage ?? '', /HTTP 403；insufficient_quota；余额和订阅额度均不足/, '当前预检查失败应按传入真实错误摘要写入最近错误')
}

async function testPrecheckWaitsForInFlightConcurrencyBeforeMarking(): Promise<void> {
  const { account, group, gatewayAccount } = createGatewayAccount('预检查等待并发归零')
  const concurrencySlot = accountConcurrency.tryAcquireAccountConcurrency(account.id, 10)
  assert.equal(concurrencySlot.acquired, true, '测试应能先占用账号并发槽')

  await withDbServiceRole(() => gatewaySideEffects.completeGatewayAccountPrecheckForTest(gatewayAccount, undefined, {
    systemAccountId: 'sys_admin',
    groupId: group.id,
    apiKeyId: 'precheck-concurrency-key',
    clientIp: '10.20.30.40',
    endpoint: '/v1/responses',
    reason: '模拟探针失败但仍有存量请求'
  }))

  assertActiveAccount(account.id, '账号仍有在途并发时，事前确认失败不能立刻写入临时不可调用')
  const runtime = gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]
  assert.equal(runtime?.status, 'precheck_pending', '等待并发归零期间应保持事前确认运行态')
  assert.match(runtime?.reason ?? '', /等待 1 个在途请求结束/, '运行态原因应说明正在等待并发归零')

  await withDbServiceRole(async () => {
    concurrencySlot.release()
    await waitFor(() => repositories.findAccountSummary(account.id, adminAccess)?.status === 'temporary_unavailable', 1000)
  })

  const afterRelease = repositories.findAccountSummary(account.id, adminAccess)
  assert.equal(afterRelease?.status, 'temporary_unavailable', '账号并发归零后，探针失败状态应允许写入临时不可调用')
  assert.match(afterRelease?.lastErrorMessage ?? '', /模拟探针失败但仍有存量请求/, '并发归零后写库应保留探针失败原因')
}

function createGatewayAccount(name: string, credentialExtras: Record<string, unknown> = {}): {
  account: AccountSummary
  group: ReturnType<typeof repositories.createGroup>
  gatewayAccount: OpenAIAccountSecret
} {
  const group = repositories.createGroup({
    name: `${name}分组-${Math.random().toString(16).slice(2, 8)}`,
    providerCode: 'openai'
  }, adminAccess)
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: `${name}-${Math.random().toString(16).slice(2, 8)}`,
    type: 'api_key',
    groupId: group.id,
    credentials: {
      api_key: `sk-${Math.random().toString(16).slice(2)}`,
      base_url: 'https://api.openai.com/v1',
      ...credentialExtras
    },
    status: 'active',
    schedulable: true
  }, adminAccess)
  const gatewayAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, 'sys_admin', { ignoreAvailability: true })
  assert(gatewayAccount, '应能读取到测试网关账号对象')
  assert.equal(gatewayAccount.status, 'active', '测试网关账号初始应为正常状态')
  return { account, group, gatewayAccount }
}

function assertActiveAccount(accountId: string, message: string): void {
  const account = repositories.findAccountSummary(accountId, adminAccess)
  assert.equal(account?.status, 'active', `${message}：实际状态 ${account?.status}`)
  assert.equal(account?.cooldownUntil, undefined, `${message}：实际冷却时间 ${account?.cooldownUntil}`)
}

function createRuntimeAccount(id: string): OpenAIAccountSecret {
  return {
    id,
    providerCode: 'openai',
    systemAccountId: 'sys_admin',
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: '事前确认运行态账号',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 10,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedModels: ['gpt-5.5'],
    currentConcurrency: 0,
    baseUrl: 'https://api.openai.com/v1',
    apiKey: 'sk-precheck-runtime',
    streamFailureCount: 0,
    credentials: {
      api_key: 'sk-precheck-runtime',
      base_url: 'https://api.openai.com/v1'
    }
  }
}

function createRuntimeAuthorizedAccount(id: string): OpenAIAccountSecret {
  return {
    ...createRuntimeAccount(id),
    systemAccountId: 'sys_owner',
    accountOwnerSystemAccountId: 'sys_owner',
    groupOwnerSystemAccountId: 'sys_grantee',
    bindingSystemAccountId: 'sys_grantee',
    accountAccessType: 'account_authorized',
    boundGroupId: 'group-authorized',
    accountAuthorizationId: 'auth-account-a',
    name: '授权事前确认运行态账号'
  }
}

async function withDbServiceRole<T>(action: () => Promise<T>): Promise<T> {
  const previousRole = runtimeConfig.processRole
  runtimeConfig.processRole = 'db-service'
  try {
    return await action()
  } finally {
    runtimeConfig.processRole = previousRole
  }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

async function waitFor(predicate: () => boolean, timeoutMs: number): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt <= timeoutMs) {
    if (predicate()) {
      return
    }
    await delay(20)
  }
  assert.fail(`等待条件超时 ${timeoutMs}ms`)
}
