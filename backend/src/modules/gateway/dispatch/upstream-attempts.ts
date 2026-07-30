import type { Request } from 'express'

import { getRequestLogger, logRequestStage, sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import type { GatewayTimeoutProfile } from '../policy/timeout-profile.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  isEffectiveOpenAIStreamRequest,
  isStartedUpstreamTransportError,
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
import { prepareAnthropicMessagesBodyForAttempt } from '../upstream/body-preparation.js'
import { applyGrokAccessDeniedFallback } from '../../providers/drivers/xai/grok-access-denied-fallback.js'

const primaryStartedGatewayTransportErrors = new WeakSet<object>()

export function isPrimaryStartedGatewayTransportError(error: unknown): boolean {
  return isWeakSetValue(error) && primaryStartedGatewayTransportErrors.has(error)
}

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
  const upstreamBody = prepareAnthropicMessagesBodyForAttempt(req, headers, upstreamUrl, body)

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
      disableTimeouts: timeoutProfile.timeoutsDisabled === true,
      signal,
      transport: upstreamTransportForAttempt(headers, upstreamUrl)
    })
    const fallbackResult = await applyGrokAccessDeniedFallback({
      upstreamUrl,
      headers,
      body: upstreamBody,
      response,
      signal,
      requestFallback: async (fallbackUrl, fallbackHeaders) => await requestUpstream(fallbackUrl, {
        method: req.method,
        headers: fallbackHeaders,
        body: upstreamBody,
        proxyUrl: account.proxyUrl,
        timeoutMs: socketTimeoutMs,
        requestTimeoutMs,
        firstByteDeadlineMs,
        firstByteDeadlineTransport: isEffectiveOpenAIStreamRequest(req, account) ? 'stream' : 'non_stream',
        onFirstByteDeadline,
        disableTimeouts: timeoutProfile.timeoutsDisabled === true,
        signal,
        transport: upstreamTransportForAttempt(fallbackHeaders, fallbackUrl)
      })
    })
    response = fallbackResult.response
    if (fallbackResult.usedFallback) {
      getRequestLogger().warn({
        event: 'grok_cli_access_denied_api_fallback_succeeded',
        accountId: account.id,
        method: req.method,
        path: new URL(upstreamUrl).pathname
      }, 'Grok CLI 代理拒绝访问，已改用官方 API')
    }
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
    if (isStartedUpstreamTransportError(error) && isWeakSetValue(error)) {
      primaryStartedGatewayTransportErrors.add(error)
    }
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
    const nextUpstreamBody = prepareAnthropicMessagesBodyForAttempt(req, headers, upstreamUrl, nextBody)
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
      disableTimeouts: timeoutProfile.timeoutsDisabled === true,
      signal,
      transport: upstreamTransportForAttempt(headers, upstreamUrl)
    })
  }

  const codexBridgeState = getCodexResponsesContextState(req)
  const codexBridgeCompletionHandler = codexResponsesChatBridgeCompletionHandlerForRequest(req, account)
  const transformedResponse = transformGatewayUpstreamResponseForAccount(req, account, response, {
    signal,
    requestClientCompatibility,
    continueUpstreamJsonRequest,
    codexResponsesChatBridgePreviousResponseId: codexBridgeCompletionHandler ? codexBridgeState?.previousResponseId : undefined,
    codexResponsesChatBridgeCompletionHandler: codexBridgeCompletionHandler,
    codexResponsesChatBridgeContinueChatRequest: async (nextBody) => {
      return continueUpstreamJsonRequest(nextBody, 'gateway_codex_bridge_continue_chat_request_started')
    }
  })
  return transformedResponse
}

function isWeakSetValue(value: unknown): value is object {
  return (typeof value === 'object' && value !== null) || typeof value === 'function'
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
