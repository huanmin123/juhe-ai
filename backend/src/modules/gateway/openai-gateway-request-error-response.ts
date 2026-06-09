import type { Response } from 'express'

import { responseHeadersToObject, type AuditCaptureContext } from './audit-capture.service.js'
import { downstreamConnectionClosedMessage } from './openai-gateway-client-abort.js'
import { gatewayErrorPayload, sendGatewayErrorResponse } from './openai-gateway-responses.js'
import { isUpstreamRequestAbortedError } from './openai-gateway-upstream.js'
import { OpenAIOAuthCodexAdapterError } from './openai-oauth-codex-adapter.js'
import { OpenAIResponsesChatBridgeError } from './openai-responses-chat-bridge.js'

interface HandleGatewayRequestKnownErrorResponseInput {
  res: Response
  auditCapture: AuditCaptureContext
  error: unknown
  signal: AbortSignal
}

export function handleGatewayRequestKnownErrorResponse(input: HandleGatewayRequestKnownErrorResponseInput): boolean {
  const { res, auditCapture, error, signal } = input

  if (isUpstreamRequestAbortedError(error) || signal.aborted) {
    auditCapture.finalize({
      outcome: 'client_aborted',
      success: false,
      statusCode: res.statusCode,
      responseHeaders: responseHeadersToObject(res),
      errorPhase: 'client',
      errorMessage: downstreamConnectionClosedMessage
    })
    if (!res.writableEnded && !res.destroyed) {
      res.end()
    }
    return true
  }

  if (error instanceof OpenAIOAuthCodexAdapterError) {
    const statusCode = error.statusCode
    const responsePayload = gatewayErrorPayload(error.message, error.type)
    sendGatewayErrorResponse(res, statusCode, responsePayload)
    auditCapture.finalize({
      outcome: 'gateway_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(responsePayload),
      responsePartType: 'gateway_error',
      errorPhase: 'request_validation',
      errorCode: error.code,
      errorMessage: error.message
    })
    return true
  }

  if (error instanceof OpenAIResponsesChatBridgeError) {
    const statusCode = error.statusCode
    const responsePayload = gatewayErrorPayload(error.message, error.type, error.code)
    sendGatewayErrorResponse(res, statusCode, responsePayload)
    auditCapture.finalize({
      outcome: 'gateway_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(responsePayload),
      responsePartType: 'gateway_error',
      errorPhase: 'request_validation',
      errorCode: error.code,
      errorMessage: error.message
    })
    return true
  }

  return false
}
