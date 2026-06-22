import { createHash } from 'node:crypto'

import type { Request } from 'express'

import {
  clampHybridLevel
} from '../../../domain/api-key-hybrid-routing.js'
import { isOpenAIProtocolProfile } from '../../../domain/provider-protocol.js'
import type { ApiKeyHybridRoutingConfig } from '../../../domain/types.js'
import { tryAcquireAccountConcurrency } from '../../../shared/account-concurrency.js'
import { createAppCache } from '../../../shared/cache.js'
import type { GatewayApiKeyRow, OpenAIAccountSecret } from '../../../storage/repositories.js'
import { filterGatewayAccountsByRequestCapability } from '../dispatch/account-capability-filter.js'
import { filterGatewayAccountsByRequestedModel } from '../dispatch/model-filter.js'
import { parseOpenAIUsageFromJsonBuffer } from '../protocols/openai-v1/usage.js'
import {
  getGatewayRequestBodyState,
  type GatewayRawBodyRequest
} from '../request/body.js'
import { parseGatewayJsonBodyInWorker } from '../request/json-parser.js'
import { requestModel } from '../request/metadata.js'
import {
  listCachedOpenAIAccountsForGroupAsync
} from '../runtime/runtime-cache.service.js'
import { requestUpstream } from '../upstream/request.js'
import {
  buildGatewayUpstreamRequestParts,
  buildGatewayUpstreamUrlsForAccount
} from '../../providers/drivers/registry.js'
import { recordHybridScoringAttempt } from '../usage/records.js'
import { emptyUsage } from '../usage/types.js'

export interface HybridScoringResult {
  level: number
  confidence?: number
  reason?: string
  defaulted: boolean
  cacheHit?: boolean
  errorCode?: string
  errorMessage?: string
  scoringAccountId?: string
  statusCode?: number
}

interface HybridScoringCacheEntry {
  level: number
  confidence?: number
  reason?: string
}

const hybridScoringContextMaxBytes = 128 * 1024
const hybridScoringRawBodyParseMaxBytes = hybridScoringContextMaxBytes
const hybridScoringResponseMaxBytes = 2 * 1024 * 1024
const hybridScoringCacheMaxEntries = 10_000
const hybridScoringCacheMaxTtlMs = 60 * 60 * 1000

const hybridScoringCache = createAppCache<string, HybridScoringCacheEntry>({
  name: 'gateway:hybrid-scoring-result',
  max: hybridScoringCacheMaxEntries,
  ttlMs: hybridScoringCacheMaxTtlMs,
  updateAgeOnGet: false
})

export async function scoreHybridGatewayRequest(input: {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  config: ApiKeyHybridRoutingConfig
  traceId: string
  clientIp?: string
  endpoint: string
  signal?: AbortSignal
}): Promise<HybridScoringResult> {
  const startedAt = Date.now()
  let account: OpenAIAccountSecret | undefined
  let statusCode: number | undefined
  try {
    const body = await parseHybridRequestBody(input.req, input.config.scoringTimeoutMs, input.signal)
    const context = buildHybridScoringContext(input.req, body)
    const cacheKey = buildHybridScoringCacheKey({
      req: input.req,
      apiKeyRecord: input.apiKeyRecord,
      config: input.config,
      endpoint: input.endpoint,
      context
    })
    const cached = cacheKey ? hybridScoringCache.get(cacheKey) : undefined
    if (cached) {
      return {
        level: cached.level,
        confidence: cached.confidence,
        reason: cached.reason,
        defaulted: false,
        cacheHit: true
      }
    }
    const scoringBody = buildHybridScoringRequestBody(input.config.scoringModel, context, input.config.qualityPreference)
    const scoringReq = createHybridScoringGatewayRequest(scoringBody)
    account = await selectHybridScoringAccount({
      groupId: input.config.scoringGroupId,
      systemAccountId: input.apiKeyRecord.system_account_id,
      scoringModel: input.config.scoringModel,
      scoringReq
    })
    if (!account) {
      return defaultScoringResult(input.config, 'no_scoring_account', '混合路由评分分组没有可用评分账户')
    }
    const slot = tryAcquireAccountConcurrency(account.id, account.concurrencyLimit)
    if (!slot.acquired) {
      return defaultScoringResult(input.config, 'scoring_account_busy', '混合路由评分账户并发已满')
    }
    try {
      const upstreamUrls = buildGatewayUpstreamUrlsForAccount(account, scoringReq)
      const upstreamUrl = upstreamUrls[0]
      if (!upstreamUrl) {
        throw new Error('混合路由评分账户不支持 Chat Completions 请求')
      }
      const requestParts = await buildGatewayUpstreamRequestParts(scoringReq, account, {
        systemAccountId: input.apiKeyRecord.system_account_id,
        apiKeyId: input.apiKeyRecord.id,
        groupId: input.config.scoringGroupId
      }, input.signal, {
        requestClientCompatibility: 'openai_standard'
      })
      const response = await requestUpstream(upstreamUrl, {
        method: scoringReq.method,
        headers: requestParts.headers,
        body: requestParts.body,
        proxyUrl: account.proxyUrl,
        timeoutMs: input.config.scoringTimeoutMs,
        requestTimeoutMs: input.config.scoringTimeoutMs,
        signal: buildHybridScoringAbortSignal(input.signal, input.config.scoringTimeoutMs)
      })
      statusCode = response.status
      const responseBody = await readScoringResponseBody(response.body)
      const usage = parseOpenAIUsageFromJsonBuffer(responseBody)
      if (!response.ok) {
        recordHybridScoringAttempt({
          traceId: input.traceId,
          clientIp: input.clientIp,
          systemAccountId: input.apiKeyRecord.system_account_id,
          apiKeyId: input.apiKeyRecord.id,
          groupId: input.config.scoringGroupId,
          account,
          endpoint: `${input.endpoint}#hybrid-scoring`,
          statusCode: response.status,
          success: false,
          startedAt,
          scoringModel: input.config.scoringModel,
          usage,
          errorCode: 'hybrid_scoring_http_error',
          errorMessage: `评分模型返回 HTTP ${response.status}`,
          requestSnapshot: { model: input.config.scoringModel, contextBytes: Buffer.byteLength(context, 'utf8') },
          responseSnapshot: { statusCode: response.status, body: responseBodySnippet(responseBody) }
        })
        return defaultScoringResult(input.config, 'hybrid_scoring_http_error', `评分模型返回 HTTP ${response.status}`, account.id, response.status)
      }
      const parsed = parseHybridScoringResponse(responseBody)
      const scoringResult: HybridScoringResult = {
        level: clampHybridLevel(parsed.level),
        confidence: parsed.confidence,
        reason: parsed.reason,
        defaulted: false,
        cacheHit: false,
        scoringAccountId: account.id,
        statusCode: response.status
      }
      rememberHybridScoringCacheResult(cacheKey, scoringResult, input.config.scoringCacheTtlSeconds)
      recordHybridScoringAttempt({
        traceId: input.traceId,
        clientIp: input.clientIp,
        systemAccountId: input.apiKeyRecord.system_account_id,
        apiKeyId: input.apiKeyRecord.id,
        groupId: input.config.scoringGroupId,
        account,
        endpoint: `${input.endpoint}#hybrid-scoring`,
        statusCode: response.status,
        success: true,
        startedAt,
        scoringModel: input.config.scoringModel,
        usage,
        requestSnapshot: { model: input.config.scoringModel, contextBytes: Buffer.byteLength(context, 'utf8') },
        responseSnapshot: { statusCode: response.status, parsed }
      })
      return scoringResult
    } finally {
      slot.release()
    }
  } catch (error) {
    if (account) {
      recordHybridScoringAttempt({
        traceId: input.traceId,
        clientIp: input.clientIp,
        systemAccountId: input.apiKeyRecord.system_account_id,
        apiKeyId: input.apiKeyRecord.id,
        groupId: input.config.scoringGroupId,
        account,
        endpoint: `${input.endpoint}#hybrid-scoring`,
        statusCode,
        success: false,
        startedAt,
        scoringModel: input.config.scoringModel,
        usage: emptyUsage(),
        errorCode: 'hybrid_scoring_failed',
        errorMessage: error instanceof Error ? error.message : String(error)
      })
    }
    return defaultScoringResult(
      input.config,
      'hybrid_scoring_failed',
      error instanceof Error ? error.message : String(error),
      account?.id,
      statusCode
    )
  }
}

export function clearHybridScoringCacheForTest(): void {
  hybridScoringCache.clear()
}

async function selectHybridScoringAccount(input: {
  groupId: string
  systemAccountId: string
  scoringModel: string
  scoringReq: Request
}): Promise<OpenAIAccountSecret | undefined> {
  const accounts = (await listCachedOpenAIAccountsForGroupAsync(input.groupId, input.systemAccountId))
    .filter((account) =>
      account.status === 'active'
      && account.proxyProfileUnavailable !== true
      && Boolean(account.apiKey)
      && Boolean(account.baseUrl)
      && isOpenAIProtocolProfile(account)
    )
  const capabilityFilter = filterGatewayAccountsByRequestCapability(input.scoringReq, accounts, {
    requestClientCompatibility: 'openai_standard'
  })
  return filterGatewayAccountsByRequestedModel(capabilityFilter.accounts, input.scoringModel).accounts[0]
}

async function parseHybridRequestBody(req: Request, timeoutMs: number, signal?: AbortSignal): Promise<Record<string, unknown> | undefined> {
  const request = req as GatewayRawBodyRequest
  if (typeof request.body === 'object' && request.body !== null && !Array.isArray(request.body)) {
    return request.body as Record<string, unknown>
  }
  if (
    request.gatewayParsedJsonBodyAvailable
    && typeof request.gatewayParsedJsonBody === 'object'
    && request.gatewayParsedJsonBody !== null
    && !Array.isArray(request.gatewayParsedJsonBody)
  ) {
    return request.gatewayParsedJsonBody as Record<string, unknown>
  }
  if (!request.rawBody?.length) {
    return undefined
  }
  if (request.rawBody.length > hybridScoringRawBodyParseMaxBytes) {
    return undefined
  }
  const parsed = await parseGatewayJsonBodyInWorker(request.rawBody, timeoutMs, signal)
  return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
    ? parsed as Record<string, unknown>
    : undefined
}

function buildHybridScoringRequestBody(
  model: string,
  context: string,
  qualityPreference: ApiKeyHybridRoutingConfig['qualityPreference']
): Record<string, unknown> {
  return {
    model,
    stream: false,
    temperature: 0,
    max_tokens: 160,
    messages: [
      {
        role: 'system',
        content: [
          '你是网关请求分级器，只做成本路由评分。',
          '必须根据当前请求的真实目标、完整上下文、约束数量、失败代价、所需可靠性、输出可验证性、可逆性和返工成本动态评分。',
          '不要按固定关键词、固定业务领域或固定任务名称机械分级；同一类任务可能因为上下文、风险、约束和质量要求不同而落入不同等级。',
          '如果请求明确包含上一次执行失败、验证失败、缺文件、输出协议不合格或修复要求，要把真实返工风险纳入评分；但不能因为出现“修复”等字样机械给高等级。',
          qualityPreferenceInstruction(qualityPreference),
          '1 表示最低成本模型也足够，10 表示必须使用可用的最强模型；只输出你对本次请求的相对等级。',
          '只输出 JSON：{"level":数字,"confidence":0到1,"reason":"一句话"}。'
        ].join('\n')
      },
      {
        role: 'user',
        content: context
      }
    ]
  }
}

function createHybridScoringGatewayRequest(body: Record<string, unknown>): Request {
  const rawBody = Buffer.from(JSON.stringify(body), 'utf8')
  const headers: Record<string, string> = {
    accept: 'application/json',
    'content-type': 'application/json',
    'content-length': String(rawBody.byteLength)
  }
  return {
    method: 'POST',
    originalUrl: '/v1/chat/completions',
    path: '/v1/chat/completions',
    headers,
    body,
    rawBody,
    ip: '127.0.0.1',
    socket: { remoteAddress: '127.0.0.1' },
    header(name: string) {
      return headers[name.toLowerCase()]
    },
    get(name: string) {
      return headers[name.toLowerCase()]
    }
  } as unknown as Request
}

function qualityPreferenceInstruction(preference: ApiKeyHybridRoutingConfig['qualityPreference']): string {
  if (preference === 'cost_first') {
    return '当前 API Key 偏好省钱：当失败风险、返工成本和质量不确定性都较低时，可以更积极选择较低等级；仍必须按本次真实上下文动态判断。'
  }
  if (preference === 'quality_first') {
    return '当前 API Key 偏好质量：当请求存在明显不确定性、隐性依赖或失败后返工成本较高时，应更保守地提高等级；仍必须按本次真实上下文动态判断。'
  }
  return '当前 API Key 偏好均衡：在成本和完成质量之间保持中性判断，按本次真实上下文动态给出等级。'
}

function buildHybridScoringContext(req: Request, body: Record<string, unknown> | undefined): string {
  const bodyContext = hybridScoringBodyContext(req, body)
  const payload = {
    method: req.method,
    path: req.originalUrl.split('?')[0] || req.path,
    originalModel: requestModel(req),
    body: bodyContext
  }
  const text = JSON.stringify(payload)
  if (Buffer.byteLength(text, 'utf8') <= hybridScoringContextMaxBytes) {
    return text
  }
  return JSON.stringify({
    ...payload,
    body: '[request_too_large_for_full_scoring_context]',
    rawBodyBytes: (req as GatewayRawBodyRequest).rawBody?.byteLength,
    truncated: true
  })
}

function hybridScoringBodyContext(req: Request, body: Record<string, unknown> | undefined): unknown {
  if (body) {
    return sanitizeScoringValue(body, { bytes: 0, truncated: false, depth: 0 })
  }
  const state = getGatewayRequestBodyState(req)
  if (!state || state.rawBodyBytes <= 0) {
    return undefined
  }
  return {
    _gatewayBody: {
      rawBodyBytes: state.rawBodyBytes,
      contentType: state.contentType,
      jsonParseStatus: state.jsonParseStatus,
      model: state.model,
      stream: state.stream,
      imageGeneration: state.imageGeneration,
      imageGenerationForced: state.imageGenerationForced,
      omittedReason: state.rawBodyBytes > hybridScoringRawBodyParseMaxBytes
        ? 'raw_body_exceeds_hybrid_scoring_parse_limit'
        : 'body_not_available'
    }
  }
}

function sanitizeScoringValue(value: unknown, context: { bytes: number; truncated: boolean; depth: number }): unknown {
  if (value === undefined || value === null || typeof value === 'number' || typeof value === 'boolean') {
    context.bytes += 8
    return value
  }
  if (typeof value === 'string') {
    const maxLength = Math.max(0, Math.min(4096, hybridScoringContextMaxBytes - context.bytes))
    context.bytes += Math.min(Buffer.byteLength(value, 'utf8'), maxLength)
    return value.length > maxLength ? `${value.slice(0, maxLength)}...[truncated]` : value
  }
  if (context.depth >= 8 || context.bytes >= hybridScoringContextMaxBytes) {
    context.truncated = true
    return '[truncated]'
  }
  if (Array.isArray(value)) {
    const output: unknown[] = []
    for (let index = 0; index < value.length && index < 50; index += 1) {
      context.depth += 1
      output.push(sanitizeScoringValue(value[index], context))
      context.depth -= 1
      if (context.bytes >= hybridScoringContextMaxBytes) break
    }
    if (value.length > output.length) output.push(`[${value.length - output.length} items truncated]`)
    return output
  }
  if (typeof value === 'object') {
    const output: Record<string, unknown> = {}
    let count = 0
    for (const [key, item] of Object.entries(value as Record<string, unknown>)) {
      if (count >= 80 || context.bytes >= hybridScoringContextMaxBytes) {
        output._truncated = true
        break
      }
      context.bytes += Buffer.byteLength(key, 'utf8')
      context.depth += 1
      output[key] = sanitizeScoringValue(item, context)
      context.depth -= 1
      count += 1
    }
    return output
  }
  return String(value)
}

function buildHybridScoringCacheKey(input: {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  config: ApiKeyHybridRoutingConfig
  endpoint: string
  context: string
}): string | undefined {
  if (input.config.scoringCacheEnabled === false || input.config.scoringCacheTtlSeconds <= 0) {
    return undefined
  }
  return createHash('sha256')
    .update(JSON.stringify({
      systemAccountId: input.apiKeyRecord.system_account_id,
      apiKeyId: input.apiKeyRecord.id,
      endpoint: input.endpoint,
      config: hybridScoringConfigFingerprint(input.config),
      request: {
        method: input.req.method,
        path: input.req.originalUrl.split('?')[0] || input.req.path,
        originalModel: requestModel(input.req),
        rawBodyDigest: rawBodyDigest(input.req),
        contextDigest: digestText(input.context),
        selectedHeadersDigest: digestText(JSON.stringify(selectedScoringCacheHeaders(input.req)))
      }
    }))
    .digest('hex')
}

function hybridScoringConfigFingerprint(config: ApiKeyHybridRoutingConfig): Record<string, unknown> {
  return {
    scoringModel: config.scoringModel,
    scoringContextMode: config.scoringContextMode,
    qualityPreference: config.qualityPreference,
    scoringTimeoutMs: config.scoringTimeoutMs,
    failureDefaultLevel: config.failureDefaultLevel,
    cacheAffinityEnabled: config.cacheAffinityEnabled,
    affinityTtlSeconds: config.affinityTtlSeconds,
    switchMinLevelDelta: config.switchMinLevelDelta,
    downgradeConsecutiveLowCount: config.downgradeConsecutiveLowCount,
    levelRoutes: config.levelRoutes.map((route) => ({
      minLevel: route.minLevel,
      maxLevel: route.maxLevel,
      targetModel: route.targetModel,
      enabled: route.enabled
    }))
  }
}

function selectedScoringCacheHeaders(req: Request): Record<string, string> {
  const headers: Record<string, string> = {}
  for (const name of scoringCacheHeaderNames) {
    const value = stringHeaderValue(req.header(name))
    if (value) {
      headers[name] = value
    }
  }
  return headers
}

function stringHeaderValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function rawBodyDigest(req: Request): string | undefined {
  const rawBody = (req as GatewayRawBodyRequest).rawBody
  return rawBody?.length ? digestBuffer(rawBody) : undefined
}

function digestText(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

function digestBuffer(value: Buffer): string {
  return createHash('sha256').update(value).digest('hex')
}

function rememberHybridScoringCacheResult(
  key: string | undefined,
  result: HybridScoringResult,
  ttlSeconds: number
): void {
  if (!key || result.defaulted || result.cacheHit) return
  const ttlMs = Math.min(hybridScoringCacheMaxTtlMs, Math.max(1, Math.trunc(ttlSeconds)) * 1000)
  hybridScoringCache.set(key, {
    level: result.level,
    confidence: result.confidence,
    reason: result.reason
  }, { ttlMs })
}

function buildHybridScoringAbortSignal(parent: AbortSignal | undefined, timeoutMs: number): AbortSignal {
  const timeoutSignal = AbortSignal.timeout(timeoutMs)
  return parent ? AbortSignal.any([parent, timeoutSignal]) : timeoutSignal
}

async function readScoringResponseBody(body: AsyncIterable<Uint8Array> | null): Promise<Buffer> {
  if (!body) return Buffer.alloc(0)
  const chunks: Buffer[] = []
  let bytes = 0
  for await (const chunk of body) {
    const buffer = Buffer.from(chunk)
    bytes += buffer.byteLength
    if (bytes > hybridScoringResponseMaxBytes) {
      throw new Error('混合路由评分响应超过保护上限')
    }
    chunks.push(buffer)
  }
  return Buffer.concat(chunks)
}

function parseHybridScoringResponse(body: Buffer): { level: number; confidence?: number; reason?: string } {
  const response = JSON.parse(body.toString('utf8')) as {
    choices?: Array<{ message?: { content?: unknown } }>
  }
  const content = response.choices?.[0]?.message?.content
  const text = typeof content === 'string'
    ? content
    : Array.isArray(content)
      ? content.map((item) => typeof item === 'string' ? item : JSON.stringify(item)).join('\n')
      : ''
  const jsonText = extractJsonObjectText(text)
  if (!jsonText) {
    throw new Error('评分模型未返回 JSON')
  }
  const parsed = JSON.parse(jsonText) as Record<string, unknown>
  const level = Number(parsed.level)
  if (!Number.isFinite(level)) {
    throw new Error('评分模型返回的 level 无效')
  }
  const confidence = Number(parsed.confidence)
  return {
    level,
    confidence: Number.isFinite(confidence) ? Math.max(0, Math.min(1, confidence)) : undefined,
    reason: typeof parsed.reason === 'string' ? parsed.reason : undefined
  }
}

function extractJsonObjectText(text: string): string | undefined {
  const fenced = text.match(/```(?:json)?\s*([\s\S]*?)```/i)?.[1]?.trim()
  const source = fenced || text.trim()
  if (source.startsWith('{') && source.endsWith('}')) {
    return source
  }
  const start = source.indexOf('{')
  const end = source.lastIndexOf('}')
  return start >= 0 && end > start ? source.slice(start, end + 1) : undefined
}

function responseBodySnippet(body: Buffer): string {
  return body.toString('utf8', 0, Math.min(body.byteLength, 2048))
}

function defaultScoringResult(
  config: ApiKeyHybridRoutingConfig,
  errorCode: string,
  errorMessage: string,
  scoringAccountId?: string,
  statusCode?: number
): HybridScoringResult {
  return {
    level: config.failureDefaultLevel,
    defaulted: true,
    errorCode,
    errorMessage,
    scoringAccountId,
    statusCode
  }
}

const scoringCacheHeaderNames = [
  'session_id',
  'session-id',
  'x-session-id',
  'conversation_id',
  'conversation-id',
  'x-conversation-id',
  'prompt_cache_key',
  'x-prompt-cache-key',
  'previous_response_id',
  'previous-response-id',
  'x-previous-response-id',
  'x-codex-turn-metadata'
]
