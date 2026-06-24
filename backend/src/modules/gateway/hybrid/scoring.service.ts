import { createHash } from 'node:crypto'

import type { Request } from 'express'

import {
  clampHybridLevel
} from '../../../domain/api-key-hybrid-routing.js'
import type { ApiKeyHybridRoutingConfig } from '../../../domain/types.js'
import { createAppCache } from '../../../shared/cache.js'
import type { GatewayApiKeyRow } from '../../../storage/repositories.js'
import {
  getGatewayRequestBodyState,
  type GatewayRawBodyRequest
} from '../request/body.js'
import { parseGatewayJsonBodyInWorker } from '../request/json-parser.js'
import { requestModel } from '../request/metadata.js'
import { recordHybridScoringAttempt } from '../usage/records.js'
import {
  dispatchHybridAuxiliaryChatCompletion,
  emptyHybridAuxiliaryUsage
} from './auxiliary-dispatch.service.js'

export interface HybridScoringResult {
  level: number
  confidence?: number
  factors?: string[]
  reason?: string
  defaulted: boolean
  failed?: boolean
  cacheHit?: boolean
  errorCode?: string
  errorMessage?: string
  scoringAccountId?: string
  scoringGroupId?: string
  statusCode?: number
}

interface HybridScoringCacheEntry {
  level: number
  confidence?: number
  factors?: string[]
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
        factors: cached.factors,
        reason: cached.reason,
        defaulted: false,
        cacheHit: true
      }
    }
    const scoringBody = buildHybridScoringRequestBody(input.config.scoringModel, context)
    const scoringReq = createHybridScoringGatewayRequest(scoringBody)
    const dispatch = await dispatchHybridAuxiliaryChatCompletion({
      req: scoringReq,
      apiKeyRecord: input.apiKeyRecord,
      targetModel: input.config.scoringModel,
      traceId: input.traceId,
      clientIp: input.clientIp,
      endpoint: input.endpoint,
      trafficSource: 'hybrid_scoring',
      timeoutMs: input.config.scoringTimeoutMs,
      responseMaxBytes: hybridScoringResponseMaxBytes,
      noAccountErrorCode: 'no_scoring_account',
      noAccountErrorMessage: '混合路由绑定分组池没有可用评分账户',
      dispatchErrorCode: 'hybrid_scoring_failed',
      dispatchErrorMessage: '混合路由评分模型调用失败',
      httpErrorCode: 'hybrid_scoring_http_error',
      responseTooLargeMessage: '混合路由评分响应超过保护上限',
      signal: input.signal,
      requestClientCompatibility: 'openai_standard'
    })
    if (dispatch.outcome === 'failed') {
      if (dispatch.shouldRecordUsage && dispatch.account && dispatch.groupId) {
        recordHybridScoringAttempt({
          traceId: input.traceId,
          clientIp: input.clientIp,
          systemAccountId: input.apiKeyRecord.system_account_id,
          apiKeyId: input.apiKeyRecord.id,
          groupId: dispatch.groupId,
          account: dispatch.account,
          endpoint: `${input.endpoint}#hybrid-scoring`,
          statusCode: dispatch.statusCode,
          success: false,
          startedAt,
          scoringModel: input.config.scoringModel,
          usage: emptyHybridAuxiliaryUsage(),
          errorCode: dispatch.errorCode,
          errorMessage: dispatch.errorMessage,
          requestSnapshot: { model: input.config.scoringModel, contextBytes: Buffer.byteLength(context, 'utf8') },
          responseSnapshot: { statusCode: dispatch.statusCode }
        })
      }
      return failedScoringResult(input.config, dispatch.errorCode, dispatch.errorMessage, dispatch.account?.id, dispatch.statusCode)
    }
    let parsed: { level: number; confidence?: number; factors?: string[]; reason?: string }
    try {
      parsed = parseHybridScoringResponse(dispatch.responseBody)
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : String(error)
      recordHybridScoringAttempt({
        traceId: input.traceId,
        clientIp: input.clientIp,
        systemAccountId: input.apiKeyRecord.system_account_id,
        apiKeyId: input.apiKeyRecord.id,
        groupId: dispatch.groupId,
        account: dispatch.account,
        endpoint: `${input.endpoint}#hybrid-scoring`,
        statusCode: dispatch.statusCode,
        success: false,
        startedAt,
        scoringModel: input.config.scoringModel,
        usage: dispatch.usage,
        errorCode: 'hybrid_scoring_failed',
        errorMessage,
        requestSnapshot: { model: input.config.scoringModel, contextBytes: Buffer.byteLength(context, 'utf8') },
        responseSnapshot: { statusCode: dispatch.statusCode, body: responseBodySnippet(dispatch.responseBody) }
      })
      dispatch.finish({ success: false, errorCode: 'hybrid_scoring_failed', errorMessage })
      return failedScoringResult(input.config, 'hybrid_scoring_failed', errorMessage, dispatch.account.id, dispatch.statusCode)
    }
    const scoringResult: HybridScoringResult = {
      level: clampHybridLevel(parsed.level),
      confidence: parsed.confidence,
      factors: parsed.factors,
      reason: parsed.reason,
      defaulted: false,
      cacheHit: false,
      scoringAccountId: dispatch.account.id,
      scoringGroupId: dispatch.groupId,
      statusCode: dispatch.statusCode
    }
    rememberHybridScoringCacheResult(cacheKey, scoringResult, input.config.scoringCacheTtlSeconds)
    recordHybridScoringAttempt({
      traceId: input.traceId,
      clientIp: input.clientIp,
      systemAccountId: input.apiKeyRecord.system_account_id,
      apiKeyId: input.apiKeyRecord.id,
      groupId: dispatch.groupId,
      account: dispatch.account,
      endpoint: `${input.endpoint}#hybrid-scoring`,
      statusCode: dispatch.statusCode,
      success: true,
      startedAt,
      scoringModel: input.config.scoringModel,
      usage: dispatch.usage,
      requestSnapshot: { model: input.config.scoringModel, contextBytes: Buffer.byteLength(context, 'utf8') },
      responseSnapshot: { statusCode: dispatch.statusCode, parsed }
    })
    dispatch.finish({ success: true })
    return scoringResult
  } catch (error) {
    return failedScoringResult(
      input.config,
      'hybrid_scoring_failed',
      error instanceof Error ? error.message : String(error),
      undefined,
      undefined
    )
  }
}

export function clearHybridScoringCacheForTest(): void {
  hybridScoringCache.clear()
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
  context: string
): Record<string, unknown> {
  return {
    model,
    stream: false,
    temperature: 0,
    max_tokens: 240,
    messages: [
      {
        role: 'system',
        content: [
          '你是网关请求难度评分器，只负责给当前请求打一个 1 到 10 的绝对难度等级，用于后续成本路由。',
          '你只评估本次请求本身的难度、质量风险和返工成本，评分必须是客观判断。',
          '不要考虑任何模型名称、模型价格、供应商、用户配置的档位范围或最终会路由到哪个模型。',
          '不要考虑省钱偏好、质量偏好或任何用户路由策略；这些只属于路由层，不属于难度评分。',
          '不要假设 1 到 10 会被如何分组；档位范围由用户另行配置，与你无关。',
          '不要按固定关键词、业务领域、技术栈、文件名、任务名称或题型机械分级；同一类请求可能因为上下文、风险、约束和质量要求不同而得到完全不同的等级。',
          '评分尺度：1 表示极低难度，目标明确、上下文很少、影响局部、几乎无依赖、失败容易发现且容易修正。',
          '评分尺度：5 表示中等难度，有多个约束或一定上下文，需要结构化处理、保持局部一致性，失败会带来一定返工。',
          '评分尺度：10 表示最高难度，上下文复杂、多模块或多轮依赖强，需要高可靠推理或最终质量把关，失败代价很高。',
          '不要为了覆盖完整空间而强行拉高或拉低；但当请求风险明显不同，应敢于使用更高或更低等级。',
          '评分时综合判断：目标明确度、上下文跨度、依赖范围、约束数量、约束相互影响、跨文件或跨步骤一致性、严格输出格式、规划或架构判断、复杂推理、风险权衡、失败可发现性、可回滚性和是否会污染后续步骤。',
          '如果上下文显示被截断、信息缺失或关键依赖不可见，应把不确定性和潜在返工风险计入评分。',
          '上下文少只代表上下文处理成本低，不等于请求本身一定低难度；如果本次请求需要多步精确推理、优化选择、证明、组合约束、边界条件处理或严格正确性，应按真实推理难度提高等级。',
          '可验证性只降低发现错误的成本，不等于降低完成难度；如果错误会导致结果不可用、需要重新生成或污染后续调用，仍应计入返工成本。',
          '等级不是固定题型或固定范围映射；只能根据当前请求实际暴露的信息动态判断。直接作答、局部执行、状态变化、候选权衡、系统化比较、性能要求、质量要求、跨上下文一致性和失败代价都只是影响因素，不是硬编码规则。',
          '如果请求产物会被后续系统、测试、用户流程、业务决策或其他调用直接使用，要把可运行性、边界条件、异常路径、性能约束、输入不变性、协议语义、可维护性和后续修复成本纳入整体判断；只有当失败会影响后续流程、造成错误传播、需要较大返工或难以局部修复时，才显著提高等级。',
          '如果请求明确包含上一次失败、验收失败、缺文件、输出协议不合格或修复要求，要把真实返工风险纳入评分；但不能因为出现“修复”等字样机械给高等级。',
          '如果已有清晰计划且本次只是低风险局部执行，可以降低等级；但如果本次执行本身仍有复杂推理、严格质量要求或高返工风险，不应仅因已有计划而降级。',
          '如果是累计接入、修复失败、跨文件一致性、最终验收或高返工成本，应提高等级。',
          '只输出你对本次请求的绝对难度等级。',
          'level 必须是 1 到 10 的整数；confidence 表示你对本次评分的把握；reason 必须说明本次请求的具体依据，不要写泛泛的任务类别。',
          'factors 必须是 1 到 5 个短标签，只写本次评分最关键的因素，例如上下文跨度、约束耦合、严格格式、多步推理、性能要求、后续污染、返工成本、最终验收等；不要写模型名或价格。',
          '只输出 JSON：{"level":数字,"confidence":0到1,"reason":"一句话","factors":["短标签"]}。'
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
    scoringFallbackMaxLevel: config.scoringFallbackMaxLevel,
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
    factors: result.factors,
    reason: result.reason
  }, { ttlMs })
}

function parseHybridScoringResponse(body: Buffer): { level: number; confidence?: number; factors?: string[]; reason?: string } {
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
    factors: parseHybridScoringFactors(parsed.factors),
    reason: typeof parsed.reason === 'string' ? parsed.reason : undefined
  }
}

function parseHybridScoringFactors(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined
  const factors = value
    .map((item) => typeof item === 'string' ? item.trim() : '')
    .filter(Boolean)
    .map((item) => item.slice(0, 40))
  return factors.length ? [...new Set(factors)].slice(0, 5) : undefined
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

function failedScoringResult(
  config: ApiKeyHybridRoutingConfig,
  errorCode: string,
  errorMessage: string,
  scoringAccountId?: string,
  statusCode?: number
): HybridScoringResult {
  return {
    level: config.scoringFallbackMaxLevel,
    defaulted: false,
    failed: true,
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
