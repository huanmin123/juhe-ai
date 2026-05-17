import type { Response } from 'express'

import { responseHeadersToObject, type AuditCaptureContext } from './audit-capture.service.js'
import { gatewayErrorPayload, sendGatewayErrorResponse } from './openai-gateway-responses.js'
import { parseClientVisibleUpstreamErrorForAudit, sendRawUpstreamErrorResponse } from './openai-gateway-error-helpers.js'
import { UpstreamRejectedRequestError } from './openai-gateway-failure-dispatch.js'
import { isUpstreamRequestAbortedError } from './openai-gateway-upstream.js'
import { OpenAIOAuthCodexAdapterError } from './openai-oauth-codex-adapter.js'

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
      errorMessage: '请求已取消'
    })
    if (!res.writableEnded && !res.destroyed) {
      res.end()
    }
    return true
  }

  if (error instanceof UpstreamRejectedRequestError) {
    const auditError = parseClientVisibleUpstreamErrorForAudit(error.response, error.message)
    sendRawUpstreamErrorResponse(res, error.response)
    auditCapture.finalize({
      outcome: 'upstream_failed',
      success: false,
      statusCode: error.response.statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: error.response.bodyText,
      responsePartType: 'gateway_error',
      errorPhase: 'upstream_response',
      errorCode: auditError.errorCode,
      errorMessage: auditError.errorMessage
    })
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

  return false
}
