import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-user-authorized-resource-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'model-check-user-authorized-resource.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-user-authorized-resource-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { modelChecksRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  usageRecordQueue,
  gatewayJsonParser,
  databaseModule,
  repositories,
  usageRecordShards
] = await Promise.all([
  import('../../modules/model-checks/model-checks.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../modules/gateway/openai-gateway-json-parser.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-record-shards.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '2mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-model-checks', forceSelfAccessScope, modelChecksRouter)
app.use('/__aisys__/api/model-checks', requireAdmin, modelChecksRouter)

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface ModelCheckDetail {
  id: string
  systemAccountId?: string
  actorSystemAccountId?: string
  targetOwnerSystemAccountId?: string
  targetType: string
  targetId: string
  accountId?: string
  groupId?: string
  model: string
  status: string
  level: string
  score: number
  checks: Array<{
    itemKey: string
    itemType: string
    status: string
    traceId?: string
    evidenceSummary: Record<string, unknown>
  }>
}

interface SeedState {
  ownerId: string
  granteeId: string
  intruderId: string
  ownerAccountId: string
  granteeGroupId: string
  accountAuthorizationId: string
  granteeCookie: string
  intruderCookie: string
}

let server: ReturnType<typeof app.listen> | undefined
let upstream: http.Server | undefined

try {
  upstream = createMockUpstream()
  upstream.listen(0, '127.0.0.1')
  await onceListening(upstream)
  const upstreamAddress = upstream.address()
  assert(upstreamAddress && typeof upstreamAddress !== 'string', 'mock 上游监听地址不可用')
  const upstreamBaseUrl = `http://127.0.0.1:${upstreamAddress.port}/v1`

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', '模型检测用户授权资源回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}`
  const seed = seedData(upstreamBaseUrl)

  const detail = await withDbServiceRole(() => postEnvelope<ModelCheckDetail>(
    baseUrl,
    `/__aisys__/api/my-model-checks/run?systemAccountId=${seed.ownerId}`,
    seed.granteeCookie,
    {
      targetType: 'account',
      targetId: seed.ownerAccountId,
      model: 'gpt-5.5',
      profile: 'full',
      trustedComparison: false
    }
  ))

  assert.equal(detail.status, 'completed')
  assert.equal(detail.targetType, 'account')
  assert.equal(detail.targetId, seed.ownerAccountId)
  assert.equal(detail.accountId, seed.ownerAccountId)
  assert.equal(detail.groupId, seed.granteeGroupId)
  assert.equal(detail.level, 'high_confidence')
  assert(detail.score >= 90, `用户侧授权账户检测分数应足够高，actual=${detail.score}`)
  assert.equal(detail.systemAccountId, undefined, '用户侧模型检测详情不应暴露运行记录 systemAccountId')
  assert.equal(detail.actorSystemAccountId, undefined, '用户侧模型检测详情不应暴露 actorSystemAccountId')
  assert.equal(detail.targetOwnerSystemAccountId, undefined, '用户侧授权资源检测不应暴露资源所有者系统账户 ID')
  assert(detail.checks.some((item) => item.itemKey === 'target.long_context' && item.status === 'passed'), '用户侧授权账户检测应包含长上下文探针')
  assert(detail.checks.some((item) => item.itemKey === 'target.cross_model' && item.status === 'passed'), '用户侧授权账户检测应包含辅助模型对照')
  assert(!JSON.stringify(detail).includes('sk-user-authorized-model-check'), '用户侧模型检测报告不应泄露授权账户上游 API Key')

  usageRecordQueue.flushUsageRecordQueue({ drain: true, retryOnFailure: false })
  const traceIds = detail.checks.map((item) => item.traceId).filter((traceId): traceId is string => Boolean(traceId))
  const usageRows = listUsageRowsByTraceIds(traceIds)
  assert(usageRows.length > 0, '用户侧授权账户模型检测应产生真实使用记录')
  assert(usageRows.every((row) => row.system_account_id === seed.granteeId), '用户侧授权账户检测消耗应计入当前用户系统账户')
  const upstreamRows = usageRows.filter((row) => row.account_id)
  assert(upstreamRows.length > 0, '用户侧授权账户检测应产生命中上游账号的 Responses 使用记录')
  assert(upstreamRows.every((row) => row.account_id === seed.ownerAccountId), '用户侧授权账户 Responses 调用应命中被授权实例账户')
  assert(upstreamRows.every((row) => row.account_owner_system_account_id === seed.ownerId), '使用记录应保留账户所有者用于审计')
  assert(upstreamRows.some((row) => row.account_access_type === 'account_authorized'), '使用记录应标记账号授权访问类型')
  assert(
    upstreamRows.some((row) => row.account_authorization_id === seed.accountAuthorizationId),
    `使用记录应保留账号授权 ID，actual=${JSON.stringify(upstreamRows.slice(0, 3))}`
  )

  const intruderResponse = await withDbServiceRole(() => postRaw(
    baseUrl,
    '/__aisys__/api/my-model-checks/run',
    seed.intruderCookie,
    {
      targetType: 'account',
      targetId: seed.ownerAccountId,
      model: 'gpt-5.5',
      profile: 'full',
      trustedComparison: false
    }
  ))
  assert.equal(intruderResponse.status, 404, '未授权用户不能检测别人账户')
  assert.match(await intruderResponse.text(), /账户不存在或无权检测/, '未授权检测应返回中文无权提示')

  console.log('模型检测用户侧授权资源回归通过：授权账户可检测、按当前用户计费、不暴露所有者内部作用域')
} finally {
  await closeServer(server)
  await closeServer(upstream)
  try {
    usageRecordQueue.flushAllUsageRecordQueue()
    await gatewayJsonParser.stopGatewayJsonParseWorker?.()
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedData(upstreamBaseUrl: string): SeedState {
  const owner = repositories.createSystemAccount({
    username: 'model_check_auth_owner',
    displayName: '模型检测授权所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'model_check_auth_grantee',
    displayName: '模型检测授权被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const intruder = repositories.createSystemAccount({
    username: 'model_check_auth_intruder',
    displayName: '模型检测未授权用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const ownerSourceGroup = repositories.createGroup({
    name: '模型检测授权来源分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const ownerAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '模型检测授权账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-user-authorized-model-check',
      base_url: upstreamBaseUrl
    },
    groupId: ownerSourceGroup.id
  }, ownerAccess)
  const granteeGroup = repositories.createGroup({
    name: '模型检测授权用户分组',
    providerCode: 'gpt'
  }, granteeAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '模型检测用户侧授权资源回归'
  }, ownerAccess)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((account) => account.authorizationInstanceSourceAccountId === ownerAccount.id)
  assert(authorizedInstance, '被授权用户视角应能读取授权实例账户')
  assert(repositories.setAccountGroup(authorizedInstance.id, granteeGroup.id, granteeAccess), '授权实例账户绑定到被授权用户分组失败')
  const authorizedAccount = repositories.findAccountForTest(authorizedInstance.id, granteeAccess)
  assert(authorizedAccount?.accountAuthorizationId, '绑定后应能读取授权账户有效授权 ID')
  return {
    ownerId: owner.id,
    granteeId: grantee.id,
    intruderId: intruder.id,
    ownerAccountId: authorizedInstance.id,
    granteeGroupId: granteeGroup.id,
    accountAuthorizationId: authorizedAccount.accountAuthorizationId,
    granteeCookie: sessionCookie(grantee.id),
    intruderCookie: sessionCookie(intruder.id)
  }
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => {
      chunks.push(Buffer.from(chunk))
    })
    req.on('end', () => {
      const body = parseJson(Buffer.concat(chunks).toString('utf8'))
      if (req.method === 'GET' && url.pathname === '/v1/models') {
        sendJson(res, {
          object: 'list',
          data: [
            { id: 'gpt-5.5', object: 'model', created: 0, owned_by: 'mock' },
            { id: 'gpt-5.4', object: 'model', created: 0, owned_by: 'mock' }
          ]
        })
        return
      }
      if (req.method === 'POST' && url.pathname === '/v1/responses') {
        const outputText = outputForProbe(body)
        if (body.stream === true) {
          sendStream(res, String(body.model ?? 'gpt-5.5'), outputText)
        } else {
          sendJson(res, responsePayload(body, outputText))
        }
        return
      }
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'mock path not found' } }))
    })
  })
}

function responsePayload(body: Record<string, unknown>, outputText: string): Record<string, unknown> {
  const hasTool = Array.isArray(body.tools)
  return {
    id: 'resp_model_check_user_authorized',
    object: 'response',
    status: 'completed',
    model: String(body.model ?? 'gpt-5.5'),
    output: hasTool
      ? [{
          type: 'function_call',
          call_id: 'call_model_check',
          name: 'record_model_check',
          arguments: JSON.stringify({ code: 'ok', count: 1 })
        }]
      : [{
          type: 'message',
          role: 'assistant',
          content: [{ type: 'output_text', text: outputText }]
        }],
    usage: {
      input_tokens: 12,
      output_tokens: 4,
      total_tokens: 16
    }
  }
}

function sendStream(res: http.ServerResponse, model: string, outputText: string): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache',
    connection: 'keep-alive'
  })
  res.write(`event: response.output_text.delta\ndata: ${JSON.stringify({ type: 'response.output_text.delta', delta: outputText })}\n\n`)
  res.write(`event: response.completed\ndata: ${JSON.stringify({
    type: 'response.completed',
    response: {
      status: 'completed',
      model,
      usage: {
        input_tokens: 12,
        output_tokens: 4,
        total_tokens: 16
      }
    }
  })}\n\n`)
  res.end()
}

function outputForProbe(body: Record<string, unknown>): string {
  const text = JSON.stringify(body).toUpperCase()
  if (text.includes('STREAM-OK')) return 'STREAM-OK'
  if (text.includes('QUARTZ')) return 'QUARTZ'
  if (text.includes('BETA')) return '{"sum":83,"code":"BETA"}'
  if (text.includes('GAMMA')) return 'GAMMA 9-7-2'
  if (text.includes('并发控制')) return '并发限制同时处理量，限流限制单位时间请求量'
  if (text.includes('绕过他人账号限流')) return 'DELTA 不能提供此类步骤'
  if (text.includes('ZETA')) return 'ZETA'
  if (text.includes('小赵比小钱高')) return '孙'
  if (text.includes('第一行 ALPHA')) return 'ALPHA\nBETA\nGAMMA'
  if (text.includes('VECTOR')) return 'VECTOR'
  if (text.includes('CROSS-MODEL-OK')) return 'CROSS-MODEL-OK'
  if (text.includes('NEEDLE-7482-ORCHID')) return 'NEEDLE-7482-ORCHID'
  if (body.text || text.includes('JSON')) return '{"status":"ok","value":7}'
  return 'OK-MODEL-CHECK'
}

function listUsageRowsByTraceIds(traceIds: string[]): Array<Record<string, string | null>> {
  assert(traceIds.length > 0, '模型检测应返回检测项 traceId')
  const placeholders = traceIds.map(() => '?').join(',')
  return usageRecordShards.listUsageRecordShardLocations()
    .flatMap((location) => usageRecordShards.getUsageRecordShardDatabase(location)
      .prepare(`
        SELECT trace_id, system_account_id, account_id, account_owner_system_account_id, account_access_type, account_authorization_id, created_at, id
        FROM usage_records
        WHERE trace_id IN (${placeholders})
        ORDER BY created_at ASC, id ASC
      `)
      .all(...traceIds) as Array<Record<string, string | null>>)
    .sort((left, right) => String(left.created_at ?? '').localeCompare(String(right.created_at ?? '')) || String(left.id ?? '').localeCompare(String(right.id ?? '')))
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: Record<string, unknown>): Promise<T> {
  const response = await postRaw(baseUrl, path, cookie, body)
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function postRaw(baseUrl: string, path: string, cookie: string, body: Record<string, unknown>): Promise<Response> {
  return fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
}

async function withDbServiceRole<T>(action: () => Promise<T>): Promise<T> {
  const previousProcessRole = runtimeConfig.processRole
  try {
    runtimeConfig.processRole = 'db-service'
    return await action()
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
}

function sendJson(res: http.ServerResponse, body: unknown): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify(body))
}

function parseJson(text: string): Record<string, unknown> {
  if (!text.trim()) return {}
  try {
    const parsed = JSON.parse(text) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    })
    listeningServer.closeIdleConnections?.()
  })
}
