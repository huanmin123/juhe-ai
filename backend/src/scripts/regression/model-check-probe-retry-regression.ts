import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { ModelCheckItemSummary } from '../../domain/types.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-probe-retry-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-probe-retry-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const retryState = {
  transientBasicAttempts: 0,
  transientStreamAttempts: 0,
  persistentBasicAttempts: 0
}
const upstream = createRetryAwareUpstream()
let stopGatewayJsonParseWorker: (() => Promise<void>) | undefined

try {
  await listen(upstream)
  const [
    { createMockGatewayFixture },
    { runModelCheck },
    gatewayJsonParser
  ] = await Promise.all([
    import('../maintenance/mockdata-fixtures.js'),
    import('../../modules/model-checks/model-checks.service.js'),
    import('../../modules/gateway/openai-gateway-json-parser.js')
  ])
  stopGatewayJsonParseWorker = gatewayJsonParser.stopGatewayJsonParseWorker

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstream)}/v1`

  const transientFixture = createMockGatewayFixture({
    label: 'model-check-retry-transient',
    upstreamBaseUrl,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const transientAccount = transientFixture.accounts[0]
  assert(transientAccount, 'mock fixture should create a transient target account')

  const transientRun = await runModelCheck({
    targetType: 'account',
    targetId: transientAccount.id,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: false
  }, access)
  const transientBasic = requiredCheck(transientRun.checks, 'target.responses_basic')
  const transientStream = requiredCheck(transientRun.checks, 'target.responses_stream')
  assert.equal(transientRun.level, 'high_confidence', '瞬态上游异常恢复后不应误判失败')
  assert.equal(retryState.transientBasicAttempts, 3, '瞬态异常 basic 探针应在同一账号上尝试三次')
  assert.equal(retryState.transientStreamAttempts, 3, '瞬态流式异常应在同一账号上尝试三次')
  assert.equal(transientBasic.status, 'passed', '第 3 次恢复后 basic 探针应通过')
  assert.equal(transientBasic.evidenceSummary.attemptCount, 3, `basic 探针应记录总尝试次数：${JSON.stringify(transientBasic.evidenceSummary)}`)
  assert.equal(transientBasic.evidenceSummary.retryAttemptCount, 2, 'basic 探针应记录重试次数')
  assert.deepEqual(transientBasic.evidenceSummary.attemptStatusCodes, [503, 503, 200], 'basic 探针应记录每次尝试状态码')
  assert.equal(transientStream.status, 'passed', '第 3 次恢复后流式探针应通过')
  assert.equal(transientStream.evidenceSummary.attemptCount, 3, '流式探针应记录总尝试次数')
  assert.deepEqual(transientStream.evidenceSummary.attemptStatusCodes, [503, 503, 200], '流式探针应记录每次 HTTP 状态码')

  const persistentFixture = createMockGatewayFixture({
    label: 'model-check-retry-persistent',
    upstreamBaseUrl,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const persistentAccount = persistentFixture.accounts[0]
  assert(persistentAccount, 'mock fixture should create a persistent target account')

  const persistentRun = await runModelCheck({
    targetType: 'account',
    targetId: persistentAccount.id,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: false
  }, access)
  const persistentBasic = requiredCheck(persistentRun.checks, 'target.responses_basic')
  assert.equal(persistentRun.level, 'unavailable', '连续重试仍失败时应落不可检测')
  assert.equal(retryState.persistentBasicAttempts, 3, '持续异常 basic 探针应达到最大尝试次数')
  assert.equal(persistentBasic.status, 'failed', '持续异常时 basic 探针应失败')
  assert.equal(persistentBasic.evidenceSummary.attemptCount, 3, '持续异常报告应记录总尝试次数')
  assert.equal(persistentBasic.evidenceSummary.retryAttemptCount, 2, '持续异常报告应记录重试次数')
  assert.deepEqual(persistentBasic.evidenceSummary.attemptStatusCodes, [503, 503, 503], '持续异常报告应记录全部失败状态码')
  assert(!persistentRun.checks.some((item) => item.itemKey === 'target.behavior_probe'), 'basic 连续失败后不应继续执行重型行为探针')

  console.log('模型检测探针重试回归通过：瞬态异常同账号重试后恢复，持续异常三次后失败')
} finally {
  await stopGatewayJsonParseWorker?.()
  await closeServer(upstream)
}

function requiredCheck(checks: ModelCheckItemSummary[], itemKey: string): ModelCheckItemSummary {
  const check = checks.find((item) => item.itemKey === itemKey)
  assert(check, `检测报告应包含 ${itemKey}`)
  return check
}

function createRetryAwareUpstream(): http.Server {
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
        const authorization = String(req.headers.authorization ?? '').toLowerCase()
        const bodyText = JSON.stringify(body).toUpperCase()
        if (bodyText.includes('OK-MODEL-CHECK')) {
          if (authorization.includes('model-check-retry-transient')) {
            retryState.transientBasicAttempts += 1
            if (retryState.transientBasicAttempts <= 2) {
              sendError(res, '模拟瞬态上游异常')
              return
            }
          }
          if (authorization.includes('model-check-retry-persistent')) {
            retryState.persistentBasicAttempts += 1
            sendError(res, '模拟持续上游异常')
            return
          }
        }
        if (body.stream === true && bodyText.includes('STREAM-OK') && authorization.includes('model-check-retry-transient')) {
          retryState.transientStreamAttempts += 1
          if (retryState.transientStreamAttempts <= 2) {
            sendStreamFailure(res, String(body.model ?? 'gpt-5.5'), '模拟流式临时异常')
            return
          }
        }
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
    id: 'resp_model_check_probe_retry',
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

function sendStreamFailure(res: http.ServerResponse, _model: string, message: string): void {
  sendError(res, message)
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

function sendJson(res: http.ServerResponse, body: unknown): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify(body))
}

function sendError(res: http.ServerResponse, message: string): void {
  res.writeHead(503, { 'content-type': 'application/json' })
  res.end(JSON.stringify({ error: { message } }))
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
