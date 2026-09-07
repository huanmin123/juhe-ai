import type { Request } from 'express'

import {
  getGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  gatewayJsonBodyLargeWarningBytes,
  type GatewayRawBodyRequest
} from '../../request/body.js'
import {
  GatewayJsonWorkerCanceledError,
  isGatewayJsonWorkerInvalidJsonError,
  isGatewayJsonWorkerQueueFullError,
  normalizeOpenAIOAuthCodexParsedBodyInWorker,
  parseGatewayRequestJsonBody
} from '../../request/json-parser.js'
import {
  normalizeOpenAIOAuthCodexParsedBody,
  type NormalizedCodexBody,
  type OpenAIOAuthCodexAccount,
  type OpenAIOAuthCodexIdentity,
  type OpenAIOAuthCodexSessionResolution
} from './oauth-normalizer.js'
import { OpenAIOAuthCodexAdapterError } from './oauth-errors.js'
import { isOpenAICodexClientHeaders, normalizeOpenAICodexClientHeaders } from './client-headers.js'
import { copyOfficialOAuthClientRequestHeaders } from '../../upstream/header-policy.js'
import { getRequestLogger, sanitizeUrlForLog } from '../../../../shared/request-context.js'
import type { GptRequestOverrideModelCapabilities } from '../../../providers/drivers/gpt/request-overrides.js'
import { markGatewayCodexHistorySanitized } from '../../request/serialized-json-body.js'

export {
  isolateOpenAIOAuthCodexSessionId,
  type OpenAIOAuthCodexAccount,
  type OpenAIOAuthCodexIdentity
} from './oauth-normalizer.js'
export { OpenAIOAuthCodexAdapterError } from './oauth-errors.js'

export interface OpenAIOAuthCodexRequestParts {
  headers: Headers
  body?: Buffer | string
}

type OpenAIOAuthCodexRawBodyRequest = GatewayRawBodyRequest & {
  openAIOAuthCodexLargeBodyLogged?: boolean
  openAIOAuthCodexNormalizedBodyCache?: {
    rawBody?: Buffer
    parsedBody: unknown
    values: Map<string, Promise<NormalizedCodexBody>>
  }
}

let openAIOAuthCodexNormalizationObserverForTest: (() => void) | undefined

export function setOpenAIOAuthCodexNormalizationObserverForTest(observer: (() => void) | undefined): void {
  openAIOAuthCodexNormalizationObserverForTest = observer
}

interface OpenAIOAuthCodexRequestOptions {
  modelOverride?: string
  requestOverrideModelCapabilities?: GptRequestOverrideModelCapabilities
  sanitizeCodexHistory?: boolean
}

export async function buildOpenAIOAuthCodexRequestParts(
  req: Request,
  inputHeaders: Record<string, string | string[] | undefined>,
  account: OpenAIOAuthCodexAccount,
  identity: OpenAIOAuthCodexIdentity,
  signal?: AbortSignal,
  options: OpenAIOAuthCodexRequestOptions = {}
): Promise<OpenAIOAuthCodexRequestParts> {
  const compact = isOpenAIOAuthCodexCompactRequest(req)
  const normalizedBody = await normalizeOpenAIOAuthCodexBody(req, inputHeaders, account, identity, compact, signal, options)
  const sanitizedBody = normalizedBody.bodyBytes
    ? normalizedCodexBodyBuffer(normalizedBody.bodyBytes)
    : undefined
  return {
    headers: buildOpenAIOAuthCodexHeaders(inputHeaders, account, {
      compact,
      stream: normalizedBody.stream,
      session: normalizedBody.session,
      model: normalizedBody.model
    }),
    body: sanitizedBody && normalizedBody.codexHistorySanitized
      ? markGatewayCodexHistorySanitized(sanitizedBody)
      : normalizedBody.body
  }
}

function normalizedCodexBodyBuffer(body: Uint8Array): Buffer {
  return Buffer.isBuffer(body)
    ? body
    : Buffer.from(body.buffer, body.byteOffset, body.byteLength)
}

export function isOpenAIOAuthCodexCompactRequest(req: Request): boolean {
  const path = (req.originalUrl || req.path || '').split('?', 1)[0] || ''
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/responses/compact'
}

async function normalizeOpenAIOAuthCodexBody(
  req: Request,
  inputHeaders: Record<string, string | string[] | undefined>,
  account: OpenAIOAuthCodexAccount,
  identity: OpenAIOAuthCodexIdentity,
  compact: boolean,
  signal?: AbortSignal,
  options: OpenAIOAuthCodexRequestOptions = {}
): Promise<NormalizedCodexBody> {
  if (req.method === 'GET' || req.method === 'HEAD') {
    return { stream: false, session: {} }
  }

  const body = await parseOpenAIOAuthCodexJsonObjectBody(req, signal)
  const normalizeInput = {
    inputHeaders,
    account,
    identity,
    compact,
    sanitizeCodexHistory: options.sanitizeCodexHistory,
    modelOverride: options.modelOverride,
    requestOverrideModelCapabilities: options.requestOverrideModelCapabilities
  }
  const rawBodyBytes = (req as GatewayRawBodyRequest).rawBody?.byteLength ?? 0
  if (rawBodyBytes > gatewayJsonBodyInlineParseMaxBytes) {
    return await normalizeLargeOpenAIOAuthCodexBody(req, body, rawBodyBytes, normalizeInput, signal)
  }
  return normalizeOpenAIOAuthCodexParsedBody(body, normalizeInput)
}

// worker_threads clones parsedBody synchronously in postMessage. Equivalent
// account attempts reuse this result to avoid cloning the full request again.
async function normalizeLargeOpenAIOAuthCodexBody(
  req: Request,
  body: unknown,
  rawBodyBytes: number,
  normalizeInput: Parameters<typeof normalizeOpenAIOAuthCodexParsedBody>[1],
  signal?: AbortSignal
): Promise<NormalizedCodexBody> {
  if (signal?.aborted) {
    throw new GatewayJsonWorkerCanceledError('网关 JSON worker 任务已取消')
  }
  const request = req as OpenAIOAuthCodexRawBodyRequest
  const rawBody = request.rawBody
  let cache = request.openAIOAuthCodexNormalizedBodyCache
  if (!cache || cache.rawBody !== rawBody || cache.parsedBody !== body) {
    cache = {
      rawBody,
      parsedBody: body,
      values: new Map()
    }
    request.openAIOAuthCodexNormalizedBodyCache = cache
  }
  const key = openAIOAuthCodexNormalizationCacheKey(normalizeInput)
  const existing = cache.values.get(key)
  if (existing) return await existing

  openAIOAuthCodexNormalizationObserverForTest?.()
  const normalization = normalizeOpenAIOAuthCodexParsedBodyInWorker(
    body,
    rawBodyBytes,
    normalizeInput,
    undefined,
    signal
  )
  cache.values.set(key, normalization)
  try {
    return await normalization
  } catch (error) {
    if (cache.values.get(key) === normalization) {
      cache.values.delete(key)
    }
    if (isGatewayJsonWorkerQueueFullError(error)) {
      throw new OpenAIOAuthCodexAdapterError('网关请求解析繁忙，请稍后重试', 'server_overloaded', {
        statusCode: 503,
        type: 'server_overloaded'
      })
    }
    throw error
  }
}

function openAIOAuthCodexNormalizationCacheKey(
  input: Parameters<typeof normalizeOpenAIOAuthCodexParsedBody>[1]
): string {
  return JSON.stringify([
    input.compact,
    input.sanitizeCodexHistory === true,
    input.modelOverride ?? '',
    input.identity.systemAccountId,
    input.identity.apiKeyId ?? '',
    headerValue(input.inputHeaders, 'session-id') ?? '',
    headerValue(input.inputHeaders, 'thread-id') ?? '',
    headerValue(input.inputHeaders, 'prompt_cache_key') ?? '',
    headerValue(input.inputHeaders, 'x-prompt-cache-key') ?? '',
    stableNormalizationCacheToken(input.account.credentials?.service_tier_override),
    stableNormalizationCacheToken(input.account.credentials?.reasoning_effort_override),
    input.requestOverrideModelCapabilities?.supportedServiceTiers ?? [],
    input.requestOverrideModelCapabilities?.supportedReasoningEfforts ?? []
  ])
}

function stableNormalizationCacheToken(value: unknown): string {
  if (value === undefined) return 'undefined'
  if (value === null) return 'null'
  if (typeof value === 'string') return `string:${value}`
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint') {
    return `${typeof value}:${String(value)}`
  }
  try {
    return `${typeof value}:${JSON.stringify(value)}`
  } catch {
    return `${typeof value}:${Object.prototype.toString.call(value)}`
  }
}

async function parseOpenAIOAuthCodexJsonObjectBody(req: Request, signal?: AbortSignal): Promise<unknown> {
  logOpenAIOAuthCodexLargeBodyParse(req)
  if (req.body !== undefined) {
    return req.body
  }
  const requestWithBody = req as GatewayRawBodyRequest
  if (requestWithBody.gatewayParsedJsonBodyAvailable) {
    return requestWithBody.gatewayParsedJsonBody
  }

  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.jsonParseStatus === 'invalid_json') {
    throw new OpenAIOAuthCodexAdapterError('请求体必须是有效的 JSON 对象')
  }

  const rawBody = requestWithBody.rawBody
  if (rawBody && rawBody.length > 0) {
    try {
      return await parseGatewayRequestJsonBody(req, undefined, signal)
    } catch (error) {
      if (error instanceof OpenAIOAuthCodexAdapterError) {
        throw error
      }
      if (isGatewayJsonWorkerQueueFullError(error)) {
        throw new OpenAIOAuthCodexAdapterError('网关请求解析繁忙，请稍后重试', 'server_overloaded', {
          statusCode: 503,
          type: 'server_overloaded'
        })
      }
      if (isGatewayJsonWorkerInvalidJsonError(error)) {
        throw new OpenAIOAuthCodexAdapterError('请求体必须是有效的 JSON 对象')
      }
      throw error
    }
  }

  if (req.body === undefined || isEmptyPlainObject(req.body)) {
    return {}
  }
  return req.body
}

function logOpenAIOAuthCodexLargeBodyParse(req: Request): void {
  const request = req as OpenAIOAuthCodexRawBodyRequest
  const rawBody = request.rawBody
  const bodyState = getGatewayRequestBodyState(req)
  if (request.openAIOAuthCodexLargeBodyLogged || !rawBody || rawBody.length <= gatewayJsonBodyLargeWarningBytes) {
    return
  }
  request.openAIOAuthCodexLargeBodyLogged = true
  getRequestLogger().warn({
    event: 'openai_oauth_codex_large_body_parse',
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl || req.path || ''),
    rawBodyBytes: rawBody.length,
    jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes,
    gatewayJsonParseStatus: bodyState?.jsonParseStatus
  }, 'OpenAI OAuth Codex 大请求体进入受限解析')
}

function buildOpenAIOAuthCodexHeaders(
  inputHeaders: Record<string, string | string[] | undefined>,
  account: OpenAIOAuthCodexAccount,
  input: {
    compact: boolean
    stream: boolean
    session: OpenAIOAuthCodexSessionResolution
    model?: string
  }
): Headers {
  const validatedAttestation = new Headers()
  copyOpenAIOAuthCodexAttestationHeader(validatedAttestation, inputHeaders)
  const headers = copySafeOpenAIOAuthCodexClientHeaders(inputHeaders)
  const attestation = validatedAttestation.get('x-oai-attestation')
  if (attestation) headers.set('x-oai-attestation', attestation)
  const nativeCodexClient = isOpenAICodexClientHeaders(headers)
  normalizeOpenAICodexClientHeaders(headers, input.model)
  headers.set('authorization', `Bearer ${account.apiKey}`)
  headers.set('content-type', 'application/json')
  if (!headers.has('openai-beta')) headers.set('openai-beta', 'responses=experimental')
  if (!nativeCodexClient) {
    headers.set('accept', input.compact || !input.stream ? 'application/json' : 'text/event-stream')
  }

  const accountId = stringCredential(account.credentials, 'account_id')
    || stringCredential(account.credentials, 'chatgpt_account_id')
  if (accountId) {
    headers.set('chatgpt-account-id', accountId)
  }
  if (input.session.sessionId) {
    headers.set('session-id', input.session.sessionId)
  }
  if (input.session.conversationId) {
    headers.set('thread-id', input.session.conversationId)
    if (!headers.get('x-client-request-id')) {
      headers.set('x-client-request-id', input.session.conversationId)
    }
  }

  return headers
}

function copyOpenAIOAuthCodexAttestationHeader(
  output: Headers,
  inputHeaders: Record<string, string | string[] | undefined>
): void {
  const value = headerValue(inputHeaders, 'x-oai-attestation')
  if (!value) return
  if (value.length > 32 * 1024 || /[\r\n\0]/.test(value)) {
    throw new OpenAIOAuthCodexAdapterError(
      'Codex 设备证明 header 无效',
      'invalid_openai_oauth_codex_attestation'
    )
  }
  output.set('x-oai-attestation', value)
}

function copySafeOpenAIOAuthCodexClientHeaders(
  inputHeaders: Record<string, string | string[] | undefined>
): Headers {
  return copyOfficialOAuthClientRequestHeaders(inputHeaders, 'openai_codex')
}

function headerValue(inputHeaders: Record<string, string | string[] | undefined>, name: string): string | undefined {
  const lowerName = name.toLowerCase()
  for (const [inputName, value] of Object.entries(inputHeaders)) {
    if (inputName.toLowerCase() !== lowerName) {
      continue
    }
    const first = Array.isArray(value) ? value.find((item) => item.trim()) : value
    return typeof first === 'string' && first.trim() ? first.trim() : undefined
  }
  return undefined
}

function isEmptyPlainObject(value: unknown): boolean {
  if (!isPlainObject(value)) return false
  for (const key in value) {
    if (Object.prototype.hasOwnProperty.call(value, key)) return false
  }
  return true
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function stringCredential(credentials: Record<string, unknown> | undefined, key: string): string | undefined {
  const value = credentials?.[key]
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}
