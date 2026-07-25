import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { AccountErrorHandlingRuleAction } from '../../modules/accounts/account-error-policy-validation.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

type RequestKind = 'responses_background' | 'responses_hosted_tool' | 'audio_speech'
type FailureMode = 'transport_reset' | 'complete_http_failure'

interface ReplayScenario {
  id: string
  requestKind: RequestKind
  failureMode: FailureMode
  policyAction?: AccountErrorHandlingRuleAction
  expectedAccountStatus: 'active' | 'temporary_unavailable' | 'error'
  replayExpected: boolean
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

const scenarios: ReplayScenario[] = [
  {
    id: 'background_transport_reset',
    requestKind: 'responses_background',
    failureMode: 'transport_reset',
    expectedAccountStatus: 'active',
    replayExpected: false
  },
  {
    id: 'background_no_policy_http_failure',
    requestKind: 'responses_background',
    failureMode: 'complete_http_failure',
    expectedAccountStatus: 'active',
    replayExpected: false
  },
  {
    id: 'background_explicit_cooldown',
    requestKind: 'responses_background',
    failureMode: 'complete_http_failure',
    policyAction: 'temp_unschedulable',
    expectedAccountStatus: 'temporary_unavailable',
    replayExpected: false
  },
  {
    id: 'background_explicit_retry_next',
    requestKind: 'responses_background',
    failureMode: 'complete_http_failure',
    policyAction: 'retry_next',
    expectedAccountStatus: 'active',
    replayExpected: false
  },
  {
    id: 'hosted_tool_transport_reset',
    requestKind: 'responses_hosted_tool',
    failureMode: 'transport_reset',
    expectedAccountStatus: 'active',
    replayExpected: false
  },
  {
    id: 'hosted_tool_no_policy_http_failure',
    requestKind: 'responses_hosted_tool',
    failureMode: 'complete_http_failure',
    expectedAccountStatus: 'active',
    replayExpected: false
  },
  {
    id: 'hosted_tool_explicit_disable',
    requestKind: 'responses_hosted_tool',
    failureMode: 'complete_http_failure',
    policyAction: 'error_disabled',
    expectedAccountStatus: 'error',
    replayExpected: false
  },
  {
    id: 'hosted_tool_explicit_retry_next',
    requestKind: 'responses_hosted_tool',
    failureMode: 'complete_http_failure',
    policyAction: 'retry_next',
    expectedAccountStatus: 'active',
    replayExpected: false
  },
  {
    id: 'audio_transport_reset',
    requestKind: 'audio_speech',
    failureMode: 'transport_reset',
    expectedAccountStatus: 'active',
    replayExpected: false
  },
  {
    id: 'audio_no_policy_http_failure',
    requestKind: 'audio_speech',
    failureMode: 'complete_http_failure',
    expectedAccountStatus: 'active',
    replayExpected: false
  },
  {
    id: 'audio_explicit_cooldown',
    requestKind: 'audio_speech',
    failureMode: 'complete_http_failure',
    policyAction: 'temp_unschedulable',
    expectedAccountStatus: 'temporary_unavailable',
    replayExpected: false
  },
  {
    id: 'audio_explicit_retry_next',
    requestKind: 'audio_speech',
    failureMode: 'complete_http_failure',
    policyAction: 'retry_next',
    expectedAccountStatus: 'active',
    replayExpected: false
  }
]
assert.equal(scenarios.length, 12, '副作用真实 HTTP 回归必须保留三类请求各四个场景')

const scenarioById = new Map(scenarios.map((scenario) => [scenario.id, scenario]))
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
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

async function main(): Promise<void> {
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  const hits: UpstreamHit[] = []
  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
    settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })

    upstreamServer = createUpstreamServer(hits)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`
    const fixtures = scenarios.map((scenario) => createScenarioFixture(scenario, upstreamBaseUrl))

    gatewayCache.clearGatewayRuntimeCache()
    appServer = http.createServer(app)
    await listen(appServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverPort(appServer)}`

    for (const fixture of fixtures) {
      await exerciseScenario(gatewayBaseUrl, fixture, hits)
    }

    console.log('副作用请求真实 HTTP 重放回归通过：background、hosted tool、audio 在 transport、完整 HTTP 与任意显式策略下均保持 at-most-once')
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
        res.writeHead(471, {
          'content-type': 'application/json; charset=utf-8',
          'x-vendor-secret': `vendor-header-${scenario.id}`
        })
        res.end(JSON.stringify({
          error: {
            type: `vendor_type_${scenario.id}`,
            code: `vendor_code_${scenario.id}`,
            message: `${policyMarker(scenario)} vendor-private-error-${scenario.id}`
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
    supportedModels: ['gpt-4o-mini'],
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
  const response = await fetch(`${gatewayBaseUrl}${scenarioPath(scenario)}?scenario=${encodeURIComponent(scenario.id)}`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${fixture.apiKey}`,
      'content-type': 'application/json',
      'x-trace-id': traceId
    },
    body: JSON.stringify(scenarioBody(scenario))
  })
  const responseText = await response.text()
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  await auditLogQueue.flushAllAuditLogQueueAsync()

  const hits = allHits.filter((hit) => hit.scenarioId === scenario.id)
  const firstHits = hits.filter((hit) => hit.authorization === fixture.firstAuthorization)
  const fallbackHits = hits.filter((hit) => hit.authorization === fixture.fallbackAuthorization)
  assert.equal(hits.length, scenario.replayExpected ? 2 : 1, `${scenario.id} 不得出现额外 Key/账户请求`)
  assert.deepEqual(
    hits.map((hit) => hit.authorization),
    scenario.replayExpected
      ? [fixture.firstAuthorization, fixture.fallbackAuthorization]
      : [fixture.firstAuthorization],
    `${scenario.id} 上游命中顺序或数量错误`
  )
  assert.equal(
    firstHits.length,
    1,
    `${scenario.id} 必须且只能命中首账户一次；gateway HTTP ${response.status}: ${responseText}`
  )
  assert.equal(firstHits[0]?.bodyAccepted, true, `${scenario.id} 首账户必须完整接收请求体后才制造失败`)
  assert.equal(firstHits[0]?.path, scenarioPath(scenario), `${scenario.id} 必须真实命中目标端点`)
  assert.equal(fallbackHits.length, scenario.replayExpected ? 1 : 0, `${scenario.id} 后备账户命中数错误`)

  if (scenario.replayExpected) {
    assert.equal(response.status, 200, `${scenario.id} 显式 retry_next 应成功切换：${responseText}`)
    assert.match(responseText, new RegExp(`fallback-(?:success|${scenario.id})`), `${scenario.id} 必须返回后备账户成功正文`)
    assert.equal(fallbackHits[0]?.bodyAccepted, true, `${scenario.id} 后备账户必须真实接收请求体`)
  } else {
    assert.equal(response.status, 503, `${scenario.id} 不可重放结果应返回稳定 503：${responseText}`)
    const errorPayload = parseJsonObject(responseText).error
    assert(isRecord(errorPayload), `${scenario.id} 必须返回结构化网关错误：${responseText}`)
    assert.equal(errorPayload.type, 'upstream_outcome_unknown', `${scenario.id} 必须返回稳定 error.type`)
    assert.equal(errorPayload.code, 'upstream_outcome_unknown', `${scenario.id} 必须返回稳定 error.code`)
    assert.equal(errorPayload.message, '上游可能已接收请求，但结果未知；网关未自动重放', `${scenario.id} 不得把副作用请求误写成图片请求`)
    assert.doesNotMatch(responseText, /图片/, `${scenario.id} 客户端文案不得误称图片请求`)
    assert.doesNotMatch(responseText, /vendor-private-error|vendor_code_|vendor_type_|471/, `${scenario.id} 不得泄漏供应商原错`)
    assert.equal(response.headers.get('x-vendor-secret'), null, `${scenario.id} 不得透传供应商私有错误头`)
    const audit = repositories.listAuditLogs({ traceId, pageSize: 10 }).items.at(0)
    assert(audit, `${scenario.id} 必须写入可审计的网关失败记录`)
    const replayBlock = (await gatewayMetadataPayloads(audit.id)).find((entry) => entry.label === 'upstream_automatic_replay_blocked')
    assert(replayBlock, `${scenario.id} 必须使用通用自动重放阻止审计标签`)
    assert.equal(replayBlock.metadata?.requestLane, 'text', `${scenario.id} 审计必须保留当前请求 lane`)
    assert.equal(replayBlock.metadata?.endpoint, `POST ${scenarioPath(scenario)}`, `${scenario.id} 审计必须保留 endpoint`)
  }

  const firstAccount = repositories.findAccountForTest(fixture.firstAccountId, access)
  const fallbackAccount = repositories.findAccountForTest(fixture.fallbackAccountId, access)
  assert.equal(firstAccount?.status, scenario.expectedAccountStatus, `${scenario.id} 首账户状态动作未落地`)
  assert.equal(fallbackAccount?.status, 'active', `${scenario.id} 未命中的后备账户必须保持 active`)
}

function scenarioPath(scenario: ReplayScenario): string {
  return scenario.requestKind === 'audio_speech' ? '/v1/audio/speech' : '/v1/responses'
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

async function gatewayMetadataPayloads(auditLogId: string): Promise<Array<{
  label?: string
  metadata?: Record<string, unknown>
}>> {
  const detail = repositories.getAuditLogDetail(auditLogId)
  if (!detail) return []
  const payloads = await Promise.all(detail.payloads
    .filter((payload) => payload.partType === 'gateway_metadata')
    .map((payload) => repositories.getAuditLogPayload(auditLogId, payload.id)))
  return payloads
    .map((payload) => parseJsonObject(payload?.bodyText ?? ''))
    .filter((payload) => payload.type === 'gateway_metadata')
    .map((payload) => ({
      label: typeof payload.label === 'string' ? payload.label : undefined,
      metadata: isRecord(payload.metadata) ? payload.metadata : undefined
    }))
}

function parseJsonObject(value: string): Record<string, unknown> {
  try {
    const parsed: unknown = JSON.parse(value)
    return isRecord(parsed) ? parsed : {}
  } catch {
    return {}
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
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
  await new Promise<void>((resolveClose) => server.close(() => resolveClose()))
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
