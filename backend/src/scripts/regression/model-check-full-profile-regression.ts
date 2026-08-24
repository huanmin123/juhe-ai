import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { ModelCheckRunDetail, ModelQualityPolicy } from '../../domain/types.js'
import { stopModelCheckTokenWorker } from '../../modules/model-checks/model-checks-token-worker.service.js'

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
const fullPolicy: ModelQualityPolicy = {
  systemAccountId: 'sys_admin',
  revision: 0,
  profile: 'full',
  manualEnforcementEnabled: false,
  penaltyThreshold: 70,
  penaltyAction: 'fallback',
  recoveryIntervalMinutes: 10
}
let upstreamResponseRequestCount = 0
let tokenIntegrityRequestCount = 0
let gpt56JuiceRequestCount = 0
let forceGpt56JuiceMismatch = false
let observedStreamFlags: boolean[] = []
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
  repositories.updateAccount(account.id, {
    supportedModels: ['gpt-5.5', 'gpt-5.4', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna']
  }, { systemAccountId: 'sys_admin', role: 'admin' })

  const quickRun = await runModelCheck({
    targetType: 'account',
    targetId: account.id,
    model: 'gpt-5.6-sol'
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  assert.equal(quickRun.status, 'completed')
  assert.equal(quickRun.profile, 'quick', '省略 profile 时必须使用快速检测')
  assert(!quickRun.checks.some((item) => item.itemKey === 'target.gpt56_juice'), 'GPT-5.6 快速检测不得执行 Juice 专项探针')
  assert.equal(gpt56JuiceRequestCount, 0, 'GPT-5.6 快速检测不得向上游发送 Juice 专项请求')
  assert.equal(upstreamResponseRequestCount, 8, '快速检测应只使用账户选定形态执行基础、同形态语义、结构化、工具、三点 Token 与配对跨模型请求')
  assert(observedStreamFlags.length > 0 && observedStreamFlags.every(Boolean), 'Responses SSE 账户的全部快速探针必须使用流式形态')
  for (const itemKey of ['target.responses_basic', 'target.responses_stream', 'target.structured_output', 'target.tool_calling', 'target.usage_shape', 'target.token_integrity', 'target.cross_model']) {
    assert(quickRun.checks.some((item) => item.itemKey === itemKey), `快速检测必须包含 ${itemKey}`)
  }
  assert.equal(quickRun.checks.filter((item) => item.itemType === 'token_integrity').length, 1, '快速检测 Token 诊断必须聚合为一个报告项')
  const quickTokenIntegrity = quickRun.checks.find((item) => item.itemKey === 'target.token_integrity')
  assert(quickTokenIntegrity, '快速检测必须保留 Token 诚信诊断结果')
  assert.equal(quickTokenIntegrity.status, 'skipped', '快速检测只采集一轮 Token 样本，必须如实标记为证据不足')
  assert.equal(quickTokenIntegrity.score, 0, '快速检测的 Token 诊断不得形成评分或处罚证据')
  assert.equal(quickTokenIntegrity.maxScore, 0, '快速检测的 Token 诊断不得进入质量评分')
  assert.equal(quickTokenIntegrity.evidenceSummary.diagnosticOnly, true, '快速检测的 Token 诊断必须明确标记为诊断性证据')
  assert(!quickRun.checks.some((item) => ['behavior_probe', 'long_context', 'stability', 'distribution_similarity'].includes(item.itemType)), '快速检测不得执行行为、长上下文、稳定性或分布探针')
  assert.equal(quickRun.resultSummary.trustedComparison, false)
  assert.notEqual(quickRun.level, 'high_confidence', '快速检测不允许输出高可信结论')
  repositories.updateAccount(account.id, { healthCheckEndpointMode: 'responses_json' }, { systemAccountId: 'sys_admin', role: 'admin' })
  observedStreamFlags = []
  const jsonModeRun = await runModelCheck({
    targetType: 'account',
    targetId: account.id,
    model: 'gpt-5.6-sol'
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  assert.equal(jsonModeRun.requestSummary.healthCheckEndpointMode, 'responses_json', '检测记录必须保存账户选定的 JSON 请求形态')
  assert(observedStreamFlags.length > 0 && observedStreamFlags.every((stream) => !stream), 'Responses JSON 账户不得被模型检测附带请求 SSE')
  assert(!jsonModeRun.checks.some((item) => item.itemKey === 'target.responses_stream'), 'JSON 主形态不得出现 SSE 探针项')

  repositories.updateAccount(account.id, { healthCheckEndpointMode: 'responses_sse' }, { systemAccountId: 'sys_admin', role: 'admin' })
  upstreamResponseRequestCount = 0
  tokenIntegrityRequestCount = 0
  observedStreamFlags = []
  forceGpt56JuiceMismatch = true

  const progressItemKeys: string[] = []
  const accountRun = await runModelCheck({
    targetType: 'account',
    targetId: account.id,
    model: 'gpt-5.6-luna',
    profile: 'full',
    trustedComparison: false
  }, { systemAccountId: 'sys_admin', role: 'admin' }, undefined, (event) => {
    if ('itemKey' in event) progressItemKeys.push(event.itemKey)
  }, { policy: fullPolicy })
  forceGpt56JuiceMismatch = false
  await assertRunShape(accountRun, {
    targetType: 'account',
    targetId: account.id,
    expectedAccountId: account.id,
    highConfidence: false,
    minimumScore: 60
  })
  assert.equal(accountRun.accountId, account.id, '账户目标报告应记录被测账号 ID')
  assert.equal(tokenIntegrityRequestCount, 9, '完整检测 Token 诚信探针必须保持三轮共 9 个物理请求')
  assert(!accountRun.checks.some((item) => item.itemType === 'model_catalog'), '模型检测报告不应再包含本地模型目录检测项')
  assert(!progressItemKeys.some((itemKey) => itemKey.endsWith('.model_catalog')), '模型检测进度不应再执行本地模型目录探针')

  await usageRecordQueue.flushUsageRecordQueueAsync({ drain: true, retryOnFailure: false })
  const runTraceIds = new Set(accountRun.checks.map((check) => check.traceId).filter((traceId): traceId is string => Boolean(traceId)))
  const usageRows = repositories.listUsageRecords({ systemAccountId: 'sys_admin', systemAccountFilterId: 'sys_admin', role: 'admin' }, {
    page: 1,
    pageSize: 200
  }).items.filter((row) => runTraceIds.has(row.traceId))
  assert(usageRows.length > 0, '账户检测应产生可按 traceId 追溯的真实使用记录')
  assert(usageRows.some((row) => row.accountId === account.id), '账户检测的模型请求应命中被测账号')
  assert(!usageRows.some((row) => row.accountId && row.accountId !== account.id), '账户目标检测不应静默切到同分组其他账号')
  assert(!usageRows.some((row) => row.accountId === secondAccount.id), '账户目标检测不应命中第二个分组账号')

  const juiceItem = accountRun.checks.find((item) => item.itemKey === 'target.gpt56_juice')
  assert(juiceItem, 'GPT-5.6 深度检测必须生成 Juice 专项汇总项')
  assert.equal(accountRun.level, 'suspicious', 'Juice 命中其他 GPT-5.6 签名时必须标记疑似混用')
  assert.equal(juiceItem.status, 'failed', 'Juice 混用必须保留失败专项项')
  assert.equal(juiceItem.maxScore, 0, 'Juice 专项不得改写通用评分标尺')
  assert.equal(juiceItem.evidenceSummary.hardAnomaly, true, 'Juice 混用必须保留硬异常证据')
  assert.equal(juiceItem.evidenceSummary.strongAnomaly, true, '三个高档 Juice 变体一致混用必须保留强异常证据')
  assert.equal(juiceItem.evidenceSummary.scorePenalty, 25, '强 Juice 异常必须固定扣减 25 分')
  assert.equal(accountRun.score, 66, '该稳定 mock 场景的通用分数应被 Juice 强异常固定扣减 25 分')
  assert.equal(gpt56JuiceRequestCount, 6, 'GPT-5.6 深度检测必须发送六个 Juice 专项请求')
  const juiceContract = accountRun.requestSummary.gpt56Juice as Record<string, unknown> | undefined
  assert.equal(juiceContract?.version, 'gpt56-juice-v2', '报告必须记录 Juice 专项契约版本')
  assert.match(String(juiceContract?.hash ?? ''), /^[a-f0-9]{64}$/, '报告必须记录 Juice 专项契约哈希')
  assert(accountRun.probeSetVersion.includes('gpt56-juice-v2'), 'GPT-5.6 深度检测必须把 Juice 版本并入探针集版本')
  assert.equal(accountRun.qualityDecision?.triggered, true, '强 Juice 异常扣分后低于处罚阈值时必须触发现有质量策略')
  assert(accountRun.qualityDecision?.reasonCodes.includes('gpt56_juice_strong_anomaly'), '强 Juice 异常必须写入质量决策原因')

  console.log('模型检测完整 profile 回归通过：AI 账户目标闭环、HTTP 200 内容评分与 GPT-5.6 Juice 专项契约通过')
} finally {
  await stopModelCheckTokenWorker()
  await stopGatewayJsonParseWorker?.()
  await closeServer(upstream)
}

async function assertRunShape(run: ModelCheckRunDetail, options: {
  targetType: 'account'
  targetId: string
  expectedAccountId?: string
  highConfidence: boolean
  minimumScore?: number
}): Promise<void> {
  assert.equal(run.status, 'completed')
  assert.equal(run.targetType, options.targetType)
  assert.equal(run.targetId, options.targetId)
  assert.equal(run.level, options.highConfidence ? 'high_confidence' : run.level)
  assert(run.score >= (options.minimumScore ?? 90), `完整检测分数应达到该场景下限，actual=${run.score}`)
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
            { id: 'gpt-5.6-sol', object: 'model', created: 0, owned_by: 'mock' },
            { id: 'gpt-5.6-terra', object: 'model', created: 0, owned_by: 'mock' },
            { id: 'gpt-5.6-luna', object: 'model', created: 0, owned_by: 'mock' },
            { id: 'gpt-5.4', object: 'model', created: 0, owned_by: 'mock' }
          ]
        })
        return
      }
      if (req.method === 'POST' && url.pathname === '/v1/responses') {
        upstreamResponseRequestCount += 1
        observedStreamFlags.push(body.stream === true)
        if (JSON.stringify(body).includes('Controlled token integrity probe')) tokenIntegrityRequestCount += 1
        if (Array.isArray(body.include) && body.include.includes('reasoning.encrypted_content')) gpt56JuiceRequestCount += 1
        const outputText = outputForProbe(body)
        if (body.stream === true) {
          sendStream(res, String(body.model ?? 'gpt-5.6-sol'), outputText, Array.isArray(body.tools))
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
    model: String(body.model ?? 'gpt-5.6-sol'),
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

function sendStream(res: http.ServerResponse, model: string, outputText: string, hasTool = false): void {
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
      ...(hasTool ? {
        output: [{
          type: 'function_call',
          call_id: 'call_model_check',
          name: 'record_model_check',
          arguments: JSON.stringify({ code: 'ok', count: 1 })
        }]
      } : {}),
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
  const coverage = JSON.stringify(body).match(/Juice=(\d+)/i)
  if (coverage) return coverage[1]
  if (text.includes('REPLY WITH EXACTLY: 32')) return '32'
  if (text.includes('REPLY WITH EXACTLY: 48')) return '48'
  if (text.includes('JUICE NUMBER') || text.includes('SOURCE\\\":\\\"VALID CHANNELS')) {
    if (forceGpt56JuiceMismatch) return '40'
    if (body.model === 'gpt-5.6-terra') return '32'
    if (body.model === 'gpt-5.6-luna') return '48'
    return '40'
  }
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
  const needle = text.match(/NEEDLE-(?:LOW|MEDIUM|HIGH|EXTREME)-\d+/)
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
