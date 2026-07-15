import type { Request } from 'express'

import {
  getGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  gatewayJsonBodyLargeWarningBytes,
  type GatewayRawBodyRequest
} from '../../request/body.js'
import {
  isGatewayJsonWorkerQueueFullError,
  normalizeOpenAIOAuthCodexBodyInWorker,
  parseGatewayJsonBodyInWorker
} from '../../request/json-parser.js'
import {
  normalizeOpenAIOAuthCodexParsedBody,
  type NormalizedCodexBody,
  type OpenAIOAuthCodexAccount,
  type OpenAIOAuthCodexIdentity,
  type OpenAIOAuthCodexSessionResolution
} from './oauth-normalizer.js'
import { OpenAIOAuthCodexAdapterError } from './oauth-errors.js'
import { normalizeOpenAICodexClientHeaders } from './client-headers.js'
import { getRequestLogger, sanitizeUrlForLog } from '../../../../shared/request-context.js'
import type { GptRequestOverrideModelCapabilities } from '../../../providers/drivers/gpt/request-overrides.js'

export {
  isolateOpenAIOAuthCodexSessionId,
  type OpenAIOAuthCodexAccount,
  type OpenAIOAuthCodexIdentity
} from './oauth-normalizer.js'
export { OpenAIOAuthCodexAdapterError } from './oauth-errors.js'

export interface OpenAIOAuthCodexRequestParts {
  headers: Headers
  body?: string
}

type OpenAIOAuthCodexRawBodyRequest = GatewayRawBodyRequest & {
  openAIOAuthCodexLargeBodyLogged?: boolean
}

interface OpenAIOAuthCodexRequestOptions {
  modelOverride?: string
  requestOverrideModelCapabilities?: GptRequestOverrideModelCapabilities
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
  return {
    headers: buildOpenAIOAuthCodexHeaders(inputHeaders, account, {
      compact,
      stream: normalizedBody.stream,
      session: normalizedBody.session,
      model: normalizedBody.model
    }),
    body: normalizedBody.body
  }
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

  const rawBody = (req as GatewayRawBodyRequest).rawBody
  if (rawBody && rawBody.length > gatewayJsonBodyInlineParseMaxBytes) {
    try {
      return await normalizeOpenAIOAuthCodexBodyInWorker(rawBody, {
        inputHeaders,
        account,
        identity,
        compact,
        modelOverride: options.modelOverride,
        requestOverrideModelCapabilities: options.requestOverrideModelCapabilities
      }, undefined, signal)
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
      throw new OpenAIOAuthCodexAdapterError('请求体必须是有效的 JSON 对象')
    }
  }

  const body = await parseOpenAIOAuthCodexJsonObjectBody(req, signal)
  return normalizeOpenAIOAuthCodexParsedBody(body, {
    inputHeaders,
    account,
    identity,
    compact,
    modelOverride: options.modelOverride,
    requestOverrideModelCapabilities: options.requestOverrideModelCapabilities
  })
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

  const rawBody = (req as GatewayRawBodyRequest).rawBody
  if (rawBody && rawBody.length > 0) {
    try {
      return rawBody.length > gatewayJsonBodyInlineParseMaxBytes
        ? await parseGatewayJsonBodyInWorker(rawBody, undefined, signal)
        : JSON.parse(rawBody.toString('utf8')) as unknown
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
      throw new OpenAIOAuthCodexAdapterError('请求体必须是有效的 JSON 对象')
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
  const headers = new Headers()

  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'accept-language')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-client-request-id')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-codex-beta-features')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-codex-turn-state')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-codex-turn-metadata')
  copyOpenAIOAuthCodexAttestationHeader(headers, inputHeaders)

  const incomingOriginator = headerValue(inputHeaders, 'originator')
  if (incomingOriginator) headers.set('originator', incomingOriginator)
  const incomingUserAgent = headerValue(inputHeaders, 'user-agent')
  if (incomingUserAgent) headers.set('user-agent', incomingUserAgent)
  const incomingVersion = headerValue(inputHeaders, 'version')
  if (incomingVersion) headers.set('version', incomingVersion)
  normalizeOpenAICodexClientHeaders(headers, input.model)
  headers.set('authorization', `Bearer ${account.apiKey}`)
  headers.set('content-type', 'application/json')
  headers.set('accept', input.compact || !input.stream ? 'application/json' : 'text/event-stream')

  const accountId = stringCredential(account.credentials, 'account_id')
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

function copyAllowedOpenAIOAuthCodexHeader(
  output: Headers,
  inputHeaders: Record<string, string | string[] | undefined>,
  name: string
): void {
  const value = headerValue(inputHeaders, name)
  if (!value) {
    return
  }
  output.set(name, value)
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
