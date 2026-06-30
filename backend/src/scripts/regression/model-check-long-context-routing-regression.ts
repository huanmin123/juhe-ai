import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { ModelCheckItemSummary } from '../../domain/types.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-long-context-routing-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-long-context-routing-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const targetModel = 'gpt-5.5'
const largeContextDowngradeThresholdChars = 18_000
const upstream = createLengthAwareUpstream()
let stopGatewayJsonParseWorker: (() => Promise<void>) | undefined

try {
  await listen(upstream)
  const [
    { createMockGatewayFixture },
    { runModelCheck },
    gatewayJsonParser
  ] = await Promise.all([
    import('../maintenance/mockdata/fixtures.js'),
    import('../../modules/model-checks/model-checks.service.js'),
    import('../../modules/gateway/request/json-parser.js')
  ])
  stopGatewayJsonParseWorker = gatewayJsonParser.stopGatewayJsonParseWorker

  const fixture = createMockGatewayFixture({
    label: '模型检测长上下文按长度降级',
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
  assert.equal(detail.level, 'uncertain', '短探针通过但长上下文按长度降级时不能判为较可信或高可信')
  assert.match(detail.message, /上下文|长度|降级/, '总览消息应提示长上下文或长度降级风险')
  const longContext = requiredCheck(detail.checks, 'target.long_context')
  assert.equal(longContext.status, 'warning', '部分长上下文窗口失败时聚合项应为 warning')
  assert.equal(longContext.evidenceSummary.modelMismatch, false, '本场景模拟模型字段伪装一致，不能依赖 model 字段发现问题')
  const summaries = Array.isArray(longContext.evidenceSummary.summaries)
    ? longContext.evidenceSummary.summaries as Array<Record<string, unknown>>
    : []
  assert(summaries.some((item) => item.key === 'context_8k' && item.foundNeedle === true), '8k 窗口应通过，证明短上下文伪装可以过关')
  assert(summaries.some((item) => item.key === 'context_20k' && item.foundNeedle === false), '20k 窗口应失败，识别按上下文长度降级')
  assert(summaries.some((item) => item.key === 'context_60k' && item.foundNeedle === false), '60k 窗口应失败，识别按上下文长度降级')
  assert(!['high_confidence', 'likely'].includes(detail.level), `长度降级场景不能输出可信结论：${detail.level}`)

  console.log('模型检测长上下文降级回归通过：短探针全绿但大窗口失败时结论降为不确定')
} finally {
  await stopGatewayJsonParseWorker?.()
  await closeServer(upstream)
}

function requiredCheck(checks: ModelCheckItemSummary[], itemKey: string): ModelCheckItemSummary {
  const check = checks.find((item) => item.itemKey === itemKey)
  assert(check, `检测报告应包含 ${itemKey}`)
  return check
}

function createLengthAwareUpstream(): http.Server {
  return http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => {
      chunks.push(Buffer.from(chunk))
    })
    req.on('end', () => {
      const rawBody = Buffer.concat(chunks).toString('utf8')
      const body = parseJson(rawBody)
      if (req.method === 'GET' && url.pathname === '/v1/models') {
        sendJson(res, {
          object: 'list',
          data: [
            { id: targetModel, object: 'model', created: 0, owned_by: 'mock' },
            { id: 'gpt-5.4', object: 'model', created: 0, owned_by: 'mock' }
          ]
        })
        return
      }
      if (req.method === 'POST' && url.pathname === '/v1/responses') {
        const outputText = outputForProbe(body, rawBody.length)
        if (body.stream === true) {
          sendStream(res, String(body.model ?? targetModel), outputText)
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
    id: 'resp_model_check_long_context_routing',
    object: 'response',
    status: 'completed',
    model: String(body.model ?? targetModel),
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
      input_tokens: Math.ceil(JSON.stringify(body).length / 4),
      output_tokens: 4,
      total_tokens: Math.ceil(JSON.stringify(body).length / 4) + 4
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

function outputForProbe(body: Record<string, unknown>, rawBodyLength: number): string {
  const text = JSON.stringify(body).toUpperCase()
  if (rawBodyLength > largeContextDowngradeThresholdChars) return 'LARGE-CONTEXT-DOWNGRADED'
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
  const needle = text.match(/NEEDLE-\d+-[A-Z]+/)
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
