import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { ModelCheckRunDetail } from '../../domain/types.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-full-profile-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-full-profile-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const upstream = createMockUpstream()
let stopGatewayJsonParseWorker: (() => Promise<void>) | undefined

try {
  await listen(upstream)
  const [
    repositories,
    { createMockGatewayFixture },
    { runModelCheck },
    usageRecordQueue,
    gatewayJsonParser
  ] = await Promise.all([
    import('../../storage/repositories.js'),
    import('../maintenance/mockdata/fixtures.js'),
    import('../../modules/model-checks/model-checks.service.js'),
    import('../../modules/gateway/usage/record-queue.service.js'),
    import('../../modules/gateway/request/json-parser.js')
  ])
  stopGatewayJsonParseWorker = gatewayJsonParser.stopGatewayJsonParseWorker

  const fixture = createMockGatewayFixture({
    label: '模型检测完整闭环',
    upstreamBaseUrl: `http://127.0.0.1:${serverPort(upstream)}/v1`,
    systemAccountId: 'sys_admin',
    accountCount: 2
  })
  const account = fixture.accounts[0]
  const secondAccount = fixture.accounts[1]
  assert(account, 'mock fixture should create a target account')
  assert(secondAccount, 'mock fixture should create a second group account')

  const progressItemKeys: string[] = []
  const accountRun = await runModelCheck({
    targetType: 'account',
    targetId: account.id,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: false
  }, { systemAccountId: 'sys_admin', role: 'admin' }, undefined, (event) => {
    if ('itemKey' in event) progressItemKeys.push(event.itemKey)
  })
  await assertRunShape(accountRun, {
    targetType: 'account',
    targetId: account.id,
    expectedAccountId: account.id,
    highConfidence: true
  })
  assert.equal(accountRun.accountId, account.id, '账户目标报告应记录被测账号 ID')
  assert(!accountRun.checks.some((item) => item.itemType === 'model_catalog'), '模型检测报告不应再包含本地模型目录检测项')
  assert(!progressItemKeys.some((itemKey) => itemKey.endsWith('.model_catalog')), '模型检测进度不应再执行本地模型目录探针')

  usageRecordQueue.flushUsageRecordQueue({ drain: true, retryOnFailure: false })
  const runTraceIds = new Set(accountRun.checks.map((check) => check.traceId).filter((traceId): traceId is string => Boolean(traceId)))
  const usageRows = repositories.listUsageRecords({ systemAccountId: 'sys_admin', role: 'admin' }, {
    page: 1,
    pageSize: 200
  }).items.filter((row) => runTraceIds.has(row.traceId))
  assert(usageRows.length > 0, '账户检测应产生可按 traceId 追溯的真实使用记录')
  assert(usageRows.some((row) => row.accountId === account.id), '账户检测的模型请求应命中被测账号')
  assert(!usageRows.some((row) => row.accountId && row.accountId !== account.id), '账户目标检测不应静默切到同分组其他账号')
  assert(!usageRows.some((row) => row.accountId === secondAccount.id), '账户目标检测不应命中第二个分组账号')

  console.log('模型检测完整 profile 回归通过：AI 账户目标闭环，辅助模型对照与长上下文探针通过')
} finally {
  await stopGatewayJsonParseWorker?.()
  await closeServer(upstream)
}

async function assertRunShape(run: ModelCheckRunDetail, options: {
  targetType: 'account'
  targetId: string
  expectedAccountId?: string
  highConfidence: boolean
}): Promise<void> {
  assert.equal(run.status, 'completed')
  assert.equal(run.targetType, options.targetType)
  assert.equal(run.targetId, options.targetId)
  assert.equal(run.level, options.highConfidence ? 'high_confidence' : run.level)
  assert(run.score >= 90, `完整检测分数应足够高，actual=${run.score}`)
  assert(run.checks.some((item) => item.itemKey === 'target.long_context' && item.status === 'passed'), '完整检测应包含并通过长上下文探针')
  assert(run.checks.some((item) => item.itemKey === 'target.cross_model' && item.status === 'passed'), '完整检测应包含并通过辅助模型对照')
  assert(run.checks.some((item) => item.itemKey === 'target.stability' && item.status === 'passed'), '完整检测应包含并通过稳定性探针')
  assert(!run.checks.some((item) => item.itemType === 'model_catalog'), '完整检测不应把本地模型目录作为可信度证据')
  assert(run.checks.every((item) => item.evidenceSummary.modelMismatch !== true), '通过场景不应出现模型不匹配证据')
  const serialized = JSON.stringify(run)
  assert(!serialized.includes('sk-mockdata'), '检测报告不应泄露 mock 上游 API Key')
  assert(!serialized.includes('Authorization'), '检测报告不应泄露 Authorization 头')
  if (options.expectedAccountId) {
    assert.equal(run.accountId, options.expectedAccountId)
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
    id: 'resp_model_check_full_profile',
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
