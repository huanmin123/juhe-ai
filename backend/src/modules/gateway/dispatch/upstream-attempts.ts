import type { Request } from 'express'

import { getRequestLogger, sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  isEffectiveOpenAIStreamRequest,
  requestUpstream,
  upstreamRequestTimeoutMs,
  upstreamSocketTimeoutMs,
  type GatewayUpstreamResponse
} from '../upstream/request.js'

interface PerformUpstreamRequestAttemptInput {
  req: Request
  account: UpstreamAccount
  upstreamUrl: string
  attemptIndex: number
  auditAttemptIndex: number
  headers: Headers
  body?: Buffer | string
  settings: GatewaySettings
  attemptStartedAt: number
  signal?: AbortSignal
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
    settings,
    attemptStartedAt,
    signal
  } = input
  const socketTimeoutMs = upstreamSocketTimeoutMs(req, settings, account)
  const requestTimeoutMs = upstreamRequestTimeoutMs(req, settings, account)
  const safeUpstreamUrl = sanitizeUrlCredentialsForLog(upstreamUrl) ?? 'unknown'

  getRequestLogger().info({
    event: 'gateway_upstream_request_started',
    accountId: account.id,
    accountType: account.type,
    accountStatus: account.status,
    upstreamUrl: safeUpstreamUrl,
    attemptIndex,
    auditAttemptIndex,
    method: req.method,
    stream: isEffectiveOpenAIStreamRequest(req, account),
    requestBodyBytes: typeof body === 'string' ? Buffer.byteLength(body, 'utf8') : body?.byteLength,
    socketTimeoutMs,
    requestTimeoutMs,
    proxyEnabled: Boolean(account.proxyUrl)
  }, '网关开始请求上游')

  const response = await requestUpstream(upstreamUrl, {
    method: req.method,
    headers,
    body,
    proxyUrl: account.proxyUrl,
    timeoutMs: socketTimeoutMs,
    requestTimeoutMs,
    signal
  })

  getRequestLogger().info({
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

  return response
}
