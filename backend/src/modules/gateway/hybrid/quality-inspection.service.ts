import type { Request } from 'express'

import type {
  ApiKeyHybridLevelRoute,
  ApiKeyHybridQualityInspectionConfig,
  ApiKeyHybridRoutingConfig
} from '../../../domain/types.js'
import type { GatewayApiKeyRow } from '../../../storage/repositories.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { getGatewayRequestBodyState, type GatewayRawBodyRequest } from '../request/body.js'
import { parseGatewayRequestJsonBody } from '../request/json-parser.js'
import { requestModel } from '../request/metadata.js'
import { recordHybridScoringAttempt } from '../usage/records.js'
import {
  dispatchHybridAuxiliaryChatCompletion,
  emptyHybridAuxiliaryUsage
} from './auxiliary-dispatch.service.js'
import type { HybridScoringResult } from './scoring.service.js'

export type HybridQualityFailureType =
  | 'protocol_invalid'
  | 'missing_required_output'
  | 'low_quality'
  | 'unsafe_or_policy'
  | 'tool_or_schema_mismatch'
  | 'other'

export type HybridQualityRetryRecommendation =
  | 'accept'
  | 'retry_same_model'
  | 'upgrade_next_level'
  | 'return_error'
export type HybridQualityAction = HybridQualityRetryRecommendation | 'repair_then_upgrade'
  | 'pass_through'

export interface HybridQualityScoreResult {
  pass: boolean
  score: number
  confidence?: number
  failureType?: HybridQualityFailureType
  reason?: string
  retryRecommendation: HybridQualityRetryRecommendation
}

export interface HybridQualityInspectionOutcome {
  triggered: boolean
  triggerReason: string
  pass: boolean
  result?: HybridQualityScoreResult
  actualAction?: HybridQualityAction
  qualityAccountId?: string
  statusCode?: number
  errorCode?: string
  errorMessage?: string
}

const hybridQualityContextMaxBytes = 192 * 1024
const hybridQualityResponseMaxBytes = 2 * 1024 * 1024
const hybridQualityRequestParseMaxBytes = 128 * 1024

export async function inspectHybridGatewayQuality(input: {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  config: ApiKeyHybridRoutingConfig
  scoring: HybridScoringResult
  targetRoute: ApiKeyHybridLevelRoute
  targetModel: string
  responseBodyText: string
  traceId: string
  clientIp?: string
  endpoint: string
  signal?: AbortSignal
}): Promise<HybridQualityInspectionOutcome> {
  const qualityConfig = input.config.qualityInspection
  const trigger = shouldTriggerHybridQualityInspection({
    req: input.req,
    config: input.config,
    scoring: input.scoring,
    targetRoute: input.targetRoute,
    responseBodyText: input.responseBodyText
  })
  if (!qualityConfig?.enabled || !trigger.triggered) {
    return {
      triggered: false,
      triggerReason: qualityConfig?.enabled ? trigger.reason : 'quality_inspection_disabled',
      pass: true
    }
  }

  const startedAt = Date.now()
  try {
    const requestBody = await parseHybridQualityRequestBody(input.req, input.signal)
    const context = buildHybridQualityContext({
      req: input.req,
      requestBody,
      responseBodyText: input.responseBodyText,
      scoring: input.scoring,
      targetModel: input.targetModel,
      triggerReason: trigger.reason
    })
    const qualityBody = buildHybridQualityRequestBody(qualityConfig, context)
    const qualityReq = createHybridQualityGatewayRequest(qualityBody)
    const dispatch = await dispatchHybridAuxiliaryChatCompletion({
      req: qualityReq,
      apiKeyRecord: input.apiKeyRecord,
      targetModel: qualityConfig.scoringModel,
      traceId: input.traceId,
      clientIp: input.clientIp,
      endpoint: input.endpoint,
      trafficSource: 'hybrid_quality_scoring',
      timeoutMs: input.config.scoringTimeoutMs,
      responseMaxBytes: hybridQualityResponseMaxBytes,
      noAccountErrorCode: 'no_quality_scoring_account',
      noAccountErrorMessage: '混合路由绑定分组池没有可用质量评分账户',
      dispatchErrorCode: 'hybrid_quality_scoring_failed',
      dispatchErrorMessage: '混合路由质量评分模型调用失败',
      httpErrorCode: 'hybrid_quality_scoring_http_error',
      responseTooLargeMessage: '混合路由质量评分响应超过保护上限',
      signal: input.signal,
      requestClientCompatibility: 'openai_standard'
    })
    if (dispatch.outcome === 'failed') {
      if (dispatch.shouldRecordUsage && dispatch.account && dispatch.groupId) {
        await recordHybridScoringAttempt({
          traceId: input.traceId,
          clientIp: input.clientIp,
          systemAccountId: input.apiKeyRecord.system_account_id,
          apiKeyId: input.apiKeyRecord.id,
          groupId: dispatch.groupId,
          account: dispatch.account,
          endpoint: `${input.endpoint}#hybrid-quality-scoring`,
          statusCode: dispatch.statusCode,
          success: false,
          startedAt,
          scoringModel: qualityConfig.scoringModel,
          usage: emptyHybridAuxiliaryUsage(),
          errorCode: dispatch.errorCode,
          errorMessage: dispatch.errorMessage,
          requestSnapshot: { model: qualityConfig.scoringModel, contextBytes: Buffer.byteLength(context, 'utf8') },
          responseSnapshot: { statusCode: dispatch.statusCode },
          trafficSource: 'hybrid_quality_scoring'
        })
      }
      return qualityInspectionUnavailable(
        dispatch.errorCode,
        dispatch.errorMessage,
        qualityConfig.unavailableAction,
        dispatch.account?.id,
        dispatch.statusCode
      )
    }
    let dispatchFinished = false
    const finishDispatch = async (finish: Parameters<typeof dispatch.finish>[0]) => {
      await dispatch.finish(finish)
      dispatchFinished = true
    }
    try {
      let parsed: HybridQualityScoreResult
      try {
        parsed = parseHybridQualityResponse(dispatch.responseBody)
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : String(error)
        await finishDispatch({ success: false, errorCode: 'hybrid_quality_scoring_failed', errorMessage })
        await recordHybridScoringAttempt({
          traceId: input.traceId,
          clientIp: input.clientIp,
          systemAccountId: input.apiKeyRecord.system_account_id,
          apiKeyId: input.apiKeyRecord.id,
          groupId: dispatch.groupId,
          account: dispatch.account,
          endpoint: `${input.endpoint}#hybrid-quality-scoring`,
          statusCode: dispatch.statusCode,
          success: false,
          startedAt,
          scoringModel: qualityConfig.scoringModel,
          usage: dispatch.usage,
          errorCode: 'hybrid_quality_scoring_failed',
          errorMessage,
          requestSnapshot: { model: qualityConfig.scoringModel, contextBytes: Buffer.byteLength(context, 'utf8') },
          responseSnapshot: { statusCode: dispatch.statusCode, body: responseBodySnippet(dispatch.responseBody) },
          trafficSource: 'hybrid_quality_scoring'
        })
        return qualityInspectionUnavailable(
          'hybrid_quality_scoring_failed',
          errorMessage,
          qualityConfig.unavailableAction,
          dispatch.account.id,
          dispatch.statusCode
        )
      }
      const actualAction = resolveHybridQualityAction(parsed, qualityConfig)
      await finishDispatch({ success: true })
      try {
        await recordHybridScoringAttempt({
          traceId: input.traceId,
          clientIp: input.clientIp,
          systemAccountId: input.apiKeyRecord.system_account_id,
          apiKeyId: input.apiKeyRecord.id,
          groupId: dispatch.groupId,
          account: dispatch.account,
          endpoint: `${input.endpoint}#hybrid-quality-scoring`,
          statusCode: dispatch.statusCode,
          success: true,
          startedAt,
          scoringModel: qualityConfig.scoringModel,
          usage: dispatch.usage,
          requestSnapshot: { model: qualityConfig.scoringModel, contextBytes: Buffer.byteLength(context, 'utf8') },
          responseSnapshot: { statusCode: dispatch.statusCode, parsed },
          trafficSource: 'hybrid_quality_scoring'
        })
      } catch (error) {
        logger.warn(errorLogFields(error, {
          event: 'hybrid_quality_scoring_success_usage_record_failed',
          traceId: input.traceId,
          accountId: dispatch.account.id
        }), '混合路由质量评分已完成，成功使用记录写入失败')
      }
      return {
        triggered: true,
        triggerReason: trigger.reason,
        pass: parsed.pass,
        result: parsed,
        actualAction,
        qualityAccountId: dispatch.account.id,
        statusCode: dispatch.statusCode
      }
    } finally {
      if (!dispatchFinished) {
        await dispatch.finish({ success: false, errorCode: 'hybrid_quality_scoring_failed', errorMessage: '混合路由质量评分调用未完成收尾' })
      }
    }
  } catch (error) {
    return qualityInspectionUnavailable(
      'hybrid_quality_scoring_failed',
      error instanceof Error ? error.message : String(error),
      input.config.qualityInspection?.unavailableAction ?? 'pass_through',
      undefined,
      undefined
    )
  }
}

export function shouldTriggerHybridQualityInspection(input: {
  req: Request
  config: ApiKeyHybridRoutingConfig
  scoring: HybridScoringResult
  targetRoute: ApiKeyHybridLevelRoute
  responseBodyText: string
}): { triggered: boolean; reason: string } {
  const qualityConfig = input.config.qualityInspection
  if (!qualityConfig?.enabled) {
    return { triggered: false, reason: 'quality_inspection_disabled' }
  }
  if (qualityConfig.triggerMode === 'always_for_hybrid') {
    return { triggered: true, reason: 'always_for_hybrid' }
  }
  if (qualityConfig.triggerMode === 'quality_first_only') {
    return input.config.qualityPreference === 'quality_first'
      ? { triggered: true, reason: 'quality_first_preference' }
      : { triggered: false, reason: 'quality_first_only_not_matched' }
  }
  if (input.config.qualityPreference === 'quality_first') {
    return { triggered: true, reason: 'quality_first_preference' }
  }
  if (hasStrictOutputRequirement(input.req)) {
    return { triggered: true, reason: 'strict_output_requirement' }
  }
  if (!input.responseBodyText.trim()) {
    return { triggered: true, reason: 'empty_response_body' }
  }
  if (isLowOrMidTargetRoute(input.targetRoute, qualityConfig.maxTriggerLevel)) {
    return { triggered: true, reason: 'low_or_mid_route_level' }
  }
  return { triggered: false, reason: 'low_risk_request' }
}

function isLowOrMidTargetRoute(route: ApiKeyHybridLevelRoute, maxTriggerLevel: number): boolean {
  return route.minLevel <= maxTriggerLevel
}

function resolveHybridQualityAction(
  result: HybridQualityScoreResult,
  config: ApiKeyHybridQualityInspectionConfig
): HybridQualityAction {
  if (result.pass) return 'accept'
  if (result.failureType === 'unsafe_or_policy') return 'return_error'
  if (result.retryRecommendation === 'return_error') return 'return_error'
  return config.failureAction
}

async function parseHybridQualityRequestBody(req: Request, signal?: AbortSignal): Promise<Record<string, unknown> | undefined> {
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
  if (!request.rawBody?.length || request.rawBody.length > hybridQualityRequestParseMaxBytes) {
    return undefined
  }
  const parsed = await parseGatewayRequestJsonBody(req, undefined, signal)
  return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
    ? parsed as Record<string, unknown>
    : undefined
}

function buildHybridQualityRequestBody(
  config: ApiKeyHybridQualityInspectionConfig,
  context: string
): Record<string, unknown> {
  return {
    model: config.scoringModel,
    stream: false,
    temperature: 0,
    max_tokens: 220,
    messages: [
      {
        role: 'system',
        content: [
          '你是网关响应质量评分器，只判断一个 200 响应是否足以交付。',
          '必须根据原始请求目标、必要约束覆盖度、输出协议、明显遗漏、自相矛盾、可验证性、失败返工成本和安全边界进行抽象判断。',
          '不要按业务领域、技术栈、模块名、关键词、样本题类型或模型名称给固定结论；同一领域请求可能因为约束和风险不同而不同。',
          '只输出 JSON：{"pass":布尔值,"score":0到100,"confidence":0到1,"failureType":"protocol_invalid|missing_required_output|low_quality|unsafe_or_policy|tool_or_schema_mismatch|other","reason":"一句话","retryRecommendation":"accept|retry_same_model|upgrade_next_level|return_error"}。'
        ].join('\n')
      },
      {
        role: 'user',
        content: context
      }
    ]
  }
}

function buildHybridQualityContext(input: {
  req: Request
  requestBody: Record<string, unknown> | undefined
  responseBodyText: string
  scoring: HybridScoringResult
  targetModel: string
  triggerReason: string
}): string {
  const payload = {
    method: input.req.method,
    path: input.req.originalUrl.split('?')[0] || input.req.path,
    originalOrCurrentModel: requestModel(input.req),
    targetModel: input.targetModel,
    triggerReason: input.triggerReason,
    routeScoring: {
      level: input.scoring.level,
      confidence: input.scoring.confidence,
      reason: input.scoring.reason,
      defaulted: input.scoring.defaulted
    },
    request: input.requestBody
      ? sanitizeQualityValue(input.requestBody, { bytes: 0, depth: 0 })
      : requestBodySummary(input.req),
    response: sanitizeQualityResponseText(input.responseBodyText)
  }
  const text = JSON.stringify(payload)
  if (Buffer.byteLength(text, 'utf8') <= hybridQualityContextMaxBytes) {
    return text
  }
  return JSON.stringify({
    ...payload,
    request: '[request_omitted_for_quality_context_size]',
    response: sanitizeQualityResponseText(input.responseBodyText, 8192),
    truncated: true
  })
}

function hasStrictOutputRequirement(req: Request): boolean {
  const body = typeof req.body === 'object' && req.body !== null && !Array.isArray(req.body)
    ? req.body as Record<string, unknown>
    : undefined
  if (!body) {
    const state = getGatewayRequestBodyState(req)
    return state?.imageGeneration === true || state?.imageGenerationForced === true
  }
  return Boolean(
    body.response_format
    || body.tools
    || body.tool_choice
  )
}

function requestBodySummary(req: Request): unknown {
  const state = getGatewayRequestBodyState(req)
  if (!state) return undefined
  return {
    rawBodyBytes: state.rawBodyBytes,
    contentType: state.contentType,
    jsonParseStatus: state.jsonParseStatus,
    model: state.model,
    stream: state.stream,
    imageGeneration: state.imageGeneration,
    imageGenerationForced: state.imageGenerationForced
  }
}

function sanitizeQualityValue(value: unknown, context: { bytes: number; depth: number }): unknown {
  if (value === undefined || value === null || typeof value === 'number' || typeof value === 'boolean') {
    context.bytes += 8
    return value
  }
  if (typeof value === 'string') {
    const maxLength = Math.max(0, Math.min(4096, hybridQualityContextMaxBytes - context.bytes))
    context.bytes += Math.min(Buffer.byteLength(value, 'utf8'), maxLength)
    return value.length > maxLength ? `${value.slice(0, maxLength)}...[truncated]` : value
  }
  if (context.depth >= 8 || context.bytes >= hybridQualityContextMaxBytes) {
    return '[truncated]'
  }
  if (Array.isArray(value)) {
    const output: unknown[] = []
    for (let index = 0; index < value.length && index < 50; index += 1) {
      context.depth += 1
      output.push(sanitizeQualityValue(value[index], context))
      context.depth -= 1
      if (context.bytes >= hybridQualityContextMaxBytes) break
    }
    if (value.length > output.length) output.push(`[${value.length - output.length} items truncated]`)
    return output
  }
  if (typeof value === 'object') {
    const output: Record<string, unknown> = {}
    let count = 0
    for (const [key, item] of Object.entries(value as Record<string, unknown>)) {
      if (count >= 80 || context.bytes >= hybridQualityContextMaxBytes) {
        output._truncated = true
        break
      }
      context.bytes += Buffer.byteLength(key, 'utf8')
      context.depth += 1
      output[key] = sanitizeQualityValue(item, context)
      context.depth -= 1
      count += 1
    }
    return output
  }
  return String(value)
}

function sanitizeQualityResponseText(value: string, maxChars = 16_384): string {
  return value.length > maxChars ? `${value.slice(0, maxChars)}...[truncated]` : value
}

function createHybridQualityGatewayRequest(body: Record<string, unknown>): Request {
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

function parseHybridQualityResponse(body: Buffer): HybridQualityScoreResult {
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
    throw new Error('质量评分模型未返回 JSON')
  }
  const parsed = JSON.parse(jsonText) as Record<string, unknown>
  const pass = parsed.pass === true
  const score = Number(parsed.score)
  const confidence = Number(parsed.confidence)
  const failureType = normalizeFailureType(parsed.failureType)
  const retryRecommendation = normalizeRetryRecommendation(parsed.retryRecommendation, pass)
  return {
    pass,
    score: Number.isFinite(score) ? Math.max(0, Math.min(100, score)) : pass ? 100 : 0,
    confidence: Number.isFinite(confidence) ? Math.max(0, Math.min(1, confidence)) : undefined,
    failureType,
    reason: typeof parsed.reason === 'string' ? parsed.reason : undefined,
    retryRecommendation
  }
}

function normalizeFailureType(value: unknown): HybridQualityFailureType | undefined {
  if (
    value === 'protocol_invalid'
    || value === 'missing_required_output'
    || value === 'low_quality'
    || value === 'unsafe_or_policy'
    || value === 'tool_or_schema_mismatch'
    || value === 'other'
  ) {
    return value
  }
  return undefined
}

function normalizeRetryRecommendation(value: unknown, pass: boolean): HybridQualityRetryRecommendation {
  if (pass) return 'accept'
  if (
    value === 'retry_same_model'
    || value === 'upgrade_next_level'
    || value === 'return_error'
  ) {
    return value
  }
  return 'upgrade_next_level'
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

function qualityInspectionUnavailable(
  errorCode: string,
  errorMessage: string,
  unavailableAction: ApiKeyHybridQualityInspectionConfig['unavailableAction'],
  qualityAccountId?: string,
  statusCode?: number
): HybridQualityInspectionOutcome {
  const passThrough = unavailableAction === 'pass_through'
  return {
    triggered: true,
    triggerReason: 'quality_scoring_unavailable',
    pass: passThrough,
    result: {
      pass: false,
      score: 0,
      reason: errorMessage,
      retryRecommendation: 'return_error'
    },
    actualAction: passThrough ? 'pass_through' : 'return_error',
    qualityAccountId,
    statusCode,
    errorCode,
    errorMessage
  }
}
