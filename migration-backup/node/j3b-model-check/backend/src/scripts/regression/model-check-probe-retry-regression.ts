import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { ModelCheckItemSummary, ModelQualityPolicy } from '../../domain/types.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-probe-retry-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-probe-retry-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const retryState = {
  transientBasicAttempts: 0,
  transientStreamAttempts: 0,
  persistentBasicAttempts: 0,
  rateLimitedBasicAttempts: 0,
  opaque400BasicAttempts: 0,
  opaque429BasicAttempts: 0,
  createdBasicAttempts: 0,
  http200QualityAttempts: 0,
  tokenTransportAttempts: 0
}
const fullPolicy: ModelQualityPolicy = {
  systemAccountId: 'sys_admin',
  revision: 0,
  profile: 'full',
  manualEnforcementEnabled: false,
  penaltyThreshold: 70,
  penaltyAction: 'fallback',
  recoveryIntervalMinutes: 10
}
const upstream = createRetryAwareUpstream()
let stopGatewayJsonParseWorker: (() => Promise<void>) | undefined
let stopModelCheckTokenWorker: (() => Promise<void>) | undefined
let closeStorageDatabases: (() => void) | undefined

try {
  await listen(upstream)
  const [
    { createMockGatewayFixture },
    { getModelCheckRun, runModelCheck },
    gatewayJsonParser,
    tokenWorker
  ] = await Promise.all([
    import('../maintenance/mockdata/fixtures.js'),
    import('../../modules/model-checks/model-checks.service.js'),
    import('../../modules/gateway/request/json-parser.js'),
    import('../../modules/model-checks/model-checks-token-worker.service.js')
  ])
  const databaseModule = await import('../../storage/database.js')
  closeStorageDatabases = databaseModule.closeStorageDatabases
  stopGatewayJsonParseWorker = gatewayJsonParser.stopGatewayJsonParseWorker
  stopModelCheckTokenWorker = tokenWorker.stopModelCheckTokenWorker

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
  }, access, undefined, undefined, { policy: fullPolicy })
  assert.equal(
    transientRun.level,
    'high_confidence',
    `瞬态上游异常恢复后不应误判失败：${JSON.stringify(checkStatusSummary(transientRun.checks))}`
  )
  const transientStream = requiredCheck(transientRun.checks, 'target.responses_stream')
  assert.equal(retryState.transientBasicAttempts, 3, '瞬态异常 basic 探针应在同一账号上最多尝试三次')
  assert.equal(retryState.transientStreamAttempts, 3, '瞬态流式异常应在同一账号上最多尝试三次')
  const transientBasic = requiredCheck(transientRun.checks, 'target.responses_basic')
  assert.equal(transientBasic.status, 'passed', '恢复成功的基础预检应记录为通过')
  assert.deepEqual(transientBasic.evidenceSummary.attemptStatusCodes, [502, 502, 200], '基础预检应保留两次失败和第三次恢复的状态码')
  assert.equal(transientStream.status, 'passed', '第 3 次恢复后流式探针应通过')
  assert.equal(transientStream.evidenceSummary.attemptCount, 3, '流式探针应记录总尝试次数')
  assert.deepEqual(transientStream.evidenceSummary.attemptStatusCodes, [502, 502, 200], '流式探针应记录每次 HTTP 状态码')

  const rateLimitedFixture = createMockGatewayFixture({
    label: 'model-check-retry-rate-limit',
    upstreamBaseUrl,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const rateLimitedAccount = rateLimitedFixture.accounts[0]
  assert(rateLimitedAccount, 'mock fixture should create a rate-limited target account')
  const rateLimitedRun = await runModelCheck({
    targetType: 'account',
    targetId: rateLimitedAccount.id,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: false
  }, access, undefined, undefined, { policy: fullPolicy })
  assert.equal(rateLimitedRun.level, 'high_confidence', `429 瞬态限流恢复后不应误判失败：${JSON.stringify(checkStatusSummary(rateLimitedRun.checks))}`)
  assert.equal(retryState.rateLimitedBasicAttempts, 3, '429 basic 探针应等待后在同一账号上最多尝试三次')
  const rateLimitedBasic = requiredCheck(rateLimitedRun.checks, 'target.responses_basic')
  assert.equal(rateLimitedBasic.status, 'passed', '限流恢复成功的基础预检应记录为通过')
  assert.deepEqual(rateLimitedBasic.evidenceSummary.attemptStatusCodes, [502, 502, 200], '限流基础预检应保留两次失败和第三次恢复的状态码')

  const opaque400Fixture = createMockGatewayFixture({
    label: 'model-check-retry-opaque-400',
    upstreamBaseUrl,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const opaque400Account = opaque400Fixture.accounts[0]
  assert(opaque400Account, 'mock fixture should create an opaque 400 target account')
  const opaque400Run = await runModelCheck({
    targetType: 'account',
    targetId: opaque400Account.id,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: false
  }, access, undefined, undefined, { policy: fullPolicy })
  const opaque400Basic = requiredCheck(opaque400Run.checks, 'target.responses_basic')

  const opaque429Fixture = createMockGatewayFixture({
    label: 'model-check-retry-opaque-429',
    upstreamBaseUrl,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const opaque429Account = opaque429Fixture.accounts[0]
  assert(opaque429Account, 'mock fixture should create an opaque 429 target account')
  const opaque429Run = await runModelCheck({
    targetType: 'account',
    targetId: opaque429Account.id,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: false
  }, access, undefined, undefined, { policy: fullPolicy })
  const opaque429Basic = requiredCheck(opaque429Run.checks, 'target.responses_basic')
  assert.equal(retryState.opaque400BasicAttempts, 3, '400 + rate_limit 文本与 429 应执行同样的有界重试')
  assert.equal(retryState.opaque429BasicAttempts, 3, '429 + 任意正文应执行同样的有界重试')
  assert.equal(opaque400Basic.status, opaque429Basic.status, '400 与 429 的探针动作结果必须等价')
  assert.equal(opaque400Basic.maxScore, opaque429Basic.maxScore, '400 与 429 不得改变评分分母')
  assert.equal(opaque400Basic.evidenceSummary.requestFailure, opaque429Basic.evidenceSummary.requestFailure, '400 与 429 请求失败事实必须等价')
  assert.equal(opaque400Basic.evidenceSummary.excludedFromScoring, opaque429Basic.evidenceSummary.excludedFromScoring, '400 与 429 排除评分事实必须等价')
  assert.equal(opaque400Basic.evidenceSummary.rateLimited, undefined, '400 正文 rate_limit 不能被内部解释为限流')
  assert.equal(opaque429Basic.evidenceSummary.rateLimited, undefined, '429 状态码不能被内部解释为限流')
  assert.deepEqual(opaque400Basic.evidenceSummary.attemptStatusCodes, [502, 502, 502], '网关错误包装状态码应保留三次失败事实')
  assert.deepEqual(opaque400Basic.evidenceSummary.attemptUpstreamStatusCodes, [400, 400, 400], '400 应保留原始上游状态码诊断')
  assert.deepEqual(opaque429Basic.evidenceSummary.attemptStatusCodes, [502, 502, 502], '网关错误包装状态码应保留三次失败事实')
  assert.deepEqual(opaque429Basic.evidenceSummary.attemptUpstreamStatusCodes, [429, 429, 429], '429 应保留原始上游状态码诊断')

  const createdFixture = createMockGatewayFixture({
    label: 'model-check-retry-created',
    upstreamBaseUrl,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const createdAccount = createdFixture.accounts[0]
  assert(createdAccount, 'mock fixture should create a 201 target account')
  const createdRun = await runModelCheck({
    targetType: 'account',
    targetId: createdAccount.id,
    model: 'gpt-5.5',
    profile: 'quick',
    trustedComparison: false
  }, access, undefined, undefined, { policy: fullPolicy })
  const createdBasic = requiredCheck(createdRun.checks, 'target.responses_basic')
  assert.equal(retryState.createdBasicAttempts, 3, '201 必须和其他非 200 一样达到三次尝试')
  assert.equal(createdRun.level, 'unavailable', '三次 201 后必须按未验证结束')
  assert.equal(createdRun.qualityDecision?.result, 'not_triggered', '三次 201 后不得触发质量处罚')
  assert.deepEqual(createdBasic.evidenceSummary.attemptStatusCodes, [201, 201, 201], '201 诊断必须保留三次状态码')

  const http200QualityFixture = createMockGatewayFixture({
    label: 'model-check-retry-http200-quality',
    upstreamBaseUrl,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const http200QualityAccount = http200QualityFixture.accounts[0]
  assert(http200QualityAccount, 'mock fixture should create an HTTP 200 quality target account')
  const http200QualityRun = await runModelCheck({
    targetType: 'account',
    targetId: http200QualityAccount.id,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: false
  }, access, undefined, undefined, { policy: fullPolicy })
  assert.equal(retryState.http200QualityAttempts, 1, 'HTTP 200 但模型内容验证失败不得重试')
  const http200Structured = requiredCheck(http200QualityRun.checks, 'target.structured_output')
  assert.notEqual(http200Structured.status, 'skipped', 'HTTP 200 质量失败必须作为协议质量项进入既有质量判定')
  assert(http200QualityRun.checks.some((item) => item.itemKey === 'target.tool_calling'), 'HTTP 200 质量失败后仍必须执行余下必需探针')
  assert.notEqual(http200QualityRun.resultSummary.modelCheckUnverified, true, 'HTTP 200 质量失败不能伪装成未验证传输失败')

  const tokenTransportFixture = createMockGatewayFixture({
    label: 'model-check-retry-token-transport',
    upstreamBaseUrl,
    systemAccountId: 'sys_admin',
    accountCount: 1,
    createApiKey: false
  })
  const tokenTransportAccount = tokenTransportFixture.accounts[0]
  assert(tokenTransportAccount, 'mock fixture should create a token transport target account')
  const tokenTransportRun = await runModelCheck({
    targetType: 'account',
    targetId: tokenTransportAccount.id,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: false
  }, access, undefined, undefined, { policy: fullPolicy })
  const tokenIntegrity = requiredCheck(tokenTransportRun.checks, 'target.token_integrity')
  assert.equal(retryState.tokenTransportAttempts, 3, 'Token integrity 第三次 non-200 后不得继续剩余 Token 样本')
  assert.equal(tokenIntegrity.evidenceSummary.attemptCount, 3, 'Token integrity 应保留三次 transport 尝试')
  assert.equal(tokenIntegrity.evidenceSummary.sampleCount, 0, 'Token integrity transport 失败样本因缺少 usage 不应伪装为有效样本')
  assert.match(String(tokenIntegrity.evidenceSummary.message), /暂不支持形成 Token 诚信结论/)
  assert(tokenTransportRun.checks.some((item) => item.itemKey === 'target.identity_observation'), 'Token 质量结论无法形成时仍应继续身份探针')
  assert(tokenTransportRun.checks.some((item) => item.itemKey === 'target.cross_model'), 'Token 质量结论无法形成时仍应继续后续功能探针')
  assert.doesNotMatch(JSON.stringify(tokenTransportRun), /已终止后续探针/, 'Token transport 失败不得产生终止后续探针误导文本')
  assert.notEqual(tokenTransportRun.resultSummary.modelCheckUnverified, true, 'Token 质量结论无法形成不应伪装成整次检测未验证')

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
  }, access, undefined, undefined, { policy: fullPolicy })
  const persistentBasic = requiredCheck(persistentRun.checks, 'target.responses_basic')
  assert.equal(persistentRun.level, 'unavailable', '连续重试仍失败时应落不可检测')
  assert.equal(persistentRun.qualityDecision?.result, 'not_triggered', '三次非 200 终止时不得触发质量处罚')
  assert.match(persistentRun.qualityDecision?.message ?? '', /未形成质量判定证据/, '三次非 200 终止原因必须明确未形成质量判定证据')
  assert.equal(retryState.persistentBasicAttempts, 3, '持续异常 basic 探针应达到最大尝试次数')
  assert.equal(persistentBasic.status, 'skipped', '持续异常时 basic 探针应记录为请求失败未计分')
  assert.equal(persistentBasic.maxScore, 0, '持续异常时 basic 探针不应进入评分分母')
  assert.equal(persistentBasic.evidenceSummary.requestFailure, true, '持续异常报告应标记请求失败')
  assert.equal(persistentBasic.evidenceSummary.excludedFromScoring, true, '持续异常报告应标记不参与评分')
  assert.equal(persistentBasic.evidenceSummary.attemptCount, 3, '持续异常报告应记录总尝试次数')
  assert.equal(persistentBasic.evidenceSummary.retryAttemptCount, 2, '持续异常报告应记录重试次数')
  assert.deepEqual(persistentBasic.evidenceSummary.attemptStatusCodes, [502, 502, 502], '持续异常报告应记录全部网关包装失败状态码')
  assert.deepEqual(persistentBasic.evidenceSummary.attemptUpstreamStatusCodes, [503, 503, 503], '持续异常报告应记录全部原始上游状态码')
  assert(!persistentRun.checks.some((item) => item.itemKey === 'target.behavior_probe'), 'basic 连续失败后不应继续执行重型行为探针')

  const baseDetail = await getModelCheckRun(tokenTransportRun.id, access)
  assert(baseDetail, 'dataset 基础模型检测详情应可读取')
  const baseItemCount = baseDetail.checks.length
  databaseModule.getStatsDatabase().exec('DROP TABLE IF EXISTS model_account_trust_results')
  const detailWithoutTrust = await getModelCheckRun(tokenTransportRun.id, access)
  assert(detailWithoutTrust, 'latest trust 查询失败时仍应返回基础模型检测详情')
  assert.equal(detailWithoutTrust.checks.length, baseItemCount, 'latest trust 查询失败不得丢失基础检测项')

  console.log('模型检测探针重试回归通过：统一延迟重试、瞬态恢复和持续失败未计分均符合预期')
} finally {
  await stopGatewayJsonParseWorker?.()
  await stopModelCheckTokenWorker?.()
  closeStorageDatabases?.()
  await closeServer(upstream)
  rmSync(tempRoot, { recursive: true, force: true })
}

function requiredCheck(checks: ModelCheckItemSummary[], itemKey: string): ModelCheckItemSummary {
  const check = checks.find((item) => item.itemKey === itemKey)
  assert(check, `检测报告应包含 ${itemKey}`)
  return check
}

function checkStatusSummary(checks: ModelCheckItemSummary[]): Array<{ itemKey: string; status: string; errorMessage?: string }> {
  return checks.map((item) => ({
    itemKey: item.itemKey,
    status: item.status,
    errorMessage: item.errorMessage
  }))
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
        if (authorization.includes('model-check-retry-token-transport') && bodyText.includes('CONTROLLED TOKEN INTEGRITY PROBE')) {
          retryState.tokenTransportAttempts += 1
          sendError(res, '模拟 Token integrity transport 异常')
          return
        }
        if (bodyText.includes('OK-MODEL-CHECK')) {
          if (authorization.includes('model-check-retry-opaque-400')) {
            retryState.opaque400BasicAttempts += 1
            sendOpaqueError(res, 400, { error: { message: 'rate_limit text is untrusted', type: 'rate_limit' } })
            return
          }
          if (authorization.includes('model-check-retry-opaque-429')) {
            retryState.opaque429BasicAttempts += 1
            sendOpaqueError(res, 429, { error: { message: 'provider returned an opaque response' } })
            return
          }
          if (authorization.includes('model-check-retry-created')) {
            retryState.createdBasicAttempts += 1
            sendOpaqueError(res, 201, responsePayload(body, 'OK-MODEL-CHECK'))
            return
          }
          if (authorization.includes('model-check-retry-rate-limit')) {
            retryState.rateLimitedBasicAttempts += 1
            if (retryState.rateLimitedBasicAttempts <= 2) {
              sendRateLimit(res)
              return
            }
          }
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
        if (authorization.includes('model-check-retry-http200-quality') && bodyText.includes('MODEL_CHECK_STRUCTURED_OUTPUT')) {
          retryState.http200QualityAttempts += 1
          sendJson(res, {
            id: 'resp_model_check_quality_failure',
            object: 'response',
            status: 'completed',
            model: String(body.model ?? 'gpt-5.5'),
            output: []
          })
          return
        }
        if (body.stream === true && bodyText.includes('STREAM-OK') && authorization.includes('model-check-retry-transient')) {
          retryState.transientStreamAttempts += 1
          if (retryState.transientStreamAttempts <= 2) {
            sendError(res, '模拟流式临时异常')
            return
          }
        }
        const outputText = outputForProbe(body)
        if (body.stream === true) {
          sendStream(res, String(body.model ?? 'gpt-5.5'), outputText, Array.isArray(body.tools))
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
  const identityOutput = outputForIdentityCanary(body)
  if (identityOutput !== undefined) return identityOutput
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
  const needle = text.match(/NEEDLE-(?:LOW|MEDIUM|HIGH|EXTREME)-\d+/)
  if (needle) return needle[0]
  if (body.text || text.includes('JSON')) return '{"status":"ok","value":7}'
  return 'OK-MODEL-CHECK'
}

function outputForIdentityCanary(body: Record<string, unknown>): string | undefined {
  const prompt = inputPrompt(body)
  if (!prompt.includes('CANARY-')) return undefined
  const tag = prompt.match(/tag"?\s*[:=]\s*"?(CANARY-[A-Z0-9-]+)"?/i)?.[1]
  if (!tag) return undefined
  if (prompt.includes('result 等于')) {
    const match = prompt.match(/result 等于\s*(-?\d+)\s*\+\s*(-?\d+)/)
    if (!match) return undefined
    return JSON.stringify({ result: Number(match[1]) + Number(match[2]), tag })
  }
  if (prompt.includes('过滤为大于') && prompt.includes('升序')) {
    return `[1, 2, 3].filter((value) => value > 1).sort((left, right) => left - right) // ${tag}`
  }
  if (prompt.includes('largest 是')) {
    const match = prompt.match(/largest 是\s*([\d、]+)\s*中第二大值加\s*(-?\d+)/)
    if (!match) return undefined
    const values = match[1].split('、').map(Number).sort((left, right) => right - left)
    return JSON.stringify({ largest: (values[1] ?? 0) + Number(match[2]), tag })
  }
  if (prompt.includes('中间结论错误地声称')) {
    const match = prompt.match(/声称\s*(-?\d+)\+(-?\d+)=/)
    if (!match) return undefined
    return JSON.stringify({ correct: Number(match[1]) + Number(match[2]), tag })
  }
  if (prompt.includes('队列超时') && prompt.includes('queue timeout')) {
    return JSON.stringify({ zh: '队列超时', en: 'queue timeout', tag })
  }
  if (prompt.includes('action 枚举') && prompt.includes('ids 数组')) {
    const match = prompt.match(/ids 数组\s*\[([\d,\s]+)\]/)
    if (!match) return undefined
    return JSON.stringify({ action: 'inspect', payload: { ids: match[1].split(',').map((value) => Number(value.trim())), dryRun: true }, tag })
  }
  if (prompt.includes('封闭时间线')) {
    return JSON.stringify({ version: 'B', tag })
  }
  return undefined
}

function inputPrompt(body: Record<string, unknown>): string {
  const input = Array.isArray(body.input) ? body.input[0] : undefined
  const content = input && typeof input === 'object' && !Array.isArray(input)
    ? (input as { content?: unknown }).content
    : undefined
  const firstContent = Array.isArray(content) && content[0] && typeof content[0] === 'object' && !Array.isArray(content[0])
    ? content[0] as { text?: unknown }
    : undefined
  return typeof firstContent?.text === 'string' ? firstContent.text : ''
}

function sendJson(res: http.ServerResponse, body: unknown): void {
  res.writeHead(200, { 'content-type': 'application/json' })
  res.end(JSON.stringify(body))
}

function sendError(res: http.ServerResponse, message: string): void {
  res.writeHead(503, { 'content-type': 'application/json' })
  res.end(JSON.stringify({ error: { message } }))
}

function sendRateLimit(res: http.ServerResponse): void {
  res.writeHead(429, { 'content-type': 'application/json' })
  res.end(JSON.stringify({ error: { message: 'group requests-per-minute limit exceeded', type: 'rate_limit_exceeded' } }))
}

function sendOpaqueError(res: http.ServerResponse, statusCode: number, body: unknown): void {
  res.writeHead(statusCode, { 'content-type': 'application/json' })
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
    server.closeAllConnections()
  })
}
