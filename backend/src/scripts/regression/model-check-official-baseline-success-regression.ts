import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-baseline-success-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'model-check-baseline-success-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })

const upstream = createMockUpstream()
let stopGatewayJsonParseWorker: (() => Promise<void>) | undefined

try {
  await listen(upstream)
  const [
    repositories,
    { createMockGatewayFixture },
    { getModelCheckOptions, runModelCheck },
    gatewayJsonParser
  ] = await Promise.all([
    import('../../storage/repositories.js'),
    import('../maintenance/mockdata-fixtures.js'),
    import('../../modules/model-checks/model-checks.service.js'),
    import('../../modules/gateway/openai-gateway-json-parser.js')
  ])
  stopGatewayJsonParseWorker = gatewayJsonParser.stopGatewayJsonParseWorker

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const targetFixture = createMockGatewayFixture({
    label: '模型检测目标',
    upstreamBaseUrl: `http://127.0.0.1:${serverPort(upstream)}/v1`,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const baselineFixture = createMockGatewayFixture({
    label: '官网基线',
    upstreamBaseUrl: `http://127.0.0.1:${serverPort(upstream)}/v1`,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const targetAccount = targetFixture.accounts[0]
  const baselineAccount = baselineFixture.accounts[0]
  assert(targetAccount, 'target account should exist')
  assert(baselineAccount, 'baseline account should exist')

  const updatedBaseline = repositories.updateAccount(baselineAccount.id, {
    name: '官网基线 OpenAI 模型检测账号',
    credentials: {
      api_key: 'sk-baseline-regression',
      base_url: `http://127.0.0.1:${serverPort(upstream)}/v1`,
      model_check_baseline: true
    },
    groupId: baselineFixture.group.id,
    status: 'active',
    schedulable: true
  }, access)
  assert(updatedBaseline, 'baseline account should update')

  const options = getModelCheckOptions(access)
  assert.equal(options.officialBaseline.enabledByDefault, false, '官网对照仍必须默认关闭')
  assert.equal(options.officialBaseline.available, true, '配置基线账户后 options 应报告可用')

  const detail = await runModelCheck({
    targetType: 'account',
    targetId: targetAccount.id,
    model: 'gpt-5.5',
    profile: 'full',
    officialBaseline: true
  }, access)

  assert.equal(detail.status, 'completed')
  assert.equal(detail.officialBaseline, true)
  assert.equal(detail.officialBaselineAvailable, true)
  assert.equal(detail.level, 'high_confidence', '官网对照通过后应允许高可信')
  assert(detail.checks.some((item) => item.itemKey === 'official_baseline.responses_basic' && item.status === 'passed'), '应记录官网基线基础探针')
  assert(detail.checks.some((item) => item.itemKey === 'official_baseline.long_context' && item.status === 'passed'), '官网基线也应执行长上下文探针')
  assert(detail.checks.some((item) => item.itemKey === 'official_baseline.comparison' && item.status === 'passed'), '应记录官网基线对照汇总项')
  assert(!JSON.stringify(detail).includes('sk-baseline-regression'), '官网对照报告不应泄露基线 API Key')

  console.log('模型检测官网对照成功回归通过：显式开启后执行目标与官网基线同探针集')
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
    id: 'resp_model_check_baseline_success',
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
