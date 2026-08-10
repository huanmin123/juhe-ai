import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

import express, { type ErrorRequestHandler } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { AccountErrorHandlingRuleAction } from '../../modules/accounts/account-error-policy-validation.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

type RequestKind = 'responses_background' | 'responses_hosted_tool' | 'audio_speech' | 'image_generation'
type FailureMode = 'transport_reset' | 'complete_http_failure'

interface ReplayScenario {
  id: string
  requestKind: RequestKind
  failureMode: FailureMode
  failureStatusCode?: number
  failureErrorCode?: string
  policyAction?: AccountErrorHandlingRuleAction
  expectedAccountStatus: 'active' | 'temporary_unavailable' | 'error'
}

interface ScenarioFixture {
  scenario: ReplayScenario
  apiKey: string
  firstAccountId: string
  fallbackAccountId: string
  firstAuthorization: string
  fallbackAuthorization: string
}

interface UpstreamHit {
  scenarioId: string
  authorization: string
  path: string
  bodyAccepted: boolean
}

interface GatewayMiddlewareErrorDiagnostic {
  scenarioId?: string
  traceId?: string
  errorName: string
  errorMessage: string
  headersSent: boolean
  writableEnded: boolean
  destroyed: boolean
}

interface RawGatewayResponse {
  statusCode: number
  text: string
  socketLocalPort?: number
}

const scenarios: ReplayScenario[] = [
  {
    id: 'background_transport_reset',
    requestKind: 'responses_background',
    failureMode: 'transport_reset',
    expectedAccountStatus: 'active',
  },
  {
    id: 'background_no_policy_http_failure',
    requestKind: 'responses_background',
    failureMode: 'complete_http_failure',
    expectedAccountStatus: 'active',
  },
  {
    id: 'background_untrusted_401',
    requestKind: 'responses_background',
    failureMode: 'complete_http_failure',
    failureStatusCode: 401,
    expectedAccountStatus: 'active',
  },
  {
    id: 'background_model_not_found',
    requestKind: 'responses_background',
    failureMode: 'complete_http_failure',
    failureStatusCode: 503,
    failureErrorCode: 'model_not_found',
    expectedAccountStatus: 'active',
  },
  {
    id: 'background_model_not_supported',
    requestKind: 'responses_background',
    failureMode: 'complete_http_failure',
    failureStatusCode: 400,
    failureErrorCode: 'model_not_supported',
    expectedAccountStatus: 'active',
  },
  {
    id: 'background_explicit_cooldown',
    requestKind: 'responses_background',
    failureMode: 'complete_http_failure',
    policyAction: 'temp_unschedulable',
    expectedAccountStatus: 'temporary_unavailable',
  },
  {
    id: 'background_explicit_retry_next',
    requestKind: 'responses_background',
    failureMode: 'complete_http_failure',
    policyAction: 'retry_next',
    expectedAccountStatus: 'active',
  },
  {
    id: 'hosted_tool_transport_reset',
    requestKind: 'responses_hosted_tool',
    failureMode: 'transport_reset',
    expectedAccountStatus: 'active',
  },
  {
    id: 'hosted_tool_no_policy_http_failure',
    requestKind: 'responses_hosted_tool',
    failureMode: 'complete_http_failure',
    expectedAccountStatus: 'active',
  },
  {
    id: 'hosted_tool_untrusted_401',
    requestKind: 'responses_hosted_tool',
    failureMode: 'complete_http_failure',
    failureStatusCode: 401,
    expectedAccountStatus: 'active',
  },
  {
    id: 'hosted_tool_model_not_found',
    requestKind: 'responses_hosted_tool',
    failureMode: 'complete_http_failure',
    failureStatusCode: 503,
    failureErrorCode: 'model_not_found',
    expectedAccountStatus: 'active',
  },
  {
    id: 'hosted_tool_model_not_supported',
    requestKind: 'responses_hosted_tool',
    failureMode: 'complete_http_failure',
    failureStatusCode: 400,
    failureErrorCode: 'model_not_supported',
    expectedAccountStatus: 'active',
  },
  {
    id: 'hosted_tool_explicit_disable',
    requestKind: 'responses_hosted_tool',
    failureMode: 'complete_http_failure',
    policyAction: 'error_disabled',
    expectedAccountStatus: 'error',
  },
  {
    id: 'hosted_tool_explicit_retry_next',
    requestKind: 'responses_hosted_tool',
    failureMode: 'complete_http_failure',
    policyAction: 'retry_next',
    expectedAccountStatus: 'active',
  },
  {
    id: 'audio_transport_reset',
    requestKind: 'audio_speech',
    failureMode: 'transport_reset',
    expectedAccountStatus: 'active',
  },
  {
    id: 'audio_no_policy_http_failure',
    requestKind: 'audio_speech',
    failureMode: 'complete_http_failure',
    expectedAccountStatus: 'active',
  },
  {
    id: 'audio_untrusted_401',
    requestKind: 'audio_speech',
    failureMode: 'complete_http_failure',
    failureStatusCode: 401,
    expectedAccountStatus: 'active',
  },
  {
    id: 'audio_model_not_found',
    requestKind: 'audio_speech',
    failureMode: 'complete_http_failure',
    failureStatusCode: 503,
    failureErrorCode: 'model_not_found',
    expectedAccountStatus: 'active',
  },
  {
    id: 'audio_model_not_supported',
    requestKind: 'audio_speech',
    failureMode: 'complete_http_failure',
    failureStatusCode: 400,
    failureErrorCode: 'model_not_supported',
    expectedAccountStatus: 'active',
  },
  {
    id: 'audio_explicit_cooldown',
    requestKind: 'audio_speech',
    failureMode: 'complete_http_failure',
    policyAction: 'temp_unschedulable',
    expectedAccountStatus: 'temporary_unavailable',
  },
  {
    id: 'audio_explicit_retry_next',
    requestKind: 'audio_speech',
    failureMode: 'complete_http_failure',
    policyAction: 'retry_next',
    expectedAccountStatus: 'active',
  },
  {
    id: 'image_generation_transport_reset',
    requestKind: 'image_generation',
    failureMode: 'transport_reset',
    expectedAccountStatus: 'active',
  },
  {
    id: 'image_generation_no_policy_http_failure',
    requestKind: 'image_generation',
    failureMode: 'complete_http_failure',
    expectedAccountStatus: 'active',
  },
  {
    id: 'image_generation_untrusted_401',
    requestKind: 'image_generation',
    failureMode: 'complete_http_failure',
    failureStatusCode: 401,
    expectedAccountStatus: 'active',
  },
  {
    id: 'image_generation_model_not_found',
    requestKind: 'image_generation',
    failureMode: 'complete_http_failure',
    failureStatusCode: 503,
    failureErrorCode: 'model_not_found',
    expectedAccountStatus: 'active',
  },
  {
    id: 'image_generation_model_not_supported',
    requestKind: 'image_generation',
    failureMode: 'complete_http_failure',
    failureStatusCode: 400,
    failureErrorCode: 'model_not_supported',
    expectedAccountStatus: 'active',
  }
]
assert.equal(scenarios.length, 26, '统一切号回归必须覆盖四类请求的 HTTP、transport 与显式策略失败')

const keepAliveScenarios: ReplayScenario[] = [
  {
    id: 'keep_alive_background_transport_reset',
    requestKind: 'responses_background',
    failureMode: 'transport_reset',
    expectedAccountStatus: 'active',
  },
  {
    id: 'keep_alive_hosted_tool_http_failure',
    requestKind: 'responses_hosted_tool',
    failureMode: 'complete_http_failure',
    expectedAccountStatus: 'active',
  }
]

const scenarioById = new Map([...scenarios, ...keepAliveScenarios].map((scenario) => [scenario.id, scenario]))
const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-side-effect-replay-http-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'gateway-side-effect-replay-http-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.runtimeMode = 'standalone'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.runtimeStateDriver = 'memory'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  readWorkerPool,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('./f3-audit-direct-input-test-support.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const gatewayMiddlewareErrors: GatewayMiddlewareErrorDiagnostic[] = []
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
const captureGatewayMiddlewareError: ErrorRequestHandler = (error, req, res, next) => {
  gatewayMiddlewareErrors.push({
    scenarioId: typeof req.query.scenario === 'string' ? req.query.scenario : undefined,
    traceId: typeof req.headers['x-trace-id'] === 'string' ? req.headers['x-trace-id'] : undefined,
    errorName: error instanceof Error ? error.name : typeof error,
    errorMessage: error instanceof Error ? error.message : String(error),
    headersSent: res.headersSent,
    writableEnded: res.writableEnded,
    destroyed: res.destroyed
  })
  next(error)
}
app.use(captureGatewayMiddlewareError)

async function main(): Promise<void> {
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  const hits: UpstreamHit[] = []
  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
    const imagePermissionOwner = repositories.updateSystemAccount(access.systemAccountId, { imageGenerationEnabled: true })
    assert.equal(imagePermissionOwner?.imageGenerationEnabled, true, '副作用回归必须为图片场景显式开启系统账号图像权限')
    settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })

    upstreamServer = createUpstreamServer(hits)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`
    const fixtures = scenarios.map((scenario) => createScenarioFixture(scenario, upstreamBaseUrl))
    const keepAliveFixtures = keepAliveScenarios.map((scenario) => createScenarioFixture(scenario, upstreamBaseUrl))

    gatewayCache.clearGatewayRuntimeCache()
    appServer = http.createServer(app)
    await listen(appServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverPort(appServer)}`

    for (const fixture of fixtures) {
      await exerciseScenario(gatewayBaseUrl, fixture, hits)
    }
    await exerciseKeepAliveTransportResetSequence(gatewayBaseUrl, keepAliveFixtures, hits)

    console.log('统一候选切换真实 HTTP 回归通过：background、hosted tool、audio、image 的 HTTP 与 transport 失败均切换后备账户')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    try {
      await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    await removeTempRoot()
  }
}

function createUpstreamServer(hits: UpstreamHit[]): http.Server {
  return http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const scenarioId = url.searchParams.get('scenario') ?? ''
    const scenario = scenarioById.get(scenarioId)
    const authorization = String(req.headers.authorization ?? '')
    const hit: UpstreamHit = {
      scenarioId,
      authorization,
      path: url.pathname,
      bodyAccepted: false
    }
    hits.push(hit)
    req.resume()
    req.once('end', () => {
      hit.bodyAccepted = true
      if (!scenario) {
        res.writeHead(500, { 'content-type': 'application/json' })
        res.end('{"error":{"message":"unknown regression scenario"}}')
        return
      }

      const firstAuthorization = `Bearer ${scenarioApiKey(scenario, 'first')}`
      const fallbackAuthorization = `Bearer ${scenarioApiKey(scenario, 'fallback')}`
      if (authorization === firstAuthorization) {
        if (scenario.failureMode === 'transport_reset') {
          res.destroy()
          return
        }
        res.writeHead(scenario.failureStatusCode ?? 471, {
          'content-type': 'application/json; charset=utf-8',
          'x-vendor-secret': `vendor-header-${scenario.id}`
        })
        res.end(JSON.stringify({
          error: {
            type: `vendor_type_${scenario.id}`,
            code: scenario.failureErrorCode ?? `vendor_code_${scenario.id}`,
            message: `${policyMarker(scenario)} vendor-private-error-${scenario.id}; provider claims request never started`
          }
        }))
        return
      }

      if (authorization !== fallbackAuthorization) {
        res.writeHead(401, { 'content-type': 'application/json' })
        res.end('{"error":{"message":"unexpected regression credential"}}')
        return
      }
      sendFallbackSuccess(res, scenario)
    })
  })
}

function sendFallbackSuccess(res: http.ServerResponse, scenario: ReplayScenario): void {
  if (scenario.requestKind === 'audio_speech') {
    res.writeHead(200, { 'content-type': 'audio/mpeg' })
    res.end(`mock-audio-fallback-${scenario.id}`)
    return
  }
  if (scenario.requestKind === 'image_generation') {
    res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
    res.end(JSON.stringify({
      created: 1,
      data: [{ b64_json: Buffer.from(`mock-image-fallback-${scenario.id}`, 'utf8').toString('base64') }]
    }))
    return
  }
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: `resp_fallback_${scenario.id}`,
    object: 'response',
    status: 'completed',
    output: [{
      id: `msg_${scenario.id}`,
      type: 'message',
      status: 'completed',
      role: 'assistant',
      content: [{ type: 'output_text', text: `fallback-success-${scenario.id}`, annotations: [] }]
    }]
  }))
}

function createScenarioFixture(scenario: ReplayScenario, upstreamBaseUrl: string): ScenarioFixture {
  const group = repositories.createGroup({
    name: `副作用重放-${scenario.id}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  const firstApiKey = scenarioApiKey(scenario, 'first')
  const fallbackApiKey = scenarioApiKey(scenario, 'fallback')
  const firstAccount = createAccount({
    groupId: group.id,
    name: `01-${scenario.id}-first`,
    apiKey: firstApiKey,
    baseUrl: upstreamBaseUrl,
    priority: 0,
    policyAction: scenario.policyAction,
    policyMarker: policyMarker(scenario)
  })
  const fallbackAccount = createAccount({
    groupId: group.id,
    name: `02-${scenario.id}-fallback`,
    apiKey: fallbackApiKey,
    baseUrl: upstreamBaseUrl,
    priority: 10,
    fallbackEnabled: true
  })
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `副作用重放 Key-${scenario.id}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active',
    description: 'side-effect replay real HTTP regression'
  }, access)
  assert(apiKey.key)
  return {
    scenario,
    apiKey: apiKey.key,
    firstAccountId: firstAccount.id,
    fallbackAccountId: fallbackAccount.id,
    firstAuthorization: `Bearer ${firstApiKey}`,
    fallbackAuthorization: `Bearer ${fallbackApiKey}`
  }
}

function createAccount(input: {
  groupId: string
  name: string
  apiKey: string
  baseUrl: string
  priority: number
  fallbackEnabled?: boolean
  policyAction?: AccountErrorHandlingRuleAction
  policyMarker?: string
}) {
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: input.name,
    type: 'api_key',
    credentials: {
      api_key: input.apiKey,
      base_url: input.baseUrl,
      error_handling_rules: input.policyAction
        ? [{
            enabled: true,
            name: `用户显式策略-${input.name}`,
            priority: 1,
            status_codes: [471],
            keywords: [requiredPolicyMarker(input.policyMarker)],
            action: input.policyAction
          }]
        : undefined
    },
    groupId: input.groupId,
    supportedModels: ['gpt-4o-mini', 'gpt-image-1'],
    healthCheckModel: 'gpt-4o-mini',
    status: 'active',
    schedulable: true,
    concurrencyLimit: 1,
    priority: input.priority,
    fallbackEnabled: input.fallbackEnabled
  }, access)
  repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  return account
}

async function exerciseScenario(
  gatewayBaseUrl: string,
  fixture: ScenarioFixture,
  allHits: UpstreamHit[]
): Promise<void> {
  const { scenario } = fixture
  const traceId = `side-effect-replay-${scenario.id}-${Date.now()}`
  const middlewareErrorOffset = gatewayMiddlewareErrors.length
  const requestUrl = `${gatewayBaseUrl}${scenarioPath(scenario)}?scenario=${encodeURIComponent(scenario.id)}`
  let response: Response
  try {
    response = await fetch(requestUrl, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${fixture.apiKey}`,
        connection: 'close',
        'content-type': 'application/json',
        'x-trace-id': traceId
      },
      body: JSON.stringify(scenarioBody(scenario))
    })
  } catch (error) {
    throw new Error(
      `${scenario.id} 网关请求在响应头前失败：${requestUrl}；middleware=${gatewayMiddlewareDiagnostics(middlewareErrorOffset)}`,
      { cause: error }
    )
  }
  let responseText: string
  try {
    responseText = await response.text()
  } catch (error) {
    throw new Error(
      `${scenario.id} 网关已返回 HTTP ${response.status}，但响应正文读取中断；middleware=${gatewayMiddlewareDiagnostics(middlewareErrorOffset)}`,
      { cause: error }
    )
  }
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  await auditLogQueue.flushAllAuditLogQueueAsync()
  assert.deepEqual(
    gatewayMiddlewareErrors.slice(middlewareErrorOffset),
    [],
    `${scenario.id} 网关不得把内部异常交给 Express finalhandler 销毁客户端 socket`
  )

  const hits = allHits.filter((hit) => hit.scenarioId === scenario.id)
  const firstHits = hits.filter((hit) => hit.authorization === fixture.firstAuthorization)
  const fallbackHits = hits.filter((hit) => hit.authorization === fixture.fallbackAuthorization)
  assert.equal(hits.length, 2, `${scenario.id} 必须只命中首账户和后备账户；gateway HTTP ${response.status}: ${responseText}`)
  assert.deepEqual(
    hits.map((hit) => hit.authorization),
    [fixture.firstAuthorization, fixture.fallbackAuthorization],
    `${scenario.id} 上游命中顺序或数量错误`
  )
  assert.equal(
    firstHits.length,
    1,
    `${scenario.id} 必须且只能命中首账户一次；gateway HTTP ${response.status}: ${responseText}`
  )
  assert.equal(firstHits[0]?.bodyAccepted, true, `${scenario.id} 首账户必须完整接收请求体后才制造失败`)
  assert.equal(firstHits[0]?.path, scenarioPath(scenario), `${scenario.id} 必须真实命中目标端点`)
  assert.equal(fallbackHits.length, 1, `${scenario.id} 后备账户命中数错误`)
  assert.equal(response.status, 200, `${scenario.id} 当前候选未交付结果时应成功切换：${responseText}`)
  if (scenario.requestKind === 'image_generation') {
    assert.match(responseText, /"data":\[\{"b64_json":"/, `${scenario.id} 必须返回后备账户图片正文`)
  } else {
    assert.match(responseText, new RegExp(`fallback-(?:success|${scenario.id})`), `${scenario.id} 必须返回后备账户成功正文`)
  }
  assert.equal(fallbackHits[0]?.bodyAccepted, true, `${scenario.id} 后备账户必须真实接收请求体`)

  const firstAccount = repositories.findAccountForTest(fixture.firstAccountId, access)
  const fallbackAccount = repositories.findAccountForTest(fixture.fallbackAccountId, access)
  assert.equal(firstAccount?.status, scenario.expectedAccountStatus, `${scenario.id} 首账户状态动作未落地`)
  assert.equal(fallbackAccount?.status, 'active', `${scenario.id} 未命中的后备账户必须保持 active`)
}

async function exerciseKeepAliveTransportResetSequence(
  gatewayBaseUrl: string,
  fixtures: ScenarioFixture[],
  allHits: UpstreamHit[]
): Promise<void> {
  assert.equal(fixtures.length, 2, 'keep-alive 专项必须使用两组全新隔离 fixture')
  const agent = new http.Agent({ keepAlive: true, maxSockets: 1, maxFreeSockets: 1 })
  let firstSocketLocalPort: number | undefined
  try {
    for (const fixture of fixtures) {
      const { scenario } = fixture
      const traceId = `side-effect-keep-alive-${scenario.id}-${Date.now()}`
      const middlewareErrorOffset = gatewayMiddlewareErrors.length
      const hitOffset = allHits.length
      const response = await rawGatewayPost(
        `${gatewayBaseUrl}${scenarioPath(scenario)}?scenario=${encodeURIComponent(scenario.id)}`,
        JSON.stringify(scenarioBody(scenario)),
        {
          authorization: `Bearer ${fixture.apiKey}`,
          'content-type': 'application/json',
          'x-trace-id': traceId
        },
        agent
      )
      assert.equal(response.statusCode, 200, `${scenario.id} keep-alive 请求必须由后备账户完成：${response.text}`)
      assert.match(response.text, new RegExp(`fallback-(?:success|${scenario.id})`), `${scenario.id} keep-alive 请求必须返回后备账户正文`)
      const newHits = allHits.slice(hitOffset).filter((hit) => hit.scenarioId === scenario.id)
      assert.deepEqual(
        newHits.map((hit) => hit.authorization),
        [fixture.firstAuthorization, fixture.fallbackAuthorization],
        `${scenario.id} keep-alive 请求必须按首账户、后备账户顺序执行`
      )
      assert.equal(newHits[0]?.bodyAccepted, true, `${scenario.id} keep-alive 请求体必须已被上游完整接收`)
      assert.equal(newHits[1]?.bodyAccepted, true, `${scenario.id} keep-alive 后备账户必须完整接收请求体`)
      assert.deepEqual(
        gatewayMiddlewareErrors.slice(middlewareErrorOffset),
        [],
        `${scenario.id} keep-alive 请求不得产生 Express 末端异常`
      )
      if (firstSocketLocalPort === undefined) {
        firstSocketLocalPort = response.socketLocalPort
        assert(firstSocketLocalPort, '首个 keep-alive 请求必须记录客户端 socket 端口')
      } else {
        assert.equal(response.socketLocalPort, firstSocketLocalPort, '连续副作用请求必须复用同一个 keep-alive socket')
      }
    }
  } finally {
    agent.destroy()
  }
}

function rawGatewayPost(
  url: string,
  body: string,
  headers: Record<string, string>,
  agent: http.Agent
): Promise<RawGatewayResponse> {
  return new Promise((resolvePromise, rejectPromise) => {
    let settled = false
    const request = http.request(url, {
      method: 'POST',
      agent,
      headers: {
        ...headers,
        'content-length': String(Buffer.byteLength(body))
      }
    })
    const finishError = (error: Error) => {
      if (settled) return
      settled = true
      request.destroy()
      rejectPromise(error)
    }
    request.setTimeout(5_000, () => finishError(new Error(`keep-alive 网关请求超时：${url}`)))
    request.once('error', finishError)
    request.once('response', (response) => {
      const chunks: Buffer[] = []
      response.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
      response.once('aborted', () => finishError(new Error(`keep-alive 网关响应正文中断：${url}`)))
      response.once('error', finishError)
      response.once('end', () => {
        if (settled) return
        settled = true
        resolvePromise({
          statusCode: response.statusCode ?? 0,
          text: Buffer.concat(chunks).toString('utf8'),
          socketLocalPort: request.socket?.localPort
        })
      })
    })
    request.end(body)
  })
}

function gatewayMiddlewareDiagnostics(offset: number): string {
  return JSON.stringify(gatewayMiddlewareErrors.slice(offset))
}

function scenarioPath(scenario: ReplayScenario): string {
  if (scenario.requestKind === 'audio_speech') return '/v1/audio/speech'
  if (scenario.requestKind === 'image_generation') return '/v1/images/generations'
  return '/v1/responses'
}

function scenarioBody(scenario: ReplayScenario): Record<string, unknown> {
  if (scenario.requestKind === 'audio_speech') {
    return {
      model: 'gpt-4o-mini',
      input: `audio side effect ${scenario.id}`,
      voice: 'alloy'
    }
  }
  if (scenario.requestKind === 'responses_background') {
    return {
      model: 'gpt-4o-mini',
      input: `background side effect ${scenario.id}`,
      background: true,
      stream: false
    }
  }
  if (scenario.requestKind === 'image_generation') {
    return {
      model: 'gpt-image-1',
      prompt: `image side effect ${scenario.id}`,
      size: '1024x1024'
    }
  }
  return {
    model: 'gpt-4o-mini',
    input: `hosted tool side effect ${scenario.id}`,
    tools: [{ type: 'web_search' }],
    stream: false
  }
}

function scenarioApiKey(scenario: ReplayScenario, role: 'first' | 'fallback'): string {
  return `sk-side-effect-${scenario.id}-${role}`
}

function policyMarker(scenario: ReplayScenario): string {
  return `explicit-policy-marker-${scenario.id}`
}

function requiredPolicyMarker(value: string | undefined): string {
  assert(value, '显式策略测试账户必须提供唯一正文匹配标记')
  return value
}

async function listen(server: http.Server): Promise<void> {
  await new Promise<void>((resolveListen, rejectListen) => {
    server.once('error', rejectListen)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', rejectListen)
      resolveListen()
    })
  })
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolveClose, rejectClose) => {
    server.close((error) => error ? rejectClose(error) : resolveClose())
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address === 'object', 'server address unavailable')
  return address.port
}

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (!(error instanceof Error) || !/EBUSY|EPERM/.test(error.message)) throw error
      await delay(200)
    }
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
