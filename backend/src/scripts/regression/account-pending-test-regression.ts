import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-pending-test-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-pending-test.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-pending-test-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  accountImport,
  accountExport,
  { testOpenAIAccount },
  { flushGatewayAccountSideEffects },
  { flushAllUsageRecordQueue, setDbServiceUsageRecordLocalWriteAllowedForTest },
  { closeSqliteReadWorkerPool }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/accounts/account-import.service.js'),
  import('../../modules/accounts/account-export.service.js'),
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const healthSettings = {
  intervalHours: 12,
  jitterMinutes: 0,
  failureThreshold: 3
}
let mockOpenAIServer: http.Server | undefined

try {
  const accountsRouteSource = readFileSync(resolve('src/modules/accounts/accounts.routes.ts'), 'utf8')
  const accountManagementPatchSource = readFileSync(resolve('src/storage/account-management-patch.repository.ts'), 'utf8')
  const openAIOAuthRouteSource = readFileSync(resolve('src/modules/openai-oauth/openai-oauth.routes.ts'), 'utf8')
  const forceActivateRouteSource = readFileSync(resolve('src/modules/accounts/account-force-activate.routes.ts'), 'utf8')
  const accountRuntimeMutationSource = readFileSync(resolve('src/storage/account-runtime-mutation.repository.ts'), 'utf8')
  assert.match(accountManagementPatchSource, /healthCheckReason: nextStatus === 'pending_test' \? 'activation' : undefined/, '重新检查和异常恢复进入 pending_test 后必须标记后台激活检查')
  assert.match(accountsRouteSource, /account\.healthCheckRequired && account\.healthCheckReason[\s\S]+dispatchAccountHealthCheck\(account\.id, account\.healthCheckReason\)/, '路由必须投递集中写入层声明的健康检查')
  setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  mockOpenAIServer = createMockOpenAIServer()
  mockOpenAIServer.listen(0, '127.0.0.1')
  await onceListening(mockOpenAIServer)
  const address = mockOpenAIServer.address()
  assert(address && typeof address !== 'string', '待检查账户 mock 上游地址不可用')
  const mockBaseUrl = `http://127.0.0.1:${address.port}`

  const owner = repositories.createSystemAccount({
    username: 'account_pending_test_owner',
    displayName: '待检查账户回归用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const group = repositories.createGroup({
    name: '待检查账户回归分组',
    providerCode: 'gpt'
  }, access)

  const pending = repositories.createAccount(accountPayload({
    name: '默认创建待检查账户',
    apiKey: 'sk-pending-default',
    groupId: group.id,
    baseUrl: mockBaseUrl
  }), access)
  assert.equal(pending.status, 'pending_test', '新建账户默认应为待检查')
  assert.equal(pending.schedulable, false, '待检查账户默认不得参与调度')
  assert.equal(pending.healthCheckModel, 'gpt-5.5', '新建账户必须保存属于支持模型的检查模型')
  assert.match(pending.lastErrorMessage ?? '', /等待后台健康检查/, '待检查账户应记录等待后台健康检查的提示')
  assert.equal(
    repositories.listOpenAIAccountsForGroup(group.id, owner.id).some((account) => account.id === pending.id),
    false,
    '待检查账户不应进入网关调度候选'
  )
  assert.equal(
    repositories.clearAccountFailureState(pending.id, access)?.status,
    'pending_test',
    '普通恢复入口不应激活待检查账户'
  )
  assert.match(forceActivateRouteSource, /acknowledgedAccountAvailable !== true/, '人工恢复必须要求用户明确确认账户当前可用')
  assert.match(accountsRouteSource, /const creationStatus = accountCreationStatusInput\(parsed\.data\.status\)/, '普通账户创建必须先归一化用户选择的状态')
  assert.match(accountsRouteSource, /\.\.\.creationStatus/, '普通账户创建必须由后端派生首次检查和调度标记')
  assert.match(openAIOAuthRouteSource, /status: z\.enum\(\['active', 'pending_test', 'disabled'\]\)\.optional\(\)/, 'OpenAI OAuth 创建应接受待检查状态')
  assert.equal((openAIOAuthRouteSource.match(/\.\.\.accountCreationStatusInput\(parsed\.data\.status\)/g) ?? []).length, 2, 'OpenAI OAuth 两个创建入口都必须由后端按状态派生调度标记')
  assert.match(forceActivateRouteSource, /accounts\.force_activate_pending/, '人工恢复必须写入独立操作审计键')
  assert.match(accountRuntimeMutationSource, /forceActivatePendingAccountAsync[\s\S]+client\.transaction[\s\S]+config_revision = \?/, 'PostgreSQL 人工放行必须在事务内按配置版本 CAS')
  assert.match(accountRuntimeMutationSource, /forceActivatePendingAccount[\s\S]+account_expires_at IS NULL OR account_expires_at > \?/, '人工放行写入时必须再次校验套餐未过期')

  const forcePending = repositories.createAccount(accountPayload({
    name: '用户确认后人工放行账户',
    apiKey: 'sk-pending-force-activate',
    groupId: group.id,
    baseUrl: mockBaseUrl
  }), access)
  const forceActivated = repositories.forceActivatePendingAccount(forcePending.id, access)
  assert.equal(forceActivated.changed, true, '账户所有者应能人工放行自有待检查账户')
  assert.equal(forceActivated.account?.status, 'active', '人工放行后应立即恢复正常状态')
  assert.equal(forceActivated.account?.schedulable, true, '人工放行后应立即参与调度')
  assert.equal(
    repositories.forceActivatePendingAccount(forcePending.id, access).changed,
    false,
    '人工放行必须使用 pending_test 精确状态守卫，不能重复执行'
  )
  const scheduledPending = repositories.createAccount({
    ...accountPayload({
      name: '时间计划外人工放行账户',
      apiKey: 'sk-pending-force-scheduled',
      groupId: group.id,
      baseUrl: mockBaseUrl
    }),
    availabilitySchedule: {
      enabled: true,
      timezone: 'UTC',
      mode: 'allow_windows' as const,
      dateRange: { startDate: '2999-01-01' },
      windows: [{ daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:00', end: '23:59' }]
    }
  }, access)
  const scheduledForceResult = repositories.forceActivatePendingAccount(scheduledPending.id, access)
  assert.equal(scheduledForceResult.account?.status, 'disabled', '人工放行不得绕过账户时间计划')
  assert.equal(scheduledForceResult.account?.schedulable, true, '时间计划只控制当前状态，不应覆盖用户允许参与调度的持久开关')

  const pendingCandidate = repositories.findOpenAIAccountForGroup(
    group.id,
    pending.id,
    owner.id,
    { includeUnavailable: true, ignoreAvailability: true }
  )
  assert(pendingCandidate, '待检查账户应可作为隔离的人工诊断候选')
  const manualSuccess = await testOpenAIAccount(pending, {
    model: 'gpt-5.5',
    testEndpointMode: 'responses_sse',
    candidateAccount: pendingCandidate
  })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(manualSuccess.success, true, `待检查账户人工测试应成功：${manualSuccess.message}`)
  const afterManualSuccess = repositories.findAccountSummary(pending.id, access)
  assert.equal(afterManualSuccess?.status, 'pending_test', '人工测试成功不能激活账户')
  assert.equal(afterManualSuccess?.schedulable, false, '人工测试成功不能恢复账户调度')
  assert.equal(afterManualSuccess?.healthCheckModel, 'gpt-5.5', '人工测试成功不能改写检查模型')

  const firstPendingFailure = repositories.recordAccountHealthCheckFailure(pending.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: 'Invalid API key'
  })
  assert.equal(firstPendingFailure.changed, true, '后台健康检查失败应记录待检查账户的失败详情')
  assert.equal(firstPendingFailure.transitionedToError, false, '首次失败不应立即把待检查账户转为异常')
  assert.equal(
    Date.parse(firstPendingFailure.nextHealthCheckAt ?? '') - Date.parse(firstPendingFailure.checkedAt),
    60 * 60_000,
    '待检查账户失败后必须固定 1 小时复检'
  )
  const failedPending = repositories.findAccountSummary(pending.id, access)
  assert.equal(failedPending?.status, 'pending_test', '后台健康检查失败后仍应由系统自动重试')
  assert.equal(failedPending?.schedulable, false, '后台健康检查失败后不得参与调度')
  assert.equal(failedPending?.effectiveAvailability?.label, '账户检查失败', '待检查失败应显示明确状态')
  assert.equal(failedPending?.effectiveAvailability?.color, 'red', '待检查失败应使用红色状态')
  assert.match(failedPending?.effectiveAvailability?.reason ?? '', /自动重试/, '待检查失败应说明系统会自动重试')

  const pendingBeforeRestart = repositories.findAccountSummary(pending.id, access)
  assert(pendingBeforeRestart, '重新检查前应能读取待检查账户')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET last_error_trace_id = ?, last_health_check_trace_id = ? WHERE id = ?')
    .run('stale-error-trace', 'stale-health-trace', pending.id)
  const restartedPending = repositories.clearAccountFailureState(pending.id, access, { allowPendingTestRestore: true })
  assert.equal(restartedPending?.status, 'pending_test', '重新检查必须保持待检查状态')
  assert.equal(restartedPending?.schedulable, false, '重新检查后必须保持不可调度')
  assert.equal(restartedPending?.lastHealthCheckAt, undefined, '重新检查必须清空上次健康检查时间')
  assert.equal(restartedPending?.healthCheckFailureCount, 0, '重新检查必须清空健康检查失败计数')
  assert.equal(restartedPending?.healthCheckFailureStartedAt, undefined, '重新检查必须清空首次失败窗口')
  assert.equal(restartedPending?.lastHealthCheckErrorCode, undefined, '重新检查必须清空健康检查错误码')
  assert.equal(restartedPending?.lastErrorTraceId, undefined, '重新检查必须清空旧错误 trace')
  assert.equal(restartedPending?.lastHealthCheckTraceId, undefined, '重新检查必须清空旧健康检查 trace')
  assert((restartedPending?.configRevision ?? 0) > (pendingBeforeRestart?.configRevision ?? 0), '重新检查必须递增配置版本以拒绝在途旧探针结果')
  assert.equal(repositories.recordAccountHealthCheckSuccess(pending.id, {
    ...healthSettings,
    statusCode: 200,
    expectedConfigRevision: pendingBeforeRestart?.configRevision
  }), false, '重新检查后在途旧健康成功不得写回')

  repositories.recordAccountHealthCheckFailure(pending.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: 'Invalid API key after restart'
  })
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET health_check_failure_started_at = ? WHERE id = ?')
    .run(new Date(Date.now() - 25 * 60 * 60_000).toISOString(), pending.id)
  const timedOutPending = repositories.recordAccountHealthCheckFailure(pending.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: 'Invalid API key after 24 hours'
  })
  assert.equal(timedOutPending.transitionedToError, true, '从首次失败起满 24 小时仍失败必须转为异常')
  assert.equal(timedOutPending.nextHealthCheckAt, undefined, '转为异常后不应继续安排 pending_test 复检')
  const timedOutAccount = repositories.findAccountSummary(pending.id, access)
  assert.equal(timedOutAccount?.status, 'error', '激活检查超时必须写入 error 状态')
  assert.equal(timedOutAccount?.schedulable, false, '激活检查超时后必须不可调度')
  assert.equal(timedOutAccount?.lastErrorCode, 'account_activation_check_timeout', '激活检查超时必须写入明确错误码')
  assert.match(timedOutAccount?.lastErrorMessage ?? '', /持续 24 小时仍未通过/, '激活检查超时必须写入明确错误原因')

  const recoveredError = repositories.clearAccountFailureState(pending.id, access)
  assert.equal(recoveredError?.status, 'pending_test', '异常恢复只能进入待检查，不能直接恢复正常')
  assert.equal(recoveredError?.schedulable, false, '异常恢复后后台检查通过前不得调度')
  assert.equal(recoveredError?.lastErrorCode, undefined, '异常恢复应清空终态错误码')
  assert.equal(recoveredError?.healthCheckFailureStartedAt, undefined, '异常恢复应重置首次失败窗口')

  assert.equal(repositories.recordAccountHealthCheckSuccess(pending.id, {
    ...healthSettings,
    statusCode: 200
  }), true, '后台健康检查成功应激活待检查账户')
  const activated = repositories.findAccountSummary(pending.id, access)
  assert.equal(activated?.status, 'active', '后台健康检查成功应把待检查账户改为正常')
  assert.equal(activated?.schedulable, true, '后台健康检查成功应恢复调度')

  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET last_error_trace_id = ?,
          last_health_check_at = ?,
          last_health_success_at = ?,
          last_health_check_trace_id = ?
      WHERE id = ?
    `)
    .run('stale-config-error-trace', new Date().toISOString(), new Date().toISOString(), 'stale-config-health-trace', pending.id)
  const changedCredentials = repositories.updateAccount(pending.id, {
    credentials: {
      api_key: 'sk-manual-failure',
      base_url: mockBaseUrl
    }
  }, access)
  assert.equal(changedCredentials?.status, 'pending_test', '关键配置变更后应重新进入待检查')
  assert.equal(changedCredentials?.schedulable, false, '关键配置变更后后台检查成功前不得调度')
  assert.equal(changedCredentials?.lastErrorTraceId, undefined, '关键配置变更进入待检查时必须清理旧错误 trace')
  const changedCredentialsStored = repositories.findAccountSummary(pending.id, access)
  assert.equal(changedCredentialsStored?.lastHealthCheckAt, undefined, '关键配置变更进入待检查时必须清理旧健康检查时间')
  assert.equal(changedCredentialsStored?.lastHealthSuccessAt, undefined, '关键配置变更进入待检查时必须清理旧健康成功事实')
  assert.equal(changedCredentialsStored?.lastHealthCheckTraceId, undefined, '关键配置变更进入待检查时必须清理旧健康检查 trace')

  const failedCandidate = repositories.findOpenAIAccountForGroup(
    group.id,
    pending.id,
    owner.id,
    { includeUnavailable: true, ignoreAvailability: true }
  )
  assert(failedCandidate, '关键配置变更后的账户应可作为隔离的人工诊断候选')
  const manualFailure = await testOpenAIAccount(changedCredentials!, {
    model: 'gpt-5.5',
    testEndpointMode: 'responses_sse',
    candidateAccount: failedCandidate
  })
  await flushGatewayAccountSideEffects()
  flushAllUsageRecordQueue()
  assert.equal(manualFailure.success, false, '无效凭据的人工测试应返回失败')
  const afterManualFailure = repositories.findAccountSummary(pending.id, access)
  assert.equal(afterManualFailure?.status, 'pending_test', '人工测试失败不能改写账户状态')
  assert.equal(afterManualFailure?.schedulable, false, '人工测试失败不能改写调度状态')
  assert.equal(afterManualFailure?.healthCheckModel, 'gpt-5.5', '人工测试失败不能改写检查模型')

  assert.equal(repositories.recordAccountHealthCheckSuccess(pending.id, {
    ...healthSettings,
    statusCode: 200
  }), true, '只有后台健康检查成功才能再次激活账户')
  assert.equal(repositories.findAccountSummary(pending.id, access)?.status, 'active', '后台检查成功后账户应恢复正常')

  const requestedActive = repositories.createAccount({
    ...accountPayload({
      name: '请求正常状态的新账户',
      apiKey: 'sk-requested-active',
      groupId: group.id,
      baseUrl: mockBaseUrl
    }),
    status: 'active'
  }, access)
  assert.equal(requestedActive.status, 'pending_test', '新账户请求正常状态仍应由后台检查激活')
  assert.equal(requestedActive.schedulable, false, '新账户后台检查成功前不得调度')

  const explicitlyActivated = repositories.createAccount({
    ...accountPayload({
      name: '显式跳过检查的新账户',
      apiKey: 'sk-explicitly-active',
      groupId: group.id,
      baseUrl: mockBaseUrl
    }),
    status: 'active',
    skipInitialHealthCheck: true
  }, access)
  assert.equal(explicitlyActivated.status, 'active', '显式跳过检查的新账户应直接可调度')
  assert.equal(explicitlyActivated.schedulable, true, '显式跳过检查的新账户应参与调度')

  const importResult = accountImport.executeAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '导入 active 转待检查账户',
        providerCode: 'gpt',
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        type: 'api_key',
        status: 'active',
        groupId: group.id,
        supportedModels: ['gpt-5.5'],
        healthCheckModel: 'gpt-5.5',
        credentials: { api_key: 'sk-import-active-to-pending', base_url: mockBaseUrl }
      }
    ]
  }, {}, access)
  assert.equal(importResult.summary.accounts.create, 1, '导入回归账户应创建成功')
  const importedId = importResult.accounts[0]?.accountId
  assert(importedId, '导入结果应返回账户 ID')
  const imported = repositories.findAccountSummary(importedId, access)
  assert.equal(imported?.status, 'pending_test', '导入 active 账户应落库为待检查')
  assert.equal(imported?.schedulable, false, '导入后待检查账户不得参与调度')
  assert.equal(imported?.healthCheckModel, 'gpt-5.5', '导入应恢复账户检查模型')

  const exportResult = accountExport.exportAccountsAsImportDocument({ accountIds: [importedId] }, access)
  assert.equal(exportResult.document.accounts[0]?.status, 'pending_test', '导出应保留待检查状态')
  assert.equal(exportResult.document.accounts[0]?.healthCheckModel, 'gpt-5.5', '导出应保留账户检查模型')

  console.log('账户待检查回归通过：新建和关键配置变更进入 pending_test，人工测试保持诊断语义，后台检查或用户确认人工放行可激活')
} finally {
  setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  await closeServer(mockOpenAIServer)
  await closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function accountPayload(input: {
  name: string
  apiKey: string
  groupId: string
  baseUrl: string
}) {
  return {
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: input.name,
    type: 'api_key',
    credentials: {
      api_key: input.apiKey,
      base_url: input.baseUrl
    },
    groupId: input.groupId,
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5'
  } as const
}

function createMockOpenAIServer(): http.Server {
  return http.createServer((req, res) => {
    if (req.method !== 'POST' || req.url?.split('?', 1)[0] !== '/v1/responses') {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }
    const authorization = String(req.headers.authorization ?? '')
    req.resume()
    req.on('end', () => {
      if (authorization.includes('manual-failure')) {
        res.writeHead(401, { 'content-type': 'application/json' })
        res.end(JSON.stringify({
          error: {
            code: 'invalid_api_key',
            message: 'Invalid API key',
            type: 'invalid_request_error'
          }
        }))
        return
      }
      if (String(req.headers.accept ?? '').includes('text/event-stream')) {
        res.writeHead(200, { 'content-type': 'text/event-stream' })
        res.end([
          'event: response.created',
          'data: {"type":"response.created","response":{"id":"resp_pending_test_mock","status":"in_progress"}}',
          '',
          'event: response.output_text.delta',
          'data: {"type":"response.output_text.delta","delta":"ok"}',
          '',
          'event: response.completed',
          'data: {"type":"response.completed","response":{"id":"resp_pending_test_mock","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}',
          '',
          ''
        ].join('\n'))
        return
      }
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end(JSON.stringify({
        id: 'resp_pending_test_mock',
        object: 'response',
        status: 'completed',
        model: 'gpt-5.5',
        output: [{
          type: 'message',
          role: 'assistant',
          content: [{ type: 'output_text', text: 'ok' }]
        }],
        usage: {
          input_tokens: 1,
          output_tokens: 1,
          total_tokens: 2
        }
      }))
    })
  })
}

async function onceListening(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

async function closeServer(server?: http.Server): Promise<void> {
  if (!server) return
  await new Promise<void>((resolvePromise) => {
    server.close(() => resolvePromise())
  })
}
