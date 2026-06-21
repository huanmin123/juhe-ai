import type { Request, Response } from 'express'

import { responseHeadersToObject, type AuditCaptureContext } from '../audit/capture.service.js'
import { downstreamConnectionClosedMessage } from '../response/client-abort.js'
import { gatewayErrorPayload, gatewayErrorPayloadForProtocol, sendGatewayErrorResponse } from '../response/responses.js'
import { isUpstreamRequestAbortedError } from '../upstream/request.js'
import { OpenAIOAuthCodexAdapterError } from '../adapters/gpt-codex/oauth-adapter.js'
import { gatewayProtocolClientErrorProtocolForRequest } from '../protocols/registry.js'
import { GatewayRequestValidationError } from './validation-error.js'

interface HandleGatewayRequestKnownErrorResponseInput {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  error: unknown
  signal: AbortSignal
}

export function handleGatewayRequestKnownErrorResponse(input: HandleGatewayRequestKnownErrorResponseInput): boolean {
  const { req, res, auditCapture, error, signal } = input

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

  if (error instanceof OpenAIOAuthCodexAdapterError || error instanceof GatewayRequestValidationError) {
    const statusCode = error.statusCode
    const responsePayload = gatewayErrorPayload(error.message, error.type, error.code)
    const protocol = gatewayProtocolClientErrorProtocolForRequest(req)
    const clientPayload = gatewayErrorPayloadForProtocol(responsePayload, protocol)
    sendGatewayErrorResponse(res, statusCode, responsePayload, { protocol })
    auditCapture.finalize({
      outcome: 'gateway_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(clientPayload),
      responsePartType: 'gateway_error',
      errorPhase: 'request_validation',
      errorCode: error.code,
      errorMessage: error.message
    })
    return true
  }

  return false
}
