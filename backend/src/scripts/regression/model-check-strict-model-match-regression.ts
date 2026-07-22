import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-strict-match-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-strict-match-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const responseModel = 'gpt-5.4-mini-2026-03-17'
const targetModel = 'gpt-5.4'
const mappedRequestModel = 'gpt-5.5'
const mappedUpstreamModel = 'gpt-5.6-sol'
const upstream = createMockUpstream()
let upstreamRequestCount = 0
let stopGatewayJsonParseWorker: (() => Promise<void>) | undefined

try {
  await listen(upstream)
  const [
    { createMockGatewayFixture },
    { runModelCheck },
    repositories,
    gatewayJsonParser,
    { buildModelMatchEvidence }
  ] = await Promise.all([
    import('../maintenance/mockdata/fixtures.js'),
    import('../../modules/model-checks/model-checks.service.js'),
    import('../../storage/repositories.js'),
    import('../../modules/gateway/request/json-parser.js'),
    import('../../modules/model-checks/model-checks-parsing.js')
  ])
  stopGatewayJsonParseWorker = gatewayJsonParser.stopGatewayJsonParseWorker

  const fixture = createMockGatewayFixture({
    label: '模型检测严格模型匹配',
    upstreamBaseUrl: `http://127.0.0.1:${serverPort(upstream)}/v1`,
    systemAccountId: 'sys_admin',
    accountCount: 2,
    createApiKey: false
  })
  const account = fixture.accounts[0]
  const mappedAccount = fixture.accounts[1]
  assert(account, 'mock fixture should create an account')
  assert(mappedAccount, 'mock fixture should create a mapped account')

  const quickRequestCountBefore = upstreamRequestCount
  const quickDetail = await runModelCheck({
    targetType: 'account',
    targetId: account.id,
    model: targetModel,
    profile: 'quick',
    trustedComparison: false
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  assert.equal(quickDetail.level, 'suspicious', '快速检测遇到明确模型字段冲突时必须判为疑似不符')
  assert.equal(upstreamRequestCount - quickRequestCountBefore, 1, '快速检测发现基础模型字段冲突后必须立即结束')
  assert.deepEqual(quickDetail.checks.map((item) => item.itemType).sort(), ['responses_basic', 'usage_shape'].sort(), '快速模型字段冲突不应继续执行第二个轻探针')

  const detail = await runModelCheck({
    targetType: 'account',
    targetId: account.id,
    model: targetModel,
    profile: 'full',
    trustedComparison: false
  }, { systemAccountId: 'sys_admin', role: 'admin' })

  assert.equal(detail.status, 'completed')
  assert.equal(detail.level, 'suspicious', '变体模型响应不能被判为较可信或高可信')
  assert.match(detail.message, /不一致|降级/, '总览消息应提示模型字段不一致')
  const trustReport = detail.resultSummary.trustReport as Record<string, unknown> | undefined
  assert.equal(trustReport?.mappingStatus, 'undeclared_mismatch', '未声明响应模型冲突必须与显式映射分开')
  assert.equal(trustReport?.identityStatus, 'suspected_downgrade', '未声明模型冲突应形成疑似降级身份结论')
  assert.equal(trustReport?.usageIntegrityStatus, 'insufficient_evidence', '只有 usage 形态时不能冒充 Token 诚信结论')

  const mismatchItems = detail.checks.filter((item) => item.evidenceSummary.modelMismatch === true)
  assert(mismatchItems.length >= 5, '关键 Responses 探针都应记录模型不匹配证据')
  assert(mismatchItems.every((item) => item.status === 'failed'), '模型字段明显不匹配时关键探针应失败')
  assert(
    mismatchItems.some((item) => item.itemKey === 'target.responses_basic' && item.evidenceSummary.responseModel === responseModel),
    '基础 Responses 探针应保存脱敏后的实际 response model'
  )
  assert(!JSON.stringify(detail).includes('sk-mockdata'), '检测报告不应泄露上游 API Key')

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  repositories.updateAccount(mappedAccount.id, {
    supportedModels: [mappedUpstreamModel],
    modelMappings: [{
      sourceModel: mappedRequestModel,
      sourceEndpointFamily: 'responses',
      upstreamModel: mappedUpstreamModel,
      upstreamEndpointFamily: 'responses',
      enabled: true
    }]
  }, access)
  const mappedDetail = await runModelCheck({
    targetType: 'account',
    targetId: mappedAccount.id,
    model: mappedRequestModel,
    profile: 'full',
    trustedComparison: false
  }, access)
  assert.equal(mappedDetail.status, 'completed')
  assert.equal(
    mappedDetail.checks.some((item) => item.itemKey.startsWith('target.') && item.evidenceSummary.modelMismatch === true),
    false,
    '合法模型映射不应被模型检测误判为返回模型不一致'
  )
  const mappedBasic = mappedDetail.checks.find((item) => item.itemKey === 'target.responses_basic')
  assert(mappedBasic, '映射模型检测应生成基础 Responses 检测项')
  assert.equal(mappedBasic.evidenceSummary.requestModel, mappedRequestModel, '模型检测证据应保留请求模型')
  assert.equal(mappedBasic.evidenceSummary.upstreamModel, mappedUpstreamModel, '模型检测证据应保留实际上游模型')
  assert.equal(mappedBasic.evidenceSummary.expectedModel, mappedUpstreamModel, '映射命中后应按实际上游模型校验返回模型')
  assert.equal(mappedBasic.evidenceSummary.responseModel, mappedRequestModel, '映射命中后应允许上游返回公开请求模型名')
  assert.equal(mappedBasic.evidenceSummary.modelMappingApplied, true, '模型检测证据应明确标记模型映射已命中')
  const mappedTrustReport = mappedDetail.resultSummary.trustReport as Record<string, unknown> | undefined
  assert.equal(mappedTrustReport?.mappingStatus, 'configured_mapping', '显式模型映射必须展示为已配置映射')
  assert.notEqual(mappedTrustReport?.identityStatus, 'suspected_downgrade', '显式映射不能被身份维度误判为降级')
  assert.equal(buildModelMatchEvidence('gpt-unrelated', mappedUpstreamModel, {
    requestModel: mappedRequestModel,
    upstreamModel: mappedUpstreamModel,
    modelMappingApplied: true
  }).modelMismatch, true, '映射场景返回第三个无关模型时仍必须判定不一致')

  console.log('模型检测严格模型匹配回归通过：变体模型不会冒充目标模型，合法映射允许请求模型或映射目标模型')
} finally {
  await stopGatewayJsonParseWorker?.()
  await closeServer(upstream)
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
          data: [{ id: targetModel, object: 'model', created: 0, owned_by: 'mock' }]
        })
        return
      }
      if (req.method === 'POST' && url.pathname === '/v1/responses') {
        upstreamRequestCount += 1
        const outputText = outputForProbe(body)
        const actualResponseModel = responseModelForBody(body)
        if (body.stream === true) {
          sendStream(res, outputText, actualResponseModel)
        } else {
          sendJson(res, responsePayload(body, outputText, actualResponseModel))
        }
        return
      }
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'mock path not found' } }))
    })
  })
}

function responsePayload(body: Record<string, unknown>, outputText: string, actualResponseModel: string): Record<string, unknown> {
  const hasTool = Array.isArray(body.tools)
  return {
    id: 'resp_model_check_strict_match',
    object: 'response',
    status: 'completed',
    model: actualResponseModel,
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
      input_tokens: 8,
      output_tokens: 3,
      total_tokens: 11
    }
  }
}

function sendStream(res: http.ServerResponse, outputText: string, actualResponseModel: string): void {
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
      model: actualResponseModel,
      usage: {
        input_tokens: 8,
        output_tokens: 3,
        total_tokens: 11
      }
    }
  })}\n\n`)
  res.end()
}

function responseModelForBody(body: Record<string, unknown>): string {
  return body.model === mappedUpstreamModel ? mappedRequestModel : responseModel
}

function outputForProbe(body: Record<string, unknown>): string {
  const text = JSON.stringify(body).toUpperCase()
  if (text.includes('STREAM-OK')) return 'STREAM-OK'
  if (text.includes('QUARTZ')) return 'QUARTZ'
  if (text.includes('VECTOR')) return 'VECTOR'
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
