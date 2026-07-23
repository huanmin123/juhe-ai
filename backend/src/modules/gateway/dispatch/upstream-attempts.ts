import type { Request } from 'express'

import { getRequestLogger, logRequestStage, sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import type { GatewayTimeoutProfile } from '../policy/timeout-profile.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  isEffectiveOpenAIStreamRequest,
  requestUpstream,
  upstreamRequestTimeoutMs,
  upstreamSocketTimeoutMs,
  type GatewayUpstreamResponse
} from '../upstream/request.js'
import { transformGatewayUpstreamResponseForAccount } from '../../providers/drivers/registry.js'
import type { ClientCompatibilityCapability } from '../../../domain/types.js'
import {
  codexResponsesChatBridgeCompletionHandlerForRequest,
  getCodexResponsesContextState
} from '../codex-responses/chat-bridge-state.js'
import type { FirstByteDeadlineHandler } from '../upstream/first-byte-deadline.js'

interface PerformUpstreamRequestAttemptInput {
  req: Request
  account: UpstreamAccount
  upstreamUrl: string
  attemptIndex: number
  auditAttemptIndex: number
  headers: Headers
  body?: Buffer | string
  timeoutProfile: GatewayTimeoutProfile
  attemptStartedAt: number
  signal?: AbortSignal
  requestClientCompatibility?: ClientCompatibilityCapability
  firstByteDeadlineMs?: number
  onFirstByteDeadline?: FirstByteDeadlineHandler
}

export async function performUpstreamRequestAttempt(
  input: PerformUpstreamRequestAttemptInput
): Promise<GatewayUpstreamResponse> {
  const {
    req,
    account,
    upstreamUrl,
    attemptIndex,
    auditAttemptIndex,
    headers,
    body,
    timeoutProfile,
    attemptStartedAt,
    signal,
    requestClientCompatibility,
    firstByteDeadlineMs,
    onFirstByteDeadline
  } = input
  const socketTimeoutMs = upstreamSocketTimeoutMs(req, timeoutProfile, account)
  const requestTimeoutMs = upstreamRequestTimeoutMs(timeoutProfile)
  const safeUpstreamUrl = sanitizeUrlCredentialsForLog(upstreamUrl) ?? 'unknown'
  const upstreamBody = normalizeAnthropicMessagesBodyForAttempt(headers, upstreamUrl, body)

  getRequestLogger().debug({
    event: 'gateway_upstream_request_started',
    accountId: account.id,
    accountType: account.type,
    accountStatus: account.status,
    upstreamUrl: safeUpstreamUrl,
    attemptIndex,
    auditAttemptIndex,
    method: req.method,
    stream: isEffectiveOpenAIStreamRequest(req, account),
    requestBodyBytes: typeof upstreamBody === 'string' ? Buffer.byteLength(upstreamBody, 'utf8') : upstreamBody?.byteLength,
    socketTimeoutMs,
    requestTimeoutMs,
    proxyEnabled: Boolean(account.proxyUrl)
  }, '网关开始请求上游')

  const fetchHeadersStartedAt = performance.now()
  let response: GatewayUpstreamResponse
  try {
    response = await requestUpstream(upstreamUrl, {
      method: req.method,
      headers,
      body: upstreamBody,
      proxyUrl: account.proxyUrl,
      timeoutMs: socketTimeoutMs,
      requestTimeoutMs,
      firstByteDeadlineMs,
      firstByteDeadlineTransport: isEffectiveOpenAIStreamRequest(req, account) ? 'stream' : 'non_stream',
      onFirstByteDeadline,
      signal,
      transport: upstreamTransportForAttempt(headers, upstreamUrl)
    })
    logRequestStage('upstream.fetch_headers', {
      accountId: account.id,
      providerCode: account.providerCode,
      attemptIndex,
      auditAttemptIndex,
      statusCode: response.status,
      ok: response.ok,
      proxyEnabled: Boolean(account.proxyUrl)
    }, 'success', fetchHeadersStartedAt)
  } catch (error) {
    logRequestStage('upstream.fetch_headers', {
      accountId: account.id,
      providerCode: account.providerCode,
      attemptIndex,
      auditAttemptIndex,
      failureReason: signal?.aborted ? 'request_aborted' : 'upstream_request_error',
      decisionInputs: {
        socketTimeoutMs,
        requestTimeoutMs,
        proxyEnabled: Boolean(account.proxyUrl)
      }
    }, signal?.aborted ? 'aborted' : 'expected_failure', fetchHeadersStartedAt)
    throw error
  }

  getRequestLogger().debug({
    event: 'gateway_upstream_response_received',
    accountId: account.id,
    accountType: account.type,
    upstreamUrl: safeUpstreamUrl,
    attemptIndex,
    auditAttemptIndex,
    statusCode: response.status,
    ok: response.ok,
    contentType: response.headers.get('content-type'),
    elapsedMs: Date.now() - attemptStartedAt,
    stream: isEffectiveOpenAIStreamRequest(req, account)
  }, '网关收到上游响应头')

  const continueUpstreamJsonRequest = async (nextBody: Record<string, unknown>, eventName = 'gateway_continue_upstream_json_request_started') => {
    const nextBodyBuffer = Buffer.from(JSON.stringify(nextBody), 'utf8')
    const nextUpstreamBody = normalizeAnthropicMessagesBodyForAttempt(headers, upstreamUrl, nextBodyBuffer)
    getRequestLogger().debug({
      event: eventName,
      accountId: account.id,
      accountType: account.type,
      upstreamUrl: safeUpstreamUrl,
      attemptIndex,
      auditAttemptIndex,
      requestBodyBytes: typeof nextUpstreamBody === 'string' ? Buffer.byteLength(nextUpstreamBody, 'utf8') : nextUpstreamBody?.byteLength,
      socketTimeoutMs,
      requestTimeoutMs
    }, '网关继续请求上游')
    return requestUpstream(upstreamUrl, {
      method: req.method,
      headers: new Headers(headers),
      body: nextUpstreamBody,
      proxyUrl: account.proxyUrl,
      timeoutMs: socketTimeoutMs,
      requestTimeoutMs,
      signal,
      transport: upstreamTransportForAttempt(headers, upstreamUrl)
    })
  }

  const codexBridgeState = getCodexResponsesContextState(req)
  const codexBridgeCompletionHandler = codexResponsesChatBridgeCompletionHandlerForRequest(req, account)
  return transformGatewayUpstreamResponseForAccount(req, account, response, {
    signal,
    requestClientCompatibility,
    continueUpstreamJsonRequest,
    codexResponsesChatBridgePreviousResponseId: codexBridgeCompletionHandler ? codexBridgeState?.previousResponseId : undefined,
    codexResponsesChatBridgeCompletionHandler: codexBridgeCompletionHandler,
    codexResponsesChatBridgeContinueChatRequest: async (nextBody) => {
      return continueUpstreamJsonRequest(nextBody, 'gateway_codex_bridge_continue_chat_request_started')
    }
  })
}

function upstreamTransportForAttempt(
  headers: Headers,
  upstreamUrl: string
): 'fetch' | undefined {
  if (!isAnthropicMessagesRequestHeaders(headers)) return undefined
  try {
    const path = new URL(upstreamUrl).pathname.replace(/^\/v1(?=\/|$)/, '') || '/'
    return path === '/messages' || path === '/messages/count_tokens' ? 'fetch' : undefined
  } catch {
    return undefined
  }
}

function isAnthropicMessagesRequestHeaders(headers: Headers): boolean {
  return Boolean(headers.get('anthropic-version'))
    && (
      Boolean(headers.get('x-api-key'))
      || Boolean(headers.get('anthropic-api-key'))
      || Boolean(headers.get('authorization'))
    )
}

function normalizeAnthropicMessagesBodyForAttempt(
  headers: Headers,
  upstreamUrl: string,
  body: Buffer | string | undefined
): Buffer | string | undefined {
  if (!body || !isAnthropicMessagesRequestHeaders(headers) || !isAnthropicMessagesPath(upstreamUrl)) {
    return body
  }
  const text = Buffer.isBuffer(body) ? body.toString('utf8') : body
  let parsed: unknown
  try {
    parsed = JSON.parse(text) as unknown
  } catch {
    return body
  }
  if (!isJsonRecord(parsed)) return body
  const normalized: Record<string, unknown> = { ...parsed }
  if (normalized.stream === false) {
    delete normalized.stream
  }
  if (Array.isArray(normalized.messages)) {
    normalized.messages = normalized.messages.map(normalizeAnthropicMessageForAttempt)
  }
  const normalizedText = JSON.stringify(normalized)
  return Buffer.isBuffer(body) ? Buffer.from(normalizedText, 'utf8') : normalizedText
}

function normalizeAnthropicMessageForAttempt(value: unknown): unknown {
  if (!isJsonRecord(value) || !Array.isArray(value.content)) return value
  const content = value.content
  if (!content.every(isPlainAnthropicTextBlockForAttempt)) return value
  return {
    ...value,
    content: content.map((block) => block.text).join('')
  }
}

function isPlainAnthropicTextBlockForAttempt(value: unknown): value is { type: 'text'; text: string } {
  return isJsonRecord(value)
    && value.type === 'text'
    && typeof value.text === 'string'
    && Object.keys(value).every((key) => key === 'type' || key === 'text')
}

function isAnthropicMessagesPath(upstreamUrl: string): boolean {
  try {
    return (new URL(upstreamUrl).pathname.replace(/^\/v1(?=\/|$)/, '') || '/') === '/messages'
  } catch {
    return false
  }
}

function isJsonRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
