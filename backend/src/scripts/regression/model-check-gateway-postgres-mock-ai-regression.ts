import { strict as assert } from 'node:assert'
import http from 'node:http'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { startDbServiceSupervisor } from '../../modules/db-service/db-service-supervisor.js'
import { prepareGptAccountBeforeDispatch } from '../../modules/providers/drivers/gpt/oauth-dispatch-preparation.js'
import {
  runGatewayProbe
} from '../../modules/model-checks/model-checks-gateway-probe.js'
import { createModelCheckProbeRequest } from '../../modules/model-checks/model-checks.payloads.js'
import { findModelCheckProfileForAccountModel } from '../../modules/model-checks/model-checks.profiles.js'
import { setOpenAIOAuthTokenRefresherForTest } from '../../modules/openai-oauth/openai-oauth-access-token-refresh.service.js'
import { clearGatewayRuntimeCacheLocal } from '../../modules/gateway/runtime/runtime-cache.service.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createAccountAsync,
  createGroupAsync,
  createSystemAccountAsync,
  findAccountForTestAsync,
  listOpenAIAccountsForGroupResultAsync
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import type { GatewayProbeResult } from '../../modules/model-checks/model-checks-evaluation.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '模型检测 PG 网关 MockAI 回归需要 JUHE_AI_DATABASE_DRIVER=postgres')
assert.equal(runtimeConfig.queueDriver, 'redis_stream', '模型检测 PG 网关 MockAI 回归需要 JUHE_AI_QUEUE_DRIVER=redis_stream')

runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false

const marker = `model_check_gateway_pg_mock_${Date.now()}_${Math.random().toString(16).slice(2)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'admin' }
const upstream = createMockUpstream()
const groupIds: string[] = []
const accountIds: string[] = []
const systemAccountIds: string[] = []

try {
  await startLocalDbService()
  await listen(upstream)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstream)}/v1`
  const group = await createGroupAsync({
    name: `PG MockAI 模型检测分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  groupIds.push(group.id)
  const account = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `PG MockAI 模型检测账户 ${marker}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-${marker}`,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5', 'gpt-5.4']
  }, access)
  accountIds.push(account.id)

  const accountForTest = await findAccountForTestAsync(account.id, access)
  assert(accountForTest, 'PG MockAI 账户应可读取')
  const profile = findModelCheckProfileForAccountModel(accountForTest, 'gpt-5.5')
  assert(profile, 'PG MockAI 账户应支持模型检测 profile')
  assert(accountForTest.boundGroupId, 'PG MockAI 账户应绑定分组')
  const candidate = (await listOpenAIAccountsForGroupResultAsync(accountForTest.boundGroupId, access.systemAccountId, {
    includeUnavailable: true
  })).accounts.find((item) => item.id === account.id)
  assert(candidate, 'PG MockAI 账户应进入分组候选账号')

  const target = {
    identity: {
      systemAccountId: access.systemAccountId,
      groupId: accountForTest.boundGroupId
    },
    candidateAccounts: [candidate]
  }
  const catalog = await runGatewayProbe(target, {
    method: 'GET',
    path: '/v1/models',
    itemKey: 'target.model_catalog',
    responseProtocol: profile.protocol
  })
  const responsesRequest = createModelCheckProbeRequest(profile.protocol, 'gpt-5.5', 'Reply with exactly: OK-MODEL-CHECK', {
    maxOutputTokens: 16,
    stream: false
  })
  const responses = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesRequest.path,
    itemKey: 'target.responses_basic',
    body: responsesRequest.body,
    responseProtocol: responsesRequest.responseProtocol,
    expectedModel: responsesRequest.expectedModel
  })

  assertProbePassed('model catalog', catalog)
  assertProbePassed('responses basic', responses)
  await assertDbServiceRoleGatewayProbe(upstreamBaseUrl)
  await assertOpenAIOAuthRefreshUsesPostgresDbService(upstreamBaseUrl)
  console.log(JSON.stringify({
    message: 'PG/Redis 模型检测 MockAI 网关探针回归通过',
    catalogStatusCode: catalog.statusCode,
    responsesStatusCode: responses.statusCode,
    responsesModel: responses.model
  }))
} finally {
  setOpenAIOAuthTokenRefresherForTest()
  await cleanupRows(accountIds, groupIds, systemAccountIds)
  await closeServer(upstream)
  await closeRedisClients()
  await closePostgresPool()
}

process.exit(0)

function assertProbePassed(label: string, result: GatewayProbeResult): void {
  assert(!String(result.errorMessage ?? result.bodyText ?? '').includes('JUHE_AI_DATABASE_DRIVER=postgres 不能回退写入 SQLite'), `${label} 不应触发 PostgreSQL 回退 SQLite 错误`)
  assert.equal(result.success, true, `${label} 探针必须通过，status=${result.statusCode}, error=${result.errorMessage ?? ''}, body=${String(result.bodyText ?? '').slice(0, 300)}`)
}

async function assertOpenAIOAuthRefreshUsesPostgresDbService(upstreamBaseUrl: string): Promise<void> {
  const group = await createGroupAsync({
    name: `PG MockAI OAuth 刷新分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  groupIds.push(group.id)
  const account = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `PG MockAI OAuth 刷新账户 ${marker}`,
    type: 'oauth',
    credentials: {
      access_token: `oauth-expired-${marker}`,
      refresh_token: `oauth-refresh-${marker}`,
      expires_at: new Date(Date.now() - 60_000).toISOString(),
      client_id: 'codex-cli-test',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5', 'gpt-5.4']
  }, access)
  accountIds.push(account.id)
  setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => ({
    accessToken: `oauth-refreshed-${marker}`,
    refreshToken,
    expiresIn: 3600,
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    clientId: clientId ?? 'codex-cli-test'
  }))

  const candidate = (await listOpenAIAccountsForGroupResultAsync(group.id, access.systemAccountId, {
    includeUnavailable: true
  })).accounts.find((item) => item.id === account.id)
  assert(candidate, 'PG MockAI OAuth 账户应进入分组候选账号')
  assert.equal(candidate.type, 'oauth', 'PG MockAI OAuth 候选账号类型应为 oauth')
  const refreshed = await prepareGptAccountBeforeDispatch(candidate)
  assert.equal(refreshed.apiKey, `oauth-refreshed-${marker}`, 'OAuth access token 刷新应通过 DB service 写回并返回新 token')

  const latest = await findAccountForTestAsync(account.id, access)
  assert.equal(latest?.credentials.access_token, `oauth-refreshed-${marker}`, 'OAuth access token 应写入 PostgreSQL 账户凭据')
}

async function assertDbServiceRoleGatewayProbe(upstreamBaseUrl: string): Promise<void> {
  const systemAccount = await createSystemAccountAsync({
    username: `dbsvc_probe_${marker}`,
    displayName: `DBServiceProbe${marker}`,
    description: 'PG 模型检测 DB service 回归',
    password: `Pwd${marker}Aa1!`,
    role: 'user',
    status: 'active',
    mustChangePassword: false,
    imageGenerationEnabled: false
  })
  systemAccountIds.push(systemAccount.id)
  const dbServiceAccess: AccessScope = {
    systemAccountId: systemAccount.id,
    role: 'user'
  }
  const group = await createGroupAsync({
    name: `PG MockAI DB service 模型检测分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, dbServiceAccess)
  groupIds.push(group.id)
  const account = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `PG MockAI DB service 模型检测账户 ${marker}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-db-service-${marker}`,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5', 'gpt-5.4']
  }, dbServiceAccess)
  accountIds.push(account.id)

  const accountForTest = await findAccountForTestAsync(account.id, dbServiceAccess)
  assert(accountForTest, 'PG MockAI DB service 账户应可读取')
  const profile = findModelCheckProfileForAccountModel(accountForTest, 'gpt-5.5')
  assert(profile, 'PG MockAI DB service 账户应支持模型检测 profile')
  assert(accountForTest.boundGroupId, 'PG MockAI DB service 账户应绑定分组')
  const candidate = (await listOpenAIAccountsForGroupResultAsync(accountForTest.boundGroupId, dbServiceAccess.systemAccountId, {
    includeUnavailable: true
  })).accounts.find((item) => item.id === account.id)
  assert(candidate, 'PG MockAI DB service 账户应进入分组候选账号')

  clearGatewayRuntimeCacheLocal()
  const previousRole = runtimeConfig.processRole
  runtimeConfig.processRole = 'db-service'
  try {
    const target = {
      identity: {
        systemAccountId: dbServiceAccess.systemAccountId,
        groupId: accountForTest.boundGroupId
      },
      candidateAccounts: [candidate]
    }
    const catalog = await runGatewayProbe(target, {
      method: 'GET',
      path: '/v1/models',
      itemKey: 'target.model_catalog',
      responseProtocol: profile.protocol
    })
    const responsesRequest = createModelCheckProbeRequest(profile.protocol, 'gpt-5.5', 'Reply with exactly: OK-MODEL-CHECK', {
      maxOutputTokens: 16,
      stream: false
    })
    const responses = await runGatewayProbe(target, {
      method: 'POST',
      path: responsesRequest.path,
      itemKey: 'target.responses_basic',
      body: responsesRequest.body,
      responseProtocol: responsesRequest.responseProtocol,
      expectedModel: responsesRequest.expectedModel
    })
    assertProbePassed('db-service role model catalog', catalog)
    assertProbePassed('db-service role responses basic', responses)
  } finally {
    runtimeConfig.processRole = previousRole
    clearGatewayRuntimeCacheLocal()
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
    id: 'resp_model_check_gateway_pg_mock',
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
  const needle = text.match(/NEEDLE-(?:LOW|MEDIUM|HIGH|EXTREME)-\d+/)
  if (needle) return needle[0]
  if (body.text || text.includes('JSON')) return '{"status":"ok","value":7}'
  return 'OK-MODEL-CHECK'
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

function listen(server: http.Server): Promise<void> {
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'mock upstream should be listening')
  return address.port
}

function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    })
  })
}

async function cleanupRows(
  accountIdsInput: readonly string[],
  groupIdsInput: readonly string[],
  systemAccountIdsInput: readonly string[]
): Promise<void> {
  const pool = await getPostgresPool()
  for (const accountId of accountIdsInput) {
    await pool.query('DELETE FROM juhe_dataset.model_check_items WHERE run_id IN (SELECT id FROM juhe_dataset.model_check_runs WHERE target_id = $1)', [accountId])
    await pool.query('DELETE FROM juhe_dataset.model_check_runs WHERE target_id = $1', [accountId])
    await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = $1', [accountId])
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = $1', [accountId])
  }
  for (const groupId of groupIdsInput) {
    await pool.query('DELETE FROM juhe_business.group_accounts WHERE group_id = $1', [groupId])
    await pool.query('DELETE FROM juhe_business.groups WHERE id = $1', [groupId])
  }
  for (const systemAccountId of systemAccountIdsInput) {
    await pool.query('DELETE FROM juhe_business.api_keys WHERE system_account_id = $1', [systemAccountId])
    await pool.query(`
      DELETE FROM juhe_business.route_strategy_groups
      WHERE route_strategy_id IN (SELECT id FROM juhe_business.route_strategies WHERE system_account_id = $1)
         OR group_id IN (SELECT id FROM juhe_business.groups WHERE system_account_id = $1)
    `, [systemAccountId])
    await pool.query('DELETE FROM juhe_business.route_strategies WHERE system_account_id = $1', [systemAccountId])
    await pool.query(`
      DELETE FROM juhe_business.group_accounts
      WHERE group_id IN (SELECT id FROM juhe_business.groups WHERE system_account_id = $1)
         OR account_id IN (SELECT id FROM juhe_business.accounts WHERE system_account_id = $1)
    `, [systemAccountId])
    await pool.query('DELETE FROM juhe_business.accounts WHERE system_account_id = $1', [systemAccountId])
    await pool.query('DELETE FROM juhe_business.groups WHERE system_account_id = $1', [systemAccountId])
    await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = $1', [systemAccountId])
  }
}

function startLocalDbService(): Promise<void> {
  return new Promise((resolvePromise, rejectPromise) => {
    const timer = setTimeout(() => rejectPromise(new Error('DB service 启动超时')), 15_000)
    startDbServiceSupervisor({
      onReady: () => {
        clearTimeout(timer)
        resolvePromise()
      }
    })
  })
}
