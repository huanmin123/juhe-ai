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
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const responseModel = 'gpt-5.4-mini-2026-03-17'
const targetModel = 'gpt-5.4'
const upstream = createMockUpstream()
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
    import('../../modules/gateway/request/json-parser.js')
  ])
  stopGatewayJsonParseWorker = gatewayJsonParser.stopGatewayJsonParseWorker

  const fixture = createMockGatewayFixture({
    label: '模型检测严格模型匹配',
    upstreamBaseUrl: `http://127.0.0.1:${serverPort(upstream)}/v1`,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const account = fixture.accounts[0]
  assert(account, 'mock fixture should create an account')

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

  const mismatchItems = detail.checks.filter((item) => item.evidenceSummary.modelMismatch === true)
  assert(mismatchItems.length >= 5, '关键 Responses 探针都应记录模型不匹配证据')
  assert(mismatchItems.every((item) => item.status === 'failed'), '模型字段明显不匹配时关键探针应失败')
  assert(
    mismatchItems.some((item) => item.itemKey === 'target.responses_basic' && item.evidenceSummary.responseModel === responseModel),
    '基础 Responses 探针应保存脱敏后的实际 response model'
  )
  assert(!JSON.stringify(detail).includes('sk-mockdata'), '检测报告不应泄露上游 API Key')

  console.log('模型检测严格模型匹配回归通过：gpt-5.4-mini 不会被误判为 gpt-5.4')
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
        const outputText = outputForProbe(body)
        if (body.stream === true) {
          sendStream(res, outputText)
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
    id: 'resp_model_check_strict_match',
    object: 'response',
    status: 'completed',
    model: responseModel,
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

function sendStream(res: http.ServerResponse, outputText: string): void {
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
      model: responseModel,
      usage: {
        input_tokens: 8,
        output_tokens: 3,
        total_tokens: 11
      }
    }
  })}\n\n`)
  res.end()
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
