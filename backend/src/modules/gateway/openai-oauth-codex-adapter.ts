import type { Request } from 'express'

import {
  getGatewayRequestBodyState,
  gatewayJsonBodyLargeWarningBytes,
  type GatewayRawBodyRequest
} from './openai-gateway-request-body.js'
import {
  normalizeOpenAIOAuthCodexBodyInWorker,
  parseGatewayJsonBodyInWorker
} from './openai-gateway-json-parser.js'
import {
  OpenAIOAuthCodexAdapterError,
  normalizeOpenAIOAuthCodexParsedBody,
  type NormalizedCodexBody,
  type OpenAIOAuthCodexAccount,
  type OpenAIOAuthCodexIdentity,
  type OpenAIOAuthCodexSessionResolution
} from './openai-oauth-codex-normalizer.js'
import { getRequestLogger, sanitizeUrlForLog } from '../../shared/request-context.js'

export {
  OpenAIOAuthCodexAdapterError,
  isolateOpenAIOAuthCodexSessionId,
  type OpenAIOAuthCodexAccount,
  type OpenAIOAuthCodexIdentity
} from './openai-oauth-codex-normalizer.js'

export interface OpenAIOAuthCodexRequestParts {
  headers: Headers
  body?: string
}

type OpenAIOAuthCodexRawBodyRequest = GatewayRawBodyRequest & {
  openAIOAuthCodexLargeBodyLogged?: boolean
}

export async function buildOpenAIOAuthCodexRequestParts(
  req: Request,
  inputHeaders: Record<string, string | string[] | undefined>,
  account: OpenAIOAuthCodexAccount,
  identity: OpenAIOAuthCodexIdentity,
  signal?: AbortSignal
): Promise<OpenAIOAuthCodexRequestParts> {
  const compact = isOpenAIOAuthCodexCompactRequest(req)
  const normalizedBody = await normalizeOpenAIOAuthCodexBody(req, inputHeaders, account, identity, compact, signal)
  return {
    headers: buildOpenAIOAuthCodexHeaders(inputHeaders, account, {
      compact,
      stream: normalizedBody.stream,
      session: normalizedBody.session
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
  signal?: AbortSignal
): Promise<NormalizedCodexBody> {
  if (req.method === 'GET' || req.method === 'HEAD') {
    return { stream: false, session: {} }
  }

  const rawBody = (req as GatewayRawBodyRequest).rawBody
  if (rawBody && rawBody.length > gatewayJsonBodyLargeWarningBytes) {
    try {
      return await normalizeOpenAIOAuthCodexBodyInWorker(rawBody, {
        inputHeaders,
        account,
        identity,
        compact
      }, undefined, signal)
    } catch (error) {
      if (error instanceof OpenAIOAuthCodexAdapterError) {
        throw error
      }
      throw new OpenAIOAuthCodexAdapterError('请求体必须是有效的 JSON 对象')
    }
  }

  const body = await parseOpenAIOAuthCodexJsonObjectBody(req, signal)
  return normalizeOpenAIOAuthCodexParsedBody(body, {
    inputHeaders,
    account,
    identity,
    compact
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
      return rawBody.length > gatewayJsonBodyLargeWarningBytes
        ? await parseGatewayJsonBodyInWorker(rawBody, undefined, signal)
        : JSON.parse(rawBody.toString('utf8')) as unknown
    } catch (error) {
      if (error instanceof OpenAIOAuthCodexAdapterError) {
        throw error
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
  }, 'OpenAI OAuth Codex 大请求体进入兼容解析')
}

function buildOpenAIOAuthCodexHeaders(
  inputHeaders: Record<string, string | string[] | undefined>,
  account: OpenAIOAuthCodexAccount,
  input: {
    compact: boolean
    stream: boolean
    session: OpenAIOAuthCodexSessionResolution
  }
): Headers {
  const headers = new Headers()

  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'accept-language')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-client-request-id')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-codex-beta-features')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-codex-turn-state')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-codex-turn-metadata')

  const incomingOriginator = headerValue(inputHeaders, 'originator')
  const originator = isCodexOriginator(incomingOriginator) ? incomingOriginator : 'codex_cli_rs'
  headers.set('originator', originator)

  const incomingUserAgent = headerValue(inputHeaders, 'user-agent')
  const keepIncomingUserAgent = isCodexUserAgent(incomingUserAgent)
    || (isCodexOriginator(incomingOriginator) && Boolean(incomingUserAgent))
  headers.set('user-agent', keepIncomingUserAgent ? incomingUserAgent ?? openAICodexUserAgent : openAICodexUserAgent)

  const incomingVersion = headerValue(inputHeaders, 'version')
  headers.set('version', isVersionLike(incomingVersion) && isCodexOriginator(incomingOriginator) ? incomingVersion : openAICodexVersion)

  const openAIBeta = headerValue(inputHeaders, 'openai-beta')
  headers.set('openai-beta', openAIBeta && openAIBeta.toLowerCase().includes('responses') ? openAIBeta : 'responses=experimental')
  headers.set('authorization', `Bearer ${account.apiKey}`)
  headers.set('content-type', 'application/json')
  headers.set('accept', input.compact || !input.stream ? 'application/json' : 'text/event-stream')

  const chatGPTAccountId = stringCredential(account.credentials, 'chatgpt_account_id') ?? stringCredential(account.credentials, 'account_id')
  if (chatGPTAccountId) {
    headers.set('chatgpt-account-id', chatGPTAccountId)
  }
  if (input.session.sessionId) {
    headers.set('session_id', input.session.sessionId)
  }
  if (input.session.conversationId) {
    headers.set('conversation_id', input.session.conversationId)
  }

  return headers
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
  return isPlainObject(value) && Object.keys(value).length === 0
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function stringCredential(credentials: Record<string, unknown> | undefined, key: string): string | undefined {
  const value = credentials?.[key]
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function isCodexOriginator(value: string | undefined): value is string {
  return typeof value === 'string' && /^codex(?:_|$)/i.test(value.trim())
}

function isCodexUserAgent(value: string | undefined): value is string {
  return typeof value === 'string' && value.toLowerCase().includes('codex')
}

function isVersionLike(value: string | undefined): value is string {
  return typeof value === 'string' && /^[0-9]+(?:\.[0-9]+){1,3}(?:[-+][0-9a-z.-]+)?$/i.test(value.trim())
}

const openAICodexVersion = '0.125.0'
const openAICodexUserAgent = `codex_cli_rs/${openAICodexVersion}`
