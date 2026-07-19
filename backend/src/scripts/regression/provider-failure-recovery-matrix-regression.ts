import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'

type ProtocolKind = 'openai' | 'anthropic'

interface MatrixCase {
  label: string
  providerCode: string
  providerProtocolProfileId: string
  protocolKind: ProtocolKind
  upstreamPrefix: string
  model: string
  usageSemantic: 'openai' | 'anthropic'
  baseUrl: (origin: string) => string
}

interface UpstreamHit {
  providerLabel: string
  path: string
  authorization: string
  xApiKey: string
  bodyText: string
}

interface CaseRuntime {
  item: MatrixCase
  localApiKey: string
  groupId: string
  primaryAccountId: string
  rescueAccountId: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-provider-failure-recovery-matrix-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'provider-failure-recovery-matrix.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'provider-failure-recovery-matrix-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  settingsRepository,
  customProviderModelsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  sqliteReadWorkerPool
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../storage/custom-provider-models.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: UpstreamHit[] = []
const cases: MatrixCase[] = [
  {
    label: 'gpt',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    protocolKind: 'openai',
    upstreamPrefix: 'gpt',
    model: 'gpt-5.5',
    usageSemantic: 'openai',
    baseUrl: (origin) => `${origin}/gpt`
  },
  {
    label: 'openai-compatible',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    protocolKind: 'openai',
    upstreamPrefix: 'openai-compatible',
    model: 'compatible-model',
    usageSemantic: 'openai',
    baseUrl: (origin) => `${origin}/openai-compatible`
  },
  {
    label: 'deepseek',
    providerCode: DEEPSEEK_PROVIDER_CODE,
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    protocolKind: 'openai',
    upstreamPrefix: 'deepseek',
    model: 'deepseek-ai-v4-flash',
    usageSemantic: 'openai',
    baseUrl: (origin) => `${origin}/deepseek`
  },
  {
    label: 'glm',
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
    protocolKind: 'openai',
    upstreamPrefix: 'glm',
    model: 'glm-4.7-flash',
    usageSemantic: 'openai',
    baseUrl: (origin) => `${origin}/glm/v1`
  },
  {
    label: 'anthropic',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    protocolKind: 'anthropic',
    upstreamPrefix: 'anthropic',
    model: 'claude-haiku-4-5',
    usageSemantic: 'anthropic',
    baseUrl: (origin) => `${origin}/anthropic`
  }
]

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })
  gatewayCache.clearGatewayRuntimeCache()

  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createMatrixMockUpstream()
    await listen(upstreamServer)
    const upstreamOrigin = `http://127.0.0.1:${serverAddress(upstreamServer).port}`

    const runtimes = cases.map((item) => createCaseRuntime(item, upstreamOrigin))
    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    for (const runtime of runtimes) {
      await assertFailureRecoveryCase(baseUrl, runtime)
    }

    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    assertUsageRecords(runtimes)
    assertAuditLogs(runtimes)

    console.log('provider failure recovery matrix regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  await sqliteReadWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function createCaseRuntime(item: MatrixCase, upstreamOrigin: string): CaseRuntime {
  ensureMatrixCaseModelCatalogEntry(item)
  const group = repositories.createGroup({
    name: `${item.label} 异常交替矩阵分组`,
    providerCode: item.providerCode,
    enabled: true
  }, access)
  const primary = repositories.createAccount({
    providerCode: item.providerCode,
    providerProtocolProfileId: item.providerProtocolProfileId,
    name: `${item.label} 异常交替主账号`,
    type: 'api_key',
    credentials: credentialsForCase(item, upstreamOrigin, 'primary'),
    groupId: group.id,
    supportedModels: [item.model],
    healthCheckModel: item.model,
    priority: 0
  }, access)
  activateMatrixAccount(primary.id, item.label, '主账号')
  const rescue = repositories.createAccount({
    providerCode: item.providerCode,
    providerProtocolProfileId: item.providerProtocolProfileId,
    name: `${item.label} 异常交替备用账号`,
    type: 'api_key',
    credentials: credentialsForCase(item, upstreamOrigin, 'rescue'),
    groupId: group.id,
    supportedModels: [item.model],
    healthCheckModel: item.model,
    priority: 100
  }, access)
  activateMatrixAccount(rescue.id, item.label, '备用账号')
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${item.label} 异常交替矩阵 Key`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, `${item.label} 回归 API Key 未返回明文密钥`)
  return {
    item,
    localApiKey: apiKey.key,
    groupId: group.id,
    primaryAccountId: primary.id,
    rescueAccountId: rescue.id
  }
}

function activateMatrixAccount(accountId: string, label: string, lane: string): void {
  assert.equal(repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true, `${label} ${lane}后台激活检查应成功`)
  const activated = repositories.findAccountSummary(accountId, access)
  assert.equal(activated?.status, 'active', `${label} ${lane}后台激活检查后应为正常状态`)
  assert.equal(activated?.schedulable, true, `${label} ${lane}后台激活检查后应参与调度`)
}

function ensureMatrixCaseModelCatalogEntry(item: MatrixCase): void {
  customProviderModelsRepository.upsertCustomProviderModel({
    providerCode: item.providerCode,
    model: item.model,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: item.protocolKind === 'anthropic' ? ['messages'] : ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: access.systemAccountId
  })
}

function credentialsForCase(item: MatrixCase, upstreamOrigin: string, lane: 'primary' | 'rescue'): Record<string, unknown> {
  const credentials: Record<string, unknown> = {
    api_key: `sk-${item.label}-${lane}-upstream`,
    base_url: item.baseUrl(upstreamOrigin)
  }
  if (item.protocolKind === 'anthropic') {
    credentials.supported_endpoint_modes = ['messages_json', 'messages_sse']
  } else {
    credentials.supported_endpoint_modes = ['chat_json', 'chat_sse']
  }
  return credentials
}

async function assertFailureRecoveryCase(baseUrl: string, runtime: CaseRuntime): Promise<void> {
  const { item } = runtime
  const start = upstreamHits.length
  const first = await requestCase(baseUrl, runtime, 'failover')
  assert.equal(first.status, item.protocolKind === 'anthropic' ? 529 : 503, `${item.label} 通用客户端应原样收到主账号完整失败响应`)
  assert.match(first.text, new RegExp(`${item.label} primary failed`), `${item.label} 通用客户端失败正文必须保持上游语义`)
  assert.deepEqual(caseAuthorizations(start, item.label), [
    item.protocolKind === 'anthropic' ? `x-api-key sk-${item.label}-primary-upstream` : `Bearer sk-${item.label}-primary-upstream`
  ], `${item.label} 通用客户端完整失败响应不得触发语义切号`)

  const secondStart = upstreamHits.length
  const second = await requestCase(baseUrl, runtime, 'failover')
  assert.equal(second.status, item.protocolKind === 'anthropic' ? 529 : 503, `${item.label} 第二次通用请求仍应原样返回主账号失败响应`)
  assert.match(second.text, new RegExp(`${item.label} primary failed`), `${item.label} 第二次失败正文必须保持上游语义`)
  assert.deepEqual(caseAuthorizations(secondStart, item.label), [
    item.protocolKind === 'anthropic' ? `x-api-key sk-${item.label}-primary-upstream` : `Bearer sk-${item.label}-primary-upstream`
  ], `${item.label} 通用客户端完整失败响应不得建立跨请求账号屏蔽`)

  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewayCache.clearGatewayRuntimeCache()

  const recoveryStart = upstreamHits.length
  const recovered = await requestCase(baseUrl, runtime, 'recovered')
  assert.equal(recovered.status, 200, `${item.label} 清理运行态后主账号应可恢复成功，实际 HTTP ${recovered.status}: ${recovered.text}`)
  assertOutput(recovered.text, item, 'primary')
  assert.deepEqual(caseAuthorizations(recoveryStart, item.label), [
    item.protocolKind === 'anthropic' ? `x-api-key sk-${item.label}-primary-upstream` : `Bearer sk-${item.label}-primary-upstream`
  ], `${item.label} 恢复请求应重新命中主账号`)
}

async function requestCase(baseUrl: string, runtime: CaseRuntime, marker: 'failover' | 'recovered'): Promise<{ status: number; text: string }> {
  const { item } = runtime
  if (item.protocolKind === 'anthropic') {
    const response = await fetch(`${baseUrl}/v1/messages`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${runtime.localApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: item.model,
        max_tokens: 32,
        messages: [{ role: 'user', content: `${item.label} ${marker}` }],
        stream: false
      })
    })
    return { status: response.status, text: await response.text() }
  }
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${runtime.localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: item.model,
      messages: [{ role: 'user', content: `${item.label} ${marker}` }],
      stream: false
    })
  })
  return { status: response.status, text: await response.text() }
}

function assertOutput(text: string, item: MatrixCase, lane: 'primary' | 'rescue'): void {
  if (item.protocolKind === 'anthropic') {
    const body = JSON.parse(text) as { content?: Array<{ text?: string }> }
    assert.equal(body.content?.[0]?.text, `${item.label} ${lane} ok`, `${item.label} Anthropic 响应内容应来自 ${lane}`)
    return
  }
  const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string } }> }
  assert.equal(body.choices?.[0]?.message?.content, `${item.label} ${lane} ok`, `${item.label} OpenAI 响应内容应来自 ${lane}`)
}

function caseAuthorizations(start: number, providerLabel: string): string[] {
  return upstreamHits
    .slice(start)
    .filter((hit) => hit.providerLabel === providerLabel)
    .map((hit) => hit.xApiKey ? `x-api-key ${hit.xApiKey}` : hit.authorization)
}

function assertUsageRecords(runtimes: CaseRuntime[]): void {
  const records = repositories.listUsageRecords(access, { pageSize: 500, result: 'all' }).items
  for (const runtime of runtimes) {
    const { item } = runtime
    const providerRecords = records.filter((record) => record.providerCode === item.providerCode && record.groupId === runtime.groupId)
    assert(providerRecords.length >= 3, `${item.label} 应写入两次透明失败和一次恢复成功 usage 记录，实际 ${providerRecords.length}`)
    assert(providerRecords.every((record) => record.providerProtocolProfileId === item.providerProtocolProfileId), `${item.label} usage provider_protocol_profile_id 不应串供应商`)
    assert(providerRecords.every((record) => record.usageSemantic === item.usageSemantic), `${item.label} usage_semantic 应为 ${item.usageSemantic}`)
    const transparentStatusCode = item.protocolKind === 'anthropic' ? 529 : 503
    assert(providerRecords.filter((record) => record.accountId === runtime.primaryAccountId && record.success === true && record.statusCode === transparentStatusCode).length >= 2, `${item.label} usage 应把完整失败记录为成功透明转发并保留上游状态码`)
    assert(providerRecords.every((record) => record.accountId !== runtime.rescueAccountId), `${item.label} 通用客户端完整失败不得产生备用账号 usage`)
    assert(providerRecords.some((record) => record.accountId === runtime.primaryAccountId && record.success === true && record.statusCode === 200), `${item.label} 主账号恢复成功 usage 应存在`)
  }
}

function assertAuditLogs(runtimes: CaseRuntime[]): void {
  const database = databaseModule.getDatasetDatabase()
  const logs = database.prepare(`
    SELECT provider_code, group_id, account_id, success, final_status_code
    FROM audit_logs
  `).all() as Array<{ provider_code: string | null; group_id: string | null; account_id: string | null; success: number; final_status_code: number | null }>
  const attempts = database.prepare(`
    SELECT provider_code, group_id, account_id, success, upstream_status_code
    FROM audit_log_attempts
  `).all() as Array<{ provider_code: string | null; group_id: string | null; account_id: string | null; success: number; upstream_status_code: number | null }>
  for (const runtime of runtimes) {
    const { item } = runtime
    const transparentStatusCode = item.protocolKind === 'anthropic' ? 529 : 503
    assert(logs.some((log) => log.provider_code === item.providerCode && log.group_id === runtime.groupId && log.success === 1), `${item.label} 应写入成功审计日志`)
    assert(attempts.filter((attempt) => attempt.provider_code === item.providerCode && attempt.group_id === runtime.groupId && attempt.account_id === runtime.primaryAccountId && attempt.success === 1 && attempt.upstream_status_code === transparentStatusCode).length >= 2, `${item.label} 审计 attempt 应保留成功透明转发与上游状态码`)
    assert(!attempts.some((attempt) => attempt.provider_code === item.providerCode && attempt.group_id === runtime.groupId && attempt.account_id === runtime.rescueAccountId), `${item.label} 通用客户端完整失败不得产生备用账号审计 attempt`)
  }
}

function createMatrixMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const path = req.url?.split('?', 1)[0] ?? ''
      const providerLabel = providerLabelFromPath(path)
      upstreamHits.push({
        providerLabel,
        path,
        authorization: String(req.headers.authorization ?? ''),
        xApiKey: String(req.headers['x-api-key'] ?? ''),
        bodyText
      })
      const authorization = String(req.headers.authorization ?? '')
      const xApiKey = String(req.headers['x-api-key'] ?? '')
      const lane = authorization.includes('-primary-upstream') || xApiKey.includes('-primary-upstream') ? 'primary' : 'rescue'
      if (bodyText.includes('failover') && lane === 'primary') {
        sendProviderFailure(res, providerLabel)
        return
      }
      if (providerLabel === 'anthropic') {
        sendAnthropicSuccess(res, providerLabel, lane)
        return
      }
      sendOpenAISuccess(res, providerLabel, lane)
    })
  })
}

function providerLabelFromPath(path: string): string {
  const first = path.split('/').filter(Boolean)[0]
  return first || 'unknown'
}

function sendProviderFailure(res: http.ServerResponse, providerLabel: string): void {
  if (providerLabel === 'anthropic') {
    res.writeHead(529, { 'content-type': 'application/json; charset=utf-8' })
    res.end(JSON.stringify({
      type: 'error',
      error: {
        type: 'overloaded_error',
        msg: `${providerLabel} primary failed`
      }
    }))
    return
  }
  res.writeHead(503, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    error: {
      code: 5903,
      type: 'mock_provider_failure',
      msg: `${providerLabel} primary failed`
    }
  }))
}

function sendOpenAISuccess(res: http.ServerResponse, providerLabel: string, lane: string): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: `chatcmpl-${providerLabel}-${lane}`,
    object: 'chat.completion',
    choices: [
      {
        index: 0,
        message: { role: 'assistant', content: `${providerLabel} ${lane} ok` },
        finish_reason: 'stop'
      }
    ],
    usage: {
      prompt_tokens: 5,
      completion_tokens: 3,
      total_tokens: 8
    }
  }))
}

function sendAnthropicSuccess(res: http.ServerResponse, providerLabel: string, lane: string): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: `msg_${providerLabel}_${lane}`,
    type: 'message',
    role: 'assistant',
    model: 'claude-haiku-4-5',
    content: [{ type: 'text', text: `${providerLabel} ${lane} ok` }],
    stop_reason: 'end_turn',
    usage: {
      input_tokens: 5,
      output_tokens: 3
    }
  }))
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'server 未监听端口')
  return { port: address.port }
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    })
  })
}
