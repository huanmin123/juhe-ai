import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { OPENAI_COMPATIBLE_PROVIDER_CODE, OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'
import type { ResponseInspectionPolicyMatch } from '../../storage/response-inspection-policy.repository.js'

type PolicyLayer = 'management' | 'account'
type FieldScenarioName =
  | 'outputTextIncludes'
  | 'outputTextExcludes'
  | 'errorCodes'
  | 'errorTypes'
  | 'errorMessageIncludes'
  | 'finishReasons'
  | 'jsonPathsExists'
  | 'rawTextIncludes'

interface FieldScenario {
  name: FieldScenarioName
  transport: 'json' | 'sse'
  expected: 'retry' | 'pass'
  buildMatch: (scenarioId: string) => ResponseInspectionPolicyMatch
}

interface UpstreamHit {
  path: string
  authorization: string
  bodyText: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-response-inspection-mock-ai-fields-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'response-inspection-mock-ai-fields.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'response-inspection-mock-ai-fields-secret'
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
  responseInspectionPolicies,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/response-inspection-policy.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: UpstreamHit[] = []
const scenariosById = new Map<string, FieldScenario>()

const fieldScenarios: FieldScenario[] = [
  {
    name: 'outputTextIncludes',
    transport: 'json',
    expected: 'retry',
    buildMatch: (scenarioId) => ({ outputTextIncludes: [markerFor(scenarioId)] })
  },
  {
    name: 'outputTextExcludes',
    transport: 'json',
    expected: 'pass',
    buildMatch: (scenarioId) => ({
      outputTextIncludes: [includeMarkerFor(scenarioId)],
      outputTextExcludes: [excludeMarkerFor(scenarioId)]
    })
  },
  {
    name: 'errorCodes',
    transport: 'json',
    expected: 'retry',
    buildMatch: (scenarioId) => ({ errorCodes: [markerFor(scenarioId)] })
  },
  {
    name: 'errorTypes',
    transport: 'json',
    expected: 'retry',
    buildMatch: (scenarioId) => ({ errorTypes: [markerFor(scenarioId)] })
  },
  {
    name: 'errorMessageIncludes',
    transport: 'json',
    expected: 'retry',
    buildMatch: (scenarioId) => ({ errorMessageIncludes: [markerFor(scenarioId)] })
  },
  {
    name: 'finishReasons',
    transport: 'json',
    expected: 'retry',
    buildMatch: (scenarioId) => ({ finishReasons: [markerFor(scenarioId)] })
  },
  {
    name: 'jsonPathsExists',
    transport: 'json',
    expected: 'retry',
    buildMatch: (scenarioId) => ({ jsonPathsExists: [jsonPathFor(scenarioId)] })
  },
  {
    name: 'rawTextIncludes',
    transport: 'sse',
    expected: 'retry',
    buildMatch: (scenarioId) => ({ rawTextIncludes: [markerFor(scenarioId)] })
  }
]

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createMockOpenAIUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    for (const layer of ['management', 'account'] as const) {
      for (const scenario of fieldScenarios) {
        await runFieldScenario(baseUrl, upstreamBaseUrl, layer, scenario)
      }
    }

    console.log('response inspection mock ai field regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function runFieldScenario(
  baseUrl: string,
  upstreamBaseUrl: string,
  layer: PolicyLayer,
  scenario: FieldScenario
): Promise<void> {
  upstreamHits.length = 0
  gatewayCache.clearGatewayRuntimeCache()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()

  const scenarioId = scenarioIdFor(layer, scenario.name)
  scenariosById.set(scenarioId, scenario)
  const match = scenario.buildMatch(scenarioId)
  if (layer === 'management') {
    responseInspectionPolicies.createResponseInspectionPolicy({
      name: `mock ai ${scenarioId}`,
      enabled: true,
      priority: 1,
      scopeType: 'provider',
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      match,
      action: 'retry_next_account',
      notes: 'mock ai response inspection field regression'
    })
    gatewayCache.clearGatewayRuntimeCache()
  }

  const group = repositories.createGroup({
    name: `mock ai ${scenarioId}`,
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    name: `mock ai polluted ${scenarioId}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-mock-polluted-${scenarioId}`,
      base_url: upstreamBaseUrl,
      ...(layer === 'account'
        ? {
            response_inspection_rules: [{
              enabled: true,
              name: `account local ${scenario.name}`,
              priority: 1,
              match,
              action: 'retry_next_account'
            }]
          }
        : {})
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    name: `mock ai clean ${scenarioId}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-mock-clean-${scenarioId}`,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: `mock ai key ${scenarioId}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, `${scenarioId} missing gateway api key`)

  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: `run ${scenarioId}`,
      stream: scenario.transport === 'sse'
    })
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, `${scenarioId} expected HTTP 200, got ${response.status}: ${responseText}`)
  assert.equal(upstreamHits.some((hit) => hit.bodyText.includes(markerFor(scenarioId))), false, `${scenarioId} request body should not contain mock pollution marker`)

  if (scenario.expected === 'retry') {
    assert.equal(upstreamHits.length, 2, `${scenarioId} should retry from polluted account to clean account`)
    assert.equal(upstreamHits[0]?.authorization, `Bearer sk-mock-polluted-${scenarioId}`, `${scenarioId} first upstream hit should use polluted account`)
    assert.equal(upstreamHits[1]?.authorization, `Bearer sk-mock-clean-${scenarioId}`, `${scenarioId} second upstream hit should use clean account`)
    assert(responseText.includes(`clean ${scenarioId}`), `${scenarioId} final response should come from clean account: ${responseText}`)
    assert(!responseText.includes(markerFor(scenarioId)), `${scenarioId} final response should not leak matched mock marker`)
    await assertResponseInspectionAudit(group.id, layer, scenario, scenarioId)
    return
  }

  assert.equal(upstreamHits.length, 1, `${scenarioId} outputTextExcludes should suppress interception and avoid retry`)
  assert.equal(upstreamHits[0]?.authorization, `Bearer sk-mock-polluted-${scenarioId}`, `${scenarioId} pass scenario should only use polluted account`)
  assert(responseText.includes(includeMarkerFor(scenarioId)), `${scenarioId} pass response should include positive include marker`)
  assert(responseText.includes(excludeMarkerFor(scenarioId)), `${scenarioId} pass response should include exclude marker`)
  assert(!responseText.includes(`clean ${scenarioId}`), `${scenarioId} pass scenario should not switch accounts`)
}

async function assertResponseInspectionAudit(
  groupId: string,
  layer: PolicyLayer,
  scenario: FieldScenario,
  scenarioId: string
): Promise<void> {
  await auditLogQueue.flushAllAuditLogQueueAsync()
  const logs = repositories.listAuditLogs({
    groupId,
    pageSize: 5,
    trafficSource: 'gateway'
  }).items
  assert.equal(logs.length, 1, `${scenarioId} should create one gateway audit log`)
  assert.equal(logs[0]?.auditOutcome, 'success_after_retry', `${scenarioId} audit outcome should record success_after_retry`)
  const detail = repositories.getAuditLogDetail(logs[0]!.id)
  assert(detail, `${scenarioId} should have audit detail`)
  const metadata = await readGatewayMetadataPayloads(detail.id, detail.payloads.map((payload) => payload.id))
  const policyMetadata = metadata.find((item) =>
    item.responsePolicyMatched === true
    && item.responseInspectionIntercepted === true
    && item.policySource === layer
    && item.matchedField === scenario.name
    && item.matchedValue === expectedMatchedValue(scenario, scenarioId)
  )
  assert(policyMetadata, `${scenarioId} should audit ${layer} ${scenario.name} interception, got ${JSON.stringify(metadata)}`)
}

async function readGatewayMetadataPayloads(auditLogId: string, payloadIds: string[]): Promise<Array<Record<string, unknown>>> {
  const output: Array<Record<string, unknown>> = []
  for (const payloadId of payloadIds) {
    const payload = await repositories.getAuditLogPayload(auditLogId, payloadId, { offset: 0, limit: 64 * 1024 })
    if (payload?.partType !== 'gateway_metadata' || !payload.bodyText) continue
    const parsed = parseJsonObject(payload.bodyText)
    const metadata = parsed ? objectValue(parsed.metadata) : undefined
    if (metadata) output.push(metadata)
  }
  return output
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const authorization = String(req.headers.authorization ?? '')
      const path = req.url?.split('?', 1)[0] ?? ''
      upstreamHits.push({ path, authorization, bodyText })
      if (path !== '/v1/responses') {
        res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'mock upstream path not found' } }))
        return
      }
      const scenarioId = scenarioIdFromAuthorization(authorization)
      const scenario = scenariosById.get(scenarioId)
      assert(scenario, `unknown mock ai scenario: ${scenarioId}`)
      const polluted = authorization.includes('polluted')
      if (scenario.transport === 'sse') {
        sendResponsesSse(res, scenario, scenarioId, polluted)
        return
      }
      sendResponsesJson(res, scenario, scenarioId, polluted)
    })
  })
}

function sendResponsesJson(
  res: http.ServerResponse,
  scenario: FieldScenario,
  scenarioId: string,
  polluted: boolean
): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  if (!polluted) {
    res.end(JSON.stringify(cleanJsonResponse(scenarioId)))
    return
  }
  res.end(JSON.stringify(pollutedJsonResponse(scenario, scenarioId)))
}

function sendResponsesSse(
  res: http.ServerResponse,
  scenario: FieldScenario,
  scenarioId: string,
  polluted: boolean
): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  if (polluted && scenario.name === 'rawTextIncludes') {
    res.write(`event: vendor.mock\ndata: ${JSON.stringify({
      type: 'vendor.mock',
      marker: markerFor(scenarioId)
    })}\n\n`)
  } else {
    res.write(`event: response.output_text.delta\ndata: ${JSON.stringify({
      type: 'response.output_text.delta',
      delta: polluted ? `polluted ${markerFor(scenarioId)}` : `clean ${scenarioId}`
    })}\n\n`)
  }
  res.write(`event: response.completed\ndata: ${JSON.stringify({
    type: 'response.completed',
    response: {
      id: `resp-${scenarioId}`,
      status: 'completed',
      usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
    }
  })}\n\n`)
  res.end()
}

function cleanJsonResponse(scenarioId: string): Record<string, unknown> {
  return {
    id: `resp-${scenarioId}`,
    status: 'completed',
    output_text: `clean ${scenarioId}`,
    usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
  }
}

function pollutedJsonResponse(scenario: FieldScenario, scenarioId: string): Record<string, unknown> {
  const marker = markerFor(scenarioId)
  if (scenario.name === 'outputTextIncludes') {
    return {
      id: `resp-${scenarioId}`,
      status: 'completed',
      output_text: `polluted ${marker}`,
      usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
    }
  }
  if (scenario.name === 'outputTextExcludes') {
    return {
      id: `resp-${scenarioId}`,
      status: 'completed',
      output_text: `polluted ${includeMarkerFor(scenarioId)} ${excludeMarkerFor(scenarioId)}`,
      usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
    }
  }
  if (scenario.name === 'errorCodes') {
    return {
      id: `resp-${scenarioId}`,
      error: { code: marker, type: 'mock_type', message: 'mock response error' },
      usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
    }
  }
  if (scenario.name === 'errorTypes') {
    return {
      id: `resp-${scenarioId}`,
      error: { code: 'mock_code', type: marker, message: 'mock response error' },
      usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
    }
  }
  if (scenario.name === 'errorMessageIncludes') {
    return {
      id: `resp-${scenarioId}`,
      error: { code: 'mock_code', type: 'mock_type', message: `mock response error ${marker}` },
      usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
    }
  }
  if (scenario.name === 'finishReasons') {
    return {
      id: `resp-${scenarioId}`,
      status: marker,
      output_text: `status ${marker}`,
      usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
    }
  }
  if (scenario.name === 'jsonPathsExists') {
    return {
      id: `resp-${scenarioId}`,
      status: 'completed',
      output_text: 'json path mock payload',
      vendor_payload: {
        [scenarioId]: {
          present: true
        }
      },
      usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
    }
  }
  throw new Error(`unsupported json scenario: ${scenario.name}`)
}

function scenarioIdFor(layer: PolicyLayer, name: FieldScenarioName): string {
  return `${layer}_${name.replace(/[A-Z]/g, (letter) => `_${letter.toLowerCase()}`)}`
}

function scenarioIdFromAuthorization(authorization: string): string {
  const match = authorization.match(/sk-mock-(?:polluted|clean)-([a-z_]+)/)
  assert(match?.[1], `cannot resolve mock scenario from Authorization: ${authorization}`)
  return match[1]
}

function markerFor(scenarioId: string): string {
  return `mock_marker_${scenarioId}`
}

function includeMarkerFor(scenarioId: string): string {
  return `mock_include_${scenarioId}`
}

function excludeMarkerFor(scenarioId: string): string {
  return `mock_exclude_${scenarioId}`
}

function jsonPathFor(scenarioId: string): string {
  return `vendor_payload.${scenarioId}.present`
}

function expectedMatchedValue(scenario: FieldScenario, scenarioId: string): string {
  if (scenario.name === 'jsonPathsExists') return jsonPathFor(scenarioId)
  return markerFor(scenarioId)
}

function parseJsonObject(text: string): Record<string, unknown> | undefined {
  try {
    const value = JSON.parse(text) as unknown
    return objectValue(value)
  } catch {
    return undefined
  }
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
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
  assert(typeof address === 'object' && address !== null, 'server is not listening')
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
