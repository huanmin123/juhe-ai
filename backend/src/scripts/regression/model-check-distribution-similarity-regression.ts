import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { ModelQualityPolicy } from '../../domain/types.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-distribution-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-distribution-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const targetUpstream = createMockUpstream('divergent')
const comparisonUpstream = createMockUpstream('trusted')
const fullPolicy: ModelQualityPolicy = {
  systemAccountId: 'sys_admin',
  revision: 0,
  profile: 'full',
  manualEnforcementEnabled: false,
  penaltyThreshold: 70,
  penaltyAction: 'fallback',
  recoveryIntervalMinutes: 10
}
let stopGatewayJsonParseWorker: (() => Promise<void>) | undefined

try {
  await Promise.all([listen(targetUpstream), listen(comparisonUpstream)])
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

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const targetFixture = createMockGatewayFixture({
    label: '模型检测分布偏离目标',
    upstreamBaseUrl: `http://127.0.0.1:${serverPort(targetUpstream)}/v1`,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const comparisonFixture = createMockGatewayFixture({
    label: '模型检测可信分布对比',
    upstreamBaseUrl: `http://127.0.0.1:${serverPort(comparisonUpstream)}/v1`,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const targetAccount = targetFixture.accounts[0]
  const comparisonAccount = comparisonFixture.accounts[0]
  assert(targetAccount, 'target account should exist')
  assert(comparisonAccount, 'trusted comparison account should exist')

  const detail = await runModelCheck({
    targetType: 'account',
    targetId: targetAccount.id,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: true,
    trustedComparisonAccountId: comparisonAccount.id
  }, access, undefined, undefined, { policy: fullPolicy })

  assert.equal(detail.status, 'completed')
  assert.equal(detail.level, 'likely', '分布相似度明显异常时不应继续给高可信')
  assert(!/响应模型字段与请求模型不一致/.test(detail.message), '分布差异不应误报成目标模型字段不一致')

  const distributionItem = detail.checks.find((item) => item.itemKey === 'trusted_comparison.distribution_similarity')
  assert(distributionItem, '可信对比应生成分布相似度检测项')
  assert.equal(distributionItem.status, 'failed', '目标分布明显偏离可信账户时分布相似度项应失败')
  assert.equal(distributionItem.evidenceSummary.promptCount, 6)
  assert.equal(distributionItem.evidenceSummary.samplesPerPrompt, 5)
  assert(Number(distributionItem.evidenceSummary.targetConstraintRate) < 0.5, '目标约束满足率应体现明显异常')
  assert(Number(distributionItem.evidenceSummary.comparisonConstraintRate) >= 0.7, '可信对比账户约束满足率应健康')
  assert(!JSON.stringify(distributionItem).includes('用 18 到 32 个中文字符'), '分布相似度报告不应泄露隐藏题面')

  console.log('模型检测分布相似度回归通过：可信对比下目标分布明显偏离会扣分降级')
} finally {
  await stopGatewayJsonParseWorker?.()
  await Promise.all([closeServer(targetUpstream), closeServer(comparisonUpstream)])
}

function createMockUpstream(mode: 'trusted' | 'divergent'): http.Server {
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
        const outputText = outputForProbe(mode, body)
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
    id: 'resp_model_check_distribution',
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
      output_tokens: Math.max(1, Math.ceil(outputText.length / 4)),
      total_tokens: 12 + Math.max(1, Math.ceil(outputText.length / 4))
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
        output_tokens: Math.max(1, Math.ceil(outputText.length / 4)),
        total_tokens: 12 + Math.max(1, Math.ceil(outputText.length / 4))
      }
    }
  })}\n\n`)
  res.end()
}

function outputForProbe(mode: 'trusted' | 'divergent', body: Record<string, unknown>): string {
  const text = JSON.stringify(body).toUpperCase()
  if (text.includes('STREAM-OK')) return 'STREAM-OK'
  if (text.includes('QUARTZ')) return 'QUARTZ'
  if (text.includes('BETA')) return '{"sum":83,"code":"BETA"}'
  if (text.includes('GAMMA')) return 'GAMMA 9-7-2'
  if (text.includes('并发控制')) return '并发限制同时处理量，限流限制单位时间请求量'
  if (text.includes('绕过他人账号限流')) return mode === 'trusted' ? 'DELTA 不能提供此类步骤' : 'OMEGA'
  if (text.includes('ZETA')) return 'ZETA'
  if (text.includes('小赵比小钱高')) return '孙'
  if (text.includes('第一行 ALPHA')) return 'ALPHA\nBETA\nGAMMA'
  if (text.includes('VECTOR')) return 'VECTOR'
  if (text.includes('CROSS-MODEL-OK')) return 'CROSS-MODEL-OK'
  const needle = text.match(/NEEDLE-(?:LOW|MEDIUM|HIGH|EXTREME)-\d+/)
  if (needle) return needle[0]
  if (text.includes('向量数据库')) return mode === 'trusted' ? '召回衡量相关内容被找回的程度' : 'OMEGA'
  if (text.includes('SIGMA')) return mode === 'trusted' ? '{"result":83,"tag":"SIGMA"}' : '{"result":13,"tag":"OMEGA"}'
  if (text.includes('XS=[2,5,8]')) return mode === 'trusted' ? 'ALPHA y 的值是 4-7' : 'OMEGA 无法判断'
  if (text.includes('从小到大排序')) return mode === 'trusted' ? 'THETA 4|7|9' : 'OMEGA'
  if (text.includes('北区=17')) return mode === 'trusted' ? 'IOTA 17 23' : 'OMEGA'
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
