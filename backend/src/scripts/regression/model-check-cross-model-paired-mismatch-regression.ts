import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-cross-paired-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-cross-paired-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const targetModel = 'gpt-5.5'
const pairedModel = 'gpt-5.4'
const pairedResponseModel = 'gpt-5.4-mini-2026-03-17'
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
    import('../../modules/gateway/openai-gateway-json-parser.js')
  ])
  stopGatewayJsonParseWorker = gatewayJsonParser.stopGatewayJsonParseWorker

  const fixture = createMockGatewayFixture({
    label: '模型检测辅助模型对照误判',
    upstreamBaseUrl: `http://127.0.0.1:${serverPort(upstream)}/v1`,
    systemAccountId: 'sys_admin',
    accountCount: 1
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
  assert.equal(detail.level, 'likely', '辅助模型对照不匹配只能降低可信度，不能把目标模型直接判为疑似不符')
  assert(detail.score >= 75, `目标探针通过时总分应保持较可信区间，actual=${detail.score}`)
  assert(!/响应模型字段与请求模型不一致/.test(detail.message), '总览消息不应把辅助模型不匹配描述成目标模型字段不一致')

  const targetMismatches = detail.checks.filter((item) => item.itemKey !== 'target.cross_model' && item.evidenceSummary.modelMismatch === true)
  assert.equal(targetMismatches.length, 0, '目标模型自身探针全部返回目标模型时不应记录目标模型不匹配证据')

  const crossModelItem = detail.checks.find((item) => item.itemKey === 'target.cross_model')
  assert(crossModelItem, '完整检测应包含辅助模型对照项')
  assert.equal(crossModelItem.status, 'failed', '辅助模型返回变体时辅助模型对照项应失败并扣分')
  assert.equal(crossModelItem.evidenceSummary.pairedResponseModel, pairedResponseModel)
  assert.equal(crossModelItem.evidenceSummary.pairedModelMismatch, true)
  assert.equal(crossModelItem.evidenceSummary.crossModelMismatch, true)
  assert.notEqual(crossModelItem.evidenceSummary.modelMismatch, true, '辅助模型不匹配不应写成目标模型硬不匹配证据')

  console.log('模型检测辅助模型对照回归通过：目标模型正常时不会被辅助对照误判为疑似不符')
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
          data: [
            { id: targetModel, object: 'model', created: 0, owned_by: 'mock' },
            { id: pairedModel, object: 'model', created: 0, owned_by: 'mock' }
          ]
        })
        return
      }
      if (req.method === 'POST' && url.pathname === '/v1/responses') {
        const outputText = outputForProbe(body)
        const model = responseModelForBody(body)
        if (body.stream === true) {
          sendStream(res, model, outputText)
        } else {
          sendJson(res, responsePayload(model, outputText, body))
        }
        return
      }
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'mock path not found' } }))
    })
  })
}

function responseModelForBody(body: Record<string, unknown>): string {
  return body.model === pairedModel ? pairedResponseModel : targetModel
}

function responsePayload(model: string, outputText: string, body: Record<string, unknown>): Record<string, unknown> {
  const hasTool = Array.isArray(body.tools)
  return {
    id: 'resp_model_check_cross_paired',
    object: 'response',
    status: 'completed',
    model,
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
