import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'
import http from 'node:http'

import cors from 'cors'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX,
  SYSTEM_QUOTA_EXPLICIT_RESET_COOLDOWN_CODE,
  SYSTEM_QUOTA_GENERIC_COOLDOWN_CODE
} from '../../domain/account-runtime-provenance.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { ok } from '../../shared/http.js'
import { logger } from '../../shared/logger.js'
import { submitAccountTestAndWait } from '../shared/account-test-task-client.js'
import { installWorkerParentIpcHarness } from '../shared/worker-parent-ipc-harness.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-disabled-account-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'disabled-account-guard.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'disabled-account-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const restoreWorkerParentIpc = installWorkerParentIpcHarness()

const [
  { accountsRouter },
  { authRouter },
  { captchaAnswerForTest },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  { testOpenAIAccount },
  { applyAccountErrorHandling, readGatewaySettings },
  { handleFailedUpstreamResponse },
  accountSideEffects,
  { handleDbServiceOperation },
  { closeSqliteReadWorkerPool }
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.routes.js'),
  import('../../modules/auth/captcha.service.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/gateway/policy/account-error-policy.service.js'),
  import('../../modules/gateway/response/failure-dispatch.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: true }))
app.use(express.json({ limit: '2mb' }))
app.use('/__aisys__/api/auth', authRouter)
app.use('/__aisys__/api/settings/public', (_req, res) => {
  res.json(ok(repositories.listPublicGlobalSettings()))
})
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)

interface ApiEnvelope<T> {
  data: T
}

interface AccountSummary {
  id: string
  name: string
  type: string
  status: string
  schedulable: boolean
  lastErrorCode?: string
}

interface AccountTestResult {
  success: boolean
  message: string
}

const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  try {
    appServer = app.listen(0, '127.0.0.1')
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`
    const adminCookie = await login(baseUrl)

    const access = adminAccess
    repositories.updateSettings({
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    })
    runtimeConfig.accountHealthJobs.inputDirectory = join(tempRoot, 'account-health-jobs-input')
    runtimeConfig.accountHealthJobs.inputSigningKey = 'disabled-account-guard-j1-signing-key'
    const group = repositories.createGroup({
      name: '停用账户状态保护回归分组',
      providerCode: 'gpt'
    }, access)
    const account = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      supportedModels: ['gpt-4o-mini'],
      name: '停用账户状态保护回归',
      type: 'api_key',
      credentials: {
        api_key: 'sk-disabled-account-guard',
        base_url: 'http://127.0.0.1:9/v1'
      },
      status: 'active',
      schedulable: true,
      superPriorityEnabled: true,
      groupId: group.id
    }, access)
    assert(account.boundGroupId === group.id, '测试账户未绑定分组')
    activateAccount(account.id)

    const staleGatewayAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, 'sys_admin', { ignoreAvailability: true })
    assert(staleGatewayAccount?.status === 'active', '停用前应能读取到完整网关账号对象')
    const disabled = repositories.updateAccount(account.id, { status: 'disabled' }, access)
    assert(disabled?.status === 'disabled' && disabled.schedulable === true, '测试账户停用失败或调度意愿被错误清理')
    assertAccountDispatchFlags(account.id, true, false, '停用账户应保留用户设置的超级优先')

    const apiTestResult = await submitAccountTestAndWait<AccountTestResult>({
      baseUrl,
      path: `/__aisys__/api/accounts/${account.id}/test`,
      cookie: adminCookie,
      body: { model: 'gpt-4o-mini', prompt: 'hi' }
    })
    assert(apiTestResult.success === false, '停用账户测试接口应返回测试失败结果')
    assert(!apiTestResult.message.includes('账户已停用'), `停用账户测试不应被停用状态短路：${apiTestResult.message}`)
    assertAccountStatus(account.id, 'disabled', true, '测试接口不应改变停用状态或调度意愿')
    assertAccountDispatchFlags(account.id, true, false, '测试接口不应清理停用账户调度标记')

    const latestDisabled = repositories.findAccountForTest(account.id, access)
    assert(latestDisabled, '停用测试账户不存在')
    const serviceTest = await testOpenAIAccount(latestDisabled, { groupId: group.id })
    assert(serviceTest.success === false, '测试服务应允许停用账户进入真实测试并返回失败结果')
    assert(!serviceTest.message.includes('账户已停用'), `测试服务不应被停用状态短路：${serviceTest.message}`)
    assertAccountStatus(account.id, 'disabled', true, '测试服务不应改变停用状态或调度意愿')

    const cooldownResult = repositories.markAccountTemporaryUnavailable(account.id, '模拟冷却')
    assert(cooldownResult === undefined, '停用账户不应被标记为冷却')
    assertAccountStatus(account.id, 'disabled', true, '冷却写回不应改变停用状态或调度意愿')

    const disabledByFailure = repositories.markAccountDisabledByFailure(account.id, '模拟错误停用')
    assert(disabledByFailure === undefined, '停用账户不应被错误策略改为 error')
    assertAccountStatus(account.id, 'disabled', true, '错误停用写回不应改变停用状态或调度意愿')

    const clearResult = repositories.clearAccountFailureState(account.id, access)
    assert(clearResult?.status === 'disabled', '清理失败态不应恢复停用账户')
    assertAccountStatus(account.id, 'disabled', true, '清理失败态不应改变停用状态或调度意愿')
    assertAccountDispatchFlags(account.id, true, false, '清理失败态不应清理停用账户调度标记')
    const disabledFlagCleared = repositories.updateAccount(account.id, { superPriorityEnabled: false }, access)
    assert(disabledFlagCleared?.superPriorityEnabled === false, '停用账户应允许用户手动取消超级优先')

    const unschedulableBeforeDisabled = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      supportedModels: ['gpt-4o-mini'],
      name: '停用保留不可调度意愿',
      type: 'api_key',
      credentials: {
        api_key: 'sk-disabled-preserve-unschedulable',
        base_url: 'http://127.0.0.1:9/v1'
      },
      status: 'active',
      schedulable: false,
      groupId: group.id
    }, access)
    const disabledUnschedulable = repositories.updateAccount(unschedulableBeforeDisabled.id, { status: 'disabled' }, access)
    assert(
      disabledUnschedulable?.status === 'disabled' && disabledUnschedulable.schedulable === false,
      '停用账户不应把用户原本关闭的调度意愿重新打开'
    )

    const errorHandlingResult = applyAccountErrorHandling({
      id: account.id,
      providerCode: 'gpt',
      type: 'api_key',
      status: 'disabled',
      credentials: {}
    }, {
      success: false,
      statusCode: 503,
      bodyText: 'upstream failed'
    })
    assert(errorHandlingResult.changed === false && errorHandlingResult.accountStatus === 'disabled', '错误处理不应改变停用账户')
    assertAccountStatus(account.id, 'disabled', true, '错误处理写回不应改变停用状态或调度意愿')

    const quotaAccount = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      supportedModels: ['gpt-4o-mini'],
      name: '系统额度规则失败分发回归',
      type: 'api_key',
      credentials: {
        api_key: 'sk-system-quota-failure-dispatch',
        base_url: 'http://127.0.0.1:9/v1'
      },
      status: 'active',
      schedulable: true,
      groupId: group.id
    }, access)
    activateAccount(quotaAccount.id)
    const quotaGatewayAccount = repositories.findOpenAIAccountForGroup(group.id, quotaAccount.id, 'sys_admin', { ignoreAvailability: true })
    assert(quotaGatewayAccount?.status === 'active', '系统额度规则回归账户应为可用网关账户')
    const quotaRequest = {
      method: 'POST',
      path: '/chat/completions',
      originalUrl: '/v1/chat/completions',
      headers: {},
      body: { model: 'gpt-4o-mini', messages: [{ role: 'user', content: '额度回归' }] },
      header: () => undefined
    }
    const quotaAuditMetadata: Array<{ label?: string, metadata?: Record<string, unknown> }> = []
    const quotaInputEpochBefore = Number((databaseModule.getBusinessDatabase().prepare(`
      SELECT count(*) AS count
      FROM account_health_jobs_input_outbox
      WHERE account_id = ?
    `).get(quotaAccount.id) as { count: number }).count)
    const quotaDispatchResult = await handleFailedUpstreamResponse({
      req: quotaRequest,
      requestLane: 'text',
      usageContext: {
        traceId: 'system-quota-failure-dispatch-trace',
        trafficSource: 'gateway',
        systemAccountId: 'sys_admin',
        apiKeyId: 'system-quota-failure-dispatch-key',
        groupId: group.id,
        endpoint: '/v1/chat/completions',
        requestSnapshot: {}
      },
      auditCapture: {
        completeAttempt() {},
        addGatewayMetadata(entry: { label?: string, metadata?: Record<string, unknown> }) {
          quotaAuditMetadata.push(entry)
        }
      },
      auditAttemptId: 'system-quota-failure-dispatch-attempt',
      account: quotaGatewayAccount,
      upstreamUrl: 'http://127.0.0.1:9/v1/chat/completions',
      response: {
        status: 403,
        ok: false,
        headers: new Headers({ 'content-type': 'application/json' }),
        body: (async function * (): AsyncGenerator<Uint8Array> {
          yield Buffer.from('{"error":{"code":"insufficient_user_quota","message":"余额不足"}}')
        })()
      },
      settings: readGatewaySettings(),
      attemptStartedAt: Date.now() - 5,
      attemptIndex: 0,
      auditAttemptIndex: 0,
      signal: new AbortController().signal,
      accountStateMutationEnabled: true,
      automaticAccountStateMutationEnabled: false
    } as unknown as Parameters<typeof handleFailedUpstreamResponse>[0])
    assert(quotaDispatchResult.action === 'skip_account' && quotaDispatchResult.failureKind === 'explicit_policy', '系统额度规则必须走显式策略切号分支')
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assertAccountStatus(quotaAccount.id, 'rate_limited', true, '系统额度规则必须将账户持久化为限流中且可调度')
    const quotaInputEpochAfter = Number((databaseModule.getBusinessDatabase().prepare(`
      SELECT count(*) AS count
      FROM account_health_jobs_input_outbox
      WHERE account_id = ?
    `).get(quotaAccount.id) as { count: number }).count)
    assert(quotaInputEpochAfter > quotaInputEpochBefore, '运行态冷却必须为账户发布新的 J1 input epoch')
    assert(
      quotaAuditMetadata.some((entry) => entry.label === 'account_error_policy_matched' && entry.metadata?.ruleSource === 'system' && entry.metadata?.ruleId === 'system.upstream_insufficient_quota'),
      '系统额度规则必须写入来源和规则 ID 审计元数据'
    )
    assert(
      !Reflect.ownKeys(quotaRequest).some((key) => typeof key === 'symbol' && String(key).includes('requestFailureHealthCheckDispatched')),
      '系统额度规则命中后不得派发低上下文 request_failure 健康探针'
    )
    const explicitQuotaBoundary = new Date(Date.now() + 90 * 60_000).toISOString()
    const explicitQuotaWrite = repositories.markAccountCooldown(
      quotaAccount.id,
      explicitQuotaBoundary,
      '模拟供应商显式 reset',
      'rate_limited',
      undefined,
      undefined,
      undefined,
      undefined,
      SYSTEM_QUOTA_EXPLICIT_RESET_COOLDOWN_CODE
    )
    assert(explicitQuotaWrite?.cooldownUntil === explicitQuotaBoundary, '单 Key 账户应接受供应商显式 reset 边界')
    const staleGenericQuotaWrite = repositories.markAccountCooldown(
      quotaAccount.id,
      new Date(Date.now() + 60 * 60_000).toISOString(),
      '模拟迟到通用额度结果',
      'rate_limited',
      undefined,
      undefined,
      undefined,
      undefined,
      SYSTEM_QUOTA_GENERIC_COOLDOWN_CODE
    )
    assert(staleGenericQuotaWrite === undefined, '单 Key 账户通用额度结果不得覆盖未来显式 reset')
    const preservedQuotaAccount = repositories.listAccounts(adminAccess).find((item: AccountSummary) => item.id === quotaAccount.id)
    assert(preservedQuotaAccount?.cooldownUntil === explicitQuotaBoundary, '单 Key 账户显式 reset 边界必须保持不变')
    databaseModule.getBusinessDatabase()
      .prepare('UPDATE accounts SET last_error_code = NULL, last_error_message = ?, cooldown_until = ? WHERE id = ?')
      .run(`${LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX}旧显式 reset`, explicitQuotaBoundary, quotaAccount.id)
    const legacyGenericQuotaWrite = repositories.markAccountCooldown(
      quotaAccount.id,
      new Date(Date.now() + 60 * 60_000).toISOString(),
      '模拟迟到通用额度结果（旧显式 provenance）',
      'rate_limited',
      undefined,
      undefined,
      undefined,
      undefined,
      SYSTEM_QUOTA_GENERIC_COOLDOWN_CODE
    )
    assert(legacyGenericQuotaWrite === undefined, '单 Key 账户通用额度结果不得覆盖旧格式显式 reset')
    const preservedLegacyQuotaAccount = repositories.listAccounts(adminAccess).find((item: AccountSummary) => item.id === quotaAccount.id)
    assert(preservedLegacyQuotaAccount?.cooldownUntil === explicitQuotaBoundary, '旧格式显式 reset 边界必须保持不变')

    const opaque403Account = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      supportedModels: ['gpt-4o-mini'],
      name: '裸 403 探针分发回归',
      type: 'api_key',
      credentials: {
        api_key: 'sk-opaque-403-failure-dispatch',
        base_url: 'http://127.0.0.1:9/v1'
      },
      status: 'active',
      schedulable: true,
      groupId: group.id
    }, access)
    activateAccount(opaque403Account.id)
    const opaque403GatewayAccount = repositories.findOpenAIAccountForGroup(group.id, opaque403Account.id, 'sys_admin', { ignoreAvailability: true })
    assert(opaque403GatewayAccount?.status === 'active', '裸 403 回归账户应为可用网关账户')
    const opaque403Request = {
      method: 'POST',
      path: '/chat/completions',
      originalUrl: '/v1/chat/completions',
      headers: {},
      body: { model: 'gpt-4o-mini', messages: [{ role: 'user', content: 'opaque 403' }] },
      header: () => undefined
    }
    const opaque403DispatchResult = await handleFailedUpstreamResponse({
      req: opaque403Request,
      requestLane: 'text',
      usageContext: {
        traceId: 'opaque-403-failure-dispatch-trace',
        trafficSource: 'gateway',
        systemAccountId: 'sys_admin',
        apiKeyId: 'opaque-403-failure-dispatch-key',
        groupId: group.id,
        endpoint: '/v1/chat/completions',
        requestSnapshot: {}
      },
      auditCapture: { completeAttempt() {}, addGatewayMetadata() {} },
      auditAttemptId: 'opaque-403-failure-dispatch-attempt',
      account: opaque403GatewayAccount,
      upstreamUrl: 'http://127.0.0.1:9/v1/chat/completions',
      response: {
        status: 403,
        ok: false,
        headers: new Headers({ 'content-type': 'application/json' }),
        body: (async function * (): AsyncGenerator<Uint8Array> {
          yield Buffer.from('{"error":{"message":"Forbidden"}}')
        })()
      },
      settings: readGatewaySettings(),
      attemptStartedAt: Date.now() - 5,
      attemptIndex: 0,
      auditAttemptIndex: 0,
      signal: new AbortController().signal,
      accountStateMutationEnabled: true,
      automaticAccountStateMutationEnabled: false
    } as unknown as Parameters<typeof handleFailedUpstreamResponse>[0])
    assert(opaque403DispatchResult.action === 'skip_account' && opaque403DispatchResult.failureKind === 'opaque_http', '裸 403 不得命中系统额度策略')
    assert(
      Reflect.ownKeys(opaque403Request).some((key) => typeof key === 'symbol' && String(key).includes('requestFailureHealthCheckDispatched')),
      '裸 403 仍必须派发普通 request_failure 健康探针'
    )

    const streamFailureResult = repositories.recordAccountStreamFailure({
      accountId: account.id,
      thresholdCount: 1,
      thresholdWindowMinutes: 1,
      action: 'cooldown',
      reason: '模拟流式异常'
    })
    assert(streamFailureResult.triggered === false, '停用账户不应触发流式熔断状态写回')
    assertAccountStatus(account.id, 'disabled', true, '流式熔断不应改变停用状态或调度意愿')

    const dbServiceResult = await handleDbServiceOperation({
      type: 'apply_account_error_handling',
      account: staleGatewayAccount,
      input: {
        success: true,
        bodyText: ''
      }
    })
    assert(dbServiceResult.changed === false, 'DB service 成功回写不应恢复停用账户')
    assertAccountStatus(account.id, 'disabled', true, 'DB service 成功回写不应改变停用状态或调度意愿')

    const staleFailureResult = await handleDbServiceOperation({
      type: 'apply_account_error_handling',
      account: staleGatewayAccount,
      input: {
        success: false,
        statusCode: 503,
        bodyText: 'late upstream failure'
      }
    })
    assert(staleFailureResult.changed === false, 'DB service 失败回写不应把已停用账户改成临时不可调用')
    assertAccountStatus(account.id, 'disabled', true, 'DB service 失败回写不应改变停用状态或调度意愿')

    const errorAccount = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      supportedModels: ['gpt-4o-mini'],
      name: '异常账户测试成功不自动恢复',
      type: 'api_key',
      credentials: {
        api_key: 'sk-error-account-guard',
        base_url: 'http://127.0.0.1:9/v1'
      },
      status: 'active',
      schedulable: true,
      fallbackEnabled: true,
      groupId: group.id
    }, access)
    assert(errorAccount.boundGroupId === group.id, '异常测试账户未绑定分组')
    activateAccount(errorAccount.id)
    const staleActiveErrorGatewayAccount = repositories.findOpenAIAccountForGroup(group.id, errorAccount.id, 'sys_admin', { ignoreAvailability: true })
    assert(staleActiveErrorGatewayAccount?.status === 'active', '异常前应能读取到完整网关账号对象')
    const markedError = repositories.markAccountException(errorAccount.id, 'oauth_token_refresh_failed', '模拟异常')
    assert(markedError?.status === 'error' && markedError.schedulable === false, '异常测试账户标记失败')
    assertAccountDispatchFlags(errorAccount.id, false, true, '异常账户应保留用户设置的降级备用')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '异常账户应保留初始异常类型')
    const staleErrorGatewayAccount = repositories.findOpenAIAccountForGroup(group.id, errorAccount.id, 'sys_admin', { ignoreAvailability: true })
    assert(staleErrorGatewayAccount?.status === 'error', '异常账户应能被测试链路按指定账号读取')
    const successOnErrorResult = await handleDbServiceOperation({
      type: 'apply_account_error_handling',
      account: staleErrorGatewayAccount,
      input: {
        success: true,
        bodyText: ''
      }
    })
    assert(successOnErrorResult.changed === false, '异常账户测试成功不应自动恢复，请使用异常恢复')
    assertAccountStatus(errorAccount.id, 'error', false, '成功回写不应自动恢复异常账户')
    assertAccountDispatchFlags(errorAccount.id, false, true, '成功回写不应清理异常账户调度标记')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '成功回写不应清理异常类型')

    const errorRaceAccount = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      supportedModels: ['gpt-4o-mini'],
      name: '异常竞态成功回写不自动恢复',
      type: 'api_key',
      credentials: {
        api_key: 'sk-error-race-account-guard',
        base_url: 'http://127.0.0.1:9/v1'
      },
      status: 'active',
      schedulable: true,
      groupId: group.id
    }, access)
    assert(errorRaceAccount.boundGroupId === group.id, '异常竞态测试账户未绑定分组')
    activateAccount(errorRaceAccount.id)
    repositories.markAccountTemporaryUnavailable(errorRaceAccount.id, '模拟冷却状态')
    const staleCooldownGatewayAccount = repositories.findOpenAIAccountForGroup(group.id, errorRaceAccount.id, 'sys_admin', { ignoreAvailability: true })
    assert(staleCooldownGatewayAccount?.status === 'temporary_unavailable', '异常竞态前应能读取到过期冷却网关账号对象')
    repositories.markAccountException(errorRaceAccount.id, 'oauth_token_refresh_failed', '模拟复测期间进入异常')
    const staleSuccessAfterErrorResult = await handleDbServiceOperation({
      type: 'apply_account_error_handling',
      account: staleCooldownGatewayAccount,
      input: {
        success: true,
        bodyText: ''
      }
    })
    assert(staleSuccessAfterErrorResult.changed === false, '过期成功回写不应把复测期间进入异常的账户恢复正常')
    assertAccountStatus(errorRaceAccount.id, 'error', false, '过期成功回写不应改变复测期间进入异常的账户状态')
    assertAccountErrorCode(errorRaceAccount.id, 'oauth_token_refresh_failed', '过期成功回写不应清理复测期间进入异常的账户异常类型')

    const staleFailureOnErrorResult = await handleDbServiceOperation({
      type: 'apply_account_error_handling',
      account: staleActiveErrorGatewayAccount,
      input: {
        success: false,
        statusCode: 503,
        bodyText: 'late upstream failure'
      }
    })
    assert(staleFailureOnErrorResult.changed === false, '过期网关失败回写不应把异常账户降级成临时不可调用')
    assertAccountStatus(errorAccount.id, 'error', false, '过期失败回写不应改变异常账户状态')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '过期失败回写不应覆盖异常类型')

    const errorCooldownResult = repositories.markAccountTemporaryUnavailable(errorAccount.id, '异常后模拟冷却')
    assert(errorCooldownResult === undefined, '异常账户不应被标记为冷却')
    assertAccountStatus(errorAccount.id, 'error', false, '冷却写回不应改变异常账户状态')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '冷却写回不应覆盖异常类型')

    const errorDisabledByFailure = repositories.markAccountDisabledByFailure(errorAccount.id, '异常后模拟错误覆盖')
    assert(errorDisabledByFailure === undefined, '异常账户不应被错误策略覆盖异常类型')
    assertAccountStatus(errorAccount.id, 'error', false, '错误策略不应改变异常账户状态')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '错误策略不应覆盖异常类型')

    const errorStreamFailureResult = repositories.recordAccountStreamFailure({
      accountId: errorAccount.id,
      thresholdCount: 1,
      thresholdWindowMinutes: 1,
      action: 'disable',
      reason: '异常后模拟流式异常'
    })
    assert(errorStreamFailureResult.triggered === false, '异常账户不应触发流式熔断状态写回')
    assertAccountStatus(errorAccount.id, 'error', false, '流式熔断不应改变异常账户状态')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '流式熔断不应覆盖异常类型')

    const editedError = repositories.updateAccount(errorAccount.id, { name: '异常账户编辑后仍不可调度', status: 'error', schedulable: true }, access)
    assert(editedError?.status === 'error' && editedError.schedulable === false, '编辑异常账户不应把调度标记打开')
    assert(editedError?.fallbackEnabled === true, '编辑异常账户不应清理降级备用')
    const errorFlagCleared = repositories.updateAccount(errorAccount.id, { fallbackEnabled: false }, access)
    assert(errorFlagCleared?.fallbackEnabled === false, '异常账户应允许用户手动取消降级备用')
    let statusChangeBlocked = false
    try {
      repositories.updateAccount(errorAccount.id, { status: 'temporary_unavailable' }, access)
    } catch (error) {
      statusChangeBlocked = error instanceof Error && error.message.includes('异常账户只能停用或使用异常恢复')
    }
    assert(statusChangeBlocked, '编辑异常账户不应绕过异常恢复切换到其他软状态')

    const recoveredError = repositories.clearAccountFailureState(errorAccount.id, access)
    assert(recoveredError?.status === 'pending_test', '异常恢复只能进入待检查状态')
    assert(recoveredError?.schedulable === false, '异常恢复后后台检查通过前不得参与调度')

    const createdError = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      supportedModels: ['gpt-4o-mini'],
      name: '创建时异常账户不可调度',
      type: 'api_key',
      credentials: {
        api_key: 'sk-created-error-account-guard',
        base_url: 'http://127.0.0.1:9/v1'
      },
      groupId: group.id
    }, access)
    activateAccount(createdError.id)
    const markedCreatedError = repositories.markAccountException(createdError.id, 'manual_account_error', '模拟异常账户停用')
    assert(markedCreatedError?.status === 'error' && markedCreatedError.schedulable === false, '异常账户应强制不可调度')
    const disabledError = repositories.updateAccount(createdError.id, { status: 'disabled' }, access)
    assert(disabledError?.status === 'disabled', '自有异常账户必须允许人工停用')

    const pendingDisable = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      supportedModels: ['gpt-4o-mini'],
      name: '待检查账户允许人工停用',
      type: 'api_key',
      credentials: {
        api_key: 'sk-pending-disable-account-guard',
        base_url: 'http://127.0.0.1:9/v1'
      },
      groupId: group.id
    }, access)
    assert(pendingDisable.status === 'pending_test', '待检查停用回归账户初始状态应为 pending_test')
    const disabledPending = repositories.updateAccount(pendingDisable.id, { status: 'disabled' }, access)
    assert(disabledPending?.status === 'disabled', '自有待检查账户必须允许人工停用')

    console.log('停用/异常账户状态保护回归通过：系统额度 403 分发、测试、恢复、错误处理和熔断写回均符合状态边界')
  } finally {
    await closeServer(appServer)
    await closeSqliteReadWorkerPool().catch(() => undefined)
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    restoreWorkerParentIpc()
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function assertAccountStatus(accountId: string, status: string, schedulable: boolean, message: string): void {
  const account = repositories.listAccounts(adminAccess).find((item: AccountSummary) => item.id === accountId)
  assert(account, `${message}：账户不存在`)
  assert(account.status === status, `${message}：实际状态 ${account.status}`)
  assert(account.schedulable === schedulable, `${message}：实际调度标记 ${account.schedulable}`)
}

function activateAccount(accountId: string): void {
  const activated = repositories.projectAccountHealthFixtureSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  assert(activated, `测试账户 ${accountId} 激活失败`)
}

function assertAccountDispatchFlags(accountId: string, superPriorityEnabled: boolean, fallbackEnabled: boolean, message: string): void {
  const account = repositories.listAccounts(adminAccess).find((item: AccountSummary) => item.id === accountId)
  assert(account, `${message}：账户不存在`)
  assert(account.superPriorityEnabled === superPriorityEnabled, `${message}：实际超级优先 ${account.superPriorityEnabled}`)
  assert(account.fallbackEnabled === fallbackEnabled, `${message}：实际降级备用 ${account.fallbackEnabled}`)
}

function assertAccountErrorCode(accountId: string, code: string, message: string): void {
  const account = repositories.listAccounts(adminAccess).find((item: AccountSummary) => item.id === accountId)
  assert(account, `${message}：账户不存在`)
  assert(account.lastErrorCode === code, `${message}：实际异常类型 ${account.lastErrorCode}`)
}

async function login(baseUrl: string): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string; image: string }>(baseUrl, '/__aisys__/api/auth/captcha')
  const captchaCode = captchaAnswerForTest(captcha.captchaId)
  assert(captchaCode, '测试夹具无法读取登录验证码')
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      username: 'admin',
      password: 'admin',
      captchaId: captcha.captchaId,
      captchaCode
    })
  })
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert(response.ok, `登录失败：HTTP ${response.status} ${await response.text()}`)
  assert(cookie, '登录未返回会话 Cookie')
  const passwordResponse = await fetch(`${baseUrl}/__aisys__/api/auth/change-password`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({ oldPassword: 'admin', newPassword: 'admin-regression-password' })
  })
  assert(passwordResponse.ok, `回归夹具修改初始密码失败：HTTP ${passwordResponse.status} ${await passwordResponse.text()}`)
  return cookie
}

async function getEnvelope<T>(baseUrl: string, path: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`)
  const text = await response.text()
  assert(response.ok, `${path} HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return { port: address.port }
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

main().catch((error) => {
  console.error('\n停用账户状态保护回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
