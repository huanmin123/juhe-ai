import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
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

  assert.equal(repositories.recordAccountHealthCheckFailure(pending.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: 'Invalid API key'
  }).changed, true, '后台健康检查失败应记录待检查账户的失败详情')
  const failedPending = repositories.findAccountSummary(pending.id, access)
  assert.equal(failedPending?.status, 'pending_test', '后台健康检查失败后仍应由系统自动重试')
  assert.equal(failedPending?.schedulable, false, '后台健康检查失败后不得参与调度')
  assert.equal(failedPending?.effectiveAvailability?.label, '账户检查失败', '待检查失败应显示明确状态')
  assert.equal(failedPending?.effectiveAvailability?.color, 'red', '待检查失败应使用红色状态')
  assert.match(failedPending?.effectiveAvailability?.reason ?? '', /自动重试/, '待检查失败应说明系统会自动重试')

  assert.equal(repositories.recordAccountHealthCheckSuccess(pending.id, {
    ...healthSettings,
    statusCode: 200
  }), true, '后台健康检查成功应激活待检查账户')
  const activated = repositories.findAccountSummary(pending.id, access)
  assert.equal(activated?.status, 'active', '后台健康检查成功应把待检查账户改为正常')
  assert.equal(activated?.schedulable, true, '后台健康检查成功应恢复调度')

  const changedCredentials = repositories.updateAccount(pending.id, {
    credentials: {
      api_key: 'sk-manual-failure',
      base_url: mockBaseUrl
    }
  }, access)
  assert.equal(changedCredentials?.status, 'pending_test', '关键配置变更后应重新进入待检查')
  assert.equal(changedCredentials?.schedulable, false, '关键配置变更后后台检查成功前不得调度')

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

  console.log('账户待检查回归通过：新建和关键配置变更进入 pending_test，人工测试不改状态或检查模型，只有后台健康检查成功激活')
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
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end(JSON.stringify({
        id: 'resp_pending_test_mock',
        object: 'response',
        status: 'completed',
        model: 'gpt-5.5',
        output: [],
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
